package world

import "testing"

type testEnt struct {
	HP int
}

// 池必须能用同一个下标反复复活，且每次都换世代。
func TestPoolSpawnKillRespawn(t *testing.T) {
	p := NewPool[testEnt](4, 16)

	id1, v1 := p.Spawn()
	if v1 == nil {
		t.Fatal("第一次 Spawn 不应失败")
	}
	if id1 == InvalidID {
		t.Fatal("Spawn 返回了 InvalidID")
	}
	v1.HP = 100

	if got := p.Get(id1); got == nil || got.HP != 100 {
		t.Fatalf("Get 读回的 HP = %v，期望 100", got)
	}

	if !p.Kill(id1) {
		t.Fatal("Kill 应成功")
	}
	if p.Alive(id1) {
		t.Fatal("Kill 之后不应 alive")
	}
	// 旧 ID 必须彻底失效，不能因为槽位被复用就"复活"。
	if got := p.Get(id1); got != nil {
		t.Fatalf("已回收的旧 ID 仍能读到 %+v", got)
	}

	id2, v2 := p.Spawn()
	if v2 == nil {
		t.Fatal("第二次 Spawn 不应失败")
	}
	if id2 == id1 {
		t.Fatal("复用槽位必须换新 ID（世代不同）")
	}
	if id2.Index() != id1.Index() {
		t.Fatalf("期望复用同一个下标，得到 id1=%v id2=%v", id1, id2)
	}
	if id2.Gen() != id1.Gen()+1 {
		t.Fatalf("世代应 +1：id1=%d id2=%d", id1.Gen(), id2.Gen())
	}
}

// 复用的槽位必须被清零，否则"新怪继承旧怪的狂暴状态"这类幽灵 bug 会到处冒。
func TestPoolSpawnClearsState(t *testing.T) {
	p := NewPool[testEnt](2, 4)

	id, v := p.Spawn()
	v.HP = 999
	p.Kill(id)

	_, v2 := p.Spawn()
	if v2.HP != 0 {
		t.Fatalf("复用槽位残留了上一轮的 HP=%d", v2.HP)
	}
}

// 槽位 0 保留给 InvalidID，因此第一个 Spawn 应拿到下标 1。
func TestPoolSkipsSlotZero(t *testing.T) {
	p := NewPool[testEnt](4, 4)
	id, _ := p.Spawn()
	if id.Index() == 0 {
		t.Fatal("0 号槽位必须保留给 InvalidID，不能被分配")
	}
}

// capacity 只是初始预分配，max 才是硬上限：超出容量时会按需增长。
func TestPoolGrowsPastCapacityUpToMax(t *testing.T) {
	p := NewPool[testEnt](2, 3) // 预分配 2 个，最多 3 个
	var ids []EntityID

	for {
		id, v := p.Spawn()
		if v == nil {
			break
		}
		ids = append(ids, id)
		if len(ids) > 10 {
			t.Fatal("Spawn 没有在硬上限处停下")
		}
	}
	if len(ids) != 3 {
		t.Fatalf("硬上限 3 应能生成 3 个，实际 %d", len(ids))
	}
	if p.Len() != 3 {
		t.Fatalf("Len = %d，期望 3", p.Len())
	}
}

// 达到硬上限后必须明确失败，不能静默生成。
func TestPoolAtMaxReturnsNil(t *testing.T) {
	p := NewPool[testEnt](1, 2)
	for i := 0; i < 2; i++ {
		if _, v := p.Spawn(); v == nil {
			t.Fatalf("第 %d 次 Spawn 不应失败", i+1)
		}
	}
	if _, v := p.Spawn(); v != nil {
		t.Fatal("达到硬上限后 Spawn 必须返回 nil")
	}
}

// 回收一个之后再 Spawn 应当成功——上限看的是存活数，不是历史总数。
func TestPoolKillFreesCapacityAtMax(t *testing.T) {
	p := NewPool[testEnt](1, 2)
	id1, _ := p.Spawn()
	p.Spawn()
	if _, v := p.Spawn(); v != nil {
		t.Fatal("满员时不应生成")
	}

	p.Kill(id1)
	if _, v := p.Spawn(); v == nil {
		t.Fatal("回收后应能重新生成")
	}
}

func TestPoolKillIdempotent(t *testing.T) {
	p := NewPool[testEnt](4, 8)
	id, _ := p.Spawn()

	if !p.Kill(id) {
		t.Fatal("首次 Kill 应成功")
	}
	if p.Kill(id) {
		t.Fatal("重复 Kill 应返回 false")
	}
	if p.Len() != 0 {
		t.Fatalf("重复 Kill 把 live 计数弄坏了：%d", p.Len())
	}
}

func TestPoolKillUnknownID(t *testing.T) {
	p := NewPool[testEnt](4, 8)
	id, _ := p.Spawn()
	p.Kill(id)

	// 用旧世代 KILL 应失败，用非法 ID 也应失败。
	if p.Kill(id) {
		t.Fatal("旧世代 ID 不应能 Kill")
	}
	if p.Kill(InvalidID) {
		t.Fatal("InvalidID 不应能 Kill")
	}
	if p.Kill(MakeID(4095, 0)) {
		t.Fatal("越界索引不应能 Kill")
	}
}

// Each 必须遍历到每一个存活实体，且顺序稳定（槽位下标序）。
func TestPoolEach(t *testing.T) {
	p := NewPool[testEnt](8, 16)

	want := map[EntityID]bool{}
	for i := 0; i < 5; i++ {
		id, _ := p.Spawn()
		want[id] = true
	}

	seen := map[EntityID]bool{}
	var order []uint16
	p.Each(func(id EntityID, v *testEnt) bool {
		seen[id] = true
		order = append(order, id.Index())
		return true
	})

	if len(seen) != 5 {
		t.Fatalf("Each 遍历到 %d 个，期望 5", len(seen))
	}
	for id := range want {
		if !seen[id] {
			t.Fatalf("Each 漏掉了 %v", id)
		}
	}
	// 顺序必须是下标升序——Go map 迭代随机，模拟路径上不能有 map。
	for i := 1; i < len(order); i++ {
		if order[i] <= order[i-1] {
			t.Fatalf("Each 顺序不是稳定的下标升序：%v", order)
		}
	}
}

func TestPoolEachEarlyBreak(t *testing.T) {
	p := NewPool[testEnt](8, 16)
	for i := 0; i < 5; i++ {
		p.Spawn()
	}

	n := 0
	p.Each(func(id EntityID, v *testEnt) bool {
		n++
		return n < 2
	})
	if n != 2 {
		t.Fatalf("提前中断后应只遍历 2 次，实际 %d", n)
	}
}

func TestPoolReset(t *testing.T) {
	p := NewPool[testEnt](4, 8)
	for i := 0; i < 3; i++ {
		p.Spawn()
	}
	p.Reset()

	if p.Len() != 0 {
		t.Fatalf("Reset 后 Len = %d", p.Len())
	}
	id, v := p.Spawn()
	if v == nil {
		t.Fatal("Reset 后应能重新 Spawn")
	}
	if id.Gen() != 0 {
		t.Fatalf("Reset 后世代应归零，实际 %d", id.Gen())
	}
}
