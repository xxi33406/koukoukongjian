package ai

import (
	"koukoukongjian/internal/game/world"
)

// playerRadius 与 systems.PlayerRadius 同值（0.3）。
// M4 把玩家半径挪进 PlayerState 后删除本常量——两个包不能互相 import，
// 在此之前各持一份，改动时必须同步。
const playerRadius = 0.3

// moveAlongPath 沿当前拐点走 speed 米/秒。走完一段切下一段；
// 路径耗尽但还没到目标（下一轮 recalc 会补）就原地站住。
// 每一步都过 CircleFree 兜底：string pulling 保视线，但浮点累积误差
// 可能贴墙——撞了就停，等 0.3s 后的路径重算救它。
func moveAlongPath(w *world.World, m *world.MonsterState, dt, speed float64) {
	if m.PathIdx >= uint16(m.PathLen) {
		m.Vel = world.Vec2{}
		return
	}
	step := speed * dt
	for step > 0 && m.PathIdx < uint16(m.PathLen) {
		wp := m.Path[m.PathIdx]
		toWP := wp.Sub(m.Pos)
		d := toWP.Len()
		if d <= step {
			// 能走到这个拐点：先试整段（可能穿过未检测的薄墙角）。
			if w.Terrain.CircleFree(wp, float64(m.Radius)) {
				m.Pos = wp
				m.PathIdx++
				step -= d
				continue
			}
			m.Vel = world.Vec2{}
			return
		}
		dir := toWP.Scale(1 / d)
		next := m.Pos.Add(dir.Scale(step))
		if w.Terrain.CircleFree(next, float64(m.Radius)) {
			m.Pos = next
			m.Vel = dir.Scale(speed)
		} else {
			m.Vel = world.Vec2{} // 卡住：不动，等重算
		}
		return
	}
}
