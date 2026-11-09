// Package config 集中定义服务器的全部可调参数。
//
// 设计原则：
//   - 所有参数都有合理默认值，配置文件缺席时服务器仍能启动；
//   - 所有参数都可被 TOML 覆盖，灰盒调参时不必重新编译；
//   - 加载后立即校验，把错误暴露在启动时而不是运行中的某次除零。
package config

import (
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

// Config 是配置文件的完整映射，字段名与 TOML 键一一对应。
type Config struct {
	Server  Server  `toml:"server"`
	Network Network `toml:"network"`
	Combat  Combat  `toml:"combat"`
	AI      AI      `toml:"ai"`
	Space   Space   `toml:"space"`
	World   World   `toml:"world"`
	Log     Log     `toml:"log"`
}

// Server 是进程级参数。
type Server struct {
	Name     string `toml:"name"`
	Bind     string `toml:"bind"`
	TickRate int    `toml:"tick_rate"` // 逻辑 tick 频率（Hz）
	SubSteps int    `toml:"sub_steps"` // 每 tick 的物理子步数
}

// Network 是同步与补偿参数。
type Network struct {
	InterpDelayMs     int `toml:"interp_delay_ms"`     // 客户端插值缓冲
	PositionHistoryMs int `toml:"position_history_ms"` // 玩家位置环形缓冲时长
}

// Combat 是战斗判定参数。
type Combat struct {
	HitLagCompensation bool    `toml:"hit_lag_compensation"`
	HitLeniencyRadius  float64 `toml:"hit_leniency_radius"` // 米
	MaxBullets         int     `toml:"max_bullets"`
}

// AI 是怪物决策频率参数。
type AI struct {
	AggroRescoreIntervalS float64 `toml:"aggro_rescore_interval_s"`
	PathRecalcIntervalS   float64 `toml:"path_recalc_interval_s"`
}

// Space 是两套格子各自的参数。
//
// 注意这是两个独立的格子：地形位图决定"哪块地能走"，空间索引只用来加速
// "附近有哪些实体"的查询。混用会让两边的精度互相拖累。
type Space struct {
	SpatialCellSize       float64 `toml:"spatial_cell_size"`       // 米，空间索引格
	SpatialBruteThreshold int     `toml:"spatial_brute_threshold"` // 实体数低于此值改用暴力遍历
	TerrainCellSize       float64 `toml:"terrain_cell_size"`       // 米，地形位图格
}

// World 是世界尺度参数。
type World struct {
	TerrainWidth  int `toml:"terrain_width"`  // 地形格数
	TerrainHeight int `toml:"terrain_height"` // 地形格数
	MaxPlayers    int `toml:"max_players"`
}

// Log 是日志参数。
type Log struct {
	Level  string `toml:"level"`  // debug / info / warn / error
	Format string `toml:"format"` // text / json
}

// Default 返回一份可直接运行的默认配置。
//
// 这些数值来自 docs/combat-design-v0.1.md §7 的参数汇总表，改动前请先改文档。
func Default() *Config {
	return &Config{
		Server: Server{
			Name:     "koukoukongjian",
			Bind:     "0.0.0.0:7777",
			TickRate: 20,
			SubSteps: 3,
		},
		Network: Network{
			InterpDelayMs:     100,
			PositionHistoryMs: 500,
		},
		Combat: Combat{
			HitLagCompensation: true,
			HitLeniencyRadius:  0.15,
			MaxBullets:         512,
		},
		AI: AI{
			AggroRescoreIntervalS: 0.5,
			PathRecalcIntervalS:   0.3,
		},
		Space: Space{
			SpatialCellSize:       2.0,
			SpatialBruteThreshold: 200,
			TerrainCellSize:       1.0,
		},
		World: World{
			TerrainWidth:  128,
			TerrainHeight: 128,
			MaxPlayers:    4,
		},
		Log: Log{
			Level:  "info",
			Format: "text",
		},
	}
}

// Load 读取 TOML 配置文件并与默认值合并。
//
// path 为空、或文件不存在时返回默认配置且 loaded 为空字符串——配置文件缺席
// 不是错误，这样第一次跑不必先准备文件。返回的 loaded 是实际读取的路径，
// 调用方据此决定日志措辞。
func Load(path string) (cfg *Config, loaded string, err error) {
	cfg = Default()
	if path == "" {
		return cfg, "", nil
	}

	if _, statErr := os.Stat(path); statErr != nil {
		if os.IsNotExist(statErr) {
			return cfg, "", nil
		}
		return nil, "", fmt.Errorf("stat config %q: %w", path, statErr)
	}

	meta, err := toml.DecodeFile(path, cfg)
	if err != nil {
		return nil, "", fmt.Errorf("decode config %q: %w", path, err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		// 未知键几乎总意味着拼写错误，而拼错的键会静默保留默认值——
		// 于是"我明明改了配置却不生效"这类问题极难查。直接报错更好。
		return nil, "", fmt.Errorf("config %q has unknown keys: %v", path, undecoded)
	}

	return cfg, path, nil
}

// Validate 检查参数是否在自洽范围内。返回的错误可以直接展示给运维。
func (c *Config) Validate() error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if c.Server.TickRate < 1 || c.Server.TickRate > 240 {
		add("server.tick_rate = %d，必须在 1..240", c.Server.TickRate)
	}
	if c.Server.SubSteps < 1 || c.Server.SubSteps > 16 {
		add("server.sub_steps = %d，必须在 1..16", c.Server.SubSteps)
	}
	if hz := c.Server.TickRate * c.Server.SubSteps; hz > 240 {
		add("server.tick_rate × sub_steps = %d Hz，超过 240 Hz 上限", hz)
	}
	if c.Server.Bind == "" {
		add("server.bind 不能为空")
	}

	if c.Network.InterpDelayMs < 0 {
		add("network.interp_delay_ms = %d，不能为负", c.Network.InterpDelayMs)
	}
	if c.Network.PositionHistoryMs < 0 {
		add("network.position_history_ms = %d，不能为负", c.Network.PositionHistoryMs)
	}

	if c.Combat.HitLeniencyRadius < 0 {
		add("combat.hit_leniency_radius = %v，不能为负", c.Combat.HitLeniencyRadius)
	}
	if c.Combat.MaxBullets < 1 || c.Combat.MaxBullets > 4096 {
		add("combat.max_bullets = %d，必须在 1..4096（实体索引只有 12 位）", c.Combat.MaxBullets)
	}

	if c.AI.AggroRescoreIntervalS <= 0 {
		add("ai.aggro_rescore_interval_s = %v，必须为正", c.AI.AggroRescoreIntervalS)
	}
	if c.AI.PathRecalcIntervalS <= 0 {
		add("ai.path_recalc_interval_s = %v，必须为正", c.AI.PathRecalcIntervalS)
	}

	if c.Space.SpatialCellSize <= 0 {
		add("space.spatial_cell_size = %v，必须为正", c.Space.SpatialCellSize)
	}
	if c.Space.TerrainCellSize <= 0 {
		add("space.terrain_cell_size = %v，必须为正", c.Space.TerrainCellSize)
	}
	if c.Space.SpatialBruteThreshold < 0 {
		add("space.spatial_brute_threshold = %d，不能为负", c.Space.SpatialBruteThreshold)
	}

	if c.World.MaxPlayers < 1 || c.World.MaxPlayers > 4 {
		add("world.max_players = %d，必须在 1..4", c.World.MaxPlayers)
	}
	if c.World.TerrainWidth < 1 || c.World.TerrainWidth > 1024 {
		add("world.terrain_width = %d，必须在 1..1024", c.World.TerrainWidth)
	}
	if c.World.TerrainHeight < 1 || c.World.TerrainHeight > 1024 {
		add("world.terrain_height = %d，必须在 1..1024", c.World.TerrainHeight)
	}

	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		add("log.level = %q，必须是 debug / info / warn / error", c.Log.Level)
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		add("log.format = %q，必须是 text / json", c.Log.Format)
	}

	if len(problems) == 0 {
		return nil
	}

	msg := "配置校验失败："
	for _, p := range problems {
		msg += "\n  - " + p
	}
	return fmt.Errorf("%s", msg)
}

// TickInterval 是逻辑 tick 的周期。
func (c *Config) TickInterval() time.Duration {
	return time.Second / time.Duration(c.Server.TickRate)
}

// SubDt 是单个物理子步的时长，单位秒。
//
// 刻意返回固定的浮点值（1/(tickRate×subSteps)），而不是测量真实耗时——
// 用真实耗时会让模拟结果随机器负载变化，回放就对不上了。
func (c *Config) SubDt() float64 {
	return 1.0 / float64(c.Server.TickRate*c.Server.SubSteps)
}

// SubInterval 是单个物理子步的时长。
func (c *Config) SubInterval() time.Duration {
	return c.TickInterval() / time.Duration(c.Server.SubSteps)
}
