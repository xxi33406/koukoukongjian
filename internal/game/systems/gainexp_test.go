package systems

import (
	"testing"

	"koukoukongjian/internal/game/world"
)

// spawnPlayerAt 生成一个「已加入房间」的存活玩家。
//
// 必须手动补 Level/ExpToNext：Pool.Spawn 按契约 `var zero T` 清零整个槽位
// （pool.go），刚 Spawn 出来的玩家是 0 级。出生值在真实路径上由
// internal/room/net.go 的 handleJoin 写入，这里是它在测试里的等价物。
// ExpToNext 取 world 的曲线首级值而不是写死 20，跟 handleJoin 同一口径。
func spawnPlayerAt(t *testing.T, w *world.World, x, y float64) (world.EntityID, *world.PlayerState) {
	t.Helper()
	id, p := w.Players.Spawn()
	if p == nil {
		t.Fatalf("玩家池已满，Spawn 返回 nil")
	}
	p.Pos = world.V(x, y)
	p.State = world.PlayerAlive
	p.Level = 1
	p.Exp = 0
	p.ExpToNext = uint32(w.Prog.ExpBase)
	p.MaxHP = 100
	p.HP = 100
	return id, p
}

func TestExpForLevel(t *testing.T) {
	w := newSystemsWorld(t, 1)
	cases := []struct {
		level uint8
		want  uint32
		why   string
	}{
		{1, 20, "首级就是 ExpBase，没有乘数"},
		{2, 27, "20×1.35 = 27.0"},
		{3, 36, "20×1.35² = 36.45，四舍五入到 36"},
	}
	for _, c := range cases {
		if got := ExpForLevel(w.Prog, c.level); got != c.want {
			t.Errorf("ExpForLevel(%d) = %d，期望 %d（%s）", c.level, got, c.want, c.why)
		}
	}

	// 单调递增：曲线必须越来越贵，否则「频率递减」的设计（spec L105）不成立。
	prev := ExpForLevel(w.Prog, 1)
	for lv := uint8(2); lv <= 20; lv++ {
		got := ExpForLevel(w.Prog, lv)
		if got <= prev {
			t.Fatalf("level %d 需要 %d 点，不高于 level %d 的 %d——曲线不再递增",
				lv, got, lv-1, prev)
		}
		prev = got
	}

	// 下限保护：level 0（未走 handleJoin 的玩家）不能让 pow 的负指数
	// 把结果压成 0，那会让 AddExp 的 for 循环拿 0 当门槛。
	if got := ExpForLevel(w.Prog, 0); got == 0 {
		t.Errorf("ExpForLevel(0) = 0，期望至少 1")
	}
}

func TestAddExpLevelsUp(t *testing.T) {
	w := newSystemsWorld(t, 1)
	_, p := spawnPlayerAt(t, w, 0, 0)

	// 20 点正好够 1→2（ExpBase=20）。
	if leveled := AddExp(w, p, 20); !leveled {
		t.Fatalf("给 20 点经验应升到 2 级，实际未升级")
	}
	if p.Level != 2 {
		t.Errorf("Level = %d，期望 2", p.Level)
	}
	if p.Exp != 0 {
		t.Errorf("Exp = %d，期望 0（20 点全被 1 级门槛 20 扣掉）", p.Exp)
	}
	if want := ExpForLevel(w.Prog, 2); p.ExpToNext != want {
		t.Errorf("ExpToNext = %d，期望 %d", p.ExpToNext, want)
	}
	if p.ExpToNext <= 20 {
		t.Errorf("ExpToNext = %d，期望比 1 级的 20 更大", p.ExpToNext)
	}
	if p.MaxHP != 110 {
		t.Errorf("MaxHP = %v，期望 110（100+%v）", p.MaxHP, LevelUpMaxHPGain)
	}
	if p.HP != 110 {
		t.Errorf("HP = %v，期望 110（升级加满增量）", p.HP)
	}
}

func TestAddExpMultipleLevelsAtOnce(t *testing.T) {
	w := newSystemsWorld(t, 1)
	_, p := spawnPlayerAt(t, w, 0, 0)

	// 一次给 200 点。门槛序列 20/27/36/49/66/90…，累计到 6 级要
	// 20+27+36+49+66 = 198 ≤ 200，再上一级要 90 点不够 —— 停在 6 级，剩 2 点。
	if leveled := AddExp(w, p, 200); !leveled {
		t.Fatalf("给 200 点经验应升级，实际未升级")
	}
	if p.Level != 6 {
		t.Fatalf("Level = %d，期望 6（200 点能连升 5 级）", p.Level)
	}
	if p.Exp != 2 {
		t.Errorf("Exp = %d，期望 2（200-198）", p.Exp)
	}
	if want := ExpForLevel(w.Prog, 6); p.ExpToNext != want {
		t.Errorf("ExpToNext = %d，期望 %d", p.ExpToNext, want)
	}
	if want := 100 + 5*LevelUpMaxHPGain; p.MaxHP != want {
		t.Errorf("MaxHP = %v，期望 %v（升了 5 级）", p.MaxHP, want)
	}
}

func TestAddExpAtMaxLevel(t *testing.T) {
	w := newSystemsWorld(t, 1)
	_, p := spawnPlayerAt(t, w, 0, 0)
	p.Level = uint8(w.Prog.MaxLevel)
	p.Exp = 0
	p.ExpToNext = 10

	if leveled := AddExp(w, p, 1000); leveled {
		t.Fatalf("满级后给经验不应再升级")
	}
	if p.Exp != 0 {
		t.Errorf("Exp = %d，期望 0——满级后经验不再增长", p.Exp)
	}
	if p.Level != uint8(w.Prog.MaxLevel) {
		t.Errorf("Level = %d，期望停在 %d", p.Level, w.Prog.MaxLevel)
	}
}

func TestGrantKillExpSharedWithinRadius(t *testing.T) {
	w := newSystemsWorld(t, 1)
	// 三个玩家散布在共享半径内，击杀点在原点附近。
	ps := make([]*world.PlayerState, 3)
	_, ps[0] = spawnPlayerAt(t, w, 0, 0)
	_, ps[1] = spawnPlayerAt(t, w, 5, 0)
	_, ps[2] = spawnPlayerAt(t, w, 0, 15)

	leveled := GrantKillExp(w, world.V(0, 0), 30)
	if len(leveled) != 0 {
		t.Fatalf("每人分 10 点经验不该升级，实际升级 %d 人", len(leveled))
	}
	for i, p := range ps {
		if p.Exp != 10 {
			t.Errorf("玩家 %d 的 Exp = %d，期望 10（30 点三人平分）", i, p.Exp)
		}
	}
}

func TestGrantKillExpNotSharedBeyondRadius(t *testing.T) {
	w := newSystemsWorld(t, 1)
	_, near := spawnPlayerAt(t, w, 0, 0)
	_, far := spawnPlayerAt(t, w, ExpShareRadius+1, 0)

	GrantKillExp(w, world.V(0, 0), 10)
	if near.Exp == 0 {
		t.Errorf("半径内的玩家 Exp = 0，期望拿到经验")
	}
	if far.Exp != 0 {
		t.Errorf("半径外 %v m 的玩家 Exp = %d，期望 0（共享半径 %v m）",
			ExpShareRadius+1, far.Exp, ExpShareRadius)
	}
}

func TestGrantKillExpSkipsDeadPlayers(t *testing.T) {
	w := newSystemsWorld(t, 1)
	_, alive := spawnPlayerAt(t, w, 0, 0)
	_, downed := spawnPlayerAt(t, w, 1, 0)
	_, ghost := spawnPlayerAt(t, w, 2, 0)
	downed.State = world.PlayerDowned
	ghost.State = world.PlayerGhost

	GrantKillExp(w, world.V(0, 0), 10)
	if alive.Exp == 0 {
		t.Errorf("存活玩家 Exp = 0，期望拿到经验")
	}
	if downed.Exp != 0 {
		t.Errorf("倒地玩家 Exp = %d，期望 0", downed.Exp)
	}
	if ghost.Exp != 0 {
		t.Errorf("鬼魂玩家 Exp = %d，期望 0", ghost.Exp)
	}
}

func TestGrantKillExpSplitsEvenly(t *testing.T) {
	w := newSystemsWorld(t, 1)
	_, a := spawnPlayerAt(t, w, 0, 0)
	_, b := spawnPlayerAt(t, w, 3, 0)

	GrantKillExp(w, world.V(0, 0), 8)
	if a.Exp != 4 || b.Exp != 4 {
		t.Errorf("两人分 8 点经验，实际 %d / %d，期望 4 / 4", a.Exp, b.Exp)
	}

	// 平分除不尽时每人至少 1 点：1 点经验分给 3 人不能变成「谁都不涨」。
	w2 := newSystemsWorld(t, 2)
	_, c := spawnPlayerAt(t, w2, 0, 0)
	_, d := spawnPlayerAt(t, w2, 1, 0)
	_, e := spawnPlayerAt(t, w2, 2, 0)
	GrantKillExp(w2, world.V(0, 0), 1)
	if c.Exp == 0 || d.Exp == 0 || e.Exp == 0 {
		t.Errorf("1 点经验分 3 人，实际 %d / %d / %d，期望每人至少 1",
			c.Exp, d.Exp, e.Exp)
	}
}

func TestLevelUpClampsHPToMaxHP(t *testing.T) {
	w := newSystemsWorld(t, 1)
	_, p := spawnPlayerAt(t, w, 0, 0) // HP=100, MaxHP=100

	AddExp(w, p, 20)
	if p.HP != p.MaxHP {
		t.Fatalf("满血升级后 HP = %v、MaxHP = %v，期望相等（110）", p.HP, p.MaxHP)
	}

	// 越界态：HP 已经高于 MaxHP（治疗溢出/数值异常）。升级时 +10 会把它
	// 推得更高，必须钳到新上限而不是保留溢出量。
	_, q := spawnPlayerAt(t, w, 0, 0)
	q.HP = 105 // 超过 MaxHP=100 的非法值
	AddExp(w, q, 20)
	if q.MaxHP != 110 {
		t.Fatalf("MaxHP = %v，期望 110", q.MaxHP)
	}
	if q.HP != 110 {
		t.Errorf("HP = %v，期望钳到新上限 110（不是 115）", q.HP)
	}
}
