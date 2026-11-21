package world

import (
	"testing"

	"koukoukongjian/internal/config"
)

// newStatsWorld 造一个干净世界。HubCount=0 关掉地图生成，避免
// mapgen 消耗 RNG（本文件的断言全都与地图无关）。
func newStatsWorld(t *testing.T) *World {
	t.Helper()
	cfg := config.Default()
	cfg.Map.HubCount = 0
	return NewWorld(cfg, 42)
}

// TestPlayerStatsStartZeroed 钉住"新玩家从零开始"。
//
// 这不是废话测试：统计字段与 Gold/Contribute 一样存在 PlayerState 里，
// 而池槽位是复用的。若将来有人把统计挪进 room 的 map，旧统计会在槽位
// 复用时被下一个玩家继承——症状是"新加入的玩家一进来就有 30 个击杀"。
func TestPlayerStatsStartZeroed(t *testing.T) {
	w := newStatsWorld(t)
	id, p := w.Players.Spawn()
	if id == InvalidID || p == nil {
		t.Fatal("玩家池 Spawn 失败")
	}
	if p.Kills != 0 || p.Deaths != 0 || p.ReflectKills != 0 {
		t.Fatalf("新玩家统计 = {kills %d, deaths %d, reflect %d}，期望全 0",
			p.Kills, p.Deaths, p.ReflectKills)
	}
}

// TestKillIncrementsKillerKills 钉住击杀计数落在来源玩家身上。
func TestKillIncrementsKillerKills(t *testing.T) {
	w := newStatsWorld(t)
	pid, _ := w.Players.Spawn()
	mid, m := w.Monsters.Spawn()
	m.HP, m.MaxHP = 10, 50

	ev := w.ResolveDamage(pid, mid, EntityClassMonster, 20, ElementNone, DamageTags{}, InvalidID)
	if !ev.IsKill {
		t.Fatalf("20 点伤害打 10 血怪，IsKill = false，期望 true")
	}

	p := w.Players.Get(pid)
	if p.Kills != 1 {
		t.Fatalf("击杀后 Kills = %d，期望 1", p.Kills)
	}
}

// TestDeathIncrementsVictimDeaths 钉住阵亡计数落在被打死的玩家身上。
func TestDeathIncrementsVictimDeaths(t *testing.T) {
	w := newStatsWorld(t)
	vid, v := w.Players.Spawn()
	v.HP, v.MaxHP = 10, 100

	ev := w.ResolveDamage(InvalidID, vid, EntityClassPlayer, 20, ElementNone, DamageTags{}, InvalidID)
	if !ev.IsKill {
		t.Fatalf("20 点伤害打 10 血玩家，IsKill = false，期望 true")
	}
	p := w.Players.Get(vid)
	if p.Deaths != 1 {
		t.Fatalf("阵亡后 Deaths = %d，期望 1", p.Deaths)
	}
	if p.State != PlayerGhost {
		t.Fatalf("阵亡后 State = %v，期望 PlayerGhost", p.State)
	}
}

// TestReflectKillCountsSeparately 是 M7 的必要断言：
// spec L114「反射弹幕的击杀归属算反弹者」，§11.1 因此单列 reflect_kills。
//
// 反射击杀同时算 Kills（这个人确实拿了一杀）与 ReflectKills（这一杀是
// 靠反射拿的）。分两列而不是只留前者：前者决定战绩，后者决定"反射流"
// 这个 build 身份是否真的在起作用（spec L66 的构筑深度指标）。
func TestReflectKillCountsSeparately(t *testing.T) {
	w := newStatsWorld(t)
	pid, _ := w.Players.Spawn()
	mid, m := w.Monsters.Spawn()
	m.HP, m.MaxHP = 10, 50

	// 第七个参数是 Reflector：非零表示这一发是反射来的。
	ev := w.ResolveDamage(pid, mid, EntityClassMonster, 20, ElementNone, DamageTags{}, pid)
	if !ev.IsKill {
		t.Fatalf("反射伤害未致死不满足前提：IsKill = false")
	}
	if ev.Reflector != pid {
		t.Fatalf("事件 Reflector = %v，期望 %v", ev.Reflector, pid)
	}
	p := w.Players.Get(pid)
	if p.Kills != 1 {
		t.Fatalf("Kills = %d，期望 1（反射击杀也是击杀）", p.Kills)
	}
	if p.ReflectKills != 1 {
		t.Fatalf("ReflectKills = %d，期望 1", p.ReflectKills)
	}
}

// TestNonReflectKillLeavesReflectKillsZero 是对照组。
//
// 没有这条，把 "if true" 写进反射分支也全绿——只测正例的测试无法区分
// "反射被记上了"和"所有击杀都被记成反射"。
func TestNonReflectKillLeavesReflectKillsZero(t *testing.T) {
	w := newStatsWorld(t)
	pid, _ := w.Players.Spawn()
	mid, m := w.Monsters.Spawn()
	m.HP, m.MaxHP = 10, 50

	ev := w.ResolveDamage(pid, mid, EntityClassMonster, 20, ElementNone, DamageTags{}, InvalidID)
	if !ev.IsKill {
		t.Fatal("前提失效：普通伤害未致死")
	}
	p := w.Players.Get(pid)
	if p.Kills != 1 || p.ReflectKills != 0 {
		t.Fatalf("Kills = %d, ReflectKills = %d，期望 1 与 0", p.Kills, p.ReflectKills)
	}
}

// TestStatsSurviveRevive 钉住"战绩不因复活清零"。
//
// Kill() 会清一堆字段（HP/Vel/冷却/引导），统计不在此列——把人打死一次
// 就把他的击杀数抹掉，会让战绩页在阵亡玩家身上永远显示 0。
func TestStatsSurviveRevive(t *testing.T) {
	w := newStatsWorld(t)
	pid, p := w.Players.Spawn()
	p.Kills = 7
	p.ReflectKills = 3
	p.HP = 5

	w.ResolveDamage(InvalidID, pid, EntityClassPlayer, 10, ElementNone, DamageTags{}, InvalidID)

	got := w.Players.Get(pid)
	if got.Kills != 7 || got.ReflectKills != 3 {
		t.Fatalf("阵亡后 Kills = %d, ReflectKills = %d，期望 7 与 3（统计不该被 Kill 清掉）",
			got.Kills, got.ReflectKills)
	}
	if got.Deaths != 1 {
		t.Fatalf("Deaths = %d，期望 1", got.Deaths)
	}
}
