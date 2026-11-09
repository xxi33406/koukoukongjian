package netx

import (
	"sort"
	"time"
)

// 可靠通道：选择性重传 + 有序交付。
//
// 设计取舍（docs/server-design-v0.1.md §3.2）：
//   - 用 ack_seq + ack_bits 做累积+选择性确认，一个包可以确认过去 33 个；
//   - RTO 初始 100ms，指数退避，上限 500ms；收到新 ack 就重置退避；
//   - **不做拥塞控制**。v0.1 是局域网直连，没有拥塞可言；做错了反而
//     在自己家里无谓地降速。将来上公网再说。
//
// 为什么不用"每个包一个 timer"：4 人房间只有可靠通道用得上重传，而
// 可靠消息很稀疏（握手、事件、UI 确认），每包一个 timer 的创建销毁成本
// 远超收益。这里用一个逻辑时钟扫一遍队列，20ms 粒度足够。
const (
	// RTOInitial 是首次重传等待时长。
	//
	// 100ms 是本项目 tick 的 2 倍（20Hz = 50ms/tick）。选它的理由：局域网
	// 丢包几乎全是缓冲区瞬时溢出或路由抖动，重传太快没有意义（对端还没
	// 处理完）；太慢则一次丢包造成 200ms+ 的可感知卡顿。
	RTOInitial = 100 * time.Millisecond

	// RTOMax 是退避上限。
	//
	// 500ms 而不是常见的 1s：v0.1 目标是局域网，连续丢 5 次以上基本等于
	// 对端已经掉线，此时走心跳超时（3s）更快；而 500ms 的上限让"偶发
	// 连丢 2~3 个包"能在 1.5s 内恢复。
	RTOMax = 500 * time.Millisecond

	// sendWindow 是已发送未确认的包数量上限。
	//
	// 1024 与 seq 空间的回绕窗口一致：UDP 上序号是 u16，若在途包超过
	// 32767 就无法区分新旧。1024 远小于这个数，同时足够 4 人房间的
	// 可靠消息吞吐（这些消息很稀疏）。
	sendWindow = 1024

	// recvWindow 是接收端记录"已收"的位图宽度。
	//
	// 32 对应包头里 ack_bits 是 u32。再大的窗口就需要更多字节，而 33 个
	// 包的确认覆盖（20Hz 下 1.65 秒）已经超过 RTOMax 的两倍，够用。
	recvWindow = 32
)

// retransmit 统计重传相关指标，供观测。
type retransmitStats struct {
	Sent       uint64
	Retransmit uint64
	Acked      uint64
	Dropped    uint64
}

// reliableSender 是可靠通道的发送侧。
type reliableSender struct {
	nextSeq uint16

	// in-flight 按 seq 排序（用最小堆会更快，但可靠消息很稀疏，
	// 排序一个通常只有 0~3 个元素的切片比堆更简单也更快）。
	inflight []*outstandingPacket

	lastAckSeq  uint16
	lastAckBits uint32
	haveAck     bool

	rto     time.Duration
	stats   retransmitStats
	nowFunc func() time.Time
}

type outstandingPacket struct {
	seq uint16
	// parts 是这条消息的各个分片（含分片头）。未分片的消息只有 1 个
	// 元素，且不含分片头。
	//
	// 存整个 parts 而不是单片：分片共享一个 seq，重传时整条一起补发。
	parts   [][]byte
	firstAt time.Time
	lastAt  time.Time
	// attempts 是已经发出的次数（首发算 1）。
	attempts int
}

// Payload 把各片还原成原始载荷（去掉分片头、按序拼接）。
//
// 主要给测试与日志用：正常发送路径直接拿 parts 逐片写包头，不需要还原。
func (p *outstandingPacket) Payload() []byte {
	if len(p.parts) == 1 {
		return p.parts[0]
	}
	var total int
	for _, body := range p.parts {
		if len(body) > FragHeaderSize {
			total += len(body) - FragHeaderSize
		}
	}
	out := make([]byte, 0, total)
	for _, body := range p.parts {
		if len(body) <= FragHeaderSize {
			continue
		}
		out = append(out, body[FragHeaderSize:]...)
	}
	return out
}

func newReliableSender(now func() time.Time) *reliableSender {
	if now == nil {
		now = time.Now
	}
	return &reliableSender{
		rto:     RTOInitial,
		nowFunc: now,
	}
}

// canSend 判断窗口是否还有余量。
func (s *reliableSender) canSend() bool {
	return len(s.inflight) < sendWindow
}

// outboundPacket 是一条待发送的可靠消息（可能切成多片）。
//
// 关键设计：**整条消息只占一个 seq**，所有片共享它。分片只是为了绕开
// 单包 1200 字节的上限，不是独立的可靠单元。
//
// 这不是随意选的。如果每片各占一个 seq，接收侧就必须"每片都推进可靠
// 序号"，可它又要等整条凑齐才能交付——于是第一片的 seq 一到达就被
// 消费掉，而重组还没完成，后面几片的 seq 全排在它后面等一个永远不会
// 再出现的确认。实测表现就是大消息发出去永远收不到（
// TestClientLargePayloadFragmentation 挂 5 秒超时）。
//
// 共享 seq 之后语义变得干净：片丢一片就是整条消息丢，重传整条，
// 接收侧靠 MsgID 丢弃重复片，凑齐后按一个 seq 交付。
type outboundPacket struct {
	seq uint16
	// parts 是各片的完整 body（含分片头）。单片消息时只有 1 个元素，
	// 且不含分片头。
	parts [][]byte
	// fragmented 表示 parts 里每片都带分片头、发送时要置 FlagFragmented。
	fragmented bool
}

// Send 把一条消息登记为待发送，返回它占用的 seq。
//
// 返回 false 表示窗口已满或消息过大（超过 MaxFragments 片）。对本项目
// 而言窗口满意味着对端已经很久没有响应，实际应当走会话超时而不是继续
// 排队。
//
// 需要拿到各片内容的调用方（会话层要逐个写包头发出）请用 SendParts；
// Send 只是它的薄包装，供只关心序号的场景使用。
func (s *reliableSender) Send(payload []byte) (uint16, bool) {
	pkt, ok := s.SendParts(payload)
	if !ok {
		return 0, false
	}
	return pkt.seq, true
}

// SendParts 把一条消息登记为待发送，返回带全部分片的描述。
//
// 关键设计：整条消息只占一个 seq（见 outboundPacket 的注释）。
func (s *reliableSender) SendParts(payload []byte) (*outboundPacket, bool) {
	frags := FragCount(len(payload))
	if frags > MaxFragments {
		// 消息太大，切成 MaxFragments 片也装不下。宁可直接失败，也不要
		// 悄悄发出一半——接收侧会拼出一条被截断的、语义错误的消息。
		return nil, false
	}
	if !s.canSend() {
		return nil, false
	}

	// 短消息不切，省掉分片头。
	if frags == 1 && len(payload) <= MaxPayload {
		seq, ok := s.enqueue([][]byte{payload})
		if !ok {
			return nil, false
		}
		return &outboundPacket{seq: seq, parts: [][]byte{payload}}, true
	}

	// MsgID 用本条消息的 seq：一条消息一个 seq，天然唯一，不需要另一个
	// 计数器；接收侧看到同一个 MsgID 的片就知道它们属于同一条消息。
	seq := s.nextSeq
	msgID := seq

	parts := make([][]byte, 0, frags)
	for i := 0; i < frags; i++ {
		start := i * fragChunk
		end := start + fragChunk
		if end > len(payload) {
			end = len(payload)
		}

		fh := FragHeader{MsgID: msgID, Index: uint16(i), Count: uint16(frags)}
		body := make([]byte, FragHeaderSize, FragHeaderSize+end-start)
		fh.Encode(body)
		body = append(body, payload[start:end]...)
		parts = append(parts, body)
	}

	seq, ok := s.enqueue(parts)
	if !ok {
		return nil, false
	}
	return &outboundPacket{seq: seq, parts: parts, fragmented: true}, true
}

// enqueue 把一条消息（全部片）登记为一个待确认单元，返回它的 seq。
func (s *reliableSender) enqueue(parts [][]byte) (uint16, bool) {
	if !s.canSend() {
		return 0, false
	}
	seq := s.nextSeq
	s.nextSeq++

	// 复制载荷：调用方通常复用缓冲区，不复制会导致排队的包内容被后续
	// 发送覆盖——这个 bug 只在重传时暴露，因为首发的包已经发出去了。
	copied := make([][]byte, len(parts))
	for i, p := range parts {
		buf := make([]byte, len(p))
		copy(buf, p)
		copied[i] = buf
	}

	now := s.nowFunc()
	s.inflight = append(s.inflight, &outstandingPacket{
		seq:      seq,
		parts:    copied,
		firstAt:  now,
		lastAt:   now,
		attempts: 1,
	})
	s.stats.Sent++
	return seq, true
}

// Ack 处理对端发来的确认信息，从在途队列里移除已确认的包。
//
// **编码语义（必须与接收侧 AckInfo 严格一致）**：这是游戏引擎的经典做法
// （Quake3 / Source 都用它），不是 TCP 的累积确认。
//
//   - ackSeq = 我**收到过的最大序号**，不要求连续；
//   - ackBits 第 i 位 = ackSeq-1-i 这个序号是否也收到了。
//
// 它和 TCP SACK 的差别在于 ackSeq 不表示"连续到哪"，所以位图才有信息
// 量——正好用来表达缺口。第一版把接收侧的 ackSeq 写成"最大连续点"
// （nextExpected-1），位图就永远全是 1，等于没有选择性重传。这个不一致
// 是测试暴露出来的。
func (s *reliableSender) Ack(ackSeq uint16, ackBits uint32) {
	// 收到确认就重置退避——说明链路是通的。
	if !s.haveAck || !SeqGreater(s.lastAckSeq, ackSeq) {
		s.rto = RTOInitial
	}
	if s.haveAck && SeqGreater(s.lastAckSeq, ackSeq) {
		// 迟到的旧 ack，忽略。乱序到达的 ack 不能用来回退状态。
		return
	}
	s.lastAckSeq = ackSeq
	s.lastAckBits = ackBits
	s.haveAck = true

	kept := s.inflight[:0]
	for _, p := range s.inflight {
		if ackedBy(ackSeq, ackBits, p.seq) {
			s.stats.Acked++
			continue
		}
		kept = append(kept, p)
	}
	// 清空尾部引用，避免载荷被意外持有导致 GC 无法回收。
	for i := len(kept); i < len(s.inflight); i++ {
		s.inflight[i] = nil
	}
	s.inflight = kept
}

// ackedBy 判断某个 seq 是否被这次 ack 覆盖。
func ackedBy(ackSeq uint16, ackBits uint32, seq uint16) bool {
	diff := int(int16(seq - ackSeq))
	switch {
	case diff == 0:
		// 对方收到过的最大序号，必然确认。
		return true
	case diff > 0:
		// 比对方最大序号还新：还没到，不能确认。
		return false
	case diff > -recvWindow:
		// 落在位图窗口内，看对应位。
		return ackBits&(1<<uint(-diff-1)) != 0
	default:
		// 比窗口还旧。对方一直在收包（ackSeq 已推进到很远）却没确认它，
		// 说明这个包真丢了——**不能**当成已确认，要留着等重传。
		return false
	}
}

// Due 返回当前应当重传的包。调用方逐条发出去。
//
// 指数退避按每个包自己的重传次数计算：第 n 次发送（首发算第 1 次）等
// 基础 RTO × 2^(n-1)，封顶 RTOMax。基础 RTO 由 s.rto 给出，并在收到
// ack 时重置回 RTOInitial。
//
// 注意**不要**在这里再全局翻倍 s.rto：那样全局退避与逐包退避会叠加，
// 两次重传后就跳到 4 倍（第一版就是这么写的，测试的时序断言暴露了它）。
func (s *reliableSender) Due() []*outstandingPacket {
	if len(s.inflight) == 0 {
		return nil
	}
	now := s.nowFunc()
	var due []*outstandingPacket
	for _, p := range s.inflight {
		if now.Sub(p.lastAt) >= s.rtoFor(p.attempts) {
			p.lastAt = now
			p.attempts++
			s.stats.Retransmit++
			due = append(due, p)
		}
	}
	return due
}

// rtoFor 计算第 attempts 次发送（首发算第 1 次）后的等待时长。
func (s *reliableSender) rtoFor(attempts int) time.Duration {
	if attempts <= 1 {
		return s.rto
	}
	d := s.rto
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= RTOMax {
			return RTOMax
		}
	}
	return d
}

// InFlight 返回在途包数。
func (s *reliableSender) InFlight() int { return len(s.inflight) }

// Stats 返回发送侧统计的副本。
func (s *reliableSender) Stats() retransmitStats { return s.stats }

// NextSeq 返回下一个将分配的序号，用于测试。
func (s *reliableSender) NextSeq() uint16 { return s.nextSeq }

// reliableReceiver 是可靠通道的接收侧。
//
// 它的任务是两件：产生用于回送给对方的 ack 信息，以及把乱序到达的包
// 按序交付给上层。
type reliableReceiver struct {
	// nextExpected 是下一个应当按序交付的序号。
	//
	// **固定从 0 开始，不用首个到达的包来初始化**。第一版就是用首包初始化，
	// 结果是 seq=0 丢了以后 nextExpected 从 1 起跳，重传回来的 seq=0 被当成
	// "旧包"永久丢弃——第一条可靠消息丢了就再也收不到。这个 bug 在
	// 随机丢包测试里表现为"50 条只收到 49 条"。
	//
	// 会话内序号从 0 起，重连时 Reset。将来若要做跨重连的序号延续，
	// 需要在握手里同步初始序号。
	nextExpected uint16

	// received 记录已收到但还不能按序交付的包。
	received map[uint16][]byte

	// maxSeen 是收到过的最大序号，用于生成 ack（不要求连续）。
	//
	// 单独维护它而不是用 nextExpected-1：ack 要告诉对方"我收到了哪些"，
	// 包括那些因为缺口还不能交付的。用最大连续点会让位图恒为全 1，
	// 选择性重传就失效了。
	maxSeen uint16

	// lowDelivered 是已交付区间 [lowDelivered, nextExpected) 的下界，
	// 用于在生成位图时区分"已交付"和"从没收到"。
	//
	// 它在环形序号空间里必须始终落后 nextExpected 不超过 recvWindow，
	// 否则位图会把早已过期的序号误判为已交付，对方的缺口就永远补不上。
	lowDelivered uint16
	hasDelivered bool

	haveReceived bool
	stats        retransmitStats
}

func newReliableReceiver() *reliableReceiver {
	return &reliableReceiver{
		received: make(map[uint16][]byte, 8),
	}
}

// AckInfo 返回应当回送给发送方的 ack 字段。
//
// 即使当前没有新消息到达也必须能回 ack——这正是"纯 ack 包"的用途：
// 对方的最后一个包丢了，需要靠一个只带 ack 的空包来告知。
func (r *reliableReceiver) AckInfo() (ackSeq uint16, ackBits uint32, ok bool) {
	if !r.haveReceived {
		return 0, 0, false
	}
	ackSeq = r.maxSeen

	// dLow 是已交付区间在环形空间里的宽度。
	dLow := 0
	if r.hasDelivered {
		dLow = SeqDiff(r.nextExpected, r.lowDelivered)
	}

	for i := 0; i < recvWindow; i++ {
		seq := ackSeq - 1 - uint16(i)

		// 还在缓冲里的：确定收到了。
		if _, has := r.received[seq]; has {
			ackBits |= 1 << uint(i)
			continue
		}

		// 已交付的也要置位，否则对方会把已经交付的包当成丢失而反复重传。
		// 判据是 seq 落在环形区间 [lowDelivered, nextExpected) 内。
		if dLow > 0 {
			d := SeqDiff(r.nextExpected, seq)
			if d > 0 && d <= dLow {
				ackBits |= 1 << uint(i)
			}
		}
	}
	return ackSeq, ackBits, true
}

// Receive 处理一个到达的包，返回可以按序交付的消息列表。
//
// 返回的切片可能为空（包是乱序的、重复的、或者很旧的）。
func (r *reliableReceiver) Receive(seq uint16, payload []byte) [][]byte {
	r.haveReceived = true
	if SeqGreater(seq, r.maxSeen) {
		r.maxSeen = seq
	}

	// 比期望还旧：要么是重复包，要么是已经交付过的。直接丢弃。
	//
	// 注意这里必须用 SeqGreater 的反向判断，而不是 seq < nextExpected：
	// 回绕后直接比较会让所有新包被当成旧包。
	if seq != r.nextExpected && !SeqGreater(seq, r.nextExpected) {
		r.stats.Dropped++
		return nil
	}

	// 重复的（之前已缓存）。
	if _, dup := r.received[seq]; dup {
		r.stats.Dropped++
		return nil
	}

	// 缓冲副本：UDP 读取缓冲区通常复用，不复制会读到下一包的内容。
	buf := make([]byte, len(payload))
	copy(buf, payload)
	r.received[seq] = buf
	r.stats.Acked++

	var out [][]byte
	// 按序交付能交付的全部。
	for {
		p, ok := r.received[r.nextExpected]
		if !ok {
			break
		}
		delete(r.received, r.nextExpected)
		out = append(out, p)

		if !r.hasDelivered {
			r.lowDelivered = r.nextExpected
			r.hasDelivered = true
		}
		r.nextExpected++
		// 已交付区间最多保留 recvWindow 那么宽。
		if SeqDiff(r.nextExpected, r.lowDelivered) > recvWindow {
			r.lowDelivered = r.nextExpected - uint16(recvWindow)
		}
	}
	return out
}

// Pending 返回已收到但还不能交付的包数。
func (r *reliableReceiver) Pending() int { return len(r.received) }

// Reset 清空接收状态（会话重连时）。
func (r *reliableReceiver) Reset() {
	r.received = make(map[uint16][]byte, 8)
	r.nextExpected = 0
	r.maxSeen = 0
	r.lowDelivered = 0
	r.hasDelivered = false
	r.haveReceived = false
}

// Stats 返回接收侧统计。
func (r *reliableReceiver) Stats() retransmitStats { return r.stats }

// sortInflight 按 seq 排序在途队列。
//
// 回绕会破坏朴素排序，所以用 SeqGreater 做比较函数。这个方法目前只在
// 测试与观测里用——真正发送时不要求顺序。
func (s *reliableSender) sortInflight() {
	sort.SliceStable(s.inflight, func(i, j int) bool {
		return SeqGreater(s.inflight[i].seq, s.inflight[j].seq)
	})
}
