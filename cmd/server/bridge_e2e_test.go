package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/game/world"
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
	return startE2EWith(t, func(cfg *config.Config) { cfg.Map.HubCount = 0 }, nil)
}

// startE2EWith 是带钩子的版本：tweak 在 room.New 之前改配置，setup 在
// r.Run 之前改世界（摆怪、调成长曲线）。
//
// 两个钩子的时机不能提前也不能推后：配置必须在 room.New 之前，因为房间
// 把 cfg.Progression 的指针抄进了 w.Prog；世界必须在 Run 之前，因为
// Run 之后 *world.World 归 tick goroutine 独占，测试 goroutine 再碰就是
// 数据竞争（AGENTS.md §2.5）。
func startE2EWith(t *testing.T, tweak func(*config.Config), setup func(*room.Room)) *e2eHarness {
	t.Helper()

	cfg := config.Default()
	if tweak != nil {
		tweak(cfg)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	r := room.New(cfg, log, room.Options{ID: "e2e", Seed: 42})
	if setup != nil {
		setup(r)
	}

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

// sendAttackFrames 持续按"朝 +X 瞄准 + 按住攻击"发输入，直到 stop 关闭。
//
// 走快照通道（与真实客户端一致）：输入是覆盖式数据，可靠通道上会被队头
// 阻塞连带延迟，见 TestReliableChannelRejectsInput。
func sendAttackFrames(t *testing.T, c *netx.Client) (stop func()) {
	t.Helper()

	done := make(chan struct{})
	var once sync.Once
	stop = func() { once.Do(func() { close(done) }) }
	t.Cleanup(stop)

	go func() {
		tick := uint32(1)
		for {
			select {
			case <-done:
				return
			default:
			}
			input, err := proto.Marshal(&proto.Envelope{
				Body: &proto.Envelope_Input{Input: &proto.Input{
					ClientTick: tick,
					Frames: []*proto.InputFrame{{
						Tick: tick, MoveX: 0, MoveY: 0,
						AimX: 1000, AimY: 0, Buttons: 1, // 1 = 攻击
					}},
				}},
			})
			if err != nil {
				return
			}
			c.SendSnapshot(input)
			tick++
			time.Sleep(16 * time.Millisecond)
		}
	}()
	return stop
}

// waitSelfInvuln 等一份自身快照，其无敌位等于 want。
func waitSelfInvuln(t *testing.T, c *netx.Client, want bool, what string) *proto.PlayerSelf {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
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
		self := body.Snapshot.Self
		got := self.Flags&uint32(world.PlayerFlagInvulnerable) != 0
		if got == want {
			return self
		}
	}
	t.Fatalf("等自身无敌位 = %v 超时（%s）", want, what)
	return nil
}

// TestChooseSkillRoundTrip 验证可靠下行与可靠上行这两条的完整接线：
// 房间发候选 → 网桥泵到会话 → 客户端收到 SkillChoices → 客户端回
// ChooseSkill → 网桥转交 room.ApplyChoice → tick 循环装技能并撤无敌。
//
// 这条测试是"可靠下行到底通不通"的唯一实证。只测 room 内部（
// internal/room/progression_test.go）证明不了网桥那一跳：房间把消息投进
// 通道就算成功，而真正把它塞进会话的是 bridge 的 pumpReliable；那一跳
// 断了，房间侧的测试全绿、线上却永远收不到候选。
func TestChooseSkillRoundTrip(t *testing.T) {
	var spawnAt world.Vec2

	h := startE2EWith(t,
		func(cfg *config.Config) {
			cfg.Map.HubCount = 0
			// 20 点经验刚好够从 1 级升到 2 级（ExpBase=20），一发子弹
			// 打死的怪就是一次升级。
			cfg.Progression.KillExp = 20
			// 窗口 8 秒（160 tick）远大于测试的 5 秒观察窗：这样"无敌被撤掉"
			// 只可能是玩家真的选了技能，不可能是超时兜底。窗口设 3 秒时
			// 超时路径也会在观察窗内撤掉无敌，断言就分不清两条路径。
			cfg.Progression.SkillChoiceWindowS = 8
		},
		func(r *room.Room) {
			// room.spawnPoint 是包私有的，cmz/server 够不到；无图世界的
			// slot 0 出生点是"世界中心左上方 1.5 m"，这里按同一公式算。
			// 这段重复不提取到 room 的导出 API 是有意的：出生点公式属于
			// 房间实现细节，为测试多开一个导出方法会让它变成公共契约。
			w := r.World.Terrain.WorldWidth()
			h := r.World.Terrain.WorldHeight()
			self := world.V(-1.5+w/2, -1.5+h/2)
			spawnAt = world.V(self.X+3, self.Y)
			id, m := r.World.Monsters.Spawn()
			m.HP, m.MaxHP = 5, 5
			m.Radius = 0.4
			m.Pos = spawnAt
			_ = id
		})

	c := dialE2E(t, h.addr, "chooser")
	sendAttackFrames(t, c)

	choices := waitEnvelope(t, c, "SkillChoices", func(e *proto.Envelope) bool {
		_, ok := e.Body.(*proto.Envelope_SkillChoices)
		return ok
	})
	list := choices.Body.(*proto.Envelope_SkillChoices).SkillChoices
	if list.GetLevel() != 2 {
		t.Fatalf("候选里的等级 = %d，期望 2（打死了那只 5 血的怪）", list.GetLevel())
	}
	if len(list.GetChoices()) != 3 {
		t.Fatalf("候选数量 = %d，期望 3", len(list.GetChoices()))
	}

	// 先确认窗口真的开着（无敌位在快照里为 1）。少了这一步，下面"等无敌
	// 变 false"会被窗口期第一帧就满足——那一帧本来就还没开或已开过，测试
	// 永远绿，连 ChooseSkill 被整个丢掉都发现不了（实测：把桥里的转交
	// 删掉，本用例照样 PASS）。
	waitSelfInvuln(t, c, true, "升级后窗口内应为无敌")

	// 选第一个，然后必须观察到无敌位被撤掉——那是"房间真的处理了这一条
	// ChooseSkill"的证据。窗口 8 秒、观察时限 5 秒，靠时间差排除超时路径。
	pick, err := proto.Marshal(&proto.Envelope{
		Body: &proto.Envelope_ChooseSkill{ChooseSkill: &proto.ChooseSkill{ChoiceIndex: 0}},
	})
	if err != nil {
		t.Fatalf("编码 ChooseSkill 失败：%v", err)
	}
	if !c.Send(pick) {
		t.Fatal("发送 ChooseSkill 失败")
	}

	waitSelfInvuln(t, c, false, "选了技能之后无敌应立刻撤掉")
}

// TestInteractIsAcceptedButIgnored 验证 Interact 在 M4 不会打崩连接。
//
// 背包与拾取是 Task 9/10，这里只有"收到并忽略"这条路。若 OnReliable 漏了
// 这个 case，消息会落到 default 分支——行为一样，所以断言只能取"连接仍
// 可用"，真正守住的是"不要因为未实现就 panic 或断开"。
func TestInteractIsAcceptedButIgnored(t *testing.T) {
	h := startE2E(t)
	c := dialE2E(t, h.addr, "interactor")

	msg, err := proto.Marshal(&proto.Envelope{
		Body: &proto.Envelope_Interact{Interact: &proto.Interact{Kind: 1}},
	})
	if err != nil {
		t.Fatalf("编码 Interact 失败：%v", err)
	}
	if !c.Send(msg) {
		t.Fatal("发送 Interact 失败")
	}

	// 连接还活着：Ping 能收到 Pong。
	ping, err := proto.Marshal(&proto.Envelope{
		Body: &proto.Envelope_Ping{Ping: &proto.Ping{ClientTimeNs: 42}},
	})
	if err != nil {
		t.Fatalf("编码 Ping 失败：%v", err)
	}
	if !c.SendSnapshot(ping) {
		t.Fatal("发送 Ping 失败")
	}
	// 连接还活着：快照通道还能收到 Pong（或至少还能收到快照）。
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
		if pong, ok := env.Body.(*proto.Envelope_Pong); ok && pong.Pong.ClientTimeNs == 42 {
			return
		}
	}
	t.Fatal("Interact 之后等 Pong 超时：这条消息可能把会话读循环卡住了")
}
