// Package netx 实现 UDP 传输层：包头编解码、可靠通道、会话管理与分片。
//
// 分层纪律（docs/server-design-v0.1.md §3.2）：本包只认识包头
// （magic / seq / ack / channel / flags），**不解析载荷内容**。载荷是
// protobuf 字节流，由 internal/proto 负责。这样改消息字段不必动传输层，
// 改传输语义也不必动消息定义。
//
// 本包也不 import internal/game：游戏逻辑通过 channel 与这里交换
// 已编码的字节切片和不可变的值类型，World 永远只被 tick goroutine 触碰。
package netx

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// 包头布局（12 字节，全部小端）：
//
//	 0    1    2    3    4    5    6    7    8    9   10   11
//	+----+----+----+----+----+----+----+----+----+----+----+----+
//	|  magic  |v |ch|    seq    |  ack_seq  |     ack_bits    |fl|
//	+----+----+----+----+----+----+----+----+----+----+----+----+
//	  2    1    2        2            2              4          1
//
// magic 放在最前面，是为了让误发到本端口的其他流量（浏览器探测、端口扫
// 描、别的服务）能在读 2 个字节后被立刻丢掉，而不是被当成我们的包一路解
// 析到载荷才失败。
const (
	// Magic 是包头魔数 0x4B4B，小端写入后正好是 ASCII "KK"。
	Magic uint16 = 0x4B4B

	// Version 是当前协议版本。
	//
	// 版本不匹配直接丢包，不回错误包——回了等于给扫端口的送一个确认
	// "这里有个 KK 服务"，而且版本协商的正确做法是客户端超时重试而不是
	// 在数据通道里来回协商。灰盒阶段客户端与服务端同仓库，版本必然一致。
	Version byte = 1

	// HeaderSize 是固定包头长度。
	HeaderSize = 12

	// MaxPayload 是单个包的载荷上限。
	//
	// 1200 是 IETF 建议的保守值：以太网 1500 减去 IPv6 头 40 和 UDP 头 8
	// 还有 1452，再留出隧道封装的余量（VPN 常见 60 字节、PPPoE 8 字节）。
	// 用 1472 这种"刚好塞满以太网"的值会在有隧道时静默分片。
	MaxPayload = 1200

	// MaxPacketSize 是单个包的最大长度。
	MaxPacketSize = HeaderSize + MaxPayload

	// MaxFragments 限制单条消息的分片数。
	//
	// 256 × 1196 ≈ 306 KB，远大于任何一个合理的可靠消息。设上限的目的
	// 不是省内存，而是让恶意/错乱的 fragCount 无法触发巨量分配。
	MaxFragments = 256
)

var (
	// ErrShortPacket 表示收到的字节数不足。
	ErrShortPacket = errors.New("netx: 包长度不足")
	// ErrBadMagic 表示魔数不匹配，通常是对端不是本协议或流量被污染。
	ErrBadMagic = errors.New("netx: 魔数不匹配")
	// ErrBadVersion 表示协议版本不一致。
	ErrBadVersion = errors.New("netx: 协议版本不匹配")
	// ErrBadChannel 表示通道号越界。
	ErrBadChannel = errors.New("netx: 非法通道号")
	// ErrPayloadTooBig 表示载荷超过单包上限。
	ErrPayloadTooBig = errors.New("netx: 载荷超长")
)

// Channel 是逻辑通道号。
//
// 两条通道复用同一个 UDP socket 和同一份包头，靠这个字段分流。分成两条
// 而不是"每条通道一个 socket"，是因为 NAT 后面的客户端很难稳定获得多个
// 端口映射，而且多一个 socket 就多一份超时与清理逻辑。
type Channel uint8

const (
	// ChannelReliable 承载握手、房间事件、UI 确认等"丢一条就状态分叉"
	// 的消息。有序、可靠、自动重传。
	ChannelReliable Channel = 0

	// ChannelSnapshot 承载快照下行。不可靠、只保留最新的。
	//
	// 快照重传是纯浪费：玩家需要的是"现在在哪"，而不是"两帧前在哪"。
	// 丢掉的一帧用下一帧插值补上即可。
	ChannelSnapshot Channel = 1

	channelCount = 2
)

// String 便于日志阅读。
func (c Channel) String() string {
	switch c {
	case ChannelReliable:
		return "reliable"
	case ChannelSnapshot:
		return "snapshot"
	default:
		return fmt.Sprintf("ch(%d)", uint8(c))
	}
}

// Valid 判断通道号是否在本版本支持范围内。
func (c Channel) Valid() bool { return c < channelCount }

// Flags 是包头的标志位。
type Flags uint8

const (
	// FlagFragmented 表示本包是一个分片，载荷前有 4 字节分片头。
	FlagFragmented Flags = 1 << 0

	// FlagLastFrag 表示这是最后一个分片。
	//
	// 单独一个 bit 而不是靠 index == count-1 判断：靠下标判断要求
	// count 也正确，一个损坏的 count 会让重组的期望长度错掉；有明确的
	// 结束标记，重组层就能在凑齐时立刻交付，不必等超时。
	FlagLastFrag Flags = 1 << 1
)

// Has 判断某个标志位是否置位。
func (f Flags) Has(bit Flags) bool { return f&bit != 0 }

// Header 是解码后的包头。
//
// 用结构体而不是直接在字节切片上取偏移，是为了让"哪些字段参与判断"在
// 代码里一眼可见：Seq 用于去重、AckSeq/AckBits 用于重传决策、Channel 用
// 于分流、Flags 用于重组。
type Header struct {
	Version byte
	Channel Channel
	Seq     uint16
	AckSeq  uint16
	AckBits uint32
	Flags   Flags
}

// Encode 把包头写入 dst 的前 HeaderSize 字节。
//
// dst 必须至少 HeaderSize 长，调用方负责保证——这里不检查，因为它在每
// 个 goroutine 每次收发时都会走，加一次长度判断不值得（编解码函数在
// 本包内部使用，调用点可控）。
func (h Header) Encode(dst []byte) {
	binary.LittleEndian.PutUint16(dst[0:2], Magic)
	dst[2] = h.Version<<4 | byte(h.Channel)&0x0F
	binary.LittleEndian.PutUint16(dst[3:5], h.Seq)
	binary.LittleEndian.PutUint16(dst[5:7], h.AckSeq)
	binary.LittleEndian.PutUint32(dst[7:11], h.AckBits)
	dst[11] = byte(h.Flags)
}

// DecodeHeader 解析包头并校验。
//
// 校验顺序刻意是"便宜的在前"：先看长度，再看魔数（2 字节比较），最后才
// 拆分字段。这样被扫端口打到时，绝大多数包只花一次长度比较就被丢掉。
func DecodeHeader(src []byte) (Header, error) {
	if len(src) < HeaderSize {
		return Header{}, fmt.Errorf("%w: 收到 %d 字节，至少需要 %d", ErrShortPacket, len(src), HeaderSize)
	}
	if binary.LittleEndian.Uint16(src[0:2]) != Magic {
		return Header{}, ErrBadMagic
	}

	h := Header{
		Version: src[2] >> 4,
		Channel: Channel(src[2] & 0x0F),
		Seq:     binary.LittleEndian.Uint16(src[3:5]),
		AckSeq:  binary.LittleEndian.Uint16(src[5:7]),
		AckBits: binary.LittleEndian.Uint32(src[7:11]),
		Flags:   Flags(src[11]),
	}

	if h.Version != Version {
		return Header{}, fmt.Errorf("%w: 收到 v%d，本端 v%d", ErrBadVersion, h.Version, Version)
	}
	if !h.Channel.Valid() {
		return Header{}, fmt.Errorf("%w: %d", ErrBadChannel, h.Channel)
	}
	return h, nil
}

// Append 把包头和 payload 追加到 dst 末尾并返回新切片。
//
// 用 append 而不是分配定长数组，是为了让调用方能复用缓冲区：服务端每
// tick 要给每个玩家编一次包，20Hz × 4 人 × 60 秒 = 4800 次/分钟，每次
// 一次新分配会让 GC 压力白白升高。
func (h Header) Append(dst, payload []byte) []byte {
	var tmp [HeaderSize]byte
	h.Encode(tmp[:])
	dst = append(dst, tmp[:]...)
	return append(dst, payload...)
}

// Payload 返回包中包头之后的部分。
//
// 返回的子切片与 pkt 共享底层数组，只读使用；需要留存请自行 copy。
func Payload(pkt []byte) []byte {
	if len(pkt) <= HeaderSize {
		return nil
	}
	return pkt[HeaderSize:]
}

// SeqGreater 判断序列号 a 是否在 b 之后，正确处理 u16 回绕。
//
// 直接写 a > b 在回绕后会永久失效：seq 从 65535 跳到 0 时，所有新包都会
// 被判为"更旧"而丢弃，表现为连接突然卡死且不恢复。
//
// 转成有符号差值比较是序列号环上的标准做法，前提是两者相差不超过
// 32767。对本项目的包速率（20Hz 快照 = 每秒 20 个序号）意味着约 27 分钟
// 的窗口，远超任何合理的乱序或延迟。
func SeqGreater(a, b uint16) bool {
	return int16(a-b) > 0
}

// SeqDiff 返回 a 相对 b 的前进量，有符号，正确处理回绕。
//
// 返回负数表示 a 在 b 之前。用于计算"落后多少个包"以决定是否请求全量快照。
func SeqDiff(a, b uint16) int {
	return int(int16(a - b))
}

// FragHeader 是分片头，仅当 Flags 含 FlagFragmented 时存在于载荷最前面。
//
// 分片信息没有塞进固定包头，是因为不分片是常态：塞进去会让每个普通包都
// 背 4 字节无用开销（20Hz × 4 人下一年约 100 万包）。
type FragHeader struct {
	MsgID uint16
	Index uint16
	Count uint16
}

// FragHeaderSize 是分片头长度。
const FragHeaderSize = 6

// Encode 把分片头写入 dst 的前 FragHeaderSize 字节。
func (f FragHeader) Encode(dst []byte) {
	binary.LittleEndian.PutUint16(dst[0:2], f.MsgID)
	binary.LittleEndian.PutUint16(dst[2:4], f.Index)
	binary.LittleEndian.PutUint16(dst[4:6], f.Count)
}

// DecodeFragHeader 解析并校验分片头。
func DecodeFragHeader(src []byte) (FragHeader, error) {
	if len(src) < FragHeaderSize {
		return FragHeader{}, fmt.Errorf("%w: 分片头需要 %d 字节，收到 %d", ErrShortPacket, FragHeaderSize, len(src))
	}
	f := FragHeader{
		MsgID: binary.LittleEndian.Uint16(src[0:2]),
		Index: binary.LittleEndian.Uint16(src[2:4]),
		Count: binary.LittleEndian.Uint16(src[4:6]),
	}
	// Count 为 0 会让"最后一片"永远不存在，1..MaxFragments 之外的
	// Index 也永远凑不齐，两者都是必然挂死的状态，提前拒绝。
	if f.Count == 0 || f.Count > MaxFragments {
		return FragHeader{}, fmt.Errorf("netx: 分片数 %d 非法（1..%d）", f.Count, MaxFragments)
	}
	if f.Index >= f.Count {
		return FragHeader{}, fmt.Errorf("netx: 分片下标 %d 越界（总数 %d）", f.Index, f.Count)
	}
	return f, nil
}
