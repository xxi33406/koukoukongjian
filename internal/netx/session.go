package netx

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Session 是一条客户端连接。
//
// 并发模型（docs/server-design-v0.1.md §17「M1 的并发约束」）：
//
//   - **收包**：只有 Server 的读 goroutine 调用 HandlePacket，它独占
//     重组器与接收侧状态。
//   - **发快照**：tick 循环直接调用 SendSnapshot。它只读原子字段
//     （地址、快照序号），然后把**编好包头**的整包投进 out 队列。
//     快照每秒数千个，绝不能走锁。
//   - **发可靠消息**：tick 循环或读 goroutine 调用 SendReliable。
//     这条路径用 mu 保护——可靠消息很稀疏（握手、事件、UI 确认），
//     锁竞争可忽略，而正确性远比这点开销重要。
//
// 这个分工不是理论洁癖：M1 之前世界是单 goroutine 的，网络层一进来就
// 打破了那个前提，把边界划清楚才能让 -race 有意义。
type Session struct {
	id   uint64
	addr *net.UDPAddr

	// slot 是房间槽位，-1 表示还没加入房间。原子是因为 tick 循环要读它。
	slot atomic.Int32

	// ---------- 以下字段由 mu 保护 ----------
	mu       sync.Mutex
	reliable *reliableSender
	recv     *reliableReceiver
	reasm    *Reassembler

	// reliableAcked/reliableBits 是"我们已确认对方的可靠序号"，用于生成回包 ack。
	reliableAcked atomic.Uint32
	reliableBits  atomic.Uint32
	hasReliable   atomic.Bool

	// peerAckSeq/peerAckBits 是对方最近一次给我们的确认。仅用于观测。
	peerAckSeq  atomic.Uint32
	peerAckBits atomic.Uint32

	// snapSeq 是快照通道的发送序号，每发一包 +1。
	snapSeq atomic.Uint32
	// snapRecvSeq 是已接收快照的最大序号，用于丢弃迟到包。
	snapRecvSeq atomic.Uint32
	hasSnapRecv atomic.Bool

	// out 是下行队列。满了就丢最旧的——网络层不能因为对端慢而阻塞 tick。
	//
	// 容量给小：快照是有时效的，积压 64 帧意味着对端已经 3 秒没响应，
	// 这些快照发出去也是废的，不如丢掉保持低延迟。
	out chan []byte

	// done 用于通知写 goroutine 退出。
	//
	// 这里**不能**用 close(out) 结束：tick goroutine 可能正卡在
	// `s.out <- pkt` 上，而 close 一个还有发送者的 channel 会直接 panic。
	// 关 done 让发送方走 select 的分支退出，是唯一不会 panic 的写法。
	done      chan struct{}
	closeOnce sync.Once

	lastRecv atomic.Int64 // unixnano
	lastSend atomic.Int64 // unixnano
	created  time.Time

	// 统计（原子，观测用）
	sentPackets   atomic.Uint64
	recvPackets   atomic.Uint64
	droppedSend   atomic.Uint64
	recvBadPacket atomic.Uint64
}

// newSession 构造会话。addr 之后不再变（同一 socket 内一个地址一条会话）。
func newSession(id uint64, addr *net.UDPAddr, queueSize int, now time.Time) *Session {
	if queueSize <= 0 {
		queueSize = 64
	}
	s := &Session{
		id:       id,
		addr:     addr,
		reliable: newReliableSender(nil),
		recv:     newReliableReceiver(),
		reasm:    NewReassembler(nil),
		out:      make(chan []byte, queueSize),
		done:     make(chan struct{}),
		created:  now,
	}
	s.slot.Store(-1)
	s.lastRecv.Store(now.UnixNano())
	return s
}

// ID 返回会话编号。
func (s *Session) ID() uint64 { return s.id }

// Addr 返回对端地址。
func (s *Session) Addr() *net.UDPAddr { return s.addr }

// Slot 返回房间槽位，-1 表示未加入房间。
func (s *Session) Slot() int32 { return s.slot.Load() }

// SetSlot 设置房间槽位。
func (s *Session) SetSlot(v int32) { s.slot.Store(v) }

// LastRecv 返回最后一次收到包的时间。
func (s *Session) LastRecv() time.Time {
	return time.Unix(0, s.lastRecv.Load())
}

// LastSend 返回最后一次发包的时间。
func (s *Session) LastSend() time.Time {
	return time.Unix(0, s.lastSend.Load())
}

// Out 返回下行队列，写 goroutine 从这里取包发出去。
func (s *Session) Out() <-chan []byte { return s.out }

// Done 返回关闭信号，写 goroutine 收到后应当排空队列再退出。
func (s *Session) Done() <-chan struct{} { return s.done }

// Close 关闭会话。重复调用安全。
//
// 注意：关闭后 out 里可能还有残留包，写 goroutine 应当先排空再退出，
// 否则最后一条可靠消息（往往是 PlayerLeft 这类告别消息）发不出去。
func (s *Session) Close() {
	s.closeOnce.Do(func() { close(s.done) })
}

// Closed 报告会话是否已关闭。
func (s *Session) Closed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// enqueue 把整包投进下行队列，满了丢最旧的。返回是否投递成功。
func (s *Session) enqueue(pkt []byte) bool {
	select {
	case <-s.done:
		return false
	default:
	}

	select {
	case s.out <- pkt:
		s.sentPackets.Add(1)
		s.lastSend.Store(time.Now().UnixNano())
		return true
	default:
	}

	// 队列满：丢**最旧**的一个，腾位置给新包。
	// 丢新的会永远滞后；丢旧的最坏情况是客户端少一帧。
	select {
	case <-s.out:
		s.droppedSend.Add(1)
	default:
	}

	select {
	case s.out <- pkt:
		s.sentPackets.Add(1)
		s.lastSend.Store(time.Now().UnixNano())
		return true
	case <-s.done:
		return false
	default:
		s.droppedSend.Add(1)
		return false
	}
}

// SendSnapshot 发送一个不可靠的快照包。
//
// 返回 false 表示队列已满或会话已关闭。**丢弃是正常路径**：快照的价值
// 随时间指数衰减，重发一个 200ms 前的快照不如让客户端用下一帧插值。
func (s *Session) SendSnapshot(payload []byte) bool {
	h := Header{
		Version: Version,
		Channel: ChannelSnapshot,
		Seq:     uint16(s.snapSeq.Add(1) - 1),
	}
	// 回包带上我们对对方的可靠确认。ack 是跨通道的：哪怕是快照包，
	// 也能让对方知道它的可靠消息收没收到，省掉专门的纯 ack 包。
	if ackSeq, ackBits, ok := s.ackInfo(); ok {
		h.AckSeq = ackSeq
		h.AckBits = ackBits
		h.Flags |= FlagHasAck
	}

	pkt := h.Append(make([]byte, 0, HeaderSize+len(payload)), payload)
	return s.enqueue(pkt)
}

// SendReliable 发一条可靠消息（会重传直到被确认）。
//
// 返回 false 表示在途窗口已满、队列已满或会话已关闭。窗口满意味着对端
// 很长时间没有确认，实际应当走会话超时而不是继续排队。
func (s *Session) SendReliable(payload []byte) bool {
	if s.Closed() {
		return false
	}

	s.mu.Lock()
	seq, ok := s.reliable.Send(payload)
	s.mu.Unlock()
	if !ok {
		return false
	}

	return s.enqueueReliable(seq, payload)
}

// enqueueReliable 把已登记的可靠包编码并投递。
func (s *Session) enqueueReliable(seq uint16, payload []byte) bool {
	h := Header{
		Version: Version,
		Channel: ChannelReliable,
		Seq:     seq,
	}
	if ackSeq, ackBits, ok := s.ackInfo(); ok {
		h.AckSeq = ackSeq
		h.AckBits = ackBits
		h.Flags |= FlagHasAck
	}
	pkt := h.Append(make([]byte, 0, HeaderSize+len(payload)), payload)
	return s.enqueue(pkt)
}

// ackInfo 返回用于回包的 ack 字段。
func (s *Session) ackInfo() (uint16, uint32, bool) {
	if !s.hasReliable.Load() {
		return 0, 0, false
	}
	return uint16(s.reliableAcked.Load()), s.reliableBits.Load(), true
}

// HandlePacket 处理一个从本会话收到的包。只能由读 goroutine 调用。
//
// 返回值：reliable 是**已组装完、可按序交付**的可靠消息载荷列表；snapshot
// 是本包携带的快照载荷（快照即包即消息，没有重组）。调用方负责解析。
func (s *Session) HandlePacket(h Header, payload []byte, now time.Time) (reliable [][]byte, snapshot []byte, err error) {
	s.lastRecv.Store(now.UnixNano())
	s.recvPackets.Add(1)

	// 无论哪个通道，对方包头里的 ack 都要处理——这是我们发出的可靠包的
	// 确认来源。必须看 FlagHasAck：ackSeq=0 既可能是"确认了 seq 0"，
	// 也可能是"还没收到过任何包"的零值，靠值本身分不出来。
	if h.Flags.Has(FlagHasAck) {
		s.peerAckSeq.Store(uint32(h.AckSeq))
		s.peerAckBits.Store(h.AckBits)
		s.mu.Lock()
		s.reliable.Ack(h.AckSeq, h.AckBits)
		s.mu.Unlock()
	}

	switch h.Channel {
	case ChannelReliable:
		return s.handleReliable(h, payload)
	case ChannelSnapshot:
		return nil, s.handleSnapshot(h, payload), nil
	default:
		return nil, nil, fmt.Errorf("%w: 通道 %d", ErrBadChannel, h.Channel)
	}
}

func (s *Session) handleReliable(h Header, payload []byte) ([][]byte, []byte, error) {
	body := payload
	if h.Flags.Has(FlagFragmented) {
		f, err := DecodeFragHeader(body)
		if err != nil {
			s.recvBadPacket.Add(1)
			return nil, nil, err
		}
		whole, done, err := s.reasm.Push(f, body[FragHeaderSize:])
		if err != nil {
			s.recvBadPacket.Add(1)
			return nil, nil, err
		}
		if !done {
			// 分片还没凑齐，但**仍需更新 ack**——对方要知道这些分片到了，
			// 否则会一直重传已经收到的分片。
			s.refreshReliableAck()
			return nil, nil, nil
		}
		body = whole
	}

	msgs := s.recv.Receive(h.Seq, body)
	s.refreshReliableAck()
	return msgs, nil, nil
}

// refreshReliableAck 刷新我们要回给对方的 ack 字段。
func (s *Session) refreshReliableAck() {
	ackSeq, ackBits, ok := s.recv.AckInfo()
	if !ok {
		return
	}
	s.reliableAcked.Store(uint32(ackSeq))
	s.reliableBits.Store(ackBits)
	s.hasReliable.Store(true)
}

func (s *Session) handleSnapshot(h Header, payload []byte) []byte {
	// 旧包直接丢：快照有时效，乱序到达的旧快照比没有更糟（会让画面倒退）。
	if s.hasSnapRecv.Load() {
		last := uint16(s.snapRecvSeq.Load())
		if h.Seq == last || !SeqGreater(h.Seq, last) {
			s.recvBadPacket.Add(1)
			return nil
		}
	}
	s.snapRecvSeq.Store(uint32(h.Seq))
	s.hasSnapRecv.Store(true)
	return payload
}

// DrainDue 把到期的重传包投递出去。由网络层的定时器周期调用。
//
// 返回本次重传的包数。
func (s *Session) DrainDue() int {
	if s.Closed() {
		return 0
	}

	s.mu.Lock()
	due := s.reliable.Due()
	s.mu.Unlock()

	for _, p := range due {
		s.enqueueReliable(p.seq, p.payload)
	}
	return len(due)
}

// SweepReassembly 清理超时的半成品分片，返回清理掉的集合数。
func (s *Session) SweepReassembly() int { return s.reasm.Sweep() }

// InFlight 返回未确认的可靠包数，用于观测。
func (s *Session) InFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reliable.InFlight()
}

// SessionStats 是会话的观测快照。
type SessionStats struct {
	ID            uint64
	Slot          int32
	SentPackets   uint64
	RecvPackets   uint64
	DroppedSend   uint64
	BadPackets    uint64
	ReliableInFly int
	LastRecvAge   time.Duration
}

// Stats 返回会话统计。
func (s *Session) Stats(now time.Time) SessionStats {
	return SessionStats{
		ID:            s.id,
		Slot:          s.slot.Load(),
		SentPackets:   s.sentPackets.Load(),
		RecvPackets:   s.recvPackets.Load(),
		DroppedSend:   s.droppedSend.Load(),
		BadPackets:    s.recvBadPacket.Load(),
		ReliableInFly: s.InFlight(),
		LastRecvAge:   now.Sub(s.LastRecv()),
	}
}
