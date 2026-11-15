package systems

import (
	"testing"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/game/world"
)

// BenchmarkCollide 80 怪 × 32 弹规模（spec 单波 4 怪 × 8 波在场的上界再放宽）。
// 目的：证明暴力遍历在灰盒规模够用（combat-design §8 #7：42.7ns 量级），
// 若 M5 上量后超标，换空间网格时接口不变只换内部。
//
// M2 决策时没有留下实测数字，这里是补承诺：数字写进汇报与 commit 正文。
func BenchmarkCollide(b *testing.B) {
	cfg := config.Default()
	cfg.Map.HubCount = 0 // 空白世界：无墙干扰，纯碰撞开销
	w := world.NewWorld(cfg, 1)

	for i := 0; i < 80; i++ {
		id, m := w.Monsters.Spawn()
		if id == world.InvalidID {
			b.Fatal("怪物池满")
		}
		m.Pos = world.V(float64(i%10)*2+1, float64(i/10)*2+1)
		m.HP, m.MaxHP, m.Radius = 50, 50, 0.4
	}
	w.Players.Spawn() // Collide 不看玩家，但保持世界形状一致

	for i := 0; i < 32; i++ {
		id, bl := w.Bullets.Spawn()
		if id == world.InvalidID {
			b.Fatal("子弹池满")
		}
		bl.Pos = world.V(float64(i)*0.5, float64(i%10))
		bl.Vel = world.V(20, 0)
		bl.Radius = 0.12
		bl.Life = 10
		bl.Damage = 10
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// 每轮把 PrevPos 拉回 Pos，避免子弹真的飞走导致命中集漂移。
		w.Bullets.Each(func(_ world.EntityID, bl *world.BulletState) bool {
			bl.PrevPos = bl.Pos
			return true
		})
		_ = Collide(w)
	}
}
