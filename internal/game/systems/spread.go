package systems

import (
	"koukoukongjian/internal/game/world"
)

// 分离阈值（米）。
//
// 128×128 米地图的对角线约 181 米。Light=25 米大约是「隔了一个据点」，
// Extreme=45 米是「已经各自打不同据点」。数字取整而不是精确值：这两个
// 阈值是「玩家能感觉到队伍散了」的经验值，精确到米没有意义，而写
// 25.0/45.0 会让后来的人以为它们来自某次测算。
const (
	spreadLightDist   = 25.0
	spreadExtremeDist = 45.0
)

// UpdatePartySpread 按全队最远两人的距离判定分离等级，写进 World 并返回。
//
// 判据是「最远配对」而不是「两两平均」：四个人里 3 个抱团、1 个跑远了，
// 平均值可能很小，但那个跑远的人才是真正脱离队伍的人。用平均值会让
// 「三个人抱团、一个人被围攻」这种最需要增援的场景不触发惩罚。
//
// 只算活人：鬼魂被拴绳拉在活人附近（M5），而且它本来就不能战斗，
// 把它算进距离判定等于让「有人死了」本身触发怪物狂暴。
func UpdatePartySpread(w *world.World) world.SpreadLevel {
	if w == nil {
		return world.SpreadTogether
	}

	// 收集活人位置。最多 MaxSlots=4 个，用定长数组避免每 tick 一次分配。
	var pos [4]world.Vec2
	n := 0
	w.Players.Each(func(_ world.EntityID, p *world.PlayerState) bool {
		if p.State != world.PlayerAlive {
			return true
		}
		if n < len(pos) {
			pos[n] = p.Pos
			n++
		}
		return true
	})

	level := world.SpreadTogether
	if n >= 2 {
		var maxSq float64
		for i := 0; i < n; i++ {
			for j := i + 1; j < n; j++ {
				if d := pos[i].DistSq(pos[j]); d > maxSq {
					maxSq = d
				}
			}
		}
		switch {
		case maxSq > spreadExtremeDist*spreadExtremeDist:
			level = world.SpreadExtreme
		case maxSq > spreadLightDist*spreadLightDist:
			level = world.SpreadLight
		}
	}

	w.Spread = level
	return level
}

// ApplySpreadToMonsters 把分离等级写进每只怪物的标志位。
//
// 两级互斥而不是叠加：分离到极端时已经是狂暴，再叠加分裂会让「怪越来
// 越强」的反馈变成双份，玩家看到的是「我分开一下整个世界就崩了」。
// 从轻度退回一起时标志要被清掉——不清的话怪物会永久狂暴，而玩家
// 完全不知道发生了什么。
func ApplySpreadToMonsters(w *world.World) {
	if w == nil {
		return
	}
	w.Monsters.Each(func(_ world.EntityID, m *world.MonsterState) bool {
		if m.IsDead() {
			return true
		}
		// 先清两级标志，再按当前等级重设。用 &^ 而不是整体覆盖 Flags：
		// 其它标志（Aggroed/Buffable/Dead）不归这里管。
		m.Flags &^= world.MonsterFlagEnraged | world.MonsterFlagSplits

		switch w.Spread {
		case world.SpreadLight:
			m.Flags |= world.MonsterFlagSplits
		case world.SpreadExtreme:
			m.Flags |= world.MonsterFlagEnraged
		}
		return true
	})
}
