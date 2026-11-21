package systems

import (
	"koukoukongjian/internal/game/world"
)

// ApplyHits 把所有候选命中交给 ResolveDamage 结算，并回收命中的子弹。
//
// 这个函数是"碰撞"与"伤害"之间唯一的桥：Hit 里的数值必须原样传进
// ResolveDamage，不能在这里做任何加减——一旦在这里改数值，那条路径就
// 绕过了唯一入口，加词条时会漏掉（combat-design-v0.1.md §4.1）。
//
// 关于技能的伤害倍率（p.Derived.DamageMul）：它**不**在这里乘，而是在
// spawnBullet（fire.go）把子弹造出来的那一刻就乘进了 b.Damage。两个理由：
//
//  1. 语义。子弹是独立实体，它带着"出膛时是多少伤"这个事实飞出去。若在
//     命中时才取来源的当前倍率，一颗"装强化弹头时打出"的子弹会在飞行
//     途中掉伤害——中途换技能（spec L37）不该改写已经发生的事。冲刺用
//     SpeedBuffMul 快照是同一条原则。
//  2. 唯一。一旦这里也乘，子弹就会吃两次倍率（10×1.2×1.2=14.4），而
//     症状只是"伤害比预期高一点"，极难联想到乘了两遍。伤害放大点必须
//     只有一个，它选了"生成时"。
//
// 代价要写明：任何绕开 spawnBullet 造出来的伤害（未来的近战、道具爆炸）
// 都不会自动吃到 DamageMul，得各自在生成/调用处乘。这条缺口记在任务
// 汇报的已知限制里——它比"两处都乘"安全，因为漏乘表现为"没加成"，
// 而重复乘表现为"数值莫名其妙"，前者能靠实测发现，后者只能靠算。
//
// 命中后回收子弹：M2 没有穿透（Pierce=0）。不回收的话子弹会沿轨迹继续
// 命中后面所有怪，一颗子弹打穿一排。
//
// 空事件（Amount<=0）不下发：目标已死或伤害<=0 时 ResolveDamage 不改状态，
// 这种事件对客户端没有意义，下发只会浪费带宽、让事件计数虚高。
func ApplyHits(w *world.World, hits []Hit) []world.DamageEvent {
	events := make([]world.DamageEvent, 0, len(hits))

	for _, h := range hits {
		// 子弹只打怪物（M2 起 Collide 只遍历怪物池），所以类别固定是
		// 怪物；EntityClassMonster 恰好是零值，写出来是为了让读者不必
		// 去查零值代表什么。
		ev := w.ResolveDamage(h.Src, h.Target, world.EntityClassMonster, h.Damage, h.Element, world.DamageTags{}, h.Reflector)
		if ev.Amount > 0 {
			events = append(events, ev)
		}
		if h.Bullet.IsValid() {
			w.Bullets.Kill(h.Bullet)
		}
	}

	return events
}
