package room

import (
	"testing"
	"time"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/game/world"
	"koukoukongjian/internal/proto"
)

// testBossConfig 返回一份只填必要字段的 Boss 参数。
//
// 不直接用 config.Default().Boss：那个 800 血 / 半血阈值是给真实对局调的，
// 而测试里手工摆的怪血量与它无关。但 Phase2AtPct 必须非零——零值会让
// UpdateBossPhase 直接短路。
func testBossConfig() *config.Boss {
	return &config.Boss{HP: 800, Phase2AtPct: 0.5}
}

// TestRoomBossSpawnedAfterAllHubsCleared 端到端钉住 Boss 刷新接线。
//
// 只测「Boss 真的出现在怪物池里」这一件事：SpawnBoss 的时机细节由
// systems/boss_test.go 覆盖，这里守的是「room 的 tick 到底调没调它」——
// M6 的教训是接线层零覆盖时纯函数测试可以全绿而功能完全没跑。
//
// 通过 r.World 读池子会与 tick goroutine 撞车（-race 实测报 Write at
// Pool.Spawn vs Read at Pool.Each），所以走 r.bossSeen 这个原子镜像。
func TestRoomBossSpawnedAfterAllHubsCleared(t *testing.T) {
	r, _ := startRoomWithOpts(t, "boss-spawn",
		Options{ID: "boss-spawn", Seed: 11, WavesEnabled: true},
		func(r *Room) {
			r.World.Boss = testBossConfig()
			// 预置：全部据点已清场。HubCleared 是 UpdateWaves 的终态，
			// 不会再被改回去。
			for i := range r.World.Hubs {
				r.World.Hubs[i].State = world.HubCleared
			}
		})

	waitForCondition(t, 3*time.Second, "tick 循环刷出 Boss", func() bool {
		return r.bossSeen.Load()
	})
}

// TestRoomBossPhaseProgresses 端到端钉住 UpdateBossPhase 接线。
//
// 直接摆一只残血 Boss 到池子里，等它自己进二阶段——守的是
// 「room 的 tick 调了 UpdateBossPhase」。阶段从 bossPhaseSeen 读。
func TestRoomBossPhaseProgresses(t *testing.T) {
	r, _ := startRoomWithOpts(t, "boss-phase",
		Options{ID: "boss-phase", Seed: 13, WavesEnabled: true},
		func(r *Room) {
			r.World.Boss = testBossConfig()
			r.World.BossSpawned = true // 阻止 SpawnBoss 再刷一只
			id, m := r.World.Monsters.Spawn()
			if id == world.InvalidID {
				t.Fatal("怪物池满")
			}
			m.Pos = r.World.Hubs[r.World.SpawnHub].Center
			m.Radius = 0.9
			m.HP, m.MaxHP = 100, 800 // 远低于 50% 阈值
			m.TypeID = 2
			m.Flags |= world.MonsterFlagBoss
			m.BossPhase = 1
		})

	waitForCondition(t, 3*time.Second, "Boss 进入二阶段", func() bool {
		return r.bossPhaseSeen.Load() >= 2
	})
}

// TestRoomBossAppearsInSnapshot 钉住 Boss 真的进了快照。
//
// 上面两个用例只证明 tick 循环改了世界状态；这一条证明客户端看得见——
// 两件事在接线断掉时都会失败，但失败原因完全不同（一个是 SpawnBoss 没调，
// 一个是快照没下发）。
func TestRoomBossAppearsInSnapshot(t *testing.T) {
	r, cancel := startRoomWithOpts(t, "boss-snap",
		Options{ID: "boss-snap", Seed: 23, WavesEnabled: true},
		func(r *Room) {
			r.World.Boss = testBossConfig()
			for i := range r.World.Hubs {
				r.World.Hubs[i].State = world.HubCleared
			}
		})
	_ = cancel

	res, sink := joinPlayer(t, r, "watcher")

	// 等一帧里出现带 Boss 标志的怪物实体。
	waitForCondition(t, 3*time.Second, "快照中出现 Boss 实体", func() bool {
		r.SendInput(res.Slot, InputCommand{Seq: uint16(time.Now().UnixNano() & 0xffff), Aim: 0, HasAim: true})
		select {
		case data := <-sink:
			s := decodeSnapshot(t, data)
			for ref, e := range snapshotEntities(s) {
				if ref.kind == proto.EntityKind_ENTITY_KIND_MONSTER &&
					e.Flags&uint32(world.MonsterFlagBoss) != 0 {
					return true
				}
			}
		case <-time.After(50 * time.Millisecond):
		}
		return false
	})
}

// TestRoomNoBossWithoutConfig 是对照组：没配 Boss 时整个链路安静。
func TestRoomNoBossWithoutConfig(t *testing.T) {
	r, _ := startRoomWithOpts(t, "boss-none",
		Options{ID: "boss-none", Seed: 19, WavesEnabled: true},
		func(r *Room) {
			r.World.Boss = nil
			for i := range r.World.Hubs {
				r.World.Hubs[i].State = world.HubCleared
			}
		})

	// 给 tick 循环足够的时间去（错误地）刷 Boss。
	time.Sleep(200 * time.Millisecond)
	if r.bossSeen.Load() {
		t.Fatal("Boss 参数为空却刷出了 Boss")
	}
	if got := r.bossPhaseSeen.Load(); got != 0 {
		t.Fatalf("bossPhaseSeen = %d，期望 0", got)
	}
}

// TestTrackBossPhaseDoesNotLogOnSpawn 钉住「刷出那一 tick 不算二阶段」。
//
// bossPhaseSeen 初值 0，而 Boss 刷出来时就带 BossPhase=1。只判
// `phase <= seen` 会让刷出那一 tick 就打一行「Boss 进入二阶段」——
// 实测端到端日志里出现过 phase=1 hp_pct=1 的假二阶段。
func TestTrackBossPhaseDoesNotLogOnSpawn(t *testing.T) {
	r, _ := startRoomWithOpts(t, "boss-nophase",
		Options{ID: "boss-nophase", Seed: 11, WavesEnabled: true},
		func(r *Room) {
			r.World.Boss = testBossConfig()
			for i := range r.World.Hubs {
				r.World.Hubs[i].State = world.HubCleared
			}
		})

	waitForCondition(t, 3*time.Second, "tick 循环刷出 Boss", func() bool {
		return r.bossSeen.Load()
	})
	// 刷出之后再跑几 tick，让 trackBossPhase 充分执行。
	time.Sleep(150 * time.Millisecond)
	// 断言 seen 仍是 0：只要 trackBossPhase 把 phase=1 当成「换阶段」
	// 记下，seen 就会变成 1，刷出那一 tick 会多打一行假日志。
	if seen := r.bossPhaseSeen.Load(); seen != 0 {
		t.Fatalf("刷出时 bossPhaseSeen = %d，期望 0——刷出那一 tick 被当成了阶段切换", seen)
	}
}
