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
