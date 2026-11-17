package room

import (
	"testing"

	"koukoukongjian/internal/game/world"
)

// joinNoRun 在「New 之后、Run 之前」直接调一次 handleJoin，返回结果。
//
// 为什么不用 startRoom + 快照：这里要断言的 Level/ExpToNext 是纯服务端
// 状态，快照里不带它们（客户端不需要，加进快照就得同步改 proto，属于
// 另一件事）。而 Run 之后 World 归 tick goroutine 独占，测试 goroutine
// 直接读它是数据竞争（AGENTS.md §2.5）；不开 Run 就没有第二个 goroutine，
// 读 World 是安全的——与 TestNewSpawnsDummyTargets 读 New 摆好的靶子是
// 同一个前提。
func joinNoRun(t *testing.T, r *Room, name string) (JoinResult, *world.PlayerState) {
	t.Helper()
	req := JoinRequest{
		Name:  name,
		Sink:  make(chan []byte, 8),
		Reply: make(chan JoinResult, 1),
	}
	r.handleJoin(req)
	select {
	case res := <-req.Reply:
		if res.Err != nil {
			t.Fatalf("加入失败：%v", res.Err)
		}
		p := r.World.Players.Get(res.EntityID)
		if p == nil {
			t.Fatalf("加入后应能按 ID %v 取到玩家实体", res.EntityID)
		}
		return res, p
	default:
		t.Fatal("handleJoin 未回执（Reply 里没有结果）")
		return JoinResult{}, nil
	}
}

// 池把槽位整个清零（见 world.TestPlayerLevelFieldsZeroedOnSpawn），所以
// 「新玩家是 1 级」这件事只能由 handleJoin 补上。这条测试守的就是这一步
// 不会被漏掉——漏了的现象是新玩家 Level=0：经验照常能加，升级判定却把
// 它当"已经满级"或者"非法等级"，而且全程不报错，是那种要先看见数值才
// 知道坏了的 bug。
func TestJoinInitializesLevel(t *testing.T) {
	cfg := testConfig()
	// ExpBase 刻意改成非默认值：断言 ExpToNext 时若实现里硬编码了 20，
	// 用默认配置的测试发现不了（20 == 20 恒真）。
	cfg.Progression.ExpBase = 33
	wantNext := uint32(cfg.Progression.ExpBase)

	r := New(cfg, quietLogger(), Options{ID: "level", Seed: 7})
	_, p := joinNoRun(t, r, "lv")

	if p.Level != 1 {
		t.Fatalf("出生 Level = %d，期望 1（Pool.Spawn 清零，必须由 handleJoin 设）", p.Level)
	}
	if p.Exp != 0 {
		t.Fatalf("出生 Exp = %d，期望 0", p.Exp)
	}
	// 初值必须来自配置而不是写死的 20：ExpBase 一改，曲线首级就跟着改。
	if p.ExpToNext != wantNext {
		t.Fatalf("出生 ExpToNext = %d，期望 %d（= cfg.Progression.ExpBase）", p.ExpToNext, wantNext)
	}
}

// 玩家离开后座位与实体都被回收，下一个人重新加入必须还是 1 级新手——
// 否则上一局的老兵会把自己的等级带进下一局。Kill 已把槽位清零，
// 这里验证的是"清零 + handleJoin 补 1"两段合起来的结果。
func TestRejoinAfterRecycleStartsAtLevelOne(t *testing.T) {
	cfg := testConfig()
	cfg.Progression.ExpBase = 20
	r := New(cfg, quietLogger(), Options{ID: "level-rejoin", Seed: 7})

	res1, p1 := joinNoRun(t, r, "a")
	p1.Level = 9
	p1.Exp = 500
	p1.ExpToNext = 4096

	// 回收实体并腾出座位（等价于玩家掉线时 tick 循环做的那两步），
	// 这样下一次 Spawn 会复用同一个槽位。
	if !r.World.Players.Kill(res1.EntityID) {
		t.Fatal("Kill 应成功回收存活玩家")
	}
	r.peers.remove(res1.Slot)

	res2, p2 := joinNoRun(t, r, "b")
	if p2.Level != 1 {
		t.Fatalf("回收后重新加入 Level = %d，期望 1（等级不能跨局残留）", p2.Level)
	}
	if p2.Exp != 0 {
		t.Fatalf("回收后重新加入 Exp = %d，期望 0", p2.Exp)
	}
	if p2.ExpToNext != uint32(cfg.Progression.ExpBase) {
		t.Fatalf("回收后重新加入 ExpToNext = %d，期望 %d", p2.ExpToNext, uint32(cfg.Progression.ExpBase))
	}
	// 确认确实复用了同一槽位——否则这条测试测不到"复用槽位"这个前提。
	if res2.EntityID.Index() != res1.EntityID.Index() {
		t.Fatalf("期望复用槽位 %d，实际拿到 %d，测试前提不成立",
			res1.EntityID.Index(), res2.EntityID.Index())
	}
}
