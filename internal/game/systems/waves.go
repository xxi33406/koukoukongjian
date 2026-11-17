package systems

import (
	"math"

	"koukoukongjian/internal/game/world"
)

// UpdateWaves 推进全部据点的波次状态机（每 tick 一次，tick 循环里、子步外）。
//
// 状态机：HubIdle（任一活玩家进圈）→ startWave → HubFighting（当前波全灭
// 且还有后续波 → 等到 NextAt 刷下一波；最后一波清完 → HubCleared 终态，
// 再调不重刷）。签名无返回值（计划 L2273 定稿）：波次事件只进 room 日志
// 不下发 proto——GameEventKind 不加枚举连累 Unity 侧，M6 据点经济再定义
// 真正的 HubEvent。
//
// 防御：Wav 未装配（老调用方/老测试没经过 NewWorld 新字段）、Hubs 空
// （HubCount=0 哨兵世界，短路与 ai 包同款）、Terrain nil 时全部 no-op，
// M2 老测试零改动。
func UpdateWaves(w *world.World) {
	if w.Wav == nil || w.Terrain == nil || len(w.Hubs) == 0 {
		return
	}
	for i := range w.Hubs {
		hub := &w.Hubs[i]
		switch hub.State {
		case world.HubIdle:
			if playerInHub(w, hub) {
				startWave(w, hub)
			}
		case world.HubFighting:
			if waveAlive(w, hub) > 0 {
				continue
			}
			// 当前波全灭。
			if hub.Wave >= w.Wav.WavesPerHub {
				hub.State = world.HubCleared
				continue
			}
			// InterWaveDelayS 是硬间隔不是空等：NextAt 在上一波刷出时就
			// 已定下，时钟不到不推进。用 tick 不用时间戳——真实时钟随机器
			// 负载漂移，会破坏回放一致性（world.go TickFloat 同款取舍）。
			if w.Tick < hub.NextAt {
				continue
			}
			startWave(w, hub)
		case world.HubCleared:
			// 终态：不再刷。
		}
	}
}

// playerInHub 任意活着的玩家在据点半径内即视为进入（4 人合作，一人触发全队收益）。
// 距离比较用平方，省开方（vec.go DistSq 同款取舍）。
func playerInHub(w *world.World, hub *world.Hub) bool {
	in := false
	w.Players.Each(func(_ world.EntityID, p *world.PlayerState) bool {
		if p.State == world.PlayerAlive && p.Pos.DistSq(hub.Center) <= hub.RadiusM*hub.RadiusM {
			in = true
			return false // 提前中断
		}
		return true
	})
	return in
}

// startWave 在据点内撒一波怪。
//
// 撒点：半径 0.6R 的圆环 ×(0.8+0.4×RandFloat64) 抖动，全部走 w.RandFloat64
// 包装（rngCalls 计数完整，回放一致；禁止直接碰 w.RNG）。
//
// 池满（Spawn 返回 InvalidID）时 break 灰盒接受少刷、不报错不 panic：怪物
// 池 128 容量 vs 每波 ≤4 只 ×7 据点 ×3 波，正常对局到不了上限；少刷一两只
// 比崩掉整个 tick 好（pool.go Spawn 注释同款取舍）。
func startWave(w *world.World, hub *world.Hub) {
	hub.Wave++
	hub.State = world.HubFighting
	hub.NextAt = w.Tick + ticksFor(w, w.Wav.InterWaveDelayS)
	hub.WaveN = 0
	for j := 0; j < w.Wav.MonstersPerWave; j++ {
		id, m := w.Monsters.Spawn()
		if id == world.InvalidID {
			break // 池满：灰盒接受少刷
		}
		ang := w.RandFloat64() * 6.283185307179586 // 2π
		rad := hub.RadiusM * 0.6 * (0.8 + 0.4*w.RandFloat64())
		m.Pos = world.V(hub.Center.X+math.Cos(ang)*rad, hub.Center.Y+math.Sin(ang)*rad)
		m.HP, m.MaxHP = w.Mon.HP, w.Mon.HP // 单怪表只有一种（灰盒）
		m.Radius = 0.4
		m.TypeID = 1
		hub.WaveIDs[j] = id
		hub.WaveN++
	}
}

// waveAlive 数当前波还活着的怪（只查 WaveIDs，不全池扫）。
//
// 判活口径是 world.MonsterState.IsDead 的取反，而不是池槽位（Pool.Alive）：
// 击杀在 Reap 接线（Task 9/10）前不回收槽位、接上后 Kill 才清槽，标志口径
// 两种路径都正确。与 reap 系统的回收判定必须同源——两处口径一旦分叉，
// 会出现"Reap 已经删了但波次还等它"或反过来的卡波（M4 Task 1 一并改掉
// 了原来的 HP>0 口径）。
func waveAlive(w *world.World, hub *world.Hub) int {
	n := 0
	for i := 0; i < hub.WaveN; i++ {
		if m := w.Monsters.Get(hub.WaveIDs[i]); m != nil && !m.IsDead() {
			n++
		}
	}
	return n
}

// ticksFor 把秒数换算成 tick 数，至少 1。与 world.go 的 secondsToTicks 同款
// （未导出，不能复用）：0 会让波间间隔失效成"同 tick 连刷"，那是节奏崩坏
// 而不是配置错误，运行时很难察觉，所以下限 1 是硬保证。
func ticksFor(w *world.World, seconds float64) uint32 {
	t := uint32(seconds*float64(w.TickRate) + 0.5)
	if t == 0 {
		t = 1
	}
	return t
}
