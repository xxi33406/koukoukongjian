package fog

import (
	"testing"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/game/world"
)

func TestRevealLightsCircleAndPersists(t *testing.T) {
	cfg := config.Default()
	w := world.NewWorld(cfg, 5)
	f := New(w.Terrain.Width(), w.Terrain.Height())

	pid, p := w.Players.Spawn()
	p.Pos = world.V(64, 64)
	p.State = world.PlayerAlive

	first := f.Reveal(w, cfg.Fog.RevealRadius)
	if len(first) == 0 {
		t.Fatal("首次揭示应点亮一批格")
	}
	// 点亮数应接近 πr²（半径 8 → 约 201 格）。
	if len(first) < 150 || len(first) > 260 {
		t.Errorf("首亮格数 %d 偏离 π×8²≈201 太远", len(first))
	}
	// 全部在半径内。
	for _, idx := range first {
		cx, cy := int(idx)%int(w.Terrain.Width()), int(idx)/int(w.Terrain.Width())
		pos := world.V(float64(cx)+0.5, float64(cy)+0.5)
		if pos.DistSq(p.Pos) > (cfg.Fog.RevealRadius+1)*(cfg.Fog.RevealRadius+1) {
			t.Errorf("点亮格 (%d,%d) 距玩家超过半径+1", cx, cy)
		}
	}

	// 原地不动：增量应为 0（永久保留）。
	again := f.Reveal(w, cfg.Fog.RevealRadius)
	if len(again) != 0 {
		t.Errorf("原地不动增量应 0，实际 %d", len(again))
	}
	_ = pid
}

func TestRevealIncrementalAfterMove(t *testing.T) {
	cfg := config.Default()
	w := world.NewWorld(cfg, 5)
	f := New(w.Terrain.Width(), w.Terrain.Height())

	id, p := w.Players.Spawn()
	p.Pos = world.V(20, 20)
	p.State = world.PlayerAlive
	f.Reveal(w, cfg.Fog.RevealRadius)

	// 移动 2m：新圆与旧圆重叠大半，增量应远小于首亮。
	p.Pos = world.V(22, 20)
	inc := f.Reveal(w, cfg.Fog.RevealRadius)
	if len(inc) == 0 || len(inc) > 120 {
		t.Errorf("移动 2m 的增量 %d 应在 (0,120) 内", len(inc))
	}
	_ = id
}

func TestLitReturnsStableCopy(t *testing.T) {
	f := New(8, 8)
	if f.Lit(0) || f.Lit(63) {
		t.Error("新 fog 应全暗")
	}
	f.lit[0] = 0xFF
	if !f.Lit(0) || !f.Lit(7) || f.Lit(8) {
		t.Error("bitset 位语义错误")
	}
}

// 鬼魂也要点亮迷雾：spec L49「可探图」。
//
// 只让活人点灯的话，鬼魂走一圈回来地图仍是黑的——"可探图"就是一句空话，
// 而玩家分不清是"迷雾没实现"还是"我死了所以看不见"。
func TestGhostRevealsFog(t *testing.T) {
	cfg := config.Default()
	w := world.NewWorld(cfg, 5)
	f := New(w.Terrain.Width(), w.Terrain.Height())

	_, p := w.Players.Spawn()
	p.State = world.PlayerGhost
	p.HP = 0
	p.Pos = world.V(64, 64)

	inc := f.Reveal(w, cfg.Fog.RevealRadius)
	if len(inc) == 0 {
		t.Fatalf("鬼魂没有点亮任何迷雾格")
	}
}

// TestBitsIsCopy 钉住 Bits 返回副本而不是内部切片。
//
// 返回引用的话，snapshot.Builder 拿到的那份位图会在下一 tick 的 Reveal
// 里被改掉——客户端看到的迷雾比实际少一块，且只在玩家移动时出现。
func TestBitsIsCopy(t *testing.T) {
	cfg := config.Default()
	w := world.NewWorld(cfg, 5)
	f := New(w.Terrain.Width(), w.Terrain.Height())
	_, p := w.Players.Spawn()
	p.Pos = world.V(10, 10)
	p.State = world.PlayerAlive
	f.Reveal(w, cfg.Fog.RevealRadius)

	first := f.Bits()
	if len(first) == 0 {
		t.Fatal("Bits 返回空位图")
	}
	// 逐字保存 first 当下的内容：若 Bits 返回的是内部切片，Reveal 会
	// 把 first 原地改掉，下面的比较就会不等。
	want := append([]uint64(nil), first...)
	litBefore := f.LitCount()

	// 走到图的另一头再点亮一片，内部 lit 必然变化。
	p.Pos = world.V(100, 100)
	f.Reveal(w, cfg.Fog.RevealRadius)
	if f.LitCount() == litBefore {
		t.Fatalf("内部位图没变（LitCount 仍 %d），本用例无法区分副本与引用", litBefore)
	}

	for i := range want {
		if first[i] != want[i] {
			t.Fatalf("先前取到的副本在 Reveal 后被改了：第 %d 字 %#x → %#x", i, want[i], first[i])
		}
	}
}
