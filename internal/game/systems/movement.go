// Package systems 是实现模拟推进的各系统。
//
// 每个系统只做一件事，且只接受 world 里的数据——它们不知道网络、不知道
// 房间、不知道 tick 从哪来。这样做的原因是可测试性：给一个 World，跑一次
// Move，断言位置，不需要起服务器也不需要等 tick。
//
// 系统的调用顺序由 room 决定，不由这里决定。顺序即游戏逻辑
// （docs/combat-design-v0.1.md §1.1），把顺序留给编排层是刻意的：
// 系统各自可复用在别的顺序里，但那个顺序一定对应另一种游戏。
package systems

import (
	"math"

	"koukoukongjian/internal/game/world"
)

// PlayerSpeed 是玩家的移动速度（米/秒）。
//
// M1 先写死成常量：这一阶段的验收目标是"网络闭环能跑通"，不是移动手感。
// 等 M2 做战斗核心时会挪进 config，和武器、技能参数放到一起调。
const PlayerSpeed = 6.0

// MaxInputMagnitude 是摇杆输入的长度上限，对应 proto 里 ×1000 的量化。
//
// 客户端把方向向量乘以 1000 后取整发送，所以这里的 1000 就是"推满"。
const MaxInputMagnitude = 1000.0

// DecodeMove 把量化的摇杆输入转成单位方向向量。
//
// 长度超过 MaxInputMagnitude 时会归一化——这既是钳制（防止客户端作弊
// 发 ×5000 的输入获得五倍速），也顺手处理了斜向输入的过冲。长度为 0
// 时返回零向量（不移动）。
func DecodeMove(x, y int32) world.Vec2 {
	fx := float64(x)
	fy := float64(y)
	l2 := fx*fx + fy*fy
	if l2 == 0 {
		return world.Vec2{}
	}
	if !isFinite(fx) || !isFinite(fy) {
		return world.Vec2{}
	}
	mag := math.Sqrt(l2)
	if mag > MaxInputMagnitude {
		// 超长输入按满推处理，方向保留。
		return world.V(fx/mag, fy/mag)
	}
	// 短推保留比例（走路/推一半的手感），最多到 1。
	return world.V(fx/MaxInputMagnitude, fy/MaxInputMagnitude)
}

// DecodeAim 把量化的瞄准向量转成弧度角。
//
// 输入是零向量（客户端没瞄准）时返回 keep，保持上一次的朝向——直接归零
// 会让角色瞬间转向 +X，看起来像抽搐。
func DecodeAim(x, y int32, keep float64) float64 {
	fx := float64(x)
	fy := float64(y)
	if fx == 0 && fy == 0 {
		return keep
	}
	if !isFinite(fx) || !isFinite(fy) {
		return keep
	}
	return math.Atan2(fy, fx)
}

// ApplyInput 把一次输入应用到玩家身上。
//
// 只设置意图，不动位置——位置由 Integrate 在子步里推进。分开的好处是
// 一次输入可以被多个子步看到（20Hz 输入、60Hz 模拟时，一个输入帧要作用
// 于三个子步），而位置积分必须按子步进行才不会让动量跳变。
//
// hasAim=false 表示客户端这一帧没给方向（aim 向量全零），此时保留原朝向：
// 自动瞄准要靠"玩家没瞄"这个信号去锁最近的怪，而 0 是合法朝向（正 +X），
// 不能拿它当"没瞄"。
//
// 不采用"调用方先存旧 AimDir、调完再还原"的写法：那种写法要求每个调用点
// 都记得还原，漏一处就是"某条路径下自动瞄准失效"，而且症状只在没给方向时
// 出现，很难联想到这里。
func ApplyInput(p *world.PlayerState, move world.Vec2, aim float64, hasAim bool) {
	p.MoveIntent = move
	if hasAim {
		p.AimDir = aim
	}
}

// Integrate 按当前意图推进一个子步的位置。
//
// dt 是子步时长（秒）。这里刻意不做碰撞检测：碰撞属于 Collide 系统，
// 顺序上排在移动之后。移动只负责"想去的方向走一步"，碰撞负责"这一步
// 是否能站住"。
func Integrate(p *world.PlayerState, dt float64) {
	// 只有活着的玩家能移动。鬼魂另有一套速度，留给 M5。
	if p.State != world.PlayerAlive {
		p.Vel = world.Vec2{}
		return
	}

	p.Vel = world.V(p.MoveIntent.X*PlayerSpeed, p.MoveIntent.Y*PlayerSpeed)
	p.Pos.X += p.Vel.X * dt
	p.Pos.Y += p.Vel.Y * dt
}

func isFinite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}
