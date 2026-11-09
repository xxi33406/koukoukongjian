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
	ApplyInput(&p, world.V(0.5, 0), 1.0)

	if !approx(p.MoveIntent.X, 0.5, 1e-9) {
		t.Fatalf("MoveIntent.X = %f，期望 0.5", p.MoveIntent.X)
	}
	if !approx(p.AimDir, 1.0, 1e-9) {
		t.Fatalf("AimDir = %f，期望 1.0", p.AimDir)
	}
}

func TestIntegrateMovesAtPlayerSpeed(t *testing.T) {
	var p world.PlayerState
	p.State = world.PlayerAlive
	p.Pos = world.V(0, 0)
	p.MoveIntent = world.V(1, 0)

	Integrate(&p, 1.0/60)

	want := PlayerSpeed / 60
	if !approx(p.Pos.X, want, 1e-9) {
		t.Fatalf("一个子步后 x = %f，期望 %f", p.Pos.X, want)
	}
	if p.Pos.Y != 0 {
		t.Fatalf("y 不该变，得到 %f", p.Pos.Y)
	}
	if !approx(p.Vel.X, PlayerSpeed, 1e-9) {
		t.Fatalf("速度 = %f，期望 %f", p.Vel.X, PlayerSpeed)
	}
}

func TestIntegrateThreeSubStepsEqualsOneTick(t *testing.T) {
	// 20Hz tick 里跑 3 个子步，总位移必须等于 一整 tick 的位移，
	// 否则客户端预测和服务器结果会系统性错开。
	var p world.PlayerState
	p.State = world.PlayerAlive
	p.MoveIntent = world.V(1, 0)

	for i := 0; i < 3; i++ {
		Integrate(&p, 1.0/60)
	}

	if !approx(p.Pos.X, PlayerSpeed/20, 1e-9) {
		t.Fatalf("三个子步位移 = %f，期望 %f", p.Pos.X, PlayerSpeed/20)
	}
}

func TestIntegrateZeroIntentStops(t *testing.T) {
	var p world.PlayerState
	p.State = world.PlayerAlive
	p.MoveIntent = world.V(1, 0)
	Integrate(&p, 0.1)

	// 松手。
	p.MoveIntent = world.Vec2{}
	before := p.Pos.X
	Integrate(&p, 0.1)

	if p.Pos.X != before {
		t.Fatalf("无输入时位置不该变，%f → %f", before, p.Pos.X)
	}
	if p.Vel.X != 0 {
		t.Fatalf("无输入时速度应为 0，得到 %f", p.Vel.X)
	}
}

func TestIntegrateGhostDoesNotMove(t *testing.T) {
	var p world.PlayerState
	p.State = world.PlayerGhost
	p.MoveIntent = world.V(1, 0)
	Integrate(&p, 0.1)

	if p.Pos.X != 0 {
		t.Fatalf("鬼魂不该用活人速度移动，得到 x=%f", p.Pos.X)
	}
}

func TestIntegrateDiagonalNotFaster(t *testing.T) {
	// 斜向推满的意图已归一化，位移模长应与直线相同。
	var p world.PlayerState
	p.State = world.PlayerAlive

	dir := DecodeMove(1000, 1000)
	p.MoveIntent = dir
	Integrate(&p, 1.0/60)

	got := math.Hypot(p.Pos.X, p.Pos.Y)
	want := PlayerSpeed / 60
	if !approx(got, want, 1e-9) {
		t.Fatalf("斜向位移模长 = %f，期望 %f", got, want)
	}
}
