# koukoukongjian

元气骑士品类的 **4 人合作 PvE 弹幕肉鸽**游戏服务器。Go 编写，权威服，从零实现——自研 UDP 传输层、确定性模拟、格子寻路、怪物 AI、元素反应、死亡循环、Boss 战。

> 这个项目的目标是**学习游戏后端**。它面向局域网 / 内网自娱，**不上公网**，不做运营能力（无账号、无支付、无反外挂、无水平扩展）。
>
> Unity 客户端尚未开始。服务端可以独立跑通一整局，用内置 bot 验证。

---

## 现在能跑什么

一个玩家从连入房间到打完 Boss 的完整链路已经可用：

```
连入（Hello → JoinRoom，可靠通道拿到座位号与世界种子）
  ↓
出生在出生据点，迷雾只亮周围 8 米
  ↓
手动瞄准射击 / 松开方向键走服务端自动瞄准，打死怪掉经验与掉落物
  ↓
升级 → 三选一技能（窗口内无敌，5 秒不选随机给）
  ↓
进下一个据点触发波次，清完 3 波变「据点已清」，开商店买技能与道具
  ↓
死在哪个元素据点，尸体旁掉的就是那个元素的技能
  ↓
死了变鬼魂（0.8 倍速、可探图、被拴在最近的活人身边），去祭坛引导 3 秒复活，半血
  ↓
所有据点清完 → 最后一个据点刷 Boss，血量过半进二阶段（狂暴 + 召唤小怪）
  ↓
打完 Boss → 结算下发到每个客户端，同时落盘一行 JSONL
```

全队变鬼魂 = 团灭，本局失败。

---

## 快速开始

需要 **Go 1.27+**。

```bash
# 起服务器（默认读 configs/server.toml，监听 0.0.0.0:7777）
go run ./cmd/server

# 另开一个终端，放 2 个 bot 进来打
go run ./cmd/bot -count 2 -attack
```

服务器的 tick 是 20Hz，每 tick 3 个物理子步（60Hz 物理）。

### 端到端验证一个完整流程

```bash
# 摆 4 个静止靶，bot 自动瞄准打掉它们
go run ./cmd/server -dummy-targets 4
go run ./cmd/bot -count 2 -attack -hold-aim=false -circle=false
```

```bash
# 走真实关卡：据点波次 + Boss（用一份小配置让一局跑到头的速度快些）
go run ./cmd/server -waves -seed 42 -duration 120s
go run ./cmd/bot -count 4 -attack -duration 110s -hold-aim=false -circle=false
```

> ⚠️ **bot 的 `-circle` 默认是 true，会一直绕圈移动，基本打不中怪。**
> 想看到击杀、波次推进、Boss 流程，必须显式加 `-circle=false -hold-aim=false`。
> `-hold-aim=false` 表示不给瞄准方向，由服务端自动瞄准最近的怪。

### 落盘对局结果

```bash
go run ./cmd/server -match-log ./out/matches.jsonl
```

一局一行 JSON，字段见 `internal/game/world/match.go` 的 `MatchResult`。

### 验证

```bash
gofmt -l internal cmd                          # 必须无输出
go vet ./...                                   # 必须无告警
go test ./...                                  # 必须全绿
CGO_ENABLED=1 go test -race -count=1 ./...     # 数据竞争检测
```

`-race` 需要 cgo，Windows 上得先装 gcc（例如 mingw64）并 `go env -w CC=<path>/gcc.exe`。
**生产构建保持 `CGO_ENABLED=0`**，cgo 只用于测试。

---

## 架构

```
cmd/server, cmd/bot  ──→  internal/{config, obs, room, netx, proto, snapshot, game}
internal/room        ──→  internal/{game/world, game/systems, snapshot, proto, config}
internal/snapshot    ──→  internal/{game/world, proto}
internal/netx        ──→  标准库
internal/game/**     ──→  不 import 任何网络 / 房间包
```

- `cmd/server/bridge.go` 是 `netx` 与 `room` **唯一**的交叉点。
- `internal/game/world` 是叶子包，只依赖 `config`。

### 并发模型

```
网络 goroutine（每会话一个）         tick goroutine（每房间一个）
        │                                    │
        │  channel 投递输入                   │  独占持有 *world.World
        ├───────────────────────────────────→│
        │                                    │
        │  ←─────────────────────────────────┤  快照走有界 channel，满了丢最旧
        │  自己发 UDP                         │
```

规则：

1. `*world.World` 的唯一持有者和访问者是 `Room.Run` 那个 goroutine。
2. 上行输入走 channel，tick **非阻塞**读，绝不阻塞 tick。
3. 下行快照走每会话有界 channel，满了丢最旧的。
4. 会话自身的可变状态由网络 goroutine 独占，tick 只读不可变副本。

这条纪律是靠**结构**保证的，不是靠 `-race`——`-race` 只能证明"跑过的路径上没撞上"。

### 确定性

同样的种子 + 同样的输入序列 = 同样的世界。

- `dt` 是固定的 `1/(tickRate*subSteps)`，**绝不用真实耗时**——否则模拟随负载变化，回放全废。
- 所有随机数走 `World.RandIntn / RandFloat64 / RandRange` 包装，带调用计数。
- 模拟路径里**禁止遍历 map**（顺序随机），一律走池子的槽位升序。

---

## 代码规模

| 项 | 值 |
| --- | --- |
| Go 包 | 15 |
| 生产代码 | 15,349 行（不含 `*.pb.go` 与测试） |
| 测试代码 | 19,843 行 |
| 测试函数 | 725 个 |
| 提交 | 124 个 |

直接依赖只有 **2 个**：`BurntSushi/toml`（配置）与 `google.golang.org/protobuf`（载荷序列化）。传输层、寻路、AI、空间索引全部手写。

---

## 实现状态

| 里程碑 | 内容 |
| --- | --- |
| M0 工程基线 | 配置（TOML，拒绝未知键）/ 日志 / Vec2、实体池、空间网格、地形 / 房间 tick |
| M1 网络闭环 | 自研 UDP 双通道、12 字节包头、分片、会话、bot |
| M2 战斗核心 | 武器表、扫掠碰撞、伤害唯一入口、死亡标记与回收分离、自动瞄准兜底 |
| M3 世界与波次 | 地图生成（据点 + 最小生成树走廊 + 连通性校验）、A\* + 拐点压缩、AI 四态与仇恨、波次、迷雾 |
| M4 构筑 | 等级经验、技能池、升级三选一、窗口无敌、背包、拾取物 |
| M5 死亡循环 | 鬼魂状态、拴绳、祭坛复活、掉落、鬼魂搬运、团灭 |
| M6 差异化 | 元素反应、区域效果、子弹网格、反射面、弹幕交汇、据点元素、贡献分账、商店、距离惩罚 |
| M7 收口 | 战绩统计、终局判定、JSONL 落盘、结算下发、Boss 两阶段 + 元素易伤、tick 分位数指标 |

**尚未实现**：Unity 客户端、第二关、场景道具（油桶 / 火药）、投掷物实装、掉线重连、SQLite 跨局查询。

---

## 目录结构

```
cmd/
  server/          服务器可执行（bridge 是 netx 与 room 的唯一交叉点）
  bot/             压测 / 冒烟用 bot 客户端
configs/
  server.toml      配置样例。所有键可省略，省略即用内置默认值
internal/
  config/          TOML 加载与校验（拒绝未知键、拒绝 NaN/Inf）
  obs/             结构化日志（slog）
  netx/            自研 UDP：双通道、分片、会话、序号与 ack
  proto/           protobuf 定义与生成代码；载荷序列化
  room/            房间：tick 循环、座位、输入收集、快照广播、各子系统接线
  snapshot/        增量快照构建（字段级 diff，全量帧兜底）
  store/           JSONL 追加写入器（队列满丢弃，绝不阻塞）
  game/
    world/         世界状态：实体池、地形、向量、伤害、据点、寻路、对局结果
    systems/       无状态系统：移动、开火、碰撞、技能、区域、掉落、复活、分账、Boss
    build/         构筑数据表：技能、道具、元素反应表、三选一
    ai/            怪物状态机与仇恨打分
    mapgen/        地图生成
    fog/           队伍共享迷雾（bitset）
```

---

## 读代码前值得知道的几个决定

这些是踩过坑之后定下来的，改之前建议先想清楚为什么要改：

- **整条消息只占一个 seq**，不是每片一个。可靠通道是有序字节流，"半条消息"没有意义。
- **包头是手写二进制，只有载荷是 protobuf**。传输层必须先读 seq/ack/ch 才能决定丢不丢，这 12 字节不能走 protobuf。
- **伤害只有一个入口**：`world.ResolveDamage`。想加新的伤害来源就往它走，不要在调用方直接扣血。
- **两个实体池会撞号**（玩家池和怪物池各自从 index 1 编号）。所以 `ResolveDamage` 带 `EntityClass` 参数，快照的 delta 键带 `EntityKind`——**不能靠"先查怪物池"猜类型**，那是真实修过的 bug。
- **玩家死亡判据是 `State != PlayerAlive`**，怪物死亡判据是 `MonsterFlagDead` 显式标志。不要用 `HP <= 0` 判——加治疗或复活之后它会骗人。
- **冷却钳在 `-dt`**，不是钳在 0。钳 0 会让冷却短于一 tick 的武器被静默压速。
- **波次事件不进 proto**。`GameEventKind` 加一个枚举会连累客户端联调，波次变化只进服务端日志。
- **金币不直给玩家**，记进据点池子，清完据点后 30% 保底均分 + 70% 按贡献分账。直给会让弱贡献者一直拿不到钱。
- **JSONL 不引 SQLite**。一局一条、只追加、零依赖。写入器队列满就丢并计数，绝不让落盘阻塞 tick。

---

## 配置

`configs/server.toml` 是完整样例，每个键都有注释。几个值得注意的：

```toml
[server]
tick_rate = 20   # 逻辑 tick 频率
sub_steps = 3    # 每 tick 的物理子步数

[map]
hub_count = 7    # 据点数；0 是"关地图"哨兵，测试用空白世界

[monster]
hp = 50          # 怪物血量
attack_damage = 10
```

Boss 参数在样例配置里没有写出来，走的是代码内置默认值（`internal/config/config.go` 的 `Default()`）。想改就在配置里自己加一段——加载器会接受它：

```toml
[boss]
hp = 800              # 0 表示本关没有 Boss（哨兵值，合法）
speed = 2.0
attack_damage = 20
phase2_at_pct = 0.5   # 血量降到 50% 进二阶段
fire_vuln_mul = 1.5   # 火元素易伤倍率，必须 ≥1
summon_per_phase2 = 2 # 进二阶段时召唤的小怪数
```

加载器**拒绝未知键**——拼错的键会让默认值静默生效，那类问题极难查，所以宁可启动时报错。

---

## 关于文档

设计文档、决策记录、里程碑报告都在**开发者本机**，没有进这个仓库（`.gitignore` 排除了 `docs/` 与 `*.md`）。

所以这个仓库里只有代码——但关键的设计理由都写在代码注释里，尤其是那些"为什么不这么做"的注释。想理解某处为什么这样写，先读它上面的注释。

---

## 已知限制

- 手感是纸面数值，**从未经真人试玩校准**。
- 只有一张地图、一关、一个 Boss。
- 技能池 12 个（4 个被动 + 8 个主动）。被动只能折成 4 个倍数，没有事件触发类效果（反伤 / 击杀回血这类）。
- 玩家与怪物之间没有碰撞体积，可以互相穿过。
- 掉线即离线，没有重连。
- 单人后端、无水平扩展、无持久化世界状态。
