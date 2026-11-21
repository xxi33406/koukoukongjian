package world

import (
	"testing"

	"koukoukongjian/internal/config"
)

func newMatchWorld(t *testing.T) *World {
	t.Helper()
	cfg := config.Default()
	cfg.Map.HubCount = 0
	return NewWorld(cfg, 7)
}

// TestMatchResultCollectsAllPlayers 钉住"每个在座玩家一行"。
func TestMatchResultCollectsAllPlayers(t *testing.T) {
	w := newMatchWorld(t)
	p1, a := w.Players.Spawn()
	p2, b := w.Players.Spawn()
	a.Kills, a.Contribute, a.Gold = 3, 120.5, 40
	b.Kills, b.Contribute, b.Gold = 1, 20.0, 10
	w.Tick = 400

	res := w.SnapshotMatch("room-x", 0, 1000, 1020)
	if len(res.Players) != 2 {
		t.Fatalf("玩家行数 = %d，期望 2", len(res.Players))
	}
	// 行顺序按池槽位升序（Pool.Each 的顺序），因此与 Spawn 顺序一致。
	// 落盘后按 slot 检索所以顺序本身不重要——但测试要钉住它稳定，否则
	// 同种子两次跑出的文件内容会不同，diff 比对失效。
	if res.Players[0].Player != p1 || res.Players[1].Player != p2 {
		t.Fatalf("行顺序不是槽位升序：%v, %v", res.Players[0].Player, res.Players[1].Player)
	}
	if res.Players[0].Kills != 3 || res.Players[0].Gold != 40 {
		t.Fatalf("首行 = {kills %d, gold %d}，期望 3 与 40",
			res.Players[0].Kills, res.Players[0].Gold)
	}
}

// TestMatchResultSkipsEmptySlots 钉住池里被回收的槽位不产出行。
func TestMatchResultSkipsEmptySlots(t *testing.T) {
	w := newMatchWorld(t)
	id, _ := w.Players.Spawn()
	w.Players.Kill(id)

	res := w.SnapshotMatch("room-x", 0, 0, 0)
	if len(res.Players) != 0 {
		t.Fatalf("回收后玩家行数 = %d，期望 0", len(res.Players))
	}
}

// TestMatchResultCarriesMeta 钉住房间元信息原样带上。
func TestMatchResultCarriesMeta(t *testing.T) {
	w := newMatchWorld(t)
	w.Seed = 4242
	res := w.SnapshotMatch("room-7", OutcomeCleared, 1700000000, 1700001500)

	if res.RoomID != "room-7" {
		t.Fatalf("RoomID = %q，期望 room-7", res.RoomID)
	}
	if res.WorldSeed != 4242 {
		t.Fatalf("WorldSeed = %d，期望 4242", res.WorldSeed)
	}
	if res.Outcome != OutcomeCleared {
		t.Fatalf("Outcome = %v，期望 OutcomeCleared", res.Outcome)
	}
	if res.DurationSec != 1500 {
		t.Fatalf("DurationSec = %d，期望 1500", res.DurationSec)
	}
}

// TestMatchResultDurationNeverNegative 钉住时钟倒退（NTP 校时）不产生负时长。
func TestMatchResultDurationNeverNegative(t *testing.T) {
	w := newMatchWorld(t)
	res := w.SnapshotMatch("room-x", OutcomeWiped, 2000, 1000)
	if res.DurationSec != 0 {
		t.Fatalf("结束早于开始时时长 = %d，期望 0（不允许负数）", res.DurationSec)
	}
}

// TestOutcomeStrings 钉住枚举的字符串形式（日志与 JSON 都要用）。
func TestOutcomeStrings(t *testing.T) {
	cases := []struct {
		o    MatchOutcome
		want string
	}{
		{OutcomeUnknown, "unknown"},
		{OutcomeWiped, "wiped"},
		{OutcomeCleared, "cleared"},
	}
	for _, c := range cases {
		if got := c.o.String(); got != c.want {
			t.Fatalf("%d.String() = %q，期望 %q", c.o, got, c.want)
		}
	}
}

// TestMatchResultContributeIncludesDamageTaken 钉住两列语义不同。
//
// Damage 是"造成的伤害"，Contribute 按 spec L134 是"造成伤害 + 0.5×承受"。
// 若不分开算，两列永远相同，而 §11.1 把两列都列出来正是为了区分
// "谁在打"与"谁在扛"——一个只扛不打的玩家贡献值应该大于零。
//
// 用 TotalDamage/TotalDamageTaken 而不是 Contribute/DamageTaken：后者
// 是**据点级**的，SettleHub 每清一个据点就清零（"每据点重新计算"，
// spec L79）。拿它们算整局战报会让"最后一个据点没杀人"的玩家战报归零。
func TestMatchResultContributeIncludesDamageTaken(t *testing.T) {
	w := newMatchWorld(t)
	_, p := w.Players.Spawn()
	p.TotalDamage = 100 // 整局造成的伤害
	p.TotalDamageTaken = 60

	res := w.SnapshotMatch("room-x", 0, 0, 1)
	row := res.Players[0]
	if row.Damage != 100 {
		t.Fatalf("Damage = %v，期望 100（只算造成的伤害）", row.Damage)
	}
	// 100 + 0.5×60 = 130
	if row.Contribute != 130 {
		t.Fatalf("Contribute = %v，期望 130（100 + 0.5×60）", row.Contribute)
	}
}

// TestMatchResultIgnoresHubLevelCounters 钉住据点级计数不参与战报。
//
// 这条是防回归：有人图省事把 SnapshotMatch 改成读 Contribute/DamageTaken，
// 单局单据点时看不出问题，多据点时战报会只剩最后一个据点的数。
func TestMatchResultIgnoresHubLevelCounters(t *testing.T) {
	w := newMatchWorld(t)
	_, p := w.Players.Spawn()
	// 模拟"刚结算完一个据点"：据点级清零，整局累计保留。
	p.Contribute = 0
	p.DamageTaken = 0
	p.TotalDamage = 500
	p.TotalDamageTaken = 200

	res := w.SnapshotMatch("room-x", 0, 0, 1)
	row := res.Players[0]
	if row.Damage != 500 {
		t.Fatalf("Damage = %v，期望 500（读整局累计而不是据点级）", row.Damage)
	}
	if row.Contribute != 600 {
		t.Fatalf("Contribute = %v，期望 600（500 + 0.5×200）", row.Contribute)
	}
}

// TestMatchResultMaxWaveIsHighestHub 钉住进度取最高波数而不是别的。
func TestMatchResultMaxWaveIsHighestHub(t *testing.T) {
	w := newMatchWorld(t)
	// 无图世界没有 Hubs，先手工造三个：0/3/7 波。
	w.Hubs = []Hub{
		{Center: V(10, 10), RadiusM: 5, Wave: 0},
		{Center: V(30, 30), RadiusM: 5, Wave: 3},
		{Center: V(50, 50), RadiusM: 5, Wave: 7},
	}

	res := w.SnapshotMatch("room-x", 0, 0, 1)
	if res.MaxWave != 7 {
		t.Fatalf("MaxWave = %d，期望 7（取最高据点波数）", res.MaxWave)
	}
}
