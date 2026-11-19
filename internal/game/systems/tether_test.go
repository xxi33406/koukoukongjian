package systems

import (
	"math"
	"testing"

	"koukoukongjian/internal/game/world"
)

// 鬼魂超出拴绳半径时被拉回球面最近点（硬钳位）。
//
// 为什么是硬钳位而不是"位移不生效"：软拒绝会让鬼魂贴着边界抖，绕障碍时
// 还可能彻底卡死。硬钳位保证"鬼魂不会跑远"这条规则真的成立，而不是靠
// 玩家自觉——而靠自觉的规则在多人游戏里等于不存在。
func TestTetherClampsGhostToRadius(t *testing.T) {
	w := newSystemsWorld(t, 7)
	w.GhostTether = 5.0

	_, alive := w.Players.Spawn()
	alive.State = world.PlayerAlive
	alive.HP = 100
	alive.Pos = world.V(10, 10)

	_, ghost := w.Players.Spawn()
	ghost.State = world.PlayerGhost
	ghost.Pos = world.V(30, 10) // 距离 20，远超半径 5

	TetherGhosts(w)

	if d := ghost.Pos.Sub(alive.Pos).Len(); math.Abs(d-5.0) > 1e-6 {
		t.Fatalf("钳位后距离 = %v，期望 5.0", d)
	}
	// 方向必须保持：钳到球面上最近的点，不是随手挪回旁边
	if math.Abs(ghost.Pos.Y-10) > 1e-6 {
		t.Fatalf("钳位改变了方向：Pos = %v", ghost.Pos)
	}
	if ghost.Pos.X < 14.9 || ghost.Pos.X > 15.1 {
		t.Fatalf("钳位后 X = %v，期望 ≈15", ghost.Pos.X)
	}
}

// 半径内的鬼魂不动：钳位只在超界时生效。
func TestTetherLeavesInRangeGhostAlone(t *testing.T) {
	w := newSystemsWorld(t, 7)
	w.GhostTether = 5.0

	_, alive := w.Players.Spawn()
	alive.State = world.PlayerAlive
	alive.Pos = world.V(10, 10)

	_, ghost := w.Players.Spawn()
	ghost.State = world.PlayerGhost
	ghost.Pos = world.V(12, 10) // 距离 2

	TetherGhosts(w)

	if ghost.Pos != world.V(12, 10) {
		t.Fatalf("半径内的鬼魂被移动了：%v", ghost.Pos)
	}
}

// 拴到最近的那个活人：队伍分散成两拨时，鬼魂跟任意一拨都行。
//
// 用质心会出现一个虚空拴绳中心——两拨之间的空地上鬼魂能自由活动，
// 而那里离两拨都很远，等于拴绳失效。
func TestTetherUsesNearestAlive(t *testing.T) {
	w := newSystemsWorld(t, 7)
	w.GhostTether = 3.0

	_, near := w.Players.Spawn()
	near.State = world.PlayerAlive
	near.Pos = world.V(10, 10)

	_, far := w.Players.Spawn()
	far.State = world.PlayerAlive
	far.Pos = world.V(100, 100)

	_, ghost := w.Players.Spawn()
	ghost.State = world.PlayerGhost
	ghost.Pos = world.V(20, 10)

	TetherGhosts(w)

	// 应该拴到 near 上，不是 far
	if d := ghost.Pos.Sub(near.Pos).Len(); math.Abs(d-3.0) > 1e-6 {
		t.Fatalf("到最近活人的距离 = %v，期望 3.0（到远处活人 %v）",
			d, ghost.Pos.Sub(far.Pos).Len())
	}
}

// 没有活人时不能钳位：团灭后鬼魂全僵在原地，连祭坛都走不到。
func TestTetherSkippedWhenNoAlive(t *testing.T) {
	w := newSystemsWorld(t, 7)
	w.GhostTether = 5.0

	_, ghost := w.Players.Spawn()
	ghost.State = world.PlayerGhost
	ghost.Pos = world.V(30, 30)

	TetherGhosts(w)

	if ghost.Pos != world.V(30, 30) {
		t.Fatalf("没有活人时鬼魂被移动了：%v", ghost.Pos)
	}
}

// 半径配成 0 或负数时跳过（配置错误不该把人钉死在原点）。
func TestTetherZeroRadiusIsNoop(t *testing.T) {
	w := newSystemsWorld(t, 7)
	w.GhostTether = 0

	_, alive := w.Players.Spawn()
	alive.State = world.PlayerAlive
	alive.Pos = world.V(10, 10)

	_, ghost := w.Players.Spawn()
	ghost.State = world.PlayerGhost
	ghost.Pos = world.V(30, 10)

	TetherGhosts(w)

	if ghost.Pos != world.V(30, 10) {
		t.Fatalf("半径为 0 时鬼魂被移动了：%v", ghost.Pos)
	}
}

// 活人不受影响：拴的是鬼魂。
func TestTetherIgnoresAlivePlayers(t *testing.T) {
	w := newSystemsWorld(t, 7)
	w.GhostTether = 3.0

	_, a := w.Players.Spawn()
	a.State = world.PlayerAlive
	a.Pos = world.V(10, 10)

	_, b := w.Players.Spawn()
	b.State = world.PlayerAlive
	b.Pos = world.V(50, 50)

	TetherGhosts(w)

	if b.Pos != world.V(50, 50) {
		t.Fatalf("活人被拴绳挪动了：%v", b.Pos)
	}
}
