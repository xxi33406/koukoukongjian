package systems

import (
	"koukoukongjian/internal/game/world"
)

// Weapon 是一把武器的静态定义。
//
// 连发与单发的差别只有一处：AutoFire。发射动作（生成子弹、赋速度、扣冷却）
// 完全相同，所以不需要两套代码路径——见 docs/combat-design-v0.1.md 与本
// 计划 §0 的说明。加新武器只加一行表项。
//
// 参数留在代码里而不是 config：这是策划内容，改它不需要重启调参；等 M4 构筑
// 系统把武器做成掉落物时，再整体外置到数据文件（用户 2026-11-12 拍板）。
type Weapon struct {
	// ID 是武器编号，写进协议与存档，只能追加不能改号。
	//
	// 显式带着 ID 而不是靠表下标隐式对应：下标 0 起、ID 1 起，
	// 差一格的映射在插入条目时必然错位，而症状是"打出来的子弹伤害
	// 属于另一把枪"这种极难联想的问题。
	ID          uint16
	Name        string
	Cooldown    float64 // 两发之间的最小间隔，秒
	AutoFire    bool    // true=按住持续射击；false=每次按下只发一发
	BulletSpeed float64 // 米/秒
	BulletLife  float64 // 秒
	BulletRad   float32 // 米，子弹碰撞半径
	Damage      float64
	Element     world.Element
}

// 武器 ID。数值写进协议与存档，只能追加不能改号。
const (
	WeaponRifle      uint16 = 1 // 连发
	WeaponHandCannon uint16 = 2 // 单发
)

// WeaponTable 是全部武器定义。
//
// 数值取舍：
//   - 步枪冷却 0.15 s = 6.67 发/秒。低于 0.1 s 时连发听感变成"嗡"声而不是枪声。
//   - 伤害 10 对齐 docs/combat-design-v0.1.md §7 的「玩家 10 伤」基准值。
//   - 弹速 20 m/s 与设计文档一致，子步位移 0.333 m（见 §3 扫掠判定的必要性）。
var WeaponTable = []Weapon{
	{
		ID:          WeaponRifle,
		Name:        "rifle",
		Cooldown:    0.15,
		AutoFire:    true,
		BulletSpeed: 20,
		BulletLife:  1.2,
		BulletRad:   0.12,
		Damage:      10,
		Element:     world.ElementNone,
	},
	{
		ID:          WeaponHandCannon,
		Name:        "hand_cannon",
		Cooldown:    0.6,
		AutoFire:    false,
		BulletSpeed: 26,
		BulletLife:  1.1,
		BulletRad:   0.18,
		Damage:      32,
		Element:     world.ElementFire,
	},
}

// WeaponByID 按 ID 取武器定义。
//
// 返回 ok 而不是零值：零值 Weapon 的 Cooldown=0 会让 Fire 陷入补发死循环
// （for cooldown <= 0 { fire(); cooldown += 0 }），必须让调用方显式处理。
func WeaponByID(id uint16) (Weapon, bool) {
	for i := range WeaponTable {
		if WeaponTable[i].ID == id {
			return WeaponTable[i], true
		}
	}
	return Weapon{}, false
}

// DefaultWeaponID 是玩家出生时手里的武器。
const DefaultWeaponID uint16 = WeaponRifle
