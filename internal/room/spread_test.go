package room

import (
	"testing"
	"time"

	"koukoukongjian/internal/game/world"
	"koukoukongjian/internal/proto"
)

// TestRoomSpreadEnragesMonsters 钉住距离惩罚真的接进了 tick 循环。
//
// M6 Task 11 的变异验证暴露过一次缺口：把 room.go 里那两行
// UpdatePartySpread / ApplySpreadToMonsters 删掉，systems 层的 15 个用例
// 与 ai 层 6 个用例仍然全绿——它们测的是纯函数，没人验证「谁在 tick 里
// 调它」。表现到玩家侧就是：全队被拆散，怪物毫无反应，而测试一片绿。
//
// 断言走快照里的 flags，不是直接读 r.World：tick goroutine 独占 World，
// 测试 goroutine 直接读是数据竞争（-race 会报）。
func TestRoomSpreadEnragesMonsters(t *testing.T) {
	var mid world.EntityID

	// 第二个玩家拉到 60 米外（> 45 米极端线）。摆位在 setup 回调里做：
	// 那时 Run 还没起来，独占权还在测试手里。
	r, _ := startRoomWith(t, "spread-enrage", func(r *Room) {
		mid = spawnTarget(r, r.World.Hubs[r.World.SpawnHub].Center.Add(world.V(3, 0)), 50)
		_, second := r.World.Players.Spawn()
		if second == nil {
			t.Fatal("玩家池满，无法摆第二个玩家")
		}
		second.State = world.PlayerAlive
		second.HP, second.MaxHP = 100, 100
		second.Pos = r.World.Hubs[r.World.SpawnHub].Center.Add(world.V(60, 0))
	})
	_, sink := joinPlayer(t, r, "near")

	waitSnapshot(t, sink, 3*time.Second, "怪物进入狂暴（flags 含 Enraged）", func(s *proto.Snapshot) bool {
		e, ok := snapshotEntities(s)[entityRef{proto.EntityKind_ENTITY_KIND_MONSTER, uint32(mid)}]
		if !ok {
			return false
		}
		return e.Flags&uint32(world.MonsterFlagEnraged) != 0
	})
}

// TestRoomSpreadTogetherKeepsMonstersNormal 是对照组：全队在一起时检查
// 不到狂暴位，证明上一条不是「所有怪永远带 Enraged」。
func TestRoomSpreadTogetherKeepsMonstersNormal(t *testing.T) {
	var mid world.EntityID

	r, _ := startRoomWith(t, "spread-together", func(r *Room) {
		mid = spawnTarget(r, r.World.Hubs[r.World.SpawnHub].Center.Add(world.V(3, 0)), 50)
	})
	_, sink := joinPlayer(t, r, "solo")

	// 先等这个怪被看到（它一定会出现，flags=0）。
	waitSnapshot(t, sink, 2*time.Second, "怪物出现在快照里", func(s *proto.Snapshot) bool {
		_, ok := snapshotEntities(s)[entityRef{proto.EntityKind_ENTITY_KIND_MONSTER, uint32(mid)}]
		return ok
	})

	// 再确认后续若干帧里都没带 Enraged。单帧检查不够：标志位是每 tick
	// 重写的，只有「一段时间内从来没出现过」才能说明判据没误触发。
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case data := <-sink:
			s := decodeSnapshot(t, data)
			if e, ok := snapshotEntities(s)[entityRef{proto.EntityKind_ENTITY_KIND_MONSTER, uint32(mid)}]; ok {
				if e.Flags&uint32(world.MonsterFlagEnraged) != 0 {
					t.Fatalf("全队在一起时怪物带上了 Enraged 位（flags=%b）", e.Flags)
				}
			}
		case <-deadline:
			return
		}
	}
}
