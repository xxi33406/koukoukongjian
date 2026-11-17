package systems

import (
	"math"

	"koukoukongjian/internal/game/world"
)

// maxCatchUpShots 限制一帧最多补发几发，防止武器冷却被改错时打爆子弹池。
const maxCatchUpShots = 8

// Fire 按玩家手里的武器处理一次开火尝试。
//
// attackHeld 是这一 tick 的按键状态（「当前按住」），不是「刚按下」。
// 单发武器的「刚按下」由 p.PrevAttackHeld 与它比较算出——客户端发来的位
// 经过 bridge 的窗口按位或，不能直接当按下沿用。
//
// dt 是这一 tick 的时长（秒），用于递减冷却。用固定值 dt=1/tickRate，
// 绝不用真实耗时，否则同一输入的模拟结果会随机器负载变化。
//
// 返回是否真的发射了子弹，供测试与指标用。
func Fire(w *world.World, id world.EntityID, p *world.PlayerState, attackHeld, aimIsManual bool, dt float64) bool {
	wep, ok := WeaponByID(p.WeaponID)
	if !ok {
		return false
	}
	return fireWith(w, id, p, wep, attackHeld, aimIsManual, dt)
}

// fireWith 是 Fire 的实际实现，武器由参数传入。
//
// 拆出来的理由：测试需要试「冷却比 tick 短」这类参数组合，而唯一的
// 其它做法是临时改全局 WeaponTable。改全局表会在并行测试之间互相污染，
// 而且污染表现为「另一个测试偶尔失败」，很难定位。
func fireWith(w *world.World, id world.EntityID, p *world.PlayerState, wep Weapon, attackHeld, aimIsManual bool, dt float64) bool {
	if p == nil || p.State != world.PlayerAlive {
		return false
	}

	// 零冷却会让下面的补发循环变成死循环（cooldown += 0 永远 <= 0）。
	if wep.Cooldown <= 0 {
		return false
	}

	prevHeld := p.PrevAttackHeld
	p.PrevAttackHeld = attackHeld

	// 冷却始终按固定 dt 递减，且允许变成负数——负数表示「这一 tick 里
	// 欠了几发」，下面的补发循环负责还清。例如冷却 0.02、tick 0.05 时，
	// 一个 tick 内本该打 2.5 发，只有让 cooldown 走到负数才可能循环多次。
	//
	// 但如果写 if p.FireCooldown > 0 { p.FireCooldown -= dt } 再把负值钳回 0，
	// cooldown 从 0.02 减 0.05 会直接落到 0，补发循环打一发就变回 0.02 并退出，
	// 实际射速被静默压到 20 发/秒（理论 50 发/秒），而没有任何报错。
	//
	// 也不能完全不管下界：松开攻击键时这里同样在递减，松 10 秒 cooldown
	// 会变成 -10，再按下时第一帧一口气吐到 maxCatchUpShots（8 发），
	// 表现为「松开一会儿再开火，枪口喷出一串子弹」。
	// 所以把下界钳在 -dt：最多欠一个 tick 的量，既保住补发又不积攒欠账。
	p.FireCooldown -= dt
	if p.FireCooldown < -dt {
		p.FireCooldown = -dt
	}

	if !attackHeld {
		return false
	}

	// 单发武器只在按下沿开火。连发武器按住就开火。
	if !wep.AutoFire && prevHeld {
		return false
	}

	// 自动瞄准在循环外算一次：一次开火决策只定一次方向。
	// 放进补发循环里会在补发多发时重复做全池遍历，而且若两只怪等距，
	// 每轮都可能选中不同的，枪口就在两怪之间跳。
	if !aimIsManual {
		p.AimDir = ResolveAim(w, p, p.Pos, false)
	}

	fired := false
	shots := 0
	for p.FireCooldown <= 0 && shots < maxCatchUpShots {
		if !spawnBullet(w, id, p, wep) {
			break // 子弹池满，剩下的留到下一 tick，别死循环
		}
		fired = true
		p.FireCooldown += wep.Cooldown
		shots++
	}
	return fired
}

// spawnBullet 从玩家位置朝 AimDir 发射一颗子弹。
//
// 返回 false 表示子弹池已满。满了就不发而不是挤掉最老的：挤掉最老会让
// 玩家看到自己的子弹凭空消失，而「这一发没打出来」符合直觉。
func spawnBullet(w *world.World, owner world.EntityID, p *world.PlayerState, wep Weapon) bool {
	if w.Bullets.Len() >= w.MaxBullets {
		return false
	}
	_, b := w.Bullets.Spawn()
	if b == nil {
		return false
	}

	dir := world.V(math.Cos(p.AimDir), math.Sin(p.AimDir))

	// 枪口偏移：从玩家中心再往前推 0.5 m，否则子弹出生点就在玩家
	// 自己的碰撞体里，扫掠判定的起点会落在自己身上。
	const muzzleOffset = 0.5
	b.Pos = world.V(p.Pos.X+dir.X*muzzleOffset, p.Pos.Y+dir.Y*muzzleOffset)
	b.PrevPos = b.Pos
	b.Vel = world.V(dir.X*wep.BulletSpeed, dir.Y*wep.BulletSpeed)
	b.Radius = wep.BulletRad
	b.Life = float32(wep.BulletLife) // Weapon.BulletLife 是 float64，BulletState.Life 是 float32
	// 被动「强化弹头」在这一行生效：子弹把开火那一刻的伤害倍率**快照**进去，
	// 之后无论玩家怎么换技能，这颗已经在飞的子弹都按原来的伤害结算。
	//
	// 为什么是子弹快照而不是在 ApplyHits 里乘来源的当前倍率：技能可换
	// （spec L37），同一颗子弹从出膛到命中之间玩家可能换掉被动，若在命中
	// 时才取倍率，一颗"装强化弹头时打出"的子弹会在飞行途中掉伤害。这与
	// 冲刺用 SpeedBuffMul 快照是同一条原则：已生效的效果按施放时的数值走完。
	//
	// Derived 走 Effective()：还没重算过的玩家倍率是零值，直接乘会让子弹
	// 零伤害，而且完全不报错（见 world.PlayerDerived.Effective）。
	b.Damage = wep.Damage * p.Derived.Effective().DamageMul
	b.OwnerID = owner
	b.Element = wep.Element
	b.Pierce = 0
	return true
}
