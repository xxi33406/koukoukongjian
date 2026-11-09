package snapshot

import (
	"testing"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/game/world"
	"koukoukongjian/internal/proto"
)

// newTestWorld 建一个能装 4 人的空世界。
func newTestWorld(t *testing.T) *world.World {
	t.Helper()
	return world.NewWorld(config.Default(), 42)
}

// spawnPlayer 放一个玩家，返回稳定 ID 与可写指针。
//
// Pool 的槽位从 1 开始（0 号留给 InvalidID），所以第一个玩家拿到
// index=1 而不是 0。测试一律用返回值，不假设槽位编号。
func spawnPlayer(t *testing.T, w *world.World) (world.EntityID, *world.PlayerState) {
	t.Helper()
	id, p := w.Players.Spawn()
	if p == nil {
		t.Fatal("生成玩家失败：池已满")
	}
	p.MaxHP = 100
	p.HP = 100
	return id, p
}

func TestBuildFirstSnapshotIsFull(t *testing.T) {
	w := newTestWorld(t)
	b := NewBuilder()

	snap, st := b.Build(w, world.InvalidID, false)

	if !st.Full {
		t.Fatal("第一次构建必须是全量")
	}
	if snap.ServerTick != w.Tick {
		t.Fatalf("ServerTick = %d，期望 %d", snap.ServerTick, w.Tick)
	}
	if snap.Self != nil {
		t.Fatal("世界里没有玩家，self 应为 nil")
	}
}

func TestBuildSelfGoesToSelfFieldNotAdds(t *testing.T) {
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)
	b := NewBuilder()

	snap, _ := b.Build(w, selfID, false)

	if snap.Self == nil {
		t.Fatal("selfID 对应的玩家存在，self 不应为 nil")
	}
	if snap.Self.EntityId != uint32(selfID) {
		t.Fatalf("self.EntityId = %d，期望 %d", snap.Self.EntityId, uint32(selfID))
	}
	// 自己没有变化时不该重复出现在 adds 里——那是别人的视角。
	for _, d := range snap.Adds {
		if d.EntityId == uint32(selfID) {
			t.Fatal("自己不应出现在 adds 里")
		}
	}
	// 但必须进 seen，否则下一帧会被当成新增再发一次。
	if b.Seen() != 1 {
		t.Fatalf("Seen = %d，期望 1（自己要跟踪）", b.Seen())
	}
}

func TestBuildSecondSnapshotIsIncremental(t *testing.T) {
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)
	b := NewBuilder()

	first, _ := b.Build(w, selfID, false)
	if first == nil {
		t.Fatal("首帧不该为 nil")
	}

	// 世界没变化，第二帧不该有 add/update/remove。
	second, st := b.Build(w, selfID, false)
	if st.Full {
		t.Fatal("第二帧不该是全量")
	}
	if st.Adds != 0 || st.Updates != 0 || st.Removes != 0 {
		t.Fatalf("静止世界不该有增量，得到 adds=%d updates=%d removes=%d",
			st.Adds, st.Updates, st.Removes)
	}
	_ = second
}

func TestBuildDetectsPositionChange(t *testing.T) {
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)
	otherID, other := spawnPlayer(t, w)
	b := NewBuilder()

	first, _ := b.Build(w, selfID, false)
	if len(first.Adds) != 1 {
		t.Fatalf("首帧应有 1 个新增（另一个玩家），得到 %d", len(first.Adds))
	}
	if first.Adds[0].EntityId != uint32(otherID) {
		t.Fatalf("adds[0] = %d，期望 %d", first.Adds[0].EntityId, uint32(otherID))
	}

	// 另一个玩家移动。
	other.Pos = world.V(10, 5)
	snap, st := b.Build(w, selfID, false)

	if st.Updates != 1 {
		t.Fatalf("移动后 updates = %d，期望 1", st.Updates)
	}
	if st.Adds != 0 {
		t.Fatalf("移动不该产生新增，得到 %d", st.Adds)
	}
	if got := proto.DequantizePos(snap.Updates[0].GetPos().GetX()); got < 9.9 || got > 10.1 {
		t.Fatalf("x 反量化 = %f，期望约 10", got)
	}
}

func TestBuildDetectsHPChange(t *testing.T) {
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)
	_, other := spawnPlayer(t, w)
	b := NewBuilder()
	b.Build(w, selfID, false)

	other.HP = 55
	snap, st := b.Build(w, selfID, false)

	if st.Updates != 1 {
		t.Fatalf("掉血后 updates = %d，期望 1", st.Updates)
	}
	if snap.Updates[0].GetHp() != 55 {
		t.Fatalf("HP = %d，期望 55", snap.Updates[0].GetHp())
	}
}

func TestBuildRemovesDeadEntity(t *testing.T) {
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)
	otherID, _ := spawnPlayer(t, w)
	b := NewBuilder()
	b.Build(w, selfID, false)

	if !w.Players.Kill(otherID) {
		t.Fatal("回收玩家失败")
	}

	snap, st := b.Build(w, selfID, false)

	if st.Removes != 1 {
		t.Fatalf("removes = %d，期望 1", st.Removes)
	}
	if len(snap.Removes) != 1 || snap.Removes[0] != uint32(otherID) {
		t.Fatalf("removes = %v，期望 [%d]", snap.Removes, uint32(otherID))
	}
	if b.Seen() != 1 {
		t.Fatalf("Seen = %d，期望 1（死掉的实体要移出跟踪）", b.Seen())
	}
}

func TestBuildRemovesAreSentInFullSnapshotsToo(t *testing.T) {
	// 全量时也必须带 removes：客户端保留着自己见过的实体，不通知删除
	// 就会在地图上留下幽灵。
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)
	otherID, _ := spawnPlayer(t, w)
	b := NewBuilder()
	b.Build(w, selfID, false)

	w.Players.Kill(otherID)
	snap, st := b.Build(w, selfID, true)

	if !st.Full {
		t.Fatal("强制全量时 st.Full 应为 true")
	}
	if len(snap.Removes) != 1 {
		t.Fatalf("全量快照的 removes = %v，期望包含已死的玩家", snap.Removes)
	}
}

func TestBuildPeriodicFull(t *testing.T) {
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)
	b := NewBuilder()

	b.Build(w, selfID, false) // t=0 全量
	// 推进到 FullInterval 之前：仍是增量。
	w.Tick = FullInterval - 1
	_, st := b.Build(w, selfID, false)
	if st.Full {
		t.Fatalf("tick=%d 时不该全量", w.Tick)
	}

	// 到点：应该自动全量。
	w.Tick = FullInterval
	_, st = b.Build(w, selfID, false)
	if !st.Full {
		t.Fatalf("tick=%d 时应触发周期性全量", w.Tick)
	}
}

func TestBuildForceFull(t *testing.T) {
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)
	b := NewBuilder()
	b.Build(w, selfID, false)

	_, st := b.Build(w, selfID, true)
	if !st.Full {
		t.Fatal("full=true 时必须全量")
	}
}

func TestBuildForgetForcesAdd(t *testing.T) {
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)
	otherID, _ := spawnPlayer(t, w)
	b := NewBuilder()
	b.Build(w, selfID, false)

	b.Forget(otherID)
	snap, st := b.Build(w, selfID, false)

	if st.Adds != 1 {
		t.Fatalf("Forget 后 adds = %d，期望 1", st.Adds)
	}
	if snap.Adds[0].EntityId != uint32(otherID) {
		t.Fatalf("adds[0] = %d，期望 %d", snap.Adds[0].EntityId, uint32(otherID))
	}
}

func TestBuildResetForcesFull(t *testing.T) {
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)
	b := NewBuilder()
	b.Build(w, selfID, false)

	b.Reset()

	if b.Seen() != 0 {
		t.Fatalf("Reset 后 Seen = %d，期望 0", b.Seen())
	}
	_, st := b.Build(w, selfID, false)
	if !st.Full {
		t.Fatal("Reset 后必须重新全量")
	}
}

func TestBuildClampsHP(t *testing.T) {
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)
	_, other := spawnPlayer(t, w)
	b := NewBuilder()
	b.Build(w, selfID, false)

	// 负血量（伤害结算的中间态）不能变成反号。
	other.HP = -30
	snap, _ := b.Build(w, selfID, false)
	if got := snap.Updates[0].GetHp(); got != 0 {
		t.Fatalf("负血量钳位后 = %d，期望 0", got)
	}

	// 超大值不能溢出成负数。
	other.HP = 1e9
	snap, _ = b.Build(w, selfID, false)
	if got := snap.Updates[0].GetHp(); got != 32767 {
		t.Fatalf("超大血量钳位后 = %d，期望 32767", got)
	}
}

func TestBuildPoolsDoNotCollide(t *testing.T) {
	// 玩家池和怪物池各自从索引 1 开始编号，同一个 EntityID 可以同时
	// 指向一个玩家和一只怪物。如果 seen 只用裸 ID 做键，怪物第一次出现
	// 会被当成"更新"，客户端却根本没有它——地图上就是一只隐形的怪。
	w := newTestWorld(t)
	pid, _ := spawnPlayer(t, w) // 玩家池第一个：index=1

	mid, _ := w.Monsters.Spawn() // 怪物池第一个：index=1
	if pid != mid {
		t.Fatalf("玩家 ID=%d 与怪物 ID=%d 未撞号，测试前提失效", pid, mid)
	}

	// 用旁观视角，两个实体都走 adds，撞号会直接表现为少发一个。
	b := NewBuilder()
	snap, st := b.Build(w, world.InvalidID, false)

	if st.Adds != 2 {
		t.Fatalf("adds = %d，期望 2（玩家 + 怪物各一个）", st.Adds)
	}
	kinds := map[proto.EntityKind]int{}
	for _, a := range snap.Adds {
		kinds[a.Kind]++
	}
	if kinds[proto.EntityKind_ENTITY_KIND_PLAYER] != 1 ||
		kinds[proto.EntityKind_ENTITY_KIND_MONSTER] != 1 {
		t.Fatalf("实体类型分布 = %v，期望玩家与怪物各 1", kinds)
	}
	if b.Seen() != 2 {
		t.Fatalf("Seen = %d，期望 2（玩家与怪物各自独立跟踪）", b.Seen())
	}
}

func TestBuildMonsterFields(t *testing.T) {
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)

	id, m := w.Monsters.Spawn()
	if m == nil {
		t.Fatal("生成怪物失败")
	}
	m.Pos = world.V(20, 20)
	m.HP = 50
	m.MaxHP = 50
	m.Radius = 0.4
	m.Element = world.ElementFire
	m.State = world.MonsterChase

	b := NewBuilder()
	snap, _ := b.Build(w, selfID, false)

	var d *proto.EntityDelta
	for _, a := range snap.Adds {
		if a.EntityId == uint32(id) && a.Kind == proto.EntityKind_ENTITY_KIND_MONSTER {
			d = a
		}
	}
	if d == nil {
		t.Fatal("怪物没有出现在 adds 里")
	}
	if d.GetElement() != uint32(world.ElementFire) {
		t.Fatalf("Element = %d，期望 %d", d.GetElement(), world.ElementFire)
	}
	if got := proto.DequantizeRadius(d.GetRadiusQ()); got < 0.39 || got > 0.41 {
		t.Fatalf("Radius 反量化 = %f，期望约 0.4", got)
	}
}

func TestBuildBulletFields(t *testing.T) {
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)

	bid, bl := w.Bullets.Spawn()
	if bl == nil {
		t.Fatal("生成子弹失败")
	}
	bl.Pos = world.V(3, 4)
	bl.Vel = world.V(20, 0)
	bl.OwnerID = selfID
	bl.Element = world.ElementWater
	bl.Radius = 0.15

	b := NewBuilder()
	snap, _ := b.Build(w, selfID, false)

	var d *proto.EntityDelta
	for _, a := range snap.Adds {
		if a.EntityId == uint32(bid) && a.Kind == proto.EntityKind_ENTITY_KIND_BULLET {
			d = a
		}
	}
	if d == nil {
		t.Fatal("子弹没有出现在 adds 里")
	}
	if d.GetOwnerId() != uint32(selfID) {
		t.Fatalf("OwnerId = %d，期望 %d", d.GetOwnerId(), uint32(selfID))
	}
}

func TestBuildBulletReflectorChangeTriggersUpdate(t *testing.T) {
	// 子弹被反射后归属变了，客户端要换颜色，必须重发。
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)

	_, bl := w.Bullets.Spawn()
	bl.OwnerID = selfID
	bl.Pos = world.V(1, 1)
	bl.Vel = world.V(20, 0)

	b := NewBuilder()
	b.Build(w, selfID, false)

	// 只有反射者变化，位置和速度都没动。
	bl.ReflectorID = selfID
	_, st := b.Build(w, selfID, false)

	if st.Updates != 1 {
		t.Fatalf("反射者变化后 updates = %d，期望 1", st.Updates)
	}
}

func TestBuildPickupRefID(t *testing.T) {
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)

	pid, pk := w.Pickups.Spawn()
	if pk == nil {
		t.Fatal("生成拾取物失败")
	}
	pk.Pos = world.V(6, 6)
	pk.Kind = world.PickupSkill
	pk.RefID = 7

	b := NewBuilder()
	snap, _ := b.Build(w, selfID, false)

	var d *proto.EntityDelta
	for _, a := range snap.Adds {
		if a.EntityId == uint32(pid) && a.Kind == proto.EntityKind_ENTITY_KIND_PICKUP {
			d = a
		}
	}
	if d == nil {
		t.Fatal("拾取物没有出现在 adds 里")
	}
	if d.GetOwnerId() != 7 {
		t.Fatalf("RefID 复用 OwnerId = %d，期望 7", d.GetOwnerId())
	}
	if d.GetState() != uint32(world.PickupSkill) {
		t.Fatalf("State = %d，期望 %d", d.GetState(), world.PickupSkill)
	}
}

func TestBuildPickupRefIDChangeTriggersUpdate(t *testing.T) {
	// RefID 决定贴图，变了必须重发。
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)

	_, pk := w.Pickups.Spawn()
	pk.Pos = world.V(6, 6)
	pk.Kind = world.PickupSkill
	pk.RefID = 3

	b := NewBuilder()
	b.Build(w, selfID, false)

	pk.RefID = 9
	snap, st := b.Build(w, selfID, false)

	if st.Updates != 1 {
		t.Fatalf("RefID 变化后 updates = %d，期望 1", st.Updates)
	}
	if snap.Updates[0].GetOwnerId() != 9 {
		t.Fatalf("RefID = %d，期望 9", snap.Updates[0].GetOwnerId())
	}
}

func TestBuildPlayerStateChangeNotifies(t *testing.T) {
	// 玩家变成鬼魂，别人要看到他变半透明，必须进 updates。
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)
	_, other := spawnPlayer(t, w)
	b := NewBuilder()
	b.Build(w, selfID, false)

	other.State = world.PlayerGhost
	snap, st := b.Build(w, selfID, false)

	if st.Updates != 1 {
		t.Fatalf("状态变化后 updates = %d，期望 1", st.Updates)
	}
	if snap.Updates[0].GetState() != uint32(world.PlayerGhost) {
		t.Fatalf("State = %d，期望 %d", snap.Updates[0].GetState(), world.PlayerGhost)
	}
}

func TestBuildSeenGrowsAndShrinks(t *testing.T) {
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)
	spawnPlayer(t, w)
	b := NewBuilder()

	b.Build(w, selfID, false)
	if b.Seen() != 2 {
		t.Fatalf("Seen = %d，期望 2", b.Seen())
	}

	w.Monsters.Spawn()
	w.Monsters.Spawn()
	b.Build(w, selfID, false)
	if b.Seen() != 4 {
		t.Fatalf("Seen = %d，期望 4", b.Seen())
	}
}

func TestBuildEmptyWorld(t *testing.T) {
	w := newTestWorld(t)
	b := NewBuilder()

	snap, st := b.Build(w, world.InvalidID, false)

	if snap == nil {
		t.Fatal("空世界也要返回快照")
	}
	if st.Adds != 0 || st.Updates != 0 || st.Removes != 0 {
		t.Fatalf("空世界不该有实体，得到 %+v", st)
	}
	if snap.Self != nil {
		t.Fatal("空世界的 self 应为 nil")
	}
}

func TestBuildUnknownSelfID(t *testing.T) {
	// 自己的实体已被回收（例如断线），self 应为 nil 而不是崩溃，
	// 且自己原来那具"身体"这时对旁观视角而言是别人的实体。
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)
	b := NewBuilder()

	snap, _ := b.Build(w, world.InvalidID, false)
	if snap.Self != nil {
		t.Fatal("InvalidID 作 self 时 self 应为 nil")
	}
	found := false
	for _, a := range snap.Adds {
		if a.EntityId == uint32(selfID) {
			found = true
		}
	}
	if !found {
		t.Fatal("没有 self 时所有玩家都应走 adds")
	}
}

func TestBuildSelfNotDoubleCountedAfterForget(t *testing.T) {
	// Forget 对自己也应有效：下一帧 self 字段照发，但不会在 adds 里出现
	// 第二份自己。
	w := newTestWorld(t)
	selfID, _ := spawnPlayer(t, w)
	b := NewBuilder()
	b.Build(w, selfID, false)

	b.Forget(selfID)
	snap, _ := b.Build(w, selfID, false)

	if snap.Self == nil {
		t.Fatal("self 还在，不该为 nil")
	}
	for _, a := range snap.Adds {
		if a.EntityId == uint32(selfID) {
			t.Fatal("自己不能出现在 adds 里")
		}
	}
}

func BenchmarkBuildSnapshot(b *testing.B) {
	cfg := config.Default()
	w := world.NewWorld(cfg, 1)
	for i := 0; i < 4; i++ {
		_, p := w.Players.Spawn()
		p.Pos = world.V(float64(i)*5, 0)
		p.HP = 100
		p.MaxHP = 100
	}
	for i := 0; i < 30; i++ {
		_, m := w.Monsters.Spawn()
		m.Pos = world.V(float64(i), float64(i))
		m.HP = 50
		m.MaxHP = 50
	}

	bld := NewBuilder()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.Tick++
		// 让实体每帧都动，保证走增量更新路径而不是"无变化"。
		w.Monsters.Each(func(_ world.EntityID, m *world.MonsterState) bool {
			m.Pos.X += 0.1
			return true
		})
		bld.Build(w, world.InvalidID, false)
	}
}
