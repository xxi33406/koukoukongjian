package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/netx"
	"koukoukongjian/internal/proto"
	"koukoukongjian/internal/room"
)

// e2eHarness 起一套完整的服务端（传输 + 协议翻译 + 房间），返回可拨号的地址。
//
// 这里刻意不起 cmd/server/main 的完整 main 流程，而是照它的组装顺序手工拼：
// 这样测试能拿到随机端口，也不受信号处理与命令行参数影响。
type e2eHarness struct {
	addr string
	room *room.Room
}

func startE2E(t *testing.T) *e2eHarness {
	t.Helper()

	// HubCount=0 关地图：e2e 断言的是"输入 → 玩家位移"这条传输链路，
	// 生成的墙会把玩家困在出生据点里，位移断言就与链路无关地失败。
	cfg := config.Default()
	cfg.Map.HubCount = 0
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	r := room.New(cfg, log, room.Options{ID: "e2e", Seed: 42})

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("绑定 UDP 失败：%v", err)
	}
	addr := conn.LocalAddr().String()

	ctx, cancel := context.WithCancel(context.Background())

	sc := netx.DefaultServerConfig()
	sc.SessionTimeout = 2 * time.Second
	sc.SweepInterval = 100 * time.Millisecond

	b := newNetBridge(ctx, r, cfg, log)
	srv, err := netx.NewServer(conn, sc, log, b)
	if err != nil {
		cancel()
		_ = conn.Close()
		t.Fatalf("创建服务端失败：%v", err)
	}

	var done bool
	stop := func() {
		if done {
			return
		}
		done = true
		cancel()
		_ = srv.Close()
		b.Wait()
	}
	t.Cleanup(stop)

	go func() { _ = srv.Run(ctx) }()
	go func() { r.Run(ctx) }()

	return &e2eHarness{addr: addr, room: r}
}

// dialE2E 连上服务端并完成握手与加入房间。
func dialE2E(t *testing.T, addr, name string) *netx.Client {
	t.Helper()

	c, err := netx.Dial(addr, netx.DefaultClientConfig())
	if err != nil {
		t.Fatalf("拨号失败：%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		c.Close()
	})
	go func() { _ = c.Run(ctx) }()

	// 握手。
	hello, err := proto.Marshal(&proto.Envelope{
		Body: &proto.Envelope_Hello{Hello: proto.NewHello(name, "")},
	})
	if err != nil {
		t.Fatalf("编码 Hello 失败：%v", err)
	}
	if !c.Send(hello) {
		t.Fatal("发送 Hello 失败")
	}
	waitEnvelope(t, c, "HelloAck", func(e *proto.Envelope) bool {
		_, ok := e.Body.(*proto.Envelope_HelloAck)
		return ok
	})

	// 加入房间。
	join, err := proto.Marshal(&proto.Envelope{
		Body: &proto.Envelope_JoinRoom{JoinRoom: &proto.JoinRoom{PreferredSlot: 0xFF}},
	})
	if err != nil {
		t.Fatalf("编码 JoinRoom 失败：%v", err)
	}
	if !c.Send(join) {
		t.Fatal("发送 JoinRoom 失败")
	}
	waitEnvelope(t, c, "JoinOk", func(e *proto.Envelope) bool {
		_, ok := e.Body.(*proto.Envelope_JoinOk)
		return ok
	})

	return c
}

// waitEnvelope 在可靠通道上等一条满足条件的小包消息。
func waitEnvelope(t *testing.T, c *netx.Client, what string, match func(*proto.Envelope) bool) *proto.Envelope {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw := c.RecvReliable(200 * time.Millisecond)
		if raw == nil {
			continue
		}
		var env proto.Envelope
		if err := proto.Unmarshal(raw, &env); err != nil {
			continue
		}
		if match(&env) {
			return &env
		}
	}
	t.Fatalf("等 %s 超时", what)
	return nil
}

// TestPingGoesOverSnapshotChannel 验证 Ping 走快照通道、Pong 也走快照通道。
//
// 这个测试守的是两件事：
//  1. Ping 被搬到了快照通道 —— 服务端必须在 OnSnapshot 里处理它，
//     而不是只在 OnReliable 里。
//  2. Pong 也从快照通道回来 —— 走可靠通道会让 RTT 样本被重传延迟污染。
//
// 只在 OnReliable 里实现 Ping 时，本测试会在等 Pong 时超时失败。
func TestPingGoesOverSnapshotChannel(t *testing.T) {
	h := startE2E(t)
	c := dialE2E(t, h.addr, "pinger")

	const sentAt = uint64(1700000000123456789)
	ping, err := proto.Marshal(&proto.Envelope{
		Body: &proto.Envelope_Ping{Ping: &proto.Ping{ClientTimeNs: sentAt}},
	})
	if err != nil {
		t.Fatalf("编码 Ping 失败：%v", err)
	}
	if !c.SendSnapshot(ping) {
		t.Fatal("发送 Ping 失败")
	}

	// Pong 必须从快照通道回来。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw := c.RecvSnapshot(200 * time.Millisecond)
		if raw == nil {
			continue
		}
		var env proto.Envelope
		if err := proto.Unmarshal(raw, &env); err != nil {
			continue
		}
		pong, ok := env.Body.(*proto.Envelope_Pong)
		if !ok || pong.Pong == nil {
			continue // 这条是快照，同一个通道上交错到达。
		}
		if pong.Pong.ClientTimeNs != sentAt {
			t.Fatalf("Pong 回显的 ClientTimeNs = %d，期望 %d", pong.Pong.ClientTimeNs, sentAt)
		}
		return
	}
	t.Fatal("快照通道上等到 Pong 超时：Ping 可能仍走在可靠通道上")
}

// TestReliableChannelRejectsInput 验证可靠通道上不再处理输入。
//
// 输入是覆盖式数据，走可靠通道会被队头阻塞连带延迟（实测一次丢包连累
// 6 帧、最大 116ms）。这条测试守住"别再把 Input 放回可靠通道"。
func TestReliableChannelRejectsInput(t *testing.T) {
	h := startE2E(t)
	c := dialE2E(t, h.addr, "wrongchannel")

	// 先记下玩家初始位置。
	before := waitSelfPos(t, c)

	// 把输入错发到可靠通道。
	input, err := proto.Marshal(&proto.Envelope{
		Body: &proto.Envelope_Input{Input: &proto.Input{
			ClientTick: 1,
			Frames: []*proto.InputFrame{{
				Tick: 1, MoveX: 1000, MoveY: 0, AimX: 1000, Buttons: 0,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("编码 Input 失败：%v", err)
	}
	if !c.Send(input) {
		t.Fatal("发送 Input 失败")
	}

	// 给它足够时间跑几个 tick（可靠通道会立刻送达，若被处理位置会变）。
	time.Sleep(300 * time.Millisecond)

	after := waitSelfPos(t, c)
	if after.x != before.x || after.y != before.y {
		t.Fatalf("可靠通道上的输入被处理了：位置从 (%.2f,%.2f) 变成 (%.2f,%.2f)",
			before.x, before.y, after.x, after.y)
	}
}

type point struct{ x, y float64 }

// waitSelfPos 等一份带 self 的快照，返回量化后的自身位置。
func waitSelfPos(t *testing.T, c *netx.Client) point {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw := c.RecvSnapshot(200 * time.Millisecond)
		if raw == nil {
			continue
		}
		var env proto.Envelope
		if err := proto.Unmarshal(raw, &env); err != nil {
			continue
		}
		body, ok := env.Body.(*proto.Envelope_Snapshot)
		if !ok || body.Snapshot == nil || body.Snapshot.Self == nil {
			continue
		}
		if body.Snapshot.Self.Pos == nil {
			continue
		}
		return point{
			x: proto.DequantizePos(body.Snapshot.Self.Pos.X),
			y: proto.DequantizePos(body.Snapshot.Self.Pos.Y),
		}
	}
	t.Fatal("等自身位置超时")
	return point{}
}

// TestInputOverSnapshotChannelMovesPlayer 验证输入走快照通道确实让玩家移动。
//
// 与上一条互补：一条守"可靠通道不收"，一条守"快照通道收得下"。
func TestInputOverSnapshotChannelMovesPlayer(t *testing.T) {
	h := startE2E(t)
	c := dialE2E(t, h.addr, "mover")

	before := waitSelfPos(t, c)

	// 持续往 +X 推，模拟真实客户端 60Hz 发输入。
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()

	sendErr := make(chan error, 1)
	go func() {
		tick := uint32(1)
		for ctx.Err() == nil {
			input, err := proto.Marshal(&proto.Envelope{
				Body: &proto.Envelope_Input{Input: &proto.Input{
					ClientTick: tick,
					Frames: []*proto.InputFrame{{
						Tick: tick, MoveX: 1000, MoveY: 0, AimX: 1000, Buttons: 0,
					}},
				}},
			})
			if err != nil {
				sendErr <- err
				return
			}
			c.SendSnapshot(input)
			tick++
			time.Sleep(16 * time.Millisecond)
		}
		sendErr <- nil
	}()

	<-ctx.Done()
	if err := <-sendErr; err != nil {
		t.Fatalf("编码输入失败：%v", err)
	}

	after := waitSelfPos(t, c)
	if after.x <= before.x {
		t.Fatalf("往 +X 推了 0.6 秒，位置从 %.2f 变成 %.2f，期望变大（输入没被处理）",
			before.x, after.x)
	}
}
