package netx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultServerConfig 是一组保守的默认值，覆盖局域网的常见情况。
func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		ReadBuffer:     2048,
		QueueSize:      64,
		SessionTimeout: 5 * time.Second,
		SweepInterval:  1 * time.Second,
		MaxSessions:    16,
	}
}

// ServerConfig 是 UDP 服务端的可调参数。
type ServerConfig struct {
	// ReadBuffer 是单包读缓冲大小。必须 ≥ MaxPacketSize，否则大包会被截断。
	ReadBuffer int
	// QueueSize 是每个会话的下行队列长度。
	QueueSize int
	// SessionTimeout 是多久收不到包就判定掉线。
	SessionTimeout time.Duration
	// SweepInterval 是重传扫描与超时清理的周期。
	SweepInterval time.Duration
	// MaxSessions 是同时存在的会话上限，防止伪造源地址把内存打满。
	MaxSessions int
}

func (c ServerConfig) validate() (ServerConfig, error) {
	if c.ReadBuffer < MaxPacketSize {
		return c, fmt.Errorf("%w: 读缓冲 %d 小于最大包长 %d", ErrBadConfig, c.ReadBuffer, MaxPacketSize)
	}
	if c.QueueSize <= 0 {
		c.QueueSize = 64
	}
	if c.SessionTimeout <= 0 {
		return c, fmt.Errorf("%w: 会话超时必须为正", ErrBadConfig)
	}
	if c.SweepInterval <= 0 {
		return c, fmt.Errorf("%w: 扫描周期必须为正", ErrBadConfig)
	}
	if c.MaxSessions <= 0 {
		return c, fmt.Errorf("%w: 会话上限必须为正", ErrBadConfig)
	}
	return c, nil
}

// Handler 接收网络层交给上层的事件。
//
// **所有回调都在网络 goroutine 上执行**，绝不能在里面跑游戏逻辑或阻塞：
// 一次阻塞会让所有玩家的收包停摆。正确处理是投进 channel 后立刻返回，
// 由 tick 循环消费（见 docs/server-design-v0.1.md §17 并发约束）。
type Handler interface {
	// OnConnect 在新会话建立时调用（收到该地址的第一个合法包之前不会调用）。
	OnConnect(s *Session)
	// OnReliable 收到一条完整、可按序交付的可靠消息。
	OnReliable(s *Session, payload []byte)
	// OnSnapshot 收到一个快照通道的包（仅用于回环测试，生产客户端不发送）。
	OnSnapshot(s *Session, payload []byte)
	// OnDisconnect 在会话超时或服务端关闭时调用。reason 仅用于日志。
	OnDisconnect(s *Session, reason string)
}

// HandlerFuncs 用函数字段实现 Handler，便于测试。
type HandlerFuncs struct {
	ConnectFn    func(s *Session)
	ReliableFn   func(s *Session, payload []byte)
	SnapshotFn   func(s *Session, payload []byte)
	DisconnectFn func(s *Session, reason string)
}

func (h HandlerFuncs) OnConnect(s *Session) {
	if h.ConnectFn != nil {
		h.ConnectFn(s)
	}
}

func (h HandlerFuncs) OnReliable(s *Session, payload []byte) {
	if h.ReliableFn != nil {
		h.ReliableFn(s, payload)
	}
}

func (h HandlerFuncs) OnSnapshot(s *Session, payload []byte) {
	if h.SnapshotFn != nil {
		h.SnapshotFn(s, payload)
	}
}

func (h HandlerFuncs) OnDisconnect(s *Session, reason string) {
	if h.DisconnectFn != nil {
		h.DisconnectFn(s, reason)
	}
}

// Server 是 UDP 服务端。
//
// 生命周期：NewServer → Run(ctx) 阻塞直到 ctx 取消 → 所有会话关闭。
type Server struct {
	cfg    ServerConfig
	log    *slog.Logger
	conn   *net.UDPConn
	handle Handler

	mu       sync.RWMutex
	sessions map[string]*Session
	nextID   atomic.Uint64

	// wg 跟踪所有后台 goroutine（每个会话一个写 goroutine）。
	//
	// 危险点：wg.Add 与 wg.Wait 并发是 WaitGroup 的未定义行为，
	// 而 Close() 可能由调用方 goroutine 触发、Run() 退出时也会调
	// shutdown。两者撞上就是 Add 与 Wait 并发——用 shutdownOnce
	// 保证只有第一个到达的能进清理，其余的等同一个结果。
	wg sync.WaitGroup

	// shutdownOnce 保证关闭路径只执行一次。
	// Close() 与 Run() 的退出分支都会调 shutdown，谁先到谁负责清理。
	shutdownOnce sync.Once
	// closing 置位后 getOrCreateSession 不再建新会话，避免
	// shutdown 遍历完 map 之后又有会话被塞进来而永远不被关闭。
	closing atomic.Bool

	// 统计
	recvTotal  atomic.Uint64
	recvBad    atomic.Uint64
	sentTotal  atomic.Uint64
	connTotal  atomic.Uint64
	dropNewSes atomic.Uint64
}

// NewServer 在 conn 上建立服务端。conn 由调用方负责关闭。
func NewServer(conn *net.UDPConn, cfg ServerConfig, log *slog.Logger, h Handler) (*Server, error) {
	if conn == nil {
		return nil, fmt.Errorf("%w: 连接为空", ErrBadConfig)
	}
	if log == nil {
		log = slog.Default()
	}
	if h == nil {
		return nil, fmt.Errorf("%w: Handler 为空", ErrBadConfig)
	}
	cfg, err := cfg.validate()
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg:      cfg,
		log:      log,
		conn:     conn,
		handle:   h,
		sessions: make(map[string]*Session, cfg.MaxSessions),
	}, nil
}

// Listen 是一个便利函数：绑定 addr 并建服务端。返回的 Server 关闭时也会关 conn。
func Listen(addr string, cfg ServerConfig, log *slog.Logger, h Handler) (*Server, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("解析监听地址 %q: %w", addr, err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("监听 %q: %w", addr, err)
	}
	s, err := NewServer(conn, cfg, log, h)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return s, nil
}

// Run 阻塞运行服务端，直到 ctx 取消。
//
// 内部起三类 goroutine：读循环（本函数自身）、每会话写循环、定时扫描循环。
// 返回时保证所有会话都已关闭、所有 goroutine 已退出。
func (s *Server) Run(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context 为空", ErrBadConfig)
	}
	var sweepWG sync.WaitGroup
	sweepWG.Add(1)
	go func() {
		defer sweepWG.Done()
		s.sweepLoop(ctx.Done())
	}()

	buf := make([]byte, s.cfg.ReadBuffer)
	for {
		// 设个读超时，这样 ctx 取消后最多等这么久就能退出。
		// 不用 SetReadDeadline 配合 ctx 的话，读会永久阻塞。
		_ = s.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				s.shutdown("服务端关闭")
				sweepWG.Wait()
				return nil
			default:
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			// UDP 上一个坏包不该让服务端挂掉，但要能看见。
			s.recvBad.Add(1)
			s.log.Debug("收包失败", "err", err)
			continue
		}
		s.recvTotal.Add(1)
		s.dispatch(buf[:n], addr)
	}
}

func (s *Server) dispatch(pkt []byte, addr *net.UDPAddr) {
	h, err := DecodeHeader(pkt)
	if err != nil {
		s.recvBad.Add(1)
		// 未建立会话的地址收到坏包是常态（端口扫描、上一个进程的残留包），
		// 只记 trace 级，否则日志会被刷屏。
		s.log.Debug("丢弃坏包", "from", addr.String(), "err", err)
		return
	}

	sess := s.getOrCreateSession(addr)
	if sess == nil {
		return
	}

	payload := Payload(pkt)

	var reliable [][]byte
	var snapshot []byte
	// 副本：HandlePacket 之后 pkt 会被下一次 ReadFromUDP 覆盖，
	// 而上面的 reliable 载荷已经在 reliableReceiver 里复制过，这里只处理
	// snapshot 的借用问题。
	reliable, snapshot, err = sess.HandlePacket(h, payload, time.Now())
	if err != nil {
		s.log.Debug("会话处理包失败", "from", addr.String(), "err", err)
		return
	}

	for _, msg := range reliable {
		s.handle.OnReliable(sess, msg)
	}
	if snapshot != nil {
		s.handle.OnSnapshot(sess, snapshot)
	}
}

func (s *Server) getOrCreateSession(addr *net.UDPAddr) *Session {
	key := addr.String()

	s.mu.RLock()
	sess := s.sessions[key]
	s.mu.RUnlock()
	if sess != nil {
		return sess
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// 双检：两个包并发进来的话，第一个已经建好了。
	if sess := s.sessions[key]; sess != nil {
		return sess
	}
	// 已经开始关闭了就别再建新会话：shutdown 可能已经把 map 遍历完，
	// 之后塞进来的会话没人会去关，写 goroutine 就泄漏了。
	if s.closing.Load() {
		return nil
	}
	if len(s.sessions) >= s.cfg.MaxSessions {
		s.dropNewSes.Add(1)
		s.log.Warn("会话数已达上限，拒绝新连接", "from", key, "max", s.cfg.MaxSessions)
		return nil
	}

	id := s.nextID.Add(1)
	sess = newSession(id, addr, s.cfg.QueueSize, time.Now())
	s.sessions[key] = sess
	s.connTotal.Add(1)

	// 写 goroutine：每个会话一个，负责从 out 队列取包发出去。
	// 不能共用一个大 goroutine 串行写所有会话——一个慢客户端会拖累别人。
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.writeLoop(sess)
	}()

	s.log.Info("新会话", "id", id, "from", key)
	s.handle.OnConnect(sess)
	return sess
}

func (s *Server) writeLoop(sess *Session) {
	for {
		select {
		case pkt := <-sess.out:
			n, err := s.conn.WriteToUDP(pkt, sess.addr)
			if err != nil {
				// 写失败通常是 ICMP 端口不可达，客户端已经走了。
				// 不要在这里关会话：超时清理会处理，而且会话关闭要让
				// 上层知道（走 OnDisconnect）。
				s.log.Debug("发包失败", "id", sess.ID(), "to", sess.addr.String(), "err", err)
				continue
			}
			s.sentTotal.Add(1)
			if n != len(pkt) {
				s.log.Debug("短写", "id", sess.ID(), "want", len(pkt), "got", n)
			}
		case <-sess.done:
			// 排空残留：最后一条包往往是 PlayerLeft 这类告别消息，
			// 直接退出会让客户端以为是被网络掐断的。
			for {
				select {
				case pkt := <-sess.out:
					if _, err := s.conn.WriteToUDP(pkt, sess.addr); err == nil {
						s.sentTotal.Add(1)
					}
				default:
					return
				}
			}
		}
	}
}

func (s *Server) sweepLoop(done <-chan struct{}) {
	t := time.NewTicker(s.cfg.SweepInterval)
	defer t.Stop()

	for {
		select {
		case <-done:
			return
		case now := <-t.C:
			s.sweepOnce(now)
		}
	}
}

func (s *Server) sweepOnce(now time.Time) {
	s.mu.RLock()
	list := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		list = append(list, sess)
	}
	s.mu.RUnlock()

	for _, sess := range list {
		if sess.Closed() {
			continue
		}
		// 重传与分片清理
		sess.DrainDue()
		sess.SweepReassembly()

		if now.Sub(sess.LastRecv()) > s.cfg.SessionTimeout {
			s.dropSession(sess, "收包超时")
		}
	}
}

// dropSession 关闭会话并从表中移除。
func (s *Server) dropSession(sess *Session, reason string) {
	key := sess.addr.String()

	s.mu.Lock()
	cur, ok := s.sessions[key]
	if ok && cur == sess {
		delete(s.sessions, key)
	}
	s.mu.Unlock()

	if !ok {
		return
	}
	sess.Close()
	s.log.Info("会话结束", "id", sess.ID(), "from", key, "reason", reason)
	s.handle.OnDisconnect(sess, reason)
}

// shutdown 关闭所有会话并等待后台 goroutine 退出。
//
// 幂等：Close() 与 Run() 的退出分支都会调它，第二次调用直接返回。
// 没有这层保护会踩两个坑：① dropSession 对同一会话重复回调
// OnDisconnect；② wg.Wait 与别处的 wg.Add 并发（WaitGroup 明令禁止）。
func (s *Server) shutdown(reason string) {
	s.shutdownOnce.Do(func() {
		s.closing.Store(true)

		s.mu.RLock()
		list := make([]*Session, 0, len(s.sessions))
		for _, sess := range s.sessions {
			list = append(list, sess)
		}
		s.mu.RUnlock()

		for _, sess := range list {
			s.dropSession(sess, reason)
		}
		// 等所有写 goroutine 排空退出。
		s.wg.Wait()
	})
}

// Close 主动关闭服务端并等待所有 goroutine 退出。
func (s *Server) Close() error {
	s.shutdown("服务端关闭")
	return s.conn.Close()
}

// Session 按地址查会话，主要用于测试与回环。
func (s *Server) Session(addr string) *Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[addr]
}

// SessionCount 返回当前会话数。
func (s *Server) SessionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}

// LocalAddr 返回监听地址。
func (s *Server) LocalAddr() net.Addr { return s.conn.LocalAddr() }

// ServerStats 是服务端的观测快照。
type ServerStats struct {
	Sessions   int
	RecvTotal  uint64
	RecvBad    uint64
	SentTotal  uint64
	ConnTotal  uint64
	DropNewSes uint64
}

// Stats 返回服务端统计。
func (s *Server) Stats() ServerStats {
	return ServerStats{
		Sessions:   s.SessionCount(),
		RecvTotal:  s.recvTotal.Load(),
		RecvBad:    s.recvBad.Load(),
		SentTotal:  s.sentTotal.Load(),
		ConnTotal:  s.connTotal.Load(),
		DropNewSes: s.dropNewSes.Load(),
	}
}
