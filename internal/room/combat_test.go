package room

import (
	"context"
	"math"
	"testing"
	"time"

	"koukoukongjian/internal/game/world"
	"koukoukongjian/internal/proto"
)

// waitSnapshot 反复从 sink 取快照，直到 cond 成立或超时。
//
// 全部走快照而不是直接读 r.World：房间跑起来之后 World 归 tick goroutine
// 独占（AGENTS.md §2.5），测试 goroutine 直接读它是数据竞争，-race 会报，
// 而"测试挂了但代码是对的"这种噪音最消耗人。走快照顺带验证了"状态真的
// 发到了客户端"这一环，比读内存更接近真实。
//
// 不用固定 time.Sleep 等：tick 循环 50ms 一次，机器快慢不同，睡死一个
// 数值必然在慢机器上偶发失败。
func waitSnapshot(t *testing.T, sink chan []byte, timeout time.Duration, what string, cond func(*proto.Snapshot) bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("等待超时：%s", what)
		}
		select {
		case data, ok := <-sink:
			if !ok {
				t.Fatalf("sink 已关闭，等待 %s 失败", what)
			}
			if cond(decodeSnapshot(t, data)) {
				return
			}
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
}

// selfAim 从快照里读自己的朝向，弧度。读不到时返回 NaN。
func selfAim(s *proto.Snapshot) float64 {
	if s == nil || s.Self == nil {
		return math.NaN()
	}
	return proto.DequantizeAim(s.Self.Aim)
}

func TestInputCarriesAimIntent(t *testing.T) {
	r, _ := startRoom(t, "aim-intent")
	res, sink := joinPlayer(t, r, "p1")

	// 给方向：HasAim=true，Aim 落到实体上，再原样发到快照里。
	r.SendInput(res.Slot, InputCommand{
		Seq: 1, Move: world.V(1, 0), Aim: 1.2, HasAim: true,
	})

	waitSnapshot(t, sink, 2*time.Second, "快照里的朝向变成 1.2", func(s *proto.Snapshot) bool {
		got := selfAim(s)
		return !math.IsNaN(got) && math.Abs(got-1.2) < 0.01
	})
}

func TestInputWithoutAimKeepsPrevious(t *testing.T) {
	// 没给方向时不能把 AimDir 冲成 0——那样自动瞄准永远看不到
	// "玩家其实没在瞄准"这个状态，因为早就被 0 覆盖了。
	r, _ := startRoom(t, "aim-keep")
	res, sink := joinPlayer(t, r, "p1")

	// 先建立一个明确的朝向。
	r.SendInput(res.Slot, InputCommand{Seq: 1, Aim: 2.0, HasAim: true})
	waitSnapshot(t, sink, 2*time.Second, "初始朝向 2.0 到位", func(s *proto.Snapshot) bool {
		got := selfAim(s)
		return !math.IsNaN(got) && math.Abs(got-2.0) < 0.01
	})

	// 再发一条不给方向的输入，用 AckedInputTick 确认它被处理了。
	r.SendInput(res.Slot, InputCommand{Seq: 2, Move: world.V(1, 0), HasAim: false})
	var lastAim float64
	waitSnapshot(t, sink, 2*time.Second, "第二条输入被确认", func(s *proto.Snapshot) bool {
		if s.AckedInputTick != 2 {
			return false
		}
		lastAim = selfAim(s)
		return true
	})

	if math.Abs(lastAim-2.0) > 0.01 {
		t.Fatalf("没给方向时朝向变成 %v，期望保持 2.0", lastAim)
	}
}

// startRoomWith 与 startRoom 相同，但在 tick 循环启动之前先调 setup。
//
// 存在的理由：想往 World 里摆静止靶必须在 Run 之前。房间跑起来后 World
// 归 tick goroutine 独占（AGENTS.md §2.5），从测试 goroutine 写它是数据
// 竞争。-race 会报，而那个报告和"摆靶代码对不对"毫无关系。
//
// startRoom 改成转调这个函数（见 Step 3a），避免同一个房间启动流程
// 存在两份实现——两份的那一天起，"给房间加一个启动参数"就只会改到一份。
func startRoomWith(t *testing.T, cfgName string, setup func(*Room)) (*Room, context.CancelFunc) {
	t.Helper()
	return startRoomWithOpts(t, cfgName, Options{ID: cfgName, Seed: 7}, setup)
}

// startRoomWithOpts 是 startRoomWith 的全参形态：波次开关是 Options 级的
// （New 里与 DummyTargets 互斥裁定，room.go:160 waves := opts.WavesEnabled &&
// opts.DummyTargets == 0），配置层变体表达不了它——startRoomWith 的第二参
// 只是快照 ID，不是配置选择器（testConfig() 恒开图）。M3 Task 10 的波次
// 用例从这条路开 WavesEnabled。
func startRoomWithOpts(t *testing.T, id string, opts Options, setup func(*Room)) (*Room, context.CancelFunc) {
	t.Helper()
	r := New(testConfig(), quietLogger(), opts)
	if setup != nil {
		setup(r)
	}

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
			// 用 Errorf 不用 Fatalf：Cleanup 里 Fatalf 会 Goexit，
			// 把注册在它之前的其他 Cleanup 一起跳过，本来能报的失败
			// 就报不出来了（既有 startRoom 用的也是 Errorf）。
			t.Errorf("房间 %s 未在 2 秒内退出", id)
		}
	})
	return r, cancel
}

// spawnTarget 在 World 里摆一只静止靶并返回它的 ID。
// 只能在 startRoomWith 的 setup 回调里调用（Run 之前）。
func spawnTarget(r *Room, pos world.Vec2, hp float64) world.EntityID {
	id, m := r.World.Monsters.Spawn()
	m.HP = hp
	m.MaxHP = hp
	m.Radius = 0.4
	m.Pos = pos
	return id
}

// entityRef 是快照里实体的唯一标识。
//
// 必须带 Kind：玩家池与怪物池各自从索引 1 开始编号（world.NewPool 保留
// 0 号槽给 InvalidID），所以同一个 EntityId 可以同时指向一个玩家和一只
// 怪物。只按 EntityId 建表，后写入的会覆盖先写入的——测试会静默地拿
// 玩家的 delta 去当怪物断言，表现为"血量为 0，永远等不到下降"。
//
// internal/snapshot 包里的 seenKey 就是同一套教训（见 M1 缺陷记录里的
// "隐形的怪"）。
type entityRef struct {
	kind proto.EntityKind
	id   uint32
}

// snapshotEntities 把这一帧快照里出现过的实体收进一张表。
//
// 全量帧把全部实体放 Adds，增量帧只把变化的放 Updates，所以两处都要看，
// 且 Updates 覆盖 Adds（同一实体可能先在全量里、后又变化）。
func snapshotEntities(s *proto.Snapshot) map[entityRef]*proto.EntityDelta {
	out := make(map[entityRef]*proto.EntityDelta, len(s.Adds)+len(s.Updates))
	for _, e := range s.Adds {
		out[entityRef{e.Kind, e.EntityId}] = e
	}
	for _, e := range s.Updates {
		out[entityRef{e.Kind, e.EntityId}] = e
	}
	return out
}

// bulletCount 数这一帧快照里有多少颗子弹。
func bulletCount(s *proto.Snapshot) int {
	n := 0
	for ref := range snapshotEntities(s) {
		if ref.kind == proto.EntityKind_ENTITY_KIND_BULLET {
			n++
		}
	}
	return n
}

// monsterHP 从快照里查某只怪的血量。这一帧没下发过它时 ok=false。
func monsterHP(s *proto.Snapshot, id uint32) (hp float64, ok bool) {
	e, found := snapshotEntities(s)[entityRef{proto.EntityKind_ENTITY_KIND_MONSTER, id}]
	if !found {
		return 0, false
	}
	return float64(e.Hp), true
}

// removedContains 判断这一帧的 Removes 里有没有某个实体。
//
// Removes 里只有裸 ID，没有 Kind（proto 定义如此），所以如果玩家与怪物
// 恰好撞号，玩家被移除时也会命中。测试里没有人会退出，因此不受影响；
// 真要让这条断言无歧义，就得先让怪物与玩家的索引不重叠——那属于 M3
// 把两个池的 ID 空间彻底隔开的事，不在 M2 范围。
func removedContains(s *proto.Snapshot, id uint32) bool {
	for _, r := range s.Removes {
		if r == id {
			return true
		}
	}
	return false
}

func TestRoomFireSpawnsBullet(t *testing.T) {
	r, _ := startRoomWith(t, "fire", nil)
	res, sink := joinPlayer(t, r, "shooter")

	r.SendInput(res.Slot, InputCommand{
		Seq: 1, Aim: 0, HasAim: true, Buttons: ButtonAttack,
	})

	waitSnapshot(t, sink, 2*time.Second, "快照里出现子弹", func(s *proto.Snapshot) bool {
		return bulletCount(s) > 0
	})
}

func TestRoomBulletDamagesMonster(t *testing.T) {
	const hp = 50.0

	var mID world.EntityID
	r, _ := startRoomWith(t, "damage", func(r *Room) {
		// 摆在座位 0 出生点正右方 3 m。Aim=0 是弧度制的 +X 方向，
		// 射出的子弹正好沿这条线飞。
		p := r.spawnPoint(0)
		mID = spawnTarget(r, world.V(p.X+3, p.Y), hp)
	})

	res, sink := joinPlayer(t, r, "shooter")
	r.SendInput(res.Slot, InputCommand{
		Seq: 1, Aim: 0, HasAim: true, Buttons: ButtonAttack,
	})

	waitSnapshot(t, sink, 3*time.Second, "怪物血量下降", func(s *proto.Snapshot) bool {
		got, ok := monsterHP(s, uint32(mID))
		return ok && got < hp
	})
}

func TestRoomReapsKilledMonster(t *testing.T) {
	var mID world.EntityID
	r, _ := startRoomWith(t, "reap", func(r *Room) {
		p := r.spawnPoint(0)
		mID = spawnTarget(r, world.V(p.X+3, p.Y), 5) // 一发就死
	})

	res, sink := joinPlayer(t, r, "shooter")
	r.SendInput(res.Slot, InputCommand{
		Seq: 1, Aim: 0, HasAim: true, Buttons: ButtonAttack,
	})

	// 判据用 Removes 而不是"查不到血量"：查不到也可能只是这一帧没有
	// 变化所以没下发，那会把"还活着但没被打"误判成"已回收"。
	waitSnapshot(t, sink, 3*time.Second, "怪物出现在 Removes 里", func(s *proto.Snapshot) bool {
		return removedContains(s, uint32(mID))
	})
}

func TestRoomAutoAimWhenNoDirection(t *testing.T) {
	const hp = 50.0

	var mID world.EntityID
	r, _ := startRoomWith(t, "autoaim", func(r *Room) {
		// 怪物在玩家正上方 3 m。玩家初始朝向是 +X（0 弧度），
		// 所以只有服务端替他把朝向转过来才可能命中。
		p := r.spawnPoint(0)
		mID = spawnTarget(r, world.V(p.X, p.Y+3), hp)
	})

	res, sink := joinPlayer(t, r, "shooter")
	// HasAim=false 且不给方向 → 服务端自动朝最近怪开火
	r.SendInput(res.Slot, InputCommand{Seq: 1, HasAim: false, Buttons: ButtonAttack})

	waitSnapshot(t, sink, 3*time.Second, "自动瞄准命中", func(s *proto.Snapshot) bool {
		got, ok := monsterHP(s, uint32(mID))
		return ok && got < hp
	})
}

// TestRoomSnapshotCarriesEvents 验证 M1 预留的 Snapshot.Events 字段：
// 击杀事件随快照下发，A=击杀者（玩家实体 id）、B=被击杀者（怪 id）。
func TestRoomSnapshotCarriesEvents(t *testing.T) {
	var mID world.EntityID
	r, _ := startRoomWith(t, "events", func(r *Room) {
		// 10 HP：一发步枪伤害正好击杀，击杀事件由 ResolveDamage 的 IsKill 产出。
		p := r.spawnPoint(0)
		mID = spawnTarget(r, world.V(p.X+3, p.Y), 10)
	})

	res, sink := joinPlayer(t, r, "shooter")
	r.SendInput(res.Slot, InputCommand{
		Seq: 1, Aim: 0, HasAim: true, Buttons: ButtonAttack,
	})

	var kill *proto.GameEvent
	waitSnapshot(t, sink, 3*time.Second, "击杀事件", func(s *proto.Snapshot) bool {
		for _, ev := range s.Events {
			if ev.Kind == proto.GameEventKind_GAME_EVENT_KIND_KILL {
				kill = ev
				return true
			}
		}
		return false
	})
	if kill == nil {
		t.Fatal("未收到击杀事件")
	}
	if kill.A != uint32(res.EntityID) || kill.B != uint32(mID) {
		t.Errorf("击杀事件 a/b = %d/%d, 期望 玩家 %d / 怪 %d",
			kill.A, kill.B, uint32(res.EntityID), uint32(mID))
	}
}

// TestRoomSnapshotCarriesFogDelta 验证 M1 预留的 Snapshot.FogDelta 字段：
// HasMap 房间里玩家加入后首 tick 必有新点亮格（出生圈），随快照下发。
func TestRoomSnapshotCarriesFogDelta(t *testing.T) {
	// startRoomWith 用 testConfig()，默认开图（HubCount=7）→ fog 状态已建。
	r, _ := startRoomWith(t, "fogdelta", nil)
	_, sink := joinPlayer(t, r, "scout")

	waitSnapshot(t, sink, 3*time.Second, "迷雾增量", func(s *proto.Snapshot) bool {
		return len(s.FogDelta) > 0
	})
}

// ——————————————————————————————————————————————————————————————
// M3 Task 10：AI 与波次接线（计划 docs/plans/2026-11-14-m3-world-waves.md
// Task 10）。全部走快照断言，绝不直接读写 r.World——tick goroutine 独占
// （AGENTS.md §2.5），测试 goroutine 碰它就是 -race 红灯。
// ——————————————————————————————————————————————————————————————

// spawnMonAt 在指定位置摆一只怪并返回它的 ID。spawnTarget 的别名：
// 计划 Task 10 用例用它，AI 测试的"摆怪"与"摆静止靶"是同一件事
// （Monsters.Spawn + 摆位摆血），不改 spawnTarget 本体、不加第二个实现。
// 只能在 startRoomWith 的 setup 回调里调用（Run 之前，无竞争）。
func spawnMonAt(r *Room, pos world.Vec2, hp float64) world.EntityID {
	return spawnTarget(r, pos, hp)
}

// monsterCount 数这一帧快照里有多少只怪。
//
// 快照没有独立的"怪物列表"字段——怪物与玩家/子弹共用 Adds/Updates 增量流，
// 按 Kind 过滤（entityRef 带 Kind 的教训见上面 snapshotEntities 的注释）。
// 注意口径：增量流只含"本帧有变化的实体"，全量帧含全部存活实体；怪物被
// AI 推动后每帧都在 Updates 里，计数因此有效。不能拿它当精确存活数用，
// 波次用例只断言 ≥ 门槛，够用。
func monsterCount(s *proto.Snapshot) int {
	n := 0
	for ref := range snapshotEntities(s) {
		if ref.kind == proto.EntityKind_ENTITY_KIND_MONSTER {
			n++
		}
	}
	return n
}

func TestRoomMonsterChasesPlayer(t *testing.T) {
	var mID world.EntityID
	r, _ := startRoomWith(t, "ai", func(r *Room) {
		// 摆怪在出生据点旁：hub.Center.Add(world.V(4.5,0))。计划原文的
		// +6 在 seed 7 这张图上是墙（探针实测：空地圆边缘 +5.0m，cell 86
		// 起 tile=1；FindPath/LineWalkable 对嵌墙点全断）——任务书要求
		// 「别摆进墙」，实测改到空地圆内。距玩家出生点（slot0 +1m 环）
		// 约 3.5m：SightRange=20 内、AttackRange 1.9m 外。
		hub := r.World.Hubs[r.World.SpawnHub]
		mID = spawnMonAt(r, hub.Center.Add(world.V(4.5, 0)), 50)
	})

	_, sink := joinPlayer(t, r, "bait")

	// 怪开始追人 = 它的位置随 tick 变化。快照坐标是量化后的 int32，
	// 位置变化直接比量化值（不反量化也行，量纲一致即可比较）。
	var lastX int32
	moved := false
	waitSnapshot(t, sink, 5*time.Second, "怪物移动", func(s *proto.Snapshot) bool {
		e, ok := snapshotEntities(s)[entityRef{proto.EntityKind_ENTITY_KIND_MONSTER, uint32(mID)}]
		if !ok || e.Pos == nil {
			return false
		}
		if lastX != 0 && e.Pos.X != lastX {
			moved = true
			return true
		}
		lastX = e.Pos.X
		return false
	})
	if !moved {
		t.Error("怪物应朝玩家移动（AI 未接线时它永远静止）")
	}
}

func TestRoomWaveSpawnsAroundHub(t *testing.T) {
	// WavesEnabled=true + DummyTargets=0 → New 落定 r.wavesEnabled=true。
	// 玩家 joinPlayer 后出生在出生据点中心 1 m 环上，半径 5 m 的触发圈
	// 直接命中，无需移动输入——handleJoin 默认出生即进圈。
	r, _ := startRoomWithOpts(t, "waves", Options{ID: "waves", Seed: 7, WavesEnabled: true}, nil)
	_, sink := joinPlayer(t, r, "camper")

	// 波次开启 + 玩家在圈内 → 几秒内快照怪物数 ≥ MonstersPerWave(4)。
	waitSnapshot(t, sink, 5*time.Second, "波次刷怪", func(s *proto.Snapshot) bool {
		return monsterCount(s) >= 4
	})
}
