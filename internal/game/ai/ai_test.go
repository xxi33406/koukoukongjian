package ai

import (
	"testing"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/game/world"
)

func newAIWorld(t *testing.T) *world.World {
	t.Helper()
	cfg := config.Default()
	cfg.Map.HubCount = 0 // 空白世界（Task 3 哨兵）
	w := world.NewWorld(cfg, 9)
	return w
}

func spawnMon(t *testing.T, w *world.World, x, y float64) world.EntityID {
	t.Helper()
	id, m := w.Monsters.Spawn()
	if id == world.InvalidID {
		t.Fatal("怪物池满")
	}
	m.Pos = world.V(x, y)
	m.HP, m.MaxHP = 50, 50
	m.Radius = 0.4
	return id
}

func spawnPl(t *testing.T, w *world.World, x, y float64) world.EntityID {
	t.Helper()
	id, p := w.Players.Spawn()
	if id == world.InvalidID {
		t.Fatal("玩家池满")
	}
	p.Pos = world.V(x, y)
	p.HP, p.MaxHP = 100, 100
	p.State = world.PlayerAlive
	return id
}

func TestRescoreAggroPicksNearestWithLOS(t *testing.T) {
	w := newAIWorld(t)
	pid := spawnPl(t, w, 10, 10)
	mid := spawnMon(t, w, 5, 10)
	far := spawnMon(t, w, 8, 10) // 更近但没有伤害记录

	// 给近怪一点伤害贡献，看权重是否压过距离。
	m := w.Monsters.Get(mid)
	m.AggroDmg[pid.Index()] = 6.0

	m2 := w.Monsters.Get(far)
	RescoreAggro(w, m2)

	if m2.TargetID != pid {
		t.Errorf("远怪应锁定造成过伤害的近怪玩家，实际 target=%v", m2.TargetID)
	}
	if m2.State != world.MonsterChase {
		t.Errorf("有目标应进 Chase，实际 %v", m2.State)
	}
	// mid 自己也有目标（距玩家 5 < 视野 20 且有视线）。
	RescoreAggro(w, m)
	if m.TargetID != pid {
		t.Errorf("近怪也应锁定玩家，实际 %v", m.TargetID)
	}
}

func TestRescoreAggroBlockedByWall(t *testing.T) {
	w := newAIWorld(t)
	spawnPl(t, w, 10, 10) // pid 无需持有：隔墙断言不读玩家
	mid := spawnMon(t, w, 5, 10)
	for cy := 0; cy < 16; cy++ {
		w.Terrain.Set(7, cy, world.TileWall) // 怪与玩家之间一堵墙
	}
	m := w.Monsters.Get(mid)
	RescoreAggro(w, m)
	if m.TargetID.IsValid() {
		t.Errorf("隔墙无视线不应锁定目标，实际 %v", m.TargetID)
	}
	if m.State != world.MonsterIdle {
		t.Errorf("无目标应保持 Idle，实际 %v", m.State)
	}
}

func TestStateMachineChaseThenAttack(t *testing.T) {
	w := newAIWorld(t)
	pid := spawnPl(t, w, 10, 10)
	mid := spawnMon(t, w, 6, 10)
	m := w.Monsters.Get(mid)
	m.TargetID = pid
	m.State = world.MonsterChase

	// 逐步推进：从 6 走到 10，中途切 attack。
	prevState := m.State
	for i := 0; i < 300; i++ {
		UpdateMonster(w, m, 1) // dt=1s 的夸张步进，纯为测试推进
		if m.State == world.MonsterAttack {
			break
		}
		prevState = m.State
	}
	if m.State != world.MonsterAttack {
		t.Fatalf("300 步内应进入 attack，卡在 %v，pos=%v", prevState, m.Pos)
	}
	if d := m.Pos.Sub(w.Players.Get(pid).Pos).LenSq(); d > (1.2+0.4+0.4)*(1.2+0.4+0.4) {
		t.Errorf("attack 怪不该远离玩家：距离平方 %.2f", d)
	}
}

func TestFastChaseWhenFar(t *testing.T) {
	w := newAIWorld(t)
	pid := spawnPl(t, w, 30, 10) // 15m 触发加速线之外
	mid := spawnMon(t, w, 6, 10)
	m := w.Monsters.Get(mid)
	m.TargetID = pid
	m.State = world.MonsterChase
	m.PathLen = 0
	m.PathIdx = 0

	// 空地直线：第一子步的位移应大于基础速度（1.5 倍）。
	UpdateMonster(w, m, 1.0)
	got := m.Pos.Sub(world.V(6, 10)).Len()
	want := 2.5 * 1.5 * 1.0
	if got < want*0.9 || got > want*1.1 {
		t.Errorf("远距追赶位移 %.2f，期望 ≈%.2f（2.5×1.5）", got, want)
	}
}

func TestMonsterNeverWalksThroughWall(t *testing.T) {
	w := newAIWorld(t)
	pid := spawnPl(t, w, 12, 5)
	mid := spawnMon(t, w, 2, 5)
	m := w.Monsters.Get(mid)
	m.TargetID = pid
	m.State = world.MonsterChase
	for cy := 0; cy < 16; cy++ {
		w.Terrain.Set(7, cy, world.TileWall) // 怪与玩家之间一堵墙（计划原文重复 Set 两次，系笔误，删一处）
	}
	for i := 0; i < 200; i++ {
		UpdateMonster(w, m, 1)
		if !w.Terrain.WalkableAt(m.Pos) {
			t.Fatalf("step %d：怪物走进墙 %v", i, m.Pos)
		}
	}
}
