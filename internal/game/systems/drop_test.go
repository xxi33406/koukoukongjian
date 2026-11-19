package systems

import (
	"testing"

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
		SpawnDrops(w, world.V(10, 10), 6)

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
		SpawnDrops(w, world.V(10, 10), 6)
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
		SpawnDrops(w, world.V(10, 10), 6)
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
		SpawnDrops(w, pos, 6)
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
	SpawnDrops(w, world.V(10, 10), 6) // 不该 panic
}
