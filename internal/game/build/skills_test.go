package build

import "testing"

// TestSkillTableShape 钉住表形状：五个主动、四个被动。
//
// 用数量而不是"至少几个"：多出来的一条和少一条一样可疑，会直接改变
// 选择界面的候选数（Task 6 的 3 选 1 依赖池子大小）。
//
// M6 加了四条主动：9/10/11 三条元素技能 + 12 号反射面。被动仍是四条。
func TestSkillTableShape(t *testing.T) {
	if got := len(ActiveSkills()); got != 8 {
		t.Fatalf("主动技能 %d 个，期望 8", got)
	}
	if got := len(PassiveSkills()); got != 4 {
		t.Fatalf("被动技能 %d 个，期望 4", got)
	}
}

// TestSkillIDsUnique 钉住 ID 唯一且非 0。
//
// 0 是 SkillActive/SkillPassive 槽位的"空槽"值，技能 ID 取 0 会让
// "没装技能"和"装了 0 号技能"无法区分——这正是 AGENTS.md §1.2 说的
// 「用值推断状态」那类根因。
func TestSkillIDsUnique(t *testing.T) {
	all := SkillTable()
	if got, want := len(all), 12; got != want {
		t.Fatalf("技能表 %d 条，期望 %d 条", got, want)
	}
	seen := make(map[uint16]string, len(all))
	for _, s := range all {
		if s.ID == 0 {
			t.Fatalf("技能 %q 的 ID 为 0，期望非 0", s.Name)
		}
		if prev, dup := seen[s.ID]; dup {
			t.Fatalf("技能 ID %d 重复：%q 与 %q", s.ID, prev, s.Name)
		}
		seen[s.ID] = s.Name
	}
}

// TestSkillByID 钉住查表：已知 ID 取得到且 Kind 正确，未知 ID 返回 false。
func TestSkillByID(t *testing.T) {
	// known 从表本身推出来，而不是手抄一份 ID→Kind 的表：手抄的那份每次
	// 加技能都要跟着改，改漏了就会在「新增技能」这种与查表无关的改动上
	// 报一条假失败。这里验证的是「SkillByID 与 SkillTable 一致」。
	known := SkillTable()
	for _, c := range known {
		s, ok := SkillByID(c.ID)
		if !ok {
			t.Fatalf("SkillByID(%d) 返回 ok=false，期望 true", c.ID)
		}
		if s.ID != c.ID {
			t.Fatalf("SkillByID(%d) 取到 ID %d，期望 %d", c.ID, s.ID, c.ID)
		}
		if s.Kind != c.Kind {
			t.Fatalf("技能 %d 的 Kind 是 %d，期望 %d", c.ID, s.Kind, c.Kind)
		}
		if s.Name == "" {
			t.Fatalf("技能 %d 的 Name 为空", c.ID)
		}
		if s.Desc == "" {
			t.Fatalf("技能 %d 的 Desc 为空，玩家看不到效果说明", c.ID)
		}
	}

	// 0 是空槽哨兵；其余取远高于当前表规模的号，避免每次加技能都要改这一行。
	for _, id := range []uint16{0, 999, 65535} {
		if s, ok := SkillByID(id); ok {
			t.Fatalf("未知技能 ID %d 返回 ok=true（取到 %q），期望 false", id, s.Name)
		}
	}
}

// TestActiveSkillsReturnsCopy 钉住「不返回内部可变切片」。
//
// M2 的 systems.WeaponTable 是导出 var，任何包都能就地改数值；
// 同样的坑在这里被访问函数挡住：改返回值不得影响下一次调用。
func TestActiveSkillsReturnsCopy(t *testing.T) {
	first := ActiveSkills()
	if len(first) == 0 {
		t.Fatalf("ActiveSkills() 返回空切片")
	}
	origName, origDamage := first[0].Name, first[0].DamageMul

	first[0].Name = "被就地改写"
	first[0].DamageMul = 99

	second := ActiveSkills()
	if second[0].Name != origName {
		t.Fatalf("改过返回值后 ActiveSkills()[0].Name 变成 %q，期望 %q", second[0].Name, origName)
	}
	if second[0].DamageMul != origDamage {
		t.Fatalf("改过返回值后 ActiveSkills()[0].DamageMul 变成 %v，期望 %v", second[0].DamageMul, origDamage)
	}

	// SkillTable 与 PassiveSkills 同样要挡住就地改写。
	table := SkillTable()
	if len(table) == 0 {
		t.Fatalf("SkillTable() 返回空切片")
	}
	table[0].Name = "被就地改写"
	if s, ok := SkillByID(1); !ok || s.Name == "被就地改写" {
		t.Fatalf("SkillTable() 的就地改写泄漏进内部表：ID 1 的 Name 变成 %q", s.Name)
	}

	passives := PassiveSkills()
	if len(passives) == 0 {
		t.Fatalf("PassiveSkills() 返回空切片")
	}
	passives[0].MaxHPAdd = 999
	if s, ok := SkillByID(passives[0].ID); !ok || s.MaxHPAdd == 999 {
		t.Fatalf("PassiveSkills() 的就地改写泄漏进内部表：ID %d 的 MaxHPAdd 变成 %v", passives[0].ID, s.MaxHPAdd)
	}
}

// TestSkillKindMatchesTable 防止有人把技能塞错组：1-4 全被动、5-8 全主动，
// 且两个访问函数返回的每一条 Kind 都与所在组一致。
func TestSkillKindMatchesTable(t *testing.T) {
	for id := uint16(1); id <= 4; id++ {
		s, ok := SkillByID(id)
		if !ok {
			t.Fatalf("SkillByID(%d) 返回 ok=false", id)
		}
		if s.Kind != SkillKindPassive {
			t.Fatalf("ID %d（%q）的 Kind 是 %d，期望 Passive", id, s.Name, s.Kind)
		}
	}
	for id := uint16(5); id <= 8; id++ {
		s, ok := SkillByID(id)
		if !ok {
			t.Fatalf("SkillByID(%d) 返回 ok=false", id)
		}
		if s.Kind != SkillKindActive {
			t.Fatalf("ID %d（%q）的 Kind 是 %d，期望 Active", id, s.Name, s.Kind)
		}
	}

	for _, s := range ActiveSkills() {
		if s.Kind != SkillKindActive {
			t.Fatalf("ActiveSkills() 里混进被动技能 %d（%q）", s.ID, s.Name)
		}
	}
	for _, s := range PassiveSkills() {
		if s.Kind != SkillKindPassive {
			t.Fatalf("PassiveSkills() 里混进主动技能 %d（%q）", s.ID, s.Name)
		}
	}
}

// TestSkillParamsConsistent 钉住数值形状：
//   - 每个被动恰好有一条加成生效（DamageMul/SpeedMul/DamageTakenMul 默认 1.0，
//     MaxHPAdd 默认 0）。两条同时生效会让 Task 5 的属性结算出现"一个技能
//     改两个乘区"的意外组合，与计划的表对不上。
//   - 每个主动都有 CooldownS > 0，否则冷却判定永远为 0、可以无限连发。
func TestSkillParamsConsistent(t *testing.T) {
	for _, s := range PassiveSkills() {
		bonuses := 0
		if s.DamageMul != 1.0 {
			bonuses++
		}
		if s.MaxHPAdd != 0 {
			bonuses++
		}
		if s.SpeedMul != 1.0 {
			bonuses++
		}
		if s.DamageTakenMul != 1.0 {
			bonuses++
		}
		if bonuses != 1 {
			t.Fatalf("被动技能 %d（%q）生效的加成有 %d 条，期望恰好 1 条：DamageMul=%v MaxHPAdd=%v SpeedMul=%v DamageTakenMul=%v",
				s.ID, s.Name, bonuses, s.DamageMul, s.MaxHPAdd, s.SpeedMul, s.DamageTakenMul)
		}
	}

	for _, s := range ActiveSkills() {
		if s.CooldownS <= 0 {
			t.Fatalf("主动技能 %d（%q）的 CooldownS 是 %v，期望 > 0", s.ID, s.Name, s.CooldownS)
		}
	}
}
