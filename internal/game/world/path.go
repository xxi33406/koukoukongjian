package world

import "container/heap"

// 格子 A*（4 邻）+ string pulling 拐点压缩。
//
// 为什么放 world 包：只依赖 Terrain，与 SegmentIntersectsCircle 同层级；
// ai 包调用它但 ai 不被任何 world 代码反向依赖，无环。
//
// 为什么 4 邻不做对角：对角穿缝（两个墙格夹的对角）需要额外判定，且
// 2 格宽走廊下 4 邻 + string pulling 的路径质量足够。8 邻省下的步数
// 会被压缩步骤吃掉，收益只体现在未压缩的格序列上。
//
// 路径点是世界坐标（格中心 = (cx+0.5)*cellSize），起点用调用方传入的
// 精确位置——怪物从当前位置走到第一个拐点不需要先吸附格中心。

// FindPath 返回 a→b 的拐点路径（世界坐标，含起点与终点），不可达返回 nil。
// 起点或终点在墙内也返回 nil（调用方先保证位置合法，这里不兜底）。
// a==b（同格或同点）返回 [a]：单点路径，调用方无需特判。
func FindPath(tr *Terrain, a, b Vec2) []Vec2 {
	startX, startY := tr.cellOf(a)
	goalX, goalY := tr.cellOf(b)
	if !tr.At(startX, startY).Walkable() || !tr.At(goalX, goalY).Walkable() {
		return nil
	}
	W := tr.Width()

	idx := func(x, y int) int { return y*W + x }
	nodes := make([]*pathNode, W*tr.Height()) // 每格至多一个节点，nil = 未入表
	closed := make([]bool, W*tr.Height())

	pq := &pathPQ{}

	mkNode := func(x, y, g, parent int) *pathNode {
		n := &pathNode{
			x: x, y: y,
			g:      g,
			f:      float64(g) + heur(x, y, goalX, goalY),
			parent: parent,
			heapIx: -1,
		}
		nodes[idx(x, y)] = n
		return n
	}

	heap.Push(pq, mkNode(startX, startY, 0, -1))

	found := false
	for pq.Len() > 0 {
		cur := heap.Pop(pq).(*pathNode)
		ci := idx(cur.x, cur.y)
		if closed[ci] {
			continue
		}
		closed[ci] = true
		if cur.x == goalX && cur.y == goalY {
			found = true
			break
		}
		for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			nx, ny := cur.x+d[0], cur.y+d[1]
			if !tr.At(nx, ny).Walkable() {
				continue
			}
			ni := idx(nx, ny)
			if closed[ni] {
				continue
			}
			g := cur.g + 1
			if n := nodes[ni]; n != nil {
				if g < n.g { // 已在开表：g 变小则原地更新并修复堆序
					n.g = g
					n.f = float64(g) + heur(nx, ny, goalX, goalY)
					n.parent = ci
					heap.Fix(pq, n.heapIx)
				}
			} else {
				heap.Push(pq, mkNode(nx, ny, g, ci))
			}
		}
	}
	if !found {
		return nil
	}

	// 回溯格路径（逆序收集），转世界坐标后逆序回正，再把头尾换成精确坐标。
	var cells []Vec2
	for n := nodes[idx(goalX, goalY)]; n != nil; {
		cells = append(cells, tr.cellCenter(n.x, n.y))
		if n.parent < 0 {
			break
		}
		n = nodes[n.parent]
	}
	for i, j := 0, len(cells)-1; i < j; i, j = i+1, j-1 {
		cells[i], cells[j] = cells[j], cells[i]
	}
	cells[0] = a
	cells[len(cells)-1] = b
	return compressPath(tr, cells)
}

func heur(x, y, gx, gy int) float64 {
	dx, dy := x-gx, y-gy
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	return float64(dx + dy) // 曼哈顿距离：4 邻移动的可行启发（不低估）
}

type pathNode struct {
	x, y   int
	g      int
	f      float64
	parent int // nodes 里的下标；-1 = 起点
	heapIx int // 当前在 pathPQ 里的位置；-1 = 不在堆里（heap.Fix 用）
}

// pathPQ 是按 f 升序的最小堆；f 相同按 (y,x) 破平——遍历顺序确定，回放一致。
// Swap/Push/Pop 全程维护 heapIx，这是 heap.Fix 能原地更新的前提。
type pathPQ struct{ nodes []*pathNode }

func (q *pathPQ) Len() int { return len(q.nodes) }
func (q *pathPQ) Less(i, j int) bool {
	if q.nodes[i].f != q.nodes[j].f {
		return q.nodes[i].f < q.nodes[j].f
	}
	if q.nodes[i].y != q.nodes[j].y {
		return q.nodes[i].y < q.nodes[j].y
	}
	return q.nodes[i].x < q.nodes[j].x
}
func (q *pathPQ) Swap(i, j int) {
	q.nodes[i], q.nodes[j] = q.nodes[j], q.nodes[i]
	q.nodes[i].heapIx = i
	q.nodes[j].heapIx = j
}
func (q *pathPQ) Push(v any) {
	n := v.(*pathNode)
	n.heapIx = len(q.nodes)
	q.nodes = append(q.nodes, n)
}
func (q *pathPQ) Pop() any {
	n := q.nodes[len(q.nodes)-1]
	q.nodes = q.nodes[:len(q.nodes)-1]
	n.heapIx = -1
	return n
}

// compressPath 是 string pulling：从当前点尽量往远处跳——只要视线可走。
// 设计文档说的"拐点压缩"（共线点合并）是它的退化情形。
func compressPath(tr *Terrain, pts []Vec2) []Vec2 {
	if len(pts) <= 2 {
		return pts
	}
	out := []Vec2{pts[0]}
	i := 0
	for i < len(pts)-1 {
		far := i + 1
		for j := len(pts) - 1; j > i; j-- {
			if tr.LineWalkable(pts[i], pts[j]) {
				far = j
				break
			}
		}
		out = append(out, pts[far])
		i = far
	}
	return out
}
