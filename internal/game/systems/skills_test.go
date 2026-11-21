package systems

import (
	"math"
	"testing"

	"koukoukongjian/internal/game/ai"
	"koukoukongjian/internal/game/build"
	"koukoukongjian/internal/game/world"
)

// equipPassive 把技能装进玩家的被动槽并重算派生属性。
//
// 重算是必须的显式一步：PlayerState.Derived 的零值是「全 0 倍率」，
// 装配技能只写槽位不改属性，属性由 RecomputeDerived 全量重算。
func equipPassive(p *world.PlayerState, slot int, id uint16) {
	p.SkillPassive[slot] = id
	RecomputeDerived(p)
}

// equipActive 把技能装进玩家的主动槽并重算派生属性。
func equipActive(p *world.PlayerState, slot int, id uint16) {
	p.SkillActive[slot] = id
	RecomputeDerived(p)
}

// firstBullet 取出池里第一颗子弹。
func firstBullet(t *testing.T, w *world.World) *world.BulletState {
	t.Helper()
	var b *world.BulletState
	w.Bullets.Each(func(_ world.EntityID, bs *world.BulletState) bool {
		b = bs
		return false
	})
	if b == nil {
		t.Fatal("子弹池为空")
	}
	return b
}

// —— 被动：改属性 ——

func TestPassiveDamageMulApplies(t *testing.T) {
	// 1 号被动「强化弹头」×1.2：步枪 10 伤应变成 12。
	w := newSystemsWorld(t, 3)
	id, p := spawnPlayerAt(t, w, 10, 10)
	p.WeaponID = WeaponRifle

	// 先看对照组：没装技能时子弹必须是原值 10。
	// 这一半守的是 Derived 零值兜底（world.PlayerDerived.Effective）——
	// 若兜底漏了，未重算的玩家会打出 0 伤子弹，而"12 变成 0"与"12 正确"
	// 在只看后者时都像"倍率没生效"，分不开是漏乘还是乘了 0。
	if !Fire(w, id, p, true, true, 0.05) {
		t.Fatal("未装技能却没能开火")
	}
	if got := firstBullet(t, w).Damage; math.Abs(got-10.0) > 1e-9 {
		t.Fatalf("未装技能子弹伤害 = %v，期望 10.0（基准值）", got)
	}
	w.Bullets.Reset()
	p.FireCooldown = 0

	equipPassive(p, 0, build.SkillIDAmmoUp)
	if !Fire(w, id, p, true, true, 0.05) {
		t.Fatal("装了强化弹头却没能开火")
	}
	b := firstBullet(t, w)
	if math.Abs(b.Damage-12.0) > 1e-9 {
		t.Fatalf("子弹伤害 = %v，期望 12.0（步枪 10 × 1.2）", b.Damage)
	}
}

func TestPassiveMaxHPAdds(t *testing.T) {
	// 2 号被动「强健体魄」+25 血：1 级基线 100 → 125。
	w := newSystemsWorld(t, 3)
	_, p := spawnPlayerAt(t, w, 10, 10)
	equipPassive(p, 0, build.SkillIDToughBody)

	if math.Abs(p.MaxHP-125) > 1e-9 {
		t.Fatalf("MaxHP = %v，期望 125（基线 100 + 25）", p.MaxHP)
	}

	// 等级项一起算：3 级 = 100 + 10×2 + 25 = 145。
	// 不测这一条，`Level > 1` 那个保护写反了（比如写成乘 Level-1 却漏了
	// uint8 下溢保护）也抓不到——1 级的加成恰好是 0，两种写法结果一样。
	p.Level = 3
	RecomputeDerived(p)
	if math.Abs(p.MaxHP-145) > 1e-9 {
		t.Fatalf("3 级 MaxHP = %v，期望 145（100 + 10×2 + 25）", p.MaxHP)
	}

	// 0 级（Pool.Spawn 的原始状态）不得下溢：100 + 25 = 125。
	// Level 是 uint8，直接算 float64(p.Level-1) 会让 0-1 回绕成 255，
	// 把这个玩家的血上限抬到 2650。
	p.Level = 0
	RecomputeDerived(p)
	if math.Abs(p.MaxHP-125) > 1e-9 {
		t.Fatalf("0 级 MaxHP = %v，期望 125（等级项不得为负）", p.MaxHP)
	}
}

func TestPassiveDamageTakenMul(t *testing.T) {
	// 4 号被动「吸收护盾」×0.8：怪物 10 点攻击只该打掉 8 点。
	w := newSystemsWorld(t, 3)
	// 先占掉玩家池的 1 号槽，让被测玩家拿到 2 号槽。
	//
	// 原因：PlayerState 与 MonsterState 两个池的 EntityID 编码完全相同
	// （都从 idx=1 起、世代同位置，world/entity.go），而 ResolveDamage
	// 先查怪物池再查玩家池（world/damage.go:55-72）。玩家与怪物恰好共用
	// 同一个索引时，打向玩家的伤害会被路由到那只怪身上——HP 一点不掉，
	// 症状是"减伤被动看起来完全没生效"。这是既有设计问题（两个池共用一个
	// ID 空间），不在 Task 5 的范围内修，测试里错开索引避开它。
	spawnPlayerAt(t, w, 0, 0) // 占位：idx=1
	pid, p := spawnPlayerAt(t, w, 10, 10)
	equipPassive(p, 0, build.SkillIDShield)

	// 走真实的怪物攻击路径（ai.UpdateMonster → ResolveDamage），
	// 而不是在测试里手工乘倍率：手工乘只能证明字段存在，证明不了
	// 那条路径真的乘了。
	mid := spawnMonsterAt(t, w, 11, 10) // 1 m，已进攻击范围（1.2 + 半径）
	m := w.Monsters.Get(mid)
	m.TargetID = pid
	m.State = world.MonsterAttack
	w.Tick = 1000 // 攻击冷却（1 s × 20 Hz = 20 tick）早已满足

	ai.UpdateMonster(w, m, 0.05)

	if got := 100 - p.HP; math.Abs(got-8) > 1e-9 {
		t.Fatalf("掉了 %v 点血，期望 8（10 × 0.8）；HP = %v", got, p.HP)
	}
}

func TestPassivesIdempotent(t *testing.T) {
	// 重算而非增量改：连续重算三次，结果必须与第一次逐位相同。
	w := newSystemsWorld(t, 3)
	_, p := spawnPlayerAt(t, w, 10, 10)
	equipPassive(p, 0, build.SkillIDAmmoUp)
	equipPassive(p, 1, build.SkillIDToughBody)

	wantMax := p.MaxHP
	want := p.Derived
	for i := 0; i < 3; i++ {
		RecomputeDerived(p)
	}

	if math.Abs(p.MaxHP-wantMax) > 1e-9 {
		t.Fatalf("重算 3 次后 MaxHP = %v，期望恒为 %v", p.MaxHP, wantMax)
	}
	got := p.Derived
	if math.Abs(got.DamageMul-want.DamageMul) > 1e-9 ||
		math.Abs(got.SpeedMul-want.SpeedMul) > 1e-9 ||
		math.Abs(got.DamageTakenMul-want.DamageTakenMul) > 1e-9 ||
		math.Abs(got.MaxHPBonus-want.MaxHPBonus) > 1e-9 {
		t.Fatalf("重算 3 次后 Derived = %+v，期望恒为 %+v", got, want)
	}
}

// TestBulletDamageSnapshottedAtFire 钉住"伤害倍率在出膛时快照"这条设计。
//
// 倍率乘在 spawnBullet 而不是 ApplyHits：子弹是独立实体，它带着"出膛时是
// 多少伤"飞出去。发射后立刻卸下被动（技能可换，spec L37），已经在飞的
// 那颗子弹必须保持 12 伤——否则「装强化弹头时打出、飞行途中被换掉」的
// 子弹会中途掉伤害，玩家看到的是"这一发突然变弱了"。
//
// 这个用例同时守住"只乘一次"：若有人在 ApplyHits 里也乘一遍，子弹出膛
// 12 再乘 1.2 就变 14.4，这条断言会立刻红。
func TestBulletDamageSnapshottedAtFire(t *testing.T) {
	w := newSystemsWorld(t, 3)
	id, p := spawnPlayerAt(t, w, 10, 10)
	p.WeaponID = WeaponRifle
	equipPassive(p, 0, build.SkillIDAmmoUp)

	if !Fire(w, id, p, true, true, 0.05) {
		t.Fatal("装了强化弹头却没能开火")
	}
	b := firstBullet(t, w)

	// 发射后卸下被动并重算（相当于游戏中换掉技能）。
	p.SkillPassive[0] = 0
	RecomputeDerived(p)
	if math.Abs(p.Derived.DamageMul-1) > 1e-9 {
		t.Fatalf("卸下后 DamageMul = %v，期望 1", p.Derived.DamageMul)
	}

	if math.Abs(b.Damage-12.0) > 1e-9 {
		t.Fatalf("飞行中子弹伤害 = %v，期望仍为 12.0（出膛时快照）", b.Damage)
	}
}

// —— 主动：可释放 ——

func TestActiveBurstDamagesNearby(t *testing.T) {
	// 6 号「环形爆发」：半径 4 m 内 25 伤，半径外不吃。
	w := newSystemsWorld(t, 3)
	pid, p := spawnPlayerAt(t, w, 10, 10)
	equipActive(p, 0, build.SkillIDBurst)

	nearID := spawnMonsterAt(t, w, 12, 10) // 2 m
	farID := spawnMonsterAt(t, w, 16, 10)  // 6 m
	near := w.Monsters.Get(nearID)
	far := w.Monsters.Get(farID)

	if !CastActive(w, pid, 0, p) {
		t.Fatal("装了环形爆发却释放失败")
	}
	if math.Abs(near.HP-25) > 1e-9 {
		t.Fatalf("半径内怪 HP = %v，期望 25（50 - 25）", near.HP)
	}
	if math.Abs(far.HP-50) > 1e-9 {
		t.Fatalf("半径外怪 HP = %v，期望 50（不该掉血）", far.HP)
	}
}

func TestActiveHeal(t *testing.T) {
	w := newSystemsWorld(t, 3)
	pid, p := spawnPlayerAt(t, w, 10, 10)
	equipActive(p, 0, build.SkillIDHeal)
	p.HP = 50

	if !CastActive(w, pid, 0, p) {
		t.Fatal("装了治疗却释放失败")
	}
	if math.Abs(p.HP-80) > 1e-9 {
		t.Fatalf("HP = %v，期望 80（50 + 30）", p.HP)
	}

	// 清冷却再放一次，验证不会超过上限。
	p.SkillCooldown[0] = 0
	p.HP = 90
	if !CastActive(w, pid, 0, p) {
		t.Fatal("清冷却后应能再次释放")
	}
	if math.Abs(p.HP-100) > 1e-9 {
		t.Fatalf("HP = %v，期望 100（90 + 30 钳到上限 100）", p.HP)
	}
}

func TestActiveSkillCooldown(t *testing.T) {
	w := newSystemsWorld(t, 3)
	pid, p := spawnPlayerAt(t, w, 10, 10)
	equipActive(p, 0, build.SkillIDBurst)

	if !CastActive(w, pid, 0, p) {
		t.Fatal("第一次释放应为 true")
	}
	if CastActive(w, pid, 0, p) {
		t.Fatal("冷却中第二次释放应为 false")
	}
	if math.Abs(p.SkillCooldown[0]-5) > 1e-9 {
		t.Fatalf("冷却 = %v，期望 5 秒（环形爆发 CooldownS）", p.SkillCooldown[0])
	}
}

func TestNoSkillNoEffect(t *testing.T) {
	w := newSystemsWorld(t, 3)
	pid, p := spawnPlayerAt(t, w, 10, 10)

	if CastActive(w, pid, 0, p) {
		t.Fatal("空槽释放应返回 false")
	}
	if CastActive(w, pid, 2, p) {
		t.Fatal("越界槽位应返回 false")
	}
	if CastActive(w, pid, -1, p) {
		t.Fatal("负槽位应返回 false")
	}

	// 把被动当主动按：槽位有值但不是主动技能，也不能释放。
	p.SkillActive[0] = build.SkillIDAmmoUp
	if CastActive(w, pid, 0, p) {
		t.Fatal("主动槽里放被动技能应返回 false")
	}

	// 未装配的 ID 同理（表里没有这个号）。
	p.SkillActive[0] = 999
	if CastActive(w, pid, 0, p) {
		t.Fatal("未知技能 ID 应返回 false")
	}
}

// —— 计时器与移动 ——

func TestTickBuffsCountsDown(t *testing.T) {
	w := newSystemsWorld(t, 3)
	_, p := spawnPlayerAt(t, w, 10, 10)
	p.SpeedBuffLeft = 1.5
	p.SpeedBuffMul = 1.8
	p.InvulnLeft = 1.5
	p.SkillCooldown[0] = 1.5
	p.SkillCooldown[1] = 0.4

	for i := 0; i < 3; i++ {
		TickBuffs(p, 0.5)
	}
	if p.SpeedBuffLeft != 0 {
		t.Fatalf("SpeedBuffLeft = %v，期望 0", p.SpeedBuffLeft)
	}
	if p.InvulnLeft != 0 {
		t.Fatalf("InvulnLeft = %v，期望 0", p.InvulnLeft)
	}
	if p.SkillCooldown[0] != 0 || p.SkillCooldown[1] != 0 {
		t.Fatalf("SkillCooldown = %v，期望 [0 0]", p.SkillCooldown)
	}

	// 归零后继续递减不能变成负数：负值会让 `> 0` 判据一直 false，
	// 看起来正常，但一旦某处改成 `>= 0` 就会永久触发。
	TickBuffs(p, 5)
	if p.SpeedBuffLeft < 0 || p.InvulnLeft < 0 || p.SkillCooldown[0] < 0 {
		t.Fatalf("归零后递减出负值：speed=%v invuln=%v cd=%v",
			p.SpeedBuffLeft, p.InvulnLeft, p.SkillCooldown[0])
	}
}

func TestSpeedMulAffectsMovement(t *testing.T) {
	w := newSystemsWorld(t, 3)
	_, plain := spawnPlayerAt(t, w, 10, 10)
	_, fast := spawnPlayerAt(t, w, 10, 10)
	equipPassive(fast, 0, build.SkillIDSwift) // ×1.15

	for _, p := range []*world.PlayerState{plain, fast} {
		p.MoveIntent = world.V(1, 0)
		Integrate(p, 1.0/60, w.Terrain)
	}

	// 未装技能的玩家：Derived 还是零值（乘区全 0）。这里必须走出基准速度，
	// 否则「刚出生还没重算过」的玩家会原地不动（零值兜底见 world.PlayerDerived）。
	if !approx(plain.Pos.X-10, PlayerSpeed/60, 1e-9) {
		t.Fatalf("未装技能位移 = %v，期望 %v", plain.Pos.X-10, PlayerSpeed/60)
	}
	want := PlayerSpeed * 1.15 / 60
	if !approx(fast.Pos.X-10, want, 1e-9) {
		t.Fatalf("装疾行位移 = %v，期望 %v（基准 × 1.15）", fast.Pos.X-10, want)
	}
}

func TestDashBuffStacksWithPassive(t *testing.T) {
	// 常量倍率（疾行 1.15）与限时倍率（冲刺 1.8）相乘，不互相覆盖。
	w := newSystemsWorld(t, 3)
	pid, p := spawnPlayerAt(t, w, 10, 10)
	equipPassive(p, 0, build.SkillIDSwift)
	equipActive(p, 1, build.SkillIDDash)

	if !CastActive(w, pid, 1, p) {
		t.Fatal("装了冲刺却释放失败")
	}
	if math.Abs(p.SpeedBuffLeft-2) > 1e-9 {
		t.Fatalf("冲刺时长 = %v，期望 2 秒", p.SpeedBuffLeft)
	}

	p.MoveIntent = world.V(1, 0)
	Integrate(p, 1.0/60, w.Terrain)

	want := PlayerSpeed * 1.15 * 1.8 / 60
	if !approx(p.Pos.X-10, want, 1e-9) {
		t.Fatalf("冲刺位移 = %v，期望 %v（6.0 × 1.15 × 1.8）", p.Pos.X-10, want)
	}
}

func TestInvulnBlocksDamage(t *testing.T) {
	w := newSystemsWorld(t, 3)
	pid, p := spawnPlayerAt(t, w, 10, 10)

	// 8 号主动设置的是秒数；ResolveDamage 只判 > 0。
	p.InvulnLeft = 1.0
	ev := w.ResolveDamage(world.InvalidID, pid, world.EntityClassPlayer, 10, world.ElementNone, world.DamageTags{}, world.InvalidID)
	if ev.Amount != 0 {
		t.Fatalf("无敌期间事件量 = %v，期望 0（不该产出伤害事件）", ev.Amount)
	}
	if p.HP != 100 {
		t.Fatalf("无敌期间 HP = %v，期望 100", p.HP)
	}

	// 常驻旗标那一条路径同样必须拦住。
	p.InvulnLeft = 0
	p.Flags |= world.PlayerFlagInvulnerable
	if ev := w.ResolveDamage(world.InvalidID, pid, world.EntityClassPlayer, 10, world.ElementNone, world.DamageTags{}, world.InvalidID); ev.Amount != 0 {
		t.Fatalf("旗标无敌期间事件量 = %v，期望 0", ev.Amount)
	}

	// 无敌结束后恢复吃伤害——否则「拦住了」与「压根没结算」分不开。
	p.Flags &^= world.PlayerFlagInvulnerable
	if ev := w.ResolveDamage(world.InvalidID, pid, world.EntityClassPlayer, 10, world.ElementNone, world.DamageTags{}, world.InvalidID); ev.Amount != 10 {
		t.Fatalf("无敌结束后事件量 = %v，期望 10", ev.Amount)
	}
	if p.HP != 90 {
		t.Fatalf("无敌结束后 HP = %v，期望 90", p.HP)
	}
}
