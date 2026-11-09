package netx

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultClientConfig 是客户端默认参数。
func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		ReadBuffer:    2048,
		QueueSize:     64,
		ReliableBuf:   128,
		SnapshotBuf:   16,
		SweepInterval: 20 * time.Millisecond,
	}
}

// ClientConfig 是客户端参数。
type ClientConfig struct {
	// ReadBuffer 是单包读缓冲，必须 ≥ MaxPacketSize。
	ReadBuffer int
	// QueueSize 是下行队列长度。
	QueueSize int
	// ReliableBuf 是可靠消息出队缓冲。可靠消息稀疏但**不能丢**，
	// 满了说明上层消费不过来，属于真正的错误而不是可容忍的拥塞。
	ReliableBuf int
	// SnapshotBuf 是快照出队缓冲。快照可以丢，所以给小。
	SnapshotBuf int
	// SweepInterval 是重传与超时清理周期。客户端要比服务端更勤快：
	// 游戏下的手感取决于输入被尽快送达，晚 20ms 就可能多一次误判。
	SweepInterval time.Duration
}

func (c ClientConfig) validate() (ClientConfig, error) {
	if c.ReadBuffer < MaxPacketSize {
		return c, fmt.Errorf("%w: 读缓冲 %d 小于最大包长 %d", ErrBadConfig, c.ReadBuffer, MaxPacketSize)
	}
	if c.QueueSize <= 0 {
		c.QueueSize = 64
	}
	if c.ReliableBuf <= 0 {
		c.ReliableBuf = 128
	}
	if c.SnapshotBuf <= 0 {
		c.SnapshotBuf = 16
	}
	if c.SweepInterval <= 0 {
		c.SweepInterval = 20 * time.Millisecond
	}
	return c, nil
}

// ClientStats 是客户端观测快照。
type ClientStats struct {
	SentPackets    uint64
	RecvPackets    uint64
	DroppedSend    uint64
	BadPackets     uint64
	DroppedReliabl uint64
	DroppedSnap    uint64
	ReliableInFly  int
	LastRecvAge    time.Duration
}

// Client 是一条到服务端的连接。
//
// 它复用 Session 的双通道与可靠通道实现，只是角色相反：客户端**主动**
// 发可靠消息、**被动**收快照。复用而不是另写一份，是因为 ack 语义、
// 回绕处理、重传退避这些东西一旦有两份实现就必然出现两份 bug，
// 而两端的 bug 往往互相掩盖，排查代价极高。
type Client struct {
	cfg  ClientConfig
	conn *net.UDPConn
	sess *Session
	log  logger

	// Reliable 是收到的可靠消息（已按序、已去重）。
	Reliable chan []byte
	// Snapshot 是收到的快照载荷。慢消费者会丢旧帧，这与客户端渲染
	// 的实际行为一致——最后到达的快照才是有用的。
	Snapshot chan []byte

	reliableDropped atomic.Uint64
	snapshotDropped atomic.Uint64

	closeOnce sync.Once
	done      chan struct{}
	wg        sync.WaitGroup
}

// logger 抽出来只为让测试能塞个空实现而不引入 slog 依赖。
type logger interface {
	Debug(msg string, args ...any)
}

// nopLogger 丢弃所有日志。
type nopLogger struct{}

func (nopLogger) Debug(string, ...any) {}

// Dial 连接到服务端地址。
func Dial(addr string, cfg ClientConfig) (*Client, error) {
	cfg, err := cfg.validate()
	if err != nil {
		return nil, err
	}
	raddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("解析服务端地址 %q: %w", addr, err)
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return nil, fmt.Errorf("连接 %q: %w", addr, err)
	}
	return newClientWithConn(conn, cfg)
}

// newClientWithConn 用已有连接构造客户端（测试用）。
func newClientWithConn(conn *net.UDPConn, cfg ClientConfig) (*Client, error) {
	cfg, err := cfg.validate()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	c := &Client{
		cfg:      cfg,
		conn:     conn,
		sess:     newSession(1, conn.RemoteAddr().(*net.UDPAddr), cfg.QueueSize, time.Now()),
		log:      nopLogger{},
		Reliable: make(chan []byte, cfg.ReliableBuf),
		Snapshot: make(chan []byte, cfg.SnapshotBuf),
		done:     make(chan struct{}),
	}
	return c, nil
}

// SetLogger 注入日志实现。
func (c *Client) SetLogger(l logger) {
	if l != nil {
		c.log = l
	}
}

// LocalAddr 返回本地地址。
func (c *Client) LocalAddr() net.Addr { return c.conn.LocalAddr() }

// RemoteAddr 返回服务端地址。
func (c *Client) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

// Send 发一条可靠消息。
func (c *Client) Send(payload []byte) bool { return c.sess.SendReliable(payload) }

// InFlight 返回未确认的可靠包数。握手/输入的观测口。
func (c *Client) InFlight() int { return c.sess.InFlight() }

// Stats 返回统计。
func (c *Client) Stats(now time.Time) ClientStats {
	s := c.sess.Stats(now)
	return ClientStats{
		SentPackets:    s.SentPackets,
		RecvPackets:    s.RecvPackets,
		DroppedSend:    s.DroppedSend,
		BadPackets:     s.BadPackets,
		DroppedReliabl: c.reliableDropped.Load(),
		DroppedSnap:    c.snapshotDropped.Load(),
		ReliableInFly:  s.ReliableInFly,
		LastRecvAge:    s.LastRecvAge,
	}
}

// Run 阻塞运行收发循环，直到 ctx 取消或 Close。
//
// 三个 goroutine：写（从 Session 的 out 队列取包发出）、读、定时扫描。
// 收包与发包分离是必须的：一个 WriteToUDP 阻塞不该让收包停摆。
func (c *Client) Run(ctx context.Context) error {
	c.wg.Add(2)
	go func() {
		defer c.wg.Done()
		c.writeLoop()
	}()
	go func() {
		defer c.wg.Done()
		c.sweepLoop(ctx)
	}()

	// 读循环跑在调用者 goroutine 上。
	err := c.readLoop(ctx)
	c.wg.Wait()
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) readLoop(ctx context.Context) error {
	buf := make([]byte, c.cfg.ReadBuffer)
	for {
		// 与 Server 同理：不设读超时，ctx 取消后读会永久阻塞。
		_ = c.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))

		n, err := c.conn.Read(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-c.done:
				return nil
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			// 对端不可达时 Windows 会给 ICMP 错误，这类错误不该终止客户端。
			c.log.Debug("客户端收包错误", "err", err)
			continue
		}

		h, err := DecodeHeader(buf[:n])
		if err != nil {
			c.log.Debug("客户端收到坏包", "err", err)
			continue
		}

		reliable, snapshot, err := c.sess.HandlePacket(h, Payload(buf[:n]), time.Now())
		if err != nil {
			c.log.Debug("客户端处理包失败", "err", err)
			continue
		}

		for _, msg := range reliable {
			select {
			case c.Reliable <- msg:
			default:
				// 可靠消息丢了会让上层逻辑错乱（比如漏掉 JoinOk），
				// 所以单独计数，让测试与指标能看见。
				c.reliableDropped.Add(1)
			}
		}
		if snapshot != nil {
			c.pushSnapshot(snapshot)
		}
	}
}

// pushSnapshot 投递快照，满则丢最旧。
func (c *Client) pushSnapshot(payload []byte) {
	// 复制：payload 指向读缓冲，下一轮 Read 会覆盖。
	buf := make([]byte, len(payload))
	copy(buf, payload)

	select {
	case c.Snapshot <- buf:
		return
	default:
	}

	select {
	case <-c.Snapshot:
		c.snapshotDropped.Add(1)
	default:
	}
	select {
	case c.Snapshot <- buf:
	default:
		c.snapshotDropped.Add(1)
	}
}

func (c *Client) writeLoop() {
	for {
		select {
		case pkt := <-c.sess.out:
			if _, err := c.conn.Write(pkt); err != nil {
				c.log.Debug("客户端发包失败", "err", err)
			}
		case <-c.sess.done:
			// 排空后退出，最后一条消息不该丢。
			for {
				select {
				case pkt := <-c.sess.out:
					_, _ = c.conn.Write(pkt)
				default:
					return
				}
			}
		case <-c.done:
			return
		}
	}
}

func (c *Client) sweepLoop(ctx context.Context) {
	t := time.NewTicker(c.cfg.SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case <-t.C:
			c.sess.DrainDue()
			c.sess.SweepReassembly()
		}
	}
}

// Close 关闭客户端。重复调用安全。
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		c.sess.Close()
	})
	return c.conn.Close()
}

// RecvReliable 在超时内取一条可靠消息。返回 nil 表示超时。
//
// 提供它是为了让调用方（bot、测试、未来的登录流程）不必自己写
// select + timer 三段式——那段代码写十遍会有九遍漏 timer.Stop。
func (c *Client) RecvReliable(timeout time.Duration) []byte {
	if timeout <= 0 {
		select {
		case msg := <-c.Reliable:
			return msg
		default:
			return nil
		}
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case msg := <-c.Reliable:
		return msg
	case <-t.C:
		return nil
	}
}

// RecvSnapshot 在超时内取一个快照。返回 nil 表示超时。
//
// 这只给测试与 bot 用：真实客户端由渲染循环驱动，不该在游戏线程上等网络。
func (c *Client) RecvSnapshot(timeout time.Duration) []byte {
	if timeout <= 0 {
		select {
		case snap := <-c.Snapshot:
			return snap
		default:
			return nil
		}
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case snap := <-c.Snapshot:
		return snap
	case <-t.C:
		return nil
	}
}
