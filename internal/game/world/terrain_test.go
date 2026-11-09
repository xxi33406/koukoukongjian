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
