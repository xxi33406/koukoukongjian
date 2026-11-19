package systems

import (
	"math"
	"testing"

	"koukoukongjian/internal/game/world"
)

// 鬼魂站在祭坛范围内开始引导，到点复活。
//
// 为什么引导要拆成 Begin/Tick 两步而不是一个 Revive 函数：引导是"持续
// 状态"，中途可能被打断（走出范围）、可能被取消（玩家主动动一下）。
// 一个原子函数在服务端没法表达"进行到一半"。
func TestReviveProgressAndComplete(t *testing.T) {
	w := newSystemsWorld(t, 7)
	w.HasMap = true
	w.Hubs = []world.Hub{{Center: world.V(20, 20), RadiusM: 5}}

	_, p := w.Players.Spawn()
	p.State = world.PlayerGhost
	p.HP = 0
	p.MaxHP = 100
	p.Pos = world.V(20, 20) // 站在祭坛中心

	// 第 1 tick：范围对、开始引导
	if !ReviveBegin(w, p, 20) {
		t.Fatalf("站在祭坛上却没能开始复活引导")
	}
	if p.ReviveAt != 20 {
		t.Fatalf("ReviveAt = %d，期望 20", p.ReviveAt)
	}
	if !p.Flags.Has(world.PlayerFlagBusy) {
		t.Fatalf("开始引导没置 Busy 位")
	}
	// 同一 tick 内不该完成
	if ReviveTick(w, p, w.Tick) {
		t.Fatalf("刚开始引导就完成了")
	}
}

// 到点复活：回半血、State 回 Alive、Busy 清掉。
func TestReviveCompletesAtDeadline(t *testing.T) {
	w := newSystemsWorld(t, 7)
	w.HasMap = true
	w.Hubs = []world.Hub{{Center: world.V(20, 20), RadiusM: 5}}

	_, p := w.Players.Spawn()
	p.State = world.PlayerGhost
	p.HP = 0
	p.MaxHP = 100
	p.Pos = world.V(20, 20)

	ReviveBegin(w, p, 5)
	w.Tick = 5
	if !ReviveTick(w, p, w.Tick) {
		t.Fatalf("到点没有完成复活")
	}

	if p.State != world.PlayerAlive {
		t.Fatalf("复活后 State = %d，期望 PlayerAlive", p.State)
	}
	if p.HP != 50 {
		t.Fatalf("复活后 HP = %v，期望半血 50", p.HP)
	}
	if p.Flags.Has(world.PlayerFlagBusy) {
		t.Fatalf("复活完成后 Busy 位没清")
	}
	if p.ReviveAt != 0 {
		t.Fatalf("复活完成后 ReviveAt = %d，期望 0", p.ReviveAt)
	}
}

// 复活给半血不是满血：满血复活等于死亡没有代价（spec L51 的设计意图是
// "刚清完据点时复活是安全的，战斗中复活 = 全队护法的名场面"——若满血
// 复活，战斗中的复活就没有风险，护法这一幕不存在）。
func TestReviveGivesHalfHP(t *testing.T) {
	w := newSystemsWorld(t, 7)
	w.HasMap = true
	w.Hubs = []world.Hub{{Center: world.V(20, 20), RadiusM: 5}}

	_, p := w.Players.Spawn()
	p.State = world.PlayerGhost
	p.MaxHP = 130 // 带强健体魄的玩家
	p.Pos = world.V(20, 20)

	ReviveBegin(w, p, 5)
	w.Tick = 5
	ReviveTick(w, p, w.Tick)

	if p.HP != 65 {
		t.Fatalf("复活后 HP = %v，期望 65（130 的一半）", p.HP)
	}
}

// 走出祭坛范围就打断（用户拍板决定 1：鬼魂永不受击，打断条件改成出范围）。
//
// 为什么不是"受击打断"：鬼魂不吃伤害（Task 1），永远不会收到受击事件，
// 受击打断这条规则在鬼魂身上根本不可能触发。spec L51 的"受击打断"是
// 站在"鬼魂也是战斗单位"的旧假设上写的，M5 按用户拍板改成位置条件。
func TestReviveInterruptedWhenLeavingAltar(t *testing.T) {
	w := newSystemsWorld(t, 7)
	w.HasMap = true
	w.Hubs = []world.Hub{{Center: world.V(20, 20), RadiusM: 5}}

	_, p := w.Players.Spawn()
	p.State = world.PlayerGhost
	p.MaxHP = 100
	p.Pos = world.V(20, 20)

	ReviveBegin(w, p, 5)
	if p.ReviveAt == 0 {
		t.Fatalf("没开始引导")
	}

	// 走出范围
	p.Pos = world.V(40, 40)
	if ReviveTick(w, p, w.Tick) {
		t.Fatalf("走出祭坛范围后仍然完成了复活")
	}
	if p.ReviveAt != 0 {
		t.Fatalf("走出范围后 ReviveAt = %d，期望被清 0", p.ReviveAt)
	}
	if p.Flags.Has(world.PlayerFlagBusy) {
		t.Fatalf("走出范围后 Busy 位没清")
	}
	if p.State != world.PlayerGhost {
		t.Fatalf("走出范围后 State 变了：%d", p.State)
	}
}

// 主动取消（玩家动一下方向键）也要能立刻断。
//
// 这一条不在 spec 里，是实现补的：引导 3 秒里玩家一定会试图走两步，
// 如果走动不打断，那"走出范围才断"就变成"只要不走出去随便动"，
// 而玩家没法判断自己还在不在引导——反馈缺失比规则严苛更糟。
func TestReviveCancelByMove(t *testing.T) {
	w := newSystemsWorld(t, 7)
	w.HasMap = true
	w.Hubs = []world.Hub{{Center: world.V(20, 20), RadiusM: 5}}

	_, p := w.Players.Spawn()
	p.State = world.PlayerGhost
	p.MaxHP = 100
	p.Pos = world.V(20, 20)

	ReviveBegin(w, p, 5)
	p.MoveIntent = world.V(1, 0) // 鬼魂想走

	ReviveTick(w, p, w.Tick)

	if p.ReviveAt != 0 {
		t.Fatalf("移动后引导没被打断：ReviveAt = %d", p.ReviveAt)
	}
}

// 活人不能开始复活引导：这个函数只服务鬼魂。
func TestReviveRejectsAlivePlayer(t *testing.T) {
	w := newSystemsWorld(t, 7)
	w.HasMap = true
	w.Hubs = []world.Hub{{Center: world.V(20, 20), RadiusM: 5}}

	_, p := w.Players.Spawn()
	p.State = world.PlayerAlive
	p.HP = 100
	p.MaxHP = 100
	p.Pos = world.V(20, 20)

	if ReviveBegin(w, p, 5) {
		t.Fatalf("活人也能开始复活引导")
	}
	if p.ReviveAt != 0 {
		t.Fatalf("活人的 ReviveAt 被写了：%d", p.ReviveAt)
	}
}

// 不在祭坛范围内不能开始。
func TestReviveRequiresAltar(t *testing.T) {
	w := newSystemsWorld(t, 7)
	w.HasMap = true
	w.Hubs = []world.Hub{{Center: world.V(20, 20), RadiusM: 5}}

	_, p := w.Players.Spawn()
	p.State = world.PlayerGhost
	p.MaxHP = 100
	p.Pos = world.V(40, 40) // 远离祭坛

	if ReviveBegin(w, p, 5) {
		t.Fatalf("不在祭坛范围内也开始了复活")
	}
}

// 没有地图时不能复活（老测试与降级房间）：
// 没有据点就没有祭坛，与 waves/ai/fog 的短路口径一致。
func TestReviveRequiresMap(t *testing.T) {
	w := newSystemsWorld(t, 7) // HasMap=false

	_, p := w.Players.Spawn()
	p.State = world.PlayerGhost
	p.MaxHP = 100
	p.Pos = world.V(20, 20)

	if ReviveBegin(w, p, 5) {
		t.Fatalf("没有地图也开始了复活")
	}
}

// 复活后的引导 tick 是"毫秒级"：判断用的是 now >= ReviveAt 而不是 ==，
// 漏调一帧不该让引导永远卡住。
func TestReviveTickTolerantToSkippedTick(t *testing.T) {
	w := newSystemsWorld(t, 7)
	w.HasMap = true
	w.Hubs = []world.Hub{{Center: world.V(20, 20), RadiusM: 5}}

	_, p := w.Players.Spawn()
	p.State = world.PlayerGhost
	p.MaxHP = 100
	p.Pos = world.V(20, 20)

	ReviveBegin(w, p, 5)
	w.Tick = 99 // 跳过了 5、6、...、98
	if !ReviveTick(w, p, w.Tick) {
		t.Fatalf("跳帧后没有完成复活")
	}
}

// ReviveTicks 把秒换算成 tick 数，20Hz 下 3 秒 = 60。
//
// 边界必须测到：0 与 NaN 是配置写错时最容易出现的两个值，而它们都会让
// "引导时长"变成 0——引导在开始的同一 tick 就完成，玩家看不到任何过程，
// 症状像"复活是瞬间的"，而配置里写的 3 秒看起来完全正确。
func TestReviveTicks(t *testing.T) {
	cases := []struct {
		rate float64
		want uint32
	}{
		{20, 60},
		{30, 90},
		{0, 1},   // 配置写错：下限 1，不返回 0
		{-20, 1}, // 负数同理
		{0.1, 1}, // 极小频率：3×0.1=0.3 四舍五入到 0，必须钳到 1
	}
	for _, c := range cases {
		if got := ReviveTicks(c.rate); got != c.want {
			t.Fatalf("ReviveTicks(%v) = %d，期望 %d", c.rate, got, c.want)
		}
	}
	// NaN 走的是另一条分支（rate != rate），单独测
	nan := math.NaN()
	if got := ReviveTicks(nan); got != 1 {
		t.Fatalf("ReviveTicks(NaN) = %d，期望 1", got)
	}
}
