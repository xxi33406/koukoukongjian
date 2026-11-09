package world

import (
	"math/rand"

	"koukoukongjian/internal/config"
)

// World 是一个房间的权威世界状态。
//
// 这里只有数据与访问器，没有系统逻辑——系统在 internal/game/systems，由
// internal/room 按固定顺序调用。这样拆分是为了让"顺序即游戏逻辑"这件事
// 在代码结构上就看得见（docs/combat-design-v0.1.md §1.2）。
type World struct {
	// Tick 是已执行的逻辑 tick 数，从 0 开始。
	Tick uint32

	Players  *Pool[PlayerState]
	Monsters *Pool[MonsterState]
	Bullets  *Pool[BulletState]
	Pickups  *Pool[PickupState]

	// Grid 是空间索引，每个 tick 重建一次。
	Grid *SpatialGrid

	// Terrain 是地形位图，决定哪里能走。
	Terrain *Terrain

	// RNG 是这个世界专用的随机源。
	//
	// 每个房间独立，绝不用全局 math/rand——全局源会被其它房间的调用推进，
	// 回放就不可能一致了。种子由房间创建时传入并记录，重放时用同一颗种子。
	RNG   *rand.Rand
	Seed  int64
	SubDt float64 // 物理子步时长，秒
	// TickRate 是逻辑 tick 频率，用于 TickFloat 换算。
	TickRate uint32
	// SubSteps 是每 tick 的物理子步数。
	SubSteps int

	// 随机数的"已消耗次数"，用于回放时恢复状态。
	//
	// math/rand.Rand 不暴露内部状态，但同一颗种子下第 N 次调用的结果唯一，
	// 所以记录调用次数等价于记录状态。这个计数器必须在每次 RNG 调用后同步
	// 更新——见 RandIntn / RandFloat64。
	rngCalls uint64

	// 时间参数（从 config 拷来，避免每处都传 cfg）。
	AggroRescoreTicks uint32
	PathRecalcTicks   uint32
	MaxBullets        int
}

// NewWorld 依据配置创建一个空世界。
func NewWorld(cfg *config.Config, seed int64) *World {
	tickRate := uint32(cfg.Server.TickRate)

	w := &World{
		Players:  NewPool[PlayerState](cfg.World.MaxPlayers, cfg.World.MaxPlayers),
		Monsters: NewPool[MonsterState](128, MaxIndex),
		Bullets:  NewPool[BulletState](cfg.Combat.MaxBullets, MaxIndex),
		Pickups:  NewPool[PickupState](64, MaxIndex),
		Terrain:  NewTerrain(cfg.World.TerrainWidth, cfg.World.TerrainHeight, cfg.Space.TerrainCellSize),
		Grid:     NewSpatialGrid(cfg.World.TerrainWidth, cfg.World.TerrainHeight, cfg.Space.TerrainCellSize, cfg.Space.SpatialCellSize),
		RNG:      rand.New(rand.NewSource(seed)),
		Seed:     seed,
		SubDt:    cfg.SubDt(),
		TickRate: tickRate,
		SubSteps: cfg.Server.SubSteps,

		AggroRescoreTicks: secondsToTicks(cfg.AI.AggroRescoreIntervalS, tickRate),
		PathRecalcTicks:   secondsToTicks(cfg.AI.PathRecalcIntervalS, tickRate),
		MaxBullets:        cfg.Combat.MaxBullets,
	}
	return w
}

// secondsToTicks 把秒数换算成 tick 数，至少 1。
//
// 至少 1 很重要：0 会让"每 N tick 做一次"的条件恒真，退化成每 tick 都算，
// 那是性能陷阱而不是配置错误，运行时很难察觉。
func secondsToTicks(seconds float64, tickRate uint32) uint32 {
	t := uint32(seconds*float64(tickRate) + 0.5)
	if t == 0 {
		t = 1
	}
	return t
}

// RandIntn 返回 [0, n) 的随机整数。
//
// 所有随机数都必须走这两个包装函数，不要在别处直接调 w.RNG——包装函数维护
// rngCalls 计数器，漏调一次回放就会分叉，而且分叉点极难定位。
func (w *World) RandIntn(n int) int {
	w.rngCalls++
	return w.RNG.Intn(n)
}

// RandFloat64 返回 [0,1) 的随机浮点。
func (w *World) RandFloat64() float64 {
	w.rngCalls++
	return w.RNG.Float64()
}

// RandRange 返回 [lo, hi) 的随机浮点。
func (w *World) RandRange(lo, hi float64) float64 {
	return lo + w.RandFloat64()*(hi-lo)
}

// RNGCalls 返回随机源已被调用的次数，回放时用它校验一致。
func (w *World) RNGCalls() uint64 { return w.rngCalls }

// TickFloat 返回当前 tick 对应的逻辑秒数。
//
// 用固定步长换算，不读真实时钟——真实时间会让模拟结果随机器负载变化，回放
// 就对不上了。
func (w *World) TickFloat() float64 {
	return float64(w.Tick) / float64(w.TickRate)
}

// Reset 把世界恢复到初始空状态，保留种子。
func (w *World) Reset() {
	w.Tick = 0
	w.Players.Reset()
	w.Monsters.Reset()
	w.Bullets.Reset()
	w.Pickups.Reset()
	w.rngCalls = 0
	w.RNG = rand.New(rand.NewSource(w.Seed))
}

// Stats 是给日志与指标用的世界快照。
type Stats struct {
	Tick     uint32
	Players  int
	Monsters int
	Bullets  int
	Pickups  int
}

// Stats 返回当前世界规模。
func (w *World) Stats() Stats {
	return Stats{
		Tick:     w.Tick,
		Players:  w.Players.Len(),
		Monsters: w.Monsters.Len(),
		Bullets:  w.Bullets.Len(),
		Pickups:  w.Pickups.Len(),
	}
}
