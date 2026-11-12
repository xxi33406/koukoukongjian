package room

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/game/systems"
	"koukoukongjian/internal/game/world"
)

// Room 是一局游戏。
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
	// events 是这一 tick 攒下的伤害/击杀事件，M2 只攒不发（快照带上它
	// 属于 M3 的 UI 反馈）。每个 tick 开头用 [:0] 清空以复用底层数组。
	events []world.DamageEvent
	// nextSnapshot 是本 tick 生成好、待网络层取走的快照。
	// 这里不用它——快照直接写进每个 peer 的 sink。保留字段会误导，故无。

	// done 关闭时 Room.Run 退出。
	done chan struct{}
	// stopped 表示 Run 已返回，避免重复 Stop。
	stopped atomic.Bool
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

// Options 是创建房间的参数。
type Options struct {
	ID   string
	Seed int64
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
		done:         make(chan struct{}),
	}
	r.World.Grid.SetBruteThreshold(cfg.Space.SpatialBruteThreshold)
	return r
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
	r.drainJoins()
	r.drainLeaves()
	r.drainInputs()

	// 1. 重建空间索引（每 tick 一次，不是每子步）。
	w.Grid.Rebuild(w.Monsters, w.Bullets, w.Players)

	// 2~3. 子步内：移动 → 碰撞 → 伤害 → 死亡清理
	for sub := 0; sub < r.subSteps; sub++ {
		r.subStep(sub)
	}

	// 3. 生成：开火属于生成，放在子步循环之外。
	// 理由见 combat-design-v0.1.md:77——发射频率以 tick 为单位，
	// 放子步里会让一 tick 生成三倍子弹（§1.2 反例 4）。
	r.fire()

	// 4. 世界级结算：M2 只有事件累积（r.events 已在子步里攒好）
	// 5. 编码快照
	r.broadcast()

	w.Tick++
	r.worldTick.Store(w.Tick)
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
		systems.Integrate(p, dt)
		return true
	})
	systems.IntegrateBullets(w, dt)

	// 2.4 碰撞与命中：只产出命中列表，不改任何血量。
	// 分两步是为了让"谁该掉血"与"实际掉多少"各自可测——合在一起时，
	// 判定写错与伤害公式写错的表现完全一样。
	hits := systems.Collide(w)

	// 2.5 伤害结算：唯一入口，死亡只标记不回收
	r.events = append(r.events, systems.ApplyHits(w, hits)...)

	// 2.6 死亡清理：回收 + 世代 +1
	r.events = append(r.events, systems.Reap(w)...)
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
type Metrics struct {
	Ticks      uint64
	Overruns   uint64
	LastTickNs int64
}

// Metrics 读取运行指标，可并发调用。
func (r *Room) Metrics() Metrics {
	return Metrics{
		Ticks:      r.ticksRun.Load(),
		Overruns:   r.overrun.Load(),
		LastTickNs: r.lastTickDur.Load(),
	}
}

// Seed 返回本房间的随机种子，重放时用它复现。
func (r *Room) Seed() int64 { return r.seed }

// WorldTick 返回当前世界 tick。可并发调用。
//
// 这是 World.Tick 的原子镜像。直接读 r.World.Tick 会与 tick 循环的写入
// 撞车——那个字段只在 tick goroutine 里安全。
func (r *Room) WorldTick() uint32 { return r.worldTick.Load() }
