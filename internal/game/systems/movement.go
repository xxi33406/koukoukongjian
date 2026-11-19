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

// GhostSpeedMul 是鬼魂相对活人的速度倍率。
//
// 0.8 的取法：鬼魂是后勤位（spec L49「可探图、可跟随存活玩家」），比活人
// 慢一点符合"不是战斗单位"，但又不能慢到跟不上队伍——拴绳半径就那么点，
// 太慢会让鬼魂每 tick 都被钳在边界上，走起来一顿一顿。
// 0.8 在 20Hz 下每 tick 走 0.24 m，追赶一张图的时间是可接受的。
const GhostSpeedMul = 0.8

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
	// 三态而不是"非 Alive 一律不动"：鬼魂要能走（spec L49「可探图」）。
	// 倒地与掉线仍然钉住——它们不是设计里的可玩状态，掉线的身体不该自己
	// 乱跑（人已经走了，屏幕上那个壳子再移动只会让人以为他还在）。
	//
	// 为什么鬼魂放在 Integrate 里而不是另立一个 IntegrateGhost：分轴滑墙、
	// 子步扫掠、被挡不清 Vel 这三段逻辑对两者完全一样，唯一的差别是
	// 速度怎么算。复制一份的话，将来改动扫掠语义（比如 slideStep 变化）
	// 必须记得改两处，漏一处就出现"活人能拐弯鬼魂卡墙角"这种只在死后
	// 才显形的 bug。
	var speedMul float64
	switch p.State {
	case world.PlayerAlive:
		// 速度 = 基准 × 常驻倍率（被动疾行）× 限时倍率（冲刺）。
		//
		// 两个倍率来自不同的地方，这是刻意的：
		// 常驻倍率在 p.Derived.SpeedMul（RecomputeDerived 按已装被动算出来的），
		// 限时倍率在 p.SpeedBuffMul（CastActive 施放那一刻快照进来的）。
		//
		// 为什么不在这一行查 build.SkillByID(SkillIDDash) 拿冲刺倍率：分层上
		// 可行（build 只依赖标准库，systems 依赖 build 不成环，本包的 skills.go
		// 已经这么用了），但语义上不对。技能可换（spec L37），玩家在冲刺途中
		// 卸下冲刺会让一个已经开始的效果中途改值或失效；而且这里每个子步都要
		// 跑，一次可避免的表遍历不值得。快照方案让 Integrate 的契约保持干净：
		// 只读 world 数据，不知道技能表的存在。
		//
		// Derived 走 Effective()：Pool.Spawn 出来的玩家还没重算过，倍率是零值，
		// 直接乘会让"刚出生的人不能动"。兜底见 world.PlayerDerived.Effective。
		speedMul = p.Derived.Effective().SpeedMul
		if p.SpeedBuffLeft > 0 && p.SpeedBuffMul > 0 {
			speedMul *= p.SpeedBuffMul
		}
	case world.PlayerGhost:
		// 鬼魂不吃任何速度加成：常驻被动（疾行）与限时冲刺都来自技能，
		// 而鬼魂不是战斗单位。让它们生效会出现"死了反而跑得快"，
		// 且拴绳会每 tick 触发，鬼魂被钳得原地抖。
		//
		// 为什么必须在 Integrate 里显式忽略而不是靠 Kill() 清干净：
		// Kill() 清的是 SpeedBuffMul/SpeedBuffLeft（限时增益），而
		// Derived.SpeedMul 是 RecomputeDerived 算出来的常驻值——Kill 不清它，
		// 因为清了复活后还得重算一遍，反而多一个「忘了重算」的坑。
		speedMul = GhostSpeedMul
	default:
		// PlayerDowned / PlayerDisconnected，以及将来新增的状态默认钉住：
		// 新状态想动就必须在这里显式加分支，不会因为"忘了处理"而让一个
		// 不该移动的状态悄悄按鬼魂速度跑起来。
		p.Vel = world.Vec2{}
		return
	}

	speed := PlayerSpeed * speedMul
	p.Vel = world.V(p.MoveIntent.X*speed, p.MoveIntent.Y*speed)

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
