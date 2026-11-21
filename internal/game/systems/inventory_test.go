package systems

import (
	"math"
	"testing"

	"koukoukongjian/internal/game/build"
	"koukoukongjian/internal/game/world"
)

// 本文件是 M4 Task 10（背包）的测试：容量约束、开包引导、受击打断。
//
// 容量规则的三个来源各不同，测试必须分别钉住，不能混成一条"装东西"的用例：
//   - 技能槽（2 主动 + 2 被动）满时**替换第二槽**（spec L42 只写"2 个技能"，
//     替换语义由 Task 9 的 TryEquipSkill 先落地，见 pickup.go）；
//   - 道具槽（3 个，spec L42）满时**丢弃**（地上再掉了还能捡，替换掉玩家手上
//     的道具是玩家不会同意的行为）；
//   - 背包引导（spec L44）期间置 PlayerFlagBusy，受击打断。

// TestEquipSkillRespectsCapacity 钉住主动技能槽的容量与溢出规则。
//
// 注意断言的方向：满槽时不是"拒绝"，而是替换 slots[1]。Task 9 已把这条
// 语义写进 TryEquipSkill（player.go），Task 10 只是复用它，所以测试要守
// 的是"第 0 槽永不被顶掉、槽里始终最多 2 个非零"。若写成"第三次返回
// false"，这里会红——而红的原因不是实现错，是测试与既有语义不一致。
func TestEquipSkillRespectsCapacity(t *testing.T) {
	w := newSystemsWorld(t, 31)
	_, p := spawnPlayerAt(t, w, 0, 0)

	if !TryEquipSkill(p, build.SkillIDDash) {
		t.Fatalf("装第一个主动技能失败")
	}
	if !TryEquipSkill(p, build.SkillIDBurst) {
		t.Fatalf("装第二个主动技能失败")
	}
	if got, want := p.SkillActive, [2]uint16{build.SkillIDDash, build.SkillIDBurst}; got != want {
		t.Fatalf("两个主动槽是 %v，期望 %v", got, want)
	}

	// 第三个主动技能：替换 [1]，[0] 不动。
	if !TryEquipSkill(p, build.SkillIDHeal) {
		t.Errorf("槽满时装第三个主动技能返回 false，期望 true（替换语义）")
	}
	if got, want := p.SkillActive, [2]uint16{build.SkillIDDash, build.SkillIDHeal}; got != want {
		t.Errorf("装第三个主动技能后槽是 %v，期望 %v（替换第二槽、保留第一槽）", got, want)
	}

	// 槽里始终只有 2 个非零：容量 2 是由定长数组表达的，但这里显式数一遍，
	// 免得日后有人把它改成切片后"能装下第 3 个"却没人发现。
	n := 0
	for _, id := range p.SkillActive {
		if id != 0 {
			n++
		}
	}
	if n != 2 {
		t.Errorf("主动槽里有 %d 个非零技能，期望 2", n)
	}

	// 未知 ID 必须被拒绝而不是塞进槽：塞进去之后快照会把一个不存在的
	// 技能 ID 发给客户端。
	before := p.SkillActive
	if TryEquipSkill(p, 9999) {
		t.Errorf("装未知技能 ID 返回 true，期望 false")
	}
	if p.SkillActive != before {
		t.Errorf("装未知 ID 后槽变成了 %v，期望不变（%v）", p.SkillActive, before)
	}
}

// TestEquipPassiveRespectsCapacity 钉住被动槽的容量，并顺带钉住"替换会
// 触发重算"。
//
// 只数槽位数是不够的：被动会改派生属性，替换掉一个被动之后属性必须跟着
// 回退。若 TryEquipSkill 漏了 RecomputeDerived，槽位数照样正确，玩家的
// 生命上限却永久带着已卸下被动的加值——这种偏差只有玩久了才发现。
func TestEquipPassiveRespectsCapacity(t *testing.T) {
	w := newSystemsWorld(t, 32)
	_, p := spawnPlayerAt(t, w, 0, 0)
	baseHP := p.MaxHP

	TryEquipSkill(p, build.SkillIDToughBody) // +25 生命上限
	if want := baseHP + 25; p.MaxHP != want {
		t.Fatalf("装强健体魄后 MaxHP=%v，期望 %v", p.MaxHP, want)
	}
	TryEquipSkill(p, build.SkillIDAmmoUp)
	if got, want := p.SkillPassive, [2]uint16{build.SkillIDToughBody, build.SkillIDAmmoUp}; got != want {
		t.Fatalf("两个被动槽是 %v，期望 %v", got, want)
	}
	// 两个被动都装上：伤害倍率叠加。
	if p.Derived.DamageMul != 1.2 || p.Derived.MaxHPBonus != 25 {
		t.Fatalf("派生属性是 %+v，期望 DamageMul=1.2 MaxHPBonus=25", p.Derived)
	}

	// 第三个被动：替换 [1]（强化弹头）为疾行，[0]（强健体魄）保留。
	if !TryEquipSkill(p, build.SkillIDSwift) {
		t.Errorf("槽满时装第三个被动技能返回 false，期望 true（替换语义）")
	}
	if got, want := p.SkillPassive, [2]uint16{build.SkillIDToughBody, build.SkillIDSwift}; got != want {
		t.Errorf("被动槽是 %v，期望 %v", got, want)
	}
	// 强化弹头被顶掉，伤害倍率该回到中性值 1。
	if p.Derived.DamageMul != 1 {
		t.Errorf("替换掉强化弹头后 DamageMul=%v，期望 1（中性值）", p.Derived.DamageMul)
	}
	// 强健体魄还在槽 [0]，生命加值不该丢。
	if want := baseHP + 25; p.MaxHP != want {
		t.Errorf("替换后 MaxHP=%v，期望 %v（强健体魄仍在槽 0）", p.MaxHP, want)
	}
}

// TestItemSlotsCapacity 钉住道具有 3 个槽（spec L42），满了就丢弃。
//
// 与技能槽的区别正在这里：技能满槽替换，道具满槽拒绝。两者写在同一份
// 规格行里，却必须有不同的溢出行为，所以分开测、分开写注释说清理由。
func TestItemSlotsCapacity(t *testing.T) {
	w := newSystemsWorld(t, 33)
	_, p := spawnPlayerAt(t, w, 0, 0)

	if !EquipItem(p, 11) {
		t.Fatalf("装第一个道具失败")
	}
	if !EquipItem(p, 12) {
		t.Fatalf("装第二个道具失败")
	}
	if !EquipItem(p, 13) {
		t.Fatalf("装第三个道具失败")
	}
	if got, want := p.ItemSlots, [3]uint16{11, 12, 13}; got != want {
		t.Fatalf("道具槽是 %v，期望 %v", got, want)
	}

	// 第四个：拒绝，且三个槽一个都不许被顶掉。
	if EquipItem(p, 14) {
		t.Errorf("槽满时装第四个道具返回 true，期望 false")
	}
	if got, want := p.ItemSlots, [3]uint16{11, 12, 13}; got != want {
		t.Errorf("槽满时道具槽变成 %v，期望不变（%v）", got, want)
	}

	// 0 是"空槽"哨兵，不是合法道具 ID。装进去会让槽看起来是空的，
	// 后续 EquipItem 又会把它当空位覆写——一个值同时表示"空"和"合法"，
	// 正是 AGENTS.md §1.2「用值推断状态」那类根因。
	if EquipItem(p, 0) {
		t.Errorf("装道具 ID 0 返回 true，期望 false（0 是空槽哨兵）")
	}
}

// TestOpenInventoryTakesTime 钉住引导时长是 2 秒（spec L44 写"数秒"）。
//
// 为什么是 2 秒而不是 1 或 5：开背包是"停下来换取信息"的交换，风险自调节
// 全靠这段可以被伤害打断的窗口。1 秒在 20Hz 下只有 20 个 tick，网络抖动
// 一两帧就能让玩家几乎无法被惩罚；5 秒会让"危险区能开但开不完"变成
// "任何地方都开不完"，规则退化成禁用背包。
func TestOpenInventoryTakesTime(t *testing.T) {
	cases := []struct {
		name string
		rate float64
		want uint32
	}{
		{"20Hz 默认", 20, 40},
		{"60Hz", 60, 120},
		{"30Hz", 30, 60},
		// 坏 tick 率不能折出 0：0 会让引导在开始的那一 tick 就完成，
		// 表现为"背包瞬间打开"，而配置错误的症状完全看不到。
		{"0 兜底", 0, 1},
		{"负数兜底", -20, 1},
		{"NaN 兜底", math.NaN(), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := OpenInventoryTicks(c.rate); got != c.want {
				t.Errorf("OpenInventoryTicks(%v)=%d，期望 %d", c.rate, got, c.want)
			}
		})
	}

	// 逐 tick 走一遍：40 个 tick 的引导，第 40 个 tick 才完成。
	// 用 BeginOpenInventory 的"结束 tick"口径，与 room 里
	// `w.Tick+OpenInventoryTicks(rate)` 的调用方式一致。
	w := newSystemsWorld(t, 34)
	_, p := spawnPlayerAt(t, w, 0, 0)
	n := OpenInventoryTicks(float64(w.TickRate))
	BeginOpenInventory(p, n)

	for tick := uint32(0); tick < n; tick++ {
		if TickOpenInventory(p, tick) {
			t.Fatalf("第 %d 个 tick 就完成了引导，期望第 %d 个 tick 才完成", tick, n)
		}
	}
	if !TickOpenInventory(p, n) {
		t.Fatalf("第 %d 个 tick 引导未完成，期望完成", n)
	}
	if p.Flags.Has(world.PlayerFlagBusy) {
		t.Errorf("引导完成后 PlayerFlagBusy 仍置位")
	}
}

// TestOpenInventoryInterruptedByDamage 钉住 spec L44 的"受击打断"。
//
// 打断必须发生在 world 包的伤害路径里，而不是等 room 每 tick 检查一次
// "血少了没有"：后者要比较上一 tick 的血量，漏一帧就少一次打断，而且
// 无敌挡掉的伤害会被误判成受击。所以这里直接打 ResolveDamage，验证
// 打断与伤害结算在同一处完成。
func TestOpenInventoryInterruptedByDamage(t *testing.T) {
	w := newSystemsWorld(t, 35)
	id, p := spawnPlayerAt(t, w, 0, 0)

	// 先验证不打断的情况：无敌挡掉的伤害不算受击。
	BeginOpenInventory(p, 100)
	p.Flags |= world.PlayerFlagInvulnerable
	w.ResolveDamage(world.InvalidID, id, world.EntityClassPlayer, 10, world.ElementNone, world.DamageTags{}, world.InvalidID)
	if !p.Flags.Has(world.PlayerFlagBusy) {
		t.Errorf("被无敌挡掉的伤害打断了引导，期望不打断（没吃到的伤害不算受击）")
	}
	if p.InventoryOpenAt == 0 {
		t.Errorf("被无敌挡掉的伤害清掉了引导结束 tick，期望保留")
	}
	p.Flags &^= world.PlayerFlagInvulnerable

	// 真的吃到伤害：引导被打断。
	hpBefore := p.HP
	ev := w.ResolveDamage(world.InvalidID, id, world.EntityClassPlayer, 10, world.ElementNone, world.DamageTags{}, world.InvalidID)
	if ev.Amount != 10 {
		t.Fatalf("这次伤害结算了 %v，期望 10", ev.Amount)
	}
	if p.HP != hpBefore-10 {
		t.Fatalf("受击后 HP=%v，期望 %v", p.HP, hpBefore-10)
	}
	if p.Flags.Has(world.PlayerFlagBusy) {
		t.Errorf("受击后 PlayerFlagBusy 仍置位，期望被清除")
	}
	if p.InventoryOpenAt != 0 {
		t.Errorf("受击后 InventoryOpenAt=%d，期望 0（引导已取消）", p.InventoryOpenAt)
	}

	// 打断之后 TickOpenInventory 不该还能把引导"走完"。
	if TickOpenInventory(p, 1000) {
		t.Errorf("引导被打断后仍在第 1000 tick 报告完成")
	}

	// 伤害只清 Busy 这一位，别的标记不能被顺手抹掉：复活无敌窗口与
	// 引导是两个独立状态，一起抹掉会让玩家在背包刚要点开时意外失去保护。
	BeginOpenInventory(p, 200)
	p.Flags |= world.PlayerFlagReconnecting
	w.ResolveDamage(world.InvalidID, id, world.EntityClassPlayer, 1, world.ElementNone, world.DamageTags{}, world.InvalidID)
	if !p.Flags.Has(world.PlayerFlagReconnecting) {
		t.Errorf("受击打断把 PlayerFlagReconnecting 也清掉了，期望只清 Busy")
	}
}

// TestInventoryBusyFlagLifecycle 钉住 PlayerFlagBusy 的置位与清除。
//
// 这个标记是"玩家现在不能做别的事"的唯一表达（state.go 里注释写着
// 「开背包 / 引导复活 / 商店交易中」）。它只有两个合法状态，任何一条
// 出口漏清都会让玩家永久卡在"忙"里——而忙的效果是拒绝交互，症状是
// "背包再也打不开"，重启才能好。
func TestInventoryBusyFlagLifecycle(t *testing.T) {
	w := newSystemsWorld(t, 36)
	_, p := spawnPlayerAt(t, w, 0, 0)

	if p.Flags.Has(world.PlayerFlagBusy) {
		t.Fatalf("新玩家出生就带着 PlayerFlagBusy")
	}

	// 开始：置位 + 记下结束 tick。
	BeginOpenInventory(p, 10)
	if !p.Flags.Has(world.PlayerFlagBusy) {
		t.Errorf("BeginOpenInventory 之后 PlayerFlagBusy 未置位")
	}
	if p.InventoryOpenAt != 10 {
		t.Errorf("InventoryOpenAt=%d，期望 10", p.InventoryOpenAt)
	}

	// 重复开始：以最后一次为准（玩家连点两下"开背包"）。
	BeginOpenInventory(p, 20)
	if p.InventoryOpenAt != 20 {
		t.Errorf("重复 BeginOpenInventory 后 InventoryOpenAt=%d，期望 20（后者覆盖）", p.InventoryOpenAt)
	}
	// 覆盖必须以最后一次为准，否则连点可以让引导被无限延长或提前结束。
	if TickOpenInventory(p, 19) {
		t.Errorf("第 19 tick 就完成了引导，期望按最后一次的结束 tick 20 判定")
	}

	// 未在引导时 Tick 是空操作：不能返回 true（那会让调用方以为开完了）。
	if TickOpenInventory(p, 20) != true {
		t.Fatalf("第 20 tick 引导未完成，期望完成")
	}
	if p.Flags.Has(world.PlayerFlagBusy) {
		t.Errorf("完成后 PlayerFlagBusy 仍置位")
	}
	if p.InventoryOpenAt != 0 {
		t.Errorf("完成后 InventoryOpenAt=%d，期望 0", p.InventoryOpenAt)
	}
	if TickOpenInventory(p, 21) {
		t.Errorf("已完成后再次 Tick 返回 true，期望 false")
	}

	// 显式打断：两个字段一起清。
	BeginOpenInventory(p, 100)
	InterruptBusy(p)
	if p.Flags.Has(world.PlayerFlagBusy) {
		t.Errorf("InterruptBusy 之后 PlayerFlagBusy 仍置位")
	}
	if p.InventoryOpenAt != 0 {
		t.Errorf("InterruptBusy 之后 InventoryOpenAt=%d，期望 0", p.InventoryOpenAt)
	}

	// 打断只动 Busy 这一位。
	p.Flags |= world.PlayerFlagInvulnerable
	InterruptBusy(p)
	if !p.Flags.Has(world.PlayerFlagInvulnerable) {
		t.Errorf("InterruptBusy 把 PlayerFlagInvulnerable 也清掉了，期望只清 Busy")
	}

	// 结束 tick 传 0 是调用方的错误（0 是"未在引导"的哨兵）。这时必须
	// 仍然置 Busy，但把结束 tick 钳到 1，让引导在下一 tick 就结束——
	// 否则 Busy 会永久留着，玩家从此打不开背包。
	BeginOpenInventory(p, 0)
	if !p.Flags.Has(world.PlayerFlagBusy) {
		t.Errorf("结束 tick 传 0 时没有置 Busy，期望置位")
	}
	if p.InventoryOpenAt == 0 {
		t.Errorf("结束 tick 传 0 时 InventoryOpenAt 仍是 0，会与「未在引导」撞车")
	}
	if !TickOpenInventory(p, 1) {
		t.Errorf("结束 tick 传 0 时引导没有在下一 tick 结束")
	}
}
