package world

import "math"

// Tile 是地形位图的一格。
type Tile uint8

const (
	// TileFloor 可通行。
	TileFloor Tile = iota
	// TileWall 不可通行。
	TileWall
	// TileSpawnZone 出生区（可走，但可能禁止刷怪）。
	TileSpawnZone
)

// Walkable 报告这一格能否通行。
func (t Tile) Walkable() bool { return t != TileWall }

// Terrain 是地形位图，决定"哪块地能走、哪块挡子弹"。
//
// 与 SpatialGrid 是两个完全不同的东西，不要合并：
//   - Terrain 是静态的、细粒度（1 m）、存"格子属性"，一局只建一次；
//   - SpatialGrid 是动态的、粗粒度（2 m）、存"实体下标"，每 tick 重建。
//
// 混在一起的后果是两边精度互相拖累：地形变粗则碰撞边缘跳动，索引变细则每 tick
// 重建开销爆炸。
type Terrain struct {
	width    int // 横向格数
	height   int // 纵向格数
	cellSize float64

	tiles []Tile
}

// NewTerrain 创建全空地地形。
func NewTerrain(widthCells, heightCells int, cellSize float64) *Terrain {
	if widthCells < 1 {
		widthCells = 1
	}
	if heightCells < 1 {
		heightCells = 1
	}
	t := &Terrain{
		width:    widthCells,
		height:   heightCells,
		cellSize: cellSize,
		tiles:    make([]Tile, widthCells*heightCells),
	}
	return t
}

// Width 返回横向格数。
func (t *Terrain) Width() int { return t.width }

// Height 返回纵向格数。
func (t *Terrain) Height() int { return t.height }

// CellSize 返回地形格边长（米）。
func (t *Terrain) CellSize() float64 { return t.cellSize }

// WorldWidth 返回世界宽度（米）。
func (t *Terrain) WorldWidth() float64 { return float64(t.width) * t.cellSize }

// WorldHeight 返回世界高度（米）。
func (t *Terrain) WorldHeight() float64 { return float64(t.height) * t.cellSize }

// inBounds 报告格子坐标是否在图内。
func (t *Terrain) inBounds(cx, cy int) bool {
	return cx >= 0 && cx < t.width && cy >= 0 && cy < t.height
}

// At 返回指定格的类型。图外返回 TileWall——把图外当作墙是唯一安全的选择：
// 返回 TileFloor 会让实体走出世界，返回零值则需要调用方到处判越界。
func (t *Terrain) At(cx, cy int) Tile {
	if !t.inBounds(cx, cy) {
		return TileWall
	}
	return t.tiles[cy*t.width+cx]
}

// Set 设置指定格的类型，越界时忽略。
func (t *Terrain) Set(cx, cy int, tile Tile) {
	if !t.inBounds(cx, cy) {
		return
	}
	t.tiles[cy*t.width+cx] = tile
}

// WalkableAt 报告世界坐标处的格子能否通行。
func (t *Terrain) WalkableAt(pos Vec2) bool {
	cx := int(pos.X / t.cellSize)
	cy := int(pos.Y / t.cellSize)
	return t.At(cx, cy).Walkable()
}

// FillRect 把一块矩形区域刷成指定类型，用于灰盒摆场景。
func (t *Terrain) FillRect(x0, y0, x1, y1 int, tile Tile) {
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			t.Set(x, y, tile)
		}
	}
}

// FillBorder 把四条边刷成墙，做出一个封闭场地。
func (t *Terrain) FillBorder(tile Tile) {
	for x := 0; x < t.width; x++ {
		t.Set(x, 0, tile)
		t.Set(x, t.height-1, tile)
	}
	for y := 0; y < t.height; y++ {
		t.Set(0, y, tile)
		t.Set(t.width-1, y, tile)
	}
}

// WalkableCount 返回可通行格数，用于灰盒检查地图是否合理。
func (t *Terrain) WalkableCount() int {
	n := 0
	for _, tile := range t.tiles {
		if tile.Walkable() {
			n++
		}
	}
	return n
}

// Hash 计算地形的稳定哈希，用于回放校验与"地图是否被意外改动"的检测。
//
// 用 FNV-1a：不自带 map 迭代、不依赖指针、跨平台稳定。地形一局只算一次，
// 用不着更快的东西。
func (t *Terrain) Hash() uint64 {
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	h := uint64(offset)
	for _, tile := range t.tiles {
		h ^= uint64(tile)
		h *= prime
	}
	return h
}

// FillAll 把整张图设成同一种瓦片。地图生成（M3 mapgen）的起点是"全墙"，
// 然后逐块挖出据点与走廊。
func (t *Terrain) FillAll(tile Tile) {
	for i := range t.tiles {
		t.tiles[i] = tile
	}
}

// CircleFree 判断以 pos 为圆心、r 为半径的圆是否完全不与墙重叠。
// 检查圆覆盖到的每个格子（圆心格 + 四至），墙格重叠即 false。
// 图外按墙处理——At 越界返回 TileWall，天然挡住"走出世界"。
func (t *Terrain) CircleFree(pos Vec2, r float64) bool {
	inv := 1.0 / t.cellSize
	cx0 := int(math.Floor((pos.X - r) * inv))
	cx1 := int(math.Floor((pos.X + r) * inv))
	cy0 := int(math.Floor((pos.Y - r) * inv))
	cy1 := int(math.Floor((pos.Y + r) * inv))
	for cy := cy0; cy <= cy1; cy++ {
		for cx := cx0; cx <= cx1; cx++ {
			if !t.At(cx, cy).Walkable() {
				// 粗判：格子矩形与圆重叠才算撞。r 小于半格时角上的
				// "格子矩形沾到但圆没沾到"会被误拒——灰盒接受，墙边
				// 会显得比实际厚不到半格，玩家感知不到。
				return false
			}
		}
	}
	return true
}

// LineWalkable 判断世界坐标 a→b 的直线是否全程走在可走格上（含两端）。
// 用于 AI 视线（隔墙不吸怪）与寻路结果的拐点压缩（string pulling），
// 也被子弹撞墙回收复用（PrevPos→Pos 的子步线段）。
// 格级 Bresenham：把两端转到格坐标后走整条格线。
func (t *Terrain) LineWalkable(a, b Vec2) bool {
	ax, ay := t.cellOf(a)
	bx, by := t.cellOf(b)
	dx := bx - ax
	dy := by - ay
	sx, sy := 1, 1
	if dx < 0 {
		sx = -1
	}
	if dy < 0 {
		sy = -1
	}
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	err := dx - dy
	x, y := ax, ay
	for {
		if !t.At(x, y).Walkable() {
			return false
		}
		if x == bx && y == by {
			return true
		}
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x += sx
		}
		if e2 < dx {
			err += dx
			y += sy
		}
	}
}

// cellCenter 返回格 (cx,cy) 的世界坐标中心。
func (t *Terrain) cellCenter(cx, cy int) Vec2 {
	return V((float64(cx)+0.5)*t.cellSize, (float64(cy)+0.5)*t.cellSize)
}

// cellOf 把世界坐标转成格坐标并钳到图内（负数与越界都拉回边界）。
func (t *Terrain) cellOf(p Vec2) (int, int) {
	cx := int(math.Floor(p.X / t.cellSize))
	cy := int(math.Floor(p.Y / t.cellSize))
	if cx < 0 {
		cx = 0
	}
	if cy < 0 {
		cy = 0
	}
	if cx >= t.width {
		cx = t.width - 1
	}
	if cy >= t.height {
		cy = t.height - 1
	}
	return cx, cy
}
