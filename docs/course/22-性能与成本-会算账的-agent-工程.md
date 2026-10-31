# 第 22 章 性能与成本：会算账的 Agent 工程

> 上一章：[第 21 章 怎么"考试"一个 Agent：四道安全门禁](21-怎么考试一个-agent-四道安全门禁.md) ｜ 下一章：[第 23 章 韧性与工程纪律](23-韧性与工程纪律.md)
>
> 第 21 章把"它对不对"变成了一份必须满分的考卷。可一份 1.000 的考卷不会告诉你：
> 用户等第一句话要多久、一轮到底花在检索还是模型上、哪些调用在花钱、哪些只是在本机烧 CPU。
> 这一章把 Agent 工程算成一笔账——**先测量，再归因，最后才谈优化**。
> 真实文件、命令与实测数字集中在文末 [§22.6 参考代码与目录](#226-参考代码与目录)。

---

## 22.1 坏 case 开场：一份 1.000 的考卷，答不出"快不快"

设想一个很常见的演示现场：四道门禁全绿，确认门绕不过去，引用一条不差。台下有人问：

> "它为什么想这么久？"

你打开日志，只看到一串"成功"。没有 p50，没有 p95，没有"第一句话是什么时候出现的"，
也没有"这 3 秒里模型占了多少、数据库占了多少"。**系统能证明自己是对的，却证明不了自己值得等。**

更糟的是，性能问题经常以三种"看起来有数"的方式被误读：

1. **用平均值代替长尾。** 平均 300 ms 听着还行，但如果 p95 是 2 秒，每二十个用户就有一个在等两秒。
2. **把冷启动算进分布。** 第一次查询要建连接池、暖查询计划；把它混进样本，p95 会穿着"冷启动"的外衣出现。
3. **把模型时间和运行时时间混在一起。** 模型慢是供应商的事，图编排慢才是你的代码的事；两个数字不分开，优化就无处下手。

所以这一章有两个题目：**怎么把延迟拆到能归因的粒度**，以及**怎么把成本拆到能做取舍的粒度**。
第 21 章问"它做对了吗"，这一章问"它值不值"。

---

## 22.2 概念与原理：把"慢"和"贵"拆成可归因的格子

### 22.2.1 先问"哪个数"：平均值、p50 与 p95

延迟不是一个数，而是一条分布。工程上最常看两个点：

- **p50（中位数）**：一半请求比它快。它描述"典型体验"。
- **p95**：95% 的请求比它快，剩下 5% 比它慢。它描述"尾部有多坏"。

平均值会被长尾拉高、也会被大量快请求压低，既不描述典型，也不描述尾部。所以项目的性能报告
只打印 p50 / p95，不打印平均值。

还有一个比统计口径更隐蔽的坑：**热身样本**。项目在检索延迟测试里明确把第一轮丢掉，
理由写在代码注释里（逐字）：

> The first pass pays connection pools and plan caches; it is warm-up, not a sample. Dropping it is what stops a cold first query from wearing the p95 as a costume.

——**"第一轮付的是连接池和查询计划的钱，它是热身，不是样本。丢掉它，才不会让一次冷启动的查询
披着 p95 的外衣出现。"**

这就是性能测量的第一条纪律：**先定义"你在测什么"，再决定哪些样本不算。**

### 22.2.2 一个 turn 有两个延迟：第一个字，和整轮

对话 Agent 的延迟不能只报一个数，因为**流式**把"用户什么时候能开始读"和"这一轮什么时候结束"
拆成了两件事：

- **first text（首字延迟）**：客户端第一次看到答案文字的时刻。
- **whole turn（整轮延迟）**：这一轮真正结束、状态落库、引用校验完成的时刻。

README 里那张表就是这两个时钟的对照（离线、scripted provider、固定每块 20 ms 的节奏）：

| 有据可依的一轮（离线，`PLATEPILOT_PERF=1`） | first text | whole turn |
|---|---|---|
| one-shot（整段回答） | p50 122.4 ms | p50 123.0 ms |
| streamed（流式回答） | p50 65.2 ms | p50 130.9 ms |

读这张表要读出三件事：

1. **流式只移动第一个数。** 首字从 122.4 ms 降到 65.2 ms，用户能早半轮开始读。
2. **整轮并没有变快，反而略慢。** 同一套工作，流式路径要**边生成边校验**，比"整段拿到再校验"
   多了一点协调成本；130.9 ms 对 123.0 ms 就是这笔成本。
3. **两个数字都接近相等，才是坏信号。** 如果一个号称流式的运行里 first text 和 whole turn
   永远一样，说明答案其实是整块到达的——流式开关没真的接上，或者传输在攒帧。

### 22.2.3 时间花在哪：把端到端拆成可相加的几段

一条检索路径的端到端时间，大致可以写成：

```text
search = 硬过滤 + 关键词召回 + embedding 往返 + 向量召回 + 融合 + 池内重排 + 证据组装
```

一条对话 turn 的端到端时间，则是：

```text
turn = 槽位抽取 + 多轮规划 + 工具执行 + 答案生成 + 引用校验 + 持久化
```

拆开之后，每个数字才有对应的"该动谁"。项目的实测给了两个锚点：

- **检索**：活语料上 p50 129 ms / p95 158 ms，其中 **embedding 往返约占 53%**。
  按这两个数做乘法，p50 里大约 `129 × 53% ≈ 68 ms` 花在 embedding 上；剩下的才是数据库过滤、融合与组装。
  这直接决定优化顺序：想显著改善检索 p50，先看 embedding 的模型、硬件与超时，
  而不是先加一个数据库索引。
- **Agent 运行时**：离线 scripted provider、无网络、无真库，每轮自身开销 **p50 0.34 ms**。
  这个数字说明图编排、工具注册表、检查点写入本身**近乎免费**；真实对话里那几百毫秒到几秒，
  主要是模型解码和检索，不是"图太慢"。

### 22.2.4 为什么用固定 20 ms/块来测

用真模型测延迟，测到的是"模型此刻有多快"，不是一个可复现的工程数。项目的性能套件改用
scripted provider，并把每个 chunk 的间隔固定为 `20 ms`。README 对这一点的解释（逐字）：

> Those numbers are the runtime's own, measured against a scripted provider paced at a fixed 20 ms per chunk: they say what the agent spends, not what a model would take.

——**"这些数字是运行时自己的开销，用固定每块 20 ms 的 scripted provider 测出来：
它们说的是 Agent 花了多少，而不是模型会花多少。"**

固定节奏在这里是一把**尺子**：它把"解码成本"变成一个已知常量，于是两条回答路径的差异
就只剩下"运行时决定何时发布"这一件事。**尺子不等于预报**——不能拿 122.4 ms 去承诺真实模型的首字延迟。

### 22.2.5 成本在哪：三种完全不同的花费

延迟之外还有第二本账。PlatePilot 的花费可以分成三类，它们的优化手段完全不同：

| 花费 | 在哪发生 | 计费方式 | 本项目的选择 |
|---|---|---|---|
| Embedding | 本地 Ollama，`qwen3-embedding:0.6b`，1024 维 | 不按调用付费，花 CPU/GPU 与时间 | 本地跑，换来中文/英文混合语料的可控性与零调用费；代价是它占了检索 p50 的一半以上 |
| Chat completion | 远端 OpenAI 兼容接口 | 按 token 计费 | 用 `CHAT_MODEL_PLAN` / `CHAT_MODEL_ANSWER` / `CHAT_MODEL_EXTRACT` 允许拆成不同模型，把便宜模型放在抽取上 |
| 离线 digest | 远端 LLM（可选） | 按 token 计费，且是批量 | 默认 `DIGEST_ENABLED=false`；规则基线 `digest:rules:v1` **零模型费用**，LLM 版本显式开启 |

除了直接调用费，还有一笔"上下文费"：送给模型的证据是有预算的。证据组装的默认 token 预算
是 **2000**（`DefaultEvidenceTokenBudget`），超预算的块**整块丢弃**而不是截断。这个数字同时是
质量旋钮和成本旋钮：放宽它会买回更多证据，也会买回更高的 token 账单。

调用次数也被代码封了顶，而不是靠模型自觉：

- 每轮最多 **5** 个 `plan → tools` 循环（`AGENT_MAX_TOOL_ROUNDS`）；
- 同线程最多 **3** 次澄清（`AGENT_MAX_CLARIFICATIONS`）；
- 单次工具调用 **15 s** 超时（`AGENT_TOOL_TIMEOUT`）；
- 槽位抽取 **10 s** 超时（`AGENT_SLOT_EXTRACT_TIMEOUT`）；
- 单次 chat 请求 **60 s** 超时、额外重试 **2** 次（`CHAT_TIMEOUT` / `CHAT_MAX_RETRIES`）。

这些上限不是"性能优化"，是**成本护栏**：没有它们，一个反复调工具的模型可以把一轮对话变成
五次模型调用、五次检索和一次超时重试。

### 22.2.6 把取舍写成一张表

性能与成本从来不是"越快越好"，而是"把预算花在用户能感知的地方"。几个真实开关的取舍：

| 选择 | 买到了什么 | 付了什么 |
|---|---|---|
| 打开向量通道 | 语义召回（第 7、8 章） | 检索 p50 的一半以上是 embedding 往返 |
| 打开流式回答 | 首字提前约一半 | 整轮略慢，因为要边发布边校验 |
| 打开池内重排 | 用精确 cosine 纠正 ANN 的近似顺序 | 多一次全候选池计算；没有网络往返，但吃 CPU |
| 接 rerank provider | 更好的顺序（端口已留） | 多一次外部调用；默认 3 s 超时，失败保留融合顺序 |
| 放宽证据预算 | 更多可引用证据 | 更高 token 成本，更长的答案生成时间 |

**优化的第一步不是改代码，而是知道自己现在站在哪一格。**

---

## 22.3 承载思路的最小示例：测量必须成对出现

下面这段骨架把上面的原则压成 30 行。它不是仓库源码的搬运，只保留"测什么、怎么报"的骨架：

```go
func measureSearch(ctx context.Context, svc *retrieval.Service, queries []string) {
	var searchSamples, embedSamples []time.Duration
	warm := true

	for round := 0; round < 3; round++ {
		for _, q := range queries {
			start := time.Now()
			if _, err := svc.Search(ctx, requestFor(q)); err != nil {
				continue
			}
			elapsed := time.Since(start)

			if warm {
				warm = false // 第一轮是热身：连接池、查询计划，不是样本
				continue
			}
			searchSamples = append(searchSamples, elapsed)
		}
	}

	// embedding 单独计时：不知道它占多少，就无法判断该优化谁
	for i := 0; i < 10; i++ {
		start := time.Now()
		_, err := embedder.EmbedQuery(ctx, "安静的适合约会的布鲁克林意大利餐厅")
		if err != nil {
			break
		}
		embedSamples = append(embedSamples, time.Since(start))
	}

	report("search end-to-end", searchSamples)
	report("embedding round-trip", embedSamples)
	callOutShare(searchSamples, embedSamples)
}
```

这段骨架有三个刻意的设计：

1. **丢掉热身**：否则冷启动会伪装成 p95。
2. **成对打印**：端到端与 embedding 同时出现，读者不用自己做减法。
3. **主动说出占比**：`callOutShare` 直接打印"embedding 占中位检索的 53%"，
   而不是留下两个数字让人猜。项目的 `callOutBottleneck` 就是这么做的——它甚至会在
   embedding 超过端到端时，直接说"瓶颈是向量通道，不是数据库"。

Agent 侧同样成对：`agent_perf_test.go` 每轮读两个时钟（first text / whole turn），
最后再把两条路径的 first text 并排打印。**一个数字只能说明快慢，两个数字才能说明结构。**

---

## 22.4 取舍与边界

**1. 微基准不是生产容量。**

检索延迟测试只取最多 24 条 query、每轮 3 遍；它描述的是"这套 fixture 覆盖的工作负载"，
不是并发用户下的吞吐。没有并发、没有冷缓存、没有跨网络，这些都不在数字里。
它能抓回归，不能当 SLA。

**2. 样本少时，p95 只是一个警报器。**

embedding 只取 10 个样本，agent 每条用例 20 轮、11 条用例、两条路径。这个量级足够发现
"从 65 ms 涨到 200 ms"，但不足以做精细的容量规划。项目把性能套件放在 `PLATEPILOT_PERF`
后面，也是同一个判断：**每个提交都跑一次的百分位，没人会读；从来不跑的数字，没人有。**

**3. scripted provider 测的是"我们花的"，不是"模型会花的"。**

固定 20 ms/块把模型变成一个常量。真实部署里，模型解码、网络抖动、供应商限流都会改变首字延迟。
所以这张表只能用来比较**两条路径的差异**和**运行时自身的回归**，不能拿去向用户承诺绝对延迟。

**4. 性能不是账单。**

仓库测量的是**时间**，不是**钱**。真正的成本核算还需要每次调用的 token 用量和供应商单价；
本项目没有把价格表写进代码，也不应该假装自己有。能确定的是结构：embedding 本地、chat 远端、
digest 可选且默认关闭——**先把"哪些调用会花钱"说清楚，再谈具体金额。**

**5. 优化顺序：先归因，再改一档，再复测。**

如果 p95 由模型主导，你去加数据库索引不会有任何变化；如果由 embedding 主导，你去改 prompt 也一样。
每次只改一个开关（向量通道、oversample、top_k、证据预算），重新跑同一套 query，才有可比性。

**6. 流式的收益有时间边界。**

流式把首字提前，但不减少总工作量，还会让整轮略慢。它适合"用户要开始读"的交互场景；
如果客户端本来就要等完整答案再做后处理，流式的收益只是心理上的。**别把"首字早"说成"整轮快"。**

---

## 22.5 课后实验

**跑（真的把两本账算出来）：**

1. **跑完整性能套件**（需要真库与 Ollama）：

   ```bash
   make -C chat-service perf
   ```

   它会依次跑检索延迟与 Agent 延迟。先读两张表：检索的 p50/p95 与 embedding 占比，
   Agent 的 first text / whole turn。**确认你能指出：这两个套件测的不是同一件事。**

2. **只跑检索，并读瓶颈结论**：

   ```bash
   cd chat-service
   PLATEPILOT_PERF=1 go test ./internal/retrieval/ -run TestRetrievalLatency -count=1 -v -timeout 600s
   ```

   如果本地没有 embedding provider，输出会明确说"向量通道没有跑，百分位只描述结构化+关键词"。
   这句话本身就是第 23 章"降级必须可见"的一个实例。

3. **关掉向量通道再跑一次**：

   ```bash
   RETRIEVAL_ENABLE_VECTOR=false PLATEPILOT_PERF=1 \
     go test ./internal/retrieval/ -run TestRetrievalLatency -count=1 -v -timeout 600s
   ```

   对比 p50：消失的那部分时间，就是 embedding 往返的代价。**但也要记住另一面**：
   语义召回同时消失了，这不是免费的优化。

4. **改一个检索旋钮，复测同一套 query**：把 `RETRIEVAL_OVERSAMPLE` 从 2 调到 1 或 3，
   或把 `RETRIEVAL_TOP_K` 从 5 调到 10，重跑第 2 步。只改一个，才能把变化归因到它。

5. **验证性能套件默认不跑**：

   ```bash
   cd chat-service && go test ./internal/retrieval/ -run TestRetrievalLatency -v
   ```

   应当看到 skip 与"set PLATEPILOT_PERF=1"的提示。**一个每次提交都跑的性能门禁，
   通常只会被忽略；把它放在显式开关后面，是刻意的。**

**读：**

6. 读 `chat-service/internal/retrieval/perf_test.go`：`perfQueryLimit = 24`、`perfQueryRounds = 3`、
   `perfEmbedSamples = 10` 三个常量的理由，`warm` 变量如何丢掉第一轮，`callOutBottleneck`
   如何把"占比"直接写成结论。
7. 读 `chat-service/internal/agent/agent_perf_test.go`：`perfTurnsPerCase = 20`、
   `perfPath` 为什么需要 `ChunkDelay`、`measurePath` 里的两个时钟、`logAnswerPathEffect`
   为什么要把两条路径并排打印。
8. 读 `chat-service/internal/agent/agent_eval_test.go` 的 `evalPacingDelay = 20 * time.Millisecond`
   与它上方的注释：**为什么没有固定节奏，"首字延迟"测到的会是假 provider 而不是运行时。**
9. 读 `chat-service/internal/retrieval/rerank.go`：`DefaultRerankTimeout = 3s`、
   `ApplyRerank` 为什么永不返回错误、`validatePermutation` 为什么必须拒绝"少了候选"的结果。
10. 读 `chat-service/internal/retrieval/assemble.go` 的 `DefaultEvidenceTokenBudget = 2000`
    与 `assemble_test.go` 里"超预算整块丢弃"的断言：**预算既是成本，也是引用完整性。**
11. 读根 `.env.example` 的 `CHAT_TIMEOUT`、`CHAT_MAX_RETRIES`、`AGENT_MAX_TOOL_ROUNDS`、
    `AGENT_TOOL_TIMEOUT`、`AGENT_SLOT_EXTRACT_TIMEOUT`、`DIGEST_*` 注释：
    **哪些数字是成本护栏，哪些是质量旋钮。**

**想：**

12. **122.4 ms 和 65.2 ms 之间差的 57 ms 去哪了？** 用本章的两个时钟解释：
    流式在生成过程中就发布了第一段，整段路径必须等所有 chunk 解码完才发布。
    再解释为什么整轮反而从 123.0 ms 涨到 130.9 ms。
13. **如果检索 p95 从 158 ms 涨到 400 ms，你先查什么？** 按"先归因"的顺序列出三步：
    看 embedding 占比、看是否新增了 rerank 调用、看数据库过滤是否退化成顺序扫描。
    ——这正好把第 7、8 章和本章接起来。
14. **一份 1.000 的门禁和一条 p95 回归，分别由哪一章负责？** 回答这个问题，
    就等于说清了"正确性"和"可用性"为什么必须分开度量。

---

## 22.6 参考代码与目录

**性能套件**

- `chat-service/internal/retrieval/perf_test.go`：`TestRetrievalLatency`（`PLATEPILOT_PERF` 门控）、
  `perfQueryLimit = 24`、`perfQueryRounds = 3`、`perfEmbedSamples = 10`、热身丢弃、
  `reportLatency`（p50/p95 + n）、`callOutBottleneck`（embedding 占中位检索的比例）。
- `chat-service/internal/agent/agent_perf_test.go`：`TestAgentTurnLatency`、`perfTurnsPerCase = 20`、
  `perfPath`（one-shot / streamed）、`measurePath`（first text 与 whole turn 两个时钟）、
  `logPerfRow`、`logAnswerPathEffect`（两条路径并排）。
- `chat-service/internal/agent/agent_eval_test.go`：`evalPacingDelay = 20 * time.Millisecond`、
  `evalOptions.ChunkDelay`、`evalObservation.firstText`（"没有固定节奏，测到的是假 provider"）。
- `chat-service/Makefile`：`perf` target（先检索、后 Agent；需要 pg-up 与 `ollama serve`）。
- `README.md`：Status 中的实测数字（检索 p50 129 / p95 158、embedding ~53%、运行时 p50 0.34 ms）
  与 "Running the agent evaluation and performance harness" 的两条路径表。

**成本护栏与预算**

- `chat-service/internal/config/config.go`：`defaultAgentMaxToolRounds = 5`、
  `defaultAgentToolTimeout = 15s`、`defaultAgentSlotExtractTimeout = 10s`、
  `defaultAgentMaxClarifications = 3`。
- 根 `.env.example`：`CHAT_TIMEOUT=60s`、`CHAT_MAX_RETRIES=2`、`AGENT_MAX_TOOL_ROUNDS`、
  `AGENT_TOOL_TIMEOUT`、`AGENT_SLOT_EXTRACT_TIMEOUT`、`CHAT_MODEL_PLAN/ANSWER/EXTRACT`、
  `DIGEST_ENABLED=false`、`DIGEST_CONCURRENCY=2`、`DIGEST_RATE_QPS=1`、`DIGEST_BATCH_SIZE=100`。
- `chat-service/internal/retrieval/assemble.go`：`DefaultEvidenceTokenBudget = 2000`、
  `minEvidenceTokenBudget`；`assemble_test.go` 的"整块丢弃"与预算断言。
- `chat-service/internal/retrieval/rerank.go`：`DefaultRerankTimeout = 3s`、
  `ApplyRerank`（失败保留融合顺序）、`validatePermutation`。
- `chat-service/internal/retrieval/rescore.go`：`Rescore`（精确 cosine 覆盖、失败降级保留召回分数）
  与 `rescorePool`（"ANN 页外"不等于"不相关"）。
- `data-pipeline/internal/pipeline/knowledge/digest.go`：`digest:rules:v1` 零模型基线、
  LLM digest 的开关与来源校验（第 6、24 章）。
- `shared/embedding/ollama/`：本地 `qwen3-embedding:0.6b`、1024 维；
  `data-pipeline/internal/llm/` 与 `chat-service/internal/app/chatmodel.go` 的
  `retryTransport`：远端 chat 的 `Timeout` / `MaxRetries` / `Retry-After` 处理。

**把两本账接起来读**

- 第 7 章：HNSW 与硬过滤，决定了检索时间的数据库那一段。
- 第 8 章：多路召回、池内重排，决定了 CPU 那一段。
- 第 9 章：证据预算与整块丢弃，决定了上下文成本那一段。
- 第 21 章：四道门禁，决定了"正确性"那一段——它和本章的 p95 一起，才构成完整的验收。
