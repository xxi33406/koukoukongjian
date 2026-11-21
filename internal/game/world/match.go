package world

// MatchOutcome 是本局的结果（server-design §11.1 的 match_result.outcome）。
//
// 用枚举而不是 bool：spec 里明确有三种终态（团灭/通关/未结束），而
// "未结束"必须是零值——房间关闭时可能既没团灭也没通关（进程被 kill、
// 或者玩家全部离开）。用 bool 的话"没打完"与"团灭"会撞在 false 上。
type MatchOutcome uint8

const (
	// OutcomeUnknown 表示本局没有正常结束（服务器被中断、玩家全部离开）。
	OutcomeUnknown MatchOutcome = iota
	// OutcomeWiped 是团灭：全队变鬼魂（spec L52）。
	OutcomeWiped
	// OutcomeCleared 是通关：全部据点清完且 Boss 被击败（spec L94）。
	OutcomeCleared
)

func (o MatchOutcome) String() string {
	switch o {
	case OutcomeWiped:
		return "wiped"
	case OutcomeCleared:
		return "cleared"
	default:
		return "unknown"
	}
}

// PlayerResult 是一名玩家在一局里的战绩（§11.1 player_result 的一行）。
type PlayerResult struct {
	// Player 是局内实体 id。落盘时换成 slot——实体 id 会随池槽位复用
	// 而失去意义，不能当历史主键。
	Player       EntityID `json:"-"`
	Slot         int      `json:"slot"`
	Damage       float64  `json:"damage"`
	Contribute   float64  `json:"contribute"`
	Kills        uint32   `json:"kills"`
	ReflectKills uint32   `json:"reflect_kills"`
	Deaths       uint32   `json:"deaths"`
	Gold         uint32   `json:"gold"`
}

// MatchResult 是一局的结果（§11.1 match_result + player_result）。
//
// 同时是落盘结构与协议结构：store 直接 json.Marshal 它，snapshot 从它转
// proto.MatchResult。三处各写一份结构一定会漂——AGENTS §1.2 的「分别设计
// 接口的两半」讲的就是这个。
type MatchResult struct {
	RoomID      string         `json:"room_id"`
	WorldSeed   int64          `json:"world_seed"`
	StartedAt   int64          `json:"started_at"` // Unix 秒
	EndedAt     int64          `json:"ended_at"`
	Outcome     MatchOutcome   `json:"outcome"`
	MaxWave     int            `json:"max_wave"`
	DurationSec int64          `json:"duration_sec"`
	Players     []PlayerResult `json:"players"`
}

// SnapshotMatch 把当前世界聚成一局结果。
//
// startedAt/endedAt 由调用方给（room 知道真实时钟，world 不持有）：world
// 里只有逻辑 tick，而 §11.1 要的是墙体时钟语义，两者不能互推——用 tick
// 换算会在 tick 超时（Run 里的 overrun 丢弃落后时间）时算错时长。
//
// 只收集池里存活的槽位（Pool.Each 跳过 alive=false），因此"中途离开的
// 玩家"不会出现在结果里。这不是丢数据——他确实不在这局的战绩里，
// 而"曾经加入过"这件事需要另外的记录通道，v0.1 不做。
func (w *World) SnapshotMatch(roomID string, outcome MatchOutcome, startedAt, endedAt int64) MatchResult {
	res := MatchResult{
		RoomID:    roomID,
		WorldSeed: w.Seed,
		StartedAt: startedAt,
		EndedAt:   endedAt,
		Outcome:   outcome,
		MaxWave:   w.maxWave(),
	}
	// 时钟倒退（NTP 校时、容器迁移）不许产生负时长：§11.1 的
	// duration_sec 是有符号列，但负值会被下游按"数据损坏"处理。
	if d := endedAt - startedAt; d > 0 {
		res.DurationSec = d
	}

	w.Players.Each(func(id EntityID, p *PlayerState) bool {
		// Slot 暂用池索引；room 层把它换成真实座位号（两者在 4 人局里
		// 通常一致，但池槽位复用时索引会变）。
		//
		// Damage 读 TotalDamage 而不是 Contribute：后者是据点级的，
		// SettleHub 每清一个据点清零（spec L79「每据点重新计算」）。
		// 拿它算整局战报会让"最后一个据点没杀人"的玩家显示 0 伤害。
		res.Players = append(res.Players, PlayerResult{
			Player:       id,
			Slot:         int(id.Index()),
			Damage:       p.TotalDamage,
			Contribute:   p.TotalDamage + 0.5*p.TotalDamageTaken,
			Kills:        p.Kills,
			ReflectKills: p.ReflectKills,
			Deaths:       p.Deaths,
			Gold:         p.Gold,
		})
		return true
	})
	return res
}

// maxWave 取所有据点里打到的最高波数。
//
// 不用「已完成据点数」当进度：玩家可能清完 6 个据点而最后一个卡在第 2 波，
// 那才是他真实打到的深度。取 max 而不是 sum，因为波数是"每据点 5~8 波"
// 的同一把尺子，sum 会随据点数量线性膨胀，两局没法比。
func (w *World) maxWave() int {
	best := 0
	for i := range w.Hubs {
		if w.Hubs[i].Wave > best {
			best = w.Hubs[i].Wave
		}
	}
	return best
}
