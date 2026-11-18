// Command bot 是一个无头测试客户端。
//
// 它的用途不是"玩"，而是把网络闭环的每一环都压出来：握手、加入、输入
// 上行、快照下行、断线重连。压力测试和丢包回归都靠它跑。
//
// 设计上刻意只依赖 netx 与 proto，不碰 room / world——如果 bot 需要
// import 服务端内部结构才能连上，说明协议本身不完整。
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"koukoukongjian/internal/netx"
	"koukoukongjian/internal/proto"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "bot 失败：%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		addr     = flag.String("addr", "127.0.0.1:7777", "服务器地址（须与服务器 bind 端口一致）")
		name     = flag.String("name", "bot", "玩家名")
		count    = flag.Int("count", 1, "并发 bot 数量")
		duration = flag.Duration("duration", 10*time.Second, "运行时长")
		inputHz  = flag.Int("input-hz", 30, "输入发送频率")
		circle   = flag.Bool("circle", true, "绕圈移动（否则随机方向）")
		verbose  = flag.Bool("v", false, "打印每帧快照统计")

		attack  = flag.Bool("attack", false, "一直按住攻击键")
		holdAim = flag.Bool("hold-aim", true, "是否给瞄准方向（false 则置零，触发服务端自动瞄准）")
	)
	flag.Parse()

	if *count < 1 {
		return fmt.Errorf("count 必须 >= 1")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *duration)
	defer cancel()

	var (
		totalSnap atomic.Uint64
		totalRecv atomic.Uint64
		okBots    atomic.Int32
	)

	var wg sync.WaitGroup
	for i := 0; i < *count; i++ {
		botName := *name
		if *count > 1 {
			botName = fmt.Sprintf("%s-%d", *name, i)
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			err := runBot(ctx, *addr, botName, idx, *inputHz, *circle, *attack, *holdAim, *verbose,
				&totalSnap, &totalRecv)
			if err != nil {
				fmt.Printf("[%s] 失败：%v\n", botName, err)
				return
			}
			okBots.Add(1)
		}(i)
	}
	wg.Wait()

	fmt.Printf("完成：%d/%d 个 bot 正常退出，共收到 %d 份快照，%d 个包\n",
		okBots.Load(), *count, totalSnap.Load(), totalRecv.Load())
	if okBots.Load() == 0 {
		return fmt.Errorf("所有 bot 都失败了")
	}
	return nil
}

// bot 是一个 bot 的运行时状态。
type bot struct {
	name string
	idx  int

	snaps atomic.Uint64
	recv  atomic.Uint64

	// tick 是客户端自增的输入序号，服务端会回显它。
	tick atomic.Uint32

	// entities 是快照流重放出的在场实体表（-v 实体行的数据源），
	// 只在 recvSnapshots 那个 goroutine 里读写，无需加锁。
	// 键带池种类（entityRef）：四个池各自从索引 1 编号，裸 ID 会撞号。
	// 逻辑见 entities.go；单测在 entities_test.go。
	entities map[entityRef]entInfo

	// selfID 是自己的实体 id（来自快照 self 字段）；invalidSelfID 表示
	// 还没收到过带 self 的快照。
	selfID uint32

	kills  atomic.Uint64 // 击杀：KILL 事件的击杀者（A）是自己
	deaths atomic.Uint64 // 阵亡：KILL/DEATH 事件的被击杀者（B）是自己
}

// invalidSelfID 与 world.InvalidID 对齐（proto 层不 import world，本地镜像）。
const invalidSelfID uint32 = 0

func runBot(ctx context.Context, addr, name string, idx, inputHz int, circle, attack, holdAim, verbose bool,
	snaps, recv *atomic.Uint64) error {

	cfg := netx.DefaultClientConfig()
	c, err := netx.Dial(addr, cfg)
	if err != nil {
		return fmt.Errorf("连接失败：%w", err)
	}
	defer c.Close()

	b := &bot{name: name, idx: idx, selfID: invalidSelfID, entities: make(map[entityRef]entInfo)}

	// Client.Run 会阻塞在读循环上，所以放独立 goroutine。
	runErr := make(chan error, 1)
	go func() { runErr <- c.Run(ctx) }()

	// 1. 握手。等 HelloAck。
	if err := b.handshake(c, name); err != nil {
		return err
	}

	// 2. 加入房间。等 JoinOk。
	if err := b.join(c); err != nil {
		return err
	}

	// 3. 并行跑输入发送与快照接收。
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		b.sendInputs(ctx, c, inputHz, circle, attack, holdAim)
	}()
	go func() {
		defer wg.Done()
		b.recvSnapshots(ctx, c, verbose)
	}()
	wg.Wait()

	select {
	case err := <-runErr:
		if err != nil && ctx.Err() == nil {
			return fmt.Errorf("网络读循环退出：%w", err)
		}
	default:
	}

	// 汇报统计。RecvPackets 是传输层计数，snaps 是应用层计数——两者
	// 差得多说明有大量非快照包（比如重复的 ack 包），是有用的信号。
	st := c.Stats(time.Now())
	snaps.Add(b.snaps.Load())
	recv.Add(uint64(st.RecvPackets))

	fmt.Printf("[%s] 快照 %d 份，收包 %d 个，发包 %d 个，重传中 %d，丢发送 %d\n",
		b.name, b.snaps.Load(), st.RecvPackets, st.SentPackets,
		st.ReliableInFly, st.DroppedSend)

	fmt.Printf("[%s] 击杀 %d，阵亡 %d\n", b.name, b.kills.Load(), b.deaths.Load())

	if b.snaps.Load() == 0 {
		return fmt.Errorf("一份快照都没收到")
	}
	return nil
}

// handshake 发送 Hello 并等待 HelloAck。
func (b *bot) handshake(c *netx.Client, name string) error {
	hello := proto.NewHello(name, "")
	data, err := proto.Marshal(&proto.Envelope{
		Body: &proto.Envelope_Hello{Hello: hello},
	})
	if err != nil {
		return err
	}
	if !c.Send(data) {
		return fmt.Errorf("发送 Hello 失败")
	}

	reply := c.RecvReliable(3 * time.Second)
	if reply == nil {
		return fmt.Errorf("等待 HelloAck 超时")
	}
	var env proto.Envelope
	if err := proto.Unmarshal(reply, &env); err != nil {
		return fmt.Errorf("HelloAck 解码失败：%w", err)
	}
	ack, ok := env.Body.(*proto.Envelope_HelloAck)
	if !ok {
		return fmt.Errorf("期望 HelloAck，收到别的消息")
	}
	if ack.HelloAck.TickRate == 0 {
		return fmt.Errorf("HelloAck 里 tick_rate 为 0")
	}
	return nil
}

// join 发送 JoinRoom 并等待 JoinOk。
func (b *bot) join(c *netx.Client) error {
	data, err := proto.Marshal(&proto.Envelope{
		Body: &proto.Envelope_JoinRoom{
			JoinRoom: &proto.JoinRoom{RoomId: "", PreferredSlot: 0xFF},
		},
	})
	if err != nil {
		return err
	}
	if !c.Send(data) {
		return fmt.Errorf("发送 JoinRoom 失败")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		reply := c.RecvReliable(500 * time.Millisecond)
		if reply == nil {
			continue
		}
		var env proto.Envelope
		if err := proto.Unmarshal(reply, &env); err != nil {
			continue
		}
		if ok, isOk := env.Body.(*proto.Envelope_JoinOk); isOk {
			if ok.JoinOk.WorldSeed == 0 {
				return fmt.Errorf("JoinOk 里 world_seed 为 0")
			}
			return nil
		}
	}
	return fmt.Errorf("等待 JoinOk 超时")
}

// inputWindow 是每包重复携带的输入帧数。
//
// 输入走不可靠通道，丢包就丢意图。重复携带最近 3 帧后，短按被彻底丢失
// 的概率从 10% 降到 0.1%（连续丢 3 包）。带宽代价：带 3 帧 6.5 KB/s，
// 带 1 帧 2.7 KB/s，而下行快照是 60~120 KB/s，占比可忽略。
// 实测见 docs/channel-design.md。
const inputWindow = 3

// sendInputs 按固定频率发输入，方向绕圈或随机。
//
// 每包带最近 inputWindow 帧（按 tick 升序），服务器按 tick 去重，取最新
// 一帧作移动意图，窗口内所有 buttons 做按位或。这样一包丢失不会丢意图。
func (b *bot) sendInputs(ctx context.Context, c *netx.Client, hz int, circle, attack, holdAim bool) {
	if hz <= 0 {
		hz = 30
	}
	interval := time.Second / time.Duration(hz)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	rng := rand.New(rand.NewSource(int64(b.idx)*7919 + 13))
	// 每个 bot 起始相位错开，否则所有 bot 叠在一起走。
	phase := float64(b.idx) * math.Pi / 2

	// window 保存最近几帧，环形使用。0 号位永远是最旧的。
	var window [inputWindow]*proto.InputFrame

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		tick := b.tick.Add(1)

		var mx, my, ax, ay int32
		if circle {
			// 绕圈走：方向随 tick 匀速旋转。
			t := float64(tick) * 0.05
			ang := t + phase
			mx = int32(math.Cos(ang) * 1000)
			my = int32(math.Sin(ang) * 1000)
			// 瞄准与移动同向，这样画面上朝向和速度一致。
			ax, ay = mx, my
		} else {
			ang := rng.Float64() * 2 * math.Pi
			mx = int32(math.Cos(ang) * 1000)
			my = int32(math.Sin(ang) * 1000)
			ax, ay = mx, my
		}

		if !holdAim {
			// 不给方向：aim_x/aim_y 置零，服务端据此走自动瞄准。
			// 注意不能用"方向等于 0 弧度"来表达"没给方向"——
			// 0 是合法朝向（正 +X），两者必须分开表达。
			ax, ay = 0, 0
		}
		var buttons uint32
		if attack {
			buttons = 1 // room.ButtonAttack
		}

		// 左移窗口，把新帧放进最后一格。
		copy(window[:], window[1:])
		window[inputWindow-1] = &proto.InputFrame{
			Tick:  tick,
			MoveX: mx, MoveY: my,
			AimX: ax, AimY: ay,
			Buttons: buttons,
		}

		// 只发已经填上的帧：客户端刚启动时前两包不满 3 帧。
		frames := make([]*proto.InputFrame, 0, inputWindow)
		for _, f := range window {
			if f != nil {
				frames = append(frames, f)
			}
		}

		msg := &proto.Envelope{
			Body: &proto.Envelope_Input{
				Input: &proto.Input{
					ClientTick: tick,
					Frames:     frames,
				},
			},
		}
		data, err := proto.Marshal(msg)
		if err != nil {
			return
		}
		// 走快照通道：输入是覆盖式数据，迟到不如不到。
		if !c.SendSnapshot(data) {
			// 发送队列满，跳过这一包。窗口里的帧不会丢——下一包还会带上
			// 它们，这正是重复携带的好处。
			continue
		}
	}
}

// recvSnapshots 收快照并统计。
func (b *bot) recvSnapshots(ctx context.Context, c *netx.Client, verbose bool) {
	// 实体行节流：快照 20Hz，全打会刷屏；每秒一行足够观察波次刷怪。
	var lastEntLine time.Time
	var kills, deaths uint64

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		data := c.RecvSnapshot(200 * time.Millisecond)
		if data == nil {
			continue
		}
		// 快照通道上不止快照一种消息（Pong 也走这条通道），所以要先解
		// Envelope 看类型。直接往 proto.Snapshot 里解不会报错，只会得到
		// 一个空快照——那种"连上了但画面不动"的 bug 很难查。
		var env proto.Envelope
		if err := proto.Unmarshal(data, &env); err != nil {
			continue
		}
		body, ok := env.Body.(*proto.Envelope_Snapshot)
		if !ok || body.Snapshot == nil {
			continue
		}
		snap := body.Snapshot
		b.snaps.Add(1)
		if snap.Self != nil {
			b.selfID = snap.Self.EntityId
		}

		// —— 实体表重放 ——
		//
		// 一步到位交给 applySnapshot：全量帧要先清表重建（丢包自愈），
		// removes 要按 kind 删。这三件事的顺序是协议语义，散在 main 里
		// 迟早被某次"顺手加一行"打乱，且有单测钉住才好改。
		b.applySnapshot(snap)

		// —— 事件统计 ——
		// KILL：A=击杀者、B=被击杀者；DEATH：A 无效（Reap 兜底）、B=死者。
		// 玩家被怪打死走 KILL（A=怪）；A==0 的 DEATH 是清理死亡实体。
		for _, ev := range snap.Events {
			switch ev.Kind {
			case proto.GameEventKind_GAME_EVENT_KIND_KILL:
				if ev.A == b.selfID {
					kills++
					b.kills.Store(kills)
				}
				if ev.B == b.selfID {
					deaths++
					b.deaths.Store(deaths)
				}
			case proto.GameEventKind_GAME_EVENT_KIND_DEATH:
				if ev.B == b.selfID {
					deaths++
					b.deaths.Store(deaths)
				}
			}
		}

		if snap.Self == nil && b.snaps.Load() > 1 {
			// 第一份快照可能刚好落在自己加入之前；之后每份都该有 self。
			if verbose {
				fmt.Printf("[%s] 快照 %d 缺少 self\n", b.name, snap.ServerTick)
			}
		}
		if verbose && b.snaps.Load()%20 == 0 {
			x, y := 0.0, 0.0
			if snap.Self != nil {
				x = proto.DequantizePos(snap.Self.Pos.X)
				y = proto.DequantizePos(snap.Self.Pos.Y)
			}
			fmt.Printf("[%s] tick=%d self=(%.1f,%.1f) adds=%d upd=%d rem=%d\n",
				b.name, snap.ServerTick, x, y,
				len(snap.Adds), len(snap.Updates), len(snap.Removes))
		}

		// —— -v 实体行：每秒一行，前 8 个实体 ——
		//
		// 端到端验证靠这行判断波次是否刷怪（M 数量变化）与怪是否在动
		// （坐标变化）。只打前 8 个避免弹幕期刷屏；怪多时看 M 计数即可。
		if verbose && time.Since(lastEntLine) >= time.Second {
			lastEntLine = time.Now()
			fmt.Printf("[%s] %s\n", b.name, formatEntities(b.entities, 8))
		}
	}
}
