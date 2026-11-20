package world

import (
	"testing"

	"koukoukongjian/internal/config"
)

func newZoneWorld(t *testing.T) *World {
	t.Helper()
	cfg := config.Default()
	cfg.Map.HubCount = 0 // 测试用空白世界，不生成地图
	return NewWorld(cfg, 42)
}

// TestZoneSpawnAndGet 钉住池的基本契约。
func TestZoneSpawnAndGet(t *testing.T) {
	w := newZoneWorld(t)

	id, z := w.Zones.Spawn()
	if z == nil {
		t.Fatal("Spawn 返回 nil")
	}
	if !id.IsValid() {
		t.Fatal("Spawn 返回的 ID 无效")
	}
	if got := w.Zones.Get(id); got != z {
		t.Fatalf("Get(%d) 拿到的不是 Spawn 的那个槽位", id)
	}
	if w.Zones.Len() != 1 {
		t.Fatalf("Len() = %d，期望 1", w.Zones.Len())
	}
}

// TestZoneSpawnStartsZeroed 钉住「零值即干净」的池契约。
//
// 复用槽位不能残留上一轮的区域：一个寿命已归零的蒸汽云被复用后如果
// 还带着上一位的 Life，新区域会立刻消失。
func TestZoneSpawnStartsZeroed(t *testing.T) {
	w := newZoneWorld(t)

	id, z := w.Zones.Spawn()
	z.Kind = ZoneSteam
	z.Life = 3.0
	z.Radius = 2.0
	z.OwnerID = MakeID(1, 0)

	w.Zones.Kill(id)

	_, z2 := w.Zones.Spawn()
	if z2.Kind != 0 {
		t.Fatalf("复用槽位的 Kind = %v，期望零值", z2.Kind)
	}
	if z2.Life != 0 {
		t.Fatalf("复用槽位的 Life = %v，期望零值", z2.Life)
	}
	if z2.Radius != 0 {
		t.Fatalf("复用槽位的 Radius = %v，期望零值", z2.Radius)
	}
	if z2.OwnerID != InvalidID {
		t.Fatalf("复用槽位的 OwnerID = %d，期望零值", z2.OwnerID)
	}
}

// TestZonePoolRespectsMax 钉住容量上限不 panic。
func TestZonePoolRespectsMax(t *testing.T) {
	w := newZoneWorld(t)

	spawned := 0
	for i := 0; i < MaxZones+10; i++ {
		if _, z := w.Zones.Spawn(); z != nil {
			spawned++
		}
	}
	if spawned != MaxZones {
		t.Fatalf("能 spawn %d 个区域，期望 %d", spawned, MaxZones)
	}
}

// TestZoneKindString 钉住 String()：日志要靠它。
func TestZoneKindString(t *testing.T) {
	cases := map[ZoneKind]string{
		ZoneSteam:   "steam",
		ZoneIgnite:  "ignite",
		ZoneReflect: "reflect",
		ZoneConduct: "conduct",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Fatalf("ZoneKind(%d).String() = %q，期望 %q", k, got, want)
		}
	}
}

// TestZoneKindZeroIsUnspecified 钉住零值语义。
//
// 复用槽位的 Kind 是 0，任何读到它的系统必须能认出「这不是一个真区域」。
// 把 0 定义成「未指定」而不是让 ZoneSteam = 0：后者会让每个刚 Spawn
// 还没赋值的区域都变成一片蒸汽云。
func TestZoneKindZeroIsUnspecified(t *testing.T) {
	var k ZoneKind
	if k.String() != "none" {
		t.Fatalf("零值 ZoneKind 的 String() = %q，期望 \"none\"", k.String())
	}
}
