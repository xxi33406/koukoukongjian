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
// M2 实际没有挪进 config：武器参数按拍板硬编码在 weapons.go 常量表里，
// M2 只做机制验证不做手感调参，等进入调参阶段（M5）再把两者一起挪进 config。
const PlayerSpeed = 6.0

// PlayerRadius 是玩家的碰撞半径（米）。
//
// 灰盒阶段写死：小于半格（地形格 1 m），保证圆覆盖的格数有限、判定便宜。
// 手感调参（M5）与 PlayerSpeed 一起挪 config。
const PlayerRadius = 0.3

// slideStep 是滑墙判定的最大采样步长（米）。
//
// 一个子步的位移可能远大于直径（tick 放大、击退叠加后一步 2 m 很现实），
// 只判终点会直接跳过墙格。把位移切成不超过 slideStep 的小段逐段判定，
// 撞到即停在该段——等价于用固定步长采样连续运动，钳位误差 ≤ slideStep。
// slideStep 必须小于格边长，否则单个采样段自身就能跨格穿墙。
const slideStep = 0.15

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

// Integrate 按当前意图推进一个子步的位置，带地形碰撞（分轴滑墙）。
//
// X/Y 分轴处理：撞墙的那根轴把速度清零并停在墙边，另一根轴继续走——
// 这就是"贴墙滑动"而不是"完全卡死"。斜着撞墙若整体判定（先合成位移再
// 一次判定），撞不上就全走、撞上就全停，贴墙斜走时会一步走一步停，手感
// 像在磕头。分轴后任何斜向输入在墙边都会退化成沿墙的匀速滑动。
//
// 一个子步的位移可能远大于碰撞圆直径（dt 放大、击退叠加），只判终点会
// 直接跳过墙格。所以位移被切成不超过 slideStep 的小段逐段判定：段内
// CircleFree 才前进，否则该轴速度清零、停在本段起点（不二分求墙边精确
// 距离——误差 ≤ slideStep，灰盒接受）。
//
// 顺序固定先 x 后 y。这不是可交换的处理：y 段判定用的是本子步已钳位后的
// Pos.X（y 沿着撞墙后的 x 走），交换顺序结果一般不同。回放的确定性来自
// 「顺序固定」本身，不来自「顺序无关」。
//
// tr 不判 nil：NewWorld 恒建 Terrain，nil 是编程错误，panic 比静默直通好。
// 与 IntegrateBullets 对称（同样直接用 Terrain）。
func Integrate(p *world.PlayerState, dt float64, tr *world.Terrain) {
	// 只有活着的玩家能移动。鬼魂另有一套速度，留给 M5。
	if p.State != world.PlayerAlive {
		p.Vel = world.Vec2{}
		return
	}

	p.Vel = world.V(p.MoveIntent.X*PlayerSpeed, p.MoveIntent.Y*PlayerSpeed)

	// 子步位移切成等长小段逐段判定。最后一段允许更短，保证总位移精确。
	dx := p.Vel.X * dt
	dy := p.Vel.Y * dt
	steps := int(math.Max(math.Abs(dx), math.Abs(dy))/slideStep) + 1
	segX := dx / float64(steps)
	segY := dy / float64(steps)
	// blockedX/blockedY 是循环内的局部停止标志——被墙挡住只停推进，
	// 不清 p.Vel。Vel 的语义是「玩家想往哪走」（快照 QuantizeVel 下发给
	// 客户端做插值），被挡是位置语义，写回 Vel 会让客户端看到速度分量
	// 间歇归零又恢复（闪烁）。两轴全挡时靠下面的 break 提前退出。
	blockedX := false
	blockedY := false
	for i := 0; i < steps; i++ {
		// x 轴：这一段走不过去就停步，y 不受影响（滑墙的关键）。
		if !blockedX && p.Vel.X != 0 {
			nx := p.Pos.X + segX
			if tr.CircleFree(world.V(nx, p.Pos.Y), PlayerRadius) {
				p.Pos.X = nx
			} else {
				blockedX = true
			}
		}
		// y 轴同理。用更新后的 Pos.X 判定——先 x 后 y，y 段沿已钳好的 x 走。
		if !blockedY && p.Vel.Y != 0 {
			ny := p.Pos.Y + segY
			if tr.CircleFree(world.V(p.Pos.X, ny), PlayerRadius) {
				p.Pos.Y = ny
			} else {
				blockedY = true
			}
		}
		if blockedX && blockedY {
			break
		}
	}
}

func isFinite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}
