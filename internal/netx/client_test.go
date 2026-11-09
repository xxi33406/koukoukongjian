package netx

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// startTestServerSimple 是 startTestServer 的简化包装，返回地址。
func startTestServerSimple(t *testing.T, cfg ServerConfig, h Handler) (*Server, string, func()) {
	t.Helper()
	srv, cleanup := startTestServer(t, cfg, h)
	return srv, srv.LocalAddr().String(), cleanup
}

// echoHandler 把收到的可靠消息原样回发，用于回环测试。
func echoHandler() Handler {
	return HandlerFuncs{
		ConnectFn: func(s *Session) {},
		ReliableFn: func(s *Session, payload []byte) {
			s.SendReliable(payload)
		},
		SnapshotFn: func(s *Session, payload []byte) {},
		DisconnectFn: func(s *Session, reason string) {
		},
	}
}

// TestClientConfigValidate 检查参数校验。
func TestClientConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*ClientConfig)
		wantErr bool
	}{
		{"默认值合法", func(c *ClientConfig) {}, false},
		{"读缓冲过小", func(c *ClientConfig) { c.ReadBuffer = 100 }, true},
		{"零值回落默认", func(c *ClientConfig) {
			c.QueueSize = 0
			c.ReliableBuf = 0
			c.SnapshotBuf = 0
			c.SweepInterval = 0
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultClientConfig()
			tc.mutate(&cfg)
			_, err := cfg.validate()
			if tc.wantErr && err == nil {
				t.Fatal("期望报错，实际通过")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("期望通过，实际报错：%v", err)
			}
		})
	}
}

// TestClientServerReliableEcho 是最小回环：客户端发消息，服务端原样回，
// 客户端收到。整条路径走真实 UDP socket。
func TestClientServerReliableEcho(t *testing.T) {
	_, addr, cleanup := startTestServerSimple(t, testServerConfig(), echoHandler())
	defer cleanup()

	c, err := Dial(addr, DefaultClientConfig())
	if err != nil {
		t.Fatalf("连接失败：%v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	if !c.Send([]byte("回环消息")) {
		t.Fatal("发送失败")
	}

	got := c.RecvReliable(3 * time.Second)
	if got == nil {
		t.Fatal("未收到回显消息")
	}
	if string(got) != "回环消息" {
		t.Fatalf("回显内容 = %q，期望 回环消息", got)
	}
}

// TestClientReceivesSnapshots 验证服务端下行的快照能被客户端收到。
func TestClientReceivesSnapshots(t *testing.T) {
	var col recvCollector
	srv, addr, cleanup := startTestServerSimple(t, testServerConfig(), col.handler())
	defer cleanup()

	c, err := Dial(addr, DefaultClientConfig())
	if err != nil {
		t.Fatalf("连接失败：%v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	// 先握手建立会话。
	c.Send([]byte("hi"))
	waitFor(t, 2*time.Second, "会话建立", func() bool { return srv.SessionCount() == 1 })
	sess := srv.Session(c.LocalAddr().String())
	if sess == nil {
		t.Fatal("查不到会话")
	}

	const n = 5
	for i := 0; i < n; i++ {
		if !sess.SendSnapshot([]byte(fmt.Sprintf("snap-%d", i))) {
			t.Fatalf("第 %d 个快照发送失败", i)
		}
	}

	received := 0
	for received < n {
		snap := c.RecvSnapshot(2 * time.Second)
		if snap == nil {
			t.Fatalf("只收到 %d/%d 个快照", received, n)
		}
		received++
	}
}

// TestClientReliableSurvivesSnapshotFlood 验证快照洪水不会挤掉可靠消息。
//
// 这是双通道设计的核心收益：如果所有消息走同一队列，一波快照就能把
// 握手/事件挤到后面甚至丢掉。分成两条通道后，可靠消息有独立缓冲。
func TestClientReliableSurvivesSnapshotFlood(t *testing.T) {
	var col recvCollector
	srv, addr, cleanup := startTestServerSimple(t, testServerConfig(), col.handler())
	defer cleanup()

	cfg := DefaultClientConfig()
	cfg.SnapshotBuf = 2 // 故意做小，制造丢帧
	c, err := Dial(addr, cfg)
	if err != nil {
		t.Fatalf("连接失败：%v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	c.Send([]byte("hi"))
	waitFor(t, 2*time.Second, "会话建立", func() bool { return srv.SessionCount() == 1 })
	sess := srv.Session(c.LocalAddr().String())

	// 灌入远超缓冲的快照，同时插入一条可靠消息。
	for i := 0; i < 50; i++ {
		sess.SendSnapshot([]byte("flood"))
	}
	if !sess.SendReliable([]byte("关键事件")) {
		t.Fatal("可靠消息发送失败")
	}

	// 可靠消息必须能收到——快照洪水不能影响它。
	got := c.RecvReliable(3 * time.Second)
	if got == nil {
		t.Fatal("可靠消息被快照洪水挤掉了")
	}
	if string(got) != "关键事件" {
		t.Fatalf("可靠消息内容 = %q", got)
	}
}

// TestClientLargePayloadFragmentation 验证超过单包上限的可靠消息
// 走分片路径能完整送达。
func TestClientLargePayloadFragmentation(t *testing.T) {
	_, addr, cleanup := startTestServerSimple(t, testServerConfig(), echoHandler())
	defer cleanup()

	c, err := Dial(addr, DefaultClientConfig())
	if err != nil {
		t.Fatalf("连接失败：%v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	// 3000 字节需要切成 3 片（MaxPayload 1200 减分片头）。
	big := make([]byte, 3000)
	for i := range big {
		big[i] = byte(i % 251)
	}
	if !c.Send(big) {
		t.Fatal("发送大消息失败")
	}

	got := c.RecvReliable(5 * time.Second)
	if got == nil {
		t.Fatal("未收到分片消息")
	}
	if len(got) != len(big) {
		t.Fatalf("长度 = %d，期望 %d", len(got), len(big))
	}
	for i := range big {
		if got[i] != big[i] {
			t.Fatalf("第 %d 字节 = %d，期望 %d", i, got[i], big[i])
		}
	}
}

// TestClientCloseIdempotent 验证多次关闭安全。
func TestClientCloseIdempotent(t *testing.T) {
	_, addr, cleanup := startTestServerSimple(t, testServerConfig(), echoHandler())
	defer cleanup()

	c, err := Dial(addr, DefaultClientConfig())
	if err != nil {
		t.Fatalf("连接失败：%v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("首次关闭失败：%v", err)
	}
	_ = c.Close() // 不应 panic

	if c.Send([]byte("x")) {
		t.Fatal("关闭后不应发送成功")
	}
}

// TestClientStats 验证统计口径。
func TestClientStats(t *testing.T) {
	_, addr, cleanup := startTestServerSimple(t, testServerConfig(), echoHandler())
	defer cleanup()

	c, err := Dial(addr, DefaultClientConfig())
	if err != nil {
		t.Fatalf("连接失败：%v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	c.Send([]byte("one"))
	c.Send([]byte("two"))
	if got := c.InFlight(); got != 2 {
		t.Fatalf("在途 = %d，期望 2", got)
	}

	// 收到回显后，ack 由服务端回包带回，在途应清零。
	if c.RecvReliable(3*time.Second) == nil {
		t.Fatal("未收到回显")
	}
	if c.RecvReliable(3*time.Second) == nil {
		t.Fatal("未收到第二个回显")
	}
	waitFor(t, 2*time.Second, "在途清零", func() bool { return c.InFlight() == 0 })

	if st := c.Stats(time.Now()); st.SentPackets == 0 || st.RecvPackets == 0 {
		t.Fatalf("统计异常：发出 %d 收到 %d", st.SentPackets, st.RecvPackets)
	}
}

// TestClientRecvTimeout 验证超时返回 nil 而不是阻塞。
func TestClientRecvTimeout(t *testing.T) {
	_, addr, cleanup := startTestServerSimple(t, testServerConfig(), echoHandler())
	defer cleanup()

	c, err := Dial(addr, DefaultClientConfig())
	if err != nil {
		t.Fatalf("连接失败：%v", err)
	}
	defer c.Close()

	start := time.Now()
	if got := c.RecvReliable(60 * time.Millisecond); got != nil {
		t.Fatalf("空通道应超时返回 nil，实际 %q", got)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("超时过短：%v", elapsed)
	}
	if got := c.RecvSnapshot(0); got != nil {
		t.Fatalf("非阻塞取空通道应为 nil，实际 %q", got)
	}
}

// TestClientDialBadAddr 验证连接失败路径。
func TestClientDialBadAddr(t *testing.T) {
	if _, err := Dial("这不是地址", DefaultClientConfig()); err == nil {
		t.Fatal("非法地址应报错")
	}
}

// BenchmarkClientPushSnapshot 测快照入队开销（含复制）。
func BenchmarkClientPushSnapshot(b *testing.B) {
	c := &Client{
		Snapshot: make(chan []byte, 16),
		done:     make(chan struct{}),
	}
	payload := make([]byte, 1200)

	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.pushSnapshot(payload)
		select {
		case <-c.Snapshot:
		default:
		}
	}
}
