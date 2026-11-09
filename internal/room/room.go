// Package room 负责房间的生命周期与固定步长 tick 循环。
//
// 房间是"一局游戏"的容器：它拥有一个 world.World，按固定节奏推进它，并在
// 每 tick 结束时把结果交给网络层。这一层不做战斗计算（那是 systems 的事），
// 只负责"什么时候跑什么"，以及"跑不完怎么办"。
package room

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"koukoukongjian/internal/config"
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

	// overrun 累计"这一 tick 没跑完就超时"的次数。
	//
	// 用原子量：指标会被别的 goroutine 读。tick 循环本身是单 goroutine 的，
	// 所以不用锁保护 World。
	overrun atomic.Uint64
	// ticksRun 累计执行的 tick 数。
	ticksRun atomic.Uint64
	// lastTickDur 是上一 tick 的实际耗时（纳秒），用于观测。
	lastTickDur atomic.Int64

	// done 关闭时 Room.Run 退出。
	done chan struct{}
	// stopped 表示 Run 已返回，避免重复 Stop。
	stopped atomic.Bool
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
			r.stopped.Store(true)
			r.log.Info("房间停止", "reason", "ctx done", "ticks", r.ticksRun.Load())
			return
		case <-r.done:
			r.stopped.Store(true)
			r.log.Info("房间停止", "reason", "stop called", "ticks", r.ticksRun.Load())
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
				r.stopped.Store(true)
				r.log.Info("房间停止", "reason", "ctx done during wait", "ticks", r.ticksRun.Load())
				return
			case <-r.done:
				timer.Stop()
				r.stopped.Store(true)
				r.log.Info("房间停止", "reason", "stop called during wait", "ticks", r.ticksRun.Load())
				return
			}
		}
	}
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
// 顺序即游戏逻辑（docs/combat-design-v0.1.md §1.1）。M0 阶段只搭骨架：
// 重建空间索引、跑子步、推进 tick 计数。各系统在 M1~M4 逐步填进来，但
// 这个循环的形状从第一天就定死，后面只往里加调用，不改结构。
func (r *Room) tick() {
	w := r.World

	// 0. 重建空间索引（每 tick 一次，不是每子步）。
	w.Grid.Rebuild(w.Monsters, w.Bullets, w.Players)

	// 1~2. 子步内：输入 → AI → 移动 → 碰撞 → 伤害 → 死亡清理
	for sub := 0; sub < r.subSteps; sub++ {
		r.subStep(sub)
	}

	// 3. 生成（每 tick，不在子步内 —— 否则一 tick 生成 subSteps 倍）
	//    留给 M3。

	// 4. 世界级结算：迷雾、分账、超时判定
	//    留给 M3/M4。

	// 5. 快照编码
	//    留给 M1。

	w.Tick++
}

// subStep 推进一个物理子步。
//
// M0 里它是空的——但保留这个函数而不是把循环写死，是因为后面每个子步要按
// 固定顺序调用六个系统，写死顺序会让 diff 变得很难读。
func (r *Room) subStep(sub int) {
	_ = sub
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
