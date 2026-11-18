package systems

import (
	"math"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/game/world"
)

// 成长系统（M4）。
//
// 经验是队伍范围共享的（spec L111：谁杀都算）——同半径内的存活玩家平分。
// 这样抱团推图不吃亏，独狼也不会被惩罚得太狠；反过来若按击杀者独享，
// 队伍会为了抢怪而散开，与「4 人合作 PvE」的设计相悖。

const (
	// ExpShareRadius 是经验共享半径（米）。
	// 取 20 m ≈ 一个据点空地直径 + 走廊，与怪物视野同量级：超出这个
	// 距离的玩家不在同一场战斗里，分他经验没有道理。
	ExpShareRadius = 20.0

	// ExpNearbyRatio 是范围内「非击杀者」拿到的经验比例。
	//
	// 取 30%：既让抱团时旁观者也有成长（否则「一起推图」等于「我一个人
	// 打」），又不让蹭经验优于亲手打（否则队伍里会有人专职跟着跑不输出）。
	// 这是拍板值不是拟合值——灰盒先定量级，手感阶段再调。
	//
	// 关键点：这 30% 是额外发出去的，不从击杀者的份额里扣。因此队伍的
	// 总经验会随人数膨胀（1 人 100%、4 人 100%+3×30%=190%）。这是刻意的：
	// 合作推图本来就该比单打快，代价是单怪经验不能设得太高，否则 4 人
	// 队伍会一路等级碾压。数值阶段要按「队伍总经验」而不是「单怪经验」
	// 来配 kill_exp。
	ExpNearbyRatio = 0.30

	// LevelUpMaxHPGain 是每级最大生命增量（spec L103：基线 100 血）。
	LevelUpMaxHPGain = 10.0

	// LevelUpDamageGain 是每级基础伤害增量（spec L103：基线 10 伤）。
	// 注意：当前没有「基础伤害」字段，伤害在 weapons.go 的 WeaponTable
	// 与 fire.go 的 spawnBullet 里——本任务只落常量，实际乘区在 Task 5
	// 接 PlayerDerived 时生效（计划 Task 5）。
	LevelUpDamageGain = 2.0
)

// GrantKillExp 把一只怪的经验发给击杀者与范围内的队友，返回本次升级的玩家。
//
// 分配规则（M4 拍板，与 M3 时的平分不同）：
//   - 击杀者拿全额 baseExp，且不受半径限制——是他打死的，跑到哪都算他的。
//   - 范围内其他存活玩家各拿 baseExp × ExpNearbyRatio，这 30% 是额外发的，
//     不从击杀者的份额里扣。
//
// 为什么不再平分：平分会让「多人一起打一只怪」时每个人都觉得亏，而
// 「谁杀都算」的本意是奖励合作、不是惩罚多打。全额+分成的结构让击杀者
// 不吃亏（抢尾刀的动机自然消失），旁观者也有成长。
//
// 传位置而不是怪 id：Reap 会把怪从池里删掉，调用方拿到的 id 可能已失效。
// 位置是稳定的。传 killer 是为了区分全额与分成；killer 无效或已非存活时
// 退化为旧的平分逻辑（M4 生产路径不会走到：kill 事件来自 ApplyHits，
// Src 恒为子弹 OwnerID；但测试与未来的环境伤害会用得上）。
//
// 只算存活玩家（PlayerAlive）：倒地、鬼魂、掉线的人不在战斗里，白送经验
// 会让「躺地上等队友推图」成为一种策略。
func GrantKillExp(w *world.World, pos world.Vec2, killer world.EntityID, baseExp uint32) []world.EntityID {
	radiusSq := ExpShareRadius * ExpShareRadius

	// 击杀者先定位：全额份额走它，且它不该再吃一份 30%。
	killerP := w.Players.Get(killer)
	killerAlive := killerP != nil && killerP.State == world.PlayerAlive

	var leveled []world.EntityID
	share := uint32(float64(baseExp) * ExpNearbyRatio)

	if killerAlive {
		if AddExp(w, killerP, baseExp) {
			leveled = append(leveled, killer)
		}
	}

	// 队友：范围、存活、且不是击杀者本人。
	var ids []world.EntityID
	var targets []*world.PlayerState
	w.Players.Each(func(id world.EntityID, p *world.PlayerState) bool {
		if p.State != world.PlayerAlive {
			return true
		}
		if killerAlive && id == killer {
			return true // 已经按全额发过
		}
		// 距离比较用平方，省开方（与 waves.go 的据点判定同款）。
		if p.Pos.DistSq(pos) > radiusSq {
			return true
		}
		ids = append(ids, id)
		targets = append(targets, p)
		return true
	})

	if !killerAlive {
		// 兜底：没有有效击杀者时按旧规则平分，避免「无人认领的经验」
		// 变成谁都不涨。除不尽的部分丢弃（理由同旧版：不分给某人 1 点，
		// 那会引导抢尾刀）。
		if len(targets) == 0 {
			return leveled
		}
		even := baseExp / uint32(len(targets))
		if even == 0 {
			even = 1 // 经验比人数少时每人至少 1 点，否则经验条不动
		}
		for i, p := range targets {
			if AddExp(w, p, even) {
				leveled = append(leveled, ids[i])
			}
		}
		return leveled
	}

	if share == 0 {
		// 单怪经验太低（baseExp < 4 时 30% 为 0）：不再像平分时那样硬塞
		// 1 点。此时给旁观者 0 是对的——0 点经验与 1 点经验在经验条上
		// 都看不出来，而硬塞 1 点会让 4 人队打 1 点经验的怪各自升得快。
		return leveled
	}
	for i, p := range targets {
		if AddExp(w, p, share) {
			leveled = append(leveled, ids[i])
		}
	}
	return leveled
}

// AddExp 给单个玩家加经验并处理升级，返回是否升级。
//
// 用 for 而不是 if：一次击杀给的经验可能跨过好几级的门槛（后期怪物经验
// 高、或 GM 指令批量发经验），只判一次会让多出来的经验在下一级门槛前
// 卡成负数——表现为「给了 200 点只升 1 级，剩下 180 点消失」。循环里每
// 升一级都必须重算 ExpToNext，否则会拿旧门槛反复触发同一级。
func AddExp(w *world.World, p *world.PlayerState, amount uint32) bool {
	if w == nil || p == nil || w.Prog == nil {
		return false
	}
	if p.Level >= uint8(w.Prog.MaxLevel) {
		// 满级后经验不再增长：留着没有去处（没有等级可换），却会让 HUD
		// 显示一个永远填不满的进度条，看起来像卡住了。
		return false
	}
	p.Exp += amount

	leveled := false
	for p.Exp >= p.ExpToNext && p.Level < uint8(w.Prog.MaxLevel) {
		// ExpToNext 为 0 是未初始化的玩家（没走 handleJoin 的路径）。
		// 此时门槛为 0，循环会一 tick 连升到满级——比不动更难查，所以
		// 直接当「还没进房间」处理，不升级，交给调用方补初始化。
		if p.ExpToNext == 0 {
			break
		}
		p.Exp -= p.ExpToNext
		p.Level++
		p.MaxHP += LevelUpMaxHPGain
		p.HP += LevelUpMaxHPGain
		if p.HP > p.MaxHP {
			// 升级只「回满增量」，不是回满全血：满血时升级恰好等于新上限，
			// 残血时保留残血比例之外的那 10 点。钳位是为了对付非法态
			// （HP 已溢出 MaxHP），否则溢出量会随每次升级累积。
			p.HP = p.MaxHP
		}
		p.ExpToNext = ExpForLevel(w.Prog, p.Level)
		leveled = true
	}
	return leveled
}

// ExpForLevel 返回 level 级升下一级所需经验（几何级数）。
// ExpBase × ExpGrowth^(level-1)，四舍五入。
//
// 四舍五入而不是直接截断：默认配置下 20×1.35² = 36.45，截断会得到 36
// 与取整一致，但 20×1.35 = 27.000000000000004 这类浮点误差在别的参数
// 组合下会让截断少算 1 点（20 变成 19）。+0.5 后再转是唯一不用管误差
// 方向的做法。
func ExpForLevel(prog *config.Progression, level uint8) uint32 {
	if prog == nil {
		return 1
	}
	v := prog.ExpBase * math.Pow(prog.ExpGrowth, float64(level)-1)
	// 浮点转整数越界是平台相关未定义值（AGENTS.md 2.6），必须先钳。
	// 1.35^98 已经超出 uint32 的量级，MaxLevel=99 时这条路径真的会走到。
	if math.IsNaN(v) || v >= float64(math.MaxUint32) {
		return math.MaxUint32
	}
	if v < 0 {
		return 1
	}
	got := uint32(v + 0.5)
	if got == 0 {
		// level 0 / ExpBase<1 之类的退化输入：门槛为 0 会让 AddExp 的
		// 循环一 tick 连升到满级。
		return 1
	}
	return got
}
