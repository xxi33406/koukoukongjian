package build

import (
	"testing"

	"koukoukongjian/internal/game/world"
)

// TestReactFireWaterIsSteam 钉住 v0.1 唯一实装的一格。
func TestReactFireWaterIsSteam(t *testing.T) {
	if got := React(world.ElementFire, world.ElementWater); got != ReactionSteam {
		t.Fatalf("fire+water = %v，期望 ReactionSteam", got)
	}
	if got := React(world.ElementWater, world.ElementFire); got != ReactionSteam {
		t.Fatalf("water+fire = %v，期望 ReactionSteam（反应必须是双向的）", got)
	}
}

// TestReactIsSymmetric 穷举 25 组，钉住表的对称性。
//
// 为什么必须穷举：表是 5×5 手写的，只填一半（fire 行有水、water 行没火）
// 在只测 fire+water 一个方向时完全看不出来，症状是「按 A 顺序打出来是蒸汽、
// 按 B 顺序打出来什么都没有」——而玩家根本不知道有顺序这回事。
func TestReactIsSymmetric(t *testing.T) {
	all := []world.Element{
		world.ElementNone, world.ElementFire, world.ElementWater,
		world.ElementOil, world.ElementThunder,
	}
	for _, a := range all {
		for _, b := range all {
			ab := React(a, b)
			ba := React(b, a)
			if ab != ba {
				t.Fatalf("%v+%v = %v，但 %v+%v = %v（不对称）", a, b, ab, b, a, ba)
			}
		}
	}
}

// TestReactUndefinedCombinationsAreNone 钉住 v0.1 的实装边界。
//
// 三层意义：①火+油、水+雷这些 M7 才做的组合必须是 None（否则交汇会生成
// 一个没有效果的区域，玩家看到云却无事发生）；②任何元素与自己不反应
// （两个火球撞一起不会更火）；③None 与任何东西都不反应。
func TestReactUndefinedCombinationsAreNone(t *testing.T) {
	cases := []struct {
		a, b world.Element
		why  string
	}{
		{world.ElementFire, world.ElementOil, "火+油 = 点燃，M7 才做"},
		{world.ElementWater, world.ElementThunder, "水+雷 = 导电，M7 才做"},
		{world.ElementFire, world.ElementFire, "同元素不反应"},
		{world.ElementWater, world.ElementWater, "同元素不反应"},
		{world.ElementFire, world.ElementNone, "无元素不参与反应"},
		{world.ElementNone, world.ElementNone, "无元素不参与反应"},
		{world.ElementFire, world.ElementThunder, "未定义组合"},
		{world.ElementOil, world.ElementWater, "未定义组合"},
	}
	for _, c := range cases {
		if got := React(c.a, c.b); got != ReactionNone {
			t.Fatalf("%v+%v = %v，期望 ReactionNone（%s）", c.a, c.b, got, c.why)
		}
	}
}

// TestReactOutOfRangeIsNone 钉住越界不 panic。
//
// Element 是 uint8，一个损坏的存档或协议值可能喂进 99。这里让越界退化
// 成「不反应」——交汇逻辑读到 ReactionNone 就跳过，不会炸整个 tick。
func TestReactOutOfRangeIsNone(t *testing.T) {
	var bogus world.Element = 99
	if got := React(bogus, world.ElementFire); got != ReactionNone {
		t.Fatalf("越界元素参与 = %v，期望 ReactionNone", got)
	}
	if got := React(world.ElementFire, bogus); got != ReactionNone {
		t.Fatalf("越界元素参与 = %v，期望 ReactionNone", got)
	}
}

// TestReactionKindString 钉住 String()：日志与调试要靠它，空实现会让所有
// 反应在日志里都印成 "unknown"。
func TestReactionKindString(t *testing.T) {
	cases := map[ReactionKind]string{
		ReactionNone:  "none",
		ReactionSteam: "steam",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Fatalf("ReactionKind(%d).String() = %q，期望 %q", k, got, want)
		}
	}
}

// TestReactionZoneKind 钉住「反应结果落到哪种区域」的映射。
//
// 单独一个函数而不是在交汇代码里写 switch：M7 加 Ignite/Conduct 时
// 只需要改这一处和表，交汇逻辑不用动。
func TestReactionZoneKind(t *testing.T) {
	if got := ReactionZoneKind(ReactionSteam); got != world.ZoneSteam {
		t.Fatalf("steam 的区域类型 = %v，期望 world.ZoneSteam", got)
	}
	if got := ReactionZoneKind(ReactionNone); got != 0 {
		t.Fatalf("none 的区域类型 = %v，期望 0（不生成区域）", got)
	}
}
