package systems

import (
	"testing"
)

func TestReapRemovesDeadMonsters(t *testing.T) {
	w := newSystemsWorld(t, 6)
	mID, m := w.Monsters.Spawn()
	m.HP = 0
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

	first := Reap(w)
	second := Reap(w)

	if len(first) != 1 {
		t.Fatalf("第一次回收产出 %d 条击杀，期望 1", len(first))
	}
	if len(second) != 0 {
		t.Fatalf("第二次回收又产出 %d 条击杀，重复计数会让分账算错", len(second))
	}
}
