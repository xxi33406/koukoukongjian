package build

import "koukoukongjian/internal/game/world"

// ReactionKind 是两个元素交汇时产生的反应类型。
//
// 零值是 ReactionNone，与「没有反应」同义——这样未定义的组合天然正确，
// 不需要在每个查表处写 if。
type ReactionKind uint8

const (
	ReactionNone  ReactionKind = iota // 不反应：两条子弹各自继续飞
	ReactionSteam                     // 蒸汽：生成减速云
	// M7 追加：
	// ReactionIgnite  燃烧（火+油）
	// ReactionConduct 导电（水+雷）
)

func (k ReactionKind) String() string {
	switch k {
	case ReactionSteam:
		return "steam"
	default:
		return "none"
	}
}

// reactionTable 是 5×5 的元素组合表，索引直接是 world.Element 的 iota 值。
//
// 用定长数组而不是 map：查表在交汇判定里每 tick 可能上百次，而
// Element 的取值范围已经被 world 的 iota 封死（0-4）。数组查表零分配、
// 零哈希，也不会出现「map 里存了一个 Element(99) 的键」这种越界数据。
//
// 表的每一行与对应列必须手动保持一致（对称性）。这是手写表的固有风险，
// 所以 elements_test.go 里有一条 25 组穷举的对称性测试钉住它。
var reactionTable = [5][5]ReactionKind{
	//                 none  fire         water        oil  thunder
	/* none    */ {0, 0, 0, 0, 0},
	/* fire    */ {0, 0, ReactionSteam, 0, 0}, // 火+油 = 点燃（M7）
	/* water   */ {0, ReactionSteam, 0, 0, 0}, // 水+雷 = 导电（M7）
	/* oil     */ {0, 0, 0, 0, 0},
	/* thunder */ {0, 0, 0, 0, 0},
}

// React 查两个元素的反应结果。参数顺序无关（表是对称的）。
//
// 越界索引返回 ReactionNone 而不是 panic：Element 是 uint8，理论上能被
// 一个损坏的存档或协议值喂进 99。这里让越界退化成「不反应」，交汇逻辑
// 得到 ReactionNone 就跳过，不会炸整个 tick。
func React(a, b world.Element) ReactionKind {
	if int(a) >= len(reactionTable) || int(b) >= len(reactionTable) {
		return ReactionNone
	}
	return reactionTable[a][b]
}

// ReactionZoneKind 把反应结果映射成它要生成的区域类型。
//
// 单独抽出来是为了让 M7 加新反应时只改这一处：交汇判定只调
// ReactionZoneKind，不关心有哪些反应。ReactionNone 返回 0 表示不生成。
func ReactionZoneKind(k ReactionKind) world.ZoneKind {
	switch k {
	case ReactionSteam:
		return world.ZoneSteam
	default:
		return 0
	}
}
