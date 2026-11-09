// Package world 定义权威模拟的世界状态与实体存储。
//
// 这一层刻意不依赖 net / room / proto —— 它只描述"世界里有什么"以及
// "怎么访问"，不关心这些东西从哪来、要发给谁。分层纪律见
// docs/server-design-v0.1.md §3.2。
package world

import "math"

// Vec2 是二维向量，同时用于位置、速度、方向。
//
// 用 float64 而不是定点数：实测定点只快约 12%（docs/decisions-game-backend.md
// §5），而浮点更直观、标准库支持完整。只有当将来要做帧同步确定性回放时才需要
// 换定点——那时整个模拟都得跟着改，不是局部替换。
type Vec2 struct {
	X, Y float64
}

// V 是 Vec2 的简写构造器，让战术性代码短一些。
func V(x, y float64) Vec2 { return Vec2{X: x, Y: y} }

func (a Vec2) Add(b Vec2) Vec2      { return Vec2{a.X + b.X, a.Y + b.Y} }
func (a Vec2) Sub(b Vec2) Vec2      { return Vec2{a.X - b.X, a.Y - b.Y} }
func (a Vec2) Scale(s float64) Vec2 { return Vec2{a.X * s, a.Y * s} }
func (a Vec2) Dot(b Vec2) float64   { return a.X*b.X + a.Y*b.Y }
func (a Vec2) Cross(b Vec2) float64 { return a.X*b.Y - a.Y*b.X }
func (a Vec2) LenSq() float64       { return a.Dot(a) }
func (a Vec2) Len() float64         { return math.Sqrt(a.Dot(a)) }

// DistSq 返回两点距离的平方。
//
// 比较距离时优先用它：省一次开方，且在只需要比大小的地方没有意义损失。
func (a Vec2) DistSq(b Vec2) float64 {
	d := a.Sub(b)
	return d.Dot(d)
}

// Normalized 返回单位向量。零向量返回零向量而不是 NaN。
func (a Vec2) Normalized() Vec2 {
	l := a.Len()
	if l == 0 {
		return Vec2{}
	}
	return a.Scale(1 / l)
}

// ClampLen 把向量长度限制在 max 以内，方向不变。
func (a Vec2) ClampLen(max float64) Vec2 {
	l2 := a.LenSq()
	if l2 <= max*max || l2 == 0 {
		return a
	}
	return a.Scale(max / math.Sqrt(l2))
}

// IsFinite 报告坐标是否都是有限值。
//
// 用于在系统边界处做体检：一次 NaN 污染会顺着积分传播到整个池，而 NaN 与
// 任何数比较都为 false，所有后续判定都会静默失效。早发现早止损。
func (a Vec2) IsFinite() bool {
	return !math.IsNaN(a.X) && !math.IsNaN(a.Y) && !math.IsInf(a.X, 0) && !math.IsInf(a.Y, 0)
}

// SegmentIntersectsCircle 判断线段 [a,b] 是否与圆 (c, r) 相交。
//
// 这是子弹命中判定的核心。必须用扫掠而不是"终点是否在圆内"：子弹 20 m/s 在
// 一个 1/60 s 子步里走 0.333 m，而玩家半径 0.3 m —— 只查终点会漏掉擦边命中，
// 更快的子弹（比如 60 m/s，1 m/子步）则会稳定穿透。
func SegmentIntersectsCircle(a, b, c Vec2, r float64) bool {
	ab := b.Sub(a)
	ac := c.Sub(a)

	den := ab.Dot(ab)
	var t float64
	if den == 0 {
		// 线段退化成点（实体本子步没动）。
		// 必须特判：不特判则除零得 NaN，而 NaN 与任何数比较都返回 false，
		// 结果是把所有静止实体的命中全部静默漏掉。
		t = 0
	} else {
		t = ac.Dot(ab) / den
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
	}

	closest := a.Add(ab.Scale(t))
	return closest.DistSq(c) <= r*r
}
