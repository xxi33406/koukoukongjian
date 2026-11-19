package systems

import (
	"koukoukongjian/internal/game/world"
)

// AltarRadius 是"站在祭坛边"的判定半径（米）。
//
// 2.5 的取法依据据点空地：HubRadiusCells=5 米是据点空地半径，祭坛在中心，
// 2.5 米保证整个判定圆都在空地上，不会有人"卡在墙边但算在祭坛内"。
// 也不能太大：祭坛判定圆盖住整个据点的话，"走出范围才打断"就退化成
// "走出据点才打断"，而据点半径 5 米在 20Hz 下要跑 0.5 秒才会断，
// 玩家按了方向键却还在引导，表现是"我明明动了为什么没断"。
//
// 注意 Hub.RadiusM 是波次触发半径（5 米），不是祭坛交互半径——接线时
// 不要图省事把它传进来。
const AltarRadius = 2.5

// ReviveSeconds 是复活引导时长（秒）。
//
// 3 秒 = 60 tick。取法：spec L51 写"引导数秒 + 受击打断"，M5 改成"走出
// 范围打断"。3 秒在战斗中足够让队友赶过来护法（spec L51 的"名场面"），
// 又不至于让"刚清完据点的安全复活"等得心烦。开背包的引导是 2 秒
// （systems/inventory.go 的 OpenInventorySeconds），复活比它长是因为代价
// 更大——复活是团队资源。
const ReviveSeconds = 3.0

// ReviveBegin 让祭坛范围内的鬼魂开始复活引导。
//
// 返回 false 的四种情形：不是鬼魂、不在祭坛范围、没有地图、已经在引导中。
// 「已经在引导中」返回 false 而不是重置计时：重置会让玩家靠反复按交互键
// 把引导永远停在"刚开始"，而那时进度条一直在走也没有意义。同一 tick 内
// 重复调用返回 false 是幂等语义的一部分。
func ReviveBegin(w *world.World, p *world.PlayerState, endAt uint32) bool {
	if p == nil || w == nil {
		return false
	}
	if p.State != world.PlayerGhost {
		return false
	}
	if p.ReviveAt != 0 {
		return false // 已经在引导中
	}
	if _, ok := w.NearestAltar(p.Pos, AltarRadius); !ok {
		return false // 不在祭坛范围（含没有地图）
	}
	if endAt == 0 {
		// 调用方传 0 有两种可能：算错了时长、或者这个 tick 就是第 0 tick。
		// 前者要能看到引导立即完成（会暴露时长算错），后者只是一种边界。
		// 钳到 1 让两种情形都变成"下一 tick 完成"——比让引导永远不完成好。
		endAt = 1
	}
	p.ReviveAt = endAt
	p.Flags |= world.PlayerFlagBusy
	return true
}

// ReviveTick 推进复活引导，到点复活并返回 true。
//
// 三种退出：离开祭坛范围 → 打断；玩家给了移动输入 → 打断（实现补的规则，
// 见测试 TestReviveCancelByMove 的理由）；到点 → 复活。
//
// 复活给半血（用户规格未指定，取"死亡有代价"的最小表达）：满血复活会让
// 死亡没有代价，spec L51 的"战斗中复活 = 全队护法的名场面"就不成立——
// 反正复活完是满血，队友护法是白护的。半血让复活后的第一件事变成找血瓶。
func ReviveTick(w *world.World, p *world.PlayerState, now uint32) bool {
	if p == nil || w == nil || p.ReviveAt == 0 {
		return false
	}

	// 位置条件优先：玩家走出范围是最常见的打断，也是唯一有明确视觉反馈的。
	if _, ok := w.NearestAltar(p.Pos, AltarRadius); !ok {
		interruptRevive(p)
		return false
	}
	// 移动打断：判 MoveIntent 而不是判 Pos 有没有变——鬼魂被拴绳钳位时
	// 位置会自己变，那不是玩家想动。
	if p.MoveIntent.LenSq() > 0 {
		interruptRevive(p)
		return false
	}
	if now < p.ReviveAt {
		return false
	}

	// 到点
	p.ReviveAt = 0
	p.Flags &^= world.PlayerFlagBusy
	p.State = world.PlayerAlive
	p.HP = p.MaxHP * 0.5
	// 复活瞬间给一个短暂无敌：不给会被刚复活的同一 tick 的怪（或残留子弹）
	// 立刻打死，而玩家看不到"我复活了但立刻又死了"之间的因果关系，只会
	// 觉得复活没用。1 秒≈20 tick 足够走两步。
	p.InvulnLeft = 1.0
	return true
}

// interruptRevive 是引导打断的统一出口：清两个字段。
//
// 提出来是因为有三处打断（出范围、移动、以及将来可能的受击），
// 分散写必然有一处漏清 Busy 位，症状是"鬼魂复活不了但也不能开背包"。
func interruptRevive(p *world.PlayerState) {
	p.ReviveAt = 0
	p.Flags &^= world.PlayerFlagBusy
}

// ReviveTicks 把秒换算成 tick 数（下限 1）。
//
// 与 world.secondsToTicks 同款，但它不导出（world 包内部用），而
// room 层要算引导结束点，所以这里复一份。不直接复用是为了让 systems
// 不依赖 world 的未导出函数（那需要把它改成导出，而它的语义是
// "世界的 tick 换算"，属于 world 包自己）。
func ReviveTicks(rate float64) uint32 {
	if rate <= 0 || rate != rate { // NaN 判定
		return 1
	}
	n := uint32(ReviveSeconds*rate + 0.5)
	if n == 0 {
		return 1
	}
	return n
}
