// Package ai 怪物 AI：仇恨打分（每 AggroRescoreIntervalS）+ 四态状态机 +
// 沿 A* 路径移动。设计依据 server-design §7：不上行为树，状态机够用。
//
// 分层：ai 只 import world；参数经 World.Mon（config.Monster 指针，NewWorld
// 装配）传入，因此 ai 不 import config——避免双层导入。
// 空白世界（HubCount=0，HasMap=false）时 room 不会调 ai 系统——Task 3 的
// 短路约定（计划 L1153）；本包函数内不再判 HasMap，因为 M2 老测试不调 ai 包、
// room 的 Monsters.Each 对空池天然 no-op，短路在接线层已满足。
package ai

import (
	"koukoukongjian/internal/game/world"
)

// AggroWeights 仇恨三路权重（server-design §7：0.6 伤害 / 0.3 距离 / 0.1 承伤）。
// 包级常量：调参属 M5 手感阶段，届时挪 config。
const (
	aggroWRecent = 0.6
	aggroWDist   = 0.3
	aggroWTaken  = 0.1
)

// aggroForget 是近期伤害的指数遗忘因子：每次 rescore 旧值 ×0.5。
// 0.5 配 0.5s 重算间隔 ≈ 1 秒半衰期——新伤害能明显压过旧伤害。
const aggroForget = 0.5

// RescoreAggro 重算一只怪的仇恨目标（每 AggroRescoreTicks 一次）。
// 视野 = SightRange 内且 Bresenham 视线可通（LineWalkable 含 supercover，
// 共角不共边的墙缝挡视线）；隔墙不吸怪。
func RescoreAggro(w *world.World, m *world.MonsterState) {
	if w.Mon == nil || w.Terrain == nil {
		return // 防御 config 未装配（HasMap 短路约定在接线层，见包注释）
	}
	for i := range m.AggroDmg {
		m.AggroDmg[i] *= aggroForget
	}

	bestScore := 0.0
	var best world.EntityID
	w.Players.Each(func(pid world.EntityID, p *world.PlayerState) bool {
		if p.State != world.PlayerAlive {
			return true
		}
		dSq := m.Pos.DistSq(p.Pos)
		if dSq > w.Mon.SightRange*w.Mon.SightRange {
			return true
		}
		if !w.Terrain.LineWalkable(m.Pos, p.Pos) {
			return true
		}
		dist := p.Pos.Sub(m.Pos).Len()
		inv := 0.0
		if dist > 0.001 {
			inv = 1 / dist
		}
		score := aggroWRecent*m.AggroDmg[pid.Index()] + aggroWDist*inv + aggroWTaken*p.DamageTaken
		// 平分时锁定 index 更小的玩家（Each 是槽位升序，严格 > 保证稳定）。
		if score > bestScore {
			bestScore = score
			best = pid
		}
		return true
	})

	if !best.IsValid() {
		m.State = world.MonsterIdle
		m.TargetID = world.InvalidID
		m.PathLen = 0
		m.Vel = world.Vec2{}
		return
	}
	m.TargetID = best
	m.State = world.MonsterChase
}

// UpdateMonster 推进一只怪一个子步：状态迁移、寻路节流、沿路径移动、近战攻击。
// dt 是子步秒数；计时器按调用次数自增（名义 tick——room 每 tick 调一次，见
// 汇报偏差清单），攻击冷却与重算节流都挂在 w.AggroRescoreTicks / w.PathRecalcTicks 上。
func UpdateMonster(w *world.World, m *world.MonsterState, dt float64) {
	if m.HP <= 0 {
		return // 尸体不动
	}

	// 计时器推进与重算（每子步判一次，命中才干活）。
	m.AggroRescoreAt++
	if m.AggroRescoreAt >= w.AggroRescoreTicks {
		m.AggroRescoreAt = 0
		RescoreAggro(w, m)
	}
	m.PathRecalcAt++
	recalcDue := m.PathRecalcAt >= w.PathRecalcTicks
	if recalcDue {
		m.PathRecalcAt = 0
	}

	switch m.State {
	case world.MonsterIdle:
		m.Vel = world.Vec2{}
		return

	case world.MonsterChase:
		target := w.Players.Get(m.TargetID)
		if target == nil || target.State != world.PlayerAlive {
			m.State = world.MonsterIdle
			m.TargetID = world.InvalidID
			m.PathLen = 0
			return
		}
		dist := m.Pos.Sub(target.Pos).Len()
		if dist <= w.Mon.AttackRange+float64(m.Radius)+playerRadius {
			m.State = world.MonsterAttack
			m.Vel = world.Vec2{}
			return
		}
		if recalcDue || m.PathLen == 0 {
			refillPath(w, m, target.Pos)
		}
		moveAlongPath(w, m, dt, chaseSpeed(w, m, dist))

	case world.MonsterAttack:
		target := w.Players.Get(m.TargetID)
		if target == nil || target.State != world.PlayerAlive {
			m.State = world.MonsterIdle
			m.TargetID = world.InvalidID
			m.PathLen = 0
			return
		}
		dist := m.Pos.Sub(target.Pos).Len()
		if dist > w.Mon.AttackRange+float64(m.Radius)+playerRadius+0.3 {
			m.State = world.MonsterChase // 脱离攻击范围，回追（0.3 迟滞防边界抖动）
			return
		}
		m.Vel = world.Vec2{}
		if w.Tick-m.AttackAt >= attackCooldownTicks(w) {
			m.AttackAt = w.Tick
			w.ResolveDamage(world.InvalidID, m.TargetID, w.Mon.AttackDamage, world.ElementNone, world.DamageTags{})
		}

	case world.MonsterReturn:
		// v0.1 没有"出生锚点"字段，return 退化为 idle（M4 据点归属时补锚点）。
		m.State = world.MonsterIdle
	}
}

// chaseSpeed 反风筝：目标超过 FastChaseDist 时按倍率加速（server-design §7）。
func chaseSpeed(w *world.World, m *world.MonsterState, dist float64) float64 {
	if dist > w.Mon.FastChaseDist {
		return w.Mon.Speed * w.Mon.FastChaseMul
	}
	return w.Mon.Speed
}

func attackCooldownTicks(w *world.World) uint32 {
	t := uint32(w.Mon.AttackCooldownS*float64(w.TickRate) + 0.5)
	if t == 0 {
		t = 1
	}
	return t
}

// refillPath 重算到目标点的路径，拐点写进 m.Path（定长 64，超出截断）。
func refillPath(w *world.World, m *world.MonsterState, target world.Vec2) {
	path := world.FindPath(w.Terrain, m.Pos, target)
	if path == nil {
		m.PathLen = 0
		return
	}
	n := len(path)
	if n > int(len(m.Path)) {
		n = int(len(m.Path))
	}
	copy(m.Path[:n], path[:n])
	m.PathLen = uint8(n)
	m.PathIdx = 0
	// 0 号点是当前位置，跳过它。
	if m.PathLen > 1 {
		m.PathIdx = 1
	}
}
