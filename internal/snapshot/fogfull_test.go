package snapshot

import (
	"testing"

	"koukoukongjian/internal/game/world"
)

// TestFullFrameCarriesFog 钉住 full 帧带迷雾位图。
//
// 没有它，客户端重连后整张图回到全黑——已探明的区域要重新走一遍。
// §9 已知限制记了两轮。
func TestFullFrameCarriesFog(t *testing.T) {
	w := newTestWorld(t)
	id, p := spawnPlayer(t, w)
	p.Pos = world.V(50, 50)

	bits := []uint64{0xFF, 0, 0, 0x1}
	b := NewBuilder()
	b.FogBits = bits

	// 首帧（builder 里没有既有 key）必然是 full。
	snap, st := b.Build(w, id, false)
	if !st.Full {
		t.Fatal("首帧不是 full 帧")
	}
	if len(snap.FogFull) != len(bits) {
		t.Fatalf("FogFull 长度 = %d，期望 %d", len(snap.FogFull), len(bits))
	}
	for i := range bits {
		if snap.FogFull[i] != bits[i] {
			t.Fatalf("FogFull[%d] = %#x，期望 %#x", i, snap.FogFull[i], bits[i])
		}
	}
}

// TestIncrementalFrameSkipsFog 钉住增量帧不带位图。
//
// 增量帧有 fog_delta 就够，每帧重发 2KB 是纯浪费——20Hz 下就是 40KB/s。
func TestIncrementalFrameSkipsFog(t *testing.T) {
	w := newTestWorld(t)
	id, _ := spawnPlayer(t, w)

	b := NewBuilder()
	b.FogBits = []uint64{0xFF}
	b.Build(w, id, false) // 首帧 full

	w.Tick++
	snap, st := b.Build(w, id, false)
	if st.Full {
		t.Fatal("第二帧不该是 full")
	}
	if snap.FogFull != nil {
		t.Fatalf("增量帧带了迷雾位图（%d 字），应该留空", len(snap.FogFull))
	}
}
