package systems

import (
	"testing"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/game/world"
)

// spawnBossForTest 直接摆一只 Boss（不经 SpawnBoss，避免依赖据点状态）。
//
// 单个用例只关心"阶段推进"这一件事，而 SpawnBoss 需要地图 + 全部据点
// 清场两个前置——把它们拖进每个用例会让失败信息指向错误的方向。
func spawnBossForTest(t *testing.T, w *world.World, hp float64) (world.EntityID, *world.MonsterState) {
	t.Helper()
	id, m := w.Monsters.Spawn()
	if id == world.InvalidID {
		t.Fatal("怪物池满了，摆不出 Boss")
	}
	m.Pos = world.V(50, 50)
	m.Radius = 0.9
	m.HP, m.MaxHP = hp, hp
	m.TypeID = 2
	m.Flags |= world.MonsterFlagBoss
	m.BossPhase = 1
	return id, m
}

// bossWorld 造一个带 Boss 配置的空白世界。
func bossWorld(t *testing.T, b config.Boss) *world.World {
	t.Helper()
	w := newSystemsWorld(t, 5)
	w.Boss = &b
	return w
}

// TestBossEntersPhase2BelowHalfHP 钉住核心阈值。
//
// 边界用 <=：HP 恰好等于阈值时换阶段。「差一点就换」与「到了才换」在
// 玩家感知上没有区别，但写成 < 会让阈值那一帧白打一次——而血量是整数
// 伤害累积出来的，恰好落在阈值上并不罕见。
func TestBossEntersPhase2BelowHalfHP(t *testing.T) {
	w := bossWorld(t, config.Boss{HP: 800, Phase2AtPct: 0.5})
	_, m := spawnBossForTest(t, w, 800)

	// 800 血的 50% = 400。留 401 应当还在一阶段。
	m.HP = 401
	UpdateBossPhase(w)
	if m.BossPhase != 1 {
		t.Fatalf("HP=401/800 时 BossPhase = %d，期望仍为 1", m.BossPhase)
	}

	// 400 正好到阈值：换阶段用 <=。
	m.HP = 400
	UpdateBossPhase(w)
	if m.BossPhase != 2 {
		t.Fatalf("HP=400/800 时 BossPhase = %d，期望 2", m.BossPhase)
	}
	if !m.Flags.Has(world.MonsterFlagEnraged) {
		t.Fatal("二阶段没有加上 MonsterFlagEnraged（AI 靠它加速加伤）")
	}
}

// TestBossPhaseOnlyAdvances 钉住不回退：治疗把血量拉回半血以上也不回一阶段。
//
// 回退会让玩家看到 Boss 从二阶段忽然变回一阶段，读起来像 bug。而血量
// 本身就能表达「还差多少」，不需要回退来表达。
func TestBossPhaseOnlyAdvances(t *testing.T) {
	w := bossWorld(t, config.Boss{HP: 800, Phase2AtPct: 0.5})
	_, m := spawnBossForTest(t, w, 800)

	m.HP = 300
	UpdateBossPhase(w)
	if m.BossPhase != 2 {
		t.Fatalf("进入二阶段失败，BossPhase = %d", m.BossPhase)
	}

	m.HP = 800 // 被治疗满
	UpdateBossPhase(w)
	if m.BossPhase != 2 {
		t.Fatalf("血量回满后 BossPhase 退回 %d，期望仍为 2（阶段只进不退）", m.BossPhase)
	}
}

// TestBossSummonsOnceOnPhase2 钉住只召唤一次。
func TestBossSummonsOnceOnPhase2(t *testing.T) {
	w := bossWorld(t, config.Boss{HP: 800, Phase2AtPct: 0.5, SummonPerPhase2: 2})
	w.Mon = &config.Monster{HP: 50, Speed: 2.5}
	_, m := spawnBossForTest(t, w, 800)

	before := w.Monsters.Len()
	m.HP = 100
	UpdateBossPhase(w)
	if got := w.Monsters.Len() - before; got != 2 {
		t.Fatalf("二阶段召唤了 %d 只，期望 2", got)
	}
	if !m.BossSummoned {
		t.Fatal("召唤后 BossSummoned = false")
	}

	// 再调：不重复召唤。
	before = w.Monsters.Len()
	UpdateBossPhase(w)
	if got := w.Monsters.Len() - before; got != 0 {
		t.Fatalf("第二次调用又召唤了 %d 只，期望 0", got)
	}
}

// TestPhase2SummonsAreNormalMonsters 钉住召唤物不带 Boss 标志、TypeID 不是 2。
//
// TypeID 单独断言：变异验证发现只查标志位时，把召唤物的 TypeID 写成 2
// （客户端会把它渲染成 Boss）没有任何用例变红。
func TestPhase2SummonsAreNormalMonsters(t *testing.T) {
	w := bossWorld(t, config.Boss{HP: 800, Phase2AtPct: 0.5, SummonPerPhase2: 3})
	w.Mon = &config.Monster{HP: 50, Speed: 2.5}
	_, m := spawnBossForTest(t, w, 800)

	m.HP = 100
	UpdateBossPhase(w)

	bossCount, summonCount := 0, 0
	w.Monsters.Each(func(id world.EntityID, mon *world.MonsterState) bool {
		if mon.Flags.Has(world.MonsterFlagBoss) {
			bossCount++
			return true
		}
		summonCount++
		if mon.TypeID == 2 {
			t.Fatalf("召唤物 %v 的 TypeID = 2，客户端会把它渲染成 Boss", id)
		}
		if mon.HP != 50 {
			t.Fatalf("召唤物 HP = %v，期望按 w.Mon.HP = 50", mon.HP)
		}
		return true
	})
	if bossCount != 1 {
		t.Fatalf("带 Boss 标志的实体有 %d 个，期望 1（召唤物不应带）", bossCount)
	}
	if summonCount != 3 {
		t.Fatalf("召唤物 %d 只，期望 3", summonCount)
	}
}

// TestBossPhaseIgnoresNonBossMonsters 钉住普通怪不参与。
func TestBossPhaseIgnoresNonBossMonsters(t *testing.T) {
	w := bossWorld(t, config.Boss{HP: 100, Phase2AtPct: 0.5})
	id, mon := w.Monsters.Spawn()
	if id == world.InvalidID {
		t.Fatal("池满")
	}
	mon.HP, mon.MaxHP = 10, 100
	UpdateBossPhase(w) // 不应 panic、不应改任何东西
	if mon.BossPhase != 0 {
		t.Fatalf("普通怪的 BossPhase = %d，期望保持 0", mon.BossPhase)
	}
	if mon.Flags.Has(world.MonsterFlagEnraged) {
		t.Fatal("普通怪被置上了狂暴标志")
	}
}

// TestBossPhaseShortCircuitsWithoutConfig 钉住没配 Boss 时安全。
func TestBossPhaseShortCircuitsWithoutConfig(t *testing.T) {
	w := newSystemsWorld(t, 5)
	w.Boss = nil
	UpdateBossPhase(w) // 不 panic
}

// TestBossPhaseSkipsDeadBoss 钉住尸体不参与（Reap 之前的那一帧）。
func TestBossPhaseSkipsDeadBoss(t *testing.T) {
	w := bossWorld(t, config.Boss{HP: 800, Phase2AtPct: 0.5, SummonPerPhase2: 2})
	w.Mon = &config.Monster{HP: 50}
	_, m := spawnBossForTest(t, w, 800)

	m.HP = 0
	m.Flags |= world.MonsterFlagDead
	before := w.Monsters.Len()
	UpdateBossPhase(w)
	if m.BossPhase != 1 {
		t.Fatalf("已死的 Boss 进入了 %d 阶段", m.BossPhase)
	}
	if got := w.Monsters.Len() - before; got != 0 {
		t.Fatalf("已死的 Boss 召唤了 %d 只", got)
	}
}
