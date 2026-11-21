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

	// 判据与"是否已经报过"必须同源（M5 注释），而判据本身在 M7 抽到
	// teamAllGhosted 供 CheckOutcome 只读复用——两处各写一份遍历，
	// 将来判据改了（比如把 PlayerDisconnected 排除）会只改到一半。
	if !teamAllGhosted(w) {
		// 有活人（或空房间）：复位，下一轮团灭还会再报。
		// 放在这里而不是 ReviveTick 里：判据与复位的条件必须是同一个
		// （"有人在活着"），写在两处就会分叉。
		w.Wiped = false
		return false
	}
	if w.Wiped {
		return false // 已经报过
	}
	w.Wiped = true
	return true
}
