package netx

import (
	"bytes"
	"errors"
	"math/rand"
	"testing"
)

func TestHeaderRoundTrip(t *testing.T) {
	want := Header{
		Version: Version,
		Channel: ChannelReliable,
		Seq:     12345,
		AckSeq:  6789,
		AckBits: 0xDEADBEEF,
		Flags:   FlagFragmented,
	}

	var buf [HeaderSize]byte
	want.Encode(buf[:])

	got, err := DecodeHeader(buf[:])
	if err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	if got != want {
		t.Fatalf("往返不一致：\n got %+v\nwant %+v", got, want)
	}
}

// 边界值最容易暴露位运算写错（掩码漏了、位移反了）。
func TestHeaderRoundTripEdgeValues(t *testing.T) {
	cases := []Header{
		{Version: Version, Channel: 0, Seq: 0, AckSeq: 0, AckBits: 0, Flags: 0},
		{Version: Version, Channel: 1, Seq: 65535, AckSeq: 65535, AckBits: 0xFFFFFFFF, Flags: FlagFragmented | FlagLastFrag},
		{Version: Version, Channel: 1, Seq: 256, AckSeq: 1, AckBits: 0x80000000, Flags: 0},
		{Version: Version, Channel: 0, Seq: 1, AckSeq: 256, AckBits: 1, Flags: FlagLastFrag},
	}
	for _, want := range cases {
		var buf [HeaderSize]byte
		want.Encode(buf[:])
		got, err := DecodeHeader(buf[:])
		if err != nil {
			t.Fatalf("%+v 解码失败：%v", want, err)
		}
		if got != want {
			t.Fatalf("往返不一致：\n got %+v\nwant %+v", got, want)
		}
	}
}

// 包头必须正好 12 字节——协议文档里写死了，改动等于换协议。
func TestHeaderSize(t *testing.T) {
	h := Header{Version: Version}
	var buf [HeaderSize]byte
	h.Encode(buf[:])
	if len(buf) != 12 {
		t.Fatalf("HeaderSize = %d，协议定的是 12", len(buf))
	}
}

// 魔数写在最前面，误发的流量读 2 字节就被丢掉。
func TestMagicIsFirstTwoBytes(t *testing.T) {
	var buf [HeaderSize]byte
	Header{Version: Version}.Encode(buf[:])
	if buf[0] != 0x4B || buf[1] != 0x4B {
		t.Fatalf("前两字节应为 0x4B 0x4B（ASCII \"KK\"），实际 %#x %#x", buf[0], buf[1])
	}
}

func TestDecodeHeaderRejectsShort(t *testing.T) {
	for n := 0; n < HeaderSize; n++ {
		if _, err := DecodeHeader(make([]byte, n)); !errors.Is(err, ErrShortPacket) {
			t.Fatalf("%d 字节应报 ErrShortPacket，得到 %v", n, err)
		}
	}
}

func TestDecodeHeaderRejectsBadMagic(t *testing.T) {
	buf := make([]byte, HeaderSize)
	buf[0], buf[1] = 0xFF, 0xFF
	if _, err := DecodeHeader(buf); !errors.Is(err, ErrBadMagic) {
		t.Fatalf("应报 ErrBadMagic，得到 %v", err)
	}
}

func TestDecodeHeaderRejectsBadVersion(t *testing.T) {
	h := Header{Version: Version + 1}
	var buf [HeaderSize]byte
	h.Encode(buf[:])
	if _, err := DecodeHeader(buf[:]); !errors.Is(err, ErrBadVersion) {
		t.Fatalf("应报 ErrBadVersion，得到 %v", err)
	}
}

func TestDecodeHeaderRejectsBadChannel(t *testing.T) {
	h := Header{Version: Version, Channel: 2}
	var buf [HeaderSize]byte
	h.Encode(buf[:])
	if _, err := DecodeHeader(buf[:]); !errors.Is(err, ErrBadChannel) {
		t.Fatalf("通道 2 应被拒绝，得到 %v", err)
	}
}

// 版本号被塞在高 4 位、通道在低 4 位，通道最大只能是 15。
func TestChannelFitsInLowNibble(t *testing.T) {
	if Channel(15).Valid() {
		t.Fatal("只有 0 和 1 是合法通道")
	}
	if !ChannelReliable.Valid() || !ChannelSnapshot.Valid() {
		t.Fatal("0 和 1 必须合法")
	}
}

func TestChannelString(t *testing.T) {
	if ChannelReliable.String() != "reliable" {
		t.Errorf("得到 %q", ChannelReliable.String())
	}
	if ChannelSnapshot.String() != "snapshot" {
		t.Errorf("得到 %q", ChannelSnapshot.String())
	}
	if s := Channel(7).String(); s == "" {
		t.Error("非法通道也要能打印")
	}
}

func TestFlagsHas(t *testing.T) {
	f := FlagFragmented | FlagLastFrag
	if !f.Has(FlagFragmented) || !f.Has(FlagLastFrag) {
		t.Fatal("两个标志位都应置位")
	}
	if Flags(0).Has(FlagFragmented) {
		t.Fatal("零值不应有任何标志")
	}
}

func TestAppendAndPayloadRoundTrip(t *testing.T) {
	payload := []byte("hello world")
	h := Header{Version: Version, Channel: ChannelSnapshot, Seq: 7}

	var dst []byte
	dst = h.Append(dst, payload)

	if len(dst) != HeaderSize+len(payload) {
		t.Fatalf("包长 = %d，期望 %d", len(dst), HeaderSize+len(payload))
	}

	got, err := DecodeHeader(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got != h {
		t.Fatalf("包头不一致：%+v vs %+v", got, h)
	}
	if !bytes.Equal(Payload(dst), payload) {
		t.Fatalf("载荷不一致：%q", Payload(dst))
	}
}

func TestPayloadEmpty(t *testing.T) {
	var dst []byte
	dst = Header{Version: Version}.Append(dst, nil)
	if p := Payload(dst); len(p) != 0 {
		t.Fatalf("空载荷应返回空，得到 %q", p)
	}
	// 只有包头时也不能越界。
	if p := Payload(dst[:HeaderSize]); p != nil {
		t.Fatalf("纯包头应返回 nil，得到 %q", p)
	}
}

// Append 写包头不应该额外分配内存（复用调用方的缓冲）。
//
// 注意不能拿两次 Append 的结果互相比对：两次都从同一个 len=0 的 buf 出发
// 会共享底层数组，第二次自然覆盖第一次——那是 append 的语义，不是 bug。
// 正确的验证方式是测分配次数。
func TestAppendDoesNotAllocate(t *testing.T) {
	h := Header{Version: Version}
	payload := []byte("abcdefghij")
	buf := make([]byte, 0, 256)

	allocs := testing.AllocsPerRun(100, func() {
		_ = h.Append(buf, payload)
	})
	if allocs != 0 {
		t.Fatalf("Append 每次分配了 %.1f 次，应复用缓冲做到 0 次", allocs)
	}
}

// 传入的缓冲容量够时，结果必须落在它里面。
func TestAppendUsesCallerBuffer(t *testing.T) {
	h := Header{Version: Version}
	buf := make([]byte, 0, 256)
	out := h.Append(buf, []byte("aaa"))

	if cap(out) != 256 {
		t.Fatalf("应复用调用方缓冲的容量，实际 cap=%d", cap(out))
	}
	if !bytes.Equal(Payload(out), []byte("aaa")) {
		t.Fatal("载荷不正确")
	}
}

// 序列号比较必须正确处理回绕，否则跨越 65535 后连接会永久卡死。
func TestSeqGreaterHandlesWraparound(t *testing.T) {
	cases := []struct {
		a, b uint16
		want bool
		desc string
	}{
		{2, 1, true, "普通递增"},
		{1, 2, false, "普通递减"},
		{0, 65535, true, "从 65535 回绕到 0，0 更新"},
		{65535, 0, false, "反过来"},
		{100, 65000, true, "跨回绕点"},
		{32768, 0, false, "相差 32768 视为更旧（窗口边缘）"},
		{32767, 0, true, "相差 32767 仍算更新"},
		{5, 5, false, "相同不算更大"},
	}
	for _, tc := range cases {
		if got := SeqGreater(tc.a, tc.b); got != tc.want {
			t.Errorf("%s：SeqGreater(%d, %d) = %v，期望 %v", tc.desc, tc.a, tc.b, got, tc.want)
		}
	}
}

// 单调递增序列（含回绕）必须被 SeqGreater 判为严格递增。
func TestSeqGreaterMonotonicAcrossWrap(t *testing.T) {
	seq := uint16(65530)
	prev := seq
	for i := 0; i < 100; i++ {
		seq++
		if !SeqGreater(seq, prev) {
			t.Fatalf("第 %d 步：%d 应大于 %d", i, seq, prev)
		}
		prev = seq
	}
}

func TestSeqDiff(t *testing.T) {
	cases := []struct {
		a, b uint16
		want int
	}{
		{10, 5, 5},
		{5, 10, -5},
		{0, 65535, 1},
		{65535, 0, -1},
		{1000, 990, 10},
	}
	for _, tc := range cases {
		if got := SeqDiff(tc.a, tc.b); got != tc.want {
			t.Errorf("SeqDiff(%d, %d) = %d，期望 %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestFragHeaderRoundTrip(t *testing.T) {
	want := FragHeader{MsgID: 7, Index: 3, Count: 10}
	var buf [FragHeaderSize]byte
	want.Encode(buf[:])

	got, err := DecodeFragHeader(buf[:])
	if err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	if got != want {
		t.Fatalf("往返不一致：%+v vs %+v", got, want)
	}
}

func TestDecodeFragHeaderRejectsBad(t *testing.T) {
	cases := []struct {
		name  string
		index uint16
		count uint16
	}{
		{"分片数为 0", 0, 0},
		{"分片数超上限", 0, MaxFragments + 1},
		{"下标超过总数", 5, 3},
		{"下标远超总数", 300, 1},
	}
	for _, tc := range cases {
		var buf [FragHeaderSize]byte
		FragHeader{MsgID: 1, Index: tc.index, Count: tc.count}.Encode(buf[:])
		if _, err := DecodeFragHeader(buf[:]); err == nil {
			t.Errorf("%s 应被拒绝（index=%d count=%d）", tc.name, tc.index, tc.count)
		}
	}
}

// 不分片的消息也会带一个 index=0/count=1 的分片头，这是合法的。
func TestDecodeFragHeaderAcceptsSingleFragment(t *testing.T) {
	var buf [FragHeaderSize]byte
	FragHeader{MsgID: 1, Index: 0, Count: 1}.Encode(buf[:])
	f, err := DecodeFragHeader(buf[:])
	if err != nil {
		t.Fatalf("单片消息应合法：%v", err)
	}
	if f.Index != 0 || f.Count != 1 {
		t.Fatalf("得到 %+v", f)
	}
}

func TestDecodeFragHeaderRejectsShort(t *testing.T) {
	for n := 0; n < FragHeaderSize; n++ {
		if _, err := DecodeFragHeader(make([]byte, n)); err == nil {
			t.Fatalf("%d 字节应被拒绝", n)
		}
	}
}

// 分片数为 0 会让"最后一片"永远不存在，必须提前拒绝而不是让重组永久挂起。
func TestFragCountZeroCannotHang(t *testing.T) {
	var buf [FragHeaderSize]byte
	FragHeader{MsgID: 1, Index: 0, Count: 0}.Encode(buf[:])
	if _, err := DecodeFragHeader(buf[:]); err == nil {
		t.Fatal("Count=0 必须被拒绝")
	}
}

// 随机往返：能覆盖手写单元测试想不到的位组合。
func TestHeaderRoundTripRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(20261109))
	for i := 0; i < 2000; i++ {
		want := Header{
			Version: Version,
			Channel: Channel(rng.Intn(2)),
			Seq:     uint16(rng.Intn(65536)),
			AckSeq:  uint16(rng.Intn(65536)),
			AckBits: rng.Uint32(),
			Flags:   Flags(rng.Intn(4)),
		}
		var buf [HeaderSize]byte
		want.Encode(buf[:])
		got, err := DecodeHeader(buf[:])
		if err != nil {
			t.Fatalf("第 %d 次解码失败：%v（包 %+v）", i, err, want)
		}
		if got != want {
			t.Fatalf("第 %d 次往返不一致：\n got %+v\nwant %+v", i, got, want)
		}
	}
}

// 被扫端口打到时不能 panic，也不能分配巨量内存。
func TestDecodeHeaderOnGarbageNoPanic(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 1000; i++ {
		n := rng.Intn(HeaderSize + 20)
		pkt := make([]byte, n)
		rng.Read(pkt)
		// 不关心错误类型，只要求不 panic。
		_, _ = DecodeHeader(pkt)
		if len(pkt) >= HeaderSize {
			_ = Payload(pkt)
		}
	}
}

func BenchmarkHeaderEncode(b *testing.B) {
	h := Header{Version: Version, Channel: ChannelSnapshot, Seq: 42, AckSeq: 40, AckBits: 0xFF}
	var buf [HeaderSize]byte
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h.Encode(buf[:])
	}
}

func BenchmarkHeaderDecode(b *testing.B) {
	var buf [HeaderSize]byte
	Header{Version: Version, Channel: ChannelSnapshot, Seq: 42}.Encode(buf[:])
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := DecodeHeader(buf[:]); err != nil {
			b.Fatal(err)
		}
	}
}
