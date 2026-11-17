package systems

import (
	"math"

	"koukoukongjian/internal/game/world"
)

// AimLockRange 是自动瞄准的最大锁定距离，米。
//
// 取值依据：步枪射程 = 20 m/s × 1.2 s = 24 m。锁定范围不能超过射程
// （锁定了也打不中），取 15 m 让锁定的目标一定在弹道能覆盖的范围内。
// 太小（<8 m）会让玩家觉得"明明看得见却锁不上"。
const AimLockRange = 15.0

// NearestMonster 找出 from 为圆心、maxRange 为半径内最近的活怪。
//
// 遍历整个池而不是查空间索引：池里通常个位数怪物，而网格（
// internal/game/world/grid.go:101）把玩家/怪物/子弹混存且不带池标识，
// 查出来分不清是什么。实测 200 实体以内暴力更快（decisions §4）。
//
// 等距时取池下标小的：必须稳定，否则同一 tick 重放两次会选出不同目标，
// 回放与确定性测试都会失效。池的 Each 是下标升序，配合严格小于即可。
func NearestMonster(w *world.World, from world.Vec2, maxRange float64) (world.EntityID, bool) {
	best := world.InvalidID
	bestSq := maxRange * maxRange
	found := false

	w.Monsters.Each(func(id world.EntityID, m *world.MonsterState) bool {
		// 瞄准一具尸体没有意义。判据是显式标志而不是 HP<=0：M4 起
		// 治疗能把 HP 拉回正数，见 world.MonsterFlagDead。
		if m.IsDead() {
			return true
		}
		d := from.Sub(m.Pos).LenSq()
		// 严格小于：等距时不替换，保住先遍历到的那只。
		if d < bestSq {
			bestSq = d
			best = id
			found = true
		}
		return true
	})
	return best, found
}

// ResolveAim 决定这一帧开火该用哪个方向。
//
// manual 表示客户端这一帧给了方向（aim_x/aim_y 非零）。给了就用手给的，
// 一个百分点都不改——这是用户 2026-11-12 拍板的"纯手动指哪打哪"。
//
// 只有没给方向时才走自动瞄准：找最近的活怪，把枪口转过去。找不到就保持
// 原朝向（p.AimDir），不要归零——归零等于把角色强行转向 +X，视觉上抽搐。
func ResolveAim(w *world.World, p *world.PlayerState, from world.Vec2, manual bool) float64 {
	if manual {
		return p.AimDir
	}
	id, ok := NearestMonster(w, from, AimLockRange)
	if !ok {
		return p.AimDir
	}
	m := w.Monsters.Get(id)
	if m == nil {
		return p.AimDir
	}
	d := m.Pos.Sub(from)
	if d.LenSq() < 1e-12 {
		// 怪和玩家重叠：atan2(0,0)=0，会让枪口突然转向 +X。
		return p.AimDir
	}
	return math.Atan2(d.Y, d.X)
}
