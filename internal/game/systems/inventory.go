package systems

import (
	"math"

	"koukoukongjian/internal/game/world"
)

// 本文件是背包系统（M4 Task 10）：容量约束、开包引导、受击打断。
//
// spec L42 把背包拆成三类容量：1 把武器 + 2 个技能 + 3 种道具。武器
// （WeaponID）在 M4 还是单值，换武器走里程碑掉落，不存在"满了"的问题；
// 技能槽由 Task 9 的 TryEquipSkill 管（本文件不重复实现）；这里只补
// 道具槽与开包引导两件事。
//
// 三类容量的溢出规则**故意不一致**，理由写在各函数上：
//   - 技能满槽 -> 替换第二槽（场上随时会掉技能，替换是玩家的主动选择机会）
//   - 道具满槽 -> 丢弃（道具没有技能那么强的即时价值，替换等于替玩家做决定）

// OpenInventorySeconds 是开背包的引导时长（spec L44「数秒」取 2 秒）。
//
// 为什么是 2：这段窗口就是整条规则的全部——spec L44 的"风险自调节"靠
// 玩家自己判断"这个位置值不值得停下来两秒"。取值区间由两端夹住：
//
//	下界——20Hz 下 1 秒只有 20 个 tick，网络抖动一两帧、或者一只怪的攻击
//	提前量给得宽一点，玩家就几乎无论如何都能开完，规则等于不存在；
//	上界——5 秒已经超过一次据点遭遇战的长度，任何有怪的地方都开不完，
//	规则从"要判断"退化成"别用"，玩家会绕过它而不是权衡它。
//
// 2 秒 = 40 tick 留出了"看见怪 -> 决定取消"的反应时间（人的简单反应
// 约 0.25 秒，含决策约 0.6 秒），同时明显长于一次闪避的距离。
const OpenInventorySeconds = 2.0

// OpenInventoryTicks 把引导时长换算成 tick 数。
//
// 参数是 float64 而不是 uint32 的 tick 率，与 PickupTTLTicks 保持同一签名
// 形状：调用方手里是 config 的 TickRate（int），若这里收 int，测试要为了
// 覆盖坏值（0 / 负数 / NaN）先做一次转换，而转换本身就会掩盖坏值。
//
// 兜底成 1 而不是 0：0 会让引导在开始的那一 tick 就完成，症状是"背包
// 瞬间打开"，而配置写错的症状反而完全看不见。至少 1 tick 保证引导永远
// 需要经过一次 Tick 才结束，打断逻辑也就永远有生效机会。
func OpenInventoryTicks(rate float64) uint32 {
	if math.IsNaN(rate) || rate <= 0 {
		return 1
	}
	ticks := uint32(OpenInventorySeconds*rate + 0.5)
	if ticks < 1 {
		return 1
	}
	return ticks
}

// EquipItem 尝试把道具装进道具槽，装上了返回 true。
//
// 规则：装第一个空槽（spec L42 三个槽位），全满返回 false 且不动任何槽。
//
// 为什么满槽是丢弃而不是像技能那样替换第二槽：两条规则看起来该一致，
// 但玩家的决策成本不一样。技能掉在地上时玩家看得见它是什么、要跟身上
// 哪个技能比，替换是一个明确的选择；而道具在 M4 还没有表（build 包只有
// 技能表），捡起一个数字 ID 的道具时玩家根本没有"要不要换"的信息基础，
// 替他覆盖掉一个已经占位的道具只是损失。所以这里保守地拒绝，把"换什么"
// 留给 M5 的道具界面。
//
// id 为 0 时拒绝：0 是"空槽"的哨兵（PlayerState.ItemSlots 是零值即空）。
// 让 0 进槽等于一个值同时表示"空"和"合法 ID"，后续 EquipItem 会把那一格
// 当空位覆写，玩家看见的是"捡了但背包没变"——AGENTS.md §1.2「用值推断
// 状态」那类根因，从入口就挡住。
func EquipItem(p *world.PlayerState, id uint16) bool {
	if p == nil || id == 0 {
		return false
	}
	for i := range p.ItemSlots {
		if p.ItemSlots[i] == 0 {
			p.ItemSlots[i] = id
			return true
		}
	}
	return false
}

// BeginOpenInventory 开始开背包引导，now 是当前 tick，结束时刻按
// OpenInventoryTicks 算出后写进 InventoryOpenAt。
//
// 重复调用以最后一次为准：客户端连点"开背包"时，若先来的那一次优先，
// 引导会在第二次点击后极短的时间内结束（第二次点击只是被忽略），玩家
// 看到的是"我连点了两下反而更快打开了"。以后一次为准则连点只会重置
// 进度，符合"重新开始"的直觉。
//
// endAt 传 0（或算出 0）时钳到 now+1：0 是"未在引导"的哨兵，写进去会让
// Busy 位置着而引导永远推不完，玩家从此打不开背包。宁可让它下一 tick
// 就结束，也不要留一个永不消失的忙状态。
func BeginOpenInventory(p *world.PlayerState, endAt uint32) {
	if p == nil {
		return
	}
	if endAt == 0 {
		endAt = 1
	}
	p.InventoryOpenAt = endAt
	p.Flags |= world.PlayerFlagBusy
}

// TickOpenInventory 推进开背包引导，到点返回 true 并清位。
//
// 判据是 now >= InventoryOpenAt 而不是 ==：tick 号是单调递增的，但调用方
// 可能因为某帧漏调（例如房间刚起步、暂停恢复）而跳过恰好等于的那一 tick；
// 用 == 比较会让引导永远差一帧，症状是"背包卡在最后一格不开"。
func TickOpenInventory(p *world.PlayerState, now uint32) bool {
	if p == nil || p.InventoryOpenAt == 0 {
		return false
	}
	if now < p.InventoryOpenAt {
		return false
	}
	p.InventoryOpenAt = 0
	p.Flags &^= world.PlayerFlagBusy
	return true
}

// InterruptBusy 打断当前引导（清 Busy 位与结束 tick）。
//
// 这是 world 包那段内联逻辑的薄封装，给"没有伤害事件但必须打断"的调用方
// 用（例如房间逻辑判定玩家掉线、或主动取消）。真正的受击打断不能走这里：
// world 包不能 import systems（分层，AGENTS.md §2.4），所以
// internal/game/world/damage.go 的 resolvePlayerDamage 里内联了同样的两行。
// 两处必须同时改——这是分层换来的代价，注释互相指着对方。
//
// 只清 Busy 这一位：Invulnerable（复活保护）与 Reconnecting（断线重连）
// 是独立状态，顺手一起清会让玩家在背包被怪打断的瞬间意外失去保护。
func InterruptBusy(p *world.PlayerState) {
	if p == nil {
		return
	}
	p.InventoryOpenAt = 0
	p.Flags &^= world.PlayerFlagBusy
}
