package main

import (
	"fmt"
	"sort"
	"strings"

	"koukoukongjian/internal/proto"
)

// bot 侧的轻量实体表：把快照流（adds/updates/removes）重放成"当前在场
// 实体"视图，供 -v 的实体行每秒打印。bot 不做预测也不插值，服务器发什么
// 就显示什么——它是灰盒观测工具，不是客户端实现。
//
// 服务器的 delta 恒带 Kind+Pos（internal/snapshot/snapshot.go 的
// playerDelta/monsterDelta 等都填全字段），全量帧每 20 tick 自愈一次，
// 所以这张表不需要处理"增量缺 Kind"之外的空缺；Pos 为 nil 时保留旧值
// 只是对协议允许形态的防御。

// entityRef 是实体表的主键：池种类 + 池内下标。
//
// 必须带 kind（同一套教训见 internal/snapshot 的 seenKey、internal/room
// 的 combat_test.go）：四个实体池各自从索引 1 开始编号（world.NewPool
// 保留 0 号槽给 InvalidID），所以同一个 uint32 可以同时是"玩家 1"和
// "怪物 1"。裸 ID 做键时后写覆盖先写——M3 审查实测到 bot 表里 M 条目被
// P 覆盖，长跑后 M 计数偏低，看起来像波次没刷怪。
type entityRef struct {
	kind proto.EntityKind
	id   uint32
}

// entInfo 是实体表里的一行。
//
// 不含 kind：kind 已是键的一部分，再存一份就有两个真相，二者不一致时
// 渲染出来的字母会和键打架。要展示种类时读 entityRef.kind。
type entInfo struct {
	x, y float64
}

// applySnapshot 把一份快照重放进实体表。
//
// 顺序是协议语义的一部分，不能换：
//  1. FULL 帧先清表——全量是丢包自愈的唯一机会（快照走不可靠 UDP，
//     client.go:167 实证会丢），remove 单发不补，丢一个就永久幽灵；
//  2. 再按 adds 重填（全量帧的在场实体全在 adds 里）；
//  3. 增量帧并入 adds/updates；
//  4. 最后处理 removes——放在 adds 之后，保证"同一帧里删掉又被重新生成"
//     的实体（池复用同一槽位）以新数据为准。
func (b *bot) applySnapshot(snap *proto.Snapshot) {
	if snap.Full {
		// 只清不重分配：clear 保留桶，避免每 20 tick 一次的全量帧制造垃圾。
		clear(b.entities)
	}
	for _, d := range snap.Adds {
		b.applyDelta(d)
	}
	for _, d := range snap.Updates {
		b.applyDelta(d)
	}
	for i, id := range snap.Removes {
		var kind proto.EntityKind
		if i < len(snap.RemovesKind) {
			kind = snap.RemovesKind[i]
		}
		b.removeEntity(kind, id)
	}
}

// removeEntity 删掉一条实体。
//
// kind 不可用（removes_kind 缺失/截断，或服务端给了 UNSPECIFIED）时退化成
// "按裸 ID 删掉四个池里所有同号条目"：宁可多删——多删的实体下一帧全量里
// 会以 adds 回来；少删就是永久幽灵，而那正是这张表要修的病。
func (b *bot) removeEntity(kind proto.EntityKind, id uint32) {
	if kind != proto.EntityKind_ENTITY_KIND_UNSPECIFIED {
		delete(b.entities, entityRef{kind: kind, id: id})
		return
	}
	for _, k := range knownKinds {
		delete(b.entities, entityRef{kind: k, id: id})
	}
}

// knownKinds 是协议里全部实体种类（不含 UNSPECIFIED）。
//
// 用显式列表而不是遍历枚举的 map：这里只在降级路径上跑，但要的是
// "确定性顺序 + 编译期可见的种类全集"，枚举遍历拿不到前者。
var knownKinds = []proto.EntityKind{
	proto.EntityKind_ENTITY_KIND_PLAYER,
	proto.EntityKind_ENTITY_KIND_MONSTER,
	proto.EntityKind_ENTITY_KIND_BULLET,
	proto.EntityKind_ENTITY_KIND_PICKUP,
}

// applyDelta 把一条实体增量并进实体表。
//
// Kind 为 UNSPECIFIED 时仍按 UNSPECIFIED 建条目而不是丢弃：这条增量本来
// 就不该出现（服务端 delta 恒填 Kind），静默丢弃会让"实体在画面上存在、
// 表里却没有"变成查不出的悬案；留着它，-v 行里的 '?' 就是最直接的线索。
func (b *bot) applyDelta(d *proto.EntityDelta) {
	ref := entityRef{kind: d.Kind, id: d.EntityId}
	e := b.entities[ref]
	if d.Pos != nil {
		e.x = proto.DequantizePos(d.Pos.X)
		e.y = proto.DequantizePos(d.Pos.Y)
	}
	b.entities[ref] = e
}

// kindChar 是 proto.EntityKind → 单字符缩写的映射。
//
// PICKUP 取 U（P 已被玩家占用）；未知种类打 ?，宁可日志里有个问号也别
// 静默吞掉新实体类型。
func kindChar(k proto.EntityKind) byte {
	switch k {
	case proto.EntityKind_ENTITY_KIND_PLAYER:
		return 'P'
	case proto.EntityKind_ENTITY_KIND_MONSTER:
		return 'M'
	case proto.EntityKind_ENTITY_KIND_BULLET:
		return 'B'
	case proto.EntityKind_ENTITY_KIND_PICKUP:
		return 'U'
	default:
		return '?'
	}
}

// formatEntities 把实体表渲染成一行摘要，按 (id, kind) 升序取前 limit 个。
//
// 固定前缀 "entities:" 是为了让验证脚本 grep 得稳；空表时只打前缀。
// 同号时按 kind 升序破平：map 遍历随机，不定序会让同一帧两次打印的行
// 顺序不同，验证脚本的账号比对就失去意义。
func formatEntities(ents map[entityRef]entInfo, limit int) string {
	if limit <= 0 {
		limit = 8
	}
	refs := make([]entityRef, 0, len(ents))
	for ref := range ents {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].id != refs[j].id {
			return refs[i].id < refs[j].id
		}
		return refs[i].kind < refs[j].kind
	})
	if len(refs) > limit {
		refs = refs[:limit]
	}

	var sb strings.Builder
	sb.WriteString("entities:")
	for _, ref := range refs {
		e := ents[ref]
		fmt.Fprintf(&sb, " %c%d(%.1f,%.1f)", kindChar(ref.kind), ref.id, e.x, e.y)
	}
	return sb.String()
}
