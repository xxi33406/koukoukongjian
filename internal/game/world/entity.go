package world

// EntityID 用 16 位编码"世代 + 索引"，高 4 位世代、低 12 位索引。
//
// 为什么要世代：实体被回收后索引会被复用，如果客户端还持有旧 ID（比如一颗
// 已消失的子弹），只发索引就会错误地指向新的那个实体——表现为"打空气掉血"
// 或"捡到不存在的东西"。世代递增后旧 ID 自然失效，查表时对不上就丢弃。
//
// 12 位索引 = 每个池最多 4096 个槽位；4 位世代 = 同一个槽位可复用 16 轮。
// 16 轮足够覆盖任何一次网络往返的滞留时间。
type EntityID uint16

const (
	// MaxIndex 是每个实体池的最大槽位数。
	MaxIndex = 1 << 12 // 4096
	// IndexMask 取出低 12 位索引。
	IndexMask = MaxIndex - 1
	// GenShift 是世代位的偏移。
	GenShift = 12
	// MaxGen 是世代回绕前的最大值。
	MaxGen = 0x0F

	// InvalidID 是零值，表示"没有实体"。
	//
	// 索引 0 与世代 0 的组合留给它，因此实体池的 0 号槽位不使用——见 NewPool
	// 的说明。用零值表示无效是 Go 的惯例，让 var id EntityID 天然正确。
	InvalidID EntityID = 0
)

// MakeID 组装一个实体 ID。
func MakeID(index uint16, gen uint8) EntityID {
	return EntityID(uint16(gen&MaxGen)<<GenShift | (index & IndexMask))
}

// Index 返回槽位下标。
func (e EntityID) Index() uint16 { return uint16(e) & IndexMask }

// Gen 返回世代号。
func (e EntityID) Gen() uint8 { return uint8(e >> GenShift) }

// IsValid 报告这个 ID 是否指向一个可能的实体。
func (e EntityID) IsValid() bool { return e != InvalidID }

// EntityClass 说明一个 EntityID 属于哪个实体池。
//
// 为什么必须有它：EntityID 只编码「世代 + 索引」，两个池各自从索引 1 开始
// 编号，所以 w.Players.Spawn() 与 w.Monsters.Spawn() 会给出**同一个数值**
// （都是 index=1, gen=0）。也就是说光有一个 EntityID 无法判断它指向玩家
// 还是怪物——这不是理论风险：ResolveDamage 曾按「先查怪物池、再查玩家池」
// 的顺序猜目标类型，怪物打玩家的伤害因此落到撞号的那只怪物身上，玩家血条
// 纹丝不动（探针实测：怪 50→40、玩家 100→100）。
//
// 因此凡是「按 id 去某个池取实体」的地方，调用方必须同时给出池的类别，
// 不能靠「先查哪个池」的顺序猜。零值刻意留给了怪物池：子弹命中怪物是唯一
// 不显式写这个字段的热路径，让零值等于既有语义，可以少改一处热循环。
type EntityClass uint8

const (
	// EntityClassMonster 表示 id 属于怪物池。
	EntityClassMonster EntityClass = iota
	// EntityClassPlayer 表示 id 属于玩家池。
	EntityClassPlayer
)

// String 便于日志里直接打印。
func (e EntityID) String() string {
	if e == InvalidID {
		return "EntityID(none)"
	}
	return "EntityID(idx=" + itoa(int(e.Index())) + ",gen=" + itoa(int(e.Gen())) + ")"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
