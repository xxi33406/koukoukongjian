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
// 命中后回收子弹：M2 没有穿透（Pierce=0）。不回收的话子弹会沿轨迹继续
// 命中后面所有怪，一颗子弹打穿一排。
//
// 空事件（Amount<=0）不下发：目标已死或伤害<=0 时 ResolveDamage 不改状态，
// 这种事件对客户端没有意义，下发只会浪费带宽、让事件计数虚高。
func ApplyHits(w *world.World, hits []Hit) []world.DamageEvent {
	events := make([]world.DamageEvent, 0, len(hits))

	for _, h := range hits {
		ev := w.ResolveDamage(h.Src, h.Target, h.Damage, h.Element, world.DamageTags{})
		if ev.Amount > 0 {
			events = append(events, ev)
		}
		if h.Bullet.IsValid() {
			w.Bullets.Kill(h.Bullet)
		}
	}

	return events
}
