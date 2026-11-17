package world

import (
	"testing"

	"koukoukongjian/internal/config"
)

func newWorldForTest() *World {
	// HubCount=0 是 mapgen 的"关地图"哨兵：保持 M2 全空地行为，
	// 让伤害/碰撞类测试不被生成的墙干扰。
	cfg := config.Default()
	cfg.Map.HubCount = 0
	return NewWorld(cfg, 42)
}

func TestResolveDamageSubtractsHP(t *testing.T) {
	w := newWorldForTest()
	id, m := w.Monsters.Spawn()
	m.HP = 50
	m.MaxHP = 50

	ev := w.ResolveDamage(InvalidID, id, 10, ElementNone, DamageTags{})

	if m.HP != 40 {
		t.Fatalf("HP = %v，期望 40", m.HP)
	}
	if ev.Amount != 10 {
		t.Fatalf("事件伤害 = %v，期望 10", ev.Amount)
	}
	if ev.IsKill {
		t.Fatal("还有 40 血却标记击杀")
	}
}

func TestResolveDamageMarksKillNotRemove(t *testing.T) {
	// 死亡只标记，不回收——回收留给 Reap，否则遍历中删除破坏索引。
	w := newWorldForTest()
	id, m := w.Monsters.Spawn()
	m.HP = 5
	m.MaxHP = 50

	ev := w.ResolveDamage(InvalidID, id, 10, ElementNone, DamageTags{})

	if !ev.IsKill {
		t.Fatal("血量打到 0 以下却没标记击杀")
	}
	if !w.Monsters.Alive(id) {
		t.Fatal("死了的怪被 ResolveDamage 直接回收了，回收必须留给 Reap")
	}
	if m.HP > 0 {
		t.Fatalf("HP = %v，死亡后血量应 <= 0（不能留在正数）", m.HP)
	}
	// M4：击杀必须置显式标志。只靠 HP=0 会在引入治疗后失效——治疗把
	// HP 拉回正数时，标志仍要能说明"这只已经死了"。
	if !m.IsDead() {
		t.Fatal("打死却没置 MonsterFlagDead：治疗把 HP 拉回正数后就复活了")
	}
}

// TestResolveDamageOnHealedCorpseIsNoop 钉住"HP 回到正数但仍是尸体"：
// M4 的治疗会把 HP 拉回来，判据必须是标志，否则治疗等于顺手复活。
func TestResolveDamageOnHealedCorpseIsNoop(t *testing.T) {
	w := newWorldForTest()
	mID, m := w.Monsters.Spawn()
	m.HP = 5
	m.MaxHP = 50
	w.ResolveDamage(InvalidID, mID, 10, ElementNone, DamageTags{})

	m.HP = 30 // 模拟治疗技能把血拉回正数，标志不动

	ev := w.ResolveDamage(InvalidID, mID, 10, ElementNone, DamageTags{})
	if ev.Amount != 0 {
		t.Fatalf("对已死（HP 已被治疗回正数）的目标造成了 %v 伤害", ev.Amount)
	}
	if ev.IsKill {
		t.Fatal("对尸体重复产出击杀事件，分账会算两次")
	}
	if m.HP != 30 {
		t.Fatalf("HP = %v，期望仍是 30（尸体不吃伤害也不掉血）", m.HP)
	}
}

func TestResolveDamageDoesNotGoBelowZero(t *testing.T) {
	w := newWorldForTest()
	id, m := w.Monsters.Spawn()
	m.HP = 3
	m.MaxHP = 50

	w.ResolveDamage(InvalidID, id, 100, ElementNone, DamageTags{})

	if m.HP < 0 {
		t.Fatalf("HP = %v，允许为负会让血条算出负宽度", m.HP)
	}
}

func TestResolveDamageRecordsContributor(t *testing.T) {
	// 伤害要记到发射者头上（分账用，spec L134）。
	w := newWorldForTest()
	pID, p := w.Players.Spawn()
	p.Contribute = 0
	mID, m := w.Monsters.Spawn()
	m.HP = 100
	m.MaxHP = 100

	w.ResolveDamage(pID, mID, 25, ElementNone, DamageTags{})

	if p.Contribute != 25 {
		t.Fatalf("贡献 = %v，期望 25", p.Contribute)
	}
}

func TestResolveDamageOnUnknownTargetIsNoop(t *testing.T) {
	w := newWorldForTest()
	ev := w.ResolveDamage(InvalidID, MakeID(999, 1), 10, ElementNone, DamageTags{})
	if ev.Amount != 0 {
		t.Fatalf("目标不存在却产生了 %v 伤害", ev.Amount)
	}
}

func TestResolveDamageOnDeadTargetIsNoop(t *testing.T) {
	// 已死的怪不能再吃伤害：否则同一个 tick 内多颗子弹命中会让
	// 击杀事件重复产出，分账算两次。
	w := newWorldForTest()
	mID, m := w.Monsters.Spawn()
	m.HP = 0
	m.Flags |= MonsterFlagDead
	m.MaxHP = 50

	ev := w.ResolveDamage(InvalidID, mID, 10, ElementNone, DamageTags{})
	if ev.Amount != 0 {
		t.Fatalf("对已死目标造成了 %v 伤害", ev.Amount)
	}
}
