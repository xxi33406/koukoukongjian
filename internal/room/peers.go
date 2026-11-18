package room

import (
	"koukoukongjian/internal/game/build"
	"koukoukongjian/internal/game/world"
	"koukoukongjian/internal/snapshot"
)

// MaxSlots 是房间的座位数，与 config.World.MaxPlayers 的上限一致。
//
// 座位号（slot）与实体 ID 是两回事：slot 是"四把椅子中的第几把"，实体
// 是玩家在池里的那具身体。玩家离开后椅子空出来，下一个加入的人坐同一把
// 椅子，但拿到的是全新世代的实体。分开之后，快照里的 self 字段用实体 ID
// 寻址，就不会因为重连而错位。
const MaxSlots = 4

// peer 是一个已加入的玩家。
//
// 生命周期：join 时创建，leave 或房间停止时销毁。所有字段只在 tick 循环
// 那个 goroutine 里读写，所以不需要锁。
type peer struct {
	// slot 是座位号（0..MaxSlots-1）。
	slot int
	// id 是这个玩家在实体池里的 ID。玩家还没生成实体时是 InvalidID。
	id world.EntityID
	// name 是显示名，M1 只用于日志。
	name string

	// sink 是快照的输出通道。tick 循环非阻塞写入，满则丢弃——
	// 慢客户端丢帧比拖慢整个房间好。
	sink chan []byte
	// builder 是快照增量状态，每个接收者一份。
	builder *snapshot.Builder

	// lastInput 是这一 tick 用到的最新输入。
	lastInput InputCommand
	// lastInputSeq 记录最后一次收到的输入序号，回显给客户端做预测校正。
	lastInputSeq uint16

	// hasAim 是最近一条输入是否带了瞄准方向。
	// 为 false 时要走自动瞄准——注意不能靠 Aim==0 判断，
	// 0 是合法的朝向（正 +X）。
	hasAim bool

	// buttons 是最近一条输入的按键位掩码（"当前按住"，不是"本帧按下"）。
	buttons uint32

	// queued 是攒下来的输入条数，用于观测"客户端发得太快"。
	queued int
	// dropped 是因为 sink 满而丢掉的快照数。
	dropped uint64

	// pendingChoices 是服务端发出去、还在等玩家选的候选。
	//
	// 存在这里而不是随消息发完就丢：超时随机、以及客户端因为丢包没收到
	// 候选时，唯一的补救是把服务端记着的这份再发一次。它同时也是
	// ChooseSkill.choice_index 的索引空间——客户端选的下标指向的是这一次
	// 下发的列表，不是技能表，所以顺序必须与下发时完全一致（不要排序）。
	pendingChoices []build.Skill
	// choiceDeadline 是决策截止 tick（w.Tick 的相对时刻，用 room 的 tick 基准）。
	//
	// 用 tick 而不是墙钟时间：超时判定发生在 tick 循环里，"再过 N 个 tick"
	// 精确可复现；用 time.Now 会让同一份回放在快慢机器上超时点不同。
	choiceDeadline uint32
	// awaitingChoice 表示这个玩家当前处在选择窗口里（窗口内无敌）。
	//
	// 单独一个布尔值而不是靠 len(pendingChoices) > 0 推断：选完之后候选与
	// 窗口是两个独立的生命周期——池子耗尽时会有"开了窗口但没有候选"的
	// 情况，用长度推断就会把它当成"不在窗口里"，无敌永远不开。
	awaitingChoice bool
}

// peers 管理四个座位。
type peers struct {
	// slots[i] 是坐在第 i 把椅子上的人，nil 表示空位。
	slots [MaxSlots]*peer
	// count 是已占用的椅子数。
	count int
}

// findFree 返回第一个空位下标，没有则返回 -1。
func (ps *peers) findFree() int {
	for i := 0; i < MaxSlots; i++ {
		if ps.slots[i] == nil {
			return i
		}
	}
	return -1
}

// at 返回第 slot 把椅子上的人，空位或越界返回 nil。
func (ps *peers) at(slot int) *peer {
	if slot < 0 || slot >= MaxSlots {
		return nil
	}
	return ps.slots[slot]
}

// each 遍历所有在座玩家。顺序是座位号升序——遍历顺序必须稳定，
// 因为快照里实体的发送顺序会影响客户端看到的第一帧。
func (ps *peers) each(fn func(p *peer)) {
	for i := 0; i < MaxSlots; i++ {
		if p := ps.slots[i]; p != nil {
			fn(p)
		}
	}
}

// put 把 peer 放上指定的椅子。
func (ps *peers) put(p *peer) {
	ps.slots[p.slot] = p
	ps.count++
}

// remove 把指定座位的 peer 摘下并返回。空位返回 nil。
func (ps *peers) remove(slot int) *peer {
	if slot < 0 || slot >= MaxSlots {
		return nil
	}
	p := ps.slots[slot]
	if p == nil {
		return nil
	}
	ps.slots[slot] = nil
	ps.count--
	return p
}
