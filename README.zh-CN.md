# libcommand

[English](README.md) | 简体中文

`libcommand` 是一个面向 Go 的进程内 Bash 路径模拟器。它能够解析 Bash、
建模受支持的 Shell 状态、在值无法确定时探索可达结果，并将展开后的命令分发给
调用方提供的 Handler。

模拟器不会启动宿主机进程，也不会读写宿主机文件系统。它适用于命令发现、策略
评估、测试和执行过程可视化等不应直接运行原始脚本的场景。

> [!IMPORTANT]
> `libcommand` 实现的是实用的 Bash 子集，而不是完整的 Bash，也不是操作系统
> 沙箱。注册的 Handler 属于应用代码，仍然可以产生真实的外部副作用。

## 核心能力

| 能力 | 说明 |
| --- | --- |
| 隔离执行 | Shell 变量、流、函数、选项和文件均保存在当前模拟的独立状态中。 |
| 明确的命令边界 | 通过精确命令名和 `"*"` fallback 将展开后的调用映射到 Go Handler；任何命令都不会回退到宿主机执行。 |
| 保守的不确定性建模 | 未知参数和进程状态以类型化 unresolved 数据继续传播，不会被伪造成具体字符串。 |
| 可达路径探索 | 当控制流依赖未知状态时，在共享执行步数预算内探索代表性的成功与失败路径。 |
| 嵌套 Shell 执行 | `eval`、`source`、`bash -c`、`sh` 和自定义 Handler 共用同一套解析器与执行器。 |
| 有界物化 | 每次模拟的执行步数和逻辑保留状态均支持配置上限。 |
| 可选 Trace | 可输出运行事件、路径分叉、逻辑内存和状态快照，供调试器与可视化工具使用。 |
| 配置可复用 | 构建后的 `Simulator` 不可变，可并发处理彼此隔离的模拟请求。 |

## 环境要求与安装

- Go 1.26 或更高版本
- `mvdan.cc/sh/v3` 由 Go Modules 解析

项目当前在 `dev` 分支开发。首个正式版本发布前，请显式安装该分支：

```bash
go get github.com/nullptrpanic/libcommand@dev
```

生产构建应固定到具体 Tag 或 Commit，以保证依赖可复现。

## 快速开始

注册应用需要观测或实现的外部命令，构建不可变模拟器，然后提交 Bash 源码：

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/nullptrpanic/libcommand"
)

func main() {
	simulator := libcommand.NewBuilder().
		Limits(&libcommand.Limits{
			MaxExecutionSteps: 50_000,
			MaxMemoryBytes:    4 << 20,
		}).
		Command("lark-cli", func(
			_ context.Context,
			shell *libcommand.CommandContext,
			invocation *libcommand.Invocation,
		) (*libcommand.CommandResult, error) {
			fmt.Printf("%s %#v\n", invocation.Name, invocation.Args)
			if err := shell.State().SetVariable("LAST_COMMAND", invocation.Name); err != nil {
				return nil, err
			}
			return &libcommand.CommandResult{
				Stdout:   []byte("accepted\n"),
				ExitCode: 0,
			}, nil
		}).
		Build()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := simulator.Simulate(ctx, &libcommand.SimulationRequest{
		Source: `lark-cli im messages-send --text "$MESSAGE"`,
		Env:    map[string]string{"MESSAGE": "hello"},
		Args:   []string{"first-argument"},
		Stdin:  []byte("input\n"),
	})
	if err != nil {
		panic(err)
	}
}
```

`SimulationRequest.Stdin` 是有限的具体输入流。nil 或空切片都表示立即 EOF。
脚本中暴露的 `$0` 固定为 `command.sh`。

## 架构

### 整体结构

公共包负责配置和 API 契约；`internal/runtime` 负责 Shell 语法与执行机制；
`internal/builtin` 负责所有 call-like 命令的具体语义。Builtin 与调用方命令
因此能够共用同一条分发链路，同时不向外暴露运行时路径构造。

```mermaid
flowchart LR
    subgraph Host["宿主应用"]
        Builder["Builder + Limits"]
        Handlers["注册的 Go 命令"]
        Request["SimulationRequest"]
        Observer["可选 TraceObserver"]
    end

    subgraph Public["github.com/nullptrpanic/libcommand"]
        Simulator["不可变 Simulator"]
        API["Invocation · CommandContext · State"]
    end

    subgraph Engine["模拟执行引擎"]
        Parser["mvdan/sh 解析器"]
        Index["候选命令索引"]
        Runtime["Runtime 执行器"]
        Registry["Builtin 注册表"]
        Paths["多路径执行状态"]
        VFS["虚拟文件系统与流"]
        Budget["步数与内存预算"]
    end

    Builder --> Simulator
    Handlers --> Builder
    Request --> Simulator
    Simulator --> Parser
    Parser --> Index
    Parser --> Runtime
    Index --> Runtime
    Registry --> Runtime
    Runtime <--> Paths
    Paths <--> VFS
    Budget --> Runtime
    Runtime --> API
    API --> Handlers
    Runtime -.-> Observer
```

| 区域 | 职责 |
| --- | --- |
| 公共包 | Builder、Limits、模拟请求、Handler、类型化 Invocation、状态变更 API 和 Trace API。 |
| `internal/runtime` | 解析器集成、展开、赋值、函数、运算符、控制流、管道、重定向、虚拟 IO、路径分叉、取消和预算。 |
| `internal/builtin` | `echo`、`cd`、`env`、`eval`、`source`、`bash` 等确定性命令和 Wrapper 的实现。 |
| `internal/materialize` | 防溢出的逻辑字节统计和默认物化上限。 |
| `cmd/libcommand` | 输出模拟得到的 `lark-cli` 调用的示例 CLI。 |
| Playground | 用于 AST 与实时运行视图的浏览器 UI、Web Worker 和 WebAssembly Runtime。 |

### 单次模拟生命周期

每次 `Simulate` 调用都会创建新的执行上下文和独立状态。`Simulator` 中的命令表
和 Limits 可以复用；不同调用之间不会共享请求数据或路径状态。

```mermaid
sequenceDiagram
    participant Caller as 调用方
    participant Simulator
    participant Parser as 解析器
    participant Evaluator as 执行器
    participant Registry as 命令注册表
    participant Handler

    Caller->>Simulator: Simulate(ctx, request)
    Simulator->>Simulator: 校验请求物化大小
    Simulator->>Parser: 解析 Bash 源码
    Parser-->>Simulator: AST
    Simulator->>Evaluator: 使用全新状态执行 AST
    Evaluator->>Evaluator: 构建候选命令索引
    loop 可达语句与执行路径
        Evaluator->>Evaluator: 展开并应用 Shell 语义
        Evaluator->>Registry: 查找展开后的命令名
        alt 调用方命令
            Registry->>Handler: Command(ctx, commandContext, invocation)
            Handler-->>Evaluator: CommandResult 或 error
        else 内部 Builtin
            Registry-->>Evaluator: 确定性的模拟结果
        else Fallback
            Registry-->>Evaluator: Unresolved 结果
        end
        Evaluator->>Evaluator: 应用结果、分叉并检查预算
    end
    Evaluator-->>Caller: nil 或模拟错误
```

执行器会在执行单元之间检查 `context.Context`。Handler 同步运行，必须在
Context 被取消后及时返回；进程内 Handler 无法由模拟器强制中断。

## 路径探索与 unresolved 数据

默认 `"*"` 命令返回 unresolved 结果。未知 stdout、stderr 和退出状态会经过
受支持的赋值、命令替换、管道、重定向、虚拟文件和 Builtin 继续传播。只有当
控制流必须读取未知结果时，执行器才创建分支。

例如：

```bash
feature-gate && deploy blue
deploy common
```

```mermaid
flowchart TD
    Start["feature-gate<br/>未注册命令"]
    Decision{"退出状态"}
    Success["可能成功的路径"]
    Failure["可能失败的路径"]
    Blue["deploy blue"]
    CommonA["deploy common"]
    CommonB["deploy common"]
    EndA["路径 A 完成"]
    EndB["路径 B 完成"]

    Start --> Decision
    Decision -->|"0"| Success
    Decision -->|"非 0"| Failure
    Success --> Blue --> CommonA --> EndA
    Failure --> CommonB --> EndB
```

公共后缀会在每条保留路径上分别执行，因此同一个 Handler 可能被调用多次。
单次模拟内的回调顺序是确定的，但回调不会被自动去重，也不具备事务语义。

未知数据不会以伪造的具体值跨越应用命令边界：

- `ArgumentString` 保存具体参数；
- `ArgumentUnresolved` 表示整个参数的值无法确定；
- `Invocation.Unresolved.Env` 列出值无法确定的已导出变量名；
- `Invocation.Unresolved.Dir` 和 `Invocation.Unresolved.Stdin` 分别标记工作
  目录和输入流是否无法确定。

未知命令名、动态源码、重定向路径或数组索引无法被安全枚举；当语义必须使用
具体值时，模拟器会返回带源码位置的 unresolved semantics 错误。

## 命令分发与覆盖规则

`Build` 首先复制默认注册表，再覆盖调用方注册项；同名命令以最后一次注册为准。
精确查找始终先于 `"*"` fallback，任何查找结果都不会启动宿主机进程。

```mermaid
flowchart TD
    Expanded["已确定的展开后命令名"]
    Function{"直接调用是否命中<br/>Shell 函数？"}
    FunctionBody["执行函数体"]
    Control{"是否为执行器控制语义？<br/>break · continue · return · exit"}
    ControlRuntime["设置 Runtime 控制信号"]
    Exact{"是否存在精确定义？"}
    User["调用方注册命令"]
    Builtin["默认 Builtin 或 Wrapper"]
    Fallback["配置的 * fallback"]

    Expanded --> Function
    Function -->|"是"| FunctionBody
    Function -->|"否"| Control
    Control -->|"是"| ControlRuntime
    Control -->|"否"| Exact
    Exact -->|"调用方覆盖"| User
    Exact -->|"默认实现"| Builtin
    Exact -->|"未命中"| Fallback
```

`command` 和 `builtin` Wrapper 会绕过 Shell 函数，并通过同一份精确定义表进行
分发。调用方可以覆盖任意默认 call 命令，包括 `echo`、`cd`、`eval`、
`source`、`exec`、`command` 和 `builtin`。四个由执行器管理的控制转移不能
被 Builder 注册项替换。

默认注册表包含常用 Shell Builtin 和确定性的进程内辅助命令：

| 分类 | 命令 |
| --- | --- |
| 基础命令 | `:`, `true`, `false`, `echo`, `printf`, `pwd`, `cd`, `wait` |
| 输入与变量 | `read`, `mapfile`, `readarray`, `set`, `shift`, `unset`, `getopts`, `shopt` |
| 声明 | `declare`, `local`, `export`, `readonly`, `typeset`, `let` |
| 条件与 Trap | `test`, `[`, `trap`, `type` |
| 分发与动态执行 | `command`, `builtin`, `env`, `exec`, `eval`, `source`, `.`, `bash`, `sh` |
| 模拟工具 | 整数 `seq`、基于 stdin 的 `base64` 和逐行 `rev` |

## 嵌套与动态 Shell 执行

Builtin 和调用方 Handler 通过 `CommandContext` 返回声明式操作。回调本身不会
递归驱动执行器，也不构造路径结果。回调返回后，Runtime 再通过常规解析、分发、
路径分叉和预算逻辑应用该操作。

```mermaid
flowchart LR
    Call["Builtin 或调用方命令"]
    Context["CommandContext"]
    Result["声明式 CommandResult"]
    Runtime["Runtime 应用操作"]

    subgraph Operations["嵌套操作"]
        Invoke["Invoke / InvokeWithEnvironment"]
        Evaluate["Evaluate"]
        Source["Source"]
        Child["RunShell"]
        Replace["Replace"]
    end

    Current["当前 Shell 状态"]
    SourceScope["当前状态 + Source 作用域"]
    ChildState["隔离的子 Shell 状态"]
    Dispatcher["统一命令分发器"]

    Call --> Context --> Result --> Runtime
    Runtime --> Invoke --> Dispatcher
    Runtime --> Evaluate --> Current
    Runtime --> Source --> SourceScope
    Runtime --> Child --> ChildState
    Runtime --> Replace --> Dispatcher
    Current --> Runtime
    SourceScope --> Runtime
    ChildState --> Runtime
```

不同操作具有明确且不同的状态边界：

| 操作 | 执行状态 | 可观察结果 |
| --- | --- | --- |
| `Evaluate` / `eval` | 当前 Shell 状态 | 变量和文件系统变更保留在各结果路径上；动态 AST 节点会加入候选发现和 Trace。 |
| `Source` / `source` / `.` | 当前 Shell 状态，加 Source 深度和可选的位置参数作用域 | Source 内的变更会保留；临时位置参数会恢复；`return` 结束被 Source 的内容；仅从虚拟文件读取。 |
| `RunShell` / `bash` / `sh` | 根据已导出变量、目录、stdin、请求选项和虚拟文件系统副本创建的新子 Shell | 子 Shell 局部变量不会泄漏；输出、状态、已消费输入、Issue 和最终虚拟文件系统会合并回父路径。 |
| `Invoke` | 当前状态，不查找 Shell 函数 | 选中的精确命令通过标准 Invocation 契约执行。 |
| `Replace` / `exec` | 当前状态 | 替换成功后终止当前执行路径。 |

自定义命令可以执行生成的 Bash，而不需要接触内部路径类型：

```go
builder.Command("evaluate", func(
	_ context.Context,
	shell *libcommand.CommandContext,
	_ *libcommand.Invocation,
) (*libcommand.CommandResult, error) {
	return shell.Evaluate(`record generated`, "generated", 1), nil
})
```

## State 与命令契约

每个命令都会收到只读 `Invocation` 和仅在当前回调期间有效的
`CommandContext`。`State()` 提供受支持的状态变更能力，但不会暴露 Runtime
的路径构造：

- `Directory` 和 `ChangeDirectory`；
- `Variable`、`SetVariable` 和 `UnsetVariable`；
- `CommandContext` 暴露的虚拟文件系统、输入、选项、查找、算术和嵌套执行操作。

状态变更只作用于当前路径，采用 Copy-on-Write，并受逻辑物化预算检查。命令返回
后不得继续持有 `CommandContext` 或 `State` 指针。

返回值语义是明确的：

| 返回值 | 含义 |
| --- | --- |
| 非 nil Result，nil Error | 应用 stdout、stderr、退出码、Action 或声明式操作。 |
| `&CommandResult{Unresolved: true}`，nil Error | 输出流和退出状态均无法确定。 |
| nil Result，nil Error | 放弃处理本次调用，使用 unresolved-command 行为。 |
| 非 nil Error | 中止模拟并透传错误。 |
| `CommandStop` | 成功终止全部活动和待执行路径。 |

Handler Panic 会被转换为模拟错误。回调返回后，Runtime 会检查 stdout 和 stderr
是否超过预算；已经发生的外部副作用无法回滚。

## 资源模型

每次模拟默认包含两个独立上限：

| Limit | 默认值 | 作用范围 |
| --- | ---: | --- |
| `MaxExecutionSteps` | 10,000 | 动态语句、命令调用，以及路径探索产生的额外后继。 |
| `MaxMemoryBytes` | 2 MiB | 初始请求和模拟器保留的保守逻辑物化数据。 |

```mermaid
flowchart TB
    Limit["MaxMemoryBytes"]
    Request["Source · Env · Args · Stdin"]
    Syntax["初始与动态 AST<br/>候选命令索引"]
    States["全部保留路径状态"]
    Data["变量 · 流 · 作用域 · Trap<br/>命令替换 · 集合"]
    Files["虚拟路径 · 目录 · 文件内容<br/>确定性元数据"]
    Calls["Invocation 快照<br/>命令 stdout 与 stderr"]

    Limit --> Request
    Limit --> Syntax
    Limit --> States
    States --> Data
    States --> Files
    Limit --> Calls
```

该上限是保守的逻辑数据上限，而不是 Go Heap 或进程 RSS 的精确限制。解析器内部
分配、Go Runtime 开销，以及 Handler 返回前自行产生的分配，无法在同一进程中
被硬限制。处理不可信 Bash 时，应将这些预算和 Context Deadline 与适合部署
环境的进程级隔离组合使用。

## Trace 与 Playground

Trace 默认不启用。`Simulate` 不构建 Trace 索引、不捕获状态快照，也不发送事件。
`SimulateTrace` 同步输出轻量事件；`SimulateTraceWithOptions` 还可以捕获受限的
状态和命令直接输出快照。

```mermaid
flowchart LR
    Request["模拟请求"]
    Mode{"API"}
    Normal["Simulate"]
    Trace["SimulateTrace"]
    Snapshot["SimulateTraceWithOptions"]
    Eval["同一套 Shell 执行器"]
    NoEvents["无 Observer 事件和快照"]
    Events["AST · 语句 · 命令<br/>分叉 · 路径 · 逻辑内存"]
    Context["可选且有界的状态快照"]

    Request --> Mode
    Mode --> Normal --> Eval --> NoEvents
    Mode --> Trace --> Eval --> Events
    Mode --> Snapshot --> Eval --> Events --> Context
```

Observer 在模拟 Goroutine 上执行，因此缓慢或阻塞的 Observer 会增加 Trace 模式
的延迟。Observer 返回 `false` 会停止后续事件，但不会终止 Shell 执行。
`TraceNode.Embedded` 用来标记条件命令、命令替换、管道操作数等父语句内部的求值
细节。它只是一项展示元数据：节点 ID、执行事件和 Shell 行为均不改变，因此 AST
视图可以折叠它，执行视图仍可完整保留。`TraceNode.FlowGroup` 区分同一父节点下
顺序执行的语句体和互斥语句体，供分层布局使用。`TraceNode.FlowCanSkip` 标记无需
进入任何可见语句体即可继续的条件容器，例如没有 `else` 的 `if`。二者均为展示
元数据，不具备执行语义。

浏览器 Playground 将模拟器编译为 WebAssembly，并在一次性 Web Worker 中运行。
它提供独立的 AST 和实时 Runtime 视图、命令注册、路径可视化、执行控制和逻辑
内存快照。

### Playground 动态预览

**实时 Runtime 调用流。** 展开后的命令按照实际执行顺序逐个出现；当前节点、
画布位置、逻辑内存和节点检查器会随模拟进度同步更新。

![Playground 实时构建 Runtime 命令流并查看命令输出](assets/playground/runtime-flow.gif)

**AST 实时执行。** 动图从 AST 视角开始并点击 **Run simulation**。解析得到的拓扑
保持不变，已到达节点按执行顺序逐个高亮，未到达语法始终保留为虚线。父语句
内部的求值细节会被折叠，真实的分支体和循环体仍然展示；`eval` 动态解析出的语法
（包括最终的 `lark-cli` 调用）会在发现时挂到同一棵 AST 上。

![Playground 在稳定 AST 上依次高亮 Base64 解码、eval 和动态解析出的 lark-cli 调用](assets/playground/ast-flow.gif)

```bash
make playground
./target/playground/playground -addr 127.0.0.1:8080
```

打开 `http://127.0.0.1:8080`。UI 模型、JavaScript Handler、部署边界和图例请
参见 [playground/README.md](playground/README.md)。

## 支持的 Bash 子集

当前子集聚焦常见的编排脚本：

- 标量变量、索引和关联数组、导出变量、位置参数、引用，以及受支持的参数展开、
  算术展开、Brace Expansion 和 Glob；
- 顺序语句、`if`、`case`、`&&`、`||`、取反、Word/Arithmetic `for`、
  `while` 和 `until`；
- 函数、Subshell、命令替换、管道、后台命令，以及不带 Job Operand 的 `wait`；
- 虚拟文件、Here Document、Here String、常用重定向和 Process Substitution；
- `test`、`[`、受支持的 `[[` 表达式、声明、选项、Trap、输入 Builtin、循环控制
  和函数返回；
- `command`、`builtin`、`env`、`exec`、`eval`、基于虚拟文件的 `source`，
  以及常见的 `bash -c` 或 `sh` 形式。

支持范围取决于具体行为，而不只是命令名。未支持的选项和语义会返回错误或
unresolved 结果，不会静默调用宿主机实现。

## 已知限制

- 不保证任意 Bash 兼容性；少见 Builtin、选项、Coprocess、Job Operand、
  File Descriptor 和重定向形式可能不受支持。
- 嵌入 Python 或其他非 Bash 语言中的命令对 Bash 解析器不可见。
- 命令 stdout 和 stderr 是聚合流，无法重建字节级交错顺序和独立 FD Offset。
- 虚拟文件系统从空的 `/` 开始，永远不会读取宿主机文件系统。
- 与宿主机身份、进程和随机数相关的值会被标为 unresolved；必须使用具体值时
  则返回错误。
- 未知字符串、字段数量和循环长度不会被穷举；执行器使用类型化未知值和代表性路径。
- 对抗性输入场景下，进程内执行不能替代 Container、OS Sandbox 或外部资源限制器。

## 项目结构

```text
.
├── api.go, builder.go, simulator.go   公共 Go API
├── internal/runtime/                 Shell 执行与路径探索
├── internal/builtin/                 统一命令注册表与 Builtin
├── internal/materialize/             逻辑内存统计
├── cmd/libcommand/                   命令发现示例 CLI
├── cmd/playground-wasm/              WebAssembly Bridge 与 Trace Adapter
├── cmd/playground/                   自包含 Playground Server
├── playground/                       浏览器应用
├── examples/simple/                  嵌入脚本的 Go 示例
└── testdata/fuzz/                     持久化 Fuzz Corpus
```

## 构建与验证

将全部可交付命令构建到 `target/`：

```bash
make all
```

| 产物 | 路径 |
| --- | --- |
| 示例 CLI | `target/libcommand/libcommand` |
| 自包含 Playground | `target/playground/playground` |

使用 `make clean` 删除构建产物。

使用当前环境选择且满足 `go.mod` 的 Go Toolchain 执行仓库门禁：

```bash
./verify.sh
```

门禁包括格式检查、测试、`go vet`、Race Detection 和全仓覆盖率检查。单独运行
一个 Fuzz Target 的示例：

```bash
go test -run '^$' -fuzz '^FuzzSimulatorSourceStability$' -fuzztime=10s .
```

## 反馈

请通过 [GitHub Issues](https://github.com/nullptrpanic/libcommand/issues) 提交 Bug
和边界清晰的功能需求。报告应包含能够复现问题的最小 Bash 样本、预期的可达命令
调用，以及使用的 Limits 配置。
