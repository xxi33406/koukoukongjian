// Package build 是构筑系统：技能定义、候选生成（Task 6）、属性结算（Task 5）。
//
// 分层：只允许 import world；当前这张表是纯数据，连 world 都用不上。
// 由 systems（效果执行）与 room（选择流程）调用。
//
// 技能表先用代码常量（沿用 M2 WeaponTable 的先例）：M4 没有编辑器，
// JSON 反而增加改错风险；M7 数值阶段再外置到 config/skills.json。
//
// 与 WeaponTable 的差别（那次是导出 var，任何包都能就地改数值）：
// 这里内部表未导出，对外只给返回副本的访问函数——见 SkillTable 的说明。
package build

// SkillKind 区分被动与主动。
//
// 零值是 Passive 而不是"未设置"：技能表的每一条都必须显式成组，
// 用 0 当 Passive 让漏写 Kind 的条目立刻表现为"被动"，在
// TestSkillKindMatchesTable 里会被抓出来。
type SkillKind uint8

const (
	SkillKindPassive SkillKind = iota // 装配后持续生效，改属性
	SkillKindActive                   // 按键触发，带冷却
)

// String 让日志与测试失败信息读得懂。
func (k SkillKind) String() string {
	switch k {
	case SkillKindPassive:
		return "passive"
	case SkillKindActive:
		return "active"
	default:
		return "unknown"
	}
}

// Skill 是一条技能定义。
//
// 被动与主动共用同一个结构：字段按 Kind 解释，未用到的字段保持中性值
// （乘区 1.0、加法 0）。分成两个结构会让 SkillActive/SkillPassive 两个
// 槽位的通用访问代码要写两套分支，而两套分支正是"漏同步"的来源。
type Skill struct {
	ID   uint16 // 写进协议与存档，只能追加不能改号；0 保留给"空槽"
	Name string
	Kind SkillKind
	Desc string // 一句话说清效果与数值，直接给玩家看

	// —— 被动属性加成（乘区叠加，1.0 = 无加成）——
	//
	// DamageMul/SpeedMul/DamageTakenMul 是乘区：叠多个同类被动时相乘，
	// 成长是复利的，所以单条倍率必须贴近 1.0。
	// MaxHPAdd 是加法：生命是绝对数值，乘区形式会让"强健体魄"随等级
	// 与其它加成一起膨胀，而设计的意图只是"多 25 点血"。
	DamageMul      float64 // 子弹伤害倍率
	MaxHPAdd       float64 // 最大生命加值（点）
	SpeedMul       float64 // 移速倍率
	DamageTakenMul float64 // 受击伤害倍率（<1 = 减伤）

	// —— 主动效果参数 ——
	CooldownS float64 // 冷却，秒；必须 > 0，否则冷却判定恒为 0、无限连发
	DurationS float64 // 持续类技能的时长，秒
	Amount    float64 // 治疗量 / 爆发伤害
	Radius    float64 // 爆发半径，米
}

// 技能 ID。数值写进协议与存档，只能追加不能改号。
const (
	SkillIDAmmoUp    uint16 = 1 // 强化弹头
	SkillIDToughBody uint16 = 2 // 强健体魄
	SkillIDSwift     uint16 = 3 // 疾行
	SkillIDShield    uint16 = 4 // 吸收护盾
	SkillIDDash      uint16 = 5 // 冲刺
	SkillIDBurst     uint16 = 6 // 环形爆发
	SkillIDHeal      uint16 = 7 // 治疗
	SkillIDInvuln    uint16 = 8 // 短时无敌

	// —— M6：元素技能与反射面 ——
	//
	// 8 之后追加，编号不能复用：技能 ID 写进协议（SkillChoices.choices
	// 的 skill_id）与快照，改号会让存档与回放错位。
	SkillIDFlame   uint16 = 9  // 火焰喷射（火）
	SkillIDWave    uint16 = 10 // 水波（水）
	SkillIDBolt    uint16 = 11 // 雷击（雷）
	SkillIDReflect uint16 = 12 // 反射面
)

// skillTable 是全部技能定义，内部持有。
//
// 用固定长度的数组而不是切片：条数由编译器数，加一条自动变长，
// 而 append 到切片上会在扩容时把底层数组换掉——内部表就可能
// 与某个取过子切片的调用方悄悄分家。
//
// 不导出（WeaponTable 是导出 var，M2 被指出任何包都能就地改数值）。
// 对外只走 SkillTable/SkillByID/ActiveSkills/PassiveSkills。
var skillTable = [...]Skill{
	// —— 四个被动（ID 1-4）——
	// 乘区字段显式写 1.0：零值 0 会被读成"伤害归零"，而 0 与 1.0 在
	// 乘区里是"灾难"与"无影响"的区别，不能靠省略。
	{
		ID:             SkillIDAmmoUp,
		Name:           "强化弹头",
		Kind:           SkillKindPassive,
		Desc:           "子弹伤害 ×1.2。",
		DamageMul:      1.20,
		SpeedMul:       1,
		DamageTakenMul: 1,
	},
	{
		ID:             SkillIDToughBody,
		Name:           "强健体魄",
		Kind:           SkillKindPassive,
		Desc:           "最大生命 +25。",
		MaxHPAdd:       25,
		DamageMul:      1,
		SpeedMul:       1,
		DamageTakenMul: 1,
	},
	{
		ID:             SkillIDSwift,
		Name:           "疾行",
		Kind:           SkillKindPassive,
		Desc:           "移动速度 ×1.15。",
		SpeedMul:       1.15,
		DamageMul:      1,
		DamageTakenMul: 1,
	},
	{
		ID:             SkillIDShield,
		Name:           "吸收护盾",
		Kind:           SkillKindPassive,
		Desc:           "受到的伤害 ×0.8（相当于减伤 20%）。",
		DamageTakenMul: 0.80,
		DamageMul:      1,
		SpeedMul:       1,
	},

	// —— 四个主动（ID 5-8）——
	// 冲刺复用 SpeedMul 字段表示"持续期内的加速倍率"：它是限时的，
	// 与被动疾行的常驻倍率在 Task 5 的结算里分开叠。
	{
		ID:        SkillIDDash,
		Name:      "冲刺",
		Kind:      SkillKindActive,
		Desc:      "向当前朝向冲刺，2 秒内移速 ×1.8，冷却 6 秒。",
		CooldownS: 6,
		DurationS: 2,
		SpeedMul:  1.8,
	},
	{
		ID:        SkillIDBurst,
		Name:      "环形爆发",
		Kind:      SkillKindActive,
		Desc:      "对周围 4 米内的敌人各造成 25 点伤害，冷却 5 秒。",
		CooldownS: 5,
		Amount:    25,
		Radius:    4.0,
	},
	{
		ID:        SkillIDHeal,
		Name:      "治疗",
		Kind:      SkillKindActive,
		Desc:      "立即回复 30 点生命（不超过上限），冷却 15 秒。",
		CooldownS: 15,
		Amount:    30,
	},
	{
		ID:        SkillIDInvuln,
		Name:      "短时无敌",
		Kind:      SkillKindActive,
		Desc:      "1.5 秒内免疫所有伤害，冷却 20 秒。",
		CooldownS: 20,
		DurationS: 1.5,
	},

	// —— M6：反射面 ——
	{
		ID:   SkillIDReflect,
		Name: "反射面",
		Kind: SkillKindActive,
		Desc: "在脚下放一面镜子，队友的子弹打上去会弹回来，伤害算你的。",
		// CooldownS 12 秒：反射是「配合的高光时刻」，不是高频操作。
		// 低于 8 秒会让反射面变成常驻地形，玩家不再需要挑时机。
		CooldownS: 12.0,
		DurationS: 3.0,
		Radius:    2.0,
	},
}

// SkillTable 返回全部技能定义的副本。
//
// 返回副本而不是内部切片：调用方拿到的是值切片，就地改元素只改自己那份。
// M2 的 WeaponTable 是导出 var，等于把内部表交给全仓库随意涂改，
// 而"某个包把 Damage 改成 1"这类事故不会在编译期暴露。
//
// 元素是值类型，copy 出来的切片与原表不共享任何可写内存（string 是
// 不可变的，float64/uint16 是值），所以浅拷贝足够。
func SkillTable() []Skill {
	out := make([]Skill, len(skillTable))
	copy(out, skillTable[:])
	return out
}

// SkillByID 按 ID 取技能定义。
//
// 返回 ok 而不是零值：零值 Skill 的 Kind 是 Passive、各乘区是 0，
// 会被下游当真数据用（乘区 0 = 伤害归零）。必须让调用方显式处理未知 ID。
func SkillByID(id uint16) (Skill, bool) {
	if id == 0 { // 0 是槽位的"空"值，不可能是技能 ID
		return Skill{}, false
	}
	for i := range skillTable {
		if skillTable[i].ID == id {
			return skillTable[i], true
		}
	}
	return Skill{}, false
}

// ActiveSkills 返回全部主动技能（过滤结果，新切片）。
//
// 每次调用都新建切片：候选生成（Task 6）会在上面做抽样与洗牌，
// 返回内部切片的话洗牌顺序会留在表里，导致不同房间的候选互相影响。
func ActiveSkills() []Skill {
	out := make([]Skill, 0, len(skillTable))
	for _, s := range skillTable {
		if s.Kind == SkillKindActive {
			out = append(out, s)
		}
	}
	return out
}

// PassiveSkills 返回全部被动技能（过滤结果，新切片）。理由同 ActiveSkills。
func PassiveSkills() []Skill {
	out := make([]Skill, 0, len(skillTable))
	for _, s := range skillTable {
		if s.Kind == SkillKindPassive {
			out = append(out, s)
		}
	}
	return out
}
