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

	// choices 是待应用的技能选择队列。
	//
	// 不能在 OnReliable 里直接调 room.ApplyChoice：netx 服务端只有一个
	// 读 goroutine（server.go 的 dispatch 循环），所有会话的所有包都在
	// 它上面串行分发。ApplyChoice 要等 tick 循环回执（正常 0~50ms，
	// 故障时到 1 秒上限），在那上面等，整个服务端的收包——包括其他
	// 玩家的输入——都会跟着停。所以这里只做非阻塞投递，由一个专门的
	// worker 串行应用。
	choices chan choiceApply

	// interacts 是待应用的交互请求队列（开背包 / 拾取最近）。
	//
	// 为什么不复用 choices 那条队列：两者虽然都是"网络 goroutine 投递、
	// 固定 worker 串行应用"，但耦合在一个队列上会互相阻塞——worker 在
	// 处理一次交互时若撞上 tick 卡死要等满 1 秒（room.Interact 的超时
	// 上限），同一队列里排着的技能选择就跟着等 1 秒，而技能选择窗口
	// 本来就只有几秒。分开两条队列则是各等各的，一个房间的卡顿不会
	// 顺带冻结另一个玩家的升级流程。
	//
	// 代价是多一个 goroutine 与一条队列，两者都常驻且容量个位数，
	// 与它换来的故障隔离相比可以接受。
	interacts chan interactApply

	// shopBuys 是待应用的买入请求队列。
	//
	// 单独一条队列而不是塞进 interacts：买入可能在 tick 卡死时等满 1 秒，
	// 而交互（开背包）是玩家的即时动作，被买入拖住的表现是"按了没反应"。
	// 与 interacts/choices 分开的理由完全一样。
	shopBuys chan shopBuyApply
}

// shopBuyApply 是一条待应用的买入请求。
type shopBuyApply struct {
	session    *netx.Session
	slot       int
	hub        uint32
	itemIndex  int
	targetSlot int
}

// interactApply 是一条待应用的交互请求。
type interactApply struct {
	session *netx.Session
	slot    int
	kind    int
}

// choiceApply 是一条待应用的技能选择。
type choiceApply struct {
	session *netx.Session
	slot    int
	index   int
}

// choiceQueueSize 是技能选择队列的容量。
//
// 玩家一次升级只产生一条选择，四条座位同时选也才 4 条；留 16 是给
// "点了又改主意连点几下"的余量。队列满时丢弃最合理：选择是幂等键控的
// 低频操作，而且窗口超时会给没选上的人兜底。
const choiceQueueSize = 16

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

	// lastInputTick 是最近一次已处理的输入帧 tick，用于去重。
	//
	// 输入走不可靠通道且客户端会重复携带最近几帧，同一帧到达多次是常态。
	// 没有这条记录，重复帧会让按键按位或把已松开的键又续上。
	lastInputTick uint32
	// hasInput 区分"还没收到过输入"与"收到的第一帧 tick 恰好是 0"。
	//
	// 少了它，lastInputTick 的零值会被当成合法数据，客户端发的第一帧
	// tick=0 会被判成已处理而丢弃——玩家加入后第一次操作没反应。
	hasInput bool

	// limiter 限制输入包频率。见 ratelimit.go 里对"为什么需要"的说明。
	limiter inputLimiter
}

// newNetBridge 创建网桥。
func newNetBridge(ctx context.Context, r *room.Room, cfg *config.Config, log *slog.Logger) *netBridge {
	b := &netBridge{
		r:       r,
		cfg:     cfg,
		log:     log,
		token:   newTokenIssuer(),
		ctx:     ctx,
		peers:   make(map[uint64]*clientPeer),
		choices: make(chan choiceApply, choiceQueueSize),
		// 交互队列容量与技能选择同量级：交互同样是按键触发的低频操作。
		interacts: make(chan interactApply, choiceQueueSize),
		shopBuys:  make(chan shopBuyApply, choiceQueueSize),
	}
	// 可靠下行的泵：房间只把"要发什么"投进通道，找会话是网桥的事
	// （room 不 import netx，AGENTS.md §2.4）。放在构造函数里起，与
	// OnConnect 等回调谁先到无关，避免"第一条消息到达时泵还没起"。
	b.wg.Add(1)
	go b.pumpReliable()
	b.wg.Add(1)
	go b.pumpChoices()
	b.wg.Add(1)
	go b.pumpInteracts()
	b.wg.Add(1)
	go b.pumpShopBuys()
	return b
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
//
// 只放"迟到仍有语义"的消息：握手、加入房间。这些漏一条客户端就进不来，
// 必须重传。
//
// 输入与 Ping 不在这里——它们是覆盖式数据（迟到的样本没用），走快照通道。
// 曾经为了图省事把 Input 放在这里，代价是队头阻塞让 10% 丢包下有 60% 的帧
// 被连带延迟，见 docs/milestone-m1-input-channel.md。
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
	case *proto.Envelope_ChooseSkill:
		// 技能三选一必须走可靠通道：漏一条玩家就永远不知道自己在等什么，
		// 只能等窗口超时随机装一个。它也不像输入那样是覆盖式数据——选 A
		// 和选 B 是不同的事实，不能只保留最后一条。
		b.handleChooseSkill(s, body.ChooseSkill)
	case *proto.Envelope_ShopBuy:
		// M6 Task 10 接线：买入。走可靠通道是因为它是离散事务——
		// 丢一条等于玩家按了没反应，而重发一条不会造成重复扣款
		// （room 侧按货架下标 + Sold 标记幂等）。
		b.handleShopBuy(s, body.ShopBuy)
	case *proto.Envelope_Interact:
		// M4 Task 10 接线：开背包（kind=2）走 room 的交互队列，由 tick
		// goroutine 去写 PlayerState.InventoryOpenAt 与 Busy 位。
		//
		// 走可靠通道（而不是与输入同一条快照通道）是 Task 7 定协议时的
		// 决定，理由在这里仍然成立：交互是离散事件，丢掉一条等于玩家的
		// 一次按键凭空消失；而输入是覆盖式数据，丢了下一帧就来新的。
		b.handleInteract(s, body.Interact)
	default:
		b.log.Debug("可靠通道上未处理的消息类型", "session", s.ID())
	}
}

// OnSnapshot 处理不可靠通道上的上行消息。
//
// 走这条通道的是输入与 Ping。选它有代价（丢了就没了），但走可靠通道的
// 代价更大：两者都是覆盖式数据，迟到不如不到。完整取舍见
// docs/channel-design.md。
func (b *netBridge) OnSnapshot(s *netx.Session, payload []byte) {
	var env proto.Envelope
	if err := proto.Unmarshal(payload, &env); err != nil {
		// 不可靠通道上解码失败很正常（丢包后的残包），不值得 warn。
		b.log.Debug("快照消息解码失败", "session", s.ID(), "err", err)
		return
	}

	switch body := env.Body.(type) {
	case *proto.Envelope_Input:
		b.handleInput(s, body.Input)
	case *proto.Envelope_Ping:
		b.handlePing(s, body.Ping)
	default:
		b.log.Debug("未处理的快照消息类型", "session", s.ID())
	}
}

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

// pumpReliable 把房间的可靠下行队列泵到各会话上。
//
// 为什么要有这一层：room 包不 import netx（分层硬约束，AGENTS.md §2.4），
// 拿不到 *netx.Session，而 SendReliable 只有会话有。反过来让 room 直接
// 持有发送函数又会让回调在 tick goroutine 上阻塞（SendReliable 窗口满时
// 返回 false，但会话写队列的竞争与重传全在 netx 内部）。所以房间只投
// "座位号 + 字节"，找会话、发、以及发失败后的退避都在这里。
//
// 找不到座位或会话已关就直接丢：玩家已经不在了，重试没有意义。
func (b *netBridge) pumpReliable() {
	defer b.wg.Done()
	for {
		select {
		case msg, ok := <-b.r.Reliable():
			if !ok {
				// 房间侧目前永不关闭这个通道（见 room.Reliable 注释），
				// 这一支是给未来真关掉时的防御：不判 ok 会读到零值
				// 消息并永远空转。
				return
			}
			if s := b.sessionForSlot(msg.Slot); s != nil {
				b.sendReliableWithRetry(s, msg.Data)
			}
		case <-b.ctx.Done():
			return
		}
	}
}

// sessionForSlot 找座位号对应的会话。锁内只取指针，发送在锁外做。
func (b *netBridge) sessionForSlot(slot int) *netx.Session {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, cp := range b.peers {
		if cp.joined && cp.slot == slot && cp.session != nil {
			return cp.session
		}
	}
	return nil
}

// sendReliableWithRetry 发送一条可靠下行消息，短暂拥塞时重试几次。
//
// 为什么需要重试：SendReliable 在"可靠窗口满"时返回 false，那种失败是
// 瞬时的——下一两个 tick 里对端的 ack 到了窗口就会松开。不重试的话一条
// 技能候选会因为几百毫秒的抖动直接丢掉。
//
// 为什么不无限重试：这个 goroutine 是全局共享的，卡在一条消息上会让
// 其他玩家的候选一起堵住。重试 3 次（约 60ms）已覆盖瞬时拥塞；再失败就
// 交给窗口超时兜底——玩家仍会自动获得一个技能，只是没得选。
//
// 不做窗口内周期重发，理由同上：周期重发会把"没收到"变成"收到十几条
// 列表"，客户端会反复弹窗，而最终仍由超时收敛。
func (b *netBridge) sendReliableWithRetry(s *netx.Session, data []byte) {
	const attempts = 3
	const backoff = 20 * time.Millisecond
	for i := 0; i < attempts; i++ {
		if s.SendReliable(data) {
			return
		}
		select {
		case <-s.Done():
			return
		case <-b.ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
	b.log.Debug("可靠下行重试后仍失败，交给窗口超时兜底", "session", s.ID())
}

// handleChooseSkill 把玩家的技能选择排进队列。
//
// 这里绝不能直接调 room.ApplyChoice。原因有两条，任一条都足够致命：
//
//  1. 本函数跑在 netx 服务端唯一的读 goroutine 上（server.go:249 的
//     dispatch 循环串行分发所有会话的所有包）。ApplyChoice 要等 tick
//     循环回执，正常 0~50ms、故障时到 1 秒上限；在这里等，全场收包
//     一起停。
//  2. 也不能改成"起个 goroutine 再等"：那让每个选择包换来一个 goroutine，
//     一个连点鼠标的客户端就能把服务端堆爆。所以是固定 worker + 有界队列。
//
// 锁内只取座位号，投递也在锁外——虽然投递本身不阻塞，但没必要占锁。
func (b *netBridge) handleChooseSkill(s *netx.Session, msg *proto.ChooseSkill) {
	b.mu.Lock()
	cp, ok := b.peers[s.ID()]
	var slot int
	var joined bool
	if ok {
		slot, joined = cp.slot, cp.joined
	}
	b.mu.Unlock()

	if !ok || !joined || slot < 0 {
		b.log.Debug("未加入的会话发来技能选择", "session", s.ID())
		return
	}

	select {
	case b.choices <- choiceApply{session: s, slot: slot, index: int(msg.GetChoiceIndex())}:
	default:
		b.log.Debug("技能选择队列满，丢弃", "session", s.ID(), "slot", slot)
	}
}

// handleInteract 把交互请求投进队列。
//
// 结构照抄 handleChooseSkill（同一套"锁内只取座位号、锁外投递"的理由），
// 差别只在校验 kind：room.Interact 自己也会拒，但在这里先拒能少走一趟
// 队列，也让日志能区分"客户端发了不认识的 kind"与"房间侧拒绝"。
func (b *netBridge) handleInteract(s *netx.Session, msg *proto.Interact) {
	kind := int(msg.GetKind())
	if kind != room.InteractKindPickupNearest && kind != room.InteractKindOpenInventory {
		// proto 注释已经写明"其余值服务端忽略"，所以这是 Debug 不是 Warn：
		// 未来版本的客户端可能先发了新 kind，服务端不该刷警告。
		b.log.Debug("未知的交互类型，忽略", "session", s.ID(), "kind", kind)
		return
	}

	b.mu.Lock()
	cp, ok := b.peers[s.ID()]
	var slot int
	var joined bool
	if ok {
		slot, joined = cp.slot, cp.joined
	}
	b.mu.Unlock()

	if !ok || !joined || slot < 0 {
		b.log.Debug("未加入的会话发来交互", "session", s.ID(), "kind", kind)
		return
	}

	select {
	case b.interacts <- interactApply{session: s, slot: slot, kind: kind}:
	default:
		b.log.Debug("交互队列满，丢弃", "session", s.ID(), "slot", slot, "kind", kind)
	}
}

// pumpInteracts 串行应用交互请求。
//
// 串行而非并发：Interact 会把请求投进 room 的 interacts 队列并等回执，
// 并发调用的收益是零（room 侧本来就是 tick 级串行消费），却要多管理一批
// 生命周期、多一份并发正确性的证明。
func (b *netBridge) pumpInteracts() {
	defer b.wg.Done()
	for {
		select {
		case req := <-b.interacts:
			if err := b.r.Interact(req.slot, req.kind); err != nil {
				// 与技能选择同款：这些都是客户端行为或状态问题（死了还
				// 开背包、未知 kind、房间已停），Debug 足够，免得误触刷屏。
				b.log.Debug("交互被拒绝",
					"session", req.session.ID(), "slot", req.slot,
					"kind", req.kind, "err", err)
			}
		case <-b.ctx.Done():
			return
		}
	}
}

// pumpChoices 串行应用技能选择。
//
// 串行是刻意的：同一个玩家的两次选择必须按到达顺序应用，否则"先选 A
// 再选 B"可能变成先 B 后 A，槽位里的技能与玩家看到的最后选择不一致。
// 队列长度与玩家数同量级，串行处理不会成为瓶颈。
func (b *netBridge) pumpChoices() {
	defer b.wg.Done()
	for {
		select {
		case req := <-b.choices:
			if err := b.r.ApplyChoice(req.slot, req.index); err != nil {
				// 不在窗口里、下标越界都是客户端行为问题（窗口过期、
				// 与服务端列表不同步），不是服务端故障；Debug 足够，
				// 免得误点刷屏。
				b.log.Debug("技能选择被拒绝",
					"session", req.session.ID(), "slot", req.slot,
					"index", req.index, "err", err)
			}
		case <-b.ctx.Done():
			return
		}
	}
}

func (b *netBridge) sendJoinOk(s *netx.Session, slot int) {
	ok := &proto.JoinOk{
		Slot:      uint32(slot),
		WorldSeed: b.r.Seed(),
		// 不能用 b.r.World.Tick：那个字段只在 tick goroutine 里写，
		// 在这里读就是跨 goroutine 读正在被改的内存（-race 抓得到）。
		StartTick: b.r.WorldTick(),
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

// maxInputWindow 是一包输入最多接受多少帧。
//
// 客户端建议带 3 帧；留到 8 是给"客户端卡了 100ms 后一次补发"留余量。
// 上限不是为了省内存（一帧 30 字节），而是为了抵挡恶意客户端一次塞一万帧
// 让服务器在每个 tick 里做无意义的按位或。
const maxInputWindow = 8

// handleInput 处理一包上行输入。
//
// 这条通道不可靠，所以客户端每包重复携带最近几帧。这里的职责是三件事：
//
//  1. 去重：同一帧可能被多包携带送达多次，只能处理一次。
//  2. 取最新帧作移动与瞄准意图：帧是覆盖式的，中间帧的移动意图没有意义。
//     逐帧应用会让角色沿压缩后的轨迹瞬移（与 room.drainInputs 的理由相同）。
//  3. 按键做按位或：捕获"按下又松开发生在两包之间"的短按。只对**新帧**
//     做或，否则已处理过的旧帧会把按住状态永远续上。
//
// 代价必须写明：补帧场景下"松开"会晚一包（约 16ms）才生效。因为 or 把
// 窗口内出现过的按键都当作按住。对连发式攻击只多打一颗子弹，对技能键
// （按一次触发一次）反而是必要的。见 docs/channel-design.md。
func (b *netBridge) handleInput(s *netx.Session, msg *proto.Input) {
	if len(msg.Frames) == 0 {
		return
	}
	frames := msg.Frames
	if len(frames) > maxInputWindow {
		// 帧按 tick 升序，最新的在尾部。
		frames = frames[len(frames)-maxInputWindow:]
	}

	b.mu.Lock()
	cp, ok := b.peers[s.ID()]
	if !ok || !cp.joined {
		b.mu.Unlock()
		return
	}

	// 限速在锁内、去重之前：被限速的包连解析都不该做。
	// 队形（r.inputs）是所有玩家共享的，一个坏客户端能挤掉别人。
	if !cp.limiter.allow(time.Now()) {
		b.mu.Unlock()
		return
	}

	newest, buttons, changed := mergeInputFrames(frames, cp.lastInputTick, cp.hasInput)
	if !changed {
		// 整包都是已处理过的帧。重复送达很常见（丢包后客户端补帧），
		// 静默忽略，不要重复投递到房间。
		b.mu.Unlock()
		return
	}
	cp.lastInputTick = newest.Tick
	cp.hasInput = true
	slot := cp.slot
	b.mu.Unlock()

	// 量化解码必须在网桥做，不能进 room——×1000 是协议约定，房间不该知道。
	cmd := room.InputCommand{
		Seq:  uint16(newest.Tick),
		Move: systems.DecodeMove(newest.MoveX, newest.MoveY),
		Aim:  systems.DecodeAim(newest.AimX, newest.AimY, 0),
		// 两个分量都为 0 才算"没给方向"。只有一个是 0 是合法的
		// 正右/正上瞄准。
		HasAim:  newest.AimX != 0 || newest.AimY != 0,
		Buttons: buttons,
	}
	b.r.SendInput(slot, cmd)
}

// mergeInputFrames 从一包输入里挑出最新帧，并把窗口内的按键按位或。
//
// 参数 lastTick/hasLast 是上一包已处理到的位置。返回 changed=false 表示
// 整包都是重复帧，调用方应直接忽略。
//
// 抽成纯函数是为了能脱离网络与锁测它——这段逻辑的三个坑（重复帧续上按键、
// tick 回绕、首帧 tick=0）都只在特定输入序列下暴露，用真 UDP 测很难覆盖。
func mergeInputFrames(frames []*proto.InputFrame, lastTick uint32, hasLast bool) (newest *proto.InputFrame, buttons uint32, changed bool) {
	for _, f := range frames {
		if f == nil {
			continue
		}
		if hasLast && !tickAfter(f.Tick, lastTick) {
			continue // 已处理过的帧
		}
		buttons |= f.Buttons
		if newest == nil || tickAfter(f.Tick, newest.Tick) {
			newest = f
		}
	}
	return newest, buttons, newest != nil
}

// tickAfter 判断 a 是否晚于 b，用 int32 差处理 uint32 回绕。
//
// 直接写 a > b 会在 tick 越过 2^32 边界时永久失效——那时候所有新帧都
// 被判成"旧帧"，玩家再也动不了，而且没有任何报错。
// 60Hz 下 2^32 tick 约 828 天，实际撞不上，但 uint16 的 Seq 字段会——
// 两者共用同一个 helper 保证不会只改一处。
func tickAfter(a, b uint32) bool { return int32(a-b) > 0 }

// handlePing 回应 RTT 探测。
//
// Ping 与 Pong 都走快照通道。Ping 测的是"此刻往返多久"，补发的旧样本
// 算出的 RTT 是过去的 RTT，比没有还糟——它会把网络变差的信号抹平。
// 走可靠通道还会白占发窗与重传预算。
func (b *netBridge) handlePing(s *netx.Session, msg *proto.Ping) {
	pong := &proto.Pong{
		ClientTimeNs: msg.ClientTimeNs,
		ServerTimeNs: uint64(time.Now().UnixNano()),
	}
	b.sendSnap(s, &proto.Envelope{Body: &proto.Envelope_Pong{Pong: pong}})
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

// sendSnap 把消息编码后走快照通道。
//
// 失败不重试也不记 warn：快照通道本来就不保证送达，下一包接上就好。
func (b *netBridge) sendSnap(s *netx.Session, env *proto.Envelope) {
	data, err := proto.Marshal(env)
	if err != nil {
		b.log.Warn("编码失败", "session", s.ID(), "err", err)
		return
	}
	if !s.SendSnapshot(data) {
		b.log.Debug("快照通道发送失败（队列满或已关闭）", "session", s.ID())
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

// handleShopBuy 把买入请求投进队列。
//
// 结构照抄 handleInteract（同一套"锁内只取座位号、锁外投递"的理由）。
func (b *netBridge) handleShopBuy(s *netx.Session, msg *proto.ShopBuy) {
	b.mu.Lock()
	cp, ok := b.peers[s.ID()]
	var slot int
	var joined bool
	if ok {
		slot, joined = cp.slot, cp.joined
	}
	b.mu.Unlock()

	if !ok || !joined || slot < 0 {
		b.log.Debug("未加入的会话发来买入", "session", s.ID())
		return
	}

	select {
	case b.shopBuys <- shopBuyApply{
		session:    s,
		slot:       slot,
		hub:        msg.GetHub(),
		itemIndex:  int(msg.GetItemIndex()),
		targetSlot: int(msg.GetTargetSlot()),
	}:
	default:
		// 队列满时丢弃：买入是玩家的显式点击，丢了他会再点一次，
		// 而且下一次会看到刷新后的货架，比让会话读 goroutine 卡在这里好。
		b.log.Debug("买入队列满，丢弃", "session", s.ID(), "slot", slot)
	}
}

// pumpShopBuys 串行应用买入请求。
//
// 串行而非并发：ShopBuy 会把请求投进 room 的 shopBuys 队列并等回执，
// 并发调用没有收益（room 侧本来就是 tick 级串行消费），却要多管一批
// 生命周期。与 pumpInteracts / pumpChoices 同款。
func (b *netBridge) pumpShopBuys() {
	defer b.wg.Done()
	for {
		select {
		case req := <-b.shopBuys:
			if err := b.r.ShopBuy(req.slot, req.hub, req.itemIndex, req.targetSlot); err != nil {
				// 拒绝原因多半是客户端行为（钱不够/商品已售出/不在据点内），
				// Debug 足够，免得玩家反复点买不起的东西时刷屏。
				b.log.Debug("买入被拒绝",
					"session", req.session.ID(), "slot", req.slot,
					"hub", req.hub, "item", req.itemIndex, "err", err)
			}
		case <-b.ctx.Done():
			return
		}
	}
}
