package main

import "time"

// 输入限速的默认参数。
//
// 为什么需要限速：输入队列（internal/room 的 r.inputs）是所有玩家共享的。
// 一个客户端每秒发一万个包，能在几个 tick 内把队列填满，此时其他玩家的
// SendInput 全部返回 false —— 别人不是因为网络差而不动，是被一个坏客户端
// 挤掉了。这不是安全问题（没有崩溃），是公平性问题。
//
// 参数怎么定的：
//   - 客户端按 60Hz 发输入，即 60 pps。
//   - 允许 2 倍冗余给"卡了一下之后补发"，即 120 pps。
//   - burst 给 60，等于允许攒 1 秒的配额。客户端暂停 1 秒后连发 60 包
//     不会被误伤，而真正的洪水（1000 pps）在 0.5 秒内就会耗尽配额。
const (
	inputRatePerSec = 120.0
	inputBurst      = 60.0
)

// inputLimiter 是单会话的输入包令牌桶。
//
// 用令牌桶而不是固定窗口计数，是为了避免窗口边界的突发：固定窗口下
// 客户端可以在每秒末尾和下一秒开头发双倍包，令牌桶不会。
//
// 这个结构不是并发安全的，调用方必须持有 bridge.mu —— 与 clientPeer 的
// 其他字段一致。
type inputLimiter struct {
	budget float64
	last   time.Time
}

// allow 判断此刻能否放行一个包。
//
// now 由调用方传入而不是内部取 time.Now()，是为了能用一个假时钟测试
// 突发与恢复行为——真实时间测这个要么跑得慢，要么不稳定。
func (l *inputLimiter) allow(now time.Time) bool {
	if l.last.IsZero() {
		// 第一次调用：给满额，否则玩家刚连上就被限速。
		l.budget = inputBurst
	} else {
		elapsed := now.Sub(l.last).Seconds()
		if elapsed > 0 {
			// 只补正数：系统时钟回退（NTP 校正）时 elapsed 为负，
			// 若照加会让预算减少，把正常玩家锁死好几秒。
			l.budget += elapsed * inputRatePerSec
		}
	}
	l.last = now

	if l.budget > inputBurst {
		l.budget = inputBurst
	}
	if l.budget < 1 {
		return false
	}
	l.budget--
	return true
}
