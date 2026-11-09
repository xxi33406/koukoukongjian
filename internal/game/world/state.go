package world

// Element 是元素类型。
//
// 火/水/油/雷四元素统一服务于子弹、场景道具、位置死亡技能与交汇组合
// （spec/多人肉鸽差异化设计文档.md L96-104）。这里只定义枚举，元素表与反应
// 规则属于战斗系统，见 internal/game/systems。
type Element uint8

const (
	ElementNone Element = iota
	ElementFire
	ElementWater
	ElementOil
	ElementThunder
)

func (e Element) String() string {
	switch e {
	case ElementNone:
		return "none"
	case ElementFire:
		return "fire"
	case ElementWater:
		return "water"
	case ElementOil:
		return "oil"
	case ElementThunder:
		return "thunder"
	default:
		return "unknown"
	}
}

// PlayerState 是一个玩家槽的完整状态。
//
// 字段按访问频率排列：热字段（每个物理子步都读写）在前，冷字段在后。这不是
// 为了凑 cache line，而是为了读代码时能一眼看出"每帧在动的东西"是哪些。
type PlayerState struct {
	// —— 热字段：每个子步 ——
	Pos        Vec2
	Vel        Vec2
	MoveIntent Vec2    // 归一化的移动意图
	AimDir     float64 // 朝向弧度
	HP         float64

	// —— 温字段：每 tick 或事件时 ——
	WeaponID     uint16
	SkillActive  [2]uint16
	SkillPassive [2]uint16
	ItemSlots    [3]uint16

	// —— 冷字段 ——
	MaxHP       float64
	Gold        uint32
	Contribute  float64 // 本据点贡献值，分账用（spec L134）
	DamageTaken float64 // 本据点承受伤害，参与贡献折算
	State       PlayerStateKind
	Flags       PlayerFlags
	GhostTether float32 // 鬼魂拴绳半径（spec L66）

	// —— 网络校正 ——
	LastAckedInput uint16
	Ping           uint32 // 平滑后的 RTT，毫秒
}

// PlayerStateKind 是玩家的粗粒度状态。
type PlayerStateKind uint8

const (
	PlayerAlive PlayerStateKind = iota
	PlayerGhost
	PlayerDowned
	PlayerDisconnected
)

// PlayerFlags 是位标记。
type PlayerFlags uint8

const (
	PlayerFlagInvulnerable PlayerFlags = 1 << iota
	PlayerFlagBusy                     // 开背包 / 引导复活 / 商店交易中
	PlayerFlagReconnecting
)

// Has 报告标记位是否置位。
func (f PlayerFlags) Has(bit PlayerFlags) bool { return f&bit != 0 }

// MonsterState 是一只怪物的完整状态。
type MonsterState struct {
	// —— 热字段 ——
	Pos    Vec2
	Vel    Vec2
	HP     float64
	Radius float32

	// —— AI 温字段：每 0.3~0.5 秒 ——
	TargetID   EntityID // 当前仇恨目标（InvalidID 表示无）
	AggroScore float64
	State      MonsterStateKind
	PathLen    uint8

	// Path 是拐点压缩后的寻路缓存。
	//
	// 定长 [64] 而不是切片：定长让整个怪物池能被整块 copy、序列化，也让回放
	// 时的世界哈希好算（决策见 docs/combat-design-v0.1.md §8 #2）。A* 原始
	// 输出是每格一点，但压缩成拐点后跨图路径通常只剩 5~10 点，64 绰绰有余。
	Path    [64]Vec2
	PathIdx uint16

	// —— 冷字段 ——
	MaxHP     float64
	TypeID    uint16
	Element   Element
	Flags     MonsterFlags
	PoisonRem float64 // 位置死亡技能的"强化"累计值（spec L131）

	// —— 计时器（单位：tick）——
	AggroRescoreAt uint32
	PathRecalcAt   uint32
}

// MonsterStateKind 是怪物的 AI 状态机状态。
type MonsterStateKind uint8

const (
	MonsterIdle MonsterStateKind = iota
	MonsterChase
	MonsterAttack
	MonsterReturn
)

// MonsterFlags 是位标记。
type MonsterFlags uint8

const (
	MonsterFlagAggroed  MonsterFlags = 1 << iota // 已进入战斗
	MonsterFlagEnraged                           // 距离惩罚第二级：狂暴
	MonsterFlagSplits                            // 距离惩罚第一级：攻击分裂
	MonsterFlagBuffable                          // 可被掉落的强化效果加强
)

// Has 报告标记位是否置位。
func (f MonsterFlags) Has(bit MonsterFlags) bool { return f&bit != 0 }

// BulletState 是一颗子弹的完整状态。
type BulletState struct {
	// —— 热字段：每个子步，位置与速度决定一切 ——
	Pos    Vec2
	Vel    Vec2
	Radius float32
	Life   float32 // 剩余寿命，秒
	Damage float64

	// —— 归属 ——
	OwnerID     EntityID // 发射者，伤害记在他头上
	ReflectorID EntityID // 反射者；非零时贡献同时记给他（spec L87）

	// —— 属性 ——
	Element Element
	Flags   BulletFlags
	Pierce  uint8 // 剩余可穿透次数

	// —— 上一子步位置，用于扫掠判定 ——
	//
	// 单独存而不是回退积分：一旦扣血/分裂等逻辑改变了速度，用新的速度反推
	// 上一位置就会算错。存下来是最省事也最不会错的做法。
	PrevPos Vec2
}

// BulletFlags 是位标记。
type BulletFlags uint8

const (
	BulletFlagReflectable  BulletFlags = 1 << iota // 可被队友反射/折射
	BulletFlagInteractable                         // 参与弹幕交汇
	BulletFlagPiercing                             // 可穿透（次数由 Pierce 决定）
	BulletFlagHostile                              // 敌对弹幕
)

// Has 报告标记位是否置位。
func (f BulletFlags) Has(bit BulletFlags) bool { return f&bit != 0 }

// PickupState 是地上的拾取物（掉落技能、血瓶、金币）。
type PickupState struct {
	Pos     Vec2
	Kind    PickupKind
	RefID   uint16   // 指向具体物品表的下标
	Life    float32  // 剩余存活时间，秒；到 0 消失（spec L131 的 10 秒）
	Carrier EntityID // 正被鬼魂搬运时非零（spec L66）
	Amount  int32    // 金币/血量的数量
}

// PickupKind 是拾取物种类。
type PickupKind uint8

const (
	PickupGold PickupKind = iota
	PickupHealth
	PickupSkill
	PickupItem
)
