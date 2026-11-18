package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultIsValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("默认配置必须自洽，却有错：%v", err)
	}
}

func TestDefaultKeyValues(t *testing.T) {
	cfg := Default()

	// 这些数字是文档拍过板的，改它们等于改设计，测试在这里挡住手滑。
	if cfg.Server.TickRate != 20 {
		t.Errorf("TickRate = %d，文档定的是 20", cfg.Server.TickRate)
	}
	if cfg.Server.SubSteps != 3 {
		t.Errorf("SubSteps = %d，文档定的是 3", cfg.Server.SubSteps)
	}
	if cfg.Network.InterpDelayMs != 100 {
		t.Errorf("InterpDelayMs = %d，文档定的是 100", cfg.Network.InterpDelayMs)
	}
	if cfg.Network.PositionHistoryMs != 500 {
		t.Errorf("PositionHistoryMs = %d，文档定的是 500", cfg.Network.PositionHistoryMs)
	}
	if !cfg.Combat.HitLagCompensation {
		t.Error("受击宽容默认必须开启")
	}
	if cfg.Combat.HitLeniencyRadius != 0.15 {
		t.Errorf("HitLeniencyRadius = %v，文档定的是 0.15", cfg.Combat.HitLeniencyRadius)
	}
	if cfg.World.TerrainWidth != 128 || cfg.World.TerrainHeight != 128 {
		t.Errorf("默认地形 %d×%d，文档定的是 128×128", cfg.World.TerrainWidth, cfg.World.TerrainHeight)
	}
	if cfg.World.MaxPlayers != 4 {
		t.Errorf("MaxPlayers = %d，设计要求 4 人", cfg.World.MaxPlayers)
	}
	if cfg.Space.SpatialBruteThreshold != 200 {
		t.Errorf("SpatialBruteThreshold = %d，实测结论是 200", cfg.Space.SpatialBruteThreshold)
	}
}

func TestTickInterval(t *testing.T) {
	cfg := Default()
	if got := cfg.TickInterval(); got != 50*time.Millisecond {
		t.Fatalf("TickInterval = %v，20Hz 应为 50ms", got)
	}
	if got := cfg.SubInterval(); got != 50*time.Millisecond/3 {
		t.Fatalf("SubInterval = %v", got)
	}
}

// SubDt 必须用配置算出固定步长，不能用真实耗时——否则模拟不可复现。
func TestSubDtIsFixedTimestep(t *testing.T) {
	cfg := Default()
	want := 1.0 / (20.0 * 3.0)
	if got := cfg.SubDt(); abs(got-want) > 1e-12 {
		t.Fatalf("SubDt = %v，期望 %v", got, want)
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	cfg, loaded, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("文件不存在不应报错，得到 %v", err)
	}
	if loaded != "" {
		t.Fatalf("loaded 应为空，得到 %q", loaded)
	}
	if cfg.Server.TickRate != 20 {
		t.Fatalf("应返回默认值")
	}
}

func TestLoadEmptyPathReturnsDefaults(t *testing.T) {
	cfg, loaded, err := Load("")
	if err != nil {
		t.Fatalf("空路径不应报错，得到 %v", err)
	}
	if loaded != "" || cfg == nil {
		t.Fatal("空路径应返回默认配置")
	}
}

func TestLoadOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.toml")
	// 只写一个键，其余应保持默认。
	if err := os.WriteFile(path, []byte("[server]\ntick_rate = 30\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, loaded, err := Load(path)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if loaded != path {
		t.Fatalf("loaded = %q，期望 %q", loaded, path)
	}
	if cfg.Server.TickRate != 30 {
		t.Fatalf("TickRate = %d，期望 30", cfg.Server.TickRate)
	}
	// 未提及的键必须保留默认值，而不是变成零值。
	if cfg.Server.SubSteps != 3 {
		t.Fatalf("未覆盖的 SubSteps 变成了 %d", cfg.Server.SubSteps)
	}
	if cfg.Network.InterpDelayMs != 100 {
		t.Fatalf("未覆盖的 InterpDelayMs 变成了 %d", cfg.Network.InterpDelayMs)
	}
}

// 拼错的键必须报错：静默保留默认值会让人以为配置生效了。
func TestLoadRejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(path, []byte("[server]\ntick_rat = 30\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := Load(path); err == nil {
		t.Fatal("未知键必须报错")
	}
}

func TestLoadRejectsBadTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(path, []byte("[server\ntick_rate = = 30\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path); err == nil {
		t.Fatal("语法错误必须报错")
	}
}

func TestValidateCollectsAllErrors(t *testing.T) {
	cfg := Default()
	cfg.Server.TickRate = 0
	cfg.Server.SubSteps = 0
	cfg.World.MaxPlayers = 0

	err := cfg.Validate()
	if err == nil {
		t.Fatal("应报错")
	}
	msg := err.Error()
	// 一次报全，不要让人改一个跑一次。
	for _, want := range []string{"tick_rate", "sub_steps", "max_players"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误信息里缺少 %q：%s", want, msg)
		}
	}
}

func TestValidateRejectsNegativeAndAbsurd(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"负的插值延迟", func(c *Config) { c.Network.InterpDelayMs = -1 }, "interp_delay_ms"},
		{"零精度网格", func(c *Config) { c.Space.SpatialCellSize = 0 }, "spatial_cell_size"},
		{"负的地形尺寸", func(c *Config) { c.World.TerrainWidth = -5 }, "terrain_width"},
		{"超过上限的玩家数", func(c *Config) { c.World.MaxPlayers = 9 }, "max_players"},
		{"未知日志级别", func(c *Config) { c.Log.Level = "verbose" }, "log.level"},
		{"未知日志格式", func(c *Config) { c.Log.Format = "yaml" }, "log.format"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mut(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("%s 应被拒绝", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应提到 %q，实际：%v", tc.want, err)
			}
		})
	}
}

// 4 人是设计上限（spec 写的"人数上限 4"），5 人必须被拒。
func TestValidateMaxPlayersCeiling(t *testing.T) {
	cfg := Default()
	cfg.World.MaxPlayers = 4
	if err := cfg.Validate(); err != nil {
		t.Fatalf("4 人应通过：%v", err)
	}
	cfg.World.MaxPlayers = 5
	if err := cfg.Validate(); err == nil {
		t.Fatal("5 人应被拒绝")
	}
}

func TestDefaultMapFogWavesMonster(t *testing.T) {
	c := Default()
	if c.Map.HubCount != 7 {
		t.Errorf("HubCount = %d, 期望 7", c.Map.HubCount)
	}
	if c.Map.HubRadiusCells != 5 {
		t.Errorf("HubRadiusCells = %d, 期望 5", c.Map.HubRadiusCells)
	}
	if c.Map.CorridorWidth != 2 {
		t.Errorf("CorridorWidth = %d, 期望 2", c.Map.CorridorWidth)
	}
	if c.Map.HubMinDistCells != 20 {
		t.Errorf("HubMinDistCells = %d, 期望 20", c.Map.HubMinDistCells)
	}
	if c.Fog.RevealRadius != 8.0 {
		t.Errorf("RevealRadius = %v, 期望 8.0", c.Fog.RevealRadius)
	}
	if c.Waves.WavesPerHub != 3 || c.Waves.MonstersPerWave != 4 {
		t.Errorf("Waves = %+v, 期望 3 波 × 4 怪", c.Waves)
	}
	if c.Waves.InterWaveDelayS != 1.0 {
		t.Errorf("InterWaveDelayS = %v, 期望 1.0", c.Waves.InterWaveDelayS)
	}
	m := c.Monster
	if m.Speed != 2.5 || m.HP != 50 || m.AttackDamage != 10 {
		t.Errorf("Monster = %+v, 期望 Speed 2.5 / HP 50 / Damage 10（spec：怪物 50 血 10 攻）", m)
	}
	if m.AttackRange != 1.2 || m.AttackCooldownS != 1.0 {
		t.Errorf("Monster = %+v, 期望 AttackRange 1.2 / Cooldown 1.0", m)
	}
	if m.FastChaseDist != 15.0 || m.FastChaseMul != 1.5 {
		t.Errorf("Monster = %+v, 期望 FastChaseDist 15 / Mul 1.5", m)
	}
	if m.SightRange != 20.0 {
		t.Errorf("SightRange = %v, 期望 20.0", m.SightRange)
	}
}

func TestValidateRejectsBadMapMonster(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Config)
	}{
		{"HubCount 超上限", func(c *Config) { c.Map.HubCount = 99 }},
		{"HubCount 为负", func(c *Config) { c.Map.HubCount = -1 }}, // 0 是合法哨兵：测试用空白世界（Task 3 定稿）
		{"据点最小距离不足（重叠）", func(c *Config) { c.Map.HubMinDistCells = 5; c.Map.HubRadiusCells = 5 }},
		{"走廊宽超上限", func(c *Config) { c.Map.CorridorWidth = 9 }},
		{"据点半径过小", func(c *Config) { c.Map.HubRadiusCells = 1 }},
		{"迷雾半径过小", func(c *Config) { c.Fog.RevealRadius = 0.5 }},
		{"迷雾半径为 NaN", func(c *Config) { c.Fog.RevealRadius = math.NaN() }}, // NaN 穿过一切区间比较，必须单独拦
		{"怪物速度为 NaN", func(c *Config) { c.Monster.Speed = math.NaN() }},
		{"怪物血量为 Inf", func(c *Config) { c.Monster.HP = math.Inf(1) }},
		{"怪物速度为负", func(c *Config) { c.Monster.Speed = -1 }},
		{"追赶倍率小于 1", func(c *Config) { c.Monster.FastChaseMul = 0.9 }},
		{"每波怪数超上限", func(c *Config) { c.Waves.MonstersPerWave = 33 }},
		{"波间隔为负", func(c *Config) { c.Waves.InterWaveDelayS = -0.1 }},
		{"视野距离过小", func(c *Config) { c.Monster.SightRange = 0.5 }},
		// 以下五项是 M4 Task 2 实测发现的漏网字段：它们有区间校验，
		// 但 NaN 对一切区间比较恒为假，等于裸奔。没有这组负例，把
		// addNaN 那五行删掉不会有任何测试变红。
		{"命中容差半径为 NaN", func(c *Config) { c.Combat.HitLeniencyRadius = math.NaN() }},
		{"仇恨重算间隔为 Inf", func(c *Config) { c.AI.AggroRescoreIntervalS = math.Inf(1) }},
		{"路径重算间隔为 NaN", func(c *Config) { c.AI.PathRecalcIntervalS = math.NaN() }},
		{"空间格尺寸为 NaN", func(c *Config) { c.Space.SpatialCellSize = math.NaN() }},
		{"地形格尺寸为 Inf", func(c *Config) { c.Space.TerrainCellSize = math.Inf(-1) }},
	}
	for _, tc := range cases {
		c := Default()
		tc.mut(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s：Validate 应报错，却返回 nil", tc.name)
		}
	}
}

func TestValidateAcceptsHubCountZeroSentinel(t *testing.T) {
	// HubCount==0 是"空白世界"哨兵（Task 3 所有测试 helper 靠它关地图）。
	// 没有这条正向断言，将来有人把校验"修"回 <1 时不会有任何测试变红，
	// 哨兵静默失效、Task 3 的地基塌掉。负例（-1/99）在 TestValidateRejectsBadMapMonster 里。
	c := Default()
	c.Map.HubCount = 0
	if err := c.Validate(); err != nil {
		t.Errorf("HubCount=0（关地图哨兵）应通过 Validate，却报错：%v", err)
	}
}

// M4 升级曲线默认值。ExpBase=20 的依据是 spec L105「第一关第一个据点就能
// 升一级」：一个据点 3 波 × 4 怪 = 12 只，KillExp=8 合计 96 点，远超首级
// 所需 20 点——曲线起点必须很低，否则第一条"升一级"的正反馈会被推迟。
func TestDefaultProgression(t *testing.T) {
	c := Default()
	if c.Progression.ExpBase != 20 {
		t.Errorf("ExpBase = %v, 期望 20", c.Progression.ExpBase)
	}
	if c.Progression.ExpGrowth != 1.35 {
		t.Errorf("ExpGrowth = %v, 期望 1.35", c.Progression.ExpGrowth)
	}
	if c.Progression.MaxLevel != 20 {
		t.Errorf("MaxLevel = %d, 期望 20", c.Progression.MaxLevel)
	}
	if c.Progression.SkillChoiceWindowS != 5 {
		t.Errorf("SkillChoiceWindowS = %v, 期望 5", c.Progression.SkillChoiceWindowS)
	}
	if c.Progression.KillExp != 8 {
		t.Errorf("KillExp = %d, 期望 8", c.Progression.KillExp)
	}
}

// 每个非法字段单独一条负例：一条用例同时改多个字段，就分不清是哪条校验
// 生效了（某条校验被删掉，别的字段仍让整体报错，测试照样绿）。
func TestValidateRejectsBadProgression(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"ExpBase 为 0", func(c *Config) { c.Progression.ExpBase = 0 }, "progression.exp_base"},
		{"ExpBase 为负", func(c *Config) { c.Progression.ExpBase = -1 }, "progression.exp_base"},
		{"ExpBase 为 NaN", func(c *Config) { c.Progression.ExpBase = math.NaN() }, "progression.exp_base"},
		// ExpGrowth=1 合法（线性），<1 意味着越升越快，与"升级频率递减"的设计目标相反。
		{"ExpGrowth 小于 1", func(c *Config) { c.Progression.ExpGrowth = 0.9 }, "progression.exp_growth"},
		{"ExpGrowth 为 NaN", func(c *Config) { c.Progression.ExpGrowth = math.NaN() }, "progression.exp_growth"},
		{"MaxLevel 为 0", func(c *Config) { c.Progression.MaxLevel = 0 }, "progression.max_level"},
		// Level 字段是 uint8（上限 255）；设计上 20 级封顶，99 是"明显过大"的护栏。
		{"MaxLevel 超上限", func(c *Config) { c.Progression.MaxLevel = 100 }, "progression.max_level"},
		{"SkillChoiceWindowS 为 0", func(c *Config) { c.Progression.SkillChoiceWindowS = 0 }, "progression.skill_choice_window_s"},
		{"SkillChoiceWindowS 为 NaN", func(c *Config) { c.Progression.SkillChoiceWindowS = math.NaN() }, "progression.skill_choice_window_s"},
		{"KillExp 为 0", func(c *Config) { c.Progression.KillExp = 0 }, "progression.kill_exp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mut(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("%s 应被拒绝，Validate 却返回 nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应提到 %q，实际：%v", tc.want, err)
			}
		})
	}
}

// 边界正向：ExpGrowth 恰好等于 1 是合法的线性曲线，MaxLevel=1 与 99 是
// 区间两端。没有这些正向断言，将来有人把比较写成 < / > 写反方向（把闭
// 区间改成开源区间）时不会有任何测试变红。
func TestValidateAcceptsProgressionBoundaries(t *testing.T) {
	cfg := Default()
	cfg.Progression.ExpGrowth = 1
	cfg.Progression.MaxLevel = 1
	if err := cfg.Validate(); err != nil {
		t.Fatalf("ExpGrowth=1 / MaxLevel=1 应通过，却报错：%v", err)
	}
	cfg.Progression.MaxLevel = 99
	if err := cfg.Validate(); err != nil {
		t.Fatalf("MaxLevel=99 应在 1..99 内，却报错：%v", err)
	}
}

// TOML 的 progression 段必须能被读到。与 TestLoadOverridesDefaults 同一手法。
func TestLoadProgressionFromTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.toml")
	body := "[progression]\n" +
		"exp_base = 35.5\n" +
		"exp_growth = 1.5\n" +
		"max_level = 12\n" +
		"skill_choice_window_s = 3.5\n" +
		"kill_exp = 11\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, loaded, err := Load(path)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if loaded != path {
		t.Fatalf("loaded = %q，期望 %q", loaded, path)
	}
	p := cfg.Progression
	// 期望值写成常量字面量，而不是再算一遍 TOML 里的数：重新算一遍等于把
	// 同一个错误写两遍，读成什么都会"一致"。
	if p.ExpBase != 35.5 || p.ExpGrowth != 1.5 || p.MaxLevel != 12 ||
		p.SkillChoiceWindowS != 3.5 || p.KillExp != 11 {
		t.Fatalf("Progression = %+v, 期望 {ExpBase:35.5 ExpGrowth:1.5 MaxLevel:12 WindowS:3.5 KillExp:11}", p)
	}
	// 未提及的其他组必须保持默认，不能被 DecodeFile 清成零值。
	if cfg.Server.TickRate != 20 || cfg.Monster.HP != 50 {
		t.Fatalf("其他组被清空了：tick_rate=%d monster.hp=%v", cfg.Server.TickRate, cfg.Monster.HP)
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// TestValidateRejectsBadFastChaseDist 钉死 monster.fast_chase_dist > 0。
//
// 根因（M3 审查 M1）：反风筝逻辑是 `if dist > FastChaseDist { 用 Speed×Mul }`
// （internal/game/ai/ai.go:157-162 chaseSpeed）。阈值 <= 0 时这个比较对任何
// 非零距离恒为真，怪物永远以加速档跑——「远程风筝怪」这个玩法直接消失，
// 而且不会有任何报错或异常日志，只是打起来感觉"怪怎么一直这么快"。
// 这是「用值推断状态」的近亲：0 既没被当成合法值校验，也没被当成禁用值
// 特判，于是它成了一个能通过全部校验、却让玩法失效的合法配置。
func TestValidateRejectsBadFastChaseDist(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Config)
	}{
		{"追赶距离为 0", func(c *Config) { c.Monster.FastChaseDist = 0 }},
		{"追赶距离为负", func(c *Config) { c.Monster.FastChaseDist = -1 }},
		{"追赶距离为 NaN", func(c *Config) { c.Monster.FastChaseDist = math.NaN() }},
		{"追赶距离为 Inf", func(c *Config) { c.Monster.FastChaseDist = math.Inf(1) }},
		// Inf 也必须拦：dist > +Inf 恒为假，等于反风筝永远不触发——
		// 与 <=0 是同一个坑的另一端，同一条校验顺带覆盖。
	}
	for _, tc := range cases {
		c := Default()
		tc.mut(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s：Validate 应报错，却返回 nil", tc.name)
		}
	}
}

// TestValidateAcceptsFastChaseDistAboveSight 是正向边界：追赶距离大于视野
// 是合法配置（怪物发现你的距离比它开始加速的距离还近，等于不会加速），
// 只是不好玩，不该被当作错误拒绝。
func TestValidateAcceptsFastChaseDistAboveSight(t *testing.T) {
	c := Default()
	c.Monster.FastChaseDist = c.Monster.SightRange + 10
	if err := c.Validate(); err != nil {
		t.Errorf("FastChaseDist 大于视野应通过 Validate，却报错：%v", err)
	}
}
