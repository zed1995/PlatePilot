# 第 19 章 SSE 与验证台：把一个 turn 实时送到浏览器

> 上一章：[第 18 章 敢写下去的 Agent：HITL 确认门、幂等与预约](18-敢写下去的-agent-hitl-确认门-幂等与预约.md) ｜ 下一章：[第 20 章 Agent 可观测性：run / tool / node 三级审计](20-agent-可观测性-run-tool-node-三级审计.md)
>
> 第三篇收尾时，服务端已经攒下一整套**事件名**：`message.delta`、`tool.start`、
> `state.awaiting_input`、`confirmation.required`……但读者从第 13 章起只见过它们的名字，
> 没见过它们**长什么样、按什么顺序、在什么时刻**到达用户眼前。这一章把它们真正送进浏览器：
> 一次 turn 怎么在 HTTP 上被摊开成帧、这套帧为什么必须是**封闭集**、流断了或错在哪一段该怎么表现，
> 以及验证台怎么把一串事件渲染成"**Agent 在想什么**"。
> 真实文件与函数集中在文末 [§19.9 参考代码与目录](#199-参考代码与目录)。

---

## 19.1 坏 case 开场：两分钟里一个字节都没有

先把问题摆到桌面上。一次真实对话要 **1–2 分钟**，而这段时间里，服务端做的两件大事——`plan`（决定下一步做什么）
和 `answer`（合成最终回答）——**各自是一次几十秒的同步模型调用**。计划里那份最早的事件集是这样的：

```text
message.start / message.delta / message.replace
tool.start / tool.finish / citation
state.awaiting_input / confirmation.required / memory.saved
message.end / error
```

现在数一数：`message.start` 发出之后，到第一次 `tool.start` 之间，如果模型正在"想"，这一串事件里
**一个字节都不会产生**。用户看到的是一个**没有内容的"运行中"**——一个空白 spinner。而"空白 spinner"
不是一种状态，它是**一次两分钟的等待最糟的可见形态**：用户分不清它在思考、在卡死、还是已经死了。

这是第一个坏 case，也是这一章要修的东西之一。但真正让本章动手的，是**第二个**——一个**真的发生过**的坏 case，
它把一个更尖锐的问题摔在面前：**流是发出去了，但没人看见。**

### 一次真实故障（2026-10-06）：整条流被前端静默丢掉

**现象**：一轮对话在前端**没有任何中间过程**。等一分钟后，提问、回答、候选、run 明细**一次性全部出现**。

**排查**：服务端、代理、事件序列**全部正常**——直接 `curl` `:8080` 与经 Vite 代理 `curl` `:5173`，
都能在 `t≈0` 收到 `message.start` / `phase.started`，随后每 3s 一帧 `phase.progress`；
响应头也干净（`transfer-encoding: chunked`，无 `Content-Encoding` / `Content-Length`，
带 `x-accel-buffering: no`）。

**根因在浏览器侧，而且极其安静**：服务端把**事件名放在 SSE 的 `event:` 行上**——`sse.go` 的 payload
结构体里**没有** `type` 字段——而前端 `sendMessage` 只做了 `JSON.parse(frame.data) as StreamEvent`，
**把 `frame.event` 丢掉了**。于是 reducer 的 `switch (event.type)` 对每一帧都读到 `undefined`、
一律走 `default`，结构性事件判断也一律返回 false：**整条流被静默丢弃**。用户最后看到的一切，
都来自 turn 结束时那次"重新拉取 transcript"，"突然一次性出现"正是那次 refetch 的形状。

**为什么测试没拦住**：当时的用例要么 mock 掉 `sendMessage`（`AgentConsole.test.tsx`），
要么直接构造带 `type` 的对象喂给 reducer（`useAgentTurn.test.ts`）。**"SSE 帧 → 带 `type` 的事件"
这条缝隙没有任何用例覆盖**，而它的失败模式是**静默的**——没有任何断言会变红。

这就是本章的全部题目：**一条流要跑到用户眼前，中间有五跳；五跳里任何一跳的"契约对不上"，
失败都不在服务端报错，而是变成一个看起来"什么都没有发生"的页面。**

---

## 19.2 概念与原理：为什么是 SSE，不是 WebSocket

### 先看数据的方向

选传输协议之前，先问一件更朴素的事：**这一条连接上，谁要说话？**

一次 turn 里，服务端要往下推的东西很多：开场的帧、每一个工具的开始与结束、答案文本的增量、
引用校验后的替换、等待用户的状态、token 用量、警告、错误。而**用户要说的话只有一句，
且它永远发生在下一轮**——再发一条消息，是**另一条 HTTP 请求**，不是同一条连接上的反向帧。

于是这个产品的形状是**单向推送**：服务端推一长串，客户端只在**结束时**（或者根本不需要）回应。
这就是 **SSE（Server-Sent Events）** 最贴的形状。

### SSE 是一个 HTTP 响应，不是一个新协议

这是 SSE 最被低估的好处：它在协议层面**什么都不是**——只是**一个 `Content-Type: text/event-stream`
的普通 HTTP 响应**，body 是一段有格式的文本流。所以：

| 你原本就有的东西 | 用 SSE 时还要不要额外做 |
|---|---|
| 端口与路由 | 不要。和 `/v1` 其它接口同一个服务、同一份中间件 |
| 错误信封 | 不要。开流之前照旧是那套 JSON 错误信封（19.5 会讲"开流之前"这条线画在哪） |
| 鉴权头 / 身份头 | 不要。`X-User-ID` 照旧走请求头 |
| 调试手段 | 不要。`curl -N` 能看见原始字节，`-N` 关掉 curl 自己的缓冲 |
| 浏览器兼容中间层 | 不要。反向代理只需**别缓冲**——这一条项目是显式处理的（见下） |

对照一下 WebSocket：它是一条**独立的、升级过的双向协议**——长连接握手、自己的帧格式、
自己的心跳（ping/pong）、自己的错误语义、自己的生命周期。这些都要学、都要在两端实现、
都要在测试里覆盖。它能给你的是**服务端与客户端在同一连接上互发**的能力，而这个产品**没有这种需求**。

一个诚实的取舍式总结：

- **需要双向、需要低频小消息、需要服务端主动 ping 于毫秒级**（在线协作、协同编辑、游戏、行情推送）→ WebSocket 合适。
- **需要"服务端把一个长过程讲给客户端听"，且想让这一切留在 HTTP 语义里**（模型流式输出、
  任务进度、日志跟随）→ SSE 合适，**而且更容易调试**：它的每一帧都是**可读文本**，
  一次 `curl` 就能把整轮对话的帧序打印出来。

外加一条很实际的约束：**浏览器的 `EventSource` 用不了。** 它**只支持 GET、不支持自定义请求头**——
而这里的 turn 是 `POST /v1/conversations/:id/messages`，还**必须带身份头**。
所以客户端不是 `EventSource`，而是 `fetch` + `ReadableStream`（`web/src/lib/sse.ts` 的开场注释逐字）：

> The built-in EventSource cannot be used for this stream: the turn endpoint is
> a POST that carries an identity header, and EventSource supports neither. This
> is a fetch + ReadableStream reader instead, which is also the only option that
> can be cancelled, and cancelling is how a Stop button reaches the server.

"**可取消**，而且取消就是 Stop 按钮到达服务端的方式"——这一句在 19.5 会变成一条硬行为：
客户端断开必须**真的让模型停下来**，而不是让它在对着一个没人看的socket继续花钱。

### SSE 本体：四行知识

SSE 的线格式小到可以在这里讲完：

```text
event: message.delta        ← 事件名（可以省略；省略时浏览器当默认事件）
data: {"delta":"这家店"}     ← 数据行（可以多行，客户端按 \n 拼接）
: keep-alive               ← 以冒号开头的是注释帧，客户端应当忽略
                       ← 一个空行结束一帧
```

四条推论，本章后面都会用到：

1. **帧的边界是空行**，不是 TCP 包。一个帧可能被拆在两次 `read()` 里，也可能几个帧挤在一次 `read()` 里。
   **客户端必须自己缓冲**（19.5）。
2. **注释帧**是保活的标准做法：它是合法的 SSE 帧，但**不构成事件**——于是"心跳"不需要发明事件名，
   也不会污染事件契约（19.5）。
3. **事件名在 `event:` 行，不在 JSON 里**。这正是 19.1 那次故障的现场：两半必须**在客户端被重新合起来**。
4. **响应不能被缓冲**。任何一层中间件把 body 攒起来，流就退化成"最后一次性到达"。
   项目在这里显式设了一个头：`X-Accel-Buffering: no`——注释写得很直白：**"Disables response buffering
   for common reverse proxies (nginx)"**。

---

## 19.3 一帧的五跳：从图到浏览器

一次 turn 的事件，从产生到抵达浏览器要经过**五个位置**。理解这五跳，就理解了"为什么改一个事件名
要动好几处代码"。

| 跳 | 位置 | 它做什么 | 它**不**做什么 |
|---|---|---|---|
| ① 产生 | `agent` 包内的 `Event` | 图在节点里发出**领域事件**（"这个工具开始了"） | 不知道 SSE、不知道 HTTP |
| ② 映射 | `app/chatservice.go` 的 `toStreamEvent` | 把 `agent.Event` 映射成 `httpapi.StreamEvent` | 不做编码 |
| ③ 编码 | `httpapi/sse.go` 的 `StreamEvent.payload()` | 按事件名 marshal 成**该事件的 JSON** | 不决定何时发 |
| ④ 写出 | `streamWriter.write` + `sse.Writer` | 拼成 `event:`/`data:` 行并**立即 flush** | 不重排、不合并 |
| ⑤ 解析 | 前端 `lib/sse.ts` → `api/chat.ts` → `useAgentTurn.reduce` | 切帧、把事件名合回 payload、归约成可渲染状态 | **不认识的帧不许弄坏一轮** |

**②那一跳有一个刻意的设计**，注释几乎是本章的题眼（`chatservice.go`，逐字）：

> The two share event-name strings by contract, but never share a type.

——**两层共享的是"事件名字符串"这一份契约，而不是类型**。`httpapi.StreamEvent` 是**传输中立**
的形状（`sse.go` 的注释：*"this layer owns the SSE encoding, so the agent never knows about
Server-Sent Events"*），于是 agent 包可以继续对 HTTP 一无所知，而 SSE 层可以继续对图一无所知。
代价是**同一个事件名在两处出现**：一处漏改，就是 19.1 那个静默故障的近亲。**这是这门课反复出现的
同一个主题的第三次现身**——第 4 章的端口、第 14 章的工具注册、第 16 章的引用闭包，都在处理
"同一份真相在几处落地，靠什么保证它们不漂移"。

### 帧是"随图执行"发出去的，不是攒到最后

这一条落在**两个入口的分工**上（第 13 章讲过 `Run` / `RunLive`，这里是它们在传输层的用法）：

- `Run`：跑完整个 turn，**返回完整转录 + 一个已关闭的事件通道**。它是**批接口**——每一帧都在最后
  可用，一帧也不提前。（离线评测与回放要的正是这个。）
- `RunLive`：图在**自己的 goroutine** 上跑，每一帧**产生时**就交给回调。它是**流接口**。
  `runner.go` 的注释把二者的差别说得比任何解释都清楚（逐字）：

> a delta delivered after the turn has finished is not a first token, it is the whole
> answer in pieces, and that is exactly the difference streaming is supposed to make.

——**"turn 结束后才送达的增量，不是首 token，只是把整段答案切成很多片。"**

写出的语义同样硬：`streamWriter.write` 之后**每一帧立即 flush**，注释说得很具体——

> Every frame flushes immediately: tool.start must reach the browser before the tool returns,
> buffering it until message.end would defeat the point of streaming.

——**`tool.start` 必须在工具返回之前到达浏览器**。否则用户看到的"工具正在跑"永远只在事后出现，
它就从"实时状态"变成了"历史记录"。

### 128 帧缓冲：一个必须被"同时抽干"的池子

agent 内部的事件通道是**有缓冲**的，容量是一个常量：

```go
eventBuffer = 128
```

缓冲的作用是**生产者不必等消费者**：节点发出一帧就继续跑，不会被一个慢网络卡住。但它同时埋了一个陷阱，
`RunLive` 的注释把它讲透了（逐字）：

> The channel holds eventBuffer frames; a turn that publishes more than that while nobody
> reads would block on the next send until the client's context expired, so a long answer
> could not be delivered at all.

——**"如果没人读，发出第 129 帧时会阻塞，直到客户端 context 过期；于是一段长答案根本送不出去。"**
一个长得超过 128 帧的回答（一次流式回答很容易）**不是"慢"，是"送不出去"**。

所以 `RunLive` 的关键动作不是"读通道"，而是**与图并行地读**：

```go
// 图在自己的 goroutine 上跑；主 goroutine 同时抽干通道——"同时"才是重点。
go func() { defer close(em.ch); result, _ := r.invoke(ctx, em, in); finished <- result }()

var forwardErr error
for ev := range em.ch {
    if forwardErr != nil { continue }          // 已经失败：继续抽干，别让图卡在没人读的缓冲上
    if err := onEvent(ev); err != nil {
        forwardErr = err; cancel()             // 客户端没了：取消整轮，让图和模型一起停
    }
}
```

这里有两个**都是刻意**的细节：

- **出错后仍然继续抽干**。图不能因为消费者跑掉而**阻塞在一个没人读的通道上**——它会随着被取消的
  context 自己停下来。
- **`onEvent` 报错 = 客户端消失 = 取消整轮**。这正是"Stop 按钮"在服务端的落点。

再往上一层，`app/chatservice.go` 还加了一层**更严的背压**：

```go
// Runner.Run 会把事件发到有缓冲的通道上，所以这一层再转转到一个无缓冲通道来抽干：
// 这让背压是诚实的——一个慢的 SSE 客户端会拖慢这一轮，而不是让 128 个缓冲事件堆积。
```

——**agent 那层有缓冲（为了不让节点卡住），应用这层**故意没有缓冲**（为了不让事件在内存里堆积）。
两句话看起来矛盾，其实是在两个不同的问题上各站一边：
**图的"不被消费者拖死"** 与 **服务的"不替消费者攒着一堆没人看的帧"**。

### `message.start` 必须是第一帧

有一处顺序被特意钉死，而且它是**从故障里学来的**（`nodes.go` 的注释，逐字）：

> message.start is emitted by invoke() before any graph node runs so that the run's start
> frame is the first byte the client receives. Emitting it here instead would race against
> the instrumented wrapper's phase.started and let phase.started arrive first; a client that
> resets its state on message.start would then lose the phase rows that arrived before it.

——**`message.start` 由 `invoke()` 在**任何节点之前**发出**，否则它会和第一个 `phase.started`
**抢跑**；而客户端在 `message.start` 上**重置本轮状态**，于是"比它先到的那行 phase"会被**擦掉**。

这条约束的教训是通用的：**一个"重置状态"的事件，必须在它所重置的那段序列的最前面**。
客户端侧也留了对称的宽容（`useAgentTurn.ts` 注释）：一次迟到的 `message.start` **不会清掉
已经收集到的 phase 行**——服务端今天保证了顺序，但"今天保证"不等于"客户端可以假设"。

---

## 19.4 封闭事件集：为什么必须封闭，怎么把它焊死

### 事件全表

`POST /v1/conversations/:id/messages` 的回答就是这条流。**契约里的每一条**如下（README 的表格
与 `sse.go` 的常量一一对应；加粗部分是 M4 落地后补的四类帧，见 19.6）：

| 事件 | payload | 含义 |
|---|---|---|
| `message.start` | `run_id`, `thread_id` | 一轮开始了。**在此之前的一切都属于上一轮** |
| `message.delta` | `delta` | 回答文本的增量，**整轮都是"暂定"的** |
| `message.replace` | `text` | **丢掉本轮文本，改用这一整段** |
| `thinking.delta` | `delta` | **模型自己的推理增量**，独占一条通道 |
| `tool.start` | `call_id`, `tool` | 一次工具调用开始 |
| `tool.finish` | `call_id`, `status`, `latency_ms` | **那个 `call_id`** 的调用结束 |
| `citation` | `evidence_ids` | 本轮**通过校验**的证据 id 集合 |
| `state.awaiting_input` | `state`, `pending_action`, `missing_slots` | 线程挂了一个**问题**，等下一句话 |
| `confirmation.required` | `state`, `pending_action`, `summary` | 线程挂了一次**写入**，去 confirm 路由回答 |
| `memory.saved` | `memory_id`, `memory_type`, `content`, `refreshed` | 本轮写了一条长期记忆 |
| `message.end` | `finish_reason`, `usage`, `warnings` | 本轮结束。**只有到这一刻文本才是最终的** |
| `error` | `code`, `message` | 本轮失败；`code` 是 `errs.Code` |
| `phase.started` | `phase_id`, `phase`, `title`, `started_at` | 一个粗粒度阶段开始（ingress / plan / tools / answer） |
| `phase.progress` | `phase_id`, `title` | **改写**正在运行的那行的标题，不新开一行 |
| `phase.finished` | `phase_id`, `phase`, `finished_at`, `outcome` | 阶段结束（成功时 `outcome` 省略） |
| `step.started` | `phase_id`, `phase`, `step_id`, `step`, `title`, `started_at` | ingress 内部的一个子动作开始 |
| `step.finished` | `phase_id`, `step_id`, `finished_at`, `outcome` | 子动作结束 |

### 服务端侧：两条规矩

**规矩一：不认识的事件类型，宁可报错也不发出去。** `payload()` 的 `default` 分支：

```go
default:
    // An unknown event type would desynchronize the client's state machine;
    // dropping it with an error is safer than emitting a nameless frame.
    return nil, errUnknownStreamEvent(ev.Type)
```

——**"一个未知的事件类型会让客户端的状态机失同步；报错比发一帧无名的帧安全。"**
注意它选择的是**报错**，而不是"发一帧只有 `data:` 的帧"或"静默丢弃"：**静默丢弃正是 19.1 那次故障的形状。**

**规矩二：契约里的每一个事件都必须被真的扫到。** 测试里有一份**手写的完整清单**：

```go
// 这里手写列出，而不是从常量推导——于是一次新增事件变成"两处刻意的编辑"：
// payload() 对未映射的类型报错，而这份清单证明每个映射过的类型都可达。
func everyStreamEventType() []StreamEventType { /* 17 个常量 */ }
```

这条测试的注释把它的**杠杆**说清楚了：

> That makes an incomplete sweep a runtime failure, which is why the sweep is asserted.

——**漏扫 = 运行时失败，所以这份清单本身要被断言。** 具体地：如果你加了常量但忘了在 `payload()`
里分支，那条事件一旦真的发出就会**报错**（规矩一拦住了它）；而这条测试会**在 CI 里**先告诉你
"你漏了一处"。**两道检查咬在一起，把"忘了改第二处"从线上事故降级成测试失败。**

### 客户端侧：两条对称但**方向相反**的规矩

| 侧 | 规矩 | 原文依据 |
|---|---|---|
| 服务端 | **永不发出契约未定义的事件** | README：*the server never emits one the contract does not define* |
| 客户端 | **遇到不认识的事件名，必须忽略它** | `api/chat.ts`：*Callers must treat an unmatched name as "ignore"* |

**两边不对称，而且这种不对称是刻意的。** 服务端封闭是为了让"流的语义"可被穷举、可被测试；
客户端宽容是为了让**服务端学新东西**（这已经是第三篇里第二次扩事件集了：`phase.*` / `step.*` /
`phase.progress` / `thinking.delta` 都是后加的）**不会打破老客户端**。`api/chat.ts` 的注释写明了
这个未来（逐字）：

> The back end's event set is closed today but grows as the agent does — node-level trace is
> already planned — and a client that rejected an unfamiliar frame would break every time the
> server learned something new.

前端的实现就是 reducer 里那个 `default`：

```ts
default:
  // 一个不认识的事件永远不许弄坏一轮：上面这份联合类型是服务端今天发的东西，
  // 而"下一类事件"是随某一个 release 来的，不是随这个文件来的。
  return state
```

**"忽略"在这里是状态不变**——不是"吞掉然后继续"那么含糊，而是**明确地、什么也不做地继续**。

### 逐条讲"为什么是这个形状"

契约里最值得停下来的几条：

**① `message.delta` 是暂定的，`message.replace` 是整段而不是 diff。**
这是第 16 章的引用闭包在协议上的投影：答案在**生成之后**被校验，首轮引用了本轮并不拥有的
证据 id 就要**整轮重新生成**——可**在流式下，错的文本已经到用户屏幕上了**。协议的解就是这两个事件：

```text
message.delta    整轮标注"暂定"，客户端累积成一份"正在渲染的东西"
message.replace  丢弃本轮累积的一切，以这一整段为准
message.end      到这之前，什么都不要落本地存储
```

为什么是**整段**而不是补丁？`sse.go` 的注释（逐字）：

> It is a whole body rather than a diff because the corrected answer is a fresh generation,
> not an edit of the first one: character offsets from the first body do not survive into
> the second.

**"第二个正文不是第一个正文的编辑，而是重新生成；第一份里的字符偏移在第二份里不存在。"**
测试还把这条钉死：replace 帧**只许带 `text`，`delta` 字段的"存在"本身就算失败**——
因为**一个把增量与替换搞混的客户端会把答案渲染两遍**。

**② `thinking.delta` 不与 `message.delta` 合并。** 它俩的 payload **长得一模一样**
（都是 `{"delta": "..."}`），区别**只在事件名**：

> the event name is what tells the client whether to append to the answer or to the thinking trace.

**"事件名是唯一告诉客户端该追加到答案还是追加到思考轨迹的东西。"** 为什么不能混？因为
**"模型在想"不是"模型在说"**——把推理内容混进答案正文，等于**伪造了一段用户以为有出处的话**。

**③ `tool.finish` 按 `call_id` 配对，绝不按位置。** 第 14 章讲过工具轮的并发：同一轮最多 3 个只读调用
**并发**跑，于是**完成顺序与调用顺序不同**。前端的注释：*tool.start and tool.finish pair on call_id,
never on array position — a concurrent tool call reorders the frames and position would mismatch.*
更细的一条在服务端的工具轮注释里：`tool.finish` **按完成顺序发出，而不是排队等排序**——

> A finish frame is a claim about the clock, and holding one back so it could be sorted would
> make the latency it reports wrong. The interleaving is also the only place the concurrency
> is visible to a client, which is the point of it.

——**"一个 finish 帧是对时钟的陈述；为了排序把它压住，会让它报告的延迟变成假的。而这种交错
还是并发对一个客户端唯一可见的地方，而那正是重点。"** 所以"帧乱序"不是 bug，是**特性**：
**保序的信息在 `seq` 里（审计行），不在这条流里。**

**④ `memory.saved` 带**内容**，不只带 id。**

> The content is carried because it is the memory: a client that showed only an id would be
> telling the user "something was saved" and asking them to go look it up, when the whole
> point of the turn was the sentence.

**"只给一个 id，等于告诉用户'有东西被保存了'，然后让他自己去查——而这一轮的重点就是那句话。"**
还有一个更细的取舍：第一次保存时 `refreshed` **字段被省略**，而不是发一个 `false`——
注释说，**一个靠"字段在不在"判断的客户端，不能把第一次保存读成"重复"**。

**⑤ `state.awaiting_input` 与 `confirmation.required` 分开。** 第 18 章讲过语义（一个是非题 vs 一个文本问题），
这里补上协议的理由——`sse.go` 的注释逐字：

> the two are answered by different endpoints: a clarification by another message, a confirmation by a decision POST.

**"两者由不同端点回答。"** 于是前端也就能诚实：**分不清这两者，前端就会给一个是非题递上一个文本输入框。**
另外 `state.awaiting_input` 的字段**刻意复用了 checkpoint 自己的词汇**：

> a client that stored them could reconstruct the pending state, and the same values are what
> GET /v1/conversations/:id reports, so the stream and the read path cannot disagree about
> what "waiting" means.

——**"流与读路径不能对'在等什么'给出不同答案。"** 这又一次是本章的主题：**同一份真相，不许有两个说法。**

---

## 19.5 一个流的三段人生：开流前、开流后、客户端走了

一条 SSE 流**不是只有一种错误**。项目在这里划了一条线，而且这条线**决定了错误以什么形状出现**：
先看 `SendMessageHandler` 的注释（逐字）：

> Validation and thread-existence failures happen before the stream opens and therefore use the
> normal JSON error envelope. Everything after the first byte — including a model failure
> mid-turn — is delivered as an error event inside the stream, because the client is already
> parsing SSE frames.

翻译成三段：

| 段 | 什么时候 | 错误长什么样 | 为什么 |
|---|---|---|---|
| 开流之前 | 请求体不合法、线程不存在 | **普通 JSON 错误信封**（`400` / `404`） | 此时客户端还在用普通 HTTP 语义读响应，发一帧流是"用一个还没建立的通道说话" |
| 开流之后 | 模型中途失败、上游超时 | **流内的 `error` 帧**（`code` + `message`） | 客户端已经在解析 SSE 帧了，它只能在这条流上读到失败 |
| 客户端走了 | 连接断开 | **什么都不发** | 没人读了 |

第三段有个必须停一下的实现细节：

```go
emitErr := svc.SendMessage(ctx, in, sw.write)
close(stopHeartbeat)
// 一个被取消的 context 就是客户端断开：没有人还在读一帧错误，
// 而且对一条死连接写这一帧本身也可能失败。
if emitErr != nil && ctx.Err() == nil {
    _ = sw.write(StreamEvent{Type: StreamError, Code: ..., Message: ...})
}
```

——**"`ctx.Err() == nil` 这个条件是全部重点"**：客户端还在，失败就**必须**说；
客户端已经走了，说给谁听？而且**对死连接写帧的行为本身也可能失败**——于是"报告失败"成了又一次失败。

这一段的另一半在 `RunLive`（19.3）：`onEvent` 报错 → **取消整轮**。两端合起来就是
"Stop 按钮"这条链路完整的样子：**浏览器 abort → fetch 断开 → 服务端 context 取消 → 图停 → 模型调用停**。
测试 `TestClientDisconnectCancelsRun` 就是这条链路的断言：**客户端取消后，run 的 context 必须在秒级内被观察到已取消**
（`fake.blockOnCancel` + `cancelObserved` 通道）。

### 流里的错误帧要**遮蔽内情**

错误帧不是把 `err.Error()` 原样发出去。测试里有两个正对着的用例：

| 用例 | 输入 | 期望的帧 |
|---|---|---|
| `TestSendMessageFallbackErrorFrame` | `provider_unavailable: model down` | `code = "provider_unavailable"`，`message = "provider_unavailable: model down"` |
| `TestSendMessageInternalErrorFrameHidesDetails` | `internal: db password hunter2 exploded` | `code = "internal"`，`message = "internal server error"`，**且帧里不许出现 `hunter2`** |

——**同一个信封的两种待遇**：能被用户理解的原因（模型不可用）**照实说**；
**内部错误（可能含密钥、含内情）只留一个 `internal` 和一句通用文案**。这条规矩与整个
`/v1` 面上的错误信封是同一套，这里只是**换个传输层再落一次地**——**唯一的差别是：
开流之后连"换个传输层"的机会都没有了，所以遮蔽这件事必须在流里再做一遍。**

### 心跳：15 秒一次的注释帧

一个"模型想几十秒"的 turn 在**代理**眼里和一个死连接没有区别。解法是**保活**：

```go
// heartbeatInterval 必须低于服务端每次写的超时（默认 30s），
// 这样"模型想了一会儿"的安静 turn 不会被代理误判成死连接。
const heartbeatInterval = 15 * time.Second
```

心跳写在**一个独立 goroutine** 里，由 `stopHeartbeat` / `ctx.Done()` 收尾；内容是**注释帧**
（`':' + "keep-alive"`），注释写明了为什么选注释：**"Comments start with ':' and are ignored by EventSource clients."**
——**心跳不需要新事件名**，它不需要出现在契约里。

而**客户端必须知道怎么把它扔了**（`lib/sse.ts`，逐字）：

> Frames that carry no event name are dropped rather than emitted, which is what disposes of
> the back end's 15s ': keep-alive' comment frames — a comment has no event line and would
> otherwise read as an unnamed event.

——**"没有事件名的帧被丢弃，这就是后端那句 15s 的 `: keep-alive` 的去处。"**
两侧对同一个 15 秒的约定，各自说了一遍——**这份"契约"没有写在任何 schema 里，它写在两边的注释里。**
（这也解释了为什么 19.1 那类故障能活下来：**注释是文档，不是测试。**）

### 前端的两条纪律

**纪律一：帧的边界由客户端自己保证。** `readSSEStream` 的注释（逐字）：

> The accumulating buffer is not defensive: a single SSE frame arrives as whatever TCP handed
> us, so assuming one read is one frame would interleave a message.delta with a frame boundary
> and hand JSON.parse half an object.

——**"缓冲不是防御性代码：一个 SSE 帧到达时是 TCP 给我们的任意切法。"** 它的实现是
**按 `\n\n` 切 + 保留残余**，并在流关闭后再补一次：

```ts
if (buffer.trim() !== '') emitFrame(buffer, onFrame)   // 最后没有空行收尾的帧仍然欠着，
                                                      // 而那一帧常常就是这一轮结束的 message.end
```

**纪律二：结构性事件不许排队等文本增量。** 一次 turn 会有**几千个** `message.delta`，
一次性 `setState` 会让浏览器整轮都在重渲染而不是在追加文本——所以增量**按 50ms 批量归约**
（注释：*50ms is below the threshold where streaming reads as chunky and above where it reads as wasted work*）。
但**结构性的帧不能等**：

```ts
// 结构性事件短路增量批次：phase 变化或工具行要在到达的瞬间出现。
// 增量仍然按 50ms 批量；两条路共用同一个待处理队列——这是在不做两片状态的情况下保序的便宜办法。
if (isStructuralEvent(event)) { /* 先把队列抽干，再立刻归约这一帧 */ }
```

`isStructuralEvent` 是一份**封闭列表**（`phase.*` / `step.*` / `message.start` / `message.replace` /
`tool.start` / `tool.finish` / `state.awaiting_input` / `confirmation.required` / `memory.saved` / `error`），
注释又把这门课的复调说了一遍：**"这份清单是刻意封闭的：加一项意味着两处刻意的编辑"**（这里与 reducer）。

### 流莫名关闭 = interrupted

一个 turn 有三种"结束"：`message.end`（正常）、`error` 帧（失败）、**以及什么都没有**。
第三种真实存在——`useAgentTurn` 的注释说这是 provider 偶尔会干的事——所以它有一个**自己的状态**：

> interrupted is the state a turn lands in when the stream closes with no message.end and no
> error. The model provider does this occasionally; without it the UI would sit on "running"
> forever for a turn that is already over.

而这里还藏着**本轮与前端的最后一处"契约同步"**：turn 结束后前端会**重新拉一次 transcript**，
把"流里渲染的那一轮"与"服务端真正落库的那一轮"对齐。注释解释了为什么必须先丢弃流里已经渲染的：

> A finished turn exists twice: once as the events this page reduced, and once as rows the server
> wrote. Rendering both would show the question and its answer twice over.

——**一轮结束之后，权威版本是库里的那一份，流里那份是"过程"。** 这与第 18 章那句
"**状态是权威的，展示是派生的**"是同一句话的另一个侧面。

### 那次故障留下的守卫

回到 19.1 的那次真实故障：**修法与守卫都在前端这一侧**。

- **修**：`api/chat.ts` 在解析帧时把**事件名合回 payload**（`{ ...payload, type: frame.event }`），
  注释把这个"为什么不能忘"写在了出事的地方（逐字）：

  > The event name is the type. The back end puts it on the SSE `event:` line and never inside
  > the JSON body, so the two halves have to be recombined here: a reducer that switches on
  > `event.type` reads `undefined` for every frame otherwise, and the entire stream reduces to
  > a no-op.

- **守卫**：新增 `web/src/api/chat.test.ts`，走**真实的
  `fetch → ReadableStream → readSSEStream → sendMessage` 全链路**，断言**事件顺序与 `type`、
  payload 字段**，以及**"非 JSON 帧被跳过但不终止本轮"**。

这条守卫的形状值得记下来：**mock 掉中间层，恰好把中间层最容易出错的那段跳过了。**
所以这条测试**刻意不 mock 任何中间层**——它直接喂 chunk 字符串，让真实解析器去切。

### 一个流里另外两条"必须想清楚"的边界

- **开流前必须先确认线程存在**。handler 在打开流之前**单独查一次线程**：
  *"posting into a mistyped thread must 404 as JSON, not stream an error event into a conversation
  that does not exist."*——**一个拼错的 id 不该被当成一次对话失败**。
- **同一路由的 CORS 预检**也得对：`TestCORSPreflightOnSSERouteAllowsUserHeader` 断言
  SSE 路由的预检**允许身份头**，否则浏览器根本发不出这个 POST。

---

## 19.6 验证台：把"Agent 在想什么"做成可视化教材

服务端所有的帧，最终都要回答用户一个问题：**"它到底在干什么？"** 验证台就是那个回答的现场。
`web/src/pages/AgentConsole.tsx` 的开场注释先把**这一个页面为什么不在运维台里**说清了（逐字）：

> AgentConsole is the agent verification bench. It is a second, deliberate departure from
> "read-only admin console" (web/AGENTS.md): this page talks to /v1 as a person and does write.
> That is why it lives on its own route with its own sidebar group rather than being folded into
> the operator pages — the moment a page can send a message, the word "read-only" stops
> describing the app.

——**"一个页面一旦能发消息，'只读'这个词就不再描述这个应用了。"** 这就是第 4 章那条
"分层即纪律"在产品层面的样子：运维台**只读**（`/admin/v1`，loopback 守卫），
验证台**以人的身份写**（`/v1`，带 `X-User-ID`）。**两套接口、两条路由、两种权限假设，不混在一个页面里。**

### 三列，一个想法

同一份注释给出了页面的骨架（逐字）：

> Three columns, one idea: the thread list is what exists, the transcript is what was said, and
> the rail is what happened behind it.

**"三列一个想法：左边是存在什么（会话列表），中间是说了什么（转录），右边是背后发生了什么（边栏）。"**

| 区域 | 回答的问题 | 主要组件 |
|---|---|---|
| 左列 | 有哪些会话 | `thread-list.tsx` |
| 中列 | 说了什么 / **正在说什么** | `message-list.tsx`、`answer-text.tsx`、`composer.tsx`、`step-timeline.tsx` |
| 右列（边栏） | **背后发生了什么** | `tool-timeline.tsx`、`citation-drawer.tsx`、`candidate-cards.tsx`、`memory-panel.tsx`、`gate-banner.tsx`、`run-replay-panel.tsx` |
| 顶部状态 | 这一轮现在处于什么状态 | `turn-status-bar.tsx` |

几个组件的设计取舍值得单拎出来，因为它们都是**"把协议翻译成人的问题"**的例子：

**`step-timeline.tsx`——把"空白 spinner"换成"有名字的动作"。** 它的注释（逐字）：

> this one shows the work the agent was doing when no tool was on the wire. Together they answer
> the question a user has during a two-minute turn: "what is it doing right now".

**"它显示的是'没有工具在链路上时'Agent 在做的事"**——正好补上 19.1 那个坏 case 的窟窿。

**`tool-timeline.tsx`——同一份列表渲染两次。** 注释（逐字）：

> The live stream says a tool started and later that it finished; only the run replay says what it
> returned. So the same list is rendered twice in a turn's life — once while it runs, once when
> the replay lands — and the second render is strictly more informative.

**"直播说'开始了'又'结束了'，只有回放说'返回了什么'。"** 于是同一份数据结构被渲染两遍，
差别只在**有没有挂上摘要**——这是"第二遍严格更有信息量"的一个干净样本，也是第 20 章审计的**前端入口**。

**`citation-drawer.tsx`——脚注必须能打开。** 注释（逐字）：

> A [^n] in the answer is a claim with a pointer. Until the pointer opens onto the document it
> names, the number is decoration. So this fetches the one document on demand rather than
> prefetching the whole citation set: a turn cites six to ten documents and the user reads one.

**"一个 `[^n]` 是一个带着指针的断言；在指针能打开它所命名的那份文档之前，那个数字只是装饰。"**
而且它**按需取那一条**，不预取全集——**一轮引用 6–10 份文档，而用户只读其中一份**（第 9 章的
"证据组装"在这里终于有了人眼可见的落点）。

前端还专门保留了一个**用来抓谎的推断**（`useAgentTurn.ts` 末尾）：

```ts
// 引用事件是服务端认为"可引用"的集合；答案文本是模型"选择标注"的东西。
// 把两者比对，正是这台机器能说"这个脚注背后什么都没有"、而不是打开一个空抽屉的方式。
export function unresolvedCitations(text: string, citable: number[]): number[] { /* ... */ }
```

——**"服务端说可引用的是这 6 个；答案里真的标了的那几个才对得上；对不上的，就是要有话直说的那部分。"**

### 状态栏：为什么 `awaiting_*` 不是终态

`turn-status-bar.tsx` 把 turn 的状态映射成人话，逐字如下：

| 状态 | 显示 |
|---|---|
| `streaming` | 运行中 |
| `done` | 已完成 |
| `failed` | 失败 |
| `awaiting_user` | 等待你回答 |
| `awaiting_confirm` | 等待你确认 |
| `canceled` | 已停止 |
| `interrupted` | 未正常结束 |

而 `awaiting_user` / `awaiting_confirm` 这两个状态**是 `message.end` 之后才判出来的**——这一点在
reducer 里被特意写在最显眼的位置：

```ts
case 'message.end':
  // 等待态压过 done：线程是挂住的、下一次请求是用户的，
  // 所以报"完成"会是与服务端将要给出的答案不同的另一个说法。
  phase: state.confirmation ? 'awaiting_confirm' : state.awaiting ? 'awaiting_user' : 'done',
```

——**"两个等待态不是终态。"** 它们的理由在 19.4 已经讲过（两者由**不同端点**回答），前端只是把它翻译成
**两句话、两个不同的下一步操作**：一个是文本输入框，一个是确认/取消按钮。

### phase / step 帧：为什么值得新增四个事件

回到本章开场那个坏 case。修法是在协议上**新增四帧**（加上后来补的 `phase.progress` 与
`thinking.delta`），理由是**计划里那套事件在这段静默里一个字节都不产生**：

> 一轮真实对话要 1–2 分钟，其中 `plan` 与 `answer` 各是一次几十秒的同步 LLM 调用。计划里的事件集
> 在这段静默里一个字节都不产生，前端只能显示一个没有内容的「运行中」。

新增的四帧不是"再来一批状态"，而是**把一轮拆成有名字的动作**：

```text
message.start → phase.started(ingress-1) → step.started/finished ×3
  → phase.finished(ingress-1) → phase.started(plan-1) → phase.progress(plan-1)
  → ... → phase.started(answer-1) → message.delta → phase.finished(answer-1)
  → message.end
```

（这是实测的帧序，来自 M4 的实施记录。）三条设计上的取舍，全部写在这套帧的名字与字段里：

**① `phase_id` 存在，是因为 `plan` 会循环。** `phase` 只有四个值
（`ingress` / `plan` / `tools` / `answer`），而 **`plan` 与 `tools` 会来回循环**。所以配对**必须按 id**：

> PhaseID and StepID are run-unique, server-assigned strings a client uses to pair start and
> finish frames when several rounds of the same phase happen — tools in particular loops
> plan->tools->plan, and a client that keyed off Phase alone would replace the first round's
> running row on the second.

一个按**名字**配对的前端，会把第二轮的 `plan` 当成第一轮的**同一个阶段被改写**——**"循环"就会看起来像"并列步骤"**，
而那正是这套帧想让人看见的东西的反面。

**② `step` 只属于 `ingress`，因为只有 ingress 里"等待"是无名的。** 三个子动作各有中文标题：
`loading_context`「读取最近会话与候选快照」、`embedding_memory`「检索长期记忆」、
`interpreting`「提取意图与槽位」。注释说它们的目的是让**加载阶段本身**不是一个"巨大的无名等待"。

**③ `phase.progress` 改写标题，不新开一行。** 一个 `plan` 调用可能几十秒，客户端不需要新行，
只需要那一行**在动**：

```text
phase.started(plan-1, "正在制定下一步计划")
phase.progress(plan-1, "正在推理（已 6 秒）")     ← 只有 phase_id 与 title，
                                                ← 连 started_at 都不带（时间没变，行还在跑）
```

心跳密度是**3 秒**（`progressHeartbeatInterval = 3s`），取舍也写下来了：
**1 秒会在长轮里灌满 128 帧的事件通道，3 秒足够让用户看见行在动。**

**四个开关纪律**（这一节最后一条，也是最重要的一条）：

- 这批帧是**加法**：老客户端**按事件名忽略**它们（19.4 的客户端规矩），关闭也不改变传输。
- 一个开关管到底：`AGENT_PHASE_EVENTS`（默认 **true**）关掉时，
  **phase / step 帧与进度心跳一起静默**——不存在"关了 phase 帧但还在发心跳"的中间态。
- 关掉**不改变答案本身，只改变线宽**，所以可以随时回退而不动数据。
- 实测数字（M4 验证记录）：前端 `npx tsc --noEmit` 无输出、`npx vitest run` **22 个文件 124 个用例全绿**；
  浏览器实测发送后约 5 秒，主对话区与状态栏已是「运行中　正在加载会话上下文」，
  **之后才出现回答文本——中间过程可见。**

### 为什么前端值得为"可测"留一块地

`useAgentTurn` 把 reducer 拆成**纯函数**并导出：

```ts
// reduce 是这个 hook 的纯的那一半，于是测试可以不开 React 驱动它。
export function reduce(state: TurnState, event: StreamEvent): TurnState { /* ... */ }
```

——于是**整条状态机可以不开浏览器、不连服务端地被驱动**。这也是 19.1 那次故障的结构性教训：
**纯函数好测，但"帧 → 事件"的解析层没人测。** 后来补的守卫正是补在那条缝上
（`web/src/api/chat.test.ts` 走真实解析链路），**而不是继续给纯函数加用例**。

---

## 19.7 取舍与边界

- **为什么不用 WebSocket？** 因为**只有一个方向需要"实时"**：一个 turn 的产物是服务端往下推的，
  用户的下一句话**是另一条 HTTP 请求**。用 WebSocket 要把握手、帧格式、双端心跳、错误语义、
  连接生命周期**全部自己实现一遍**，换来的能力（同一条连接上双向）**这个产品用不到**。
  SSE 还额外送你一件事：**它是纯 HTTP**——同一端口、同一中间件、同一套错误信封、`curl -N` 直接可读。
- **代价：没有断点续传。** 这条流**不用 `id:` 字段**，也就没有 `Last-Event-ID` 重连语义
  （注意：SSE 的 `WriteEvent` 第一参就是事件 id，这里刻意留空）。原因不是省事，而是
  **一次 turn 不是一份可重放的日志**：它结束之后，权威版本是 transcript 里的那几行，
  客户端"重连"的正确动作是**重新读一次 transcript**（`AgentConsole` 里那套
  "结束后重新拉取 + 去掉已经在流里渲染过的回声"就是这件事）。**要重放一帧不落地推给所有客户端，
  那是另一个产品需求（多人同看一次对话）**，本项目不做。
- **代价：慢客户端会拖慢这一轮。** 应用层**刻意**用无缓冲通道转发（不让 128 个缓冲事件堆积），
  所以一个卡住的浏览器**会**让这一轮的帧积压在图上。诚实的说法是：**这是背压，不是 bug**；
  它的兜底是 `emit` 失败会**取消整轮**——只是"卡住但没断"的连接，代价是模型仍会在跑。
- **代价：同一份契约要在两处落地。** 服务端一份封闭集（常量 + `payload()` + 测试清单），
  客户端一份（联合类型 + reducer + `isStructuralEvent`），加上 19.3 的五跳里
  **两层各自的中立类型**。这份重复是**有意的**（每层都要有自己的真相），但它的守卫只能是
  **两边各自写的测试**——19.1 那次故障就是一次"缝隙没被测到"的真实账单。
  实践结论：**改一个事件名，要动的地方至少有六处**（Go 常量、`payload()`、`everyStreamEventType()`、
  TS 联合、reducer、`isStructuralEvent`）——**把它们列进 checklist，比指望记性可靠**。
- **验证台能写，所以它不属于运维台。** `/agent` 以人的身份走 `/v1`（能发消息、能确认、能改记忆），
  运维页读 `/admin/v1`。这条边界和第 4/第 18 章的身份假设是同一个：**`X-User-ID` 是占位身份**，
  没有 principal 可校验——**"loopback + 单用户"是有意的起点，不是安全方案**。
- **心跳是一种"没写进 schema 的契约"。** 15s 这个数字出现在服务端常量与前端注释里各一次，
  没有测试盯着它。**这是一种可接受的漂移风险**：心跳只影响"代理会不会误判"，
  不像 `message.delta` 那样会改变渲染结果——**但它提醒我们：不是所有契约都能被 schema 表达。**

---

## 19.8 课后实验

**跑（看真正的字节）：**

1. `make run-chat` 之后，用 `curl -N` 直接打这条流，把帧序打印出来：

   ```bash
   curl -N -s -X POST localhost:8080/v1/conversations/<thread_id>/messages \
     -H 'Content-Type: application/json' -H 'X-User-ID: demo-user' \
     -d '{"content":"曼哈顿中城有什么安静的日料"}'
   ```

   断言你**看得到**：`message.start` 是**第一帧**、`phase.started(ingress-1)` 紧跟其后、
   三个 `step.started/finished` 成对、每 3s 一帧 `phase.progress`、答案的
   `message.delta` 之后才有 `message.end`。**每一帧都应该是"立刻到达"，而不是最后一起到达。**
2. **制造一次断开**：在 1 的命令上按 `Ctrl-C`，然后看服务端日志——本轮应当**停**，而不是继续跑完。
   这正是 `TestClientDisconnectCancelsRun` 在测的东西。
3. 关掉 phase 帧再看一遍：`AGENT_PHASE_EVENTS=false` 重启，帧序**少掉整个 phase/step/进度段**，
   而**答案一字不差**。再关一次流式：`PLATEPILOT_ANSWER_STREAMING=false`，
   回答变成**一帧 `message.delta`**，且**本轮永远不会出现 `message.replace`**。
4. 打开浏览器 `/agent`（`make dev`），发一条会触发工具的消息，盯着**中列**与**右列**同时变化：
   中列是 `step-timeline` 的行在动，右列是 `tool-timeline` 的行在开与关；
   点答案里的 `[^n]`，看 `citation-drawer` **按需拉取那一份文档**。
5. 触发一次同名餐厅，让 `state.awaiting_input` 出现，看**状态栏**变成「等待你回答」、
   并且**给的是一个输入框而不是一个是/否**；再触发一次预约，看 `confirmation.required`
   让状态变成「等待你确认」。**这两条正是 19.4 里"两个等待事件必须分开"的人眼验证。**
6. 前端测试：`cd web && npm test`（vitest，22 个文件 124 个用例）。重点看三处：
   `lib/sse.test.ts`（切帧：**一个帧被拆成两次 `read()`、几个帧挤在一次 `read()`、
   最后没有空行收尾**这几种情况各断言一遍）、`api/chat.test.ts`（**唯一一个走真实解析链路的**用例，
   也是 19.1 那次静默故障留下的守卫）、`hooks/useAgentTurn.test.ts`（纯函数状态机，
   含 `tool.*` 按 `call_id` 配对、replace 整段替换、`interrupted`）。

**读：**

7. 读 [sse.go](../../chat-service/internal/httpapi/sse.go)：`heartbeatInterval`（为什么 15s）、
   `StreamEventType` 常量表（每条事件的注释都是设计说明）、`StreamEvent` 为什么"传输中立"、
   `payload()` 的 `default` 报错分支、`streamWriter`（立即 flush / `X-Accel-Buffering`）、
   `runHeartbeats`。
8. 读 [conversation_handler.go](../../chat-service/internal/httpapi/conversation_handler.go)：
   `SendMessageHandler`（开流前的线程存在性检查、`ctx.Err()` 那道判断）、`ChatService.SendMessage`
   的中文备注（emit 在调用方 goroutine 上、串行、按序）、路由注册顺序。
9. 读 [runner.go](../../chat-service/internal/agent/runner.go)：`eventBuffer = 128`、
   `emitter.send`（尊重 run context）、`Run` 与 `RunLive` 的分工、`RunLive` 里
   "出错仍继续抽干"的那段与 `cancel()`。
10. 读 [chatservice.go](../../chat-service/internal/app/chatservice.go)：`SendMessage`
    （为什么这里要**无缓冲**转发）、`toStreamEvent`（为什么两层只共享事件名而不共享类型）。
11. 读 [useAgentTurn.ts](../../web/src/hooks/useAgentTurn.ts)：`reduce`、`isStructuralEvent`、
    `flushNow`（50ms 与结构性短路）、`unresolvedCitations`；再读
    [sse.ts](../../web/src/lib/sse.ts) 与 [chat.ts](../../web/src/api/chat.ts) 的 `sendMessage`
    （帧缓冲、丢弃无名帧、把 `frame.event` 合回 `type`、不重试的理由）。
12. 读 [conversation_sse_test.go](../../chat-service/internal/httpapi/conversation_sse_test.go) 与
    [sse_event_test.go](../../chat-service/internal/httpapi/sse_event_test.go)：
    `TestSendMessageSSEEventSequence`（`Content-Type` 前缀 + 帧序 + payload 字段）、
    错误帧三兄弟（runtime / fallback / **遮蔽内情**）、
    `TestClientDisconnectCancelsRun`、`everyStreamEventType` 那条扫封闭集的测试。

**想（改配置 / 加断言）：**

13. 写一条测试：**断言"第一帧在 turn 结束之前到达"**。用脚本化 provider 制造一个慢 turn
    （比如工具轮之间 sleep），断言客户端**在服务端返回之前**已经读到 `message.start` 与
    `tool.start`——**这是把"实时"变成可断言属性的那条测试，也是最缺的一条。**
14. 把 `eventBuffer` 从 128 改到 2，跑一次长回答，观察**没有 `RunLive` 的并行抽干时**会发生什么；
    再改回来。你会亲手看到 19.3 那条注释说的是什么。
15. 加一个事件（比如 `tool.progress`）需要改哪几处？**照 19.7 的清单数一遍**，
    然后用 `sse_event_test.go` 与 `web/src/api/chat.test.ts` 验证你漏没漏。
16. 想一想：如果把 `AGENT_PHASE_EVENTS` 关掉，19.1 那个坏 case 会不会**重新出现**？
    它挡住的是"空白 spinner"，还是"整条流被丢弃"？——**这两个问题的答案不同，
    而它们正好区分了本章的两类失败：一类是协议没发，一类是客户端没读。**

---

## 19.9 参考代码与目录

**传输层（本章主体）**

- [sse.go](../../chat-service/internal/httpapi/sse.go)：`heartbeatInterval = 15s`（必须低于服务端每次写的
  30s 超时）、`StreamEventType` 常量表（含 `thinking.delta` / `phase.*` / `step.*` / `phase.progress`
  与它们的注释）、`StreamEvent`（传输中立形状；`Delta` 与 `ReplaceText` 为什么是两个字段）、
  每个事件的 payload 结构体（`sseReplaceData`、`sseAwaitingInputData`、`sseConfirmationData`、
  `sseMemorySavedData.refreshed` 为何省略、`ssePhaseProgressData` 为何最小）、
  `streamWriter`（`SetStatusCode(200)` / `X-Accel-Buffering: no` / 每帧 flush）、
  `payload()`（`default` 分支返回 `errUnknownStreamEvent`）、`runHeartbeats`。
- [conversation_handler.go](../../chat-service/internal/httpapi/conversation_handler.go)：
  `HeaderUserID = "X-User-ID"`、`maxMessageRunes = 4000`、`defaultHistoryPageSize = 20` /
  `maxHistoryPageSize = 100`、`ChatService` 端口（`SendMessage` 的 emit 契约：
  **串行、按事件顺序、在调用方 goroutine 上**）、`registerChatRoutes`、`SendMessageHandler`
  （开流前的线程存在性检查 → JSON 404；`close(stopHeartbeat)`；`emitErr != nil && ctx.Err() == nil`
  才发 error 帧）、`sendMessageRequest.Validate`。
- [chatservice.go](../../chat-service/internal/app/chatservice.go)：`SendMessage`
  （`RunLive` + **无缓冲**转发：*a slow SSE client throttles the run instead of letting
  128 buffered events pile up*）、`toStreamEvent`（*share event-name strings by contract,
  but never share a type*）。

**运行时（帧从哪来）**

- [runner.go](../../chat-service/internal/agent/runner.go)：`eventBuffer = 128`、`emitter`
  （`send` 尊重 run context）、`Run`（批接口：跑完才给通道）、`RunLive`
  （流接口：逐帧回调；**与图并行抽干**；`onEvent` 出错 → `cancel()`；出错后仍继续抽干）。
- [nodes.go](../../chat-service/internal/agent/nodes.go)：`ingress`（`message.start` 由 `invoke()`
  在任何节点之前发出，否则与 `phase.started` 抢跑）、三个 `emitStep` 子动作与中文标题、
  工具轮的并发与 `tool.finish` 为什么按完成顺序发出。

**前端验证台**

- [AgentConsole.tsx](../../web/src/pages/AgentConsole.tsx)：三列一个想法、`withoutEchoedTurn`
  （一轮结束后为何要丢掉流里的回声以对齐 transcript）、`tabTools` / `tabCitations` / `tabUtilities`。
- [useAgentTurn.ts](../../web/src/hooks/useAgentTurn.ts)：`TurnPhase`（含 `interrupted`）、
  `reduce`（纯函数；`tool.*` 按 `call_id` 配对；`message.replace` 是整段；`message.end` 之后
  等待态压过 `done`；`message.start` 不重置 `phases`）、`isStructuralEvent`（封闭清单）、
  `flushNow`（50ms 批量 + 结构性短路）、`unresolvedCitations`。
- [sse.ts](../../web/src/lib/sse.ts)：为什么不用 `EventSource`（POST + 身份头 + 需要可取消）、
  `readSSEStream`（按 `\n\n` 切帧 + 残余缓冲 + 收尾补帧）、`emitFrame`（丢弃无名帧 ⇒ 心跳的去处）。
- [chat.ts](../../web/src/api/chat.ts)：`StreamEvent` 联合类型（"不认识的名字必须忽略"）、
  `sendMessage`（`AbortController`、`X-User-ID`、开流前的 JSON 信封、非 JSON 帧跳过但不终止本轮、
  把 `frame.event` 合回 `type`、**刻意不重试**的理由）。
- 组件：[turn-status-bar.tsx](../../web/src/components/agent/turn-status-bar.tsx)（状态标签映射）、
  [step-timeline.tsx](../../web/src/components/agent/step-timeline.tsx)、
  [tool-timeline.tsx](../../web/src/components/agent/tool-timeline.tsx)（同一列表渲染两遍）、
  [citation-drawer.tsx](../../web/src/components/agent/citation-drawer.tsx)（按需取一份文档）、
  [candidate-cards.tsx](../../web/src/components/agent/candidate-cards.tsx)、
  [memory-panel.tsx](../../web/src/components/agent/memory-panel.tsx)、
  [gate-banner.tsx](../../web/src/components/agent/gate-banner.tsx)、
  [run-replay-panel.tsx](../../web/src/components/agent/run-replay-panel.tsx)。
- 路由：[App.tsx](../../web/src/App.tsx)（`/agent` 与运维页分成两组）。

**配置、文档与测试**

- [config.go](../../chat-service/internal/config/config.go)：`defaultAgentPhaseEvents = true`、
  `AgentConfig.PhaseEvents`（`AGENT_PHASE_EVENTS`）、`AnswerStreaming`
  （`PLATEPILOT_ANSWER_STREAMING`）。
- [.env.example](../../.env.example)：`# Publishes phase.started / phase.finished (and the three
  ingress sub-steps)`、`# AGENT_PHASE_EVENTS=true`。
- [README.md](../../README.md)：`### The turn's event stream` 小节（事件表、"Text is provisional
  until `message.end`"、`PLATEPILOT_ANSWER_STREAMING=false` 的降级行为、**帧随图产生而转发**、
  `Run` / `RunLive` 与 128 帧缓冲）。
- [platepilot-m4-task-document.md](../../docs/platepilot-m4-task-document.md)：§附录 E.1
  （新增 `phase.progress` / `thinking.delta` 两类帧）、E.2（`progressHeartbeatInterval = 3s`
  与两个开关的偏差记录）、**E.3（2026-10-06 前端把整个流丢掉的真实故障：现象 / 根因 / 为什么测试
  没拦住 / 修复与守卫）**、E.4（帧序实测与 `vitest` 22 文件 124 用例全绿的验证记录）。
- 测试：[conversation_sse_test.go](../../chat-service/internal/httpapi/conversation_sse_test.go)
  （`TestSendMessageSSEEventSequence`、`TestSendMessageRejectsInvalidContent`、
  `TestSendMessageRuntimeErrorEvent`、`TestSendMessageFallbackErrorFrame`、
  `TestSendMessageInternalErrorFrameHidesDetails`、`TestClientDisconnectCancelsRun`、
  `TestCORSPreflightOnSSERouteAllowsUserHeader`）、
  [sse_event_test.go](../../chat-service/internal/httpapi/sse_event_test.go)（`everyStreamEventType`
  的封闭集扫描、replace 帧不得带 `delta`、`memory.saved` 的 `refreshed` 省略规则、phase/step/progress
  的字段形状）、[nodes_instrument_test.go](../../chat-service/internal/agent/nodes_instrument_test.go)
  （`instrumented` 的 start/finish 成对、出错也要 close、开关关掉不发光）、
  [web/src/api/chat.test.ts](../../web/src/api/chat.test.ts)（走真实解析链路的守卫）、
  [web/src/hooks/useAgentTurn.test.ts](../../web/src/hooks/useAgentTurn.test.ts)、
  [web/src/pages/AgentConsole.test.tsx](../../web/src/pages/AgentConsole.test.tsx)。

这一章把事件**送到了用户眼前**，也顺手留下了一个新问题：这条流讲的是"**正在发生**"，
可它**随连接而死**——一轮对话结束、页面关掉、进程重启之后，那 128 帧里发生过的每一件事
还留在哪里？`phase.started` 说了 `plan` 花了 22 秒，可**这个数字没人保存**；
`tool.finish` 说了延迟 83ms，可**这一行没有落库**。下一章就接手这件事：
把"一次 turn 发生过什么"变成**可查询的审计**——run 一行、工具调用一行、每个图节点一段 span，
以及为什么审计的写入**失败也不能拖垮一次对话**。

> 下一章：[第 20 章 Agent 可观测性：run / tool / node 三级审计](20-agent-可观测性-run-tool-node-三级审计.md)
