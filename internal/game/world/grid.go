package world

// SpatialGrid 是均匀网格空间索引，用来把"附近有哪些实体"从 O(n) 压到 O(1)。
//
// 两个实测反直觉结论决定了这个实现（docs/decisions-game-backend.md §4）：
//
//  1. 用 map[int32][]uint16 做格子比暴力遍历还慢（1000 实体时 196 ns vs 127 ns），
//     哈希开销吃掉了全部收益。必须用扁平切片，下标 = y*w + x。
//  2. 实体数 200 以内暴力遍历最快（42.7 ns vs 网格 369 ns）。所以本类型保留
//     暴力分支，低于阈值时根本不重建网格。
//
// 注意这里存的是"候选项"，不是精确答案：格子只负责把参与精确判定的实体筛到
// 常数级，命中与否仍由 SegmentIntersectsCircle 这类精确测试决定。
type SpatialGrid struct {
	cellSize float64
	cols     int // 横向格数
	rows     int // 纵向格数

	// cells 长度固定为 cols*rows，每格存该格内的实体槽位下标。
	//
	// 用 [][]uint16 而不是 []uint16 是为了省内存：绝大多数格子是空的或只有
	// 一两个实体，扁平大数组会浪费几十倍空间，而每格一个小切片只付 slice header
	// 的代价。重用这些切片（不清零，只截断长度）是关键——见 Rebuild。
	cells [][]uint16

	scratch []uint16 // 重建时的临时容器，避免每次分配

	// bruteThreshold 是"低于它就别用网格"的实体数阈值。
	bruteThreshold int
}

// NewSpatialGrid 创建空间索引。
//
// worldWidth/Height 是地形格数，terrainCell 是地形格边长（米），cell 是空间
// 索引格边长（米）。两者刻意分开：地形要细（1 m，碰撞边缘精度），索引要粗
// （2 m，减少格数）。
func NewSpatialGrid(worldWidthCells, worldHeightCells int, terrainCell, cell float64) *SpatialGrid {
	worldW := float64(worldWidthCells) * terrainCell
	worldH := float64(worldHeightCells) * terrainCell

	cols := int(worldW/cell) + 1
	rows := int(worldH/cell) + 1
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}

	return &SpatialGrid{
		cellSize:       cell,
		cols:           cols,
		rows:           rows,
		cells:          make([][]uint16, cols*rows),
		bruteThreshold: 200,
	}
}

// SetBruteThreshold 覆盖"低于多少实体就不建网格"的阈值。
func (g *SpatialGrid) SetBruteThreshold(n int) {
	if n < 0 {
		n = 0
	}
	g.bruteThreshold = n
}

// BruteThreshold 返回当前阈值。
func (g *SpatialGrid) BruteThreshold() int { return g.bruteThreshold }

// CellSize 返回空间索引格的边长（米）。
func (g *SpatialGrid) CellSize() float64 { return g.cellSize }

// Cols 返回横向格数。
func (g *SpatialGrid) Cols() int { return g.cols }

// Rows 返回纵向格数。
func (g *SpatialGrid) Rows() int { return g.rows }

// cellIndex 把世界坐标换算成格子下标。越界钳到边缘格。
func (g *SpatialGrid) cellIndex(pos Vec2) int {
	cx := int(pos.X / g.cellSize)
	cy := int(pos.Y / g.cellSize)
	if cx < 0 {
		cx = 0
	} else if cx >= g.cols {
		cx = g.cols - 1
	}
	if cy < 0 {
		cy = 0
	} else if cy >= g.rows {
		cy = g.rows - 1
	}
	return cy*g.cols + cx
}

// Rebuild 重建网格索引。
//
// 调用时机是每个 tick 一次，不是每个子步：子步内实体最多移动 0.333 m，而格
// 宽 2 m，跨格概率约 17%，粗筛漏掉的只会是擦边命中，再由精确测试兜底。每子步
// 重建反而把 3 倍开销花在几乎相同的索引上。
func (g *SpatialGrid) Rebuild(monsters *Pool[MonsterState], bullets *Pool[BulletState], players *Pool[PlayerState]) {
	// 清空：只截断长度，底层数组留着复用。
	for i := range g.cells {
		g.cells[i] = g.cells[i][:0]
	}

	insert := func(pos Vec2, id EntityID) {
		ci := g.cellIndex(pos)
		g.cells[ci] = append(g.cells[ci], id.Index())
	}

	players.Each(func(id EntityID, p *PlayerState) bool {
		insert(p.Pos, id)
		return true
	})
	monsters.Each(func(id EntityID, m *MonsterState) bool {
		insert(m.Pos, id)
		return true
	})
	bullets.Each(func(id EntityID, b *BulletState) bool {
		insert(b.Pos, id)
		return true
	})
}

// QueryRadius 返回中心 pos、半径 r 的圆所覆盖的 3×3（或更大）格子里的候选下标。
//
// 返回的切片被内部复用，下一次 Query 会覆盖它。调用方要么立即用完，要么自己
// copy —— 这个约定省掉了每次查询的分配，而查询是热路径。
func (g *SpatialGrid) QueryRadius(pos Vec2, r float64) []uint16 {
	g.scratch = g.scratch[:0]

	span := int(r/g.cellSize) + 1
	cx := int(pos.X / g.cellSize)
	cy := int(pos.Y / g.cellSize)

	x0, x1 := cx-span, cx+span
	y0, y1 := cy-span, cy+span
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 >= g.cols {
		x1 = g.cols - 1
	}
	if y1 >= g.rows {
		y1 = g.rows - 1
	}

	for y := y0; y <= y1; y++ {
		row := y * g.cols
		for x := x0; x <= x1; x++ {
			g.scratch = append(g.scratch, g.cells[row+x]...)
		}
	}
	return g.scratch
}

// CellAt 返回指定格内的实体下标切片，仅供调试与测试。
func (g *SpatialGrid) CellAt(col, row int) []uint16 {
	if col < 0 || col >= g.cols || row < 0 || row >= g.rows {
		return nil
	}
	return g.cells[row*g.cols+col]
}

// LiveCells 返回非空格子的数量，用于指标与测试。
func (g *SpatialGrid) LiveCells() int {
	n := 0
	for i := range g.cells {
		if len(g.cells[i]) > 0 {
			n++
		}
	}
	return n
}
