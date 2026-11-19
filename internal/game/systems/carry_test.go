package systems

import (
	"testing"

	"koukoukongjian/internal/game/world"
)

// 鬼魂走到掉落物旁把它拎起来（Carrier = 鬼魂 ID），但不结算。
//
// 「不结算」是这条规则的关键：鬼魂喝血瓶毫无意义（它不受击、也没有血），
// 若捡起时顺手 applyPickup，血瓶就被鬼魂吞了，复活回来的队友什么也没拿到。
func TestGhostPicksUpAndCarries(t *testing.T) {
	w := newSystemsWorld(t, 7)

	gid, ghost := w.Players.Spawn()
	ghost.State = world.PlayerGhost
	ghost.Pos = world.V(10, 10)

	// 怪掉的血瓶：Carrier 是 InvalidID，活人捡不到
	pid := SpawnPickup(w, world.V(10.5, 10), world.PickupHealth, 0, 30)
	if pid == world.InvalidID {
		t.Fatalf("前提失效：没能生成血瓶")
	}

	GhostPickups(w)

	pk := w.Pickups.Get(pid)
	if pk == nil {
		t.Fatalf("血瓶消失了")
	}
	if pk.Carrier != gid {
		t.Fatalf("Carrier = %d，期望鬼魂 %d", pk.Carrier, gid)
	}
	// 不结算：鬼魂的 HP 不该变
	if ghost.HP != 0 {
		t.Fatalf("鬼魂捡血瓶时被治疗了：HP = %v", ghost.HP)
	}
}

// 活人捡不到没被鬼魂搬过的血瓶（spec L45）。
//
// 这条与 TestGhostPicksUpAndCarries 是一对：只实现搬运不实现"活人捡不到"，
// 血瓶的稀缺性就没了；只实现"捡不到"不实现搬运，血瓶就永远入库不了。
func TestAliveCannotPickUncarriedHealth(t *testing.T) {
	w := newSystemsWorld(t, 7)

	_, p := w.Players.Spawn()
	p.State = world.PlayerAlive
	p.HP = 50
	p.MaxHP = 100
	p.Pos = world.V(10, 10)

	pid := SpawnPickup(w, world.V(10.2, 10), world.PickupHealth, 0, 30)

	UpdatePickups(w)

	if w.Pickups.Get(pid) == nil {
		t.Fatalf("活人捡走了没人搬的血瓶（spec L45 不允许）")
	}
	if p.HP != 50 {
		t.Fatalf("活人被治疗了：HP = %v，期望 50", p.HP)
	}
}

// 鬼魂把搬着的东西交给附近的活人：Carrier 改成活人 ID。
func TestGhostHandsOffToAlive(t *testing.T) {
	w := newSystemsWorld(t, 7)

	aid, alive := w.Players.Spawn()
	alive.State = world.PlayerAlive
	alive.HP = 50
	alive.MaxHP = 100
	alive.Pos = world.V(10, 10)

	gid, ghost := w.Players.Spawn()
	ghost.State = world.PlayerGhost
	ghost.Pos = world.V(10.2, 10)

	pid := SpawnPickup(w, world.V(10, 10), world.PickupHealth, 0, 30)
	w.Pickups.Get(pid).Carrier = gid // 鬼魂手里

	GhostHandoffs(w)

	pk := w.Pickups.Get(pid)
	if pk == nil {
		t.Fatalf("血瓶消失了")
	}
	if pk.Carrier != aid {
		t.Fatalf("Carrier = %d，期望活人 %d", pk.Carrier, aid)
	}
}

// 交接后由原有的活人拾取路径结算：不在这里 applyPickup。
//
// 为什么不在交接时直接结算：那会是第二条结算路径。M4 的 Task 10 已经为
// 避免第二条改世界路径写过长注释（Interact kind=1 只打 Debug 的理由），
// 两条路径的规则（半径、槽位满、Amount 钳位）必须同步维护，改一处漏一处。
func TestHandoffThenNormalPickupApplies(t *testing.T) {
	w := newSystemsWorld(t, 7)

	aid, alive := w.Players.Spawn()
	alive.State = world.PlayerAlive
	alive.HP = 50
	alive.MaxHP = 100
	alive.Pos = world.V(10, 10)

	gid, ghost := w.Players.Spawn()
	ghost.State = world.PlayerGhost
	ghost.Pos = world.V(10.2, 10)

	pid := SpawnPickup(w, world.V(10, 10), world.PickupHealth, 0, 30)
	w.Pickups.Get(pid).Carrier = gid

	GhostHandoffs(w) // Carrier → 活人
	UpdatePickups(w) // 正常结算

	if alive.HP != 80 {
		t.Fatalf("交接后活人 HP = %v，期望 80（50+30）", alive.HP)
	}
	if w.Pickups.Get(pid) != nil {
		t.Fatalf("交接后血瓶没被回收")
	}
	_ = aid
}

// 鬼魂不能捡技能掉落物以外的"自己用"的东西——但技能与道具可以搬。
func TestGhostCarriesSkillAndItem(t *testing.T) {
	w := newSystemsWorld(t, 7)

	gid, ghost := w.Players.Spawn()
	ghost.State = world.PlayerGhost
	ghost.Pos = world.V(10, 10)

	sid := SpawnPickup(w, world.V(10.3, 10), world.PickupSkill, 6, 0)
	iid := SpawnPickup(w, world.V(10.4, 10), world.PickupItem, 2, 0)

	GhostPickups(w)

	if pk := w.Pickups.Get(sid); pk == nil || pk.Carrier != gid {
		t.Fatalf("技能掉落物没被鬼魂搬起")
	}
	if pk := w.Pickups.Get(iid); pk == nil || pk.Carrier != gid {
		t.Fatalf("道具掉落物没被鬼魂搬起")
	}
}

// 鬼魂不捡金币掉落物：金币 M5 不再作为掉落物产生，但池里可能残留
// 测试或旧逻辑塞进去的金币，鬼魂捡它没有意义（给不了活人，也不该自己吃）。
func TestGhostIgnoresGold(t *testing.T) {
	w := newSystemsWorld(t, 7)

	_, ghost := w.Players.Spawn()
	ghost.State = world.PlayerGhost
	ghost.Pos = world.V(10, 10)

	gid := SpawnPickup(w, world.V(10.1, 10), world.PickupGold, 0, 5)

	GhostPickups(w)

	if pk := w.Pickups.Get(gid); pk != nil && pk.Carrier != world.InvalidID {
		t.Fatalf("鬼魂捡了金币掉落物：Carrier = %d", pk.Carrier)
	}
}

// 已经不是鬼魂的人手里的东西要放下来（复活之后）。
//
// 不清会出现"复活了的玩家手里还挂着三个血瓶没人能捡"，而这些血瓶
// 的 Carrier 早已无效，永远进不了任何人的背包。
func TestCarrierClearedWhenCarrierNoLongerGhost(t *testing.T) {
	w := newSystemsWorld(t, 7)

	gid, ghost := w.Players.Spawn()
	ghost.State = world.PlayerAlive // 已经复活
	ghost.Pos = world.V(10, 10)

	pid := SpawnPickup(w, world.V(10, 10), world.PickupHealth, 0, 30)
	w.Pickups.Get(pid).Carrier = gid

	GhostPickups(w)

	if pk := w.Pickups.Get(pid); pk != nil && pk.Carrier != world.InvalidID {
		t.Fatalf("复活后仍挂着搬运者：Carrier = %d", pk.Carrier)
	}
}

// 死掉的鬼魂（被池回收）手里的东西也要放开。
func TestCarrierClearedWhenGhostGone(t *testing.T) {
	w := newSystemsWorld(t, 7)

	gid, ghost := w.Players.Spawn()
	ghost.State = world.PlayerGhost
	ghost.Pos = world.V(10, 10)

	pid := SpawnPickup(w, world.V(10, 10), world.PickupHealth, 0, 30)
	w.Pickups.Get(pid).Carrier = gid

	w.Players.Kill(gid) // 掉线/离开

	GhostPickups(w)

	if pk := w.Pickups.Get(pid); pk != nil && pk.Carrier != world.InvalidID {
		t.Fatalf("搬运者消失后仍挂着：Carrier = %d", pk.Carrier)
	}
}

// 血瓶被鬼魂搬着时，活人不能在半路截走。
//
// 这条是 pickupTarget 改动的直接守护：Carrier 指向一个还在搬的鬼魂时，
// 活人走到血瓶上也不该捡起来——否则鬼魂的搬运路径就没意义了，队友直接
// 自己走过去拿就行，spec L50 的"后勤位"分工消失。
func TestAliveCannotStealFromCarryingGhost(t *testing.T) {
	w := newSystemsWorld(t, 7)

	_, alive := w.Players.Spawn()
	alive.State = world.PlayerAlive
	alive.HP = 50
	alive.MaxHP = 100
	alive.Pos = world.V(10, 10)

	gid, ghost := w.Players.Spawn()
	ghost.State = world.PlayerGhost
	ghost.Pos = world.V(10.1, 10)

	pid := SpawnPickup(w, world.V(10, 10), world.PickupHealth, 0, 30)
	w.Pickups.Get(pid).Carrier = gid // 鬼魂正搬着

	UpdatePickups(w)

	if w.Pickups.Get(pid) == nil {
		t.Fatalf("活人从搬运中的鬼魂手里截走了血瓶")
	}
	if alive.HP != 50 {
		t.Fatalf("活人被治疗了：HP = %v，期望 50", alive.HP)
	}
	_ = ghost
}
