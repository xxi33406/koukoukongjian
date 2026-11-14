// Package mapgen 依据 config 与世界 RNG 生成灰盒第一关：
// 全墙底板 + HubCount 个圆形据点空地 + 2 格宽走廊（Prim MST + 冗余边）。
// 据点放不满时降级保留能放下的部分（撒点重试 200 次上限），0 个才报错。
//
// 回放一致性三原则：
//  1. 所有随机数走 w.RandIntn/RandFloat64（rngCalls 计数完整）；
//  2. 排序比较用 (distSq, fromIdx, toIdx) 稳定 key，浮点并列不出歧义；
//  3. 撒点重试与整体重开是确定性循环——同种子同调用序列，结果唯一。
//
// mapgen 只 import world（Vec2/Terrain/Hub）。Generate 由编排层
// internal/room 在 NewWorld 之后调用——world 反向调 mapgen 会构成
// import cycle（编译器拒绝），room→mapgen→world 无环。
package mapgen

import (
	"fmt"
	"log/slog"
	"math"
	"sort"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/game/world"
)

// Result 是一次成功生成的产出。
type Result struct {
	Hubs     []world.Hub
	SpawnHub int // 出生据点下标（固定 0）
}

const (
	maxLayoutRetries   = 10 // 整体重试上限
	maxPlacementRetry  = 200
	spawnZoneHalfCells = 2 // 据点中心 5×5 标 TileSpawnZone
	extraEdges         = 2 // MST 之外补的环边数
)

// Generate 在 w.Terrain 上生成地图，并在 w.Hubs / w.HasMap 上落运行时数据。
//
// Generate 独占 w.Terrain：无论成功、降级还是失败，返回时 Terrain 要么是
// 完整成品图，要么恢复成全空地——绝不把半成品墙留在世界里。失败/哨兵
// 路径 HasMap=false，调用方据此短路波次/AI/迷雾。
func Generate(w *world.World, cfg *config.Config) (*Result, error) {
	tr := w.Terrain
	W, H := int(tr.Width()), int(tr.Height())

	if cfg.Map.HubCount == 0 {
		// 哨兵：测试用空白世界——不填墙、不生成据点。恢复全空地而不是
		// "不动 Terrain"：Generate 对 Terrain 的契约是"要么成品要么空白"，
		// 留着调用方预先弄脏的格会让降级世界的地形状态不可预测。
		tr.FillAll(world.TileFloor)
		w.HasMap = false
		w.Hubs = nil
		w.SpawnHub = 0
		return &Result{}, nil
	}

	cell := tr.CellSize()
	hubR := cfg.Map.HubRadiusCells
	corrW := cfg.Map.CorridorWidth

	// 图小到连一个据点（半径 hubR + 1 格边距）都摆不下时直接失败：
	// 再往下走撒点循环的 RandIntn(W-2*margin) 会因 n<=0 panic。
	if margin := hubR + 1; W < 2*margin+1 || H < 2*margin+1 {
		tr.FillAll(world.TileFloor)
		w.Hubs = nil
		w.SpawnHub = 0
		w.HasMap = false
		return nil, fmt.Errorf("mapgen: 图 %dx%d 放不下任何半径 %d 的据点（需 ≥%d）",
			W, H, hubR, 2*margin+1)
	}

	for retry := 0; retry < maxLayoutRetries; retry++ {
		tr.FillAll(world.TileWall)

		centers, placed := placeHubs(w, cfg, W, H)
		if len(centers) == 0 {
			continue
		}

		// 据点开圆空地 + 中心 SpawnZone。
		for _, c := range centers {
			carveCircle(tr, c.cx, c.cy, hubR, world.TileFloor)
			carveRect(tr, c.cx-spawnZoneHalfCells, c.cy-spawnZoneHalfCells,
				c.cx+spawnZoneHalfCells, c.cy+spawnZoneHalfCells, world.TileSpawnZone)
		}

		// Prim MST（稳定 key 排序）连走廊。
		edges := mstEdges(w, centers)
		edges = append(edges, extraRingEdges(w, centers, edges)...)
		for _, e := range edges {
			carveCorridor(tr, centers[e.from], centers[e.to], corrW)
		}

		// 连通验证：任一据点不可达就整轮重试（确定性——RNG 序列继续）。
		if !allReachable(tr, W, H, centers) {
			continue
		}

		hubs := make([]world.Hub, len(centers))
		for i, c := range centers {
			hubs[i] = world.Hub{
				Center:  world.V((float64(c.cx)+0.5)*cell, (float64(c.cy)+0.5)*cell),
				RadiusM: float64(hubR) * cell,
				State:   world.HubIdle,
			}
		}
		res := &Result{Hubs: hubs, SpawnHub: 0}
		w.Hubs = hubs
		w.SpawnHub = 0
		w.HasMap = true
		if placed < cfg.Map.HubCount {
			// 降级：图上放不满 HubCount 个据点。能放几个放几个（placeHubs
			// 已在单点 200 次重试上限处停手），留 1 个据点就保持 HasMap=true
			// 让波次系统照常跑；0 个才走下面的失败路径。
			slog.Warn("mapgen: 据点数降级（能放几个放几个）",
				"requested", cfg.Map.HubCount, "placed", placed,
				"seed", w.Seed, "map", fmt.Sprintf("%dx%d", W, H))
		}
		return res, nil
	}

	// 失败：恢复全空地。error 路径留下满图墙的话，调用方 HasMap=false
	// 短路波次/AI 后，玩家与怪物会站在看不见的墙里。
	tr.FillAll(world.TileFloor)
	w.Hubs = nil
	w.SpawnHub = 0
	w.HasMap = false
	return nil, fmt.Errorf("mapgen: %d 轮重试内无法生成任何间距 ≥%d 的据点布局（图 %dx%d）",
		maxLayoutRetries, cfg.Map.HubMinDistCells, W, H)
}

type cellPos struct{ cx, cy int }

// placeHubs rejection sampling 撒点：间距 ≥ HubMinDistCells，距边 ≥ hubR+1。
// 某个据点连续 maxPlacementRetry 次找不到合法位置就停手，返回已放下的
// 前缀（尽力放置，据点数可以少于 HubCount）；一个都放不下才算整体失败。
func placeHubs(w *world.World, cfg *config.Config, W, H int) ([]cellPos, int) {
	hubR := cfg.Map.HubRadiusCells
	margin := hubR + 1
	var out []cellPos
	for i := 0; i < cfg.Map.HubCount; i++ {
		placed := false
		for try := 0; try < maxPlacementRetry; try++ {
			cx := margin + w.RandIntn(W-2*margin)
			cy := margin + w.RandIntn(H-2*margin)
			ok := true
			for _, p := range out {
				dx, dy := cx-p.cx, cy-p.cy
				if dx*dx+dy*dy < cfg.Map.HubMinDistCells*cfg.Map.HubMinDistCells {
					ok = false
					break
				}
			}
			if ok {
				out = append(out, cellPos{cx, cy})
				placed = true
				break
			}
		}
		if !placed {
			break
		}
	}
	return out, len(out)
}

// carveCircle 把半径 r 的圆内格设成 tile（ceil 圆判定）。
func carveCircle(tr *world.Terrain, cx, cy, r int, tile world.Tile) {
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			if dx*dx+dy*dy <= r*r {
				tr.Set(cx+dx, cy+dy, tile)
			}
		}
	}
}

// carveRect 闭区间矩形填格（SpawnZone 用）。x0/x1、y0/y1 先归一化——
// 与 terrain.FillRect 一致；MST 边的方向与坐标无关（from 不保证 x/y 更小），
// 不归一化时 x0 > x1 的循环会整体空转，走廊整段消失。
func carveRect(tr *world.Terrain, x0, y0, x1, y1 int, tile world.Tile) {
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	for cy := y0; cy <= y1; cy++ {
		for cx := x0; cx <= x1; cx++ {
			tr.Set(cx, cy, tile)
		}
	}
}

type edge struct{ from, to int }

// mstEdges 用 Prim 最小生成树连接据点；比较 key (distSq, from, to) 稳定。
func mstEdges(w *world.World, centers []cellPos) []edge {
	n := len(centers)
	in := make([]bool, n)
	in[0] = true
	var out []edge
	for len(out) < n-1 {
		var best *edge
		bestD := 0
		for i := 0; i < n; i++ {
			if !in[i] {
				continue
			}
			for j := 0; j < n; j++ {
				if in[j] {
					continue
				}
				dx, dy := centers[i].cx-centers[j].cx, centers[i].cy-centers[j].cy
				d := dx*dx + dy*dy
				if best == nil || d < bestD || (d == bestD && (i < best.from || (i == best.from && j < best.to))) {
					e := edge{i, j}
					best = &e
					bestD = d
				}
			}
		}
		if best == nil {
			break
		}
		in[best.to] = true
		out = append(out, *best)
	}
	return out
}

// extraRingEdges 补 2 条不在树上的最短边，制造环路避免单树死区。
func extraRingEdges(w *world.World, centers []cellPos, tree []edge) []edge {
	isTree := map[edge]bool{}
	for _, e := range tree {
		isTree[e] = true
		isTree[edge{e.to, e.from}] = true
	}
	type cand struct {
		e edge
		d int
	}
	var cands []cand
	for i := 0; i < len(centers); i++ {
		for j := i + 1; j < len(centers); j++ {
			if isTree[edge{i, j}] {
				continue
			}
			dx, dy := centers[i].cx-centers[j].cx, centers[i].cy-centers[j].cy
			cands = append(cands, cand{edge{i, j}, dx*dx + dy*dy})
		}
	}
	sort.Slice(cands, func(a, b int) bool {
		if cands[a].d != cands[b].d {
			return cands[a].d < cands[b].d
		}
		return cands[a].e.from < cands[b].e.from || (cands[a].e.from == cands[b].e.from && cands[a].e.to < cands[b].e.to)
	})
	out := make([]edge, 0, extraEdges)
	for _, c := range cands {
		if len(out) >= extraEdges {
			break
		}
		out = append(out, c.e)
	}
	return out
}

// carveCorridor L 形走廊（先 x 后 y），宽 corrW 格。水平段与垂直段都按
// corrW 排开（沿 y 与沿 x 各 corrW 格），保证 2 格宽瓶颈语义。
func carveCorridor(tr *world.Terrain, a, b cellPos, corrW int) {
	carveRect(tr, a.cx, a.cy, b.cx, a.cy+corrW-1, world.TileFloor)                                 // 水平段
	carveRect(tr, b.cx, min2(a.cy, b.cy), b.cx+corrW-1, max2(a.cy, b.cy)+corrW-1, world.TileFloor) // 垂直段
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// allReachable 从 centers[0] 做 4 邻 BFS，验证所有据点中心可达。
func allReachable(tr *world.Terrain, W, H int, centers []cellPos) bool {
	seen := make([]bool, W*H)
	start := centers[0].cy*W + centers[0].cx
	seen[start] = true
	queue := []int{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		cx, cy := cur%W, cur/W
		for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			nx, ny := cx+d[0], cy+d[1]
			if nx < 0 || ny < 0 || nx >= W || ny >= H {
				continue
			}
			idx := ny*W + nx
			if seen[idx] || !tr.At(nx, ny).Walkable() {
				continue
			}
			seen[idx] = true
			queue = append(queue, idx)
		}
	}
	for _, c := range centers {
		if !seen[c.cy*W+c.cx] {
			return false
		}
	}
	return true
}

// cellOf 供测试（mapgen_test.go 的 connected）把世界坐标转格坐标。
// cellSize 换算（floor 语义），不依赖 cellSize=1。
func cellOf(p world.Vec2, W, H int, cell float64) cellPos {
	cx := int(math.Floor(p.X / cell))
	cy := int(math.Floor(p.Y / cell))
	if cx < 0 {
		cx = 0
	}
	if cx >= W {
		cx = W - 1
	}
	if cy < 0 {
		cy = 0
	}
	if cy >= H {
		cy = H - 1
	}
	return cellPos{cx, cy}
}
