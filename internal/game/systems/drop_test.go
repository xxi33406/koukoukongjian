package systems

import (
	"testing"

	"koukoukongjian/internal/game/build"
	"koukoukongjian/internal/game/world"
)

// clearPickups 清空拾取物池（测试辅助）。
//
// 先收集再 Kill：Pool.Each 按槽位下标走，遍历中 Kill 会把槽位挂回 free
// 队列，后续遍历可能重复取到同一个槽位。
func clearPickups(w *world.World) {
	var ids []world.EntityID
	w.Pickups.Each(func(id world.EntityID, _ *world.PickupState) bool {
		ids = append(ids, id)
		return true
	})
	for _, id := range ids {
		w.Pickups.Kill(id)
	}
}

// 每一次击杀都掷两次独立骰：血瓶 15%、技能 5%，可以同时掉（用户拍板）。
//
// 为什么必须独立而不是"先掷一次决定掉什么"：用户明确说「两种会同时出现」。
// 互斥写法下永远不会同时掉，而这件事只有统计才看得出来——单看代码
// "两次 if rand()" 与 "一次 if/else" 都很像模像样。
func TestSpawnDropsRollsIndependently(t *testing.T) {
	w := newSystemsWorld(t, 20261118)

	for i := 0; i < 400; i++ {
		clearPickups(w)
		SpawnDrops(w, world.V(10, 10), world.ElementNone)

		kinds := map[world.PickupKind]int{}
		w.Pickups.Each(func(_ world.EntityID, pk *world.PickupState) bool {
			kinds[pk.Kind]++
			return true
		})
		if kinds[world.PickupHealth] > 0 && kinds[world.PickupSkill] > 0 {
			return // 见过同时掉，测试通过
		}
	}
	t.Fatalf("400 次击杀里没有出现「血瓶与技能同时掉」，两条掷骰可能被写成了互斥")
}

// 掉落概率落在配置值附近（15% / 5%）。
//
// 用统计而不是精确值：RandFloat64 是确定的（M3 已论证），但样本量小时
// 占比会抖。4000 次击杀下 15% 的标准差约 0.56%，给 ±3% 的容差已经足够宽，
// 再宽就测不出"写成 30%"这种错一个数量级的 bug。
func TestDropRates(t *testing.T) {
	w := newSystemsWorld(t, 424242)

	const n = 4000
	healthN, skillN := 0, 0
	for i := 0; i < n; i++ {
		clearPickups(w)
		SpawnDrops(w, world.V(10, 10), world.ElementNone)
		w.Pickups.Each(func(_ world.EntityID, pk *world.PickupState) bool {
			switch pk.Kind {
			case world.PickupHealth:
				healthN++
			case world.PickupSkill:
				skillN++
			}
			return true
		})
	}

	healthRate := float64(healthN) / n
	skillRate := float64(skillN) / n
	if healthRate < 0.12 || healthRate > 0.18 {
		t.Fatalf("血瓶掉率 = %.3f，期望 0.15±0.03", healthRate)
	}
	if skillRate < 0.02 || skillRate > 0.08 {
		t.Fatalf("技能掉率 = %.3f，期望 0.05±0.03", skillRate)
	}
}

// 掉落的拾取物起始寿命是 30 秒（spec L89，M4 规格修订）。
//
// 循环到真的掉出东西为止：15%/5% 的掷骰在某个固定种子下前几次全不中是
// 完全可能的，而断言"检查属性"的测试不该因为运气不对就直接失败——
// 那样失败信息会指向"没有生成掉落物"，读的人以为掉落逻辑坏了，其实只是
// 这几次没中。
func TestSpawnDropsSetTTL(t *testing.T) {
	w := newSystemsWorld(t, 7)

	var got []*world.PickupState
	for i := 0; i < 200 && len(got) == 0; i++ {
		clearPickups(w)
		SpawnDrops(w, world.V(10, 10), world.ElementNone)
		w.Pickups.Each(func(_ world.EntityID, pk *world.PickupState) bool {
			got = append(got, pk)
			return true
		})
	}
	if len(got) == 0 {
		t.Fatalf("200 次击杀没掉出任何东西，掉落逻辑可能完全没执行")
	}
	for _, pk := range got {
		if pk.Life <= 0 {
			t.Fatalf("掉落物的 Life = %v，期望正数（30 秒 TTL）", pk.Life)
		}
	}
}

// 死亡位置就是掉落位置：M6 的位置死亡技能依赖这一点。
func TestSpawnDropsAtKillPosition(t *testing.T) {
	w := newSystemsWorld(t, 7)
	pos := world.V(33.5, 47.25)

	var got []*world.PickupState
	for i := 0; i < 200 && len(got) == 0; i++ {
		clearPickups(w)
		SpawnDrops(w, pos, world.ElementNone)
		w.Pickups.Each(func(_ world.EntityID, pk *world.PickupState) bool {
			got = append(got, pk)
			return true
		})
	}
	if len(got) == 0 {
		t.Fatalf("200 次击杀没掉出任何东西")
	}
	for _, pk := range got {
		if pk.Pos != pos {
			t.Fatalf("掉落物位置 = %v，期望 %v", pk.Pos, pos)
		}
	}
}

// 池满时静默丢弃，不 panic（灰盒可接受）。
func TestSpawnDropsWhenPoolFull(t *testing.T) {
	w := newSystemsWorld(t, 7)
	for i := 0; i < 10000; i++ {
		if SpawnPickup(w, world.V(1, 1), world.PickupGold, 0, 1) == world.InvalidID {
			break
		}
	}
	SpawnDrops(w, world.V(10, 10), world.ElementNone) // 不该 panic
}

// TestSpawnDropsPrefersElementSkills 钉住按元素筛技能。
//
// 位置死亡的语义是「死在哪决定掉什么」。传 Fire 时只能掉火系技能，
// 这是它与 M5「随机挑一个主动技能」的全部区别。
//
// 收集**每一次**掉出的技能而不是只看第一个：主动技能表里 8 条中只有 1 条
// 带 Fire 元素，若能掉出不筛选的随机技能，那么连续十几次里几乎必然出现
// 非火系。只看第一个会让这个测试在 seed 恰好先掷出火系时变成假绿——
// 变异测试（把筛选条件改成 false）立刻暴露了这一点。
func TestSpawnDropsPrefersElementSkills(t *testing.T) {
	w := newSystemsWorld(t, 7)

	var got []uint16
	for i := 0; i < 200 && len(got) < 20; i++ {
		clearPickups(w)
		SpawnDrops(w, world.V(20, 20), world.ElementFire)
		w.Pickups.Each(func(_ world.EntityID, pk *world.PickupState) bool {
			if pk.Kind == world.PickupSkill {
				got = append(got, pk.RefID)
			}
			return true
		})
	}
	if len(got) == 0 {
		t.Fatal("200 次触发都没掉出技能（5% 概率下约 0.003% 的概率，怀疑实现有误）")
	}

	for _, ref := range got {
		s, ok := build.SkillByID(ref)
		if !ok {
			t.Fatalf("掉出的技能 ID %d 在表里不存在", ref)
		}
		if s.Element != world.ElementFire {
			t.Fatalf("火区掉出了元素为 %v 的技能（%s），期望 ElementFire（本次共掉出 %d 个）",
				s.Element, s.Name, len(got))
		}
	}
}

// TestSpawnDropsNoElementFallsBackToAllActive 钉住无属性据点的回退。
//
// 多数据点没有属性（用户规则：每种属性最多一个）。那些据点必须能正常
// 掉技能，只是不筛选——否则 7 个据点里有 4 个永远不掉技能。
func TestSpawnDropsNoElementFallsBackToAllActive(t *testing.T) {
	w := newSystemsWorld(t, 7)

	var got bool
	for i := 0; i < 200; i++ {
		clearPickups(w)
		SpawnDrops(w, world.V(20, 20), world.ElementNone)
		w.Pickups.Each(func(_ world.EntityID, pk *world.PickupState) bool {
			if pk.Kind == world.PickupSkill {
				got = true
			}
			return true
		})
		if got {
			break
		}
	}
	if !got {
		t.Fatal("无属性据点 200 次都没掉出技能——回退到全部主动技能的逻辑没生效")
	}
}

// TestSpawnDropsElementWithNoSkillFallsBack 钉住「属性存在但库里没有该元素技能」。
//
// M6 三种元素各只有一个技能，若将来某种元素的技能被移除，筛出来是空
// 列表。此时必须回退到全部主动技能，而不是「什么都不掉」——后者会让
// 那个据点变成一个永远不掉技能的坏图。
func TestSpawnDropsElementWithNoSkillFallsBack(t *testing.T) {
	w := newSystemsWorld(t, 7)

	// 用一个表里没有技能的元素（Oil 在 v0.1 没有对应技能）。
	var got bool
	for i := 0; i < 200; i++ {
		clearPickups(w)
		SpawnDrops(w, world.V(20, 20), world.ElementOil)
		w.Pickups.Each(func(_ world.EntityID, pk *world.PickupState) bool {
			if pk.Kind == world.PickupSkill {
				got = true
			}
			return true
		})
		if got {
			break
		}
	}
	if !got {
		t.Fatal("Oil 元素筛出空列表后没有回退到全部主动技能")
	}
}
