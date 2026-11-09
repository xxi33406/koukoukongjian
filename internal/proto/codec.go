package proto

import (
	"fmt"

	"google.golang.org/protobuf/proto"
)

// 编解码与量化。
//
// 这一层是 Go 服务端与 Unity 客户端之间的数值契约：任何一端的量化公式
// 写错，症状都是"画面轻微跳动"或"子弹偏一点点"，很难从现象反推原因。
// 所以公式只在这里定义一次，并通过测试把具体数值钉死。
//
// 量化的必要性（docs/combat-design-v0.1.md §6.2）：
//   - 省带宽：float64 位置是 8 字节，int16 量化后 2 字节；
//   - 省不确定性：浮点在不同平台/编译器下可能差最后一位，量化后两端一致。

const (
	// PosScale 是位置量化系数：1/32 m。
	//
	// 精度 3.1 cm，玩家半径 30 cm，误差远小于碰撞尺度，视觉不可见。
	// 范围 ±1024 m，128×128 的地图有大量余量。
	PosScale = 32.0

	// VelScale 是速度量化系数：1/256 m/s。
	//
	// 玩家约 6 m/s，子弹约 20 m/s，±128 m/s 的范围足够。
	VelScale = 256.0

	// AimScale 是朝向量化系数：2π/65536。
	// 精度 0.0055°，远超过视觉需要。
	AimScale = 65536.0 / (2 * 3.14159265358979323846)

	// RadiusScale 是半径量化系数：1/256 m。
	RadiusScale = 256.0
)

// QuantizePos 把世界坐标量化成协议里的整数。
func QuantizePos(v float64) int32 {
	q := v * PosScale
	// 先钳位再取整：直接转 int32 在超范围时是未定义行为（Go 里会得到
	// 与平台相关的值），钳位后行为确定。
	if q > 1<<30 {
		q = 1 << 30
	} else if q < -(1 << 30) {
		q = -(1 << 30)
	}
	if q < 0 {
		return int32(q - 0.5)
	}
	return int32(q + 0.5)
}

// DequantizePos 把协议里的整数还原成世界坐标。
func DequantizePos(q int32) float64 { return float64(q) / PosScale }

// QuantizeVel 把速度量化成协议里的整数。
func QuantizeVel(v float64) int32 {
	q := v * VelScale
	if q > 1<<30 {
		q = 1 << 30
	} else if q < -(1 << 30) {
		q = -(1 << 30)
	}
	if q < 0 {
		return int32(q - 0.5)
	}
	return int32(q + 0.5)
}

// DequantizeVel 把协议里的整数还原成速度。
func DequantizeVel(q int32) float64 { return float64(q) / VelScale }

// QuantizeAim 把弧度朝向量化成 u16 空间。
func QuantizeAim(rad float64) uint32 {
	// 归一化到 [0, 2π)
	const twoPi = 2 * 3.14159265358979323846
	r := rad
	for r < 0 {
		r += twoPi
	}
	for r >= twoPi {
		r -= twoPi
	}
	v := r*AimScale + 0.5
	if v >= 65536 {
		return 0 // 归一化后刚好绕回 0
	}
	return uint32(v)
}

// DequantizeAim 把量化朝向还原成弧度。
func DequantizeAim(q uint32) float64 {
	return float64(q) / AimScale
}

// QuantizeRadius 把半径量化成协议里的整数。
func QuantizeRadius(v float64) uint32 {
	if v <= 0 {
		return 0
	}
	q := v*RadiusScale + 0.5
	if q > 65535 {
		return 65535
	}
	return uint32(q)
}

// DequantizeRadius 把量化半径还原。
func DequantizeRadius(q uint32) float64 { return float64(q) / RadiusScale }

// Marshal 序列化成字节。
//
// 用官方 proto 库而不是手写编码：同一份 .proto 生成 Go 与 C# 两端代码，
// schema 是唯一真相，字段顺序错位这类只在运行时暴露的 bug 才不会发生。
// 代价是慢 8 倍（实测 2730ns vs 237ns），但相对 50ms 的 tick 预算占
// 0.005%，可以忽略。
func Marshal(m proto.Message) ([]byte, error) {
	b, err := proto.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("proto: 序列化失败: %w", err)
	}
	return b, nil
}

// Unmarshal 反序列化。
//
// 明确拒绝未知字段会更好，但 proto3 的默认行为是保留它们以便向前兼容。
// 这里保持默认——新客户端发来的字段，旧服务器应当忽略而不是断开。
func Unmarshal(data []byte, m proto.Message) error {
	if err := proto.Unmarshal(data, m); err != nil {
		return fmt.Errorf("proto: 反序列化失败: %w", err)
	}
	return nil
}

// EnvelopeBody 是 Envelope 的 oneof 负载。
//
// 生成代码里的接口名 isEnvelope_Body 是未导出的，包外无法引用，
// 所以在这里导出一个别名，让调用方能写 Wrap(&Envelope_Hello{...})。
type EnvelopeBody = isEnvelope_Body

// Wrap 把消息装进 Envelope。
//
// 可靠通道是字节流语义，必须自带消息边界，所以每条消息都过 Envelope。
func Wrap(body EnvelopeBody) *Envelope {
	return &Envelope{Body: body}
}

// NewHello 构造 Hello 消息，附带协议版本校验。
func NewHello(name, token string) *Hello {
	return &Hello{
		ProtocolVersion: uint32(ProtocolVersion),
		PlayerName:      name,
		Token:           token,
	}
}

// ProtocolVersion 是当前协议版本，与 netx.Version 保持同步。
//
// 两者分开定义是有意的：netx.Version 校验的是**包格式**（包头布局），
// ProtocolVersion 校验的是**消息内容**（字段含义）。消息改动不需要动
// 包头格式，反之亦然。
const ProtocolVersion = 1
