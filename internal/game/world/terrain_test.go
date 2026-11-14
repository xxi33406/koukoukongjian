package world

import "testing"

func TestTerrainBounds(t *testing.T) {
	tr := NewTerrain(128, 128, 1.0)
	if tr.WorldWidth() != 128 {
		t.Fatalf("世界宽 = %v，期望 128", tr.WorldWidth())
	}
	if tr.WorldHeight() != 128 {
		t.Fatalf("世界高 = %v，期望 128", tr.WorldHeight())
	}

	// 图内默认是可通行的空地。
	if !tr.At(10, 10).Walkable() {
		t.Fatal("默认应为可通行")
	}
	// 图外一律当作墙——这是唯一安全的选择。
	if tr.At(-1, 10).Walkable() {
		t.Fatal("左侧图外应为墙")
	}
	if tr.At(10, -1).Walkable() {
		t.Fatal("上侧图外应为墙")
	}
	if tr.At(128, 10).Walkable() {
		t.Fatal("右侧图外应为墙")
	}
	if tr.At(10, 128).Walkable() {
		t.Fatal("下侧图外应为墙")
	}
}

func TestTerrainSetAndWalkableAt(t *testing.T) {
	tr := NewTerrain(32, 32, 1.0)
	tr.Set(5, 5, TileWall)

	if tr.WalkableAt(V(5.5, 5.5)) {
		t.Fatal("(5.5,5.5) 应为墙")
	}
	if !tr.WalkableAt(V(6.5, 5.5)) {
		t.Fatal("(6.5,5.5) 应为空地")
	}
	// 越界坐标应判为不可走。
	if tr.WalkableAt(V(-10, 5)) {
		t.Fatal("图外坐标应不可走")
	}
}

// Set 越界必须静默忽略，不能 panic——灰盒摆场景时经常刷到图外。
func TestTerrainSetOutOfBoundsIgnored(t *testing.T) {
	tr := NewTerrain(8, 8, 1.0)
	tr.Set(-1, 5, TileWall)
	tr.Set(5, -1, TileWall)
	tr.Set(8, 5, TileWall)
	tr.Set(5, 8, TileWall)
	// 只要没 panic 且世界没被破坏即可。
	if n := tr.WalkableCount(); n != 64 {
		t.Fatalf("越界写入不应改变世界，可通行格数 = %d", n)
	}
}

func TestTerrainFillRect(t *testing.T) {
	tr := NewTerrain(10, 10, 1.0)
	// 逆序参数也应正确工作。
	tr.FillRect(7, 7, 2, 2, TileWall)

	// (2,2) 到 (7,7) 共 6×6 = 36 格变成墙。
	wantWalkable := 100 - 36
	if n := tr.WalkableCount(); n != wantWalkable {
		t.Fatalf("可通行格数 = %d，期望 %d", n, wantWalkable)
	}
	if tr.At(2, 2).Walkable() || tr.At(7, 7).Walkable() {
		t.Fatal("矩形四角应为墙")
	}
	if !tr.At(1, 1).Walkable() || !tr.At(8, 8).Walkable() {
		t.Fatal("矩形外应为空地")
	}
}

func TestTerrainFillBorder(t *testing.T) {
	tr := NewTerrain(10, 10, 1.0)
	tr.FillBorder(TileWall)

	// 四条边共 10*2 + 8*2 = 36 格。
	if n := tr.WalkableCount(); n != 100-36 {
		t.Fatalf("可通行格数 = %d，期望 %d", n, 100-36)
	}
	for i := 0; i < 10; i++ {
		if tr.At(i, 0).Walkable() || tr.At(i, 9).Walkable() {
			t.Fatal("上下边应为墙")
		}
		if tr.At(0, i).Walkable() || tr.At(9, i).Walkable() {
			t.Fatal("左右边应为墙")
		}
	}
	if !tr.At(5, 5).Walkable() {
		t.Fatal("中心应为空地")
	}
}

// 哈希必须稳定：同一份地形每次算出同一个值，不同地形算出不同的值。
func TestTerrainHashStable(t *testing.T) {
	a := NewTerrain(16, 16, 1.0)
	b := NewTerrain(16, 16, 1.0)

	if a.Hash() != b.Hash() {
		t.Fatalf("两块相同的空地哈希不同：%x vs %x", a.Hash(), b.Hash())
	}
	// 同一对象连算两次必须一致。
	h1 := a.Hash()
	if h2 := a.Hash(); h1 != h2 {
		t.Fatalf("同一对象两次哈希不同")
	}

	b.Set(5, 5, TileWall)
	if a.Hash() == b.Hash() {
		t.Fatal("地形改动后哈希应当变化")
	}
}

func TestFillAllSetsEveryTile(t *testing.T) {
	tr := NewTerrain(4, 3, 1.0)
	tr.FillAll(TileWall)
	if tr.WalkableCount() != 0 {
		t.Errorf("FillAll(墙) 后可走格 = %d, 期望 0", tr.WalkableCount())
	}
	if tr.At(0, 0) != TileWall || tr.At(3, 2) != TileWall {
		t.Errorf("FillAll(墙) 后角落仍是 %v/%v, 期望全墙", tr.At(0, 0), tr.At(3, 2))
	}
	tr.FillAll(TileFloor)
	if tr.WalkableCount() != 12 {
		t.Errorf("FillAll(地) 后可走格 = %d, 期望 12", tr.WalkableCount())
	}
}

func TestCircleFreeRejectsWallOverlap(t *testing.T) {
	tr := NewTerrain(8, 8, 1.0)
	tr.Set(5, 5, TileWall)
	// 圆心在墙格中心，半径再小也撞。
	if tr.CircleFree(V(5.5, 5.5), 0.3) {
		t.Error("圆心落在墙格上，CircleFree 应为 false")
	}
	// 圆贴着墙格边缘：圆心(4.9,5.5) 半径 0.3 → x 覆盖到 5.2，侵入墙格 [5,6)。
	if tr.CircleFree(V(4.9, 5.5), 0.3) {
		t.Error("圆侵入墙格边缘，CircleFree 应为 false")
	}
	// 完全在空地内部。
	if !tr.CircleFree(V(2.0, 2.0), 0.3) {
		t.Error("空地内部的圆，CircleFree 应为 true")
	}
	// 图外按墙处理（At 越界返回 TileWall）。
	if tr.CircleFree(V(-0.5, 2.0), 0.1) {
		t.Error("圆越出世界边界，CircleFree 应为 false")
	}
}

func TestLineWalkableCrossesWall(t *testing.T) {
	tr := NewTerrain(8, 8, 1.0)
	tr.Set(4, 4, TileWall)
	// (1,1) → (6,6) 的格线穿过 (4,4)。
	if tr.LineWalkable(V(1.5, 1.5), V(6.5, 6.5)) {
		t.Error("视线穿墙，LineWalkable 应为 false")
	}
	// 绕开墙的线。
	if !tr.LineWalkable(V(1.5, 1.5), V(6.5, 1.5)) {
		t.Error("同行空地视线应为 true")
	}
	// 端点本身在墙里。
	if tr.LineWalkable(V(4.5, 4.5), V(6.5, 6.5)) {
		t.Error("起点在墙内，LineWalkable 应为 false")
	}
}

func TestLineWalkableBlocksDiagonalCorner(t *testing.T) {
	tr := NewTerrain(8, 8, 1.0)
	// 两墙对角相接（只在角上碰）：(4,5) 与 (5,4) 是墙，(4,4)/(5,5) 是空地。
	// 标准格级 Bresenham 对角线只走 (4,4)→(5,5) 两个格，不查正交邻格，
	// 会把这条「夹缝」判成可走——子弹与 AI 视线都会从墙角斜穿。
	// supercover 修法：斜走一步时补查两个正交邻格，两个都空才允许对角通过。
	tr.Set(4, 5, TileWall)
	tr.Set(5, 4, TileWall)
	if tr.LineWalkable(V(4.5, 4.5), V(5.5, 5.5)) {
		t.Error("对角墙缝不应视为视线可走，LineWalkable 应为 false")
	}
	// 只有一侧是墙时，另一侧正交格仍通——对角线贴着单墙角穿过是合法路径
	// （supercover 语义：只挡「两侧都墙」的角缝，不误杀单侧贴角）。
	// 摆位：墙在 (5,4)（行进方向的右侧正交格），左侧 (4,5) 空。
	tr2 := NewTerrain(8, 8, 1.0)
	tr2.Set(5, 4, TileWall)
	if !tr2.LineWalkable(V(4.5, 4.5), V(5.5, 5.5)) {
		t.Error("单侧墙角的对角线应从另一侧正交格通过，LineWalkable 应为 true")
	}
}
