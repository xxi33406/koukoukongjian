package proto

import (
	"math"

	"koukoukongjian/internal/game/world"
)

// maxEventsPerSnapshot 是单帧事件上限：20Hz × 32 条 = 640 事件/秒的峰值
// 足够灰盒对局；截断时优先保留 KILL/DEATH。
const maxEventsPerSnapshot = 32

// DamageEventToProto 把世界侧伤害事件翻译成协议事件。
//
// kind 规则：IsKill 且 Src 有效 → KILL；IsKill 且 Src 无效（Reap 兜底产出）
// → DEATH；否则 DAMAGE。A=Src、B=Dst。
// value 用 ×1000 定点（与 proto 注释约定一致），负数与 NaN 钳 0，
// 上界钳 MaxInt32——浮点转整数越界是平台相关的未定义值（AGENTS.md §2.6），
// 所以必须在转换前钳，转换后钳就晚了。
func DamageEventToProto(ev world.DamageEvent) *GameEvent {
	kind := GameEventKind_GAME_EVENT_KIND_DAMAGE
	if ev.IsKill {
		if ev.Src.IsValid() {
			kind = GameEventKind_GAME_EVENT_KIND_KILL
		} else {
			kind = GameEventKind_GAME_EVENT_KIND_DEATH
		}
	}

	scaled := ev.Amount * 1000
	switch {
	case math.IsNaN(scaled), scaled <= 0:
		scaled = 0
	case scaled > math.MaxInt32:
		scaled = math.MaxInt32
	}
	return &GameEvent{
		Kind:    kind,
		A:       uint32(ev.Src),
		B:       uint32(ev.Dst),
		Value:   int32(scaled),
		Element: elementValue(ev.Element),
	}
}

// EventsToProto 批量映射，超过上限时优先保留击杀类事件。
//
// IsKill 的事件（KILL 与 DEATH）整体排前、普通 DAMAGE 排后，同类之间
// 保持原相对顺序；两类各自按上限截断。
func EventsToProto(evs []world.DamageEvent) []*GameEvent {
	if len(evs) == 0 {
		return nil
	}
	out := make([]*GameEvent, 0, len(evs))
	var kills []*GameEvent
	var plain []*GameEvent
	for _, ev := range evs {
		g := DamageEventToProto(ev)
		if ev.IsKill {
			kills = append(kills, g)
		} else {
			plain = append(plain, g)
		}
	}
	out = append(out, kills...)
	if len(out) > maxEventsPerSnapshot {
		out = out[:maxEventsPerSnapshot]
		return out
	}
	remain := maxEventsPerSnapshot - len(out)
	if len(plain) > remain {
		plain = plain[:remain]
	}
	out = append(out, plain...)
	return out
}

// elementValue 世界元素 → 协议 element 裸值（uint32）。
// proto 侧无 Element 枚举，数值与 world.Element iota 对齐：0=none 1=fire 2=water 3=oil 4=thunder。
func elementValue(e world.Element) uint32 {
	return uint32(e)
}
