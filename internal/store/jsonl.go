// Package store 把一局的结果落盘。
//
// 只做一件事：把 world.MatchResult 追加进 JSONL 文件。不做查询、不做
// 迁移、不引入数据库驱动——server-design §11.1 的第 2 层只要求"记下
// 每局发生了什么"，存储介质不是设计的一部分（拍板决定 #2）。
//
// 三条硬约束（§11.1.3），本包只负责其中两条，第三条在 room 层：
//  1. 写盘不能出现在 tick goroutine 内 —— Report 非阻塞，内部投有界
//     channel；满了丢并计数（丢一局战绩好过卡死整局）。
//  2. 房间关闭前必须 flush —— Close 会排空 channel 再刷盘再关文件。
//  3. SQLite 单写者 —— 本包不用 SQLite，天然满足；将来换实现时这里
//     是唯一要改的地方。
package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"koukoukongjian/internal/game/world"
)

// 默认值。QueueSize 取 64：一局只产生一条记录，64 足够吸收几个房间
// 同时结束的尖峰；再大只是给内存里的坏数据更多容身之处。
const (
	defaultQueueSize = 64
	defaultBufSize   = 64 << 10
)

// WriterOptions 是 NewWriter 的参数。
type WriterOptions struct {
	// Path 是 JSONL 文件路径。空字符串表示不落盘（no-op writer）。
	Path string
	// QueueSize 是内部 channel 容量，0 取默认 64。
	QueueSize int
	// NoWorker 为 true 时不启动消费 goroutine（仅供测试"队列满时丢弃"）。
	NoWorker bool
	// OnError 在写盘出错时被调用（可为 nil）。写盘失败不中断进程：
	// 磁盘满、权限变更都是运维问题，不该让对局跟着停。
	OnError func(error)
}

// Writer 是并发安全的 JSONL 追加写入器。
type Writer struct {
	opts WriterOptions

	queued  chan world.MatchResult
	dropped atomic.Uint64

	// closeOnce 保证 Close 只走一次（判据与 shutdown 的 sync.Once 同款：
	// wg.Wait 与 wg.Add 并发是未定义行为）。
	closeOnce sync.Once
	closed    chan struct{}
	// workerDone 在消费 goroutine 退出后关闭，Close 等它。
	workerDone chan struct{}

	file *os.File
	buf  *bufio.Writer
	// mu 只保护 file/buf 的写入与关闭，不保护 channel。
	mu sync.Mutex
	// err 记录第一次写盘错误（Close 返回它）。
	err error
}

// NewWriter 创建写入器并启动消费 goroutine。
//
// Path 为空时返回一个可用的 no-op writer 而不是 nil：调用方（room 或
// cmd/server）不必到处判空，而"没配置路径"是最常见的情形（本地跑 bot）。
func NewWriter(opts WriterOptions) (*Writer, error) {
	if opts.QueueSize <= 0 {
		opts.QueueSize = defaultQueueSize
	}
	w := &Writer{
		opts:       opts,
		queued:     make(chan world.MatchResult, opts.QueueSize),
		closed:     make(chan struct{}),
		workerDone: make(chan struct{}),
	}

	if opts.Path == "" {
		close(w.workerDone)
		return w, nil
	}

	// 父目录不存在时自动创建：运维传 data/matches.jsonl 时 data/ 通常
	// 还不存在，报错退出会让整个服务器起不来，而这只是日志功能。
	if dir := filepath.Dir(opts.Path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: 创建目录 %s 失败: %w", dir, err)
		}
	}
	// O_APPEND：多进程同时写（多个房间共用一个文件）时靠内核保证每次
	// write 是原子的追加，不需要跨进程锁。
	f, err := os.OpenFile(opts.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("store: 打开 %s 失败: %w", opts.Path, err)
	}
	w.file = f
	w.buf = bufio.NewWriterSize(f, defaultBufSize)

	if opts.NoWorker {
		close(w.workerDone)
		return w, nil
	}
	go w.loop()
	return w, nil
}

// Report 投递一局结果。绝阻塞（§11.1.3 硬约束第 1 条）。
//
// NoWorker 不在这里短路：它的语义只是"不启动消费 goroutine"（供测试
// 造出"队列没人取"的场景），记录仍然进队列。若在这里提前返回，"队列
// 满时丢弃"这条路径就永远走不到——变异测试会证明它是假绿（第一版就是
// 这么写的：把 select 改成阻塞发送后测试仍然全绿）。只有 Path 为空
// （no-op writer）才真正不投递。
func (w *Writer) Report(rec world.MatchResult) {
	if w == nil || w.opts.Path == "" {
		return
	}
	select {
	case w.queued <- rec:
	case <-w.closed:
		w.dropped.Add(1)
	default:
		// 队列满：写盘侧挂了或磁盘卡住。丢弃并计数——丢一局战绩
		// 好过让 tick 循环阻塞在 channel 上。
		w.dropped.Add(1)
	}
}

// Dropped 返回被丢弃的记录数，供指标观测。
func (w *Writer) Dropped() uint64 {
	if w == nil {
		return 0
	}
	return w.dropped.Load()
}

// Close 排空队列、刷盘、关文件。可重复调用。
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	var err error
	w.closeOnce.Do(func() {
		close(w.closed)
		// 等消费 goroutine 把剩余记录写完再动文件。
		<-w.workerDone
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.buf != nil {
			if ferr := w.buf.Flush(); ferr != nil {
				err = fmt.Errorf("store: 刷盘失败: %w", ferr)
			}
		}
		if w.file != nil {
			if cerr := w.file.Close(); cerr != nil && err == nil {
				err = fmt.Errorf("store: 关文件失败: %w", cerr)
			}
		}
		if w.err != nil && err == nil {
			err = w.err
		}
	})
	return err
}

// loop 是唯一的消费者（单写者，§11.1.3 硬约束第 3 条的等价物）。
func (w *Writer) loop() {
	defer close(w.workerDone)
	for {
		select {
		case rec := <-w.queued:
			w.writeOne(rec)
		case <-w.closed:
			// 排空 channel 后再退出：Close 已经关闭 closed，但队列里
			// 可能还有记录——它们是这一批最后一局的结果（往往最重要）。
			for {
				select {
				case rec := <-w.queued:
					w.writeOne(rec)
				default:
					return
				}
			}
		}
	}
}

// writeOne 写一行。出错只记录并回调，不 panic——写盘失败不该让对局停。
func (w *Writer) writeOne(rec world.MatchResult) {
	line, err := json.Marshal(rec)
	if err != nil {
		// 结构体里的全是基础类型与字符串，Marshal 失败意味着类型被改了
		// （比如塞进了 chan）。记录下来而不是静默吞掉。
		w.recordErr(fmt.Errorf("store: 序列化失败: %w", err))
		return
	}
	// JSONL 的 L 就是换行：json.Marshal 不产生行分隔符，必须自己补。
	// 少了这一行，两条记录会粘成一整行——用 Scanner 按行读只能读到第
	// 一条，看起来像"后面那局丢了"，实际是两局写成了一行。
	line = append(line, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf == nil {
		return // 已被 Close
	}
	if _, werr := w.buf.Write(line); werr != nil {
		w.recordErr(fmt.Errorf("store: 写入失败: %w", werr))
		return
	}
	// 逐行 flush：一局一条记录，写入频率是"每局一次"（局级），不是
	// tick 级。逐行刷盘换来的是"进程被 kill -9 也最多丢正在写的那一行"，
	// 而代价在一次事务里完全不可见。
	if ferr := w.buf.Flush(); ferr != nil {
		w.recordErr(fmt.Errorf("store: 刷盘失败: %w", ferr))
		return
	}
	if serr := w.file.Sync(); serr != nil {
		// Sync 失败通常意味着磁盘问题；仍然记录但不视为致命。
		w.recordErr(fmt.Errorf("store: fsync 失败: %w", serr))
	}
}

func (w *Writer) recordErr(err error) {
	if err == nil {
		return
	}
	w.mu.Lock()
	if w.err == nil {
		w.err = err
	}
	w.mu.Unlock()
	if w.opts.OnError != nil {
		w.opts.OnError(err)
	}
}

// ErrClosed 表示写入器已关闭。
//
// 目前 Report 对已关闭的 writer 是静默丢弃（不返回错误），这个哨兵
// 留给将来需要区分"丢弃原因"的调用方。
var ErrClosed = errors.New("store: writer 已关闭")
