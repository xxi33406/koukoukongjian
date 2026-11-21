package systems

import (
	"testing"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/game/world"
)

// newBossWorld 造一个带 4 个据点的世界（newSystemsWorld 是关地图的）。
//
// 手工填 Hubs 而不是跑 mapgen：mapgen 会消耗 RNG，同一颗种子先跑一次
// 生成会让后续随机断言的期望值全变（M7 Task 3 的教训）。
func newBossWorld(t *testing.T) *world.World {
	t.Helper()
	w := newSystemsWorld(t, 5)
	w.HasMap = true
	w.Hubs = []world.Hub{
		{Center: world.V(10, 10), RadiusM: 5, State: world.HubIdle},
		{Center: world.V(40, 10), RadiusM: 5, State: world.HubIdle},
		{Center: world.V(10, 40), RadiusM: 5, State: world.HubIdle},
		{Center: world.V(40, 40), RadiusM: 5, State: world.HubIdle},
	}
	w.BossHub = -1
	w.Boss = &config.Boss{
		HP:           800,
		Speed:        2.0,
		AttackDamage: 20,
		AttackRange:  1.8,
		Radius:       0.9,
		Phase2AtPct:  0.5,
	}
	return w
}

// clearAllHubsExcept 把除 keep 之外的全部据点推到清场态。
//
// keep < 0 表示"全部清场"（没有例外）。
func clearAllHubsExcept(w *world.World, keep int) {
	for i := range w.Hubs {
		if i == keep {
			w.Hubs[i].State = world.HubFighting
			continue
		}
		w.Hubs[i].State = world.HubCleared
	}
}

// TestSpawnBossWhenLastHubCleared 钉住核心时机：全部据点清完才刷 Boss。
func TestSpawnBossWhenLastHubCleared(t *testing.T) {
	w := newBossWorld(t)

	// 差一个据点没清：不该刷。
	clearAllHubsExcept(w, 0)
	if SpawnBoss(w) {
		t.Fatal("还有据点未清完就刷出了 Boss")
	}
	if w.BossSpawned {
		t.Fatal("BossSpawned 在未满足条件时被置位")
	}

	// 全清：刷。
	w.Hubs[0].State = world.HubCleared
	if !SpawnBoss(w) {
		t.Fatal("全部据点清完后没有刷出 Boss")
	}
	if !w.BossSpawned {
		t.Fatal("刷出后 BossSpawned = false")
	}
	if !w.HasBoss() {
		t.Fatal("刷出后在怪物池里找不到带 MonsterFlagBoss 的实体")
	}
}

// TestSpawnBossOnlyOnce 钉住不重复刷。
func TestSpawnBossOnlyOnce(t *testing.T) {
	w := newBossWorld(t)
	clearAllHubsExcept(w, -1)

	if !SpawnBoss(w) {
		t.Fatal("首次调用没刷出 Boss")
	}
	before := w.Monsters.Len()
	if SpawnBoss(w) {
		t.Fatal("第二次调用仍然返回 true")
	}
	if after := w.Monsters.Len(); after != before {
		t.Fatalf("第二次调用后怪物数 %d → %d，期望不变", before, after)
	}
}

// TestBossSpawnedAtBossHub 钉住落点、血量、TypeID 三项。
//
// TypeID 单独断言：客户端也靠它区分渲染，不只靠标志位。少了这条断言，
// 把 TypeID 写成 1（普通怪）不会有任何用例变红（变异验证发现的）。
func TestBossSpawnedAtBossHub(t *testing.T) {
	w := newBossWorld(t)
	clearAllHubsExcept(w, -1)

	if !SpawnBoss(w) {
		t.Fatal("没刷出 Boss")
	}
	want := w.Hubs[len(w.Hubs)-1].Center
	found := false
	w.Monsters.Each(func(_ world.EntityID, m *world.MonsterState) bool {
		if !m.Flags.Has(world.MonsterFlagBoss) {
			return true
		}
		found = true
		if d := m.Pos.DistSq(want); d > 1.0 {
			t.Fatalf("Boss 位置 %v 距据点中心 %v 有 %.2f 米，期望 ≤1 米",
				m.Pos, want, m.Pos.Sub(want).Len())
		}
		if m.HP != w.Boss.HP || m.MaxHP != w.Boss.HP {
			t.Fatalf("Boss 血量 = %v/%v，期望 %v", m.HP, m.MaxHP, w.Boss.HP)
		}
		if m.TypeID != 2 {
			t.Fatalf("Boss TypeID = %d，期望 2（客户端靠它区分渲染）", m.TypeID)
		}
		if m.BossPhase != 1 {
			t.Fatalf("Boss 初始阶段 = %d，期望 1", m.BossPhase)
		}
		return false
	})
	if !found {
		t.Fatal("怪物池里没有 Boss")
	}
}

// TestHasBossIgnoresNormalMonsters 钉住身份判据只看标志位。
func TestHasBossIgnoresNormalMonsters(t *testing.T) {
	w := newBossWorld(t)
	spawnMonsterAt(t, w, 10, 10)
	if w.HasBoss() {
		t.Fatal("普通怪被当成 Boss")
	}
}

// TestHasBossIgnoresDeadBoss 钉住"死了的 Boss 不算还有 Boss"。
//
// HasBoss 回答"现在还有没有 Boss 要打"，BossDead 回答"打过了没有"。
// 两者在 Boss 被 Reap 回收后都是 false 但语义不同，混用会让"打完了"
// 被当成"还没刷"。
func TestHasBossIgnoresDeadBoss(t *testing.T) {
	w := newBossWorld(t)
	clearAllHubsExcept(w, -1)
	if !SpawnBoss(w) {
		t.Fatal("没刷出 Boss")
	}
	w.Monsters.Each(func(_ world.EntityID, m *world.MonsterState) bool {
		if m.Flags.Has(world.MonsterFlagBoss) {
			m.HP = 0
			m.Flags |= world.MonsterFlagDead
			return false
		}
		return true
	})
	if w.HasBoss() {
		t.Fatal("已死的 Boss 仍被算作「还有 Boss 要打」")
	}
}

// TestBossDeadFlaggedOnKill 钉住 Boss 被击杀时置 BossDead。
func TestBossDeadFlaggedOnKill(t *testing.T) {
	w := newBossWorld(t)
	pid, _ := spawnPlayerAt(t, w, 5, 5)
	clearAllHubsExcept(w, -1)
	if !SpawnBoss(w) {
		t.Fatal("没刷出 Boss")
	}
	var bossID world.EntityID
	w.Monsters.Each(func(id world.EntityID, m *world.MonsterState) bool {
		if m.Flags.Has(world.MonsterFlagBoss) {
			bossID = id
			return false
		}
		return true
	})
	if !bossID.IsValid() {
		t.Fatal("找不到 Boss 实体")
	}

	w.ResolveDamage(pid, bossID, world.EntityClassMonster, 2000, world.ElementNone, world.DamageTags{}, world.InvalidID)
	if !w.BossDead {
		t.Fatal("Boss 被打死后 BossDead = false")
	}
}

// TestNoBossInNoMapWorld 钉住无图世界不刷 Boss。
func TestNoBossInNoMapWorld(t *testing.T) {
	w := newSystemsWorld(t, 5)
	if w.HasMap {
		t.Fatal("前提失效：newSystemsWorld 应是关地图世界")
	}
	w.Boss = &config.Boss{HP: 100}
	if SpawnBoss(w) {
		t.Fatal("无图世界刷出了 Boss")
	}
}

// TestNoBossWhenConfigDisabled 钉住 Boss 参数未装配时不刷（HP=0 哨兵）。
func TestNoBossWhenConfigDisabled(t *testing.T) {
	w := newBossWorld(t)
	w.Boss = nil
	clearAllHubsExcept(w, -1)
	if SpawnBoss(w) {
		t.Fatal("Boss 参数为空时仍然刷出了 Boss")
	}
}

// TestSpawnBossRetriesWhenPoolFull 钉住池满时不置 BossSpawned（下一 tick 重试）。
//
// 池满时"少刷一 tick"是最轻的降级；置 BossSpawned 会让本关永久失去 Boss，
// 那比少刷一 tick 糟得多。
func TestSpawnBossRetriesWhenPoolFull(t *testing.T) {
	w := newBossWorld(t)
	clearAllHubsExcept(w, -1)
	// 把怪物池塞满。
	for i := 0; i < world.MaxIndex; i++ {
		if id, _ := w.Monsters.Spawn(); id == world.InvalidID {
			break
		}
	}
	if SpawnBoss(w) {
		t.Fatal("池满时仍然报成功")
	}
	if w.BossSpawned {
		t.Fatal("池满时置了 BossSpawned——本关之后再也刷不出 Boss")
	}
}
