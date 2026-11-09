package world

import "testing"

func newTestGrid() *SpatialGrid {
	// 128 格地形 × 1m = 128m 世界；索引格 2m → 65×65
	return NewSpatialGrid(128, 128, 1.0, 2.0)
}

func TestGridDimensions(t *testing.T) {
	g := newTestGrid()
	// 128m / 2m = 64，加 1 格容纳边界上的实体 → 65
	if g.Cols() != 65 {
		t.Fatalf("Cols = %d，期望 65", g.Cols())
	}
	if g.Rows() != 65 {
		t.Fatalf("Rows = %d，期望 65", g.Rows())
	}
	if g.CellSize() != 2.0 {
		t.Fatalf("CellSize = %v", g.CellSize())
	}
}

// 越界坐标必须钳到边缘格，而不是 panic 或算到别的格。
func TestGridCellIndexClamps(t *testing.T) {
	g := newTestGrid()

	// 左上角外
	if ci := g.cellIndex(V(-100, -100)); ci != 0 {
		t.Fatalf("负坐标应钳到 0 号格，实际 %d", ci)
	}
	// 右下角外
	last := g.Cols()*g.Rows() - 1
	if ci := g.cellIndex(V(10000, 10000)); ci != last {
		t.Fatalf("超大坐标应钳到最后一格 %d，实际 %d", last, ci)
	}
	// 正常情况下不应越界
	ci := g.cellIndex(V(64, 64))
	if ci < 0 || ci >= g.Cols()*g.Rows() {
		t.Fatalf("正常坐标算出越界下标 %d", ci)
	}
}

func TestGridRebuildAndQuery(t *testing.T) {
	g := newTestGrid()
	players := NewPool[PlayerState](4, 4)
	monsters := NewPool[MonsterState](16, 64)
	bullets := NewPool[BulletState](16, 64)

	// 两只怪，一只在近处、一只在远处。
	nearID, near := monsters.Spawn()
	near.Pos = V(10, 10)
	farID, far := monsters.Spawn()
	far.Pos = V(100, 100)

	g.Rebuild(monsters, bullets, players)

	cands := g.QueryRadius(V(10, 10), 1.0)
	if !containsIndex(cands, nearID.Index()) {
		t.Fatalf("近处实体 %v 未被索引到，候选=%v", nearID, cands)
	}
	if containsIndex(cands, farID.Index()) {
		t.Fatalf("100 米外的实体 %v 不应出现在候选里", farID)
	}

	// 现在查询远处，应只拿到远处那个。
	cands = g.QueryRadius(V(100, 100), 1.0)
	if !containsIndex(cands, farID.Index()) {
		t.Fatalf("远处实体 %v 未被索引到", farID)
	}
	if containsIndex(cands, nearID.Index()) {
		t.Fatalf("近处实体不该出现在远处查询里")
	}
}

// Rebuild 必须清掉上一轮的内容，否则已移动/死亡的实体会残留在旧格子里。
func TestGridRebuildClearsStaleCells(t *testing.T) {
	g := newTestGrid()
	players := NewPool[PlayerState](4, 4)
	monsters := NewPool[MonsterState](16, 64)
	bullets := NewPool[BulletState](16, 64)

	id, m := monsters.Spawn()
	m.Pos = V(10, 10)
	g.Rebuild(monsters, bullets, players)
	if len(g.QueryRadius(V(10, 10), 1.0)) == 0 {
		t.Fatal("第一次 Rebuild 后应有候选")
	}

	// 实体移动后重建，旧格必须清空。
	m.Pos = V(90, 90)
	g.Rebuild(monsters, bullets, players)
	if c := g.QueryRadius(V(10, 10), 1.0); len(c) != 0 {
		t.Fatalf("旧格子 %v 仍残留 %d 个候选", V(10, 10), len(c))
	}
	if c := g.QueryRadius(V(90, 90), 1.0); !containsIndex(c, id.Index()) {
		t.Fatal("新位置未被索引")
	}

	// 实体死亡后重建，格子必须彻底空掉。
	monsters.Kill(id)
	g.Rebuild(monsters, bullets, players)
	if c := g.QueryRadius(V(90, 90), 1.0); len(c) != 0 {
		t.Fatalf("实体死亡后仍残留 %d 个候选", len(c))
	}
}

// 格子只做粗筛，候选项可能比真实邻近实体多——但不能少。
func TestGridQueryIncludesNeighbors(t *testing.T) {
	g := newTestGrid()
	players := NewPool[PlayerState](4, 4)
	monsters := NewPool[MonsterState](16, 64)
	bullets := NewPool[BulletState](16, 64)

	// 实体在 (10.5, 10.5)，查询点在 (9.5, 10.5)——距 1 米，跨格边界两侧。
	id, m := monsters.Spawn()
	m.Pos = V(10.5, 10.5)

	g.Rebuild(monsters, bullets, players)
	cands := g.QueryRadius(V(9.5, 10.5), 1.0)
	if !containsIndex(cands, id.Index()) {
		t.Fatalf("相邻格内的实体必须被粗筛到（半径跨越格边界）")
	}
}

func TestGridLiveCells(t *testing.T) {
	g := newTestGrid()
	players := NewPool[PlayerState](4, 4)
	monsters := NewPool[MonsterState](16, 64)
	bullets := NewPool[BulletState](16, 64)

	g.Rebuild(monsters, bullets, players)
	if n := g.LiveCells(); n != 0 {
		t.Fatalf("空世界 LiveCells = %d", n)
	}

	_, m1 := monsters.Spawn()
	m1.Pos = V(10, 10)
	_, m2 := monsters.Spawn()
	m2.Pos = V(50, 50)
	g.Rebuild(monsters, bullets, players)
	if n := g.LiveCells(); n != 2 {
		t.Fatalf("两个分开的实体应占 2 个格，实际 %d", n)
	}
}

func TestGridBruteThreshold(t *testing.T) {
	g := newTestGrid()
	if g.BruteThreshold() != 200 {
		t.Fatalf("默认阈值应为 200，实际 %d", g.BruteThreshold())
	}
	g.SetBruteThreshold(50)
	if g.BruteThreshold() != 50 {
		t.Fatalf("设置后应为 50，实际 %d", g.BruteThreshold())
	}
	g.SetBruteThreshold(-5)
	if g.BruteThreshold() != 0 {
		t.Fatalf("负值应钳到 0，实际 %d", g.BruteThreshold())
	}
}

func containsIndex(s []uint16, want uint16) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
