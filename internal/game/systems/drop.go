package systems

import (
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

// SpawnDrops 在死亡位置掷骰生成掉落物。
//
// 两条概率独立判定（用户明确"两种会同时出现"）：先掷血瓶、再掷技能，
// 互不影响。写成 if/else 会让两者互斥，而互斥在单次击杀上看不出来，
// 只有统计能发现——所以 drop_test.go 里有 TestDropRates 钉概率值。
//
// skillID 由调用方给（room 从 build 技能表里挑）。不用 w.RNG 在这里抽
// 技能：掉落什么技能是"关卡设计"的决定，不是模拟随机的一部分。
// M6 位置死亡技能会把这里换成"按死亡位置查表"。
//
// 池满时静默丢弃（SpawnPickup 自己会返 InvalidID）：掉落是奖励，
// 池满说明屏幕上已经全是东西，这时报错只会刷屏。
func SpawnDrops(w *world.World, pos world.Vec2, skillID uint16) {
	if w == nil {
		return
	}
	if w.RandFloat64() < DropHealthChance {
		SpawnPickup(w, pos, world.PickupHealth, 0, DropHealthAmount)
	}
	if w.RandFloat64() < DropSkillChance {
		SpawnPickup(w, pos, world.PickupSkill, skillID, 0)
	}
}
