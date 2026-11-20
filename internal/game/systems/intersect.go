package systems

import (
	"koukoukongjian/internal/game/build"
	"koukoukongjian/internal/game/world"
)

// intersectionTolerance 是交汇判定的距离余量（米）。
//
// server-design §6.3 写的是「2px」。本项目的量化单位是 1/32 米
// （proto.PosScale = 32），2px 即 2/32 = 0.0625 米。留一点余量是因为
// 两条子弹往往在同一子步内擦肩而过，严格相等会让「看起来撞上了」的
// 判定频繁落空——玩家只会觉得「这功能有 bug」。
const intersectionTolerance = 2.0 / 32.0

// Intersection 是一次交汇的结果，供 room 打日志与统计。
type Intersection struct {
	A, B     world.EntityID
	Pos      world.Vec2
	Reaction build.ReactionKind
	ZoneID   world.EntityID
}

// IntersectBullets 检测本 tick 内可以发生元素反应的子弹对，返回发生的反应列表。
//
// 判定条件（全部满足才反应）：
//   - 两条子弹属于不同玩家（自己的子弹不交汇）
//   - 两条子弹都带 BulletFlagInteractable（spec L67：交互性是词条）
//   - 圆心距 < 两半径之和 + intersectionTolerance
//   - build.React(a.Element, b.Element) != ReactionNone
//
// 反应成功时销毁两条子弹，并在交点生成对应类型的区域。
//
// 用子弹网格而不是双层遍历：见 bulletgrid.go 的说明。
//
// 「一颗子弹一 tick 最多参与一次交汇」由两条既有机制保证，不靠额外的
// 去重表。改动下面任何一条都必须重新审视这个结论：
//
//  1. Pool.Each 按槽位下标升序遍历，且跳过 alive=false 的槽位。Kill 之后
//     该槽位不会再作为 a 出现在外层循环里。
//  2. 网格是本 tick 开头建的，Near 可能返回本 tick 内已被 Kill 的 id。
//     这些 id 被下面 w.Bullets.Get(bID) == nil 全部拦住——Get 校验世代号，
//     读到的是 nil 而不是清零后的零值数据（那会生成一片零位置的区域）。
//
// bID.Index() <= aID.Index() 是**性能优化不是正确性机制**：没有它，配对
// 仍会被上面两条拦住，只是每对要判两遍。变异测试确认删掉它所有用例都
// 通过——留着的理由是把内层循环的次数减半。
//
// 早先的草稿里还有一个 consumed 切片做「已消费」标记。同样的变异测试
// 证明删掉它后全部用例仍然通过，已删除：留着会让后来的人以为去重靠它，
// 而真正兜底的是 Get 的世代校验。
func IntersectBullets(w *world.World) []Intersection {
	if w == nil || w.Bullets.Len() < 2 {
		return nil
	}

	grid := NewBulletGrid(2.0)
	grid.Build(w)

	var out []Intersection

	w.Bullets.Each(func(aID world.EntityID, a *world.BulletState) bool {
		if !a.Flags.Has(world.BulletFlagInteractable) {
			return true
		}
		for _, bID := range grid.Near(a.Pos) {
			// 同一对只判一次：池的 Each 是槽位升序，用 Index 比较
			// 能保证 (a,b) 与 (b,a) 只走其中一次。
			if bID.Index() <= aID.Index() {
				continue
			}
			b := w.Bullets.Get(bID)
			if b == nil {
				continue // 网格里的旧 id
			}
			if !b.Flags.Has(world.BulletFlagInteractable) {
				continue
			}
			if b.OwnerID == a.OwnerID {
				continue // 自己的子弹不交汇
			}

			r := float64(a.Radius) + float64(b.Radius) + intersectionTolerance
			if a.Pos.DistSq(b.Pos) > r*r {
				continue
			}

			reaction := build.React(a.Element, b.Element)
			if reaction == build.ReactionNone {
				// 不反应：两条子弹都继续飞。刻意不销毁——玩家的直觉
				// 是「没有反应就该穿过去」，而不是「被队友吃掉了」。
				continue
			}

			pos := a.Pos.Add(b.Pos).Scale(0.5)

			// 先记下结果再销毁：Kill 会清零槽位，之后读 a.Element 拿到的是零值。
			zoneKind := build.ReactionZoneKind(reaction)
			// 归属给谁：交汇是两人共同的成果，但伤害/效果总要记一个人。
			// 取 A（槽位小的一方）——确定性优先，且两人贡献在 Contribute
			// 里本来就不通过区域结算（区域只服务元素反应本身）。
			var zoneID world.EntityID
			if zoneKind != 0 {
				zoneID = SpawnZone(w, pos, 3.0, 4.0, zoneKind, a.OwnerID, reactionElement())
			}

			out = append(out, Intersection{
				A:        aID,
				B:        bID,
				Pos:      pos,
				Reaction: reaction,
				ZoneID:   zoneID,
			})

			w.Bullets.Kill(aID)
			w.Bullets.Kill(bID)

			// a 已被销毁，这一轮不能再拿它配对。
			return true
		}
		return true
	})

	return out
}

// reactionElement 返回反应产生的区域应该带哪个元素。
//
// 当前所有反应都产出无元素区域；M7 若按反应类型区分再扩展。
//
// 蒸汽云带 ElementNone：它不是火也不是水，是一种新东西。带 Fire 会让
// 后续的水子弹与它再反应一次（蒸汽+水），而那在表里是未定义组合，
// 表现为「蒸汽云莫名其妙消失」。
func reactionElement() world.Element {
	return world.ElementNone
}
