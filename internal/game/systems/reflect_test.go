package systems

import (
	"math"
	"testing"

	"koukoukongjian/internal/game/build"
	"koukoukongjian/internal/game/world"
)

// spawnReflectZone 在 pos 处放一片反射面，主人是 owner。
func spawnReflectZone(t *testing.T, w *world.World, pos world.Vec2, radius float64, owner world.EntityID) world.EntityID {
	t.Helper()
	id := SpawnZone(w, pos, radius, 3.0, world.ZoneReflect, owner, world.ElementNone)
	if !id.IsValid() {
		t.Fatal("SpawnZone 返回无效 ID")
	}
	return id
}

// TestSpawnZoneWritesFields 钉住共用辅助把每个字段都写对。
func TestSpawnZoneWritesFields(t *testing.T) {
	w := newSystemsWorld(t, 7)
	owner := world.MakeID(1, 0)

	id := SpawnZone(w, world.V(20, 20), 2.0, 3.0, world.ZoneReflect, owner, world.ElementNone)
	z := w.Zones.Get(id)
	if z == nil {
		t.Fatal("SpawnZone 之后 Get 返回 nil")
	}
	if z.Pos.X != 20 || z.Pos.Y != 20 {
		t.Fatalf("Pos = %v，期望 (20,20)", z.Pos)
	}
	if z.Radius != 2.0 {
		t.Fatalf("Radius = %v，期望 2.0", z.Radius)
	}
	if z.Life != 3.0 {
		t.Fatalf("Life = %v，期望 3.0", z.Life)
	}
	if z.Kind != world.ZoneReflect {
		t.Fatalf("Kind = %v，期望 ZoneReflect", z.Kind)
	}
	if z.OwnerID != owner {
		t.Fatalf("OwnerID = %d，期望 %d", z.OwnerID, owner)
	}
	// SlowMul 必须是 1.0（不减速）而不是零值：留 0 会让 TickZoneSlow 里
	// speed *= 0 把玩家完全冻住。这条是本次实现里最容易漏的一处。
	if z.SlowMul != 1.0 {
		t.Fatalf("SlowMul = %v，期望 1.0（零值会冻住玩家）", z.SlowMul)
	}
}

// TestReflectBulletsRedirectsOutward 钉住反射的几何：沿径向弹出去。
//
// 反射面是**圆**不是平面镜，所以语义是「沿区域中心 → 子弹的方向弹出」，
// 不是「速度分量取反」。这两种写法在子弹正对中心飞来时结果一致，在
// 横向飞过时完全不同：
//
//	子弹在 (20,21.5)（中心正下方 1.5 米），速度 (-20,0)（朝 -X 横飞）
//	径向反射 → 速度 (0,+20)，沿 +Y 弹开
//	分量取反 → 速度 (+20,0)，继续朝 +X 穿过镜子
//
// 「分量取反」在横飞时会让子弹**穿过**反射面（原路返回的是速度的那个
// 分量），玩家看到子弹从镜子里穿过去，是最直观的错误表现。
func TestReflectBulletsRedirectsOutward(t *testing.T) {
	w := newSystemsWorld(t, 7)
	owner := world.MakeID(1, 0)

	// 反射面在 (20,20)，半径 2 米。
	spawnReflectZone(t, w, world.V(20, 20), 2.0, owner)

	// 子弹在 (20,21.5) 向左飞（朝 -X），速度 (-20, 0)，即将进入反射面。
	bid := spawnBulletAt(t, w, 20, 21.5, -20, 0)

	before := w.Bullets.Get(bid)
	if before == nil {
		t.Fatal("子弹没生成")
	}
	beforeSpeed := before.Vel.Len()

	n := ReflectBullets(w)
	if n != 1 {
		t.Fatalf("反射了 %d 颗子弹，期望 1", n)
	}

	after := w.Bullets.Get(bid)
	if after == nil {
		t.Fatal("反射后子弹消失了——反射不是销毁")
	}
	// 速度大小不变：反射不加速也不减速。
	if got := after.Vel.Len(); math.Abs(got-beforeSpeed) > 1e-6 {
		t.Fatalf("反射后速度大小 = %v，期望 ≈ %v", got, beforeSpeed)
	}
	// 方向沿径向外：从区域中心指向子弹当前位置。
	wantDir := after.Pos.Sub(world.V(20, 20)).Normalized()
	gotDir := after.Vel.Normalized()
	if math.Abs(gotDir.X-wantDir.X) > 1e-6 || math.Abs(gotDir.Y-wantDir.Y) > 1e-6 {
		t.Fatalf("反射后速度方向 = %v，期望 ≈ %v（沿区域中心 → 子弹的径向）", gotDir, wantDir)
	}
	// 具体数值：(20,21.5) 在中心正下方，径向是 (0,1)，所以速度应为 (0,+20)。
	if math.Abs(after.Vel.X) > 1e-6 || after.Vel.Y <= 0 {
		t.Fatalf("反射后 Vel = %v，期望 ≈ (0, +%v)——横飞的子弹应被弹成向上的", after.Vel, beforeSpeed)
	}
}

// TestReflectBulletsChangesOwnership 钉住归属：反射后伤害算区域主人的。
//
// spec L114「反射弹幕归属算反弹者」。ResolveDamage 用 b.OwnerID 作为
// 伤害来源，所以 OwnerID 必须改；ReflectorID 保留给客户端与结算页。
func TestReflectBulletsChangesOwnership(t *testing.T) {
	w := newSystemsWorld(t, 7)
	shooter := world.MakeID(1, 0)
	mirror := world.MakeID(2, 0)

	spawnReflectZone(t, w, world.V(20, 20), 2.0, mirror)
	bid := spawnBulletAt(t, w, 20, 21.5, -20, 0)

	b := w.Bullets.Get(bid)
	b.OwnerID = shooter
	b.ReflectorID = world.InvalidID

	ReflectBullets(w)

	b = w.Bullets.Get(bid)
	if b.OwnerID != mirror {
		t.Fatalf("反射后 OwnerID = %d，期望 %d（反射面主人）", b.OwnerID, mirror)
	}
	if b.ReflectorID != mirror {
		t.Fatalf("反射后 ReflectorID = %d，期望 %d", b.ReflectorID, mirror)
	}
}

// TestReflectBulletsPushesOut 钉住位置推出。
//
// 不推出去的话下一帧子弹还在区域内，会立刻再反射一次——两颗子弹互相
// 弹来弹去，或者一颗子弹在同一帧被反射两次（方向来回抵消，看起来像
// 「子弹卡在镜子里」）。
func TestReflectBulletsPushesOut(t *testing.T) {
	w := newSystemsWorld(t, 7)
	owner := world.MakeID(1, 0)

	spawnReflectZone(t, w, world.V(20, 20), 2.0, owner)
	bid := spawnBulletAt(t, w, 20, 21.5, -20, 0)

	ReflectBullets(w)

	b := w.Bullets.Get(bid)
	d := b.Pos.Sub(world.V(20, 20)).Len()
	if d < 2.0+float64(b.Radius)-1e-6 {
		t.Fatalf("反射后子弹距区域中心 %v，期望 ≥ %v（必须推到边界外）", d, 2.0+float64(b.Radius))
	}
}

// TestReflectBulletsRespectsMaxBounces 钉住反射次数上限。
//
// 两面反射面对射会让子弹无限来回飞。maxBounces=2 保证它最多被弹两次
// 就变成一颗普通子弹，最终撞墙或到期消失。
func TestReflectBulletsRespectsMaxBounces(t *testing.T) {
	w := newSystemsWorld(t, 7)
	owner := world.MakeID(1, 0)

	spawnReflectZone(t, w, world.V(20, 20), 2.0, owner)
	bid := spawnBulletAt(t, w, 20, 21.5, -20, 0)

	b := w.Bullets.Get(bid)
	b.Bounces = maxBounces // 已经弹满了

	n := ReflectBullets(w)
	if n != 0 {
		t.Fatalf("反射了 %d 颗子弹，期望 0（已达上限）", n)
	}
	if got := w.Bullets.Get(bid).Vel.X; got >= 0 {
		t.Fatalf("已达上限的子弹 Vel.X = %v，期望保持负值（没被反射）", got)
	}
}

// TestReflectBulletsSkipsOutsideZone 钉住距离判定。
func TestReflectBulletsSkipsOutsideZone(t *testing.T) {
	w := newSystemsWorld(t, 7)
	owner := world.MakeID(1, 0)

	spawnReflectZone(t, w, world.V(20, 20), 2.0, owner)
	// 子弹在 10 米外。
	bid := spawnBulletAt(t, w, 20, 30, -20, 0)

	if n := ReflectBullets(w); n != 0 {
		t.Fatalf("反射了 %d 颗子弹，期望 0（子弹在区域外）", n)
	}
	if got := w.Bullets.Get(bid).Vel.X; got >= 0 {
		t.Fatalf("区域外的子弹 Vel.X = %v，期望保持负值", got)
	}
}

// TestReflectBulletsSkipsNonReflectZone 钉住只对反射面生效。
//
// 蒸汽云不是镜子。若判定不看 Kind，走位进蒸汽云的子弹会被莫名其妙弹回去。
func TestReflectBulletsSkipsNonReflectZone(t *testing.T) {
	w := newSystemsWorld(t, 7)
	owner := world.MakeID(1, 0)

	SpawnZone(w, world.V(20, 20), 2.0, 3.0, world.ZoneSteam, owner, world.ElementNone)
	bid := spawnBulletAt(t, w, 20, 21.5, -20, 0)

	if n := ReflectBullets(w); n != 0 {
		t.Fatalf("蒸汽云反射了 %d 颗子弹，期望 0", n)
	}
	if got := w.Bullets.Get(bid).Vel.X; got >= 0 {
		t.Fatalf("蒸汽云里的子弹 Vel.X = %v，期望保持负值", got)
	}
}

// TestCastActiveReflectZone 钉住技能 12 能施放并生成区域。
func TestCastActiveReflectZone(t *testing.T) {
	w := newSystemsWorld(t, 7)
	pid, p := spawnPlayerAt(t, w, 20, 20)
	p.SkillActive[0] = build.SkillIDReflect

	if !CastActive(w, pid, 0, p) {
		t.Fatal("施放反射面失败")
	}
	if w.Zones.Len() != 1 {
		t.Fatalf("区域数 = %d，期望 1", w.Zones.Len())
	}
	var found bool
	w.Zones.Each(func(_ world.EntityID, z *world.ZoneState) bool {
		if z.Kind == world.ZoneReflect && z.OwnerID == pid {
			found = true
		}
		return true
	})
	if !found {
		t.Fatal("没有找到属于施放者的反射面区域")
	}
	// 冷却必须被设上，否则能无限连放。
	if p.SkillCooldown[0] <= 0 {
		t.Fatalf("施放后冷却 = %v，期望 >0", p.SkillCooldown[0])
	}
}
