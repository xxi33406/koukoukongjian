package room

import (
	"errors"

	"koukoukongjian/internal/game/world"
)

// 房间对外的三个入口（加入、输入、离开）都是消息式的。
//
// 原因见 server-design-v0.1.md §"M1 的并发约束"：world.World 的唯一
// 持有者是 tick 循环那个 goroutine。任何想改世界的操作都必须把请求排进
// 队列，由 tick 循环在自己的节奏里处理。这样整条模拟路径上没有锁。
var (
	// ErrRoomFull 表示四个玩家位都有人了。
	ErrRoomFull = errors.New("room: 房间已满")
	// ErrRoomStopped 表示房间已经停止，不再接受请求。
	ErrRoomStopped = errors.New("room: 房间已停止")
	// ErrJoinTimeout 表示加入请求排队后迟迟没被处理。
	ErrJoinTimeout = errors.New("room: 加入超时")
	// ErrNotChoosing 表示这个玩家当前不在技能选择窗口里。
	ErrNotChoosing = errors.New("room: 当前没有待选的技能")
	// ErrChoiceOutOfRange 表示选择下标不在候选列表范围内。
	ErrChoiceOutOfRange = errors.New("room: 选择下标越界")
	// ErrChoiceTimeout 表示选择请求排队后迟迟没被 tick 循环处理。
	//
	// 它不是"玩家超时"，而是"房间没有响应"——正常路径下 tick 50ms 一次，
	// 请求在下一个 tick 就被消费。
	ErrChoiceTimeout = errors.New("room: 选择请求处理超时")
	// ErrInteractKind 表示交互请求的 kind 不在已知取值里。
	//
	// 单独成一个错误而不是复用 errNoPeer：bridge 日志要区分"客户端发了
	// 服务端不认识的交互"与"这个座位没有活着的实体"，前者是版本不一致，
	// 后者是玩家已经死了，处理方式完全不同。
	ErrInteractKind = errors.New("room: 未知的交互类型")
	// ErrInteractTimeout 表示交互请求排队后迟迟没被 tick 循环处理。
	//
	// 与 ErrChoiceTimeout 同源：tick 循环卡死时不能让 bridge 的会话读
	// goroutine 永久粘在这个调用上。
	ErrInteractTimeout = errors.New("room: 交互请求处理超时")
)

// InputCommand 是一次玩家输入。
//
// Move 与 Aim 已经解码成真实单位（不是量化的整数），因为量化解码属于
// 协议细节，不该让房间知道 ×1000 这个约定。
//
// Buttons 是位掩码（1=攻击 2=技能1 4=技能2 8=交互）。它是"当前按住什么"
// 而不是"这一帧按下了什么"——按下与松开的边沿由系统自己比较上一帧得出，
// 否则一次按键在 60Hz 客户端上会连发三次。
type InputCommand struct {
	// Seq 是客户端自增的输入序号，用于回显 last_acked_input 做预测校正。
	Seq uint16
	// Move 是单位方向向量乘以推杆深度（长度 0..1）。
	Move world.Vec2
	// Aim 是瞄准角度（弧度）。
	Aim float64
	// HasAim 表示客户端这一帧是否给了瞄准方向。
	//
	// 不能靠 Aim==0 判断：0 是合法朝向（正 +X）。必须单独传一个布尔，
	// 否则玩家朝正右方瞄准时会被当成"没瞄准"，自动瞄准夺走控制权。
	HasAim bool
	// Buttons 是当前按住的按键位掩码。
	Buttons uint32
}

// 输入位掩码，与 proto.InputFrame.buttons 的约定一致。
const (
	ButtonAttack   uint32 = 1 << 0
	ButtonSkill1   uint32 = 1 << 1
	ButtonSkill2   uint32 = 1 << 2
	ButtonInteract uint32 = 1 << 3
)

// JoinRequest 是请求加入房间。
//
// Sink 由调用方创建并持有读端，写端交给房间：tick 循环往里写快照，并在
// 玩家离开或房间停止时关闭它。关闭权必须归房间——写 goroutine 收到关闭
// 信号才退出，而如果关闭权在第三方手里，就会在 tick 正写的时候 panic。
//
// Reply 必须有缓冲（容量 1）：tick 循环是非阻塞发送的，没有缓冲而调用方
// 还没开始收时，回执会被静默丢掉，调用方永远等下去。
type JoinRequest struct {
	Name  string
	Sink  chan []byte
	Reply chan JoinResult
}

// JoinResult 是加入的结果。
type JoinResult struct {
	// Slot 是分配到的玩家槽位（0..MaxSlots-1）。
	Slot int
	// EntityID 是这个玩家在实体池里的 ID，客户端预测要用。
	EntityID world.EntityID
	// Seed 是世界的随机种子，客户端重建同构世界用。
	Seed int64
	// StartTick 是加入时的世界 tick。
	StartTick uint32
	// Err 非空表示加入失败（房间满、房间已停）。
	Err error
}

// newJoinResult 造一个失败的加入回执。
func newJoinResult(err error) JoinResult { return JoinResult{Err: err} }
