package build

import (
	"testing"

	"koukoukongjian/internal/config"
	"koukoukongjian/internal/game/world"
)

// newChoiceWorld 造一个确定种子的世界当随机源。
//
// 必须用 world.NewWorld 而不是手工拼 &world.World{RNG: rand.New(...)}：
// GenerateChoices 走的是 RandIntn/RandFloat64 包装，而包装会累加
// rngCalls——只有真实世界才能验证"同一颗种子 → 同一串随机消耗"。
func newChoiceWorld(t *testing.T, seed int64) *world.World {
	t.Helper()
	return world.NewWorld(config.Default(), seed)
}

func choiceIDs(cs []Skill) []uint16 {
	out := make([]uint16, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

func allSkillIDs() []uint16 {
	all := SkillTable()
	out := make([]uint16, len(all))
	for i, s := range all {
		out[i] = s.ID
	}
	return out
}

// TestChoicesAreThreeUniqueAndUnowned 钉住候选的基本形状：3 个、互不重复、
// 不含已装备。
//
// 已装备故意取一个被动（强化弹头）加一个主动（冲刺）：只在被动池上做排除
// 的实现会漏掉主动那一半，一条测试同时压住两边。
func TestChoicesAreThreeUniqueAndUnowned(t *testing.T) {
	w := newChoiceWorld(t, 1)
	owned := []uint16{SkillIDAmmoUp, SkillIDDash}

	cs := GenerateChoices(w, owned, 3)
	if len(cs) != 3 {
		t.Fatalf("候选 %d 个（%v），期望 3 个", len(cs), choiceIDs(cs))
	}

	seen := make(map[uint16]bool, len(cs))
	for _, c := range cs {
		if c.ID == 0 {
			t.Fatalf("候选 ID 为 0（候选：%v），期望非 0", choiceIDs(cs))
		}
		if _, ok := SkillByID(c.ID); !ok {
			t.Fatalf("候选 %q（ID %d）不在技能表里", c.Name, c.ID)
		}
		if seen[c.ID] {
			t.Fatalf("候选 ID %d 重复出现（候选：%v），期望互不重复", c.ID, choiceIDs(cs))
		}
		seen[c.ID] = true
		for _, o := range owned {
			if o == c.ID {
				t.Fatalf("候选里出现了已装备的技能 ID %d（候选：%v），期望排除", c.ID, choiceIDs(cs))
			}
		}
	}
}

// TestActiveIsRarerThanPassive 钉住"主动稀有"这条设计决定。
//
// 用户拍板（2026-11-17）：主动被动都有，但主动概率较少，单次抽取 0.30。
// 统计 1000 次三选一 = 3000 个候选，主动占比应落在 0.15~0.50。
//
// 界为什么这么宽：这是"不脆"的界，抓的是实现退化成"两边各半"（0.50）
// 或"几乎没有主动"（远低于 0.15）这两类结构错误，而不是去卡 0.30 的
// 小数点后两位——固定 seed 下本来就完全确定，卡太紧只会让人调参时误报。
func TestActiveIsRarerThanPassive(t *testing.T) {
	const trials = 1000
	w := newChoiceWorld(t, 20261117)

	active := 0
	for i := 0; i < trials; i++ {
		for _, c := range GenerateChoices(w, nil, 3) {
			if c.Kind == SkillKindActive {
				active++
			}
		}
	}

	ratio := float64(active) / float64(trials*3)
	t.Logf("1000 次三选一：主动 %d/%d，占比 %.4f（目标 %.2f）", active, trials*3, ratio, ActiveSkillChance)

	if ratio < 0.15 || ratio > 0.50 {
		t.Fatalf("主动占比 %.4f（主动 %d/%d），期望落在 0.15~0.50", ratio, active, trials*3)
	}
	// 第二道闸：0.50 恰好是"两边各半"的值，落在上界内会擦边通过，
	// 所以再压一条 0.45。3000 次抽取在 0.30 附近的 σ≈0.008，0.45 已是
	// 18σ 之外；seed 固定，这一条既不会 flaky，又能真的抓住各半。
	if ratio > 0.45 {
		t.Fatalf("主动占比 %.4f 高得不像稀有（主动 %d/%d），期望 ≤0.45（两边各半是 0.50）", ratio, active, trials*3)
	}
}

// TestChoicesWhenAllOwned 钉住池子耗尽时的降级：全部技能已装备 → 返回空，
// 且不许 panic。
//
// 返回空是设计而不是缺陷：调用方（Task 8）要能识别"这次升级没有可选项"，
// 转而给数值奖励或跳过。这里若为了凑够 3 个而重复返回已装备的技能，
// 玩家会看到"已经有的技能还能再选一次"，那是比空候选更糟的 bug。
func TestChoicesWhenAllOwned(t *testing.T) {
	w := newChoiceWorld(t, 7)

	cs := GenerateChoices(w, allSkillIDs(), 3)
	if len(cs) != 0 {
		t.Fatalf("全部技能已装备时返回 %d 个候选（%v），期望 0 个", len(cs), choiceIDs(cs))
	}
}

// TestChoicesDeterministic 钉住回放一致性：同一颗种子 → 同一串候选。
//
// 比对三次连续抽取而不是一次：只抽一次时，实现里"漏调一次 RNG"的错
// 可能被更早的消耗掩盖；连抽三次会把随机消耗的顺序差放大出来。
// 同时钉住 rngCalls：它是回放时校验随机源的唯一凭据。
func TestChoicesDeterministic(t *testing.T) {
	const rounds = 3
	a := newChoiceWorld(t, 42)
	b := newChoiceWorld(t, 42)

	for i := 0; i < rounds; i++ {
		gotA := choiceIDs(GenerateChoices(a, nil, 3))
		gotB := choiceIDs(GenerateChoices(b, nil, 3))
		if len(gotA) != len(gotB) {
			t.Fatalf("第 %d 轮候选数 %d vs %d，期望相同", i, len(gotA), len(gotB))
		}
		for j := range gotA {
			if gotA[j] != gotB[j] {
				t.Fatalf("第 %d 轮第 %d 个候选 %d vs %d（种子相同），期望完全一致；%v vs %v",
					i, j, gotA[j], gotB[j], gotA, gotB)
			}
		}
	}

	if a.RNGCalls() != b.RNGCalls() {
		t.Fatalf("同种子下 RNG 消耗 %d vs %d，期望一致", a.RNGCalls(), b.RNGCalls())
	}
}

// TestChoicesExcludesOwnedFromBothPools 钉住两个池各自都做排除。
//
// 前一条测试只装了 2 个技能，池子还够满；这里把两个池各拿掉一半，
// 剩下 2 被动 + 2 主动，"必须 3 个候选"就逼着实现真去两个池里取，
// 排除漏做会立刻返回已装备的 ID。
func TestChoicesExcludesOwnedFromBothPools(t *testing.T) {
	w := newChoiceWorld(t, 3)

	owned := []uint16{
		SkillIDAmmoUp, SkillIDToughBody, // 被动拿走 2 个
		SkillIDDash, SkillIDHeal, // 主动拿走 2 个
	}

	cs := GenerateChoices(w, owned, 3)
	if len(cs) != 3 {
		t.Fatalf("候选 %d 个（%v），期望 3 个（剩 4 个可选）", len(cs), choiceIDs(cs))
	}

	seen := make(map[uint16]bool, len(cs))
	for _, c := range cs {
		if seen[c.ID] {
			t.Fatalf("候选 ID %d 重复（候选：%v）", c.ID, choiceIDs(cs))
		}
		seen[c.ID] = true
		for _, o := range owned {
			if o == c.ID {
				t.Fatalf("候选里出现了已装备的技能 ID %d（候选：%v）", c.ID, choiceIDs(cs))
			}
		}
	}

	// 剩下的 4 个可选里被动只有 2 个、主动只有 2 个：3 个候选必然跨池，
	// 这条断言等价于"实现真的两个池都查了"。
	passive, active := 0, 0
	for _, c := range cs {
		if c.Kind == SkillKindActive {
			active++
		} else {
			passive++
		}
	}
	if passive == 0 || active == 0 {
		t.Fatalf("候选全落在同一池（被动 %d、主动 %d，候选：%v），期望跨池", passive, active, choiceIDs(cs))
	}
}

// TestChoicesReturnsFewerWhenPoolExhausted 钉住"池子不够就给实际数量"。
//
// 只剩 2 个可选时返回 2 个，而不是返回 2 个之后再从已装备里补第 3 个。
// 补齐会让玩家看到重复的选项，降级才是对的（调用方按 len 判断）。
func TestChoicesReturnsFewerWhenPoolExhausted(t *testing.T) {
	w := newChoiceWorld(t, 11)

	actives := ActiveSkills()
	passives := PassiveSkills()
	// 各池留最后一个不装备，其余全装：可选 2 个。
	owned := make([]uint16, 0, len(actives)+len(passives))
	for _, s := range actives[:len(actives)-1] {
		owned = append(owned, s.ID)
	}
	for _, s := range passives[:len(passives)-1] {
		owned = append(owned, s.ID)
	}

	cs := GenerateChoices(w, owned, 3)
	if len(cs) != 2 {
		t.Fatalf("只剩 2 个可选时返回 %d 个（%v），期望 2 个", len(cs), choiceIDs(cs))
	}
	for _, c := range cs {
		for _, o := range owned {
			if o == c.ID {
				t.Fatalf("候选 %d 是已装备的技能（候选：%v），期望只在剩余池里取", c.ID, choiceIDs(cs))
			}
		}
	}
	if cs[0].ID == cs[1].ID {
		t.Fatalf("两个候选 ID 都是 %d，期望互不重复", cs[0].ID)
	}
}

// TestChoicesNilWorldNoPanic 钉住防御：w 为 nil 时返回空，不 panic。
//
// 参数是 *world.World，nil 在 Go 里是一等公民；候选生成会被 Task 8 的
// 升级流程调用，那里一旦"世界还没建好就来要候选"就 panic，整局会挂掉。
func TestChoicesNilWorldNoPanic(t *testing.T) {
	if cs := GenerateChoices(nil, nil, 3); len(cs) != 0 {
		t.Fatalf("nil world 返回 %d 个候选（%v），期望 0 个", len(cs), choiceIDs(cs))
	}
}

// TestChoicesZeroN 钉住 n<=0 时返回空：不加这个判断，make([]Skill, 0, n)
// 在 n 为负时直接 panic（cap 为负）。
func TestChoicesZeroN(t *testing.T) {
	w := newChoiceWorld(t, 5)
	for _, n := range []int{0, -1} {
		if cs := GenerateChoices(w, nil, n); len(cs) != 0 {
			t.Fatalf("n=%d 返回 %d 个候选（%v），期望 0 个", n, len(cs), choiceIDs(cs))
		}
	}
}

// TestChoicesDoNotMutateTable 钉住"抽样不污染技能表"。
//
// 实现会在池子上做下标删除；如果池子直接来自内部表，删一个就会把表改短。
// 这条测试连着抽十几次后比对长度与顺序。
func TestChoicesDoNotMutateTable(t *testing.T) {
	w := newChoiceWorld(t, 13)
	before := SkillTable()

	for i := 0; i < 16; i++ {
		GenerateChoices(w, nil, 3)
	}

	after := SkillTable()
	if len(after) != len(before) {
		t.Fatalf("技能表长度从 %d 变成 %d，期望不变", len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID {
			t.Fatalf("技能表第 %d 条从 ID %d 变成 %d，期望顺序不变", i, before[i].ID, after[i].ID)
		}
	}
	if len(ActiveSkills()) != 4 || len(PassiveSkills()) != 4 {
		t.Fatalf("抽样后池子变成主动 %d / 被动 %d，期望各 4", len(ActiveSkills()), len(PassiveSkills()))
	}
}
