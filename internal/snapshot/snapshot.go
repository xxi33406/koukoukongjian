// Package snapshot 把 world 里的实体状态编码成协议消息。
//
// 它位于 game 与 proto 之间：既知道 world 的 SoA 布局，也知道 proto 的
// 字段。把这一层单独拎出来，是为了让 internal/game 保持"不 import 网络"
// 的分层纪律——game 只管算，这里只管把算完的东西翻译成字节。
//
// 增量策略：每个接收者各持一份 seen（记录上次发给它的实体版本），
// 本 tick 逐实体比较，变化的进 updates、新的进 adds、消失的进 removes。
// 不用"diff 两份世界"的做法——那要保存整个世界的上一帧副本，内存翻倍
// 且每次比较都要扫全量；按接收者记住 key 更省也更快。
package snapshot

import (
	"math"

	"koukoukongjian/internal/game/world"
	"koukoukongjian/internal/proto"
)

// FullInterval 是全量快照的间隔（tick 数）。
//
// 每 20 tick（20Hz 下 1 秒）发一次全量，让客户端能在丢包后自愈，也顺带
// 兜住增量逻辑的 bug——即使某个变化漏发了，一秒后也会被全量纠正。
//
// 注意全量不是"清空重发"：它只是把当前所有存活实体无条件当新增发送，
// removes 照常计算。这样客户端不需要在收到全量时清空本地实体表，也就
// 不会在全量帧的瞬间丢掉不该丢的东西。
const FullInterval = 20

// seenKey 是 seen 映射的键。
//
// 必须带实体类型：玩家池和怪物池各自从索引 1 开始编号，同一个 EntityID
// 可以同时指向一个玩家和一只怪物。只用裸 ID 做键会让两者互相覆盖——
// 表现出来就是怪物第一次出现被当成"更新"而客户端根本没有它。
type seenKey struct {
	kind entityType
	id   uint32
}

// entityType 区分四个实体池，取值与 proto.EntityKind 对应。
type entityType uint8

const (
	typePlayer entityType = iota
	typeMonster
	typeBullet
	typePickup
)

// entityTypeToProto 把内部的 entityType 映射成协议枚举。
//
// 为什么不直接 cast：typePlayer 是 iota 的 0，而 proto 的
// ENTITY_KIND_UNSPECIFIED 也是 0，PLAYER 却是 1——两套编号整体错位一格。
// 直接转类型会把玩家标成"未指定"、把拾取物标成子弹，而且因为枚举底层
// 都是整数，编译器一个警告都不会给。
//
// 未知值返回 UNSPECIFIED 而不是 panic：这函数的产出只进 removes_kind，
// 客户端对 UNSPECIFIED 的处理是"按裸 ID 删"（老客户端的退化路径），
// 比让整个 tick 崩掉合理。
func entityTypeToProto(t entityType) proto.EntityKind {
	switch t {
	case typePlayer:
		return proto.EntityKind_ENTITY_KIND_PLAYER
	case typeMonster:
		return proto.EntityKind_ENTITY_KIND_MONSTER
	case typeBullet:
		return proto.EntityKind_ENTITY_KIND_BULLET
	case typePickup:
		return proto.EntityKind_ENTITY_KIND_PICKUP
	default:
		return proto.EntityKind_ENTITY_KIND_UNSPECIFIED
	}
}

// entityKey 是判定"实体是否变化"的比较键。
//
// 只放真正影响客户端的字段。放进 Gold、PathIdx 这类纯服务端状态会让
// 实体每次都被判为变化，增量就退化成全量了。
type entityKey struct {
	posX, posY int32
	velX, velY int32
	aim        uint32
	hp         int16
	state      uint8
	flags      uint8
	element    uint8
	// ref 是"归属或类别"这类第二标识：子弹的发射者、拾取物的种类编号。
	ref uint32
}

// Builder 为单个接收者构建快照。
//
// 每个连接一个，**不可并发使用**：内部有 seen 映射。调用方在 tick 循环
// 里顺序使用即可，不需要锁。
type Builder struct {
	// seen 记录上次发给这个接收者的实体版本。
	seen map[seenKey]entityKey
	// lastFull 是上次全量的 tick，用来触发周期性全量。
	lastFull uint32
	haveFull bool
}

// NewBuilder 创建一个快照构建器。
func NewBuilder() *Builder {
	return &Builder{
		seen: make(map[seenKey]entityKey, 256),
	}
}

// Stats 是一次构建的统计，用于观测与测试。
type Stats struct {
	Adds    int
	Updates int
	Removes int
	Full    bool
}

// Build 为 selfID 对应的玩家构建一份快照。
//
// 返回的消息由调用方 Marshal 后发送。full 为 true 时无视增量状态，把
// 当前所有存活实体都放进 adds。
//
// selfID 是接收者自己的实体 ID：这个玩家的完整状态走 Snapshot.self
// 字段（含血量、金币等别人不需要知道的），其余玩家只发位置等公开字段。
// 传 world.InvalidID 表示观战者，此时所有玩家都走 adds。
func (b *Builder) Build(w *world.World, selfID world.EntityID, full bool) (*proto.Snapshot, Stats) {
	var st Stats

	// 周期性全量：丢包自愈的兜底。
	if !b.haveFull || full || w.Tick-b.lastFull >= FullInterval {
		full = true
		st.Full = true
		b.lastFull = w.Tick
		b.haveFull = true
	}

	snap := &proto.Snapshot{
		ServerTick: w.Tick,
		// 客户端靠这个标志决定"清空实体表重建"还是"增量合并"。
		// 此刻 full 已被上面的周期性判定覆写过，正值即本帧全量。
		Full: full,
	}

	// 当前这一帧出现过的实体，用来算 removes。
	alive := make(map[seenKey]struct{}, w.Monsters.Len()+w.Players.Len())

	// emit 是四个池共用的增量判定。
	//
	// 返回值表示"是否产生了需要发送的内容"，反正调用方总是继续遍历。
	emit := func(k seenKey, key entityKey, makeDelta func() *proto.EntityDelta) {
		alive[k] = struct{}{}

		prev, seen := b.seen[k]
		switch {
		case full || !seen:
			snap.Adds = append(snap.Adds, makeDelta())
			st.Adds++
		case prev != key:
			snap.Updates = append(snap.Updates, makeDelta())
			st.Updates++
		}
		b.seen[k] = key
	}

	// —— 玩家 ——
	w.Players.Each(func(id world.EntityID, p *world.PlayerState) bool {
		if id == selfID {
			snap.Self = playerSelf(id, p)
			// 自己也要进 seen，否则下一 tick 会被当成"新增"再发一遍。
			b.seen[seenKey{typePlayer, uint32(id)}] = playerKey(p)
			alive[seenKey{typePlayer, uint32(id)}] = struct{}{}
			return true
		}
		emit(
			seenKey{typePlayer, uint32(id)},
			playerKey(p),
			func() *proto.EntityDelta { return playerDelta(id, p) },
		)
		return true
	})

	// —— 怪物 ——
	w.Monsters.Each(func(id world.EntityID, m *world.MonsterState) bool {
		emit(
			seenKey{typeMonster, uint32(id)},
			monsterKey(m),
			func() *proto.EntityDelta { return monsterDelta(id, m) },
		)
		return true
	})

	// —— 子弹 ——
	//
	// 子弹生命通常只有几十个 tick，但客户端必须逐颗看到才能画弹幕，
	// 所以不做特殊的生命周期省略。
	w.Bullets.Each(func(id world.EntityID, bl *world.BulletState) bool {
		emit(
			seenKey{typeBullet, uint32(id)},
			bulletKey(bl),
			func() *proto.EntityDelta { return bulletDelta(id, bl) },
		)
		return true
	})

	// —— 拾取物 ——
	w.Pickups.Each(func(id world.EntityID, pk *world.PickupState) bool {
		emit(
			seenKey{typePickup, uint32(id)},
			pickupKey(pk),
			func() *proto.EntityDelta { return pickupDelta(id, pk) },
		)
		return true
	})

	// —— 消失的实体 ——
	//
	// 全量时也要算：客户端会保留自己看到过的实体，不告诉它删就会留下幽灵。
	// 这也是为什么全量不能简单地"清空 seen 重发"——那样 removes 永远是空的。
	for k := range b.seen {
		if _, ok := alive[k]; !ok {
			snap.Removes = append(snap.Removes, k.id)
			// 与 removes 平行同序：第 i 个 kind 描述第 i 个 remove。
			snap.RemovesKind = append(snap.RemovesKind, entityTypeToProto(k.kind))
			delete(b.seen, k)
			st.Removes++
		}
	}

	return snap, st
}

// Forget 让某个实体在下一帧被当成新增。
//
// 用于实体状态发生客户端无法理解的突变时（例如被击退传送），强制重发
// 全字段而不是让客户端在两个位置之间插值。
func (b *Builder) Forget(id world.EntityID) {
	delete(b.seen, seenKey{typePlayer, uint32(id)})
	delete(b.seen, seenKey{typeMonster, uint32(id)})
	delete(b.seen, seenKey{typeBullet, uint32(id)})
	delete(b.seen, seenKey{typePickup, uint32(id)})
}

// Reset 清空增量状态，下次 Build 必然发全量。
//
// 客户端重连、显式请求全量快照时调用。
func (b *Builder) Reset() {
	for k := range b.seen {
		delete(b.seen, k)
	}
	b.haveFull = false
	b.lastFull = 0
}

// Seen 返回当前跟踪的实体数量，用于观测。
func (b *Builder) Seen() int { return len(b.seen) }

// ——————————————————————————————————————————————————————————————
// 比较键
// ——————————————————————————————————————————————————————————————

func playerKey(p *world.PlayerState) entityKey {
	return entityKey{
		posX: proto.QuantizePos(p.Pos.X),
		posY: proto.QuantizePos(p.Pos.Y),
		velX: proto.QuantizeVel(p.Vel.X),
		velY: proto.QuantizeVel(p.Vel.Y),
		aim:  proto.QuantizeAim(p.AimDir),
		hp:   int16(clamp16(p.HP)),
		// State 与 Flags 变化也要通知客户端（鬼魂/受伤等表现）。
		state: uint8(p.State),
		flags: uint8(p.Flags),
	}
}

func monsterKey(m *world.MonsterState) entityKey {
	return entityKey{
		posX:  proto.QuantizePos(m.Pos.X),
		posY:  proto.QuantizePos(m.Pos.Y),
		velX:  proto.QuantizeVel(m.Vel.X),
		velY:  proto.QuantizeVel(m.Vel.Y),
		hp:    clamp16(m.HP),
		state: uint8(m.State),
		flags: uint8(m.Flags),
		// 元素会决定客户端画什么颜色的特效。
		element: uint8(m.Element),
	}
}

func bulletKey(bl *world.BulletState) entityKey {
	return entityKey{
		posX:  proto.QuantizePos(bl.Pos.X),
		posY:  proto.QuantizePos(bl.Pos.Y),
		velX:  proto.QuantizeVel(bl.Vel.X),
		velY:  proto.QuantizeVel(bl.Vel.Y),
		flags: uint8(bl.Flags),
		// 反射后的子弹要变色，元素决定颜色。
		element: uint8(bl.Element),
		// 发射者决定客户端按谁的队伍画颜色，反射者变了也要重发。
		ref: uint32(bl.OwnerID)<<16 | uint32(bl.ReflectorID),
	}
}

// pickupFlagCarried 是 EntityDelta.flags 的 bit0：这个掉落物正被鬼魂/队友搬运。
//
// 拾取物没有状态位（flags 原本恒 0），所以这个比特是白捡的；子弹的
// world.BulletFlags 占用同一字段，但两者 Kind 不同，客户端按 Kind 解释。
const pickupFlagCarried uint32 = 1 << 0

// pickupLifeSlot 把剩余寿命量化成 0.5 秒一档，返回档位（1 档 = 500 毫秒）。
//
// 为什么要量化：寿命每 tick 都变（20Hz），原始浮点会让每个掉落物每 tick
// 产生一条 update，白白吃带宽；0.5 秒对"还剩几秒"的倒计时环完全够用。
//
// 代价必须写清楚：同一档内值不变，因此不产生 delta，客户端看到的倒计时
// 是 0.5 秒粒度的。这不会出现"停在 0 不动"——最后一档走完后 Life 归零，
// UpdatePickups 会把掉落物 Kill 掉，客户端收到的是 remove 而不是 0。
//
// 必须有下界与 NaN 保护：AGENTS §2.6 说浮点转整数越界是平台相关的未定义值，
// 档位为负会变成反号的 Hp（客户端把倒计时画成"还有 36 秒"）。
func pickupLifeSlot(pk *world.PickupState) int16 {
	slot := float64(pk.Life) / 0.5
	if math.IsNaN(slot) || slot <= 0 {
		return 0
	}
	if slot >= math.MaxInt16 {
		return math.MaxInt16
	}
	return int16(slot)
}

// pickupKey 是掉落物的快照 diff 键。
//
// M5 加了两项（M4 遗留的可见性缺口：掉落物在客户端只有位置，看不出
// 它快没了、也看不出谁搬着它）：
//   - hp：客户端画倒计时环的唯一依据。不透明的话"快要消失"这条规则对
//     玩家不存在，而它正是"现在捡还是打完这波再捡"的决策依据。
//   - flags：鬼魂搬着的东西要能被看见（spec L50 把搬运做成了团队协作的
//     一部分，看不见就等于不存在）。
//
// 两项都必须进键：判重键不带寿命的话，"寿命变了"会被当成"没变"，
// 客户端永远停在第一档刻度上，看起来像卡死。
func pickupKey(pk *world.PickupState) entityKey {
	var carries uint8
	if pk.Carrier != world.InvalidID {
		carries = 1
	}
	return entityKey{
		posX:  proto.QuantizePos(pk.Pos.X),
		posY:  proto.QuantizePos(pk.Pos.Y),
		state: uint8(pk.Kind),
		flags: carries,
		hp:    pickupLifeSlot(pk),
		// RefID 是技能/道具编号，决定客户端选哪张贴图。
		ref: uint32(pk.RefID),
	}
}

// ——————————————————————————————————————————————————————————————
// 实体 → 消息
// ——————————————————————————————————————————————————————————————

func playerSelf(id world.EntityID, p *world.PlayerState) *proto.PlayerSelf {
	return &proto.PlayerSelf{
		EntityId: uint32(id),
		Pos:      vecQ(p.Pos),
		Vel:      vecQ(p.Vel),
		Aim:      proto.QuantizeAim(p.AimDir),
		Hp:       int32(clamp16(p.HP)),
		MaxHp:    int32(clamp16(p.MaxHP)),
		Gold:     p.Gold,
		State:    uint32(p.State),
		Flags:    uint32(p.Flags),
		// Level 在 world 里是 uint8（只可能是 1..99），协议侧没有 uint8，
		// 这里扩宽成 uint32。Level=0 表示还没进 handleJoin，客户端据此
		// 可以先把经验条藏起来。
		Level:     uint32(p.Level),
		Exp:       p.Exp,
		ExpToNext: p.ExpToNext,
	}
}

func playerDelta(id world.EntityID, p *world.PlayerState) *proto.EntityDelta {
	return &proto.EntityDelta{
		EntityId: uint32(id),
		Kind:     proto.EntityKind_ENTITY_KIND_PLAYER,
		Pos:      vecQ(p.Pos),
		Vel:      vecQ(p.Vel),
		Aim:      proto.QuantizeAim(p.AimDir),
		Hp:       int32(clamp16(p.HP)),
		MaxHp:    int32(clamp16(p.MaxHP)),
		State:    uint32(p.State),
		Flags:    uint32(p.Flags),
	}
}

func monsterDelta(id world.EntityID, m *world.MonsterState) *proto.EntityDelta {
	return &proto.EntityDelta{
		EntityId: uint32(id),
		Kind:     proto.EntityKind_ENTITY_KIND_MONSTER,
		Pos:      vecQ(m.Pos),
		Vel:      vecQ(m.Vel),
		Hp:       int32(clamp16(m.HP)),
		MaxHp:    int32(clamp16(m.MaxHP)),
		State:    uint32(m.State),
		Flags:    uint32(m.Flags),
		Element:  uint32(m.Element),
		RadiusQ:  proto.QuantizeRadius(float64(m.Radius)),
	}
}

func bulletDelta(id world.EntityID, bl *world.BulletState) *proto.EntityDelta {
	return &proto.EntityDelta{
		EntityId: uint32(id),
		Kind:     proto.EntityKind_ENTITY_KIND_BULLET,
		Pos:      vecQ(bl.Pos),
		Vel:      vecQ(bl.Vel),
		Element:  uint32(bl.Element),
		Flags:    uint32(bl.Flags),
		OwnerId:  uint32(bl.OwnerID),
		RadiusQ:  proto.QuantizeRadius(float64(bl.Radius)),
	}
}

func pickupDelta(id world.EntityID, pk *world.PickupState) *proto.EntityDelta {
	var carries uint32
	if pk.Carrier != world.InvalidID {
		carries = pickupFlagCarried
	}
	return &proto.EntityDelta{
		EntityId: uint32(id),
		Kind:     proto.EntityKind_ENTITY_KIND_PICKUP,
		Pos:      vecQ(pk.Pos),
		State:    uint32(pk.Kind),
		// Hp 复用为剩余寿命（毫秒）：拾取物没有血量概念，这个字段原本恒 0，
		// 客户端拿它画倒计时环。0.5 秒一档乘以 500 还原成毫秒。
		//
		// 注意这是量化值：客户端看到的是 0.5 秒粒度的倒计时，同档内不刷新
		// （见 pickupKey 的注释）。最后一档走完掉落物就被 Kill，不会停在 0。
		//
		// 必须先转 int32 再乘 500：int16 上界 32767×500 远超 int16 范围，
		// 先乘会回绕成负数，客户端把倒计时画成"还有 -16383 秒"。
		Hp: int32(pickupLifeSlot(pk)) * 500,
		// Flags 位 0 = 正被鬼魂/队友搬运（拾取物原本恒 0，不与其他语义冲突）。
		Flags: carries,
		// RefID 放技能/道具的编号，客户端据此选贴图。EntityDelta 没有独立
		// 字段可用，复用 OwnerId——拾取物没有归属，语义上不冲突。
		OwnerId: uint32(pk.RefID),
		RadiusQ: proto.QuantizeRadius(0.3),
	}
}

func vecQ(v world.Vec2) *proto.Vec2Q {
	return &proto.Vec2Q{
		X: proto.QuantizePos(v.X),
		Y: proto.QuantizePos(v.Y),
	}
}

// clamp16 把血量等浮点值安全地转成 int16。
//
// 必须钳位：Go 里浮点转整数越界是平台相关的未定义值，血量 -1 或
// 极大值会变成反号的数字，客户端血条直接错乱。
func clamp16(v float64) int16 {
	if v <= 0 {
		return 0
	}
	if v >= 32767 {
		return 32767
	}
	return int16(v)
}

// MatchResultToProto 把世界侧的一局结果转成协议结构（M7）。
//
// 为什么不直接让 world 产出 proto 消息：world 是叶子包（不 import proto），
// 而 proto 依赖 world 的枚举时会成环。转换层放在 snapshot——它本来就
// 是"世界状态到协议"的唯一翻译处。
func MatchResultToProto(rec world.MatchResult) *proto.MatchResult {
	out := &proto.MatchResult{
		RoomId:      rec.RoomID,
		WorldSeed:   rec.WorldSeed,
		StartedAt:   rec.StartedAt,
		EndedAt:     rec.EndedAt,
		Outcome:     matchOutcomeToProto(rec.Outcome),
		MaxWave:     uint32(rec.MaxWave),
		DurationSec: rec.DurationSec,
	}
	if len(rec.Players) > 0 {
		out.Players = make([]*proto.MatchPlayer, 0, len(rec.Players))
		for i := range rec.Players {
			p := &rec.Players[i]
			out.Players = append(out.Players, &proto.MatchPlayer{
				Slot:         uint32(p.Slot),
				Damage:       p.Damage,
				Contribute:   p.Contribute,
				Kills:        p.Kills,
				ReflectKills: p.ReflectKills,
				Deaths:       p.Deaths,
				Gold:         p.Gold,
			})
		}
	}
	return out
}

// matchOutcomeToProto 显式映射而不是直接 cast。
//
// 两侧的枚举值恰好一致（0/1/2）纯属巧合，而 cast 会让"world 侧加一种
// 终态"变成"proto 侧静默多出一个未定义值"——客户端按 default 分支
// 渲染成"未结束"，症状是"明明团灭了却显示加载中"。显式 switch 会在
// 新增分支时把编译错误留在本文件。
func matchOutcomeToProto(o world.MatchOutcome) proto.MatchOutcome {
	switch o {
	case world.OutcomeWiped:
		return proto.MatchOutcome_MATCH_OUTCOME_WIPED
	case world.OutcomeCleared:
		return proto.MatchOutcome_MATCH_OUTCOME_CLEARED
	default:
		return proto.MatchOutcome_MATCH_OUTCOME_UNSPECIFIED
	}
}
