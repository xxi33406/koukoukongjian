package systems

import (
	"math"
	"testing"

	"koukoukongjian/internal/game/world"
)

func spawnBulletAt(t *testing.T, w *world.World, x, y, vx, vy float64) world.EntityID {
	t.Helper()
	id, b := w.Bullets.Spawn()
	b.Pos = world.V(x, y)
	b.PrevPos = b.Pos
	b.Vel = world.V(vx, vy)
	b.Life = 1
	b.Radius = 0.1
	b.Damage = 10
	return id
}

func TestIntegrateBulletsMovesByVelocity(t *testing.T) {
	w := newSystemsWorld(t, 1)
	id := spawnBulletAt(t, w, 0, 0, 20, 0)
	b := w.Bullets.Get(id)

	IntegrateBullets(w, 1.0/60.0)

	if math.Abs(b.Pos.X-20.0/60.0) > 1e-9 {
		t.Fatalf("x 位移 = %v，期望 %v", b.Pos.X, 20.0/60.0)
	}
	if math.Abs(b.Pos.Y) > 1e-9 {
		t.Fatalf("y 位移 = %v，期望 0", b.Pos.Y)
	}
}

func TestIntegrateBulletsRecordsPrevPos(t *testing.T) {
	// PrevPos 必须在移动前保存，扫掠判定靠它算线段起点。
	w := newSystemsWorld(t, 1)
	id := spawnBulletAt(t, w, 100, 50, 20, 0)
	b := w.Bullets.Get(id)

	IntegrateBullets(w, 1.0/60.0)

	if math.Abs(b.PrevPos.X-100) > 1e-9 || math.Abs(b.PrevPos.Y-50) > 1e-9 {
		t.Fatalf("PrevPos = (%v,%v)，期望移动前的 (100,50)", b.PrevPos.X, b.PrevPos.Y)
	}
}

func TestIntegrateBulletsExpiresByLife(t *testing.T) {
	w := newSystemsWorld(t, 1)
	id := spawnBulletAt(t, w, 0, 0, 20, 0)
	b := w.Bullets.Get(id)
	b.Life = 0.02

	IntegrateBullets(w, 1.0/60.0) // 0.0167，还剩 0.0033
	if !w.Bullets.Alive(id) {
		t.Fatal("寿命还剩 0.0033s 就被回收了")
	}

	IntegrateBullets(w, 1.0/60.0) // 超时
	if w.Bullets.Alive(id) {
		t.Fatal("寿命耗尽却仍在池里")
	}
}

func TestIntegrateBulletsReapsManyAtOnce(t *testing.T) {
	// 遍历中回收子弹：Pool.Each 是下标升序且支持提前中断，
	// 但 Kill 会改动槽位状态。必须验证同时有多颗子弹到期时不会漏掉/重复处理。
	w := newSystemsWorld(t, 1)
	var ids []world.EntityID
	for i := 0; i < 10; i++ {
		id := spawnBulletAt(t, w, float64(i), 0, 20, 0)
		w.Bullets.Get(id).Life = 0.01
		ids = append(ids, id)
	}

	IntegrateBullets(w, 1.0/60.0)

	if w.Bullets.Len() != 0 {
		t.Fatalf("10 颗全部到期，池里还剩 %d 颗", w.Bullets.Len())
	}
}
