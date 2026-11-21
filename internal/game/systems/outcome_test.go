package systems

import (
	"testing"

	"koukoukongjian/internal/game/world"
)

// newOutcomeWorld 造一个"有 3 个据点"的世界。
//
// 手工填 Hubs 而不调 mapgen：systems 不该为一个终局判定测试引入地图生成，
// 而且 mapgen 会消耗 RNG——同一颗种子在测试里跑一次 mapgen，后续所有
// 随机相关断言的期望值都会跟着变。这里只造 CheckOutcome 真正读的字段
// （HasMap + Hubs[].State），其余保持零值。
func newOutcomeWorld(t *testing.T) *world.World {
	t.Helper()
	w := newSystemsWorld(t, 11)
	w.HasMap = true
	w.Hubs = []world.Hub{
		{Center: world.V(10, 10), RadiusM: 5, State: world.HubIdle, Element: world.ElementFire},
		{Center: world.V(40, 10), RadiusM: 5, State: world.HubIdle, Element: world.ElementWater},
		{Center: world.V(10, 40), RadiusM: 5, State: world.HubIdle},
	}
	if !w.HasMap || len(w.Hubs) == 0 {
		t.Fatal("前提失效：世界没有据点，无法测终局判定")
	}
	return w
}

// allHubsCleared 把全部据点推到清场态。
func allHubsCleared(w *world.World) {
	for i := range w.Hubs {
		w.Hubs[i].State = world.HubCleared
	}
}

// TestOutcomeUnknownWhileHubsRemain 钉住"据点没清完不算通关"。
func TestOutcomeUnknownWhileHubsRemain(t *testing.T) {
	w := newOutcomeWorld(t)
	if got := CheckOutcome(w); got != world.OutcomeUnknown {
		t.Fatalf("刚开局 Outcome = %v，期望 unknown", got)
	}
}

// TestOutcomeClearedRequiresAllHubsAndBoss 是核心断言。
//
// 三个阶段各测一次，因为「通关」由两个条件与运算而成——只测最终态
// 无法区分"据点判定没生效"和"Boss 判定没生效"。
func TestOutcomeClearedRequiresAllHubsAndBoss(t *testing.T) {
	w := newOutcomeWorld(t)

	// 阶段 1：据点清完但 Boss 还在 → 不算通关。
	allHubsCleared(w)
	w.BossDead = false
	if got := CheckOutcome(w); got != world.OutcomeUnknown {
		t.Fatalf("据点清完但 Boss 未死 Outcome = %v，期望 unknown", got)
	}

	// 阶段 2：Boss 死了但还有据点没清 → 不算通关。
	w.BossDead = true
	w.Hubs[0].State = world.HubFighting
	if got := CheckOutcome(w); got != world.OutcomeUnknown {
		t.Fatalf("Boss 死但据点未清完 Outcome = %v，期望 unknown", got)
	}

	// 阶段 3：两者都满足 → 通关。
	allHubsCleared(w)
	if got := CheckOutcome(w); got != world.OutcomeCleared {
		t.Fatalf("据点清完且 Boss 死 Outcome = %v，期望 cleared", got)
	}
}

// TestOutcomeWipedOverridesCleared 钉住优先级：同一 tick 既通关又团灭时算团灭。
//
// 这在天花板情况下真会发生：最后一个玩家打死 Boss 后被残弹打死。
// 算团灭而不是通关，因为 spec L52 把"全队变鬼魂"定义为本局失败——
// 一个人都没活下来，"通关"就没有意义（没人能进下一关）。
func TestOutcomeWipedOverridesCleared(t *testing.T) {
	w := newOutcomeWorld(t)
	allHubsCleared(w)
	w.BossDead = true

	if _, p := w.Players.Spawn(); p != nil {
		p.State = world.PlayerGhost
	}
	if got := CheckOutcome(w); got != world.OutcomeWiped {
		t.Fatalf("全队鬼魂且条件满足 Outcome = %v，期望 wiped", got)
	}
}

// TestOutcomeEmptyRoomIsNotWipe 钉住空房间不是团灭。
//
// CheckTeamWipe 已经有这条判据（M5）；这里再钉一次是因为 Outcome
// 决定的是**要不要落盘一条失败记录**——空房间（没人加入过）落一条
// "团灭"会污染战绩统计。
func TestOutcomeEmptyRoomIsNotWipe(t *testing.T) {
	w := newOutcomeWorld(t)
	if got := CheckOutcome(w); got == world.OutcomeWiped {
		t.Fatal("空房间被判定为团灭")
	}
}

// TestOutcomeNoMapWorldNeverCleared 钉住无图世界（HubCount=0）永远不判通关。
func TestOutcomeNoMapWorldNeverCleared(t *testing.T) {
	w := newSystemsWorld(t, 3) // newSystemsWorld 关地图
	if w.HasMap {
		t.Fatal("前提失效：newSystemsWorld 应是关地图世界")
	}
	w.BossDead = true
	if got := CheckOutcome(w); got == world.OutcomeCleared {
		t.Fatal("无图世界被判定为通关")
	}
}

// TestOutcomeHasMapFalseWithHubsStillNotCleared 钉住 HasMap 与 Hubs
// 两个条件都要判。构造一个"有据点但 HasMap=false"的病态世界——
// 生产代码不会造出来，但 HasMap 是显式字段而非 len(Hubs)>0 的派生量，
// 两者不一致时以 HasMap 为准（它是 mapgen 的契约：没有图就没有
// 可玩的关卡，据点只是残留数据）。
func TestOutcomeHasMapFalseWithHubsStillNotCleared(t *testing.T) {
	w := newOutcomeWorld(t)
	allHubsCleared(w)
	w.BossDead = true
	w.HasMap = false
	if got := CheckOutcome(w); got == world.OutcomeCleared {
		t.Fatal("HasMap=false 但据点在，被判定为通关")
	}
}
