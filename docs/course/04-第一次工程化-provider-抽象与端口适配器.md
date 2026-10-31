# 第 4 章 第一次工程化：Provider 抽象与端口/适配器

> 上一章：[第 3 章 一个 for 循环](03-一个-for-循环就是你的第一个-agent.md) ｜ 第一篇完 ｜ 下一篇：第 5 章 数据从哪里来
>
> 本章涉及的仓库真实文件、函数与实测数字集中在文末 [§4.11 参考代码与目录](#411-参考代码与目录)。
>
> **版本注记（2026-10-31）**：本章讲的是 M0 立地基时的端口设计。此后聊天端口随两个重构演进：
> 在线侧（chat-service）改为直接面向 Eino 的模型接口（第 12 章详述这段演化），批量侧收窄为
> `data-pipeline/internal/llm` 的单方法补全端口。本章的**概念与纪律**——依赖倒置、端口与适配器、
> 能力配置化、边界测试——没有任何变化；聊天端口的现状以 §4.11 与第 12 章为准。

---

## 4.1 坏 case 开场：如果第 3 章直接连真模型

回看第 3 章的循环，它依赖一个「带工具调用的补全能力」这个参数。假如我们图省事，
让业务代码直接 `http.Post` 调 OpenRouter、或者干脆 import 厂商 SDK，会立刻收获
三个麻烦：

1. **换不起。** 模型选型是会变的：规划要工具能力强的、写回答要文笔好的、槽位抽取
   想用便宜的小模型；哪天换个说同样协议的 vLLM 网关，HTTP 细节和错误方言全都不同。
   厂商类型一旦散落在业务代码里，换模型等于重写。
2. **测不起。** 每个单测都发真实请求：慢、花钱、结果随模型版本漂移。第 21 章那种
   「86+ 个 case 每次提交都跑、且必须逐 bit 可复现」的门禁根本不可能存在。
3. **错不起。** 429、5xx、网关用 200 状态码在身体里藏 error、流式第一帧前失败与
   第一帧后失败的重试语义不同——这些 HTTP 世界的破烂事不该长在 Agent 的节点代码里。

这三个麻烦不是本项目独有的，任何「把外部服务接进核心业务」的系统都会撞上它们。
所对应的通用解法有名字：**依赖倒置**、**端口与适配器**。本章先讲清这套思路，
再回来看 PlatePilot 是怎么在 M0 就把它立成地基的。

---

## 4.2 依赖倒置：先想「我需要什么能力」，再想「谁来提供」

新手接外部服务时，直觉是「我要调 OpenRouter，所以写一个 OpenRouter 客户端」——
依赖方向是 **业务 → 具体厂商**。这样业务代码里到处是厂商的名字、字段、错误码。

依赖倒置把这个箭头掉个头：**业务先声明「我需要什么能力」，定义成接口；具体实现
去满足这个接口**。

第 3 章的循环真正需要的能力，用一句话说清就是：给定一段对话和一组工具说明书，
返回「模型的回答」或者「模型想调用的工具」。把它写成接口，业务侧认识的类型全是
项目自己的：

```go
// 业务只认识这三个问题，不认识任何 HTTP、任何厂商
type ToolCallingProvider interface {
    SupportsTools() bool
    SupportsParallelTools() bool
    ChatWithTools(req ChatRequest, tools []ToolSpec) (ToolCallResponse, error)
}
```

于是编译期依赖方向变成 **业务 → 抽象**：业务代码里不可能出现 `openai`、`http.Post`、
厂商错误码——因为类型系统里根本没有它们。

> **判据**：如果你在业务包里能 import 到一个厂商 SDK，说明抽象没做完。
> 好的边界是「业务包 import 列表里找不到任何具体外部服务的名字」。

### 为什么要按能力切成三个接口，而不是一个胖接口

就算做了抽象，也常见「一个巨大接口把模型能做的事全塞进去」的偷懒做法。更稳的做法
是**按能力切细**——这正是「接口隔离原则」在模型这层的样子：

- **不是每个模型都全能。** 便宜的小模型可能只能做普通补全，不保证结构化输出；
  一个实现只声明它真有的能力，调用方按能力位判断，而不是运行时收到 400 才知道。
- **依赖最小化就是边界本身。** 槽位抽取器只依赖「结构化输出」这一个能力，它就
  **永远无法**被改成发起工具调用——不是靠约定，是靠类型。
- **测试替身极好写。** 模拟一个单方法接口，比模拟一个上帝对象便宜得多。

M0 时的 PlatePilot，正是把聊天能力切成了「普通补全 / 工具调用 / 结构化输出」三个端口；
而 embedding 是**另一个完全独立的端口**——聊天模型和 embedding 模型独立选型、
独立部署（一个远端、一个本地），把它们绑进同一个接口才是怪事。（聊天端口后来随
重构演进：在线侧由 Eino 的模型接口取代，批量侧收窄成单方法端口——见 §4.11 版本注记
与第 12 章。但「按能力切分、embedding 独立」这条判断至今成立。）

---

## 4.3 端口与适配器：一个核心，一圈端口

把「接口在业务侧定义」这件事推到系统级，就是**端口与适配器（又叫六边形架构）**：

```text
              ┌─────────── 端口（业务定义的接口）───────────┐
   适配器 ────▶│  chat   embedding   store   ...            │◀──── 适配器
 (openai)     │           系统核心：领域 + 应用             │      (postgres)
   适配器 ────▶│  （只依赖抽象，不认识任何具体技术）         │◀──── 适配器
 (fake)       └────────────────────────────────────────────┘      (memory)
```

- **核心**是领域与应用逻辑，它只面向端口编程；
- **端口**是核心自己定义的「我需要什么」，如 chat、embedding、存储；
- **适配器**是每个端口的具体实现，如 openai / fake、ollama / fake、postgres / memory。

方向铁律：**适配器向端口靠拢，不是端口为适配器改形**。换适配器不动核心，换核心不动协议。

这里有一条最容易被忽略、也最该讲清的纪律：

> **可替换性必须被验证过才算数。**

一个端口如果只有一个实现，那它还只是「预留的语法」，不是真正的可替换——你永远
不知道这个接口的抽象是否合适，因为没有第二个实现来检验它。PlatePilot 的做法是：
**每个端口都真的有至少两个实现**（一个生产、一个离线替身），而且**跑同一套测试**。
这条纪律会在 §4.6 兑现。（聊天侧后来的重构恰好是个印证：端口从项目自定义换成
Eino 的模型接口时，「一个生产实现 + 测试包内的接口 fake」这个结构原样跟着搬了——
纪律没变，变的只是接口由谁定义。）

---

## 4.4 模型接口长什么样：OpenAI 兼容协议与它的两个陷阱

「聊天模型的接口」在今天几乎有一个事实标准：OpenAI 的 `/chat/completions`。
理解它，就理解了绝大多数模型服务的共同形状（OpenRouter、vLLM、Ollama 兼容路由都讲它）。

请求体是这几个字段：

| 字段 | 含义 |
|---|---|
| `messages` | 对话数组，四种角色 system / user / assistant / tool |
| `tools` | 工具说明书数组，信封是 `{"type":"function","function":{...}}`，参数里放 JSON Schema |
| `tool_choice` | 本适配器恒为 `"auto"`——选不选工具由模型决定 |
| `parallel_tool_calls` | 是否允许一次返回多个工具调用 |
| `stream` | 走 SSE 流式还是一次性返回 |
| `response_format` | 结构化输出，`json_schema`（严格）或 `json_object`（广泛支持）两档 |

非流式响应就是 `id / model / choices / usage`，`choices[0].message` 和 `finish_reason`
映射回你自己的领域类型；`usage` 里的 token 账后面算成本要用。

### 陷阱一：工具参数是「内容为 JSON 的字符串」，不是 JSON 对象

这是手写协议客户端第一个会咬人的地方。协议里 `arguments` 不是嵌套对象，而是**一个
字符串，字符串里装着 JSON**：

```json
{"tool_calls":[{"id":"c1","type":"function",
  "function":{"name":"search_restaurants","arguments":"{\"text\":\"quiet italian\"}"}}]}
```

也就是说你要做一层「字符串信封 ↔ 内层 JSON」的往返。**流式场景更碎**：SSE 里参数是
按片段一层层追加的字符串，必须先拼接完才能解析。

### 陷阱二：能力是模型的属性，不是协议的属性

说同一种协议的模型，能力天差地别：有没有 function calling、能不能并行、支不支持
严格 JSON Schema、上下文多大。这些**不能靠协议本身回答**，得由部署者声明。

项目用一张显式能力表（`CHAT_SUPPORTS_TOOLS` / `CHAT_SUPPORTS_PARALLEL_TOOLS` /
`CHAT_SUPPORTS_JSON_SCHEMA` / `CHAT_CONTEXT_TOKENS`）声明，默认走保守子集
（工具与流式开，并行与严格 schema 关，窗口 32768）。配套的关键纪律是：

> **不可能成功的请求，在本地直接拒绝，不发 HTTP。**

模型没声明工具能力却去调「带工具的补全」，立刻返回错误——因为真实失败形态会是
「你在等结构化调用，它还你一段散文」，让这种错误晚一步、以 400 的形式出现，只会更难排查。

这里还有一个刻意的取舍：**能力靠配置声明，不靠运行时探测**。自动探测看起来聪明，
但会让行为随上游变化而漂移，而第 21 章的评测要的是可复现。配错了后果自负，换来的是
「同样的输入永远走同样的路径」。

---

## 4.5 适配器要吞掉的「HTTP 破烂事」

端口把能力的形状固定下来，剩下所有「和外部服务打交道的脏活」都被关进适配器。
一个成熟的模型适配器，至少要替业务消化这四件事：

1. **领域 ↔ wire 映射。** 项目 DTO 和协议 JSON 的双向转换，包括 `finish_reason`、
   工具调用、流式 delta；还要兼容不同模型的方言（比如推理模型的思考字段
   `reasoning` / `reasoning_content` 就有两种写法）。
2. **重试与退避的分类。** 这是最容易写错的地方。只有**瞬时可重试**的错误才重试：
   429、408、5xx、以及网络传输错误。而 **400（请求本身写错）、401/403（鉴权）、
   402/404（没钱 / 模型名错）必须立刻失败**——这些是配置故障，重试只会耽误报错时机，
   把一个「一眼能看出配错了」变成「卡半天然后失败」。退避要指数化，并尊重 `Retry-After`。
3. **网关的方言错误。** 一些网关会用 **HTTP 200 + body 里的 `error` 字段 + 空 choices**
   来表达「上游过载」。只看状态码的客户端会把它当成成功、然后拿着空 choices 崩在
   一段毫不相关的代码里。适配器要把它识别成「可重试的上游不可用」，并把上游消息
   （限长）带给运维——光看状态码无法行动。
4. **流式的重试边界。** 这是写流式应用时最值钱的一条经验：

   > **`Stream` 只在「第一帧到达之前」重试。**

   第一帧之后，用户屏幕上已经有文本了；此时重放整个 turn 会造成重复输出。而首帧之前
   调用方什么都没看见，第二次尝试对它完全透明。同一个「重试」，在流式里必须区分
   「还没吐字」和「已经吐字」两个阶段，这两种语义天差地别。

embedding 适配器是同一套思路的翻版：批量输入、自己的超时与重试；模型没拉取时对方
用 200 + `error` 回答，这种**永久性配置故障不重试**，错误消息直接告诉你去跑
`ollama pull <model>`。还有一个细节值得学：**返回的向量要逐个校验**——空向量、维度不符、
NaN/Inf 全部在客户端就报错，否则它们会一路流到数据库，变成一句含义不明的类型错误。

---

## 4.6 可替换性的兑现：让全部测试离线、确定、免费

§4.3 说「可替换性必须被验证过」。端口抽象最大的回报，正是在测试侧兑现——

只要业务面向端口编程，就能给它喂两个**测试替身**：

- **脚本化聊天模型**：你直接设置「它下一次返回什么」（一段文本 / 一个工具调用请求 /
  一份结构化结果 / 一个错误），并记录「我收到了什么请求」供断言。于是整张 Agent 图
  可以**离线、确定、零成本**地被驱动。
- **确定性 embedding**：向量是输入文本的**纯函数**，同样语料永远产生同样向量，宽度
  与真实模型一致（混用了会因维度立刻穿帮），还提供「必定失败」的实现专门练习失败路径。

为什么强调**确定性**？因为随机向量下测试通过只代表「这次运气好」，而不是「逻辑正确」。

这两个替身的价值远不止单测，它们撑起了后面整条工程链路：

- **契约测试**：内存实现和真实数据库适配器跑**同一套**行为断言，让上层逻辑完全离线；
- **评测门禁**（第 21 章）：用 YAML 编排「模型第 N 次返回什么」，零成本驱动**真实的图**，
  加 case 只是改数据；
- **性能核算**（第 22 章）：用固定节奏的脚本 provider，测出来的是**我们自己花了多少时间**，
  而不是模型服务器今天的心情。

> **纠一个常见误解。** 生产里也可以把 embedding 配成 fake（那个进程内确定性实现），
> 此时语义通道**照常跑**，只是向量不携带真实语义。真正让语义通道停跑、trace 里记
> `ran:false` 的，是**完全不配 `EMBEDDING_PROVIDER`**（能力缺失），或显式关掉向量通道
> （`RETRIEVAL_ENABLE_VECTOR=false`）。「配了 fake」和「没配」是两回事。

---

## 4.7 边界靠「会失败的测试」固化，不靠自觉

端口和分层只是约定，而约定会被「顺手一下」破坏：今天有人在领域层加一句
「从 postgres 行映射过来」的注释，明天就可能真加一个 `sql.NullString` 字段。
靠文档请求大家自觉，是靠不住的。

更稳的工程做法是：**把架构边界写成会失败的测试**。用 Go 的解析器遍历源码（不需要
真的编译整个工程），然后断言：

- **依赖白名单**：领域层的 import 只允许标准库和它自己——数据库驱动、Web 框架、
  LLM 编排框架、任何厂商 SDK 出现即失败；
- **连词汇都不许提**：更严格的一条，源码与**注释**里出现 `postgres` / `pgvector` /
  `sql` 这类存储词汇也算失败。因为「存储词汇」正是后端技术一点点爬回领域层的路径。
- **框架不得越界**：Agent 编排框架（Eino）的类型只许出现在 `internal/agent` 下，
  唯一的例外是装配包 `internal/app`（它构造具体模型实例）；服务之间也禁止互相 import。
  （M4 最初连节点之间也只许传项目自有 DTO、框架类型锁死在两个桥接包；后来发现那层
  转换是纯搬运，边界改为「Eino 不出 agent 与 app」，节点直接说 `schema.Message`——
  第 12 章讲这段演化。）

PlatePilot 把这三类边界都写成了测试（第 20 章的 run 审计、第 12 章的 Eino 抽象会反复
回到它们）。配套的纪律是一条心态：

> **违反这些测试时，正确反应是想「代码是不是放错了层」，而不是改测试放宽边界。**

架构测试是这个项目里少数「**应该很难改**」的测试——它是少数该被当成红线、而不是
当成绊脚石的东西。当然它也有边界：它守得住 import 和词汇，守不住语义（你在领域层
手写一个只用标准库的 HTTP 客户端，它发现不了）。所以最后一道仍是 review 与契约测试。

---

## 4.8 装配只发生在一个地方，故障按「还能不能给正确答案」分级

所有具体实现在**唯一一个装配包**里被拼起来——服务的其余部分完全不知道 `openai` 的
存在。配置走统一的三步：`Load()` → `Validate()` → `Redacted()`；缺关键配置启动即失败
并给出明确错误；任何打印配置的地方都经过脱敏，**密钥不进日志是硬规矩**。

故障处理不是「一视同仁」，而是按一句判据分级：**这个能力没了，还能不能给出正确答案？**

- **chat provider**：**配置了却构建失败 = 拒绝启动**（从 M4 起模型是主路径，静默降级
  只会在运行期变成一堆看不懂的错误）；**完全没配置 = 没有 Agent 面但服务仍可启动**，
  骨架、schema 校验、健康检查照常可用（开篇词的「冒烟档」正依赖这一点）。
- **embedding**：构建失败或缺配置 = **降级**——检索退到结构化与关键词通道，trace
  如实记录语义通道没跑，而不是拒绝启动。
- **上游过载 / provider 不支持流**：在适配器层重试，或降级为整段完成并在结束帧告警。

同一份系统，两种故障态度，差异完全来自上面那句判据。「拒绝」和「降级」的选择要在
设计时就写清楚，而不是等线上出事时临时拍脑袋。

---

## 4.9 取舍与边界

- **OpenAI 兼容是刻意的窄选择。** 一个适配器、一套 wire 类型覆盖 OpenRouter、vLLM、
  Ollama 兼容路由；代价是用不上各家专有特性（只能经一个唯一的逃生舱字段透传）。
  对本项目足够；专有收益出现时再开新适配器，而不是让厂商类型泄漏进领域层。
- **能力声明靠配置，不靠探测。** 已在 §4.4 讲过：换来的是可复现。
- **结构化输出从不被信任。** `json_schema` 严格模式只保证「合法 JSON」，保证不了
  业务合法；解析失败只纠正重试一次，第二次失败明确报错。业务校验（行政区真实存在吗？
  证据 ID 本轮真的有吗？）永远在代码侧（第 15 章）。
- **适配器拥有传输策略，业务拥有产品策略。** 重试几次、超时多久在适配器；
  「embedding 挂了要不要关掉语义通道」「写操作要不要人确认」在业务。两层不要互相借权。
- **抽象不是越早越好，但这一层值得早。** 只接一个模型、永远不换、也不写自动化测试的
  小脚本，抽象是纯负担；而一旦进入「多模型选型 + 离线评测 + 多人协作」，这层就是刚需。
  PlatePilot 属于后者，所以 M0 就先立好。

---

## 4.10 课后实验

**跑（全部离线、不花一分钱）**

```bash
cd data-pipeline
go test ./internal/llm/...  # 用 httptest 模拟 429/5xx/200-error，验证重试与分类
cd ../shared
go test ./domain/ -run 'TestDomainLayer' -v
go test ./embedding/...
cd ../chat-service
go test ./internal/agent/ -run TestEinoIsConfinedToTheAgentBoundary -v
```

**观察（改环境变量，看行为变化）**

- 在 `.env` 里把 `CHAT_MODEL` 改成一个明显不存在的名字（前提是已配置 chat provider），
  再 `make run-chat`，对照 §4.5 想想哪个分支会接住这个错误；再试把
  `CHAT_PROVIDER` 整个清空，观察服务是否仍然启动、Agent 面是否还在——这就是 §4.8 的
  「拒绝 vs 降级」。
- 把 `EMBEDDING_PROVIDER` 从 `fake` 改成空值，对照 §4.6 想想 trace 里的语义通道会
  发生什么变化。
- 做一个思想实验：如果把领域层里某个文件的**注释**改写成包含 `postgres` 字样，
  哪条测试会红？为什么连注释都要管？

---

## 4.11 参考代码与目录

> 本节是本章涉及的真实实现清单。正文只讲思路，想深入实现的读者从这里进去。

**端口（业务定义的接口）**

- [shared/embedding/embedding.go](../../shared/embedding/embedding.go)：独立的 embedding 端口。
- `shared/rerank`：预留空实现的重排端口（第 8、24 章再谈）。
- 聊天侧的「端口」如今由框架与批量客户端承担：在线侧是 Eino 的
  `model.BaseChatModel` / `model.ToolCallingChatModel`（`chat-service/go.mod` 引入），
  批量侧是 [data-pipeline/internal/llm/llm.go](../../data-pipeline/internal/llm/llm.go)
  的单方法 `Completer`。M0 时代项目自定义的 `ChatProvider` 三端口已于 2026-10-31 移除
  （m0 附录 C 记录了这段演进）；`shared/domain/chat` 的请求/响应 DTO 仍是两条链路共用的词汇。

**协议与适配器**

- [data-pipeline/internal/llm/client.go](../../data-pipeline/internal/llm/client.go)：
  OpenAI 兼容协议的批量侧实现；文件顶部是唯一的端点常量 `chatCompletionsPath`，
  wire 类型包内不导出；重试分类（仅 429/408/5xx/传输错误可重试）、`Retry-After` 遵守、
  200+body-error 识别都在这里。默认单次 60s 超时、首调之外再试 2 次（`DefaultMaxRetries=2`）。
- [data-pipeline/internal/llm/mapping.go](../../data-pipeline/internal/llm/mapping.go)：
  领域 DTO ↔ wire 类型双向映射，含「JSON 字符串信封」的往返。
- [chat-service/internal/app/chatmodel.go](../../chat-service/internal/app/chatmodel.go)：
  在线侧装配——用 eino-ext 的 openai 模型为规划 / 回答 / 抽取三个岗位各建一个实例，
  共享一个 `retryTransport`（单次尝试超时、流式豁免、有界重试、`Retry-After`、附加请求头），
  抽取模型的 response format 在这里按 `CHAT_SUPPORTS_JSON_SCHEMA` 固定。
- [shared/embedding/ollama/client.go](../../shared/embedding/ollama/client.go)：embedding 适配器，
  POST `/api/embed`、30s 超时、3 次重试、返回向量逐个校验。

**测试替身与契约**

- [shared/embedding/fake/fake.go](../../shared/embedding/fake/fake.go)：确定性 embedding，
  向量是输入的纯函数，默认宽度 1024，提供 `Failing(err)` 练习失败路径。
- 聊天侧的模型 fake 不进 `shared/testkit`（那是 shared 模块，不能引 Eino）：
  agent 测试用 [runner_test.go](../../chat-service/internal/agent/runner_test.go) 的
  `scriptedProvider`（实现 `model.ToolCallingChatModel`，严格按序弹出、未编排调用大声失败），
  app 测试用 [fake_model_test.go](../../chat-service/internal/app/fake_model_test.go) 的
  `fakeChatModel`（队列驱动，未排响应即报错）。
- [shared/store/contract/](../../shared/store/contract/)：内存与 Postgres 适配器共用的行为契约。

**架构边界测试**

- [shared/domain/architecture_test.go](../../shared/domain/architecture_test.go)：
  `TestDomainLayerHasNoFrameworkOrVendorDependencies`（import 白名单）、
  `TestDomainLayerDoesNotNameAStorageEngine`（连注释里的存储词汇都不许出现）、
  `TestServicesDoNotDependOnEachOther`（两服务禁止互相 import）。
- [chat-service/internal/agent/architecture_test.go](../../chat-service/internal/agent/architecture_test.go)：
  `TestEinoIsConfinedToTheAgentBoundary`——Eino 只许出现在 `internal/agent` 下，
  唯一例外是装配包 `internal/app`（构造模型实例）；它 walk 整个仓库，
  连 data-pipeline 将来依赖 Eino 也会被抓住。
- [chat-service/internal/admin/architecture_test.go](../../chat-service/internal/admin/architecture_test.go)：
  admin 应用层禁止 import 具体 postgres 适配器，只能面向仓储端口。

**装配与配置**

- [chat-service/internal/app/app.go](../../chat-service/internal/app/app.go)：全服务唯一装配具体实现的包；
  `buildChatModels` 把配置映射成三个模型实例（§4.8 的「配置了却构建失败即启动失败」在这里），
  `buildEmbedding` 在 ollama 与 fake 之间切换。
- [chat-service/internal/config/config.go](../../chat-service/internal/config/config.go)：
  `Load()` / `Validate()` / `Redacted()`；`ChatConfig.Enabled()` 就是 `Provider != ""`。

---

第一篇到此结束。你现在手里有：一个真实的数据问题（第 1 章）、一张概念地图
（第 2 章）、一个会撞墙的 40 行 Agent（第 3 章），以及一套让它可替换、可测试、
边界不腐化的工程骨架（第 4 章）。下一篇进入数据工程与 RAG——第 5 章从「2.5 GB
文件为什么不能 `json.Unmarshal`」开始，正面处理第 3 章的头号坏 case：幻觉。