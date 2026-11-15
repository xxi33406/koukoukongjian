package proto

import (
	"testing"

	"koukoukongjian/internal/game/world"
)

func TestDamageEventMapping(t *testing.T) {
	cases := []struct {
		name string
		ev   world.DamageEvent
		want GameEventKind
	}{
		{"伤害", world.DamageEvent{Src: 5, Dst: 9, Amount: 12.5}, GameEventKind_GAME_EVENT_KIND_DAMAGE},
		{"击杀", world.DamageEvent{Src: 5, Dst: 9, Amount: 32, IsKill: true}, GameEventKind_GAME_EVENT_KIND_KILL},
		{"死亡（Reap 产出）", world.DamageEvent{Dst: 9, IsKill: true}, GameEventKind_GAME_EVENT_KIND_DEATH},
	}
	for _, tc := range cases {
		got := DamageEventToProto(tc.ev)
		if got.Kind != tc.want {
			t.Errorf("%s：Kind = %v, 期望 %v", tc.name, got.Kind, tc.want)
		}
		if got.A != uint32(tc.ev.Src) || got.B != uint32(tc.ev.Dst) {
			t.Errorf("%s：a/b = %d/%d, 期望 %d/%d", tc.name, got.A, got.B, tc.ev.Src, tc.ev.Dst)
		}
	}
	if got := DamageEventToProto(world.DamageEvent{Amount: 3.0, Element: world.ElementFire}); got.Element != 1 {
		// proto 侧 element 是裸 uint32：0=none 1=fire 2=water 3=oil 4=thunder（与 world.Element iota 对齐）
		t.Errorf("元素未映射：%v, 期望 1", got.Element)
	}
	if got := DamageEventToProto(world.DamageEvent{Amount: 3.0}); got.Value != 3000 {
		// value 量化 ×1000（proto 注释约定：千分位定点数）
		t.Errorf("value 量化错误：%d, 期望 3000", got.Value)
	}
}

func TestEventBatchTruncated(t *testing.T) {
	var evs []world.DamageEvent
	for i := 0; i < 40; i++ {
		evs = append(evs, world.DamageEvent{Src: 1, Dst: 2, Amount: 1, IsKill: i%7 == 0})
	}
	got := EventsToProto(evs)
	if len(got) > 32 {
		t.Errorf("批量映射应截断到 32 条以内，实际 %d", len(got))
	}
	// IsKill 的事件必须优先保留（击杀比伤害重要）。
	for _, g := range got {
		if g.Kind == GameEventKind_GAME_EVENT_KIND_KILL {
			return // 找到至少一条 KILL 即可
		}
	}
	t.Error("截断时 KILL 事件被丢光了")
}
