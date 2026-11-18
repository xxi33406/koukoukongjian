package systems

import (
	"testing"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/game/world"
)

// newWaveWorld 建一个带据点的测试世界（波次测试的公共底座）。
//
// 与 newSystemsWorld（aim_test.go，HubCount=0 关地图）的区别：波次必须开
// 地图——哨兵世界 w.Hubs 为空，UpdateWaves 直接短路，测不出任何东西。
// NewWorld 不生成地图（mapgen 由 room 层调用，见 world.go 注释），所以据点
// 在这里手工摆：Center/RadiusM 口径对齐 mapgen.go 的据点构造（Center=格心、
// RadiusM=HubRadiusCells×cell），不 import mapgen——测试只依赖 world 与
// config，不引入对生成器的耦合。
func newWaveWorld(t *testing.T) (*world.World, *config.Config) {
	t.Helper()
	cfg := config.Default()
	w := world.NewWorld(cfg, 77)

	cell := w.Terrain.CellSize()
	for _, c := range [][2]int{{10, 10}, {40, 10}, {10, 40}, {40, 40}, {64, 64}} {
		w.Hubs = append(w.Hubs, world.Hub{
			Center:  world.V((float64(c[0])+0.5)*cell, (float64(c[1])+0.5)*cell),
			RadiusM: float64(cfg.Map.HubRadiusCells) * cell,
			State:   world.HubIdle,
		})
	}
	w.HasMap = true
	return w, cfg
}

// playerAtHub 在指定据点中心放一个活玩家（一人触发全队收益的最小前置）。
func playerAtHub(t *testing.T, w *world.World, hubIdx int) world.EntityID {
	t.Helper()
	id, p := w.Players.Spawn()
	p.Pos = w.Hubs[hubIdx].Center
	p.HP, p.MaxHP = 100, 100
	p.State = world.PlayerAlive
	return id
}

// TestWaveSpawnsAllHubsOnFirstTick 钉住 M4 规格修订：怪物开局就刷，不等
// 玩家进圈。所有据点在同一次 UpdateWaves 里全部开波——一个玩家都没有的
// 世界里也照刷，这正是「怪是地图的一部分」的意思。
func TestWaveSpawnsAllHubsOnFirstTick(t *testing.T) {
	w, cfg := newWaveWorld(t)

	if w.Players.Len() != 0 {
		t.Fatalf("前提失效：本用例要求世界无玩家，实际 %d 个", w.Players.Len())
	}

	UpdateWaves(w)
	for i := range w.Hubs {
		hub := &w.Hubs[i]
		if hub.State != world.HubFighting {
			t.Errorf("据点 %d 开局应 Fighting（不进圈也刷），实际 %v", i, hub.State)
		}
		if hub.Wave != 1 {
			t.Errorf("据点 %d 开局应为第 1 波，实际 %d", i, hub.Wave)
		}
		if hub.WaveN != cfg.Waves.MonstersPerWave {
			t.Errorf("据点 %d 第一波应刷 %d 只，实际 %d",
				i, cfg.Waves.MonstersPerWave, hub.WaveN)
		}
	}

	// 怪物必须真的进了池子：5 个据点 × MonstersPerWave。
	want := len(w.Hubs) * cfg.Waves.MonstersPerWave
	if got := w.Monsters.Len(); got != want {
		t.Errorf("池中怪 %d，期望 %d（%d 据点 × %d 只）",
			got, want, len(w.Hubs), cfg.Waves.MonstersPerWave)
	}
}

// TestWaveSpawnPointsStayNearTheirHub 刷出的怪必须落在自己据点的圆环内。
//
// 这条与「全部据点同时刷」是两个独立的失效模式：撒点算错（比如用了全局
// 随机坐标而不是据点中心 + 极坐标）时据点状态仍会正确推进，只有位置是错的。
// 位置错在 M4 之后才致命——怪物 AI 会追人，出生点离得远只表现为「怪来得慢」，
// 很难看出是 bug。
func TestWaveSpawnPointsStayNearTheirHub(t *testing.T) {
	w, cfg := newWaveWorld(t)
	UpdateWaves(w)

	for i := range w.Hubs {
		hub := &w.Hubs[i]
		for j := 0; j < hub.WaveN; j++ {
			m := w.Monsters.Get(hub.WaveIDs[j])
			if m == nil {
				t.Fatalf("据点 %d 的 WaveIDs[%d] 槽位已失效", i, j)
			}
			// 撒点半径 0.6R × (0.8~1.2) = [0.48R, 0.72R]，留到 0.8R 的余量。
			if d := m.Pos.Sub(hub.Center).Len(); d > hub.RadiusM*0.8 {
				t.Errorf("据点 %d 的怪距中心 %.2f m，超过上限 %.2f m",
					i, d, hub.RadiusM*0.8)
			}
		}
	}
	_ = cfg
}

// TestWaveHoldsBetweenWavesWithoutClockAdvance 硬间隔的负路径。
// 开波后杀光但不推时钟，波次必须原地保持 Fighting 且 Wave 不动。
// mutation 实验删掉 waves.go 的 NextAt 判定后老用例仍全绿（它们每轮都推
// w.Tick=hub.NextAt，阻塞分支从未走过负路径），这条用例补上缺口。
func TestWaveHoldsBetweenWavesWithoutClockAdvance(t *testing.T) {
	w, _ := newWaveWorld(t)
	hub := &w.Hubs[4]
	UpdateWaves(w) // 第 1 波刷出，NextAt 已定

	// 只杀本据点的怪：其他据点也刷了，全杀会顺带推进它们，与用例无关。
	killHubWave(w, hub)
	UpdateWaves(w)
	if hub.State != world.HubFighting {
		t.Fatalf("间隔未到应保持 Fighting，实际 %v", hub.State)
	}
	if hub.Wave != 1 {
		t.Fatalf("间隔未到不得推进波次，实际 hub.Wave=%d", hub.Wave)
	}
}

// killHubWave 把某据点当前波的怪标记为死（M4 起死亡态是 HP 归零 +
// MonsterFlagDead 置位，与 Reap 的判据同源）。
func killHubWave(w *world.World, hub *world.Hub) {
	for i := 0; i < hub.WaveN; i++ {
		if m := w.Monsters.Get(hub.WaveIDs[i]); m != nil {
			m.HP = 0
			m.Flags |= world.MonsterFlagDead
		}
	}
}

func TestWaveClearAdvancesThenCompletes(t *testing.T) {
	w, cfg := newWaveWorld(t)
	hub := &w.Hubs[2]
	UpdateWaves(w) // 第 1 波刷出

	// 杀光当前波 → 时钟推到 NextAt → UpdateWaves 推进到第 2 波。
	// InterWaveDelayS 是硬间隔：startWave 刷出本波时即定下下一波最早可刷
	// tick，不推时钟间隔未到、波不会推进。按假时钟约定（AGENTS §2.7）直接
	// 改 w.Tick，不睡真实时间。
	killHubWave(w, hub)
	w.Tick = hub.NextAt
	UpdateWaves(w)
	if hub.Wave != 2 {
		t.Errorf("清波后应推进到第 2 波，实际 hub.Wave=%d", hub.Wave)
	}
	if hub.State != world.HubFighting {
		t.Errorf("还有后续波，应保持 Fighting，实际 %v", hub.State)
	}

	// 连清到最后一波完成：每轮杀光 + 推时钟 + 推进，WavesPerHub 轮后 Cleared。
	for i := 0; i < cfg.Waves.WavesPerHub; i++ {
		killHubWave(w, hub)
		w.Tick = hub.NextAt
		UpdateWaves(w)
	}
	if hub.State != world.HubCleared {
		t.Fatalf("全部波清完应 Cleared，实际 %v", hub.State)
	}
	if hub.Wave != cfg.Waves.WavesPerHub {
		t.Errorf("完成时 hub.Wave=%d，期望 %d", hub.Wave, cfg.Waves.WavesPerHub)
	}

	// 再调一次不炸也不重刷。
	n := w.Monsters.Len()
	UpdateWaves(w)
	if hub.State != world.HubCleared {
		t.Errorf("Cleared 是终态，不应变化，实际 %v", hub.State)
	}
	if got := w.Monsters.Len(); got != n {
		t.Errorf("Cleared 后不应重刷：池中怪 %d，期望 %d", got, n)
	}
}

// TestWaveAliveIgnoresHealedButDeadMonster 钉住清波口径与 Reap 同源（M4
// Task 1）：治疗把 HP 拉回正数、标志仍在的怪不能算活着，否则波次永远不
// 结束（面板上血是满的，逻辑上却是尸体）。
func TestWaveAliveIgnoresHealedButDeadMonster(t *testing.T) {
	w, _ := newWaveWorld(t)
	hub := &w.Hubs[1]
	UpdateWaves(w) // 第 1 波刷出

	// 只动本据点的怪：所有据点同 tick 开波，全池遍历会连别的据点一起改。
	for i := 0; i < hub.WaveN; i++ {
		if m := w.Monsters.Get(hub.WaveIDs[i]); m != nil {
			m.HP = 50                        // 治疗回满血
			m.Flags |= world.MonsterFlagDead // 但标志还在：是尸体
		}
	}
	if n := waveAlive(w, hub); n != 0 {
		t.Fatalf("waveAlive = %d，期望 0：HP 回满但标志为死的怪不该算活着", n)
	}

	w.Tick = hub.NextAt
	UpdateWaves(w)
	if hub.Wave != 2 {
		t.Errorf("清波后应推进到第 2 波，实际 hub.Wave=%d", hub.Wave)
	}
}

func TestWaveIDsTrackedForKillDetection(t *testing.T) {
	w, cfg := newWaveWorld(t)
	playerAtHub(t, w, 4) // 玩家位置已不影响开波，留着是为了体现"有玩家时也一样"
	hub := &w.Hubs[4]
	UpdateWaves(w)
	if hub.WaveN == 0 {
		t.Fatal("波未刷出")
	}
	if hub.WaveN != cfg.Waves.MonstersPerWave {
		t.Errorf("WaveN=%d，期望 %d", hub.WaveN, cfg.Waves.MonstersPerWave)
	}
	for i := 0; i < hub.WaveN; i++ {
		// 清波检测的口径与 waveAlive 同源（M4 起是 IsDead 的取反）：
		// 刷出的怪既不能带死亡标志，血量也必须为正。
		m := w.Monsters.Get(hub.WaveIDs[i])
		if m == nil {
			t.Errorf("WaveIDs[%d] 槽位已失效", i)
			continue
		}
		if m.IsDead() || m.HP <= 0 {
			t.Errorf("WaveIDs[%d] 刷出即应为满血，实际 dead=%v HP=%v", i, m.IsDead(), m.HP)
		}
	}
}

// 池满灰盒路径（Task 7 审查 M-1 探针转正）：怪物池被预塞满后 startWave
// 必须少刷而非 panic，且 WaveN/WaveIDs 与实际刷出数一致——Task 9/10 的
// 击杀检测读这两个字段，不一致就会把没刷出的怪当活着。
func TestWaveSpawnsGracefullyWhenPoolFull(t *testing.T) {
	w, cfg := newWaveWorld(t)
	playerAtHub(t, w, 0)
	hub := &w.Hubs[0]

	// 预塞怪物池：Monsters 池容量 128（world.NewWorld），把 free 栈耗尽。
	filled := 0
	for {
		_, m := w.Monsters.Spawn()
		if m == nil {
			break
		}
		m.HP, m.MaxHP = 50, 50
		filled++
	}
	UpdateWaves(w)
	// 池满时一只都刷不出来：状态进 Fighting（开波动作本身成立），
	// WaveN=0、WaveIDs 前 WaveN 个有效——绝不能 panic 或计数错位。
	if hub.State != world.HubFighting {
		t.Fatalf("池满开波应仍进 Fighting，实际 %v", hub.State)
	}
	if hub.WaveN != 0 {
		t.Fatalf("池满应刷 0 只，实际 WaveN=%d（预塞 %d 只）", hub.WaveN, filled)
	}
	for i := 0; i < hub.WaveN; i++ {
		if w.Monsters.Get(hub.WaveIDs[i]) == nil {
			t.Errorf("WaveIDs[%d] 指向无效槽位", i)
		}
	}
	_ = cfg
}
