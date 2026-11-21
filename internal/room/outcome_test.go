package room

import (
	"sync"
	"testing"
	"time"

	"koukoukongjian/internal/game/world"
	"koukoukongjian/internal/proto"
)

// memSink 是测试用的内存 sink，记录房间上报了什么。
//
// 必须带锁：reportMatch 在 tick goroutine 的 shutdown 路径里被调用，而
// 测试在另一个 goroutine 里轮询 reported——裸切片是真实的数据竞争，
// -race 会报。生产实现（store.Writer）本来就在自己的 goroutine 里串行，
// 只有测试这个"同步内存实现"需要自己保证可见性。
type memSink struct {
	mu       sync.Mutex
	reported []world.MatchResult
}

func (m *memSink) Report(rec world.MatchResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reported = append(m.reported, rec)
}

// count 返回已上报条数（加锁读）。
func (m *memSink) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.reported)
}

// at 返回第 i 条上报（加锁读）。越界返回零值。
func (m *memSink) at(i int) world.MatchResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i < 0 || i >= len(m.reported) {
		return world.MatchResult{}
	}
	return m.reported[i]
}

// waitForCondition 轮询等待条件成立。20ms 一档，超时报错。
//
// 与 waitSnapshot 的分工：那个专等快照条件（要排空通道），这个等任意
// 状态（这里等的是"关房流程跑完并上报了"）。
func waitForCondition(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

// TestRoomReportsMatchOnStop 钉住"关房时上报一次结果"。
func TestRoomReportsMatchOnStop(t *testing.T) {
	sink := &memSink{}
	r, cancel := startRoomWithOpts(t, "outcome-stop", Options{ID: "outcome-stop", Seed: 7}, func(r *Room) {
		r.SetResultSink(sink)
	})
	defer cancel()

	// 让 tick 跑几轮，确保结局判定被执行过。
	time.Sleep(100 * time.Millisecond)
	r.Stop()

	waitForCondition(t, 3*time.Second, "关房上报结果", func() bool {
		return sink.count() > 0
	})
	if got := sink.at(0).RoomID; got != "outcome-stop" {
		t.Fatalf("上报 RoomID = %q，期望 outcome-stop", got)
	}
}

// TestRoomReportsSeatNotPoolIndex 钉住 PlayerResult.Slot 是座位号不是池索引。
//
// world 层只能填 id.Index()（拿不到座位），所以必须由 room 反查替换。
// join 两个玩家：第一个座位 0、第二个座位 1，而玩家池索引恰好也是 1 和 2
// ——座位号与池索引在这条用例里必然不同，能真正区分两种实现。
func TestRoomReportsSeatNotPoolIndex(t *testing.T) {
	sink := &memSink{}
	r, cancel := startRoomWithOpts(t, "outcome-seat", Options{ID: "outcome-seat", Seed: 7}, func(r *Room) {
		r.SetResultSink(sink)
	})
	defer cancel()

	joinPlayer(t, r, "a")
	res, _ := joinPlayer(t, r, "b")
	if res.Slot != 1 {
		t.Fatalf("第二个玩家座位 = %d，期望 1（前提失效）", res.Slot)
	}

	time.Sleep(100 * time.Millisecond)
	r.Stop()
	waitForCondition(t, 3*time.Second, "关房上报结果", func() bool {
		return sink.count() > 0
	})

	rec := sink.at(0)
	if len(rec.Players) != 2 {
		t.Fatalf("上报玩家行数 = %d，期望 2", len(rec.Players))
	}
	seen := map[int]bool{}
	for _, p := range rec.Players {
		seen[p.Slot] = true
	}
	if !seen[0] || !seen[1] {
		t.Fatalf("上报座位集合 = %v，期望包含 0 与 1（不是玩家池索引 1、2）", seen)
	}
}

// TestRoomReportsOnlyOnce 钉住 reportMatch 幂等：重复调用只上报一次。
//
// 直接调 reportMatch 而不是只靠重复 Stop：shutdown 在 Run 里每个退出
// 分支各调一次、而 Run 只会返回一次，所以"重复 Stop"根本走不到第二次
// shutdown——用它测幂等是假绿（把 CompareAndSwap 删掉测试仍然全绿，
// 变异验证当场证明）。真正的风险是将来有人在别的路径（比如 ctx 取消后
// 的清理钩子）再调一次 shutdown，那时只有 CAS 兜得住。
//
// Run 已返回之后再调是安全的：tick goroutine 已经结束，此时没有并发。
func TestRoomReportsOnlyOnce(t *testing.T) {
	sink := &memSink{}
	r, cancel := startRoomWithOpts(t, "outcome-once", Options{ID: "outcome-once", Seed: 7}, func(r *Room) {
		r.SetResultSink(sink)
	})
	defer cancel()

	time.Sleep(50 * time.Millisecond)
	r.Stop()
	r.Stop() // 第二次必须无副作用
	waitForCondition(t, 3*time.Second, "首次上报", func() bool {
		return sink.count() == 1
	})

	// Run 已返回，此刻直接调 reportMatch 不会与 tick goroutine 竞争。
	r.reportMatch()
	r.reportMatch()

	if n := sink.count(); n != 1 {
		t.Fatalf("上报次数 = %d，期望 1（reportMatch 必须幂等）", n)
	}
}

// TestRoomOutcomeWipedWhenAllGhost 钉住团灭被识别成 wiped 而不是 unknown。
//
// 在 setup 里直接把玩家摆成鬼魂：此刻 Run 还没启动，World 没有被 tick
// goroutine 接管，直接写是安全的（startRoomWithOpts 的 setup 契约）。
// 走公开输入路径做不到"全队变鬼"——那需要把玩家打死，而血量在 tick 里。
func TestRoomOutcomeWipedWhenAllGhost(t *testing.T) {
	sink := &memSink{}
	r, cancel := startRoomWithOpts(t, "outcome-wipe", Options{ID: "outcome-wipe", Seed: 7}, func(r *Room) {
		r.SetResultSink(sink)
		if _, p := r.World.Players.Spawn(); p != nil {
			p.State = world.PlayerGhost
			p.HP = 0
		}
	})
	defer cancel()

	time.Sleep(150 * time.Millisecond)
	r.Stop()
	waitForCondition(t, 3*time.Second, "关房上报结果", func() bool {
		return sink.count() > 0
	})
	if got := sink.at(0).Outcome; got != world.OutcomeWiped {
		t.Fatalf("全队鬼魂 Outcome = %v，期望 wiped", got)
	}
}

// TestRoomOutcomeUnknownWithAlivePlayer 钉住有活人就既不 wiped 也不 cleared。
func TestRoomOutcomeUnknownWithAlivePlayer(t *testing.T) {
	sink := &memSink{}
	r, cancel := startRoomWithOpts(t, "outcome-alive", Options{ID: "outcome-alive", Seed: 7}, func(r *Room) {
		r.SetResultSink(sink)
	})
	defer cancel()

	joinPlayer(t, r, "victim")
	time.Sleep(100 * time.Millisecond)
	r.Stop()
	waitForCondition(t, 3*time.Second, "关房上报结果", func() bool {
		return sink.count() > 0
	})
	if got := sink.at(0).Outcome; got != world.OutcomeUnknown {
		t.Fatalf("有活人且据点未清 Outcome = %v，期望 unknown", got)
	}
}

// TestResultSinkNilIsSafe 钉住没设 sink 时不 panic。
func TestResultSinkNilIsSafe(t *testing.T) {
	_, cancel := startRoomWithOpts(t, "outcome-nil", Options{ID: "outcome-nil", Seed: 7}, nil)
	time.Sleep(50 * time.Millisecond)
	cancel() // ctx 取消走 shutdown 分支，此时 sink 为 nil
	time.Sleep(100 * time.Millisecond)
	// 无 panic 即通过。
}

// TestRoomBroadcastsMatchResult 钉住结局那一刻通过可靠通道下发一次结算。
//
// 为什么走可靠通道而不是快照：Snapshot 的字段号 1-10 已满，而结算消息
// 不是状态同步的一部分（客户端拿到一次就够），塞进每帧等于让不可靠通道
// 承担"必达"语义。
//
// 走真实死亡路径而不是 setup 里手摆鬼魂：手摆的那个玩家在 joinPlayer
// 之前就存在，而 join 会 spawn 一个活人，于是"全队鬼魂"永不成立。
// 摆一只一击必杀的怪在出生点，玩家被打死后就是唯一的（鬼魂）玩家。
func TestRoomBroadcastsMatchResult(t *testing.T) {
	sink := &memSink{}
	r, cancel := startRoomWith(t, "outcome-cast", func(r *Room) {
		r.SetResultSink(sink)
		// 攻击力拉到一击必杀：默认 10 点打 100 血要十次，测试会慢十秒。
		r.World.Mon.AttackDamage = 500
		spawnTarget(r, r.World.Hubs[r.World.SpawnHub].Center, 100000)
	})
	defer cancel()

	_, snapSink := joinPlayer(t, r, "victim")
	env := waitReliable(t, r, snapSink, 25*time.Second, "结算下发", func(e *proto.Envelope) bool {
		return e.GetMatchResult() != nil
	})
	mr := env.GetMatchResult()
	if mr.Outcome != proto.MatchOutcome_MATCH_OUTCOME_WIPED {
		t.Fatalf("下发 Outcome = %v，期望 WIPED", mr.Outcome)
	}
	if mr.RoomId != "outcome-cast" {
		t.Fatalf("下发 RoomId = %q，期望 outcome-cast", mr.RoomId)
	}
	if len(mr.Players) != 1 {
		t.Fatalf("下发玩家行数 = %d，期望 1", len(mr.Players))
	}
	if got := mr.Players[0].Slot; got != 0 {
		t.Fatalf("下发座位 = %d，期望 0（座位号不是池索引）", got)
	}
	if mr.Players[0].Deaths != 1 {
		t.Fatalf("下发 Deaths = %d，期望 1", mr.Players[0].Deaths)
	}
}
