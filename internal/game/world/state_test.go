package world

import "testing"

func TestPlayerFireFieldsAreZeroed(t *testing.T) {
	var p PlayerState
	if p.FireCooldown != 0 {
		t.Fatalf("FireCooldown 零值 = %v，必须为 0（出生即可开火）", p.FireCooldown)
	}
	if p.PrevAttackHeld {
		t.Fatalf("PrevAttackHeld 零值 = true，必须为 false")
	}
}

func TestPoolSpawnClearsFireState(t *testing.T) {
	// 复用槽位必须清零：否则新玩家继承上一个玩家的冷却，
	// 表现为"刚进场打不出子弹"，而且只有换人时才偶发。
	p := NewPool[PlayerState](2, 2)
	id, a := p.Spawn()
	a.FireCooldown = 0.5
	a.PrevAttackHeld = true
	p.Kill(id)

	_, b := p.Spawn()
	if b.FireCooldown != 0 || b.PrevAttackHeld {
		t.Fatalf("复用槽位未清零：cooldown=%v prev=%v", b.FireCooldown, b.PrevAttackHeld)
	}
}
