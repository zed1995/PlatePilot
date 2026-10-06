# PlatePilot M4 Agent 运行时任务文档

> 生成日期：2026-10-02
> 依据文档：
> - `docs/platepilot-requirements.md`（US-05、US-06、US-07）
> - `docs/platepilot-technical-prd.md`（§8.3 LLM Provider 抽象、§8.4 OpenAI-Compatible Chat Adapter）
> - `docs/platepilot-implementation-plan.md`（M4-01 ~ M4-11、Gate D）
> - M0/M1/M2/M3 任务文档（已交付）
>
> 里程碑：M4 — Agent 运行时（Eino Graph、OpenAI-Compatible Chat Provider、工具调用、Run 审计、会话状态、Chat/SSE）
> 验收门：Gate D（Agent 运行时，见 §1.2）
> 当前状态：**待开始**。首批执行 M4-02 + M4-03（OpenRouter 聊天适配器与能力表）。
>
> 关键渠道决策（2026-10-02 用户确认）：M4 的聊天模型渠道使用 **OpenRouter**（OpenAI 兼容协议）。
> `base_url` / API key / model ID 全部通过环境变量配置，代码与仓库中不写死任何渠道地址之外的默认模型，
> 更不允许出现真实密钥。同一适配器以后也可指向 vLLM / Ollama / 其他 OpenAI 兼容端点。

---

## 0. 如何使用本文档

本文档是 M4 的唯一执行清单。每个任务都给出：目标、交付物、实现要点、依赖、工作量级和验收标准。
实施时按 §3.2 的顺序拆提交；一个任务完成后对照 §6.1 的通用 DoD 自检。

代码契约以仓库现状为准；本文档中的接口形状是**约定**，实现时允许按 Eino 与既有代码的实际签名调整，
但不得改变任务的目标与验收标准。实际落地与计划不一致之处，追加到文末「附录 E：实施记录」。

### 0.1 起点状态（2026-10-02，M0/M1/M2/M3 已交付）

**M4 可直接消费的产物：**

| 产物 | 位置 | 说明 |
| --- | --- | --- |
| Chat 端口三件套 | `shared/chat/chat.go` | `ChatProvider`（Complete/Stream）、`ToolCallingProvider`、`StructuredOutputProvider`、`ChatStream` |
| 聊天领域 DTO | `shared/domain/chat/chat.go` | ChatRequest/ChatMessage/ChatChunk/ToolCallResponse/TokenUsage/FinishReason/StructuredRequest |
| 工具领域 DTO | `shared/domain/tool/tool.go` | ToolSpec（Parameters 为 JSON Schema RawMessage）、ToolCall、ToolResult |
| 会话领域 DTO | `shared/domain/conversation/conversation.go` | Conversation、Checkpoint（version、state、pending_action、missing_slots、evidence_ids、selected_restaurant_id） |
| 记忆领域 DTO | `shared/domain/memory/memory.go` | Memory（preference/constraint/fact，含可选 embedding） |
| 审计领域 DTO | `shared/domain/run/run.go` | AgentRun（模型/token/延迟/计数/错误码）、ToolCallRecord |
| 仓储端口 | `shared/store/repository.go` | `ConversationRepository`、`MemoryRepository`、`RunRepository`（M0-06 已定义） |
| 内存仓储 | `shared/store/memory/{conversation,memory,run}.go` | 三个端口的内存实现，供单测与契约套件使用 |
| 检索应用服务 | `chat-service/internal/retrieval/` | `Service.Search(retrieval.Request)`（M3-05 混合融合）、`Service.Evidence(EvidenceRequest)`（M3-07 证据组装） |
| 配置位 | `chat-service/internal/config/config.go` | `ChatConfig{Provider, BaseURL, APIKey, Model, ExtraHeaders}`，含启用校验与脱敏 |
| 装配位 | `chat-service/internal/app/app.go` | `Deps` 已有 Chat/ToolCalling/Structured/Conversations/Memories/Runs 字段，`Connect()` 尚未构建 |
| HTTP 骨架 | `chat-service/internal/httpapi/` | Hertz router、requestid/recover/cors/accesslog 中间件、统一错误响应 |
| 错误模型 | `shared/domain/errs/` | invalid_argument / unauthorized / provider_timeout / provider_unavailable / validation_failed 等可直接复用 |
| Mock Provider | `shared/testkit/mock_provider.go` | MockChatProvider 已实现三端口，供 Agent 编排测试 |

**M4 的缺口（本里程碑要补齐的）：**

1. `go.mod` 没有 Eino 依赖，没有 agent 包 —— M4-01。
2. `shared/chat/openai/` 只有一行 `TODO(M4-02)` 占位 —— M4-02。
3. 没有模型能力表；`ChatConfig` 缺超时、重试与能力开关 —— M4-03。
4. 没有工具注册表与任何工具实现 —— M4-04/05/06。
5. PostgreSQL 没有 Agent 运行时表：`agent_runs`、`tool_calls`、`conversations`、`conversation_checkpoints`、`user_memories`；
   三个仓储端口没有 postgres 适配器 —— M4-07/08/09。
6. 没有最终回答合成与引用校验节点 —— M4-10。
7. 路由只有 `/v1/restaurants/*` 与 `/admin/v1/*`，没有 Chat/SSE 与会话 API —— M4-11。
8. 会话**消息**目前没有 DTO、表和端口方法（只有线程元数据与 Checkpoint）——M4-08 一并补齐（见该任务说明）。

**环境现状：**

- 本地 PostgreSQL（pgvector + PostGIS）在线；M3 语料 3,000 家餐厅 / 30,198 篇活跃文档已入库。
- 本地 Ollama 当前未启动、embedding 模型未拉取。M4-02 的聊天联调不依赖 Ollama；
  但 M4-05/06 的工具走真实检索时需要 `qwen3-embedding:0.6b`（`ollama serve && ollama pull qwen3-embedding:0.6b`）。
- OpenRouter 账号、API key 与具体 model ID 由用户在实施完成后填入 `.env`（`.env` 已 git-ignore）。

### 0.2 工作量级定义

沿用 M2/M3 定义：S 约 0.5 天以内；M 约 1 天；L 约 1.5–2 天。

| 任务 | 量级 | 任务 | 量级 |
| --- | --- | --- | --- |
| M4-01 Eino Graph 骨架 | L | M4-07 Run 与 Tool Call 审计 | M |
| M4-02 OpenAI-Compatible Adapter | M | M4-08 Conversation/Checkpoint 持久化 | L |
| M4-03 模型能力表 | S | M4-09 记忆检索与注入 | M |
| M4-04 工具注册表 | M | M4-10 回答与引用合成 | M |
| M4-05 search_restaurants 工具 | M | M4-11 Chat/SSE 端点与会话 API | L |
| M4-06 get_restaurant_evidence 工具 | M | | |

### 0.3 前置条件

```bash
# 1. M0-M3 基线健康（离线，不需要数据库/embedding 在线）
go build ./... && go vet ./... && go test ./...

# 2. 数据库迁移为最新（M4-07/08 之前必须）
go run ./data-pipeline migrate

# 3. OpenRouter（M4-02 完成后的联调前置，用户填写）
#    在 .env 中配置 CHAT_PROVIDER / CHAT_BASE_URL / CHAT_API_KEY / CHAT_MODEL
#    model 必须是 OpenRouter 上支持 tools（function calling）的模型
```

---

## 1. M4 目标与退出条件

### 1.1 里程碑目标

把 M3 的「检索 + 证据」读路径接入一个**可恢复、可审计、引用可信**的 Agent 运行时：

1. 基于 Eino Graph 显式状态机：入口/上下文加载 → 意图与槽位 → 规划 → 工具调用 → 证据整合 → 回答生成 → 状态保存。
2. Eino 只消费项目自有的 `ChatProvider` 端口；厂商 SDK/线格式只存在于适配器层。
3. 默认聊天渠道为 OpenRouter（OpenAI 兼容），模型能力通过能力表声明；不支持所需能力的模型在调用前被拒绝。
4. 工具参数经 JSON Schema 校验；未知工具、越权参数返回结构化错误；工具结果限制 scope/top_k。
5. 每轮 run 可审计：模型、token、延迟、工具名、参数摘要、证据 ID、最终回答、错误码落库。
6. 会话线程与 checkpoint 持久化，中断后可恢复；消息历史可回放（M5 依赖）。
7. 对外提供 Chat/SSE 端点，前端可逐步接收 token 与工具状态事件。
8. Agent 回答必须引用证据；无证据不编造；不支持的问题礼貌拒绝并给出边界。

### 1.2 退出条件（Milestone Exit Criteria / Gate D）

以下七条全部满足才允许进入 M5：

1. **线程与流式**：能创建线程、发送消息，并通过 SSE 收到完整事件流（token 增量、工具状态、引用、结束/错误）。
2. **能力拒绝**：配置的模型不支持所需工具能力时，请求在发出 HTTP 之前被拒绝，返回明确错误码与可操作提示。
3. **工具校验**：工具调用参数通过 JSON Schema 校验；未知工具、缺必填参数、类型错误均返回结构化错误，不产生 500。
4. **Run 审计**：每轮 run 在库中可查到模型渠道/模型名、token 用量、延迟、每次工具调用（名称、参数摘要、状态、耗时）、
   证据 ID 列表、最终回答（或错误码）。
5. **可恢复**：线程中断后可基于最近 checkpoint 恢复到 `current_state / pending_action / missing_slots / 已选证据`。
6. **引用可信**：回答中的引用 ID 全部来自当轮 `get_restaurant_evidence` 返回；引用越界视为运行时错误而非静默裁剪。
7. **质量基线（离线）**：Agent 编排全部节点有单测；工具→证据→回答链路在 Mock Provider + 内存仓储下有端到端测试；
   `go build/vet/test ./...` 全绿。首 token 延迟在本机真实环境记录数值（M6-03 设硬门限，M4 只记录不设门）。

---

## 2. 目标数据形态

### 2.1 分层与依赖方向

```text
HTTP 层（hertz）
  chat-service/internal/httpapi        Chat/SSE Handler、会话 API、SSE 事件编码
        │  只依赖应用层与领域 DTO
        ▼
应用/编排层
  chat-service/internal/agent          Graph 装配、节点、状态、工具注册表、回答合成
  chat-service/internal/retrieval      M3 已交付（Search / Evidence），被工具包装
        │  只依赖端口
        ▼
端口层（shared）
  shared/chat                          ChatProvider / ToolCallingProvider / StructuredOutputProvider
  shared/store                         Repository 端口
        │
        ▼
适配器层（厂商/框架类型只允许出现在这里）
  shared/chat/openai                   OpenAI 兼容适配器（OpenRouter 是一个配置实例）
  chat-service/internal/agent/einomodel   Eino model.ToolCallingChatModel → 项目 Chat 端口的适配
  shared/store/postgres                运行时五张表的适配器
```

**硬约束（架构测试已强制，M4 不得破坏）：**

- 领域包（`shared/domain/...`）不得 import 任何厂商 SDK、Eino、Hertz、数据库驱动。
- Eino 类型不得进入领域 DTO 与数据库：Graph 节点边界完成 `schema.Message` ↔ 领域 DTO 的转换。
- **不引入** `eino-ext/components/model/openai`：否则模型调用绕过项目的能力表、审计与错误模型。
  Eino 需要的 `model.ToolCallingChatModel` 由 `internal/agent/einomodel` 包装项目端口实现
  （PRD §8.2：「Eino 只消费该端口」）。
- 两个服务仍只能通过 `shared/` 共享代码。

### 2.2 目标目录结构（M4 结束时）

```text
shared/
  chat/
    chat.go                  # M0：端口（不改）
    openai/
      types.go               # M4-02：OpenAI 线格式 DTO（包内私有）
      mapping.go             # M4-02：领域 DTO ↔ 线格式、SSE tool_call 分片合并
      client.go              # M4-02：Complete/ChatWithTools/CompleteStructured、重试、错误映射
      stream.go              # M4-02：SSE ChatStream 实现
      capabilities.go        # M4-03：能力表
      client_test.go / mapping_test.go / stream_test.go / smoke_test.go
  store/
    postgres/
      migrations/
        0005_agent_runtime.sql       # M4-07：agent_runs、tool_calls
        0006_conversation_memory.sql # M4-08/09：conversations、conversation_checkpoints、
                                     #          conversation_messages、user_memories
      run_repository.go              # M4-07
      run_repository_test.go
      conversation_repository.go     # M4-08（含消息读写）
      conversation_repository_test.go
      memory_repository.go           # M4-09
      memory_repository_test.go

chat-service/internal/
  config/config.go          # M4-03/04：Chat 能力/超时 + Agent 旋钮
  agent/
    graph.go                # M4-01：Eino Graph 装配与 Runner
    state.go                # M4-01：Graph 业务状态（M4-08 接 checkpoint 持久化）
    nodes.go                # M4-01：ingress/plan/answer/finalize 节点
    einomodel/
      chatmodel.go          # M4-01：Eino ToolCallingChatModel 适配项目端口
    toolreg/
      registry.go           # M4-04：注册表、Schema 校验、超时、未知工具处理
      registry_test.go
    tools/
      search_restaurants.go # M4-05
      restaurant_evidence.go# M4-06
      tools_test.go
    answer/
      compose.go            # M4-10：证据上下文拼装、引用提取与校验
      compose_test.go
    audit/
      hooks.go              # M4-07：节点级审计钩子（token/延迟/工具记录）
  httpapi/
    conversation_handler.go # M4-11：会话与消息 API
    sse.go                  # M4-11：SSE 事件编码与刷新
```

### 2.3 目标接口形状（约定）

#### 2.3.1 Graph 业务状态（M4-01 定义，M4-08 持久化）

```go
// internal/agent/state.go（约定，字段可按实现增补）
type TurnState struct {
    TraceID      string
    ThreadID     string
    UserID       string
    UserInput    string
    Messages     []chat.ChatMessage   // 当轮/历史消息（M4-08 起从库中回放）
    Memories     []memory.Memory      // M4-09 注入
    Intent       string               // M5 细化；M4 先分 chat / restaurant_qa
    Candidates   []search.RestaurantCandidate
    Evidence     []evidence.Evidence
    ToolRounds   int
    FinalAnswer  *chat.Answer         // M4-10：text + citations + follow_ups
    Warnings     []string
}
```

#### 2.3.2 SSE 事件（M4-11，前端契约）

```text
event: message.start  data: {"run_id":"...","thread_id":"..."}
event: message.delta  data: {"delta":"..."}
event: tool.start     data: {"call_id":"...","tool":"search_restaurants"}
event: tool.finish    data: {"call_id":"...","status":"succeeded","latency_ms":83}
event: citation       data: {"evidence_ids":[101,104]}
event: message.end    data: {"finish_reason":"stop","usage":{"input_tokens":...,"output_tokens":...}}
event: error          data: {"code":"provider_unavailable","message":"..."}
```

#### 2.3.3 HTTP 路由（M4-11 新增）

```text
POST   /v1/conversations                 # 创建线程
GET    /v1/conversations/:id             # 线程元数据 + checkpoint 摘要
GET    /v1/conversations/:id/messages    # 消息历史（分页）
POST   /v1/conversations/:id/messages    # 发送消息，Accept: text/event-stream → SSE
GET    /v1/memories                      # M4-09：查看当前用户记忆
DELETE /v1/memories/:memory_id           # M4-09：用户删除记忆
```

---

## 3. 任务清单总览

| 任务 | 名称 | 主要交付 | 依赖 | 量级 |
| --- | --- | --- | --- | --- |
| M4-01 | Eino Graph 骨架 | graph/state/nodes + einomodel 适配；Mock Provider 跑通一轮 | M0 端口 | L |
| M4-02 | OpenAI-Compatible Chat Adapter | openai 适配器：Chat/Stream/Tools/JSON | M0 端口 | M |
| M4-03 | 模型能力表 | capabilities + 配置扩展 + 启动装配 | M4-02 | S |
| M4-04 | 工具注册表 | 注册、JSON Schema 校验、超时、错误结构化 | M0 工具 DTO | M |
| M4-05 | search_restaurants 工具 | 包装 M3 Search | M3-05、M4-04 | M |
| M4-06 | get_restaurant_evidence 工具 | 包装 M3 Evidence | M3-07、M4-04 | M |
| M4-07 | Run 与 Tool Call 审计 | 0005 迁移 + RunRepository PG 实现 + 审计钩子 | M0 端口 | M |
| M4-08 | Conversation/Checkpoint 持久化 | 消息 DTO/端口扩展 + 0006 迁移 + PG 实现 + 恢复 | M4-01、M4-07 | L |
| M4-09 | 记忆检索与注入 | MemoryRepository PG 实现 + 每轮加载注入 + 查询/删除 API | M4-08 | M |
| M4-10 | 回答与引用合成 | 证据 prompt、引用提取/越界校验、无证据拒绝 | M4-06 | M |
| M4-11 | Chat/SSE 端点与会话 API | SSE handler、会话路由、端到端 | M4-01/07/08/10 | L |

### 3.1 依赖图

```text
M4-02 ── M4-03 ─────────────────────┐
                                    ├──> M4-11
M4-01（Mock 可先行）─┐               │
                    ├─> M4-07 ─> M4-08 ─> M4-09 ──┘
M4-04 ─┬─> M4-05    │
       └─> M4-06 ─> M4-10 ─────────┘
```

- M4-01 与 M4-02 完全可并行（前者用 MockChatProvider）。
- M4-04 不依赖任何模型代码，可与 M4-01/02 并行。
- M4-07 只依赖端口与迁移模式，可与工具任务并行。
- 关键路径：M4-02/03 →（与 M4-01 汇合）→ M4-10 → M4-11；M4-08 → M4-11。

### 3.2 推荐执行顺序

**切片 0（首批，本次执行）：M4-02 + M4-03** —— 纯适配器 + 配置，无 Eino 依赖，
完成后用户填 OpenRouter 配置即可跑冒烟测试，提前消除渠道不确定性。

**切片 1（编排骨架）：M4-01 → M4-04 → M4-05 → M4-06 → M4-10**
Mock Provider 下打通「规划→工具→证据→回答」单轮闭环与引用校验（离线可测）。

**切片 2（状态与审计）：M4-07 → M4-08 → M4-09**
迁移、PG 适配器、checkpoint 恢复、记忆注入。

**切片 3（对外端点）：M4-11**
接真实 OpenRouter + 本地 embedding，端到端 SSE，对照 Gate D 验收。

---

## 4. 详细任务

### M4-01 Eino Graph 骨架

**目标**：引入 Eino，建立显式状态机与节点边界；在不依赖任何真实模型的前提下跑通一轮对话。

**交付物：**

1. `go get github.com/cloudwego/eino`（仅核心库；**不引** eino-ext 的厂商 model）。
2. `internal/agent/einomodel/chatmodel.go`：实现 `model.ToolCallingChatModel`
   （`Generate / Stream / WithTools`），内部委托项目的 `chat.ChatProvider / ToolCallingProvider`；
   完成 `schema.Message` ↔ `chat.ChatMessage / tool.ToolCall` 双向映射；tool_call 分片在 Stream 路径按 index 合并。
3. `internal/agent/state.go`：`TurnState`（见 §2.3.1）。
4. `internal/agent/graph.go`：用 `compose.NewGraph` 装配固定节点与边：
   `START → ingress → plan → branch(tool? ) → tools → answer → finalize → END`；
   工具轮次自增，超过 `MaxToolRounds` 强制进入 answer 并附带 warning；
   提供 `Runner.Run(ctx, TurnInput) (TurnResult, error)` 与可流式订阅的事件通道（M4-11 接 SSE）。
5. `internal/agent/nodes.go`：
   - `ingress`：装载线程上下文（M4-08 前为空实现）、记忆（M4-09 前为空）；
   - `plan`：M4 只做最小路由 —— 能直接回答 vs 需要餐厅检索（M5 再扩多槽位规划）；
   - `answer`：M4-10 前先透传模型文本；
   - `finalize`：汇总 usage/warnings（M4-07 在此挂审计钩子，M4-08 在此存 checkpoint）。

**实现要点：**

- 不使用 `flow/agent/react.NewAgent`：它只有 model↔tools 两节点循环，放不下 checkpoint/审计/槽位节点。
  可参考其工具回边模式，但图保持显式（Eino 文档的 Graph + Branch 模式）。
- 图的泛型类型在边界统一为项目 DTO（自定义 Lambda 节点），`schema.Message` 只出现在 einomodel 内。
- 所有节点错误经 `*errs.Error` 透传，不在节点内吞错；provider 故障结束本轮并置 run 失败。
- 事件通道至少包含：delta、tool_start、tool_finish、done、error（形状对齐 §2.3.2）。

**验收标准：**

- MockChatProvider 脚本「无工具直答」与「返回 tool_call → 工具桩 → 二次调用直答」两条路径都有测试；
- `MaxToolRounds` 超限测试（模型持续要求调工具时不死循环）；
- 取消 ctx 时 Run 立即返回且不写成功状态；
- 领域层与 shared 包不出现任何 Eino import（架构测试扩一条断言）。

**工作量：L。依赖：M0 端口/DTO。**

---

### M4-02 OpenAI-Compatible Chat Adapter

**目标**：实现 OpenAI `/chat/completions` 兼容适配器，OpenRouter 作为一个配置实例；
覆盖 Chat / Stream / Tools / JSON 结构化输出，全部映射到项目自有 DTO 与错误模型。

**交付物：**

| 文件 | 内容 |
| --- | --- |
| `shared/chat/openai/types.go` | 线格式 DTO：message、tools、tool_calls、usage、choices、SSE chunk、error 体（包内私有） |
| `shared/chat/openai/mapping.go` | 领域 ↔ 线格式：角色、tool/tool_call_id 消息、ToolSpec→function、finish_reason、usage；tool 参数合法性校验 |
| `shared/chat/openai/client.go` | `Client`、`Options`、`New/NewWithOptions`、Complete/ChatWithTools/CompleteStructured、重试、错误码映射 |
| `shared/chat/openai/stream.go` | SSE 解析 + `ChatStream`；tool_call 分片按 index 累积；`[DONE]`/心跳/error 帧处理 |
| `shared/chat/openai/smoke_test.go` | 真实渠道冒烟测试，默认 skip，env gate 开启 |

**实现要点：**

1. 沿用 `shared/embedding/ollama/client.go` 的全部既有约定：
   - `NewWithOptions(Options{..., HTTPClient, Sleep})`；Timeout 默认 60s、MaxRetries 默认 2、指数退避；
   - 仅 429/5xx/408/传输错误重试；429 尊重 `Retry-After`；
   - 错误响应读 ≤2KB body 片段拼进错误信息（**状态码 + 响应体**透出）；
   - 每次尝试独立 attempt context；调用方取消不重试；
   - 包内编译期端口断言。
2. URL：`{BaseURL}/chat/completions`（trim 尾斜杠）；请求头
   `Authorization: Bearer <api_key>`、`Content-Type: application/json`、`User-Agent: platepilot-chat`，
   再叠加 `ExtraHeaders`（OpenRouter 的 `X-Title / HTTP-Referer` 由此注入）。
3. model 取 `req.Model`，空则用配置模型；OpenRouter 要求 model 必填，缺省即配置校验失败。
4. 错误映射：400→invalid_argument；401/403→unauthorized；402（余额）/404（模型无 endpoint）→provider_unavailable
   且信息中带模型名；超时/取消→provider_timeout；200 但 body 不可解码或 tool arguments 非合法 JSON→provider_unavailable。
5. Stream：`stream:true`；**不套 60s 总超时**（由调用方 ctx 控制整体，建连/等头受 Timeout 约束）；
   bufio 行缓冲放大到 1MB；忽略 `:` 心跳；每个文本 delta 直出；
   tool_calls 按 `index` 累积（首片带 id/name，arguments 逐片拼接），ChatChunk 输出累积快照；
   data 帧携带 `error` 对象时 Recv 返回 `*errs.Error`；`[DONE]` 正常 EOF；Close 幂等并归还 body。
6. 结构化输出：能力开启时发
   `response_format:{type:"json_schema",json_schema:{name:"result",strict:true,schema}}`；
   能力关闭时退化为 `json_object` + 在消息中要求按 schema 输出；对结果做 `json.Valid`，
   失败以 temperature=0 重试 1 次，再失败返回 validation_failed。
7. `ProviderOptions` 只识别两个保留键：`extra_body`（map 浅合并进请求顶层，OpenRouter 专有参数以后走这里）
   与 `extra_headers`（map 追加请求头）；未知键忽略。渠道专有结构不得扩散出适配器。
8. 暴露 `ModelID()` 供 M4-07 审计记录模型名。
9. 零新增第三方依赖（stdlib net/http + encoding/json + bufio）。

**测试（全部用 `http.RoundTripper` 离线 seam，不用 httptest 监听端口 —— 沙箱限制，沿用 ollama 测试约定）：**

- Complete 请求头/体形状、响应、usage、finish_reason 映射；
- tools 序列化与 tool_calls 响应映射；非法 arguments 报错；
- 400/401/402/404/429/500/超时/断网的错误码与重试次数；
- SSE：文本多帧、tool_call 参数分片、心跳、`[DONE]`、error 帧、Close 幂等；
- structured：json_schema 请求形状、非法 JSON 重试一次、能力关闭退化路径；
- extra_body/extra_headers 透传、未知键忽略。

**冒烟测试（用户配好 .env 后手动执行）：**

```bash
PLATEPILOT_TEST_CHAT=1 go test ./shared/chat/openai/ -run Smoke -count=1 -v
# 用例：① 普通 Complete 一轮；② 给一个 get_weather 玩具工具，断言模型返回结构化 tool_calls；
#       ③ Stream 至少收到一个 chunk。直接读 CHAT_BASE_URL/CHAT_API_KEY/CHAT_MODEL，未配置即 skip。
```

**工作量：M。依赖：M0 端口。参考：[OpenAI Chat Completions](https://platform.openai.com/docs/api-reference/chat)、
[Eino ToolsNode](https://www.cloudwego.io/docs/eino/core_modules/components/tools_node_guide/)（SSE tool_call 分片语义一致）。**

---

### M4-03 模型能力表

**目标**：把「这个模型会什么」从代码猜测变成显式配置；能力不足时在本地拒绝，不把无效请求发给渠道。

**交付物：**

1. `shared/chat/openai/capabilities.go`：
   ```go
   type Capabilities struct {
       Tools         bool
       ParallelTools bool
       JSONSchema    bool
       Streaming     bool
       ContextTokens int
   }
   ```
   `SupportsTools/SupportsParallelTools/SupportsJSONSchema()` 直接读它；
   `ChatWithTools` 在 Tools=false 时**不发 HTTP**，返回 invalid_argument（错误信息提示检查 CHAT_SUPPORTS_TOOLS 与模型选择）；
   ParallelTools=false 时请求体置 `parallel_tool_calls:false`。
2. 扩展 `chat-service/internal/config` 的 ChatConfig（加载、默认值、校验、Summary；key 继续脱敏）：

   | 变量 | 默认 | 说明 |
   | --- | --- | --- |
   | `CHAT_TIMEOUT` | `60s` | 单次非流式请求超时，必须 >0 |
   | `CHAT_MAX_RETRIES` | `2` | 额外重试次数，≥0 |
   | `CHAT_SUPPORTS_TOOLS` | `true` | 模型支持 function calling |
   | `CHAT_SUPPORTS_PARALLEL_TOOLS` | `false` | 并行工具调用（小模型默认关） |
   | `CHAT_SUPPORTS_JSON_SCHEMA` | `false` | 严格 json_schema（OpenRouter 模型差异大，默认关，走退化路径） |
   | `CHAT_CONTEXT_TOKENS` | `32768` | 模型上下文窗口，>0 |

3. `app.Connect()`：`cfg.Chat.Enabled()` 时构建适配器，同一 client 赋给
   `Deps.Chat / ToolCalling / Structured`；构建失败或配置缺失 → **启动失败**
   （聊天是 M4 主路径，错配应当即报错，不做 embedding 那样的静默降级）。启动日志输出
   provider/base_url/model/capabilities（不含 key）。
4. 同步更新 `.env.example` 与配置测试。

**验收标准：**

- 能力开关的默认值/覆盖/非法值（timeout=0、retries=-1、context=0）均有测试；
- Tools=false 时 ChatWithTools 不产生 HTTP 请求（RoundTripper 计数为 0）；
- check-config 输出 chat 段；启动日志 key 仍为 `***`。

**工作量：S。依赖：M4-02。**

---

### M4-04 工具注册表

**目标**：Agent 与工具之间的唯一中介；统一 Schema 暴露、参数校验、超时与错误形状。

**交付物：**

1. `internal/agent/toolreg/registry.go`：
   ```go
   type Handler func(ctx context.Context, args json.RawMessage) (tool.ToolResult, error)
   type Entry struct { Spec tool.ToolSpec; Handler Handler; Timeout time.Duration }
   type Registry interface {
       Register(e Entry) error
       Specs() []tool.ToolSpec
       Invoke(ctx context.Context, call tool.ToolCall) tool.ToolResult // 永不 panic，错误收敛进 ToolResult
   }
   ```
2. Register 校验：name 唯一且匹配 `^[a-z][a-z0-9_]{1,63}$`；Parameters 必须是合法 JSON Schema（object）；
   重复注册报错。
3. Invoke 行为：未知工具 → ToolResult{IsError:true, Code:not_found}；参数 JSON 非法 / 不符 Schema
   （必填缺失、类型不符、额外属性策略）→ validation_failed；处理器 panic  recover 为 internal；
   单工具超时（默认 `AGENT_TOOL_TIMEOUT=15s`）→ provider_timeout 语义的工具错误（不让单个工具挂死整轮）。
4. 生成 Eino `tool.InvokableTool`：从 Entry 生成 `ToolInfo`（ParamsOneOf 用 schema JSON），
   InvokableRun 内部调 Registry.Invoke 并把 ToolResult 序列化为工具消息内容；Eino 类型只出现在该文件。
5. 配置：`AGENT_MAX_TOOL_ROUNDS`（默认 5，M4-01 使用）、`AGENT_TOOL_TIMEOUT`（默认 15s）。

**验收标准：**

- 用测试 Schema 覆盖：合法参数通过、缺必填/类型错误/非法 JSON/未知工具/重名/超时/panic 六类异常；
- 工具错误是结构化结果而不是节点 error 中断整轮（除超时外模型可看到错误并自我修正）；
- `Specs()` 输出可直接作为 ChatWithTools 的入参。

**工作量：M。依赖：M0 工具 DTO。**

---

### M4-05 search_restaurants 工具

**目标**：把 M3-05 的混合检索暴露给模型，作为餐厅发现工具。

**交付物：** `internal/agent/tools/search_restaurants.go`

- Schema（参数）：`query`（自然语言，可选）、`borough`、`cuisine`、`min_rating`、`open_now`、
  `top_k`（1..10，默认取配置 top_k）。
- 处理器：映射为 `search.RestaurantFilter`（复用 `Filter.Validate()`，borough 白名单、min_rating 精度都走既有逻辑）
  与 `retrieval.Request` 调 `Service.Search`；返回紧凑 JSON：
  餐厅 id/name/borough/cuisine/address/rating + trace 的 channels/warnings 摘要；
  不把全部评分明细倾倒给模型。
- scope 约束：top_k 被钳制；空结果是正常工具结果（带提示语），不是错误。
- 审计：调用成功后把候选数、通道降级 warnings 交审计钩子（M4-07 消费）。

**验收标准：**

- 用内存/假检索端口构造单测：参数→retrieval.Request 映射、非法 borough 返回 validation_failed、
  top_k 钳制、空结果与降级 warnings 透传；
- 工具描述（description）为英文函数说明 + 参数示例，保证小模型也能稳定选中。

**工作量：M。依赖：M3-05、M4-04。**

---

### M4-06 get_restaurant_evidence 工具

**目标**：给定候选餐厅，取回带 ID 的证据片段，作为回答中**唯一合法**的引用来源。

**交付物：** `internal/agent/tools/restaurant_evidence.go`

- Schema：`restaurant_ids`（必填、1..5 个 int64）、`query`（用户原话，必填）、`topic`（可选）、
  `doc_types`（可选枚举）。
- 处理器：调 `Service.Evidence(EvidenceRequest{...})`；结果按片段输出：
  `evidence_id / restaurant_id / doc_type / source / title / content / token_count`；
  总量受 M3 既有 token 预算控制。
- 边界：restaurant_ids 缺失直接复用检索层 `retrieval_no_scope` 错误（M3 已保证不会无界召回）；
  证据 ID 必须原样回传，禁止工具层重排后丢 ID。

**验收标准：**

- 单测：多餐厅证据汇聚、无 id 报错、doc_types 过滤、结果中每个片段都带可引用 ID；
- 与 M4-05 串联的测试：search 选店 → evidence 取证明。

**工作量：M。依赖：M3-07、M4-04。**

---

### M4-07 Run 与 Tool Call 审计

**目标**：Gate D 第 4 条。每轮 run 与每次工具调用落库可查。

**交付物：**

1. `0005_agent_runtime.sql`：
   - `agent_runs`：trace_id（唯一）、thread_id、run_id（PK）、status、model_provider、model_name、
     started_at/finished_at、latency_ms、token_input/output、retrieval_count、tool_call_count、error_code；
     thread_id 索引、started_at 索引。
   - `tool_calls`：call_id（PK）、run_id（FK→agent_runs ON DELETE CASCADE）、tool_name、
     arguments（jsonb，**只存脱敏摘要**）、result_summary、status、latency_ms、created_at；run_id 索引。
2. `shared/store/postgres/run_repository.go`：实现 `Start / Finish / RecordToolCall`
   （upsert 语义、时间与计数由调用方给全）；内存实现已有，按同一契约对齐。
3. 契约套件：新增 `shared/store/contract/run.go`（或沿用现有契约文件风格），memory 与 postgres 跑同一套；
   `PLATEPILOT_REQUIRE_DB=1` 才跑 postgres（既有约定）。
4. `internal/agent/audit/hooks.go`：Runner 集成 —— run 开始写 Start；finalize 汇总
   模型 usage（累加多轮）、总延迟、检索次数、工具次数后 Finish；工具节点每次执行 RecordToolCall
   （arguments 经白名单/截断脱敏：只保留非敏感业务字段，联系方式类不入库）。

**验收标准：**

- 契约测试覆盖 running→succeeded/failed 状态流转、工具记录外键、缺失 run 的工具调用报错；
- EXPLAIN/索引至少覆盖按 thread_id 查最近 runs；
- 端到端（切片 3）后能用 SQL 列出一轮对话的完整审计行；
- 审计写入失败不得让用户请求失败：记 error 日志并继续（审计是旁路；但 Start 失败要在日志显式告警）。

**工作量：M。依赖：M0 端口、迁移模式（M1-01）。**

---

### M4-08 Conversation 与 Checkpoint 持久化

**目标**：Gate D 第 5 条。线程元数据、消息历史、可恢复 checkpoint 全部落 PostgreSQL。

**交付物：**

1. **补一个 M0 遗漏的领域概念**：`shared/domain/conversation/message.go`
   ```go
   type Message struct {
       MessageID  string
       ThreadID   string
       Role       string // user / assistant / tool
       Content    string
       ToolCalls  []tool.ToolCall    `json:",omitempty"`
       EvidenceIDs []int64           `json:",omitempty"`
       CreatedAt  time.Time
   }
   ```
   端口扩展两个方法（同步更新内存实现）：
   `AppendMessage(ctx, conversation.Message) error`、
   `ListMessages(ctx, threadID string, limit, beforeID string) ([]conversation.Message, error)`。
   说明：M0 只定义了线程与 Checkpoint，但 M4-11 的对话 API 与 M5-09 的历史回放必须有消息存储，
   在本任务补齐比塞进 checkpoint JSON 更符合既有规范化建模约定。
2. `0006_conversation_memory.sql`：
   - `conversations`：thread_id PK、user_id、title、current_state、created_at/updated_at/last_message_at；
   - `conversation_checkpoints`：thread_id PK/FK、version（乐观锁）、state、pending_action、missing_slots(text[])、
     evidence_ids(bigint[])、selected_restaurant_id、created_at；`UPDATE ... WHERE version=$n` 防并发覆盖；
   - `conversation_messages`：message_id PK、thread_id FK CASCADE、role、content、tool_calls(jsonb)、
     evidence_ids(bigint[])、seq（每线程自增）、created_at；(thread_id, seq) 索引支撑历史回放；
   - `user_memories`（M4-09 使用）：memory_id PK、user_id、memory_type、content、source、confidence、
     embedding vector(1024) NULL、created_at/updated_at/deleted_at；(user_id, deleted_at) 索引。
3. `shared/store/postgres/conversation_repository.go`：Get/Upsert/SaveCheckpoint/LoadCheckpoint/AppendMessage/ListMessages；
   旧 checkpoint 按版本保留一行（thread_id PK，覆盖更新；恢复只认最近版本）。
4. Runner 接线：ingress 加载 Conversation + 最近 Checkpoint + 最近 N 条消息；
   finalize 保存：用户消息、助手消息、新 checkpoint（state、证据 id、缺失槽位）；
   提供 `Runner.Resume(ctx, threadID)` 显式恢复入口（M5 HITL 直接复用）。

**验收标准：**

- 契约测试：线程 upsert、checkpoint 版本冲突、消息追加与分页、恢复读出的 state/evidence_ids 与写入一致；
- 集成测试：跑到一半构造 finalize 前失败 → Resume 能读到 pending_action 与 missing_slots；
- 不存在跨线程串消息的可能（所有查询都带 thread_id 条件并在测试中断言）。

**工作量：L。依赖：M4-01（Runner 有 finalize 可挂）、M4-07（迁移顺序）。**

---

### M4-09 记忆检索与注入

**目标**：每轮自动带上用户长期偏好/约束；记忆对用户可见、可删；**M4 不做自动写记忆**
（「只保存用户明确要求记住的内容」是 M5 策略任务，本任务只提供读注入与 CRUD）。

**交付物：**

1. `shared/store/postgres/memory_repository.go`：List/Upsert/Delete（软删 deleted_at）；
   List 只返回未删除记忆，按 updated_at desc 限量。
2. `ingress` 节点：按 user_id 加载记忆，按类型拼装为系统上下文段
   （preference/constraint 分组、限量，超出窗口的低置信度记忆截断并记 warning）。
3. HTTP：`GET /v1/memories`、`DELETE /v1/memories/:id`（M4-11 一起接线；user_id 暂用请求头/占位主体，
   与 M3 接口无真实鉴权的现状一致，M6 接正式身份）。
4. 内部写入能力保留给 M5：本里程碑不暴露 save_memory 工具。

**验收标准：**

- 契约测试覆盖 upsert/软删/List 过滤；
- 注入测试：约束（如「不吃辣」）出现在发给模型的系统消息中，且记忆超量时按置信度截断；
- 用户删除后后续轮次不再注入。

**工作量：M。依赖：M4-08。**

---

### M4-10 最终回答与引用合成

**目标**：Gate D 第 6 条。回答只基于当轮证据，引用可核验，无证据时拒绝编造。

**交付物：** `internal/agent/answer/compose.go`

1. 证据上下文拼装：系统指令明确「只能使用 <evidence> 中的内容；每个事实性结论后标注 [^证据id]；
   信息不足时直接说明并给出可追问方向」；证据按 doc_type 分组、带元数据（餐厅名/来源/时间）。
2. 模型输出后处理：提取引用 ID 集合，与当轮 `TurnState.Evidence` 的合法 ID 集合做差集：
   - 越界 ID → 视为运行时错误（provider_unavailable 归类的「模型违约」），触发**一次**带纠错指令的重试；
   - 重试仍越界 → 本轮失败并在 SSE error 中给出明确 code（新增 `agent_citation_violation`，
     加在 errs 中，映射 422）。
3. 无证据路径：当轮没有任何证据却被要求事实性餐厅问题时，走固定拒答模板（不调第二次生成）；
   寒暄/无证据也能回答的问题不受限（plan 节点区分）。
4. 输出 `chat.Answer{Text, Citations []int64, FollowUps []string}`（Answer DTO 放 domain/chat；
   follow_ups 由模型一次生成，最多 3 条）。

**验收标准：**

- 单测：正常引用、越界引用触发一次重试并在第二次通过、两次都越界返回 citation_violation、
  无证据拒答、证据 ID 去重与排序；
- 任何成功回答都满足 citations ⊆ 当轮证据 ID（表驱动断言）。

**工作量：M。依赖：M4-06（有证据工具）。**

---

### M4-11 Chat/SSE 端点与会话 API

**目标**：Gate D 第 1 条。对外可用的会话 API 与流式事件。

**交付物：**

1. 路由（§2.3.3）：创建线程、查线程、消息历史分页、发消息（SSE）、记忆查询/删除。
2. `httpapi/sse.go`：`Content-Type: text/event-stream`、心跳（15s 注释帧）、
   按 §2.3.2 编码 Runner 事件；客户端断开时取消 ctx 联动取消 run；
   统一 error 事件携带 errs code/message（HTTP 头已 200 时的运行中错误也走 error 事件）。
3. `conversation_handler.go`：请求 DTO 与校验（消息非空、长度上限）、idgen 生成 thread_id/run_id；
   每请求挂 request_id 与 run 审计的 trace 关联（M4-07）。
4. Hertz SSE：优先 `github.com/hertz-contrib/sse`；若其 flush 语义不满足工具事件实时性，
   退化为直接操作 `RequestContext` flush（实现时验证，二选一，不引入两套）。
5. CORS：SSE 路径纳入现有 CORS 中间件配置。
6. 端到端验证脚本/命令放附录 B：curl 发消息观察事件序列。

**验收标准：**

- HTTP 层单测（离线，mock Runner）：建线程→发消息→事件顺序
  start → (tool.start/finish)* → citation → end；错误事件 code 正确；客户端断开 run 被取消；
- 真实环境（OpenRouter + 本地 embedding）手工跑通：
  「曼哈顿中城适合约会的日餐，预算中等」→ 两次工具调用 → 带引用流式回答；
- 历史消息 API 可回放完整线程；中断恢复（M4-08）经 API 可验证。

**工作量：L。依赖：M4-01、M4-07、M4-08、M4-10（M4-09 同批）。**

---

## 5. 推荐执行顺序与并行化

### 5.1 单人执行（本次开始的顺序）

```text
M4-02 → M4-03                # 切片 0：适配器 + 能力表（本次首批，完成后联 OpenRouter）
M4-01 → M4-04                # 切片 1 起：编排骨架与工具机制
M4-05 → M4-06 → M4-10        # 工具与回答（Mock 闭环）
M4-07 → M4-08 → M4-09        # 切片 2：审计、状态、记忆
M4-11                        # 切片 3：SSE 端到端 + Gate D
```

### 5.2 并行建议

- M4-01 与 M4-02 无依赖，可并行；M4-04 也可并行起步。
- M4-07 的迁移与适配器不经过 Agent，可与工具开发并行。
- 单开发者时按 5.1 串行，每完成一个任务跑一次全量离线测试再进入下一个。

### 5.3 纵向切片（每片都可演示）

1. **渠道切片**：M4-02+03 —— 用冒烟测试向 OpenRouter 完成真实对话与工具调用（无 UI、无 Agent）。
2. **智能切片**：M4-01+04+05+06+10 —— Mock 模型下「检索→证据→引用回答」离线闭环。
3. **产品切片**：M4-07+08+09+11 —— 浏览器可对话、可恢复、可审计。

---

## 6. M4 完成定义（DoD）检查清单

### 6.1 每个任务通用 DoD（继承实施计划 §10）

- [ ] 任务目标对应的验收标准全部满足。
- [ ] 新增公开接口有用法注释；包注释说明分层位置。
- [ ] 领域层无厂商/框架依赖；Eino 类型不跨出 agent 包；厂商线格式不跨出 openai 包。
- [ ] 新增配置有默认值、校验、check-config 输出与 `.env.example` 文档；密钥只从环境读取且日志脱敏。
- [ ] 应用层错误使用 `*errs.Error`，HTTP 层只做 code→status 映射。
- [ ] 单测覆盖成功路径与主要失败路径；数据库测试纳入 memory+postgres 契约套件并带 env gate。
- [ ] 涉及 SQL 的任务补迁移文件与索引/EXPLAIN 检查。
- [ ] `go build ./... && go vet ./... && go test ./...` 全绿；无新增第三方依赖时在提交说明注明。
- [ ] tsc 与 web 测试不受影响（M4 主要是后端）。

### 6.2 M4 里程碑门（Gate D）

- [ ] 1. 创建线程 + SSE 收发完整事件流（M4-11）。
- [ ] 2. 能力不足在请求前拒绝，错误码明确（M4-03）。
- [ ] 3. 工具参数 JSON Schema 校验；未知工具结构化错误（M4-04）。
- [ ] 4. Run 审计完整：模型、token、延迟、工具、参数摘要、证据 ID、回答/错误码（M4-07）。
- [ ] 5. checkpoint 恢复线程状态（M4-08）。
- [ ] 6. 引用只来自当轮证据（M4-10）。
- [ ] 7. 全量离线测试通过；真实渠道/检索下端到端演示并记录首 token 延迟。

---

## 7. 与后续里程碑的衔接

- **M5（对话状态与多轮规划）**：消费 M4-01 的 plan 节点扩展多槽位意图；消费 M4-08 的 checkpoint 做
  awaiting_clarification / awaiting_confirmation / HITL；启用 M4-09 预留的记忆写入并加确认策略；
  消息历史直接来自 conversation_messages。
- **M6（质量与前端）**：Run 审计是 M6-03 评测数据来源；SSE 事件是 M6-06 前端对话流的契约；
  能力表与首 token 记录支撑 M6-03 门限。
- **M7（集成/部署）**：OpenRouter key 进入部署密钥管理；CHAT_* 是部署模板必填段。

---

## 8. 风险与注意事项

1. **OpenRouter 模型差异**：模型 ID 填错返回 404（no endpoints）、余额不足 402、区域/权限 403 ——
   适配器必须透出状态码与 body，冒烟测试第一步即暴露；模型名纯配置，禁止在代码里兜底猜测/自动换模型。
2. **模型实际不支持 tools**：能力表默认值与所选模型要匹配；并行工具默认关；
   冒烟测试的工具用例是上线前的硬检查。
3. **SSE tool_call 分片**：OpenAI 系流式协议按 index 增量给 id/name/arguments，
   拼装逻辑必须有专项分片测试，否则会出现「工具名偶发为空」类随机故障。
4. **流式请求被超时误杀**：流式调用不能套普通请求总超时；建连超时与整体 ctx 分离。
5. **Eino 版本 API 变动**：以落地时 `github.com/cloudwego/eino` 最新稳定版 API 为准；
   适配集中在 einomodel 与 toolreg 两个文件，升级影响面可控。
6. **审计脱敏**：tool_calls.arguments 可能包含用户输入的敏感信息，入库前白名单截断；
   错误响应片段不回显 Authorization 头。
7. **审计旁路化**：审计写失败只告警不阻断用户请求，但 Start 连续失败必须在日志/健康检查中可见。
8. **checkpoint 并发**：同一线程并发发消息靠 version 乐观锁拒绝后写；MVP 不做线程队列，返回 conflict。
9. **引用可信是硬失败**：模型两次输出越界引用即本轮失败，不要静默删除引用后放行（那会让无据回答流出）。
10. **本地 embedding 依赖**：M4-05/06 真实链路需要 Ollama 在线；离线测试全部用假端口，不因此挂红。

---

## 附录 A：环境变量（M4 相关）

```bash
# --- Chat Provider（M4 起必需）---------------------------------------------
CHAT_PROVIDER=openai_compatible
CHAT_BASE_URL=https://openrouter.ai/api/v1
CHAT_API_KEY=<在 OpenRouter 控制台申请，只填进 .env，不进仓库>
CHAT_MODEL=<OpenRouter 上支持 function calling 的模型 ID，例如厂商/模型名形式>
# OpenRouter 归因头（可选）
# CHAT_EXTRA_HEADERS_JSON={"X-Title":"PlatePilot","HTTP-Referer":"http://localhost:8080"}
# 单次非流式请求超时 / 重试
# CHAT_TIMEOUT=60s
# CHAT_MAX_RETRIES=2
# 模型能力表（必须与所选模型实际能力一致）
# CHAT_SUPPORTS_TOOLS=true
# CHAT_SUPPORTS_PARALLEL_TOOLS=false
# CHAT_SUPPORTS_JSON_SCHEMA=false
# CHAT_CONTEXT_TOKENS=32768

# --- Agent 运行时（M4-01/04）-----------------------------------------------
# AGENT_MAX_TOOL_ROUNDS=5      # 单轮对话内最多工具调用轮次（防模型死循环）
# AGENT_TOOL_TIMEOUT=15s       # 单个工具执行超时

# --- 复用 M3 的检索配置（search/evidence 工具依赖）--------------------------
# EMBEDDING_PROVIDER=ollama
# OLLAMA_BASE_URL=http://localhost:11434
# EMBEDDING_MODEL=qwen3-embedding:0.6b
# EMBEDDING_DIMENSIONS=1024
# RETRIEVAL_TOP_K=5
```

## 附录 B：常用命令速查

```bash
# 0. 基线检查（全程离线）
go build ./... && go vet ./... && go test ./...

# 1. 配置自检（chat 段出现即接线成功）
go run ./chat-service check-config

# 2. OpenRouter 冒烟（用户填好 .env 后）
PLATEPILOT_TEST_CHAT=1 go test ./shared/chat/openai/ -run Smoke -count=1 -v

# 3. 数据库契约测试（M4-07/08/09）
PLATEPILOT_REQUIRE_DB=1 go test ./shared/store/postgres/... -count=1

# 4. 启动服务
docker compose -f deploy/docker-compose.yml up -d
go run ./data-pipeline migrate
go run ./chat-service serve

# 5. 端到端：建线程 → 流式发问（M4-11 后）
#    M6 鉴权前用 X-User-ID 头占位身份；记忆接口强制要求，对话接口允许匿名。
# 5.1 建线程（返回 JSON，记下 thread_id）
curl -s -X POST http://localhost:8080/v1/conversations \
  -H 'Content-Type: application/json' \
  -H 'X-User-ID: demo-user' \
  -d '{"title":"nyc date night"}'
THREAD_ID=<上一步返回的 thread_id>

# 5.2 发消息并观察 SSE 事件序列（-N 关闭缓冲）
#     预期帧序：message.start → (tool.start/tool.finish)* → message.delta*
#              → citation → message.end；空闲时每 15s 收到 “: keep-alive” 注释帧；
#     中途失败则在流内收到 event: error（code/message）。
curl -N -X POST "http://localhost:8080/v1/conversations/${THREAD_ID}/messages" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -H 'X-User-ID: demo-user' \
  -H 'X-Trace-ID: trace-demo-001' \
  -d '{"content":"曼哈顿中城适合约会的日餐，预算中等，要 4 星以上"}'

# 5.3 线程元数据（含最新 checkpoint 摘要）与历史回放（游标分页）
curl -s "http://localhost:8080/v1/conversations/${THREAD_ID}" \
  -H 'X-User-ID: demo-user'
curl -s "http://localhost:8080/v1/conversations/${THREAD_ID}/messages?limit=20"
#   翻页：?limit=20&before_id=<上一页最早一条 message_id>

# 5.4 记忆查询 / 删除（无 X-User-ID 返回 400）
curl -s http://localhost:8080/v1/memories -H 'X-User-ID: demo-user'
curl -s -o /dev/null -w '%{http_code}\n' -X DELETE \
  http://localhost:8080/v1/memories/<memory_id> -H 'X-User-ID: demo-user'   # 204

# 6. 审计核对（M4-07 后）
psql "$POSTGRES_DSN" -c \
  "SELECT run_id,status,model_name,latency_ms,token_input,token_output,tool_call_count FROM agent_runs ORDER BY started_at DESC LIMIT 5;"
psql "$POSTGRES_DSN" -c \
  "SELECT tool_name,status,latency_ms,result_summary FROM tool_calls ORDER BY created_at DESC LIMIT 10;"
```

## 附录 E：实施记录（M4 实际落地时的发现）

> 按 §0 的约定，落地与计划的差异记在这里，不回头改原计划。本节记录 M4 交付后围绕
> 「让一轮对话在结束之前就能被看见」所做的运行时修补，以及其中一次真实故障的根因。

### E.1 事件契约新增两类帧（§2.3.2 的封闭集之外）

一轮真实对话要 1–2 分钟，其中 `plan` 与 `answer` 各是一次几十秒的同步 LLM 调用。计划里的
事件集（`message.start/delta/replace`、`tool.start/finish`、`citation`、
`state.awaiting_input`、`confirmation.required`、`memory.saved`、`message.end`、`error`）
在这段静默里一个字节都不产生，前端只能显示一个没有内容的「运行中」。因此新增两帧，
并同步 `web/src/api/chat.ts` 的 `StreamEvent` 联合类型、`useAgentTurn` 的 reducer 与
`isStructuralEvent` 封闭集、以及 `sse_event_test.go` 的 `everyStreamEventType()`：

| 事件 | 用途 | 为什么不是别的 |
| --- | --- | --- |
| `phase.progress` | 改写正在运行的 phase 行的标题（`正在推理（已 N 秒）`），按 `phase_id` 匹配 | 不新开一行：`plan` 会随 `plan ⇄ tools` 循环重复出现，新开行会让"循环"看起来像并列步骤 |
| `thinking.delta` | 推理模型在首个答案 token 之前的 `reasoning` / `reasoning_content` 增量，独占一条通道 | 不与 `message.delta` 合并：它是「模型在想」而不是「模型在说」，混进答案正文就是伪造引用文本 |

### E.2 与计划的偏差

| 计划 | 实际 | 原因 |
| --- | --- | --- |
| `agent_runs` 只记 `error_code`（M4-07） | 新增 `error_message` 列（迁移 `0013_run_error_message.sql`），`Finish` / `scanRun` / `runColumns` 同步；`Start` 的 `ON CONFLICT DO UPDATE` 把它重置为空；契约用例 `running_to_failed` 补写读断言 | 只有 `error_code` 时分不清「模型被下架」和「代码写错了」，两者都落成 `provider_unavailable` |
| 失败原因只进审计表 | `Runner.Deps` 增加 `Logger`，`invoke` 失败分支打一条 `agent run failed`（`run_id` / `thread_id` / `trace_id` / `code` / `error`） | 审计行的 message 被截断到 512 runes（`maxMessageChars`），完整原因得有个地方看 |
| `agent.Config` 没有流式开关 | `PLATEPILOT_ANSWER_STREAMING`（默认 true）、`AGENT_PHASE_EVENTS`（默认 true） | 沿用 §5 的约定：影响观感与线宽的决策放环境变量，不重编译即可回退 |
| 答案按行 / 整块下发 | `lineEmitter` 逐段下发（`answer/compose.go`，UTF-8 安全） | 按行缓冲会让答案「整行整行」地跳出来，正好抵消流式的意义 |
| — | `startPhaseProgress` / `publishProgress`，`progressHeartbeatInterval = 3s`（`nodes_instrument.go`），`plan` 与 `answer` 两处接入 | 3s 是心跳密度与通道容量的取舍：1s 会在长轮里灌满 128 帧的事件通道，3s 足够让用户看见行在动 |
| — | 根 Makefile 增加 `ollama-up` / `ollama-down` / `ollama-logs` / `ollama-pull` | 本地 embedding 自 M3 起就是前置，之前只能手敲 `ollama serve` |

**两条必须记住的约束**：

1. `startPhaseProgress` 在 `currentPhaseID(ctx) == ""` 时**返回 nil 且不发任何帧**。也就是说
   `AGENT_PHASE_EVENTS=false` 时，心跳与 phase / step 帧一起静默——开关只有一个，不存在
   「关了 phase 帧但还在发心跳」的中间态。
2. 关掉这两个开关**不改变答案本身**，只改变线宽，所以它们可以随时回退而不动数据。

### E.3 一次真实故障：前端把整个流丢掉了（2026-10-06）

**现象**：一轮对话在前端没有任何中间过程，等 1–2 分钟后所有信息（提问、回答、候选、
run 明细）一次性出现。

**排查结论**：服务端、代理、事件序列全部正常——直接 `curl` `:8080` 与经 Vite 代理
`curl` `:5173`，都能在 t≈0 收到 `message.start` / `phase.started`，随后每 3s 一帧
`phase.progress`；响应头干净（`transfer-encoding: chunked`，无 `Content-Encoding` /
`Content-Length`，带 `x-accel-buffering: no`）。问题在浏览器侧。

**根因**：服务端把事件名放在 SSE 的 `event:` 行上——`sse.go` 的 payload 结构体里
**没有** `type` 字段——而前端 `sendMessage` 只做了 `JSON.parse(frame.data) as StreamEvent`，
把 `frame.event` 丢掉了。于是 `reduce()` 的 `switch (event.type)` 对每一帧都读到
`undefined`、一律走 `default`，`isStructuralEvent()` 也一律返回 false：**整个流被静默丢弃**。
用户最后看到的一切，都来自 turn 结束时 `onFinished → writeTurn()` 触发的那次重新拉取，
「突然一次性出现」正是这次 refetch 的形状。

**为什么测试没拦住**：现有用例要么 mock 掉 `sendMessage`（`AgentConsole.test.tsx`），要么
直接构造带 `type` 的对象喂给 `reduce`（`useAgentTurn.test.ts`）。**「SSE 帧 → 带 `type`
的事件」这条缝隙没有任何用例覆盖**，而它的失败模式是静默的——没有任何断言会变红。

**修复与守卫**：`web/src/api/chat.ts` 把 `frame.event` 作为 `type` 合回 payload；新增
`web/src/api/chat.test.ts`，走真实的
`fetch → ReadableStream → readSSEStream → sendMessage` 全链路，断言事件顺序与 `type`、
payload 字段，以及「非 JSON 帧被跳过但不终止本轮」。

### E.4 验证记录（2026-10-06）

- 前端：`npx tsc --noEmit` 无输出；`npx vitest run` **22 个文件 124 个用例全绿**。
- 浏览器实测（`http://localhost:5173/agent`）：发送后约 5 秒，主对话区与状态栏已是
  「运行中　正在加载会话上下文」，之后才出现回答文本——中间过程可见。
- 帧序实测（`curl -N`）：

  ```
  message.start → phase.started(ingress-1) → step.started/finished ×3
    → phase.finished(ingress-1) → phase.started(plan-1) → phase.progress(plan-1)
    → ... → phase.started(answer-1) → message.delta → phase.finished(answer-1)
    → message.end
  ```
- Go 侧本轮未改动；`make test` / `vet` / `build` 保持绿。

**事件契约变更表**：`docs/platepilot-agent-optimization-plan.md` 的「附录 A」已同步
`thinking.delta` 与 `phase.progress` 两行（2026-10-06）。

### E.5 一次真实故障：200 体里的 error 信封被读成「没有 choices」（2026-10-06）

**现象**：Agent 验证台一轮对话直接失败，没有进入任何 phase 的后续步骤：

```
[NodeRunError] provider_unavailable: openai: model "nvidia/nemotron-3-ultra-550b-a55b:free" returned no choices
node path: [plan]
```

**排查**：用同样的 base URL 与模型直接发一次非流式请求，OpenRouter 返回的是 **HTTP 200**，
body 是一个 error 信封、根本没有 `choices`：

```json
{"id":"gen-...","error":{"message":"Upstream error from Nvidia: Service temporarily overloaded",
 "code":503,"metadata":{"error_type":"provider_overloaded"}}}
```

`attempt` 只按状态行分类，因此把「路由到的上游过载」当成协议异常处理：真正原因被丢掉，
`retryable` 还被判成 `false`，第一次尝试失败就结束——整轮对话死在 `plan`。这也是为什么
同一个模型偶尔能用：过载是间歇的，而这条路径恰恰是唯一不重试的那条。

**修复**（`shared/chat/openai`）：

| 改动 | 位置 |
| --- | --- |
| `completionResponse` 新增 `Error *apiErrorBody`（200 体里的 error 信封，用指针区分「没有」与「空」） | `types.go` |
| `attempt` 先认 error 信封：把上游 message 连同模型名一起报出；是否重试由信封里的 code 决定（429/408/5xx 重试，其余 4xx 不重试，缺 code 视为可重试） | `client.go` |
| 「只有空 `choices`」也改为可重试——过载路由就是这样丢响应的，重试才是恢复手段 | `client.go` |
| 抽出 `retryableStatus` / `codeAsInt`，`classifyError` 复用同一判定 | `client.go` |

**流式路径**（`answer` 阶段的同一故障）：`decodeFrame` 本来就能报出真实原因
（实测 `openai: stream error: Upstream error from Nvidia: ...`），但 `Client.Stream`
的注释里写着「pre-flight 失败一律不重试」（理由是失败的请求在计费层可能不幂等），
所以同一个过载在流式上依然会打断回答。经确认改为**只在首个 chunk 之前重试**：

| 改动 | 位置 |
| --- | --- |
| `Stream` 拆出 `openStream`（一次 HTTP + 分类），并按 `maxRetries` 退避重试 | `client.go` |
| 打开流后**先读第一帧**再返回：此时失败说明还没有任何内容交给调用方，重试对调用方完全不可见；一旦第一帧已交付，后续失败即最终失败（否则会重复用户已经看到的文本） | `client.go` |
| `sseStream.pending` 暂存这第一帧，`Recv` 先吐 pending 再继续扫 body | `stream.go` |
| 非 200 的 pre-flight 也改为按 `retryableStatus` 重试（原先一律不重试的规则作废，理由写进 `openStream` 注释） | `client.go` |

不会造成「静默空回答」的副作用：空流（第一个 `Recv` 直接 `io.EOF`）仍按合法空流交回调用方，
不当作拨号失败去重连。

**验证**：

- `go test ./chat/openai/...` 全绿。unary 新增 3 个用例：200 + error 信封（断言报出上游原因与
  模型名，且共 3 次尝试）／200 + 4xx error 信封（只 1 次尝试）／空 `choices`（3 次尝试）。
- 流式新增 3 个用例：首帧前失败 → 第二次尝试成功且 `calls=2`；首帧前一直失败 → 报出上游原因
  且 `calls=3`；**首帧之后**失败 → 报错不重试且 `calls=1`（同时保留已下发的文本）。
- 真实模型冒烟（`PLATEPILOT_TEST_CHAT=1`）：`TestSmokeComplete`、`TestSmokeToolCalls` 稳定通过；
  `TestSmokeStream` **改动前**命中上游过载、约 0.45s 单次失败；**改动后**同一个用例跑出两种结果——
  一次三项全过，一次在上游持续过载时于 ~4.5s 后仍失败（1s+2s 两次退避 = 确实重试了 3 次，
  报出的仍是上游真实原因）。也就是说重试能把**间歇性**过载救回来，救不回**整段时间都不可用**的
  路由；这正是 `:free` 路由的常态，要稳定就得换非 free 的模型或调大 `CHAT_MAX_RETRIES`。
- `go test ./...`（shared）、`go vet ./...`（shared）、`go test ./...`（chat-service）均绿。

**要记住的代价**：`:free` 路由（这里是 `nvidia/nemotron-3-ultra-550b-a55b:free`）的上游过载会反复
出现，重试救回单轮的方式是**把首字节延迟加上 1s、2s…**。要稳定就换成非 free 的模型。
