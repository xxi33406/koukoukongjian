package room

import (
	"testing"
	"time"

	"koukoukongjian/internal/game/systems"
	"koukoukongjian/internal/game/world"
	"koukoukongjian/internal/proto"
)

// 本文件是 M4 Task 10 的接线测试：交互队列 → 开背包引导，以及 Task 9 的
// UpdatePickups 在 tick 里真的被调到了。
//
// 为什么 room 层还要再测一遍（systems 层已有 6 个用例）：systems 的用例
// 直接调函数，绕过了"bridge 投递 → tick 消费 → 世界被改 → 快照下发"这条
// 真实路径。Task 9 交付 UpdatePickups 时它一个生产调用方都没有，那种状态
// 下 systems 层的绿灯说明不了任何事（AGENTS.md §1.2「单元测试绕过了真实
// 路径」）。这里的用例只做一件事：确认这些函数被房间真的调到了。
//
// 并发约束（AGENTS.md §2.5）：观察一律走快照——PlayerState 只在 tick
// goroutine 里安全，模块测试里读它就是数据竞争。

// selfBusy 从快照里读自己身上的"忙"标记位（开背包引导中）。
func selfBusy(s *proto.Snapshot) bool {
	return s != nil && s.Self != nil &&
		s.Self.Flags&uint32(world.PlayerFlagBusy) != 0
}

// selfGold 从快照里读自己的金币。
func selfGold(s *proto.Snapshot) uint32 {
	if s == nil || s.Self == nil {
		return 0
	}
	return s.Self.Gold
}

// TestInteractOpenInventorySetsAndClearsBusy 验证一次开背包交互会让玩家
// 在引导期间带上 Busy 标记，并在引导结束后自动清掉。
//
// 断言"自动清掉"是关键的一半：只断言置位的话，一个永远置位的实现（引导
// 永不完成）也能过，而那正是玩家最不能接受的失败方式——背包打不开，且
// 从此一直被当成"忙"。
func TestInteractOpenInventorySetsAndClearsBusy(t *testing.T) {
	r, _ := startRoomWith(t, "inventory", nil)
	res, sink := joinPlayer(t, r, "looter")

	// 先确认起步状态是"不忙"，否则后面读出 Busy 分不清是自己置的还是残留。
	waitSnapshot(t, sink, 2*time.Second, "加入后第一帧不带 Busy", func(s *proto.Snapshot) bool {
		return s.Self != nil && !selfBusy(s)
	})

	if err := r.Interact(res.Slot, InteractKindOpenInventory); err != nil {
		t.Fatalf("开背包交互失败：%v", err)
	}

	// Interact 已等到 tick 循环的回执，此刻引导已开始，下一帧就该看到位。
	waitSnapshot(t, sink, time.Second, "开背包引导中带上 Busy", selfBusy)

	// 引导 2 秒（systems.OpenInventorySeconds），20Hz 下 40 tick。超时给
	// 3 秒：这里要证明的是"会结束"，不是"恰好 2.000 秒结束"——时长本身
	// 由 systems 的 TestOpenInventoryTakesTime 按 tick 数精确验证。
	waitSnapshot(t, sink, 3*time.Second, "引导结束后 Busy 被清掉", func(s *proto.Snapshot) bool {
		return s.Self != nil && !selfBusy(s)
	})
}

// TestInteractRejectsUnknownKind 验证越界 kind 在入口就被拒。
//
// 这条守的是"协议前向兼容"：proto 注释写明「其余值服务端忽略」，若入口
// 不拦、让它排进队列，tick 循环里那条拒绝路径与真正的内部错误在日志上
// 长得一样，联调时会误判成房间逻辑出错。
func TestInteractRejectsUnknownKind(t *testing.T) {
	r, _ := startRoomWith(t, "badkind", nil)
	res, _ := joinPlayer(t, r, "confused")

	if err := r.Interact(res.Slot, 99); err != ErrInteractKind {
		t.Fatalf("越界 kind 的错误是 %v，期望 %v", err, ErrInteractKind)
	}
	// M4 里 kind=1（拾取最近）是自动拾取的冗余入口，必须成功返回而不是
	// 报错——客户端按交互键不该收到一个失败回执。
	if err := r.Interact(res.Slot, InteractKindPickupNearest); err != nil {
		t.Fatalf("kind=1 应当成功返回，实得 %v", err)
	}
}

// TestPickupsCollectedAutomatically 验证 UpdatePickups 真的被 tick 调到了，
// 且出生点上的金币会被自动捡走（Task 9 的函数此前没有生产调用方）。
func TestPickupsCollectedAutomatically(t *testing.T) {
	r, _ := startRoomWith(t, "autopickup", func(r *Room) {
		// 摆在座位 0 的出生点上：join 会把玩家放到同一个点，距离 0。
		p := r.spawnPoint(0)
		if id := systems.SpawnPickup(r.World, p, world.PickupGold, 0, 7); id == world.InvalidID {
			t.Fatalf("摆放掉落物失败")
		}
	})
	_, sink := joinPlayer(t, r, "picker")

	waitSnapshot(t, sink, 2*time.Second, "出生点的金币被自动捡走", func(s *proto.Snapshot) bool {
		return selfGold(s) == 7
	})
}
