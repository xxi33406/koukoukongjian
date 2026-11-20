package systems

import "koukoukongjian/internal/game/world"

// maxBounces 是一颗子弹最多能被反射几次。
//
// 两面反射面对射会让子弹无限来回飞。上限 2 保证它最多被弹两次就退化成
// 普通子弹，最终撞墙或到期消失。数字取 2 而不是 1：一次往返（弹回原主
// 再被另一面弹走）是「反射流」的完整回合，只允许 1 次会让玩家觉得
// 「我的镜子只能用半次」。
const maxBounces = 2

// ReflectBullets 让所有撞到反射面的子弹反弹，返回反射了几颗。
//
// 判定放在子步外（每 tick 一次）而不是子步内：反射改的是子弹的方向，
// 而「同一 tick 内」是 spec 与 server-design §6.3 给交汇定下的时间粒度。
// 放进子步会让同一颗子弹在 3 个子步里被判定 3 次。
//
// 不碰 w.Players：M6 不做 PvP，反射只影响子弹自身。子弹是否会打到玩家
// 由 Collide 决定，而 Collide 现在只查怪物池。
func ReflectBullets(w *world.World) int {
	if w == nil || w.Zones.Len() == 0 {
		return 0
	}

	var reflected int
	var done []world.EntityID // 本轮反射过的子弹，避免同帧被两面镜子各弹一次

	w.Bullets.Each(func(bid world.EntityID, b *world.BulletState) bool {
		if b.Bounces >= maxBounces {
			return true
		}
		for _, id := range done {
			if id == bid {
				return true
			}
		}

		var zone *world.ZoneState
		w.Zones.Each(func(_ world.EntityID, z *world.ZoneState) bool {
			if z.Kind != world.ZoneReflect {
				return true
			}
			r := z.Radius + float64(b.Radius)
			if b.Pos.DistSq(z.Pos) > r*r {
				return true
			}
			zone = z
			return false // 找到第一个就停
		})
		if zone == nil {
			return true
		}

		reflectOff(b, zone)
		b.Bounces++
		done = append(done, bid)
		reflected++
		return true
	})

	return reflected
}

// reflectOff 把子弹沿「区域中心 → 子弹」的法线反射出去。
//
// 用「子弹相对区域中心的方向」当法线而不是用子弹速度求反射向量：
// 反射面是一个圆（不是一面平镜），圆形的反射语义就是「沿径向弹出去」。
// 速度方向与径向相反时（子弹正冲中心飞来）两者的结果一致；斜着飞进
// 来时径向反射会让子弹拐得更多——这是刻意的，圆形镜面本来就会让弹道
// 向外散开，比「只翻速度分量」更容易让玩家看清子弹被弹走了。
func reflectOff(b *world.BulletState, z *world.ZoneState) {
	normal := b.Pos.Sub(z.Pos)
	if normal.LenSq() < 1e-12 {
		// 子弹恰好在圆心：任取一个方向，否则 Normalized 返回 (0,0)，
		// 反射后的速度会是零——子弹原地停住不动，看起来像卡住了。
		normal = world.V(1, 0)
	}
	normal = normal.Normalized()

	speed := b.Vel.Len()
	b.Vel = normal.Scale(speed)

	// 归属改成反射面主人（spec L114）。ResolveDamage 用 OwnerID 记伤害
	// 来源，所以这里必须改；ReflectorID 同时置上，供快照变色与结算页。
	b.OwnerID = z.OwnerID
	b.ReflectorID = z.OwnerID

	// 推到边界外：不推的话下一帧子弹还在区域内，会立刻再被反射一次。
	// 加 0.01 米余量防浮点误差让判定刚好卡在边界上。
	b.Pos = z.Pos.Add(normal.Scale(z.Radius + float64(b.Radius) + 0.01))
	b.PrevPos = b.Pos
}
