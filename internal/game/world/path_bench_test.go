package world

import "testing"

// BenchmarkFindPath 测单次寻路的耗时与分配。
//
// 存在的理由（M4 Task 11 第 4 项）：M3 审查指出 FindPath 每次调用都新建
// nodes/closed/pq/cells 四份切片，80 怪 × 每 0.3 秒重算 ≈ 264 次/秒，
// 但没有实测数字，「该不该复用缓冲」只能靠猜。这里给出实测依据。
//
// 实测结论（go1.27.3 / windows/amd64）：long 62032 B/op、861 allocs/op；
// near 46304 B/op、575 allocs/op。但分配的大头不是那四份切片——
// memprofile（-memprofilerate 1）显示 FindPath.func2（mkNode 闭包里每格
// 一个 &pathNode）占 alloc_space 的 64.86%，FindPath 本体（含那四份切片）
// 31.32%，pathPQ.Push 3.49%。复用切片的上限收益只是 FindPath 本体那 31%
// 里的一小块，而占 65% 的「每格一次堆分配」必须把 pathNode 改成值切片 +
// 下标链接才能消掉，那是重写 A* 核心，不属于"复用缓冲"。所以 Task 11 不做
// 这个改动，留 M5（见 docs/plans/2026-11-17-m4-progression.md）；保留本
// 基准是为了让接手的人能直接复现这组数字，而不是重新论证一遍。
//
// 两个子场景分开测，因为它们的开销来源完全不同：
//   - long：40×40 格地形、横穿全图绕墙。A* 的开表/闭表/回溯/string
//     pulling 四条路径全走到，是上界。
//   - near：同一张图上的近距离目标（10 格内）。ai 的感知半径 20 m、
//     怪物真正的追击目标通常就在这个量级，是常态。
//
// 用地形当"绕墙"而不是直线可达：直线可达的地形上 string pulling 一步
// 到位，测不出 A* 主体的开销。
func BenchmarkFindPath(b *testing.B) {
	tr := NewTerrain(40, 40, 1.0)
	for cy := 1; cy < 38; cy++ {
		tr.Set(20, cy, TileWall)
	}

	cases := []struct {
		name    string
		a, goal Vec2
	}{
		{"long", V(4.5, 20.5), V(35.5, 20.5)},
		{"near", V(16.5, 20.5), V(24.5, 20.5)},
	}
	for _, c := range cases {
		if p := FindPath(tr, c.a, c.goal); p == nil {
			b.Fatalf("%s：基准地形不可达，测试前提失效", c.name)
		}
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if FindPath(tr, c.a, c.goal) == nil {
					b.Fatal("路径丢失")
				}
			}
		})
	}
}
