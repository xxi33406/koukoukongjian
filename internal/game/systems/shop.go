package systems

import (
	"errors"
	"fmt"

	"koukoukongjian/internal/game/build"
	"koukoukongjian/internal/game/world"
)

// ShopItemKind 区分货架上的两类商品。
type ShopItemKind uint8

const (
	ShopItemPassive ShopItemKind = iota // 被动技能
	ShopItemItem                        // 道具
)

// String 给日志与 UI 用。
func (k ShopItemKind) String() string {
	switch k {
	case ShopItemPassive:
		return "passive"
	case ShopItemItem:
		return "item"
	default:
		return "none"
	}
}

// ShopItem 是私人货架上的一项。
type ShopItem struct {
	// Index 是货架下标（0-3）。客户端买的时候发这个下标而不是 RefID，
	// 因为「买第几项」与「这一项是什么」是两件事——后者服务端说了算，
	// 客户端只发它看到的那个位置。
	Index uint8

	Kind  ShopItemKind
	RefID uint16 // 技能 ID 或道具 ID

	Price uint32
	Sold  bool
}

// rackPassives / rackItems 是私人货架的商品构成（用户拍板：只做血瓶+燃烧瓶+水瓶）。
const (
	rackPassives = 2
	rackItems    = 2
)

// 商店错误。
//
// 都导出：room 要按类型分支翻译成给客户端的提示，而错误文本会变、
// 类型判断不会。
var (
	ErrShopNoRack    = errors.New("货架为空")
	ErrShopItemIndex = errors.New("货架下标越界")
	ErrShopSold      = errors.New("商品已售出")
	ErrShopNoGold    = errors.New("金币不足")
)

// NewRack 为一个玩家生成私人货架：2 个被动技能 + 2 件道具。
//
// owned 是这个玩家已拥有的技能 ID（槽位里的 + 待选的），货架不会卖
// 已有的——spec L37「技能获取远超携带上限，强迫取舍」，如果货架卖你
// 已经装了的技能，那笔钱花得毫无意义。
//
// 每个玩家的货架用独立的骰序（各自调 w.RandIntn），所以两个人的货架
// 不同（spec L83「私人货架：每个玩家的商品列表不一样」）。
func NewRack(w *world.World, owned []uint16) []ShopItem {
	out := make([]ShopItem, 0, rackPassives+rackItems)
	if w == nil {
		return out
	}

	// 被动技能：排除已拥有的。
	passives := filterUnowned(build.PassiveSkills(), owned)
	for i := 0; i < rackPassives; i++ {
		if len(passives) == 0 {
			break
		}
		j := w.RandIntn(len(passives))
		s := passives[j]
		passives = append(passives[:j], passives[j+1:]...)
		out = append(out, ShopItem{
			Index: uint8(len(out)),
			Kind:  ShopItemPassive,
			RefID: s.ID,
			// 定价：被动技能统一 50 金（一个据点大约分到 100-200 金，
			// 所以一局能买 2-4 个技能）。数值阶段再按技能强度分档。
			Price: 50,
		})
	}

	// 道具：三种里随机挑两种（不重复）。
	items := build.ItemTable()
	for i := 0; i < rackItems; i++ {
		if len(items) == 0 {
			break
		}
		j := w.RandIntn(len(items))
		it := items[j]
		items = append(items[:j], items[j+1:]...)
		out = append(out, ShopItem{
			Index: uint8(len(out)),
			Kind:  ShopItemItem,
			RefID: it.ID,
			Price: it.Price,
		})
	}

	return out
}

// filterUnowned 过滤掉 owned 里的技能。
//
// 同时排除「注册表里不存在」的技能在 M6 不会出现，但 M7 加内容时可能
// 出现「货架引用了一个被删掉的技能」，那时它会让商店卖出一个买了没用
// 的东西。
func filterUnowned(all []build.Skill, owned []uint16) []build.Skill {
	out := make([]build.Skill, 0, len(all))
	for _, s := range all {
		if containsSkill(owned, s.ID) {
			continue
		}
		out = append(out, s)
	}
	return out
}

func containsSkill(ids []uint16, want uint16) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// BuyItem 从货架上买第 index 项，扣 p 的金币并给出技能或道具。
//
// 返回 (是否成交, 错误)。两者都返回是因为调用方需要区分「业务拒绝」
// （钱不够/已售出，要给客户端提示）与「不该发生」（下标越界）。
func BuyItem(w *world.World, p *world.PlayerState, rack []ShopItem, index int) (bool, error) {
	if w == nil || p == nil {
		return false, ErrShopNoRack
	}
	if len(rack) == 0 {
		return false, ErrShopNoRack
	}
	if index < 0 || index >= len(rack) {
		return false, fmt.Errorf("%w: %d（货架共 %d 项）", ErrShopItemIndex, index, len(rack))
	}

	it := &rack[index]
	if it.Sold {
		return false, ErrShopSold
	}
	if p.Gold < it.Price {
		return false, fmt.Errorf("%w: 需要 %d，持有 %d", ErrShopNoGold, it.Price, p.Gold)
	}

	// 先给东西再扣钱：给东西失败时钱不该扣。
	switch it.Kind {
	case ShopItemPassive:
		if !TryEquipSkill(p, it.RefID) {
			// 技能槽满且替换失败（TryEquipSkill 是替换语义，理论上
			// 不会失败；留着这个分支防 M7 改成拒绝语义时漏掉）。
			return false, fmt.Errorf("装备技能 %d 失败", it.RefID)
		}
	case ShopItemItem:
		if !EquipItem(p, it.RefID) {
			// 道具槽满：拒绝而不是替换。M4 定下的不对称规则——
			// 技能满槽替换（玩家看得见能比较），道具满槽拒绝
			// （M4 没有道具表，替玩家覆盖只可能是损失）。
			return false, fmt.Errorf("道具槽已满，无法放入 %d", it.RefID)
		}
	default:
		return false, fmt.Errorf("未知的商品类型 %d", it.Kind)
	}

	p.Gold -= it.Price
	it.Sold = true
	return true, nil
}

// SpendGold 从玩家身上扣 amount 金币，不足时返回错误。
//
// 替队友买（spec L84-85）需要「扣款」与「给货」分离：钱从买家扣，
// 东西给收货人。BuyItem 是「扣款 + 给货」的合体，这个只是扣款的一半。
func SpendGold(p *world.PlayerState, amount uint32) error {
	if p == nil {
		return ErrShopNoGold
	}
	if p.Gold < amount {
		return fmt.Errorf("%w: 需要 %d，持有 %d", ErrShopNoGold, amount, p.Gold)
	}
	p.Gold -= amount
	return nil
}

// GiveItem 把一件道具放进玩家的道具槽。
func GiveItem(p *world.PlayerState, itemID uint16) bool {
	return EquipItem(p, itemID)
}
