package systems

import (
	"koukoukongjian/internal/game/build"
	"koukoukongjian/internal/game/world"
)

// 掉落概率（用户拍板决定 2：血瓶 15%、技能 5%，两条独立掷骰）。
//
// 为什么不放在 config 里：M2 的武器表、M4 的技能表都走的"代码常量表"
// 路线（用户已拍板"配置先代码常量表，M7 再外置 JSON"）。掉落概率属于
// 数值调参，与技能数值同性质，跟着走同一条路线，不单独为它开一组配置
// ——那会让"哪些数值在 config、哪些在代码"变成一件要靠记忆的事。
const (
	// DropHealthChance 是每次击杀掉血瓶的概率。
	//
	// 15% 的依据：一次据点遭遇战约 10~20 只怪（Waves.MonstersPerWave=4 ×
	// 数波），期望 1.5~3 个血瓶。血瓶回 30，玩家 100 血，这个量级够一次
	// 小失误、不够浪。
	DropHealthChance = 0.15

	// DropSkillChance 是每次击杀掉技能掉落物的概率。
	//
	// 5% 刻意压低：spec L89 的位置死亡技能是 M6 差异化核心（"怪死于特定
	// 地点掉落特定技能"），M5 这里的技能掉落只是让鬼魂搬运有事可做。
	// 掉率高会废掉 M6 的"在哪杀"设计——满地都是技能，位置就不重要了。
	DropSkillChance = 0.05
)

// DropHealthAmount 是血瓶恢复量。
//
// 30 与 M4 治疗技能的 Amount 同量级：一次捡满回三成血，让"捡血瓶"成为
// 一个有意义的决策点，而不是捡十个才见效。
const DropHealthAmount = 30

// SpawnDrops 在 pos 处按两条独立骰子决定掉落物。
//
// element 是「怪死在哪里」的元素属性：据点有属性时只掉该元素的技能，
// 无属性（或该元素没有技能）时回退到全部主动技能。这是 spec L18
// 「空间即资源」的实现点——死在哪决定掉什么。
//
// 两条概率独立判定（用户明确"两种会同时出现"）：先掷血瓶、再掷技能，
// 互不影响。写成 if/else 会让两者互斥，而互斥在单次击杀上看不出来，
// 只有统计能发现——所以 drop_test.go 里有 TestDropRates 钉概率值。
//
// 池满时静默丢弃（SpawnPickup 自己会返 InvalidID）：掉落是奖励，
// 池满说明屏幕上已经全是东西，这时报错只会刷屏。
func SpawnDrops(w *world.World, pos world.Vec2, element world.Element) {
	if w == nil {
		return
	}
	if w.RandFloat64() < DropHealthChance {
		SpawnPickup(w, pos, world.PickupHealth, 0, DropHealthAmount)
	}
	if w.RandFloat64() < DropSkillChance {
		if id, ok := pickSkillForElement(w, element); ok {
			SpawnPickup(w, pos, world.PickupSkill, id, 0)
		}
	}
}

// pickSkillForElement 按元素筛主动技能，空则回退到全部。
//
// 「回退」而不是「不掉」：三种元素在 M6 各只有一个技能，若某种元素的
// 技能被移除（调参期常见），筛出来是空表。此时什么都不掉会让那个据点
// 变成一个永远不掉技能的坏图，而这与「位置有意义」的意图完全相反。
//
// 随机数走 w.RandIntn 包装（不是裸 rand）：掉落属于模拟路径，消耗要计入
// rngCalls 才能回放。
func pickSkillForElement(w *world.World, element world.Element) (uint16, bool) {
	all := build.ActiveSkills()
	if len(all) == 0 {
		return 0, false
	}

	if element != world.ElementNone {
		var pool []build.Skill
		for _, s := range all {
			if s.Element == element {
				pool = append(pool, s)
			}
		}
		if len(pool) > 0 {
			return pool[w.RandIntn(len(pool))].ID, true
		}
	}

	return all[w.RandIntn(len(all))].ID, true
}
