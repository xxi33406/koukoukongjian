package build

import "koukoukongjian/internal/game/world"

// ItemKind 是道具的使用方式。
type ItemKind uint8

const (
	ItemConsumable ItemKind = iota + 1 // 消耗品：直接使用（血瓶）
	ItemThrowable                      // 投掷物：扔到地上产生区域（燃烧瓶/水瓶，M7 实装）
)

// String 给日志与 UI 用。
func (k ItemKind) String() string {
	switch k {
	case ItemConsumable:
		return "consumable"
	case ItemThrowable:
		return "throwable"
	default:
		return "none"
	}
}

// Item 是一件可购买/可携带的道具。
type Item struct {
	ID   uint16
	Name string
	Desc string

	Kind ItemKind

	// Element 是投掷物携带的元素，消耗品为 ElementNone。
	// M7 的连锁反应（燃烧瓶扔进蒸汽、水瓶灭火）要用它。
	Element world.Element

	// Amount 是效果的数值（血瓶回血量、燃烧瓶伤害）。
	Amount float64

	// Price 是商店售价（金币）。
	Price uint32
}

// 道具 ID。
//
// 从 1 开始，0 留给 PickupState.RefID 的「无引用」以及 ItemSlots 的空槽。
// 与技能 ID 同一约定：0 是哨兵不是合法值。
const (
	ItemIDHealthPotion uint16 = 1 // 血瓶
	ItemIDFireBottle   uint16 = 2 // 燃烧瓶
	ItemIDWaterBottle  uint16 = 3 // 水瓶
)

// itemTable 是道具总表。
//
// 未导出 + 对外返回副本：M2 的 WeaponTable 是导出 var，任何人都能改它，
// 一旦某个测试改了表，另一个测试的结果就跟着变，而且失败点离原因很远。
var itemTable = [...]Item{
	{
		ID:     ItemIDHealthPotion,
		Name:   "血瓶",
		Desc:   "立刻回复 40 点生命",
		Kind:   ItemConsumable,
		Amount: 40,
		Price:  30,
	},
	{
		ID:      ItemIDFireBottle,
		Name:    "燃烧瓶",
		Desc:    "扔出后在地面燃起火焰",
		Kind:    ItemThrowable,
		Element: world.ElementFire,
		Amount:  15,
		Price:   40,
	},
	{
		ID:      ItemIDWaterBottle,
		Name:    "水瓶",
		Desc:    "扔出后在地面留下水洼",
		Kind:    ItemThrowable,
		Element: world.ElementWater,
		Amount:  10,
		Price:   40,
	},
}

// ItemTable 返回道具表的副本。
func ItemTable() []Item {
	out := make([]Item, len(itemTable))
	copy(out, itemTable[:])
	return out
}

// ItemByID 按 ID 查道具。
func ItemByID(id uint16) (Item, bool) {
	for i := range itemTable {
		if itemTable[i].ID == id {
			return itemTable[i], true
		}
	}
	return Item{}, false
}
