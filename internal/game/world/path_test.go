package world

import "testing"

func TestFindPathStraightOpen(t *testing.T) {
	tr := NewTerrain(16, 16, 1.0) // 全空地
	path := FindPath(tr, V(2.5, 2.5), V(12.5, 12.5))
	if len(path) != 2 {
		t.Errorf("空地直线路径应压缩成 2 点（起+终），实际 %d 点：%v", len(path), path)
	}
	if path[0] != (V(2.5, 2.5)) || path[len(path)-1] != V(12.5, 12.5) {
		t.Errorf("路径端点错误：%v → %v", path[0], path[len(path)-1])
	}
}

func TestFindPathAroundWall(t *testing.T) {
	tr := NewTerrain(16, 16, 1.0)
	// 竖墙 x=8，y∈[2,13)（两端留口）。
	for cy := 2; cy < 13; cy++ {
		tr.Set(8, cy, TileWall)
	}
	path := FindPath(tr, V(4.5, 7.5), V(12.5, 7.5))
	if path == nil {
		t.Fatal("绕墙路径应存在")
	}
	if path[len(path)-1] != V(12.5, 7.5) {
		t.Errorf("终点必须是目标：%v", path[len(path)-1])
	}
	// 每个拐点必须可走（string pulling 只跳视线可达的点）。
	for i, p := range path {
		if !tr.WalkableAt(p) {
			t.Errorf("拐点 %d %v 落在墙里", i, p)
		}
	}
	// 相邻拐点之间必须视线可走（string pulling 的正确性定义）。
	for i := 1; i < len(path); i++ {
		if !tr.LineWalkable(path[i-1], path[i]) {
			t.Errorf("拐点 %d→%d（%v→%v）视线被墙挡住", i-1, i, path[i-1], path[i])
		}
	}
}

func TestFindPathUnreachable(t *testing.T) {
	tr := NewTerrain(16, 16, 1.0)
	// 全封闭墙圈。
	for i := 0; i < 16; i++ {
		tr.Set(i, 8, TileWall)
	}
	if path := FindPath(tr, V(4.5, 4.5), V(4.5, 12.5)); path != nil {
		t.Errorf("被墙隔死时应返回 nil，实际 %v", path)
	}
}

func TestFindPathStartInWall(t *testing.T) {
	tr := NewTerrain(16, 16, 1.0)
	tr.Set(4, 4, TileWall)
	if path := FindPath(tr, V(4.5, 4.5), V(12.5, 12.5)); path != nil {
		t.Errorf("起点在墙内应返回 nil，实际 %v", path)
	}
}

func TestFindPathStartEqualsGoal(t *testing.T) {
	tr := NewTerrain(16, 16, 1.0)
	a := V(4.5, 4.5)
	path := FindPath(tr, a, a)
	// 起点即终点：A* 首次展开即命中目标，回溯只有起点一格，
	// 压缩后返回 [a]——调用方拿到的路径含起点（path.go 契约），长度 1。
	if len(path) != 1 || path[0] != a {
		t.Errorf("起点==终点应返回 [a]，实际 %v", path)
	}
}

func TestCompressPathKeepsEndpointsAndSight(t *testing.T) {
	tr := NewTerrain(16, 16, 1.0)
	grid := []Vec2{V(1.5, 1.5), V(1.5, 8.5), V(5.5, 8.5), V(5.5, 12.5)}
	got := compressPath(tr, grid)
	if got[0] != grid[0] || got[len(got)-1] != grid[len(grid)-1] {
		t.Errorf("压缩必须保留首尾：%v", got)
	}
	if len(got) > len(grid) {
		t.Errorf("压缩只能变短：%d → %d", len(grid), len(got))
	}
}
