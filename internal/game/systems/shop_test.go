package systems

import (
	"testing"

	"koukoukongjian/internal/game/build"
)

// freeRack 是一个不受 RNG 影响的固定货架，供不关心随机性的用例使用。
func freeRack() []ShopItem {
	return []ShopItem{
		{Index: 0, Kind: ShopItemPassive, RefID: build.SkillIDAmmoUp, Price: 50},
		{Index: 1, Kind: ShopItemPassive, RefID: build.SkillIDToughBody, Price: 50},
		{Index: 2, Kind: ShopItemItem, RefID: build.ItemIDHealthPotion, Price: 30},
		{Index: 3, Kind: ShopItemItem, RefID: build.ItemIDFireBottle, Price: 40},
	}
}

// TestNewRackHasFourItems 钉住货架形状：2 被动 + 2 道具。
func TestNewRackHasFourItems(t *testing.T) {
	w := newSystemsWorld(t, 7)

	rack := NewRack(w, nil)
	if len(rack) != 4 {
		t.Fatalf("货架有 %d 项，期望 4 项（2 被动 + 2 道具）", len(rack))
	}
	for i, it := range rack {
		if it.Index != uint8(i) {
			t.Fatalf("第 %d 项的 Index = %d，期望 %d（下标必须与位置一致，客户端按 Index 买）", i, it.Index, i)
		}
		if it.Sold {
			t.Fatalf("第 %d 项生成时就是售出状态", i)
		}
		if it.Price == 0 {
			t.Fatalf("第 %d 项价格为 0", i)
		}
	}
	// 前两项是技能，后两项是道具。
	if rack[0].Kind != ShopItemPassive || rack[1].Kind != ShopItemPassive {
		t.Fatal("前两项不是被动技能")
	}
	if rack[2].Kind != ShopItemItem || rack[3].Kind != ShopItemItem {
		t.Fatal("后两项不是道具")
	}
}

// TestNewRackExcludesOwned 钉住不出售玩家已有的技能。
//
// spec L37「技能获取远超携带上限，强迫取舍」——如果货架卖你已经装了的
// 技能，那笔钱花得毫无意义。
func TestNewRackExcludesOwned(t *testing.T) {
	w := newSystemsWorld(t, 7)

	// 把所有被动技能都标为已拥有。
	var owned []uint16
	for _, s := range build.PassiveSkills() {
		owned = append(owned, s.ID)
	}

	rack := NewRack(w, owned)
	for _, it := range rack {
		if it.Kind != ShopItemPassive {
			continue
		}
		for _, id := range owned {
			if it.RefID == id {
				t.Fatalf("货架卖出了玩家已拥有的技能 %d", it.RefID)
			}
		}
	}
}

// TestNewRackIsDeterministicPerSeed 钉住同种子同结果。
func TestNewRackIsDeterministicPerSeed(t *testing.T) {
	var first []ShopItem
	for i := 0; i < 2; i++ {
		w := newSystemsWorld(t, 1234)
		rack := NewRack(w, nil)
		if i == 0 {
			first = rack
			continue
		}
		for j := range rack {
			if rack[j].RefID != first[j].RefID {
				t.Fatalf("第 %d 项两次生成不同：%d vs %d", j, rack[j].RefID, first[j].RefID)
			}
		}
	}
}

// TestBuyItemDeductsGold 钉住扣钱与售出标记。
func TestBuyItemDeductsGold(t *testing.T) {
	w := newSystemsWorld(t, 7)
	_, p := spawnPlayerAt(t, w, 20, 20)
	p.Gold = 100
	rack := freeRack()

	ok, err := BuyItem(w, p, rack, 2) // 血瓶 30
	if err != nil {
		t.Fatalf("买入失败：%v", err)
	}
	if !ok {
		t.Fatal("买入返回 false")
	}
	if p.Gold != 70 {
		t.Fatalf("买入后 Gold = %d，期望 70", p.Gold)
	}
	if !rack[2].Sold {
		t.Fatal("买入后货架项没有标记售出——能被重复购买")
	}
}

// TestBuyItemFailsWhenPoor 钉住钱不够时拒绝且不扣钱。
func TestBuyItemFailsWhenPoor(t *testing.T) {
	w := newSystemsWorld(t, 7)
	_, p := spawnPlayerAt(t, w, 20, 20)
	p.Gold = 10
	rack := freeRack()

	ok, err := BuyItem(w, p, rack, 2) // 血瓶 30，钱不够
	if ok {
		t.Fatal("钱不够却买成功了")
	}
	if err == nil {
		t.Fatal("钱不够却没有返回错误——客户端无法显示原因")
	}
	if p.Gold != 10 {
		t.Fatalf("失败后 Gold = %d，期望仍是 10（不能扣钱）", p.Gold)
	}
	if rack[2].Sold {
		t.Fatal("失败后货架项被标记售出")
	}
}

// TestBuyItemFailsWhenSold 钉住已售出的不能再买。
func TestBuyItemFailsWhenSold(t *testing.T) {
	w := newSystemsWorld(t, 7)
	_, p := spawnPlayerAt(t, w, 20, 20)
	p.Gold = 100
	rack := freeRack()
	rack[2].Sold = true

	ok, err := BuyItem(w, p, rack, 2)
	if ok {
		t.Fatal("已售出的商品买成功了")
	}
	if err == nil {
		t.Fatal("已售出却没有返回错误")
	}
}

// TestBuyItemFailsOnBadIndex 钉住越界拒绝。
func TestBuyItemFailsOnBadIndex(t *testing.T) {
	w := newSystemsWorld(t, 7)
	_, p := spawnPlayerAt(t, w, 20, 20)
	p.Gold = 100
	rack := freeRack()

	for _, idx := range []int{-1, 4, 99} {
		ok, err := BuyItem(w, p, rack, idx)
		if ok {
			t.Fatalf("下标 %d 买成功了", idx)
		}
		if err == nil {
			t.Fatalf("下标 %d 没有返回错误", idx)
		}
	}
}

// TestBuyItemGivesPassiveSkill 钉住买被动技能会装进槽位并重算属性。
func TestBuyItemGivesPassiveSkill(t *testing.T) {
	w := newSystemsWorld(t, 7)
	_, p := spawnPlayerAt(t, w, 20, 20)
	p.Gold = 100
	rack := freeRack()

	ok, err := BuyItem(w, p, rack, 0) // 强化弹头
	if err != nil || !ok {
		t.Fatalf("买入被动技能失败：ok=%v err=%v", ok, err)
	}

	var found bool
	for _, id := range p.SkillPassive {
		if id == build.SkillIDAmmoUp {
			found = true
		}
	}
	if !found {
		t.Fatalf("买入后被动槽位 = %v，期望包含 %d", p.SkillPassive, build.SkillIDAmmoUp)
	}
	// 强化弹头 DamageMul = 1.20，装进槽位后 Derived 必须被重算。
	if p.Derived.DamageMul <= 1.0 {
		t.Fatalf("装被动后 Derived.DamageMul = %v，期望 >1（RecomputeDerived 没被调用）", p.Derived.DamageMul)
	}
}

// TestBuyItemGivesItem 钉住买道具会装进道具槽。
func TestBuyItemGivesItem(t *testing.T) {
	w := newSystemsWorld(t, 7)
	_, p := spawnPlayerAt(t, w, 20, 20)
	p.Gold = 100
	rack := freeRack()

	ok, err := BuyItem(w, p, rack, 2) // 血瓶
	if err != nil || !ok {
		t.Fatalf("买入道具失败：ok=%v err=%v", ok, err)
	}

	var found bool
	for _, id := range p.ItemSlots {
		if id == build.ItemIDHealthPotion {
			found = true
		}
	}
	if !found {
		t.Fatalf("买入后道具槽 = %v，期望包含 %d", p.ItemSlots, build.ItemIDHealthPotion)
	}
}

// TestBuyItemForTeammate 钉住「替队友买」（spec L84-85）。
//
// 买家与收货人是两个人：钱从买家扣，东西给收货人。这在灰盒阶段是
// 「一个人跑商店给大家带东西」的社交行为，代码上必须允许两者不同。
func TestBuyItemForTeammate(t *testing.T) {
	w := newSystemsWorld(t, 7)
	_, buyer := spawnPlayerAt(t, w, 20, 20)
	_, mate := spawnPlayerAt(t, w, 22, 20)
	buyer.Gold = 100
	mate.Gold = 0
	rack := freeRack()

	// 这里验证的是「钱从 buyer 扣、东西给 mate」的分离能力。
	// API 上由调用方决定扣谁的钱：先扣 buyer，再给 mate 装。
	if err := SpendGold(buyer, rack[2].Price); err != nil {
		t.Fatalf("扣款失败：%v", err)
	}
	if ok := GiveItem(mate, build.ItemIDHealthPotion); !ok {
		t.Fatal("给队友装道具失败")
	}

	if buyer.Gold != 70 {
		t.Fatalf("买家 Gold = %d，期望 70", buyer.Gold)
	}
	if mate.Gold != 0 {
		t.Fatalf("收货人 Gold = %d，期望 0（钱不该从他扣）", mate.Gold)
	}
	var found bool
	for _, id := range mate.ItemSlots {
		if id == build.ItemIDHealthPotion {
			found = true
		}
	}
	if !found {
		t.Fatalf("收货人道具槽 = %v，期望包含 %d", mate.ItemSlots, build.ItemIDHealthPotion)
	}
}

// TestSpendGoldRejectsInsufficient 钉住钱不够时 SpendGold 返回错误且不扣。
func TestSpendGoldRejectsInsufficient(t *testing.T) {
	w := newSystemsWorld(t, 7)
	_, p := spawnPlayerAt(t, w, 20, 20)
	p.Gold = 10

	if err := SpendGold(p, 30); err == nil {
		t.Fatal("钱不够却扣款成功了")
	}
	if p.Gold != 10 {
		t.Fatalf("失败后 Gold = %d，期望 10", p.Gold)
	}
}

// TestGiveItemRejectsWhenFull 钉住道具槽满时拒绝（M4 的规则：满槽拒绝而不是替换）。
//
// 与技能满槽替换 slots[1] 不同：技能玩家看得见能比较，道具在没有界面时
// 替玩家覆盖只可能是损失。
func TestGiveItemRejectsWhenFull(t *testing.T) {
	w := newSystemsWorld(t, 7)
	_, p := spawnPlayerAt(t, w, 20, 20)
	p.ItemSlots[0] = build.ItemIDHealthPotion
	p.ItemSlots[1] = build.ItemIDFireBottle
	p.ItemSlots[2] = build.ItemIDWaterBottle

	if ok := GiveItem(p, build.ItemIDHealthPotion); ok {
		t.Fatal("道具槽已满却装进去了")
	}
}
