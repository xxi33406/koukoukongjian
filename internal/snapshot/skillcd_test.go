package snapshot

import (
	"math"
	"testing"
)

// TestPlayerSelfCarriesSkillCooldown 钉住技能冷却进快照。
//
// M4 起 PlayerState.SkillCooldown 就存在，但从不进快照——§5 已知限制里挂了
// 三轮。前端要做技能图标灰化/扇形遮罩必须有它，否则「技能在冷却」完全
// 不可见，玩家只能靠试。
func TestPlayerSelfCarriesSkillCooldown(t *testing.T) {
	w := newTestWorld(t)
	id, p := spawnPlayer(t, w)
	p.SkillCooldown[0] = 1.5
	p.SkillCooldown[1] = 0.25

	b := NewBuilder()
	snap, _ := b.Build(w, id, false)
	if snap.Self == nil {
		t.Fatal("Self 为 nil")
	}
	got := snap.Self.SkillCooldownQ
	if len(got) != 2 {
		t.Fatalf("SkillCooldownQ 长度 = %d，期望 2", len(got))
	}
	// 量化到 1/100 秒：1.5 → 150，0.25 → 25。
	if got[0] != 150 {
		t.Fatalf("槽 0 冷却 = %d，期望 150（1.5s × 100）", got[0])
	}
	if got[1] != 25 {
		t.Fatalf("槽 1 冷却 = %d，期望 25", got[1])
	}
}

// TestSkillCooldownQuantizeClamps 钉住负值与溢出。
//
// float64 → uint32 越界在 Go 里是平台相关的未定义值，不是截断
// （AGENTS.md §2.6 第一条）。冷却可能被技能表配成 1e9 秒，NaN 也可能
// 从 TOML 的 nan 字面量漏进来。
func TestSkillCooldownQuantizeClamps(t *testing.T) {
	cases := []struct {
		in   float64
		want uint32
	}{
		{-1, 0},
		{0, 0},
		{0.004, 0},
		{0.006, 1},
		{1e9, math.MaxUint32},
	}
	for _, c := range cases {
		if got := quantizeCooldown(c.in); got != c.want {
			t.Fatalf("quantizeCooldown(%v) = %d，期望 %d", c.in, got, c.want)
		}
	}
	// NaN 折 0（与 clamp16/quantizeAim 的既有纪律一致）。
	if got := quantizeCooldown(math.NaN()); got != 0 {
		t.Fatalf("NaN 量化成 %d，期望 0", got)
	}
}
