package netx

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

func TestReliableSenderAllocatesIncreasingSeq(t *testing.T) {
	s := newReliableSender(nil)
	var seqs []uint16
	for i := 0; i < 5; i++ {
		seq, ok := s.Send([]byte("x"))
		if !ok {
			t.Fatalf("第 %d 次发送失败", i)
		}
		seqs = append(seqs, seq)
	}
	for i := 1; i < len(seqs); i++ {
		if !SeqGreater(seqs[i], seqs[i-1]) {
			t.Fatalf("序号未递增：%v", seqs)
		}
	}
}

// 调用方复用缓冲区是 Go 的常规写法，Send 必须复制，否则重传时发出去的是
// 后来被覆盖的内容。这个 bug 只在重传路径上暴露，极难发现。
func TestReliableSenderCopiesPayload(t *testing.T) {
	s := newReliableSender(nil)
	buf := []byte("original")
	seq, _ := s.Send(buf)

	// 调用方复用同一个缓冲区写新内容。
	copy(buf, "OVERWRIT")

	// 模拟重传取包。
	due := s.Due()
	_ = due
	// 直接检查在途队列里的内容没被改。
	var found bool
	for _, p := range s.inflight {
		if p.seq == seq {
			if string(p.payload) != "original" {
				t.Fatalf("载荷被调用方的写入覆盖了：%q", p.payload)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("没找到在途包")
	}
}

func TestReliableSenderWindowLimit(t *testing.T) {
	s := newReliableSender(nil)
	for i := 0; i < sendWindow; i++ {
		if _, ok := s.Send([]byte("x")); !ok {
			t.Fatalf("第 %d 个包不该被拒", i)
		}
	}
	if _, ok := s.Send([]byte("x")); ok {
		t.Fatal("窗口满后应拒绝")
	}
}

func TestReliableSenderAckRemovesInFlight(t *testing.T) {
	s := newReliableSender(nil)
	for i := 0; i < 10; i++ {
		s.Send([]byte("x"))
	}
	if s.InFlight() != 10 {
		t.Fatalf("在途 %d，期望 10", s.InFlight())
	}

	// ackSeq=4 只说"收到过 4"，位图全 1 才表示 0..3 也收到了。
	s.Ack(4, 0xFFFFFFFF)
	if s.InFlight() != 5 {
		t.Fatalf("ack 0..4 后应在途 5（seq 5..9），实际 %d", s.InFlight())
	}
}

// 选择性 ack：位图能确认"中间丢掉的那些"之后到达的包。
func TestReliableSenderSelectiveAck(t *testing.T) {
	s := newReliableSender(nil)
	for i := 0; i < 5; i++ {
		s.Send([]byte("x"))
	}
	// 对端收到了 4（最大）、3、2，但 1 和 0 没收到。
	// 位图第 0 位 = 3，第 1 位 = 2。
	s.Ack(4, 0b11)

	if s.InFlight() != 2 {
		t.Fatalf("应剩 2 个在途（seq 0、1），实际 %d", s.InFlight())
	}
	if s.inflight[0].seq != 0 || s.inflight[1].seq != 1 {
		t.Fatalf("剩余的应是 seq 0、1，实际 %d、%d", s.inflight[0].seq, s.inflight[1].seq)
	}
}

// 落在位图窗口之外的旧包不能被当成已确认——那会导致消息永久丢失。
func TestReliableSenderDoesNotAckBeyondWindow(t *testing.T) {
	s := newReliableSender(nil)
	for i := 0; i < 100; i++ {
		s.Send([]byte("x"))
	}
	// 对端只收到了 seq=99，前面全丢。
	s.Ack(99, 0)

	if s.InFlight() != 99 {
		t.Fatalf("只有 seq=99 被确认，应剩 99 个在途，实际 %d", s.InFlight())
	}
	// seq=0 必须还在队列里等重传。
	var found bool
	for _, p := range s.inflight {
		if p.seq == 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("seq=0 被误认为已确认，它将永远丢失")
	}
}

// 迟到的旧 ack 不能把已经确认掉的包还原回来（那会导致重复交付）。
func TestReliableSenderIgnoresStaleAck(t *testing.T) {
	s := newReliableSender(nil)
	for i := 0; i < 10; i++ {
		s.Send([]byte("x"))
	}
	s.Ack(8, 0)
	before := s.InFlight()

	// 一个乱序到达的旧 ack
	s.Ack(3, 0)
	if s.InFlight() != before {
		t.Fatalf("旧 ack 改变了在途数：%d → %d", before, s.InFlight())
	}
}

// 重传必须按 RTO 触发，且逐包指数退避到上限。
func TestReliableSenderRetransmitTiming(t *testing.T) {
	clock := newFakeClock()
	s := newReliableSender(clock.now)

	s.Send([]byte("x"))

	// 还没到 RTO
	clock.advance(RTOInitial - time.Millisecond)
	if due := s.Due(); len(due) != 0 {
		t.Fatalf("未到 RTO 不该重传，却重传了 %d 个", len(due))
	}

	// 到 RTO
	clock.advance(2 * time.Millisecond)
	due := s.Due()
	if len(due) != 1 {
		t.Fatalf("到 RTO 应重传 1 个，实际 %d", len(due))
	}

	// attempts 现在是 2，下次等待 RTOInitial×2 = 200ms
	clock.advance(150 * time.Millisecond)
	if due := s.Due(); len(due) != 0 {
		t.Fatal("退避期内不该重传")
	}
	clock.advance(60 * time.Millisecond)
	if due := s.Due(); len(due) != 1 {
		t.Fatal("退避期满（200ms）应重传")
	}

	// attempts 现在是 3，下次等待 400ms
	clock.advance(250 * time.Millisecond)
	if due := s.Due(); len(due) != 0 {
		t.Fatal("400ms 还没到，不该重传")
	}
	clock.advance(200 * time.Millisecond)
	if due := s.Due(); len(due) != 1 {
		t.Fatal("400ms 到了应重传")
	}
}

// 连续丢包时 RTO 必须退避到上限就停下，不能无限增长。
func TestReliableSenderRetransmitBackoffCapped(t *testing.T) {
	clock := newFakeClock()
	s := newReliableSender(clock.now)
	s.Send([]byte("x"))

	// 反复重传直到 attempts 很大
	for i := 0; i < 20; i++ {
		clock.advance(RTOMax)
		s.Due()
	}

	// 再等 RTOMax 应该还有重传（上限是 RTOMax，不是更长）
	clock.advance(RTOMax)
	if due := s.Due(); len(due) != 1 {
		t.Fatalf("退避应封顶在 %v，实际没有触发重传", RTOMax)
	}
}

// 退避是逐包的：同一个包反复重传，等待时间以 2 的幂增长并封顶 RTOMax。
// 收到 ack 后这个包的退避状态随包一起消失，新发的包从 RTOInitial 起算。
func TestReliableSenderRtoResetsOnAck(t *testing.T) {
	clock := newFakeClock()
	s := newReliableSender(clock.now)

	s.Send([]byte("x"))
	s.Send([]byte("y"))

	// 把 seq=0 的退避推到上限
	for i := 0; i < 10; i++ {
		clock.advance(RTOMax)
		s.Due()
	}
	var p0 *outstandingPacket
	for _, p := range s.inflight {
		if p.seq == 0 {
			p0 = p
		}
	}
	if p0 == nil {
		t.Fatal("seq=0 不在队列里")
	}
	if p0.attempts < 5 {
		t.Fatalf("重传多次后 attempts 应很大，实际 %d", p0.attempts)
	}
	if wait := s.rtoFor(p0.attempts); wait != RTOMax {
		t.Fatalf("退避应封顶 %v，实际 %v", RTOMax, wait)
	}

	// 确认掉 seq=0
	s.Ack(0, 0)
	if s.InFlight() != 1 {
		t.Fatalf("应只剩 seq=1，实际 %d", s.InFlight())
	}
	// 新发的包必须从 RTOInitial 起算，不能继承旧包的退避。
	newSeq, ok := s.Send([]byte("z"))
	if !ok {
		t.Fatal("发送失败")
	}
	for _, p := range s.inflight {
		if p.seq == newSeq {
			if p.attempts != 1 {
				t.Fatalf("新包 attempts 应为 1，实际 %d", p.attempts)
			}
		}
	}
}

func TestReliableSenderStats(t *testing.T) {
	clock := newFakeClock()
	s := newReliableSender(clock.now)

	s.Send([]byte("a"))
	s.Send([]byte("b"))
	clock.advance(RTOInitial + time.Millisecond)
	s.Due()

	st := s.Stats()
	if st.Sent != 2 {
		t.Errorf("Sent = %d，期望 2", st.Sent)
	}
	if st.Retransmit != 2 {
		t.Errorf("Retransmit = %d，期望 2", st.Retransmit)
	}

	// 位图全 1 才能真正确认 seq 0 和 1（ackSeq=1 只说"收到过 1"）
	s.Ack(1, 0xFFFFFFFF)
	if st := s.Stats(); st.Acked != 2 {
		t.Errorf("Acked = %d，期望 2", st.Acked)
	}
}

// ---------- 接收侧 ----------

func TestReliableReceiverInOrderDelivery(t *testing.T) {
	r := newReliableReceiver()

	if got := r.Receive(0, []byte("a")); len(got) != 1 || string(got[0]) != "a" {
		t.Fatalf("第一个包应立刻交付，得到 %v", got)
	}
	if got := r.Receive(1, []byte("b")); len(got) != 1 || string(got[0]) != "b" {
		t.Fatalf("顺序到达应立即交付，得到 %v", got)
	}
}

// 乱序到达时必须缓冲，等补齐后一次性按序交付。
func TestReliableReceiverReorders(t *testing.T) {
	r := newReliableReceiver()

	if got := r.Receive(0, []byte("a")); len(got) != 1 {
		t.Fatal("0 应交付")
	}
	// 2 先到（1 丢了）
	if got := r.Receive(2, []byte("c")); len(got) != 0 {
		t.Fatalf("2 不该交付（缺 1），得到 %d 个", len(got))
	}
	if r.Pending() != 1 {
		t.Fatalf("应有 1 个待交付，实际 %d", r.Pending())
	}

	// 1 到了，应连续交付 bc
	got := r.Receive(1, []byte("b"))
	if len(got) != 2 {
		t.Fatalf("应交付 2 个，实际 %d", len(got))
	}
	if string(got[0]) != "b" || string(got[1]) != "c" {
		t.Fatalf("交付顺序错：%q %q", got[0], got[1])
	}
	if r.Pending() != 0 {
		t.Fatal("清空后不该有残留")
	}
}

// 重复包必须被丢弃，不能重复交付——上层会因此执行两次同样的操作。
func TestReliableReceiverDropsDuplicate(t *testing.T) {
	r := newReliableReceiver()

	r.Receive(0, []byte("a"))
	if got := r.Receive(0, []byte("a")); len(got) != 0 {
		t.Fatalf("重复包不该交付，得到 %v", got)
	}
	if st := r.Stats(); st.Dropped != 1 {
		t.Errorf("Dropped = %d，期望 1", st.Dropped)
	}
}

// 已经交付过的旧包（比 nextExpected 还小）也必须丢。
func TestReliableReceiverDropsOld(t *testing.T) {
	r := newReliableReceiver()
	r.Receive(0, []byte("a"))
	r.Receive(1, []byte("b"))

	if got := r.Receive(0, []byte("a")); len(got) != 0 {
		t.Fatal("旧包不该交付")
	}
}

// ack 信息必须反映真实收到的情况。
//
// 语义：ackSeq = 收到过的**最大**序号（不要求连续），位图第 i 位表示
// ackSeq-1-i 是否收到（含已交付的）。
func TestReliableReceiverAckInfo(t *testing.T) {
	r := newReliableReceiver()

	if _, _, ok := r.AckInfo(); ok {
		t.Fatal("没收到任何包时不该有 ack")
	}

	r.Receive(0, []byte("a"))
	ackSeq, ackBits, ok := r.AckInfo()
	if !ok {
		t.Fatal("收到后应有 ack")
	}
	if ackSeq != 0 || ackBits != 0 {
		t.Fatalf("只有 seq 0 时 ack=(%d,%#x)，期望 (0,0)", ackSeq, ackBits)
	}

	// 收到 2 但缺 1：ackSeq 应推进到 2（表示"我收到过 2"），
	// 位图第 0 位（seq=1）为 0，第 1 位（seq=0，已交付）为 1。
	r.Receive(2, []byte("c"))
	ackSeq, ackBits, _ = r.AckInfo()
	if ackSeq != 2 {
		t.Fatalf("ackSeq 应是最大收到序号 2，实际 %d", ackSeq)
	}
	if ackBits != 0b10 {
		t.Fatalf("位图应表示「0 已收、1 没收到」，期望 0b10，实际 %#b", ackBits)
	}

	// 补上 1，位图应变成全收（0、1、2 都确认）
	r.Receive(1, []byte("b"))
	ackSeq, ackBits, _ = r.AckInfo()
	if ackSeq != 2 {
		t.Fatalf("ackSeq 应仍是 2，实际 %d", ackSeq)
	}
	if ackBits != 0b11 {
		t.Fatalf("补齐后 0、1 都应收，期望 0b11，实际 %#b", ackBits)
	}
}

// 位图必须能告诉对方"我收到了 3 和 4，但 2 没收到"。
//
// 这正是选择性重传的意义：对方只需重发 seq=2 一个包，不必重发 3、4。
func TestReliableReceiverAckBitsReflectGaps(t *testing.T) {
	r := newReliableReceiver()
	r.Receive(0, []byte("a"))
	r.Receive(1, []byte("b"))
	// 制造缺口：2 不来，来 3 和 4
	r.Receive(3, []byte("d"))
	r.Receive(4, []byte("e"))

	ackSeq, ackBits, _ := r.AckInfo()
	if ackSeq != 4 {
		t.Fatalf("ackSeq 应是最大收到序号 4，实际 %d", ackSeq)
	}
	// 位图：bit0=seq3(收到) bit1=seq2(缺) bit2=seq1(已交付) bit3=seq0(已交付)
	const want = 0b1101
	if ackBits != want {
		t.Fatalf("位图应是 %#b（3收、2缺、1收、0收），实际 %#b", want, ackBits)
	}

	// 对方据此只重传 seq=2，补齐后应一次性交付 2、3、4。
	got := r.Receive(2, []byte("c"))
	if len(got) != 3 {
		t.Fatalf("应连续交付 3 条，实际 %d", len(got))
	}
	if string(got[0]) != "c" || string(got[1]) != "d" || string(got[2]) != "e" {
		t.Fatalf("交付顺序错：%q %q %q", got[0], got[1], got[2])
	}
}

// 跨回绕的序号必须正确处理，否则长时间运行后连接卡死。
//
// 接收端序号固定从 0 起（见 reliableReceiver.nextExpected 的注释），
// 所以这里直接把内部状态推到回绕边缘，而不是真的发 65533 个包。
func TestReliableReceiverWraparound(t *testing.T) {
	r := newReliableReceiver()
	r.nextExpected = 65533
	r.maxSeen = 65533
	r.haveReceived = true

	for i := 0; i < 10; i++ {
		seq := uint16(65533 + i) // 65533, 65534, 65535, 0, 1, ...
		got := r.Receive(seq, []byte{byte(i)})
		if len(got) != 1 {
			t.Fatalf("第 %d 个（seq=%d）应交付，实际 %d 个", i, seq, len(got))
		}
		if got[0][0] != byte(i) {
			t.Fatalf("第 %d 个内容错误：%d", i, got[0][0])
		}
	}
}

// 回绕后的乱序也要能重组。
func TestReliableReceiverWraparoundOutOfOrder(t *testing.T) {
	r := newReliableReceiver()
	r.nextExpected = 65535
	r.maxSeen = 65535
	r.haveReceived = true

	if got := r.Receive(65535, []byte("a")); len(got) != 1 {
		t.Fatal("65535 应交付")
	}
	// seq 1 先到（0 还没来）
	if got := r.Receive(1, []byte("c")); len(got) != 0 {
		t.Fatalf("1 不该交付（缺 0），得到 %d", len(got))
	}
	// 0 到了，应连续交付 0、1
	got := r.Receive(0, []byte("b"))
	if len(got) != 2 {
		t.Fatalf("应连续交付 2 个，实际 %d", len(got))
	}
	if string(got[0]) != "b" || string(got[1]) != "c" {
		t.Fatalf("顺序错误：%q %q", got[0], got[1])
	}
}

// 回绕点的 ack 位图也必须正确，否则回绕瞬间会误报确认。
func TestReliableReceiverAckBitsAtWraparound(t *testing.T) {
	r := newReliableReceiver()
	r.nextExpected = 65534
	r.maxSeen = 65534
	r.haveReceived = true

	// 交付 65534、65535，然后 0 丢失、1 到达
	r.Receive(65534, []byte("a"))
	r.Receive(65535, []byte("b"))
	r.Receive(1, []byte("d"))

	ackSeq, ackBits, _ := r.AckInfo()
	if ackSeq != 1 {
		t.Fatalf("ackSeq 应是 1，实际 %d", ackSeq)
	}
	// bit0 = seq 0（没收到 → 0）
	// bit1 = seq 65535（已交付 → 1）
	// bit2 = seq 65534（已交付 → 1）
	const want = 0b110
	if ackBits != want {
		t.Fatalf("位图应是 %#b（0 缺、65535 收、65534 收），实际 %#b", want, ackBits)
	}

	// 补上 0，应交付 0、1
	got := r.Receive(0, []byte("c"))
	if len(got) != 2 {
		t.Fatalf("应交付 2 条，实际 %d", len(got))
	}
	if string(got[0]) != "c" || string(got[1]) != "d" {
		t.Fatalf("顺序错误：%q %q", got[0], got[1])
	}
}

// 接收端也必须复制载荷，否则 UDP 缓冲区复用会让缓存的内容被覆盖。
func TestReliableReceiverCopiesPayload(t *testing.T) {
	r := newReliableReceiver()
	// 先制造一个缺口，让包进入缓冲
	r.Receive(0, []byte("a"))

	buf := []byte("bc")
	r.Receive(2, buf)
	copy(buf, "XX") // 复用

	got := r.Receive(1, []byte("b"))
	if len(got) != 2 {
		t.Fatalf("应交付 2 个，实际 %d", len(got))
	}
	if string(got[1]) != "bc" {
		t.Fatalf("缓冲的载荷被覆盖了：%q", got[1])
	}
}

func TestReliableReceiverReset(t *testing.T) {
	r := newReliableReceiver()
	r.Receive(0, []byte("a"))
	r.Receive(2, []byte("c"))
	if r.Pending() != 1 {
		t.Fatal("应有 1 个待交付")
	}
	r.Reset()
	if r.Pending() != 0 {
		t.Fatal("Reset 后应清空")
	}
	if _, _, ok := r.AckInfo(); ok {
		t.Fatal("Reset 后不该有 ack")
	}
}

// 端到端：发送侧 + 接收侧跑一遍完整对话，包含丢包与重传。
func TestReliableEndToEndWithLoss(t *testing.T) {
	clock := newFakeClock()
	sender := newReliableSender(clock.now)
	receiver := newReliableReceiver()

	msgs := []string{"hello", "world", "third", "fourth", "fifth"}
	for _, m := range msgs {
		sender.Send([]byte(m))
	}

	// 模拟：seq=1 丢了，其余正常到达。
	// seq=0 能按序交付，但 1 之后的全被卡住（顺序保证）。
	delivered := deliverAll(sender, receiver, map[uint16]bool{1: true})
	if len(delivered) != 1 || string(delivered[0]) != "hello" {
		t.Fatalf("只有 seq=0 能交付，实际交付 %d 条", len(delivered))
	}
	if receiver.Pending() != 3 {
		t.Fatalf("2、3、4 应在缓冲里等 1，实际 %d", receiver.Pending())
	}

	// 对端回 ack：ackSeq=4（收到过 4），位图告诉对方 1 没收到。
	ackSeq, ackBits, ok := receiver.AckInfo()
	if !ok {
		t.Fatal("应有 ack")
	}
	if ackSeq != 4 {
		t.Fatalf("ackSeq 应是 4，实际 %d", ackSeq)
	}
	// bit0=seq3(收到) bit1=seq2(收到) bit2=seq1(缺) bit3=seq0(已交付)
	if want := uint32(0b1011); ackBits != want {
		t.Fatalf("位图应是 %#b，实际 %#b", want, ackBits)
	}
	sender.Ack(ackSeq, ackBits)

	// 除 seq=1 外的包都被确认，队列里只剩它。
	if sender.InFlight() != 1 {
		t.Fatalf("应只剩 seq=1 待重传，实际 %d", sender.InFlight())
	}

	// 等 RTO，重传
	clock.advance(RTOInitial + time.Millisecond)
	due := sender.Due()
	if len(due) != 1 {
		t.Fatalf("应重传 1 个包，实际 %d", len(due))
	}
	if due[0].seq != 1 {
		t.Fatalf("应重传 seq=1，实际 %d", due[0].seq)
	}

	// 重传的包到达，应一次性交付剩余全部
	got := receiver.Receive(due[0].seq, due[0].payload)
	if len(got) != 4 {
		t.Fatalf("补齐后应交付 4 条，实际 %d", len(got))
	}
	for i, m := range msgs[1:] {
		if string(got[i]) != m {
			t.Fatalf("第 %d 条 = %q，期望 %q", i, got[i], m)
		}
	}

	// 确认全部收到
	ackSeq, ackBits, _ = receiver.AckInfo()
	sender.Ack(ackSeq, ackBits)
	if sender.InFlight() != 0 {
		t.Fatalf("全部确认后不该有在途，实际 %d", sender.InFlight())
	}
}

// deliverAll 把发送侧所有在途包（不含 drop 里的）投递给接收侧，
// 返回接收侧交付出来的消息。
func deliverAll(s *reliableSender, r *reliableReceiver, drop map[uint16]bool) [][]byte {
	var delivered [][]byte
	for _, p := range s.inflight {
		if drop[p.seq] {
			continue
		}
		delivered = append(delivered, r.Receive(p.seq, p.payload)...)
	}
	return delivered
}

// 随机丢包下，靠重传最终必须全部送达且顺序正确。
func TestReliableEndToEndRandomLoss(t *testing.T) {
	clock := newFakeClock()
	sender := newReliableSender(clock.now)
	receiver := newReliableReceiver()

	const n = 50
	want := make([]string, n)
	for i := 0; i < n; i++ {
		want[i] = fmt.Sprintf("msg-%03d", i)
		sender.Send([]byte(want[i]))
	}

	// 固定种子的确定性丢包模式：每 3 个丢 1 个
	dropped := make(map[uint16]bool)
	for i := 0; i < n; i += 3 {
		dropped[uint16(i)] = true
	}

	var got []string
	rounds := 0
	for len(got) < n && rounds < 200 {
		rounds++

		// 首发（第一轮）或重传
		var toSend []*outstandingPacket
		if rounds == 1 {
			toSend = sender.inflight
		} else {
			toSend = sender.Due()
		}

		for _, p := range toSend {
			// 第一轮里被标记的丢掉；之后的轮次都送达
			if rounds == 1 && dropped[p.seq] {
				continue
			}
			for _, m := range receiver.Receive(p.seq, p.payload) {
				got = append(got, string(m))
			}
		}

		// 回 ack
		if ackSeq, ackBits, ok := receiver.AckInfo(); ok {
			sender.Ack(ackSeq, ackBits)
		}
		clock.advance(RTOInitial + time.Millisecond)
	}

	if len(got) != n {
		t.Fatalf("只收到 %d/%d 条，用了 %d 轮", len(got), n, rounds)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 条 = %q，期望 %q（顺序错乱）", i, got[i], want[i])
		}
	}
}

func BenchmarkReliableSenderAck(b *testing.B) {
	s := newReliableSender(nil)
	for i := 0; i < sendWindow; i++ {
		s.Send([]byte("0123456789"))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Ack(uint16(i), 0xFFFF)
	}
}

func BenchmarkReliableReceiverInOrder(b *testing.B) {
	r := newReliableReceiver()
	payload := bytes.Repeat([]byte("x"), 64)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Receive(uint16(i), payload)
	}
}
