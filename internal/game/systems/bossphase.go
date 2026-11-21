package systems

import (
	"math"

	"koukoukongjian/internal/game/world"
)

// BossPhase2Flag 是进入二阶段时额外置的标志。
//
// 复用 MonsterFlagEnraged（距离惩罚第二级的"狂暴"）而不是新加一位：
// AI 侧已经有"狂暴 → 加速 + 加伤"的完整实现（ai.go 的 chaseSpeed 与
// 攻击 switch），新加一位意味着那两处要再判一次，且两套倍率如何叠加
// 要重新定义。复用代价是"Boss 二阶段"与"队伍走太散"共用同一个位，
// 好处是引擎里只有一个加速加伤的真相——M7 的取舍是"少一条并行的伤害
// 路径"优先。
//
// 若 M8 需要区分（例如 Boss 二阶段不该触发分裂攻击），那时再加位并在
// ai.go 的 switch 里优先判 Boss。
const BossPhase2Flag = world.MonsterFlagEnraged

// bossSummonRingRadius 是二阶段召唤物的环半径（米）。
//
// 2.0 米：Boss 半径 0.9，普通怪 0.4，两者贴在一起会互相卡住 CircleFree；
// 再远一点玩家能看出"它们是从 Boss 身上散出来的"。
const bossSummonRingRadius = 2.0

// UpdateBossPhase 推进 Boss 阶段（每 tick 一次，UpdateWaves 之后、AI 之前）。
//
// 顺序约束：必须在 AI 之前——AI 的 chaseSpeed 与攻击 switch 读
// MonsterFlagEnraged 决定速度与伤害，晚于 AI 会让切换那一 tick 白跑。
//
// 两阶段规则（spec L96，拍板决定 #3 的精简版）：
//   - HP ≤ 配置 HP × Phase2AtPct 时进入二阶段（只进不退）
//   - 进入时置 MonsterFlagEnraged（AI 自动吃加速加伤）
//   - 进入时召唤 SummonPerPhase2 只普通怪，只召唤一次
//
// 为什么"只进不退"：阶段回退会让玩家看到 Boss 从二阶段忽然变回一阶段
// （治疗、或者玩家重生期间回血），读起来像 bug。而血量阈值本身就能表达
// "还差多少"，不需要回退来表达。
//
// 阈值用配置的 HP 而不是 m.MaxHP：两者正常时相等，但配置改了之后（热更
// 参数、或测试里手工摆一只）只有配置值能表达"这一关的 Boss 应该多硬"。
func UpdateBossPhase(w *world.World) {
	if w == nil || w.Boss == nil || w.Boss.Phase2AtPct <= 0 {
		return
	}
	threshold := w.Boss.HP * w.Boss.Phase2AtPct

	w.Monsters.Each(func(_ world.EntityID, m *world.MonsterState) bool {
		if !m.Flags.Has(world.MonsterFlagBoss) || m.IsDead() {
			return true
		}
		// 已进二阶段就直接跳过。
		//
		// 这条是提前返回的优化，不是正确性机制：下面的三件写入
		// （BossPhase = 2、Flags 置同一位置位、BossSummoned 挡住重复
		// 召唤）全都幂等，删掉这条后所有用例仍然全绿（变异验证实测
		// 确认）。留着它是因为每 tick 扫全部怪物时能少做几次无谓赋值，
		// 且读代码时"已经二阶段了不必再看"比"反正幂等"清楚。
		if m.BossPhase >= 2 {
			return true
		}
		if m.HP > threshold {
			return true
		}

		m.BossPhase = 2
		m.Flags |= BossPhase2Flag

		// 召唤：只一次。池满时静默少刷——与 startWave 的池满纪律一致，
		// 而下一 tick Reap 会腾出槽位。这里不重试：二阶段只有一次，
		// 重试会让"少刷一次"变成"每 tick 补一只直到补满"。
		if m.BossSummoned || w.Boss.SummonPerPhase2 <= 0 {
			return true
		}
		m.BossSummoned = true
		n := w.Boss.SummonPerPhase2
		for i := 0; i < n; i++ {
			id, mon := w.Monsters.Spawn()
			if id == world.InvalidID {
				break
			}
			// 绕 Boss 摆一圈：角度按 i 均分。
			// 不掷骰——召唤位置不该消耗 RNG，否则本局后面的掉落序列
			// 会被"Boss 什么时候进二阶段"改变，而那是玩家行为决定的，
			// 回放就对不上。
			ang := float64(i) * (2 * math.Pi / float64(n))
			mon.Pos = world.V(
				m.Pos.X+bossSummonRingRadius*math.Cos(ang),
				m.Pos.Y+bossSummonRingRadius*math.Sin(ang),
			)
			mon.Radius = 0.4
			// TypeID 1 = 普通怪。客户端靠它区分渲染，不只靠标志位。
			mon.TypeID = 1
			if w.Mon != nil {
				mon.HP, mon.MaxHP = w.Mon.HP, w.Mon.HP
			}
		}
		return true
	})
}
