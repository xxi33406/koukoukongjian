// Command server 是 koukoukongjian 的权威游戏服务器。
//
// 结构上分三层：netx（UDP 传输）→ bridge（协议翻译）→ room（模拟与快照）。
// 三者的依赖是单向的，见各包的包注释。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/netx"
	"koukoukongjian/internal/obs"
	"koukoukongjian/internal/room"
	"koukoukongjian/internal/store"
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
		bind     = flag.String("bind", "", "覆盖配置里的监听地址，如 0.0.0.0:7777")
		noNet    = flag.Bool("no-net", false, "不启动网络层，只跑 tick 循环（M0 冒烟用）")

		dummyTargets = flag.Int("dummy-targets", 0, "开局摆放的静止靶数量（端到端验证用，0 表示不摆）")
		waves        = flag.Bool("waves", false, "启用据点波次（M3）；与 -dummy-targets 互斥，互斥裁定在 room.New 内")
		matchLog     = flag.String("match-log", "", "对局结果落盘路径（JSONL，每局追加一行），空表示不落盘")
	)
	flag.Parse()

	cfg, loaded, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *logLevel != "" {
		cfg.Log.Level = *logLevel
	}
	if *bind != "" {
		cfg.Server.Bind = *bind
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

	r := room.New(cfg, log, room.Options{ID: "room-1", Seed: s, DummyTargets: *dummyTargets, WavesEnabled: *waves})

	// 对局结果落盘（M7）。落盘不可用不该让服务器起不来：这是日志功能，
	// 不是启动的硬依赖，降级为不落盘并告警。
	if *matchLog != "" {
		mw, err := store.NewWriter(store.WriterOptions{
			Path: *matchLog,
			OnError: func(werr error) {
				log.Warn("对局结果写盘失败", "err", werr)
			},
		})
		if err != nil {
			log.Warn("对局结果落盘不可用，本次不记录", "path", *matchLog, "err", err)
		} else {
			// defer 而不是在退出统计前手动关：Close 要排空队列再刷盘，
			// 放在最外层 defer 保证"进程里的所有退出路径"都刷过。
			defer func() {
				if cerr := mw.Close(); cerr != nil {
					log.Warn("关闭落盘写入器失败", "err", cerr)
				} else if d := mw.Dropped(); d > 0 {
					log.Warn("有对局结果被丢弃（写盘队列满）", "dropped", d)
				}
			}()
			r.SetResultSink(mw)
			log.Info("对局结果将落盘", "path", *matchLog)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	// 房间在自己的 goroutine 里跑 tick 循环，主 goroutine 管网络与信号。
	var roomWG sync.WaitGroup
	roomWG.Add(1)
	go func() {
		defer roomWG.Done()
		r.Run(ctx)
	}()

	var srv *netx.Server
	var bridge *netBridge
	if !*noNet {
		u := netx.DefaultServerConfig()
		// 会话超时稍长于 3 个 tick：太短会让一次 GC 停顿被误判成掉线，
		// 太长则掉线玩家的鬼魂（M5）迟迟不出现。
		u.SessionTimeout = 5 * time.Second

		b := newNetBridge(ctx, r, cfg, log)
		// 注意变量名：这里绝不能叫 s——外层 s 是种子，遮蔽之后
		// 日志里的 seed 会变成整台服务器的结构体（真踩过）。
		listener, err := netx.Listen(cfg.Server.Bind, u, log, b)
		if err != nil {
			roomWG.Wait()
			return err
		}
		srv = listener
		bridge = b

		addr := cfg.Server.Bind
		if la := listener.LocalAddr(); la != nil {
			addr = la.String()
		}
		log.Info("服务器就绪",
			"version", version,
			"addr", addr,
			"room", r.ID,
			"tick_rate", cfg.Server.TickRate,
			"tick_interval_ms", cfg.TickInterval().Milliseconds(),
			"sub_dt", cfg.SubDt(),
			"world_m", fmt.Sprintf("%.0f×%.0f", r.World.Terrain.WorldWidth(), r.World.Terrain.WorldHeight()),
			"seed", s,
		)

		// 网络跑在独立 goroutine 里，这样 ctx 取消时两边都能干净退出。
		netErr := make(chan error, 1)
		go func() { netErr <- listener.Run(ctx) }()

		roomWG.Wait()
		// 房间停了，网络层也该停。ctx 已取消，最多等它一个读超时。
		if err := <-netErr; err != nil && !errors.Is(err, context.Canceled) {
			log.Warn("网络层退出", "err", err)
		}
	} else {
		log.Info("服务器就绪（无网络层）",
			"version", version,
			"room", r.ID,
			"tick_rate", cfg.Server.TickRate,
			"seed", s,
		)
		roomWG.Wait()
	}

	if bridge != nil {
		bridge.Wait()
	}

	m := r.Metrics()
	st := srvStats(srv)
	log.Info("退出统计",
		"ticks", m.Ticks,
		"overruns", m.Overruns,
		"last_tick_ms", float64(m.LastTickNs)/1e6,
		"rng_calls", r.World.RNGCalls(),
		"world_tick", r.World.Tick,
		"peers", r.PeerCount(),
		"recv", st.RecvTotal,
		"sent", st.SentTotal,
		"bad", st.RecvBad,
	)
	// 压测硬指标（M7 §11.5 拍板决定 #5）：overruns == 0 且 p99 < 50ms。
	// 单独一行而不是并进上面的"退出统计"：那一行是运维排障用的常规字段，
	// 这一行是压测报告的取证行，两者生命周期不同（前者每次都看，后者
	// 只在验收/压测时看），混在一起会让"这行变长了"掩盖"多了新指标"。
	log.Info("tick 分位",
		"samples", m.TickSamples,
		"p50_ms", float64(m.P50TickNs)/1e6,
		"p99_ms", float64(m.P99TickNs)/1e6,
		"p100_ms", float64(m.P100TickNs)/1e6,
		"overruns", m.Overruns,
	)
	return nil
}

// srvStats 读取服务端统计，网络层没开时返回零值。
func srvStats(s *netx.Server) netx.ServerStats {
	if s == nil {
		return netx.ServerStats{}
	}
	return s.Stats()
}

// nowSeed 生成一个可寻回的种子。
func nowSeed() int64 { return time.Now().UnixNano() }
