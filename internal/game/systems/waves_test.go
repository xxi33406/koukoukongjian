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

func TestWaveTriggersOnEnterAndSpawns(t *testing.T) {
	w, cfg := newWaveWorld(t)
	playerAtHub(t, w, 1)
	hub := &w.Hubs[1]

	if hub.State != world.HubIdle {
		t.Fatalf("进圈前应 Idle，实际 %v", hub.State)
	}

	UpdateWaves(w)
	if hub.State != world.HubFighting {
		t.Fatalf("进圈后应 Fighting，实际 %v", hub.State)
	}
	if hub.WaveN != cfg.Waves.MonstersPerWave {
		t.Errorf("第一波应刷 %d 只，实际 %d", cfg.Waves.MonstersPerWave, hub.WaveN)
	}
	// 怪物必须真的进了池子，且位置在据点附近。
	alive := 0
	w.Monsters.Each(func(_ world.EntityID, m *world.MonsterState) bool {
		alive++
		if m.Pos.Sub(hub.Center).Len() > hub.RadiusM*0.8 {
			t.Errorf("刷怪点距据点中心过远：%v（上限 %.1f）", m.Pos, hub.RadiusM*0.8)
		}
		return true
	})
	if alive != cfg.Waves.MonstersPerWave {
		t.Errorf("池中怪 %d != %d", alive, cfg.Waves.MonstersPerWave)
	}
}

// 硬间隔的负路径（Task 7 审查 I-1）：开波后杀光但不推时钟，波次必须原地
// 保持 Fighting 且 Wave 不动。mutation 实验删掉 waves.go 的 NextAt 判定后
// 老用例仍全绿（它们每轮都推 w.Tick=hub.NextAt，阻塞分支从未走过负路径），
// 这条用例补上缺口。
func TestWaveHoldsBetweenWavesWithoutClockAdvance(t *testing.T) {
	w, _ := newWaveWorld(t)
	playerAtHub(t, w, 4)
	hub := &w.Hubs[4]
	UpdateWaves(w) // 第 1 波刷出，NextAt 已定

	w.Monsters.Each(func(_ world.EntityID, m *world.MonsterState) bool {
		m.HP = 0 // 杀光但不推时钟
		return true
	})
	UpdateWaves(w)
	if hub.State != world.HubFighting {
		t.Fatalf("间隔未到应保持 Fighting，实际 %v", hub.State)
	}
	if hub.Wave != 1 {
		t.Fatalf("间隔未到不得推进波次，实际 hub.Wave=%d", hub.Wave)
	}
}

func TestWaveClearAdvancesThenCompletes(t *testing.T) {
	w, cfg := newWaveWorld(t)
	playerAtHub(t, w, 2)
	hub := &w.Hubs[2]
	UpdateWaves(w) // 第 1 波刷出

	// 杀光当前波 → 时钟推到 NextAt → UpdateWaves 推进到第 2 波。
	// InterWaveDelayS 是硬间隔：startWave 刷出本波时即定下下一波最早可刷
	// tick，不推时钟间隔未到、波不会推进。计划测试骨架漏了这步，按假时钟
	// 约定（AGENTS §2.7）直接改 w.Tick，不睡真实时间。
	killWave := func() {
		w.Monsters.Each(func(_ world.EntityID, m *world.MonsterState) bool {
			m.HP = 0 // Reap 在 Task 9/10 才接线，直接改字段模拟击杀
			return true
		})
	}
	killWave()
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
		killWave()
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

func TestWaveOnlyTriggersInsideRadius(t *testing.T) {
	w, cfg := newWaveWorld(t)
	// 玩家站在据点外：3 号据点右侧 2 倍半径处（10 m > 5 m 触发半径），
	// 且远离其余据点，不会误触发。
	id, _ := w.Players.Spawn()
	p := w.Players.Get(id)
	far := w.Hubs[3].Center.Add(world.V(w.Hubs[3].RadiusM*2, 0))
	p.Pos = far
	p.HP, p.MaxHP = 100, 100
	p.State = world.PlayerAlive

	UpdateWaves(w)
	if w.Hubs[3].State != world.HubIdle {
		t.Errorf("玩家不在圈内不应开波，实际 %v", w.Hubs[3].State)
	}
	if w.Hubs[4].State != world.HubIdle {
		t.Errorf("远处据点不应被误触发，实际 %v", w.Hubs[4].State)
	}
	_ = cfg
}

func TestWaveIDsTrackedForKillDetection(t *testing.T) {
	w, cfg := newWaveWorld(t)
	playerAtHub(t, w, 4)
	hub := &w.Hubs[4]
	UpdateWaves(w)
	if hub.WaveN == 0 {
		t.Fatal("波未刷出")
	}
	if hub.WaveN != cfg.Waves.MonstersPerWave {
		t.Errorf("WaveN=%d，期望 %d", hub.WaveN, cfg.Waves.MonstersPerWave)
	}
	for i := 0; i < hub.WaveN; i++ {
		// 清波检测的口径是 HP>0（waveAlive 同款）：槽位要活着、血量要为正。
		m := w.Monsters.Get(hub.WaveIDs[i])
		if m == nil {
			t.Errorf("WaveIDs[%d] 槽位已失效", i)
			continue
		}
		if m.HP <= 0 {
			t.Errorf("WaveIDs[%d] 怪 HP=%v，刷出即应为满血", i, m.HP)
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
