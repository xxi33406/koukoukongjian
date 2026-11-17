package systems

import (
	"testing"

	"koukoukongjian/internal/game/world"
)

func TestReapRemovesDeadMonsters(t *testing.T) {
	w := newSystemsWorld(t, 6)
	mID, m := w.Monsters.Spawn()
	m.HP = 0
	m.Flags |= world.MonsterFlagDead // 死亡态 = HP 归零 + 标志置位（M4 起判据是标志）
	m.MaxHP = 50

	kills := Reap(w)

	if w.Monsters.Alive(mID) {
		t.Fatal("HP=0 的怪没有被回收")
	}
	if len(kills) != 1 || kills[0].Dst != mID {
		t.Fatalf("击杀事件 = %+v，期望 1 条指向 %v", kills, mID)
	}
}

func TestReapKeepsAliveMonsters(t *testing.T) {
	w := newSystemsWorld(t, 6)
	mID, m := w.Monsters.Spawn()
	m.HP = 1
	m.MaxHP = 50

	if kills := Reap(w); len(kills) != 0 {
		t.Fatalf("活着的怪被回收了：%+v", kills)
	}
	if !w.Monsters.Alive(mID) {
		t.Fatal("活着的怪被回收了")
	}
}

func TestReapBumpsGeneration(t *testing.T) {
	// 回收后世代必须 +1，否则客户端会拿旧 ID 引用新实体，
	// 表现为"新的怪一出现就是残血的"。
	w := newSystemsWorld(t, 6)
	mID, m := w.Monsters.Spawn()
	oldGen := mID.Gen()
	m.HP = 0
	m.Flags |= world.MonsterFlagDead
	Reap(w)

	newID, _ := w.Monsters.Spawn()
	if newID.Index() != mID.Index() {
		t.Skip("槽位未复用，本测试前提不成立")
	}
	if newID.Gen() == oldGen {
		t.Fatalf("复用槽位后世代仍是 %d，客户端无法区分新旧实体", oldGen)
	}
}

func TestReapIsIdempotent(t *testing.T) {
	// 连续调用两次不能重复产出击杀事件。
	w := newSystemsWorld(t, 6)
	_, m := w.Monsters.Spawn()
	m.HP = 0
	m.Flags |= world.MonsterFlagDead

	first := Reap(w)
	second := Reap(w)

	if len(first) != 1 {
		t.Fatalf("第一次回收产出 %d 条击杀，期望 1", len(first))
	}
	if len(second) != 0 {
		t.Fatalf("第二次回收又产出 %d 条击杀，重复计数会让分账算错", len(second))
	}
}

// TestReapReapsHealedCorpse 钉住"判据是标志而不是 HP"：治疗把 HP 拉回
// 正数而标志仍在的怪必须被回收——这条和下面 TestReapSparesRevivedMonster
// 是两个方向，合起来才说明判据只认标志。
func TestReapReapsHealedCorpse(t *testing.T) {
	w := newSystemsWorld(t, 6)
	mID, m := w.Monsters.Spawn()
	m.HP = 50 // 治疗把血拉满
	m.Flags |= world.MonsterFlagDead

	kills := Reap(w)

	if w.Monsters.Alive(mID) {
		t.Fatal("HP=50 但标志为死的怪没被回收")
	}
	if len(kills) != 1 || kills[0].Dst != mID {
		t.Fatalf("击杀事件 = %+v，期望 1 条指向 %v", kills, mID)
	}
}

// TestReapSparesRevivedMonster 是反方向：标志被清掉（M5 祭坛复活）
// 就算 HP 还是 0 也不能回收，否则复活出来的怪会被当尸体删掉。
func TestReapSparesRevivedMonster(t *testing.T) {
	w := newSystemsWorld(t, 6)
	mID, m := w.Monsters.Spawn()
	m.HP = 0 // 血还没补回来
	m.Flags &^= world.MonsterFlagDead

	kills := Reap(w)

	if !w.Monsters.Alive(mID) {
		t.Fatal("标志已清（已复活）的怪被当成尸体回收了")
	}
	if len(kills) != 0 {
		t.Fatalf("复活中的怪产出了 %d 条击杀事件，期望 0", len(kills))
	}
}
