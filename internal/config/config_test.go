package config

import (
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

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
