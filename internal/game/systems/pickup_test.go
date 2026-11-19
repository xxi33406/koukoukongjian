package systems

import (
	"math"
	"testing"

	"koukoukongjian/internal/game/build"
	"koukoukongjian/internal/game/world"
)

// tickPickups 按世界的 tick 频率推进 n 个 tick，收集全部事件。
//
// 不复用时间推进的假时钟：拾取物的寿命是"每 tick 减 dt"，推进方式必须和
// 真实 tick 循环逐字一致（dt = 1/TickRate），否则测试测的是另一套衰减速
// 率。这里的手工循环就是 room.Run 里那一步的等价物。
func tickPickups(w *world.World, n int) []world.DamageEvent {
	var evs []world.DamageEvent
	for i := 0; i < n; i++ {
		evs = append(evs, UpdatePickups(w)...)
		w.Tick++
	}
	return evs
}

// TestSpawnPickupOnKill 钉住"怪死了地上真的多了一个掉落物"这件事。
//
// 掉落概率与接线（Reap → SpawnPickup）不在 Task 9 范围，所以这里直接调
// SpawnPickup，验证它把调用方给的四样东西（位置、种类、RefID、数量）逐字
// 写进了池槽位。位置取一只怪尸体所在处——调用方在击杀路径上拿到的是击杀
// 位置，这里用 spawnMonsterAt 造出同一场景，不另写一套坐标。
func TestSpawnPickupOnKill(t *testing.T) {
	w := newSystemsWorld(t, 11)
	killedAt := world.V(6, -2)
	spawnMonsterAt(t, w, killedAt.X, killedAt.Y)

	id := SpawnPickup(w, killedAt, world.PickupGold, 0, 7)
	if id == world.InvalidID {
		t.Fatalf("SpawnPickup 返回 InvalidID，期望一个有效 ID")
	}

	pk := w.Pickups.Get(id)
	if pk == nil {
		t.Fatalf("SpawnPickup 返回了 %v，但池里取不到这个拾取物", id)
	}
	if pk.Pos != killedAt {
		t.Errorf("位置是 %v，期望 %v（应落在尸体处）", pk.Pos, killedAt)
	}
	if pk.Kind != world.PickupGold {
		t.Errorf("种类是 %v，期望 PickupGold(%v)", pk.Kind, world.PickupGold)
	}
	if pk.Amount != 7 {
		t.Errorf("数量是 %d，期望 7", pk.Amount)
	}
	// 新掉落物必须带着满寿命出生：寿命靠每 tick 递减，出生若为 0，
	// 它会在地上一帧都活不到就被清掉，表现为"掉落了但看不见"。
	if want := float32(30); pk.Life != want {
		t.Errorf("初始寿命是 %v 秒，期望 %v 秒", pk.Life, want)
	}
	if w.Pickups.Len() != 1 {
		t.Errorf("池里活着的拾取物是 %d 个，期望 1 个", w.Pickups.Len())
	}
}

// TestPickupExpiresAfterThirtySeconds 钉住 30 秒寿命（spec L131，M4 规格修订）。
//
// 旧计划里写过 10 秒，用户按"走一趟再回来还在"的节奏改成了 30 秒；这里用
// 20Hz 实测 tick 数而不是读常量，任何人把 30 改成别的值都会在这里红灯。
func TestPickupExpiresAfterThirtySeconds(t *testing.T) {
	w := newSystemsWorld(t, 12)
	id := SpawnPickup(w, world.V(0, 0), world.PickupGold, 0, 1)
	if id == world.InvalidID {
		t.Fatal("SpawnPickup 返回 InvalidID")
	}

	// 599 tick = 29.95 秒：还在。少留 0.05 秒余量，避开浮点边界。
	tickPickups(w, 599)
	if !w.Pickups.Alive(id) {
		t.Fatalf("29.95 秒时拾取物已消失，期望还能活到 30 秒")
	}

	// 到第 605 tick = 30.25 秒：必须已消失。
	evs := tickPickups(w, 6)
	if w.Pickups.Alive(id) {
		t.Fatalf("30.25 秒时拾取物仍在，期望 30 秒后消失")
	}
	// 到期必须产出"消失"事件，否则 M5 的分账/统计无从知道东西被清掉了。
	found := false
	for _, e := range evs {
		if e.Dst == id {
			found = true
		}
	}
	if !found {
		t.Errorf("到期时没有产出 Dst=%v 的消失事件，收到 %d 条", id, len(evs))
	}
}

// TestPickupCollectedWithinRadius 钉住"走过去就捡到"。
//
// 半径取 PickupRadius=1.2（玩家半径 0.3 + 余量）：太大会隔着墙捡到，太小
// 要玩家精确对点，灰盒阶段前者比后者难受得多。
func TestPickupCollectedWithinRadius(t *testing.T) {
	w := newSystemsWorld(t, 13)
	pid, p := spawnPlayerAt(t, w, 0, 0)
	id := SpawnPickup(w, world.V(1.0, 0), world.PickupGold, 0, 5)

	// 1.0 < 1.2：在拾取半径内。
	evs := tickPickups(w, 1)
	if w.Pickups.Alive(id) {
		t.Errorf("玩家在 %.1f 米内却没捡到（PickupRadius=%.1f）", 1.0, PickupRadius)
	}
	if !hasEventFor(evs, id) {
		t.Errorf("拾取后没有产出 Dst=%v 的事件，收到 %d 条", id, len(evs))
	}
	if p.Gold != 5 {
		t.Errorf("捡到金币后 Gold=%d，期望 5", p.Gold)
	}
	_ = pid

	// 1.5 > 1.2：半径外不许捡。
	far := SpawnPickup(w, world.V(1.5, 0), world.PickupGold, 0, 5)
	tickPickups(w, 1)
	if !w.Pickups.Alive(far) {
		t.Errorf("玩家距离 %.1f 米（半径 %.1f 外）却捡到了", 1.5, PickupRadius)
	}
}

// hasEventFor 报告事件里有没有以 id 为 Dst 的那条。
func hasEventFor(evs []world.DamageEvent, id world.EntityID) bool {
	for _, e := range evs {
		if e.Dst == id {
			return true
		}
	}
	return false
}

// TestPickupGrantsExpOrGold 钉住四类拾取物的落地效果。
//
// 名字沿用计划（ExpOrGold），但 M4 的 PickupKind 没有"经验瓶"——经验走
// grantKillExp 的共享半径分配，不落地。所以这里实测金币、技能、道具、
// 血瓶四条真实分支；经验那条不存在，硬造一个枚举只为对齐测试名是错的。
func TestPickupGrantsExpOrGold(t *testing.T) {
	t.Run("金币进 Gold", func(t *testing.T) {
		w := newSystemsWorld(t, 14)
		_, p := spawnPlayerAt(t, w, 0, 0)
		SpawnPickup(w, world.V(0.5, 0), world.PickupGold, 0, 12)
		tickPickups(w, 1)
		if p.Gold != 12 {
			t.Errorf("Gold=%d，期望 12", p.Gold)
		}
	})

	t.Run("金币累加不覆盖", func(t *testing.T) {
		w := newSystemsWorld(t, 15)
		_, p := spawnPlayerAt(t, w, 0, 0)
		SpawnPickup(w, world.V(0.5, 0), world.PickupGold, 0, 12)
		tickPickups(w, 1)
		SpawnPickup(w, world.V(0, 0.5), world.PickupGold, 0, 8)
		tickPickups(w, 1)
		if p.Gold != 20 {
			t.Errorf("两次拾取后 Gold=%d，期望 20（12+8）", p.Gold)
		}
	})

	t.Run("被动技能进被动槽并立刻重算生命上限", func(t *testing.T) {
		w := newSystemsWorld(t, 16)
		_, p := spawnPlayerAt(t, w, 0, 0)
		SpawnPickup(w, world.V(0.5, 0), world.PickupSkill, build.SkillIDToughBody, 0)
		tickPickups(w, 1)
		if got := p.SkillPassive[0]; got != build.SkillIDToughBody {
			t.Fatalf("被动槽[0]=%d，期望 %d（强健体魄）", got, build.SkillIDToughBody)
		}
		// 强健体魄 +25 生命上限。装完必须立刻重算：不重算的话血条不变，
		// 玩家会以为没捡上（room.applyChoiceLocked 同款要求）。
		if want := BaseMaxHP + 25; p.MaxHP != want {
			t.Errorf("MaxHP=%v，期望 %v（基线 %v + 25）", p.MaxHP, want, BaseMaxHP)
		}
	})

	t.Run("主动技能进主动槽", func(t *testing.T) {
		w := newSystemsWorld(t, 17)
		_, p := spawnPlayerAt(t, w, 0, 0)
		SpawnPickup(w, world.V(0.5, 0), world.PickupSkill, build.SkillIDHeal, 0)
		tickPickups(w, 1)
		if got := p.SkillActive[0]; got != build.SkillIDHeal {
			t.Errorf("主动槽[0]=%d，期望 %d（治疗）", got, build.SkillIDHeal)
		}
	})

	t.Run("道具进第一个空槽", func(t *testing.T) {
		w := newSystemsWorld(t, 18)
		_, p := spawnPlayerAt(t, w, 0, 0)
		SpawnPickup(w, world.V(0.5, 0), world.PickupItem, 3, 0)
		tickPickups(w, 1)
		if got := p.ItemSlots[0]; got != 3 {
			t.Errorf("道具槽[0]=%d，期望 3", got)
		}
	})

	t.Run("血瓶回血且不超上限", func(t *testing.T) {
		w := newSystemsWorld(t, 19)
		pid, p := spawnPlayerAt(t, w, 0, 0)
		p.HP = 50

		// 血瓶只有被搬运（Carrier 指向某个玩家）时才可被玩家拾取，见
		// TestHealthPickupNotCollectibleByPlayer。这里直接造一个「已交接给
		// 这个玩家」的：M5 起 Carrier 必须是指向玩家的真实 ID，才能表达
		// 「鬼魂把它递到了这个人手里」——写随便一个数字会让测试的通过与否
		// 取决于两个池的编号是否恰好撞上。
		id := SpawnPickup(w, world.V(0.5, 0), world.PickupHealth, 0, 30)
		if pk := w.Pickups.Get(id); pk != nil {
			pk.Carrier = pid
		}
		tickPickups(w, 1)
		if p.HP != 80 {
			t.Errorf("回血后 HP=%v，期望 80（50+30）", p.HP)
		}

		// 再捡一瓶，溢出的部分要钳在上限，不能把血条顶到 100% 以上。
		hid := SpawnPickup(w, world.V(0.5, 0), world.PickupHealth, 0, 30)
		if pk := w.Pickups.Get(hid); pk != nil {
			pk.Carrier = pid
		}
		tickPickups(w, 1)
		if p.HP != p.MaxHP {
			t.Errorf("溢出回血后 HP=%v，期望钳到 MaxHP=%v", p.HP, p.MaxHP)
		}
	})
}

// TestHealthPickupNotCollectibleByPlayer 钉住 spec L45：怪掉的血瓶玩家不能捡。
//
// 血瓶的设计归属是"鬼魂搬运"（spec L50）——玩家变成鬼后才有资格把地上的
// 血瓶搬给活着的队友，交接后 Carrier 指向那个活人才允许被拾取。M4 时
// 搬运动作还不存在（GhostPickups/GhostHandoffs 是 M5 Task 8 才写的），
// 所以这条规则当时表现为"任何血瓶都捡不起来"；M5 起它仍然是这条规则，
// 只是另一半补上了。
func TestHealthPickupNotCollectibleByPlayer(t *testing.T) {
	w := newSystemsWorld(t, 20)
	_, p := spawnPlayerAt(t, w, 0, 0)
	p.HP = 40

	id := SpawnPickup(w, world.V(0.5, 0), world.PickupHealth, 0, 30)
	// 明确写 Carrier=InvalidID：怪掉的瓶子没人搬（pool.Spawn 也清零，这里
	// 显式表达意图，免得以后有人以为漏了赋值）。
	if pk := w.Pickups.Get(id); pk != nil {
		pk.Carrier = world.InvalidID
	}

	// 站 1 秒（20 tick）也不许捡起来，血瓶必须还在地上、血也不许涨。
	tickPickups(w, 20)
	if !w.Pickups.Alive(id) {
		t.Errorf("没人搬的血瓶被玩家直接捡走了，spec L45 要求不可捡")
	}
	if p.HP != 40 {
		t.Errorf("HP=%v，期望保持 40（没捡起来就不该回血）", p.HP)
	}
}

// TestPickupPoolFullIsGraceful 钉住池满不崩。
//
// 地上东西已经够多时，多掉一个不如不掉：Spawn 返回 InvalidID 让调用方
// 静默丢弃（waves.go 少刷怪、pool.go 注释都是同一取舍）。panic 会把整个
// tick 带走，那才是真正的故障。
func TestPickupPoolFullIsGraceful(t *testing.T) {
	w := newSystemsWorld(t, 21)

	// 一路掉到池子拒收为止，不预设计"什么时候算满"：NewPool 的 64 只是初始
	// 预热槽数，槽位会按需生长到硬上限（MaxIndex-1）才停。硬编码任一数字都
	// 会在有人调容量时变成一条假测试。
	spawned := 0
	fullAt := -1
	for i := 0; i < world.MaxIndex+64; i++ {
		if SpawnPickup(w, world.V(float64(i), 0), world.PickupGold, 0, 1) == world.InvalidID {
			fullAt = i
			break
		}
		spawned++
	}
	if fullAt < 0 {
		t.Fatalf("掉了 %d 个仍未拒绝，池子看起来没有上限", spawned)
	}
	if spawned < 64 {
		t.Errorf("池满前只成功生成 %d 个，少于初始槽数 64", spawned)
	}
	if got := w.Pickups.Len(); got != spawned {
		t.Errorf("成功生成 %d 个但池里活着 %d 个", spawned, got)
	}

	// 池满时再掉必须静默返回 InvalidID，且世界状态一点不变。
	for i := 0; i < 5; i++ {
		if id := SpawnPickup(w, world.V(999, 999), world.PickupGold, 0, 1); id != world.InvalidID {
			t.Fatalf("池满时第 %d 次生成返回了 %v，期望 InvalidID", i+1, id)
		}
	}
	if got := w.Pickups.Len(); got != spawned {
		t.Errorf("池满后再生成，池里变成 %d 个，期望仍是 %d 个", got, spawned)
	}

	// 全部到期后池位要能被回收复用，否则一整局下来用满一次就再也没有
	// 掉落物了。这里把寿命压到不足一个 tick 再推进，走的是 UpdatePickups
	// 的到期分支，和 30 秒自然到期是同一条路径。
	w.Pickups.Each(func(_ world.EntityID, pk *world.PickupState) bool {
		pk.Life = 0.001
		return true
	})
	tickPickups(w, 1)
	if got := w.Pickups.Len(); got != 0 {
		t.Errorf("全部到期后池里还剩 %d 个，期望 0 个", got)
	}
	if id := SpawnPickup(w, world.V(0, 0), world.PickupGold, 0, 1); id == world.InvalidID {
		t.Errorf("池位已清空却仍生成失败，槽位没有回收")
	}
}

// TestSpawnPickupClampsAmount 钉住浮点→int32 的钳位（AGENTS §2.6 第一条）。
//
// PickupState.Amount 是 int32，而 SpawnPickup 收 float64：不钳位时
// int32(NaN) / int32(1e30) 是平台相关的未定义值，金币会变成负数或天文数字。
func TestSpawnPickupClampsAmount(t *testing.T) {
	w := newSystemsWorld(t, 22)
	cases := []struct {
		in   float64
		want int32
		why  string
	}{
		{math.NaN(), 0, "NaN 不能变成未定义值"},
		{math.Inf(1), math.MaxInt32, "正无穷顶到 int32 上限"},
		{-5, 0, "负数当 0：捡到钱反而扣钱是玩家无法理解的"},
		{1e30, math.MaxInt32, "超上限钳到位"},
		{7.9, 7, "小数截断"},
	}
	for _, c := range cases {
		id := SpawnPickup(w, world.V(0, 0), world.PickupGold, 0, c.in)
		pk := w.Pickups.Get(id)
		if pk == nil {
			t.Fatalf("SpawnPickup(%v) 返回的 ID 取不到实体", c.in)
		}
		if pk.Amount != c.want {
			t.Errorf("amount=%v 存成 %d，期望 %d（%s）", c.in, pk.Amount, c.want, c.why)
		}
		w.Pickups.Kill(id)
	}
}
