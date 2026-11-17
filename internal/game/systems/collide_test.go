package systems

import (
	"testing"

	"koukoukongjian/internal/game/world"
)

// spawnMonsterAt 这个辅助在 Task 3 的 aim_test.go 里已经建好了（同包可见），
// 这里直接复用，不再写第二份——两份"摆怪"的写法迟早会在半径或血量上分叉，
// 而分叉的表现是"某些用例偶尔打不中"。

func TestCollideFindsDirectHit(t *testing.T) {
	// 子弹从 (0,0) 走到 (1,0)，怪物在 (0.5,0) 半径 0.4。
	// 点判定：终点 (1,0) 距怪物 0.5 > 0.4，漏；扫掠：命中。
	w := newSystemsWorld(t, 3)
	mID := spawnMonsterAt(t, w, 0.5, 0)
	bID, b := w.Bullets.Spawn()
	b.PrevPos = world.V(0, 0)
	b.Pos = world.V(1, 0)
	b.Radius = 0.1
	b.OwnerID = 1

	hits := Collide(w)

	if len(hits) != 1 {
		t.Fatalf("命中数 = %d，期望 1（点判定会漏掉这个擦过的情况）", len(hits))
	}
	if hits[0].Target != mID {
		t.Fatalf("命中目标 = %v，期望怪物 %v", hits[0].Target, mID)
	}
	if hits[0].Bullet != bID {
		t.Fatalf("命中子弹 = %v，期望 %v", hits[0].Bullet, bID)
	}
}

func TestCollideMissesWhenOutOfRange(t *testing.T) {
	w := newSystemsWorld(t, 3)
	spawnMonsterAt(t, w, 5, 5)
	_, b := w.Bullets.Spawn()
	b.PrevPos = world.V(0, 0)
	b.Pos = world.V(1, 0)
	b.Radius = 0.1

	if hits := Collide(w); len(hits) != 0 {
		t.Fatalf("命中数 = %d，期望 0", len(hits))
	}
}

func TestCollideUsesSweepNotPoint(t *testing.T) {
	// 关键回归：把扫掠判定换成点判定后，这个测试必须失败。
	// 高速子弹一子步走 0.333 m，怪物直径 0.8 m，正好在临界。
	w := newSystemsWorld(t, 3)
	spawnMonsterAt(t, w, 0.9, 0) // 终点在 (1.2,0)，距怪物 0.3 < 0.4+0.1=0.5 → 命中
	_, b := w.Bullets.Spawn()
	b.PrevPos = world.V(0.9, 0)
	b.Pos = world.V(1.2, 0)
	b.Radius = 0.1

	if hits := Collide(w); len(hits) != 1 {
		t.Fatalf("滑动 0.3 m 穿过半径 0.4 的怪物，命中数 = %d，期望 1", len(hits))
	}
}

func TestCollideSkipsDeadMonster(t *testing.T) {
	w := newSystemsWorld(t, 3)
	mID := spawnMonsterAt(t, w, 0.5, 0)
	dead := w.Monsters.Get(mID)
	dead.HP = 0
	dead.Flags |= world.MonsterFlagDead
	_, b := w.Bullets.Spawn()
	b.PrevPos = world.V(0, 0)
	b.Pos = world.V(1, 0)
	b.Radius = 0.1

	if hits := Collide(w); len(hits) != 0 {
		t.Fatalf("命中了已死的怪：%d 条", len(hits))
	}
}

// TestCollideSkipsHealedCorpse 钉住"判据是标志而不是 HP"：治疗把尸体
// HP 拉回正数后，子弹仍不该命中它（否则击杀事件重复产出、分账算两次）。
func TestCollideSkipsHealedCorpse(t *testing.T) {
	w := newSystemsWorld(t, 3)
	mID := spawnMonsterAt(t, w, 0.5, 0)
	corpse := w.Monsters.Get(mID)
	corpse.Flags |= world.MonsterFlagDead
	corpse.HP = 50 // 治疗拉满，标志不动
	_, b := w.Bullets.Spawn()
	b.PrevPos = world.V(0, 0)
	b.Pos = world.V(1, 0)
	b.Radius = 0.1

	if hits := Collide(w); len(hits) != 0 {
		t.Fatalf("命中了 HP=50 但标志为死的实体：%d 条", len(hits))
	}
}

func TestCollideOneHitPerBullet(t *testing.T) {
	// 一颗子弹同一子步只能命中一个目标（M2 没有穿透）。
	// 否则 Pierce=0 的子弹会打穿整排怪。
	w := newSystemsWorld(t, 3)
	spawnMonsterAt(t, w, 0.5, 0)
	spawnMonsterAt(t, w, 0.8, 0)
	_, b := w.Bullets.Spawn()
	b.PrevPos = world.V(0, 0)
	b.Pos = world.V(1.5, 0)
	b.Radius = 0.1

	if hits := Collide(w); len(hits) != 1 {
		t.Fatalf("命中数 = %d，一颗非穿透子弹同一子步应只命中 1 个", len(hits))
	}
}

func TestCollideRecordsDamageFromBullet(t *testing.T) {
	w := newSystemsWorld(t, 3)
	spawnMonsterAt(t, w, 0.5, 0)
	_, b := w.Bullets.Spawn()
	b.PrevPos = world.V(0, 0)
	b.Pos = world.V(1, 0)
	b.Radius = 0.1
	b.Damage = 37
	b.Element = world.ElementFire
	b.OwnerID = 9

	hits := Collide(w)
	if len(hits) != 1 {
		t.Fatalf("命中数 = %d", len(hits))
	}
	if hits[0].Damage != 37 {
		t.Fatalf("伤害 = %v，期望取子弹的 37", hits[0].Damage)
	}
	if hits[0].Element != world.ElementFire {
		t.Fatalf("元素 = %v，期望 fire", hits[0].Element)
	}
	if hits[0].Src != 9 {
		t.Fatalf("来源 = %v，期望子弹的 OwnerID 9", hits[0].Src)
	}
}
