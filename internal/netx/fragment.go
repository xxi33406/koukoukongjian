package netx

import (
	"errors"
	"fmt"
	"time"
)

// 分片重组：把多条带分片头的包拼回一条完整消息。
//
// 只有可靠通道使用分片。快照通道不分片——超长的快照应该跳帧重发而不是
// 拆开重传，因为快照的价值随时间指数衰减，重传一个 3 秒前的快照毫无意义。
const (
	// maxFragSets 限制同一时刻正在重组的消息条数。
	//
	// 不设限的话，一个错乱的发送方（或伪造的包）可以用无数个不同的
	// MsgID 触发聚合并发增长，直到把内存吃光。16 条已经远超正常需要：
	// 可靠通道上的消息通常只有几十字节，需要分片的本就罕见。
	maxFragSets = 16

	// maxReassemblyBytes 限制单条消息重组后的总字节数。
	//
	// 必须小于 MaxFragments × MaxPayload（256 × 1200 ≈ 307 KB），否则这个
	// 上限永远不会被触发，等于没设——第一版就是这么写错的，测试才暴露出来。
	//
	// 定 64 KB 的依据：任何合理的游戏消息都远小于它。可靠通道走的是握手
	// （几十字节）、事件（几百字节）、UI 确认（几个字节）；最长的可能是
	// 商店货架列表或技能池下发，按 20 个商品 × 每条 100 字节算也只有 2 KB，
	// 留了 30 倍余量。同时把最坏内存从 16 × 307 KB ≈ 4.9 MB 压到 1 MB。
	maxReassemblyBytes = 64 * 1024

	// reassemblyTimeout 是一条消息从首片到超时被丢弃的时长。
	//
	// 可靠通道会重传丢失的分片，所以这里的超时不需要等于 RTO，而应该
	// 覆盖"最坏情况下重传几轮"。5 秒意味着 RTO 从 100ms 退避到 500ms
	// 时还能走完约 6 轮，足够局域网与常见公网条件。
	reassemblyTimeout = 5 * time.Second
)

var (
	// ErrReassemblyOverflow 表示同时在重组的分片消息过多。
	ErrReassemblyOverflow = errors.New("netx: 分片重组队列已满")
	// ErrReassemblyTooBig 表示单条消息超出字节上限。
	ErrReassemblyTooBig = errors.New("netx: 分片消息过大")
	// ErrFragmentCountMismatch 表示同一 MsgID 的分片总数前后不一致。
	ErrFragmentCountMismatch = errors.New("netx: 分片总数不一致")
)

// fragSet 是正在重组中的一条消息。
type fragSet struct {
	count   uint16
	parts   [][]byte
	got     uint16
	size    int
	started time.Time
}

// Reassembler 按 MsgID 聚合分片。
//
// 它不持有任何网络状态，只是纯逻辑容器，因此可以单测到每一个边界。
// 零值不可用，必须用 NewReassembler 构造。
type Reassembler struct {
	sets map[uint16]*fragSet
	// now 是注入的时钟，测试里可以精确控制超时而不 sleep。
	now func() time.Time
}

// NewReassembler 构造一个重组器。now 为 nil 时使用 time.Now。
func NewReassembler(now func() time.Time) *Reassembler {
	if now == nil {
		now = time.Now
	}
	return &Reassembler{
		sets: make(map[uint16]*fragSet, maxFragSets),
		now:  now,
	}
}

// Push 提交一个分片。凑齐时返回 (完整消息, true, nil)；否则返回
// (nil, false, nil)。
//
// 注意返回值 nil/true 与 nil/false 的区别：前者表示"消息完整了但内容
// 是空的"，后者表示"还差分片"。调用方必须看第二个返回值。
//
// payload 会被复制。调用方通常直接传读缓冲里的切片，而读缓冲在下一个
// 包到达时就被复用——不复制的话，先到的片会被后来的包覆盖，凑齐后拼出
// 一条内容错乱的消息（实测表现为大消息首字节是垃圾值）。
func (r *Reassembler) Push(f FragHeader, payload []byte) ([]byte, bool, error) {
	t := r.now()

	set, ok := r.sets[f.MsgID]
	if !ok {
		// 先清理超时的，再判断是否真的满了——否则一个长达几分钟的
		// 会话里，超时集合占着名额会让新消息永远进不来。
		r.dropExpired(t)
		if len(r.sets) >= maxFragSets {
			return nil, false, fmt.Errorf("%w（已在重组 %d 条）", ErrReassemblyOverflow, len(r.sets))
		}
		parts := make([][]byte, f.Count)
		set = &fragSet{
			count:   f.Count,
			parts:   parts,
			started: t,
		}
		r.sets[f.MsgID] = set
	}

	// 同一 MsgID 的总数声明必须稳定。变了说明对端状态错乱或是伪造包，
	// 继续拼下去只会得到一个错位的消息。
	if set.count != f.Count {
		delete(r.sets, f.MsgID)
		return nil, false, fmt.Errorf("%w：MsgID %d 前 %d 片，现 %d 片",
			ErrFragmentCountMismatch, f.MsgID, set.count, f.Count)
	}

	// 重复分片（重传造成的）直接忽略。不能累加 got，否则就会永远凑不齐。
	if set.parts[f.Index] != nil {
		return nil, false, nil
	}

	if set.size+len(payload) > maxReassemblyBytes {
		delete(r.sets, f.MsgID)
		return nil, false, fmt.Errorf("%w：已收 %d 字节，再加 %d 超限",
			ErrReassemblyTooBig, set.size, len(payload))
	}

	// 复制：读缓冲会被下一个包复用，不复制则先到的片会被覆盖。
	buf := make([]byte, len(payload))
	copy(buf, payload)

	set.parts[f.Index] = buf
	set.got++
	set.size += len(payload)

	if set.got < set.count {
		return nil, false, nil
	}

	// 凑齐了，按序拼接。
	out := make([]byte, 0, set.size)
	for _, p := range set.parts {
		out = append(out, p...)
	}
	delete(r.sets, f.MsgID)
	return out, true, nil
}

// Pending 返回当前正在重组的分片消息条数，用于指标与测试。
func (r *Reassembler) Pending() int { return len(r.sets) }

// Sweep 丢弃超时的分片集合，返回被丢弃的条数。
//
// 必须周期性调用：否则一个永远凑不齐的消息会占着名额直到进程退出。
// 网络层的收包 goroutine 每轮调用一次即可。
func (r *Reassembler) Sweep() int {
	return r.dropExpired(r.now())
}

func (r *Reassembler) dropExpired(t time.Time) int {
	n := 0
	for id, set := range r.sets {
		if t.Sub(set.started) >= reassemblyTimeout {
			delete(r.sets, id)
			n++
		}
	}
	return n
}

// Reset 清空所有待重组的数据（会话关闭时调用）。
func (r *Reassembler) Reset() {
	for id := range r.sets {
		delete(r.sets, id)
	}
}
