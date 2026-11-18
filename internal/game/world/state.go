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

	// —— 开火状态（每 tick 在子步循环外判定一次）——
	//
	// 两个变量的更新频率不同，别当成一组：FireCooldown 每 tick 减一个
	// tick 的量（不是每子步），PrevAttackHeld 每 tick 覆写一次。放在一起
	// 是因为它们只服务于同一次开火决策。
	//
	// FireCooldown 是距离下次能开火还剩多少秒。0 表示随时可发。
	// 它是纯服务端状态，不进快照：客户端自己维护按键，冷却若下发会让
	// 客户端画出它预测不准的"哑火"。
	FireCooldown float64

	// PrevAttackHeld 是上一个 tick 的攻击键状态，用来算"按下沿"。
	//
	// 不能让客户端直接发"刚按下"：cmd/server/bridge.go 的 mergeInputFrames
	// 会在整个 frames 窗口内对按键做按位或（为了捕获两包之间的短按），
	// 那个位反映"窗口内按住过"，不是"这一帧刚按下"。单发武器若信它，
	// 一帧会打两发。
	PrevAttackHeld bool

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

	// —— 成长（M4）——
	//
	// Level 从 1 起。但要注意 Pool.Spawn 会 `var zero T` 把整个槽位清零
	// （pool.go），所以刚 Spawn 出来是 0——出生值必须由调用方显式补成 1，
	// 位置在 internal/room/net.go 的 handleJoin，与 p.WeaponID 同一处。
	// 池保持通用容器（子弹池/拾取物池都靠"零值即干净"），不为玩家单开
	// 一个出生钩子。
	//
	// ExpToNext 是升到下一级还需要的经验，由曲线算出后缓存在这里：曲线上
	// 每级只与当前等级有关，而加经验要拿它反复比较，几何级数每算一次都要
	// pow。缓存同时也让"当前等级要多少经验"成为一个可直接读、调试时看得
	// 见的字段，不必反推。
	Level     uint8
	Exp       uint32
	ExpToNext uint32

	// —— 技能效果（M4）——
	//
	// 这一组由 systems.RecomputeDerived 整体重算、systems.TickBuffs 递减，
	// world 包只当数据存着。为什么不在 world 里算：技能表在 build 包，
	// 而 build 依赖 world——world 再反过来 import build 就成环了（分层纪律
	// AGENTS.md §2.4：world 是叶子包）。
	Derived PlayerDerived

	// SpeedBuffLeft / InvulnLeft 是限时效果**剩余秒数**，不是到期 tick 号。
	//
	// 走到这两个字段的地方是 Integrate 与 ResolveDamage，而它们的签名里
	// 根本没有 *World，读不到 w.Tick。为了比一个 tick 号而给物理函数多塞
	// 一个 World 参数，等于把它和整个世界绑死；而秒数方案只要 tick 层每 tick
	// 减一次（systems.TickBuffs），物理函数就永远只判 `> 0`。
	//
	// 顺带换来可测性：给一个 PlayerState 塞 1.5 秒就能验证"无敌挡伤害"，
	// 不必构造 World、不必对齐 tick 号。
	SpeedBuffLeft float64
	InvulnLeft    float64

	// SkillCooldown 是两个主动槽各自的剩余冷却（秒），下标与 SkillActive 对齐。
	//
	// 用定长 [2] 而不是切片：槽位数是设计常量（spec L37 两主动两被动），
	// 切片会让"某个玩家的槽位被扩成 3 个"变成运行期可能，而 FireCooldown
	// 那种单值冷却又不够用。
	SkillCooldown [2]float64

	// SpeedBuffMul 是限时加速的倍率（冲刺），只在 SpeedBuffLeft > 0 时生效。
	//
	// 倍率在**施放时快照**进玩家状态，而不是让 Integrate 每子步去查
	// build.SkillByID(SkillIDDash)：技能可换（spec L37），若玩家在冲刺途中
	// 卸下冲刺，边查边算会让一个已经开始的效果中途失效或改值。施放那一刻
	// 取到的倍率该陪着这次效果走完。
	SpeedBuffMul float64

	// —— 网络校正 ——
	LastAckedInput uint16
	Ping           uint32 // 平滑后的 RTT，毫秒
}

// PlayerDerived 是技能与等级带来的派生倍率，由 systems.RecomputeDerived
// 整体重算。
//
// 做成"重算"而不是"增量改"：技能可换（spec L37），增量改会在反复装卸后
// 漂移——装上 +25、卸下 -25、再装上 +25，只要中间有任何一条路径漏了减，
// 血上限就永久跑偏。整体重算的输入只有"已装的被动"与"等级"，天然幂等，
// 调 1 次和调 100 次结果一样（TestPassivesIdempotent）。
//
// 零值不是合法状态：全 0 会让移动速度变 0、伤害变 0。所以读取方一律通过
// Effective 取，它把"没被重算过"的零倍率兜底成中性值 1。
type PlayerDerived struct {
	DamageMul      float64 // 造成伤害的倍率（中性 1）
	SpeedMul       float64 // 移动速度的倍率（中性 1）
	DamageTakenMul float64 // 受到伤害的倍率（中性 1）
	MaxHPBonus     float64 // 最大生命的加法加成（中性 0）
}

// Effective 返回把零值倍率兜底成中性值之后的副本。
//
// 为什么需要兜底：Pool.Spawn 用 `var zero T` 清零整个槽位（pool.go:76），
// 刚出生的玩家 Derived 是全 0。若 Integrate 直接乘 0，玩家原地不动；若
// spawnBullet 直接乘 0，子弹零伤害——两者都是"看起来跑通了但什么都没发生"，
// 而根因离症状很远。兜底让"还没重算"退化成"无技能"，是唯一安全的默认。
//
// 代价要说清：漏调 RecomputeDerived 不会崩、不会报错，只会静默按无技能跑。
// 这个代价换来的是不必在出生路径上强加一次重算调用（出生在 room 的
// handleJoin，本任务不碰 room 包）。重算漏调的另外半边症状是 MaxHP 不对，
// 那个症状是可观测的，不会长期藏着。
//
// MaxHPBonus 的零值恰好就是中性值 0，不需要兜底。
func (d PlayerDerived) Effective() PlayerDerived {
	if d.DamageMul <= 0 {
		d.DamageMul = 1
	}
	if d.SpeedMul <= 0 {
		d.SpeedMul = 1
	}
	if d.DamageTakenMul <= 0 {
		d.DamageTakenMul = 1
	}
	return d
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

	// M3：仇恨打分的三路输入（回放一致性：只在 tick 循环内读写）。
	// 数组长度是 5 不是 MaxPlayers(4)：pool.Spawn 在 free 栈耗尽后只要
	// live<max 就 append 新槽（pool.go），NewPool(max,max) 的槽位下标
	// 上界是 capacity+1——第 4 个加入的玩家 Index() 可以是 4。
	// （M3-Task6 审查 C1：满员 4 人 RescoreAggro 越界 panic 的根因，
	// 旧注释「0 号槽保留、实际索引 ≤3」的论证是错的。）
	AggroDmg [5]float64 // 近期伤害（指数遗忘：rescore 时 ×0.5）
	AttackAt uint32     // 上次攻击的 tick（冷却判定）

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

	// MonsterFlagDead 是 M4 起的唯一死亡判据。
	//
	// 为什么判据是标志而不是 HP：M2/M3 用 HP<=0 同时兼做"死亡判据"和
	// "尸体标记"，那是建立在"HP 只减不增"这个前提上的——前提一旦不成立，
	// 判据就是错的。M4 的治疗/吸血、M5 的祭坛复活都会把 HP 拉回正数，
	// 于是 HP<=0 同时意味着"死了"和"活着但血少"，两个语义纠缠在一起；
	// 更糟的是"能不能复活"会由治疗量顺手决定，而那是设计要单独拍板的事。
	// 把状态写成显式位，值与状态各走各的路：治疗只管 HP，复活只管清标志。
	MonsterFlagDead
)

// Has 报告标记位是否置位。
func (f MonsterFlags) Has(bit MonsterFlags) bool { return f&bit != 0 }

// IsDead 报告这只怪物是否已死。这是唯一死亡判据。
//
// 不要再用 HP<=0 判死（理由见 MonsterFlagDead）。所有"尸体不吃伤害 /
// 不瞄准 / 不决策 / 待回收"的分支都必须走这里。
func (m *MonsterState) IsDead() bool { return m.Flags.Has(MonsterFlagDead) }

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
	Life    float32  // 剩余存活时间，秒；到 0 消失（spec L131，M4 规格修订为 30 秒）
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
