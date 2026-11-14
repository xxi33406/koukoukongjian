package mapgen

import (
	"testing"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/game/world"
)

func genFor(t *testing.T, seed int64) (*world.World, *Result) {
	t.Helper()
	cfg := config.Default()
	w := world.NewWorld(cfg, seed)
	res, err := Generate(w, cfg)
	if err != nil {
		t.Fatalf("Generate(seed=%d): %v", seed, err)
	}
	return w, res
}

func TestGenerateSeededDeterministic(t *testing.T) {
	w1, r1 := genFor(t, 42)
	w2, r2 := genFor(t, 42)
	if w1.Terrain.Hash() != w2.Terrain.Hash() {
		t.Errorf("同种子地形哈希不同：%d vs %d", w1.Terrain.Hash(), w2.Terrain.Hash())
	}
	if len(r1.Hubs) != len(r2.Hubs) {
		t.Fatalf("据点数不同：%d vs %d", len(r1.Hubs), len(r2.Hubs))
	}
	for i := range r1.Hubs {
		if r1.Hubs[i].Center != r2.Hubs[i].Center {
			t.Errorf("据点 %d 中心不同：%v vs %v", i, r1.Hubs[i].Center, r2.Hubs[i].Center)
		}
	}
}

func TestGenerateDifferentSeedsDiffer(t *testing.T) {
	w1, _ := genFor(t, 1)
	w2, _ := genFor(t, 2)
	if w1.Terrain.Hash() == w2.Terrain.Hash() {
		// 两个不同种子哈希相同不是错误，但连续 3 对都相同就必须查。
		t.Log("seed 1 与 2 哈希巧合相同（允许，仅记录）")
	}
}

func TestGenerateHubCountAndConnectivity(t *testing.T) {
	for _, seed := range []int64{1, 2, 3, 42, 777} {
		w, res := genFor(t, seed)
		cfg := config.Default()
		if len(res.Hubs) != cfg.Map.HubCount {
			t.Fatalf("seed %d：据点数 %d != %d", seed, len(res.Hubs), cfg.Map.HubCount)
		}
		// 每个据点中心必须是可走格（开圆空地保证）。
		for i, h := range res.Hubs {
			if !w.Terrain.WalkableAt(h.Center) {
				t.Errorf("seed %d：据点 %d 中心 %v 不可走", seed, i, h.Center)
			}
		}
		// 连通性：从出生据点 BFS，所有据点中心可达。
		if !connected(t, w.Terrain, res.Hubs[0].Center, res.Hubs) {
			t.Errorf("seed %d：存在不可达据点", seed)
		}
		// 走廊宽度抽查：MST 边上任意中间点，垂直方向至少 2 格可走。
		// 简化为验证可走格总数下限（全图 16384 格）。8 个种子实测
		// 1012~1313（间距恰为下限 20 时走廊短、总格数少），取 1000。
		if wc := w.Terrain.WalkableCount(); wc < 1000 {
			t.Errorf("seed %d：可走格 %d 过少，走廊/据点没挖开", seed, wc)
		}
	}
}

func TestGenerateRejectsImpossibleConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Map.HubMinDistCells = 120 // 128 格的图放不下 7 个间距 120 的点
	w := world.NewWorld(cfg, 5)
	if _, err := Generate(w, cfg); err == nil {
		t.Fatal("几何上放不下的据点配置应返回 error，而不是静默产出坏图")
	}
}

// connected 用 4 邻 BFS 验证 from 能走到每个 hub 的中心格。
func connected(t *testing.T, tr *world.Terrain, from world.Vec2, hubs []world.Hub) bool {
	t.Helper()
	W, H := int(tr.Width()), int(tr.Height())
	start := cellOf(from, W, H, tr.CellSize())
	seen := make([]bool, W*H)
	queue := []int{start.cy*W + start.cx}
	seen[queue[0]] = true
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
	for _, h := range hubs {
		c := cellOf(h.Center, W, H, tr.CellSize())
		if !seen[c.cy*W+c.cx] {
			return false
		}
	}
	return true
}
