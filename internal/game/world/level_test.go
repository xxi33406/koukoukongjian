package world

import "testing"

// 池的契约就是"清零后交出去"：Pool.Spawn 里 `var zero T` 把整个槽位
// 归零（pool.go 的注释解释了为什么必须归零——复用槽位时上一轮的字段
// 残留会造出"新怪继承旧怪狂暴状态"这类幽灵 bug）。
//
// 所以等级字段在池这一层必须是 0，而不是 1。Level=1 是"出生"这件事的
// 语义，出生初始化归调用方（internal/room/net.go 的 handleJoin，与
// p.WeaponID = systems.DefaultWeaponID 同一处）。
//
// 这条测试守的就是这个分工：如果哪天有人"顺手"把 1 写进 Pool.Spawn，
// 池就不再是通用容器了，子弹池/拾取物池的零值语义也会被拖下水。
func TestPlayerLevelFieldsZeroedOnSpawn(t *testing.T) {
	w := newWorldForTest()
	_, p := w.Players.Spawn()
	if p.Level != 0 {
		t.Fatalf("Spawn 后 Level = %d，期望 0（池只负责清零，Level=1 由 handleJoin 设）", p.Level)
	}
	if p.Exp != 0 {
		t.Fatalf("Spawn 后 Exp = %d，期望 0", p.Exp)
	}
	if p.ExpToNext != 0 {
		t.Fatalf("Spawn 后 ExpToNext = %d，期望 0（由 handleJoin 按曲线首级填充）", p.ExpToNext)
	}
}

// 复用槽位必须回到零值：上一局 7 级的老玩家死掉、下一个人坐同一个槽位，
// 若等级不清零，新人一出生就是 7 级。这正是 pool.go 用 `var zero T` 的
// 理由，这条测试是它在等级字段上的直接证据。
func TestPoolRespawnResetsLevel(t *testing.T) {
	w := newWorldForTest()
	id, p := w.Players.Spawn()
	p.Level = 7
	p.Exp = 99
	p.ExpToNext = 512

	if !w.Players.Kill(id) {
		t.Fatal("Kill 应成功回收存活实体")
	}

	id2, p2 := w.Players.Spawn()
	if p2 == nil {
		t.Fatal("回收后应能再 Spawn 到槽位")
	}
	// 同一槽位才会复用同一块 data，世代必须已经递增。
	if id2.Index() != id.Index() {
		t.Fatalf("期望复用槽位 %d，实际拿到 %d", id.Index(), id2.Index())
	}
	if p2.Level != 0 || p2.Exp != 0 || p2.ExpToNext != 0 {
		t.Fatalf("复用槽位 Level=%d Exp=%d ExpToNext=%d，期望 0/0/0（池必须整个清零）",
			p2.Level, p2.Exp, p2.ExpToNext)
	}
}
