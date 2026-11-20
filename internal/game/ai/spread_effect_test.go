package ai

import (
	"testing"

	"koukoukongjian/internal/game/world"
)

// TestEnragedChaseIsFaster 钉住狂暴加速（spec L61「速度提升」）。
//
// 走 UpdateMonster 的真实位移而不是直接调 chaseSpeed：直接调等于把
// 「AI 有没有用上这个倍率」这件事交给读者自己确认，而它恰恰是最容易
// 漏接的一环（倍率算对了但没人调用）。
func TestEnragedChaseIsFaster(t *testing.T) {
	w := newAIWorld(t)
	pid := spawnPl(t, w, 30, 10) // 加速线之外
	mid := spawnMon(t, w, 6, 10)
	m := w.Monsters.Get(mid)
	m.TargetID = pid
	m.State = world.MonsterChase
	m.Flags |= world.MonsterFlagEnraged

	UpdateMonster(w, m, 1.0)

	got := m.Pos.Sub(world.V(6, 10)).Len()
	// 2.5（基础）× 1.5（反风筝）× 1.4（狂暴）
	want := 2.5 * 1.5 * 1.4
	if got < want*0.9 || got > want*1.1 {
		t.Fatalf("狂暴追赶位移 %.3f，期望 ≈%.3f（2.5×1.5×1.4）", got, want)
	}
}

// TestNotEnragedChaseIsNormal 是对照组：同位置无狂暴时应只有反风筝倍率。
//
// 没有这一条，TestEnragedChaseIsFaster 无法区分「狂暴生效了」与
// 「基础速度本来就更快」。
func TestNotEnragedChaseIsNormal(t *testing.T) {
	w := newAIWorld(t)
	pid := spawnPl(t, w, 30, 10)
	mid := spawnMon(t, w, 6, 10)
	m := w.Monsters.Get(mid)
	m.TargetID = pid
	m.State = world.MonsterChase

	UpdateMonster(w, m, 1.0)

	got := m.Pos.Sub(world.V(6, 10)).Len()
	want := 2.5 * 1.5
	if got < want*0.9 || got > want*1.1 {
		t.Fatalf("非狂暴追赶位移 %.3f，期望 ≈%.3f（只吃反风筝）", got, want)
	}
}

// attackOnce 让一只站在攻击范围内的怪打一下，返回玩家掉的血。
func attackOnce(t *testing.T, w *world.World, m *world.MonsterState, pid world.EntityID) float64 {
	t.Helper()
	m.State = world.MonsterAttack
	m.TargetID = pid
	w.Tick = 1000 // 保证冷却已过
	m.AttackAt = 0

	before := w.Players.Get(pid).HP
	UpdateMonster(w, m, 1.0)
	return before - w.Players.Get(pid).HP
}

// TestEnragedAttackHitsHarder 钉住狂暴伤害（spec L61「攻击提升」）。
func TestEnragedAttackHitsHarder(t *testing.T) {
	w := newAIWorld(t)
	pid := spawnPl(t, w, 10, 10)
	mid := spawnMon(t, w, 10.5, 10) // 攻击范围内
	m := w.Monsters.Get(mid)
	m.Flags |= world.MonsterFlagEnraged

	got := attackOnce(t, w, m, pid)
	// 10（AttackDamage）× 1.3
	want := 13.0
	if got < want-0.01 || got > want+0.01 {
		t.Fatalf("狂暴攻击掉血 %.2f，期望 %.2f（10×1.3）", got, want)
	}
}

// TestSplitsAttackHitsTwice 钉住轻度分裂：两次各 60%，合计 120%。
//
// 断言总量而不是「调了两次 ResolveDamage」：调用次数是实现细节，
// 玩家看到的是掉多少血。
func TestSplitsAttackHitsTwice(t *testing.T) {
	w := newAIWorld(t)
	pid := spawnPl(t, w, 10, 10)
	mid := spawnMon(t, w, 10.5, 10)
	m := w.Monsters.Get(mid)
	m.Flags |= world.MonsterFlagSplits

	got := attackOnce(t, w, m, pid)
	// 10 × 0.6 × 2 = 12
	want := 12.0
	if got < want-0.01 || got > want+0.01 {
		t.Fatalf("分裂攻击掉血 %.2f，期望 %.2f（10×0.6×2）", got, want)
	}
}

// TestSplitsDoesNotApplyWhenEnraged 钉住两级互斥的**行为**面。
//
// ApplySpreadToMonsters 保证不会同时置两位，但标志位是外部可写的
// （快照、测试、将来的词条都可能写）。两个位同时存在时必须只有
// 一个生效，否则「双重加强」会以玩家完全无法理解的方式出现。
func TestSplitsDoesNotApplyWhenEnraged(t *testing.T) {
	w := newAIWorld(t)
	pid := spawnPl(t, w, 10, 10)
	mid := spawnMon(t, w, 10.5, 10)
	m := w.Monsters.Get(mid)
	m.Flags |= world.MonsterFlagEnraged | world.MonsterFlagSplits

	got := attackOnce(t, w, m, pid)
	// 狂暴优先：10 × 1.3 = 13，而不是 10 × 0.6 × 2 = 12 或两者叠加
	if got < 12.99 || got > 13.01 {
		t.Fatalf("两位同时存在时掉血 %.2f，期望 13（狂暴优先，不叠加分裂）", got)
	}
}

// TestNormalAttackIsBaseDamage 是对照组：无标志时就是基础伤害。
func TestNormalAttackIsBaseDamage(t *testing.T) {
	w := newAIWorld(t)
	pid := spawnPl(t, w, 10, 10)
	mid := spawnMon(t, w, 10.5, 10)
	m := w.Monsters.Get(mid)

	got := attackOnce(t, w, m, pid)
	if got < 9.99 || got > 10.01 {
		t.Fatalf("普通攻击掉血 %.2f，期望 10", got)
	}
}
