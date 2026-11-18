package world

import "strconv"

// Hub 是一个据点：布局数据（mapgen 生成时定死）+ 运行时波次状态（waves 系统用）。
type Hub struct {
	Center  Vec2    // 世界坐标（格中心）
	RadiusM float64 // 触发半径（米）= HubRadiusCells × cellSize
	Wave    int     // 已刷到的波次（0 = 未激活）
	State   HubState
	NextAt  uint32       // 下一波刷怪的 tick 截止
	WaveIDs [32]EntityID // 当前波怪物（Validate 保证 MonstersPerWave ≤ 32，定长免分配）
	WaveN   int          // 当前波实际怪数
}

// HubState 是据点波次的运行时状态。
//
// M4 规格修订：怪物开局就刷在地图上，「玩家进圈」不再是开波条件。据点仍
// 是波次竞技场（一波清完、间隔后再刷下一波），但第一波的开刷时机是关卡
// 开始的那一刻。所以 HubIdle 不再表示「没人在圈里」，只表示「这一据点的
// 第一波还没刷出来」——它只作为初始值存在，第一次 UpdateWaves 就会把它
// 推走。
type HubState uint8

const (
	HubIdle     HubState = iota // 初始态：这一据点的第一波尚未刷出
	HubFighting                 // 波次进行中
	HubCleared                  // 全部波清完
)

// String 给日志用。
//
// 为什么不直接打 %v：HubState 的底层是 uint8，%v 打出来是 0/1/2，看日志
// 的人得回去翻常量表。日志是给人看的，别让人做额外的查表工作。
// 未知值打数值而不是 panic：状态机是纯内部的，出现未知值时要看得到它
// 是什么，而不是让打日志这一步把整个 tick 带崩。
func (s HubState) String() string {
	switch s {
	case HubIdle:
		return "idle"
	case HubFighting:
		return "fighting"
	case HubCleared:
		return "cleared"
	default:
		return "HubState(" + strconv.Itoa(int(s)) + ")"
	}
}
