# 第 13 章 PlatePilot 的 Agent 图全景

> 上一章：[第 12 章 Eino 入门](12-eino-入门-核心抽象与底层原理.md) ｜ 下一章：[第 14 章 工具系统](14-工具系统-六个确定性工具与注册表.md)
>
> 本章把前两章的抽象落到这一个仓库上：逐节点走读那张真实的图。
> 真实文件与函数集中在文末 [§13.12 参考代码与目录](#1312-参考代码与目录)。

---

## 13.1 一次真实的 turn：先看它跑起来是什么样

先不看代码，看一次带追问的完整对话在图上怎么走。用户在同一条线程里说了四句话：

```text
① 用户：曼哈顿安静一点的意大利菜，适合约会
   ingress → plan → tools(search) → plan → answer → finalize         （2 个工具轮）
   答案里给出一个排好序的候选列表，候选快照落库（"第二家"才有意义）

② 用户：第二家安静吗？
   ingress：从 checkpoint 读回上次选中的餐厅与候选快照，
            规则解析"第二家"= 列表第 2 行，referencePrompt 锁定该 id
   plan → tools(evidence) → plan → answer → finalize

③ 用户：那帮我明晚 7 点订 4 个位
   ingress → plan → tools(get_availability) → plan
         → tools(request_reservation 出现：不执行，校验参数→生成摘要→存 checkpoint)
         → plan（看到 ConfirmationRequired，直接返回，不再问模型）
         → answer（输出确认摘要 + confirmation.required 帧）→ finalize
   turn 结束于停机点 awaiting_confirmation，goroutine 与内存状态全部消失

④ 用户（三分钟后，另一个 HTTP 请求）：确认
   这一次根本不进图：确认服务读 checkpoint 里逐字保存的参数，
   幂等下单，再开一个新 turn 告知结果（第 18 章细讲）
```

本章要讲清楚的，就是①②③里每一个箭头发生了什么、分支凭什么这么走、③的状态凭
什么能在④里复活，以及为什么这张图同时服务于「SSE 实时推送」和「离线评测」两个
脾气完全不同的主人。

---

## 13.2 图全景：六个图节点，ingress 里还有三个子步

图在 [runner.go](../../chat-service/internal/agent/runner.go) 的 `compile` 中声明，
`compile` 上方的注释就是它的设计图（仓库原文）：

```text
START -> ingress -> plan -+-> tools -> plan      （有界环：ReAct 的本体）
                          +-> clarify -> finalize -> END
                          +-> answer  -> finalize -> END
```

| 图节点 | 职责（一句话） | 读/写状态的关键字段 |
|---|---|---|
| **ingress** | 建状态、装载上下文、产出本轮计划 | 读 checkpoint/候选/记忆；写 `Messages`、`Plan`、`MemoryContext` |
| **plan** | 问模型「下一步干什么」，或在停机条件命中时**不问** | 读写 `Messages`；写 `PendingToolCalls`、`Usage` |
| **tools** | 执行本轮工具：只读并发、写即截停 | 写 `Candidates`、`Evidence`、值域、`ClarificationOptions`、挂起字段 |
| **clarify** | 把问题当答案输出，线程挂起等待输入 | 写 `State=awaiting_clarification`、`MissingSlots`，存 checkpoint |
| **answer** | 四种情形产出最终文本（含引用闭包） | 读证据/候选/告警；写 `FinalAnswer` |
| **finalize** | 先持久化、再记成功审计，产出 TurnResult | 写库；返回结果 |

逻辑阶段上是「七步」，因为 **ingress 内部按固定顺序跑三个子步**，它们还各自
发一对 `step.started/finished` 帧，前端时间线上能看见：

1. `loading_context`：读最近会话（回放最近 20 条 user/assistant 文本，剥除
   tool_calls）、checkpoint、候选快照；
2. `embedding_memory`：检索长期记忆并构建注入块（第 17 章）；
3. `interpreting`：槽位/意图抽取，产出本轮唯一的 `Plan`（第 15 章）。

为什么 interpret 不是图上的独立节点？因为它是**进入规划前的上下文装配的一部分**，
与历史/记忆一样「每个 turn 恰好一次、不参与循环」；把它放进 ingress，plan 就可以
在图上简单地只表示「模型决策」这一件事。子步与节点的区别也体现在观测词汇上：
step 永远 scoped 在一个 `phase_id` 之下，目前只有 ingress 有 step。

### 唯一的分支：plan 出口的三选一

```go
func(_ context.Context, st *TurnState) (string, error) {
    if st.needsClarification() { return nodeClarify, nil } // ① 先问人
    if st.PendingToolCalls    { return nodeTools, nil }    // ② 再干活
    return nodeAnswer, nil                                 // ③ 最后作答
}
```

这个顺序是**优先级**，不是随便排的：需要问用户时先问，因为对一家用户尚未选定的
餐厅继续跑工具，是花预算回答错误的对象。注意三条路径的终局：

- tools 执行完有一条**回边**到 plan（第 3 章 for 循环的本体）；
- clarify 和 answer 都是**终局节点**，各自直连 finalize；
- clarify **刻意不经过 answer**——否则 answer 要么覆盖掉刚生成的问题，要么（更糟）
  针对一家用户还没选定的店写出一段推荐。

### 共享状态沿边显式流动

图签名是 `Graph[TurnInput, *TurnResult]`：START 把 `TurnInput` 交给 ingress，
ingress 产出 `*TurnState`，此后每个节点签名统一为
`func(ctx, *TurnState) (*TurnState, error)`，直到 finalize 换成 `*TurnResult`。
消息列表只是 `TurnState.Messages` 一个字段——第 3 章「消息切片即全部记忆」的时代
正式结束。节点发事件不直接依赖 Runner：emitter 存在 context 里，节点通过
`emitFromContext` 发帧；脱离 Runner 直接调节点的测试里，这些调用静默为空。

---

## 13.3 ingress：一个 turn 的全部「前生」

ingress 不调模型做决策，但它决定了模型这一轮能看到什么世界。

**① 历史回放有边界也有筛选。** 从会话存储读最近 20 条消息（`historyReplayLimit`），
且只回放 user/assistant 的**文本**：tool_calls 助理消息和 tool 结果不持久化（存的
是最终答案），而一条「带 tool_calls 却没有后续 tool 消息」的助理记录在 chat API
看来是非法转录，所以即便将来开始存也必须剥除。读不到 checkpoint（新线程）是正常
情况；存储故障只加告警、按新会话继续，不让历史基础设施故障拖垮本轮。

**② 候选快照与 checkpoint 分两表读、合起来才是一个事实。** checkpoint 说
「线程在等一家餐厅」，候选快照说「给用户看过哪几家、什么顺序」。像「第二家」
这样的序数指代，只对一份**有序列表**有意义，两者缺一则下一轮只能靠猜。

**③ Plan 在任何工具调用之前产出，且一个 turn 只产一次。** 抽取器（可缺省，缺省
是官方支持的部署形态）把用户这句话解析成意图、硬过滤、软条件、缺失槽位、指代，
连同 checkpoint 与候选快照一起作为线程上下文传入。抽取本身「不会失败」：没有
模型或模型输出不可用时回落到规则计划。之后每个需要默认值的工具都读同一个
`st.Plan`——这保证了一个 turn 里不会出现「同一句话的两种读法」。

**④ 模型可见的上下文在这里组装齐。** ingress 最终把历史、回放记录、新 user 消息
拼成 `Messages`；记忆块、指代块则在 plan 里作为 system 段注入（13.4）。

---

## 13.4 plan：有界 ReAct 环的大脑

plan 是图上唯一调用「绑定了工具的模型」的节点，但它的**第一件事不是调模型**，
而是按顺序检查停机条件：

1. **澄清上限判定**：`needsClarification()` 为真（歧义选项 ≥2，或计划标记了缺
   槽位）时先调 `settleClarificationCap`。没到上限直接返回，分支把 turn 送去
   clarify；到了上限（同线程第 4 次）则按规则处置后**清掉歧义标志、继续前进**
   （13.7），保证不会无限问下去。
2. **写挂起短路**：`ConfirmationRequired` 为真时，不调模型、直接把
   `PendingToolCalls` 置 false 返回。这行处理的是 13.1 中③的尾部：写操作在
   tools 节点被挂起后，回边把 turn 送回 plan，此时助理消息里那条 tool_call
   没有对应的 tool 结果（工具没跑），而以「悬空 tool_call」结尾的转录会被 chat
   API 拒绝；更重要的是，模型刚刚请求下单，此刻唯一该发生的事是等用户回答，
   再问一次模型是问错了问题。
3. **真正的模型轮**：拼系统提示（由注册表生成的工具策略 + 记忆块 + 指代块）+
   历史，调用 planModel，把助理消息追加进转录，累计 token 用量。
4. **轮次护栏**：响应含 tool_call 时置 `PendingToolCalls`；若已达 5 轮上限，
   反而清掉它、置 `RoundsCapped` 并加一条中文告警「已达到单轮最大工具调用次数
   5，基于已有信息作答」——分支于是走向 answer，**带着已有信息答，而不是报错**。

模型绑定工具说明书发生在 Runner 构造期（`planModel = baseModel.WithTools(
registry.ToolInfos())`），说明书由 Go 类型生成（第 14 章）。回答合成用的是
**另一个模型实例**（composer，可用 `CHAT_MODEL_ANSWER` 单独覆盖）：规划模型与
写作模型分工，是第 16、22 章的话题。

---

## 13.5 tools：并发窗口、写操作截停、按调用顺序折叠

tools 节点取最近一条助理消息上的 tool_calls，执行纪律有四条，全部写在代码注释里：

**① 只读调用一扇窗、最多 3 个并发。** 模型一条消息里请求多个互不依赖的查询，
就是在声明「我不需要它们按顺序完成」，串行要花延迟之和。`runReadOnlyTools`
给每个调用一个结果槽位（按下标放，而不是 append 共享切片），用容量 3 的信号量
限流，全部返回后再统一折叠。3 的理由也很朴素：工具就那么几个、背后是同一个
数据库，超过几个并发后延迟不再改善、连接池争用开始；一轮要三个以上独立查询的
模型输出本就罕见，排队其余的代价为零。

**② 写操作出现即截停，且其后的调用全部丢弃。** 扫描调用列表时遇到
`RequiresConfirmation` 的工具：先做**写参数值域校验**（参数里的餐厅/时段必须是
本轮真正读到过的，schema 合法不等于业务合法），再让注册表生成人话摘要；两者
通过才 `parkForConfirmation`——置 `awaiting_confirmation`、逐字保存参数、分配
本次批准 ID、**立刻存 checkpoint**，然后本轮结束。摘要都生成不出来的调用不会被
挂起：让用户批准一个「读不懂内容的操作」等于训练他们不看内容就点确认；错误以
tool message 回给模型，让模型自己改。

**③ 上层对并发无感。** 转录、turn 状态、下一轮看到的世界，与串行执行**完全
一致**：结果等全部回来后按**调用顺序**折叠（所以不能用完成顺序 append）。并发
只在两处可见——时钟，和审计行写入顺序。

**④ 顺序由 seq 承载，而不是由时间戳承载。** 并发启动前在主 goroutine 上分配
递增 `ToolCallSeq`；审计行在各调用完成时写入（完成顺序），读侧按 `seq` 排序
拿回调用顺序。`tool.start` 按调用顺序发、`tool.finish` 按完成顺序发——finish
帧是对时钟的陈述，按住它等排序会让它报告的延迟变假；帧的交错也是客户端唯一
能看见「这轮并发了」的地方。

工具返回的结构化数据由 `absorbToolData` 折叠进状态：搜索结果进候选（同时执行
「陈旧 pin 清除」——重新搜索换掉列表后，指向旧列表的「这家」不再是任何可见
事实），证据进证据列表，resolve 的三种状态（resolved / ambiguous / not_found）
是三种不同的 turn 而都不是错误，可订时段只保留**值域**（它是库存不是搜索结果），
保存记忆则发出 `memory.saved` 事件——工具不允许自己发事件，事件词汇归图所有。

---

## 13.6 clarify 与 answer：两个终局，finalize 一个出口

**clarify 是终局节点。** 它把问题文本直接当作本轮答案；区分两种问法：歧义选项
≥2 时挂 `resolve_restaurant` 动作 + `restaurant_id` 槽位；纯缺槽位时挂槽位名
列表本身（不伪造一个工具名，下一轮需要知道的是「补什么」而不是「假装谁问的」）。
问题文案用街区、地址、菜系、评分让人区分，**绝不用数据库 id 让人做选择**——
用户从没见过 id，「在 42 和 77 之间选一个」不是问题。顺序纪律：**先存
checkpoint、候选快照，再发 delta 和 awaiting_input 帧**——客户端收到「该你了」
后立刻回读线程，看到的必须正是它被告知的状态。

**answer 节点是一个四分支开关**，分支条件全在状态里，模型不再参与选择：

| 情形 | 条件 | 行为 |
|---|---|---|
| 写已挂起 | `ConfirmationRequired` | 摘要文案**代码拼接**（不经模型改写——改写可能描述出一个用户没同意的订单）+ `confirmation.required` 帧 |
| 有据作答 | 有候选或有证据 | 走 composer：事实直述、观点挂引用，引用闭包不满足则整轮重生成（第 16 章） |
| 检索为空 | 工具跑过但候选/证据皆空 | **固定拒绝话术**，不拿空上下文再让模型自由发挥（第 3 章幻觉的兜底闸门） |
| 兜底 | 闲聊 / 模型已直接给出文本 | 透传模型文本；模型整轮只调工具、撞顶没留下正文时也走拒绝话术 |

进入「有据作答」前还会把证据/候选里读起来像指令的内容扫描进 `warnings`
（注入尝试要可见，第 9 章）。

**finalize 只做两件事，但顺序是承重的。** 先 `persistTurn`（线程行 → checkpoint
版本+1 → user/assistant 两条转录 → 候选快照），成功后再写 run 成功审计：一个在
线程竞争中输掉、状态从未 durable 的 turn，不能先在审计里声称成功。持久化整体是
**尽力而为**——答案已经产出，事后因存储失败扔掉答案是更大的错；存储故障降级为
可见告警，唯独版本冲突（同线程两个 turn 竞争，409）要返回，因为那不是存储故障
而是「这轮的状态不属于这轮的历史」。如果到达 finalize 时 `FinalAnswer == nil`，
那是内部错误而不是安静地返回空。

---

## 13.7 两个停机点与跨请求的复活

图有两个「把 turn 挂起、等人」的停机点，它们在协议上必须能被客户端区分，因为
**回答它们的是两个不同的接口**：

| 停机状态 | 触发 | 事件 | 用户如何应答 |
|---|---|---|---|
| `awaiting_clarification` | 同名歧义 / 缺槽位 | `state.awaiting_input` | 再发一条**消息**（同一个发消息入口） |
| `awaiting_confirmation` | 写操作通过校验与摘要 | `confirmation.required` | 调 `POST /conversations/:id/confirm` |

只收到 `message.end` 的客户端无法区分「答完了」和「答案故意是空的、该你做事了」，
所以这两个状态各自有独立事件；混淆它们的客户端会给一个 yes/no 问题弹文本框。

挂起期间发生的事，上一章讲过原理，这里看状态机如何闭环：

- **澄清计数防死循环。** 计数存 checkpoint（跨 turn），同线程连续 3 次澄清后，
  第 4 次必须继续：有候选就按相似度最高的店**明示假设**继续（回答里必须说明
  「按最相似的 X 继续」，静默假设正是这个上限要防的失败本身）；没有可假设的
  对象（缺的是用户从没给过的槽位），则停止追问，让 answer 如实报告缺口。
- **写挂起的复活。** checkpoint 逐字保存 `PendingAction / PendingToolCallID /
  PendingArguments`；批准 ID 在服务端生成而非取自模型的调用 ID（不同 provider
  可能跨请求复用 id），它就是下游幂等键承诺的东西（第 18 章）。
- **恢复不走图。** `Resume(threadID)` 读线程 + checkpoint + 最近历史，供确认/
  中断恢复使用；图只负责「跑一个 turn」，跨 turn 的状态复原是显式的读取面。

---

## 13.8 两个入口：Run 给评测，RunLive 给 SSE

同一个编译好的 runnable，有两个驱动入口，存在的理由注释里写得很直接：它们回答
不同的问题。`Run` 回答「这轮产出了什么」；`RunLive` 回答「每一部分是什么时候
产出的」。

```go
// Run：跑完再把全部帧交给你。通道在 Run 返回前必然关闭——批处理接口。
result, events := r.Run(ctx, in)          // 离线评测：完整转录 + 全部事件可断言

// RunLive：图在独立 goroutine 上跑，每出一帧回调一次；turn 结束才返回。
result, err := r.RunLive(ctx, in, func(ev Event) error { ... })  // SSE handler
```

**为什么流式入口非要有独立 goroutine 与实时转发？** 因为「在 turn 结束后送达的
delta 不是首 token，而是被切碎的整段答案」——那就抹掉了流式的全部意义。只有
边生产边消费，首字节延迟才成立（第 22 章实测：同一工作，流式首文本 p50
65.2 ms，整段完成 p50 122.4 ms）。

**128 帧缓冲为什么不会成为答案长度上限？** emitter 背后是容量
`eventBuffer=128` 的有界通道。若像 `Run` 那样「没人读、先全攒着」，一个长答案
发到第 129 帧就会阻塞，直到客户端 context 超时——长答案根本发不出去。
`RunLive` 用一个 goroutine 上图、主循环 `for ev := range ch` **并发抽干**，
生产与消费同步推进，缓冲只吸收瞬时抖动，不承载全部输出。两个细节同样关键：

- `emitter.send` 用 `select` 监听 run context，消费方消失时节点不会永远卡在
  发送上；
- 回调返回错误（浏览器断开）→ cancel 掉图的 context，且**继续抽干**而非提前
  返回——绝不能让图阻塞在一个没人读的缓冲上；turn 自身的失败不作为返回值，
  它已经以 `error` 事件发出（传输层需要的、也是所有调用方读取的统一形态）。

`Run` 则先跑完、关闭通道、连同结果一起返回——离线测试因此可以在零网络、零
等待下断言「整轮逐帧序列」，第 21 章的四道门禁全部建立在这个形态上。

### 一帧的旅行：节点 → emitter → 传输

节点只管 `emitFromContext(Event{...})`，不知道也不关心有没有人听；emitter 从
context 取（所以直接单测节点时事件为空）；Runner 在 `invoke` 里负责 run 身份
（run_id/trace_id）、在第一个节点之前先发 `message.start`（保证客户端看到的
第一字节是轮次开始）、成功收尾发 `message.end`、失败发 `error` 并写服务器日志。
事件词汇表是封闭集（第 19 章有测试扫它），phase/step 帧则由上一章那两个包装器
统一产生，业务节点对观测无感知。

---

## 13.9 护栏与常量：模型决定每一步，代码决定边界

把图上所有「模型不可越过」的数值集中看一遍：

| 常量 | 值 | 守的是什么 |
|---|---|---|
| `defaultMaxToolRounds` | 5 | ReAct 环长度：第 6 轮请求被拒，带着已有信息作答并告警 |
| `defaultMaxClarifications` | 3 | 一条线程的连续追问次数；第 4 问说明前三问没改变任何东西 |
| `eventBuffer` | 128 | 事件通道背压；RunLive 并发抽干使其不成为答案长度上限 |
| `readOnlyToolConcurrency` | 3 | 单轮只读扇出；背后是同一个数据库 |
| `historyReplayLimit` | 20 | ingress 回放的历史条数（文本 only） |
| `progressHeartbeatInterval` | 3s | 长模型调用期间 phase.progress 心跳节奏（60s 轮次 ≈ 20 帧，远低于缓冲顶） |

这些值全部可以由配置覆盖或开关（轮次、澄清、流式、phase 事件都有显式配置），
但**修改权在部署侧，不在模型的运行时输出里**。这正是第 11 章「骨架在代码，
每一步在模型」的具体形态。

---

## 13.10 取舍与边界

**① 固定六节点，扩展靠加分支而不是加自由度。** 这张图没有「模型选择下一个
节点」的通用路由——模型只能选择调不调工具、调哪个。餐厅发现这个产品域的 turn
形态是收敛的（检索 + 少量工具 + 一个写动作），固定图让「写路径不可达」「澄清
必先于工具」成为可以离线逐条断言的属性（第 21 章）。任务域变开放时，正确做法
是重新设计图，而不是放开路由。

**② 状态显式流过节点，换来了可测性。** 每个节点都是签名统一的普通函数，测试
直接构造 `TurnState` 调节点即可，不必启动整张图；仓库里大量 `*_slice_test.go`
就是这么逐个分支钉死的。代价是状态结构体约 40 个字段——项目用分组注释和
「字段为什么存在」的注释把它管住，而这比隐式框架状态更适合需要持久化的业务。

**③ 观测双轨：span 给排查，phase 帧给用户。** `traced` 写 run_nodes（含成败与
自定义 detail），`instrumented` 发前端进度帧；两者都在装配期包一层，节点无感。
细节纪律（失败路径也落 span、phase 用每阶段独立 id 区分多轮 tools）第 20 章讲。

**④ 图不负责跨 turn。** 图运行的输入只有 `TurnInput`，跨 turn 的一切（历史、
候选、计数、挂起参数）都经 ingress 从存储显式读入、经 finalize/clarify 显式
写回。这让图本身保持无状态服务的形态，可以随意并发跑多个 run；状态的唯一家是
数据库里的 checkpoint。

**⑤ 失败语义是事件流的一部分。** 图内错误向上抛到 `invoke`，由它统一发
`error` 帧（错误码是 `errs.Code`）并记日志/审计；而持久化失败大多不杀 turn
（尽力而为），版本冲突则必须杀（409）。「哪些错能降级、哪些必须显式失败」是
逐处设计过的，第 23 章给出完整的降级矩阵。

---

## 13.11 课后实验

**跑（全离线，脚本化模型驱动真实图）**

```bash
cd chat-service
go test ./internal/agent/... -count=1
# 按切片看每个分支各自的测试，名字即文档：
go test ./internal/agent/ -run 'TestClarify|TestCitation|TestReservation|TestFollowup|TestHistory|TestSlots' -count=1 -v
```

**读**

- [runner.go](../../chat-service/internal/agent/runner.go)：`compile` 的图注释
  与装配；`Run` / `RunLive` / `invoke` 三个函数的注释（「what did the turn
  produce」vs「when did each part happen」）。
- [nodes.go](../../chat-service/internal/agent/nodes.go)：建议顺序 `ingress` →
  `plan`（看四个前置检查）→ `runTools` + `runReadOnlyTools`（看结果槽位与
  折叠顺序）→ `needsClarification` / `settleClarificationCap` → `answerNode`
  （四分支表）→ `finalize`。
- [persistence.go](../../chat-service/internal/agent/persistence.go)：
  `persistPendingCheckpoint` 与 `persistTurn` 里的顺序与告警/冲突分野。

**想**

- 13.1 的③里，tools 节点挂起写操作后，turn 为什么还要回一趟 plan、再走 answer，
  而不是从 tools 直接结束？（提示：转录对 chat API 的合法性、确认文案不许模型
  改写、两个停机点共用 finalize 一个出口。）
- 把 `readOnlyToolConcurrency` 从 3 改成 1，系统的哪些输出**应当**逐 bit 不变、
  哪些观测会变？（第 21 章的回放测试正是这条性质的门禁。）
- 为什么澄清计数在 checkpoint 里，而工具轮次只在 `TurnState` 里？如果把工具轮次
  也持久化，会带来什么错误的跨 turn 行为？

---

## 13.12 参考代码与目录

**图的声明、编译与入口**

- [chat-service/internal/agent/runner.go](../../chat-service/internal/agent/runner.go)：
  护栏常量 `defaultMaxToolRounds=5` / `defaultMaxClarifications=3` /
  `eventBuffer=128`；`Config`（`AnswerStreaming`、`PhaseEvents`、规划/回答
  双模型名）；`Deps`；`NewRunner`（建模型、绑工具、编译）；`compile`（六节点、
  静态边、三分支目标集合）；`Run` / `RunLive` / `invoke` / `fail`；`runMeta`
  （run 身份与 spanSeq 走 context）。

**六个节点（本章主体）**

- [chat-service/internal/agent/nodes.go](../../chat-service/internal/agent/nodes.go)：
  `ingress`（三子步）、`plan`（澄清上限→写挂起短路→模型轮→轮次护栏）、
  `runTools`（写截停扫描）、`runReadOnlyTools`（容量 3 信号量、结果槽位、
  seq 预分配、调用顺序折叠）、`parkForConfirmation`（值域校验→摘要→checkpoint）、
  `absorbToolData` / `absorbResolve` / `dropStalePin`、
  `needsClarification` / `settleClarificationCap`、`clarifyNode` /
  `buildClarificationQuestion`、`answerNode`（四分支）、`confirmationAnswer`、
  `finalize`、`interpret`、`referencePrompt`（指代 pin 的两种强度）、
  `planSystemPrompt`（由注册表生成）。

**状态、事件与持久化**

- [chat-service/internal/agent/state.go](../../chat-service/internal/agent/state.go)：
  `TurnState` 全字段、`TurnInput/Result`、封闭事件类型集（message.*/tool.*/
  thinking.delta/citation/state.awaiting_input/confirmation.required/memory.saved/
  phase.*/step.*）。
- [chat-service/internal/agent/persistence.go](../../chat-service/internal/agent/persistence.go)：
  `historyReplayLimit=20`、`loadConversationContext`、`replayTranscript`、
  `saveCheckpoint`（版本 +1）、`persistPendingCheckpoint`（先存后发事件）、
  `conversationConflict`（409）、`persistCandidates`（位置有序、整表替换）、
  `settleTerminalState`、`persistTurn`（先持久化后记账）、`Resume`。
- [shared/domain/conversation/conversation.go](../../shared/domain/conversation/conversation.go)：
  五种线程状态枚举与恢复原则注释。

**观测包装器与性能数字的出处**

- [chat-service/internal/agent/trace.go](../../chat-service/internal/agent/trace.go)：
  `traced` 与六节点 detail（只记计数）。
- [chat-service/internal/agent/nodes_instrument.go](../../chat-service/internal/agent/nodes_instrument.go)：
  `instrumented`、`emitStep`、`startPhaseProgress`（3s 心跳）。
- [README.md](../../README.md)「turn's event stream」与 perf 表：流式首文本
  p50 65.2 ms vs 整段 122.4 ms，整轮 130.9 ms vs 123.0 ms；Agent 运行时自身
  开销每轮 p50 0.34 ms（离线 scripted，详见第 22 章）。

下一章进入图上调用最频繁的部件：六个确定性工具。工具说明书如何从 Go 类型生成、
参数谁来校验、领域错误怎么变成模型读得懂的话，以及一个专题——为什么这里用的是
Tool Calling 而不是 MCP。
