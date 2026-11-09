# AGENTS.md

本文件给在本仓库工作的 AI 助手看。它记录**这个项目的文档标准**与**工程约定**。

---

## 1. 文档标准（强制）

写任何里程碑报告、缺陷记录、复盘时，**必须**按下面的结构。少任何一节都算没写完。

### 1.1 一条缺陷必须写全五件事

| 项 | 要求 | 反例（不合格） |
| --- | --- | --- |
| **整体思路** | 这段代码原本要解决的问题是什么、当时面对的设计约束是什么 | ❌ "修了 ack 的 bug" |
| **遇到的问题** | 具体现象，要能被人复现或看见。报错原文、错误数值、错误日志一行不落 | ❌ "ack 有 bug，已修复" |
| **为什么会出问题** | **根因**，不是"哪里写错了"，而是"为什么会写出这个错"。这一项最重要，写不出根因就说明还没查清 | ❌ "手误"/"疏忽"/"没注意" |
| **前后代码** | 修复前与修复后的确切代码，标注文件:行号；如果代码长，只贴关键几行并指出哪一行是分水岭 | ❌ "改了实现" |
| **怎么解决的** | 修法 + **为什么是这个修法**（对比被否掉的其他修法） | ❌ "改成正确的写法" |

### 1.2 「为什么会出问题」怎么写

不要写"粗心""疏忽""漏了"。那些不是原因，是还没查到原因时的填充词。真正的原因通常是下面几类之一，写清楚是哪一类：

- **分别设计接口的两半**：发送侧和接收侧各自自洽，但从没验证过互操作。→ 根因是没先写下字段定义再两头实现。
- **用值推断状态**：把零值同时当成"合法数据"和"未设置"。→ 根因是省一个布尔值。
- **优化凌驾语义**：为了"只重传丢的那片"这类优化，违背了底层通道"有序字节流"的语义。
- **语言机制**：Go 切片是引用、map 遍历随机、浮点转整数越界未定义。
- **生命周期没定义**：谁负责关闭、能关几次、关闭期间还接不接新对象。
- **单元测试绕过了真实路径**：用手工构造的输入测，正好避开了真正会出问题的那条路径。

判断标准：**如果换个聪明人按同样的顺序写，他也会踩同样的坑**，那才叫根因。如果换个人就不会踩，那说明是个人疏忽，但要解释清楚"为什么当时必然疏忽"。

### 1.3 里程碑报告的标准结构

```markdown
## 1. 一句话结论
## 2. 整体思路        ← 这个里程碑要解决什么、为什么这么切分
## 3. 验收结果        ← 命令 + 实测数字
## 4. 交付的包与依赖方向
## 5. 代码量
## 6. 缺陷记录        ← 每个按 1.1 的五件事写
## 7. 设计取舍        ← 为什么这么选，被否掉的方案及原因
## 8. 已知限制
## 9. 剩余待办
## 10. 复现命令
## 11. 环境
```

### 1.4 数字必须实测

文档里出现的每个数字——行数、耗时、带宽、丢包率、测试例数——都必须来自当次运行的命令输出。禁止估算、禁止「大概」、禁止从上一版文档抄。

行数用 `wc -l`，测试例数用 `grep -c '^func Test'`，性能用 `go test -bench`。写之前先跑一遍。

### 1.5 中文与术语

正文用中文。代码、标识符、文件路径、报错原文保持英文原样，不翻译。技术术语首次出现时给中文解释，后文可只用英文。

---

## 2. 工程约定

### 2.1 提交

遵循 git-rule 技能：

- 格式 `<类型>[可选 范围]: <描述>`，类型限 `feat/fix/ui/docs/style/refactor/perf/test/build/ci/chore`
- 一个提交一个逻辑单元，小步快走
- 提交前必须：`gofmt -l internal cmd` 空、`go vet ./...` 通过、`go test ./...` 通过
- commit message 正文要写**为什么这么改**，不要只写"改了 X"。如果修了缺陷，把根因写进 commit message

### 2.2 每次完成后

1. 按 git-rule 提交
2. 写里程碑/缺陷记录 md 到 `docs/`
3. `docs/` 与 `*.md` 被 `.gitignore` 忽略，**文档不进 git 历史**（按用户要求）。例外：`README.md`、`AGENTS.md`

### 2.3 验证命令

```bash
gofmt -l internal cmd                          # 必须无输出
go vet ./...                                   # 必须无告警
go test ./...                                  # 必须全绿
CGO_ENABLED=1 go test -race -count=1 ./...     # 必须全绿
```

`-race` 需要 cgo。本机 gcc 在 `G:\mingw64\bin\gcc.exe`，已用 `go env -w CC=...` 配好。
**生产构建保持 `CGO_ENABLED=0`**，cgo 只用于测试路径。

注意 `-race` 只能证明"跑过的路径上没撞上"，不能证明没有竞争。同一个二进制里有竞争的函数只要没被调用，`-race` 就是零报告、退出码 0。所以并发正确性要靠**结构**保证（谁独占什么数据），不能只靠工具。

### 2.4 分层纪律（硬约束）

```
cmd/{server,bot} → internal/{config,obs,room,netx,proto,snapshot,game}
internal/room    → internal/{game/world,game/systems,snapshot,proto,config}
internal/snapshot→ internal/{game/world,proto}
internal/netx    → 标准库
internal/game/** → 不 import 任何网络/房间包
```

- `internal/netx` **不 import** `internal/room`
- `internal/game` **不 import** `internal/netx`
- `cmd/server/bridge.go` 是**唯一**同时 import netx 与 room 的地方

违反分层时用 `go list -deps` 检查。

### 2.5 并发模型（硬约束，见 docs/server-design-v0.1.md §17）

1. `*world.World` 的唯一持有者与访问者是 `Room.Run` 那个 goroutine
2. 上行：网络 goroutine 把解码后的输入投入 channel，tick 循环**非阻塞**读取（`select { case <-ch: default: }`），绝不阻塞 tick
3. 下行：tick 循环把快照字节投入每个会话的有界 channel，满了丢最旧
4. 会话可变状态由网络 goroutine 独占，tick 循环只读不可变副本

### 2.6 Go 里踩过的坑（写新代码前先看）

- **浮点转整数越界是平台相关的未定义值**，不是截断。血量会变反号、坐标会瞬移。所有 `int16(f)/int32(f)` 前必须钳位。
- **切片赋值是引用**。把 `[]byte` 存进结构体前要 `copy`，尤其来源是网络读缓冲这种复用内存。
- **map 遍历顺序随机**，禁止在模拟路径里遍历 map（会让回放不可复现）。
- **`wg.Add` 与 `wg.Wait` 并发是未定义行为**。关闭路径要 `sync.Once` 保证只走一次。
- **不要 `close(发送队列)` 来收尾**，因为可能有多个 goroutine 在往它写，会 panic。用独立的 `done` channel + `sync.Once`。
- **`t.Fatalf("%+v", protoMsg)` 会复制含 `sync.Mutex` 的结构体**，vet 会报 `copies lock value`。传指针或只取字段。

### 2.7 测试写法

- 时间相关的测试用假时钟（`fakeClock`），不要 `time.Sleep` 等真实时间
- 轮询等待用 `waitFor(t, timeout, what, cond)`，不要固定 sleep
- 网络测试用真实 UDP 回环（`127.0.0.1:0` 随机端口），不要 mock 掉 UDP
- 丢包/抖动测试用真中继（见 `internal/netx/loss_test.go`），不要打桩——桩测不出乱序
- 断言错误信息要带**期望值与实际值**，例如「收到 %d 条，期望 %d」

### 2.8 环境

| 项 | 值 |
| --- | --- |
| Go | go1.27.3 windows/amd64 |
| protoc | libprotoc 3.20.3 |
| protoc-gen-go | v1.36.12（在 `G:\GoPath\bin`） |
| gcc（仅 -race） | `G:\mingw64\bin\gcc.exe` |
| 直接依赖 | `BurntSushi/toml`、`google.golang.org/protobuf` |

生成 protobuf 代码：

```bash
PATH="$PATH:$(go env GOPATH)/bin" protoc --go_out=. --go_opt=module=koukoukongjian internal/proto/game.proto
```

**protobuf 没有 uint8/uint16**，只有 32/64 位整数。小整数一律用 `uint32` 并把范围约束写进注释。
