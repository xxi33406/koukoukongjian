package systems

import (
	"koukoukongjian/internal/game/world"
)

// GhostPickups 让鬼魂捡起附近的掉落物（只标记 Carrier，不结算）。
//
// spec L45 与 L50 是一条完整规则：怪物掉的血瓶玩家不能捡，唯一入库通道
// 是鬼魂搬运。M4 只写了"玩家不能捡"这一半（pickupTarget 对 Carrier 为
// InvalidID 的血瓶返 nil），搬运动作根本不存在——所以那条规则在当时代码里
// 的表现是"怪掉的血瓶永远捡不到"，玩家会认为血瓶功能没做完。
//
// 不结算的理由：鬼魂不受击、没有血量概念（Task 1 后 HP 恒 0），捡起时
// 顺手 applyPickup 会让血瓶被鬼魂吞掉，复活回来的队友什么也拿不到。
//
// 每 tick 调一次，不进子步：搬运的语义是"走到东西旁边"，不需要 60Hz 精度；
// 而池遍历在子步里做（SubSteps=3）是三倍开销，收益为零。
//
// 这里同时承担"放开无效 Carrier"的职责（搬运者消失或已复活）：两件事
// 放在同一个遍历里，因为它们都要求"每个掉落物每 tick 检查一次 Carrier
// 是否还指向一个正在当鬼魂的玩家"。
func GhostPickups(w *world.World) {
	if w == nil {
		return
	}
	radiusSq := float64(PickupRadius) * float64(PickupRadius)

	w.Pickups.Each(func(_ world.EntityID, pk *world.PickupState) bool {
		if pk.Carrier != world.InvalidID {
			// 已经有搬运者：只检查它是否还有效。
			carrier := w.Players.Get(pk.Carrier)
			if carrier == nil || carrier.State != world.PlayerGhost {
				// 搬运者不在了（掉线/离开/池回收）或已复活：放开，让别人能捡。
				// 不放开的话这个掉落物成了永久占位，而它的 30 秒寿命到期回收
				// 时又会被当成"取消搬运"处理，症状是掉落物提前消失。
				//
				// 复活瞬间不自动转交给最近的活人：那会让刚复活的玩家莫名
				// 多出一堆东西，而他此刻最需要的是看清自己在哪。
				pk.Carrier = world.InvalidID
			}
			return true
		}

		// 金币不给鬼魂：M5 不再产生金币掉落物（击杀直给），但池里可能残留
		// （测试、旧逻辑）。鬼魂搬金币没有接收方——活人路径只在 Carrier
		// 指向自己时结算，而鬼魂永远不会把自己的 Carrier 改成别人。
		if pk.Kind == world.PickupGold {
			return true
		}

		// 找 PickupRadius 内最近的鬼魂。这里遍历玩家池而不是用 Grid：
		// 掉落物总数上限很低（一次据点战几十个），玩家数 <= 4，
		// 总比较次数是几十量级，上网格反而多一层间接。
		//
		// 严格小于配 found 首判定：等距时取槽位顺序先出现的那个，
		// 与 pickupTarget 的取舍一致（确定性来自固定的槽位顺序）。
		var (
			pickerID world.EntityID
			bestSq   = radiusSq
			found    bool
		)
		w.Players.Each(func(id world.EntityID, p *world.PlayerState) bool {
			if p.State != world.PlayerGhost {
				return true
			}
			d := p.Pos.DistSq(pk.Pos)
			if d > radiusSq {
				return true
			}
			if !found || d < bestSq {
				pickerID = id
				bestSq = d
				found = true
			}
			return true
		})
		if !found {
			return true
		}
		pk.Carrier = pickerID
		return true
	})
}

// GhostHandoffs 把鬼魂搬着的东西交给交接范围内的活人。
//
// 交接只改 Carrier，不结算：结算统一走 UpdatePickups 的活人路径。
// 两条结算路径（交接时直接 applyPickup vs 交给活人后由原路径结算）的
// 规则必须同步维护——半径、槽位满、Amount 钳位，改一处漏一处。M4 的
// Task 10 已经为了避免出现第二条改世界路径写过同样的取舍（Interact
// kind=1 只打 Debug）。
//
// 交接半径用 PickupRadius（1.2 米）：与活人自己捡东西的范围一致，
// 鬼魂走到活人身边 1.2 米内东西就"递过去了"，不需要额外的交互键。
// 用更大的半径会让血瓶从远处飞过来，玩家看不出这是鬼魂的功劳。
func GhostHandoffs(w *world.World) {
	if w == nil {
		return
	}
	radiusSq := float64(PickupRadius) * float64(PickupRadius)

	w.Pickups.Each(func(_ world.EntityID, pk *world.PickupState) bool {
		if pk.Carrier == world.InvalidID {
			return true
		}
		carrier := w.Players.Get(pk.Carrier)
		if carrier == nil || carrier.State != world.PlayerGhost {
			// 无效搬运者由 GhostPickups 放开，这里不重复处理：两处都改会让
			// "谁负责清 Carrier"变成两处都要看才能确定的事。
			return true
		}

		// 找交接范围内的活人（最近的那个）
		var (
			receiver world.EntityID
			bestSq   = radiusSq
			found    bool
		)
		w.Players.Each(func(id world.EntityID, p *world.PlayerState) bool {
			if p.State != world.PlayerAlive {
				return true
			}
			d := p.Pos.DistSq(pk.Pos)
			if d > radiusSq {
				return true
			}
			if !found || d < bestSq {
				receiver = id
				bestSq = d
				found = true
			}
			return true
		})
		if !found {
			return true // 附近没有活人，继续拿着
		}
		pk.Carrier = receiver
		return true
	})
}
