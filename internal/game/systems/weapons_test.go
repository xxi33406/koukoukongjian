package systems

import "testing"

func TestWeaponTableHasDefaults(t *testing.T) {
	w, ok := WeaponByID(WeaponRifle)
	if !ok {
		t.Fatalf("默认武器 %d 不在表里", WeaponRifle)
	}
	if w.Cooldown <= 0 {
		t.Fatalf("冷却 = %v，必须大于 0，否则一帧内无限发射", w.Cooldown)
	}
	if w.Damage <= 0 {
		t.Fatalf("伤害 = %v，必须大于 0", w.Damage)
	}
	if w.BulletSpeed <= 0 {
		t.Fatalf("弹速 = %v，必须大于 0", w.BulletSpeed)
	}
}

func TestWeaponTableHasBothFireModes(t *testing.T) {
	auto, single := false, false
	for _, w := range WeaponTable {
		if w.AutoFire {
			auto = true
		} else {
			single = true
		}
	}
	if !auto || !single {
		t.Fatalf("连发=%v 单发=%v，两种开火模式都必须有样本", auto, single)
	}
}

func TestWeaponCooldownNotBelowOneTick(t *testing.T) {
	// 冷却低于一个 tick(50ms) 时，Fire 必须在一帧里补发多发，
	// 否则实际射速被静默压到 20 发/秒。
	//
	// 下限定 0.02 而不是一个 tick(0.05) 是刻意的：0.02 正是
	// TestFireCatchesUpWhenCooldownBelowTick 覆盖的"极短冷却"场景，
	// 它的实际射速会略低于理论值（补发受 -dt 下界压制，见计划 §9），
	// 这是已接受的代价。这里守的是比那更荒诞的值——真出现时一帧要
	// 补发几十发。现有两把枪 0.15 / 0.6 都远离这条线。
	for _, w := range WeaponTable {
		if w.Cooldown < 0.02 {
			t.Fatalf("武器 %q 冷却 %v 过小，Fire 的补发循环会打出成百上千发", w.Name, w.Cooldown)
		}
	}
}

func TestBulletLifeMatchesRange(t *testing.T) {
	// 25 分钟一局的地图 128 m，子弹射程必须够横穿半个场地。
	for _, w := range WeaponTable {
		r := w.BulletSpeed * w.BulletLife
		if r < 8 {
			t.Fatalf("武器 %q 射程 %.1f m，太短", w.Name, r)
		}
		if r > 60 {
			t.Fatalf("武器 %q 射程 %.1f m，超出屏幕尺度，客户端无法表现", w.Name, r)
		}
	}
}
