package systems

import (
	"math"
	"testing"

	"koukoukongjian/internal/game/world"
)

// 击杀者拿全额金币。
//
// 为什么给击杀者全额而不是队伍平分：金币是主经济来源（spec L77），而
// 分账规则属于 M6 的"按据点贡献分账"（spec L134）。M5 先给最简单的一条，
// 免得在 M5 里做半套分账——半套分账比没有更难改，因为数值调参时会分不清
// "手感不对"是分账公式错了还是金币本身给多了。
func TestGrantKillGoldToKiller(t *testing.T) {
	w := newSystemsWorld(t, 7)

	pid, p := w.Players.Spawn()
	p.State = world.PlayerAlive
	p.HP = 100
	p.MaxHP = 100

	mid, m := w.Monsters.Spawn()
	m.HP = 1
	m.MaxHP = 50

	// 模拟 Reap 前的那一发致死伤害：Src=pid
	ev := w.ResolveDamage(pid, mid, world.EntityClassMonster, 10, world.ElementNone, world.DamageTags{})
	if !ev.IsKill {
		t.Fatalf("前提失效：这一发没有打死怪物")
	}

	if gold := GrantKillGold(w, ev.Src, ev.Dst); gold == 0 {
		t.Fatalf("击杀没有给金币")
	}
	if p.Gold != GoldPerKill {
		t.Fatalf("击杀后 Gold = %d，期望 %d", p.Gold, GoldPerKill)
	}
}

// 没有击杀者时不给金币（防止 Src 失效时奖励落到 0 号槽位上）。
//
// Src=InvalidID 的击杀在 M5 还不存在（中毒/环境伤害是 M6+），但 Reap
// 自己产出的击杀事件恰好就是 Src=InvalidID（它只知道"谁死了"）。
// 若 GrantKillGold 对 InvalidID 也走 Players.Get，会拿到 nil 而不崩，
// 但更糟的情形是 Get(InvalidID) 返回 0 号槽位——池保留 0 号给 InvalidID
// 但 Get 的实现需要确认。测试把这条边界钉住。
func TestGrantKillGoldIgnoresInvalidKiller(t *testing.T) {
	w := newSystemsWorld(t, 7)

	pid, p := w.Players.Spawn()
	p.State = world.PlayerAlive
	p.HP = 100
	p.MaxHP = 100
	_ = pid

	mid, _ := w.Monsters.Spawn()

	if gold := GrantKillGold(w, world.InvalidID, mid); gold != 0 {
		t.Fatalf("Src=InvalidID 时给了 %d 金币，期望 0", gold)
	}
	if p.Gold != 0 {
		t.Fatalf("Src=InvalidID 时玩家 Gold = %d，期望 0", p.Gold)
	}
}

// 溢出保护：金币累加不能绕过 uint32 上限。
//
// 这一条不是洁癖：M4 的 applyPickup 已经在金币累加处写过一次钳位
// （pickup.go:195），若这里不写就是同一个坑两处不同答案，而"金币变成
// 一个很小的数"只在长局才会出现，测试跑不出来。
func TestGrantKillGoldSaturates(t *testing.T) {
	w := newSystemsWorld(t, 7)

	pid, p := w.Players.Spawn()
	p.State = world.PlayerAlive
	p.Gold = math.MaxUint32 - 1

	mid, _ := w.Monsters.Spawn()

	GrantKillGold(w, pid, mid)

	if p.Gold != math.MaxUint32 {
		t.Fatalf("Gold = %d，期望钳到 MaxUint32", p.Gold)
	}
}

// 鬼魂不给金币：它已经不是经济参与者了。
//
// 击杀者是鬼魂的情形 M5 不该出现（鬼魂不能开火），但若将来有"鬼魂搬运
// 触发击杀"这类路径，金币发给鬼魂就永远是沉没成本。
func TestGrantKillGoldSkipsGhost(t *testing.T) {
	w := newSystemsWorld(t, 7)

	pid, p := w.Players.Spawn()
	p.State = world.PlayerGhost

	mid, _ := w.Monsters.Spawn()

	if gold := GrantKillGold(w, pid, mid); gold != 0 {
		t.Fatalf("鬼魂击杀给了 %d 金币，期望 0", gold)
	}
	if p.Gold != 0 {
		t.Fatalf("鬼魂 Gold = %d，期望 0", p.Gold)
	}
}
