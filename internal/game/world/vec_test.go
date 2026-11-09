package world

import (
	"math"
	"testing"
)

func TestEntityIDRoundTrip(t *testing.T) {
	cases := []struct {
		index uint16
		gen   uint8
	}{
		{1, 1},
		{1, 15},
		{4095, 0},
		{4095, 15},
		{7, 3},
	}
	for _, c := range cases {
		id := MakeID(c.index, c.gen)
		if got := id.Index(); got != c.index {
			t.Errorf("MakeID(%d,%d).Index() = %d，期望 %d", c.index, c.gen, got, c.index)
		}
		if got := id.Gen(); got != c.gen {
			t.Errorf("MakeID(%d,%d).Gen() = %d，期望 %d", c.index, c.gen, got, c.gen)
		}
	}
}

// 世代 0 + 索引 0 必须等于 InvalidID，否则"零值即无效"的惯例就破了。
func TestInvalidIDIsZero(t *testing.T) {
	if MakeID(0, 0) != InvalidID {
		t.Fatalf("MakeID(0,0) = %v，必须等于 InvalidID", MakeID(0, 0))
	}
	if InvalidID.IsValid() {
		t.Fatalf("InvalidID 不应该 valid")
	}
}

// 世代必须回绕而不是溢出到索引位。
func TestGenWraps(t *testing.T) {
	if got := MakeID(5, 15).Gen(); got != 15 {
		t.Fatalf("gen 15 应保留，实际 %d", got)
	}
	// gen 传 16 时应当被掩到 0，不能污染索引。
	id := MakeID(5, 16)
	if id.Index() != 5 {
		t.Fatalf("gen 溢出污染了索引：idx=%d", id.Index())
	}
	if id.Gen() != 0 {
		t.Fatalf("gen 应当掩到 0，实际 %d", id.Gen())
	}
}

func TestVecBasics(t *testing.T) {
	a := V(3, 4)
	if got := a.Len(); math.Abs(got-5) > 1e-12 {
		t.Fatalf("Len = %v，期望 5", got)
	}
	if got := a.DistSq(V(0, 0)); math.Abs(got-25) > 1e-12 {
		t.Fatalf("DistSq = %v，期望 25", got)
	}

	// 零向量归一化不能返回 NaN。
	z := Vec2{}.Normalized()
	if !z.IsFinite() {
		t.Fatalf("零向量归一化得到非有限值 %+v", z)
	}
	if z != (Vec2{}) {
		t.Fatalf("零向量归一化应返回零向量，实际 %+v", z)
	}

	// ClampLen 只缩不放。
	if got := V(1, 0).ClampLen(5); got != V(1, 0) {
		t.Fatalf("ClampLen 不应放大，得到 %+v", got)
	}
	got := V(10, 0).ClampLen(2)
	if math.Abs(got.Len()-2) > 1e-12 {
		t.Fatalf("ClampLen 长度 = %v，期望 2", got.Len())
	}
}

// NaN 一旦进入坐标就会顺着积分污染整个池，而 NaN 与任何数比较都是 false，
// 所有后续判定会静默失效。IsFinite 是唯一的哨兵。
func TestIsFiniteCatchesNaN(t *testing.T) {
	nan := math.NaN()
	if (Vec2{nan, 0}).IsFinite() {
		t.Fatalf("NaN 坐标应被判为非有限")
	}
	if (Vec2{math.Inf(1), 0}).IsFinite() {
		t.Fatalf("Inf 坐标应被判为非有限")
	}
	if !(Vec2{1, 2}).IsFinite() {
		t.Fatalf("普通坐标应判为有限")
	}
}

// 扫掠判定的核心价值：高速子弹穿过目标、终点在目标之外时仍能命中。
func TestSegmentIntersectsCircleSweepsThroughTarget(t *testing.T) {
	target := V(0, 0)
	const r = 0.3

	// 子弹本子步从 (-1,0) 到 (1,0)，直接穿过圆心。
	if !SegmentIntersectsCircle(V(-1, 0), V(1, 0), target, r) {
		t.Fatalf("线段穿过圆心应判为命中")
	}
	// 同样的移动若只查终点，就漏了——这正是必须扫掠的原因。
	if V(1, 0).DistSq(target) <= r*r {
		t.Fatalf("测试前提错了：终点本不应在圆内")
	}

	// 擦边：线段在 y=0.29 处水平经过，仍在半径 0.3 内。
	if !SegmentIntersectsCircle(V(-1, 0.29), V(1, 0.29), target, r) {
		t.Fatalf("擦边 0.29 应判为命中")
	}
	// 差一点：y=0.31 在半径外。
	if SegmentIntersectsCircle(V(-1, 0.31), V(1, 0.31), target, r) {
		t.Fatalf("y=0.31 超出半径 0.3，不应命中")
	}
}

// 退化成点的线段（实体没动）必须特判，否则除零得 NaN → 恒不命中。
func TestSegmentIntersectsCircleDegenerate(t *testing.T) {
	target := V(0, 0)
	const r = 0.3

	// 点落在圆内：必须命中。
	if !SegmentIntersectsCircle(V(0.1, 0), V(0.1, 0), target, r) {
		t.Fatalf("静止点位于圆内必须命中——这里是 NaN 陷阱的高发处")
	}
	// 点落在圆外：必须不命中。
	if SegmentIntersectsCircle(V(1, 0), V(1, 0), target, r) {
		t.Fatalf("静止点位于圆外不应命中")
	}
}

// 线段在圆外且不指向圆时不能误判（防止投影参数钳位出错）。
func TestSegmentIntersectsCircleMisses(t *testing.T) {
	target := V(0, 0)
	const r = 0.3

	if SegmentIntersectsCircle(V(5, 0), V(6, 0), target, r) {
		t.Fatalf("远处线段不应命中")
	}
	if SegmentIntersectsCircle(V(0, 5), V(0, 6), target, r) {
		t.Fatalf("远处竖直线段不应命中")
	}
}
