package netx

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// drainOut 把会话下行队列里现有的包全部取出。
func drainOut(s *Session) [][]byte {
	var out [][]byte
	for {
		select {
		case pkt := <-s.out:
			out = append(out, pkt)
		default:
			return out
		}
	}
}

// mustHeader 解析包并断言成功。
func mustHeader(t *testing.T, pkt []byte) Header {
	t.Helper()
	h, err := DecodeHeader(pkt)
	if err != nil {
		t.Fatalf("解析包头失败：%v", err)
	}
	return h
}

func TestSessionSendSnapshotEncodesHeader(t *testing.T) {
	s := newSession(1, nil, 8, time.Now())
	if s.ID() != 1 {
		t.Fatalf("ID = %d，期望 1", s.ID())
	}
	if s.Slot() != -1 {
		t.Fatalf("初始槽位 = %d，期望 -1", s.Slot())
	}

	payload := []byte("快照载荷")
	if !s.SendSnapshot(payload) {
		t.Fatal("投递快照失败")
	}

	pkts := drainOut(s)
	if len(pkts) != 1 {
		t.Fatalf("队列里 %d 个包，期望 1", len(pkts))
	}
	h := mustHeader(t, pkts[0])
	if h.Channel != ChannelSnapshot {
		t.Fatalf("通道 = %v，期望快照", h.Channel)
	}
	if h.Version != Version {
		t.Fatalf("版本 = %d，期望 %d", h.Version, Version)
	}
	if got := string(Payload(pkts[0])); got != string(payload) {
		t.Fatalf("载荷 = %q，期望 %q", got, payload)
	}
}

func TestSessionSnapshotSeqIncreases(t *testing.T) {
	s := newSession(1, nil, 8, time.Now())
	for i := 0; i < 5; i++ {
		s.SendSnapshot([]byte{byte(i)})
	}
	pkts := drainOut(s)
	if len(pkts) != 5 {
		t.Fatalf("投递 %d 个，期望 5", len(pkts))
	}
	for i, pkt := range pkts {
		h := mustHeader(t, pkt)
		if int(h.Seq) != i {
			t.Fatalf("第 %d 个包 seq = %d，期望 %d", i, h.Seq, i)
		}
	}
}

func TestSessionSendReliableTracksInFlight(t *testing.T) {
	s := newSession(1, nil, 8, time.Now())
	if !s.SendReliable([]byte("握手")) {
		t.Fatal("发送可靠消息失败")
	}
	if got := s.InFlight(); got != 1 {
		t.Fatalf("在途 %d，期望 1", got)
	}

	pkts := drainOut(s)
	if len(pkts) != 1 {
		t.Fatalf("队列 %d 个包，期望 1", len(pkts))
	}
	h := mustHeader(t, pkts[0])
	if h.Channel != ChannelReliable {
		t.Fatalf("通道 = %v，期望可靠", h.Channel)
	}
	if h.Seq != 0 {
		t.Fatalf("首个可靠包 seq = %d，期望 0", h.Seq)
	}
}

func TestSessionAckRemovesInFlight(t *testing.T) {
	s := newSession(1, nil, 8, time.Now())
	s.SendReliable([]byte("a"))
	s.SendReliable([]byte("b"))
	if s.InFlight() != 2 {
		t.Fatalf("在途 %d，期望 2", s.InFlight())
	}
	drainOut(s)

	// 对方确认了 seq 0（ackSeq=0，位图为空）。必须带 FlagHasAck，
	// 否则 ackSeq=0 与"还没收到过包"的零值无法区分。
	h := Header{Version: Version, Channel: ChannelSnapshot, AckSeq: 0, Flags: FlagHasAck}
	if _, _, err := s.HandlePacket(h, nil, time.Now()); err != nil {
		t.Fatalf("处理包失败：%v", err)
	}
	if s.InFlight() != 1 {
		t.Fatalf("确认后在途 %d，期望 1", s.InFlight())
	}
}

// TestSessionZeroAckWithoutFlagIsIgnored 检查没有 FlagHasAck 的 ackSeq=0
// 不能被当作"确认了 seq 0"——否则我们真的丢了 seq 0 时，对端一个还没
// 收到过任何包的初始包就会让我们把待重传的消息永久删掉。
func TestSessionZeroAckWithoutFlagIsIgnored(t *testing.T) {
	s := newSession(1, nil, 8, time.Now())
	s.SendReliable([]byte("不能丢"))
	drainOut(s)

	h := Header{Version: Version, Channel: ChannelSnapshot, AckSeq: 0}
	if _, _, err := s.HandlePacket(h, nil, time.Now()); err != nil {
		t.Fatalf("处理包失败：%v", err)
	}
	if got := s.InFlight(); got != 1 {
		t.Fatalf("无标志的 ackSeq=0 误删了在途包，在途 = %d，期望 1", got)
	}
}

func TestSessionHandleReliableDeliversInOrder(t *testing.T) {
	s := newSession(1, nil, 8, time.Now())

	// 先喂 seq=1，应当缓存不交付。
	h1 := Header{Version: Version, Channel: ChannelReliable, Seq: 1}
	msgs, _, err := s.HandlePacket(h1, []byte("第二条"), time.Now())
	if err != nil {
		t.Fatalf("处理失败：%v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("乱序包提前交付了 %d 条", len(msgs))
	}

	// 再喂 seq=0，两条一起按序交付。
	h0 := Header{Version: Version, Channel: ChannelReliable, Seq: 0}
	msgs, _, err = s.HandlePacket(h0, []byte("第一条"), time.Now())
	if err != nil {
		t.Fatalf("处理失败：%v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("交付 %d 条，期望 2", len(msgs))
	}
	if string(msgs[0]) != "第一条" || string(msgs[1]) != "第二条" {
		t.Fatalf("交付顺序错误：%q %q", msgs[0], msgs[1])
	}
}

func TestSessionHandleSnapshotDropsStale(t *testing.T) {
	s := newSession(1, nil, 8, time.Now())

	h5 := Header{Version: Version, Channel: ChannelSnapshot, Seq: 5}
	_, snap, err := s.HandlePacket(h5, []byte("五"), time.Now())
	if err != nil || snap == nil {
		t.Fatalf("首个快照应被接受，snap=%v err=%v", snap, err)
	}

	// 更旧的包必须丢，否则画面会倒退。
	for _, old := range []uint16{3, 5} {
		h := Header{Version: Version, Channel: ChannelSnapshot, Seq: old}
		_, snap, err := s.HandlePacket(h, []byte("旧"), time.Now())
		if err != nil {
			t.Fatalf("处理旧包返回错误：%v", err)
		}
		if snap != nil {
			t.Fatalf("seq=%d 的旧快照未被丢弃", old)
		}
	}

	// 更新的包正常接受。
	h6 := Header{Version: Version, Channel: ChannelSnapshot, Seq: 6}
	_, snap, err = s.HandlePacket(h6, []byte("六"), time.Now())
	if err != nil || snap == nil {
		t.Fatalf("更新的快照应被接受，snap=%v err=%v", snap, err)
	}
}

func TestSessionHandleSnapshotWraparound(t *testing.T) {
	s := newSession(1, nil, 8, time.Now())

	h := Header{Version: Version, Channel: ChannelSnapshot, Seq: 65534}
	if _, snap, _ := s.HandlePacket(h, []byte("尾"), time.Now()); snap == nil {
		t.Fatal("65534 应被接受")
	}

	// 回绕到 0 必须被判为"更新"，直接用 h.Seq > last 会误判为旧包。
	h0 := Header{Version: Version, Channel: ChannelSnapshot, Seq: 0}
	if _, snap, _ := s.HandlePacket(h0, []byte("头"), time.Now()); snap == nil {
		t.Fatal("回绕后的 0 应被判为更新")
	}
}

func TestSessionHandleBadChannel(t *testing.T) {
	s := newSession(1, nil, 8, time.Now())
	h := Header{Version: Version, Channel: Channel(9)}
	_, _, err := s.HandlePacket(h, nil, time.Now())
	if !errors.Is(err, ErrBadChannel) {
		t.Fatalf("错误 = %v，期望 ErrBadChannel", err)
	}
}

func TestSessionQueueDropsOldest(t *testing.T) {
	const queueSize = 3
	s := newSession(1, nil, queueSize, time.Now())

	// 灌满队列。
	for i := 0; i < queueSize; i++ {
		if !s.SendSnapshot([]byte{byte(i)}) {
			t.Fatalf("第 %d 个应投递成功", i)
		}
	}
	// 再来一个应挤掉最旧的（载荷 0）。
	if !s.SendSnapshot([]byte{99}) {
		t.Fatal("队列满时应丢旧的后投递成功")
	}

	pkts := drainOut(s)
	if len(pkts) != queueSize {
		t.Fatalf("队列长度 %d，期望 %d", len(pkts), queueSize)
	}
	first := Payload(pkts[0])
	if len(first) != 1 || first[0] != 1 {
		t.Fatalf("最旧的包未被丢弃，队首载荷 = %v，期望 [1]", first)
	}
	last := Payload(pkts[len(pkts)-1])
	if len(last) != 1 || last[0] != 99 {
		t.Fatalf("队尾载荷 = %v，期望 [99]", last)
	}
	if got := s.Stats(time.Now()).DroppedSend; got != 1 {
		t.Fatalf("丢弃计数 = %d，期望 1", got)
	}
}

func TestSessionCloseIsSafeAndIdempotent(t *testing.T) {
	s := newSession(1, nil, 4, time.Now())
	s.Close()
	s.Close()
	if !s.Closed() {
		t.Fatal("Close 后应报告已关闭")
	}
	if s.SendSnapshot([]byte("x")) {
		t.Fatal("已关闭的会话不应投递成功")
	}
	if s.SendReliable([]byte("x")) {
		t.Fatal("已关闭的会话不应发送可靠消息")
	}
	if n := s.DrainDue(); n != 0 {
		t.Fatalf("已关闭的会话重传了 %d 个包", n)
	}
}

func TestSessionCloseWhileSendingDoesNotPanic(t *testing.T) {
	// 这是 Go 里最经典的关闭竞态：发送方卡在 `out <- pkt` 上，
	// 另一侧 close(out) → panic: send on closed channel。
	// Session 用 done channel 而非 close(out) 就是为了避开它。
	for iter := 0; iter < 50; iter++ {
		s := newSession(uint64(iter), nil, 1, time.Now())

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				s.SendSnapshot([]byte{byte(i)})
			}
		}()

		time.Sleep(time.Microsecond)
		s.Close()
		wg.Wait()
	}
}

func TestSessionFragmentedReliable(t *testing.T) {
	s := newSession(1, nil, 8, time.Now())

	big := make([]byte, 3000)
	for i := range big {
		big[i] = byte(i)
	}

	// 切成 3 片，故意乱序送入。
	parts := [][]byte{big[0:1000], big[1000:2000], big[2000:]}
	order := []int{2, 0, 1}

	var delivered [][]byte
	for _, idx := range order {
		fh := FragHeader{MsgID: 7, Index: uint16(idx), Count: 3}
		body := make([]byte, FragHeaderSize, FragHeaderSize+len(parts[idx]))
		fh.Encode(body)
		body = append(body, parts[idx]...)

		h := Header{Version: Version, Channel: ChannelReliable, Seq: 0, Flags: FlagFragmented}
		if idx == 2 {
			h.Flags |= FlagLastFrag
		}
		msgs, _, err := s.HandlePacket(h, body, time.Now())
		if err != nil {
			t.Fatalf("第 %d 片处理失败：%v", idx, err)
		}
		delivered = append(delivered, msgs...)
	}

	if len(delivered) != 1 {
		t.Fatalf("交付 %d 条，期望 1", len(delivered))
	}
	if len(delivered[0]) != len(big) {
		t.Fatalf("重组长度 %d，期望 %d", len(delivered[0]), len(big))
	}
	for i := range big {
		if delivered[0][i] != big[i] {
			t.Fatalf("第 %d 字节不符：%d != %d", i, delivered[0][i], big[i])
		}
	}
}

func TestSessionDrainDueRetransmits(t *testing.T) {
	s := newSession(1, nil, 8, time.Now())
	s.SendReliable([]byte("会丢的"))
	drainOut(s)

	// 未超时前不该重传。
	if n := s.DrainDue(); n != 0 {
		t.Fatalf("未超时就重传了 %d 个", n)
	}

	time.Sleep(RTOInitial + 40*time.Millisecond)

	if n := s.DrainDue(); n != 1 {
		t.Fatalf("超时后重传 %d 个，期望 1", n)
	}
	pkts := drainOut(s)
	if len(pkts) != 1 {
		t.Fatalf("队列 %d 个包，期望 1", len(pkts))
	}
	h := mustHeader(t, pkts[0])
	if h.Seq != 0 {
		t.Fatalf("重传包 seq = %d，期望 0", h.Seq)
	}
	if string(Payload(pkts[0])) != "会丢的" {
		t.Fatalf("重传载荷 = %q", Payload(pkts[0]))
	}
	if got := s.InFlight(); got != 1 {
		t.Fatalf("重传后在途 %d，期望仍为 1", got)
	}
}

func TestSessionAckReflectedInOutgoingHeader(t *testing.T) {
	s := newSession(1, nil, 8, time.Now())

	// 收到对方 seq=0 的可靠消息后，我们回发的快照包里应带上 ack。
	h := Header{Version: Version, Channel: ChannelReliable, Seq: 0}
	if _, _, err := s.HandlePacket(h, []byte("hello"), time.Now()); err != nil {
		t.Fatalf("处理失败：%v", err)
	}

	s.SendSnapshot([]byte("snap"))
	pkts := drainOut(s)
	if len(pkts) != 1 {
		t.Fatalf("队列 %d 个包", len(pkts))
	}
	got := mustHeader(t, pkts[0])

	// 必须带 FlagHasAck，否则对端无法把"确认了 seq 0"和"零值"区分开。
	if !got.Flags.Has(FlagHasAck) {
		t.Fatal("回包缺少 FlagHasAck 标志")
	}
	// ackSeq 本身就确认了 seq 0，不需要靠位图。
	if got.AckSeq != 0 {
		t.Fatalf("回包 ackSeq = %d，期望 0", got.AckSeq)
	}
	// 位图第 0 位代表 ackSeq-1 = 65535，我们没收到过它，必须为 0。
	// 这条断言防的是"把 ackSeq 自己也算进位图"的偏移错误。
	if got.AckBits&1 != 0 {
		t.Fatalf("回包 ackBits = %#b，第 0 位（seq 65535）不应置位", got.AckBits)
	}
}

func TestSessionStats(t *testing.T) {
	now := time.Date(2026, 11, 9, 21, 0, 0, 0, time.UTC)
	s := newSession(7, nil, 8, now)
	s.SetSlot(3)

	if got := s.Slot(); got != 3 {
		t.Fatalf("槽位 = %d，期望 3", got)
	}

	h := Header{Version: Version, Channel: ChannelSnapshot, Seq: 1}
	if _, _, err := s.HandlePacket(h, []byte("x"), now); err != nil {
		t.Fatalf("处理失败：%v", err)
	}
	s.SendSnapshot([]byte("y"))

	st := s.Stats(now)
	if st.ID != 7 || st.Slot != 3 {
		t.Fatalf("统计 ID/槽位 = %d/%d", st.ID, st.Slot)
	}
	if st.RecvPackets != 1 {
		t.Fatalf("收包数 = %d，期望 1", st.RecvPackets)
	}
	if st.SentPackets != 1 {
		t.Fatalf("发包数 = %d，期望 1", st.SentPackets)
	}
	if st.LastRecvAge != 0 {
		t.Fatalf("最后收包时长 = %v，期望 0", st.LastRecvAge)
	}
}

func TestSessionBadFragmentIncrementsCounter(t *testing.T) {
	s := newSession(1, nil, 8, time.Now())
	// 只给 2 字节，连分片头都不够。
	h := Header{Version: Version, Channel: ChannelReliable, Seq: 0, Flags: FlagFragmented}
	if _, _, err := s.HandlePacket(h, []byte{1, 2}, time.Now()); err == nil {
		t.Fatal("期望分片头解析失败")
	}
	if got := s.Stats(time.Now()).BadPackets; got != 1 {
		t.Fatalf("坏包计数 = %d，期望 1", got)
	}
}

// TestSessionConcurrentSnapshotAndHandle 让发包与收包并发跑，
// 配合 -race 验证并发约束（tick goroutine 发快照、读 goroutine 收包）成立。
func TestSessionConcurrentSnapshotAndHandle(t *testing.T) {
	s := newSession(1, nil, 64, time.Now())

	var stop atomic.Bool
	var wg sync.WaitGroup

	// 模拟 tick 循环：不断发快照。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			s.SendSnapshot([]byte("s"))
		}
	}()

	// 模拟写 goroutine：不断清空队列。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			select {
			case <-s.out:
			default:
			}
		}
	}()

	// 模拟读 goroutine：不断收包。
	wg.Add(1)
	go func() {
		defer wg.Done()
		var seq uint16
		for !stop.Load() {
			h := Header{Version: Version, Channel: ChannelSnapshot, Seq: seq}
			seq++
			_, _, _ = s.HandlePacket(h, []byte("r"), time.Now())
			s.SendReliable([]byte("rel"))
			s.DrainDue()
		}
	}()

	time.Sleep(60 * time.Millisecond)
	stop.Store(true)
	wg.Wait()

	if s.Stats(time.Now()).RecvPackets == 0 {
		t.Fatal("收包计数为 0")
	}
}
