package world

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
