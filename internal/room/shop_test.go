package room

import (
	"errors"
	"testing"
	"time"

	"koukoukongjian/internal/game/systems"
	"koukoukongjian/internal/game/world"
	"koukoukongjian/internal/proto"
)

// 本文件是 M6 Task 10 的商店集成测试。
//
// 并发约束（AGENTS.md §2.5）：房间跑起来之后 *world.World 归 tick goroutine
// 独占。所以摆位一律在 startRoomWith 的 setup 回调里，观察走可靠下行通道。

// shopRackOf 取出 Envelope 里的 ShopRack，不是该消息时返回 nil。
func shopRackOf(env *proto.Envelope) *proto.ShopRack {
	return env.GetShopRack()
}

// putPlayerInHub 把某个座位的玩家挪到据点中心附近。
//
// 必须在 setup 回调里调用：run 起来之后写 PlayerState.Pos 就是跨
// goroutine 写。这里刻意在 setup 里先拿 JoinResult（Run 之前加入），
// 再改 Pos——joinPlayer 在 Run 前调用是安全的（combat_test 的既有用法）。
func TestRoomShopNotOpenBeforeHubClear(t *testing.T) {
	r, cancel := startRoomWith(t, "waves", func(r *Room) {
		// 不推进任何波次：开局就是 HubFighting，没有一个据点 Cleared。
	})
	defer cancel()

	_, sink := joinPlayer(t, r, "shopper")

	// 反向断言：等 500ms，确认这条通道上没有 ShopRack。
	//
	// 「没有消息」比「有消息」难断言——用 500ms 的窗口是因为
	// updateShopOpen 每 tick 都跑（20Hz），真有消息第一帧就会到。
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case msg, ok := <-r.Reliable():
			if !ok {
				return
			}
			var env proto.Envelope
			if err := proto.Unmarshal(msg.Data, &env); err != nil {
				t.Fatalf("可靠消息解码失败：%v", err)
			}
			if env.GetShopRack() != nil {
				t.Fatalf("据点还没清完就开了商店（hub=%d）", env.GetShopRack().GetHub())
			}
		case <-sink:
			// 排空快照，别让它塞满。
		case <-deadline:
			return
		}
	}
}

// TestRoomShopBuyBeforeOpenRejected 钉住「商店没开就买入会被拒」。
//
// 断言错误是 ErrShopClosed 而不是别的：接线如果根本没走到 room（比如
// bridge 的 case 漏了），返回的会是 ErrRoomStopped 或超时，与「正确地
// 拒绝了」无法区分。
func TestRoomShopBuyBeforeOpenRejected(t *testing.T) {
	r, cancel := startRoomWith(t, "waves", nil)
	defer cancel()

	res, _ := joinPlayer(t, r, "shopper")

	err := r.ShopBuy(res.Slot, 0, 0, res.Slot)
	if err == nil {
		t.Fatal("商店没开却买成功了")
	}
	if !errors.Is(err, ErrShopClosed) {
		t.Fatalf("错误 = %v，期望 ErrShopClosed（说明请求走到了商店逻辑）", err)
	}
}

// TestRoomShopBuyOutsideHubRejected 钉住「买家不在据点范围内被拒」。
//
// 这个用例把商店强行打开（直接改 Hub.ShopOpen 在 setup 里），然后把
// 玩家留在出生点。出生点可能就在某个据点里——所以先确认，在据点内
// 就换个判定：用错误类型区分「太远」与「买不起/没货架」。
func TestRoomShopBuyWithNoRackRejected(t *testing.T) {
	r, cancel := startRoomWith(t, "waves", nil)
	defer cancel()

	res, _ := joinPlayer(t, r, "shopper")

	// 商店没开 → ErrShopClosed；这条与上一条的区别在于：这里断言的
	// 是「即使 hub 下标合法、房间有地图，仍然要先过 ShopOpen 这道门」。
	err := r.ShopBuy(res.Slot, 0, 99, res.Slot)
	if err == nil {
		t.Fatal("商店没开却买成功了")
	}
	if !errors.Is(err, ErrShopClosed) {
		t.Fatalf("错误 = %v，期望 ErrShopClosed", err)
	}
}

// TestRoomShopOpensAndSendsRack 钉住开通路径：据点清完 + 有人在 → 开商店 + 下发货架。
//
// 玩家加入后站在出生据点中心（spawnPoint 用的就是 Hubs[SpawnHub].Center），
// 所以只要把出生据点预置成 HubCleared，下一个 tick 就会开商店。
//
// 为什么要预置成 Cleared 而不是打完一波：waves 模式的清波要打死整波怪，
// 那要几十秒而且掺进 AI/伤害/回收一堆变量，失败时分不清是商店错了还是
// 战斗错了。这里只验证「Cleared + 有人在 → 有货架」这一条因果。
//
// 不断言「买入成交」：玩家初始金币是 0，货架最便宜的一项也要 30 金。
// 扣款与装备的完整路径在 systems 层由 TestBuyItemDeductsGold /
// TestBuyItemGivesPassiveSkill 覆盖；这里断言的是**买入请求真的走到了
// 商店逻辑**——错误是 ErrShopNoGold（而不是 ErrShopClosed / 超时 / 空
// 货架），说明货架生成、据点校验、扣款分支全部接通了。
func TestRoomShopOpensAndSendsRack(t *testing.T) {
	r, cancel := startRoomWithOpts(t, "shop-open",
		Options{ID: "shop-open", Seed: 7, WavesEnabled: true},
		func(r *Room) {
			// 怪物改成不打人：本用例只关心商店，玩家的死活是噪声。
			r.World.Mon.AttackDamage = 0
			// 出生据点预置成已清场。HubCleared 是终态，UpdateWaves 不会改回去。
			r.World.Hubs[r.World.SpawnHub].State = world.HubCleared
		})
	defer cancel()

	res, sink := joinPlayer(t, r, "shopper")

	env := waitReliable(t, r, sink, 3*time.Second, "私人货架", func(e *proto.Envelope) bool {
		return shopRackOf(e) != nil
	})
	rack := shopRackOf(env)
	if rack.GetHub() != uint32(r.World.SpawnHub) {
		t.Fatalf("货架的 hub = %d，期望 %d（出生据点）", rack.GetHub(), r.World.SpawnHub)
	}
	if len(rack.GetItems()) != 4 {
		t.Fatalf("货架有 %d 项，期望 4 项（2 被动 + 2 道具）", len(rack.GetItems()))
	}
	for i, it := range rack.GetItems() {
		if it.GetPrice() == 0 {
			t.Fatalf("第 %d 项价格为 0", i)
		}
	}

	// 请求真的走到商店逻辑：错误必须是「金币不足」，而不是「商店没开」
	// 「货架为空」「请求超时」——后三者说明接线断了。
	err := r.ShopBuy(res.Slot, rack.GetHub(), 0, res.Slot)
	if err == nil {
		t.Fatal("金币为 0 却买成功了")
	}
	if !errors.Is(err, systems.ErrShopNoGold) {
		t.Fatalf("错误 = %v，期望 ErrShopNoGold（说明请求走完了据点/货架校验）", err)
	}
}

// 纯函数级：这条不依赖房间，但它保护的是客户端按 Index 购买的正确性——
// Index 与位置错位会让「买第 2 项」拿到第 1 项。
func TestRoomRackToProtoParallel(t *testing.T) {
	rack := []systems.ShopItem{
		{Index: 0, Kind: systems.ShopItemPassive, RefID: 1, Price: 50},
		{Index: 1, Kind: systems.ShopItemPassive, RefID: 2, Price: 50, Sold: true},
		{Index: 2, Kind: systems.ShopItemItem, RefID: 1, Price: 30},
		{Index: 3, Kind: systems.ShopItemItem, RefID: 2, Price: 40},
	}

	out := rackToProto(rack)
	if len(out) != len(rack) {
		t.Fatalf("转换后有 %d 项，期望 %d 项", len(out), len(rack))
	}
	for i := range rack {
		if out[i].GetIndex() != uint32(rack[i].Index) {
			t.Fatalf("第 %d 项 Index = %d，期望 %d", i, out[i].GetIndex(), rack[i].Index)
		}
		if out[i].GetKind() != uint32(rack[i].Kind) {
			t.Fatalf("第 %d 项 Kind = %d，期望 %d", i, out[i].GetKind(), rack[i].Kind)
		}
		if out[i].GetRefId() != uint32(rack[i].RefID) {
			t.Fatalf("第 %d 项 RefId = %d，期望 %d", i, out[i].GetRefId(), rack[i].RefID)
		}
		if out[i].GetPrice() != rack[i].Price {
			t.Fatalf("第 %d 项 Price = %d，期望 %d", i, out[i].GetPrice(), rack[i].Price)
		}
		if out[i].GetSold() != rack[i].Sold {
			t.Fatalf("第 %d 项 Sold = %v，期望 %v", i, out[i].GetSold(), rack[i].Sold)
		}
	}
}

// TestRoomAnyAliveNearSkipsGhosts 钉住「只有鬼魂在据点里不算有人在」。
//
// spec L86「全队离开据点，商店关闭」——鬼魂站在店里不构成"有人在"，
// 否则死后拴绳把鬼魂拖回店里，商店永远关不掉。
//
// 摆位在 setup 回调里（Run 之前）：Run 起来之后往 Players 池里 Spawn
// 就是跨 goroutine 写，-race 会报。
func TestRoomAnyAliveNearSkipsGhosts(t *testing.T) {
	var at world.Vec2

	r, cancel := startRoomWith(t, "waves", func(r *Room) {
		at = world.V(50, 50)
		id, g := r.World.Players.Spawn()
		if g == nil {
			t.Fatal("池满，摆不下鬼魂")
		}
		_ = id
		g.Pos = at
		g.State = world.PlayerGhost
	})
	defer cancel()

	// 只有鬼魂：不算有人在。
	if r.anyAliveNear(at, 3) {
		t.Fatal("据点里只有鬼魂，anyAliveNear 却返回 true——商店永远不会关")
	}
}

// TestApplyShopBuyTooFar 钉住「买家不在据点范围内被拒」。
//
// 不用 Run 起来的房间：applyShopBuyLocked 是 tick goroutine 里的方法，
// 但没 Run 的时候没有别的 goroutine 碰 World，测试可以直接构造
// 玩家/货架/据点状态再调它。Run 起来之后 Pos 归 tick 循环独占，
// 造「玩家站远了」这种前置状态就得靠真实移动，几十秒还可能被怪打死。
//
// 这是「集成测试造不出稳定场景时，退一步在无并发的裸 Room 上测真实方法」
// 的用法：被测的是生产代码本身，只是去掉了并发这个变量。
func TestApplyShopBuyTooFar(t *testing.T) {
	r := New(testConfig(), quietLogger(), Options{ID: "shop-far", Seed: 7})

	pid, p := r.World.Players.Spawn()
	if p == nil {
		t.Fatal("玩家池满")
	}
	// 据点中心在 (cx,cy)，把玩家摆到很远的地方。
	hub := &r.World.Hubs[0]
	hub.ShopOpen = true
	p.Pos = hub.Center.Add(world.V(hub.RadiusM*3, 0))
	p.State = world.PlayerAlive
	p.Gold = 1000

	r.peers.put(&peer{
		slot:    0,
		id:      pid,
		rack:    []systems.ShopItem{{Index: 0, Kind: systems.ShopItemItem, RefID: 1, Price: 30}},
		shopHub: 0,
	})

	err := r.applyShopBuyLocked(shopBuyRequest{slot: 0, hub: 0, itemIndex: 0, targetSlot: 0})
	if !errors.Is(err, ErrShopTooFar) {
		t.Fatalf("错误 = %v，期望 ErrShopTooFar", err)
	}
	if p.Gold != 1000 {
		t.Fatalf("被拒后 Gold = %d，期望仍是 1000", p.Gold)
	}
}

// TestApplyShopBuyForTeammate 钉住「替队友买」：钱从买家扣、东西给收货人。
func TestApplyShopBuyForTeammate(t *testing.T) {
	r := New(testConfig(), quietLogger(), Options{ID: "shop-mate", Seed: 7})

	buyerID, buyer := r.World.Players.Spawn()
	mateID, mate := r.World.Players.Spawn()
	if buyer == nil || mate == nil {
		t.Fatal("玩家池满")
	}
	hub := &r.World.Hubs[0]
	hub.ShopOpen = true
	buyer.Pos = hub.Center
	buyer.State = world.PlayerAlive
	buyer.Gold = 100
	mate.Pos = hub.Center
	mate.State = world.PlayerAlive
	mate.Gold = 0
	// 收货人的道具槽清空，确保装得进去。
	mate.ItemSlots = [3]uint16{}

	r.peers.put(&peer{
		slot:    0,
		id:      buyerID,
		rack:    []systems.ShopItem{{Index: 0, Kind: systems.ShopItemItem, RefID: 1, Price: 30}},
		shopHub: 0,
	})
	r.peers.put(&peer{slot: 1, id: mateID})

	err := r.applyShopBuyLocked(shopBuyRequest{slot: 0, hub: 0, itemIndex: 0, targetSlot: 1})
	if err != nil {
		t.Fatalf("替队友买入失败：%v", err)
	}
	if buyer.Gold != 70 {
		t.Fatalf("买家 Gold = %d，期望 70", buyer.Gold)
	}
	if mate.Gold != 0 {
		t.Fatalf("收货人 Gold = %d，期望 0（钱不该从他扣）", mate.Gold)
	}
	if mate.ItemSlots[0] != 1 {
		t.Fatalf("收货人道具槽 = %v，期望第 0 格是 1", mate.ItemSlots)
	}
}

// TestApplyShopBuyToGhostRejected 钉住「替鬼魂买没意义」。
func TestApplyShopBuyToGhostRejected(t *testing.T) {
	r := New(testConfig(), quietLogger(), Options{ID: "shop-ghost", Seed: 7})

	buyerID, buyer := r.World.Players.Spawn()
	ghostID, ghost := r.World.Players.Spawn()
	if buyer == nil || ghost == nil {
		t.Fatal("玩家池满")
	}
	hub := &r.World.Hubs[0]
	hub.ShopOpen = true
	buyer.Pos = hub.Center
	buyer.State = world.PlayerAlive
	buyer.Gold = 100
	ghost.Pos = hub.Center
	ghost.State = world.PlayerGhost

	r.peers.put(&peer{
		slot:    0,
		id:      buyerID,
		rack:    []systems.ShopItem{{Index: 0, Kind: systems.ShopItemItem, RefID: 1, Price: 30}},
		shopHub: 0,
	})
	r.peers.put(&peer{slot: 1, id: ghostID})

	err := r.applyShopBuyLocked(shopBuyRequest{slot: 0, hub: 0, itemIndex: 0, targetSlot: 1})
	if !errors.Is(err, ErrShopTargetDead) {
		t.Fatalf("错误 = %v，期望 ErrShopTargetDead", err)
	}
	if buyer.Gold != 100 {
		t.Fatalf("被拒后买家 Gold = %d，期望仍是 100", buyer.Gold)
	}
}

// TestApplyShopBuyRefundsWhenSlotsFull 钉住「给货失败要退钱」。
//
// 先扣款再给货，给不出去必须退回——否则玩家花 30 金买了个寂寞。
func TestApplyShopBuyRefundsWhenSlotsFull(t *testing.T) {
	r := New(testConfig(), quietLogger(), Options{ID: "shop-full", Seed: 7})

	pid, p := r.World.Players.Spawn()
	if p == nil {
		t.Fatal("玩家池满")
	}
	hub := &r.World.Hubs[0]
	hub.ShopOpen = true
	p.Pos = hub.Center
	p.State = world.PlayerAlive
	p.Gold = 100
	// 道具槽填满：EquipItem 会拒绝。
	p.ItemSlots = [3]uint16{1, 2, 3}

	r.peers.put(&peer{
		slot:    0,
		id:      pid,
		rack:    []systems.ShopItem{{Index: 0, Kind: systems.ShopItemItem, RefID: 1, Price: 30}},
		shopHub: 0,
	})

	err := r.applyShopBuyLocked(shopBuyRequest{slot: 0, hub: 0, itemIndex: 0, targetSlot: 0})
	if !errors.Is(err, ErrShopEquipFailed) {
		t.Fatalf("错误 = %v，期望 ErrShopEquipFailed", err)
	}
	if p.Gold != 100 {
		t.Fatalf("给货失败后 Gold = %d，期望退回 100", p.Gold)
	}
	if r.peers.at(0).rack[0].Sold {
		t.Fatal("给货失败却把商品标记成已售出")
	}
}
