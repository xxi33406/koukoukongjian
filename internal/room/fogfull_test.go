package room

import (
	"testing"
	"time"

	"koukoukongjian/internal/game/world"
	"koukoukongjian/internal/proto"
)

// TestRoomFullFrameCarriesFogBits 钉住迷雾位图真的到了客户端。
//
// systems 层的 Bits() 测试只证明「拷了副本」，证明不了「这条数据经过
// Builder → Envelope → 网络通道到了对端」。而 §9 已知限制里挂了两轮的
// 缺口恰恰是链路没接——单测全绿但玩家重连后还是全黑。
func TestRoomFullFrameCarriesFogBits(t *testing.T) {
	r, _ := startRoomWith(t, "fog-full", nil)
	res, sink := joinPlayer(t, r, "seer")

	// 首帧一定是 full：builder 里没有任何既有的 seen key。
	var full *proto.Snapshot
	deadline := time.After(3 * time.Second)
	for full == nil {
		select {
		case data := <-sink:
			s := decodeSnapshot(t, data)
			if s.Full {
				full = s
			}
		case <-deadline:
			t.Fatal("等待超时：没收到 full 帧")
		}
	}

	if len(full.FogFull) == 0 {
		t.Fatal("full 帧没带迷雾位图——客户端重连后整张图会回到全黑")
	}
	// 出生点周围 8m 半径必然点过灯（joinPlayer 之后至少跑了几 tick）。
	if lit := countBits(full.FogFull); lit == 0 {
		t.Fatalf("迷雾位图里一个格都没点亮（长度 %d 字）", len(full.FogFull))
	}
	if res.Slot != 0 {
		t.Fatalf("座位号 = %d，期望 0", res.Slot)
	}
}

// TestFogBitsCacheTracksReveal 钉住 full 帧位图缓存的失效。
//
// 缓存本身是优化（2KB memcpy × 4 peer 每 tick 是白花），但缓存不失效
// 就是 bug：玩家探索了新区域后到来的 full 帧会带旧位图，客户端表现为
// 「刚才走过的地方又黑了」，且只在重连时出现——最难查的那类。
func TestFogBitsCacheTracksReveal(t *testing.T) {
	r, _ := startRoomWith(t, "fog-cache", nil)
	_, sink := joinPlayer(t, r, "walker")

	// 等首帧过去，让缓存落定在「只有出生点周围」这一份。
	waitSnapshot(t, sink, 3*time.Second, "首帧", func(s *proto.Snapshot) bool {
		return s.Full && countBits(s.FogFull) > 0
	})
	startLit := countBits(r.fogBits)

	// 直接驱动 revealFog，不经 Run 循环：跨 goroutine 读 r.fogBits 会是
	// 数据竞争（-race 必报），而本用例要验的正是「点灯 → 缓存失效」这条
	// 因果链，不是网络链路。
	r.peers.each(func(p *peer) {
		if ent := r.World.Players.Get(p.id); ent != nil {
			ent.Pos = world.V(100, 100) // 走到图的另一头
		}
	})
	r.revealFog()
	// broadcast 每 tick 会调 fogBitsSnapshot 重填缓存——这里复现那一步。
	// 若 revealFog 没把缓存置 nil，下面取到的还是移动前那份。
	_ = r.fogBitsSnapshot()

	// 缓存必须跟着 Reveal 更新；不更新就会一直停在 startLit。
	endLit := countBits(r.fogBits)
	if endLit <= startLit {
		t.Fatalf("走了一段距离后缓存位图点亮数仍 %d（起点 %d）——缓存没有随 Reveal 失效",
			endLit, startLit)
	}
}

func countBits(words []uint64) int {
	n := 0
	for _, w := range words {
		for ; w != 0; w &= w - 1 {
			n++
		}
	}
	return n
}
