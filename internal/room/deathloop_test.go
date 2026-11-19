package room

import (
	"testing"
	"time"

	"koukoukongjian/internal/game/world"
	"koukoukongjian/internal/proto"
)

// 玩家被打死后状态变成鬼魂，客户端能在快照里看到。
//
// 走快照而不是读 r.World：tick goroutine 独占 World，测试直接读会与它
// 并发，-race 必报。这是 M2 起就立下的规矩（combat_test.go 的 waitSnapshot）。
//
// 怎么让玩家死：玩家由 joinPlayer 在 Run 之后创建，setup 回调里还没有它，
// 所以没法直接调血量。改成在出生点（= 祭坛中心）摆一只高血量的怪——
// 玩家出生在中心 1 米环上，落在怪物 AttackRange=1.2 内，怪会一直打。
// 100 血 / 10 伤 / 1 秒冷却 ≈ 10 秒后变鬼魂。
func TestRoomPlayerDiesBecomesGhost(t *testing.T) {
	r, _ := startRoomWith(t, "death-ghost", func(r *Room) {
		hub := r.World.Hubs[r.World.SpawnHub]
		spawnTarget(r, hub.Center, 10000)
	})
	_, sink := joinPlayer(t, r, "victim")

	waitSnapshot(t, sink, 25*time.Second, "玩家变鬼魂", func(s *proto.Snapshot) bool {
		return s.Self != nil && s.Self.State == uint32(world.PlayerGhost)
	})
}

// 鬼魂站到祭坛上会自动复活（自动开始，不需要按键）。
//
// 这是 M5 最完整的一条链路：被打死 → 鬼魂 → 站在祭坛上 → 引导 3 秒 →
// 复活并拿到半血。玩家出生点就是祭坛中心，所以死后不需要走动。
func TestRoomGhostRevivesAtAltar(t *testing.T) {
	r, _ := startRoomWith(t, "revive-altar", func(r *Room) {
		hub := r.World.Hubs[r.World.SpawnHub]
		spawnTarget(r, hub.Center, 10000)
	})
	_, sink := joinPlayer(t, r, "ghost")

	waitSnapshot(t, sink, 25*time.Second, "先变鬼魂", func(s *proto.Snapshot) bool {
		return s.Self != nil && s.Self.State == uint32(world.PlayerGhost)
	})
	waitSnapshot(t, sink, 15*time.Second, "在祭坛复活", func(s *proto.Snapshot) bool {
		return s.Self != nil && s.Self.State == uint32(world.PlayerAlive) && s.Self.Hp > 0
	})
}

// 复活事件能在快照的 events 里看到（客户端靠它播特效）。
func TestRoomReviveProducesEvent(t *testing.T) {
	r, _ := startRoomWith(t, "revive-event", func(r *Room) {
		hub := r.World.Hubs[r.World.SpawnHub]
		spawnTarget(r, hub.Center, 10000)
	})
	_, sink := joinPlayer(t, r, "ghost")

	waitSnapshot(t, sink, 35*time.Second, "复活事件", func(s *proto.Snapshot) bool {
		for _, ev := range s.Events {
			if ev.Kind == proto.GameEventKind_GAME_EVENT_KIND_REVIVE {
				return true
			}
		}
		return false
	})
}

// 掉落物真的会生成（Task 6 的生产调用方），且客户端能看见。
//
// 20 只 1 血怪挤在出生点前方：15%/5% 两条掉落的期望是 3 次 / 1 次，
// 只要 RNG 正常几乎必出。这里断言的是「快照里出现了 PICKUP 实体的
// adds」——M4 的教训是 systems 层全绿但生产调用方为零，所以这条必须走
// 真实 tick 而不是单测 SpawnDrops。
func TestRoomKillSpawnsDrops(t *testing.T) {
	r, _ := startRoomWith(t, "drops", func(r *Room) {
		hub := r.World.Hubs[r.World.SpawnHub]
		// 把怪物攻击力调成 0：这条用例要观察的是"击杀后掉东西"，
		// 20 只怪围上来 1 秒就能把玩家打死，玩家变鬼魂就不再开火，
		// 观察窗口直接没了。关掉伤害让玩家能站着打完整场。
		if r.World.Mon != nil {
			r.World.Mon.AttackDamage = 0
		}
		for i := 0; i < 20; i++ {
			spawnTarget(r, hub.Center.Add(world.V(float64(i)*0.3, 1.0)), 1)
		}
	})
	res, sink := joinPlayer(t, r, "shooter")

	// 持续按攻击键：房间只保留最新一条输入（drainInputs 的语义），
	// 所以每 50ms 重发一次，覆盖整个观察窗口。
	//
	// HasAim=false 是关键：手动瞄准会把方向钉死成 Aim=0（朝 +X），
	// 而怪摆在 +Y 方向 1 米处，玩家会对着空气打。给 false 才走自动瞄准
	// （systems.ResolveAim），它会锁最近的怪。
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		seq := uint16(1)
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				r.SendInput(res.Slot, InputCommand{
					Seq: seq, HasAim: false, Buttons: ButtonAttack,
				})
				seq++
			}
		}
	}()

	waitSnapshot(t, sink, 25*time.Second, "掉落物出现在快照里", func(s *proto.Snapshot) bool {
		for _, d := range s.Adds {
			if d.Kind == proto.EntityKind_ENTITY_KIND_PICKUP {
				return true
			}
		}
		return false
	})
}

// 团灭广播：全队鬼魂时 events 里出现 WIPE，且只出现一次。
//
// 「只出现一次」的观察窗口刻意短于复活引导（3 秒＝60 tick）：玩家在祭坛上
// 死后会自动复活，复活会把 World.Wiped 清回 false，之后再死当然要再报一次
// ——那是正确行为，不是重复上报。所以这里只验证「同一段团灭里不刷屏」。
func TestRoomWipeEventOnce(t *testing.T) {
	r, _ := startRoomWith(t, "wipe", func(r *Room) {
		hub := r.World.Hubs[r.World.SpawnHub]
		spawnTarget(r, hub.Center, 10000)
	})
	_, sink := joinPlayer(t, r, "solo")

	seen := 0
	waitSnapshot(t, sink, 25*time.Second, "团灭事件", func(s *proto.Snapshot) bool {
		for _, ev := range s.Events {
			if ev.Kind == proto.GameEventKind_GAME_EVENT_KIND_WIPE {
				seen++
			}
		}
		return seen > 0
	})

	// 再 drain 500ms（10 tick），确认这段时间里没有第二条。
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case data := <-sink:
			for _, ev := range decodeSnapshot(t, data).Events {
				if ev.Kind == proto.GameEventKind_GAME_EVENT_KIND_WIPE {
					seen++
				}
			}
		case <-time.After(5 * time.Millisecond):
		}
	}
	if seen != 1 {
		t.Fatalf("团灭事件出现了 %d 次，期望 1 次（同一段团灭里不应重复广播）", seen)
	}
}

// 拴绳接线在位：鬼魂存在时房间不会 panic，快照继续出。
//
// 为什么不在这里验证「鬼魂被拉回半径内」：那需要同时存在一个鬼魂和一个
// 活人锚点，而 room 的测试接口只能通过 joinPlayer 造玩家、通过怪物打死
// 玩家来变鬼魂——两条路都要等 10 秒以上且互相干扰（活人锚点自己也会被
// 怪打死）。拴绳的钳位逻辑在 systems/tether_test.go 里逐条钉住了（6 个
// 用例，直接摆世界坐标），这里只负责证明 tick 里真的调了它、且不会崩。
func TestRoomGhostTetherWired(t *testing.T) {
	r, _ := startRoomWith(t, "tether", func(r *Room) {
		hub := r.World.Hubs[r.World.SpawnHub]
		spawnTarget(r, hub.Center, 10000)
	})
	_, sink := joinPlayer(t, r, "runner")

	// 等它死一次（鬼魂出现），再确认房间还在出快照。
	waitSnapshot(t, sink, 25*time.Second, "先变鬼魂", func(s *proto.Snapshot) bool {
		return s.Self != nil && s.Self.State == uint32(world.PlayerGhost)
	})
	waitSnapshot(t, sink, 5*time.Second, "房间仍在出快照（拴绳接线没崩）", func(s *proto.Snapshot) bool {
		return s != nil && s.ServerTick > 0
	})
}
