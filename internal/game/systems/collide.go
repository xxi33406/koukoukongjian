package systems

import (
	"koukoukongjian/internal/game/world"
)

// Hit 是一次候选命中，由 Collide 产出、由 ApplyHits 消费。
//
// 刻意不在这里改血量：碰撞与伤害分开的理由是"碰撞可能产出多类结果"
// （命中、反射、穿透继续），而伤害只有一个入口。合并会让 ResolveDamage
// 被绕过（docs/combat-design-v0.1.md §4.1）。
type Hit struct {
	Bullet  world.EntityID
	Target  world.EntityID
	Src     world.EntityID // 伤害归属，取子弹的 OwnerID
	Damage  float64
	Element world.Element
	// Reflector 是反射弹幕的归属者（spec L114），非反射命中为 InvalidID。
	//
	// 必须一路带到 ResolveDamage：子弹的 OwnerID 在反射那一刻已被改成
	// 反射区域的主人，所以这里的 Src 拿到的就是"这发子弹现在属于谁"。
	// 但战绩要单独统计"反射击杀"（server-design §11.1 的 reflect_kills），
	// 而"属于谁"和"是不是靠反射拿的"是两个问题——后者只有
	// BulletState.ReflectorID 知道。丢掉它就没法区分"自己打的"和
	// "自己弹回去的"（M6 把它写进了快照供客户端变色，M7 起也进战绩）。
	Reflector world.EntityID
}

// Collide 检测本子步内所有子弹与怪物的碰撞，返回候选命中列表。
//
// 用扫掠判定（线段 vs 圆），不是点判定：子弹 20 m/s 每子步走 0.333 m，
// 怪物半径 0.4 m。点判定会漏掉所有"这一步恰好跨过去"的命中，而且这种
// 漏判没有任何报错——表现为"子弹从怪身上穿过却没扣血"，极难查。
//
// 半径相加：子弹有半径、怪物有半径，两个实体相撞的判据是中心距小于
// 两者之和，所以传进 SegmentIntersectsCircle 的 r 是 m.Radius + b.Radius。
//
// 遍历而不查空间索引：网格把玩家/怪物/子弹混存且不带池标识
// （internal/game/world/grid.go:101），查出来分不清类型。M2 怪物是个位数，
// 实测 200 实体以内暴力更快（decisions-game-backend.md §4）。
//
// 一颗非穿透子弹同一子步只命中一个目标：否则它会打穿整排怪。
func Collide(w *world.World) []Hit {
	var hits []Hit

	w.Bullets.Each(func(bID world.EntityID, b *world.BulletState) bool {
		var found *Hit
		var bestSq float64

		w.Monsters.Each(func(mID world.EntityID, m *world.MonsterState) bool {
			// 已死的怪不再吃伤害。判据是显式标志而不是 HP<=0：M4 起
			// 治疗能把 HP 拉回正数，见 world.MonsterFlagDead。
			if m.IsDead() {
				return true
			}
			r := float64(m.Radius) + float64(b.Radius)
			if !world.SegmentIntersectsCircle(b.PrevPos, b.Pos, m.Pos, r) {
				return true
			}
			// 多个目标同时命中时取最近的那个：子弹应该先打到挡在
			// 前面的怪，而不是穿过它去打后面的。用线段起点到怪物中心
			// 的距离平方比较。
			d := m.Pos.Sub(b.PrevPos).LenSq()
			if found == nil || d < bestSq {
				bestSq = d
				found = &Hit{
					Bullet:    bID,
					Target:    mID,
					Src:       b.OwnerID,
					Damage:    b.Damage,
					Element:   b.Element,
					Reflector: b.ReflectorID,
				}
			}
			return true
		})

		if found != nil {
			hits = append(hits, *found)
		}
		return true
	})

	return hits
}
