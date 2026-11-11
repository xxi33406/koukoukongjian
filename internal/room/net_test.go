package room

import (
	"context"
	"testing"
	"time"

	"koukoukongjian/internal/game/world"
	"koukoukongjian/internal/proto"
)

// startRoom 起一个跑着 tick 循环的房间，返回它和清理函数。
//
// 用真实 tick 循环而不是直接调 r.tick()：网络入口的正确性有一半在于
// "请求能被 tick 循环及时消费"，绕开循环测不出这一半。
func startRoom(t *testing.T, cfgName string) (*Room, context.CancelFunc) {
	t.Helper()
	cfg := testConfig()
	r := New(cfg, quietLogger(), Options{ID: cfgName, Seed: 7})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("房间没在 2 秒内退出")
		}
	})
	return r, cancel
}

// joinPlayer 加入一个玩家，返回结果和 sink 读端。
func joinPlayer(t *testing.T, r *Room, name string) (JoinResult, chan []byte) {
	t.Helper()
	// sink 必须有缓冲：tick 循环非阻塞写，无缓冲会让每个快照都被丢掉。
	sink := make(chan []byte, 64)
	res, err := r.Join(context.Background(), name, sink, time.Second)
	if err != nil {
		t.Fatalf("加入失败：%v", err)
	}
	return res, sink
}

// waitPeers 等到房间里有 n 个玩家。
func waitPeers(t *testing.T, r *Room, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.PeerCount() == n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待 %d 个玩家超时，当前 %d", n, r.PeerCount())
}

// nextSnapshot 从 sink 取一份快照并解码。
func nextSnapshot(t *testing.T, sink chan []byte, timeout time.Duration) *proto.Snapshot {
	t.Helper()
	select {
	case data, ok := <-sink:
		if !ok {
			t.Fatal("sink 已被关闭")
		}
		return decodeSnapshot(t, data)
	case <-time.After(timeout):
		t.Fatal("等待快照超时")
		return nil
	}
}

// decodeSnapshot 把 sink 里的一包字节解成快照。
//
// sink 里是包了 Envelope 的快照（快照通道上还有别的消息类型，裸消息
// 无法自识别，见 internal/room/net.go 的 broadcast 注释）。
func decodeSnapshot(t *testing.T, data []byte) *proto.Snapshot {
	t.Helper()
	var env proto.Envelope
	if err := proto.Unmarshal(data, &env); err != nil {
		t.Fatalf("快照解码失败：%v", err)
	}
	snap, ok := env.Body.(*proto.Envelope_Snapshot)
	if !ok || snap.Snapshot == nil {
		t.Fatalf("期望快照消息，实际是 %T", env.Body)
	}
	return snap.Snapshot
}

func TestJoinReturnsSlotAndEntity(t *testing.T) {
	r, _ := startRoom(t, "join")
	res, sink := joinPlayer(t, r, "alice")
	defer r.Leave(res.Slot)

	if res.Slot != 0 {
		t.Fatalf("第一个玩家应坐 0 号位，得到 %d", res.Slot)
	}
	if !res.EntityID.IsValid() {
		t.Fatal("实体 ID 应有效")
	}
	if res.Seed != 7 {
		t.Fatalf("种子应为 7，得到 %d", res.Seed)
	}
	if sink == nil {
		t.Fatal("sink 不该是 nil")
	}
}

func TestJoinAssignsDistinctSlots(t *testing.T) {
	r, _ := startRoom(t, "slots")

	seen := map[int]bool{}
	var results []JoinResult
	for i := 0; i < MaxSlots; i++ {
		res, _ := joinPlayer(t, r, "p")
		if seen[res.Slot] {
			t.Fatalf("座位 %d 被分配了两次", res.Slot)
		}
		seen[res.Slot] = true
		results = append(results, res)
	}
	defer func() {
		for _, res := range results {
			r.Leave(res.Slot)
		}
	}()

	// 四个玩家的实体 ID 必须互不相同，否则客户端分不清谁是谁。
	ids := map[world.EntityID]bool{}
	for _, res := range results {
		if ids[res.EntityID] {
			t.Fatalf("实体 ID %s 重复", res.EntityID)
		}
		ids[res.EntityID] = true
	}
}

func TestJoinRejectsWhenFull(t *testing.T) {
	r, _ := startRoom(t, "full")

	var results []JoinResult
	for i := 0; i < MaxSlots; i++ {
		res, _ := joinPlayer(t, r, "p")
		results = append(results, res)
	}
	defer func() {
		for _, res := range results {
			r.Leave(res.Slot)
		}
	}()

	sink := make(chan []byte, 1)
	_, err := r.Join(context.Background(), "extra", sink, time.Second)
	if err != ErrRoomFull {
		t.Fatalf("第五个玩家应被拒绝并返回 ErrRoomFull，得到 %v", err)
	}
}

func TestJoinAfterStopReturnsError(t *testing.T) {
	r, cancel := startRoom(t, "stopped")
	cancel()
	// 等 Run 真的返回，否则 stopped 还没置位。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !r.stopped.Load() {
		time.Sleep(2 * time.Millisecond)
	}

	sink := make(chan []byte, 1)
	if _, err := r.Join(context.Background(), "late", sink, time.Second); err == nil {
		t.Fatal("房间停止后加入应失败")
	}
}

func TestJoinContextCancel(t *testing.T) {
	// 房间不跑 tick 循环，加入请求会一直排在队列里。
	cfg := testConfig()
	r := New(cfg, quietLogger(), Options{ID: "no-run", Seed: 1})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sink := make(chan []byte, 1)
	if _, err := r.Join(ctx, "x", sink, time.Second); err == nil {
		t.Fatal("ctx 取消后加入应失败")
	}
}

func TestJoinTimeout(t *testing.T) {
	// 房间不跑，join 队列能放进去但没人处理，必须靠 timeout 返回。
	cfg := testConfig()
	r := New(cfg, quietLogger(), Options{ID: "timeout", Seed: 1})

	sink := make(chan []byte, 1)
	start := time.Now()
	_, err := r.Join(context.Background(), "x", sink, 30*time.Millisecond)
	if err != ErrJoinTimeout {
		t.Fatalf("应返回 ErrJoinTimeout，得到 %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("超时应该很快返回，实际用了 %v", elapsed)
	}
}

func TestJoinSpawnsPlayerAtDistinctPositions(t *testing.T) {
	r, _ := startRoom(t, "spawn")

	res0, sink0 := joinPlayer(t, r, "a")
	res1, sink1 := joinPlayer(t, r, "b")
	defer func() {
		r.Leave(res0.Slot)
		r.Leave(res1.Slot)
	}()

	// 各自收一份快照，读 self 的位置。
	snap0 := nextSnapshot(t, sink0, 2*time.Second)
	snap1 := nextSnapshot(t, sink1, 2*time.Second)

	if snap0.Self == nil || snap1.Self == nil {
		t.Fatal("快照应包含 self")
	}
	// 两个玩家不能生成在同一个点上（否则会互相卡住）。
	if snap0.Self.Pos.X == snap1.Self.Pos.X && snap0.Self.Pos.Y == snap1.Self.Pos.Y {
		t.Fatalf("两个玩家出生点重叠：%+v", snap0.Self.Pos)
	}
	// 出生点必须在地图范围内。
	w := r.World.Terrain.WorldWidth()
	h := r.World.Terrain.WorldHeight()
	px := proto.DequantizePos(snap0.Self.Pos.X)
	py := proto.DequantizePos(snap0.Self.Pos.Y)
	if px < 0 || px > w || py < 0 || py > h {
		t.Fatalf("出生点 %.2f,%.2f 超出地图 %.0f×%.0f", px, py, w, h)
	}
}

func TestInputMovesPlayer(t *testing.T) {
	r, _ := startRoom(t, "input")
	res, sink := joinPlayer(t, r, "mover")
	defer r.Leave(res.Slot)

	first := nextSnapshot(t, sink, 2*time.Second)
	startX := proto.DequantizePos(first.Self.Pos.X)

	// 一路向 +X 推满。
	for i := 0; i < 5; i++ {
		r.SendInput(res.Slot, InputCommand{
			Seq:  uint16(i + 1),
			Move: world.V(1, 0),
		})
		time.Sleep(10 * time.Millisecond)
	}

	// 等到位置真的变了。
	deadline := time.Now().Add(2 * time.Second)
	var moved bool
	for time.Now().Before(deadline) {
		select {
		case data, ok := <-sink:
			if !ok {
				t.Fatal("sink 被关闭")
			}
			snap := decodeSnapshot(t, data)
			if snap.Self == nil {
				continue
			}
			if proto.DequantizePos(snap.Self.Pos.X) > startX+0.1 {
				moved = true
				deadline = time.Time{}
			}
		case <-time.After(20 * time.Millisecond):
		}
		if moved {
			break
		}
	}
	if !moved {
		t.Fatalf("玩家没有向右移动，起始 x=%.3f", startX)
	}
}

func TestInputAcksSeq(t *testing.T) {
	r, _ := startRoom(t, "ack")
	res, sink := joinPlayer(t, r, "acker")
	defer r.Leave(res.Slot)

	const seq = 42
	r.SendInput(res.Slot, InputCommand{Seq: seq, Move: world.V(1, 0)})

	// 服务器处理输入后，快照里的 acked_input_tick 应回显这个序号。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap := nextSnapshot(t, sink, 2*time.Second)
		if snap.AckedInputTick == seq {
			return
		}
	}
	t.Fatalf("快照从未回显输入序号 %d", seq)
}

func TestLeaveFreesSlotAndSink(t *testing.T) {
	r, _ := startRoom(t, "leave")
	res, sink := joinPlayer(t, r, "leaver")
	waitPeers(t, r, 1)

	r.Leave(res.Slot)
	waitPeers(t, r, 0)

	// sink 必须被关闭，否则网络层的写 goroutine 永远退不出去。
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-sink:
			if !ok {
				// 关掉了。
				goto slotFreed
			}
		case <-deadline:
			t.Fatal("离开后 sink 没有被关闭")
		}
	}

slotFreed:
	// 座位被回收，新玩家能坐回去。
	res2, _ := joinPlayer(t, r, "next")
	defer r.Leave(res2.Slot)
	if res2.Slot != res.Slot {
		t.Fatalf("新玩家应坐上 %d 号位，得到 %d", res.Slot, res2.Slot)
	}
	// 但实体必须是全新世代，不能复用旧 ID（否则旧客户端会认错人）。
	if res2.EntityID == res.EntityID {
		t.Fatalf("复用的座位必须给新实体 ID，两边都是 %s", res.EntityID)
	}
}

func TestLeaveUnknownSlotIsHarmless(t *testing.T) {
	r, _ := startRoom(t, "leave-bad")
	// 不该 panic。
	r.Leave(99)
	r.Leave(-1)
	r.Leave(0)

	// 房间还活着。
	if _, sink := joinPlayer(t, r, "alive"); sink != nil {
		return
	}
}

func TestSendInputAfterStopReturnsFalse(t *testing.T) {
	r, cancel := startRoom(t, "input-stopped")
	res, _ := joinPlayer(t, r, "x")
	_ = res
	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !r.stopped.Load() {
		time.Sleep(2 * time.Millisecond)
	}
	if r.SendInput(0, InputCommand{Move: world.V(1, 0)}) {
		t.Fatal("房间停止后 SendInput 应返回 false")
	}
}

func TestBroadcastRemovesDepartedPlayer(t *testing.T) {
	r, _ := startRoom(t, "removes")

	// 旁观者 + 即将离开的人。
	observer, obsSink := joinPlayer(t, r, "observer")
	defer r.Leave(observer.Slot)
	victim, _ := joinPlayer(t, r, "victim")

	// 等旁观者第一次看到 victim。
	seenVictim := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !seenVictim {
		snap := nextSnapshot(t, obsSink, 2*time.Second)
		for _, d := range snap.Adds {
			if d.EntityId == uint32(victim.EntityID) {
				seenVictim = true
			}
		}
	}
	if !seenVictim {
		t.Fatal("旁观者从未在 adds 里看到 victim")
	}

	r.Leave(victim.Slot)
	waitPeers(t, r, 1)

	// victim 必须出现在某个快照的 removes 里，否则客户端留着幽灵。
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap := nextSnapshot(t, obsSink, 2*time.Second)
		for _, id := range snap.Removes {
			if id == uint32(victim.EntityID) {
				return
			}
		}
	}
	t.Fatalf("victim(%s) 从未出现在 removes 里", victim.EntityID)
}

func TestStatsReportsQueues(t *testing.T) {
	r, _ := startRoom(t, "stats")
	res, _ := joinPlayer(t, r, "x")
	defer r.Leave(res.Slot)

	st := r.Stats()
	if st.Inputs < 0 || st.Joins < 0 {
		t.Fatalf("队列长度不该为负：%+v", st)
	}
}

func TestRoomShutdownClosesAllSinks(t *testing.T) {
	r, _ := startRoom(t, "shutdown")

	sink0 := make(chan []byte, 8)
	sink1 := make(chan []byte, 8)
	if _, err := r.Join(context.Background(), "a", sink0, time.Second); err != nil {
		t.Fatalf("加入失败：%v", err)
	}
	if _, err := r.Join(context.Background(), "b", sink1, time.Second); err != nil {
		t.Fatalf("加入失败：%v", err)
	}
	waitPeers(t, r, 2)

	r.Stop()

	// 两个 sink 都必须关闭，否则两个写 goroutine 泄漏。
	for i, sink := range []chan []byte{sink0, sink1} {
		closed := false
		deadline := time.After(3 * time.Second)
		for !closed {
			select {
			case _, ok := <-sink:
				if !ok {
					closed = true
				}
			case <-deadline:
				t.Fatalf("第 %d 个 sink 没被关闭", i)
			}
		}
	}
}
