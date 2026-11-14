package systems

import (
	"math"
	"testing"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/game/world"
)

// newSystemsWorld 建一个用于 systems 包测试的世界。
//
// 全包共用一份：各测试文件原来各写一个 newXxxWorld，三份都是
// world.NewWorld(config.Default(), seed) 的薄包装，迟早会在默认配置
// 上分叉（某一份改成别的 maxBullets，那个文件的用例就测的不是同一套
// 规则了）。种子由调用方传，要复现某次随机序列时传固定值。
// HubCount=0 关地图：保持 M2 全空地行为，弹道/移动测试不被墙干扰。
func newSystemsWorld(t *testing.T, seed int64) *world.World {
	t.Helper()
	cfg := config.Default()
	cfg.Map.HubCount = 0
	return world.NewWorld(cfg, seed)
}

func spawnMonsterAt(t *testing.T, w *world.World, x, y float64) world.EntityID {
	t.Helper()
	id, m := w.Monsters.Spawn()
	m.Pos = world.V(x, y)
	m.HP = 50
	m.MaxHP = 50
	m.Radius = 0.4
	return id
}

func TestNearestMonsterPicksClosest(t *testing.T) {
	w := newSystemsWorld(t, 7)
	spawnMonsterAt(t, w, 10, 0) // 距离 10
	spawnMonsterAt(t, w, 3, 0)  // 距离 3  ← 应选它
	from := world.V(0, 0)

	id, ok := NearestMonster(w, from, 15)
	if !ok {
		t.Fatal("有怪在范围内却返回 ok=false")
	}
	m := w.Monsters.Get(id)
	if m == nil {
		t.Fatal("返回了无效实体")
	}
	if math.Abs(m.Pos.X-3) > 1e-9 {
		t.Fatalf("选中的怪在 x=%v，期望 x=3", m.Pos.X)
	}
}

func TestNearestMonsterRespectsRange(t *testing.T) {
	w := newSystemsWorld(t, 7)
	spawnMonsterAt(t, w, 30, 0)

	if _, ok := NearestMonster(w, world.V(0, 0), 15); ok {
		t.Fatal("怪在 30 m 外，锁定范围 15 m，却返回了目标")
	}
}

func TestNearestMonsterSkipsDead(t *testing.T) {
	w := newSystemsWorld(t, 7)
	dead := spawnMonsterAt(t, w, 2, 0)
	deadPtr := w.Monsters.Get(dead)
	deadPtr.HP = 0 // 已被打死的怪不能再被瞄准
	spawnMonsterAt(t, w, 8, 0)

	id, ok := NearestMonster(w, world.V(0, 0), 15)
	if !ok {
		t.Fatal("还有活怪，却返回 ok=false")
	}
	if id == dead {
		t.Fatal("瞄准了 HP=0 的怪")
	}
}

func TestNearestMonsterIsStableOnTies(t *testing.T) {
	// 两只怪等距时必须稳定选同一个（池下标小的），
	// 否则同一个 tick 重放两次会选出不同目标，回放不可复现。
	w := newSystemsWorld(t, 7)
	a := spawnMonsterAt(t, w, 0, 5)
	b := spawnMonsterAt(t, w, 5, 0)

	first, _ := NearestMonster(w, world.V(0, 0), 15)
	for i := 0; i < 20; i++ {
		got, _ := NearestMonster(w, world.V(0, 0), 15)
		if got != first {
			t.Fatalf("第 %d 次选到 %v，首次是 %v，等距时不稳定", i, got, first)
		}
	}
	if first != a && first != b {
		t.Fatalf("选到的 %v 不是这两只怪之一", first)
	}
}

func TestResolveAimManualWins(t *testing.T) {
	// 玩家给了方向就用玩家的，哪怕怪在正后方。
	w := newSystemsWorld(t, 7)
	spawnMonsterAt(t, w, 5, 0) // 怪在 +X

	p := &world.PlayerState{AimDir: math.Pi} // 玩家朝 -X
	got := ResolveAim(w, p, world.V(0, 0), true)
	if math.Abs(got-math.Pi) > 1e-9 {
		t.Fatalf("手动瞄准被改动：得到 %v，期望 %v", got, math.Pi)
	}
}

func TestResolveAimAutoUsesNearest(t *testing.T) {
	w := newSystemsWorld(t, 7)
	spawnMonsterAt(t, w, 0, 5) // 正 +Y

	p := &world.PlayerState{AimDir: 0} // 手上没给方向，朝向是默认 +X
	got := ResolveAim(w, p, world.V(0, 0), false)
	if math.Abs(got-math.Pi/2) > 1e-6 {
		t.Fatalf("自动瞄准得到 %v，期望 π/2（正 +Y）", got)
	}
}

func TestResolveAimFallsBackWhenNoTarget(t *testing.T) {
	// 没有可锁定的怪、玩家也没给方向：保持原朝向，不要归零。
	// 归零会让角色瞬间转向 +X，视觉上是抽搐。
	w := newSystemsWorld(t, 7)
	p := &world.PlayerState{AimDir: 2.5}
	got := ResolveAim(w, p, world.V(0, 0), false)
	if math.Abs(got-2.5) > 1e-9 {
		t.Fatalf("无目标时朝向变成 %v，期望保持 2.5", got)
	}
}
