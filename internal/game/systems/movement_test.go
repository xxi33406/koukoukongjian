package systems

import (
	"math"
	"testing"

	"koukoukongjian/internal/game/world"
)

func approx(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func TestDecodeMoveZeroIsZero(t *testing.T) {
	v := DecodeMove(0, 0)
	if v.X != 0 || v.Y != 0 {
		t.Fatalf("零输入应得零向量，得到 %+v", v)
	}
}

func TestDecodeMoveFullForward(t *testing.T) {
	v := DecodeMove(1000, 0)
	if !approx(v.X, 1, 1e-9) || !approx(v.Y, 0, 1e-9) {
		t.Fatalf("满推 +X 应得 (1,0)，得到 %+v", v)
	}
}

func TestDecodeMoveHalfPushKeepsRatio(t *testing.T) {
	// 推一半摇杆应该走一半速度，否则手感是"要么不动要么全速"。
	v := DecodeMove(500, 0)
	if !approx(v.X, 0.5, 1e-9) {
		t.Fatalf("半推 +X 应得 x=0.5，得到 %+v", v)
	}
}

func TestDecodeMoveClampsOversizedInput(t *testing.T) {
	// 客户端作弊发 ×5000 不能获得五倍速。
	v := DecodeMove(5000, 0)
	if got := math.Hypot(v.X, v.Y); got > 1+1e-9 {
		t.Fatalf("超长输入必须钳到 1，得到模长 %f", got)
	}
	if !approx(v.X, 1, 1e-9) {
		t.Fatalf("超长 +X 应保留方向 (1,0)，得到 %+v", v)
	}
}

func TestDecodeMoveDiagonalNotFaster(t *testing.T) {
	// 斜向推满摇杆时长度为 1000×√2 ≈ 1414，必须按满推处理，
	// 否则斜着走比直着走快 41%——这是最常见的移动 bug。
	v := DecodeMove(1000, 1000)
	if got := math.Hypot(v.X, v.Y); !approx(got, 1, 1e-9) {
		t.Fatalf("斜向满推的模长应为 1，得到 %f", got)
	}
	// 方向应为 45°（两个分量各 1/√2）。
	want := 1 / math.Sqrt2
	if !approx(v.X, want, 1e-9) || !approx(v.Y, want, 1e-9) {
		t.Fatalf("斜向方向应归一化，得到 %+v", v)
	}
}

func TestDecodeMoveNegative(t *testing.T) {
	v := DecodeMove(-1000, -1000)
	if v.X >= 0 || v.Y >= 0 {
		t.Fatalf("反向输入应得负分量，得到 %+v", v)
	}
	if got := math.Hypot(v.X, v.Y); !approx(got, 1, 1e-9) {
		t.Fatalf("模长应为 1，得到 %f", got)
	}
}

func TestDecodeAimZeroKeepsPrevious(t *testing.T) {
	// 客户端没瞄准时不能把朝向重置成 +X，否则角色抽搐。
	const keep = 1.234
	if got := DecodeAim(0, 0, keep); got != keep {
		t.Fatalf("零瞄准应返回 keep=%f，得到 %f", keep, got)
	}
}

func TestDecodeAimAxes(t *testing.T) {
	cases := []struct {
		x, y int32
		want float64
	}{
		{1000, 0, 0},
		{0, 1000, math.Pi / 2},
		{-1000, 0, math.Pi},
		{0, -1000, -math.Pi / 2},
	}
	for _, c := range cases {
		got := DecodeAim(c.x, c.y, 999)
		if !approx(got, c.want, 1e-9) {
			t.Fatalf("DecodeAim(%d,%d) = %f，期望 %f", c.x, c.y, got, c.want)
		}
	}
}

func TestApplyInputSetsIntentAndAim(t *testing.T) {
	var p world.PlayerState
	p.State = world.PlayerAlive
	ApplyInput(&p, world.V(0.5, 0), 1.0, true)

	if !approx(p.MoveIntent.X, 0.5, 1e-9) {
		t.Fatalf("MoveIntent.X = %f，期望 0.5", p.MoveIntent.X)
	}
	if !approx(p.AimDir, 1.0, 1e-9) {
		t.Fatalf("AimDir = %f，期望 1.0", p.AimDir)
	}
}

// TestApplyInputKeepsAimWhenAbsent 验证客户端没给方向时保留原朝向。
//
// 直接把 AimDir 写成 0 会让"玩家没在瞄"和"玩家瞄正 +X"变成同一个状态，
// 自动瞄准再也分不出该不该接管。
func TestApplyInputKeepsAimWhenAbsent(t *testing.T) {
	var p world.PlayerState
	p.State = world.PlayerAlive
	p.AimDir = 2.0

	ApplyInput(&p, world.V(1, 0), 0, false)

	if !approx(p.AimDir, 2.0, 1e-9) {
		t.Fatalf("没给方向时 AimDir = %f，期望保持 2.0", p.AimDir)
	}
	if !approx(p.MoveIntent.X, 1.0, 1e-9) {
		t.Fatalf("MoveIntent.X = %f，期望 1.0", p.MoveIntent.X)
	}
}

func TestIntegrateMovesAtPlayerSpeed(t *testing.T) {
	w := newSystemsWorld(t, 1)
	var p world.PlayerState
	p.State = world.PlayerAlive
	// 摆在图内空地，不贴 (0,0)——图外按墙算，出生在角落第一步就撞墙。
	p.Pos = world.V(10, 10)
	p.MoveIntent = world.V(1, 0)
	beforeX, beforeY := p.Pos.X, p.Pos.Y
	Integrate(&p, 1.0/60, w.Terrain)

	want := PlayerSpeed / 60
	if !approx(p.Pos.X-beforeX, want, 1e-9) {
		t.Fatalf("一个子步位移 x = %f，期望 %f", p.Pos.X-beforeX, want)
	}
	if p.Pos.Y != beforeY {
		t.Fatalf("y 不该变，得到位移 %f", p.Pos.Y-beforeY)
	}
	if !approx(p.Vel.X, PlayerSpeed, 1e-9) {
		t.Fatalf("速度 = %f，期望 %f", p.Vel.X, PlayerSpeed)
	}
}

func TestIntegrateThreeSubStepsEqualsOneTick(t *testing.T) {
	// 20Hz tick 里跑 3 个子步，总位移必须等于 一整 tick 的位移，
	// 否则客户端预测和服务器结果会系统性错开。
	w := newSystemsWorld(t, 1)
	var p world.PlayerState
	p.State = world.PlayerAlive
	p.Pos = world.V(10, 10) // 图内空地，避免贴边撞"图外即墙"。
	p.MoveIntent = world.V(1, 0)

	for i := 0; i < 3; i++ {
		Integrate(&p, 1.0/60, w.Terrain)
	}

	if !approx(p.Pos.X-10, PlayerSpeed/20, 1e-9) {
		t.Fatalf("三个子步位移 = %f，期望 %f", p.Pos.X-10, PlayerSpeed/20)
	}
}

func TestIntegrateZeroIntentStops(t *testing.T) {
	w := newSystemsWorld(t, 1)
	var p world.PlayerState
	p.State = world.PlayerAlive
	p.Pos = world.V(10, 10) // 图内空地。
	p.MoveIntent = world.V(1, 0)
	Integrate(&p, 0.1, w.Terrain)

	// 松手。
	p.MoveIntent = world.Vec2{}
	before := p.Pos.X
	Integrate(&p, 0.1, w.Terrain)

	if p.Pos.X != before {
		t.Fatalf("无输入时位置不该变，%f → %f", before, p.Pos.X)
	}
	if p.Vel.X != 0 {
		t.Fatalf("无输入时速度应为 0，得到 %f", p.Vel.X)
	}
}

func TestIntegrateGhostDoesNotMove(t *testing.T) {
	w := newSystemsWorld(t, 1)
	var p world.PlayerState
	p.State = world.PlayerGhost
	p.MoveIntent = world.V(1, 0)
	Integrate(&p, 0.1, w.Terrain)

	if p.Pos.X != 0 {
		t.Fatalf("鬼魂不该用活人速度移动，得到 x=%f", p.Pos.X)
	}
}

func TestIntegrateDiagonalNotFaster(t *testing.T) {
	// 斜向推满的意图已归一化，位移模长应与直线相同。
	w := newSystemsWorld(t, 1)
	var p world.PlayerState
	p.State = world.PlayerAlive
	p.Pos = world.V(10, 10) // 图内空地，斜向两轴都不贴边。

	dir := DecodeMove(1000, 1000)
	p.MoveIntent = dir
	Integrate(&p, 1.0/60, w.Terrain)

	got := math.Hypot(p.Pos.X-10, p.Pos.Y-10)
	want := PlayerSpeed / 60
	if !approx(got, want, 1e-9) {
		t.Fatalf("斜向位移模长 = %f，期望 %f", got, want)
	}
}

func TestIntegrateSlidesAlongWall(t *testing.T) {
	w := newSystemsWorld(t, 1)
	tr := w.Terrain
	// 世界中部立一堵竖墙（x=8 列），玩家在墙左侧贴墙向上走。
	for cy := 4; cy <= 12; cy++ {
		tr.Set(8, cy, world.TileWall)
	}
	id, p := w.Players.Spawn()
	p.Pos = world.V(7.5, 8.0) // 紧贴墙（半径 0.3 → 7.8 距墙列 [8,9) 还差 0.2）
	p.State = world.PlayerAlive

	// 直直往右上撞墙：x 分量必须被挡下，y 分量继续（滑墙）。
	// 注意 dt=0.5 时一步位移 2.1 m，远超圆直径 0.6 m——终点判定会
	// 直接跳过墙格，这一步逼的是「扫掠」语义。
	dir := world.V(1, 1).Normalized()
	p.MoveIntent = dir
	Integrate(p, 0.5, tr)

	if p.Pos.X > 7.7 {
		t.Errorf("撞墙后 Pos.X = %.3f，不该越过墙边（≤7.7 附近）", p.Pos.X)
	}
	if p.Pos.Y <= 8.0 {
		t.Errorf("滑墙后 Pos.Y = %.3f，y 分量应继续前进", p.Pos.Y)
	}
	// 撞墙不清 Vel：Vel 是「玩家想往哪走」的意图速度（快照 QuantizeVel
	// 下发给客户端插值），被挡是位置语义。若清零，客户端会看到速度分量
	// 间歇归零又恢复（闪烁）。这是 I-1 审查项的回归钉。
	if !approx(p.Vel.X, PlayerSpeed*dir.X, 1e-9) || !approx(p.Vel.Y, PlayerSpeed*dir.Y, 1e-9) {
		t.Errorf("撞墙后 Vel = (%.3f, %.3f)，应保持意图速度 (%.3f, %.3f) 不被清零",
			p.Vel.X, p.Vel.Y, PlayerSpeed*dir.X, PlayerSpeed*dir.Y)
	}
	_ = id
}

func TestIntegrateFreeAreaUnchanged(t *testing.T) {
	w := newSystemsWorld(t, 1) // 全空地
	_, p := w.Players.Spawn()
	p.Pos = world.V(10, 10)
	p.State = world.PlayerAlive
	p.MoveIntent = world.V(1, 0)
	Integrate(p, 0.5, w.Terrain)
	// 位移量与 M2 一致即可。实现按 slideStep 分段累加（滑墙扫掠语义），
	// 浮点逐段舍入后不再 bit 级等于一次加法的结果，用 1e-9 容差。
	want := 10.0 + PlayerSpeed*0.5
	if !approx(p.Pos.X, want, 1e-9) {
		t.Errorf("空地上 Pos.X = %.3f, 期望 %.3f（行为必须与 M2 完全一致）", p.Pos.X, want)
	}
}

func TestIntegrateDiagonalFreeAreaMatchesAnalytic(t *testing.T) {
	// 斜向空地移动的扫掠回归：分段等长且方向恒定时，位移应逐分量等于
	// 解析值。没有它，将来改动 CircleFree 或切段逻辑（比如把切段改短）
	// 造成的斜向速度损失不会被任何测试抓到（x 向直线用例测不出来）。
	w := newSystemsWorld(t, 1) // 全空地
	_, p := w.Players.Spawn()
	p.Pos = world.V(10, 10)
	p.State = world.PlayerAlive

	dir := world.V(1, 1).Normalized()
	p.MoveIntent = dir
	Integrate(p, 0.5, w.Terrain)

	wantX := 10 + PlayerSpeed*0.5*dir.X
	wantY := 10 + PlayerSpeed*0.5*dir.Y
	if !approx(p.Pos.X, wantX, 1e-9) || !approx(p.Pos.Y, wantY, 1e-9) {
		t.Errorf("斜向空地位移 = (%.6f, %.6f)，解析值 (%.6f, %.6f)",
			p.Pos.X, p.Pos.Y, wantX, wantY)
	}
}
