// Package fog 队伍共享战争迷雾（server-design §5.4）：
// 服务端持有 litCells bitset（128×128 bit = 2KB），
// 玩家半径 RevealRadius 内圆点亮亮 + 永久保留；
// Reveal 返回本次新点亮的格索引（row*Width+col），增量下发 fogDelta。
//
// 分层：fog 只 import world。room 每 tick 调 Reveal 并把增量塞进快照。
package fog

import (
	"koukoukongjian/internal/game/world"
)

// State 是一个房间的迷雾状态（队伍共享，不分玩家——spec：队伍共享探索进度）。
type State struct {
	w, h    int
	lit     []uint64 // bitset：bit i = 格 i 已点亮
	scratch []int    // Reveal 用的复用切片（避免每 tick 分配）
}

// New 创建全暗的迷雾。
func New(wCells, hCells int) *State {
	return &State{
		w:       wCells,
		h:       hCells,
		lit:     make([]uint64, (wCells*hCells+63)/64),
		scratch: make([]int, 0, 256),
	}
}

// Lit 返回格 idx 是否已点亮。
func (f *State) Lit(idx int) bool {
	return f.lit[idx/64]&(1<<(idx%64)) != 0
}

// LitCount 已点亮格数（调试/测试用）。
func (f *State) LitCount() int {
	n := 0
	for _, w := range f.lit {
		for ; w != 0; w &= w - 1 {
			n++
		}
	}
	return n
}

// Reveal 对所有活着的玩家圆内点灯，返回本次新点亮的格索引切片。
// 返回值复用内部 scratch——调用方必须立刻消费（塞进快照），不得持有。
// 玩家不存活时圆不点灯；空白世界（无玩家）返回 nil。
func (f *State) Reveal(w *world.World, radius float64) []int {
	f.scratch = f.scratch[:0]
	if w == nil || w.Terrain == nil {
		return nil // 防御：地形未装配（HasMap=false 短路约定在接线层）
	}
	rSq := (radius + 1) * (radius + 1) // +1 格宽余量，避免浮点边缘抖动反复亮灭
	w.Players.Each(func(_ world.EntityID, p *world.PlayerState) bool {
		if p.State != world.PlayerAlive {
			return true
		}
		cx := int(p.Pos.X) // cellSize=1；cellSize 可配时改 floor(p.X/cellSize)
		cy := int(p.Pos.Y)
		r := int(radius) + 1
		for dy := -r; dy <= r; dy++ {
			for dx := -r; dx <= r; dx++ {
				// 格中心距玩家位置 ≤ radius+1 即点亮（粗圆，灰盒接受）。
				gx, gy := cx+dx, cy+dy
				if gx < 0 || gy < 0 || gx >= f.w || gy >= f.h {
					continue
				}
				gxM := (float64(gx) + 0.5 - p.Pos.X)
				gyM := (float64(gy) + 0.5 - p.Pos.Y)
				if gxM*gxM+gyM*gyM > rSq {
					continue
				}
				idx := gy*f.w + gx
				if !f.Lit(idx) {
					f.lit[idx/64] |= 1 << (idx % 64)
					f.scratch = append(f.scratch, idx)
				}
			}
		}
		return true
	})
	if len(f.scratch) == 0 {
		return nil // 玩家全灭或全部原地：无增量，返回 nil（len 语义与空切片一致）
	}
	return f.scratch
}
