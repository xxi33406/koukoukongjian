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

	ev := w.ResolveDamage(InvalidID, id, EntityClassMonster, 10, ElementNone, DamageTags{})

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

	ev := w.ResolveDamage(InvalidID, id, EntityClassMonster, 10, ElementNone, DamageTags{})

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
	w.ResolveDamage(InvalidID, mID, EntityClassMonster, 10, ElementNone, DamageTags{})

	m.HP = 30 // 模拟治疗技能把血拉回正数，标志不动

	ev := w.ResolveDamage(InvalidID, mID, EntityClassMonster, 10, ElementNone, DamageTags{})
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

	w.ResolveDamage(InvalidID, id, EntityClassMonster, 100, ElementNone, DamageTags{})

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

	w.ResolveDamage(pID, mID, EntityClassMonster, 25, ElementNone, DamageTags{})

	if p.Contribute != 25 {
		t.Fatalf("贡献 = %v，期望 25", p.Contribute)
	}
}

func TestResolveDamageOnUnknownTargetIsNoop(t *testing.T) {
	w := newWorldForTest()
	ev := w.ResolveDamage(InvalidID, MakeID(999, 1), EntityClassMonster, 10, ElementNone, DamageTags{})
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

	ev := w.ResolveDamage(InvalidID, mID, EntityClassMonster, 10, ElementNone, DamageTags{})
	if ev.Amount != 0 {
		t.Fatalf("对已死目标造成了 %v 伤害", ev.Amount)
	}
}

// TestResolveDamageRoutesByIdClassNotPoolOrder 钉住跨池撞号缺陷（M3 遗留）。
//
// 玩家池与怪物池各自从索引 1 开始编号，所以 w.Players.Spawn() 与
// w.Monsters.Spawn() 第一次调用都返回 id=1（index=1, gen=0）。曾经的
// ResolveDamage 先查怪物池，于是「怪物攻击玩家」的伤害落到了撞号的那只
// 怪物身上：怪物 50→40、玩家 100→100，且没有任何报错——症状看起来像是
// 怪物 AI 根本没生效。
//
// 这条用例的价值在于：两个池都只有 1 个实体时，撞号必然发生。任何把
// 「按 id 猜池」写回来的实现都会立刻在这里变红。
func TestResolveDamageRoutesByIdClassNotPoolOrder(t *testing.T) {
	w := newWorldForTest()

	pid, p := w.Players.Spawn()
	p.HP = 100
	p.MaxHP = 100
	mid, m := w.Monsters.Spawn()
	m.HP = 50
	m.MaxHP = 50

	if pid != mid {
		// 前提不成立时这条用例证明不了任何事，直接报出来而不是静默跳过：
		// 一旦池的编号规则变了，需要有人回头重新设计这条回归。
		t.Fatalf("前提失效：玩家 id=%d 与怪物 id=%d 不再撞号，本用例需重新设计", pid, mid)
	}

	// 怪物打玩家：复刻 ai.go 的调用形态（来源 InvalidID、目标玩家）。
	w.ResolveDamage(InvalidID, pid, EntityClassPlayer, 10, ElementNone, DamageTags{})

	if p.HP != 90 {
		t.Fatalf("玩家 HP = %v，期望 90（伤害没打到玩家）", p.HP)
	}
	if m.HP != 50 {
		t.Fatalf("怪物 HP = %v，期望 50（伤害被错投到撞号的怪物身上）", m.HP)
	}

	// 反向：打向怪物的伤害不能跑到同号的玩家身上。
	w.ResolveDamage(InvalidID, mid, EntityClassMonster, 10, ElementNone, DamageTags{})

	if m.HP != 40 {
		t.Fatalf("怪物 HP = %v，期望 40", m.HP)
	}
	if p.HP != 90 {
		t.Fatalf("玩家 HP = %v，期望仍是 90（怪物挨打却由玩家掉血）", p.HP)
	}
}
