package world

// DamageTags 是伤害的词条标记。
//
// M2 全部为空，字段先留着是因为 §4.2 的结算顺序里"词条"是第 3 步——
// 现在就把位置占住，M4 加暴击/吸血时不用重排流水线。
type DamageTags struct {
	IgnoreArmor bool
	CanCrit     bool
}

// DamageEvent 是一次伤害结算的结果。
//
// 战斗系统不直接改 UI、不直接发网络包，只产出事件，由 Room 层消费：
// 一部分搭快照的 events 字段下发，一部分走可靠通道（击杀、升级）。
// 见 docs/combat-design-v0.1.md §4.3。
type DamageEvent struct {
	Src       EntityID
	Dst       EntityID
	Amount    float64
	Element   Element
	IsCrit    bool
	IsKill    bool
	Reflector EntityID // 非零表示反射弹幕造成
}

// ResolveDamage 是所有伤害的唯一入口。
//
// 任何来源——子弹、近战、技能、区域效果、反射弹幕——都必须走它。理由：
// 后期加词条（暴击、吸血、元素抗性）只需要改这一个函数；一旦允许某条
// 路径绕过，加词条时就会漏掉那条路径，表现为"某个技能不吃暴击"这类
// 只有设计师能发现的不一致。
//
// 结算顺序（combat-design-v0.1.md §4.2）：
//  1. 元素反应（M2 无元素表，Task 12 之后由 M6 补）
//  2. 护甲与减伤（M2 无护甲，全为 0）
//  3. 词条（M2 无词条）
//  4. 应用 HP -= dmg
//  5. 产出事件
//  6. 死亡判定——只标记，绝不回收
//
// 第 6 步只标记不回收是关键：Collide 与 ApplyHits 都在遍历池，此时 Kill
// 会改动槽位状态，导致漏遍历或重复遍历。回收统一留给 systems.Reap。
//
// dstClass 是目标的池类别，必须由调用方给出。不能靠「先查怪物池再查玩家池」
// 猜：两个池的 id 各自从索引 1 起编号，撞号是常态而非例外，猜错的后果是
// 伤害落到另一个池里撞号的那个实体身上，且**没有任何报错**——怪物打玩家
// 时玩家不掉血、怪物掉血，这个症状曾被当成"怪物 AI 没生效"排查了很久。
// src 永远是玩家 id（怪物伤害的来源传 InvalidID），所以它不需要类别。
func (w *World) ResolveDamage(src, dst EntityID, dstClass EntityClass, base float64, el Element, tags DamageTags) DamageEvent {
	ev := DamageEvent{Src: src, Dst: dst, Element: el}

	if base <= 0 {
		return ev
	}

	// 目标必须是存活的怪物或玩家。玩家打玩家在 M2 不存在，但入口写成
	// 通用形式，M6 的反射弹幕会用到。
	dmg := base

	if dstClass == EntityClassPlayer {
		return w.resolvePlayerDamage(ev, src, dst, dmg)
	}

	if m := w.Monsters.Get(dst); m != nil {
		if m.IsDead() {
			// 尸体不再吃伤害：否则同 tick 多颗子弹命中会让击杀事件
			// 重复产出，分账算两次。
			return ev
		}
		m.HP -= dmg
		if m.HP <= 0 {
			// 钳到 0：允许为负会让客户端血条算出负宽度。
			m.HP = 0
			// 死亡只置标志，不动槽位（第 6 步只标记不回收，见函数头）。
			// 置的是标志而不是靠 HP=0 推断：M4 的治疗能把 HP 拉回来，
			// 用值推断会让"打死"和"血少"撞在同一个表示上。
			m.Flags |= MonsterFlagDead
			ev.IsKill = true
		}
		ev.Amount = dmg
	} else {
		// 怪物池里没有这个索引：id 失效（槽位已回收、世代对不上）。
		// 不返回伤害事件，调用方据此知道"这次没结算"。
		return ev
	}

	creditSource(w, src, dmg)
	return ev
}

// resolvePlayerDamage 结算打向玩家的伤害。
//
// 单独成函数是因为玩家的结算比怪物多一步无敌判定，把两支塞进同一个
// if-else 链会让主体淹没在注释里。调用方已经声明了目标类别，这里不再猜池。
func (w *World) resolvePlayerDamage(ev DamageEvent, src, dst EntityID, dmg float64) DamageEvent {
	p := w.Players.Get(dst)
	if p == nil {
		return ev // 玩家池里没有这个索引：id 失效
	}
	// 判据是 State 而不是 HP<=0：M5 起死亡会立刻置 PlayerGhost，鬼魂
	// 不该再吃伤害。用 HP<=0 判会有一个漏：复活中（HP 还没补回来但
	// State 已经改回 Alive）的那一帧会被当成尸体，而治疗/复活恰好
	// 就是这个项目里把 HP 从 0 拉回来的两条路径。
	if p.State != PlayerAlive {
		return ev
	}

	// 无敌拦在这里、拦整个入口，而不是拦在各个伤害来源上。
	//
	// 为什么收进 world 而不在 systems 里乘：无敌是"这个目标这一瞬间不吃
	// 伤害"，它与伤害来源无关——子弹、近战、区域性伤害、反射弹幕都要拦。
	// 若让每个来源自己判，M6 加新来源时必然漏一处，而症状是"某个技能
	// 能打穿无敌"，只有设计师才看得出不对。反过来，被动的减伤倍率
	// （DamageTakenMul）必须留在调用方乘：那张表在 build 包，world 不能
	// import 它（会成环），而无敌只是一个布尔式判断，纯 world 数据。
	//
	// 两条路径：InvulnLeft 是技能给的限时无敌（秒），Flags 里的位是
	// 复活/出生保护这类由房间逻辑给的状态。两者都拦，判据取或。
	if p.Flags.Has(PlayerFlagInvulnerable) || p.InvulnLeft > 0 {
		// 返回零值事件：Amount 保持 0，调用方据此知道"这次没结算"，
		// 不会下发一条 0 伤害的事件，也不会给来源记贡献。
		return ev
	}

	p.HP -= dmg
	// 承受伤害参与贡献折算（spec L134：贡献 = 伤害 + 0.5×承受）
	p.DamageTaken += dmg

	// 受击打断开背包引导（spec L44「开背包需数秒引导，受击打断」）。
	//
	// 为什么内联在这里、而不是调 systems.InterruptBusy：world 是叶子包，
	// 不能 import systems（分层纪律 AGENTS.md §2.4，systems 反过来依赖
	// world，成环）。而打断必须与伤害结算在同一处完成——若留给 room 每
	// tick 比较"血量比上一 tick 少了没有"，漏一帧就少一次打断，且要被
	// 动的减伤/无敌混淆（血量没变不等于没挨打）。所以这两行是
	// systems.InterruptBusy 的镜像，两边必须同时改。
	//
	// 位置在无敌判定之后、扣血之后：无敌挡掉的伤害函数已经 return 了，
	// 走不到这里，就不会出现"免疫了一次伤害却被打断引导"的怪事。
	p.Flags &^= PlayerFlagBusy
	p.InventoryOpenAt = 0

	// 致死：把状态切到鬼魂。这一步是 M5 的核心——只 p.HP = 0 不改
	// State 的话，玩家在全仓 9 处 "State != PlayerAlive" 的判据里
	// 仍然是活人，能继续走、开火、放技能、捡东西，怪物也还在追他。
	//
	// 判据留在"扣血之后"的既有顺序上（p.HP 已在上面减过 dmg），而不是
	// 计划示意里的"先比较再减"：扣减在前让 DamageTaken 的累加与
	// creditSource 的分账都不必挪位置，而 Kill() 会自己把 HP 置 0，
	// 中间那个短暂的负值活不过本函数，不会被任何读取方看见。
	if p.HP <= 0 {
		p.Kill()
		ev.IsKill = true
	}
	ev.Amount = dmg

	creditSource(w, src, dmg)
	return ev
}

// creditSource 把这次伤害记到来源玩家的贡献上。
//
// 分账只在据点结束时算一次（spec L134），这里只累加。源头必须查玩家池：
// 怪物伤害的来源传 InvalidID，查不到就什么都不记（怪物没有贡献概念）。
func creditSource(w *World, src EntityID, dmg float64) {
	if srcP := w.Players.Get(src); srcP != nil {
		srcP.Contribute += dmg
	}
}
