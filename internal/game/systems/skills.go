package systems

import (
	"koukoukongjian/internal/game/build"
	"koukoukongjian/internal/game/world"
)

// BaseMaxHP 是 1 级玩家的生命上限基线（spec L103）。
//
// 为什么是常量而不是 config：M4 的成长参数（每级 +10、经验曲线）也都在
// 这里与 gainexp.go 里以常量形式给出，统一留在 M5 调参阶段一起外置；
// 只把生命挪进 config 会让"改血量上限"同时改两处来源。
const BaseMaxHP = 100.0

// ActiveSkillChance 是主动技能进入候选池的抽中概率（Task 6 用）。
//
// Task 5 先把常量落在这里：它是技能系统的数值参数，与 RecomputeDerived /
// CastActive 同属一块内容。Task 6 的候选生成直接引它，不必跨包搬。
const ActiveSkillChance = 0.30

// RecomputeDerived 从两个被动槽与等级整体重算派生属性与生命上限。
//
// 为什么是"重算"而不是"增量改"：技能可换（spec L37），增量改（装配时
// +25、卸下时 -25）要求每一条装卸路径都恰好对称，任意一条漏了减，
// 血上限就永久跑偏，而症状是"越换技能越肉"这种没人会立刻怀疑装配逻辑的
// 表现。重算的输入只有"已装的被动"与"等级"两个，天然幂等——调 1 次和
// 调 100 次逐位相同（TestPassivesIdempotent 守着这一点）。
//
// 代价是每次换技能要遍历两个槽，与它换来的"不可能漂移"完全不成比例。
//
// 等级项用 `Level > 1` 保护而不是直接乘 `Level-1`：Level 是 uint8，而
// Pool.Spawn 会把槽位清零，所以刚出生的玩家 Level 是 0。若直接算
// `LevelUpMaxHPGain*float64(p.Level-1)`，uint8 的 0-1 会下溢成 255，
// 生命上限瞬间变成 100+2550。这类"零值不是合法值"的坑在本仓库已记过
// （AGENTS.md §2.6）。
func RecomputeDerived(p *world.PlayerState) {
	if p == nil {
		return
	}

	// 先整体重置成中性值，再往上叠——重置这一步是幂等的来源。
	p.Derived = world.PlayerDerived{
		DamageMul:      1,
		SpeedMul:       1,
		DamageTakenMul: 1,
		MaxHPBonus:     0,
	}

	for _, id := range p.SkillPassive {
		if id == 0 { // 空槽
			continue
		}
		s, ok := build.SkillByID(id)
		if !ok || s.Kind != build.SkillKindPassive {
			// 槽里放着主动技能或未知 ID：忽略，不报错。
			// 这里不 panic 的理由是存档可能来自旧版本（技能改号/删号），
			// 一个坏槽不该让整局起不来；ActiveSkills() 那边的候选生成
			// 保证正常路径不会写出这种槽。
			continue
		}
		// 乘区相乘、加法相加：叠两个同类被动是复利，符合 build 包
		// Table 注释里"成长是复利，单条倍率必须贴近 1.0"的约定。
		p.Derived.DamageMul *= s.DamageMul
		p.Derived.SpeedMul *= s.SpeedMul
		p.Derived.DamageTakenMul *= s.DamageTakenMul
		p.Derived.MaxHPBonus += s.MaxHPAdd
	}

	// 生命上限 = 基线 + 每级成长 ×（等级-1）+ 被动加值。
	p.MaxHP = BaseMaxHP + p.Derived.MaxHPBonus
	if p.Level > 1 {
		p.MaxHP += LevelUpMaxHPGain * float64(p.Level-1)
	}

	// 钳回上限：卸掉「强健体魄」会让 MaxHP 掉回来，此时 HP 可能高于上限。
	// 不钳的话血条会画出超过 100% 的宽度，且下一次受伤要先把"超额血量"
	// 打掉才看得见掉血，表现为"受击没反应"。
	if p.HP > p.MaxHP {
		p.HP = p.MaxHP
	}
}

// CastActive 释放 slot 槽的主动技能（0 或 1），返回是否真的释放了。
//
// id 是玩家自己的 EntityID，由调用方传入：技能效果要给自己算贡献
// （ResolveDamage 会把伤害记到来源头上），而 PlayerState 里不存自己的 ID
// ——存了就要维护"槽位搬家时同步改字段"，池化复用下极容易失效。
//
// 返回值而不是错误：释放失败的唯一后果是"这次按键没反应"，调用方（room）
// 不需要区分是冷却中还是槽位空——两者对玩家是一样的，且都不该下发任何东西。
func CastActive(w *world.World, id world.EntityID, slot int, p *world.PlayerState) bool {
	if w == nil || p == nil {
		return false
	}
	// 槽位越界先拦：负数下标在 Go 里是运行时 panic，而这里的数据来自
	// 网络/存档，不能假设调用方一定传对。
	if slot < 0 || slot >= len(p.SkillActive) {
		return false
	}
	if p.State != world.PlayerAlive {
		return false
	}

	sid := p.SkillActive[slot]
	if sid == 0 {
		return false // 空槽
	}
	s, ok := build.SkillByID(sid)
	if !ok || s.Kind != build.SkillKindActive {
		return false // 未知 ID，或槽里放的是被动（按了也不该有反应）
	}
	if p.SkillCooldown[slot] > 0 {
		return false // 冷却中
	}

	switch sid {
	case build.SkillIDBurst:
		castBurst(w, id, p, s)
	case build.SkillIDHeal:
		castHeal(p, s)
	case build.SkillIDDash:
		castDash(p, s)
	case build.SkillIDInvuln:
		p.InvulnLeft = s.DurationS
	default:
		// 表里新增了主动技能但这里没接线：不设冷却、返回 false。
		// 静默设冷却会让玩家按了没效果却进冷却，比"没反应"更难查。
		return false
	}

	p.SkillCooldown[slot] = s.CooldownS
	return true
}

// castBurst 是 6 号「环形爆发」：半径内每个敌人各吃一次 Amount 伤害。
//
// 技能伤害不吃 Derived.DamageMul：那个倍率的名字是"子弹伤害倍率"
// （build.Skill.DamageMul 的注释），而环形爆发是位置技能不是子弹。
// 让技能也吃它会让"强化弹头"顺手强化范围伤害，那是另一条设计决定
// （要不要给技能也开一个乘区），不该由实现顺带决定。
//
// 先收集再结算，不在 Each 里直接调 ResolveDamage：ResolveDamage 会给怪物
// 置死亡标志，而遍历中改动实体状态是未定义行为（reap.go 的注释记过同一件
// 事——命中/击杀只标记，回收与批量处理留到遍历之外）。这里虽然只改标志
// 不动槽位，仍统一按"先收集 ID，再逐个结算"写，让所有群体效果长成同一个
// 形状，加新 AOE 时不会有人写出边遍历边杀的版本。
func castBurst(w *world.World, id world.EntityID, p *world.PlayerState, s build.Skill) {
	r2 := s.Radius * s.Radius
	var hit []world.EntityID
	w.Monsters.Each(func(mid world.EntityID, m *world.MonsterState) bool {
		if m.IsDead() {
			return true // 尸体不再吃伤害（ResolveDamage 里也会拦，这里省一次调用）
		}
		if m.Pos.DistSq(p.Pos) <= r2 {
			hit = append(hit, mid)
		}
		return true
	})
	for _, mid := range hit {
		// 环形爆发只伤怪物（hit 里收的都是怪物池 id），类别显式写出来。
		w.ResolveDamage(id, mid, world.EntityClassMonster, s.Amount, world.ElementNone, world.DamageTags{})
	}
}

// castHeal 是 7 号「治疗」：立刻回 Amount 点血，不超过上限。
func castHeal(p *world.PlayerState, s build.Skill) {
	p.HP += s.Amount
	if p.HP > p.MaxHP {
		// 钳上限：治疗不是护盾，溢出部分直接丢弃。
		// 不钳的话 HP 会超过 MaxHP，快照里血条算出超过 100% 的宽度。
		p.HP = p.MaxHP
	}
}

// castDash 是 5 号「冲刺」：DurationS 秒内移速乘 SpeedMul。
//
// 倍率在这里**快照**进 p.SpeedBuffMul，而不是让 Integrate 每子步去查
// build 表：技能可换（spec L37），若玩家在冲刺途中卸下冲刺技能，边查边算
// 会让一个已经开始的效果中途失效或改值。施放那一刻取到的倍率该陪着这次
// 效果走完，这也是 Integrate 能保持"只读 world 数据"的原因。
func castDash(p *world.PlayerState, s build.Skill) {
	p.SpeedBuffLeft = s.DurationS
	p.SpeedBuffMul = s.SpeedMul
}

// TickBuffs 递减玩家的全部限时效果与技能冷却，钳到 0。
//
// 每 tick 调一次，dt 是这一 tick 的固定时长（不是真实耗时）。放在 systems
// 而不是让各系统自己减：递减必须每 tick 恰好一次，散在多处时任何一个系统
// 被调两次（比如重建房间时补跑一帧）都会让冷却以双倍速走完，而这种偏差
// 只表现为"手感有点快"，几乎不可能被定位。
//
// 归零后不再减：负值会让 `> 0` 判据看起来仍然正常，但只要某处写成
// `>= 0` 就会永久触发；而负值冷却还会让"刚放完技能"与"冷却已过"在数值上
// 无法区分，调试时容易误判。
func TickBuffs(p *world.PlayerState, dt float64) {
	if p == nil || dt <= 0 {
		return
	}
	p.SpeedBuffLeft = countdown(p.SpeedBuffLeft, dt)
	p.InvulnLeft = countdown(p.InvulnLeft, dt)
	for i := range p.SkillCooldown {
		p.SkillCooldown[i] = countdown(p.SkillCooldown[i], dt)
	}
	// 加速到期时把倍率也清掉：留着它不影响判定（Integrate 只认
	// SpeedBuffLeft > 0），但会让"上一次冲刺用的倍率"在两次效果之间
	// 残留，日后若有人改成"按倍率是否非零判断是否在加速"就会立刻出错。
	if p.SpeedBuffLeft == 0 {
		p.SpeedBuffMul = 0
	}
}

// countdown 是一个倒计时的单步递减。
func countdown(left, dt float64) float64 {
	if left <= 0 {
		return 0
	}
	if left -= dt; left < 0 {
		return 0
	}
	return left
}
