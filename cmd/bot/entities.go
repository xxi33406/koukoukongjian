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

// entInfo 是实体表里的一行。
type entInfo struct {
	kind proto.EntityKind
	x, y float64
}

// applyDelta 把一条实体增量并进实体表。
func (b *bot) applyDelta(d *proto.EntityDelta) {
	e := b.entities[d.EntityId]
	e.kind = d.Kind
	if d.Pos != nil {
		e.x = proto.DequantizePos(d.Pos.X)
		e.y = proto.DequantizePos(d.Pos.Y)
	}
	b.entities[d.EntityId] = e
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

// formatEntities 把实体表渲染成一行摘要，按 id 升序取前 limit 个。
//
// 固定前缀 "entities:" 是为了让验证脚本 grep 得稳；空表时只打前缀。
func formatEntities(ents map[uint32]entInfo, limit int) string {
	if limit <= 0 {
		limit = 8
	}
	ids := make([]uint32, 0, len(ents))
	for id := range ents {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > limit {
		ids = ids[:limit]
	}

	var sb strings.Builder
	sb.WriteString("entities:")
	for _, id := range ids {
		e := ents[id]
		fmt.Fprintf(&sb, " %c%d(%.1f,%.1f)", kindChar(e.kind), id, e.x, e.y)
	}
	return sb.String()
}
