package world

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

// HubState 是据点波次的运行时状态（spec：进圈开波 → 清波 → 下一波 → 完成）。
type HubState uint8

const (
	HubIdle     HubState = iota // 玩家未进入
	HubFighting                 // 波次进行中
	HubCleared                  // 全部波清完
)
