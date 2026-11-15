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
	ents := map[uint32]entInfo{
		5:  {kind: proto.EntityKind_ENTITY_KIND_BULLET, x: 66.0, y: 64.0},
		2:  {kind: proto.EntityKind_ENTITY_KIND_PLAYER, x: 64.0, y: 64.0},
		7:  {kind: proto.EntityKind_ENTITY_KIND_MONSTER, x: 80.5, y: 88.5},
		9:  {kind: proto.EntityKind_ENTITY_KIND_MONSTER, x: 10.0, y: 11.0},
		11: {kind: proto.EntityKind_ENTITY_KIND_MONSTER, x: 12.0, y: 13.0},
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
	b := &bot{entities: map[uint32]entInfo{}}
	b.applyDelta(&proto.EntityDelta{
		EntityId: 7,
		Kind:     proto.EntityKind_ENTITY_KIND_MONSTER,
		Pos:      &proto.Vec2Q{X: proto.QuantizePos(80.5), Y: proto.QuantizePos(88.5)},
	})
	if e := b.entities[7]; e.x != 80.5 || e.y != 88.5 || kindChar(e.kind) != 'M' {
		t.Fatalf("首次写入 = %+v，期望 (80.5,88.5) M", e)
	}

	b.applyDelta(&proto.EntityDelta{EntityId: 7, Kind: proto.EntityKind_ENTITY_KIND_MONSTER})
	e := b.entities[7]
	if e.x != 80.5 || e.y != 88.5 {
		t.Fatalf("Pos=nil 的增量被当成 (0,0)：(%v,%v)，期望保持 (80.5,88.5)", e.x, e.y)
	}
}
