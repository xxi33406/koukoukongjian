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

	// Element 是这个据点的元素属性（ElementNone = 无属性）。
	//
	// 由 mapgen 生成时定死，不在运行时随机：属性是「地图的一部分」，
	// 与据点坐标同源。运行时随机会让客户端无法预测（迷雾探开前渲染
	// 不出颜色），回放也不可复现。
	//
	// 用户拍板的规则：无油区；每种属性最多一个据点；其余据点无属性。
	Element Element

	// KillGold 是本据点累积的待分账金币池。
	//
	// 挂在 Hub 而不是 World：spec L79 要求「每据点重新计算」。挂 World
	// 会让「哪个据点的钱」无法区分，两个据点同时清完时会混账。
	KillGold uint32
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

// NearestAltar 返回 radius 内最近的祭坛位置。
//
// 祭坛就是据点中心（spec L51「祭坛设于据点内」），M5 不新增实体：
// 据点空地圆半径是 HubRadiusCells=5 米，中心附近一定在空地上，不会
// 嵌墙，也不需要 mapgen 额外挖地形。M6 若要做「祭坛有独立血量、会被
// 怪拆」再升级成实体（那时这个函数要改成返回实体 ID）。
//
// 只判距离不判视线：祭坛在据点空地里，周围没有墙；加视线检查反而
// 会出现"站在祭坛边上但隔着一个人就说找不到"的怪事。
//
// HasMap=false（老测试、降级房间）时永远返回 false：没有据点就没有祭坛，
// 与 waves/ai/fog 的短路口径一致。
func (w *World) NearestAltar(pos Vec2, radius float64) (Vec2, bool) {
	if !w.HasMap || radius <= 0 || len(w.Hubs) == 0 {
		return Vec2{}, false
	}
	radiusSq := radius * radius

	var best Vec2
	bestSq := 0.0
	found := false
	for i := range w.Hubs {
		d := pos.DistSq(w.Hubs[i].Center)
		if d > radiusSq {
			continue
		}
		// 严格小于配 found 首判定：等距时取先遍历到的（Hub 顺序由
		// mapgen 定死，回放可复现）。
		if !found || d < bestSq {
			best = w.Hubs[i].Center
			bestSq = d
			found = true
		}
	}
	return best, found
}
