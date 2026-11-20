package build

import (
	"testing"
)

// TestItemTableHasThreeItems 钉住用户拍板的三种道具。
func TestItemTableHasThreeItems(t *testing.T) {
	all := ItemTable()
	if len(all) != 3 {
		t.Fatalf("道具表有 %d 种，期望 3 种（血瓶/燃烧瓶/水瓶）", len(all))
	}

	want := map[uint16]string{
		ItemIDHealthPotion: "血瓶",
		ItemIDFireBottle:   "燃烧瓶",
		ItemIDWaterBottle:  "水瓶",
	}
	for _, it := range all {
		name, ok := want[it.ID]
		if !ok {
			t.Fatalf("道具表里出现了未定义的道具 ID %d（%s）", it.ID, it.Name)
		}
		if it.Name != name {
			t.Fatalf("道具 %d 的名字是 %q，期望 %q", it.ID, it.Name, name)
		}
		if it.Price == 0 {
			t.Fatalf("道具 %d（%s）的价格是 0——免费道具会让金币失去意义", it.ID, it.Name)
		}
	}
}

// TestItemTableNotMutable 钉住对外返回副本。
//
// M2 的 WeaponTable 是导出 var，任何人都能改它。技能表与道具表都改成
// 返回副本，避免「某个测试改了表，另一个测试的结果跟着变」。
func TestItemTableNotMutable(t *testing.T) {
	a := ItemTable()
	if len(a) == 0 {
		t.Fatal("道具表为空")
	}
	orig := a[0].Name
	a[0].Name = "被改了"

	b := ItemTable()
	if b[0].Name != orig {
		t.Fatalf("修改返回的切片污染了道具表：%q，期望 %q", b[0].Name, orig)
	}
}

// TestItemByID 钉住按 ID 查表。
func TestItemByID(t *testing.T) {
	it, ok := ItemByID(ItemIDHealthPotion)
	if !ok {
		t.Fatal("血瓶查不到")
	}
	if it.Name != "血瓶" {
		t.Fatalf("血瓶的名字是 %q", it.Name)
	}
	if _, ok := ItemByID(9999); ok {
		t.Fatal("不存在的 ID 查到了道具")
	}
}

// TestItemKindClassifies 钉住消耗品/投掷物的分类。
//
// 燃烧瓶与水瓶是「往地上扔」的东西（M7 才实装投掷），血瓶是「喝掉回血」。
// 分类存在是为了让商店按类型定价与让背包知道它怎么用。
func TestItemKindClassifies(t *testing.T) {
	hp, _ := ItemByID(ItemIDHealthPotion)
	if hp.Kind != ItemConsumable {
		t.Fatalf("血瓶的 Kind = %v，期望 ItemConsumable", hp.Kind)
	}
	fire, _ := ItemByID(ItemIDFireBottle)
	if fire.Kind != ItemThrowable {
		t.Fatalf("燃烧瓶的 Kind = %v，期望 ItemThrowable", fire.Kind)
	}
	water, _ := ItemByID(ItemIDWaterBottle)
	if water.Kind != ItemThrowable {
		t.Fatalf("水瓶的 Kind = %v，期望 ItemThrowable", water.Kind)
	}
}

// TestThrowablesCarryElement 钉住投掷物带元素（M7 交汇要用）。
func TestThrowablesCarryElement(t *testing.T) {
	fire, _ := ItemByID(ItemIDFireBottle)
	if fire.Element == 0 {
		t.Fatal("燃烧瓶的元素是零值——M7 的连锁反应会读它")
	}
	water, _ := ItemByID(ItemIDWaterBottle)
	if water.Element == 0 {
		t.Fatal("水瓶的元素是零值")
	}
	if fire.Element == water.Element {
		t.Fatal("燃烧瓶与水瓶的元素相同")
	}
}
