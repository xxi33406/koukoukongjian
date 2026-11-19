package systems

import (
	"koukoukongjian/internal/game/world"
)

// CheckTeamWipe 判定团灭并保证只上报一次，返回本次是否新成立。
//
// 判据（spec L52）：房间里有玩家，且所有玩家都不是 PlayerAlive。
//   - PlayerGhost 算团灭（"全队变鬼魂"）
//   - PlayerDowned 也算（倒地没人扶，实质等于死）
//   - PlayerDisconnected 也算（spec L128「掉线即鬼魂」）
//
// 空房间不算：Players.Each 在空池上零次回调，若实现写成"allGhost 初始
// true 遍历后直接用"，房间在等玩家的第一秒就会广播失败。
//
// 复活后自动清 Wiped：不清的话第二次团灭不报，而"死后被队友救起来接着打"
// 在灰盒里是最常见的路径，不是边缘情况。
func CheckTeamWipe(w *world.World) bool {
	if w == nil {
		return false
	}

	anyAlive := false
	total := 0
	w.Players.Each(func(_ world.EntityID, p *world.PlayerState) bool {
		total++
		if p.State == world.PlayerAlive {
			anyAlive = true
			return false // 已经有人活着，不必再看
		}
		return true
	})

	if total == 0 || anyAlive {
		// 复活的人让本局重新"未团灭"。放在这里而不是 ReviveTick 里：
		// 判据与复位的条件必须是同一个（"有人在活着"），写在两处就会分叉。
		w.Wiped = false
		return false
	}
	if w.Wiped {
		return false // 已经报过
	}
	w.Wiped = true
	return true
}
