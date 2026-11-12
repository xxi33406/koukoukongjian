package systems

import (
	"math"
	"testing"

	"koukoukongjian/internal/game/world"
)

func spawnShooter(t *testing.T, w *world.World) (world.EntityID, *world.PlayerState) {
	t.Helper()
	id, p := w.Players.Spawn()
	p.Pos = world.V(0, 0)
	p.AimDir = 0 // 正 +X
	p.HP = 100
	p.MaxHP = 100
	p.State = world.PlayerAlive
	p.WeaponID = WeaponRifle
	p.FireCooldown = 0
	return id, p
}

func TestFireSpawnsBulletInAimDirection(t *testing.T) {
	w := newSystemsWorld(t, 11)
	id, p := spawnShooter(t, w)
	p.AimDir = math.Pi / 2 // 正 +Y

	Fire(w, id, p, true, true, 0.05)

	if w.Bullets.Len() != 1 {
		t.Fatalf("子弹数 = %d，期望 1", w.Bullets.Len())
	}
	var b *world.BulletState
	w.Bullets.Each(func(_ world.EntityID, bs *world.BulletState) bool {
		b = bs
		return false
	})
	if b == nil {
		t.Fatal("子弹池里有实体但遍历不到")
	}
	if math.Abs(b.Vel.X) > 1e-6 {
		t.Fatalf("速度 X = %v，正 +Y 发射应该为 0", b.Vel.X)
	}
	if b.Vel.Y <= 0 {
		t.Fatalf("速度 Y = %v，正 +Y 发射应该为正", b.Vel.Y)
	}
	if b.OwnerID != id {
		t.Fatalf("OwnerID = %v，期望 %v", b.OwnerID, id)
	}
}

func TestFireSetsCooldownAndRespectsIt(t *testing.T) {
	w := newSystemsWorld(t, 11)
	id, p := spawnShooter(t, w)

	Fire(w, id, p, true, true, 0.05)
	if p.FireCooldown <= 0 {
		t.Fatal("开火后冷却未设置")
	}

	// 冷却未走完：不能再发
	before := w.Bullets.Len()
	Fire(w, id, p, true, true, 0.05)
	if w.Bullets.Len() != before {
		t.Fatalf("冷却中仍然发射了：%d → %d", before, w.Bullets.Len())
	}
}

func TestFireAutoRepeatsWhileHeld(t *testing.T) {
	w := newSystemsWorld(t, 11)
	id, p := spawnShooter(t, w)

	// 连发：按住不放，每 tick 调用一次，冷却内不发
	fired := 0
	for i := 0; i < 20; i++ { // 20 tick = 1 秒
		n := w.Bullets.Len()
		Fire(w, id, p, true, true, 0.05)
		if w.Bullets.Len() > n {
			fired++
		}
	}
	// 步枪冷却 0.15s，1 秒理论 6~7 发
	if fired < 5 || fired > 8 {
		t.Fatalf("1 秒打了 %d 发，步枪冷却 0.15s 期望 5~8 发", fired)
	}
}

func TestFireSingleShotNeedsRelease(t *testing.T) {
	w := newSystemsWorld(t, 11)
	id, p := spawnShooter(t, w)
	p.WeaponID = WeaponHandCannon // AutoFire=false

	// 一直按住：只应发一发
	for i := 0; i < 30; i++ {
		Fire(w, id, p, true, true, 0.05)
	}
	if got := w.Bullets.Len(); got != 1 {
		t.Fatalf("按住 30 tick 打出 %d 发，单发武器期望 1 发", got)
	}

	// 松开一帧再按：才能再发一发
	Fire(w, id, p, false, false, 0.05)
	p.FireCooldown = 0 // 跳过冷却，只测「按下沿」这一条
	Fire(w, id, p, true, true, 0.05)
	if got := w.Bullets.Len(); got != 2 {
		t.Fatalf("松开再按后共 %d 发，期望 2 发", got)
	}
}

func TestFireCatchesUpWhenCooldownBelowTick(t *testing.T) {
	// 冷却比一个 tick 短时，一帧要补发多发，否则实际射速被压到 20 发/秒。
	//
	// 用 fireWith 传一个临时武器，不去改全局 WeaponTable：
	// 改全局表在并行测试下会互相污染，而且污染表现为「另一个测试
	// 偶尔失败」，极难定位。
	w := newSystemsWorld(t, 11)
	id, p := spawnShooter(t, w)
	p.FireCooldown = 0

	fast := Weapon{
		ID: WeaponRifle, Name: "fast_test",
		Cooldown: 0.02, AutoFire: true,
		BulletSpeed: 20, BulletLife: 1.2, BulletRad: 0.12,
		Damage: 10, Element: world.ElementNone,
	}
	fireWith(w, id, p, fast, true, true, 0.05)

	if got := w.Bullets.Len(); got < 2 {
		t.Fatalf("冷却 20ms、tick 50ms 只打出 %d 发，应补发 ≥2 发", got)
	}
}

func TestFireStopsCatchingUpAtLimit(t *testing.T) {
	// 冷却被写成 1ms 这种荒诞值时，补发循环必须有上限，
	// 否则一帧打爆整个子弹池，表现为「服务器卡了一下，屏幕全是子弹」。
	w := newSystemsWorld(t, 11)
	id, p := spawnShooter(t, w)
	p.FireCooldown = 0

	absurd := Weapon{
		ID: WeaponRifle, Name: "absurd_test",
		Cooldown: 0.001, AutoFire: true,
		BulletSpeed: 20, BulletLife: 1.2, BulletRad: 0.12,
		Damage: 10, Element: world.ElementNone,
	}
	fireWith(w, id, p, absurd, true, true, 0.05)

	if got := w.Bullets.Len(); got > maxCatchUpShots {
		t.Fatalf("冷却 1ms 时打出 %d 发，补发上限是 %d", got, maxCatchUpShots)
	}
}

func TestFireRespectsMaxBullets(t *testing.T) {
	w := newSystemsWorld(t, 11)
	id, p := spawnShooter(t, w)
	w.MaxBullets = 3
	for i := 0; i < 100; i++ {
		p.FireCooldown = 0
		Fire(w, id, p, true, true, 0.05)
	}
	if got := w.Bullets.Len(); got > 3 {
		t.Fatalf("子弹数 %d 超过上限 %d，子弹池应满了就不发", got, w.MaxBullets)
	}
}

func TestFireDoesNothingForNonAlive(t *testing.T) {
	w := newSystemsWorld(t, 11)
	id, p := spawnShooter(t, w)
	p.State = world.PlayerGhost
	Fire(w, id, p, true, true, 0.05)
	if got := w.Bullets.Len(); got != 0 {
		t.Fatalf("鬼魂打出了 %d 发子弹", got)
	}
}
