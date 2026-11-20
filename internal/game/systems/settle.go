package systems

import (
	"math"

	"koukoukongjian/internal/game/world"
)

// GoldPerKill 是每只怪为据点金币池贡献的金额。
//
// 从 M5 的 GrantKillGold 搬过来：数值不变（8，与 Progression.KillExp 同
// 量级），但用途从「直接给击杀者」变成「进据点池子等着分账」。一个据点
// 4 波 × 20 只 ≈ 640 金币摊给全队，比 M5 的「击杀者独吞 640」低一个
// 数量级——这正是 spec L79 想要的：钱按贡献分，不是按谁补了最后一刀。
const GoldPerKill uint32 = 8

// Contribution 把伤害与承伤折算成一个可比较的贡献值。
//
// 公式是 damage + 0.5*damageTaken（server-design §8，spec L134 待拍板
// 先按此实装）。0.5 的权重让「承伤也算贡献」但比不上输出：坦克不吃亏，
// 而光挨打不会成为主要收入来源——否则最省事的玩法是站在怪堆里不动。
const damageTakenWeight = 0.5

func Contribution(damage, damageTaken float64) float64 {
	return damage + damageTakenWeight*damageTaken
}

// HubShare 是一次按贡献分账的结果。
type HubShare struct {
	Player       world.EntityID
	Contribution float64
	Gold         uint32
}

// minShareRatio 是弱势玩家的保底份额比例（spec L79 建议 30%）。
//
// 这个数字不是「公平」，是「别让人退游」：一个刚死过一次、全程没打到
// 怪的玩家如果一分钱都没有，他会直接退出而不是再来一局（爽点⑤
// 「失败变投资」的机制保障）。
const minShareRatio = 0.30

// SettleHub 按贡献占比把 pool 金币分给所有存活玩家，返回每人的份额。
//
// 分配规则：
//  1. 每人先拿 pool*minShareRatio/n 的保底
//  2. 剩余的 pool*(1-minShareRatio) 按贡献占比分
//  3. 贡献总和为 0 时，池子按人数均分（不除零）
//  4. 余数给贡献最高者，保证总额恰好等于 pool
//
// 结算后清零所有参与者的 Contribute 与 DamageTaken（spec L79「每据点
// 重新计算」）。不清零会让第一个据点的贡献累积到第二个，强者一路领先，
// 「翻盘窗口」不存在。
//
// 只返回份额、不写玩家 Gold：那个动作在 room 层做。这样本函数可以纯
// 函数式断言（只读返回值），而「哪一处把金币注入玩家」在全仓只有一个
// 位置。
func SettleHub(w *world.World, pool uint32) []HubShare {
	if w == nil {
		return nil
	}

	// 收集存活玩家及其贡献。
	var players []world.EntityID
	var contribs []float64
	var total float64
	w.Players.Each(func(id world.EntityID, p *world.PlayerState) bool {
		// 鬼魂不参与：它拿钱没意义（商店只对活人开放），分给它是从
		// 活人手里抢钱。spec L45 说明死亡玩家的价值在搬运，不在分账。
		if p.State != world.PlayerAlive {
			return true
		}
		c := Contribution(p.Contribute, p.DamageTaken)
		// 防御：NaN 会让后续比较与累加全部污染，最终 uint32(NaN) 是
		// 平台相关的未定义值（AGENTS §2.6 第一条）。
		if math.IsNaN(c) || math.IsInf(c, 0) || c < 0 {
			c = 0
		}
		players = append(players, id)
		contribs = append(contribs, c)
		total += c
		return true
	})
	if len(players) == 0 {
		return nil
	}

	n := uint32(len(players))
	// 保底总额：pool * 30%，向下取整。乘 30 再除 100 而不是乘 0.3，
	// 避免浮点在边界上让「保底总额」超过 pool。
	minTotal := pool * 30 / 100
	minEach := minTotal / n
	rest := pool - minEach*n // 保底分完剩下的（含除不尽的余数）

	shares := make([]HubShare, len(players))
	for i, id := range players {
		shares[i] = HubShare{Player: id, Contribution: contribs[i], Gold: minEach}
	}

	// 剩下的按贡献占比分。
	if rest > 0 {
		if total <= 0 {
			// 没人贡献过：均分。除零会让所有人的份额变成 NaN。
			each := rest / n
			for i := range shares {
				shares[i].Gold += each
			}
			remainder := rest - each*n
			// 余数给槽位靠前的人（确定性，不用 map 遍历）。
			for i := uint32(0); i < remainder; i++ {
				shares[i].Gold++
			}
		} else {
			// 按贡献占比分配，用整数累加避免浮点误差累积。
			var given uint32
			topIdx := 0
			for i := range shares {
				share := uint32(float64(rest) * contribs[i] / total)
				shares[i].Gold += share
				given += share
				if contribs[i] > contribs[topIdx] {
					topIdx = i
				}
			}
			// 余数全给贡献最高者：保证总额恰好等于 pool，且不会因为
			// 取整让某个人少拿一分钱（玩家会看到「总额对不上」）。
			if given < rest {
				shares[topIdx].Gold += rest - given
			}
		}
	}

	// 结算后清零，每据点翻篇。
	w.Players.Each(func(_ world.EntityID, p *world.PlayerState) bool {
		p.Contribute = 0
		p.DamageTaken = 0
		return true
	})

	return shares
}

// CreditHubKill 把一次击杀的金币记进据点的池子，返回是否成功记入。
//
// 只有「玩家击杀」才进池：环境伤害（M7 的燃烧地面）或怪物互相残杀
// 不该产生金币。src 无效或不是玩家就直接丢弃。
func CreditHubKill(w *world.World, src world.EntityID, amount uint32) bool {
	if w == nil || !w.HasMap || amount == 0 {
		return false
	}
	p := w.Players.Get(src)
	if p == nil || p.State != world.PlayerAlive {
		return false
	}
	// 记到「击杀者所在据点」的池子里。找不到据点（在走廊上杀人）就记到
	// 最近的那一个——否则走廊上的击杀完全不产生金币，而玩家分不清
	// 「这条走廊算哪个据点」。
	idx := hubIndexAt(w, p.Pos)
	if idx < 0 {
		return false
	}
	h := &w.Hubs[idx]
	// 饱和加法：池子不该溢出，但溢出会让金额突然变成很小的数，
	// 而玩家看到的是「打完一波钱变少了」。
	if h.KillGold > math.MaxUint32-amount {
		h.KillGold = math.MaxUint32
		return true
	}
	h.KillGold += amount
	return true
}

// hubIndexAt 返回 pos 所在（或最近）据点的下标，没有据点返回 -1。
//
// 用「最近」而不是「必须在内」：走廊上的击杀也要进池，否则玩家分不清
// 「这条走廊算哪个据点」时钱就凭空消失了。
func hubIndexAt(w *world.World, pos world.Vec2) int {
	if len(w.Hubs) == 0 {
		return -1
	}
	best := -1
	var bestSq float64
	for i := range w.Hubs {
		d := pos.DistSq(w.Hubs[i].Center)
		if best < 0 || d < bestSq {
			bestSq = d
			best = i
		}
	}
	return best
}
