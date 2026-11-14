package systems

import (
	"koukoukongjian/internal/game/world"
)

// IntegrateBullets 按速度推进所有子弹一个子步，并回收寿命耗尽或撞墙的。
//
// 与玩家移动分开的理由：玩家位置由 MoveIntent 驱动、要判断存活状态；
// 子弹由自己的 Vel 驱动、要处理寿命。混在一个循环里会变成两个 if 堆在
// 一起，而且以后给子弹加弹道（追踪、抛物线）时改的是完全不同的代码。
//
// 必须在移动时保存 PrevPos：扫掠判定要用「上一子步位置 → 当前位置」这条
// 线段。不能靠"新位置减去速度*dt"反推——一旦有逻辑改了速度（反弹、
// 加速），反推就会算错起点。
//
// 子步位移：20 m/s × 1/60 s ≈ 0.333 m，而怪物半径 0.4 m。点判定会漏掉
// 擦边命中，必须扫掠（Task 7）。撞墙同理：dt 放大时一步能穿多格（测试里
// dt=0.2 一步 4 m），终点点判定会直接穿透墙列，所以用 PrevPos→Pos 的
// 格级线段判定——LineWalkable 沿线任一格是墙即回收。
//
// 子弹只做中心点判定，不减半径：弹径 0.12~0.18 m 小于半格，贴墙飞行的
// 边缘半格误差灰盒接受，省掉线段到格矩形的精确求交。
func IntegrateBullets(w *world.World, dt float64) {
	// 先收集再回收：Pool.Each 回调里 Kill 会改动槽位状态，
	// 在遍历中删除是未定义行为。
	var expired []world.EntityID

	w.Bullets.Each(func(id world.EntityID, b *world.BulletState) bool {
		b.PrevPos = b.Pos
		b.Pos.X += b.Vel.X * dt
		b.Pos.Y += b.Vel.Y * dt

		// 撞墙：起点（PrevPos）也判——贴墙出生或墙里出生的子弹立即消失，
		// 否则一枚嵌墙子弹会隔墙命中墙另一侧的目标。
		b.Life -= float32(dt)
		if b.Life <= 0 || !w.Terrain.LineWalkable(b.PrevPos, b.Pos) {
			expired = append(expired, id)
		}
		return true
	})

	for _, id := range expired {
		w.Bullets.Kill(id)
	}
}
