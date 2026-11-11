package main

import (
	"testing"
	"time"
)

// TestLimiterBurstThenBlock 验证突发额度用完后开始拒绝。
//
// 令牌桶装满能放 inputBurst 个包，第 inputBurst+1 个必须被拒——
// 否则额度形同虚设，洪水客户端照样能挤掉别人。
func TestLimiterBurstThenBlock(t *testing.T) {
	var l inputLimiter
	now := time.Unix(1000, 0)

	for i := 0; i < int(inputBurst); i++ {
		if !l.allow(now) {
			t.Fatalf("第 %d 个包被拒，突发额度 %v 还没用完", i+1, inputBurst)
		}
	}
	if l.allow(now) {
		t.Errorf("第 %d 个包被放行，突发额度 %v 已用完", int(inputBurst)+1, inputBurst)
	}
}

// TestLimiterRefillsOverTime 验证额度随时间恢复，且恢复有封顶。
//
// 补额速率是 120pps，但桶容量只有 60。所以空闲 1 秒能补 120 个令牌，
// 实际只能存 60 个 —— 这是有意的：burst 限制的是"瞬时能塞进共享队列
// 多少"，rate 限制的是"持续能吃多少"。
func TestLimiterRefillsOverTime(t *testing.T) {
	var l inputLimiter
	now := time.Unix(1000, 0)

	// 先用光。
	for i := 0; i < int(inputBurst); i++ {
		l.allow(now)
	}

	// 过 1 秒：补额 120 个，但封顶在 burst=60。
	later := now.Add(time.Second)
	for i := 0; i < int(inputBurst); i++ {
		if !l.allow(later) {
			t.Fatalf("补额后第 %d 个包被拒，期望能吃满 %v", i+1, inputBurst)
		}
	}
	if l.allow(later) {
		t.Errorf("补额后放了第 %d 个，期望封顶在 %v", int(inputBurst)+1, inputBurst)
	}
}

// TestLimiterNormalStreamNeverBlocked 验证 60Hz 正常流永不被拦。
//
// 这是这个限速器存在的意义所在——它必须对正常玩家完全透明。
func TestLimiterNormalStreamNeverBlocked(t *testing.T) {
	var l inputLimiter
	now := time.Unix(1000, 0)
	interval := time.Second / 60

	for i := 0; i < 3600; i++ { // 60 秒
		if !l.allow(now) {
			t.Fatalf("第 %d 个包（60Hz 正常流）被误拦", i+1)
		}
		now = now.Add(interval)
	}
}

// TestLimiterFloodBlocked 验证洪水会被拦住绝大部分。
//
// 一个客户端 1000pps，超出 120pps 的部分必须被拒，否则队列会被填满。
func TestLimiterFloodBlocked(t *testing.T) {
	var l inputLimiter
	now := time.Unix(1000, 0)

	allowed := 0
	// 1000pps 跑 1 秒。
	for i := 0; i < 1000; i++ {
		if l.allow(now) {
			allowed++
		}
		now = now.Add(time.Millisecond)
	}

	// 期望约 burst + rate = 60 + 120 = 180 个放行。
	// 给一点余量但不该接近 1000。
	const want = int(inputBurst + inputRatePerSec)
	if allowed > want+10 {
		t.Errorf("放行 %d 个，期望不超过 %d（1000pps 洪水被拦住的不够多）", allowed, want)
	}
	if allowed < want-10 {
		t.Errorf("放行 %d 个，期望不少于 %d（限速过严，正常玩家会受影响）", allowed, want)
	}
}

// TestLimiterBurstAfterIdle 验证空闲后攒下的额度可用。
//
// 客户端卡了一秒后补发的场景：它攒了 1 秒配额，连发要好使。
func TestLimiterBurstAfterIdle(t *testing.T) {
	var l inputLimiter
	now := time.Unix(1000, 0)

	// 先建立基线（第一次调用给满额）。
	l.allow(now)

	// 空闲 10 秒（远超补满所需）。
	later := now.Add(10 * time.Second)

	// 应当能连发一整桶。
	for i := 0; i < int(inputBurst); i++ {
		if !l.allow(later) {
			t.Fatalf("空闲后第 %d 个包被拒，配额没攒回来", i+1)
		}
	}
}

// TestLimiterClockGoesBackwards 验证系统时钟回退时不会把玩家锁死。
//
// NTP 校正是真实场景：时钟往回跳几秒，若把负 elapsed 照加进预算，
// 预算会瞬间变成大负数，玩家要等好几秒才能再动。
func TestLimiterClockGoesBackwards(t *testing.T) {
	var l inputLimiter
	now := time.Unix(1000, 0)

	// 用光配额。
	for i := 0; i < int(inputBurst); i++ {
		l.allow(now)
	}

	// 时钟回退 5 秒。
	backwards := now.Add(-5 * time.Second)
	if l.allow(backwards) {
		t.Error("时钟回退后仍放行，说明预算被错误增加")
	}

	// 回到正常时间，等足够长应当恢复。
	forward := now.Add(time.Second)
	if !l.allow(forward) {
		t.Error("时钟恢复并等够 1 秒后仍被拒，玩家被锁死")
	}
}

// TestLimiterFirstCallGivesFullBudget 验证第一次调用不受上次状态影响。
//
// 新建的 limiter 应当立刻可用，而不是要求客户端先等一段时间。
func TestLimiterFirstCallGivesFullBudget(t *testing.T) {
	var l inputLimiter
	now := time.Unix(1000, 0)

	if !l.allow(now) {
		t.Error("第一次调用被拒，玩家刚连上就动不了")
	}
	if l.budget != inputBurst-1 {
		t.Errorf("首次调用后剩余 %v，期望 %v", l.budget, inputBurst-1)
	}
}
