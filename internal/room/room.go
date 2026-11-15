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
	"koukoukongjian/internal/game/fog"
	"koukoukongjian/internal/game/mapgen"
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
		done:         make(chan struct{}),
	}
	r.World.Grid.SetBruteThreshold(cfg.Space.SpatialBruteThreshold)

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

	// 1.5 波次（M3 Task 10）：每 tick 一次，在子步之外——与迷雾 Reveal、
	// fire 同款"世界级结算"节拍。刷怪走 w.RandFloat64 包装，RNG 调用计数
	// 完整、回放一致。UpdateWaves 自带防御（Wav/Terrain/Hubs 未装配时
	// no-op），但 wavesEnabled=false 的 M2 房间连函数都不该进：降级世界
	// 没有据点，短路语义与 fog（r.fog==nil）保持一致。
	if r.wavesEnabled {
		systems.UpdateWaves(r.World)
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

	// 2~3. 子步内：移动 → 碰撞 → 伤害 → 死亡清理
	for sub := 0; sub < r.subSteps; sub++ {
		r.subStep(sub)
	}

	// 3. 生成：开火属于生成，放在子步循环之外。
	// 理由见 combat-design-v0.1.md:77——发射频率以 tick 为单位，
	// 放子步里会让一 tick 生成三倍子弹（§1.2 反例 4）。
	r.fire()

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
