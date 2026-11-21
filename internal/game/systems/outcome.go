package systems

import "koukoukongjian/internal/game/world"

// CheckOutcome 判定本局是否已经结束（每 tick 一次，tick 循环里、fire 之后）。
//
// 终态优先级：团灭 > 通关 > 未结束。
//
// 为什么团灭优先：最后一 tick 里"打死 Boss 后被残弹打死"是真会发生的。
// 算团灭而不是通关，因为 spec L52 把"全队变鬼魂"定义为本局失败——
// 一个人都没活下来时"通关"没有意义（没人能进下一关）。
//
// 为什么"未结束"必须能表达：房间因进程被 kill、或玩家全部离开而关闭时，
// 既没团灭也没通关。硬塞成两者之一会让战绩统计出现"莫名其妙的一局胜利"。
//
// 判据同源纪律：团灭判定必须复用 CheckTeamWipe 的判据（它已经处理了
// "空房间不算团灭"与"Wiped 复位"两件事），不在这里重写一遍遍历。
func CheckOutcome(w *world.World) world.MatchOutcome {
	if w == nil {
		return world.OutcomeUnknown
	}

	// 团灭优先。CheckTeamWipe 是"判据 + 只上报一次"的合并函数，
	// 这里不能调它——它会改写 w.Wiped 并发事件。所以用只读版本
	// teamAllGhosted 判据，复用同一份遍历逻辑。
	if teamAllGhosted(w) {
		return world.OutcomeWiped
	}

	// 通关：全部据点清完 + Boss 已死。
	//
	// 无图世界（HasMap=false，HubCount=0 的哨兵世界与 mapgen 降级世界）
	// 永远不通关：它没有据点，len(Hubs)==0 会让下面的循环空转得出
	// "全部清完"，于是"没有地图"会变成"已通关"——一个测试世界被判赢。
	if !w.HasMap || len(w.Hubs) == 0 {
		return world.OutcomeUnknown
	}
	for i := range w.Hubs {
		if w.Hubs[i].State != world.HubCleared {
			return world.OutcomeUnknown
		}
	}
	if !w.BossDead {
		return world.OutcomeUnknown
	}
	return world.OutcomeCleared
}

// teamAllGhosted 只读地判断"是否有玩家且全部非 Alive"。
//
// 从 CheckTeamWipe 里抽出来的判据（M7）。抽出来而不是让 CheckOutcome
// 调 CheckTeamWipe：后者有副作用（写 w.Wiped、决定要不要发事件），
// 判定与上报是两件事，混在一起会让"这一 tick 到底算不算新团灭"
// 取决于调用顺序。
func teamAllGhosted(w *world.World) bool {
	total, alive := 0, 0
	w.Players.Each(func(_ world.EntityID, p *world.PlayerState) bool {
		total++
		if p.State == world.PlayerAlive {
			alive++
		}
		return true
	})
	return total > 0 && alive == 0
}
