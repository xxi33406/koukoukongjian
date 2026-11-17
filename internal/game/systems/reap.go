package systems

import (
	"koukoukongjian/internal/game/world"
)

// Reap 回收所有已死亡的实体，产出击杀事件。
//
// 为什么必须单独一个系统、不能在上伤害的地方顺手删：
// docs/combat-design-v0.1.md §4.2 第 6 步写的是"只标记不回收"。此前的
// Collide 与 ApplyHits 都在遍历实体池，途中 Kill 会改动槽位状态，导致
// 漏遍历（后面的实体被跳过）或重复遍历。把回收集中到最后一步，遍历与
// 修改就被彻底分开了。
//
// 击杀事件单独产出而不是从 ApplyHits 拿：ApplyHits 的 IsKill 是"这一发
// 把它打死了"，而 Reap 才是"它真的离开了世界"。两者在同一 tick 内通常
// 相等，但若有任何别的路径把 HP 打到 0（中毒、环境伤害），只有 Reap 会
// 产出击杀。分账与掉落必须挂在 Reap 上。
//
// 幂等：Kill 过的怪 HP 已随槽位清零且不再 Alive，第二次调用时 Each 不会
// 再看到它，不重复产出事件、不重复 Kill。
func Reap(w *world.World) []world.DamageEvent {
	var kills []world.DamageEvent

	var dead []world.EntityID
	w.Monsters.Each(func(id world.EntityID, m *world.MonsterState) bool {
		// 判据是显式 MonsterFlagDead。M2 这里用的是 HP<=0，那时的前提是
		// HP 只减不增；M4 的治疗/吸血会把 HP 拉回正数，再用 HP 判就会把
		// 被救回来的怪顺手删掉。挂账已在此兑付。
		if m.IsDead() {
			dead = append(dead, id)
		}
		return true
	})

	for _, id := range dead {
		kills = append(kills, world.DamageEvent{
			Dst:    id,
			IsKill: true,
		})
		w.Monsters.Kill(id)
	}

	return kills
}
