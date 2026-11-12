package room

import (
	"math"
	"testing"
	"time"

	"koukoukongjian/internal/game/world"
	"koukoukongjian/internal/proto"
)

// waitSnapshot 反复从 sink 取快照，直到 cond 成立或超时。
//
// 全部走快照而不是直接读 r.World：房间跑起来之后 World 归 tick goroutine
// 独占（AGENTS.md §2.5），测试 goroutine 直接读它是数据竞争，-race 会报，
// 而"测试挂了但代码是对的"这种噪音最消耗人。走快照顺带验证了"状态真的
// 发到了客户端"这一环，比读内存更接近真实。
//
// 不用固定 time.Sleep 等：tick 循环 50ms 一次，机器快慢不同，睡死一个
// 数值必然在慢机器上偶发失败。
func waitSnapshot(t *testing.T, sink chan []byte, timeout time.Duration, what string, cond func(*proto.Snapshot) bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("等待超时：%s", what)
		}
		select {
		case data, ok := <-sink:
			if !ok {
				t.Fatalf("sink 已关闭，等待 %s 失败", what)
			}
			if cond(decodeSnapshot(t, data)) {
				return
			}
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
}

// selfAim 从快照里读自己的朝向，弧度。读不到时返回 NaN。
func selfAim(s *proto.Snapshot) float64 {
	if s == nil || s.Self == nil {
		return math.NaN()
	}
	return proto.DequantizeAim(s.Self.Aim)
}

func TestInputCarriesAimIntent(t *testing.T) {
	r, _ := startRoom(t, "aim-intent")
	res, sink := joinPlayer(t, r, "p1")

	// 给方向：HasAim=true，Aim 落到实体上，再原样发到快照里。
	r.SendInput(res.Slot, InputCommand{
		Seq: 1, Move: world.V(1, 0), Aim: 1.2, HasAim: true,
	})

	waitSnapshot(t, sink, 2*time.Second, "快照里的朝向变成 1.2", func(s *proto.Snapshot) bool {
		got := selfAim(s)
		return !math.IsNaN(got) && math.Abs(got-1.2) < 0.01
	})
}

func TestInputWithoutAimKeepsPrevious(t *testing.T) {
	// 没给方向时不能把 AimDir 冲成 0——那样自动瞄准永远看不到
	// "玩家其实没在瞄准"这个状态，因为早就被 0 覆盖了。
	r, _ := startRoom(t, "aim-keep")
	res, sink := joinPlayer(t, r, "p1")

	// 先建立一个明确的朝向。
	r.SendInput(res.Slot, InputCommand{Seq: 1, Aim: 2.0, HasAim: true})
	waitSnapshot(t, sink, 2*time.Second, "初始朝向 2.0 到位", func(s *proto.Snapshot) bool {
		got := selfAim(s)
		return !math.IsNaN(got) && math.Abs(got-2.0) < 0.01
	})

	// 再发一条不给方向的输入，用 AckedInputTick 确认它被处理了。
	r.SendInput(res.Slot, InputCommand{Seq: 2, Move: world.V(1, 0), HasAim: false})
	var lastAim float64
	waitSnapshot(t, sink, 2*time.Second, "第二条输入被确认", func(s *proto.Snapshot) bool {
		if s.AckedInputTick != 2 {
			return false
		}
		lastAim = selfAim(s)
		return true
	})

	if math.Abs(lastAim-2.0) > 0.01 {
		t.Fatalf("没给方向时朝向变成 %v，期望保持 2.0", lastAim)
	}
}
