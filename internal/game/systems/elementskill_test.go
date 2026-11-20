package systems

import (
	"testing"

	"koukoukongjian/internal/game/build"
	"koukoukongjian/internal/game/world"
)

// TestSkillTableHasElementSkills 钉住三个元素技能存在且元素正确。
func TestSkillTableHasElementSkills(t *testing.T) {
	cases := []struct {
		id   uint16
		name string
		el   world.Element
	}{
		{build.SkillIDFlame, "火焰喷射", world.ElementFire},
		{build.SkillIDWave, "水波", world.ElementWater},
		{build.SkillIDBolt, "雷击", world.ElementThunder},
	}
	for _, c := range cases {
		s, ok := build.SkillByID(c.id)
		if !ok {
			t.Fatalf("技能 %d（%s）不存在", c.id, c.name)
		}
		if s.Kind != build.SkillKindActive {
			t.Fatalf("技能 %d 的 Kind = %v，期望 SkillActive", c.id, s.Kind)
		}
		if s.Element != c.el {
			t.Fatalf("技能 %d 的元素 = %v，期望 %v", c.id, s.Element, c.el)
		}
		if s.CooldownS <= 0 {
			t.Fatalf("技能 %d 的冷却 = %v，必须 >0（否则无限连放）", c.id, s.CooldownS)
		}
		if s.Radius <= 0 {
			t.Fatalf("技能 %d 的半径 = %v，必须 >0", c.id, s.Radius)
		}
		if s.Amount <= 0 {
			t.Fatalf("技能 %d 的伤害 = %v，必须 >0", c.id, s.Amount)
		}
	}
}

// TestReflectSkillHasNoElement 钉住反射面不是元素技能（对照）。
//
// 若反射面也被标成火/水，位置死亡技能筛「火元素技能」时会把它当成
// 火系技能掉出来，玩家拿到一片镜子却发现不能攻击。
func TestReflectSkillHasNoElement(t *testing.T) {
	s, ok := build.SkillByID(build.SkillIDReflect)
	if !ok {
		t.Fatal("反射面技能不存在")
	}
	if s.Element != world.ElementNone {
		t.Fatalf("反射面的元素 = %v，期望 ElementNone", s.Element)
	}
}

// TestElementSkillDamagesMonstersInRadius 钉住区域伤害生效。
func TestElementSkillDamagesMonstersInRadius(t *testing.T) {
	w := newSystemsWorld(t, 7)

	pid, p := spawnPlayerAt(t, w, 20, 20)
	p.SkillActive[0] = build.SkillIDFlame

	// 半径内一只，半径外一只。
	inID := spawnMonsterAt(t, w, 21, 20)
	outID := spawnMonsterAt(t, w, 40, 40)

	if !CastActive(w, pid, 0, p) {
		t.Fatal("施放火焰喷射失败")
	}

	in := w.Monsters.Get(inID)
	if in == nil {
		t.Fatal("半径内的怪消失了")
	}
	if in.HP >= in.MaxHP {
		t.Fatalf("半径内的怪 HP = %v/%v，期望掉血（应该被打了）", in.HP, in.MaxHP)
	}
	out := w.Monsters.Get(outID)
	if out == nil {
		t.Fatal("半径外的怪消失了")
	}
	if out.HP != out.MaxHP {
		t.Fatalf("半径外的怪 HP = %v/%v，期望满血（不该被打）", out.HP, out.MaxHP)
	}
}

// TestElementSkillDamagesMatchTable 钉住三个技能都用对了自己的数值。
func TestElementSkillDamagesMatchTable(t *testing.T) {
	cases := []struct {
		id     uint16
		amount float64
	}{
		{build.SkillIDFlame, 20},
		{build.SkillIDWave, 12},
		{build.SkillIDBolt, 40},
	}
	for _, c := range cases {
		w := newSystemsWorld(t, 7)
		pid, p := spawnPlayerAt(t, w, 20, 20)
		p.SkillActive[0] = c.id
		mid := spawnMonsterAt(t, w, 20.5, 20) // 全部技能的半径内
		before := w.Monsters.Get(mid).HP

		if !CastActive(w, pid, 0, p) {
			t.Fatalf("技能 %d 施放失败", c.id)
		}

		after := w.Monsters.Get(mid).HP
		if got := before - after; got != c.amount {
			t.Fatalf("技能 %d 造成 %v 伤害，期望 %v", c.id, got, c.amount)
		}
	}
}

// TestCastElementalEmitsElement 钉住元素真的进了伤害事件。
//
// 这条用例是被变异测试逼出来的：把 castElemental 里的 s.Element 换成
// world.ElementNone 之后，其余所有用例仍然全绿——因为 M6 没有任何地方
// 消费事件里的元素。于是让 castElemental 返回事件，把「元素传进去了」
// 变成可断言的事实。
//
// 元素不是装饰：M7 的连锁反应（燃烧地面遇水熄灭、导电区域）都要读
// DamageEvent.Element。等到那时才发现一路都在传 None，改动面会大得多。
func TestCastElementalEmitsElement(t *testing.T) {
	cases := []struct {
		id uint16
		el world.Element
	}{
		{build.SkillIDFlame, world.ElementFire},
		{build.SkillIDWave, world.ElementWater},
		{build.SkillIDBolt, world.ElementThunder},
	}
	for _, c := range cases {
		w := newSystemsWorld(t, 7)
		pid, p := spawnPlayerAt(t, w, 20, 20)
		spawnMonsterAt(t, w, 20.5, 20)

		s, ok := build.SkillByID(c.id)
		if !ok {
			t.Fatalf("技能 %d 不存在", c.id)
		}

		events := castElemental(w, pid, p, s)

		if len(events) != 1 {
			t.Fatalf("技能 %d 产生了 %d 条伤害事件，期望 1", c.id, len(events))
		}
		if events[0].Element != c.el {
			t.Fatalf("技能 %d 的伤害事件元素 = %v，期望 %v", c.id, events[0].Element, c.el)
		}
		if events[0].Amount != s.Amount {
			t.Fatalf("技能 %d 的事件伤害 = %v，期望 %v", c.id, events[0].Amount, s.Amount)
		}
	}
}

// TestElementSkillSkipsDeadMonsters 钉住尸体不再吃技能伤害。
func TestElementSkillSkipsDeadMonsters(t *testing.T) {
	w := newSystemsWorld(t, 7)

	pid, p := spawnPlayerAt(t, w, 20, 20)
	p.SkillActive[0] = build.SkillIDFlame

	mid := spawnMonsterAt(t, w, 20.5, 20)
	m := w.Monsters.Get(mid)
	m.Flags |= world.MonsterFlagDead
	m.HP = 0

	CastActive(w, pid, 0, p)

	if m.HP != 0 {
		t.Fatalf("尸体的 HP = %v，期望仍是 0（尸体不吃伤害）", m.HP)
	}
}

// TestWeaponHandCannonIsFire 钉住手炮带火元素。
func TestWeaponHandCannonIsFire(t *testing.T) {
	wep, ok := WeaponByID(WeaponHandCannon)
	if !ok {
		t.Fatal("手炮不存在")
	}
	if wep.Element != world.ElementFire {
		t.Fatalf("手炮元素 = %v，期望 ElementFire", wep.Element)
	}
}

// TestWeaponRifleHasNoElement 钉住步枪无元素（对照）。
//
// 两把枪都带元素会让「元素协同」失去稀缺性：如果随便一把枪都能触发
// 反应，玩家就不需要为了配火水而挑武器。
func TestWeaponRifleHasNoElement(t *testing.T) {
	wep, ok := WeaponByID(WeaponRifle)
	if !ok {
		t.Fatal("步枪不存在")
	}
	if wep.Element != world.ElementNone {
		t.Fatalf("步枪元素 = %v，期望 ElementNone", wep.Element)
	}
}

// TestBulletCarriesWeaponElement 钉住武器的元素进了子弹。
//
// 这是「手炮带火」变成玩家可见事实的唯一环节：武器表写 ElementFire 而
// spawnBullet 不读它的话，子弹照样是无元素的，交汇永远不触发。
func TestBulletCarriesWeaponElement(t *testing.T) {
	w := newSystemsWorld(t, 7)

	pid, p := spawnPlayerAt(t, w, 20, 20)
	p.WeaponID = WeaponHandCannon
	p.AimDir = 0

	fireWith(w, pid, p, mustWeapon(t, WeaponHandCannon), true, false, 0.05)

	var got world.Element
	var found bool
	w.Bullets.Each(func(_ world.EntityID, b *world.BulletState) bool {
		got = b.Element
		found = true
		return false
	})
	if !found {
		t.Fatal("手炮没打出子弹")
	}
	if got != world.ElementFire {
		t.Fatalf("手炮打出的子弹元素 = %v，期望 ElementFire", got)
	}
}

func mustWeapon(t *testing.T, id uint16) Weapon {
	t.Helper()
	wep, ok := WeaponByID(id)
	if !ok {
		t.Fatalf("武器 %d 不存在", id)
	}
	return wep
}
