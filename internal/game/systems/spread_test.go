package systems

import (
	"testing"

	"koukoukongjian/internal/game/world"
)

// TestUpdatePartySpreadTogether 钉住全队在一起时是 Together。
func TestUpdatePartySpreadTogether(t *testing.T) {
	w := newSystemsWorld(t, 7)

	spawnPlayerAt(t, w, 20, 20)
	spawnPlayerAt(t, w, 25, 20) // 5 米

	if got := UpdatePartySpread(w); got != world.SpreadTogether {
		t.Fatalf("5 米间距的队形判定为 %v，期望 SpreadTogether", got)
	}
}

// TestUpdatePartySpreadLight 钉住轻度分离。
func TestUpdatePartySpreadLight(t *testing.T) {
	w := newSystemsWorld(t, 7)

	spawnPlayerAt(t, w, 10, 10)
	spawnPlayerAt(t, w, 40, 10) // 30 米

	if got := UpdatePartySpread(w); got != world.SpreadLight {
		t.Fatalf("30 米间距判定为 %v，期望 SpreadLight", got)
	}
}

// TestUpdatePartySpreadExtreme 钉住极端分离。
func TestUpdatePartySpreadExtreme(t *testing.T) {
	w := newSystemsWorld(t, 7)

	spawnPlayerAt(t, w, 10, 10)
	spawnPlayerAt(t, w, 80, 10) // 70 米

	if got := UpdatePartySpread(w); got != world.SpreadExtreme {
		t.Fatalf("70 米间距判定为 %v，期望 SpreadExtreme", got)
	}
}

// TestUpdatePartySpreadUsesFarthestPair 钉住判据是「最远两人」而不是「两两平均」。
//
// 四个人 3 个挤在一起、1 个跑远了，平均值可能很小，但那个跑远的人
// 才是真正脱离队伍的人。用平均值会让「三个人抱团、一个人被围攻」
// 这种最需要增援的场景不触发惩罚。
func TestUpdatePartySpreadUsesFarthestPair(t *testing.T) {
	w := newSystemsWorld(t, 7)

	spawnPlayerAt(t, w, 20, 20)
	spawnPlayerAt(t, w, 21, 20)
	spawnPlayerAt(t, w, 22, 20)
	spawnPlayerAt(t, w, 70, 20) // 50 米外

	if got := UpdatePartySpread(w); got != world.SpreadExtreme {
		t.Fatalf("三人抱团一人 50 米外判定为 %v，期望 SpreadExtreme（最远配对）", got)
	}
}

// TestUpdatePartySpreadIgnoresGhosts 钉住鬼魂不参与判据。
//
// 鬼魂被拴绳拉在活人附近（M5），但它本来就不能战斗，把它算进距离
// 判定等于让「有人死了」本身触发怪物狂暴——那不是设计意图。
func TestUpdatePartySpreadIgnoresGhosts(t *testing.T) {
	w := newSystemsWorld(t, 7)

	spawnPlayerAt(t, w, 20, 20)
	_, dead := spawnPlayerAt(t, w, 100, 20) // 80 米外的鬼魂
	dead.State = world.PlayerGhost

	if got := UpdatePartySpread(w); got != world.SpreadTogether {
		t.Fatalf("唯一活人在场、鬼魂在 80 米外，判定为 %v，期望 SpreadTogether", got)
	}
}

// TestUpdatePartySpreadSinglePlayer 钉住单人时不触发。
//
// 一个人玩的时候没有「分离」可言。若判据是「最远两人距离」，单人时
// 没有配对，必须显式返回 Together 而不是靠默认值碰巧对。
func TestUpdatePartySpreadSinglePlayer(t *testing.T) {
	w := newSystemsWorld(t, 7)

	spawnPlayerAt(t, w, 20, 20)

	if got := UpdatePartySpread(w); got != world.SpreadTogether {
		t.Fatalf("单人时判定为 %v，期望 SpreadTogether", got)
	}
}

// TestUpdatePartySpreadEmptyWorld 钉住零玩家不 panic。
func TestUpdatePartySpreadEmptyWorld(t *testing.T) {
	w := newSystemsWorld(t, 7)

	if got := UpdatePartySpread(w); got != world.SpreadTogether {
		t.Fatalf("空世界判定为 %v，期望 SpreadTogether", got)
	}
}

// TestUpdatePartySpreadNilWorld 钉住 nil 不 panic（room 未初始化时的防御）。
func TestUpdatePartySpreadNilWorld(t *testing.T) {
	if got := UpdatePartySpread(nil); got != world.SpreadTogether {
		t.Fatalf("nil 世界判定为 %v，期望 SpreadTogether", got)
	}
}

// TestUpdatePartySpreadWritesWorld 钉住结果写进 World（AI 要读它）。
func TestUpdatePartySpreadWritesWorld(t *testing.T) {
	w := newSystemsWorld(t, 7)

	spawnPlayerAt(t, w, 10, 10)
	spawnPlayerAt(t, w, 80, 10)

	UpdatePartySpread(w)

	if w.Spread != world.SpreadExtreme {
		t.Fatalf("UpdatePartySpread 后 w.Spread = %v，期望 SpreadExtreme", w.Spread)
	}
}

// TestSpreadLevelString 钉住 String()：日志与快照要用。
func TestSpreadLevelString(t *testing.T) {
	cases := map[world.SpreadLevel]string{
		world.SpreadTogether: "together",
		world.SpreadLight:    "light",
		world.SpreadExtreme:  "extreme",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Fatalf("SpreadLevel(%d).String() = %q，期望 %q", k, got, want)
		}
	}
}

// TestApplySpreadToMonsters 钉住标志位写入。
func TestApplySpreadToMonsters(t *testing.T) {
	w := newSystemsWorld(t, 7)

	spawnPlayerAt(t, w, 10, 10)
	spawnPlayerAt(t, w, 80, 10) // 极端分离
	mid := spawnMonsterAt(t, w, 30, 20)

	UpdatePartySpread(w)
	ApplySpreadToMonsters(w)

	m := w.Monsters.Get(mid)
	if !m.Flags.Has(world.MonsterFlagEnraged) {
		t.Fatalf("极端分离下怪物的 flags = %b，期望含 MonsterFlagEnraged", m.Flags)
	}
	if m.Flags.Has(world.MonsterFlagSplits) {
		t.Fatalf("极端分离下怪物同时带 Splits 位——两级应该互斥而不是叠加")
	}
}

// TestApplySpreadLightSetsSplits 钉住轻度分离写 Splits 位且不带 Enraged。
func TestApplySpreadLightSetsSplits(t *testing.T) {
	w := newSystemsWorld(t, 7)

	spawnPlayerAt(t, w, 10, 10)
	spawnPlayerAt(t, w, 40, 10) // 30 米：轻度
	mid := spawnMonsterAt(t, w, 30, 20)

	UpdatePartySpread(w)
	ApplySpreadToMonsters(w)

	m := w.Monsters.Get(mid)
	if !m.Flags.Has(world.MonsterFlagSplits) {
		t.Fatalf("轻度分离下怪物的 flags = %b，期望含 MonsterFlagSplits", m.Flags)
	}
	if m.Flags.Has(world.MonsterFlagEnraged) {
		t.Fatalf("轻度分离下怪物不该带 Enraged 位")
	}
}

// TestApplySpreadClearsFlags 钉住回到一起后标志被清。
//
// 不清的话怪物会永久狂暴：玩家分开过一次，之后整局所有怪都变强，
// 而玩家完全不知道发生了什么。
func TestApplySpreadClearsFlags(t *testing.T) {
	w := newSystemsWorld(t, 7)

	spawnPlayerAt(t, w, 20, 20)
	spawnPlayerAt(t, w, 22, 20)
	mid := spawnMonsterAt(t, w, 30, 20)
	m := w.Monsters.Get(mid)
	m.Flags |= world.MonsterFlagEnraged // 假装上次是狂暴状态

	UpdatePartySpread(w)
	ApplySpreadToMonsters(w)

	m = w.Monsters.Get(mid)
	if m.Flags.Has(world.MonsterFlagEnraged) {
		t.Fatalf("全队回到一起后怪物仍带 MonsterFlagEnraged")
	}
	if m.Flags.Has(world.MonsterFlagSplits) {
		t.Fatalf("全队回到一起后怪物仍带 MonsterFlagSplits")
	}
}

// TestApplySpreadKeepsOtherFlags 钉住只动两级标志，不碰 Aggroed/Buffable。
//
// 整体覆盖 Flags 会顺手清掉仇恨与可 buff 位，症状是「全队分开一下
// 所有怪都不追人了」——比不做距离惩罚更糟。
func TestApplySpreadKeepsOtherFlags(t *testing.T) {
	w := newSystemsWorld(t, 7)

	spawnPlayerAt(t, w, 10, 10)
	spawnPlayerAt(t, w, 80, 10)
	mid := spawnMonsterAt(t, w, 30, 20)
	m := w.Monsters.Get(mid)
	m.Flags |= world.MonsterFlagAggroed | world.MonsterFlagBuffable

	UpdatePartySpread(w)
	ApplySpreadToMonsters(w)

	m = w.Monsters.Get(mid)
	if !m.Flags.Has(world.MonsterFlagAggroed) {
		t.Fatalf("距离惩罚顺手清掉了 Aggroed 位")
	}
	if !m.Flags.Has(world.MonsterFlagBuffable) {
		t.Fatalf("距离惩罚顺手清掉了 Buffable 位")
	}
	if !m.Flags.Has(world.MonsterFlagEnraged) {
		t.Fatalf("极端分离下应带 Enraged 位")
	}
}

// TestApplySpreadSkipsDeadMonsters 钉住尸体不写标志。
func TestApplySpreadSkipsDeadMonsters(t *testing.T) {
	w := newSystemsWorld(t, 7)

	spawnPlayerAt(t, w, 10, 10)
	spawnPlayerAt(t, w, 80, 10)
	mid := spawnMonsterAt(t, w, 30, 20)
	m := w.Monsters.Get(mid)
	m.Flags |= world.MonsterFlagDead

	UpdatePartySpread(w)
	ApplySpreadToMonsters(w)

	m = w.Monsters.Get(mid)
	if m.Flags.Has(world.MonsterFlagEnraged) {
		t.Fatalf("尸体被写上了 Enraged 位")
	}
}
