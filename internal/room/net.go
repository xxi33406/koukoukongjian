package room

import (
	"context"
	"errors"
	"math"
	"time"

	"koukoukongjian/internal/game/systems"
	"koukoukongjian/internal/game/world"
	"koukoukongjian/internal/proto"
	"koukoukongjian/internal/snapshot"
)

// 本文件是房间对网络层的接口。
//
// 并发约束（docs/server-design-v0.1.md §"M1 的并发约束"）：
// world.World 的唯一持有者与访问者是 Room.Run 那个 goroutine。网络层
// 通过下面的方法投递请求，tick 循环在自己的节奏里消费；快照通过 Sink
// 通道单向流出。整条模拟路径上因此没有任何锁。

// Join 请求加入房间，阻塞到 tick 循环处理为止。
//
// sink 由调用方创建（有缓冲），读端自己留着喂写 goroutine，写端交给
// 房间。房间在玩家离开或房间停止时关闭它。
func (r *Room) Join(ctx context.Context, name string, sink chan []byte, timeout time.Duration) (JoinResult, error) {
	if r.stopped.Load() {
		return JoinResult{}, ErrRoomStopped
	}

	req := JoinRequest{
		Name:  name,
		Sink:  sink,
		Reply: make(chan JoinResult, 1),
	}

	select {
	case r.joins <- req:
	case <-r.done:
		return JoinResult{}, ErrRoomStopped
	case <-ctx.Done():
		return JoinResult{}, ctx.Err()
	}

	// 已经投递进去了。tick 循环会回执，或房间停止时我们放弃等待。
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case res := <-req.Reply:
		return res, res.Err
	case <-r.done:
		// 排队的请求可能已被 shutdown 前的最后一次 drain 处理掉，
		// 但 sink 也已被关闭。调用方看到 ErrRoomStopped 就该放弃。
		return JoinResult{}, ErrRoomStopped
	case <-ctx.Done():
		return JoinResult{}, ctx.Err()
	case <-timer.C:
		// 超时后请求可能已被处理，回执会留在有缓冲的 Reply 里被 GC。
		// 这个"泄漏"无害：没有 goroutine 阻塞在上面。
		return JoinResult{}, ErrJoinTimeout
	}
}

// SendInput 投递一条输入。非阻塞，队列满则丢弃并返回 false。
//
// 丢弃是正确的：积压的输入会让角色按几百毫秒前的按键移动，比不动更糟。
func (r *Room) SendInput(slot int, cmd InputCommand) bool {
	if r.stopped.Load() {
		return false
	}
	select {
	case r.inputs <- peerInput{slot: slot, cmd: cmd}:
		return true
	default:
		return false
	}
}

// Leave 请求离开房间，非阻塞。
//
// 离开是"最终会成功"的操作：tick 循环处理时座位已空就直接跳过。网络层
// 在会话断开时调用，那时它自己也快没了，等回执没有意义。
func (r *Room) Leave(slot int) {
	if r.stopped.Load() {
		return
	}
	select {
	case r.leaves <- leaveRequest{slot: slot}:
	default:
		// 队列满说明 tick 循环卡住了，此时也没有正确的清理可做。
	}
}

// ——————————————————————————————————————————————————————————————
// tick 循环内部的消费端（只在 Run 的 goroutine 上调用）
// ——————————————————————————————————————————————————————————————

// drainJoins 处理所有排队的加入请求。
func (r *Room) drainJoins() {
	for {
		select {
		case req := <-r.joins:
			r.handleJoin(req)
		default:
			return
		}
	}
}

func (r *Room) handleJoin(req JoinRequest) {
	slot := r.peers.findFree()
	if slot < 0 {
		req.Reply <- newJoinResult(ErrRoomFull)
		return
	}

	// 生成玩家实体。Pool 会复用回收的槽位并递增世代，客户端拿到的 ID
	// 因此不会指向别人的身体。
	id, p := r.World.Players.Spawn()
	if p == nil {
		req.Reply <- newJoinResult(ErrRoomFull)
		return
	}

	// 出生点：HasMap 时按座位号挂在出生据点中心的 1m 环上（四向 90°
	// 错位，相邻间距约 1.4m > 玩家直径，不会互相卡住）；无图回落旧
	// 2×2 方阵（测试/哨兵世界用）。见 spawnPoint 的实现注释。
	p.Pos = r.spawnPoint(slot)
	p.State = world.PlayerAlive
	p.MaxHP = 100
	p.HP = 100
	// 出生就带默认武器。不设的话 WeaponID 是零值，
	// systems.WeaponByID(0) 返回 ok=false，Fire 直接返回 false——
	// 表现为"服务器一切正常、客户端按键有反馈、就是一发子弹都没有"，
	// 而且日志里没有任何异常。
	p.WeaponID = systems.DefaultWeaponID

	pp := &peer{
		slot:    slot,
		id:      id,
		name:    req.Name,
		sink:    req.Sink,
		builder: snapshot.NewBuilder(),
	}
	r.peers.put(pp)
	r.peerCount.Store(int32(r.peers.count))

	r.log.Info("玩家加入",
		"slot", slot,
		"entity", id.String(),
		"name", req.Name,
		"peers", r.peers.count,
	)

	req.Reply <- JoinResult{
		Slot:      slot,
		EntityID:  id,
		Seed:      r.seed,
		StartTick: r.World.Tick,
	}
}

// spawnPoint 是第 slot 号座位的出生点。
//
// 抽成方法而不是内联：测试要往"玩家正右方 3 m"摆靶子，就得先知道出生点。
// 让测试自己再算一遍同一套公式，等于世界尺寸一改就有一半用例以"怪物血量
// 没下降"的形式静默失败——那是最难查的一类失败。
//
// HasMap 时出生在出生据点中心附近的小环错位（slot×90°，半径 1.0 m），
// 4 人不叠在一起。环半径 1.0 m 的可走性依据：SpawnZone 5×5 格中心保证
// 空地，且 Hub.Center 周围 HubRadiusCells=5 圆全空——1 m 偏移必可走
// （docs/plans/2026-11-14-m3-world-waves.md Task 4）。地图未生成时
// 退回旧方阵（HubCount=0 哨兵世界路径）。
func (r *Room) spawnPoint(slot int) world.Vec2 {
	if r.World.HasMap {
		hub := r.World.Hubs[r.World.SpawnHub]
		ang := float64(slot) * (math.Pi / 2)
		return world.V(hub.Center.X+math.Cos(ang)*1.0, hub.Center.Y+math.Sin(ang)*1.0)
	}
	w := r.World.Terrain.WorldWidth()
	h := r.World.Terrain.WorldHeight()
	return world.V(
		float64(slot%2)*3.0-1.5+w/2,
		float64(slot/2)*3.0-1.5+h/2,
	)
}

// drainLeaves 处理所有排队的离开请求。
func (r *Room) drainLeaves() {
	for {
		select {
		case req := <-r.leaves:
			r.handleLeave(req.slot)
		default:
			return
		}
	}
}

func (r *Room) handleLeave(slot int) {
	pp := r.peers.remove(slot)
	if pp == nil {
		return
	}
	// 回收实体。下一 tick 的快照里它就会出现在 removes 中（如果同一个
	// 接收者的 builder 还记着它）。
	if pp.id.IsValid() {
		r.World.Players.Kill(pp.id)
	}
	r.peerCount.Store(int32(r.peers.count))
	// 关掉 sink，网络层的写 goroutine 据此退出。
	close(pp.sink)

	r.log.Info("玩家离开", "slot", slot, "entity", pp.id.String(), "peers", r.peers.count)
}

// drainInputs 把排队的输入合并成"每个玩家最新的一条"。
//
// 不逐条应用：20Hz 的 tick 里收到 3 条输入时，前两条的结果必然被覆盖，
// 只有最后一条的位置是有意义的。逐条应用反而会让角色沿着中间那条路
// 瞬移一下。但序号要记下来——客户端靠它做预测校正。
func (r *Room) drainInputs() {
	for {
		select {
		case pi := <-r.inputs:
			pp := r.peers.at(pi.slot)
			if pp == nil {
				continue // 座位已空，丢弃
			}
			pp.lastInput = pi.cmd
			pp.lastInputSeq = pi.cmd.Seq
			pp.hasAim = pi.cmd.HasAim
			pp.buttons = pi.cmd.Buttons
			pp.queued++
		default:
			// 队列空了，把最新输入应用到实体上。
			r.peers.each(func(p *peer) {
				if p.queued == 0 {
					return
				}
				ent := r.World.Players.Get(p.id)
				if ent != nil {
					systems.ApplyInput(ent, p.lastInput.Move, p.lastInput.Aim, p.lastInput.HasAim)
					// 回显用的序号也要落到实体上，快照才能告诉客户端
					// "你的第 N 条输入我已处理"。
					ent.LastAckedInput = p.lastInputSeq
				}
				p.queued = 0
			})
			return
		}
	}
}

// broadcast 为每个在座玩家构建并下发一份快照。
//
// 非阻塞写入：sink 满说明客户端跟不上，丢这一帧比卡住整个房间好。
//
// 快照要包进 Envelope 再发，不能发裸 proto.Snapshot：快照通道上不止快照
// 一种消息（bridge 的 Pong 也走这条通道），而裸 protobuf 消息之间无法
// 自识别类型。实测裸 Snapshot{server_tick:123} 的字节是 087b102d，把它当
// Envelope 解会得到 err=nil、body=nil；反过来把 Envelope{Pong} 当 Snapshot
// 解得到 err=nil、tick=0。两种误判都不报错，客户端只会静默丢掉所有快照，
// 表现为"连上了但画面不动"。套一层 Envelope 后靠 oneof 的字段号区分类型。
func (r *Room) broadcast() {
	r.peers.each(func(p *peer) {
		snap, _ := p.builder.Build(r.World, p.id, false)
		snap.AckedInputTick = uint32(p.lastInputSeq)

		data, err := proto.Marshal(&proto.Envelope{
			Body: &proto.Envelope_Snapshot{Snapshot: snap},
		})
		if err != nil {
			// 编码失败是编程错误，但一个玩家出错不该拖垮整个房间。
			r.log.Warn("快照编码失败", "slot", p.slot, "err", err)
			return
		}

		select {
		case p.sink <- data:
		default:
			p.dropped++
		}
	})
}

// Stats 是房间的网络侧统计。
type Stats struct {
	// Peers 是在座玩家数（只在 tick 循环里可靠）。
	Peers int
	// Inputs 是输入队列的当前长度。
	Inputs int
	// Joins 是加入请求队列的当前长度。
	Joins int
}

// Stats 返回房间的网络侧统计。可并发调用。
//
// 只读通道长度这类原子值，不碰 peers——那是 tick 循环独占的。
func (r *Room) Stats() Stats {
	return Stats{
		Inputs: len(r.inputs),
		Joins:  len(r.joins),
	}
}

// PeerCount 返回在座玩家数，可并发调用。
//
// 用原子计数而不是读 peers：peers 是 tick 循环独占的切片。
func (r *Room) PeerCount() int { return int(r.peerCount.Load()) }

// errNoPeer 表示座位号没有对应玩家（内部与测试用）。
var errNoPeer = errors.New("room: 没有这个座位")
