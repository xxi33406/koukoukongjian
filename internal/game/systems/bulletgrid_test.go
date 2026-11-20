package systems

import (
	"testing"

	"koukoukongjian/internal/game/world"
)

// TestBulletGridFindsNeighbor 钉住基本查询：同格与相邻格能查到。
func TestBulletGridFindsNeighbor(t *testing.T) {
	w := newSystemsWorld(t, 7)

	// 两颗子弹：一颗在 (10,10)，一颗在 (10.5,10.5)。
	// 两者相距 0.7 米，在 2 米格宽下同格或相邻格。
	a := spawnBulletAt(t, w, 10, 10, 0, 0)
	b := spawnBulletAt(t, w, 10.5, 10.5, 0, 0)

	g := NewBulletGrid(2.0)
	g.Build(w)

	near := g.Near(world.V(10, 10))
	if !containsID(near, a) {
		t.Fatalf("Near(10,10) 里没有子弹 %d，得到 %v", a, near)
	}
	if !containsID(near, b) {
		t.Fatalf("Near(10,10) 里没有相邻格的子弹 %d，得到 %v", b, near)
	}
}

// TestBulletGridExcludesFar 钉住不会把远处子弹捞进来。
//
// 反例比正例重要：一个「总是返回全部子弹」的实现能通过上面那条测试，
// 但交汇判定会退化成 O(n²)，而那种退化不会报错、只会慢。
func TestBulletGridExcludesFar(t *testing.T) {
	w := newSystemsWorld(t, 7)

	near := spawnBulletAt(t, w, 10, 10, 0, 0)
	far := spawnBulletAt(t, w, 30, 30, 0, 0) // 20 米外，10 格

	g := NewBulletGrid(2.0)
	g.Build(w)

	got := g.Near(world.V(10, 10))
	if containsID(got, far) {
		t.Fatalf("Near(10,10) 里出现了 20 米外的子弹 %d，得到 %v", far, got)
	}
	if !containsID(got, near) {
		t.Fatalf("Near(10,10) 里没有子弹 %d，得到 %v", near, got)
	}
}

// TestBulletGridEmptyWorld 钉住空世界不 panic、返回空。
func TestBulletGridEmptyWorld(t *testing.T) {
	w := newSystemsWorld(t, 7)

	g := NewBulletGrid(2.0)
	g.Build(w)

	if got := g.Near(world.V(10, 10)); len(got) != 0 {
		t.Fatalf("空世界的 Near 返回 %v，期望空", got)
	}
}

// TestBulletGridRebuild 钉住 Build 可重复调用且不累积。
//
// 网格每 tick 重建。若 Build 只是往 map 里追加而不清空，上一 tick 已消失
// 的子弹会一直留在网格里——交汇判定会拿到一个已回收的 id，
// world.Bullets.Get 返回 nil，症状是「明明撞上了却没有反应」，很难查。
func TestBulletGridRebuild(t *testing.T) {
	w := newSystemsWorld(t, 7)

	id := spawnBulletAt(t, w, 10, 10, 0, 0)

	g := NewBulletGrid(2.0)
	g.Build(w)
	if len(g.Near(world.V(10, 10))) != 1 {
		t.Fatalf("第一次 Build 后 Near 应有 1 条，得到 %v", g.Near(world.V(10, 10)))
	}

	w.Bullets.Kill(id)

	g.Build(w)
	if got := g.Near(world.V(10, 10)); len(got) != 0 {
		t.Fatalf("子弹被回收后重建，Near 返回 %v，期望空", got)
	}
}

// TestBulletGridNegativeCoords 钉住负坐标不 panic。
//
// 地图边界外的坐标在反射面推出边界、拴绳钳位等路径上都会出现。
// 网格用 floor 除法，负坐标会落到负格号——若用格号做数组下标就会越界。
func TestBulletGridNegativeCoords(t *testing.T) {
	w := newSystemsWorld(t, 7)

	id := spawnBulletAt(t, w, -5, -5, 0, 0)

	g := NewBulletGrid(2.0)
	g.Build(w)

	got := g.Near(world.V(-5, -5))
	if !containsID(got, id) {
		t.Fatalf("负坐标的子弹 %d 没被查到，得到 %v", id, got)
	}
}

// TestBulletGridBadCellSizeFallsBack 钉住非法格宽不除零。
//
// 传 0 或负数会让 inv 变成 ±Inf，cellOf 里 Floor(Inf) 转 int 是平台相关的
// 未定义值（AGENTS §2.6 第一条）——格号会变成随机数，Near 永远查不到东西。
func TestBulletGridBadCellSizeFallsBack(t *testing.T) {
	for _, size := range []float64{0, -1, -0.5} {
		g := NewBulletGrid(size)
		if g.cellSize != 2.0 {
			t.Fatalf("NewBulletGrid(%v).cellSize = %v，期望回退到 2.0", size, g.cellSize)
		}
	}
}

// containsID 是测试辅助：某个 id 是否在切片里。
func containsID(ids []world.EntityID, want world.EntityID) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
