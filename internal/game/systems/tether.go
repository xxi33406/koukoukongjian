package systems

import (
	"koukoukongjian/internal/game/world"
)

// TetherGhosts 把超出拴绳半径的鬼魂拉回到球面上的最近点。
//
// 每 tick 调一次（不是每子步）：钳位是"事后修正"，它要作用于本 tick 全部
// 移动的结果。放进子步会在每个子步末拉一次，鬼魂的中途位置被反复改写，
// 表现为"鬼魂走路一顿一顿"——而那种抖动在 20Hz 快照里看不出来，
// 只有联调的人亲手掌舵才发现。
//
// 算法刻意简单：找最近活人 → 超界就沿方向拉回。不做视线检查、不做
// 绕障寻路——鬼魂被拴住时玩家会自己往队伍方向走，而"鬼魂被墙卡住"
// 的解是玩家走近它，不是系统替它绕路（绕路会把鬼魂送到玩家看不见的
// 地方，那比卡住更糟）。
func TetherGhosts(w *world.World) {
	if w == nil {
		return
	}
	radius := w.GhostTether
	if radius <= 0 {
		return // 没配或不拴
	}
	radiusSq := radius * radius

	w.Players.Each(func(_ world.EntityID, p *world.PlayerState) bool {
		if p.State != world.PlayerGhost {
			return true
		}

		// 找最近的活人。没有活人时跳过：团灭后所有鬼魂都该能自由走，
		// 否则它们连祭坛都到不了，复活循环彻底死锁。
		var anchor *world.PlayerState
		bestSq := 0.0
		w.Players.Each(func(_ world.EntityID, other *world.PlayerState) bool {
			if other.State != world.PlayerAlive {
				return true
			}
			d := p.Pos.DistSq(other.Pos)
			if anchor == nil || d < bestSq {
				anchor = other
				bestSq = d
			}
			return true
		})
		if anchor == nil {
			return true
		}
		if bestSq <= radiusSq {
			return true
		}

		// 硬钳位：沿"活人 → 鬼魂"方向放到球面最近点。
		// Normalized 对零向量返回什么要看实现——这里 bestSq > radiusSq > 0
		// 保证了两点不重合，方向一定非零。
		dir := p.Pos.Sub(anchor.Pos).Normalized()
		p.Pos = anchor.Pos.Add(dir.Scale(radius))
		// 速度也清掉：下一 tick 由 Integrate 按当前输入重算。不清的话
		// 快照下发的速度与钳位后的实际位移方向不一致，客户端插值会抽搐。
		p.Vel = world.Vec2{}
		return true
	})
}
