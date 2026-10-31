# 第 12 章 Eino 入门：核心抽象与底层原理

> 上一章：[第 11 章 for 循环的瓶颈](11-for-循环的瓶颈-为什么需要图编排.md) ｜ 下一章：[第 13 章 Agent 图全景](13-platepilot-的-agent-图全景.md)
>
> 本章认识执行那台状态机的框架——Eino。涉及的仓库真实文件集中在文末
> [§12.7 参考代码与目录](#127-参考代码与目录)。项目当前使用的版本是
> `github.com/cloudwego/eino v0.9.21`（见 `chat-service/go.mod`）。

---

## 12.1 坏 case 开场：手写引擎的接线错误

上一章我们在文档里手写了状态机引擎：一个节点表、一个 `route` 函数、一个
「查路由 → 执行节点」的驱动循环。设想这个引擎由团队里不同的人陆续添加节点，
几周之后 code review 时你会看到这样的合并请求：

- `route` 里新写了一个返回值 `"answr"`（拼错了），编译完全通过——它只是个
  字符串。这条边只有在某个澄清场景真的发生时才会被走到，**第三次线上对话**
  才 panic：节点不存在。
- 新人把 answer 节点的输出类型改成了一个新结构体，而 finalize 还在读旧字段。
  因为节点表是 `map[string]Node`、状态用一个大 `any` 兜着，编译器什么都没说。
- plan 节点新增了一条「直接到 END」的边，但忘记了 finalize——于是这一类 turn
  不再写会话历史，也不再记审计。**边没闭合，测试恰好没覆盖到。**
- tools 节点有时吐整块结果、有时吐 token 流，驱动循环里于是长出两套处理逻辑，
- 再叠上并发扇出和流的取消，已经没人敢动那个循环。

这四类问题——**节点名拼错、类型接错、边没闭合、流/非流混杂**——有一个共同点：
它们都是「接线错误」，与业务逻辑无关，而且全都在运行时才暴露。

Eino 这类编排框架存在的首要理由，就是把接线错误从**运行时**提前到**启动时
（甚至编译时）**。本章认识它的五个核心抽象：Chain / Graph、Lambda、Branch、
StateGraph、Compile，以及两个组件接口 ChatModel 和 Tool；最后讲流式与切面的
核心思想。不推导源码，只讲「它是什么、不这样会怎样」。

---

## 12.2 概念与原理

### 12.2.1 两层世界观：Components 是原子能力，编排把它们串起来

Eino 把一个大模型应用切成清晰的两层：

- **Components（组件）**：提供原子能力的最小单元。比如 `ChatModel`（对话）、
  `Embedding`（向量化）、`Retriever`（召回）、`Tool`（执行外部动作）。
  每个组件是一个 Go 接口。
- **编排（Chain / Graph）**：把组件（以及任意自定义函数）按业务逻辑组合、
  串联、分支、循环起来的网络。数据在网络里流动，每个节点对流过的数据都有
  格式要求。

官方文档对编排有一句很准的洞察：一张能让数据顺畅流动的网络，关键是
**上下游节点之间的数据格式是否对齐**。这句话后面会变成 Eino 的泛型签名
`Graph[I, O]`——整张图有明确的输入类型 `I` 和输出类型 `O`，接错线时
Go 编译器先报错。

还要记住一条项目铁律，它和这两层世界观直接相关：**组件是「第一公民」，
但框架不是。** PlatePilot 用 Eino 的编排层（Graph/Lambda/Branch），却把
自己的模型端口、工具注册表、领域 DTO 都放在框架之外，只用两个薄薄的适配器
接进去（12.6 节）。框架是执行器，不是系统的本体——这是上一章「先拥有自己的
状态机」的延续。

### 12.2.2 Chain 与 Graph：直线与网络

**Graph（图）** 是底层且语义完备的模型：节点（node）+ 边（edge）。用两个动作
构建：`AddXxxNode(name, node)` 放一个节点，`AddEdge(from, to)` 连一条边。
图里有两个虚拟节点：`START`（入口）和 `END`（出口）。Graph 能表达分支、并行、
循环——上一章那张带 `plan ↔ tools` 环的图，只有 Graph 能表达。

**Chain（链）** 是 Graph 的简化封装，面向「按顺序串联」这一个最常见场景：
`chain.AppendChatTemplate(...).AppendChatModel(...).AppendChatModel(...)`。
官方的定位是：现实中大多数业务只需要直线串联，点点边边的 Graph 接口
「强大但略显复杂」，所以 Chain 暴露了除**环**之外几乎全部 Graph 能力。

| | Chain | Graph |
|---|---|---|
| 形状 | 直线，`Append` 即连边 | 任意网络，显式 AddNode + AddEdge |
| 分支 / 并行 / 循环 | 分支有，**环没有** | 都有 |
| 心智成本 | 低 | 中 |
| PlatePilot 用不用 | 不用 | **用**（有界 ReAct 环必须是图） |

判断依据很简单：流程里有没有「回到前面某个节点」的边。有，就只能上 Graph。

### 12.2.3 Lambda：任意函数即节点

预置组件节点（`AddChatModelNode`、`AddToolsNode`）之外，业务逻辑怎么上图？
答案是 **Lambda**：把一个普通 Go 函数包装成节点。Eino 按数据形态提供三种：

- `InvokableLambda`：`func(ctx, I) (O, error)`——**整块进、整块出**。
- `StreamableLambda`：`func(ctx, I) (StreamReader[O], error)`——整块进、**流出**。
- `TransformableLambda`：`func(ctx, StreamReader[I]) (StreamReader[O], error)`
  ——**流进、流出**。

PlatePilot 的六个业务节点全部是 `InvokableLambda`：ingress、plan、tools、
clarify、answer、finalize 都是「读整个状态、返回整个状态」的函数。即使 answer
内部要逐 token 发流式文本，也是通过事件通道（第 13、19 章）而不是让节点本身
变成流节点——节点的输入输出始终是整块的 `*TurnState`。这个选择让图上所有节点
签名统一，流的复杂性被关在一个地方。

### 12.2.4 Branch：条件路由是一个纯函数

普通边是静态的（A 之后必是 B）。**Branch（分支）** 是动态边：给它一个函数，
它只读当前数据、返回「下一个节点的名字」，本身不执行任何动作：

```go
compose.NewGraphBranch[*TurnState](
    func(_ context.Context, st *TurnState) (string, error) { ... },
    map[string]bool{nodeTools: true, nodeClarify: true, nodeAnswer: true},
)
g.AddBranch(nodePlan, branch)
```

注意第二个参数：**分支所有可能的目标集合必须显式声明**。这正是 12.1 那个
`"answr"` 拼错事故的解药——路由函数返回的名字若不在目标集合里，编译图时就
报错，你没机会把它带到线上。分支挂在某个节点上（`AddBranch(plan, ...)`），
含义是「plan 执行完，由这个函数决定去哪」。上一章 `route` 函数里
「澄清 → 工具 → 回答」的优先级，在这里就是分支函数体内 `if` 的书写顺序。

### 12.2.5 带状态的图：WithGenLocalState，以及项目为什么不用它

Eino 的 Graph 默认是「数据沿边流动」：上游输出即下游输入。需要一张图内共享的
「全局状态」时，官方机制叫 **Graph with state**：建图时传
`WithGenLocalState(gen)` 给每次运行生成一个状态对象，节点通过
`WithStatePreHandler/WithStatePostHandler` 在执行前后读写它，节点内部则用
`compose.ProcessState` 拿到状态。

**这是本章最需要讲清的一个「不用」。** PlatePilot 的图有共享状态（上一章的
`TurnState`），却没有用这套机制，而是把图声明为
`compose.NewGraph[TurnInput, *TurnResult]()`，让第一个节点 ingress 产出
`*TurnState`，之后每个 Lambda 都签名成 `func(ctx, *TurnState) (*TurnState, error)`
——**状态作为数据沿普通边显式流过每个节点**。

理由上一章已经埋好，这里对照框架机制说得更具体：

| 框架 local state | PlatePilot 的 TurnState |
|---|---|
| 生命周期 = 一次图运行，运行结束即消失 | 要在 clarify/confirm 停机点**写进 checkpoint**，跨请求恢复（第 18 章） |
| 通过 handler/ProcessState 隐式获取 | 是函数签名上的显式参数，节点读什么写什么一眼可见、可单独测试 |
| 由框架托管存储语义 | 由项目自己持有，能逐字段加注释、做版本号与并发冲突 |
| 适合一次运行内的 fan-out 汇总 | 业务状态需要持久化、回放、审计 |

一句话：**local state 解决「一次运行内节点间共享」，而这里的状态必须比一次
运行活得久。** 框架能力没有错，是问题域不同。

### 12.2.6 Compile：把声明式的图编译成可执行对象，图错误在启动时爆炸

构建期的图只是一份声明。`g.Compile(ctx)` 把它变成一个 `Runnable[I, O]`
——可执行对象，之后对它调 `Invoke`（整块执行）或 `Stream`（流式执行）。

Compile 是本章标题里「底层原理」最值钱的一步，它替你做接线检查：

1. **节点齐备**：每条边引用的节点都必须 Add 过；
2. **边闭合**：从 START 出发，所有路径都要能到达 END，不能有断头路
   （这正是 12.1「忘了接 finalize」的事故）；
3. **分支合法**：Branch 的目标集合里的节点都存在；
4. **类型对齐**：每个节点的输入类型 = 上游节点的输出类型；整张图的入口类型
   匹配 `I`、出口匹配 `O`。类型不对是 **Go 编译错误**；图结构不对是
   **Compile 返回 error**——两者都发生在服务启动建图时。

PlatePilot 在构造 Runner 的 `NewRunner` 里调用 `r.compile()`，并把编译失败
作为构造错误直接返回：**应用启动阶段图没装好，服务就起不来**，绝不会出现
「服务正常运行，第三次对话走到某条边才崩」。

> 顺带区分两个容易混的「编译」：Go 编译器在 `go build` 时保证**类型**对齐；
> Eino 的 Compile 在**进程启动**时保证**拓扑**闭合。前者管类型，后者管形状。

### 12.2.7 两个组件接口：ChatModel 与 Tool

节点之外，Eino 定义了一组组件接口。本章只需认识两个。

**ChatModel**：对话模型组件。核心方法：

- `Generate(ctx, []*schema.Message, opts...) (*schema.Message, error)`：
  整块请求、整块响应；
- `Stream(ctx, []*schema.Message, opts...) (*schema.StreamReader[*schema.Message], error)`：
  整块请求、逐块响应。

支持工具调用的版本是 `ToolCallingChatModel`，多一个「绑定工具说明书」的能力
（`WithTools/BindTools`）。模型看到的工具说明是 `schema.ToolInfo`：名字、
描述、参数的 JSON Schema。

**Tool**：工具组件。最常用的是 `InvokableTool`，两个方法：

- `Info(ctx) (*schema.ToolInfo, error)`：报上自己的说明书（供模型选择）；
- `InvokableRun(ctx, argumentsInJSON string, opts...) (string, error)`：
  拿到模型给的 JSON 参数，执行，返回字符串结果。

注意第 2 章那句话在这里落地：**`InvokableRun` 由我们的代码调用，模型从不执行
任何东西。** 模型只产出「想调谁、参数是什么」，框架的 ToolsNode（或像本项目
那样自己的 tools 节点）负责真正调用 `InvokableRun`。

### 12.2.8 流式的核心思想：管道、三种节点形态、自动转换

流式是 Eino 主打的一块，理解三个概念即可：

1. **StreamReader / StreamWriter 与 `schema.Pipe`**：一个有类型的内存管道。
   生产者在一个 goroutine 里 `Send` 一块块数据，消费者 `Recv` 直到 EOF。
   模型的 `Stream` 返回的就是这样一个 reader。
2. **三种 Lambda 形态（12.2.3）** 本质是输入/输出「整块 vs 流」的 2×2 组合：
   整块→整块、整块→流、流→流（还有流→整块的收敛形态）。
3. **流的自动转换（stream auto-converting）**：当一个「只懂整块」的节点被接进
   一条流式链路时，框架自动在边界上把流**攒成整块**再喂给它；反之整块输出也
   能被包成一个只发一次的流。业务节点因此可以只实现自己最自然的形态，不用
   每个节点都手写流拼接。官方称之为让「流」从复杂性来源榜上除名。

本项目的实际用法很克制（第 13、19 章细讲）：模型适配器的 `Stream` 用
`schema.Pipe` 起一个 goroutine，把项目自有聊天端口的 chunk 逐个翻译成
`schema.Message` 发进图；图节点本身保持 Invokable（整块），面向用户的逐 token
推送走的是**事件通道**而不是让流穿过整张图。这样「业务节点统一整块」与
「用户看到流式」两件事互不污染。

### 12.2.9 切面思想：回调挂在节点边界，业务节点不写观测代码

一个生产系统要在每个节点前后做计时、span、日志。若写进每个节点，就是六种
写法的六个 span。Eino 的机制是 **callback handler**：实现 `OnStart / OnEnd /
OnError` 等钩子，挂在运行上下文上，框架在**节点边界**统一回调，业务函数对
观测无感知。这就是「切面」——横切关注点集中在一处。

**这里也要如实讲一个项目的「不用」。** 翻遍仓库，PlatePilot 没有使用 Eino 的
callbacks（没有任何 `callbacks.AppendGlobalHandlers` 之类的调用）。它的三级
审计之「node span」是用两个**显式包装器**实现的：`traced`（写审计 span）和
`instrumented`（发 phase 事件），在 `compile` 装配节点时把业务函数包一层：

```go
g.AddLambdaNode(nodePlan, compose.InvokableLambda(
    traced(r, nodePlan,
        instrumented(r, "plan", "正在制定下一步计划", r.plan),
        planDetail)))
```

为什么放着官方切面不用？三个具体理由，都能在实现注释里读到：

- **span 的形状是自定义的**：要带 run 身份（run_id 从 context 取）、要按每个
  节点定制 detail（候选数、轮次、是否澄清——只记计数，不记用户内容），还要求
  节点出错时落下 `failed`、panic 冒泡时闭合帧也照样发出，不留一条关不掉的
  「进行中」记录。包一层普通函数，这些都是普通代码，可离线断言。
- **事件与审计是两套切面**：`instrumented` 负责面向前端的 phase 帧（且可由
  配置整体关闭成透传），`traced` 负责面向存储的 span；二者叠加顺序需要显式
  控制。
- **统一包装本身就是纪律**：六个节点在 `compile` 里**无一例外**都包了
  `traced`，谁也没机会「忘记写」或「各写各的」；其中五个再包 `instrumented`
  发用户可见的进度帧，finalize 没有进度帧（它本身就是出口），这是唯一有意的
  差异。

> 这与不用 local state 是同一个工程姿态：**框架提供默认机制，项目只在默认机制
> 恰好贴合时使用它；横切逻辑一旦有强烈的自定义形状，就用最普通的函数包装，
> 把控制权留在自己手里。** Eino 的价值并不因为两个「不用」而减少——Compile 的
> 接线校验、Branch 的闭合检查、流式管道与转换，这些「框架独有、自己写容易错」
> 的东西才是引入它的原因。

---

## 12.3 承载思路的最小示例

一个最小的「Lambda + Branch + 有界环」图（教学骨架，形状与项目 `compile`
一致，但删掉了全部业务）：

```go
g := compose.NewGraph[Input, *Result]()

// 三个整块函数即节点：输入什么、输出什么，由泛型在 go build 时检查。
g.AddLambdaNode("plan", compose.InvokableLambda(planNode))
g.AddLambdaNode("tools", compose.InvokableLambda(toolsNode))
g.AddLambdaNode("answer", compose.InvokableLambda(answerNode))

g.AddEdge(compose.START, "plan")
g.AddEdge("tools", "plan")   // 回边：ReAct 环，Chain 表达不了，必须 Graph
g.AddEdge("answer", compose.END)

// 分支：纯函数决定去向；目标集合显式声明，拼错名字 Compile 即失败。
branch := compose.NewGraphBranch[*State](
    func(_ context.Context, st *State) (string, error) {
        if st.NeedClarify {
            return "clarify", nil
        }
        if st.PendingTools && st.Rounds < 5 {
            return "tools", nil
        }
        return "answer", nil
    },
    map[string]bool{"tools": true, "clarify": true, "answer": true})
g.AddBranch("plan", branch)

runnable, err := g.Compile(ctx)   // 拓扑检查在这里：节点缺失/边不闭合 → err
if err != nil { return err }      // 服务启动即失败，而不是第三次对话时

result, err := runnable.Invoke(ctx, input) // 真正跑一次 turn
```

对照上一章手写引擎的四个事故：节点名拼错被**目标集合 + Compile** 挡住；类型
接错被 **Go 泛型**挡住；边没闭合被 **Compile** 挡住；流的混杂由 **StreamReader
与自动转换**收敛。框架没有替你写任何业务，它替你消灭了一整类接线错误。

---

## 12.4 取舍与边界

**① 框架隔离要靠测试强制，而不是靠自觉。**
Eino 类型一旦在业务代码里随处可见，换框架或升级版本就是全仓库改动。PlatePilot
用一条 AST 架构测试把边界钉死（见 [architecture_test.go](../../chat-service/internal/agent/architecture_test.go)）：

- `github.com/cloudwego/eino*` 只准出现在 `chat-service/internal/agent/` 与
  `chat-service/internal/app`（仅装配模型实例）下，
  测试会 walk **整个仓库**（连 data-pipeline 将來依赖 Eino 也会被抓住）；
- `internal/app` 的豁免只覆盖模型装配：它构造 eino-ext 模型并在依赖上命名
  `model.BaseChatModel`——这是接线而非业务逻辑；图运行时（package agent）能用
  `compose` 和组件接口，节点之间直接传递 `schema.Message`。

违反时的正确反应是「代码放错了层」，而不是放宽测试——这条原则第 4 章立过。

**② 适配器要薄，且双向翻译。**
留下来的模型/工具桥接是 `toolreg`：`domain tool spec ↔ schema.ToolInfo`。模型侧
曾经也有一个对称的桥接包 `einomodel`（`schema.Message ↔ 项目消息 DTO` 双向翻译），
2026-10-31 被整个删掉——它是一层无损但易错的纯搬运；删掉之后节点直接把
`schema.Message` 交给 `Generate` / `Stream`。这段演化有个值得记住的教训：**为
「不让框架类型越界」而生的转换层，如果自己不承载任何判断，它本身就会变成新的
出错面**。边界要拦的是「框架类型爬进领域与存储」，不是「节点认识框架类型」。
薄到什么程度？留下的适配器里不应有业务决策——例如工具错误如何转成模型读得懂的
文本，是注册表的职责，不藏在适配器里。

**③ 框架能力用不用，按「贴合度」而不是「有没有」决定。**
本章出现两个克制的「不用」：不用框架托管状态（状态要跨请求存活）、不用
callback 切面（span 形状高度自定义）。反过来，Compile 校验、Branch 闭合、
`schema.Pipe` 流转换这些「自己写容易错」的能力则放心使用。引入框架不等于
接受它的全部默认。

**④ Chain 还是 Graph 不要提前优化。**
直线流程用 Chain 甚至普通函数即可；只有确实出现环、动态扇出时才上 Graph。
本项目因为 `tools → plan` 回边而必须用 Graph，这是需求逼出来的，不是选型偏好。

**⑤ Eino 是 Go 生态的选择，概念是通用的。**
节点/边/编译/流/切面这套词汇在 LangGraph 等框架里同构。学本章应拿到的是
「一张带类型检查的数据流图如何运行」，换框架时这部分知识直接迁移。

---

## 12.5 课后实验

**读**

- 打开 [runner.go](../../chat-service/internal/agent/runner.go) 的 `compile`，
  对照 §12.3 的最小示例，逐一找出：`NewGraph` 的两个泛型参数、六个
  `AddLambdaNode`、静态 `AddEdge`、唯一一个 `AddBranch` 及其目标集合、最后的
  `g.Compile`。
- 打开 [runner.go](../../chat-service/internal/agent/runner.go) 的 `NewRunner`：
  `deps.PlanModel` 如何在实现 `model.ToolCallingChatModel` 时被 `WithTools` 绑定
  注册表工具、绑不了或配置声明不支持时如何按「无工具」运行——体会「模型接口
  直接用，能力用配置断言」。
- 打开 [chatmodel.go](../../chat-service/internal/app/chatmodel.go) 的
  `retryTransport.RoundTrip`：哪些状态码可重试、流式请求为何豁免单次超时，
  体会「传输策略住在装配层」。
- 打开 [architecture_test.go](../../chat-service/internal/agent/architecture_test.go)，
  读懂它如何只解析 import（不编译、不依赖类型信息）就把两条边界测出来。

**跑（离线、零成本）**

```bash
cd chat-service
go test ./internal/agent/... -run TestEinoIsConfinedToTheAgentBoundary -count=1 -v
# 亲眼确认架构边界是一个会真正执行的测试，而不是文档里的口号
```

**改（故意制造一次 Compile 失败，不用提交）**

- 在 `compile` 里临时把分支目标集合中的一个名字改错（或删掉一条
  `nodeAnswer → finalize` 的边），运行 `go test ./internal/agent/ -run TestRunner -count=1`
  （或任意一个构造 Runner 的测试），观察错误是在**构造 Runner**时返回的。
  还原后再跑一遍确认全绿。
- 思想实验：如果把某个节点的入参从 `*TurnState` 改成别的类型，错误会在哪一层
  被抓到？（答：`go build`，比 Compile 更早。）

---

## 12.6 框架类型的边界：桥接包与装配包

在进入下一章的图全景之前，把「项目世界」和「Eino 世界」的交界固定下来。
这条边界经历过一次演化：M4 最初在图与模型之间放了一个 einomodel 适配层
（`schema.Message ↔ 项目消息 DTO` 的双向翻译），后来发现它是无损但易错的纯搬运，
于 2026-10-31 移除——现在节点直接说 `schema.Message`，项目自有类型守在更外层的
边界上（HTTP 契约、落库、工具说明、错误码）：

| 边界 | 项目自有（边界外） | Eino（只存在于 agent 与 app） |
|---|---|---|
| 消息（HTTP 契约与落库） | `domainchat.ChatMessage` / `conversation.Message` | 图内 transcript 直接是 `schema.Message` |
| 模型实例 | 装配期在 `internal/app/chatmodel.go` 构造 | `model.BaseChatModel` 的 Generate / Stream / WithTools |
| 模型错误 → 项目错误码 | `agent/modelerr.Map` | 框架与 HTTP 世界的错误类型 |
| 工具说明 | `toolreg.Registry`（项目工具与 JSON Schema） | `schema.ToolInfo` / `ParamsOneOf` |
| 工具执行 | `toolreg.Registry.Invoke` | `tool.InvokableTool.InvokableRun` |

- **toolreg 包**：`toolInfo` 把项目 JSON Schema 经 `eino-contrib/jsonschema`
  转成 `schema.ToolInfo`（`NewParamsOneOfByJSONSchema`）；`einoTool` 实现
  `InvokableRun`——它内部其实直接回调 `Registry.Invoke`，工具错误以 tool
  content 回给模型而不是让图崩掉。
- **app 的模型装配**：`chatmodel.go` 用 eino-ext 为三个岗位（规划 / 回答 / 抽取）
  各建一个模型实例，共享带重试与超时的传输层；这是 Eino 类型唯一被允许出现在
  `internal/agent` 之外的地方，架构测试为此开了「仅装配」的口子（§12.4①）。
- **模型失败的翻译**：节点拿到的错误先过 `agent/modelerr.Map`，框架与 HTTP
  世界的错误词汇在这里换成项目错误码（provider_timeout / provider_unavailable）。

一个细节值得注意：图运行时**并没有用 Eino 的 ToolsNode 自动执行工具**。
`einoTool` 适配器存在并被独立测试，但真实图里 tools 节点是项目自己的
`runTools`——因为它需要调用 ID、只读并发、写操作截停、seq 保序这些框架通用
ToolsNode 给不了的调度纪律（第 14 章）。**框架给了默认执行器，项目在有更强
纪律需求时自己当执行器**，这与状态、切面的取舍一脉相承。

---

## 12.7 参考代码与目录

**Eino 版本**

- [chat-service/go.mod](../../chat-service/go.mod)：`github.com/cloudwego/eino v0.9.21`、
  `github.com/eino-contrib/jsonschema v1.0.3`（Go 类型 → 工具 JSON Schema）。
- 官方文档（外部）：Eino 官方文档「Chain & Graph & Workflow 编排功能」与
  「Chain/Graph 编排介绍」——组件/编排解耦、`NewGraph`、`InvokableLambda`、
  `NewGraphBranch`、`WithGenLocalState`、`schema.Pipe` 的权威说明。

**图的声明与编译（本章核心）**

- [chat-service/internal/agent/runner.go](../../chat-service/internal/agent/runner.go)：
  `compile`（`compose.NewGraph[TurnInput, *TurnResult]`、六个
  `AddLambdaNode(..., compose.InvokableLambda(...))`、`AddEdge`、
  `NewGraphBranch[*TurnState]` 与目标集合、`g.Compile`）；`NewRunner` 中
  编译失败即构造失败。

**模型与工具的边界（桥接 + 装配）**

- [chat-service/internal/app/chatmodel.go](../../chat-service/internal/app/chatmodel.go)：
  `buildChatModels`（eino-ext 三实例 + 共享 `retryTransport`）、
  `extractionResponseFormat`（strict json_schema / json_object 两档）、
  `retryTransport.RoundTrip`（可重试状态码、`Retry-After`、流式豁免超时）——
  Eino 类型在 `internal/agent` 之外唯一被允许出现的地方。
- [chat-service/internal/agent/modelerr/modelerr.go](../../chat-service/internal/agent/modelerr/modelerr.go)：
  `Map` 把模型失败翻译成 provider_timeout / provider_unavailable，
  已带项目错误码的错误与调用方取消原样透传。
- [chat-service/internal/agent/toolreg/einotool.go](../../chat-service/internal/agent/toolreg/einotool.go)：
  `EinoTools` / `ToolInfos`、`toolInfo`（`NewParamsOneOfByJSONSchema`）、
  `einoTool.Info` / `einoTool.InvokableRun`（回调 `Registry.Invoke`）。

**架构边界（AST 强制）**

- [chat-service/internal/agent/architecture_test.go](../../chat-service/internal/agent/architecture_test.go)：
  `TestEinoIsConfinedToTheAgentBoundary`——walk 整个仓库；eino 仅限
  `internal/agent/` 与 `internal/app`（仅装配模型实例）。
- 对照第 4 章：[shared/domain/architecture_test.go](../../shared/domain/architecture_test.go)
  （domain 不允许出现任何具体技术词）。

**切面的另一种实现（没用 callbacks）**

- [chat-service/internal/agent/trace.go](../../chat-service/internal/agent/trace.go)：
  `traced` 包装器与六个 `*Detail` 函数（只记计数、不记内容）。
- [chat-service/internal/agent/nodes_instrument.go](../../chat-service/internal/agent/nodes_instrument.go)：
  `instrumented` 包装器、phase 配对 id、进度心跳。

下一章，抽象全部落地：我们逐节点走读 PlatePilot 那张真实的图——ingress 的三个
子步、plan 的有界环、tools 的并发与截停、两个停机点、finalize 的持久化，以及
`Run` 与 `RunLive` 两个入口为什么一个能给评测、一个能给 SSE。
