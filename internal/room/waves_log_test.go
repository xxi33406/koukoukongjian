package room

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"koukoukongjian/internal/game/systems"
	"koukoukongjian/internal/game/world"
)

// logCapture 是把 slog 记录收进内存的 Handler。
//
// 为什么用 Handler 捕获而不是断言 hubStates 字段：hubStates 只在 tick
// goroutine 里读写（AGENTS §2.5），测试去读它就是数据竞争——-race 会报，
// 而那个报告和"日志接没接上"毫无关系。捕获日志则验证的是**最终可观测
// 的行为**：仓库承诺「波次事件只进 room 日志」（waves.go 文件头），承诺
// 兑现的标志就是真有日志出来。
type logCapture struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Record 内部的切片由调用方复用，必须先 Clone 再存（AGENTS §2.6
	// 「切片赋值是引用」的同款陷阱）。
	c.recs = append(c.recs, r.Clone())
	return nil
}

func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

// find 返回 msg 匹配的全部记录（快照语义，返回副本）。
func (c *logCapture) find(msg string) []slog.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []slog.Record
	for _, r := range c.recs {
		if r.Message == msg {
			out = append(out, r)
		}
	}
	return out
}

// attrInt 取记录里某个整型属性；不存在返回 -1。
func attrInt(r slog.Record, key string) int {
	got := -1
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			got = int(a.Value.Int64())
			return false
		}
		return true
	})
	return got
}

// waitForLog 轮询等待出现 msg 日志，超时即失败。
//
// 用轮询而不是固定 sleep：tick 周期在 CI 上会被调度抖动拉长，固定 sleep
// 要么偶尔假失败，要么白等一大截（AGENTS §2.7）。
func waitForLog(t *testing.T, c *logCapture, msg string, timeout time.Duration) []slog.Record {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if recs := c.find(msg); len(recs) > 0 {
			return recs
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("超时 %v 未等到日志 %q（已捕获 %d 条记录）", timeout, msg, len(c.find("")))
	return nil
}

// TestTickLogsWaveStart 钉死「tick 里真的会为开波打日志」。
//
// 根因（M3 审查）：waves.go 文件头承诺「波次事件只进 room 日志」，但
// game 包不许依赖 log，签名又无返回值，这句承诺从未被兑现——全文件
// 没有一个 log. 调用。注释写成契约、没人验证契约，等于没有契约。
//
// 这条测试故意走**真实 tick 循环**而不是直接调 helper：漏掉的接线（tick
// 里忘了调比较函数）正是最容易发生的错法，手工复刻 tick 顺序的测试抓不到。
func TestTickLogsWaveStart(t *testing.T) {
	capture := &logCapture{}
	logger := slog.New(capture)
	cfg := testConfig()

	r := New(cfg, logger, Options{ID: "wavelog", Seed: 7, WavesEnabled: true})
	if len(r.World.Hubs) == 0 {
		t.Fatal("测试配置应生成带据点的地图")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("房间未在 2 秒内退出")
		}
	})

	recs := waitForLog(t, capture, "据点开波", 2*time.Second)

	// 开局第一波：每个据点恰好一条，wave 必须是 1。
	// 多于一条说明比较基准没被更新（每 tick 都当成"刚开波"，日志刷屏）；
	// 少于一个据点说明循环漏了据点。
	seen := map[int]bool{}
	for _, rec := range recs {
		hub := attrInt(rec, "hub")
		if hub < 0 {
			t.Fatalf("开波日志缺 hub 属性：%v", rec.Message)
		}
		if w := attrInt(rec, "wave"); w != 1 {
			t.Fatalf("hub %d 的开波日志 wave=%d，期望 1", hub, w)
		}
		if seen[hub] {
			t.Fatalf("hub %d 重复打了两条开波日志（比较基准未更新）", hub)
		}
		seen[hub] = true
	}
	if len(seen) != len(r.World.Hubs) {
		// 等几 tick 让其余据点也刷出来再看一眼：所有据点都在同一 tick
		// 开波（HubIdle 立即 startWave），所以这里理论上不该不足。
		t.Fatalf("开波日志覆盖 %d 个据点，期望 %d 个", len(seen), len(r.World.Hubs))
	}
}

// TestLogHubChangesCleared 覆盖「清场」分支。
//
// 同步单测、不起 tick 循环：要精确控制"最后一波被打完"只能手动杀怪，
// 而房间跑起来后 World 归 tick goroutine 独占（AGENTS §2.5），从测试
// goroutine 杀怪就是数据竞争。这里按 tick 里的同一顺序手工调三个函数，
// 验的是「状态变化 → 日志」这段逻辑本身；接线由上面那条集成测试负责。
func TestLogHubChangesCleared(t *testing.T) {
	capture := &logCapture{}
	cfg := testConfig()
	// 每个据点只刷一波：清掉第一波就是 HubCleared，不必手工推波。
	cfg.Waves.WavesPerHub = 1

	r := New(cfg, slog.New(capture), Options{ID: "waveclear", Seed: 7, WavesEnabled: true})
	if len(r.World.Hubs) == 0 {
		t.Fatal("测试配置应生成带据点的地图")
	}

	// —— 第 1 步：开波 ——
	r.recordHubStates()
	systems.UpdateWaves(r.World)
	r.logHubChanges()

	// —— 第 2 步：把每一波的怪全杀掉（World 尚未被别的 goroutine 持有）——
	for i := range r.World.Hubs {
		hub := &r.World.Hubs[i]
		if hub.State != world.HubFighting {
			t.Fatalf("据点 %d 状态 = %v，期望 HubFighting", i, hub.State)
		}
		if hub.WaveN == 0 {
			t.Fatalf("据点 %d 开波后 WaveN = 0", i)
		}
		for _, id := range hub.WaveIDs[:hub.WaveN] {
			r.World.Monsters.Kill(id)
		}
	}

	// —— 第 3 步：推到清场 ——
	r.recordHubStates()
	systems.UpdateWaves(r.World)
	r.logHubChanges()

	cleared := capture.find("据点清场")
	if len(cleared) != len(r.World.Hubs) {
		t.Fatalf("清场日志 %d 条，期望 %d 条（每个据点一条）", len(cleared), len(r.World.Hubs))
	}

	// —— 第 4 步：再推一 tick，终态不该重复打日志 ——
	r.recordHubStates()
	systems.UpdateWaves(r.World)
	r.logHubChanges()
	if n := len(capture.find("据点清场")); n != len(r.World.Hubs) {
		t.Fatalf("清场是终态，日志不该重复：%d 条，期望 %d 条", n, len(r.World.Hubs))
	}
	// 也不该冒出开波日志（HubCleared → 不再刷）。
	if n := len(capture.find("据点开波")); n != len(r.World.Hubs) {
		t.Fatalf("清场后仍在刷波：开波日志 %d 条，期望 %d 条", n, len(r.World.Hubs))
	}
}

// TestHubStatesGrowsWithHubs 钉死缓冲容量契约。
//
// hubStates 长度必须等于 len(Hubs)：长度对不上就按下标比较，多出来的
// 据点永远检测不到变化、少的话会读越界。这里用 config 层的两个据点数目
// 变体验证它随据点数量重建。
func TestHubStatesGrowsWithHubs(t *testing.T) {
	for _, hubs := range []int{2, 7} {
		cfg := testConfig()
		cfg.Map.HubCount = hubs
		r := New(cfg, quietLogger(), Options{ID: "hubstates", Seed: 11})
		if got := len(r.World.Hubs); got != hubs {
			t.Fatalf("HubCount=%d 时生成 %d 个据点", hubs, got)
		}
		r.recordHubStates()
		if got := len(r.hubStates); got != hubs {
			t.Fatalf("hubStates 长度 = %d，期望 %d", got, hubs)
		}
	}
}
