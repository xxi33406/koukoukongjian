package room

import (
	"math"
	"testing"
	"time"

	"koukoukongjian/internal/game/world"
	"koukoukongjian/internal/proto"
)

// 本文件是 M4 Task 8 的集成测试：升级 → 发候选 → 窗口无敌 → 收选择 → 超时。
//
// 并发约束（AGENTS.md §2.5）：房间跑起来之后 *world.World 归 tick goroutine
// 独占，测试 goroutine 读写它就是数据竞争。所以：
//   - 摆怪、改配置一律在 startRoomWith 的 setup 回调里（Run 之前）；
//   - 观察一律走快照与房间导出的可靠下行通道（channel 本身并发安全）；
//   - 收选择走 Room.ApplyChoice（它内部投 channel，由 tick 循环消费）。

// waitReliable 从房间的可靠下行通道取消息，直到 cond 成立或超时。
//
// 同时排空快照通道：等可靠消息的这几百毫秒里快照仍在 20Hz 下发，不消费
// 就会把缓冲塞满，随后 waitSnapshot 先读到一大批过期帧——"等无敌标记
// 消失"这种断言会在旧帧上误判成已达成（旧帧里的位与当前状态无关）。
func waitReliable(t *testing.T, r *Room, sink chan []byte, timeout time.Duration, what string, cond func(*proto.Envelope) bool) *proto.Envelope {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("等待可靠消息超时：%s", what)
		}
		select {
		case msg, ok := <-r.Reliable():
			if !ok {
				t.Fatalf("可靠通道已关闭，等待 %s 失败", what)
			}
			// 通道上流的是"座位号 + 已编码字节"，与网络上收到的东西同形。
			// 在测试里解码而不是让 room 交出 *Envelope，是为了让测试走
			// 与真实客户端完全相同的编码路径——编码错误在测试里就该炸。
			var env proto.Envelope
			if err := proto.Unmarshal(msg.Data, &env); err != nil {
				t.Fatalf("可靠消息解码失败：%v", err)
			}
			if cond(&env) {
				return &env
			}
		case _, ok := <-sink:
			if !ok {
				// 关闭的通道在 select 里永远就绪，会让上面的循环变成忙等。
				// 置 nil 后这个 case 永久阻塞，语义变成"不再看快照"。
				sink = nil
			}
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// skillChoicesOf 取出 Envelope 里的 SkillChoices，不是该消息时返回 nil。
func skillChoicesOf(env *proto.Envelope) *proto.SkillChoices {
	if env == nil {
		return nil
	}
	if body, ok := env.Body.(*proto.Envelope_SkillChoices); ok {
		return body.SkillChoices
	}
	return nil
}

// waitSkillChoices 等一条技能候选消息。
func waitSkillChoices(t *testing.T, r *Room, sink chan []byte, timeout time.Duration, what string) *proto.SkillChoices {
	t.Helper()
	env := waitReliable(t, r, sink, timeout, what, func(env *proto.Envelope) bool {
		return skillChoicesOf(env) != nil
	})
	sc := skillChoicesOf(env)
	if sc == nil {
		t.Fatalf("%s：收到的不是技能候选消息", what)
	}
	return sc
}

// selfInvulnerable 从快照里读自己身上的无敌标记位。
//
// 走快照而不是读 PlayerState：后者的 Flags 只在 tick goroutine 里安全。
func selfInvulnerable(s *proto.Snapshot) bool {
	return s != nil && s.Self != nil &&
		s.Self.Flags&uint32(world.PlayerFlagInvulnerable) != 0
}

// selfHP 从快照里读自己的血量。
func selfHP(s *proto.Snapshot) (float64, bool) {
	if s == nil || s.Self == nil {
		return 0, false
	}
	return float64(s.Self.Hp), true
}

// progressionConfig 把成长参数调成"一次击杀恰好升一级"，供本文件用。
//
// 必须在 setup 回调（Run 之前）里调：这些字段同一个 tick 循环每 tick 都在
// 读（systems.AddExp 读 ExpBase/ExpGrowth，room 读 KillExp），测试 goroutine
// 在 Run 之后改它们就是数据竞争。
//
// 数值依据（config.Default 的曲线）：
//   - ExpBase=20 是 1→2 级的门槛；2→3 级门槛 = 20×1.35^(2-1) ≈ 27。
//   - KillExp=20：一次击杀刚好跨过 1→2 且不留残余经验。
//     KillExp 大于门槛会连升多级，而 systems.GrantKillExp 只返回"升级了的
//     玩家"列表、不含次数，多级只发一次候选——测试要的是"一次击杀一次
//     候选"，所以这里让每次击杀恰好一级。
func progressionConfig(r *Room, killExp uint32, windowS float64) {
	r.cfg.Progression.ExpBase = 20
	r.cfg.Progression.ExpGrowth = 1.35
	r.cfg.Progression.KillExp = killExp
	r.cfg.Progression.SkillChoiceWindowS = windowS
}

// TestRoomSendsSkillChoicesOnLevelUp 验证击杀升级后，服务端经可靠通道
// 下发一条三选一候选，且玩家进入无敌窗口。
func TestRoomSendsSkillChoicesOnLevelUp(t *testing.T) {
	var mID world.EntityID
	r, _ := startRoomWith(t, "levelup", func(r *Room) {
		progressionConfig(r, 20, 5)
		p := r.spawnPoint(0)
		// 5 HP 一发死；摆在座位 0 出生点正右方 3 m（Aim=0 是 +X）。
		mID = spawnTarget(r, world.V(p.X+3, p.Y), 5)
	})

	res, sink := joinPlayer(t, r, "shooter")
	r.SendInput(res.Slot, InputCommand{Seq: 1, Aim: 0, HasAim: true, Buttons: ButtonAttack})

	sc := waitSkillChoices(t, r, sink, 3*time.Second, "击杀升级后的三选一候选")

	if sc.Level != 2 {
		t.Errorf("候选的等级 = %d，期望 2（一次击杀升一级）", sc.Level)
	}
	if len(sc.Choices) != 3 {
		t.Fatalf("候选数量 = %d，期望 3（技能表 8 条、玩家未拥有任何技能）", len(sc.Choices))
	}
	if sc.WindowTicks == 0 {
		t.Errorf("窗口 tick 数 = 0，期望大于 0（客户端要按它画倒计时）")
	}
	// 候选互不重复，且 skill_id 落在合法的 1..8。
	seen := make(map[uint32]bool, len(sc.Choices))
	for i, c := range sc.Choices {
		if c.SkillId == 0 || c.SkillId > 8 {
			t.Errorf("第 %d 个候选的 skill_id = %d，期望落在 1..8", i, c.SkillId)
		}
		if seen[c.SkillId] {
			t.Errorf("第 %d 个候选 skill_id = %d 与前面重复", i, c.SkillId)
		}
		seen[c.SkillId] = true
	}

	// 怪确实死了（升级的前提），且玩家确实进了无敌窗口。
	waitSnapshot(t, sink, 3*time.Second, "怪物被回收", func(s *proto.Snapshot) bool {
		return removedContains(s, uint32(mID))
	})
	waitSnapshot(t, sink, 3*time.Second, "无敌窗口开启", selfInvulnerable)
}

// TestChooseSkillAppliesAndClearsInvuln 验证玩家选择后技能进槽位、无敌清除。
//
// "技能进槽位"在快照里观察不到（PlayerSelf 没有技能槽字段，见 Task 8
// 汇报的观测盲区），这里用一条强间接证据替代：再升一级时，新的候选列表
// 不含刚刚装上的技能——build.GenerateChoices 会排除玩家已拥有的技能。
func TestChooseSkillAppliesAndClearsInvuln(t *testing.T) {
	r, _ := startRoomWith(t, "choose", func(r *Room) {
		// KillExp=27：1→2 后剩余 7 点，2→3 门槛 27，第二次击杀 7+27=34
		// 又刚好跨过一级——两次击杀各升一级、都不留残余。
		progressionConfig(r, 27, 5)
		p := r.spawnPoint(0)
		spawnTarget(r, world.V(p.X+3, p.Y), 5) // 正右，供第一次升级
		spawnTarget(r, world.V(p.X-3, p.Y), 5) // 正左，转身后打
	})

	res, sink := joinPlayer(t, r, "chooser")
	r.SendInput(res.Slot, InputCommand{Seq: 1, Aim: 0, HasAim: true, Buttons: ButtonAttack})

	first := waitSkillChoices(t, r, sink, 3*time.Second, "第一次升级的候选")
	if len(first.Choices) == 0 {
		t.Fatalf("第一次候选数量 = 0，期望 3（没有候选就无从选起）")
	}
	chosen := first.Choices[0].SkillId

	waitSnapshot(t, sink, 3*time.Second, "选择前处于无敌窗口", selfInvulnerable)

	if err := r.ApplyChoice(res.Slot, 0); err != nil {
		t.Fatalf("应用选择失败：%v，期望 nil", err)
	}

	waitSnapshot(t, sink, 3*time.Second, "选择后无敌清除", func(s *proto.Snapshot) bool {
		return !selfInvulnerable(s)
	})

	// 转身打死后一只怪，用第二次升级的候选反推"第一个技能已进槽位"。
	r.SendInput(res.Slot, InputCommand{Seq: 2, Aim: math.Pi, HasAim: true, Buttons: ButtonAttack})
	second := waitSkillChoices(t, r, sink, 5*time.Second, "第二次升级的候选")

	if second.Level != 3 {
		t.Errorf("第二次候选的等级 = %d，期望 3", second.Level)
	}
	for i, c := range second.Choices {
		if c.SkillId == chosen {
			t.Fatalf("第二次候选的第 %d 项仍是已装的技能 %d，说明选择没有被装进槽位",
				i, chosen)
		}
	}
	if len(second.Choices) != 3 {
		t.Errorf("第二次候选数量 = %d，期望 3（8 条表比 1 个已装多得多）", len(second.Choices))
	}
}

// TestSkillChoiceTimeoutPicksRandom 验证玩家不选时，窗口到期自动装上一个
// 候选并清除无敌，且状态被正确清理（之后还能再升级、再收到候选）。
func TestSkillChoiceTimeoutPicksRandom(t *testing.T) {
	r, _ := startRoomWith(t, "timeout", func(r *Room) {
		// 窗口 0.4 秒 = 8 tick：够测试观察到"开着"，又能在几秒内超时。
		//
		// KillExp 取 27 而不是首级门槛 20：本用例要打两只怪、升两次级。
		// 取 20 时第一次击杀刚好用完经验（余 0、ExpToNext 变 27），第二只
		// 给的 20 不够升级，测试会等一个永远不来的候选（实测红灯：
		// "等待可靠消息超时：超时后再次升级的候选"）。27 让两次击杀都跨
		// 过门槛：首次 27 ≥ 20 升到 2 级余 7，第二次 7+27=34 ≥ 27 升到
		// 3 级余 7。
		progressionConfig(r, 27, 0.4)
		p := r.spawnPoint(0)
		spawnTarget(r, world.V(p.X+3, p.Y), 5)
		spawnTarget(r, world.V(p.X-3, p.Y), 5)
	})

	res, sink := joinPlayer(t, r, "idler")
	r.SendInput(res.Slot, InputCommand{Seq: 1, Aim: 0, HasAim: true, Buttons: ButtonAttack})

	first := waitSkillChoices(t, r, sink, 3*time.Second, "超时用例的第一次候选")
	const wantWindow = uint32(8) // 0.4s × 20Hz + 0.5 取整
	if first.WindowTicks != wantWindow {
		t.Errorf("窗口 tick 数 = %d，期望 %d（0.4s × 20Hz）", first.WindowTicks, wantWindow)
	}

	// 全程不发 ChooseSkill：等无敌出现，再等它自己消失。
	waitSnapshot(t, sink, 3*time.Second, "窗口开启（无敌出现）", selfInvulnerable)
	waitSnapshot(t, sink, 3*time.Second, "超时后无敌自动清除", func(s *proto.Snapshot) bool {
		return !selfInvulnerable(s)
	})

	// 状态必须被清理干净：转身打死后一只怪还能再升一级、再收到候选。
	// 只清了一半（比如 awaitingChoice 留着）会让第二次升级被并进已经
	// 关闭的窗口，表现是"再也不会弹三选一"。
	r.SendInput(res.Slot, InputCommand{Seq: 2, Aim: math.Pi, HasAim: true, Buttons: ButtonAttack})
	second := waitSkillChoices(t, r, sink, 5*time.Second, "超时后再次升级的候选")
	if second.Level != 3 {
		t.Errorf("第二次候选的等级 = %d，期望 3（两次击杀各升一级）", second.Level)
	}
	if len(second.Choices) == 0 {
		t.Errorf("第二次候选为空，期望至少 1 个")
	}
}

// TestInvulnerableDuringChoiceWindow 验证窗口内怪物打不动玩家：不掉血。
//
// 光断言"没掉血"不足以证明无敌生效——也可能只是怪没打到。所以窗口结束后
// 继续观察，必须看到掉血，用来说明那只怪一直在攻击。
func TestInvulnerableDuringChoiceWindow(t *testing.T) {
	r, _ := startRoomWith(t, "invuln", func(r *Room) {
		// 窗口 2 秒 = 40 tick，够怪攻击两轮（攻击冷却 1 秒）。
		progressionConfig(r, 20, 2)
		p := r.spawnPoint(0)
		spawnTarget(r, world.V(p.X+3, p.Y), 5) // 供升级，一发死
		// 攻击者摆在玩家正左 1 m：AttackRange 1.2 + 怪半径 0.4 + 玩家
		// 半径 0.3 = 1.9 m 以内，AI 直接进 MonsterAttack，每 1 秒打 10 点。
		// 放正左而不是正右，是避免玩家朝 +X 的子弹把它一起打死。
		spawnTarget(r, world.V(p.X-1, p.Y), 1000)
	})

	res, sink := joinPlayer(t, r, "tank")
	r.SendInput(res.Slot, InputCommand{Seq: 1, Aim: 0, HasAim: true, Buttons: ButtonAttack})

	waitSkillChoices(t, r, sink, 3*time.Second, "升级后进入选择窗口")

	baseHP := -1.0
	waitSnapshot(t, sink, 5*time.Second, "窗口开启且窗口内不掉血", func(s *proto.Snapshot) bool {
		if !selfInvulnerable(s) {
			return false
		}
		hp, ok := selfHP(s)
		if !ok {
			return false
		}
		if baseHP < 0 {
			baseHP = hp
		} else if hp < baseHP {
			t.Fatalf("无敌窗口内血量下降：%v → %v（窗口内不该掉血）", baseHP, hp)
		}
		return true
	})

	// 窗口内持续观察，直到无敌自己消失（超时随机），全程不允许掉血。
	waitSnapshot(t, sink, 5*time.Second, "窗口结束（无敌清除）", func(s *proto.Snapshot) bool {
		hp, ok := selfHP(s)
		if ok && baseHP >= 0 && hp < baseHP {
			t.Fatalf("无敌窗口内血量下降：%v → %v（窗口内不该掉血）", baseHP, hp)
		}
		return !selfInvulnerable(s)
	})

	// 无敌一清，同一只怪的攻击必须立刻生效——证明它确实一直在打。
	waitSnapshot(t, sink, 5*time.Second, "窗口结束后恢复受伤", func(s *proto.Snapshot) bool {
		hp, ok := selfHP(s)
		return ok && hp < baseHP
	})
}
