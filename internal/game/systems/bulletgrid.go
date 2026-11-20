package systems

import (
	"math"

	"koukoukongjian/internal/game/world"
)

// BulletGrid 是只在子弹之间做邻近查询的均匀网格。
//
// 为什么不用 world.Grid：它把玩家/怪物/子弹混存且不带池标识，查出来的
// id 分不清属于哪个池；而且它每 tick 由 room 重建一次，复用它会让
// 「重建时机」成为交汇系统与房间循环之间的隐式耦合。
//
// 为什么需要网格：交汇判定要找出「同一 tick 内距离足够近的两条子弹」，
// 双层遍历是 O(n²)——MaxBullets=512 时 26 万次距离计算，20Hz 下每秒
// 520 万次。数字上 CPU 扛得住，但这是纯浪费，而且它挡住了以后把子弹
// 上限调高的路。
//
// 格子宽度 2 米与 world.SpatialGrid 的 spatialCellSize 一致：子弹速度
// 20 m/s 每 tick 走 1 米，2 米格意味着「同一 tick 内能相撞的两条子弹」
// 必然落在同格或相邻格。
type BulletGrid struct {
	cellSize float64
	inv      float64
	buckets  map[gridCell][]world.EntityID
}

// gridCell 是格子坐标。
//
// 单独定义类型而不是用 [2]int：作为 map 键它可比较且零分配，
// 而 [2]int 在每 tick 数万次查询里会反复构造临时数组。
type gridCell struct {
	x, y int
}

// NewBulletGrid 建一个格子宽度为 cellSize 米的子弹网格。
//
// cellSize <= 0 或 NaN 时退化成 2.0：不是静默纠正配置错误，而是让「传零」
// 这个明显的编程错误不会变成除零——inv 变成 +Inf 后 cellOf 里
// Floor(Inf) 转 int 是平台相关的未定义值（AGENTS §2.6 第一条），格号会
// 变成随机数，Near 永远查不到东西。真正的配置校验在 config 层。
func NewBulletGrid(cellSize float64) *BulletGrid {
	if cellSize <= 0 || math.IsNaN(cellSize) {
		cellSize = 2.0
	}
	return &BulletGrid{
		cellSize: cellSize,
		inv:      1 / cellSize,
		buckets:  make(map[gridCell][]world.EntityID),
	}
}

// Build 重建网格。每 tick 调一次。
//
// 复用同一个 map 而不是每次新建：map 的内存会被复用，避免每 tick 一次
// 堆分配。但**必须清空**——不清会让上一 tick 已回收的子弹留在网格里，
// 交汇判定拿到一个已死的 id，world.Bullets.Get 返回 nil，症状是
// 「明明撞上了却没有反应」。
func (g *BulletGrid) Build(w *world.World) {
	clear(g.buckets)
	if w == nil {
		return
	}
	w.Bullets.Each(func(id world.EntityID, b *world.BulletState) bool {
		c := g.cellOf(b.Pos)
		g.buckets[c] = append(g.buckets[c], id)
		return true
	})
}

// cellOf 把世界坐标换成格号。
//
// 用 math.Floor 而不是 int() 截断：负坐标下 int(-0.5) == 0，会把 (-0.5,-0.5)
// 与 (0.5,0.5) 放进同一格，两者相距 1.4 米却算邻居；Floor 下前者是 -1、
// 后者是 0，格子语义与正坐标一致。
func (g *BulletGrid) cellOf(p world.Vec2) gridCell {
	return gridCell{
		x: int(math.Floor(p.X * g.inv)),
		y: int(math.Floor(p.Y * g.inv)),
	}
}

// Near 返回 3×3 邻格里的所有子弹 id。
//
// 返回的切片是**新分配的**，调用方可以随意持有或修改。不用内部复用的
// scratch：交汇判定会边遍历边销毁子弹，持有一个会被下一次 Near 覆盖的
// 切片是自找麻烦，而每 tick 几十次小分配在这个规模下不值得优化。
//
// 3×3 而不是 1 格的理由：两颗子弹可能恰好落在格子边界的两侧，相距
// 0.1 米却不同格。3×3 覆盖了 cellSize 范围内的全部可能性。
func (g *BulletGrid) Near(pos world.Vec2) []world.EntityID {
	c := g.cellOf(pos)
	var out []world.EntityID
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			out = append(out, g.buckets[gridCell{c.x + dx, c.y + dy}]...)
		}
	}
	return out
}
