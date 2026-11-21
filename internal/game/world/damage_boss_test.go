package world

import (
	"testing"

	"koukoukongjian/internal/config"
)

// spawnBossMonster 摆一只带 Boss 标志的怪物。
func spawnBossMonster(t *testing.T, w *World, hp float64) EntityID {
	t.Helper()
	id, m := w.Monsters.Spawn()
	if id == InvalidID {
		t.Fatal("怪物池满了")
	}
	m.HP, m.MaxHP = hp, hp
	m.Flags |= MonsterFlagBoss
	return id
}

// spawnDamagePlayer 摆一个玩家（damage_test.go 里没有现成辅助）。
func spawnDamagePlayer(t *testing.T, w *World) (EntityID, *PlayerState) {
	t.Helper()
	id, p := w.Players.Spawn()
	if id == InvalidID {
		t.Fatal("玩家池满了")
	}
	p.HP, p.MaxHP = 100, 100
	p.State = PlayerAlive
	return id, p
}

// TestBossTakesExtraFireDamage 钉住核心：火伤 × FireVulnMul。
func TestBossTakesExtraFireDamage(t *testing.T) {
	w := newWorldForTest()
	w.Boss = &config.Boss{HP: 800, FireVulnMul: 1.5}
	id := spawnBossMonster(t, w, 800)

	ev := w.ResolveDamage(InvalidID, id, EntityClassMonster, 100, ElementFire, DamageTags{}, InvalidID)
	if ev.Amount < 149.9 || ev.Amount > 150.1 {
		t.Fatalf("火伤打 Boss 结算 %.2f，期望 150（100 × 1.5）", ev.Amount)
	}
	m := w.Monsters.Get(id)
	if m.HP < 649.9 || m.HP > 650.1 {
		t.Fatalf("Boss 剩余血量 %.2f，期望 650", m.HP)
	}
}

// TestBossFireVulnOnlyForFire 钉住别的元素不吃倍率。
func TestBossFireVulnOnlyForFire(t *testing.T) {
	w := newWorldForTest()
	w.Boss = &config.Boss{HP: 800, FireVulnMul: 1.5}
	id := spawnBossMonster(t, w, 800)

	ev := w.ResolveDamage(InvalidID, id, EntityClassMonster, 100, ElementWater, DamageTags{}, InvalidID)
	if ev.Amount < 99.9 || ev.Amount > 100.1 {
		t.Fatalf("水伤打 Boss 结算 %.2f，期望 100（不享受火易伤）", ev.Amount)
	}
}

// TestNormalMonsterNoFireVuln 钉住普通怪不吃倍率。
func TestNormalMonsterNoFireVuln(t *testing.T) {
	w := newWorldForTest()
	w.Boss = &config.Boss{HP: 800, FireVulnMul: 1.5}
	id, m := w.Monsters.Spawn()
	m.HP, m.MaxHP = 500, 500 // 不带 Boss 标志

	ev := w.ResolveDamage(InvalidID, id, EntityClassMonster, 100, ElementFire, DamageTags{}, InvalidID)
	if ev.Amount < 99.9 || ev.Amount > 100.1 {
		t.Fatalf("火伤打普通怪结算 %.2f，期望 100（只有 Boss 易伤）", ev.Amount)
	}
}

// TestFireVulnNilConfigSafe 钉住没配 Boss 时不 panic 也不加倍。
func TestFireVulnNilConfigSafe(t *testing.T) {
	w := newWorldForTest()
	w.Boss = nil
	id := spawnBossMonster(t, w, 800)

	ev := w.ResolveDamage(InvalidID, id, EntityClassMonster, 100, ElementFire, DamageTags{}, InvalidID)
	if ev.Amount < 99.9 || ev.Amount > 100.1 {
		t.Fatalf("无 Boss 配置时火伤结算 %.2f，期望 100", ev.Amount)
	}
}

// TestFireVulnBelowOneIgnored 钉住配置写成 <1 时不减伤。
//
// 易伤语义上不该减伤：配置写错 0.5 会变成「Boss 抗火」，与词条名相反
// 且没人会想到去查这里。减伤将来走护甲/词条那一路。
func TestFireVulnBelowOneIgnored(t *testing.T) {
	w := newWorldForTest()
	w.Boss = &config.Boss{HP: 800, FireVulnMul: 0.5}
	id := spawnBossMonster(t, w, 800)

	ev := w.ResolveDamage(InvalidID, id, EntityClassMonster, 100, ElementFire, DamageTags{}, InvalidID)
	if ev.Amount < 99.9 || ev.Amount > 100.1 {
		t.Fatalf("FireVulnMul=0.5 时火伤结算 %.2f，期望 100（易伤只放大不缩小）", ev.Amount)
	}
}

// TestFireVulnAppliesToKillAccounting 钉住倍率进了事件而不是只改血量。
//
// 这条是变异逼出来的：只断言「血量少了对的数」时，若实现改了扣血量却
// 忘了改 dmg，下游的分账与统计就拿到未加倍的数——玩家看到 Boss 掉血更快，
// 结算面板却显示原来的贡献。
func TestFireVulnAppliesToKillAccounting(t *testing.T) {
	w := newWorldForTest()
	w.Boss = &config.Boss{HP: 100, FireVulnMul: 2.0}
	pid, p := spawnDamagePlayer(t, w)
	id := spawnBossMonster(t, w, 100)

	ev := w.ResolveDamage(pid, id, EntityClassMonster, 50, ElementFire, DamageTags{}, InvalidID)
	if !ev.IsKill {
		t.Fatalf("50 × 2.0 = 100 应当打死 100 血的 Boss，IsKill = false（Amount = %.2f）", ev.Amount)
	}
	if p.Contribute < 99.9 || p.Contribute > 100.1 {
		t.Fatalf("击杀者贡献 = %.2f，期望 100（按加倍后记账）", p.Contribute)
	}
}
