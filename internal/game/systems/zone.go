package systems

import (
	"math"

	"koukoukongjian/internal/game/world"
)

// UpdateZones 递减所有区域的剩余寿命并回收到期的。
//
// 每 tick 调一次（不是每子步）：寿命是「秒」语义，按 tick 扣与
// UpdatePickups 的 TTL 同节拍。放子步里会让 SubSteps=3 时区域以三倍速
// 消失，而配置里写的寿命看起来完全正确——M4 的掉落物踩过同一个坑。
func UpdateZones(w *world.World, dt float64) {
	if w == nil || dt <= 0 || math.IsNaN(dt) || math.IsInf(dt, 0) {
		return
	}

	// 先收集再统一 Kill：Pool.Each 按槽位下标走，边遍历边 Kill 会把
	// 槽位挂回 free 队列，后续遍历可能重复取到同一个槽位。
	var gone []world.EntityID
	w.Zones.Each(func(id world.EntityID, z *world.ZoneState) bool {
		z.Life -= dt
		if z.Life <= 0 {
			gone = append(gone, id)
		}
		return true
	})
	for _, id := range gone {
		w.Zones.Kill(id)
	}
}

// TickZoneSlow 每子步重算玩家与怪物的区域减速倍率。
//
// 必须每子步重算而不是只在进入时设置：走出蒸汽云的玩家要立刻恢复速度。
// 若只在进入时写、离开时不重置，ZoneSlowMul 会永远停在进入时的值，
// 玩家会带着减速走完这一局——而那条路径上没有任何报错。
//
// 重置必须在遍历之前：否则刚离开云的实体保留上一子步的减速值。
func TickZoneSlow(w *world.World) {
	if w == nil {
		return
	}
	if w.Zones.Len() == 0 {
		// 快路径：没有区域时只做重置，不遍历区域池。
		w.Players.Each(func(_ world.EntityID, p *world.PlayerState) bool {
			p.ZoneSlowMul = 1.0
			return true
		})
		w.Monsters.Each(func(_ world.EntityID, m *world.MonsterState) bool {
			m.ZoneSlowMul = 1.0
			return true
		})
		return
	}

	w.Players.Each(func(_ world.EntityID, p *world.PlayerState) bool {
		p.ZoneSlowMul = 1.0
		// 鬼魂不吃减速：它已经只有 0.8 倍速而且不能战斗，再减速会让
		// 「可探图、可跟随」（spec L49）这两件事都做不到。
		if p.State != world.PlayerAlive {
			return true
		}
		w.Zones.Each(func(_ world.EntityID, z *world.ZoneState) bool {
			if !slowsEntity(z) {
				return true
			}
			if p.Pos.DistSq(z.Pos) > z.Radius*z.Radius {
				return true
			}
			// 取最强者而不是相乘：三片云相乘会叠加成 0.216 倍速，
			// 玩家看不出「为什么三片云这么强」。重叠区域的强度就是
			// 最强的那一片，语义单一。
			if z.SlowMul < p.ZoneSlowMul {
				p.ZoneSlowMul = z.SlowMul
			}
			return true
		})
		return true
	})

	w.Monsters.Each(func(_ world.EntityID, m *world.MonsterState) bool {
		m.ZoneSlowMul = 1.0
		if m.IsDead() {
			return true
		}
		w.Zones.Each(func(_ world.EntityID, z *world.ZoneState) bool {
			if !slowsEntity(z) {
				return true
			}
			r := z.Radius + float64(m.Radius)
			if m.Pos.DistSq(z.Pos) > r*r {
				return true
			}
			if z.SlowMul < m.ZoneSlowMul {
				m.ZoneSlowMul = z.SlowMul
			}
			return true
		})
		return true
	})
}

// slowsEntity 报告一片区域是否对站在里面的实体减速。
//
// 三个条件缺一不可：是蒸汽云、倍率已设成「比 1 更慢」、倍率是正数。
// SlowMul >= 1 直接跳过而不是「乘上它」：1.0 是 SpawnZone 给非蒸汽区域
// 的默认值，>1 则是加速——两种都表示「这片区域不管速度」。
func slowsEntity(z *world.ZoneState) bool {
	return z.Kind == world.ZoneSteam && z.SlowMul > 0 && z.SlowMul < 1
}
