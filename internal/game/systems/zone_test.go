package systems

import (
	"math"
	"testing"

	"koukoukongjian/internal/game/world"
)

// TestUpdateZonesExpiresByLife 钉住寿命递减与回收。
func TestUpdateZonesExpiresByLife(t *testing.T) {
	w := newSystemsWorld(t, 7)

	id := SpawnZone(w, world.V(20, 20), 3.0, 0.10, world.ZoneSteam, world.InvalidID, world.ElementNone)

	// 0.05 秒后还在。
	UpdateZones(w, 0.05)
	if w.Zones.Get(id) == nil {
		t.Fatal("0.05 秒后区域消失了，期望还剩 0.05 秒寿命")
	}

	// 再 0.05 秒，寿命归零被回收。
	UpdateZones(w, 0.05)
	if w.Zones.Get(id) != nil {
		t.Fatal("寿命归零后区域还在")
	}
}

// TestUpdateZonesSkipsUnspecifiedKind 钉住零值槽位不被当垃圾清掉。
//
// 池里的槽位可能被占用但还没初始化（Kind=0）。UpdateZones 只按寿命回收，
// 不按 Kind 判断——否则一个刚 Spawn 还没写字段的区域会被立刻清掉。
func TestUpdateZonesSkipsUnspecifiedKind(t *testing.T) {
	w := newSystemsWorld(t, 7)

	id, z := w.Zones.Spawn()
	z.Life = 100 // 寿命很长，不该因为 Kind=0 被特殊处理

	UpdateZones(w, 0.05)
	if w.Zones.Get(id) == nil {
		t.Fatal("Kind=0 但寿命未到的槽位被回收了——零值槽位不该被当垃圾清掉")
	}
}

// TestUpdateZonesIgnoresBadDelta 钉住非法 dt 不破坏寿命。
//
// dt 来自 tick 循环的时长计算，一旦是 NaN，z.Life -= NaN 会让寿命变成
// NaN，而 NaN <= 0 恒为 false——区域永远不消失，且再也算不回来。
func TestUpdateZonesIgnoresBadDelta(t *testing.T) {
	w := newSystemsWorld(t, 7)

	id := SpawnZone(w, world.V(20, 20), 3.0, 5.0, world.ZoneSteam, world.InvalidID, world.ElementNone)

	for _, dt := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		UpdateZones(w, dt)
		z := w.Zones.Get(id)
		if z == nil {
			t.Fatalf("dt=%v 时区域被回收了", dt)
		}
		if z.Life != 5.0 {
			t.Fatalf("dt=%v 后 Life = %v，期望仍为 5.0", dt, z.Life)
		}
	}
}

// TestTickZoneSlowResetsEachSubStep 钉住「每子步重置」。
//
// 这是本 Task 最关键的语义：离开云的玩家必须立刻恢复速度。若只在
// 进入时设置、离开时不重置，玩家的 ZoneSlowMul 会永远停在 0.6。
func TestTickZoneSlowResetsEachSubStep(t *testing.T) {
	w := newSystemsWorld(t, 7)

	_, p := spawnPlayerAt(t, w, 20, 20)
	p.ZoneSlowMul = 0.5 // 假装上一子步被减速了

	// 世界里没有任何区域。
	TickZoneSlow(w)

	if p.ZoneSlowMul != 1.0 {
		t.Fatalf("没有区域时 ZoneSlowMul = %v，期望 1.0（重置）", p.ZoneSlowMul)
	}
}

// TestTickZoneSlowResetsOnLeavingSteam 钉住「走出云」这条慢路径。
//
// 与上面那条的区别是**世界里存在区域**——上一版只有"没有区域"的用例，
// 它走的是 TickZoneSlow 的快路径（zones.Len()==0 直接重置），而真正
// 危险的是慢路径：有区域、玩家不在里面。变异测试证明删掉慢路径里的
// 重置后旧用例仍然全绿，所以必须分开钉。
func TestTickZoneSlowResetsOnLeavingSteam(t *testing.T) {
	w := newSystemsWorld(t, 7)

	_, p := spawnPlayerAt(t, w, 10, 10) // 玩家在 (10,10)
	p.ZoneSlowMul = 0.5                 // 上一子步还在云里

	SpawnZone(w, world.V(30, 30), 3.0, 5.0, world.ZoneSteam, world.InvalidID, world.ElementNone)

	TickZoneSlow(w)

	if p.ZoneSlowMul != 1.0 {
		t.Fatalf("走出蒸汽云后 ZoneSlowMul = %v，期望 1.0（必须重置）", p.ZoneSlowMul)
	}
}

// TestTickZoneSlowSlowsPlayerInsideSteam 钉住减速生效。
func TestTickZoneSlowSlowsPlayerInsideSteam(t *testing.T) {
	w := newSystemsWorld(t, 7)

	_, p := spawnPlayerAt(t, w, 20, 20)
	zid := SpawnZone(w, world.V(20, 20), 3.0, 5.0, world.ZoneSteam, world.InvalidID, world.ElementNone)
	w.Zones.Get(zid).SlowMul = 0.6

	TickZoneSlow(w)

	if p.ZoneSlowMul != 0.6 {
		t.Fatalf("蒸汽云里的玩家 ZoneSlowMul = %v，期望 0.6", p.ZoneSlowMul)
	}
}

// TestTickZoneSlowIgnoresOutside 钉住范围外的不减速。
func TestTickZoneSlowIgnoresOutside(t *testing.T) {
	w := newSystemsWorld(t, 7)

	_, p := spawnPlayerAt(t, w, 10, 10)
	zid := SpawnZone(w, world.V(20, 20), 3.0, 5.0, world.ZoneSteam, world.InvalidID, world.ElementNone)
	w.Zones.Get(zid).SlowMul = 0.6

	TickZoneSlow(w)

	if p.ZoneSlowMul != 1.0 {
		t.Fatalf("区域外的玩家 ZoneSlowMul = %v，期望 1.0", p.ZoneSlowMul)
	}
}

// TestTickZoneSlowMultipleZonesStack 钉住多片区域取最强者而不相乘。
//
// 相乘会让三片蒸汽云叠加成 0.216 倍速（几乎动不了），而玩家看不出
// 「为什么三片云这么强」。取最强者语义单一：重叠区域的强度就是最强的
// 那一片。
func TestTickZoneSlowMultipleZonesStack(t *testing.T) {
	w := newSystemsWorld(t, 7)

	_, p := spawnPlayerAt(t, w, 20, 20)
	id1 := SpawnZone(w, world.V(20, 20), 3.0, 5.0, world.ZoneSteam, world.InvalidID, world.ElementNone)
	w.Zones.Get(id1).SlowMul = 0.8
	id2 := SpawnZone(w, world.V(20, 20), 3.0, 5.0, world.ZoneSteam, world.InvalidID, world.ElementNone)
	w.Zones.Get(id2).SlowMul = 0.6

	TickZoneSlow(w)

	if p.ZoneSlowMul != 0.6 {
		t.Fatalf("两片云重叠时 ZoneSlowMul = %v，期望 0.6（取最强）", p.ZoneSlowMul)
	}
}

// TestTickZoneSlowIgnoresGhost 钉住鬼魂不吃减速。
//
// 鬼魂已经是 0.8 倍速且不能战斗，再减速会让死亡惩罚过重到「只能站着」。
// spec L49 给鬼魂的定位是「可探图、可跟随」，这两件事都需要走动。
func TestTickZoneSlowIgnoresGhost(t *testing.T) {
	w := newSystemsWorld(t, 7)

	_, p := spawnPlayerAt(t, w, 20, 20)
	p.State = world.PlayerGhost
	id := SpawnZone(w, world.V(20, 20), 3.0, 5.0, world.ZoneSteam, world.InvalidID, world.ElementNone)
	w.Zones.Get(id).SlowMul = 0.6

	TickZoneSlow(w)

	if p.ZoneSlowMul != 1.0 {
		t.Fatalf("鬼魂的 ZoneSlowMul = %v，期望 1.0（鬼魂不吃减速）", p.ZoneSlowMul)
	}
}

// TestTickZoneSlowSlowsMonster 钉住怪物也吃减速。
//
// spec 没区分敌我，而「把怪推进蒸汽云」本身就是一种战术选择。只减速
// 玩家会让蒸汽云变成纯负面地形，交汇产出的东西反而没用了。
func TestTickZoneSlowSlowsMonster(t *testing.T) {
	w := newSystemsWorld(t, 7)

	mid := spawnMonsterAt(t, w, 20, 20)
	id := SpawnZone(w, world.V(20, 20), 3.0, 5.0, world.ZoneSteam, world.InvalidID, world.ElementNone)
	w.Zones.Get(id).SlowMul = 0.6

	TickZoneSlow(w)

	m := w.Monsters.Get(mid)
	if m.ZoneSlowMul != 0.6 {
		t.Fatalf("蒸汽云里的怪物 ZoneSlowMul = %v，期望 0.6", m.ZoneSlowMul)
	}
}

// TestTickZoneSlowIgnoresDeadMonster 钉住尸体不被减速（也不被重置成污染值）。
func TestTickZoneSlowIgnoresDeadMonster(t *testing.T) {
	w := newSystemsWorld(t, 7)

	mid := spawnMonsterAt(t, w, 20, 20)
	m := w.Monsters.Get(mid)
	m.ZoneSlowMul = 0.3 // 死前的残留值
	m.Flags |= world.MonsterFlagDead

	id := SpawnZone(w, world.V(20, 20), 3.0, 5.0, world.ZoneSteam, world.InvalidID, world.ElementNone)
	w.Zones.Get(id).SlowMul = 0.6

	TickZoneSlow(w)

	if m.ZoneSlowMul != 1.0 {
		t.Fatalf("尸体的 ZoneSlowMul = %v，期望重置为 1.0", m.ZoneSlowMul)
	}
}

// TestTickZoneSlowIgnoresNonSteamZone 钉住只有蒸汽云减速。
//
// 反射面不该减速：玩家要能站在自己放的镜子旁边走位。
func TestTickZoneSlowIgnoresNonSteamZone(t *testing.T) {
	w := newSystemsWorld(t, 7)

	_, p := spawnPlayerAt(t, w, 20, 20)
	SpawnZone(w, world.V(20, 20), 3.0, 5.0, world.ZoneReflect, world.InvalidID, world.ElementNone)

	TickZoneSlow(w)

	if p.ZoneSlowMul != 1.0 {
		t.Fatalf("反射面里的玩家 ZoneSlowMul = %v，期望 1.0", p.ZoneSlowMul)
	}
}

// TestIntegrateAppliesZoneSlow 钉住 Integrate 真的吃了这个倍率。
//
// 前面几条只证明 ZoneSlowMul 被正确写入，不证明它被正确读取——一个
// 「写了但没人读」的字段在所有单元测试里都是绿的。
func TestIntegrateAppliesZoneSlow(t *testing.T) {
	w := newSystemsWorld(t, 7)

	_, p := spawnPlayerAt(t, w, 20, 20)
	p.MoveIntent = world.V(1, 0)
	p.ZoneSlowMul = 0.5

	Integrate(p, 1.0, w.Terrain)

	// 正常速度 6 m/s，减半后应该是 3 m/s。
	want := PlayerSpeed * 0.5
	if got := p.Vel.Len(); got < want*0.99 || got > want*1.01 {
		t.Fatalf("减速后速度 = %v，期望 ≈ %v", got, want)
	}
}
