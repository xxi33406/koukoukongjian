package netx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testLogger 返回一个丢弃输出的 logger，避免测试刷屏。
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testServerConfig 缩小超时，让掉线测试不必等 5 秒。
func testServerConfig() ServerConfig {
	cfg := DefaultServerConfig()
	cfg.SessionTimeout = 300 * time.Millisecond
	cfg.SweepInterval = 50 * time.Millisecond
	return cfg
}

// recvCollector 收集 Handler 回调。
type recvCollector struct {
	mu        sync.Mutex
	reliable  [][]byte
	snapshots int
	connects  int32
	disconn   []string
}

func (c *recvCollector) handler() HandlerFuncs {
	return HandlerFuncs{
		ConnectFn: func(s *Session) {
			atomic.AddInt32(&c.connects, 1)
		},
		ReliableFn: func(s *Session, payload []byte) {
			c.mu.Lock()
			// 必须复制：上层可能异步持有。
			cp := make([]byte, len(payload))
			copy(cp, payload)
			c.reliable = append(c.reliable, cp)
			c.mu.Unlock()
		},
		SnapshotFn: func(s *Session, payload []byte) {
			c.mu.Lock()
			c.snapshots++
			c.mu.Unlock()
		},
		DisconnectFn: func(s *Session, reason string) {
			c.mu.Lock()
			c.disconn = append(c.disconn, reason)
			c.mu.Unlock()
		},
	}
}

func (c *recvCollector) disconnCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.disconn)
}

func (c *recvCollector) reliableCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reliable)
}

func (c *recvCollector) reliableAt(i int) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reliable[i]
}

// waitFor 轮询等待条件成立，超时则失败。比固定 sleep 稳定得多。
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待超时（%v）：%s", timeout, what)
}

// startTestServer 起一个监听 127.0.0.1 随机端口的服务端，返回清理函数。
func startTestServer(t *testing.T, cfg ServerConfig, h Handler) (*Server, func()) {
	t.Helper()

	srv, err := Listen("127.0.0.1:0", cfg, testLogger(), h)
	if err != nil {
		t.Fatalf("启动服务端失败：%v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Run(ctx); err != nil {
			t.Errorf("服务端异常退出：%v", err)
		}
	}()

	cleanup := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("服务端未在 3 秒内退出")
		}
		_ = srv.Close()
	}
	return srv, cleanup
}

// dialTestClient 建一个连到服务端的 UDP 套接字。
func dialTestClient(t *testing.T, srv *Server) *net.UDPConn {
	t.Helper()
	raddr, ok := srv.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("监听地址类型异常：%T", srv.LocalAddr())
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		t.Fatalf("连接服务端失败：%v", err)
	}
	return conn
}

// sendReliable 从一个裸 UDP 连接发一条可靠消息。
func sendReliable(t *testing.T, conn *net.UDPConn, seq uint16, payload []byte) {
	t.Helper()
	h := Header{Version: Version, Channel: ChannelReliable, Seq: seq}
	pkt := h.Append(nil, payload)
	if _, err := conn.Write(pkt); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
}

// recvPacket 读一个包，带超时。
func recvPacket(t *testing.T, conn *net.UDPConn, timeout time.Duration) (Header, []byte) {
	t.Helper()
	buf := make([]byte, MaxPacketSize)
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatalf("设读超时失败：%v", err)
	}
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("读包失败：%v", err)
	}
	h, err := DecodeHeader(buf[:n])
	if err != nil {
		t.Fatalf("解析包头失败：%v", err)
	}
	return h, Payload(buf[:n])
}

func TestServerConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*ServerConfig)
		wantErr bool
	}{
		{"默认值合法", func(c *ServerConfig) {}, false},
		{"读缓冲过小", func(c *ServerConfig) { c.ReadBuffer = 100 }, true},
		{"会话超时为零", func(c *ServerConfig) { c.SessionTimeout = 0 }, true},
		{"扫描周期为零", func(c *ServerConfig) { c.SweepInterval = 0 }, true},
		{"会话上限为零", func(c *ServerConfig) { c.MaxSessions = 0 }, true},
		{"队列为零回落默认", func(c *ServerConfig) { c.QueueSize = 0 }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultServerConfig()
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

func TestServerRejectsBadArgs(t *testing.T) {
	if _, err := NewServer(nil, DefaultServerConfig(), testLogger(), HandlerFuncs{}); err == nil {
		t.Fatal("空连接应报错")
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("建连接失败：%v", err)
	}
	defer conn.Close()
	if _, err := NewServer(conn, DefaultServerConfig(), testLogger(), nil); err == nil {
		t.Fatal("空 Handler 应报错")
	}
}

// TestServerHandshakeRoundTrip 是 M1 的主验收：客户端发可靠消息，
// 服务端收到并回可靠消息，客户端收到，全部走真实 UDP。
func TestServerHandshakeRoundTrip(t *testing.T) {
	var col recvCollector
	srv, cleanup := startTestServer(t, testServerConfig(), col.handler())
	defer cleanup()

	conn := dialTestClient(t, srv)
	defer conn.Close()

	sendReliable(t, conn, 0, []byte("hello"))
	waitFor(t, 2*time.Second, "服务端收到 hello", func() bool {
		return col.reliableCount() == 1
	})
	if got := string(col.reliableAt(0)); got != "hello" {
		t.Fatalf("服务端收到 %q，期望 hello", got)
	}

	waitFor(t, 2*time.Second, "会话建立", func() bool {
		return srv.SessionCount() == 1
	})
	sess := srv.Session(conn.LocalAddr().String())
	if sess == nil {
		t.Fatal("按地址查不到会话")
	}

	// 服务端回一条可靠消息。
	if !sess.SendReliable([]byte("world")) {
		t.Fatal("服务端回发失败")
	}
	h, payload := recvPacket(t, conn, time.Second)
	if h.Channel != ChannelReliable {
		t.Fatalf("回包通道 = %v，期望可靠", h.Channel)
	}
	if string(payload) != "world" {
		t.Fatalf("回包载荷 = %q，期望 world", payload)
	}
	if h.Seq != 0 {
		t.Fatalf("服务端首个可靠包 seq = %d，期望 0", h.Seq)
	}
}

// TestServerSnapshotDownAndAckUp 验证两个方向：
// 服务端下行快照能被客户端收到，且客户端回包里的 ack 能清掉在途队列。
func TestServerSnapshotDownAndAckUp(t *testing.T) {
	var col recvCollector
	srv, cleanup := startTestServer(t, testServerConfig(), col.handler())
	defer cleanup()

	conn := dialTestClient(t, srv)
	defer conn.Close()

	// 先发一条让会话建立。
	sendReliable(t, conn, 0, []byte("hi"))
	waitFor(t, 2*time.Second, "会话建立", func() bool { return srv.SessionCount() == 1 })
	sess := srv.Session(conn.LocalAddr().String())

	// 下行快照。
	if !sess.SendSnapshot([]byte("snap-payload")) {
		t.Fatal("发送快照失败")
	}
	h, payload := recvPacket(t, conn, time.Second)
	if h.Channel != ChannelSnapshot {
		t.Fatalf("通道 = %v，期望快照", h.Channel)
	}
	if string(payload) != "snap-payload" {
		t.Fatalf("载荷 = %q", payload)
	}
	// 服务端收到了我们的 seq 0，回包必须带 FlagHasAck。
	if !h.Flags.Has(FlagHasAck) {
		t.Fatal("服务端快照包缺少 FlagHasAck")
	}
	if h.AckSeq != 0 {
		t.Fatalf("ackSeq = %d，期望 0", h.AckSeq)
	}

	// 客户端发一条带 ack 的包，服务端应清空在途。
	if !sess.SendReliable([]byte("event")) {
		t.Fatal("服务端发事件失败")
	}
	recvPacket(t, conn, time.Second) // 消费掉
	if sess.InFlight() != 1 {
		t.Fatalf("在途 = %d，期望 1", sess.InFlight())
	}

	// 客户端回 ack：确认 seq 0。
	ackH := Header{Version: Version, Channel: ChannelSnapshot, Seq: 0, AckSeq: 0, Flags: FlagHasAck}
	if _, err := conn.Write(ackH.Append(nil, []byte("ack"))); err != nil {
		t.Fatalf("发 ack 失败：%v", err)
	}
	waitFor(t, 2*time.Second, "在途清空", func() bool { return sess.InFlight() == 0 })
}

// TestServerReliableRetransmit 用"客户端故意不回 ack"验证服务端会重传。
func TestServerReliableRetransmit(t *testing.T) {
	var col recvCollector
	srv, cleanup := startTestServer(t, testServerConfig(), col.handler())
	defer cleanup()

	conn := dialTestClient(t, srv)
	defer conn.Close()

	sendReliable(t, conn, 0, []byte("hi"))
	waitFor(t, 2*time.Second, "会话建立", func() bool { return srv.SessionCount() == 1 })
	sess := srv.Session(conn.LocalAddr().String())

	if !sess.SendReliable([]byte("must-arrive")) {
		t.Fatal("发送失败")
	}
	h1, _ := recvPacket(t, conn, time.Second)

	// 不回 ack，等重传。RTO 初始 100ms，给足余量。
	h2, payload2 := recvPacket(t, conn, 2*time.Second)
	if h2.Seq != h1.Seq {
		t.Fatalf("重传包 seq = %d，期望与首次相同 %d", h2.Seq, h1.Seq)
	}
	if string(payload2) != "must-arrive" {
		t.Fatalf("重传载荷 = %q", payload2)
	}
}

// TestServerSessionTimeout 验证超时掉线：客户端停止发包后，
// 服务端应在 SessionTimeout 之后回收会话并回调 OnDisconnect。
func TestServerSessionTimeout(t *testing.T) {
	var col recvCollector
	cfg := testServerConfig()
	cfg.SessionTimeout = 150 * time.Millisecond
	cfg.SweepInterval = 30 * time.Millisecond

	srv, cleanup := startTestServer(t, cfg, col.handler())
	defer cleanup()

	conn := dialTestClient(t, srv)
	sendReliable(t, conn, 0, []byte("hi"))
	waitFor(t, 2*time.Second, "会话建立", func() bool { return srv.SessionCount() == 1 })

	// 停止发包，等超时。
	//
	// 必须等"会话被回收且掉线回调已发出"，不能只等 SessionCount()==0：
	// dropSession 是先删表项、后回调，两个状态之间有窗口，只等前者会
	// 在窗口内读到 0 次回调而误判失败。
	waitFor(t, 3*time.Second, "会话被回收且回调完成", func() bool {
		return srv.SessionCount() == 0 && col.disconnCount() == 1
	})

	// 掉线后客户端再发包应建立**新会话**，而不是复用旧状态。
	sendReliable(t, conn, 0, []byte("again"))
	waitFor(t, 2*time.Second, "新会话建立", func() bool { return srv.SessionCount() == 1 })
	waitFor(t, 2*time.Second, "服务端收到重连消息", func() bool { return col.reliableCount() == 2 })
	conn.Close()
}

// TestServerMaxSessions 验证会话上限：伪造源地址不能把内存打满。
func TestServerMaxSessions(t *testing.T) {
	var col recvCollector
	cfg := testServerConfig()
	cfg.MaxSessions = 2

	srv, cleanup := startTestServer(t, cfg, col.handler())
	defer cleanup()

	conns := make([]*net.UDPConn, 0, 4)
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()

	// 前两个应成功。
	for i := 0; i < 2; i++ {
		c := dialTestClient(t, srv)
		conns = append(conns, c)
		sendReliable(t, c, 0, []byte(fmt.Sprintf("c%d", i)))
	}
	waitFor(t, 2*time.Second, "两个会话建立", func() bool { return srv.SessionCount() == 2 })

	// 第三个应被拒，且不 panic。
	c3 := dialTestClient(t, srv)
	conns = append(conns, c3)
	sendReliable(t, c3, 0, []byte("c3"))

	time.Sleep(150 * time.Millisecond)
	if got := srv.SessionCount(); got != 2 {
		t.Fatalf("会话数 = %d，期望仍为 2", got)
	}
	if got := srv.Stats().DropNewSes; got == 0 {
		t.Fatal("拒绝计数为 0")
	}
}

// TestServerIgnoresBadPackets 验证坏包不会把服务端搞挂。
func TestServerIgnoresBadPackets(t *testing.T) {
	var col recvCollector
	srv, cleanup := startTestServer(t, testServerConfig(), col.handler())
	defer cleanup()

	conn := dialTestClient(t, srv)
	defer conn.Close()

	bad := [][]byte{
		{},
		{0x00},
		{0x4B, 0x4B},                   // 只有魔数
		{0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, // 错魔数
		make([]byte, 40),               // 全零
	}
	for _, pkt := range bad {
		if _, err := conn.Write(pkt); err != nil {
			t.Fatalf("写坏包失败：%v", err)
		}
	}

	// 服务端应仍在工作：随后一个正常包必须成功。
	sendReliable(t, conn, 0, []byte("still-alive"))
	waitFor(t, 2*time.Second, "坏包后仍能收包", func() bool { return col.reliableCount() == 1 })

	if srv.Stats().RecvBad == 0 {
		t.Fatal("坏包计数为 0")
	}
}

// TestServerConcurrentClients 起 4 个客户端并发收发（对应游戏的 4 人上限），
// 配合 -race 检查会话管理的并发正确性。
func TestServerConcurrentClients(t *testing.T) {
	var col recvCollector
	srv, cleanup := startTestServer(t, testServerConfig(), col.handler())
	defer cleanup()

	const clients = 4
	conns := make([]*net.UDPConn, clients)
	for i := range conns {
		conns[i] = dialTestClient(t, srv)
		defer conns[i].Close()
	}

	var wg sync.WaitGroup
	for i, c := range conns {
		wg.Add(1)
		go func(idx int, conn *net.UDPConn) {
			defer wg.Done()
			for seq := uint16(0); seq < 20; seq++ {
				payload := []byte(fmt.Sprintf("c%d-%d", idx, seq))
				h := Header{Version: Version, Channel: ChannelReliable, Seq: seq}
				if _, err := conn.Write(h.Append(nil, payload)); err != nil {
					return
				}
			}
		}(i, c)
	}
	wg.Wait()

	waitFor(t, 5*time.Second, "4 个客户端各 20 条全部到达", func() bool {
		return col.reliableCount() == clients*20
	})
	if got := srv.SessionCount(); got != clients {
		t.Fatalf("会话数 = %d，期望 %d", got, clients)
	}
}

// TestServerShutdownCleansUp 验证 Run 返回时所有会话都被关闭且回调触发，
// 这是"退出不漏 goroutine"的验收点。
func TestServerShutdownCleansUp(t *testing.T) {
	var col recvCollector
	srv, err := Listen("127.0.0.1:0", testServerConfig(), testLogger(), col.handler())
	if err != nil {
		t.Fatalf("启动失败：%v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Run(ctx)
	}()

	conn := dialTestClient(t, srv)
	defer conn.Close()
	sendReliable(t, conn, 0, []byte("hi"))
	waitFor(t, 2*time.Second, "会话建立", func() bool { return srv.SessionCount() == 1 })

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未在 3 秒内返回")
	}

	if got := srv.SessionCount(); got != 0 {
		t.Fatalf("退出后仍有 %d 个会话", got)
	}
	col.mu.Lock()
	n := len(col.disconn)
	col.mu.Unlock()
	if n != 1 {
		t.Fatalf("退出时掉线回调 %d 次，期望 1", n)
	}
	_ = srv.Close()
}

// TestServerListenBadAddr 验证监听失败时的错误路径。
func TestServerListenBadAddr(t *testing.T) {
	if _, err := Listen("这不是地址", DefaultServerConfig(), testLogger(), HandlerFuncs{}); err == nil {
		t.Fatal("非法地址应报错")
	}
}

// BenchmarkServerDispatch 测单个包的分发开销（解析头 + 会话查找 + 处理）。
func BenchmarkServerDispatch(b *testing.B) {
	s := newSession(1, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9999}, 64, time.Now())
	pkt := Header{Version: Version, Channel: ChannelSnapshot, Seq: 1}.Append(nil, []byte("payload"))
	now := time.Now()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h, err := DecodeHeader(pkt)
		if err != nil {
			b.Fatal(err)
		}
		if _, _, err := s.HandlePacket(h, Payload(pkt), now); err != nil {
			b.Fatal(err)
		}
		select {
		case <-s.out:
		default:
		}
	}
}

// TestServerCloseWhileRunning 守住 Close 与 Run 并发退出的竞态。
//
// 曾经的缺陷：Close() 与 Run() 的退出分支都会调 shutdown()，两个
// goroutine 同时遍历 sessions map、同时调 wg.Wait()，而另一处可能正好
// 在 wg.Add()——WaitGroup 明令禁止 Add 与 Wait 并发，-race 会报，实际
// 运行则可能丢会话或永久阻塞。修法是 shutdownOnce + closing 标志。
//
// 这个测试必须配 -race 才有意义：不带 race 时它只是"不 panic"。
func TestServerCloseWhileRunning(t *testing.T) {
	// 反复多次；竞态窗口很窄，单次很可能踩不到。
	for i := 0; i < 30; i++ {
		srv, err := Listen("127.0.0.1:0", DefaultServerConfig(), testLogger(), HandlerFuncs{})
		if err != nil {
			t.Fatalf("第 %d 轮启动失败：%v", i, err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		// t.Cleanup 而非 defer：Fatalf 走 Goexit 不跑 defer，
		// 不显式注册会让 vet 报 context 泄漏。
		t.Cleanup(cancel)
		runDone := make(chan struct{})
		go func() {
			defer close(runDone)
			srv.Run(ctx)
		}()

		// 让一些会话建立起来，Close 才有东西可清理。
		cli, err := Dial(srv.LocalAddr().String(), DefaultClientConfig())
		if err != nil {
			cancel()
			srv.Close()
			<-runDone
			t.Fatalf("第 %d 轮连接失败：%v", i, err)
		}
		cliCtx, cliCancel := context.WithCancel(ctx)
		go cli.Run(cliCtx)
		cli.Send([]byte("hi"))

		// 三种退出方式混着来，尽量撞出竞态窗口。
		switch i % 3 {
		case 0:
			// Close 与 ctx 取消几乎同时。
			cancel()
			srv.Close()
		case 1:
			srv.Close()
			cancel()
		case 2:
			// 只 Close，让 Run 自己从读错误里退出。
			srv.Close()
			cancel()
		}

		cliCancel()
		cli.Close()
		<-runDone
	}
}
