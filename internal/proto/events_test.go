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

// 上面的用例只产 6 条 kill（6<32），截断分支根本没触发——按原序截断它也绿。
// 真正钉住「KILL 优先」必须让 kill 数量超过上限本身。
func TestEventBatchTruncatedKillsWinOverCap(t *testing.T) {
	var evs []world.DamageEvent
	for i := 0; i < 40; i++ {
		evs = append(evs, world.DamageEvent{Src: 1, Dst: 2, Amount: float64(i + 1), IsKill: true})
	}
	got := EventsToProto(evs)
	if len(got) != 32 {
		t.Fatalf("40 条全 kill 应截到 32 条，实际 %d", len(got))
	}
	for i, g := range got {
		if g.Kind != GameEventKind_GAME_EVENT_KIND_KILL {
			t.Fatalf("got[%d].Kind = %v, 期望 KILL（kills>cap 时截断必须全是击杀）", i, g.Kind)
		}
		if g.Value != int32((i+1)*1000) {
			t.Fatalf("got[%d].Value = %d, 期望 %d（保留的必须是前 32 条，保序）", i, g.Value, (i+1)*1000)
		}
	}
}

// kills 与 plain 混合超限时：kills 整体排前、plain 排后，两类各自保序。
func TestEventBatchTruncatedKillsSortedFirst(t *testing.T) {
	var evs []world.DamageEvent
	for i := 0; i < 40; i++ {
		// 12 条 kill（i%3==1）+ 28 条 plain，kills>0 且总数>32
		evs = append(evs, world.DamageEvent{Src: 1, Dst: 2, Amount: float64(i + 1), IsKill: i%3 == 1})
	}
	got := EventsToProto(evs)
	if len(got) != 32 {
		t.Fatalf("应截到 32 条，实际 %d", len(got))
	}
	lastKill, firstPlain := -1, -1
	for i, g := range got {
		if g.Kind == GameEventKind_GAME_EVENT_KIND_KILL {
			if firstPlain >= 0 {
				t.Fatalf("got[%d] 是 KILL，但其前已有 DAMAGE——击杀必须整体排前", i)
			}
			lastKill = i
		} else if firstPlain < 0 {
			firstPlain = i
		}
	}
	if lastKill < 0 {
		t.Fatal("混合超限时击杀被丢光了")
	}
	if firstPlain >= 0 && lastKill >= firstPlain {
		t.Fatalf("击杀区末尾 %d 落在伤害区起点 %d 之后", lastKill, firstPlain)
	}
}

// 团灭事件能正确映射（M5 唯一新增的枚举）。
//
// 钉 7 这个具体数值：枚举值就是线上协议，客户端按数值分支，改号等于
// 改协议。同时钉住"枚举没被重新生成时不会悄悄少一个值"。
func TestWipeEventMaps(t *testing.T) {
	ev := GameEvent{
		Kind: GameEventKind_GAME_EVENT_KIND_WIPE,
	}
	if ev.Kind != 7 {
		t.Fatalf("GAME_EVENT_KIND_WIPE = %d，期望 7", ev.Kind)
	}
}
