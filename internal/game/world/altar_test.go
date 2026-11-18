package world

import "testing"

// 祭坛就是据点中心（spec L51「祭坛设于据点内」）。
//
// M5 不新增实体、不改 mapgen：据点空地的半径是 HubRadiusCells=5 米，
// 中心 2.5 米一定在空地上，不会嵌墙。M6 若要做「祭坛有独立血量、
// 会被怪拆」再升级成实体。
func TestNearestAltarFindsHubCenter(t *testing.T) {
	w := newWorldForTest()
	w.HasMap = true
	w.Hubs = []Hub{
		{Center: V(20, 20), RadiusM: 5},
		{Center: V(80, 80), RadiusM: 5},
	}

	pos := V(82, 81)
	center, ok := w.NearestAltar(pos, 3.0)
	if !ok {
		t.Fatalf("在据点 80,80 附近却没找到祭坛")
	}
	if center != V(80, 80) {
		t.Fatalf("找到的祭坛在 %v，期望 80,80", center)
	}
}

// 超出半径就是没找到。
func TestNearestAltarOutOfRange(t *testing.T) {
	w := newWorldForTest()
	w.HasMap = true
	w.Hubs = []Hub{{Center: V(20, 20), RadiusM: 5}}

	if _, ok := w.NearestAltar(V(40, 40), 3.0); ok {
		t.Fatalf("40 米外也找到了祭坛")
	}
}

// 没地图时永远找不到：老测试（HasMap=false）不该有祭坛。
func TestNearestAltarWithoutMap(t *testing.T) {
	w := newWorldForTest()
	if _, ok := w.NearestAltar(V(64, 64), 100.0); ok {
		t.Fatalf("没有地图时也找到了祭坛")
	}
}

// 半径配成 0 或负数时永远找不到（配置错误不该让全图都是祭坛）。
func TestNearestAltarZeroRadius(t *testing.T) {
	w := newWorldForTest()
	w.HasMap = true
	w.Hubs = []Hub{{Center: V(20, 20), RadiusM: 5}}

	if _, ok := w.NearestAltar(V(20, 20), 0); ok {
		t.Fatalf("半径为 0 时找到了祭坛")
	}
}

// 多个祭坛都在范围内时取最近的那个。
func TestNearestAltarPicksClosest(t *testing.T) {
	w := newWorldForTest()
	w.HasMap = true
	w.Hubs = []Hub{
		{Center: V(20, 20), RadiusM: 5},
		{Center: V(24, 20), RadiusM: 5},
	}

	center, ok := w.NearestAltar(V(23, 20), 10.0)
	if !ok {
		t.Fatalf("没找到祭坛")
	}
	if center != V(24, 20) {
		t.Fatalf("找到最近的祭坛是 %v，期望 24,20", center)
	}
}
