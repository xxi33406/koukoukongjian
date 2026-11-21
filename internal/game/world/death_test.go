package world

import "testing"

// 打死后必须变成鬼魂，而不是只把血清零。
//
// 守的是什么：玩家侧的死亡判据在 M5 之前是"HP<=0 什么都不做"，
// 而全仓 9 处系统都以 State != PlayerAlive 作为"不参与战斗"的判据。
// 只清血不改 State，症状是"玩家血条空了还能走能打，怪物也还在追他"——
// 看起来像 AI 有 bug，实际是死亡从来没有发生过。
func TestPlayerDiesBecomesGhost(t *testing.T) {
	w := newWorldForTest()
	id, p := w.Players.Spawn()
	p.State = PlayerAlive
	p.HP = 10
	p.MaxHP = 100

	ev := w.ResolveDamage(InvalidID, id, EntityClassPlayer, 50, ElementNone, DamageTags{}, InvalidID)

	if !ev.IsKill {
		t.Fatalf("致死伤害没有置 IsKill")
	}
	if p.State != PlayerGhost {
		t.Fatalf("死后 State = %d，期望 PlayerGhost(%d)", p.State, PlayerGhost)
	}
	if p.HP != 0 {
		t.Fatalf("死后 HP = %v，期望 0", p.HP)
	}
}

// 鬼魂不该再吃伤害：鬼魂永不受击（M5 规格决定 1）。
//
// 这条与上面的 TestPlayerDiesBecomesGhost 是一对：只改 State 不改判据，
// 鬼魂会被后续子弹继续打（HP 已经 0，再减就变负数），而 IsKill 会重复置位，
// 下游 Reap/grantKillExp 会重复分账。
func TestGhostTakesNoDamage(t *testing.T) {
	w := newWorldForTest()
	id, p := w.Players.Spawn()
	p.State = PlayerGhost
	p.HP = 0

	ev := w.ResolveDamage(InvalidID, id, EntityClassPlayer, 50, ElementNone, DamageTags{}, InvalidID)

	if ev.Amount != 0 {
		t.Fatalf("鬼魂吃到了 %v 点伤害，期望 0", ev.Amount)
	}
	if ev.IsKill {
		t.Fatalf("鬼魂被重复判定为击杀")
	}
	if p.HP != 0 {
		t.Fatalf("鬼魂 HP = %v，期望 0", p.HP)
	}
}

// Kill 必须清掉一切依赖"活着"的状态。
//
// 逐项断言而不是只测 State：每一条被漏掉的字段都有独立症状——
// 清 Vel 漏了→鬼魂带着上一帧的速度滑行；清引导漏了→鬼魂头上挂着开背包
// 进度条；清限时增益漏了→死了反而无敌加速跑。
func TestKillClearsLiveState(t *testing.T) {
	var p PlayerState
	p.State = PlayerAlive
	p.HP = 37
	p.MaxHP = 100
	p.Vel = V(3, 4)
	p.MoveIntent = V(1, 0)
	p.FireCooldown = 0.13
	p.PrevAttackHeld = true
	p.Flags = PlayerFlagBusy | PlayerFlagInvulnerable
	p.InventoryOpenAt = 999
	p.ReviveAt = 555
	p.InvulnLeft = 1.5
	p.SpeedBuffLeft = 2.0
	p.SpeedBuffMul = 1.8
	// 技能冷却与背包内容必须保留
	p.SkillCooldown = [2]float64{3.2, 0}
	p.SkillActive = [2]uint16{5, 0}

	p.Kill()

	if p.State != PlayerGhost {
		t.Fatalf("State = %d，期望 PlayerGhost", p.State)
	}
	if p.HP != 0 {
		t.Fatalf("HP = %v，期望 0", p.HP)
	}
	if p.Vel != (Vec2{}) || p.MoveIntent != (Vec2{}) {
		t.Fatalf("速度没清干净：Vel=%v MoveIntent=%v", p.Vel, p.MoveIntent)
	}
	if p.FireCooldown != 0 || p.PrevAttackHeld {
		t.Fatalf("开火状态没清干净：cooldown=%v prevHeld=%v", p.FireCooldown, p.PrevAttackHeld)
	}
	if p.Flags != 0 {
		t.Fatalf("Flags = %b，期望全部清空（Busy 与 Invulnerable 都该清）", p.Flags)
	}
	if p.InventoryOpenAt != 0 || p.ReviveAt != 0 {
		t.Fatalf("引导字段没清干净：inv=%d revive=%d", p.InventoryOpenAt, p.ReviveAt)
	}
	if p.InvulnLeft != 0 || p.SpeedBuffLeft != 0 || p.SpeedBuffMul != 0 {
		t.Fatalf("限时增益没清干净：inv=%v speedLeft=%v mul=%v",
			p.InvulnLeft, p.SpeedBuffLeft, p.SpeedBuffMul)
	}
	// 保留项：清零等于用死亡刷新技能，会被"冲进怪堆自杀放爆发"滥用
	if p.SkillCooldown[0] != 3.2 {
		t.Fatalf("技能冷却被清了：%v，期望保留 3.2", p.SkillCooldown[0])
	}
	if p.SkillActive[0] != 5 {
		t.Fatalf("技能槽被清了：%v，期望保留 5", p.SkillActive[0])
	}
}
