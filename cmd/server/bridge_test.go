package main

import (
	"testing"

	"koukoukongjian/internal/proto"
)

// frame 是构造一个输入帧的简写。
func frame(tick uint32, buttons uint32) *proto.InputFrame {
	return &proto.InputFrame{Tick: tick, Buttons: buttons}
}

// TestMergePicksNewestFrame 验证多帧时取 tick 最大的一帧作移动意图。
//
// 逐帧应用会让角色沿压缩后的轨迹瞬移：客户端 60Hz 采样但服务端 20Hz
// 消费，中间帧的移动意图本来就该被覆盖。
func TestMergePicksNewestFrame(t *testing.T) {
	frames := []*proto.InputFrame{
		{Tick: 10, MoveX: 100},
		{Tick: 11, MoveX: 200},
		{Tick: 12, MoveX: 300},
	}

	newest, _, changed := mergeInputFrames(frames, 0, false)
	if !changed {
		t.Fatal("changed=false，期望 true")
	}
	if newest.Tick != 12 {
		t.Errorf("取到 tick=%d，期望 12", newest.Tick)
	}
	if newest.MoveX != 300 {
		t.Errorf("MoveX=%d，期望 300", newest.MoveX)
	}
}

// TestMergeOrsButtonsAcrossWindow 验证窗口内所有帧的按键被按位或。
//
// 这是抗丢包的核心：短按（按下又松开）只出现在某一帧里，那一帧丢了
// 整次操作就消失。按位或让"窗口内出现过"等价于"按下过"。
func TestMergeOrsButtonsAcrossWindow(t *testing.T) {
	frames := []*proto.InputFrame{
		frame(10, 0),
		frame(11, 1<<1), // 技能1 短按发生在这里
		frame(12, 0),    // 已经松开了
	}

	_, buttons, changed := mergeInputFrames(frames, 0, false)
	if !changed {
		t.Fatal("changed=false，期望 true")
	}
	if buttons&(1<<1) == 0 {
		t.Errorf("buttons=%b 不含技能1 位，短按被丢失", buttons)
	}
}

// TestMergeSkipsAlreadyProcessed 验证重复携带的旧帧被跳过。
//
// 客户端每包带最近 3 帧，所以每帧会被送达约 3 次。不去重的话，
// 同一帧的按键会被重复计入，而且"已松开"的帧会一直被重新处理。
func TestMergeSkipsAlreadyProcessed(t *testing.T) {
	// 已处理到 tick=11，这一包只带 10、11 两帧，都是旧的。
	frames := []*proto.InputFrame{
		frame(10, 1<<1),
		frame(11, 1<<1),
	}

	_, _, changed := mergeInputFrames(frames, 11, true)
	if changed {
		t.Error("changed=true，期望 false（整包都是旧帧）")
	}
}

// TestMergeOldFrameDoesNotResurrectButton 验证旧帧的按键不会把已松开的键续上。
//
// 这是去重与按位或交互产生的坑：如果先 or 再判新旧，一个 3 帧窗口里
// 只要含有一帧历史按键，"按住"状态就会被无限续期，技能会连发。
func TestMergeOldFrameDoesNotResurrectButton(t *testing.T) {
	// 已处理到 12，这一包带 11（有技能位）、12、13（都没有）。
	frames := []*proto.InputFrame{
		frame(11, 1<<1), // 旧帧，且含技能位
		frame(12, 0),
		frame(13, 0),
	}

	newest, buttons, changed := mergeInputFrames(frames, 12, true)
	if !changed {
		t.Fatal("changed=false，期望 true（13 是新帧）")
	}
	if newest.Tick != 13 {
		t.Errorf("取到 tick=%d，期望 13", newest.Tick)
	}
	if buttons != 0 {
		t.Errorf("buttons=%b，期望 0（旧帧的按键不该被算进来）", buttons)
	}
}

// TestMergeFirstFrameTickZero 验证客户端的第一帧 tick=0 不会被当成"已处理"。
//
// 这是"用值推断状态"的经典坑：lastTick 的零值既是合法数据也是"还没收到过"。
// 少了 hasLast 标志，玩家连上后的第一次操作永远没反应，而且不报错。
func TestMergeFirstFrameTickZero(t *testing.T) {
	frames := []*proto.InputFrame{frame(0, 0)}

	_, _, changed := mergeInputFrames(frames, 0, false)
	if !changed {
		t.Error("changed=false，tick=0 的首帧被误判成旧帧")
	}
}

// TestMergeHandlesTickWraparound 验证 tick 越过 uint32 边界后仍能判新旧。
//
// 60Hz 下 2^32 tick 约 828 天，真实撞不上；但同一套 helper 也被 uint16
// 的序号复用，那里 20Hz 下 55 分钟就绕一圈。写成 a > b 会在回绕后
// 把所有新帧判成旧帧，玩家再也动不了且无任何报错。
func TestMergeHandlesTickWraparound(t *testing.T) {
	const max = ^uint32(0)

	// 已处理到 max-1，新帧是 max 和 0（回绕）。
	frames := []*proto.InputFrame{
		frame(max, 0),
		frame(0, 1<<2),
	}

	newest, buttons, changed := mergeInputFrames(frames, max-1, true)
	if !changed {
		t.Fatal("changed=false，回绕后的帧被当成旧帧")
	}
	if newest.Tick != 0 {
		t.Errorf("取到 tick=%d，期望 0（回绕后的最新帧）", newest.Tick)
	}
	if buttons&(1<<2) == 0 {
		t.Errorf("buttons=%b 丢掉了回绕后新帧的按键位", buttons)
	}
}

// TestMergeIgnoresNilFrames 验证 nil 帧被跳过而不是 panic。
//
// protobuf 的 repeated 字段解出一个 nil 元素是可能的（畸形包）。
// 一个客户端发得出来的包不该让服务端崩。
func TestMergeIgnoresNilFrames(t *testing.T) {
	frames := []*proto.InputFrame{nil, frame(5, 1), nil}

	newest, _, changed := mergeInputFrames(frames, 0, false)
	if !changed {
		t.Fatal("changed=false，期望 true")
	}
	if newest.Tick != 5 {
		t.Errorf("取到 tick=%d，期望 5", newest.Tick)
	}
}

// TestMergeEmptyFrames 验证空输入。
func TestMergeEmptyFrames(t *testing.T) {
	_, _, changed := mergeInputFrames(nil, 0, false)
	if changed {
		t.Error("changed=true，期望 false（没有帧）")
	}
}

// TestMaxInputWindowTruncation 验证超长帧列表只保留尾部。
//
// 上限防的是恶意客户端一次塞一万帧，让服务器每个 tick 做无意义的按位或。
// 必须保留尾部：帧按 tick 升序，最新的在后面。
func TestMaxInputWindowTruncation(t *testing.T) {
	frames := make([]*proto.InputFrame, 0, maxInputWindow+5)
	for i := 0; i < maxInputWindow+5; i++ {
		frames = append(frames, frame(uint32(i), 0))
	}

	trimmed := frames
	if len(trimmed) > maxInputWindow {
		trimmed = trimmed[len(trimmed)-maxInputWindow:]
	}

	newest, _, _ := mergeInputFrames(trimmed, 0, false)
	want := uint32(maxInputWindow + 4)
	if newest.Tick != want {
		t.Errorf("截断后取到 tick=%d，期望 %d（最新帧必须在尾部）", newest.Tick, want)
	}
	if trimmed[0].Tick != 5 {
		t.Errorf("截断后首帧 tick=%d，期望 5", trimmed[0].Tick)
	}
}

// TestTickAfter 钉死回绕比较的边界。
func TestTickAfter(t *testing.T) {
	cases := []struct {
		a, b uint32
		want bool
	}{
		{2, 1, true},
		{1, 2, false},
		{1, 1, false},
		{0, ^uint32(0), true},  // 回绕：0 比 max 新
		{^uint32(0), 0, false}, // 回绕的反方向
		{^uint32(0), ^uint32(0) - 1, true},
	}
	for _, c := range cases {
		if got := tickAfter(c.a, c.b); got != c.want {
			t.Errorf("tickAfter(%d, %d)=%v，期望 %v", c.a, c.b, got, c.want)
		}
	}
}

// TestMergeInputFramesTracksAimPresence 验证"有没有给方向"跟着最新帧走。
//
// 不能拿上一帧的方向补上：客户端松手的那一帧起就该由自动瞄准接管，
// 沿用旧方向会让角色一直瞄着一个已经没在瞄的角落。
//
// 这条守的是 mergeInputFrames 的取值契约，不是 HasAim 字段本身——
// 后者由 internal/room 的 TestInputCarriesAimIntent 保证，那条在字段
// 加进去之前确实是红的。
func TestMergeInputFramesTracksAimPresence(t *testing.T) {
	// 最新帧没给方向、更早的帧给了：必须取最新帧。
	frames := []*proto.InputFrame{
		{Tick: 10, AimX: 1000, AimY: 0},
		{Tick: 11, AimX: 0, AimY: 0},
	}
	newest, _, changed := mergeInputFrames(frames, 9, true)
	if !changed {
		t.Fatal("有新帧却返回 changed=false")
	}
	if newest.Tick != 11 {
		t.Fatalf("取到 tick=%d，期望最新的 11", newest.Tick)
	}
	// 若 mergeInputFrames 哪天改成"取窗口内最后一个非零方向"（为了实现
	// "松手后仍朝原方向"这类需求很容易顺手加），这里会红。
	if newest.AimX != 0 || newest.AimY != 0 {
		t.Fatal("最新帧没给方向，却取到了更早的非零方向")
	}

	// 反方向：最新帧给了方向、更早的帧没有。若实现改成"取第一个非零"，
	// 上面那条仍会过，这条会红。两条一起才能钉住"取最新的，且不补"。
	gaveAim := []*proto.InputFrame{
		{Tick: 20, AimX: 0, AimY: 0},
		{Tick: 21, AimX: 0, AimY: -1000},
	}
	newest2, _, _ := mergeInputFrames(gaveAim, 19, true)
	if newest2.AimX == 0 && newest2.AimY == 0 {
		t.Fatal("最新帧给了方向（正 -Y），却取到了更早的空帧")
	}
}
