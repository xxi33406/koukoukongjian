package systems

import (
	"math"

	"koukoukongjian/internal/game/build"
	"koukoukongjian/internal/game/world"
)

// PickupRadius 是玩家能自动捡起掉落物的距离（米）。
//
// 玩家半径 0.3，取 1.2 是"走过去就顺手捡到"的量级：再小就得对点站桩，
// 高速移动中几乎捡不到；再大就会隔着墙、隔着两个身位把东西吸过来，玩家
// 会怀疑自己的位置到底算数不算数。灰盒阶段宁宽松不宁苛刻——捡不到是挫败，
// 多捡一点只是手感毛糙。
const PickupRadius = 1.2

// PickupTTLSeconds 是掉落物在地面上的存活时间（秒）。
//
// spec L131 原本写 10 秒，M4 规格修订为 30 秒：一局里玩家要清一片怪、回
// 头捡漏、再赶到下一个刷新点，10 秒连一次来回都不够，掉落物会在玩家视线
// 之外凭空消失，看起来像丢包。30 秒足够走一趟再回来，又不会让地上堆满
// 过期物品。
const PickupTTLSeconds = 30.0

// PickupTTLTicks 把 30 秒寿命换算成 tick 数。
//
// 导出是因为 Task 10 以后的掉落接线（Reap → SpawnPickup）与测试都要按
// 自己的 tick 率算这个数；换算规则必须和 world.secondsToTicks 一致（四舍
// 五入、至少 1 tick），否则同一份秒数在不同包里折出两个 tick 数。
func PickupTTLTicks(rate float64) uint32 {
	if math.IsNaN(rate) || rate <= 0 {
		// tick 率没配好时不能返回 0：0 寿命意味着掉落物出生即消失，
		// 而调用方完全看不出是自己少填了一个配置项。
		return 1
	}
	n := PickupTTLSeconds * rate
	if n >= float64(math.MaxUint32) {
		return math.MaxUint32
	}
	t := uint32(n + 0.5)
	if t == 0 {
		return 1
	}
	return t
}

// SpawnPickup 在指定位置生成一个掉落物，返回它的实体 ID。
//
// 池满时返回 world.InvalidID 并静默丢弃：地上东西已经够多了，这时再挤进去
// 一个不如不挤（waves.go 少刷怪、pool.Spawn 返回 nil 都是同一取舍）。
// panic 会把整个 tick 带走，那才是真故障。
//
// amount 收 float64 是为了让调用方直接传伤害/血量这类浮点量，落地时按
// AGENTS §2.6 钳位成 int32——不钳位的话 int32(NaN)、int32(1e30) 是平台相关
// 的未定义值，金币会变成负数或天文数字。
func SpawnPickup(w *world.World, pos world.Vec2, kind world.PickupKind, refID uint16, amount float64) world.EntityID {
	id, pk := w.Pickups.Spawn()
	if id == world.InvalidID || pk == nil {
		return world.InvalidID
	}
	pk.Pos = pos
	pk.Kind = kind
	pk.RefID = refID
	pk.Amount = clampToInt32(amount)
	// 出生即满寿命：寿命靠每 tick 递减，出生若为 0 会在下一 tick 就被清掉。
	pk.Life = float32(PickupTTLSeconds)
	// Carrier 保持零值（world.InvalidID）：地上的瓶子没人搬。
	return id
}

// clampToInt32 把任意 float64 收进 int32 的合法范围。
//
// NaN 归 0、负数归 0、超上限钳到 MaxInt32。负数归 0 而不钳到 MinInt32：
// 掉落物的数量是"给玩家多少"，负数额度没有语义，钳到 MinInt32 会让玩家
// 捡到金币反而负债，比丢弃更糟。
func clampToInt32(v float64) int32 {
	if math.IsNaN(v) || v <= 0 {
		return 0
	}
	if v >= float64(math.MaxInt32) {
		return math.MaxInt32
	}
	return int32(v)
}

// UpdatePickups 处理掉落物的寿命与拾取，每个 tick 调一次。
//
// 没有 dt 参数：世界自己带着 TickRate，由它推导 1/TickRate 比让每个调用方
// 各传一遍更难写错——room 里已经有一处把 dt 手算错的可能性，不必再多一处。
//
// 返回的事件沿用 world.DamageEvent，只用来通知"这个 ID 从地上消失了"
// （Src=InvalidID、Amount=0）：M4 还没有事件总线，而 Reap 已经用同一个结构
// 表达"实体被回收，不是伤害"（reap.go），这里跟着既有先例走，比为此新造
// 一个只被丢弃的 PickupEvent 类型划算。真正的伤害事件一定带非零 Amount 或
// Src 有效，调用方不会把两者混淆。
func UpdatePickups(w *world.World) []world.DamageEvent {
	dt := pickupDeltaSeconds(w)

	// 遍历中不能 Kill：Pool.Each 按槽位下标走，Kill 会把槽位挂回 free 队列，
	// 同一轮里这个下标可能被再次取到，造成漏遍历或重复遍历（damage.go 的
	// "第 6 步只标记不回收"就是这个理由）。先记账，循环后统一回收。
	var (
		gone []world.EntityID
		evs  []world.DamageEvent
	)

	w.Pickups.Each(func(id world.EntityID, pk *world.PickupState) bool {
		pk.Life -= float32(dt)
		if pk.Life <= 0 {
			gone = append(gone, id)
			evs = append(evs, world.DamageEvent{Dst: id})
			return true
		}
		if p := pickupTarget(w, pk); p != nil {
			applyPickup(p, pk)
			gone = append(gone, id)
			evs = append(evs, world.DamageEvent{Dst: id})
		}
		return true
	})

	for _, id := range gone {
		w.Pickups.Kill(id)
	}
	return evs
}

// pickupDeltaSeconds 返回一个 tick 的秒数。
//
// TickRate 未配置（0）时退到 1 秒一 tick：寿命会走慢，但不会除零得到
// +Inf 或 NaN——NaN 传进 pk.Life 之后 `pk.Life <= 0` 永远是 false，掉落物
// 就永远不消失了，这种 bug 只能靠读代码发现。
func pickupDeltaSeconds(w *world.World) float64 {
	if w == nil || w.TickRate == 0 {
		return 1
	}
	return 1.0 / float64(w.TickRate)
}

// pickupTarget 返回能捡起这个掉落物的玩家，没有则返回 nil。
//
// 多个玩家同时在半径内时取最近的：不取"先遍历到的"，因为 Players 的槽位
// 顺序取决于谁先连进来，同一个掉落物在不同房间里归属不同，看起来像随机。
func pickupTarget(w *world.World, pk *world.PickupState) *world.PlayerState {
	// 血瓶要走鬼魂搬运（spec L50）：只有被鬼魂拎起来（Carrier 非零）的
	// 血瓶才对活人可捡，怪直接掉在地上的瓶子谁都不能捡（spec L45）。
	// M5 起搬运动作由 GhostPickups/GhostHandoffs 提供——这条判定与那两条
	// 合起来才是完整的规则，缺任何一半都会让血瓶要么永远捡不到、
	// 要么满地可捡。
	if pk.Kind == world.PickupHealth && pk.Carrier == world.InvalidID {
		return nil
	}

	// 被鬼魂搬着的：活人不能半路截走，要等 GhostHandoffs 把 Carrier 改成
	// 活人 ID 才算"递到手里"。没有这一条，队友自己走过去就能拿，
	// spec L50 的"死亡玩家 = 后勤位"分工消失，鬼魂没有任何存在意义。
	if pk.Carrier != world.InvalidID {
		if holder := w.Players.Get(pk.Carrier); holder != nil && holder.State == world.PlayerGhost {
			return nil
		}
	}

	var (
		best     *world.PlayerState
		maxSq    = float64(PickupRadius) * float64(PickupRadius)
		bestDist float64
	)
	// 只用 DistSq 比较，不做开方：开方是单调的，比较结果一样，却多一次
	// 每 tick 每玩家每掉落物的 sqrt。
	//
	// 严格小于（而不是 <=）配 best == nil 首判定：等距时保留先遍历到的
	// 那个。写 <= 的话等距会覆盖成后遍历到的，而这个函数上一段刚说过
	// 「不取先遍历到的」——两句话就自相矛盾了。真正的规则是「取最近的；
	// 最近的不唯一时按槽位顺序取先出现的」，后者仍是确定的切片顺序，
	// 回放可复现。
	w.Players.Each(func(_ world.EntityID, p *world.PlayerState) bool {
		if p.State != world.PlayerAlive {
			return true
		}
		d := pk.Pos.DistSq(p.Pos)
		if d > maxSq {
			return true
		}
		if best == nil || d < bestDist {
			best, bestDist = p, d
		}
		return true
	})
	return best
}

// applyPickup 把掉落物的效果落到玩家身上。
//
// 这里不做"拾取成功"的判定——能不能捡已经由 pickupTarget 决定过了，
// 两条判断分散在两地会让人误以为有两套规则。
func applyPickup(p *world.PlayerState, pk *world.PickupState) {
	switch pk.Kind {
	case world.PickupGold:
		// Amount 已在 SpawnPickup 钳成非负 int32，转 uint32 安全。
		// 仍然按 uint32 上限再钳一次：Gold 是累加字段，多捡几次就可能溢出，
		// 而溢出回绕会让老玩家的金币变成 0 甚至变少。
		gained := uint32(pk.Amount)
		if p.Gold > math.MaxUint32-gained {
			p.Gold = math.MaxUint32
		} else {
			p.Gold += gained
		}

	case world.PickupSkill:
		// 装槽语义与 room.equipSkill 一致，重算派生的责任也一并接过来：
		// 强健体魄这类被动会改生命上限，不立刻重算的话血条不动，玩家会
		// 以为没捡上（room.applyChoiceLocked 装完技能同样紧跟 RecomputeDerived）。
		TryEquipSkill(p, pk.RefID)

	case world.PickupHealth:
		// 只有被搬运的血瓶能走到这里（见 pickupTarget）。回血要钳上限，
		// 否则玩家血条会超过 100%，快照里 HP>MaxHP 会让客户端血条画到框外。
		p.HP += float64(pk.Amount)
		if p.HP > p.MaxHP {
			p.HP = p.MaxHP
		}

	case world.PickupItem:
		// 道具没有表（build 包只有技能表），RefID 就是道具 ID 本身。
		// 装第一个空槽，满了丢弃：灰盒阶段地上再掉一个下次还能捡，
		// 而"替换掉玩家已有的道具"是玩家不会预期也不会同意的行为。
		for i := range p.ItemSlots {
			if p.ItemSlots[i] == 0 {
				p.ItemSlots[i] = pk.RefID
				return
			}
		}
	}
}

// TryEquipSkill 把技能装进玩家的主动/被动槽位，装上了返回 true。
//
// 签名与 Task 10 的计划一致，因此由这里先行落地。装槽规则照抄
// room.equipSkill：按技能种类选槽组，装第一个空槽；两槽全满时替换第二槽
// （第 0 槽是玩家最早选的、最可能是他想留的那个）。
//
// 与 room 里的版本的关键差别是装完立刻 RecomputeDerived：room 的调用点自己
// 紧跟着调了一次，而这里是被掉落物拾取路径调用的，调用方不该知道"装被动
// 要重算生命上限"这件事。
func TryEquipSkill(p *world.PlayerState, id uint16) bool {
	sk, ok := build.SkillByID(id)
	if !ok {
		// 未知 ID 直接失败而不是塞进槽里：塞进去之后快照会把一个不存在的
		// 技能 ID 发给客户端，客户端查表查不到就得自己兜底。
		return false
	}
	slots := &p.SkillPassive
	if sk.Kind == build.SkillKindActive {
		slots = &p.SkillActive
	}
	for i := range slots {
		if slots[i] == 0 {
			slots[i] = sk.ID
			RecomputeDerived(p)
			return true
		}
	}
	slots[1] = sk.ID
	RecomputeDerived(p)
	return true
}
