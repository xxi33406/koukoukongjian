package systems

import (
	"testing"

	"koukoukongjian/internal/game/world"
)

// TestContributionFormula 钉住贡献公式。
//
// server-design §8 定的是 damage + 0.5*damageTaken（spec L134 待拍板先按此
// 实装）。0.5 的权重是「承伤也算贡献但比不上输出」——坦克不吃亏，但
// 光挨打不能成为主要收入来源。
func TestContributionFormula(t *testing.T) {
	cases := []struct {
		damage, taken, want float64
	}{
		{100, 0, 100},
		{0, 100, 50},
		{100, 100, 150},
		{0, 0, 0},
	}
	for _, c := range cases {
		if got := Contribution(c.damage, c.taken); got != c.want {
			t.Fatalf("Contribution(%v,%v) = %v，期望 %v", c.damage, c.taken, got, c.want)
		}
	}
}

// TestSettleHubSplitsByContribution 钉住按占比分配。
func TestSettleHubSplitsByContribution(t *testing.T) {
	w := newSystemsWorld(t, 7)

	// 三个玩家：贡献 60/30/10。
	p1, s1 := spawnPlayerAt(t, w, 10, 10)
	p2, s2 := spawnPlayerAt(t, w, 12, 10)
	p3, s3 := spawnPlayerAt(t, w, 14, 10)
	s1.Contribute, s2.Contribute, s3.Contribute = 60, 30, 10

	shares := SettleHub(w, 100)

	if len(shares) != 3 {
		t.Fatalf("分给了 %d 人，期望 3 人", len(shares))
	}
	total := uint32(0)
	for _, s := range shares {
		total += s.Gold
	}
	if total != 100 {
		t.Fatalf("分出去的金币总额 = %d，期望 100（不能凭空多也不能少）", total)
	}

	gold := map[world.EntityID]uint32{}
	for _, s := range shares {
		gold[s.Player] = s.Gold
	}
	// 高贡献者必须拿得比低贡献者多。
	if gold[p1] <= gold[p2] {
		t.Fatalf("贡献 60 的拿了 %d，贡献 30 的拿了 %d，期望前者更多", gold[p1], gold[p2])
	}
	if gold[p2] <= gold[p3] {
		t.Fatalf("贡献 30 的拿了 %d，贡献 10 的拿了 %d，期望前者更多", gold[p2], gold[p3])
	}
}

// TestSettleHubMinimumShare 钉住 30% 保底。
//
// spec L79「建议留保底（贡献最低者约 30%），防弱势玩家彻底进不了商店」。
// 这条是「失败变投资」爽点的机制保障：一个刚死过一次、全程没打到怪的
// 玩家如果一分钱都没有，他会直接退出而非再来一局。
func TestSettleHubMinimumShare(t *testing.T) {
	w := newSystemsWorld(t, 7)

	p1, s1 := spawnPlayerAt(t, w, 10, 10)
	p2, s2 := spawnPlayerAt(t, w, 12, 10)
	s1.Contribute = 990
	s2.Contribute = 10 // 极弱

	shares := SettleHub(w, 1000)

	gold := map[world.EntityID]uint32{}
	for _, s := range shares {
		gold[s.Player] = s.Gold
	}
	// 保底 = pool * 30% / n = 1000*0.3/2 = 150。
	if gold[p2] < 150 {
		t.Fatalf("弱贡献者拿了 %d，期望至少 150（pool*30%%/2）", gold[p2])
	}
	if gold[p1] <= gold[p2] {
		t.Fatalf("贡献 990 的拿了 %d，贡献 10 的拿了 %d，期望前者更多", gold[p1], gold[p2])
	}
}

// TestSettleHubZeroContributionSplitsEvenly 钉住没人打过怪时的退路。
//
// 一个据点的怪可能全被环境杀死（M7 的燃烧地面），或者全队刚复活还没
// 开火。此时总和为 0，除零会让所有人的份额变成 NaN → uint32 转换得到
// 平台相关的未定义值（AGENTS §2.6 第一条）。
func TestSettleHubZeroContributionSplitsEvenly(t *testing.T) {
	w := newSystemsWorld(t, 7)

	p1, _ := spawnPlayerAt(t, w, 10, 10)
	p2, _ := spawnPlayerAt(t, w, 12, 10)
	p3, _ := spawnPlayerAt(t, w, 14, 10)

	shares := SettleHub(w, 100)

	if len(shares) != 3 {
		t.Fatalf("分给了 %d 人，期望 3 人", len(shares))
	}
	total := uint32(0)
	for _, s := range shares {
		total += s.Gold
	}
	if total != 100 {
		t.Fatalf("总额 = %d，期望 100", total)
	}
	gold := map[world.EntityID]uint32{}
	for _, s := range shares {
		gold[s.Player] = s.Gold
	}
	// 100/3 = 33 余 1，三个人拿的差额不超过 1。
	maxG, minG := uint32(0), ^uint32(0)
	for _, g := range gold {
		if g > maxG {
			maxG = g
		}
		if g < minG {
			minG = g
		}
	}
	if maxG-minG > 1 {
		t.Fatalf("均分时最大 %d 最小 %d，差额 %d 超过 1", maxG, minG, maxG-minG)
	}
	_ = p1
	_ = p2
	_ = p3
}

// TestSettleHubSkipsDeadPlayers 钉住鬼魂不参与分账。
//
// 鬼魂拿钱没有意义（它不能买东西，商店对活人开放），而把池子分给它
// 等于从活人手里抢钱。spec L45 的血瓶规则说明死亡玩家是「后勤位」，
// 它的贡献通过搬运体现，不通过分账。
func TestSettleHubSkipsDeadPlayers(t *testing.T) {
	w := newSystemsWorld(t, 7)

	alive, a := spawnPlayerAt(t, w, 10, 10)
	_, d := spawnPlayerAt(t, w, 12, 10)
	a.Contribute = 50
	d.Contribute = 50
	d.State = world.PlayerGhost

	shares := SettleHub(w, 100)

	if len(shares) != 1 {
		t.Fatalf("分给了 %d 人，期望 1 人（鬼魂不参与）", len(shares))
	}
	if shares[0].Player != alive {
		t.Fatalf("分给了 %d，期望 %d", shares[0].Player, alive)
	}
	if shares[0].Gold != 100 {
		t.Fatalf("唯一活人拿了 %d，期望 100", shares[0].Gold)
	}
}

// TestSettleHubEmptyWorld 钉住零人时不 panic。
func TestSettleHubEmptyWorld(t *testing.T) {
	w := newSystemsWorld(t, 7)
	if shares := SettleHub(w, 100); len(shares) != 0 {
		t.Fatalf("空世界分出 %d 份，期望 0", len(shares))
	}
}

// TestSettleHubZeroPool 钉住池子为空时全给 0。
func TestSettleHubZeroPool(t *testing.T) {
	w := newSystemsWorld(t, 7)
	_, p := spawnPlayerAt(t, w, 10, 10)
	p.Contribute = 100

	shares := SettleHub(w, 0)
	if len(shares) != 1 {
		t.Fatalf("分给了 %d 人，期望 1", len(shares))
	}
	if shares[0].Gold != 0 {
		t.Fatalf("池子为 0 时拿了 %d，期望 0", shares[0].Gold)
	}
}

// TestSettleHubResetsContribute 钉住结算后清零（每据点翻篇）。
//
// spec L79「每据点重新计算」：不清零的话第一个据点的贡献会累积到第二个，
// 强者一路领先，「翻盘窗口」不存在。
func TestSettleHubResetsContribute(t *testing.T) {
	w := newSystemsWorld(t, 7)
	_, p := spawnPlayerAt(t, w, 10, 10)
	p.Contribute = 500
	p.DamageTaken = 200

	SettleHub(w, 100)

	if p.Contribute != 0 {
		t.Fatalf("结算后 Contribute = %v，期望 0", p.Contribute)
	}
	if p.DamageTaken != 0 {
		t.Fatalf("结算后 DamageTaken = %v，期望 0", p.DamageTaken)
	}
}

// TestSettleHubResultIgnoresDeadContributions 钉住鬼魂的贡献不进分母。
//
// 鬼魂不参与分配，它的贡献也不该参与占比计算——否则活人的
// 份额会因为「一个死人在旁边打了半天」而被稀释，而那份钱没有
// 任何人拿到（总额就对不上了）。
func TestSettleHubResultIgnoresDeadContributions(t *testing.T) {
	w := newSystemsWorld(t, 7)

	_, a := spawnPlayerAt(t, w, 10, 10)
	_, d := spawnPlayerAt(t, w, 12, 10)
	a.Contribute = 50
	d.Contribute = 1000 // 死人的贡献很大
	d.State = world.PlayerGhost

	shares := SettleHub(w, 100)

	if len(shares) != 1 {
		t.Fatalf("分给了 %d 人，期望 1", len(shares))
	}
	// 唯一活人应当拿到全部 100（保底 30 + 剩余 70 全归他）。
	if shares[0].Gold != 100 {
		t.Fatalf("唯一活人拿了 %d，期望 100（鬼魂的贡献不该参与占比）", shares[0].Gold)
	}
}

// TestCreditHubKill 钉住击杀金币进据点池而不是直给玩家。
func TestCreditHubKill(t *testing.T) {
	w := newSystemsWorld(t, 7)

	// newSystemsWorld 关掉了地图，这里手动造两个据点。
	w.HasMap = true
	w.Hubs = []world.Hub{
		{Center: world.V(10, 10), RadiusM: 5, State: world.HubIdle},
		{Center: world.V(40, 40), RadiusM: 5, State: world.HubIdle},
	}

	pid, p := spawnPlayerAt(t, w, 10.5, 10.5)

	before := p.Gold
	if ok := CreditHubKill(w, pid, 8); !ok {
		t.Fatalf("CreditHubKill 返回 false，期望 true")
	}
	if p.Gold != before {
		t.Fatalf("击杀后玩家金币 = %d，期望保持 %d（M6 不再直给）", p.Gold, before)
	}
	if w.Hubs[0].KillGold != 8 {
		t.Fatalf("据点 0 的池子 = %d，期望 8", w.Hubs[0].KillGold)
	}
	if w.Hubs[1].KillGold != 0 {
		t.Fatalf("据点 1 的池子 = %d，期望 0（击杀发生在据点 0）", w.Hubs[1].KillGold)
	}
}

// TestCreditHubKillRejectsNonPlayer 钉住环境击杀不进池。
func TestCreditHubKillRejectsNonPlayer(t *testing.T) {
	w := newSystemsWorld(t, 7)
	w.HasMap = true
	w.Hubs = []world.Hub{{Center: world.V(10, 10), RadiusM: 5, State: world.HubIdle}}

	// 无效来源（环境伤害）。
	if ok := CreditHubKill(w, world.InvalidID, 8); ok {
		t.Fatalf("无效来源被记入池子，期望 false")
	}
	if w.Hubs[0].KillGold != 0 {
		t.Fatalf("池子 = %d，期望 0", w.Hubs[0].KillGold)
	}
}

// TestCreditHubKillCorridorGoesToNearest 钉住走廊击杀记到最近据点。
//
// 否则玩家分不清「这条走廊算哪个据点」时，钱就凭空消失了。
func TestCreditHubKillCorridorGoesToNearest(t *testing.T) {
	w := newSystemsWorld(t, 7)
	w.HasMap = true
	w.Hubs = []world.Hub{
		{Center: world.V(10, 10), RadiusM: 5, State: world.HubIdle},
		{Center: world.V(40, 40), RadiusM: 5, State: world.HubIdle},
	}

	// 站在两个据点之间的走廊上，离据点 0 更近。
	pid, _ := spawnPlayerAt(t, w, 20, 20)

	if ok := CreditHubKill(w, pid, 8); !ok {
		t.Fatalf("走廊上的击杀被丢弃，期望记到最近据点")
	}
	if w.Hubs[0].KillGold != 8 {
		t.Fatalf("据点 0 的池子 = %d，期望 8（走廊记到最近据点）", w.Hubs[0].KillGold)
	}
	if w.Hubs[1].KillGold != 0 {
		t.Fatalf("据点 1 的池子 = %d，期望 0", w.Hubs[1].KillGold)
	}
}

// TestSettleHubRemainderGoesToTopContributor 钉住余数分配。
//
// 前面的用例都恰好整除（pool=100、3 人、60:30:10 → 42+21+7=70），所以
// 「余数丢了」这种错不会暴露，而玩家看到的是「总额对不上池子」。这里
// 用一个必然除不尽的组合：pool=101、3 人、贡献 1:1:1。
func TestSettleHubRemainderGoesToTopContributor(t *testing.T) {
	w := newSystemsWorld(t, 7)

	p1, s1 := spawnPlayerAt(t, w, 10, 10)
	p2, s2 := spawnPlayerAt(t, w, 12, 10)
	p3, s3 := spawnPlayerAt(t, w, 14, 10)
	// 贡献比 1:1:2，且 pool=103 让保底与占比都除不尽。
	s1.Contribute = 1
	s2.Contribute = 1
	s3.Contribute = 2

	shares := SettleHub(w, 103)

	total := uint32(0)
	for _, s := range shares {
		total += s.Gold
	}
	if total != 103 {
		t.Fatalf("分出去的金币总额 = %d，期望 103（余数不能丢）", total)
	}

	gold := map[world.EntityID]uint32{}
	for _, s := range shares {
		gold[s.Player] = s.Gold
	}
	// 贡献最高的必须拿最多。
	if gold[p3] <= gold[p1] || gold[p3] <= gold[p2] {
		t.Fatalf("贡献 2 的拿了 %d，贡献 1 的拿了 %d/%d，期望前者更多", gold[p3], gold[p1], gold[p2])
	}
}

// TestSettleHubEvenSplitRemainder 钉住均分路径的余数。
//
// 0 贡献时按人数均分，pool=100 三人 → 33/33/34 而不是 33/33/33。
func TestSettleHubEvenSplitRemainder(t *testing.T) {
	w := newSystemsWorld(t, 7)

	spawnPlayerAt(t, w, 10, 10)
	spawnPlayerAt(t, w, 12, 10)
	spawnPlayerAt(t, w, 14, 10)

	shares := SettleHub(w, 100)

	total := uint32(0)
	for _, s := range shares {
		total += s.Gold
	}
	if total != 100 {
		t.Fatalf("均分总额 = %d，期望 100", total)
	}
}
