package room

import (
	"testing"
	"time"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/game/world"
)

// testBossConfig 返回一份只填必要字段的 Boss 参数。
//
// 不直接用 config.Default().Boss：那个 800 血 / 半血阈值是给真实对局调的，
// 而测试里手工摆的怪血量与它无关。但 Phase2AtPct 必须非零——零值会让
// UpdateBossPhase 直接短路。
func testBossConfig() *config.Boss {
	return &config.Boss{HP: 800, Phase2AtPct: 0.5}
}

// bossInPool 返回池中活着的 Boss 及其阶段（0 表示没有）。
//
// 每次调用都重新扫一遍池子：Boss 可能被 Reap 掉槽位，缓存指针会悬空。
func bossInPool(r *Room) (world.EntityID, *world.MonsterState) {
	var id world.EntityID
	var got *world.MonsterState
	r.World.Monsters.Each(func(mid world.EntityID, m *world.MonsterState) bool {
		if m.Flags.Has(world.MonsterFlagBoss) && !m.IsDead() {
			id, got = mid, m
			return false
		}
		return true
	})
	return id, got
}

// TestRoomBossSpawnedAfterAllHubsCleared 端到端钉住 Boss 刷新接线。
//
// 只测「Boss 真的出现在怪物池里」这一件事：SpawnBoss 的时机细节由
// systems/boss_test.go 覆盖，这里守的是「room 的 tick 到底调没调它」——
// M6 的教训是接线层零覆盖时纯函数测试可以全绿而功能完全没跑。
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
		id, _ := bossInPool(r)
		return id.IsValid()
	})
}

// TestRoomBossPhaseProgresses 端到端钉住 UpdateBossPhase 接线。
//
// 直接摆一只残血 Boss 到池子里，等它自己进二阶段——守的是
// 「room 的 tick 调了 UpdateBossPhase」。
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
		_, m := bossInPool(r)
		return m != nil && m.BossPhase >= 2
	})
}

// TestRoomBossPhaseIsTrackedInLog 钉住 trackBossPhase 真的留了痕。
//
// 验收与压测都靠这行日志确认「真的进过二阶段」：没有它，「从不换阶段」
// 与「换得很晚」从外部完全无法区分。
func TestRoomBossPhaseIsTrackedInLog(t *testing.T) {
	r, _ := startRoomWithOpts(t, "boss-log",
		Options{ID: "boss-log", Seed: 17, WavesEnabled: true},
		func(r *Room) {
			r.World.Boss = testBossConfig()
			r.World.BossSpawned = true
			id, m := r.World.Monsters.Spawn()
			if id == world.InvalidID {
				t.Fatal("怪物池满")
			}
			m.Pos = r.World.Hubs[r.World.SpawnHub].Center
			m.Radius = 0.9
			m.HP, m.MaxHP = 10, 800
			m.TypeID = 2
			m.Flags |= world.MonsterFlagBoss
			m.BossPhase = 1
		})

	waitForCondition(t, 3*time.Second, "bossPhaseSeen 被更新", func() bool {
		return r.bossPhaseSeen >= 2
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
	if id, _ := bossInPool(r); id.IsValid() {
		t.Fatal("Boss 参数为空却刷出了 Boss")
	}
	if r.bossPhaseSeen != 0 {
		t.Fatalf("bossPhaseSeen = %d，期望 0", r.bossPhaseSeen)
	}
}
