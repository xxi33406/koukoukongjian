package main

import (
	"strings"
	"testing"

	"koukoukongjian/internal/proto"
)

// formatEntities / kindChar 是 bot -v 实体行的唯一逻辑单元，值得钉死格式：
// 端到端验证（M3 Task 11 Step 4）要靠 grep 这行的 M 计数判断波次是否刷怪，
// 格式漂移（空格、括号、前缀变了）会让验证脚本悄悄失配。

func TestFormatEntitiesSortsByIDAndTruncates(t *testing.T) {
	ents := map[entityRef]entInfo{
		{kind: proto.EntityKind_ENTITY_KIND_BULLET, id: 5}:   {x: 66.0, y: 64.0},
		{kind: proto.EntityKind_ENTITY_KIND_PLAYER, id: 2}:   {x: 64.0, y: 64.0},
		{kind: proto.EntityKind_ENTITY_KIND_MONSTER, id: 7}:  {x: 80.5, y: 88.5},
		{kind: proto.EntityKind_ENTITY_KIND_MONSTER, id: 9}:  {x: 10.0, y: 11.0},
		{kind: proto.EntityKind_ENTITY_KIND_MONSTER, id: 11}: {x: 12.0, y: 13.0},
	}

	got := formatEntities(ents, 3)
	want := "entities: P2(64.0,64.0) B5(66.0,64.0) M7(80.5,88.5)"
	if got != want {
		t.Fatalf("formatEntities = %q，期望 %q", got, want)
	}
	// 截断后不能有多余空格或尾部残留。
	if strings.HasSuffix(got, " ") {
		t.Fatal("实体行以空格结尾")
	}
}

func TestFormatEntitiesEmptyTable(t *testing.T) {
	// 还没收到任何实体时（比如第一份快照前）不应打出畸形行。
	if got := formatEntities(nil, 8); got != "entities:" {
		t.Fatalf("空表 = %q，期望 %q", got, "entities:")
	}
}

func TestKindCharCoversAllKinds(t *testing.T) {
	cases := []struct {
		k    proto.EntityKind
		want byte
	}{
		{proto.EntityKind_ENTITY_KIND_PLAYER, 'P'},
		{proto.EntityKind_ENTITY_KIND_MONSTER, 'M'},
		{proto.EntityKind_ENTITY_KIND_BULLET, 'B'},
		{proto.EntityKind_ENTITY_KIND_PICKUP, 'U'},
		{proto.EntityKind_ENTITY_KIND_UNSPECIFIED, '?'},
	}
	for _, c := range cases {
		if got := kindChar(c.k); got != c.want {
			t.Errorf("kindChar(%v) = %c，期望 %c", c.k, got, c.want)
		}
	}
}

func TestApplyDeltaUpdatesPosInPlace(t *testing.T) {
	// 增量帧只带变化字段是协议允许的，但本服务器的 delta 恒填 Kind+Pos；
	// Pos 为 nil 时必须保留旧坐标而不是把实体打成 (0,0)——那会让
	// 端到端日志里怪物瞬移到原点，看起来像刷怪失败。
	ref := entityRef{kind: proto.EntityKind_ENTITY_KIND_MONSTER, id: 7}
	b := &bot{entities: map[entityRef]entInfo{}}
	b.applyDelta(&proto.EntityDelta{
		EntityId: 7,
		Kind:     proto.EntityKind_ENTITY_KIND_MONSTER,
		Pos:      &proto.Vec2Q{X: proto.QuantizePos(80.5), Y: proto.QuantizePos(88.5)},
	})
	if e := b.entities[ref]; e.x != 80.5 || e.y != 88.5 || kindChar(ref.kind) != 'M' {
		t.Fatalf("首次写入 = %+v，期望 (80.5,88.5) M", e)
	}

	b.applyDelta(&proto.EntityDelta{EntityId: 7, Kind: proto.EntityKind_ENTITY_KIND_MONSTER})
	e := b.entities[ref]
	if e.x != 80.5 || e.y != 88.5 {
		t.Fatalf("Pos=nil 的增量被当成 (0,0)：(%v,%v)，期望保持 (80.5,88.5)", e.x, e.y)
	}
}

// TestEntityTableSeparatesPoolsWithSameID 钉死复合键：玩家 1 与怪物 1 并存。
//
// 根因（M3 审查实测）：四个实体池各自从索引 1 开始编号，同一个 uint32
// 可以同时是"玩家 1"和"怪物 1"。裸 ID 做键时后写覆盖先写——bot 表里那条
// M 条目被 P 覆盖，长跑后 M 计数偏低，看起来像"波次刷怪失败"。
func TestEntityTableSeparatesPoolsWithSameID(t *testing.T) {
	b := &bot{entities: map[entityRef]entInfo{}}
	b.applySnapshot(&proto.Snapshot{
		Full: true,
		Adds: []*proto.EntityDelta{
			{EntityId: 1, Kind: proto.EntityKind_ENTITY_KIND_PLAYER,
				Pos: &proto.Vec2Q{X: proto.QuantizePos(10), Y: proto.QuantizePos(11)}},
			{EntityId: 1, Kind: proto.EntityKind_ENTITY_KIND_MONSTER,
				Pos: &proto.Vec2Q{X: proto.QuantizePos(20), Y: proto.QuantizePos(21)}},
		},
	})

	if len(b.entities) != 2 {
		t.Fatalf("实体表 %d 条，期望 2（玩家 1 与怪物 1 各占一条）", len(b.entities))
	}
	p := b.entities[entityRef{kind: proto.EntityKind_ENTITY_KIND_PLAYER, id: 1}]
	m := b.entities[entityRef{kind: proto.EntityKind_ENTITY_KIND_MONSTER, id: 1}]
	if p.x != 10 || p.y != 11 {
		t.Fatalf("玩家 1 = (%v,%v)，期望 (10,11)", p.x, p.y)
	}
	if m.x != 20 || m.y != 21 {
		t.Fatalf("怪物 1 = (%v,%v)，期望 (20,21)——被玩家覆盖了", m.x, m.y)
	}

	// removes 必须带 kind：删掉怪物 1 不能把玩家 1 一起删掉。
	b.applySnapshot(&proto.Snapshot{
		Removes:     []uint32{1},
		RemovesKind: []proto.EntityKind{proto.EntityKind_ENTITY_KIND_MONSTER},
	})
	if len(b.entities) != 1 {
		t.Fatalf("删除后剩 %d 条，期望 1", len(b.entities))
	}
	if _, ok := b.entities[entityRef{kind: proto.EntityKind_ENTITY_KIND_PLAYER, id: 1}]; !ok {
		t.Fatal("删怪物 1 时把玩家 1 一起删掉了")
	}
}

// TestApplySnapshotFullRebuildsEntities 钉死全量帧的"清空重建"语义。
//
// 根因：快照走不可靠 UDP（client.go:167 实证丢包），remove 是单发不补的。
// 丢一个 remove 包，那条实体在 bot 表里就永久留存——增量合并永远等不到
// 第二次删除通知。全量帧是唯一的自愈机会；若客户端仍走增量合并，全量
// 里"没出现"的实体读起来和"这一帧没变化"完全一样，自愈就失效了。
func TestApplySnapshotFullRebuildsEntities(t *testing.T) {
	b := &bot{entities: map[entityRef]entInfo{}}

	// 第一帧建表：玩家 1、怪物 1、子弹 1。随后那颗子弹的 remove 包"丢了"
	// （测试里就是不调它），模拟真实丢包。
	b.applySnapshot(&proto.Snapshot{
		Full: true,
		Adds: []*proto.EntityDelta{
			{EntityId: 1, Kind: proto.EntityKind_ENTITY_KIND_PLAYER,
				Pos: &proto.Vec2Q{X: proto.QuantizePos(10), Y: proto.QuantizePos(11)}},
			{EntityId: 1, Kind: proto.EntityKind_ENTITY_KIND_MONSTER,
				Pos: &proto.Vec2Q{X: proto.QuantizePos(20), Y: proto.QuantizePos(21)}},
			{EntityId: 1, Kind: proto.EntityKind_ENTITY_KIND_BULLET,
				Pos: &proto.Vec2Q{X: proto.QuantizePos(30), Y: proto.QuantizePos(31)}},
		},
	})
	if len(b.entities) != 3 {
		t.Fatalf("首帧实体表 %d 条，期望 3", len(b.entities))
	}

	// 一次全量帧里只剩怪物 2：玩家 1 与子弹 1 都已离场但 remove 包丢了。
	b.applySnapshot(&proto.Snapshot{
		Full: true,
		Adds: []*proto.EntityDelta{
			{EntityId: 2, Kind: proto.EntityKind_ENTITY_KIND_MONSTER,
				Pos: &proto.Vec2Q{X: proto.QuantizePos(50), Y: proto.QuantizePos(51)}},
		},
	})

	if len(b.entities) != 1 {
		t.Fatalf("全量帧后实体表 %d 条，期望 1（幽灵条目必须被重建清掉）", len(b.entities))
	}
	if e, ok := b.entities[entityRef{kind: proto.EntityKind_ENTITY_KIND_MONSTER, id: 2}]; !ok || e.x != 50 {
		t.Fatalf("全量帧后只剩怪物 2，实际表 = %+v", b.entities)
	}

	// 增量帧不能清表：不全量时上面的幽灵仍会留着，这是协议允许的退化，
	// 但要钉住"只有 Full 才重建"——否则每帧清表 bot 就永远看不到持续实体。
	b.applySnapshot(&proto.Snapshot{
		Adds: []*proto.EntityDelta{
			{EntityId: 7, Kind: proto.EntityKind_ENTITY_KIND_BULLET,
				Pos: &proto.Vec2Q{X: proto.QuantizePos(1), Y: proto.QuantizePos(2)}},
		},
	})
	if len(b.entities) != 2 {
		t.Fatalf("增量帧后实体表 %d 条，期望 2（增量并入，不清表）", len(b.entities))
	}
}

// TestRemovesKindLengthMismatchFallsBackToBareID 钉死降级路径。
//
// removes_kind 与 removes 平行同序，正常永远等长。但"永远"是服务端的
// 承诺，客户端不能拿它当假设：真出现截断（老服务器、中间件改写）时，
// 不认识的 remove 宁可多删——下次全量帧会把还在场的实体补回来；反过来
// 少删就是永久幽灵。
func TestRemovesKindLengthMismatchFallsBackToBareID(t *testing.T) {
	b := &bot{entities: map[entityRef]entInfo{}}
	b.applySnapshot(&proto.Snapshot{
		Full: true,
		Adds: []*proto.EntityDelta{
			{EntityId: 4, Kind: proto.EntityKind_ENTITY_KIND_MONSTER,
				Pos: &proto.Vec2Q{X: proto.QuantizePos(1), Y: proto.QuantizePos(2)}},
			{EntityId: 4, Kind: proto.EntityKind_ENTITY_KIND_PLAYER,
				Pos: &proto.Vec2Q{X: proto.QuantizePos(3), Y: proto.QuantizePos(4)}},
		},
	})
	if len(b.entities) != 2 {
		t.Fatalf("实体表 %d 条，期望 2", len(b.entities))
	}

	// removes_kind 缺失（长度 0）：只能按裸 ID 把所有同号条目全删。
	b.applySnapshot(&proto.Snapshot{Removes: []uint32{4}})
	if len(b.entities) != 0 {
		t.Fatalf("裸 ID remove 后实体表 %d 条，期望 0（无法区分池时宁可多删）", len(b.entities))
	}
}
