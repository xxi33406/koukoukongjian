package systems

import "koukoukongjian/internal/game/world"

// SpawnBoss 在"全部据点清完"时刷出 Boss（每 tick 一次，UpdateWaves 之后）。
//
// 返回是否本次真的刷出来了（调用方据此打日志）。
//
// 简化（拍板决定 #3 + 设计决定 2.6）：spec L14 的完整设计是"仅 Boss 有
// 专属房间"，那要改 mapgen 多挖一块区域、改 SpawnHub 逻辑、改配置三处。
// 灰盒改用"最后一个清完的据点"当 Boss 场：要验证的是"两阶段 + 元素
// 弱点能否被玩家感知"，而不是"Boss 在另一个房间里"。
//
// 短路条件：
//   - Boss 参数未装配（nil，无图世界或配置 HP=0）
//   - 已经刷过（BossSpawned）
//   - 还有据点没清完
func SpawnBoss(w *world.World) bool {
	if w == nil || w.Boss == nil || !w.HasMap || len(w.Hubs) == 0 {
		return false
	}
	if w.BossSpawned {
		return false
	}
	// 全部据点必须是清场态。HubFighting 与 HubIdle 都不算——玩家正打到
	// 一半时 Boss 突然出现会让"清场"这个里程碑失去意义。
	for i := range w.Hubs {
		if w.Hubs[i].State != world.HubCleared {
			return false
		}
	}

	// BossHub 在首次刷出时定下来并记住（room 的日志与将来 UI 都要用）。
	// 用最后一个据点而不是随机：Boss 场应该是玩家最后打到的地方，
	// 随机会让"打完 6 个据点后又跑回第 1 个"变成可能。
	if w.BossHub < 0 || w.BossHub >= len(w.Hubs) {
		w.BossHub = len(w.Hubs) - 1
	}

	id, m := w.Monsters.Spawn()
	if id == world.InvalidID {
		// 池满：不置 BossSpawned，下一 tick 重试。
		// 不 break 也不 panic——池满时少刷是最轻的降级，而下一 tick
		// Reap 会回收尸体腾出槽位。置了 BossSpawned 会让本关永久失去
		// Boss，那比少刷一 tick 糟得多。
		return false
	}

	hub := &w.Hubs[w.BossHub]
	m.Pos = hub.Center
	m.Radius = float32(w.Boss.Radius)
	m.HP, m.MaxHP = w.Boss.HP, w.Boss.HP
	// TypeID 2 = Boss（1 = 普通怪）。客户端靠它区分渲染，不只靠标志位。
	m.TypeID = 2
	m.Flags |= world.MonsterFlagBoss
	m.BossPhase = 1

	w.BossSpawned = true
	return true
}
