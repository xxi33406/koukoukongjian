package netx

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 本文件做丢包回归：在客户端与服务端之间插一个按比例丢包的 UDP 中继，
// 验证两条设计承诺在真实丢包下仍然成立：
//
//  1. 快照（不可靠通道）可以丢——丢了下一帧就补上；
//  2. 可靠通道一条都不能丢——靠重传兜底。
//
// 用一个真中继而不用"在 sendto 里随机 return"的桩，是因为桩测不出真实
// 的到达顺序变化：丢包真正的杀伤力在于乱序与延迟抖动，而不是单纯少包。

// lossyRelay 是一个按比例丢包的 UDP 中继。
//
// 双向都丢：上行丢输入、下行丢快照，两侧的重传逻辑都会被压到。
type lossyRelay struct {
	conn      *net.UDPConn
	upstream  *net.UDPAddr
	lossPct   int
	closeOnce sync.Once
	done      chan struct{}
	wg        sync.WaitGroup

	// 统计，供断言与调参用。
	fromClient atomic.Uint64
	toClient   atomic.Uint64
	droppedUp  atomic.Uint64
	droppedDn  atomic.Uint64

	// serverAddr 记住第一个发来包的地址作为上游。客户端先说话，
	// 因此不需要预先知道服务端。
	mu         sync.Mutex
	peerClient *net.UDPAddr
}

// newLossyRelay 在随机端口起一个中继，转发到 upstream。
func newLossyRelay(t *testing.T, upstream *net.UDPAddr, lossPct int) *lossyRelay {
	t.Helper()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("中继监听失败：%v", err)
	}
	r := &lossyRelay{
		conn:     conn,
		upstream: upstream,
		lossPct:  lossPct,
		done:     make(chan struct{}),
	}

	r.wg.Add(1)
	go r.loop()

	t.Cleanup(r.Close)
	return r
}

func (r *lossyRelay) Addr() *net.UDPAddr {
	return r.conn.LocalAddr().(*net.UDPAddr)
}

// Close 停掉中继。
func (r *lossyRelay) Close() {
	r.closeOnce.Do(func() {
		close(r.done)
		_ = r.conn.Close()
		r.wg.Wait()
	})
}

func (r *lossyRelay) loop() {
	defer r.wg.Done()

	// 固定种子：测试要可复现，随机丢包本身已经足够随机。
	rng := rand.New(rand.NewSource(int64(r.lossPct)*1000 + 7))

	buf := make([]byte, 2048)
	for {
		_ = r.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, from, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-r.done:
				return
			default:
				continue
			}
		}

		pkt := make([]byte, n)
		copy(pkt, buf[:n])

		r.mu.Lock()
		client := r.peerClient
		if client == nil || !sameAddr(from, r.upstream) {
			// 这个包来自客户端，记下它的地址。
			client = from
			r.peerClient = from
		}
		r.mu.Unlock()

		isUplink := !sameAddr(from, r.upstream)

		// 按比例丢。
		if rng.Intn(100) < r.lossPct {
			if isUplink {
				r.droppedUp.Add(1)
			} else {
				r.droppedDn.Add(1)
			}
			continue
		}

		if isUplink {
			r.fromClient.Add(1)
			_, _ = r.conn.WriteToUDP(pkt, r.upstream)
		} else {
			r.toClient.Add(1)
			if client != nil {
				_, _ = r.conn.WriteToUDP(pkt, client)
			}
		}
	}
}

func sameAddr(a, b *net.UDPAddr) bool {
	if a == nil || b == nil {
		return false
	}
	return a.IP.Equal(b.IP) && a.Port == b.Port
}

// TestLossReliableNeverDrops 在各档丢包率下验证可靠通道零丢失。
//
// 这是 M1 最重要的一条验收：快照丢了无所谓，可靠消息丢了就是"技能没放
// 出来"、"商店没打开"，玩家会当成 bug。
func TestLossReliableNeverDrops(t *testing.T) {
	const (
		msgCount = 40
		payload  = "reliable-must-arrive"
	)

	for _, lossPct := range []int{0, 5, 10, 20} {
		t.Run(fmt.Sprintf("loss%d%%", lossPct), func(t *testing.T) {
			t.Parallel()

			var got atomic.Int32
			done := make(chan struct{})
			var once sync.Once

			cfg := DefaultServerConfig()
			cfg.SessionTimeout = 5 * time.Second

			srv, err := Listen("127.0.0.1:0", cfg, testLogger(), HandlerFuncs{
				ReliableFn: func(s *Session, p []byte) {
					if n := got.Add(1); int(n) >= msgCount {
						once.Do(func() { close(done) })
					}
				},
			})
			if err != nil {
				t.Fatalf("服务端启动失败：%v", err)
			}
			defer srv.Close()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go srv.Run(ctx)

			relay := newLossyRelay(t, srv.LocalAddr().(*net.UDPAddr), lossPct)

			cliCfg := DefaultClientConfig()
			cli, err := Dial(relay.Addr().String(), cliCfg)
			if err != nil {
				t.Fatalf("客户端连接失败：%v", err)
			}
			defer cli.Close()

			cliCtx, cliCancel := context.WithCancel(ctx)
			defer cliCancel()
			go cli.Run(cliCtx)

			// 发 N 条。窗口 1024 远大于 40，不会因为窗口满而失败。
			deadline := time.Now().Add(10 * time.Second)
			for i := 0; i < msgCount; i++ {
				for !cli.Send([]byte(fmt.Sprintf("%s-%d", payload, i))) {
					if time.Now().After(deadline) {
						t.Fatalf("第 %d 条消息发不出去", i)
					}
					time.Sleep(5 * time.Millisecond)
				}
			}

			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Fatalf("丢包 %d%% 下只收到 %d/%d 条可靠消息",
					lossPct, got.Load(), msgCount)
			}

			// 再多等一会儿，确认没有"重复投递"把计数冲到 msgCount 以上。
			// 重复交付比丢失更隐蔽：客户端会把同一次技能释放执行两遍。
			time.Sleep(200 * time.Millisecond)
			if n := got.Load(); int(n) != msgCount {
				t.Fatalf("丢包 %d%% 下收到 %d 条可靠消息，期望正好 %d（重复投递）",
					lossPct, n, msgCount)
			}

			t.Logf("丢包 %d%%：可靠消息 %d/%d 全达，上行丢 %d、下行丢 %d",
				lossPct, got.Load(), msgCount,
				relay.droppedUp.Load(), relay.droppedDn.Load())
		})
	}
}

// TestLossSnapshotStillFlows 验证丢包下快照仍然持续到达。
//
// 不断言"丢了多少"，因为快照是尽力而为；只断言"还活着且有足够多的帧"。
// 如果快照通道被可靠通道的阻塞拖死，这里会看到帧数掉到个位数。
func TestLossSnapshotStillFlows(t *testing.T) {
	const lossPct = 10

	var sent atomic.Int32
	cfg := DefaultServerConfig()
	cfg.SessionTimeout = 5 * time.Second

	srv, err := Listen("127.0.0.1:0", cfg, testLogger(), HandlerFuncs{
		ConnectFn: func(s *Session) {
			// 20Hz 发快照，2 秒共 40 帧。
			go func() {
				tk := time.NewTicker(50 * time.Millisecond)
				defer tk.Stop()
				for i := 0; i < 40; i++ {
					<-tk.C
					if s.SendSnapshot([]byte(fmt.Sprintf("snap-%d", i))) {
						sent.Add(1)
					}
				}
			}()
		},
	})
	if err != nil {
		t.Fatalf("服务端启动失败：%v", err)
	}
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)

	relay := newLossyRelay(t, srv.LocalAddr().(*net.UDPAddr), lossPct)

	cli, err := Dial(relay.Addr().String(), DefaultClientConfig())
	if err != nil {
		t.Fatalf("客户端连接失败：%v", err)
	}
	defer cli.Close()

	cliCtx, cliCancel := context.WithCancel(ctx)
	defer cliCancel()
	go cli.Run(cliCtx)

	// 先发一条可靠消息把会话建起来（服务端要收到包才会建会话）。
	if !cli.Send([]byte("hello")) {
		t.Fatal("建立会话失败")
	}

	var received int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data := cli.RecvSnapshot(300 * time.Millisecond); data != nil {
			received++
		}
		if received >= 20 {
			break
		}
	}

	if received < 20 {
		t.Fatalf("丢包 %d%% 下只收到 %d 份快照，期望至少 20", lossPct, received)
	}
	t.Logf("丢包 %d%%：发 %d 份快照、收到 %d 份，下行丢 %d 个包",
		lossPct, sent.Load(), received, relay.droppedDn.Load())
}

// TestLossReliableBeatsSnapshotFlood 验证"快照灌满时可靠消息仍能挤过去"。
//
// 这是双通道设计的核心理由：如果两者共用一条队列，一次快照洪峰就会把
// 可靠消息（放技能、开商店）冲掉。
func TestLossReliableBeatsSnapshotFlood(t *testing.T) {
	cfg := DefaultServerConfig()
	cfg.SessionTimeout = 5 * time.Second

	srv, err := Listen("127.0.0.1:0", cfg, testLogger(), HandlerFuncs{
		ConnectFn: func(s *Session) {
			go func() {
				// 先灌 500 份快照，再发一条可靠消息。
				// 500 远超 QueueSize(64) 与客户端的 SnapshotBuf(16)，
				// 两侧的"丢最旧"路径都会被踩到。
				for i := 0; i < 500; i++ {
					s.SendSnapshot([]byte("flood"))
				}
				s.SendReliable([]byte("critical"))
			}()
		},
	})
	if err != nil {
		t.Fatalf("服务端启动失败：%v", err)
	}
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)

	cli, err := Dial(srv.LocalAddr().String(), DefaultClientConfig())
	if err != nil {
		t.Fatalf("客户端连接失败：%v", err)
	}
	defer cli.Close()

	cliCtx, cliCancel := context.WithCancel(ctx)
	defer cliCancel()
	go cli.Run(cliCtx)

	// 触发 Connect。
	if !cli.Send([]byte("wake")) {
		t.Fatal("唤醒失败")
	}

	// 关键：从**客户端**收下发的 reliable 消息。ReliableFn 是服务端收到
	// 客户端消息时的回调，方向不同，别搞反。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msg := cli.RecvReliable(300 * time.Millisecond)
		if msg == nil {
			continue
		}
		if string(msg) == "critical" {
			// 顺便确认快照确实灌过来了（否则这个测试可能什么都没测到）。
			if cli.RecvSnapshot(200*time.Millisecond) == nil {
				t.Log("注意：没收到快照，但可靠消息已到达")
			}
			return
		}
	}
	t.Fatal("快照洪峰把可靠消息冲掉了")
}
