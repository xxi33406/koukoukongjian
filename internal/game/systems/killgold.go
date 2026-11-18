package systems

import (
	"math"

	"koukoukongjian/internal/game/world"
)

// GoldPerKill 是每只怪给出的金币，击杀时直接进击杀者钱袋。
//
// 为什么不走掉落物（用户拍板决定 2）：金币是主经济来源（spec L77），
// 做成地上物件要玩家绕圈捡，与"击杀即收益"的爽感相反；而且血瓶和技能
// 已经占了两条掉落线，再加金币会让一次击杀掉三样东西，地面变成垃圾场。
//
// 8 的取法与 Progression.KillExp 同量级（KillExp=8）：经验与金币同步增长，
// 数值调参时两个数一起看就能判断"一局下来能买几件东西"。
// M6 的据点分账（spec L134）会把它换成分账公式，届时这个常量降级为基准值。
const GoldPerKill = 8

// GrantKillGold 把击杀奖励发给击杀者，返回实际发放数额。
//
// src 用 ev.Src 而不是"扫所有玩家找谁打的"：ApplyHits 产出的伤害事件里
// Src 就是子弹的 OwnerID，这是唯一可靠的击杀归属。
//
// dst 目前不参与计算，签名保留是为了 M6 —— 精英怪与 Boss 应当给更多金币，
// 而那时只要改这个函数内部查表，不必回头改所有调用点。
//
// 不满足条件时返回 0 而不报错：击杀是高频事件，而"击杀者是鬼魂"或
// "Src 失效"都是合法状态（Reap 自己产出的击杀事件 Src 就是 InvalidID），
// 不是错误。
//
// 不消耗 RNG：金币是定额，不是掷骰。若这里取一次随机数，回放校验的
// RNGCalls 会随击杀数漂移，而击杀数受网络时序影响，回放就对不上了。
func GrantKillGold(w *world.World, src, dst world.EntityID) uint32 {
	_ = dst
	if w == nil || !src.IsValid() {
		return 0
	}
	p := w.Players.Get(src)
	if p == nil || p.State != world.PlayerAlive {
		return 0
	}
	// 饱和加法：金币是累加字段，长局里能撞上限。M4 的 applyPickup 已经在
	// 金币累加处写过一次同样的钳位（pickup.go:195），两处口径必须一致。
	if p.Gold > math.MaxUint32-GoldPerKill {
		gained := math.MaxUint32 - p.Gold
		p.Gold = math.MaxUint32
		return gained
	}
	p.Gold += GoldPerKill
	return GoldPerKill
}
