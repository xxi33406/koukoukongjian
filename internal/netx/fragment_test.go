package netx

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"
)

// fakeClock 让超时测试不必真的 sleep。
type fakeClock struct{ t time.Time }

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 9, 21, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestReassemblerSingleFragment(t *testing.T) {
	r := NewReassembler(nil)
	got, done, err := r.Push(FragHeader{MsgID: 1, Index: 0, Count: 1}, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("单片消息应立刻完成")
	}
	if string(got) != "hello" {
		t.Fatalf("得到 %q", got)
	}
	if r.Pending() != 0 {
		t.Fatal("完成后不应残留状态")
	}
}

// 分片乱序到达必须能正确重组——UDP 上这几乎是常态。
func TestReassemblerOutOfOrder(t *testing.T) {
	r := NewReassembler(nil)
	parts := []string{"AAA", "BBB", "CCC"}
	// 倒序送入
	for i := len(parts) - 1; i >= 0; i-- {
		got, done, err := r.Push(FragHeader{MsgID: 9, Index: uint16(i), Count: 3}, []byte(parts[i]))
		if err != nil {
			t.Fatal(err)
		}
		if done && i != 0 {
			t.Fatalf("第 %d 片就完成了，数据不全", i)
		}
		if done && i == 0 {
			if string(got) != "AAABBBCCC" {
				t.Fatalf("重组结果 %q，期望 AAABBBCCC", got)
			}
		}
	}
	if r.Pending() != 0 {
		t.Fatal("完成后应清理")
	}
}

// 重传会产生重复分片，重复不能计入完成度，否则会提前"凑齐"得到错误数据。
func TestReassemblerIgnoresDuplicate(t *testing.T) {
	r := NewReassembler(nil)

	if _, done, _ := r.Push(FragHeader{MsgID: 5, Index: 0, Count: 2}, []byte("first")); done {
		t.Fatal("只有一片不该完成")
	}
	// 同一片重传两次
	for i := 0; i < 3; i++ {
		if _, done, _ := r.Push(FragHeader{MsgID: 5, Index: 0, Count: 2}, []byte("first")); done {
			t.Fatal("重复分片不该让它完成")
		}
	}
	got, done, err := r.Push(FragHeader{MsgID: 5, Index: 1, Count: 2}, []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("两片都到齐了应完成")
	}
	if string(got) != "firstsecond" {
		t.Fatalf("重复分片污染了结果：%q", got)
	}
}

// 两条消息的分片在 UDP 上会交错到达，不能串味。
func TestReassemblerInterleavedMessages(t *testing.T) {
	r := NewReassembler(nil)

	// A 的第 0 片、B 的第 0 片、A 的第 1 片、B 的第 1 片
	if _, done, _ := r.Push(FragHeader{MsgID: 1, Index: 0, Count: 2}, []byte("A0")); done {
		t.Fatal("A 不该完成")
	}
	if _, done, _ := r.Push(FragHeader{MsgID: 2, Index: 0, Count: 2}, []byte("B0")); done {
		t.Fatal("B 不该完成")
	}
	a, doneA, _ := r.Push(FragHeader{MsgID: 1, Index: 1, Count: 2}, []byte("A1"))
	if !doneA || string(a) != "A0A1" {
		t.Fatalf("A 重组错误：done=%v got=%q", doneA, a)
	}
	b, doneB, _ := r.Push(FragHeader{MsgID: 2, Index: 1, Count: 2}, []byte("B1"))
	if !doneB || string(b) != "B0B1" {
		t.Fatalf("B 重组错误：done=%v got=%q", doneB, b)
	}
}

// 同一 MsgID 的分片总数变了，说明对端状态错乱，必须丢弃而不是拼出错位数据。
func TestReassemblerRejectsCountMismatch(t *testing.T) {
	r := NewReassembler(nil)

	if _, _, err := r.Push(FragHeader{MsgID: 3, Index: 0, Count: 3}, []byte("x")); err != nil {
		t.Fatal(err)
	}
	_, _, err := r.Push(FragHeader{MsgID: 3, Index: 1, Count: 5}, []byte("y"))
	if !errors.Is(err, ErrFragmentCountMismatch) {
		t.Fatalf("应报 ErrFragmentCountMismatch，得到 %v", err)
	}
	// 出错后该集合应被清理，不能留下半成品。
	if r.Pending() != 0 {
		t.Fatal("错误后应清理该集合")
	}
}

// 超时的半成品必须能被回收，否则名额会被永久占住。
func TestReassemblerSweepDropsExpired(t *testing.T) {
	clock := newFakeClock()
	r := NewReassembler(clock.now)

	if _, _, err := r.Push(FragHeader{MsgID: 1, Index: 0, Count: 3}, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if r.Pending() != 1 {
		t.Fatal("应有 1 条在重组")
	}

	clock.advance(reassemblyTimeout - time.Millisecond)
	if n := r.Sweep(); n != 0 {
		t.Fatalf("还没超时不该回收，回收了 %d 条", n)
	}
	if r.Pending() != 1 {
		t.Fatal("不该被回收")
	}

	clock.advance(2 * time.Millisecond)
	if n := r.Sweep(); n != 1 {
		t.Fatalf("超时应回收 1 条，实际 %d", n)
	}
	if r.Pending() != 0 {
		t.Fatal("应已清空")
	}
}

// 队列满时必须报错而不是无限增长——这是防内存耗尽的第一道闸。
func TestReassemblerOverflow(t *testing.T) {
	r := NewReassembler(nil)

	for i := 0; i < maxFragSets; i++ {
		if _, _, err := r.Push(FragHeader{MsgID: uint16(i), Index: 0, Count: 2}, []byte("x")); err != nil {
			t.Fatalf("第 %d 条不该失败：%v", i, err)
		}
	}
	if r.Pending() != maxFragSets {
		t.Fatalf("应有 %d 条，实际 %d", maxFragSets, r.Pending())
	}

	if _, _, err := r.Push(FragHeader{MsgID: 999, Index: 0, Count: 2}, []byte("x")); !errors.Is(err, ErrReassemblyOverflow) {
		t.Fatalf("应报 ErrReassemblyOverflow，得到 %v", err)
	}
}

// 队列满但旧集合已超时，新消息必须能挤进来——否则长时间运行的会话会
// 因为几条僵尸集合而永久拒收。
func TestReassemblerOverflowFreesExpiredFirst(t *testing.T) {
	clock := newFakeClock()
	r := NewReassembler(clock.now)

	for i := 0; i < maxFragSets; i++ {
		if _, _, err := r.Push(FragHeader{MsgID: uint16(i), Index: 0, Count: 2}, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}

	clock.advance(reassemblyTimeout)

	// 队列在名义上还是满的，但全超时了，必须接受新消息。
	if _, _, err := r.Push(FragHeader{MsgID: 500, Index: 0, Count: 2}, []byte("new")); err != nil {
		t.Fatalf("超时集合应先被清理腾出名额，却报错：%v", err)
	}
}

// 单条消息的字节总量上限，防止"256 片 × 1200 字节"的内存放大。
func TestReassemblerTooBig(t *testing.T) {
	r := NewReassembler(nil)

	chunk := make([]byte, MaxPayload)
	// 先塞到接近上限
	total := 0
	for i := 0; total < maxReassemblyBytes; i++ {
		if i >= MaxFragments {
			break
		}
		if _, _, err := r.Push(FragHeader{MsgID: 1, Index: uint16(i), Count: MaxFragments}, chunk); err != nil {
			if errors.Is(err, ErrReassemblyTooBig) {
				return // 符合预期
			}
			t.Fatalf("第 %d 片意外失败：%v", i, err)
		}
		total += len(chunk)
	}
	t.Fatal("始终没有触发上限")
}

// 调用方（网络层）直接传读缓冲里的切片，读缓冲在下一个包到达时就被复用。
// Push 必须复制，否则先到的片会被后来的包覆盖，凑齐后拼出垃圾——实测
// 表现是大消息首字节变成无关值。
func TestReassemblerCopiesPayload(t *testing.T) {
	r := NewReassembler(nil)

	// 模拟 2 片、每片 4 字节的复用读缓冲。
	buf := make([]byte, 4)

	copy(buf, "AAAA")
	if _, done, err := r.Push(FragHeader{MsgID: 7, Index: 0, Count: 2}, buf); err != nil || done {
		t.Fatalf("第 1 片：done=%v err=%v", done, err)
	}

	// 第 2 片到达，读缓冲被复用。
	copy(buf, "BBBB")
	whole, done, err := r.Push(FragHeader{MsgID: 7, Index: 1, Count: 2}, buf)
	if err != nil {
		t.Fatalf("第 2 片失败：%v", err)
	}
	if !done {
		t.Fatal("两片到齐应完成")
	}
	if string(whole) != "AAAABBBB" {
		t.Fatalf("拼出 %q，期望 \"AAAABBBB\"——第 1 片被读缓冲复写覆盖了", whole)
	}
}

func TestReassemblerReset(t *testing.T) {
	r := NewReassembler(nil)
	for i := 0; i < 3; i++ {
		r.Push(FragHeader{MsgID: uint16(i), Index: 0, Count: 2}, []byte("x"))
	}
	if r.Pending() != 3 {
		t.Fatal("应有 3 条")
	}
	r.Reset()
	if r.Pending() != 0 {
		t.Fatal("Reset 后应清空")
	}
}

// 空载荷的分片也是合法的，不能把 []byte("") 当成"没收到"。
func TestReassemblerEmptyFragments(t *testing.T) {
	r := NewReassembler(nil)
	if _, done, _ := r.Push(FragHeader{MsgID: 1, Index: 0, Count: 2}, []byte{}); done {
		t.Fatal("不该完成")
	}
	got, done, err := r.Push(FragHeader{MsgID: 1, Index: 1, Count: 2}, []byte("tail"))
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("应完成")
	}
	if string(got) != "tail" {
		t.Fatalf("得到 %q", got)
	}
}

// 完整消息本身就是空字节（两个空分片）也要正确返回 done=true。
func TestReassemblerAllEmptyFragments(t *testing.T) {
	r := NewReassembler(nil)
	r.Push(FragHeader{MsgID: 1, Index: 0, Count: 2}, []byte{})
	got, done, err := r.Push(FragHeader{MsgID: 1, Index: 1, Count: 2}, []byte{})
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("两片都到了应完成")
	}
	if len(got) != 0 {
		t.Fatalf("结果应为空，得到 %q", got)
	}
}

// 随机顺序送分片，结果必须与顺序无关。
func TestReassemblerRandomOrder(t *testing.T) {
	for _, count := range []int{2, 3, 5, 16} {
		parts := make([][]byte, count)
		want := &bytes.Buffer{}
		for i := 0; i < count; i++ {
			parts[i] = []byte(fmt.Sprintf("part%02d-", i))
			want.Write(parts[i])
		}

		// 倒序、正序、以及"中间开始"三种送法
		orders := [][]int{
			makeSeq(count, false),
			makeSeq(count, true),
			rotate(count),
		}
		for _, order := range orders {
			r := NewReassembler(nil)
			var got []byte
			for _, idx := range order {
				out, done, err := r.Push(FragHeader{MsgID: 1, Index: uint16(idx), Count: uint16(count)}, parts[idx])
				if err != nil {
					t.Fatalf("count=%d order=%v idx=%d: %v", count, order, idx, err)
				}
				if done {
					got = out
				}
			}
			if string(got) != want.String() {
				t.Fatalf("count=%d 顺序 %v 重组结果不符", count, order)
			}
		}
	}
}

func makeSeq(n int, reverse bool) []int {
	out := make([]int, n)
	for i := 0; i < n; i++ {
		if reverse {
			out[i] = n - 1 - i
		} else {
			out[i] = i
		}
	}
	return out
}

func rotate(n int) []int {
	out := make([]int, n)
	for i := 0; i < n; i++ {
		out[i] = (i + n/2) % n
	}
	return out
}
