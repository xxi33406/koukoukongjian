package build

import "koukoukongjian/internal/game/world"

// ActiveSkillChance 是单次抽取抽到主动技能的概率。
//
// 用户拍板（2026-11-17）：主动被动都有，但主动概率较少。0.30 意味着
// 三选一里平均 0.9 个主动——大部分升级给的是被动数值成长，主动技能
// 相对稀缺，与"主动技能是构筑核心、要靠取舍"的设计一致。
//
// 它在 M4 Task 5 时先落在 systems 包，Task 6 搬到 build：这个数值是
// "候选池怎么抽"的策略参数，与技能表、ID、Kind 同属一份知识；而
// systems 是执行层（把技能效果算到世界状态上），它自己并不抽候选。
// 留在 systems 会让 build 反向依赖 systems 才能读到自己的抽取参数，
// 或者把同一个 0.30 写两遍——两处各写一个数值，改一处不生效，
// 是"分离设计接口的两半"那类根因（AGENTS.md §1.2）。搬过来之后只剩
// 一个来源，systems 侧也不再有引用点。
const ActiveSkillChance = 0.30

// GenerateChoices 生成至多 n 个候选技能，排除玩家已拥有的。
//
// 为什么传 *world.World 而不是 *rand.Rand：模拟路径的随机一律走 World
// 的包装（RandIntn/RandFloat64），包装里带 rngCalls 计数——漏调一次
// 回放就会分叉，而分叉点极难定位（world.go:111-124 的注释记过这笔账）。
// 传 *rand.Rand 也能工作，但那样这个包里的随机就绕过了计数器，
// 世界层面的"这颗种子消耗了几次随机"与回放校验对不上，
// 症状是某次升级的候选与回放不一致，而日志里看不出任何异常。
//
// 抽取策略：每次先掷 ActiveSkillChance 决定从主动池还是被动池取；
// 该池空了就从另一池取。这样主动技能的期望占比接近 ActiveSkillChance，
// 又不至于在某一池耗尽时给不出 n 个候选。
//
// 为什么是"每次掷骰"而不是"按配额固定 0.9 个主动"：配额要求预先知道
// 池子里各有多少个、还要把小数余量摊到多次升级上（这次 0 个下次 1 个），
// 状态就成了跨升级的隐变量，一旦某次升级没被选择（玩家掉线、超时跳过），
// 余额往哪儿记就说不清了。每次独立掷骰只依赖当前池状态，没有跨调用状态。
//
// 返回数量可能少于 n，甚至为 0：池子耗尽时返回实际数量，不补齐、不重复。
// 补齐（从已装备里回填）会让玩家看到"已经拥有的技能还能再选一次"，
// 比空候选更糟；调用方（Task 8）按 len 判断，0 个时改给数值奖励或跳过。
//
// 性能上 n 是 3、技能表 8 条，线性查找与切片删除完全够用；
// 这里一律走切片，不建 map 再把 map 遍历一遍（AGENTS.md §2.6：
// map 遍历顺序随机，模拟路径里遍历会让回放不可复现）。
func GenerateChoices(w *world.World, owned []uint16, n int) []Skill {
	// nil 世界直接返回空：参数是指针，nil 是合法值；候选生成会被升级流程
	// 调用，那里若"世界还没建好就来要候选"就 panic，整局会挂掉。
	if w == nil || n <= 0 {
		return nil
	}

	active := unownedSkills(ActiveSkills(), owned)
	passive := unownedSkills(PassiveSkills(), owned)

	// 预分配容量取 min(n, 可选总数)：n 可能很大，而池子只有 8 条。
	total := len(active) + len(passive)
	if n > total {
		n = total
	}
	if n == 0 {
		return nil
	}

	out := make([]Skill, 0, n)
	for len(out) < n {
		var pool *[]Skill
		switch {
		case len(active) == 0 && len(passive) == 0:
			// 两池都空：循环条件也该结束了，这里兜底防死循环。
			return out
		case len(active) == 0:
			pool = &passive
		case len(passive) == 0:
			pool = &active
		default:
			// 只有两池都非空时掷骰：一池已空还掷一次，纯属白白消耗随机数，
			// 会让"同一颗种子消耗几次"多出一段没有语义的起伏——回放校验
			// 只比对次数，白消耗不会报错，但会让后来读日志的人以为这里
			// 每次都要掷骰，改实现时容易顺手把次数改掉。
			if w.RandFloat64() < ActiveSkillChance {
				pool = &active
			} else {
				pool = &passive
			}
		}

		// 下标删除：抽中的位置用最后一个补上，顺序不再是稳定顺序——
		// 但池子本来就是随机取，顺序无意义，而 O(1) 删除不必搬整片。
		i := w.RandIntn(len(*pool))
		out = append(out, (*pool)[i])
		last := len(*pool) - 1
		(*pool)[i] = (*pool)[last]
		*pool = (*pool)[:last]
	}
	return out
}

// unownedSkills 返回 pool 里不在 owned 中的技能（新切片）。
//
// 每个候选对 owned 做一次线性扫描，而不是先建 map：8 条技能、每局几个
// 已装备，线性足够；且这里天然是切片遍历，不存在 §2.6 的 map 顺序问题。
// 建 map 只为了省这点扫描，反而要多一份"map 会不会被遍历"的审查负担。
func unownedSkills(pool []Skill, owned []uint16) []Skill {
	out := make([]Skill, 0, len(pool))
	for _, s := range pool {
		if !containsSkillID(owned, s.ID) {
			out = append(out, s)
		}
	}
	return out
}

func containsSkillID(ids []uint16, id uint16) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
