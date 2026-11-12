package systems

import (
	"testing"

	"koukoukongjian/internal/game/world"
)

func TestApplyHitsRoutesEveryHitThroughResolveDamage(t *testing.T) {
	w := newSystemsWorld(t, 5)
	mID, m := w.Monsters.Spawn()
	m.HP = 50
	m.MaxHP = 50

	hits := []Hit{{Target: mID, Src: world.InvalidID, Damage: 12, Element: world.ElementNone}}
	events := ApplyHits(w, hits)

	if m.HP != 38 {
		t.Fatalf("HP = %v，期望 38", m.HP)
	}
	if len(events) != 1 || events[0].Amount != 12 {
		t.Fatalf("事件 = %+v，期望 1 条 12 伤害", events)
	}
}

func TestApplyHitsKillsAndReapsBullet(t *testing.T) {
	// 命中后子弹必须消失（M2 没有穿透），否则它会沿轨迹继续命中后面所有怪。
	w := newSystemsWorld(t, 5)
	mID, m := w.Monsters.Spawn()
	m.HP = 5
	m.MaxHP = 50
	bID, _ := w.Bullets.Spawn()

	hits := []Hit{{Bullet: bID, Target: mID, Damage: 10, Element: world.ElementNone}}
	events := ApplyHits(w, hits)

	if w.Bullets.Alive(bID) {
		t.Fatal("命中的子弹没有被回收，会继续飞行")
	}
	if len(events) != 1 || !events[0].IsKill {
		t.Fatalf("事件 = %+v，期望 1 条击杀事件", events)
	}
}

func TestApplyHitsLeavesMonsterMarkedDead(t *testing.T) {
	w := newSystemsWorld(t, 5)
	mID, m := w.Monsters.Spawn()
	m.HP = 5
	m.MaxHP = 50

	ApplyHits(w, []Hit{{Target: mID, Damage: 10, Element: world.ElementNone}})

	if !w.Monsters.Alive(mID) {
		t.Fatal("ApplyHits 直接回收了怪物，回收必须留给 Reap")
	}
	if m.HP > 0 {
		t.Fatalf("HP = %v，死亡应钳到 0", m.HP)
	}
}
