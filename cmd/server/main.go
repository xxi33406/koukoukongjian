// Command server 是 koukoukongjian 的权威游戏服务器。
//
// M0 阶段的职责：把配置、日志、房间 tick 循环串起来，并证明整个骨架能跑、
// 能优雅退出。网络层（M1）会在 Room 之上接入，不改动这里的结构。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/obs"
	"koukoukongjian/internal/room"
)

// version 由构建时注入，本地 go run 时保持 unknown。
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "启动失败：%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		cfgPath  = flag.String("config", "configs/server.toml", "TOML 配置文件路径（不存在时用内置默认值）")
		seed     = flag.Int64("seed", 0, "世界随机种子，0 表示用当前时间")
		duration = flag.Duration("duration", 0, "运行时长，0 表示一直运行（用于冒烟测试）")
		logLevel = flag.String("log-level", "", "覆盖配置里的日志级别")
	)
	flag.Parse()

	cfg, loaded, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *logLevel != "" {
		cfg.Log.Level = *logLevel
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	log, levelVar, err := obs.Setup(os.Stdout, cfg.Log.Level, cfg.Log.Format)
	if err != nil {
		return err
	}
	_ = levelVar // 留待将来做运行期调级

	if loaded != "" {
		log.Info("配置已加载", "path", loaded)
	} else {
		log.Info("使用内置默认配置", "path", *cfgPath)
	}

	// 种子固定才能复现。0 表示"随机"，显式打出来是为了让回放可寻回。
	s := *seed
	if s == 0 {
		s = nowSeed()
	}

	r := room.New(cfg, log, room.Options{ID: "room-1", Seed: s})
	log.Info("服务器就绪",
		"version", version,
		"tick_interval_ms", cfg.TickInterval().Milliseconds(),
		"sub_dt", cfg.SubDt(),
		"world_m", fmt.Sprintf("%.0f×%.0f", r.World.Terrain.WorldWidth(), r.World.Terrain.WorldHeight()),
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	// 兜底：即使信号没被捕获也要退出。DSH/CI 里 Kill 掉父进程时子进程可能
	// 收不到 SIGTERM，硬超时能避免进程僵在那里。
	r.Run(ctx)

	m := r.Metrics()
	log.Info("退出统计",
		"ticks", m.Ticks,
		"overruns", m.Overruns,
		"last_tick_ms", float64(m.LastTickNs)/1e6,
		"rng_calls", r.World.RNGCalls(),
		"world_tick", r.World.Tick,
	)
	return nil
}

// nowSeed 生成一个可寻回的种子。
func nowSeed() int64 { return time.Now().UnixNano() }
