package world

import "testing"

func TestPlayerFireFieldsAreZeroed(t *testing.T) {
	var p PlayerState
	if p.FireCooldown != 0 {
		t.Fatalf("FireCooldown 零值 = %v，必须为 0（出生即可开火）", p.FireCooldown)
	}
	if p.PrevAttackHeld {
		t.Fatalf("PrevAttackHeld 零值 = true，必须为 false")
	}
}

func TestPoolSpawnClearsFireState(t *testing.T) {
	// 复用槽位必须清零：否则新玩家继承上一个玩家的冷却，
	// 表现为"刚进场打不出子弹"，而且只有换人时才偶发。
	p := NewPool[PlayerState](2, 2)
	id, a := p.Spawn()
	a.FireCooldown = 0.5
	a.PrevAttackHeld = true
	p.Kill(id)

	_, b := p.Spawn()
	if b.FireCooldown != 0 || b.PrevAttackHeld {
		t.Fatalf("复用槽位未清零：cooldown=%v prev=%v", b.FireCooldown, b.PrevAttackHeld)
	}
}

// TestMonsterFlagDeadIsTheOnlyDeathCriterion 钉住"M4 起 HP<=0 不再等于死亡"：
// 把 HP 拉回正数必须不能让怪物复活，M5 的祭坛复活会依赖这条。
func TestMonsterFlagDeadIsTheOnlyDeathCriterion(t *testing.T) {
	w := newWorldForTest()
	id, m := w.Monsters.Spawn()
	m.HP = 0
	m.Flags |= MonsterFlagDead // 尚未实现 → 编译失败是预期红灯

	if !m.Flags.Has(MonsterFlagDead) {
		t.Fatal("MonsterFlagDead 未生效")
	}
	// 治疗把 HP 拉回来，但标志还在 → 仍然是死的
	m.HP = 50
	if !m.IsDead() {
		t.Fatal("HP 回到正数后 IsDead 仍应为 true——判据是显式标志，不是 HP")
	}
	// 清标志 → 复活
	m.Flags &^= MonsterFlagDead
	if m.IsDead() {
		t.Fatal("清掉 MonsterFlagDead 后 IsDead 应为 false")
	}
	_ = id
}

// TestMonsterFlagDeadDoesNotAliasOtherFlags 钉住 MonsterFlagDead 的位不与
// 已有的仇恨/狂暴标记撞值：撞值会让"进入战斗"的怪被当成尸体。
func TestMonsterFlagDeadDoesNotAliasOtherFlags(t *testing.T) {
	others := []MonsterFlags{
		MonsterFlagAggroed,
		MonsterFlagEnraged,
		MonsterFlagSplits,
		MonsterFlagBuffable,
	}
	m := &MonsterState{Flags: MonsterFlagAggroed | MonsterFlagEnraged | MonsterFlagSplits | MonsterFlagBuffable}
	if m.IsDead() {
		t.Fatal("只置了仇恨/狂暴等标记，IsDead 却是 true——MonsterFlagDead 与它们撞位了")
	}
	for _, f := range others {
		if MonsterFlagDead == f {
			t.Fatalf("MonsterFlagDead 与已有标记 %d 撞值", f)
		}
	}
}
