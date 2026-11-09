package world

// Pool 是一个按"世代 ID"访问的实体池，同一种实体的所有实例都住在一个 Pool 里。
//
// 布局是结构体数组（AoS of one type），不是真 SoA。这一点刻意：真正的 SoA
// （每种实体把 Pos、HP 各自拆成并列切片）在 Go 里会让每一行代码都要写下标，
// 可读性崩得很快，而实测完整 tick 只占 20Hz 预算的 0.025%（docs/bench/）——
// 性能余量 99.97% 时不该为性能牺牲可读性。池化真正的收益是"没有 GC 压力 +
// 索引稳定 + 可整块复制"，AoS 一样拿得到。
type Pool[T any] struct {
	slots []slot[T]
	free  []uint16 // 空闲槽位下标栈
	live  int      // 当前存活数量
	max   int      // 硬上限
}

type slot[T any] struct {
	data  T
	gen   uint8
	alive bool
}

// NewPool 创建一个最多容纳 capacity 个存活实体、硬上限为 max 的池。
//
// capacity 与 max 都按"存活实体数"计，不是槽位数——0 号槽位保留给
// InvalidID（见下），实际分配的槽位比 capacity 多 1。这个 +1 必须由构造函数
// 自己补，否则 NewPool[PlayerState](4,4) 会变成只能装 3 个玩家。
//
// 槽位 0 永远不分配：MakeID(0, 0) 恰好等于 InvalidID，把 0 号槽留空可以让
// "零值即无效"这个 Go 惯例成立，调用方不必到处判空指针。
func NewPool[T any](capacity, max int) *Pool[T] {
	if capacity < 1 {
		capacity = 1
	}
	if max < capacity {
		max = capacity
	}
	if max > MaxIndex-1 {
		max = MaxIndex - 1
	}

	p := &Pool[T]{
		// +1 给保留的 0 号槽位。
		slots: make([]slot[T], capacity+1),
		max:   max,
	}
	// 从后往前压栈，让第一个 Spawn 拿到下标 1。
	for i := capacity; i >= 1; i-- {
		p.free = append(p.free, uint16(i))
	}
	return p
}

// Spawn 取一个空闲槽位并返回它的 ID 与可写指针。
//
// 池已满或已达硬上限时返回 (InvalidID, nil)。调用方必须处理 nil——静默丢弃
// 一次生成比崩掉整个 tick 好，但也不能假装成功。
func (p *Pool[T]) Spawn() (EntityID, *T) {
	var idx uint16
	if n := len(p.free); n > 0 {
		idx = p.free[n-1]
		p.free = p.free[:n-1]
	} else {
		// 没有空闲槽位，只能长出新槽位——但不能超过存活上限。
		if p.live >= p.max {
			return InvalidID, nil
		}
		p.slots = append(p.slots, slot[T]{})
		idx = uint16(len(p.slots) - 1)
	}

	s := &p.slots[idx]
	s.alive = true
	// 归零再交出去：复用槽位时残留的上一轮字段（比如 flags、pathLen）如果
	// 不清，就会出现"新怪继承旧怪的狂暴状态"这类幽灵 bug。
	var zero T
	s.data = zero
	p.live++

	return MakeID(idx, s.gen), &s.data
}

// Get 返回实体指针，ID 无效、世代不符或已死亡时返回 nil。
//
// 世代检查是这里的关键：调用方持有的 ID 可能来自几帧前的快照，那个槽位现在
// 可能已经住着另一个实体了。不检查就会"打到新实体身上"。
func (p *Pool[T]) Get(id EntityID) *T {
	if id == InvalidID {
		return nil
	}
	idx := id.Index()
	if int(idx) >= len(p.slots) {
		return nil
	}
	s := &p.slots[idx]
	if !s.alive || s.gen != id.Gen() {
		return nil
	}
	return &s.data
}

// Alive 报告实体是否存在。
func (p *Pool[T]) Alive(id EntityID) bool { return p.Get(id) != nil }

// Kill 回收实体，世代递增。返回是否真的回收了（对已死实体重复 Kill 返回 false）。
func (p *Pool[T]) Kill(id EntityID) bool {
	if id == InvalidID {
		return false
	}
	idx := id.Index()
	if int(idx) >= len(p.slots) {
		return false
	}
	s := &p.slots[idx]
	if !s.alive || s.gen != id.Gen() {
		return false
	}

	s.alive = false
	s.gen = (s.gen + 1) & MaxGen
	var zero T
	s.data = zero
	p.live--
	p.free = append(p.free, idx)
	return true
}

// Len 返回当前存活数量。
func (p *Pool[T]) Len() int { return p.live }

// Cap 返回已分配的槽位总数（含空闲）。
func (p *Pool[T]) Cap() int { return len(p.slots) }

// Each 遍历所有存活实体。
//
// 回调返回 false 可提前中断。遍历顺序是槽位下标顺序——这一点很重要：Go 的
// map 迭代顺序是随机的，任何影响模拟结果的遍历都必须走切片。这个 API 的形状
// 就是为了让调用方没有机会用 map。
func (p *Pool[T]) Each(fn func(id EntityID, v *T) bool) {
	for i := range p.slots {
		s := &p.slots[i]
		if !s.alive {
			continue
		}
		if !fn(MakeID(uint16(i), s.gen), &s.data) {
			return
		}
	}
}

// Reset 清空池，世代归零。仅用于房间重建。
func (p *Pool[T]) Reset() {
	for i := range p.slots {
		var zero T
		p.slots[i] = slot[T]{data: zero}
	}
	p.free = p.free[:0]
	for i := len(p.slots) - 1; i >= 1; i-- {
		p.free = append(p.free, uint16(i))
	}
	p.live = 0
}
