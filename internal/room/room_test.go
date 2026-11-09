package room

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"koukoukongjian/internal/config"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testConfig() *config.Config {
	return config.Default()
}

// ctx 取消后 Run 必须返回，否则进程退不掉。
func TestRunStopsOnContextCancel(t *testing.T) {
	r := New(testConfig(), quietLogger(), Options{ID: "t1", Seed: 1})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	time.Sleep(120 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 Run 没有返回")
	}

	if r.Metrics().Ticks == 0 {
		t.Fatal("应该至少跑过一个 tick")
	}
}

// Stop() 也必须能让 Run 退出——网络层拆房间走的是这条路。
func TestRunStopsOnStop(t *testing.T) {
	r := New(testConfig(), quietLogger(), Options{ID: "t2", Seed: 2})

	done := make(chan struct{})
	go func() {
		r.Run(context.Background())
		close(done)
	}()

	time.Sleep(120 * time.Millisecond)
	r.Stop()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop 后 Run 没有返回")
	}
}

// 重复 Stop 不能 panic（close 已关闭的 channel 会 panic）。
func TestStopIsIdempotent(t *testing.T) {
	r := New(testConfig(), quietLogger(), Options{ID: "t3", Seed: 3})
	done := make(chan struct{})
	go func() {
		r.Run(context.Background())
		close(done)
	}()
	time.Sleep(60 * time.Millisecond)

	r.Stop()
	r.Stop()
	r.Stop()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run 没有返回")
	}
	// Run 返回之后再 Stop 也不能 panic。
	r.Stop()
}

// 频率必须准确：deadline 累加的设计要点就是这个。
//
// 20Hz 跑 500ms 应得到约 10 个 tick，容忍 ±3（CI 机器会抖）。
func TestTickRateIsAccurate(t *testing.T) {
	if testing.Short() {
		t.Skip("频率测试在 -short 下跳过")
	}

	cfg := testConfig()
	r := New(cfg, quietLogger(), Options{ID: "t4", Seed: 4})

	done := make(chan struct{})
	go func() {
		r.Run(context.Background())
		close(done)
	}()

	start := time.Now()
	time.Sleep(500 * time.Millisecond)
	r.Stop()
	<-done
	elapsed := time.Since(start)

	ticks := r.Metrics().Ticks
	want := uint64(elapsed.Seconds() * float64(cfg.Server.TickRate))

	// 最后一个 tick 之后立刻 Stop，所以实际值会略少于理论值。
	lo := want - 3
	hi := want + 1
	if ticks < lo || ticks > hi {
		t.Fatalf("%v 内跑了 %d 个 tick（20Hz 期望约 %d）", elapsed, ticks, want)
	}
}

// 空世界不该有任何超时；被计入 overrun 说明 tick 循环本身有问题。
func TestNoOverrunOnEmptyWorld(t *testing.T) {
	r := New(testConfig(), quietLogger(), Options{ID: "t5", Seed: 5})
	done := make(chan struct{})
	go func() {
		r.Run(context.Background())
		close(done)
	}()
	time.Sleep(300 * time.Millisecond)
	r.Stop()
	<-done

	m := r.Metrics()
	if m.Overruns != 0 {
		t.Fatalf("空世界出现 %d 次超时", m.Overruns)
	}
	if m.Ticks == 0 {
		t.Fatal("ticks 为 0")
	}
	// 单个空 tick 应是微秒级，超过 50ms 预算说明有阻塞。
	if m.LastTickNs > int64(50*time.Millisecond) {
		t.Fatalf("单 tick 耗时 %v，已超预算", time.Duration(m.LastTickNs))
	}
}

// World.Tick 必须跟着 tick 循环走，快照要用它做时间戳。
func TestWorldTickAdvances(t *testing.T) {
	r := New(testConfig(), quietLogger(), Options{ID: "t6", Seed: 6})
	done := make(chan struct{})
	go func() {
		r.Run(context.Background())
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	r.Stop()
	<-done

	if r.World.Tick == 0 {
		t.Fatal("World.Tick 没有推进")
	}
	// ticksRun 与 World.Tick 是同一件事的两种计数，必须相等。
	if uint64(r.World.Tick) != r.Metrics().Ticks {
		t.Fatalf("World.Tick = %d，ticksRun = %d，两者应一致",
			r.World.Tick, r.Metrics().Ticks)
	}
}

// 同一 seed 必须构造出同一世界，否则回放不可复现。
func TestSameSeedSameWorld(t *testing.T) {
	cfg := testConfig()
	a := New(cfg, quietLogger(), Options{ID: "a", Seed: 42})
	b := New(cfg, quietLogger(), Options{ID: "b", Seed: 42})

	if a.World.Terrain.Hash() != b.World.Terrain.Hash() {
		t.Fatal("同 seed 的地形哈希不同")
	}
	if a.Seed() != b.Seed() {
		t.Fatal("Seed 不一致")
	}
	if a.World.RandIntn(1000) != b.World.RandIntn(1000) {
		t.Fatal("同 seed 的第一个随机数不同")
	}
}

func TestNewAppliesOptions(t *testing.T) {
	cfg := testConfig()
	cfg.Server.SubSteps = 5

	r := New(cfg, quietLogger(), Options{ID: "custom", Seed: 99})
	if r.ID != "custom" {
		t.Fatalf("ID = %q", r.ID)
	}
	if r.Seed() != 99 {
		t.Fatalf("Seed = %d", r.Seed())
	}
	if r.subSteps != 5 {
		t.Fatalf("subSteps = %d，应从 config 读到 5", r.subSteps)
	}
	if r.tickInterval != cfg.TickInterval() {
		t.Fatalf("tickInterval = %v", r.tickInterval)
	}
	// 暴力阈值必须从配置同步到网格上，否则实测的 200 为界结论不生效。
	if r.World.Grid.BruteThreshold() != cfg.Space.SpatialBruteThreshold {
		t.Fatalf("网格阈值 = %d，配置是 %d",
			r.World.Grid.BruteThreshold(), cfg.Space.SpatialBruteThreshold)
	}
}

func TestNewDefaultsID(t *testing.T) {
	r := New(testConfig(), nil, Options{})
	if r.ID == "" {
		t.Fatal("空 ID 应被填上默认值")
	}
	// log 为 nil 时应回落到 slog.Default()，不能 panic。
	r.log.Info("smoke")
}

// Run 返回后 stopped 必须为真，否则 Stop 会重复 close。
func TestStoppedFlagAfterRun(t *testing.T) {
	r := New(testConfig(), quietLogger(), Options{ID: "t7", Seed: 7})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消
	r.Run(ctx)

	if !r.stopped.Load() {
		t.Fatal("Run 返回后 stopped 应为真")
	}
}

// 日志写入出错不能把 tick 循环带崩。
func TestRunSurvivesLoggerErrors(t *testing.T) {
	bad := slog.New(slog.NewTextHandler(errWriter{}, nil))
	r := New(testConfig(), bad, Options{ID: "t8", Seed: 8})

	done := make(chan struct{})
	go func() {
		r.Run(context.Background())
		close(done)
	}()
	time.Sleep(120 * time.Millisecond)
	r.Stop()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("日志写失败导致 Run 卡住")
	}
}

type errWriter struct{}

func (errWriter) Write(p []byte) (int, error) {
	return 0, errors.New("boom")
}
