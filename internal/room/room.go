package room

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sync/atomic"
	"time"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/game/ai"
	"koukoukongjian/internal/game/build"
	"koukoukongjian/internal/game/fog"
	"koukoukongjian/internal/game/mapgen"
	"koukoukongjian/internal/game/systems"
	"koukoukongjian/internal/game/world"
	"koukoukongjian/internal/proto"
	"koukoukongjian/internal/snapshot"
)

// Room 是一局游戏。
// histSnapshotEvery 是 tick 直方图快照的刷新间隔（TickSamples 数）。
const histSnapshotEvery = 100

type Room struct {
	ID    string
	World *world.World
	cfg   *config.Config
	log   *slog.Logger
	seed  int64

	// tickInterval 是逻辑 tick 周期（如 20Hz → 50ms）。
	tickInterval time.Duration
	subSteps     int
	// subDt 是一个子步的时长（秒），来自 config，恒定不变——绝不用真实
	// 耗时做积分，否则同一份输入在不同机器上算出不同结果，回放就废了。
	subDt float64

	// overrun 累计"这一 tick 没跑完就超时"的次数。
	//
	// 用原子量：指标会被别的 goroutine 读。tick 循环本身是单 goroutine 的，
	// 所以不用锁保护 World。
	overrun atomic.Uint64
	// ticksRun 累计执行的 tick 数。
	ticksRun atomic.Uint64
	// lastTickDur 是上一 tick 的实际耗时（纳秒），用于观测。
	lastTickDur atomic.Int64

	// peers 是四个座位。只在 tick 循环里读写。
	peers peers
	// peerCount 是 peers.count 的原子镜像，供外部 goroutine 读取。
	peerCount atomic.Int32
	// worldTick 是 World.Tick 的原子镜像，供外部 goroutine 读取。
	//
	// World.Tick 只在 tick 循环里写。别处要读（比如加入房间时回一个
	// 起始 tick）必须走这里，否则就是跨 goroutine 读正在被改的内存。
	worldTick atomic.Uint32

	// joins 是等待 tick 循环处理的加入请求。
	//
	// 带缓冲：网络 goroutine 写入时不阻塞，即使 tick 循环正忙。
	// 容量给 MaxSlots 的两倍——一局最多 4 人加入，多出来的请求可以排队
	// 等下一 tick 被拒绝，而不是让调用方卡住。
	joins chan JoinRequest
	// inputs 是等待处理的上行输入。
	//
	// 容量按"4 个玩家 × 每秒 30 条输入 × 1 秒的抖动余量"估算。满了就丢弃，
	// 因为迟到的输入比丢掉的输入更糟：它会让角色按几百毫秒前的按键移动。
	inputs chan peerInput
	// leaves 是等待处理的离开请求。
	leaves chan leaveRequest
	// choices 是等待处理的技能选择请求。
	//
	// 为什么必须走 channel：ApplyChoice 由 bridge 的网络 goroutine 调用，
	// 而 World 归 tick goroutine 独占（AGENTS.md §2.5）。直接改实体的
	// SkillPassive 就是跨 goroutine 写，-race 会报，且表现为"偶尔少装一个
	// 技能"这种查不出的偶发。与 inputs/joins 同款：网络侧投递，tick 里
	// 非阻塞消费。
	choices chan choiceRequest
	// interacts 是等待处理的交互请求（开背包 / 拾取最近）。
	//
	// 与 choices 同款约束与同款理由：Interact 由 bridge 的网络 goroutine
	// 调用，而 World 归 tick goroutine 独占（AGENTS.md §2.5）。开背包要
	// 写 PlayerState.InventoryOpenAt 与 Flags，直接写就是跨 goroutine 写。
	//
	// 容量 MaxSlots×4 而非 ×8：交互是玩家按键触发（不是每帧的输入），
	// 每人每秒最多几次；留 4 秒余量已经远超实际，满了丢弃不会积累。
	interacts chan interactRequest
	// shopBuys 是等待处理的买入请求（M6 Task 10）。
	//
	// 与 interacts 同款约束与同款理由：ShopBuy 由 bridge 的网络 goroutine
	// 调用，而 World 归 tick goroutine 独占（AGENTS.md §2.5）。买入要写
	// 玩家 Gold 与技能/道具槽位，直接写就是跨 goroutine 写。
	shopBuys chan shopBuyRequest
	// reliable 是房间要经可靠通道下发的消息（技能候选这类漏了就没法补救
	// 的消息）。room 不 import netx（分层硬约束），所以这里只出一个通道，
	// 由 bridge 那边泵到会话的可靠通道上。
	//
	// 容量 MaxSlots×4：一次升级一个玩家一条消息，加上窗口内再升级的追加
	// 下发，最坏情况是几 tick 内积压几条。满了就丢并记日志——丢掉的候选
	// 不是死局：窗口到期会自动随机装一个。
	reliable chan ReliableMessage
	// events 是这一 tick 攒下的伤害/击杀事件，M2 只攒不发（快照带上它
	// 属于 M3 的 UI 反馈）。每个 tick 开头用 [:0] 清空以复用底层数组。
	events []world.DamageEvent
	// miscEvents 装不是伤害事件的 GameEvent（复活、团灭）。
	//
	// 为什么与 r.events 分开：r.events 是 []world.DamageEvent，它的形状由
	// 伤害结算决定（Src/Dst/Amount/IsKill），硬塞复活进去会逼着给 DamageEvent
	// 加一个"我其实不是伤害"的 Kind 字段——那是把两件事装进一个结构。
	// 分开之后 broadcast 里拼一下就行，每条只有一次分配。
	//
	// 每 tick 开头用 [:0] 清空，与 r.events 同节拍。
	miscEvents []*proto.GameEvent
	// fog 是队伍共享迷雾状态（HasMap 房间才有，否则为 nil）。
	// 只在 tick 循环里读写——Run 起来之后 World 归 tick goroutine 独占，
	// fog 点灯依据玩家位置，与 World 同生命周期同并发约束。
	fog *fog.State
	// fogDelta 是本 tick 要随快照下发的迷雾增量（新点亮格索引）。
	// fog.Reveal 返回内部复用切片，必须拷进自己的数组再上快照；
	// 每 tick 用 [:0] 清空复用，空增量时置 nil 不下发（省带宽）。
	fogDelta []uint64
	// wavesEnabled 是波次开关的落定值（Options 互斥裁定后的结果）。
	// Task 10 波次系统接线前只有声明无消费；先落字段，接线时直接读。
	wavesEnabled bool
	// hubStates 是上一 tick 各据点的 HubState 快照，长度 = len(World.Hubs)。
	//
	// 用来在 tick 里 diff 出「新开波」与「清场」并打日志——waves.go 文件头
	// 承诺「波次事件只进 room 日志」，但 game 包不许依赖 log、UpdateWaves
	// 又不返回值，所以这个承诺只能在 room 侧兑现：比较前后状态。
	//
	// 只在 tick goroutine 里读写，无需加锁（AGENTS §2.5）。首 tick 时切片
	// 为空、recordHubStates 按当前据点数量重建；全零值天然等于 HubIdle，
	// 正好是据点初始态，不需要"首 tick 特判"。
	hubStates []world.HubState
	// bossPhaseSeen 是见过的最高的 Boss 阶段，只用于"阶段切换打一次日志"。
	//
	// 不能靠 BossPhase 本身判：阶段是状态（现在几阶段），日志需要的是
	// 事件（这一 tick 刚变了）。存"见过的最高值"让事件可以从比较中得出，
	// 且天然幂等——Boss 死了槽位清零也不会让日志重复。
	//
	// 用 atomics 而不是裸 uint8：Boss 阶段只存在于 world（tick goroutine
	// 独占），测试与将来的运维探针都需要从外部观察它，而"从外部观察"
	// 唯一安全的形态就是原子。
	bossPhaseSeen atomic.Uint32
	// bossSeen 表示本房间刷出过 Boss。同样是给外部观察用的原子镜像：
	// World.BossSpawned 只在 tick goroutine 里安全。
	bossSeen atomic.Bool
	// tickHist 是本房间的 tick 耗时直方图（M7 压测）。
	//
	// 只在 tick goroutine 里改。跨 goroutine 的读者（cmd/server 的退出
	// 路径）走 histSnapshot——不可变快照，每 histSnapshotEvery 个样本刷新。
	tickHist tickHistogram
	// histSnapshot 是 tickHist 的不可变快照，供 Metrics() 跨 goroutine 读。
	histSnapshot atomic.Pointer[tickHistogram]
	// pendingLevelUps 是本 tick 子步里升了级的玩家，攒到子步循环结束后
	// 统一处理（发候选 + 开无敌窗口）。
	//
	// 为什么不在 subStep 里当场处理：子步是高频物理路径（SubSteps=3），
	// 而"发候选"要生成随机数、要写 peer 状态、要编码消息。放在子步里
	// 意味着同一 tick 升一级可能被处理三次（每子步一次），候选被重复
	// 下发、窗口被重置三次。攒起来在 tick 级处理一次，与 AI/波次/fire
	// 同款"世界级结算"节拍。
	pendingLevelUps []world.EntityID
	// nextSnapshot 是本 tick 生成好、待网络层取走的快照。
	// 这里不用它——快照直接写进每个 peer 的 sink。保留字段会误导，故无。

	// done 关闭时 Room.Run 退出。
	done chan struct{}
	// stopped 表示 Run 已返回，避免重复 Stop。
	stopped atomic.Bool

	// resultSink 接收本局结果（M7）。nil 表示不落盘。
	//
	// 只在 shutdown 里被调用一次（关房那一刻），所以不需要 channel 中转：
	// shutdown 不在 tick 循环里，阻塞写盘也只会拖慢关房。
	resultSink ResultSink
	// startedAt 是房间创建时的墙体时钟（Unix 秒），用于算对局时长。
	//
	// 用墙体时钟而不是 tick 数：§11.1 的 duration_sec 是给人看的
	// （"这局打了多久"），而 tick 数在超时丢帧时会与实际时间脱钩。
	startedAt int64
	// outcome 是本局终态（每 tick 由 updateOutcome 刷新，只在 tick
	// goroutine 里写）。
	outcome world.MatchOutcome
	// reported 保证结果只上报一次。
	//
	// 用原子量：shutdown 可能从 Run 的两个分支（ctx done / stop called）
	// 与外部 Stop 竞进，重复上报会让同一局在文件里出现两行。
	reported atomic.Bool
}

// ResultSink 接收一局的结果（M7）。
//
// 为什么是接口而不是直接依赖 internal/store：Room 是游戏逻辑 + 网络入口，
// 让它知道"JSONL 文件路径"是把它和存储介质绑死。用接口后测试可以传
// 内存 sink 断言"关房时确实上报了"，而 cmd/server 负责把 store.Writer
// 接上去。将来换 SQLite 不需要动 room（拍板决定 #2 与 §11.1.3）。
//
// 实现方**不得阻塞**（§11.1.3 硬约束第 1 条）：调用点在 shutdown 里，
// 阻塞一次就拖住整个关房流程。
type ResultSink interface {
	Report(rec world.MatchResult)
}

// peerInput 是带座位号的输入。
type peerInput struct {
	slot int
	cmd  InputCommand
}

// leaveRequest 是请求离开房间。
type leaveRequest struct {
	slot  int
	reply chan error
}

// choiceRequest 是请求应用一次技能选择。
//
// reply 必须缓冲 1：tick 循环投回执时非阻塞，若调用方刚好被 ctx 取消/房间
// 停止放走，tick 循环不能因此卡住（与 JoinRequest.Reply 同款约定）。
type choiceRequest struct {
	slot  int
	index int
	reply chan error
}

// interactRequest 是请求处理一次玩家交互（开背包 / 拾取最近）。
//
// kind 直接用协议里的数值（proto.Interact.Kind：1=拾取最近，2=开背包），
// 不在这里另造一套枚举：两套枚举的映射表迟早会漏一项，而症状是"某个
// 交互键没反应"。校验放在 Interact 入口，其余值直接拒绝（proto 注释已经
// 写明"其余值服务端忽略"，客户端未来版本加的新 kind 不该在这里 panic）。
type interactRequest struct {
	slot  int
	kind  int
	reply chan error
}

// shopBuyRequest 是一次买入请求（M6 Task 10）。
//
// hub 由客户端带上来而不是服务端自己查：它能防住「在 A 据点点开商店、
// 走到 B 据点才发买入」的时序问题——服务端拿它与自己记录的那个据点比，
// 不一致就拒绝，而不是把 A 的货架按 B 的价格卖出去。
type shopBuyRequest struct {
	slot       int
	hub        uint32
	itemIndex  int
	targetSlot int
	reply      chan error
}

// ReliableMessage 是房间要经可靠通道下发的一条消息。
//
// Slot 是收件座位号；Data 是已编码的 Envelope。room 不碰会话，bridge 按
// Slot 找到对应的 *netx.Session 再 SendReliable——分层上 room 不能 import
// netx（AGENTS.md §2.4），而可靠下行只有 bridge 够得着。
type ReliableMessage struct {
	Slot int
	Data []byte
}

// choiceReplyTimeout 是 ApplyChoice 等待 tick 循环处理的上限。
//
// 取 1 秒 = 20 个 tick（20Hz）：正常路径下请求在下一个 tick 就被消费，
// 差了两个数量级。它只在 tick 循环卡死时触发，用来保证 bridge 的会话
// 读 goroutine 不会永久阻塞——那会让该会话连断线都发现不了。
const choiceReplyTimeout = time.Second

// Options 是创建房间的参数。
type Options struct {
	ID   string
	Seed int64
	// DummyTargets 是开局摆放的静止靶数量，仅供端到端验证用。
	//
	// 正常对局里怪物由波次系统生成（M3）。M2 没有波次，没有靶子就
	// 无法从客户端看到任何战斗反馈。摆在 New 而不是 Run 里：
	// Run 一旦走起来，World 就归 tick goroutine 独占。
	DummyTargets int
	// WavesEnabled 是 M3 据点波次开关。DummyTargets>0 时强制 false
	//（互斥，见 New）：bot 压测路径只要静止靶，不想要波次怪的干扰。
	WavesEnabled bool
}

// New 创建一个房间。
func New(cfg *config.Config, log *slog.Logger, opts Options) *Room {
	if opts.ID == "" {
		opts.ID = "room-1"
	}
	if log == nil {
		log = slog.Default()
	}

	r := &Room{
		ID:           opts.ID,
		World:        world.NewWorld(cfg, opts.Seed),
		cfg:          cfg,
		log:          log.With("room", opts.ID),
		seed:         opts.Seed,
		tickInterval: cfg.TickInterval(),
		subSteps:     cfg.Server.SubSteps,
		subDt:        cfg.SubDt(),
		joins:        make(chan JoinRequest, MaxSlots*2),
		inputs:       make(chan peerInput, MaxSlots*32),
		leaves:       make(chan leaveRequest, MaxSlots),
		choices:      make(chan choiceRequest, MaxSlots*4),
		interacts:    make(chan interactRequest, MaxSlots*4),
		shopBuys:     make(chan shopBuyRequest, MaxSlots*4),
		reliable:     make(chan ReliableMessage, MaxSlots*8),
		done:         make(chan struct{}),
		startedAt:    time.Now().Unix(),
	}
	r.World.Grid.SetBruteThreshold(cfg.Space.SpatialBruteThreshold)

	// 鬼魂拴绳半径（M5，spec L49「半径可配置」）。与 AggroRescoreTicks
	// 同款：配置在装配期拷进 World，模拟路径不读 cfg——systems 层不该
	// 知道配置结构，测试也能直接改 World 字段摆场景。
	r.World.GhostTether = cfg.Ghost.TetherRadius

	// 生成灰盒第一关（NewWorld 不能自己调 mapgen：会构成 import cycle）。
	// 失败按 prompt 裁定降级：记警告并保持 HasMap=false，不 panic——
	// Generate 的 error 路径已把 Terrain 恢复成全空地，世界回落到 M2
	// 行为（波次/AI/迷雾全部短路），对局照常可玩，启动不该被一张图带崩。
	// HubCount==0 的哨兵世界（测试用）走 Generate 内部哨兵分支，同样全空地。
	if _, err := mapgen.Generate(r.World, cfg); err != nil {
		log.Warn("mapgen: 地图生成失败，降级为无图世界", "room", opts.ID, "err", err)
	}

	// 迷雾状态与地形格数绑定（fog 格与 Terrain 格同尺寸）。只在开图房间
	// 建立：降级世界（HasMap=false）没有墙也没有据点，迷雾短路为不下发。
	// 格数用 Terrain.Width()/Height()（格数，非米）——fog.New 的 w/h 单位是格。
	if r.World.HasMap {
		r.fog = fog.New(r.World.Terrain.Width(), r.World.Terrain.Height())
	}

	// 波次与 DummyTargets 互斥：DummyTargets 优先（bot 压测路径不想要
	// 波次怪的干扰），两者同给时禁用波次并告警，不报错——配置矛盾不该
	// 让对局起不来。
	waves := opts.WavesEnabled && opts.DummyTargets == 0
	if opts.DummyTargets > 0 && opts.WavesEnabled {
		// r.log 自带 room 标识（:120 With("room")），与 :138 的 mapgen
		// 告警同模式——多房间日志可归组。
		r.log.Warn("-dummy-targets 与波次互斥，本次禁用波次")
	}
	r.wavesEnabled = waves

	if opts.DummyTargets > 0 {
		spawnDummyTargets(r.World, opts.DummyTargets)
	}
	return r
}

// spawnDummyTargets 在场地中心周围摆一圈静止怪，供 M2 端到端验证。
//
// 排成圆环而不是随机撒：随机撒可能撒到玩家出生点所在的 2×2 方阵里，
// 于是"打中脚边的怪"与"打中远处的怪"在日志上看起来一样，查不出
// 是射程问题还是碰撞问题。
//
// 半径 4.5 m：据点空地圆是 5 m（HubRadiusCells=5），M2 的 6 m 靶环在
// 挂到据点中心后会越出空地圆嵌墙（审查 Minor-4）；4.5 m 留 0.5 m 余量，
// 仍在方阵外圈（方阵间距 3 m）与步枪射程内。
func spawnDummyTargets(w *world.World, n int) {
	const ringRadius = 4.5
	// HasMap 时靶环挂在出生据点上：图中心大概率是墙，靶子会嵌墙
	// （审查 Important-4 同源问题）。
	cx, cy := w.Terrain.WorldWidth()/2, w.Terrain.WorldHeight()/2
	if w.HasMap {
		c := w.Hubs[w.SpawnHub].Center
		cx, cy = c.X, c.Y
	}

	for i := 0; i < n; i++ {
		ang := 2 * math.Pi * float64(i) / float64(n)
		_, m := w.Monsters.Spawn()
		if m == nil {
			return // 怪物池满
		}
		m.Pos = world.V(cx+math.Cos(ang)*ringRadius, cy+math.Sin(ang)*ringRadius)
		m.HP = 50
		m.MaxHP = 50
		m.Radius = 0.4
		m.TypeID = 1
	}
}

// ErrAlreadyRunning 表示已有 tick 循环在跑。
var ErrAlreadyRunning = errors.New("room: tick 循环已在运行")

// Run 启动固定步长 tick 循环，阻塞直到 ctx 取消或 Stop 被调用。
//
// 这里用的是最朴素的"睡到下一个整点"调度，不是"睡固定时长"——后者会让实际
// 频率随 tick 耗时不断漂移（跑 3ms 睡 47ms = 47Hz 变 50ms+3ms 一颗），累积
// 几分钟后快照时间戳就和真实时间对不上了。用 deadline 累加保证长期平均频率精确。
func (r *Room) Run(ctx context.Context) {
	next := time.Now()
	interval := r.tickInterval

	r.log.Info("房间启动",
		"tick_rate", r.cfg.Server.TickRate,
		"sub_steps", r.subSteps,
		"seed", r.seed,
		"world", r.World.Terrain.WorldWidth(),
	)

	for {
		select {
		case <-ctx.Done():
			r.shutdown("ctx done")
			return
		case <-r.done:
			r.shutdown("stop called")
			return
		default:
		}

		// 推进一个 tick。
		start := time.Now()
		r.tick()
		dur := time.Since(start)
		r.lastTickDur.Store(int64(dur))
		r.ticksRun.Add(1)
		// 采样本 tick 耗时。放在 tick() 返回之后而不是内部某个位置：
		// 要计的是"这个 tick 的全部工作"，包括广播。
		r.sampleTick(dur)

		// 累加 deadline 而不是 time.Now().Add：让长期平均频率锁定在 tickInterval，
		// 单次 tick 的抖动不会累积。
		next = next.Add(interval)

		// 若已经落后超过一个 tick，说明这一轮真的跑不动了。此时不能"追帧"——
		// 追帧会让物理正确（时间对齐）但表现更糟（一帧算两次位置的突变）。
		// 正确做法是记录 overrun 并把 deadline 拉回当前时间，承认漏了时间。
		now := time.Now()
		if now.Sub(next) > interval {
			r.overrun.Add(1)
			if r.log.Enabled(ctx, slog.LevelWarn) {
				r.log.Warn("tick 超时，丢弃落后时间",
					"tick", r.World.Tick,
					"dur_ms", float64(dur.Microseconds())/1000,
					"budget_ms", float64(interval.Microseconds())/1000,
					"overrun_total", r.overrun.Load(),
				)
			}
			next = now
		}

		if wait := time.Until(next); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				r.shutdown("ctx done during wait")
				return
			case <-r.done:
				timer.Stop()
				r.shutdown("stop called during wait")
				return
			}
		}
	}
}

// shutdown 清理所有玩家并记录停止原因。
//
// 必须给每个还在座上的玩家关掉 sink：网络层的写 goroutine 靠它退出，
// 不关就会泄漏一个 goroutine。
func (r *Room) shutdown(reason string) {
	r.stopped.Store(true)
	// 上报必须在清 peers 之前：reportMatch 要按实体 id 反查座位号，
	// 而 peers.slots 在下面几行就被清空。
	r.reportMatch()
	r.peers.each(func(p *peer) {
		close(p.sink)
	})
	for i := 0; i < MaxSlots; i++ {
		r.peers.slots[i] = nil
	}
	r.peers.count = 0
	r.peerCount.Store(0)
	r.log.Info("房间停止", "reason", reason, "ticks", r.ticksRun.Load())
}

// SetResultSink 设置结果接收方（M7）。建议在 Run 之前调用。
//
// 不检查"是否已在运行"：设置一个字段本身不是并发危险，而在 Run 之后
// 设置也只是让前几 tick 的判定看不到 sink——语义上仍安全（结局只在上报
// 那一刻读取，而上报发生在 shutdown），所以这里只做文档约束。
func (r *Room) SetResultSink(s ResultSink) {
	r.resultSink = s
}

// updateOutcome 每 tick 判定一次本局终态（M7）。
//
// 只在"从非终态进入终态"的那一刻记日志：终态是粘性的（一旦通关/团灭
// 就不再变），每 tick 打一条会把日志刷满。而 outcome 字段本身允许被
// 覆写——CheckOutcome 是纯函数，它的优先级规则（团灭 > 通关）由它自己
// 保证，这里不做二次裁决。
func (r *Room) updateOutcome() {
	if r.outcome != world.OutcomeUnknown {
		return // 已进终态，不再刷日志
	}
	got := systems.CheckOutcome(r.World)
	if got == world.OutcomeUnknown {
		return
	}
	r.outcome = got
	rec := r.World.SnapshotMatch(r.ID, got, r.startedAt, time.Now().Unix())
	r.log.Info("本局结束",
		"tick", r.World.Tick,
		"outcome", got.String(),
		"max_wave", rec.MaxWave,
		"players", len(rec.Players),
	)
	// 下发一次结算（可靠通道）。只发一次而不是每帧带：结果消息不是
	// 状态同步的一部分（客户端拿到就不再看），而每帧带要额外占快照
	// 的字段号（Snapshot 的 1-10 已满）并让不可靠通道承担"必达"语义。
	//
	// 只发给当前在座的玩家：结局那一刻已经离开的人，其战报随
	// shutdown 的落盘走（§11.1 的 player_result 表），不需要协议同步。
	r.broadcastMatchResult(rec)
}

// broadcastMatchResult 把结算消息投给每个在座玩家（M7）。
//
// 座位号在这里替换：world 层只能填池索引，而 §11.1 的 player_result.slot
// 要的是 0..MaxSlots-1 的座位号——座位号在 peers 里，只有 room 知道。
// updateOutcome 里做的替换与这里同源（都在 tick goroutine 里），
// 但这里要再算一份 proto：落盘那份是 MatchResult，下发那份是
// *proto.MatchResult，两者不共享内存。
func (r *Room) broadcastMatchResult(rec world.MatchResult) {
	for i := range rec.Players {
		if p := r.peerByID(rec.Players[i].Player); p != nil {
			rec.Players[i].Slot = p.slot
		}
	}
	env := &proto.Envelope{Body: &proto.Envelope_MatchResult{MatchResult: snapshot.MatchResultToProto(rec)}}
	r.peers.each(func(p *peer) {
		r.sendReliable(p.slot, env)
	})
}

// reportMatch 把本局结果发给 sink（关房时调用一次）。
func (r *Room) reportMatch() {
	if r.resultSink == nil || !r.reported.CompareAndSwap(false, true) {
		return
	}
	rec := r.World.SnapshotMatch(r.ID, r.outcome, r.startedAt, time.Now().Unix())
	// 座位号替换池索引：world 层只能填 id.Index()，而 §11.1 的
	// player_result.slot 要的是 0..MaxSlots-1 的座位号——座位号在 peers
	// 里，只有 room 知道。
	for i := range rec.Players {
		if p := r.peerByID(rec.Players[i].Player); p != nil {
			rec.Players[i].Slot = p.slot
		}
	}
	r.resultSink.Report(rec)
	r.log.Info("本局结果已上报",
		"outcome", rec.Outcome.String(),
		"duration_sec", rec.DurationSec,
		"players", len(rec.Players),
	)
}

// Stop 请求停止 tick 循环。可安全重复调用。
func (r *Room) Stop() {
	if r.stopped.Load() {
		return
	}
	select {
	case <-r.done:
		// 已经关过。
	default:
		close(r.done)
	}
}

// tick 推进一个逻辑 tick。
//
// 顺序即游戏逻辑（docs/combat-design-v0.1.md §1.1）。M1 阶段的形状：
// 处理加入/离开 → 应用输入 → 重建空间索引 → 跑子步 → 推进 tick 计数。
// 各系统在 M2~M6 逐步填进子步里，但这个循环的形状从第一天就定死。
func (r *Room) tick() {
	w := r.World

	// 0. 网络请求。必须在模拟之前处理，这样新玩家第一帧就有实体、
	//    离开的玩家不会在最后一帧还收到快照。
	//    events 用 [:0] 清空而不是 = nil：前者复用底层数组，后者每 tick
	//    分配一次，20Hz 下就是每秒 20 次无谓分配。
	r.events = r.events[:0]
	r.miscEvents = r.miscEvents[:0]
	r.pendingLevelUps = r.pendingLevelUps[:0]
	r.drainJoins()
	r.drainLeaves()
	r.drainInputs()
	r.drainChoices()
	r.drainInteracts()
	r.drainShopBuys()

	// 1. 重建空间索引（每 tick 一次，不是每子步）。
	w.Grid.Rebuild(w.Monsters, w.Bullets, w.Players)

	// 1.05 距离惩罚分级（M6 Task 11，spec L59-62）：全队级别，每 tick 一次。
	//
	// 必须放在 AI 之前——AI 读 w.Spread / 怪物标志位决定是否分裂、狂暴，
	// 排在后面会让惩罚永远晚一个 tick 生效（玩家分开后第一 tick 的怪
	// 按旧等级行动）。
	//
	// 每 tick 都扫一遍怪物写标志，而不是「只在等级变化时写」：后者要多存
	// 一个 prev，就多了一个可能与 w.Spread 不同步的状态。128 只怪的标志
	// 位操作是微秒级，灰盒阶段选简单。
	systems.UpdatePartySpread(w)
	systems.ApplySpreadToMonsters(w)

	// 1.2 鬼魂：拴绳 → 复活 → 团灭（M5 Task 11）。
	//
	// 顺序是语义，不是随手排的：
	//   - 拴绳必须早于复活：拴绳可能把鬼魂拉出祭坛范围，而复活判定要看
	//     「最终位置」。反过来的话，被拴住的鬼魂会在坛外持续引导——先判
	//     复活通过→再被拉走→下一 tick 又通过，引导永远在"通过但没到点"
	//     之间循环，表现是复活进度条走满却永远不复活。
	//   - 团灭放在最后：它要读"这一 tick 处理完之后"的真实状态，早于复活
	//     会把"正要复活的人"当成鬼魂报一次团灭。
	//
	// 三件都在 tick 级而非子步级：ReviveAt 是 tick 号（进子步会被同一 tick
	// 判定三次，引导实际时长缩成三分之一）；团灭上报是"每 tick 至多一次"
	// 的语义（参 CheckTeamWipe 的返回值）。
	r.tickTethers()
	r.tickRevives()
	r.tickTeamWipe()

	// 1.5 波次（M3 Task 10）：每 tick 一次，在子步之外——与迷雾 Reveal、
	// fire 同款"世界级结算"节拍。刷怪走 w.RandFloat64 包装，RNG 调用计数
	// 完整、回放一致。UpdateWaves 自带防御（Wav/Terrain/Hubs 未装配时
	// no-op），但 wavesEnabled=false 的 M2 房间连函数都不该进：降级世界
	// 没有据点，短路语义与 fog（r.fog==nil）保持一致。
	if r.wavesEnabled {
		// 顺序是语义：先记旧值 → 推进状态机 → 比出新旧差异打日志。
		// 反过来的话比较的两侧都是新值，差异恒为空，日志永远不出现——
		// 而"日志不出现"与"这一 tick 真的没开波"无法区分，正是这次要
		// 修掉的"注释承诺了但没人验证"。
		r.recordHubStates()
		systems.UpdateWaves(r.World)
		r.logHubChanges()
		// 分账必须在 UpdateWaves 之后：HubCleared 是它写进去的。
		r.settleClearedHubs()
		// 商店开关也必须在分账之后：客户端收到的 ShopRack.gold 要包含
		// 刚分到的那笔钱，否则「买得起」的东西显示成买不起。
		r.updateShopOpen()
		r.updateShopClose()
		// Boss 刷新也挂在波次进度上：spec L94「每关 5~8 波 + Boss」，
		// 没有波次就没有「全部据点清完」这个状态。
		r.spawnBossIfReady()
	}

	// 1.7 Boss 阶段推进：必须在 AI 之前——AI 的 chaseSpeed 与攻击 switch
	//     读 MonsterFlagEnraged 决定速度与伤害，晚于 AI 会让切换那一 tick
	//     白跑（Boss 已经该加速了却按一阶段速度走了 50ms）。
	//     也必须在 UpdateWaves 之后：Boss 只可能在据点清完后才存在。
	if r.wavesEnabled {
		systems.UpdateBossPhase(r.World)
		r.trackBossPhase()
	}

	// 2. AI 决策（M3 Task 10）：每 tick 一次，循环放在子步循环之外、
	//    fire 之前。这是硬要求 2 的契约，不是实现细节：
	//    UpdateMonster 的计时器（AggroRescoreAt/PathRecalcAt 按
	//    w.AggroRescoreTicks/w.PathRecalcTicks 判界、AttackAt 按 w.Tick
	//    判冷却）都按"调用次数 == tick 数"自增——错接到子步里，SubSteps=3
	//    时计时器 3 倍速跑，rescore/重路径/攻击频率全部失真（计划正文把
	//    AI 写进 subStep，Lead 已裁定以硬要求 2 为准）。dt 传子步步长
	//    r.subDt，只用于沿路径的移动距离积分，与计时器无关。
	//    HasMap 短路：无图世界没有据点也没有墙，AI 的寻路/视线无意义；
	//    w.Mon 理论上恒非 nil（NewWorld 装配），防御判空保持与 ai 包
	//    RescoreAggro 的防御口径一致。
	if r.World.HasMap && w.Mon != nil {
		w.Monsters.Each(func(_ world.EntityID, m *world.MonsterState) bool {
			ai.UpdateMonster(w, m, r.subDt)
			return true
		})
	}

	// 2.1 鬼魂搬运（M5 Task 11）：先捡起、再交接。
	//
	// 顺序是捡起在前：刚掉的掉落物与刚走到旁边的鬼魂在同一 tick 内都该
	// 生效，反过来的话玩家会看到"东西在旁边但要多等一帧才被拎起来"。
	// 放在 AI 之后、子步之前——搬运只改 Carrier 一个字段，与移动积分
	// 无关，但必须在 UpdatePickups（3.06，会按 Carrier 判可拾取性）之前。
	r.tickGhostCarry()

	// 2~3. 子步内：移动 → 碰撞 → 伤害 → 死亡清理
	for sub := 0; sub < r.subSteps; sub++ {
		r.subStep(sub)
	}

	// 3.0 限时效果递减（M4 Task 5 接线）：每 tick 恰好一次，与波次/AI 同款
	//     "世界级结算"节拍。dt 用 tick 的固定时长而不是真实耗时，理由同
	//     fire()。放子步之外是硬要求：TickBuffs 递减的是秒数倒计时与技能
	//     冷却，接进子步会让 SubSteps=3 时以三倍速走完，表现为"技能冷却
	//     只要三分之一时间"——手感问题，不会报错，极难定位。
	r.tickBuffs()

	// 3.05 开背包引导推进（M4 Task 10）：每 tick 一次，与 tickBuffs 同款
	//      理由（引导的终点是一个 tick 号，接进子步会被判定三次）。
	//      放在受击打断之后的自然位置：本 tick 子步里挨打的玩家，Busy 位
	//      已在 resolvePlayerDamage 里被清掉，这一行就不会再把引导推完。
	r.tickInventory()

	// 3.06 掉落物结算（M4 Task 9 接线）：每 tick 一次——这不是选择，
	//      UpdatePickups 内部用 pickupDeltaSeconds(w) 扣寿命，那个 dt 是
	//      **一个 tick** 的时长（1/TickRate）。放进子步循环会以 SubSteps=3
	//      倍速消耗寿命，症状是"地上掉落物总是早三分之一时间消失"，而
	//      配置里写的 30 秒看起来完全正确。
	//      相对玩家位置：必须排在子步移动之后，否则用的是上一 tick 的位置，
	//      高速移动时会出现"明明跑过去了却没捡到"。
	//      返回的事件结构同 DamageEvent（Dst=消失的 ID、Amount=0），直接
	//      追加进 r.events，与伤害事件走同一条上报路径。
	if evs := systems.UpdatePickups(r.World); len(evs) > 0 {
		r.events = append(r.events, evs...)
	}

	// 3.1 升级结算：本 tick 子步里升级的玩家在这里统一发候选、开无敌窗口。
	//     攒到 tick 级处理而不是在 subStep 里当场做，理由见 pendingLevelUps
	//     字段注释（子步高频，会重复开窗、重复下发）。
	r.handleLevelUps(r.pendingLevelUps)

	// 3.2 选择超时：窗口到期自动随机装一个。放在升级结算之后，本 tick 刚
	//     开的窗口 deadline 至少是 Tick+1，不会被这一行的判定误伤。
	r.tickSkillChoiceTimeouts()

	// 3. 生成：开火属于生成，放在子步循环之外。
	// 理由见 combat-design-v0.1.md:77——发射频率以 tick 为单位，
	// 放子步里会让一 tick 生成三倍子弹（§1.2 反例 4）。
	r.fire()

	// 3.5 终局判定（M7）。放 fire 之后：本 tick 的击杀已经结算完，
	// 判定读到的才是最新状态。放在 broadcast 之前，让客户端从本 tick
	// 的快照里就能看到"这一局结束了"。
	r.updateOutcome()

	// 4. 世界级结算：M2 只有事件累积（r.events 已在子步里攒好）
	// 5. 迷雾增量：Reveal 返回内部复用切片，必须立刻拷进 r.fogDelta——
	//    下一次 Reveal 会覆写它。先算后广播（Reveal 用的是本 tick 子步末
	//    的玩家位置），顺序固定保证回放一致。空增量置 nil，快照不带
	//    fog_delta 字段，省一次 varint 长度前缀与带宽。
	if r.fog != nil {
		r.fogDelta = r.fogDelta[:0]
		if inc := r.fog.Reveal(r.World, r.cfg.Fog.RevealRadius); len(inc) > 0 {
			for _, idx := range inc {
				r.fogDelta = append(r.fogDelta, uint64(idx))
			}
		}
	} else {
		r.fogDelta = nil
	}

	// 6. 编码快照
	r.broadcast()

	w.Tick++
	r.worldTick.Store(w.Tick)
}

// recordHubStates 把当前各据点的波次状态记进 hubStates，供本 tick 之后比较。
//
// 必须紧贴 UpdateWaves 之前调用：它记录的是"推进前"的值。
func (r *Room) recordHubStates() {
	hubs := r.World.Hubs
	// 按需重建：据点数量由地图生成决定，Run 之前就固定了，但 recordHubStates
	// 是第一个写 hubStates 的地方，这里顺手做长度对齐可以省掉一个
	// "New 里 initialize"的隐式契约（忘了初始化 → 长度 0 → 永远检测不到变化）。
	if cap(r.hubStates) < len(hubs) {
		r.hubStates = make([]world.HubState, len(hubs))
	}
	r.hubStates = r.hubStates[:len(hubs)]
	for i := range hubs {
		r.hubStates[i] = hubs[i].State
	}
}

// logHubChanges 比较 hubStates 与当前据点状态，把「新开波」与「清场」打出来。
//
// 为什么在 room 而不是 waves 里打日志：internal/game/** 不许依赖网络/房间
// 包，也不该为一个日志引入 log 依赖；UpdateWaves 的签名已定稿为无返回值
// （返回事件会让 game 包依赖一套尚未定论的 HubEvent 结构，M6 据点经济才定）。
// 在 tick 里 diff 状态是零侵入的做法：waves.go 一行不用改。
func (r *Room) logHubChanges() {
	for i := range r.World.Hubs {
		hub := &r.World.Hubs[i]
		if i >= len(r.hubStates) || r.hubStates[i] == hub.State {
			continue
		}
		prev := r.hubStates[i]
		switch hub.State {
		case world.HubFighting:
			// 只有从非 Fighting 进入 Fighting 才是"新开波"；Fighting → Fighting
			// 是刷下一波（上面的相等判断已挡掉），不重复打。
			r.log.Info("据点开波",
				"hub", i, "wave", hub.Wave,
				"monsters", hub.WaveN, "prev", prev.String())
		case world.HubCleared:
			r.log.Info("据点清场",
				"hub", i, "wave", hub.Wave, "prev", prev.String())
		}
	}
}

// subStep 推进一个物理子步。
//
// 保留这个函数而不是把循环写死，是因为后面每个子步要按固定顺序调用六个
// 系统，写死顺序会让 diff 变得很难读。
func (r *Room) subStep(sub int) {
	w := r.World
	dt := r.subDt

	// 2.1 应用输入：drainInputs 已在 tick 开头把意图写进实体。
	// 输入不在这里读——它按 tick 到达，而这里是 60Hz，重复应用会把
	// 消耗型逻辑扣三倍（combat-design-v0.1.md §1.2 反例 5）。

	// 2.2 AI 决策：M2 不做，怪物是静止靶（M3 补）。

	// 2.3 移动积分
	w.Players.Each(func(_ world.EntityID, p *world.PlayerState) bool {
		systems.Integrate(p, dt, w.Terrain)
		return true
	})
	systems.IntegrateBullets(w, dt)

	// 2.4 碰撞与命中：只产出命中列表，不改任何血量。
	// 分两步是为了让"谁该掉血"与"实际掉多少"各自可测——合在一起时，
	// 判定写错与伤害公式写错的表现完全一样。
	hits := systems.Collide(w)

	// 2.5 伤害结算：唯一入口，死亡只标记不回收。
	// 增量切片必须单独持有：applyAggro 只喂本子步的增量——
	// r.events 是全 tick 累积流（fog/broadcast 还要读），传累积流会让
	// 同一条伤害事件每子步重放回写（SubSteps=3 时一条 10 点伤害被记成
	// 10/25/45，权重放大且越早的伤害被重放越多，仇恨时序反转）。
	inc := systems.ApplyHits(w, hits)
	r.events = append(r.events, inc...)
	r.applyAggro(inc)

	// 2.5.5 击杀经验：必须在 Reap 之前——Reap 会 Kill 槽位并清零数据，
	//       之后 w.Monsters.Get(dst) 就返回 nil，拿不到死亡位置；而
	//       GrantKillExp 要按死亡位置算 20 m 分账半径。Reap 自己产出的
	//       击杀事件也不带位置（Src=InvalidID、只有 Dst），拿不到。
	//       所以扫 ApplyHits 的增量里的 IsKill（那是"这一发把它打死了"），
	//       在回收之前取 Pos。中毒/环境伤害这类非 ApplyHits 来源的击杀
	//       在 M4 还没有，缺口记在汇报的已知限制里。
	r.grantKillExp(inc)

	// 2.5.6 击杀金币（M6 Task 9）：与经验同一位置，必须在 Reap 前——
	//       金币归属靠 ev.Src，而 Reap 之后 ev.Dst 的槽位已清，位置也拿不到。
	//       M6 起不再是直给玩家，而是记进据点池子等着清完分账。
	r.creditHubKills(inc)

	// 2.5.7 掉落（M5 Task 6）：同样必须在 Reap 前取死亡位置。
	r.spawnLoot(inc)

	// 2.6 死亡清理：回收 + 世代 +1
	r.events = append(r.events, systems.Reap(w)...)
}

// applyAggro 把本子步伤害事件回写进怪物的仇恨表（硬要求 1）。
//
// ApplyHits 不改怪物状态（见 subStep 2.5 注释），而仇恨三路打分的
// 0.6 权重「近期伤害」路（ai.RescoreAggro 读 m.AggroDmg）没有任何系统
// 会写——缺了这一步，打分静默退化成两路（审查 I1：全仓库无写入者）。
// 回写放 room 接线层而不是 systems.ApplyHits：不改 systems.ApplyHits
// 签名（它的产出是事件流，改它等于让伤害结算做怪物状态写）。
// 契约：每条事件恰好回写一次——调用方必须传增量（本子步 ApplyHits
// 的返回值），不能传累积流 r.events（见 subStep 2.5 注释）。
//
// 界检查：怪物池槽位下标上界是 capacity+1（第 4 个玩家 Index()==4），
// AggroDmg 是 [5]float64——写之前判 len，防未来池容量改动越界 panic。
// 尸体（Reap 已 Kill）的 Get 返回 nil，天然跳过。
func (r *Room) applyAggro(evs []world.DamageEvent) {
	if len(evs) == 0 {
		return
	}
	w := r.World
	for _, ev := range evs {
		if !ev.Src.IsValid() {
			continue // 环境伤害/近战（Src=InvalidID）不记仇恨
		}
		m := w.Monsters.Get(ev.Dst)
		if m == nil {
			continue // 目标不是怪或已死
		}
		if i := int(ev.Src.Index()); i < len(m.AggroDmg) {
			m.AggroDmg[i] += ev.Amount
		}
	}
}

// grantKillExp 给击杀点附近的玩家分经验，把升级的玩家攒进 pendingLevelUps。
//
// 为什么挂在 ApplyHits 的增量上、而不是 Reap 的击杀事件上：Reap 会先 Kill
// 槽位再产出事件，那一瞬间怪的 Pos 已被清零、Get 也返回 nil，分账需要的
// 「死亡位置」就永久丢了；而 Reap 自己的事件只有 Dst、没有位置字段，改它
// 要动 systems 的签名。所以取 ApplyHits 里「这一发把它打死了」的那条事件
// （Amount>0 且 IsKill），在 Reap 之前查 Pos。
//
// 为什么只认 IsKill：同一 tick 多颗子弹命中同一只怪时，第一发把
// MonsterFlagDead 置上，后续几发在 ResolveDamage 里走「尸体不再吃伤害」
// 分支、Amount 保持 0 而被 ApplyHits 过滤掉——所以一条怪只会有一条 IsKill
// 事件，不会重复分账。
func (r *Room) grantKillExp(inc []world.DamageEvent) {
	if len(inc) == 0 {
		return
	}
	baseExp := r.cfg.Progression.KillExp
	if baseExp == 0 {
		// 配成 0 等于关掉击杀经验（关卡编辑器/调试用），此时连分账半径
		// 都不用算。
		return
	}
	w := r.World
	for _, ev := range inc {
		if !ev.IsKill {
			continue
		}
		m := w.Monsters.Get(ev.Dst)
		if m == nil {
			continue // 目标不是怪，或已被别的路径回收
		}
		r.pendingLevelUps = append(r.pendingLevelUps,
			systems.GrantKillExp(w, m.Pos, ev.Src, baseExp)...)
	}
}

// tickBuffs 递减所有玩家的限时效果与技能冷却。
//
// 在 tick 里调一次（不是子步）：见 tick() 3.0 的注释。
func (r *Room) tickBuffs() {
	dt := 1.0 / float64(r.cfg.Server.TickRate)
	r.World.Players.Each(func(_ world.EntityID, p *world.PlayerState) bool {
		systems.TickBuffs(p, dt)
		return true
	})
}

// choiceWindowTicks 把配置里的窗口秒数换成 tick 数。
//
// 口径与 world.secondsToTicks 一致（+0.5 四舍五入、至少 1）：窗口在 tick
// 循环里按 tick 数判定，配置却以秒书写，两套单位的换算只能有一处。
// 下限 1 是因为 0 会让 deadline 等于当前 tick，同一 tick 的下一行就超时，
// 玩家永远看不到候选。
func (r *Room) choiceWindowTicks() uint32 {
	n := uint32(r.cfg.Progression.SkillChoiceWindowS*float64(r.cfg.Server.TickRate) + 0.5)
	if n == 0 {
		n = 1
	}
	return n
}

// handleLevelUps 给刚升级的玩家发三选一候选，并开无敌窗口。
//
// 无敌用 PlayerFlagInvulnerable 而不是 InvulnLeft，理由是语义而非偏好：
// world/damage.go 的判定注释已经把两条路径分好工——InvulnLeft 是
// 「技能给的限时无敌（秒）」，Flags 里的位是「复活/出生保护这类由房间
// 逻辑给的状态」。选择窗口是房间规则（停下来做选择不该被怪打死），
// 不是玩家拥有的技能，所以走 Flag。
//
// 第二条理由是实现层面的：InvulnLeft 由 systems.TickBuffs 每 tick 递减，
// 窗口的结束时刻就取决于「递减了多少次」，而这个数字在窗口被重置（窗口
// 内又升一级）时不好对齐；Flag 由房间在窗口结束那一刻显式清除，窗口长度
// 与 choiceDeadline 是同一个 tick 基准，不会出现「显示还有 1 秒，实际已经
// 不无敌」的错位。
//
// 两个动作都改世界状态，所以只允许在 tick goroutine 里调用。
func (r *Room) handleLevelUps(leveled []world.EntityID) {
	if len(leveled) == 0 {
		return
	}
	w := r.World
	window := r.choiceWindowTicks()

	// 去重：同一 tick 打死两只怪时 GrantKillExp 会为同一个玩家返回两次
	// （每次击杀各返回一次）。不去重就会连开两次窗口、连发两条候选，
	// 玩家看到两份列表、索引空间还互相打架。
	//
	// 用切片线性查重而不用 map：玩家最多 4 个，代价可忽略；而 map 的
	// 遍历顺序随机，一旦有人日后在这里改成「遍历去重结果」就会让模拟
	// 不可复现（AGENTS.md §2.6 禁止在模拟路径里遍历 map）。这里不遍历
	// map，但省掉它更省心。
	var done []world.EntityID
	for _, id := range leveled {
		dup := false
		for _, seen := range done {
			if seen == id {
				dup = true
				break
			}
		}
		if !dup {
			done = append(done, id)
		}
	}

	for _, id := range done {
		p := r.peerByID(id)
		if p == nil {
			continue // 实体活着但没有对应座位（M5 的鬼魂会这样）
		}
		ent := w.Players.Get(id)
		if ent == nil || ent.State != world.PlayerAlive {
			continue
		}

		choices := build.GenerateChoices(w, assignedSkillIDs(ent, p), 3)
		if len(choices) == 0 {
			// 技能池耗尽（8 条全装上了）：如实跳过，不开窗口。开一个
			// 没有候选的窗口只会让玩家白等 5 秒无敌，且客户端的列表
			// 是空的、点了也没反应。数值奖励留给 M5 的拾取物，这里
			// 不临时发明一套。
			r.log.Debug("技能池已耗尽，本次升级不发候选",
				"slot", p.slot, "level", ent.Level)
			continue
		}

		// 窗口内又升级：追加到现有候选并重置窗口，而不是丢掉这一次。
		// 丢掉意味着玩家多打了一只怪、多升了一级，却什么也没得到。
		p.pendingChoices = append(p.pendingChoices, choices...)
		p.choiceDeadline = w.Tick + window
		p.awaitingChoice = true

		ent.Flags |= world.PlayerFlagInvulnerable
		// 升级会改 Level/MaxHP，属性重算的统一入口是 RecomputeDerived
		// （Task 5 的账）：AddExp 里换了一种算法加 HP 上限，两条路径
		// 数值一致但必须由它收口，否则日后加新的被动就会漏乘。
		systems.RecomputeDerived(ent)

		r.sendSkillChoices(p, window)
	}
}

// peerByID 按实体 ID 找座位。
func (r *Room) peerByID(id world.EntityID) *peer {
	var found *peer
	r.peers.each(func(p *peer) {
		if p.id == id {
			found = p
		}
	})
	return found
}

// tickSkillChoiceTimeouts 处理选择窗口超时：随机装一个候选。
//
// 超时必须有兜底，理由不是"玩家懒得选"：候选走可靠通道下发，但客户端可能
// 崩了、可能一直不选、可能根本没收到。没有超时的话玩家会永久停在无敌状态
// 且永远不再成长——一个既无敌又不成长的玩家会破坏整局平衡，而症状（某个
// 人不掉血也不升级）看起来像网络问题，很难联想到是"没做超时"。
func (r *Room) tickSkillChoiceTimeouts() {
	w := r.World
	r.peers.each(func(p *peer) {
		if !p.awaitingChoice {
			return
		}
		// 回绕安全的比较，口径与 cmd/server/bridge.go 的 tickAfter 一致：
		// uint32 直接比大小会在 2^32 tick（20Hz 下约 6.8 年）后失效，
		// 而 int32 差值法在整个区间上单调可用。
		if int32(w.Tick-p.choiceDeadline) < 0 {
			return
		}
		n := len(p.pendingChoices)
		if n == 0 {
			// 只会出现在「窗口被外部清空但没清标记」的异常态。兜底把
			// 窗口关掉，避免永久无敌。
			r.clearChoiceWindow(p)
			return
		}
		idx := 0
		if n > 1 {
			idx = w.RandIntn(n)
		}
		if err := r.applyChoiceLocked(p, idx); err != nil {
			r.log.Warn("技能选择超时兜底失败", "slot", p.slot, "err", err)
			r.clearChoiceWindow(p)
		}
	})
}

// applyChoiceLocked 把第 index 个候选装进玩家的技能槽。
//
// 只能在 tick goroutine 里调用（它改 PlayerState）。
func (r *Room) applyChoiceLocked(p *peer, index int) error {
	if !p.awaitingChoice {
		return ErrNotChoosing
	}
	if index < 0 || index >= len(p.pendingChoices) {
		return ErrChoiceOutOfRange
	}
	ent := r.World.Players.Get(p.id)
	if ent == nil {
		return errNoPeer
	}

	s := p.pendingChoices[index]
	// 装槽与重算派生都由 systems.TryEquipSkill 收口（M4 Task 10 统一）：
	// 它按技能种类选槽组、装第一个空槽、两槽满替换[1]，**并且**在返回前
	// 调一次 RecomputeDerived。所以这里不能再跟一行 RecomputeDerived——
	// 那不是"多算一次也无害"，而是重复调用的地方一旦日后被改成增量更新
	// （像 SpeedBuffMul 那样按次加减），重复就会翻倍。装上新被动必须
	// 立刻生效这一点由 TryEquipSkill 自己的契约保证（pickup.go 的注释写明
	// 捡技能与这里共用同一语义，两条路径不能有两种生效时机）。
	systems.TryEquipSkill(ent, s.ID)

	r.clearChoiceWindow(p)
	r.log.Debug("技能选择已应用",
		"slot", p.slot, "skill_id", s.ID, "name", s.Name, "kind", s.Kind.String())
	return nil
}

// clearChoiceWindow 关闭选择窗口并撤掉无敌。
//
// 三件事必须一起做：候选列表（索引空间来源）、awaitingChoice（超时判定
// 与再次升级的分支）、无敌标记。少任何一件都会留下不一致状态——比如只清
// 候选不清标记，玩家会永久无敌且超时逻辑每 tick 都撞进「长度为 0」的兜底
// 分支刷日志。
func (r *Room) clearChoiceWindow(p *peer) {
	p.pendingChoices = nil
	p.awaitingChoice = false
	p.choiceDeadline = 0
	if ent := r.World.Players.Get(p.id); ent != nil {
		// 只撤窗口自己给的那一位。技能的无敌走 InvulnLeft，是另一个
		// 字段，不会被这里误清。
		ent.Flags &^= world.PlayerFlagInvulnerable
	}
}

// assignedSkillIDs 收集玩家已占用的技能 ID：槽位里的 + 还在候选里没选的。
//
// 把 pendingChoices 也算进去，是为了让「窗口内又升一级」追加候选时不
// 重复下发同一个技能：否则两批候选里都有技能 X，玩家选了两次就占了两个
// 槽，而他只想要一个。
func assignedSkillIDs(p *world.PlayerState, rp *peer) []uint16 {
	out := make([]uint16, 0, 4+len(rp.pendingChoices))
	for _, id := range p.SkillActive {
		if id != 0 {
			out = append(out, id)
		}
	}
	for _, id := range p.SkillPassive {
		if id != 0 {
			out = append(out, id)
		}
	}
	for _, s := range rp.pendingChoices {
		out = append(out, s.ID)
	}
	return out
}

// sendSkillChoices 把当前候选列表经可靠通道下发给这个座位。
func (r *Room) sendSkillChoices(p *peer, window uint32) {
	level := uint32(1)
	if ent := r.World.Players.Get(p.id); ent != nil {
		level = uint32(ent.Level)
	}
	choices := make([]*proto.SkillChoice, 0, len(p.pendingChoices))
	for _, s := range p.pendingChoices {
		choices = append(choices, &proto.SkillChoice{
			SkillId: uint32(s.ID),
			Kind:    uint32(s.Kind),
		})
	}
	r.sendReliable(p.slot, &proto.Envelope{
		Body: &proto.Envelope_SkillChoices{SkillChoices: &proto.SkillChoices{
			Level:       level,
			Choices:     choices,
			WindowTicks: window,
		}},
	})
}

// sendReliable 把一条消息投进可靠下行队列，由 bridge 泵到会话上。
//
// 队列满时丢弃：这里绝不能阻塞——调用方是 tick 循环，阻塞一 tick 就是
// 全场卡 50ms。丢掉的候选不是死局：窗口到期会自动随机装一个，玩家损失
// 的只是「选哪个」的权利。
func (r *Room) sendReliable(slot int, env *proto.Envelope) {
	data, err := proto.Marshal(env)
	if err != nil {
		r.log.Warn("可靠消息编码失败", "slot", slot, "err", err)
		return
	}
	select {
	case r.reliable <- ReliableMessage{Slot: slot, Data: data}:
	default:
		r.log.Warn("可靠下行队列满，丢弃一条消息", "slot", slot, "queue", len(r.reliable))
	}
}

// ApplyChoice 应用玩家的技能选择。
//
// 由 bridge 在除 tick goroutine 之外的 goroutine 调用，所以必须走 channel
// 投递——直接改 r.World 的 PlayerState 就是跨 goroutine 写，-race 会报，
// 且表现为"偶尔少装一个技能"这种偶发（AGENTS.md §2.5）。
//
// 返回的错误区分三种情况，让 bridge 的日志有意义：不在窗口里（玩家点了
// 但窗口已过期）、下标越界（客户端与服务器看到的列表不一致）、房间停止。
func (r *Room) ApplyChoice(slot, choiceIndex int) error {
	if r.stopped.Load() {
		return ErrRoomStopped
	}
	req := choiceRequest{
		slot:  slot,
		index: choiceIndex,
		// 必须缓冲 1：tick 循环投回执是非阻塞的，调用方若已因超时/停止
		// 走掉，缓冲区保证 tick 循环不会卡在这里。
		reply: make(chan error, 1),
	}
	select {
	case r.choices <- req:
	case <-r.done:
		return ErrRoomStopped
	}
	select {
	case err := <-req.reply:
		return err
	case <-r.done:
		return ErrRoomStopped
	case <-time.After(choiceReplyTimeout):
		// 故障保护，不是正常路径：tick 50ms 一次，1 秒等于 20 个 tick，
		// 正常处理远快于此。没有这个上限时，一旦 tick 循环卡死（比如某
		// 个系统死循环），bridge 的会话读 goroutine 会永久阻塞在这个
		// 调用上，那个会话就再也不处理任何消息——连断线都发现不了。
		return ErrChoiceTimeout
	}
}

// 交互请求的 kind 值，与 internal/proto/game.proto 的 Interact 注释一一对应。
const (
	// InteractKindPickupNearest 是"拾取最近"（kind=1）。
	InteractKindPickupNearest = 1
	// InteractKindOpenInventory 是"开背包"（kind=2）。
	InteractKindOpenInventory = 2
)

// Interact 处理一次玩家交互。
//
// 与 ApplyChoice 同款：由 bridge 在 tick goroutine 之外的 goroutine 调用，
// 所以必须走 channel 投递，绝不能直接改 r.World（AGENTS.md §2.5）。
//
// 回执同样带超时上限，理由与 ApplyChoice 完全一样：一次卡死的 tick 循环
// 不能把某个会话的读 goroutine 永久粘住。
func (r *Room) Interact(slot, kind int) error {
	if r.stopped.Load() {
		return ErrRoomStopped
	}
	// kind 在入口就校验，不进队列。让不认识的值排到 tick 循环里再被拒，
	// 等于用一次世界结算的机会去发现"客户端发了个新版本才有的 kind"，
	// 而那种情况在日志里与"内部逻辑出错"长得一样。
	if kind != InteractKindPickupNearest && kind != InteractKindOpenInventory {
		return ErrInteractKind
	}
	req := interactRequest{
		slot: slot,
		kind: kind,
		// 缓冲 1 的理由同 choiceRequest：tick 侧投回执是非阻塞的。
		reply: make(chan error, 1),
	}
	select {
	case r.interacts <- req:
	case <-r.done:
		return ErrRoomStopped
	}
	select {
	case err := <-req.reply:
		return err
	case <-r.done:
		return ErrRoomStopped
	case <-time.After(choiceReplyTimeout):
		return ErrInteractTimeout
	}
}

// applyInteractLocked 在 tick goroutine 里应用一次交互。
//
// 名字带 Locked 与 applyChoiceLocked 对齐：它改 PlayerState，只能在 tick
// goroutine 里调用。
func (r *Room) applyInteractLocked(p *peer, kind int) error {
	ent := r.World.Players.Get(p.id)
	if ent == nil || ent.State != world.PlayerAlive {
		// 死人不能开背包，也不能拾取。返回错误而不是悄悄忽略：客户端在
		// 死亡时还发交互，说明它对自身状态的认知与服务端不一致。
		return errNoPeer
	}

	switch kind {
	case InteractKindOpenInventory:
		// 引导时长按 tick 率折算，放在这里算而不是缓存进 Room：它只在
		// 玩家按下的那一瞬间用一次，缓存反而多一份需要与 cfg 同步的状态。
		systems.BeginOpenInventory(ent, r.World.Tick+systems.OpenInventoryTicks(float64(r.cfg.Server.TickRate)))
		r.log.Debug("开始开背包引导", "slot", p.slot,
			"end_at", ent.InventoryOpenAt)
		return nil

	case InteractKindPickupNearest:
		// M4 的拾取是**自动**的：systems.UpdatePickups 每 tick 扫一遍掉落物，
		// 谁的半径内有就归谁（Task 9 的规则），手动再拾取一次是纯冗余。
		//
		// 刻意不在这里实现"立刻尝试拾取最近的一个"：那会是第二条修改世界
		// 的路径（与 UpdatePickups 各调一次 applyPickup），两条路径的半径
		// 判定、Carrier 规则、满槽丢弃规则都得保持同步，日后改一处漏一处
		// 的症状是"走过去能捡、按 E 捡不到"。更实际的是它几乎永远是 no-op
		// ——玩家能站在掉落物上按 E 的时候，自动拾取早在这一 tick 之前就
		// 把它捡走了。所以这里如实打一条 Debug 并成功返回，等 M5 出现
		// 需要"主动取舍"的掉落（例如鬼魂搬运血瓶）再给它语义。
		r.log.Debug("交互：拾取最近（M4 为自动拾取，手动语义留待 M5）",
			"slot", p.slot)
		return nil
	}
	return ErrInteractKind
}

// tickInventory 推进所有玩家的开背包引导。
//
// 在 tick 里调一次（不是子步），与 tickBuffs 同款理由：引导的结束时刻是
// 一个 tick 号，若接进 60Hz 的子步，同一个 tick 号会被判定三次，引导在
// 3 个子步里连着"完成"（第一次清位后后两次是 no-op，但这意味着实际时长短
// 于配置），而且玩家看到的进度会随 SubSteps 变化。
func (r *Room) tickInventory() {
	now := r.World.Tick
	r.World.Players.Each(func(_ world.EntityID, p *world.PlayerState) bool {
		if systems.TickOpenInventory(p, now) {
			// 完成时不发消息：背包内容（技能槽/道具槽）本来就随快照或
			// Inventory 消息走，M4 还没有"背包已打开"的客户端状态机。
			// 这条日志是给联调用看的，不打会以为引导没走完。
			r.log.Debug("开背包引导完成")
		}
		return true
	})
}

// tickRevives 推进祭坛复活引导（M5 Task 5 的接线）。
//
// 自动开始而不是按键（M5 拍板）：鬼魂站在祭坛上就进入引导，走出范围或
// 给出移动输入就打断。不需要"按 E"是刻意的——灰盒阶段玩家分不清"我没
// 按键"和"系统没响应"，而自动开始让规则可见（站着不动就在涨）。
//
// 每 tick 一次、不进子步：ReviveAt 是 tick 号，进子步会让同一个 tick 号
// 被判定三次，引导实际时长变成配置的三分之一（与 tickInventory 同款理由）。
func (r *Room) tickRevives() {
	now := r.World.Tick
	tickLen := systems.ReviveTicks(float64(r.cfg.Server.TickRate))
	r.World.Players.Each(func(id world.EntityID, p *world.PlayerState) bool {
		if p.State != world.PlayerGhost {
			return true
		}
		if p.ReviveAt == 0 {
			// 没在引导：站在祭坛上就自动开始。ReviveBegin 自己判范围
			// 与幂等（已在引导返 false），这里不重复判。
			systems.ReviveBegin(r.World, p, now+tickLen)
			return true
		}
		if !systems.ReviveTick(r.World, p, now) {
			return true
		}
		// 复活完成：广播事件给客户端播特效。
		//
		// 走 miscEvents 而不是 r.events：后者是 []world.DamageEvent，
		// 复活不是伤害（见 miscEvents 字段注释）。
		r.miscEvents = append(r.miscEvents, &proto.GameEvent{
			Kind: proto.GameEventKind_GAME_EVENT_KIND_REVIVE,
			A:    uint32(id),
		})
		r.log.Info("复活", "player", id, "pos", p.Pos)
		// RecomputeDerived 必须重算：复活给半血，而复活清掉过
		// SpeedBuffLeft/SpeedBuffMul，Derived 里缓存的倍率要与槽位内容
		// 保持一致（重生后换过装备或升级过都在这里统一收口）。
		systems.RecomputeDerived(p)
		return true
	})
}

// tickTethers 把跑远的鬼魂拉回拴绳半径内（M5 Task 3 的接线）。
//
// 半径来自 World.GhostTether（room.New 从 cfg.Ghost.TetherRadius 拷入），
// 不在这里读 cfg：systems 层不该知道配置结构，且 0（不拴）的语义由
// TetherGhosts 自己处理。
func (r *Room) tickTethers() {
	systems.TetherGhosts(r.World)
}

// tickGhostCarry 推进鬼魂搬运：先捡起、再交接（M5 Task 8 的接线）。
//
// 顺序是捡起在前：刚掉的掉落物与刚走到旁边的鬼魂在同一 tick 内都该生效，
// 反过来的话玩家会看到"东西在旁边但要多等一帧才被拎起来"。
func (r *Room) tickGhostCarry() {
	systems.GhostPickups(r.World)
	systems.GhostHandoffs(r.World)
}

// tickTeamWipe 判定团灭并广播（M5 Task 10 的接线）。
//
// 房间继续跑（M5 拍板）：灰盒没有关卡脚本，停下就只剩重启能看。
// CheckTeamWipe 保证只在新成立时返回 true，所以这里不必再记状态。
func (r *Room) tickTeamWipe() {
	if !systems.CheckTeamWipe(r.World) {
		return
	}
	r.miscEvents = append(r.miscEvents, &proto.GameEvent{
		Kind: proto.GameEventKind_GAME_EVENT_KIND_WIPE,
	})
	r.log.Warn("团灭：全队变鬼魂", "tick", r.World.Tick)
}

// creditHubKills 把本子步的击杀金币记进据点池子（不再是直给击杀者）。
//
// M5 是直给（GrantKillGold），M6 改成进池分账（spec L79 的「据点内贡献
// 大的多拿钱，每据点重新计算」）。两套规则不能并存：直给 + 分账会让
// 玩家拿到两倍，而那个 bug 不会报错、只会让经济数值完全失控。
func (r *Room) creditHubKills(inc []world.DamageEvent) {
	for _, ev := range inc {
		if !ev.IsKill {
			continue
		}
		systems.CreditHubKill(r.World, ev.Src, systems.GoldPerKill)
	}
}

// settleClearedHubs 在据点刚清完时把它的金币池按贡献分给全队。
//
// 放在 tick 的波次更新之后：HubCleared 是 UpdateWaves 写进去的，早于它
// 读不到「刚清完」。每据点只结算一次——用 KillGold 归零作为「已结算」
// 的标记，而不是加一个 Settled 布尔：池子为 0 时结算本来就无事可做。
//
// 金币注入在这里而不是 SettleHub 内部：这样 SettleHub 是纯函数（只返回
// 份额），而「哪一处把金币写进玩家」在全仓只有一个位置。
func (r *Room) settleClearedHubs() {
	if !r.World.HasMap {
		return
	}
	for i := range r.World.Hubs {
		h := &r.World.Hubs[i]
		if h.State != world.HubCleared || h.KillGold == 0 {
			continue
		}
		pool := h.KillGold
		h.KillGold = 0
		shares := systems.SettleHub(r.World, pool)
		for _, s := range shares {
			p := r.World.Players.Get(s.Player)
			if p == nil {
				continue
			}
			if p.Gold > math.MaxUint32-s.Gold {
				p.Gold = math.MaxUint32
			} else {
				p.Gold += s.Gold
			}
		}
		r.log.Info("据点分账", "hub", i, "pool", pool, "shares", len(shares))
	}
}

// spawnLoot 在每只被杀的怪物死亡位置生成掉落（M5 Task 6 的接线）。
//
// 必须在 Reap 之前调：Reap 会 Pool.Kill，槽位清零、位置没了。
//
// 位置死亡技能（spec L18「空间即资源」）：怪死在哪个据点，就掉那个据点
// 属性的技能。没有地图（HasMap=false）时全靠 SpawnDrops 内部回退到全部
// 主动技能——那种场景只有测试与 bot 会造。
//
// 走 w.RandIntn 包装而不是 rand.Intn：模拟路径必须让 rngCalls 计数完整，
// 否则回放对不上（world.go 的 RNG 纪律）。
func (r *Room) spawnLoot(inc []world.DamageEvent) {
	for _, ev := range inc {
		if !ev.IsKill {
			continue
		}
		m := r.World.Monsters.Get(ev.Dst)
		if m == nil {
			continue // 已被别处回收，拿不到位置
		}
		systems.SpawnDrops(r.World, m.Pos, r.hubElementAt(m.Pos))
	}
}

// hubElementAt 返回 pos 所在据点的元素属性，不在任何据点内返回 ElementNone。
//
// 为什么不用 NearestAltar 的返回值：NearestAltar（hub.go:49）只回传位置，
// 因为 M5 的复活系统只需要位置。给它加一个「哪个据点」的返回值会逼所有
// 调用点改签名，而这里只要多查一次个位数长度的切片。等 M7 出现第三处
// 需要「是哪个据点」时再抽 NearestHub(pos, radius) (int, bool)。
//
// 判定半径用 hub.RadiusM（据点范围 5 米）而不是系统的 AltarRadius（2.5 米）：
// 传错会让据点的外圈算作「不在据点内」，那些怪掉落的技能不按元素筛。
func (r *Room) hubElementAt(pos world.Vec2) world.Element {
	best := world.ElementNone
	var bestSq float64
	var found bool
	for i := range r.World.Hubs {
		h := &r.World.Hubs[i]
		d := pos.DistSq(h.Center)
		if d > h.RadiusM*h.RadiusM {
			continue
		}
		if !found || d < bestSq {
			bestSq = d
			best = h.Element
			found = true
		}
	}
	return best
}

// fire 让每个在座玩家按当前按键尝试开火。
//
// dt 用一个 tick 的时长而不是真实耗时：模拟必须可复现，真实耗时每个
// tick 都不同（AGENTS.md §2.6）。
//
// 遍历用 peers.each 而不是 Players.Each：需要每个玩家的按键状态，
// 那存在 peer 上而不是实体上——按键是输入信息，不是世界状态。
func (r *Room) fire() {
	tickDt := 1.0 / float64(r.cfg.Server.TickRate)

	r.peers.each(func(p *peer) {
		ent := r.World.Players.Get(p.id)
		if ent == nil {
			return
		}
		held := p.buttons&ButtonAttack != 0
		systems.Fire(r.World, p.id, ent, held, p.hasAim, tickDt)
	})
}

// Metrics 返回当前的运行指标。
// tickHistogram 是 tick 耗时的定长直方图（M7）。
//
// 256 个桶、桶宽 1ms、覆盖 0~256ms：tick 预算 50ms，瓶颈诊断只会看
// 0~100ms 这一段；再长的耗时已经超过预算一个数量级，精度不重要。
//
// 为什么不存样本切片：20Hz 跑 25 分钟是 30000 个样本，每样本 8 字节也要
// 240KB 常驻，而直方图固定 2KB（256 × uint64）且 p99 精度足够。
// 为什么不只留 max：§11.5 要的是 p99，max 会被一次 GC 尖峰带偏。
type tickHistogram struct {
	buckets [256]uint64
	total   uint64
}

// observe 记录一次耗时。
//
// 超长耗时钳到最后一桶而不是丢弃：丢掉会让 total 与真实 tick 数不符，
// 而「有多少次慢得超出量程」本身就是压测要看的信号。
func (h *tickHistogram) observe(d time.Duration) {
	if d < 0 {
		d = 0
	}
	idx := int(d / time.Millisecond)
	if idx >= len(h.buckets) {
		idx = len(h.buckets) - 1
	}
	h.buckets[idx]++
	h.total++
}

// percentile 返回 p 分位数的桶下界（p ∈ [0,1]，越界会被钳）。
//
// 返回桶下界而不是桶内插值：题目问的是「p99 有没有超过 50ms」，桶宽 1ms
// 的粒度足够判这个，插值只是让数字看起来更精确。
func (h *tickHistogram) percentile(p float64) time.Duration {
	if h.total == 0 {
		return 0
	}
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	want := uint64(p * float64(h.total))
	if want == 0 {
		// total 很小、p 也很小时算出来是 0。不兜底成 1 会让它一路
		// 落到最后返回的 len-1 桶——单个样本的 p10 会报成 255ms。
		want = 1
	}
	var seen uint64
	for i, c := range h.buckets {
		seen += c
		if seen >= want {
			return time.Duration(i) * time.Millisecond
		}
	}
	return time.Duration(len(h.buckets)-1) * time.Millisecond
}

type Metrics struct {
	Ticks      uint64
	Overruns   uint64
	LastTickNs int64
	// 以下四项是 M7 压测用（§11.5）。硬指标：Overruns==0 且 P99TickNs < 50ms。
	TickSamples uint64 // 直方图样本数（= 采样过的 tick 数）
	P50TickNs   int64
	P99TickNs   int64
	P100TickNs  int64
}

// Metrics 读取运行指标，可并发调用。
//
// 直方图本体只在 tick goroutine 里改，这里读的是不可变快照（每 100 tick
// 刷新一次）。直接读 tickHist 会与 tick 循环撞车——与 peers/lastTickDur
// 的既有纪律一致。
func (r *Room) Metrics() Metrics {
	var hist tickHistogram
	if snap := r.histSnapshot.Load(); snap != nil {
		hist = *snap
	}
	return Metrics{
		Ticks:       r.ticksRun.Load(),
		Overruns:    r.overrun.Load(),
		LastTickNs:  r.lastTickDur.Load(),
		TickSamples: hist.total,
		P50TickNs:   int64(hist.percentile(0.50)),
		P99TickNs:   int64(hist.percentile(0.99)),
		P100TickNs:  int64(hist.percentile(1.0)),
	}
}

// sampleTick 记录一次 tick 耗时，并每 100 tick 刷新一次跨 goroutine 快照。
//
// 不每 tick 刷新：atomic.Pointer.Store 每次要分配一个 2KB 结构，20Hz 下
// 就是每秒 40KB 垃圾，为「退出时看一眼分布」付这个代价不值。5 秒的
// 分辨率对压测报告足够。
func (r *Room) sampleTick(d time.Duration) {
	r.tickHist.observe(d)
	// 首个样本立即刷新：进程刚起来就报零会让「还没跑够 100 tick」与
	// 「指标没接上」看起来一样。
	if r.tickHist.total != 1 && r.tickHist.total%histSnapshotEvery != 0 {
		return
	}
	snap := r.tickHist
	r.histSnapshot.Store(&snap)
}

// Seed 返回本房间的随机种子，重放时用它复现。
func (r *Room) Seed() int64 { return r.seed }

// WorldTick 返回当前世界 tick。可并发调用。
//
// 这是 World.Tick 的原子镜像。直接读 r.World.Tick 会与 tick 循环的写入
// 撞车——那个字段只在 tick goroutine 里安全。
func (r *Room) WorldTick() uint32 { return r.worldTick.Load() }

// ShopBuy 处理一次买入请求（M6 Task 10）。
//
// 与 ApplyChoice / Interact 同款：网络 goroutine 投通道，tick goroutine
// 消费，这里等回执。理由完全一样——世界由 tick goroutine 独占，跨
// goroutine 直接改是数据竞争（AGENTS.md §2.5）。
func (r *Room) ShopBuy(slot int, hub uint32, itemIndex, targetSlot int) error {
	if r.stopped.Load() {
		return ErrRoomStopped
	}
	req := shopBuyRequest{
		slot:       slot,
		hub:        hub,
		itemIndex:  itemIndex,
		targetSlot: targetSlot,
		// 缓冲 1 的理由同 choiceRequest：tick 侧投回执是非阻塞的。
		reply: make(chan error, 1),
	}
	select {
	case r.shopBuys <- req:
	case <-r.done:
		return ErrRoomStopped
	}
	select {
	case err := <-req.reply:
		return err
	case <-r.done:
		return ErrRoomStopped
	case <-time.After(choiceReplyTimeout):
		return ErrInteractTimeout
	}
}

// drainShopBuys 把本 tick 的买入请求排队处理。
//
// 与 drainInteracts 同款：非阻塞 for-select，处理完立刻回执。不能因为某个
// 请求出错就停下——一个玩家的买入失败不该拖住其他玩家的。
func (r *Room) drainShopBuys() {
	for {
		select {
		case req := <-r.shopBuys:
			err := r.applyShopBuyLocked(req)
			select {
			case req.reply <- err:
			default:
			}
		default:
			return
		}
	}
}

// applyShopBuyLocked 在 tick goroutine 里执行买入。
//
// 名字带 Locked 与 applyChoiceLocked / applyInteractLocked 对齐。
func (r *Room) applyShopBuyLocked(req shopBuyRequest) error {
	buyer := r.peers.at(req.slot)
	if buyer == nil {
		return errNoPeer
	}
	bp := r.World.Players.Get(buyer.id)
	if bp == nil || bp.State != world.PlayerAlive {
		return errNoPeer
	}

	// 据点必须开着商店，且与客户端以为的那个一致——防「在 A 据点开商店、
	// 走到 B 据点才发买入」的时序问题。
	if !r.World.HasMap || int(req.hub) >= len(r.World.Hubs) {
		return ErrShopClosed
	}
	h := &r.World.Hubs[req.hub]
	if !h.ShopOpen {
		return ErrShopClosed
	}
	// 买家必须在据点范围内（spec L82「商店在据点内」）。
	if bp.Pos.DistSq(h.Center) > h.RadiusM*h.RadiusM {
		return ErrShopTooFar
	}

	// 收货人：默认自己（spec L84-85 替队友买）。
	target := buyer
	if req.targetSlot != req.slot {
		target = r.peers.at(req.targetSlot)
		if target == nil {
			return errNoPeer
		}
	}
	tp := r.World.Players.Get(target.id)
	if tp == nil || tp.State != world.PlayerAlive {
		// 替鬼魂买东西没有意义（它不能用），也防止"钱花了东西掉了"。
		return ErrShopTargetDead
	}

	// 货架必须属于当前据点：玩家换据点后货架要重新生成，拿旧货架的
	// 下标买新据点的东西会错位。
	if buyer.shopHub != int(req.hub) || len(buyer.rack) == 0 {
		return systems.ErrShopNoRack
	}
	if req.itemIndex < 0 || req.itemIndex >= len(buyer.rack) {
		return systems.ErrShopItemIndex
	}
	it := buyer.rack[req.itemIndex]
	if it.Sold {
		return systems.ErrShopSold
	}

	// 钱从买家扣、东西给收货人。先扣款再给货，给货失败必须退款——
	// 反过来（先给货再扣款）在扣款失败时会白送一件东西。
	if err := systems.SpendGold(bp, it.Price); err != nil {
		return systems.ErrShopNoGold
	}
	switch it.Kind {
	case systems.ShopItemPassive:
		if !systems.TryEquipSkill(tp, it.RefID) {
			bp.Gold += it.Price
			return ErrShopEquipFailed
		}
	case systems.ShopItemItem:
		if !systems.EquipItem(tp, it.RefID) {
			bp.Gold += it.Price
			return ErrShopEquipFailed
		}
	default:
		bp.Gold += it.Price
		return ErrShopEquipFailed
	}

	buyer.rack[req.itemIndex].Sold = true
	r.log.Info("商店成交", "slot", req.slot, "target", req.targetSlot,
		"item", req.itemIndex, "price", it.Price)
	return nil
}

// updateShopOpen 在据点清完时给全队生成私人货架并打开商店。
//
// 放在 tick 的波次更新之后：HubCleared 是 UpdateWaves 写进去的。
func (r *Room) updateShopOpen() {
	if !r.World.HasMap {
		return
	}
	for i := range r.World.Hubs {
		h := &r.World.Hubs[i]
		// HubCleared 且还没开过：开。用 ShopOpen 自己作为「开过」的标记，
		// 不另加字段——商店开着就是开着，没有第三种状态。
		if h.State != world.HubCleared || h.ShopOpen {
			continue
		}
		// 据点里得有一个活着的玩家才开：spec L82「商店在据点内、清完怪后
		// 开放」，全队都在别的据点时这里没有人能看商店，生成货架只是
		// 浪费 RNG 消耗（而那会让回放分叉）。
		if !r.anyAliveNear(h.Center, h.RadiusM) {
			continue
		}
		h.ShopOpen = true
		r.peers.each(func(p *peer) {
			ent := r.World.Players.Get(p.id)
			if ent == nil || ent.State != world.PlayerAlive {
				return
			}
			p.rack = systems.NewRack(r.World, assignedSkillIDs(ent, p))
			p.shopHub = i
			r.sendReliable(p.slot, &proto.Envelope{
				Body: &proto.Envelope_ShopRack{
					ShopRack: &proto.ShopRack{
						Hub:   uint32(i),
						Items: rackToProto(p.rack),
						Gold:  ent.Gold,
					},
				},
			})
		})
		r.log.Info("商店开放", "hub", i)
	}
}

// updateShopClose 在全队离开据点后关闭商店（spec L86）。
func (r *Room) updateShopClose() {
	if !r.World.HasMap {
		return
	}
	for i := range r.World.Hubs {
		h := &r.World.Hubs[i]
		if !h.ShopOpen {
			continue
		}
		if r.anyAliveNear(h.Center, h.RadiusM) {
			continue
		}
		h.ShopOpen = false
		// 货架清空：spec L86「全队离开据点，商店关闭」，重新开放时
		// 应该是新的一批商品（否则玩家可以「逛一圈回来买上次看中的」，
		// 而那个行为在数值上等于无限库存）。
		r.peers.each(func(p *peer) {
			if p.shopHub == i {
				p.rack = nil
			}
		})
		r.log.Info("商店关闭", "hub", i)
	}
}

// spawnBossIfReady 在全部据点清完后刷 Boss，并打一次日志。
//
// 只在 wavesEnabled 时调用：Boss 依附于波次进度（spec L94「每关 5~8 波 +
// Boss」），没有波次就没有「清完据点」这个状态。
func (r *Room) spawnBossIfReady() {
	if r.World.Boss == nil || r.World.BossSpawned {
		return
	}
	if !systems.SpawnBoss(r.World) {
		return
	}
	r.bossSeen.Store(true)
	r.log.Info("Boss 出现",
		"hub", r.World.BossHub,
		"hp", r.World.Boss.HP,
		"tick", r.World.Tick)
}

// trackBossPhase 在 Boss 进入二阶段时打一次日志。
//
// systems 包不依赖 slog（它是纯模拟层），所以阶段切换的留痕落在 room：
// 每 tick 比对一次「现在的阶段」与「见过的最高阶段」。验收与压测都靠这行
// 日志确认「真的进过二阶段」——没有它，一个从不换阶段的 Boss 与一个
// 换得很晚的 Boss 从外部完全无法区分。
func (r *Room) trackBossPhase() {
	phase := uint8(0)
	var hpPct float64
	r.World.Monsters.Each(func(_ world.EntityID, m *world.MonsterState) bool {
		if m.Flags.Has(world.MonsterFlagBoss) && !m.IsDead() {
			phase = m.BossPhase
			if m.MaxHP > 0 {
				hpPct = m.HP / m.MaxHP
			}
			return false
		}
		return true
	})
	if phase <= uint8(r.bossPhaseSeen.Load()) {
		return
	}
	r.bossPhaseSeen.Store(uint32(phase))
	r.log.Info("Boss 进入二阶段",
		"phase", phase,
		"hp_pct", hpPct,
		"tick", r.World.Tick)
}

// anyAliveNear 报告区域内是否有活着的玩家。
//
// 鬼魂不算：它们不能用商店，只有它们在场时开着商店没有意义。
func (r *Room) anyAliveNear(pos world.Vec2, radius float64) bool {
	r2 := radius * radius
	var found bool
	r.World.Players.Each(func(_ world.EntityID, p *world.PlayerState) bool {
		if p.State != world.PlayerAlive {
			return true
		}
		if p.Pos.DistSq(pos) <= r2 {
			found = true
			return false
		}
		return true
	})
	return found
}

// rackToProto 把货架转成协议结构。
func rackToProto(rack []systems.ShopItem) []*proto.ShopItem {
	out := make([]*proto.ShopItem, len(rack))
	for i, it := range rack {
		out[i] = &proto.ShopItem{
			Index: uint32(it.Index),
			Kind:  uint32(it.Kind),
			RefId: uint32(it.RefID),
			Price: it.Price,
			Sold:  it.Sold,
		}
	}
	return out
}
