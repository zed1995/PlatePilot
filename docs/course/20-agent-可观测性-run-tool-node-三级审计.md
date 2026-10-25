# 第 20 章 Agent 可观测性：run / tool / node 三级审计

> 上一章：[第 19 章 SSE 与验证台：把一个 turn 实时送到浏览器](19-sse-与验证台-把一个-turn-实时送到浏览器.md) ｜ 下一章：[第 21 章 怎么"考试"一个 Agent：四道安全门禁](21-怎么考试一个-agent-四道安全门禁.md)
>
> 第 19 章把一次 turn 的事件送到了用户眼前，也在结尾留下了一个尖锐的问题：那条流**随连接
> 而死**。`phase.started` 说 `plan` 花了 22 秒，可这个数字**没人保存**；`tool.finish` 说延迟
> 83 ms，可这一行**没有落库**。用户第二天说"昨天那轮特别慢"，或者"刚才那轮失败了"，
> 你手上什么都没有。这一章讲的就是补上这件事的三级审计：**一个 turn 一行 run、一次工具调用
> 一行、每个图节点一段 span**；以及一条容易被忽略的纪律——**审计是旁路，它失败了也不能
> 拖垮用户的那一次对话**。
> 真实文件与函数集中在文末 [§20.10 参考代码与目录](#2010-参考代码与目录)。

---

## 20.1 坏 case 开场：一个失败，两种写法

先看一个真实存在过的、非常安静的坏 case。

一个 turn 失败了。它可能是 `plan` 里模型返回了无法解析的东西，也可能是 `answer` 里引用校验
失败到无法收敛。**在审计表里，这两件事长得一模一样。** 这不是我推的，是 `run_nodes` 那张表的
迁移注释开头的原话（逐字）：

> agent_runs records the turn and tool_calls records each tool invocation. Neither can answer
> "which step failed", because the steps that call no tool — the planning round and the answer
> composition, which are the two that call a model — leave no tool_calls row at all. A turn that
> failed inside answer looked, in the audit trail, exactly like one that failed inside plan.

——**"一次在 `answer` 里失败的 turn，在审计轨迹里，和一次在 `plan` 里失败的 turn 完全一样。"**

为什么？因为**恰好是那两个"要调用模型"的步骤，不调用任何工具**——它们不留工具调用行。
所以"每 turn 一行 + 每工具一行"这套两级结构，有一个**结构性盲区**：**它只能看见有工具的地方。**
而失败最常发生、耗时最长、最贵的地方，恰恰是**没有工具的地方**。

后果很具体：`Node` 那条审计注释里有一句描述症状的话（逐字）：

> the answer hangs inside plan and the only surviving row is the run start, which is exactly the
> "the turn failed after 30 seconds" this table exists to replace.

——**"答案挂在 plan 里面，唯一活下来的行是 run start——而这正是 `run_nodes` 这张表要取代的
那句'那一轮跑了 30 秒然后失败了'。"**

### 第二个坏 case：双方都安静

第二个坏 case 更阴，因为它**不发生在一次 turn 里，而发生在启动时**。README 里那一段（逐字）：

> A startup failure rather than a warning, because a mismatched schema is quiet from both sides:
> the agent's audit path swallows write errors by design — a lost tool-call row does not fail a
> turn — and the read path reports the same mistake as a generic upstream error. Before this check,
> a database two migrations behind answered `GET /v1/runs/{id}` with `502 provider_unavailable`,
> which describes an outage for a database that was reachable and answering every other query.

**"一个落后两个迁移的库，让 `GET /v1/runs/{id}` 回 `502 provider_unavailable`——而那描述的
是一场'服务不可用'，实际发生的却是一个**连得上、并且在正常回答其它所有查询**的数据库。"**

两件事撞在一起才致命：

- **写侧吞错**（审计写失败不报错，见 20.5）——所以"写不进去"没有任何声音；
- **读侧把缺表映射成通用上游错误**——所以"读不出来"听起来像"供应商挂了"。

**两边都安静，排查方向就被指向了错误的地方。** 这一章因此有两个题目：**怎么把一次 turn 拆成
可查询的证据**，以及**怎么让"证据链断了"这件事必须吵起来**。

---

## 20.2 三级模型：三种问题，三种粒度

排查一个 Agent 系统时，人问的问题不是一个，而是三个层次的问题：

| 问的问题 | 需要的粒度 | 谁需要 |
|---|---|---|
| "**哪一轮**出问题了？它多久、成没成、花了多少 token、检索几次？" | turn 级（一行 run） | 用户投诉的第一步、值班 |
| "这一轮里**哪个工具**被调了？参数是什么、返回了什么、多久、成功没？" | tool 级（一次调用一行） | 判断是"模型选错了工具"还是"工具自己坏了" |
| "这一轮**卡在哪一步**？是计划慢、还是生成慢？" | node 级（每个节点一段 span） | 定位根因，也是唯一能覆盖"不调工具的步骤"的粒度 |

三级不是"多记一点"的递进，而是**三个互不替代的问题**。记住第 20.1 那个盲区：
**tool 级覆盖不了 node 级**——因为 `plan` 和 `answer` 不调用工具；**run 级也覆盖不了 node 级**——
因为"跑了 30 秒然后失败"不是一个可行动的结论。

### 三级之外还有两个名字：run_id 与 trace_id

**一个 run 是一轮对话（一次 turn）**，它有一个 `run_id`。但**用户的一次请求**和"一轮 turn"
并不总是同一个东西：用户在等待确认之后点"确认"，或者在澄清之后补一句话，那是**同一件用户可见的事**
跨了两次执行。于是还有一个 `trace_id`：

> a trace_id names one user-visible request end to end, and M6-01's acceptance is that any
> failure can be located from it.

**"一个 `trace_id` 端到端地命名一次用户可见的请求"**——它的验收标准是**任何失败都能从它被定位到**。
`agent_runs.trace_id` 上有**唯一约束**，意思是：**一个 trace 对应一行权威的 run 记录。**

这就是为什么支持工单里只需要客户给一个 `trace_id`：**它是"用户看到的那个东西"与"系统内部发生过的
一切"之间唯一的接口。**

### 状态机：为什么 `running` 必须存在

run 的状态集合是四值的（`running` / `succeeded` / `failed` / `cancelled`），数据库的 CHECK 也照着
四值约束。其中**只有 `running` 是非终态**，而它必须存在，因为**"行被写下了、但没有被收尾"是一个
真实的结局**：进程在 turn 中间被杀、审计写失败、机器断电。**一个只有终态的表，表达不了"有事情
开始过、但我们不知道它怎么结束"**——而排查最需要的恰恰是这一条。

`cancelled` 则是为"用户主动停掉这一轮"这个真实结局预留的取值（第 19 章的 Stop 按钮那条链路）。

---

## 20.3 三级模型落在库里：三张表与它们的索引

三级模型在数据库里就是三张表。它们的建表注释本身就是这一章的提纲：

```sql
-- M4-07 agent run and tool call audit.
--
-- These tables are written by the audit side path: one row per agent run and
-- one row per tool invocation. They are operational evidence, never read on
-- the answer path, so their indexes target the operational queries (a
-- thread's recent runs, a run's tool calls) rather than analytics.
```

**"它们是被审计旁路写下的运维证据，从不在答案路径上被读；所以索引瞄准的是运维查询，而不是分析。"**
这句话里有两个设计决定，都值得展开。

### ① 索引为**已知的四条读法**而建，不为"以后可能会查"而建

| 表 | 索引 | 读法 |
|---|---|---|
| `agent_runs` | `(thread_id, started_at DESC)` | "这个会话最近几轮" —— 审计的**主查询** |
| `agent_runs` | `(started_at DESC)` | 跨会话的时间窗扫描（"最近一小时失败的 turn"） |
| `tool_calls` | `(run_id, seq)` **UNIQUE** | "这一轮按请求顺序调了哪些工具" |
| `run_nodes` | `(run_id, seq)` **UNIQUE** | "这一轮的时间线" |

注意 `tool_calls` 上那条**唯一**索引的理由，它其实是一句断言：

> The index is unique because a run's tool calls have one position each; a duplicate would be a
> bug in the caller rather than a row to keep.

——**"一轮里的每个工具调用只有一个位置；出现重复是调用方的 bug，而不是一条要留下的行。"**
**把不变量写进索引，是把"本来应该由代码保证的事"交给数据库保证**——这一点第 18 章的
幂等唯一索引是同一个手法的第三次出现（第 5 章评论导入、第 18 章预约，这里是审计）。

顺带一条迁移卫生的例子：当 `seq` 引入之后，那条按"完成时刻"排序的旧索引被**显式删除**——

> keeping it would leave an index that no query wants and a reader wondering which of the two
> orders is the real one.

**"留着它会留下一个没有查询想要的索引，和一个'到底哪个顺序才算数'的疑问。"**

### ② 表结构里的每一列，都在回答一个问题

`agent_runs` 上除身份与状态外，还有：`model_provider` / `model_name`（**哪次配置跑出来的**）、
`latency_ms`、`token_input` / `token_output`、`retrieval_count` / `tool_call_count`、
`error_code`（+ 后补的 `error_message`）。这几列不是"顺手记一下"：

- `model_provider` / `model_name` 让**同一段时间的两次模型配置可对照**——第 21 章的两套配置逐 case
  diff、第 22 章的成本账，用的都是这一列；
- `retrieval_count` 与 `tool_call_count` 分开，是因为**检索是"这一轮烧了多少向量/IO"的代理指标**，
  而且它是由**工具名**推出来的（只有那两个检索工具算检索）；
- `error_code` 是**分类**，`error_message` 是**能动手改的东西**（20.6 展开）。

`tool_calls` 上最需要留心的一列是 `arguments`：

```sql
-- Redacted argument digest only: whitelisted business fields, truncated.
-- Contact details and free-form identifiers must never reach this column.
arguments jsonb NOT NULL DEFAULT '{}'::jsonb,
```

**"只存脱敏后的参数摘要：白名单里的业务字段、并且截断。联系方式与自由形式的标识符
绝不允许进入这一列。"** ——列名还是一列，语义已经是**两种**：可记录的与永不记录的。20.6 讲怎么落地它。

`run_nodes` 上最有设计感的一列是 `detail`：

```sql
-- Node-specific facts written by the node itself: candidate counts, tool
-- names, finish reason. Never read on the answer path, and never a place
-- for anything the caller sent.
detail jsonb NOT NULL DEFAULT '{}'::jsonb,
```

——**"由节点自己写下的、属于该节点的事实……绝不放任何调用方传来的东西。"**
`detail` 是一个**开放字段**，开放字段最容易变成垃圾场；这条注释的作用就是给它立一条
**写入资格**：**写它的必须是那个节点，内容必须是"这个节点自己的事实"**（候选数、工具名、结束原因）。

### ③ 三张表都在运行时表里，与内容表不重叠

`agent_runs` / `tool_calls` / `run_nodes` 属于 **`chat-service` 的运行时表集**，由在线服务写；
`restaurants` / `reviews` / `knowledge_documents` 属于**内容表集**，只有 `data-pipeline` 写。
这条"两边表集不重叠、谁也不写对方的表"的分工，是审计能成立的前提之一：
**排查证据与语料是两种东西，混在一起会让重导语料这件事变得危险。**

---

## 20.4 顺序：`seq` 与 `started_at`，一个被测量出来的设计

三级审计里最难的一处不显眼、但极其重要：**这些行按什么顺序读回来。**

### 问题：并发工具轮把"请求顺序"弄丢了

第 14 章讲过，一轮里最多 3 个只读工具**并发**跑。于是——

- **行是在每次调用返回时写的**（写的时刻就是它返回的时刻）；
- 所以**插入顺序 = 完成顺序**，而完成顺序**不代表请求顺序**：先被要求调用的可能最后才返回；
- 更糟的是，**`created_at` 是完成时刻**，它对"请求顺序"什么也没说。

但读的人要的是**请求顺序**："这一轮模型先问了这个、再问了那个"——因为那才是**模型的意图**，
也是判断"工具选得对不对"的依据。于是需要**另一个由事实决定的键**。

### 为什么是 `seq` 而不是 `started_at`：87%

最自然的选择是给每次调用记一个"开始时间"，然后按它排序。**项目试过，并且测量了，然后放弃了这个方案**
——迁移注释里原文（逐字）：

> seq rather than started_at, and this was measured rather than assumed: the two calls of one
> round are launched a few hundred nanoseconds apart, while started_at is a wall clock stored at
> microsecond resolution. A probe that reproduced the launch loop found the two timestamps equal
> **87% of the time**, so an order derived from them would be a coin toss with a tiebreak, and it
> would differ between runs of the same turn.

——**"一轮里的两次调用相隔几百纳秒被启动，而 `started_at` 是一个微秒分辨率的墙上时钟；
一个复现启动循环的探针发现，这两个时间戳有 87% 的时候完全相同。"**

**"一次投硬币加上一个平局决胜"，而且"同一轮 turn 的两次运行会得到不同的顺序"。**

这段话里有三个极好的工程习惯，值得逐条抄下来：

1. **"measured rather than assumed"**——顺序键的选择**不是一个审美问题，是一个可测量的事实**。
   项目写了一个复现启动循环的探针，量出了 87%，然后**据此改变设计**。第 22 章会看到同一习惯
   在性能上的用法。
2. **分辨率的边界是可计算的**：启动间隔（几百纳秒）与时钟分辨率（微秒）**差一个数量级以上**，
   所以"两个时间戳相等"不是偶发，而是**常态**。任何"靠时间戳区分同时发生的事"的设计，
   只要落在分辨率以下，就**注定**退化成随机。
3. **确定性是显式要求**：`"an order that depends on clock resolution is an order that differs
   between runs"`——这正是第 6 章"检索排序必须可复现"那条纪律的**同一句话**，
   只不过这次的对象是审计行的顺序。**"同一次 turn 跑两遍，读出来的时间线必须一样。"**

### `seq` 由**调用者**提供（两条理由，都很具体）

`seq` 不是仓储层数出来的，是**调用方（发起循环）**传进来的。`ToolEvent.Seq` 的注释解释了为什么
（逐字）：

> It comes from the caller because the caller is the only thing that still knows that order once
> the calls are running at the same time, and it is what the read path orders by: rows are written
> as each call returns, so neither insert order nor a timestamp can recover the request order.

**"它来自调用者，因为当这些调用同时跑起来的时候，调用者是唯一还知道那个顺序的东西。"**
再加一层：**编号必须在启动循环里分配，不能在 goroutine 内部自增。** 测试里的注释警告得非常具体：

> a counter incremented inside the goroutines would be handed out in completion order, which
> would read back as c2, c1.

——**"一个在 goroutine 内部自增的计数器会按完成顺序发号，读回来就成了 c2、c1。"**

同一份理由以另一种形式出现在 **node span** 上，而且那里的坑更隐蔽（`NodeEvent.Seq` 注释，逐字）：

> It is supplied by the caller rather than counted here because the caller is the only thing that
> knows the run is still going: **the run row is finished inside the finalize node**, so a counter
> cleared when the run finished would number finalize's own span 1 and sort it ahead of the
> ingress it came after.

——**"run 行是在 `finalize` 节点里结束的"**，所以如果在"run 结束"时清空计数器，
**`finalize` 自己的 span 会拿到编号 1，然后排到它所跟随的 `ingress` 前面去。**
**这是一个"收尾动作也是序列的一部分"的经典陷阱**：`finalize` 既结束了 run，又是 run 的最后一个节点。

### `started_at` 仍然要留：它回答 `seq` 回答不了的问题

有两个键不是冗余，而是**分工**（逐字）：

> started_at is still added, because it answers the question seq cannot: when did each call
> actually begin. That is what makes an overlap visible in the trail, and what a latency without a
> start cannot tell you.

——**"`seq` 说顺序，`started_at` 说时刻；而只有时刻能让'重叠'在轨迹里变得可见。"**
`seq` 是 1、2、3，`started_at` 是三个可能**互相重叠**的区间——**后者才是"它们真的同时跑了"**
这句话的证据。第 14 章说并发是刻意的，**审计是它唯一能被证实的地方。**

### 迁移的诚实：旧行怎么办

引入 `seq` / `started_at` 时，表里已经有行。迁移没有装作它们不存在，而是**分开处理两件不同的事**：

- **开始时间可以反推**：`started_at = created_at - latency_ms`，
  注释说这就是 node span 用的同一套推导，**"这是这些行能诚实说出的最多的话"**；
- **请求顺序无法恢复**：旧行写入时读是顺序的，于是 `created_at` 当时确实是个忠实的代理，
  **按它编号是当时唯一可用的重建方式**——注释直接把"不可恢复"写在了迁移里，而不是假装有了 `seq`
  就等于一直有 `seq`。

同理，node span 自己的 `started_at` 也是**由结束时反推**的：记录 span 时用"现在 - 耗时"得到开始时刻。
**"审计行允许反推，但不允许编造"**——这是 20.5 里"没有编号的 span宁可丢掉"的同一条原则。

### 一般教训

- **排序键要由"事实"决定，而不是由"看起来更像真的"决定。** 时间戳看起来最像"真实时间"，
  但它在这里只是 87% 的噪声。
- **能被测量的事，不要靠直觉选。** 探针 + 一个百分比，换掉了一个会在半年后表现为"偶发乱序"的 bug。
- **同一个概念（顺序）需要两个键时，就把两个键都留下，并写清楚各自回答什么问题。**

---

## 20.5 旁路原则：审计失败绝不能拖垮 turn

三级审计是**旁路**。这不是一句口号，它是这个包的第一段话（逐字）：

> Package audit is the side path that records agent runs and tool calls.
>
> Audit writes never decide a turn's outcome: every method swallows repository errors and logs
> them, so an unavailable audit store degrades observability but never the user's answer.
> Arguments are whitelisted and truncated before they leave this package; the store only receives
> the redacted digest.

**"审计写从不决定一次 turn 的结果：每个方法都吞掉仓储错误并记日志，所以一个不可用的审计存储
会降低可观测性，但绝不会影响用户的回答。"**

代价必须同时说清楚——**"审计写失败会让证据链断开，而用户一无所知"**：这正是 20.1 第二个坏 case
的成因，也是"必须打日志"和"启动期 schema 自检"（20.8）这两件事存在的理由。

### 一：写不进去也可以，但要**分开说**，因为严重程度不同

包里的日志级别不是随手写的：

| 写入 | 失败时的日志 | 为什么是这个级别 |
|---|---|---|
| `RunStart`（run 行） | **Warn**，且注释说明后果 | 见下——这一行是所有后续行的前提 |
| `ToolCall` | Error | 一条工具证据缺失 |
| `Node` | Warn | 一段 span 缺失，时间线不完整 |
| `RunFinish` | Error | 终态缺失，run 行永远停在 `running` |

`RunStart` 那条注释是整章最有信息量的一处（逐字）：

> Explicit warning: without this row every later Finish/tool call audit is silently rejected by
> the foreign key.

——**"没有这一行，之后所有的 Finish 与工具调用审计都会被外键静默拒绝。"**
`tool_calls` / `run_nodes` 的 `run_id` 是 `REFERENCES agent_runs (run_id) ON DELETE CASCADE`，
**所以 run 行是整条证据链的锚**：`RunStart` 一次失败，后面每一行都会被数据库挡掉，
而按"吞错"原则那些挡掉**也不报错**。

**这就是为什么"吞错"必须配一个高优先级的、说明后果的日志**：吞错保证用户不被影响，
日志保证**系统内部不会安静**。而这个"锚行"的语义还有一层：
**先写 `running`，最后把它改成终态**——这是"证据链先立锚、后补细节"的标准做法。

### 二：写审计时**不要被请求的取消打断**

最难也最漂亮的一处是这一句（逐字）：

> auditWriteTimeout bounds one audit write. It runs detached from the request context: **a
> canceled turn must still leave a terminal run row.**

实现只有一行：

```go
// 用 WithoutCancel 把写入从请求的取消里摘出来，但仍然给它一个上界（5s）。
func auditCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), auditWriteTimeout)
}
```

**"一个被取消的 turn 仍然必须留下终态的 run 行。"** 想一想它挡掉的是什么 bug：用户按了
Stop（或者关掉了页面），服务端**取消**了这一轮——如果审计写用的是同一个 `ctx`，
那么这次取消会**连"这一轮被取消了"这件事本身也取消掉**，run 行永远停在 `running`。
**"用户不想要的那个结果"，和"这个结果应该被记下来"，是两件事。**

同时也要看到它的**边界**：`WithoutCancel` 只是不让取消传下去，**不是无限期**——
`auditWriteTimeout` 给了 5 秒上界。**一个已经离开的客户端不应该让一个写入无限期挂着。**

### 三：span **逐条写**，而不是攒到最后一起写

这是 node 级审计的核心决定（逐字）：

> Spans are written as they happen rather than buffered until finalize. The run that most needs a
> trace is the one that never reaches finalize, and a buffer flushed at the end would have nothing
> to say about it: the answer hangs inside plan and the only surviving row is the run start, which
> is exactly the "the turn failed after 30 seconds" this table exists to replace.

**"最需要 trace 的那次 run，恰恰是那个永远没走到 `finalize` 的 run。"**
所以"在最后统一 flush"这种在批量系统里天经地义的做法，在这里是**反的**：
**它的失败模式正好落在最需要证据的那一类运行上。**

**这条原则不止适用于 span**：它其实是所有"过程中诊断"的通用要求——
**诊断数据要在产生它的那一刻落盘，因为不可能到达收尾阶段的那种运行，才是诊断存在的理由。**

### 四：没有编号的 span，宁可丢掉

```go
if ev.Seq <= 0 {
	// A span with no position cannot be placed in the timeline, and the
	// store rejects it. Dropping it here with a warning is the only
	// honest outcome: inventing a position would put the node somewhere
	// it did not run.
	h.logger.Warn("audit: run node span has no sequence number; span dropped", ...)
	return
}
```

——**"发明一个位置，等于把这个节点放到它没有跑过的地方。"**
这个决定与 20.4 的"旧行只能反推、不能编造"是同一条：**审计可以缺，不可以假。**
**一份有 90% 真实行的轨迹，比一份 100% 但其中 10% 是猜的轨迹有用得多**——因为前者的缺口是**可见的**
（少了一个 `seq`），后者的错误是**不可见的**。

### 五：审计是可选依赖，`nil` 必须安全

`Hooks` 的每个方法都以同一个判断开头：

```go
if h == nil || h.repo == nil {
	return
}
```

——**审计不可用时，Agent 照跑**。装配层也照着这个前提工作：配置了 run 仓储才构造 `Hooks`，
否则**明确记一条警告**（"agent turns run without audit trail"）。
测试把这两件事都钉住了：`TestRunnerAuditStoreFailureNeverBreaksTurn`（**仓储全线失败，
turn 仍然成功**）与 `TestRunnerWithoutAuditorStillRuns`（**根本没有 auditor，turn 照样跑**）。

这里还有一条**测试写法上的**教训，来自那个 fake 的注释（逐字）：

> The read path satisfies the port without being exercised here: a fake that implemented only the
> writes would stop compiling the moment the interface gained a read method, which is exactly the
> signal these tests want.

**"一个只实现写方法的 fake，会在接口新增读方法的那一刻编译失败——而这正是这些测试想要的信号。"**
`store.RunRepository` 是一个**完整端口**（写 + 读），fake 也只实现完整端口，
于是**端口一旦长大，测试会以"编译不过"的方式提醒你**——而不是安静地只测一半。

### 六：内存里的那两个计数器

`tool_call_count` 与 `retrieval_count` 不是从工具表里数出来的，而是**在 `Hooks` 里就地累加**
（一把互斥锁 + 一个 `map[runID]*counters`），在 `RunFinish` 时取走并清掉。
为什么不直接 `SELECT count(*)`？因为**答案路径上多一条查询不值当**，而这两个数字
**本来就是"这一轮发生过几次"的增量事实**——谁看见增量，谁记账。
`retrieval_count` 的定义也很具体：**只有那两个检索工具算检索**。

---

## 20.6 写进去的东西必须能公开：白名单、截断，与"审计不是档案"

审计表是**会被运维读的**，所以它天然是一份**新的隐私面**。项目在这里的做法可以概括成一句话：
**只存"业务事实"，绝不存"用户数据"。**

### 参数：白名单，而不是黑名单

```go
// argumentAllowlist is the only set of keys the audit stores per tool.
// Everything else — including any contact field a future tool might accept —
// is dropped by default.
```

每个工具一张**允许键的集合**（`query`、`borough`、`cuisine`、`min_rating`、`open_now`、
`top_k`；另一个工具是 `restaurant_ids`、`query`、`topic`、`doc_types`、`top_k`），
其余**一律丢弃**；未知工具、空参数、坏 JSON 一律折叠成 `{}`。

**注意"包括未来某个工具可能接受的联系方式字段"这句话的分量**：它选了白名单，因为
**黑名单的失败方式是"漏了一个"**：新工具带进来一个新参数，黑名单不知道它，于是它被写进库。
**白名单的失败方式是"少记一个"**——而少记一个业务字段，代价是排查时少一点上下文；
**多记一个手机号，代价是一次数据事故。两种失败的代价不对称，所以默认要往安全的那边倒。**

### 三个截断常量，三种理由

| 常量 | 值 | 理由（来自注释） |
|---|---|---|
| `maxArgumentChars` | 256 | 约束**一个**白名单字符串参数值 |
| `maxSummaryChars` | 512 | 约束工具结果摘要 |
| `maxMessageChars` | 512 | 约束失败消息——**"一个上游错误可以非常长（网关有时会回一整页 HTML），而审计行是诊断，不是档案，所以宁可截断也不整段写"** |

截断本身是按 **rune** 截的（而不是字节），并在末尾补一个省略号：
**"在最后一个完整字符之前切断"**——一个把中文截成半个字符的日志，比截断本身更难读。

### `error_message`：分类不够用，但也不许变成备份

第 20.3 提到 `error_code` 是分类。这一列是后来**专门补的**，理由写得很清楚（逐字）：

> A code is a classification, and it is not enough to act on: a mistyped model id, a quota refusal,
> and a region block can all surface as provider_unavailable, while the one thing that says which
> it was — the provider's own message, naming the model and the endpoint — was discarded the moment
> the turn failed.

——**"一个写错的 model id、一次配额拒绝、一次区域封禁，都可能表现为 `provider_unavailable`；
而唯一能说明到底是哪一种的东西——供应商自己的消息，里面点名了 model 与 endpoint——
在 turn 失败的那一刻被丢掉了。"**

同一段注释也把它**不**要变成什么说清了：

> The column is written only on the failure path and never read to build an answer, so it is not
> indexed. It is NOT NULL with an empty default so every read can scan it directly; an absent
> message is the empty string, not NULL.

三个细节都值得学：**只在失败路径写**（所以不索引——它不参与任何查询模式）、
**`NOT NULL` + 空默认值**（读的人不必处理 NULL 分支，"没有消息"就是空串）、
**不索引因为不用于筛选**。**"一列要么服务一个查询，要么服务一次事后阅读；两者都没有的列不该存在。"**

### 一句总结

**审计的容量是有限的，所以要为"最重要的那部分"留空间**：参数要**脱敏**、消息要**截断**、
`detail` 要**限定写入资格**。这条纪律和整门课的另外两处呼应：
第 9 章语料的脱敏（对外只暴露该暴露的）、第 18 章预约的幂等键（只留业务键，不留完整请求）。
**"能记下来"从来不等于"该记下来"**——**可观测性不是免费的，它买的是一份新的泄露面。**

---

## 20.7 查询面，与一次完整的排查演练

### 四条只读路由

| 路由 | 回答 |
|---|---|
| `GET /v1/conversations/:id/runs` | 这个会话最近的 runs（分页：默认 20、上限 100） |
| `GET /v1/runs/:run_id` | 一轮的**全貌**：run 行 + 工具调用 + 节点时间线（扁平化返回） |
| `GET /v1/runs/:run_id/nodes` | 只要时间线 |
| `GET /v1/traces/:trace_id` | **按 trace 反查**：给一个用户可见请求的名字，换回与上一条相同的结构 |

`GET /v1/runs/:run_id` 返回的字段是三级审计的简历：
`run_id` / `thread_id` / `trace_id` / `status` / `latency_ms` / `tool_call_count` /
`error_code` / `error_message`，加上 `tool_calls`（每条含 `call_id`、`seq`、`tool_name`、
`status`、`latency_ms`、脱敏后的 `arguments`、`result_summary`）与 `nodes`
（每条含 `node_id`、`node`、`seq`、`status`、`started_at`、`latency_ms`、`detail`、`error_code`）。

**"trace 反查"与"run 直查"返回**同一个形状**是刻意的**：支持工程师与开发者拿到的是同一份结构，
**所以两种入口不会互相教导出两套不同的直觉。**

### 一次坏 turn 的排查演练

三级审计存在的唯一理由，就是下面这条路径能走通。把它当成本章的"综合练习"：

**第 0 步：用户说"刚才那一轮卡住了"。** 你有一个 `thread_id`（前端页面上就有），
或者更好——一个 `trace_id`。

**第 1 步：列出这个会话最近的 runs。** 用 `GET /v1/conversations/:id/runs`。
它命中 `(thread_id, started_at DESC)` 这条索引——**这就是"审计的主查询"那句话的落点**。
你看到一排 run，每个带 `status`、`latency_ms`、`tool_call_count`。**这一步回答的是"是哪一轮"。**

**第 2 步：找一个可疑的。** 一个 `status = failed` 的、或者 `latency_ms` 明显偏高的。
**这一步回答的是"哪一轮值得查"**——注意这里**不需要任何日志**。

**第 3 步：拉全貌，先看 `error_code` 与 `error_message`。** 用 `GET /v1/runs/:run_id`。
`error_code` 告诉你**哪一类**（`provider_unavailable` / `invalid_argument` / `internal` / …），
`error_message` 给你**能动手的东西**（是不是 model id 写错、是不是配额）。**这是 20.6 那一列存在的时刻。**

**第 4 步：看时间线，找到"哪一步"。** 用同一次响应里的 `nodes`（或 `GET /v1/runs/:run_id/nodes`），
**按 `seq` 排序**：`ingress → plan → tools → plan → answer → finalize` 各段 span，
每段带自己的 `latency_ms` 与 `status`。**这一步是"只有 node 级能回答"的那个问题**：
如果失败发生在 `answer`，你会看到 `answer` 那段 span 状态是 error，
而它**上面一段 `tools` 是完全正常的**——"工具没问题，是生成挂了"这个结论，
是**用时间线读出来的，不是猜出来的**。

`detail` 在这里是补充证据：某段 span 写下了"这一轮候选 8 个""这一轮工具 3 次""结束原因 stop"，
**它们让"这一段在干什么"从靠名字猜测变成有据可查**。

**第 5 步：看工具链，判断"模型选得对不对"。** 同一次响应里的 `tool_calls`，**按 `seq`**。
每条给你 `tool_name`、脱敏后的 `arguments`、`status`、`latency_ms`、`result_summary`。
两件只有在这一步能看见的事：

- **模型要它干什么**（`arguments` 是请求的摘要）；
- **它是不是并发跑的**——把几条的 `started_at` 与 `latency_ms` 摆在一起，
  **重叠是可见的**（20.4 留下的那个键在这里兑现）。

**第 6 步：落到服务器日志。** 审计表是**分类 + 截断消息**，**日志才是第一现场**。
两者的分工是确定的：**审计告诉你"去哪找"，日志告诉你"为什么"。**
一个被截断到 512 字符的供应商 HTML 页面，在日志里才有完整的一份。

### 前端：同一份数据，两种用法

验证台里的 `run-replay-panel` 是这条路线的图形版：

- 它列 runs：`run_id`、状态、模型名、开始时间、`latency_ms`、`tool_call_count`、`error_message`
  ——**第 1、2 步**；
- 选中一个 run 之后它拉工具链并渲染每条调用（序号、`tool_name`、`latency_ms`、状态、
  `result_summary`、**脱敏后的 `arguments`**）——**第 5 步**；
- 第 19 章讲过它有"渲染两遍"的性质：**直播时它只有"开始了/结束了"，回放时才有"返回了什么"**。
  **"第二遍严格更有信息量"**——这就是连接第 19 章与本章的那句话。

**注意前端展示的 `arguments` 已经是脱敏后的**：**脱敏发生在写入侧，不在展示侧**。
这条分工很关键——**只要有一个消费者忘了过滤，泄露就已经发生了**；
把脱敏放在**唯一的入口**（audit 包）里，才不需要相信每一个消费者。

---

## 20.8 让"证据链断了"必须吵起来：启动期的 schema 自检

回到 20.1 的第二个坏 case。它的修法不在审计里，而在**装配期**：**服务启动时核对数据库的迁移版本，
落后就拒绝启动。**

### 症状：一个会说谎的 502

> Before this check, a database two migrations behind answered `GET /v1/runs/{id}` with `502
> provider_unavailable`, which describes an outage for a database that was reachable and answering
> every other query.

**这条错误消息的每一句话都是错的**：它说"上游不可用"，而真实的数据库**连得上、并且正常回答
其它所有查询**。为什么它会错成这样？因为**两层各自的"合理行为"叠加**：

- **写侧**：审计包**按设计吞错**（20.5）——链路断了，但**没有任何一层会因此失败**；
- **读侧**：一个"表不存在/列不存在"的数据库错误，被映射成了**通用上游错误**
  （"provider 不可用"）——因为映射层只知道"数据库报错了"，不知道"库落后了"。

于是**错误的方向被指向了供应商**，而真正要做的事是**跑一次迁移**。

### 修法：拒绝启动 + 点名缺失的迁移

```
verify postgres schema: invalid_argument: postgres: database "platepilot" is behind
this binary: 2 migration(s) not applied (0011_memory_search.sql, 0012_tool_call_order.sql);
run `make migrate`
```

这段错误的三个部分都是刻意的：

1. **拒绝启动，而不是打一条警告**——README 的理由是"**双方都安静**"：
   一个 warning 会淹在启动日志里，而系统随后会以**误导性的方式**继续工作很久。
2. **点名缺失的迁移文件名**——不只说"落后了"，而是**告诉你跑什么**（还给了 `make migrate`）。
   **一条错误消息的可用性，等于它把人带到下一步的距离。**
3. **带上数据库名**——多环境（本地 / 测试 / 演示）时，"哪个库落后了"是你第一件要知道的事。

### 三条边界，一条比一条重要

- **这个检查只读。** 服务**不自己改 schema**；应用迁移永远是 `data-pipeline` 的职责。
  这条分工与"两个服务表集不重叠"是同一套纪律：**一个系统里，改变 schema 的权力只应该有一个行使者。**
- **它是单向的**：比较的是**迁移记账**（`schema_migrations`），不是 schema 本身。
  所以**一个超前于二进制的库**（回滚了代码但没回滚 schema）**能通过**，
  并且会在**第一次写入时**才暴露——README 把这条也写明了，因为**"检查的盲区"和"检查的内容"同样重要**。
- **错误映射要能区分"库落后"与"库挂了"**：`42P01` / `42703` 这类"对象不存在"的 PostgreSQL 错误码
  被映射成**"schema object does not exist; run migrate"**，而不是伪装成"数据库不可用"。
  ——**同一个"数据库报错"表面下，至少有两种完全不同的人类动作**：**跑迁移** 和 **修连接**。

### 一般教训

**"双方都安静的错误，必须被一个显式失败取代。"** 这里的三个条件同时成立时，就必须做启动期检查：

1. **写侧被设计成吞错**（旁路不能拖垮主路径）；
2. **读侧会把结构性缺失说成通用故障**；
3. **两者的失败都发生在"很久以后、以误导的方向"**。

**自检的位置也很关键**：放在**装配期**（进程启动，接受第一个请求之前），
而不是"第一次真的要用它的时候"。**一个用不上审计的部署，永远不会发现自己写不进去。**

---

## 20.9 课后实验

**跑（真的把三级审计用起来）：**

1. `make pg-up && make migrate && make run-chat`，然后**制造一次成功与一次失败**：
   发一条正常消息，再发一条必然触发上游错误的消息（把 chat provider 的模型名改错即可）。
   用 `curl` 拉这两个 run：

   ```bash
   curl -s localhost:8080/v1/conversations/$THREAD/runs
   curl -s localhost:8080/v1/runs/$RUN_ID | jq
   ```

   断言你能**只看这两条响应**（**不打开日志**）回答：status、latency_ms、tool_call_count、
   `error_code` 和 `error_message` 各是什么，以及**失败发生在一段名为 `answer` 的 span 上**。
2. **制造一个"早死"的 turn（`plan` 里挂掉）**，重复上一步。对比两次的 `nodes` 时间线：
   **你能只看节点名说出失败点不同——而这正是 `run_nodes` 这张表存在的理由**（20.1）。
3. **观察跳过的 span**：用一条会触发澄清的消息，让 `clarify` 上场；再用一条直接回答的消息，
   让 `clarify` 不出现。对比 `nodes` 的 `seq` 连续性——**"跳过"在时间线里长什么样**。
4. **观察 `seq` 与 `started_at`**：发一条会触发多工具并发检索的消息，看 `tool_calls` 里的
   `seq` 顺序与 `started_at` 的重叠。这正是 20.4 那个 87% 的决定在真实数据里的样子。
5. **验证旁路**：把数据库停掉（`make pg-down`，但服务连着），再发一条消息——
   **回答必须照常返回**，日志里出现审计写入失败的 warn/error，而**run 行没有**。
   然后读一读 README 那段"双方都安静"，确认你能说清"为什么这时它不报错，以及为什么启动时会"。
6. **验证 schema 自检**：造一个落后的库（删掉 `schema_migrations` 里最新的一两条记录），
   重启服务——**它必须拒绝启动**，并点名缺失的迁移文件。再把记录补回去。
7. 前端：打开 `/agent` 的 run 面板，走一遍 20.7 的六步——**从"哪一轮"到"哪一步"到"哪个工具"**。

**读：**

8. 读迁移：`0005_agent_runtime.sql`（三张表里的两张 + "从不在答案路径上被读"那句）、
   `0010_run_nodes.sql`（**node 级存在的理由**与 `seq` 作为排序键的注释）、
   `0012_tool_call_order.sql`（**87% 那段测量**、完成顺序与请求顺序的分叉、旧行的诚实反推）、
   `0013_run_error_message.sql`（分类不够用的那段论证）。**这四份迁移的注释本身就是本章的核心材料。**
9. 读 `chat-service/internal/agent/audit/hooks.go`：包注释（旁路原则）、
   `auditCtx`（`WithoutCancel` + 5s）、`RunStart` 的外键警告、`ToolEvent.Seq` 与 `NodeEvent.Seq`
   的两段理由、`Node` 的"逐条写而不是 flush"、`Seq <= 0` 的丢弃、
   `argumentAllowlist` / `RedactArguments` / `truncateRunes`。
10. 读装配与自检：`chat-service/internal/app/app.go`（构造 auditor 的那几行、
    run 读服务只在仓储齐备时挂载、**启动时 `VerifySchema` 的位置**）、
    `shared/store/postgres/migrate.go`（"迁移只属于 pipeline"）、
    `shared/store/postgres/client.go`（"对象不存在"到 `run migrate` 的映射）、
    `shared/store/postgres/schema_guard_test.go`（落后一个迁移必须报错，且错误里要有缺失版本、
    `make migrate` 与库名）。
11. 读仓储与契约：`shared/store/postgres/run_repository.go`（三张表的写路径与
    "按 `seq` 读回"）、`shared/store/contract/audit.go`（**一份契约、两个适配器**：
    工具调用必须挂在存在的 run 上、未知 run 返回 not found、空 `run_id` 与 `seq <= 0` 被拒绝）。
12. 读测试：`runner_audit_test.go`（`TestRunnerAuditStoreFailureNeverBreaksTurn`、
    `TestRunnerWithoutAuditorStillRuns`、`TestRunnerRecordsANodeSpanPerGraphNode`、
    "span 必须按执行顺序编号"、`TestRunnerRecordsTheSpanOfTheNodeThatFailed`）、
    `tools_parallel_test.go`（"在 goroutine 内部自增会读回 c2、c1"）。

**想（改一处，看懂整条链）：**

13. **给一个新节点加 span**：假设你要给 `clarify` 加一段自己的 `detail`（比如"问了哪个槽位"），
    需要动几处？（节点里写 `NodeEvent`、`seq` 的来源、`detail` 的写入资格、
    契约里要不要加 case、前端要不要展示）——**照 20.9 第 8 条列出的文件数一遍。**
14. **如果审计表突然写不进**（比如磁盘满），这次 turn 的用户体验应该变吗？
    如果答案不变，那"磁盘满"这件事应该在哪里被发现？——**回答这两个问题，
    就等于自己推导出了 20.1 与 20.8 的因果关系。**
15. **想要"跨 run 的对比"**（比如"这个模型配置的 p95 比上一版高多少"）：
    今天的索引（`(thread_id, started_at DESC)`、`(started_at DESC)`）够吗？
    ——注意第 20.3 那条设计声明：**索引为已知的读法而建**。
    要加哪一种读法，就得先有一个**真实的查询**，再决定加不加索引。

---

## 20.10 参考代码与目录

**领域与 schema**

- `shared/domain/run/run.go`：`Status`（`running` / `succeeded` / `failed` / `cancelled`）、
  `NodeStatus`（`ok` / `error`）、`AgentRun`、`ToolCallRecord`、`RunNode`。
- 迁移（均在 `shared/store/postgres/migrations/`，**执行迁移只属于 `data-pipeline migrate`**）：
  `0005_agent_runtime.sql`：`agent_runs`（trace 唯一约束、四值状态 CHECK、两条按时间的索引）
  与 `tool_calls`（`arguments` 的脱敏注释、`status` CHECK、run 外键级联）；
  顶部"运维证据、从不在答案路径上被读"的注释。
- 迁移 `0010_run_nodes.sql`：`run_nodes`（`detail` 的写入资格、`(run_id, seq)` 唯一索引、
  trace 索引），以及**"一次在 `answer` 里失败的 turn 和一次在 `plan` 里失败的一模一样"**那段。
- 迁移 `0012_tool_call_order.sql`：并发轮的完成顺序 vs 请求顺序、**87% 的测量**、
  `seq`/`started_at` 的分工、旧行的反推与"不可恢复"的诚实、旧索引被显式删除的理由。
- 迁移 `0013_run_error_message.sql`：`error_message` 的必要性（分类不够用）、
  "失败路径才写、不索引、`NOT NULL` + 空默认值"。

**写入侧（审计旁路）**

- `chat-service/internal/agent/audit/hooks.go`：包注释（吞错 + 白名单 + 截断的统一声明）、
  `maxArgumentChars = 256` / `maxSummaryChars = 512` / `maxMessageChars = 512` /
  `auditWriteTimeout = 5s`、`auditCtx`（`context.WithoutCancel` + 超时）、
  `RunStart`（外键警告）、`ToolEvent.Seq` 与 `StartedAt` 的注释、`ToolCall`、
  `NodeEvent.Seq`（`finalize` 与计数器的坑）、`Node`（逐条写、`Seq <= 0` 丢弃）、
  `RunFinish` / `Fail`（`fail` 是"没走到 finalize"的对应物）、计数器与 `isRetrievalTool`、
  `argumentAllowlist` / `RedactArguments` / `truncateRunes`。
- `chat-service/internal/agent/runner.go`：turn 开始 `RunStart`、失败路径 `Fail`、
  `Auditor` 作为 `Deps` 里的可选依赖。
- `chat-service/internal/agent/nodes.go`：工具返回时写 `ToolCall`（**`seq` 由发起循环给出**）、
  `finalize` 里写 `RunFinish`、五个图节点的 span 名
  （`ingress` / `plan` / `tools` / `clarify` / `answer` / `finalize`）。

**读写仓储与契约**

- `shared/store/postgres/run_repository.go`：`Start` / `Finish` / `RecordToolCall` / `RecordNode`
  的 SQL；读路径按 `seq` 排序（"并发轮的完成顺序与 `seq` 排序的意义"）、
  `ListRuns` / `ListNodes` / 按 trace 解析 run。
- `shared/store/memory`：同端口的离线实现（默认测试不需要数据库）。
- `shared/store/contract/audit.go`：**一份契约、两个适配器**——running run 的生命周期、
  工具调用必须挂在存在的 run 上、未知 run → not found、空 `run_id` 与 `seq <= 0` 被拒绝、
  node span 的写入/读取/排序。

**查询面与装配**

- `chat-service/internal/httpapi/run_handler.go`：`defaultRunPageSize = 20` /
  `maxRunPageSize = 100`、`RunView` 与 tool / node 的 JSON 字段、
  `GET /v1/conversations/:id/runs`、`GET /v1/runs/:run_id`、`GET /v1/runs/:run_id/nodes`、
  `GET /v1/traces/:trace_id`；run 详情把 run + 工具链 + 时间线**扁平化**返回。
- `chat-service/internal/app/app.go`：配置了 run 仓储才构造 `audit.New`（否则记
  "agent turns run without audit trail"）、run 读服务只在仓储齐备时挂载、
  **启动时 `VerifySchema`**。
- `shared/store/postgres/migrate.go`：`PendingMigrations` ——**迁移只由 pipeline 负责，
  服务进程不悄悄执行 DDL**。
- `shared/store/postgres/client.go`：`42P01` / `42703` → "schema object does not exist; run migrate"。

**测试与文档**

- `chat-service/internal/agent/runner_audit_test.go`：`TestRunnerAuditsSuccessfulToolTurn`、
  `TestRunnerMintsTraceAndRunIDs`、`TestRunnerAuditsFailedTurn`、
  **`TestRunnerAuditStoreFailureNeverBreaksTurn`**、**`TestRunnerWithoutAuditorStillRuns`**、
  `TestRunnerRecordsANodeSpanPerGraphNode`（含"span 必须按执行顺序编号"）、
  `TestRunnerRecordsTheSpanOfTheNodeThatFailed`。
- `chat-service/internal/agent/tools_parallel_test.go`：并发轮的 `seq` 只能来自启动循环
  （"goroutine 内部自增会读回 c2、c1"）。
- `shared/store/postgres/schema_guard_test.go`：落后一个迁移必须报错，错误里要有缺失版本、
  `make migrate` 与数据库名；"库落后于代码"用删 `schema_migrations` 记录来复现。
- `chat-service/internal/httpapi/run_handler_test.go`：`TestGetRunReturnsTheToolChainInOrder`、
  `TestGetRunReturnsTheNodeTimelineInOrder`、`TestListRunNodesReturnsOnlyTheTimeline`、
  `TestGetTraceResolvesTheRunAndItsTimeline`、`TestListRunsAppliesThePagingDefaults`、
  `TestListRunsClampsTheLimitAndPassesTheCursor`、`TestGetRunReturnsTheRedactedArgumentsDigest`、
  `TestRunRoutesCoexistWithTheTranscriptRoutes`。
- `README.md`：`### The service refuses a stale schema`（**"双方都安静"**那段与错误示例）、
  `## Conversational agent (M4–M5)` 下的路由清单、`## Status` 里 M6 的验收叙述。
- `chat-service/AGENTS.md`：运行时表清单（含 `agent_runs` / `run_nodes` / `tool_calls`）、
  "DDL 与 schema 变更只属于 data-pipeline"、`VerifySchema` 那条。

这一章把"一次 turn 发生过什么"变成了可查询的证据。但证据本身不会告诉你**这个 Agent 好不好**：
你可以查到"这一轮用了哪个工具"，但查不到"**它对不对**"；你可以查到"这一轮没有说谎"，
也可以查到"这一轮引用了不存在的证据"——但你没法让系统回答"**在一份固定的用例集里，它错了几次**"。
这就是下一章的事：用一份 YAML 编排的 scripted provider，**零成本、离线、确定性地把真实的图
驱动起来**，给这个 Agent 出四道必须满分的安全门禁——**工具选择、确认门、重复预约、端到端成功率**；
以及一道容易被忽略的副题：**同一批用例，必须在"流式"与"整段"两条回答路径下各考一次。**

> 下一章：[第 21 章 怎么"考试"一个 Agent：四道安全门禁](21-怎么考试一个-agent-四道安全门禁.md)
