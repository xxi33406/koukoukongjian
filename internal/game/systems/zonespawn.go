package systems

import "koukoukongjian/internal/game/world"

// SpawnZone 在世界里放一片区域，返回它的 id（池满时返回 InvalidID）。
//
// 抽成一个函数而不是每处直接调 w.Zones.Spawn：六个字段必须一起写对
// （Pos/Radius/Life/Kind/OwnerID/Element），漏一个的症状分别是——漏 Pos
// 区域出现在原点、漏 Life 立刻消失、漏 OwnerID 反射后归属是 InvalidID
// 导致伤害记不到任何人头上。集中一处写，测试也只需要钉这一处。
func SpawnZone(
	w *world.World,
	pos world.Vec2,
	radius, life float64,
	kind world.ZoneKind,
	owner world.EntityID,
	element world.Element,
) world.EntityID {
	if w == nil {
		return world.InvalidID
	}
	id, z := w.Zones.Spawn()
	if z == nil {
		// 池满：静默丢弃。区域是表现层增强，丢掉一条蒸汽云不会让
		// 游戏不可玩；为它 panic 或阻塞 tick 都不值得。
		return world.InvalidID
	}
	z.Pos = pos
	z.Radius = radius
	z.Life = life
	z.Kind = kind
	z.OwnerID = owner
	z.Element = element
	// SlowMul 只在蒸汽云上有意义，其它类型留 1.0 表示「不减速」。
	// 留 0 会让 TickZoneSlow 里 speed *= 0 把玩家完全冻住。
	z.SlowMul = 1.0
	return id
}
