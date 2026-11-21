package room

import (
	"testing"
	"time"
)

// TestTickHistogramPercentiles 钉住分位数计算。
func TestTickHistogramPercentiles(t *testing.T) {
	var h tickHistogram
	// 999 个 1ms + 1 个 100ms：p99 应当落在 1ms 桶附近，
	// p100 必须是 100ms 那一桶。
	for i := 0; i < 999; i++ {
		h.observe(time.Millisecond)
	}
	h.observe(100 * time.Millisecond)

	if got := h.percentile(0.50); got < 900*time.Microsecond || got > 1100*time.Microsecond {
		t.Fatalf("p50 = %v，期望约 1ms", got)
	}
	// 1000 个样本的 99 分位是第 990 个，仍落在 1ms 桶（1ms 有 999 个）。
	if got := h.percentile(0.99); got > 2*time.Millisecond {
		t.Fatalf("p99 = %v，期望 ≤2ms（1000 个样本里 99 分位在 1ms 桶）", got)
	}
	if got := h.percentile(1.0); got < 90*time.Millisecond {
		t.Fatalf("p100 = %v，期望接近 100ms", got)
	}
}

// TestTickHistogramPercentileClamps 钉住越界参数不 panic、超长耗时不越界。
func TestTickHistogramPercentileClamps(t *testing.T) {
	var h tickHistogram
	if got := h.percentile(0.5); got != 0 {
		t.Fatalf("空直方图的 p50 = %v，期望 0", got)
	}
	h.observe(time.Millisecond)

	// 1 秒远超 256 桶的覆盖范围，必须落在最后一桶而不是越界 panic。
	// 这条是变异逼出来的：删掉 observe 里的 idx 钳位后，只测 1ms 的用例
	// 跑不到那条路径，全绿。
	h.observe(time.Second)
	if got := h.percentile(1.0); got != 255*time.Millisecond {
		t.Fatalf("超长耗时后的 p100 = %v，期望钳到 255ms（最后一桶）", got)
	}

	// 越界参数不 panic。
	_ = h.percentile(-1)
	_ = h.percentile(2)
}

// TestTickHistogramPercentileExactBucket 钉住"样本数不是整百时也不落到桶 0"。
//
// want = uint64(p × total) 在 total=1、p=0.1 时算出来是 0，若不兜底成 1
// 会一路走到最后一个桶——那会让"唯一一个样本"的 p10 报成 255ms。
func TestTickHistogramPercentileExactBucket(t *testing.T) {
	var h tickHistogram
	h.observe(7 * time.Millisecond)
	if got := h.percentile(0.1); got != 7*time.Millisecond {
		t.Fatalf("单样本的 p10 = %v，期望 7ms", got)
	}
}

// TestMetricsSnapshotIncludesPercentiles 钉住对外结构带上了新字段。
func TestMetricsSnapshotIncludesPercentiles(t *testing.T) {
	r := New(testConfig(), quietLogger(), Options{ID: "m", Seed: 1})
	r.sampleTick(3 * time.Millisecond)
	m := r.Metrics()
	if m.P99TickNs <= 0 {
		t.Fatalf("Metrics.P99TickNs = %d，期望 > 0", m.P99TickNs)
	}
	if m.TickSamples != 1 {
		t.Fatalf("Metrics.TickSamples = %d，期望 1", m.TickSamples)
	}
}

// TestMetricsSnapshotWithoutSamples 钉住没采过样时返回零值而不是 nil 解引用。
func TestMetricsSnapshotWithoutSamples(t *testing.T) {
	r := New(testConfig(), quietLogger(), Options{ID: "m", Seed: 1})
	m := r.Metrics()
	if m.TickSamples != 0 || m.P99TickNs != 0 {
		t.Fatalf("无样本时 Metrics = %+v，期望全零", m)
	}
}

// TestSampleTickSnapshotsEveryHundred 钉住快照刷新频率（跨 goroutine 可见性）。
//
// 不每 tick 刷新是有意的：atomic.Pointer.Store 每次分配一个 2KB 结构，
// 20Hz 下每秒 40KB 垃圾。100 tick（5 秒）一次对"退出时看一眼分布"足够。
func TestSampleTickSnapshotsEveryHundred(t *testing.T) {
	r := New(testConfig(), quietLogger(), Options{ID: "m", Seed: 1})

	// 首个样本立即刷新：进程刚起来就报零会让「还没跑够 100 tick」与
	// 「指标没接上」看起来一样。
	r.sampleTick(time.Millisecond)
	if got := r.Metrics().TickSamples; got != 1 {
		t.Fatalf("首个样本后快照 TickSamples = %d，期望 1", got)
	}

	// 1~99 之间不再刷新。
	for i := 0; i < 89; i++ { // 累计 90 个
		r.sampleTick(time.Millisecond)
	}
	if got := r.Metrics().TickSamples; got != 1 {
		t.Fatalf("90 个样本时快照 TickSamples = %d，期望仍为 1（未到 100）", got)
	}

	// 到 100 时刷新。
	for i := 0; i < 10; i++ {
		r.sampleTick(time.Millisecond)
	}
	if got := r.Metrics().TickSamples; got != 100 {
		t.Fatalf("100 个样本时快照 TickSamples = %d，期望 100", got)
	}
}
