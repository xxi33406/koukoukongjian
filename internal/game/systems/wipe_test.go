package systems

import (
	"testing"

	"koukoukongjian/internal/game/world"
)

// 全队变鬼魂就是团灭（spec L52）。只在第一次成立时返回 true。
//
// 为什么"返回 true 只一次"而不是每次 tick 都 true：调用方靠这个返回值
// 决定要不要广播事件。每次 tick 都 true 会让客户端每秒收到 20 条团灭
// 事件，表现是失败 UI 疯狂闪烁或者日志爆掉。
func TestCheckTeamWipeReportsOnce(t *testing.T) {
	w := newSystemsWorld(t, 7)

	for i := 0; i < 3; i++ {
		_, p := w.Players.Spawn()
		p.State = world.PlayerGhost
		p.HP = 0
	}

	if !CheckTeamWipe(w) {
		t.Fatalf("全队鬼魂没有判定为团灭")
	}
	if CheckTeamWipe(w) {
		t.Fatalf("团灭被重复上报")
	}
	if CheckTeamWipe(w) {
		t.Fatalf("团灭被重复上报（第三次）")
	}
}

// 有人活着就不是团灭。
func TestCheckTeamWipeNeedsAllGhosts(t *testing.T) {
	w := newSystemsWorld(t, 7)

	_, g := w.Players.Spawn()
	g.State = world.PlayerGhost

	_, a := w.Players.Spawn()
	a.State = world.PlayerAlive
	a.HP = 100

	if CheckTeamWipe(w) {
		t.Fatalf("还有活人却判定为团灭")
	}
}

// 空房间不是团灭：一个人都没进来时不该报失败。
//
// 这条容易写错：`Players.Each` 在空池上零次回调，若实现写成
// "allGhost 初始 true、遍历后直接用"，空房间会被判成团灭，
// 而房间在等玩家的第一秒就会广播失败。
func TestCheckTeamWipeEmptyRoomIsNotWipe(t *testing.T) {
	w := newSystemsWorld(t, 7)

	if CheckTeamWipe(w) {
		t.Fatalf("空房间被判定为团灭")
	}
}

// 掉线的人也算非 Alive（spec L128「掉线即鬼魂」）。
func TestCheckTeamWipeCountsDisconnectedAsGhost(t *testing.T) {
	w := newSystemsWorld(t, 7)

	_, a := w.Players.Spawn()
	a.State = world.PlayerDisconnected

	_, b := w.Players.Spawn()
	b.State = world.PlayerGhost

	if !CheckTeamWipe(w) {
		t.Fatalf("掉线+鬼魂没有判定为团灭")
	}
}

// 复活后 Wiped 要清回 false，否则第二局团灭不报。
func TestWipeClearedOnRevive(t *testing.T) {
	w := newSystemsWorld(t, 7)
	w.HasMap = true
	w.Hubs = []world.Hub{{Center: world.V(20, 20), RadiusM: 5}}

	_, p := w.Players.Spawn()
	p.State = world.PlayerGhost
	p.Pos = world.V(20, 20)
	p.MaxHP = 100

	if !CheckTeamWipe(w) {
		t.Fatalf("前提失效：单人全鬼魂应团灭")
	}

	ReviveBegin(w, p, 5)
	w.Tick = 5
	if !ReviveTick(w, p, w.Tick) {
		t.Fatalf("前提失效：复活没完成")
	}

	if CheckTeamWipe(w) {
		t.Fatalf("复活后仍报团灭")
	}
	// 再死一次要能重新报
	p.State = world.PlayerGhost
	if !CheckTeamWipe(w) {
		t.Fatalf("第二次团灭没有上报（Wiped 没被复活清掉）")
	}
}

// 倒地也算团灭：倒地没人扶等于死，而灰盒里倒地状态没有写入方（M5 只出
// 鬼魂），所以这条是为将来留的判据——若哪天有人加了倒地却不改这里，
// 全队倒地会既不算团灭也无法行动。
func TestCheckTeamWipeCountsDowned(t *testing.T) {
	w := newSystemsWorld(t, 7)

	_, a := w.Players.Spawn()
	a.State = world.PlayerDowned

	if !CheckTeamWipe(w) {
		t.Fatalf("全队倒地没有判定为团灭")
	}
}
