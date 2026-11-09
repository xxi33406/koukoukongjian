// 本文件把 netx 传输层和 room 房间接起来。
//
// 它是唯一同时 import 两者的地方。分层纪律：room 不 import netx（房间只认
// Sink 通道与 InputCommand），netx 不 import room（传输层只认字节）。这里
// 做协议解码与语义映射，两边都不知道对方的存在。
package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/game/systems"
	"koukoukongjian/internal/game/world"
	"koukoukongjian/internal/netx"
	"koukoukongjian/internal/proto"
	"koukoukongjian/internal/room"
)

// sinkBuffer 是每个玩家快照通道的容量。
//
// 20Hz 下 32 帧约 1.6 秒的缓冲。太小会在本地瞬时抖动时丢帧，太大则会让
// 一个卡住的客户端占着几十 KB 内存还继续"追"老快照——追老快照比丢帧更糟，
// 玩家会看到几秒前的世界突然快进。
const sinkBuffer = 32

// netBridge 实现 netx.Handler，把网络事件翻译成房间请求。
//
// 所有回调都在 netx 的读 goroutine 上执行，所以这里绝不能阻塞：房间侧的
// 投递方法（SendInput/Leave）都是非阻塞的，Join 带超时兜底。
type netBridge struct {
	r     *room.Room
	cfg   *config.Config
	log   *slog.Logger
	token *tokenIssuer
	// ctx 在服务端关闭时取消，用来收掉所有快照泵 goroutine。
	ctx context.Context
	// wg 等待所有快照泵退出，避免关闭时泄漏。
	wg sync.WaitGroup

	// mu 保护 peers。
	mu sync.Mutex
	// peers 记录每个会话对应的玩家状态。
	peers map[uint64]*clientPeer
}

// clientPeer 是一个会话在网桥侧的记录。
type clientPeer struct {
	session *netx.Session
	name    string
	// slot 在加入前是 -1。
	slot int
	// entity 是玩家实体 ID，加入前是 InvalidID。
	entity world.EntityID
	// sink 是快照通道的写端，由房间在玩家离开时关闭。
	// 加入前为 nil。
	sink chan []byte
	// joined 表示已完成加入，可以处理输入了。
	joined bool
}

// newNetBridge 创建网桥。
func newNetBridge(ctx context.Context, r *room.Room, cfg *config.Config, log *slog.Logger) *netBridge {
	return &netBridge{
		r:     r,
		cfg:   cfg,
		log:   log,
		token: newTokenIssuer(),
		ctx:   ctx,
		peers: make(map[uint64]*clientPeer),
	}
}

// OnConnect 在新会话建立时调用。
//
// 只回 HelloAck 表明"我看到你了"，不分配座位——座位要等 Hello + JoinRoom
// 之后才有，否则一个只发空包的地址也能占座。
func (b *netBridge) OnConnect(s *netx.Session) {
	ack := &proto.HelloAck{
		SessionId: s.ID(),
		Token:     b.token.issue(s.ID()),
		RoomId:    b.r.ID,
		TickRate:  uint32(b.cfg.Server.TickRate),
	}
	b.send(s, &proto.Envelope{Body: &proto.Envelope_HelloAck{HelloAck: ack}})

	b.mu.Lock()
	b.peers[s.ID()] = &clientPeer{session: s, slot: -1}
	b.mu.Unlock()
}

// OnReliable 处理可靠通道上的一条完整消息。
func (b *netBridge) OnReliable(s *netx.Session, payload []byte) {
	var env proto.Envelope
	if err := proto.Unmarshal(payload, &env); err != nil {
		b.log.Debug("可靠消息解码失败", "session", s.ID(), "err", err)
		return
	}

	switch body := env.Body.(type) {
	case *proto.Envelope_Hello:
		b.handleHello(s, body.Hello)
	case *proto.Envelope_JoinRoom:
		b.handleJoinRoom(s, body.JoinRoom)
	case *proto.Envelope_Input:
		b.handleInput(s, body.Input)
	case *proto.Envelope_Ping:
		b.handlePing(s, body.Ping)
	default:
		b.log.Debug("未处理的消息类型", "session", s.ID())
	}
}

// OnSnapshot 处理不可靠通道上的上行快照。
//
// 协议上没有客户端发快照的用途，忽略。这个方法存在只是为了满足
// netx.Handler 接口。
func (b *netBridge) OnSnapshot(*netx.Session, []byte) {}

// OnDisconnect 在会话超时或服务端关闭时调用。
func (b *netBridge) OnDisconnect(s *netx.Session, reason string) {
	b.mu.Lock()
	cp, ok := b.peers[s.ID()]
	delete(b.peers, s.ID())
	b.mu.Unlock()

	if !ok {
		return
	}
	if cp.joined && cp.slot >= 0 {
		// 房间会关掉 sink 并回收实体。掉线即鬼魂要等 M5，M1 先按普通
		// 离开处理（座位腾空、实体消失）。
		b.r.Leave(cp.slot)
	}
	b.log.Info("断开", "session", s.ID(), "reason", reason, "slot", cp.slot)
}

// Wait 等待所有快照泵退出。服务端关闭后调用。
func (b *netBridge) Wait() { b.wg.Wait() }

func (b *netBridge) handleHello(s *netx.Session, msg *proto.Hello) {
	if msg.ProtocolVersion != proto.ProtocolVersion {
		b.log.Warn("协议版本不匹配",
			"session", s.ID(),
			"client", msg.ProtocolVersion,
			"server", proto.ProtocolVersion,
		)
		b.leaveIfJoined(s)
		s.Close()
		return
	}

	b.mu.Lock()
	if cp, ok := b.peers[s.ID()]; ok {
		cp.name = msg.PlayerName
	}
	b.mu.Unlock()

	// HelloAck 在 OnConnect 已发出，这里只记名字，等客户端发 JoinRoom。
	b.log.Debug("握手", "session", s.ID(), "name", msg.PlayerName)
}

func (b *netBridge) handleJoinRoom(s *netx.Session, msg *proto.JoinRoom) {
	if msg.RoomId != "" && msg.RoomId != b.r.ID {
		b.log.Debug("房间号不匹配", "session", s.ID(), "want", b.r.ID, "got", msg.RoomId)
		return
	}

	b.mu.Lock()
	cp, ok := b.peers[s.ID()]
	var name string
	var already bool
	var slot int
	if ok {
		name = cp.name
		already = cp.joined
		slot = cp.slot
	}
	b.mu.Unlock()

	if !ok {
		return
	}
	// 重复请求（客户端重发）就直接回原来的结果，不能二次占座。
	if already {
		b.sendJoinOk(s, slot)
		return
	}

	sink := make(chan []byte, sinkBuffer)
	ctx, cancel := context.WithTimeout(b.ctx, 2*time.Second)
	defer cancel()

	res, err := b.r.Join(ctx, name, sink, 2*time.Second)
	if err != nil {
		b.log.Info("加入被拒绝", "session", s.ID(), "name", name, "err", err)
		return
	}

	b.mu.Lock()
	cp.slot = res.Slot
	cp.entity = res.EntityID
	cp.sink = sink
	cp.joined = true
	b.mu.Unlock()

	// 会话槽位同步给传输层（调试与统计用）。
	s.SetSlot(int32(res.Slot))

	b.sendJoinOk(s, res.Slot)

	// 起快照泵：把房间写进 sink 的字节转发到会话的 Snapshot 通道。
	b.wg.Add(1)
	go b.pumpSnapshots(s, sink)

	b.broadcastPlayerJoined(res.Slot, name)
}

// pumpSnapshots 把房间的快照通道转发到会话。
//
// 必须有一个独立 goroutine：房间的 tick 循环绝不能等网络发送，而 netx 的
// SendSnapshot 是带锁的非阻塞入队——中间需要一个能阻塞的缓冲环节。
//
// sink 被房间关闭（玩家离开或房间停止）时退出。
func (b *netBridge) pumpSnapshots(s *netx.Session, sink chan []byte) {
	defer b.wg.Done()
	for {
		select {
		case data, ok := <-sink:
			if !ok {
				return
			}
			if !s.SendSnapshot(data) {
				return // 会话已关闭
			}
		case <-s.Done():
			return
		case <-b.ctx.Done():
			return
		}
	}
}

func (b *netBridge) sendJoinOk(s *netx.Session, slot int) {
	ok := &proto.JoinOk{
		Slot:      uint32(slot),
		WorldSeed: b.r.Seed(),
		StartTick: b.r.World.Tick,
	}
	b.send(s, &proto.Envelope{Body: &proto.Envelope_JoinOk{JoinOk: ok}})
}

// broadcastPlayerJoined 通知其他已加入的玩家。
//
// 快照本身会让其他人看到新实体，这条消息只是让 UI 能提前显示名字。
func (b *netBridge) broadcastPlayerJoined(slot int, name string) {
	data, err := proto.Marshal(&proto.Envelope{
		Body: &proto.Envelope_PlayerJoined{
			PlayerJoined: &proto.PlayerJoined{Slot: uint32(slot), Name: name},
		},
	})
	if err != nil {
		return
	}

	b.mu.Lock()
	targets := make([]*netx.Session, 0, len(b.peers))
	for _, cp := range b.peers {
		if cp.joined && cp.slot != slot {
			targets = append(targets, cp.session)
		}
	}
	b.mu.Unlock()

	for _, t := range targets {
		t.SendReliable(data)
	}
}

func (b *netBridge) handleInput(s *netx.Session, msg *proto.Input) {
	b.mu.Lock()
	cp, ok := b.peers[s.ID()]
	var slot int
	var joined bool
	if ok {
		slot = cp.slot
		joined = cp.joined
	}
	b.mu.Unlock()
	if !ok || !joined || len(msg.Frames) == 0 {
		return
	}

	// 一帧只取最后一条：多帧是客户端为抗抖动重复发的，逐条应用会让角色
	// 沿中间轨迹瞬移（与 room.drainInputs 的理由相同）。
	f := msg.Frames[len(msg.Frames)-1]

	// 量化解码必须在网桥做，不能进 room——×1000 是协议约定，房间不该知道。
	cmd := room.InputCommand{
		Seq:  uint16(msg.Tick),
		Move: systems.DecodeMove(f.MoveX, f.MoveY),
		Aim:  systems.DecodeAim(f.AimX, f.AimY, 0),
	}
	cmd.Buttons = f.Buttons

	b.r.SendInput(slot, cmd)
}

func (b *netBridge) handlePing(s *netx.Session, msg *proto.Ping) {
	pong := &proto.Pong{
		ClientTimeNs: msg.ClientTimeNs,
		ServerTimeNs: uint64(time.Now().UnixNano()),
	}
	b.send(s, &proto.Envelope{Body: &proto.Envelope_Pong{Pong: pong}})
}

// send 把消息编码后走可靠通道。
func (b *netBridge) send(s *netx.Session, env *proto.Envelope) {
	data, err := proto.Marshal(env)
	if err != nil {
		b.log.Warn("编码失败", "session", s.ID(), "err", err)
		return
	}
	if !s.SendReliable(data) {
		b.log.Debug("可靠通道发送失败（窗口满或已关闭）", "session", s.ID())
	}
}

// leaveIfJoined 在协议错误时清理可能的座位。
func (b *netBridge) leaveIfJoined(s *netx.Session) {
	b.mu.Lock()
	cp, ok := b.peers[s.ID()]
	var slot int
	var joined bool
	if ok {
		slot, joined = cp.slot, cp.joined
	}
	b.mu.Unlock()
	if ok && joined && slot >= 0 {
		b.r.Leave(slot)
	}
}
