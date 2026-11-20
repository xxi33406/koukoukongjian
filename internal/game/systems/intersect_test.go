package systems

import (
	"testing"

	"koukoukongjian/internal/game/world"
)

// interactableBullet 摆一颗带交互词条的子弹，返回它的 id。
func interactableBullet(t *testing.T, w *world.World, x, y float64, owner world.EntityID, el world.Element) world.EntityID {
	t.Helper()
	id := spawnBulletAt(t, w, x, y, 0, 0)
	b := w.Bullets.Get(id)
	if b == nil {
		t.Fatal("子弹没生成")
	}
	b.OwnerID = owner
	b.Element = el
	b.Flags = world.BulletFlagInteractable
	return id
}

// TestIntersectFireWaterMakesSteam 钉住主路径：火+水在近处相撞生成蒸汽云。
func TestIntersectFireWaterMakesSteam(t *testing.T) {
	w := newSystemsWorld(t, 7)
	p1 := world.MakeID(1, 0)
	p2 := world.MakeID(2, 0)

	a := interactableBullet(t, w, 20.0, 20.0, p1, world.ElementFire)
	b := interactableBullet(t, w, 20.1, 20.0, p2, world.ElementWater) // 0.1 米

	got := IntersectBullets(w)

	if len(got) != 1 {
		t.Fatalf("产生了 %d 次交汇，期望 1", len(got))
	}
	if w.Bullets.Get(a) != nil {
		t.Fatal("交汇后子弹 A 还在——参与反应的子弹必须被销毁")
	}
	if w.Bullets.Get(b) != nil {
		t.Fatal("交汇后子弹 B 还在")
	}
	if w.Zones.Len() != 1 {
		t.Fatalf("区域数 = %d，期望 1", w.Zones.Len())
	}
	var zone *world.ZoneState
	w.Zones.Each(func(_ world.EntityID, z *world.ZoneState) bool {
		zone = z
		return false
	})
	if zone == nil {
		t.Fatal("找不到生成的区域")
	}
	if zone.Kind != world.ZoneSteam {
		t.Fatalf("区域类型 = %v，期望 ZoneSteam", zone.Kind)
	}
	// 生成点应该在两条子弹之间。
	if zone.Pos.DistSq(world.V(20.05, 20.0)) > 0.01 {
		t.Fatalf("蒸汽云位置 = %v，期望 ≈ (20.05,20)，即两子弹中点", zone.Pos)
	}
}

// TestIntersectNoReactionKeepsBothBullets 钉住「不反应就都继续飞」。
//
// 这是本 Task 最重要的一条反例：如果两条无反应的子弹相撞就双双消失，
// 玩家的直觉会是「我的子弹被队友吃掉了」。spec L67 要求交互性只在
// 特定词条下生效，没有词条（或元素不反应）时子弹应该互相穿过。
func TestIntersectNoReactionKeepsBothBullets(t *testing.T) {
	w := newSystemsWorld(t, 7)
	p1 := world.MakeID(1, 0)
	p2 := world.MakeID(2, 0)

	a := interactableBullet(t, w, 20.0, 20.0, p1, world.ElementFire)
	b := interactableBullet(t, w, 20.1, 20.0, p2, world.ElementFire) // 同为火

	got := IntersectBullets(w)

	if len(got) != 0 {
		t.Fatalf("产生了 %d 次交汇，期望 0（火+火不反应）", len(got))
	}
	if w.Bullets.Get(a) == nil {
		t.Fatal("无反应时子弹 A 被销毁了")
	}
	if w.Bullets.Get(b) == nil {
		t.Fatal("无反应时子弹 B 被销毁了")
	}
	if w.Zones.Len() != 0 {
		t.Fatalf("无反应却生成了 %d 片区域", w.Zones.Len())
	}
}

// TestIntersectRequiresDifferentOwners 钉住「自己的子弹不交汇」。
//
// 同一个人的两颗子弹（比如连发）不该互相反应——那会让「按着左键输出」
// 变成一种自伤式的技能触发，与 spec「队友弹幕可交互」的意图相反。
func TestIntersectRequiresDifferentOwners(t *testing.T) {
	w := newSystemsWorld(t, 7)
	p1 := world.MakeID(1, 0)

	a := interactableBullet(t, w, 20.0, 20.0, p1, world.ElementFire)
	b := interactableBullet(t, w, 20.1, 20.0, p1, world.ElementWater) // 同一个人

	got := IntersectBullets(w)

	if len(got) != 0 {
		t.Fatalf("同一玩家的两颗子弹产生了 %d 次交汇，期望 0", len(got))
	}
	if w.Bullets.Get(a) == nil || w.Bullets.Get(b) == nil {
		t.Fatal("同一玩家的子弹被销毁了")
	}
}

// TestIntersectRequiresInteractableFlag 钉住词条门槛。
//
// spec L67「交互性作为武器/技能词条存在，不设为全局默认」。没有这个词条
// 的子弹在物理上应该能互相穿过。
func TestIntersectRequiresInteractableFlag(t *testing.T) {
	w := newSystemsWorld(t, 7)
	p1 := world.MakeID(1, 0)
	p2 := world.MakeID(2, 0)

	a := interactableBullet(t, w, 20.0, 20.0, p1, world.ElementFire)
	b := spawnBulletAt(t, w, 20.1, 20.0, 0, 0)
	bState := w.Bullets.Get(b)
	bState.OwnerID = p2
	bState.Element = world.ElementWater
	// bState.Flags 保持零值（没有 Interactable）

	got := IntersectBullets(w)

	if len(got) != 0 {
		t.Fatalf("没有交互词条的子弹产生了 %d 次交汇，期望 0", len(got))
	}
	if w.Bullets.Get(a) == nil || w.Bullets.Get(b) == nil {
		t.Fatal("没有交互词条的子弹被销毁了")
	}
}

// TestIntersectRespectsDistance 钉住距离阈值。
func TestIntersectRespectsDistance(t *testing.T) {
	w := newSystemsWorld(t, 7)
	p1 := world.MakeID(1, 0)
	p2 := world.MakeID(2, 0)

	interactableBullet(t, w, 20.0, 20.0, p1, world.ElementFire)
	interactableBullet(t, w, 25.0, 20.0, p2, world.ElementWater) // 5 米

	got := IntersectBullets(w)

	if len(got) != 0 {
		t.Fatalf("相距 5 米的子弹产生了 %d 次交汇，期望 0", len(got))
	}
}

// TestIntersectOnlyOncePerBullet 钉住一颗子弹只参与一次交汇。
//
// 三颗子弹两两靠近时，A 与 B 反应后 A/B 都已销毁，A 不能再与 C 反应。
// 若实现里没在配对后检查「子弹还活着」，会读到已回收槽位（内容已清零），
// 症状是生成一片零位置的蒸汽云，或者干脆 panic。
func TestIntersectOnlyOncePerBullet(t *testing.T) {
	w := newSystemsWorld(t, 7)
	p1 := world.MakeID(1, 0)
	p2 := world.MakeID(2, 0)
	p3 := world.MakeID(3, 0)

	// 三颗子弹挤在 0.3 米内，元素两两可反应。
	a := interactableBullet(t, w, 20.0, 20.0, p1, world.ElementFire)
	b := interactableBullet(t, w, 20.1, 20.0, p2, world.ElementWater)
	c := interactableBullet(t, w, 20.2, 20.0, p3, world.ElementWater)

	got := IntersectBullets(w)

	if len(got) != 1 {
		t.Fatalf("产生了 %d 次交汇，期望 1（三颗子弹最多配一对）", len(got))
	}
	// 必须有一颗子弹活下来。
	alive := 0
	for _, id := range []world.EntityID{a, b, c} {
		if w.Bullets.Get(id) != nil {
			alive++
		}
	}
	if alive != 1 {
		t.Fatalf("三颗子弹里活了 %d 颗，期望 1", alive)
	}
}

// TestIntersectEmptyWorld 钉住空世界与单子弹世界的边界。
func TestIntersectEmptyWorld(t *testing.T) {
	w := newSystemsWorld(t, 7)
	if got := IntersectBullets(w); len(got) != 0 {
		t.Fatalf("空世界产生了 %d 次交汇", len(got))
	}

	interactableBullet(t, w, 20, 20, world.MakeID(1, 0), world.ElementFire)
	if got := IntersectBullets(w); len(got) != 0 {
		t.Fatalf("单子弹世界产生了 %d 次交汇", len(got))
	}
	if got := IntersectBullets(nil); len(got) != 0 {
		t.Fatalf("nil 世界产生了 %d 次交汇", len(got))
	}
}
