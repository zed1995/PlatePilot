# PlatePilot M5 纵向切片任务文档

> 生成日期：2026-10-03
> 依据文档：
> - `docs/platepilot-technical-prd.md`（§3.1 Agent 核心能力、§3.2 搜索与推荐、§3.3 RAG 证据问答、§3.4 可选 Mock 预约、§3.5 记忆与偏好、§6.1–6.6 读取链路、§7.1–7.2 工具与 API、§15 第一条纵向切片）
> - `docs/platepilot-implementation-plan.md`（M5-01 ~ M5-06、§9 Gate D/E、§13 MVP 范围、§15 第一个可演示版本）
> - `docs/platepilot-m4-task-document.md`（M4 已交付的运行时契约）
> - `AGENTS.md`（依赖方向、测试政策、提交与文档约定）
>
> 里程碑：M5 — 纵向切片（硬条件搜索、软条件推荐、指定餐厅问答、推荐解释、多轮追问、可选 Mock 预约）
> 验收门：**Milestone Exit Criteria（§1.2）**。实施计划 §9 的 Gate E（展示门）属于 M6；
> M5 的验收落在计划 §15「第一个可演示版本」的六条清单上，不新造 Gate。
> 当前状态：**待开始**。M0–M4 已交付并提交在 `main`（`8fdcebd` M4 运行时、`5554cfd` 多模块拆分、`071dd19` AGENTS.md）。
> 首批执行：**M5-01（意图与槽位层 + 硬条件搜索切片）**。
>
> 命名与命令说明：本文档中所有命令均为**多模块拆分之后**的形式（根 `Makefile` 只做转发，
> 根目录没有 `go.mod`）。M4 文档里的 `go run ./chat-service ...`、`go run ./data-pipeline migrate`
> 在拆分后已不成立，请以本文 §0.3 与附录 B 为准。

---

## 0. 如何使用本文档

本文档是 M5 的唯一执行清单。每个任务给出：目标、交付物、实现要点、依赖、工作量级和验收标准。
实施时按 §5 的顺序拆提交；一个任务完成后对照 §6.1 的通用 DoD 自检。
任务完成后按 `AGENTS.md` §8 把与计划的偏差追加到文末「附录 E：实施记录」，不要回头改原计划。

代码契约以仓库现状为准；本文档中的接口形状是**约定**，实现时允许按既有代码的实际签名调整，
但不得改变任务的目标与验收标准。

### 0.1 起点状态（2026-10-03，M0/M1/M2/M3/M4 已交付）

**M5 可直接消费的产物：**

| 产物 | 位置 | 说明 |
| --- | --- | --- |
| Eino 状态机 Runner | `chat-service/internal/agent/{runner,nodes,state}.go` | `ingress → plan →(tools→plan)*→ answer → finalize`；`Runner.Run` 返回事件通道，`Runner.Resume` 已实现 |
| 工具注册表 | `chat-service/internal/agent/toolreg/` | `Register/Specs/Invoke`，JSON Schema 校验、单工具超时、panic 收敛、结构化错误 |
| 已注册工具 | `chat-service/internal/agent/tools/` | `search_restaurants`（M4-05）、`get_restaurant_evidence`（M4-06） |
| 回答与引用合成 | `chat-service/internal/agent/answer/compose.go` | 证据 `<evidence id=...>` 拼装、`[^id]` 提取校验、越界重试一次、无证据固定拒答、`FOLLOWUPS:` 追问建议 |
| 会话/检查点/消息持久化 | `shared/store/postgres/{conversation_repository,memory_repository}.go`、`migrations/0005,0006` | `conversations`、`conversation_checkpoints`（乐观锁 version）、`conversation_messages`、`user_memories` |
| Run 与工具审计（写） | `shared/store/postgres/run_repository.go`、`0005_agent_runtime.sql` | `agent_runs`、`tool_calls`（arguments 只存脱敏摘要） |
| 记忆注入（读） | `chat-service/internal/agent/memory.go` | 每轮按 `user_id` 注入，按置信度截断 |
| Chat/SSE 会话 API | `chat-service/internal/httpapi/{conversation_handler,sse,router}.go` | `POST/GET /v1/conversations[...]`、7 种 SSE 事件、心跳、断连取消 |
| 记忆 API | `chat-service/internal/httpapi/memory_handler.go` | `GET /v1/memories`、`DELETE /v1/memories/:id` |
| 只读检索 API | `chat-service/internal/httpapi/restaurants.go` | `POST /v1/restaurants/search`、`POST /v1/restaurants[/:id]/evidence`（候选带 `reasons`、`snapshot_at`、`trace`） |
| 检索应用服务 | `chat-service/internal/retrieval/` | `Service.Search`（三通道融合、软通道失败降级）、`Service.Evidence`（证据组装） |
| 领域 DTO | `shared/domain/{search,retrieval,evidence,conversation,chat,tool,memory,run}` | `RestaurantFilter.Matches`（融合后硬条件剥离）、`conversation.State`（含 `awaiting_clarification` / `awaiting_confirmation` 常量） |
| 契约与夹具 | `shared/store/contract/`、`shared/store/memory/`、`shared/testkit/` | memory 与 postgres 跑同一套行为断言；`MockChatProvider` |
| 配置 | `chat-service/internal/config/config.go` | Chat 能力表、`AGENT_MAX_TOOL_ROUNDS`、`AGENT_TOOL_TIMEOUT`、检索权重与通道开关 |

**M5 的缺口（本里程碑要补齐的）：**

1. **`plan` 节点只有最小路由**（`unknown / chit_chat / restaurant_qa`），没有意图分类、没有槽位抽取、
   没有中英文条件归一，模型只能把整句话塞进 `search_restaurants.query` —— M5-01。
2. **没有"硬条件 vs 软条件"的结构化表达**：软条件（安静、适合约会）无法标注为"评论推断"，
   回答里也没有匹配原因与数据时间 —— M5-02。
3. **没有实体消歧**：只有"按需求搜"和"按 id 取证据"两个工具，从餐厅名进入问答没有路径 —— M5-03。
4. **澄清/确认状态从未被写入**：`conversation.State` 的两个 `awaiting_*` 常量已存在，但没有任何节点设置它们；
   `persistTurn` 一律把线程重置为 `idle` 并清空 `pending_action / missing_slots` —— M5-03/05/06。
5. **候选没有落库**：checkpoint 只有 `evidence_ids / selected_restaurant_id`，没有候选列表，
   "第二家"无法被引用；`Runner.Resume` 也恢复不出候选 —— M5-05。
6. **指代与省略无法解析**：历史回放只取 user/assistant 文本（`replayTranscript`），
   "这家 / 第二家 / 那家安静的"没有确定性解释 —— M5-05。
7. **Run 审计只写不读**：`RunRepository` 只有 `Start/Finish/RecordToolCall`，
   没有查询方法，也没有 run / tool_calls 的 HTTP 面 —— 计划的"工具调用链可回放"无法演示 —— M5-04。
8. **记忆只读**：`save_memory` 未暴露（M4-09 明确留给 M5），也没有修改入口 —— M5-07。
9. **没有任何预约结构**：没有 DTO、表、工具、幂等键，也没有"写操作必须经用户确认"的机制 —— M5-06。
10. **SSE 事件集是封闭的 7 种**（未知类型在 transport 层直接报错），缺少"等待用户输入 / 需要确认 /
    已保存记忆"这三类事件 —— M5-03/05/06/07。
11. **注册表没有"需要确认的写工具"概念**：`toolreg.Entry` 只有 `Spec/Handler/Timeout`，
    `ToolSpec.ReadOnly` 被赋值但**没有任何执行路径消费它** —— M5-06。
12. **`chat-service` 没有 `check-config` 子命令**（M4 文档承诺过），每个切片开工前的配置自检
    只能靠启动日志 —— M5-01 顺带补齐（可选）。

**环境现状：**

- 本地 PostgreSQL（pgvector + PostGIS）在线，M3 语料可用；`make pg-up` / `make migrate` 可重复执行。
- 真实端到端演示需要：本地 Ollama（`qwen3-embedding:0.6b`，向量通道）+ OpenRouter 兼容聊天渠道（工具调用）。
  离线测试**不依赖**这两者：槽位抽取、指代解析、确认闸门、候选落库全部可在内存仓储 + 假 Provider 下断言。
- `.env` 已被 git-ignore；密钥只进 `.env`，不进仓库、不进日志。

### 0.2 工作量级定义

沿用 M2/M3/M4 定义：`S` 约 0.5 天以内；`M` 约 1 天；`L` 约 1.5–2 天。

| 任务 | 量级（本文件） | 计划量级 | 差异说明 |
| --- | --- | --- | --- |
| M5-01 意图与槽位层 + 硬条件搜索切片 | L | M | 增加了中英文槽位归一、规则兜底、`interpret` 端点与切片级断言 |
| M5-02 软条件推荐与排序解释切片 | M | M | — |
| M5-03 指定餐厅问答切片 | M | M | 含实体消歧工具与澄清落库 |
| M5-04 Agent 推荐解释切片 | L | L | 含 run 读路径（端口 + 契约 + HTTP） |
| M5-05 多轮追问切片 | L | M | 候选存储、指代解析、状态机、事件契约四项相加 |
| M5-06 可选 Mock 预约切片 | L | L | 含确认闸门的通用机制（toolreg + runner） |
| M5-07 记忆写入与用户可见管理 | M | **计划外补充** | 依据 M4-09 交棒 + PRD §3.5 P0，见该任务说明 |

### 0.3 前置条件

```bash
# 1. 基线健康（全程离线，不需要数据库 / embedding / 聊天渠道）
make test && make vet && make build

# 2. 数据库迁移到最新（M5-05/M5-06 之前必须）
make pg-up        # 本地 Postgres（pgvector + PostGIS）on :55432
make migrate      # = make -C data-pipeline migrate

# 3. 真实链路联调前置（用户填 .env；离线开发不需要）
#    CHAT_PROVIDER=openai_compatible / CHAT_BASE_URL / CHAT_API_KEY / CHAT_MODEL（须支持 tools）
#    EMBEDDING_PROVIDER=ollama / EMBEDDING_MODEL=qwen3-embedding:0.6b（向量通道与软条件召回）
#    ollama serve && ollama pull qwen3-embedding:0.6b

# 4. Postgres 契约套件（新增仓储方法后必须真库验证）
make test-postgres

# 5. 检索门禁（硬条件零泄漏 + 软条件 recall 不回归）
make eval-retrieval
```

---

## 1. M5 目标与退出条件

### 1.1 里程碑目标

把 M3 的检索能力和 M4 的 Agent 运行时接成**七条可演示、可验收、可解释的纵向切片**——
每一条都从用户的一句自然语言开始，到一个可核对的结果结束：

1. **硬条件切片**：一句话 → 结构化硬过滤 → 全部满足条件的餐厅列表（带匹配原因和数据时间）。
2. **软条件切片**："安静 / 适合约会"经评论主题与向量召回影响排序，且**始终标注为评论推断**，不冒充事实。
3. **指定餐厅切片**：餐厅名 → 实体消歧（歧义就澄清）→ 该店证据 → 有引用、证据可数的回答。
4. **推荐解释切片**：Agent 自主调用搜索与证据工具，回答给出匹配原因、未知项，工具调用链可通过 API 回放。
5. **多轮追问切片**：候选与上下文落库，"第二家安静吗"能解析到具体餐厅并只取该店证据。
6. **可选预约切片**：Mock 库存 → 摘要 → **等待用户明确确认** → 幂等写入；默认关闭，不阻塞主链路。
7. **记忆切片**：只有用户明确要求才写入长期记忆，可查看、可修改、可删除，单轮搜索条件永不自动升级为记忆。

### 1.2 退出条件（Milestone Exit Criteria）

以下八条全部满足才允许进入 M6。第 1 条直接对齐实施计划 §15「第一个可演示版本」清单。

1. **可演示**：本机可完成「自然语言输入 → 至少一次 `search_restaurants` + 至少一次 `get_restaurant_evidence`
   → 1–5 个候选 → 每条推荐带来源与快照时间 → 继续追问 → 工具与检索过程可查」全链路（附录 B 有逐条 curl）。
2. **硬条件正确**：中英文槽位抽取有表驱动测试；融合后返回的候选 **100% 满足硬条件**（复用 M3 断言，
   M5 补切片级用例）；`make eval-retrieval` 不回归（硬条件零泄漏、软条件 recall 不下滑）。
3. **软条件可解释**：软条件**不进入** `search.RestaurantFilter`；命中理由写明"评论推断 + 主题"；
   回答里由评论推断的结论必须显式标注依据来自评论，无法确认的条件列入未知项。
4. **指定餐厅可答**：名称/地址 →（歧义则澄清）→ 证据 ≥2 条 → 引用全部来自该餐厅，引用越界仍按 Gate D 硬失败处理。
5. **多轮可续**：候选落库后"第二家 / 这家 / 那家安静的"有确定性解析；`awaiting_clarification` /
   `awaiting_confirmation` 跨请求存活；进程重启后发一条消息仍能按 checkpoint 续接。
6. **确认闸门**：非只读工具在用户确认前**不产生任何写入**；同一幂等键重复确认只产生一条预约；
   `RESERVATION_ENABLED=false`（默认）时主链路与全部测试与预约无关。
7. **记忆合规**：未经用户明确要求无法写入记忆（`quoted_user_text` 校验）；记忆可查看/修改/删除；
   单轮搜索条件不会变成长期记忆。
8. **质量基线（离线）**：`make test` / `make vet` / `make build` 全绿；新增仓储方法已进
   `shared/store/contract` 并由 memory + postgres 双实现通过；`make test-postgres` 真库全绿；
   架构测试（依赖方向、Eino 边界、domain 纯净性）**未被放宽**。

---

## 2. 目标数据形态

### 2.1 分层与依赖方向

```text
HTTP 层（hertz）
  chat-service/internal/httpapi        会话/SSE、confirm、记忆 PATCH、interpret、run 回放
        │  只依赖应用层与领域 DTO
        ▼
应用/编排层
  chat-service/internal/agent           graph、nodes（含 clarify）、slots、hitl、tools、toolreg
  chat-service/internal/reservation     Mock 预约应用服务（容量、hold、幂等）
  chat-service/internal/retrieval       M3 已交付（Search / Evidence），被工具包装
        │  只依赖端口
        ▼
端口层（shared/store、shared/chat）
  ConversationRepository（+候选）、RunRepository（+读）、MemoryRepository、ReservationRepository（新）
        │
        ▼
适配器层
  shared/store/postgres   迁移 0007（候选）/ 0008（预约 + checkpoint 挂起参数）
  shared/store/memory     同一契约的内存实现（离线测试）
```

**硬约束（架构测试已强制，M5 不得破坏）：**

- `shared/domain/**` 不 import 数据库驱动 / Eino / Hertz / 厂商 SDK；新增 `domain/reservation` 与
  `domain/review` 常量必须只依赖标准库。
- Eino 类型只允许出现在 `chat-service/internal/agent/{einomodel,toolreg}`；
  新增节点写成普通 Go 函数（Lambda 节点），不要把 `schema.Message` 带出这两个包。
- 两个服务之间仍不得互相 import；chat-service 需要的新类型放 `shared/` 或本服务 `internal/`。
- 候选、预约、记忆的**业务状态一律进数据库**，Eino 内存不是事实来源。

### 2.2 目标目录结构（M5 结束时）

```text
shared/
  domain/
    conversation/
      candidate.go              # M5-05：conversation.Candidate（线程候选快照）
      conversation.go           # M5-06：Checkpoint 增加 PendingArguments / PendingToolCallID
    reservation/
      reservation.go            # M5-06：Slot / Reservation / 状态常量
    review/
      topics.go                 # M5-02：topic 常量与标签从 data-pipeline 下沉（单一来源）
    errs/errs.go                # M5-05/06/07：新错误码（见 §2.3.6）
  store/
    repository.go               # M5-05/06/04：候选、预约、run 读方法
    contract/
      conversation.go           # M5-05：候选用例
      audit.go                  # M5-04：run/tool_calls 读用例
      reservation.go            # M5-06：预约用例
    memory/{candidates.go,reservations.go}   # M5-05/06
    postgres/
      migrations/
        0007_conversation_candidates.sql     # M5-05
        0008_mock_reservation.sql            # M5-06（含 checkpoint 挂起参数列）
      candidate_repository.go                # M5-05
      reservation_repository.go              # M5-06
      run_repository.go                      # M5-04：追加读方法

chat-service/internal/
  agent/
    slots/
      plan.go                   # M5-01：Plan / Intent / SoftCondition
      extract.go                # M5-01：结构化抽取 + 规则兜底
      normalize.go              # M5-01：中英文硬槽位归一（borough/cuisine/price/rating/neighborhood）
      soft.go                   # M5-02：软条件识别与 topic 映射
      reference.go              # M5-05：序数词/指代解析
      *_test.go
    nodes.go                    # M5-01/03/05：plan 接 slots；clarify 节点；pending 补槽
    runner.go                   # M5-06：确认闸门；M5-05：澄清上限
    persistence.go              # M5-05：候选读写；M5-06：挂起动作持久化
    hitl/
      confirm.go                # M5-06：待确认动作的执行与幂等
    tools/
      resolve_restaurant.go     # M5-03
      availability.go           # M5-06（get_availability）
      reservation.go            # M5-06（request_reservation）
      save_memory.go            # M5-07
    toolreg/registry.go         # M5-06：RequiresConfirmation 与注册期校验
  reservation/service.go        # M5-06
  httpapi/
    conversation_handler.go     # M5-06：POST /v1/conversations/:id/confirm
    run_handler.go              # M5-04：run / tool_calls 只读回放
    interpret_handler.go        # M5-01：POST /v1/restaurants/interpret
    memory_handler.go           # M5-07：PATCH /v1/memories/:id
    sse.go                      # M5-03/05/06/07：新增事件类型与载荷
```

### 2.3 目标接口形状（约定）

#### 2.3.1 意图与槽位（M5-01）

```go
// internal/agent/slots/plan.go（约定，字段可按实现增补）
type Intent string

const (
    IntentDiscover     Intent = "discover"      // 找餐厅，硬条件为主
    IntentRecommend    Intent = "recommend"     // 找餐厅，软条件为主
    IntentRestaurantQA Intent = "restaurant_qa" // 指定餐厅问答
    IntentReservation  Intent = "reservation"   // 预约（仅 RESERVATION_ENABLED 时可用）
    IntentChitChat     Intent = "chit_chat"
)

type SoftCondition struct {
    Text  string // 用户原话片段，如 "安静"
    Topic string // review 主题：ambience / service / wait / food / value / kid_friendly / group_friendly
}

type Plan struct {
    Intent            Intent
    HardFilters       search.RestaurantFilter // 只放确定性条件，软条件绝不进来
    SoftConditions    []SoftCondition
    NamedRestaurants  []string   // 用户点名的餐厅（M5-03 用来消歧）
    SelectedRestaurantID int64   // M5-05：由指代解析得出，0 表示未解析
    MissingSlots      []string   // 追问/澄清缺什么（borough/cuisine/party_size/date/restaurant_id）
    NeedClarification bool
    Source            string     // "model" | "rules"：抽取来源，便于离线验收
    Warnings          []string
}
```

#### 2.3.2 候选快照（M5-05）

```go
// shared/domain/conversation/candidate.go（约定）
type Candidate struct {
    ThreadID     string
    Position     int       // 1-based，"第二家"即 Position=2
    RestaurantID int64
    Name         string
    Score        float64
    Reasons      []string
    SnapshotAt   time.Time
    CreatedAt    time.Time
}
```

#### 2.3.3 挂起动作（M5-06）

```go
// shared/domain/conversation/conversation.go 扩展
type Checkpoint struct {
    // ...M4 既有字段...
    PendingToolCallID string          // 需确认的工具调用 id（无则空）
    PendingArguments  json.RawMessage // 待执行参数（已脱敏，含 idempotency_key 的派生输入）
}
```

#### 2.3.4 SSE 事件（M5 增量，前端契约）

M4 的 7 种事件（`message.start/delta`、`tool.start/finish`、`citation`、`message.end`、`error`）保持不变，
**新增 3 种**，全部是"需要用户做点什么"或"刚刚写入了什么"的信号：

```text
event: state.awaiting_input  data: {"state":"awaiting_clarification","pending_action":"resolve_restaurant",
                                    "missing_slots":["restaurant_id"],
                                    "candidates":[{"position":1,"restaurant_id":12,"name":"..."}]}
event: confirmation.required data: {"call_id":"...","action":"request_reservation",
                                    "summary":"Katz's Delicatessen · 10-20 19:00 · 2 人","expires_at":"..."}
event: memory.saved          data: {"memory_id":"...","memory_type":"constraint","content":"不吃辣"}
```

`state.awaiting_input` 之后仍以 `message.end` 收尾（本轮成功结束，只是线程停在等待态）。

#### 2.3.5 HTTP 路由（M5 增量）

```text
POST   /v1/restaurants/interpret              # M5-01：自然语言 → Plan 投影（只读、无副作用）
POST   /v1/conversations/:id/confirm          # M5-06：{"decision":"confirm"|"cancel"} 执行/取消挂起动作
GET    /v1/conversations/:id/runs             # M5-04：该线程的 run 列表（分页，只回审计摘要）
GET    /v1/runs/:run_id                       # M5-04：单个 run + 工具调用序列（只回脱敏摘要）
PATCH  /v1/memories/:memory_id                # M5-07：修改记忆内容/类型（归属校验）
```

既有路由**不改语义**：`/v1/*` 的响应字段是客户端契约（`reasons`、`trace`、`checkpoint` 摘要）。

#### 2.3.6 工具清单（M5 结束时）

| 工具 | 类型 | 引入 | 说明 |
| --- | --- | --- | --- |
| `search_restaurants` | 只读 | M4-05，M5-01/02 扩参 | 新增 `price_levels`、`neighborhood`、`name`、`soft_conditions` |
| `get_restaurant_evidence` | 只读 | M4-06 | 不变；M5-03 增加"证据充分性"约束 |
| `resolve_restaurant` | 只读 | M5-03 | 名称/地址 → 消歧（`resolved` / `ambiguous` / `not_found`） |
| `save_memory` | 写入（非确认类） | M5-07 | 必须带 `quoted_user_text` 证据性参数 |
| `get_availability` | 只读 | M5-06 | 仅 `RESERVATION_ENABLED=true` 注册 |
| `request_reservation` | 写入（确认类） | M5-06 | **先挂起、确认后执行**；仅 `RESERVATION_ENABLED=true` 注册 |

#### 2.3.7 新错误码

沿用 `errs` 前缀约定（`reservation_` / `agent_` / `memory_`），在 `shared/domain/errs/errs.go` 追加：

| 码 | HTTP | 语义 |
| --- | --- | --- |
| `reservation_unavailable` | 409 | 时段已满 / hold 过期 / 容量不足 |
| `agent_no_pending_action` | 400 | 线程没有待确认动作却收到 confirm |
| `memory_write_not_requested` | 400 | 记忆写入缺少用户明确要求的证据（`quoted_user_text` 不匹配） |

---

## 3. 任务清单总览

| 任务 | 名称 | 主要交付 | 依赖 | 量级 |
| --- | --- | --- | --- | --- |
| M5-01 | 意图与槽位层 + 硬条件搜索切片 | `agent/slots` 包、`search_restaurants` 扩参、`interpret` 端点、结果卡片 | M3-01、M3-05、M4-02 | L |
| M5-02 | 软条件推荐与排序解释切片 | topic 常量下沉、软条件不落硬过滤、评论推断标注 | M5-01、M3-03 | M |
| M5-03 | 指定餐厅问答切片 | `resolve_restaurant` 工具、歧义澄清状态、证据充分性 | M5-01、M3-04、M3-07 | M |
| M5-04 | Agent 推荐解释切片 | 解释结构 + run/tool_calls 读路径 + `runs` API | M5-01、M5-02、M4-07 | L |
| M5-05 | 多轮追问切片 | 候选落库（0007）、指代解析、`awaiting_clarification`、新 SSE 事件 | M5-03、M4-08、M4-09 | L |
| M5-06 | 可选 Mock 预约切片 | 0008 迁移、预约工具、确认闸门、幂等 | M5-05、M4-04、M4-07 | L |
| M5-07 | 记忆写入与用户可见管理 | `save_memory` + 护栏、`PATCH /v1/memories/:id` | M4-09、M5-05（事件机制） | M |

### 3.1 依赖图

```text
M5-01（slots + 硬条件）
  ├─> M5-02（软条件）─┐
  ├─> M5-03（消歧）──┼─> M5-04（解释 + 回放）─┐
  └──────────────────┴─> M5-05（多轮/状态机）─┼─> M5-06（预约确认）
                                              └─> M5-07（记忆写入）
```

- M5-01 是唯一的前置：其余任务都消费 `slots.Plan`。
- M5-02 与 M5-03 可并行；M5-04 依赖 M5-02 的软条件标注才有"可解释"的内容。
- M5-05 依赖 M5-03 的澄清路径（同一套状态机），但候选落库可先行。
- M5-07 只依赖 M5-05 的事件机制 + M4-09 的记忆端口，可与 M5-06 并行。

### 3.2 推荐执行顺序

**切片 0（理解层）：M5-01** —— 先把"一句话 → 结构化条件"做扎实，后面所有任务的输入质量都由它决定。

**切片 1（推荐链路）：M5-02 → M5-04** —— 软条件与解释结构打通，回答开始"像推荐"而不是"像列表"。

**切片 2（问答与多轮）：M5-03 → M5-05** —— 实体消歧 + 候选落库 + 指代解析 + 澄清状态。

**切片 3（可选）：M5-06** —— 预约整条链路，默认关闭。

**切片 4（并行）：M5-07** —— 记忆写入策略，可与切片 2/3 并行推进。

---

## 4. 详细任务

### M5-01 意图与槽位层 + 硬条件搜索切片

**目标**：把用户一句话变成可执行的结构化检索；端到端返回**全部满足硬条件**的餐厅列表，
并且抽取过程本身可以被单独检查（不依赖模型渠道也能验收）。

**交付物：**

1. **新包 `internal/agent/slots/`**（`plan.go` / `extract.go` / `normalize.go`，见 §2.3.1）：
   - `Extract(ctx, userInput string, pending *conversation.Checkpoint) (Plan, error)`：
     用 M4-02 已交付的 `StructuredOutputProvider.CompleteStructured` + JSON Schema 抽取；
     `CHAT_SUPPORTS_JSON_SCHEMA=false` 时走 M4-02 既有的 `json_object` 退化路径。
   - **规则兜底是必须的**：模型不可用、输出非法 JSON、或根本没有聊天渠道时，
     `Plan` 退化为 `{Intent: discover, Source: "rules"}`，把整句话作为 `Query` 走关键词 + 向量通道，
     **绝不因为"没抽出槽位"而拒绝服务或返回 500**。
2. **硬槽位归一 `normalize.go`**：
   - borough：`曼哈顿 / 中城 / 布鲁克林 / 皇后区 / 布朗克斯 / 史泰登岛` 与英文名 → 五个 canonical 值；
     商圈（中城、下城、SoHo、Williamsburg…）映射到 `Neighborhood` 而不是 Borough。
   - cuisine：常见中英文别名（`日餐/日料/寿司 → japanese|sushi`、`意餐 → italian`…）。
   - price_levels：`便宜/中等/高档/人均$…` 与 `$ $$ $$$ $$$$` → 1..4。
   - min_rating：`4 星以上 / 4.5+ / at least 4 stars` → `4.0 / 4.5`。
   - **距离条件**：`QueryOrigin+MaxDistanceMeters` 只在调用方给了坐标时可用；
     自然语言的"附近"没有地理编码器，降级为 borough/neighborhood 过滤 **并写进 `Plan.Warnings`**
     （这是已知限制，追加进附录 E）。
3. **`search_restaurants` 扩参**：新增 `price_levels`（1..4 数组）、`neighborhood`、`name`、
   `soft_conditions`（字符串数组，M5-02 消费）；参数到 `search.RestaurantFilter` 的映射复用
   `Filter.Validate()`；`soft_conditions` **不得**写入 Filter。
4. **结果卡片**：`renderCandidates` 增加 `reasons`（匹配原因）与数据时间，回答卡片与工具摘要同源；
   `RestaurantCandidate.Reasons` / `SnapshotAt` 已存在，本任务只负责把它们真正渲染出来。
5. **只读端点 `POST /v1/restaurants/interpret`**：自然语言 → `Plan` 投影（`intent`、`hard_filters`、
   `soft_conditions`、`named_restaurants`、`missing_slots`、`source`）。用途：
   （a）切片可脱离聊天渠道独立验收；（b）演示时能解释"为什么这么搜"；（c）前端调试。
   无聊天渠道时返回 `source:"rules"`，不报错。
6. **（可选，低优先）`chat-service check-config` 子命令**：加载 + 校验配置并打印 `Redacted().Summary()`
   后退出，与 `data-pipeline check-config` 对齐（补上 M4 文档承诺过但未实现的项）。

**实现要点：**

- 抽取结果一律经 `Filter.Validate()`：非法 borough/价格档在这里变成 `retrieval_invalid_filter`，
  而不是悄悄返回空列表（沿用 M3 的既有判断）。
- 抽取 prompt 明确区分"硬条件"（可确定性过滤）与"软条件"（只能由评论支撑）；两者的处理路径不同，
  绝不能因为模型把"安静"放进 `hard_filters` 就照单执行——`normalize.go` 必须把已知软词从硬条件里剔除。
- `Plan` 的产生是**每轮一次**，不是每个工具一次：`search_restaurants` 的参数由 `plan` 节点注入默认值，
  模型只需补充遗漏项。
- 埋点：`Plan.Source`（model/rules）与抽取耗时进 run 审计（M4-07 的 `result_summary` 可承载）。

**测试（全部离线）：**

- 表驱动中英文用例 ≥20 条：`曼哈顿中城 4 星以上意大利菜`、`quiet ramen in Brooklyn under $$`、
  `帮我找适合约会的日料`、`$` / `4.5+` / `中城` / 空查询 等，断言 `Plan.HardFilters` 与
  `Plan.SoftConditions` 的拆分。
- 规则兜底：无 chat provider、结构化输出非法 JSON 两种情况下 `Extract` 不返回错误且 `Source="rules"`。
- 切片级集成（内存仓储 + 假 embedding）：查询 → 抽槽 → 检索 → 断言**返回的每个候选都满足每一条硬条件**
  （复用 `Filter.Matches`），且候选数 ≥3（语料足够时）。
- 负路径：`hard_filters` 中混入软词 → 被剔除并记 warning；非法 borough → `retrieval_invalid_filter`。

**验收标准：**

- `POST /v1/restaurants/interpret` 对"曼哈顿中城 4 星以上意大利菜"返回 `borough=manhattan`、
  `min_rating=4`、`cuisines` 含 italian，软条件为空；
- 同一句话走聊天链路可返回 ≥3 家且全部满足硬条件；
- `make eval-retrieval` 硬条件泄漏数仍为 0；
- 没有聊天渠道时 `interpret` 仍返回 `source:"rules"` 且不报错。

**工作量：L。依赖：M3-01、M3-05、M4-02（结构化输出）。**

---

### M5-02 软条件推荐与排序解释切片

**目标**：让"安静、适合约会"这类**只能由评论支撑**的条件真正影响召回与排序，同时让系统
在任何可见位置都不把它当成客观事实。

**交付物：**

1. **topic 单一来源**：把 `data-pipeline/internal/pipeline/curate/topics.go` 的 topic 常量
   （`food / service / ambience / value / wait / kid_friendly / group_friendly`）**下沉到
   `shared/domain/review/topics.go`**（常量 + 中文标签），`curate` 包改为引用；
   在 data-pipeline 加一条测试断言 keyword 表的 key 集合等于 shared 常量集合，防止两边漂移
   （漂移的后果是"过滤一个不存在的主题 → 静默返回空"，正是最不容易发现的故障）。
2. **软条件进检索、但不进硬过滤**：
   - `retrieval.Request` 增加 `SoftConditions []string`；
   - 向量通道的 embedding 文本 = `Query + 软条件原文 + 主题词`；
   - `Trace.Channels[vector].Note` 写明"软条件按评论主题与向量召回，属于评论推断"；
   - 断言：`Trace.Filters` 与 `RestaurantFilter` 中**不出现**任何软条件。
3. **候选命中理由**：命中的候选在 `Reasons` 里写"评论推断：安静（ambience）"这类可核对的话术
   （`Reasons` 字段已存在，只丰富内容，不改 `/v1` 契约字段名）。
4. **回答层标注**（`answer/compose.go`）：
   - `answer.Input` 增加 `Candidates []search.RestaurantCandidate` 与 `SoftConditions []slots.SoftCondition`；
   - system instruction 增加第 6 条规则：**"由评论推断的结论必须写明依据来自评论，
     并标注对应 [^id]；不得表述为客观事实；没有任何证据支持的条件必须列入『无法确认』"**；
   - 回答中每条推荐附数据时间（来自 `Candidate.SnapshotAt`）与来源。
5. **解释性 trace 可见**：把 `Trace.Channels` 的降级/软条件说明透传到 `message.end` 的 `warnings`，
   让"这次排序里向量通道做了什么"在流里可见。

**实现要点：**

- 软条件的权重不新增配置：它就挂在既有的 `RETRIEVAL_WEIGHT_VECTOR` 上。
  "软条件能影响排序，但永远不能把硬条件不满足的餐厅拉回来"是 M3 已保证的不变量，M5 只增加表达力。
- 主题映射表是**产品词表**：`安静/氛围好/浪漫/适合约会 → ambience`、`排队/等位 → wait`、
  `服务 → service`、`性价比 → value`、`带孩子 → kid_friendly`、`适合聚会 → group_friendly`。
- 不要为软条件做"证据计数"（"3 条评论提到安静"）——那需要新的聚合，MVP 不划算；
  写主题名即可，用户可点进引用核对。

**测试：**

- 软条件不落 Filter 的单测（表驱动：把已知软词塞进硬条件，断言被剔除）；
- "安静、适合约会"下向量通道 `ran=true`，命中候选 `Reasons` 含 `ambience`；
- 回答标注：Mock 脚本返回固定文本，断言 composer 的 system instruction 包含"评论推断"约束，
  且证据不足的软条件出现在"无法确认"提示里；
- `make eval-retrieval` 软条件 recall 不低于 M3 基线。

**验收标准：** §1.2 第 3 条；`interpret` 输出里"安静"落在 `soft_conditions`，`hard_filters` 为空。

**工作量：M。依赖：M5-01、M3-03（向量召回）、M3-05（融合）。**

---

### M5-03 指定餐厅问答切片

**目标**：从餐厅名进入问答——先消歧，再取该店证据，最后给出**有 ≥2 条有效证据**支撑的回答。

**交付物：**

1. **新只读工具 `resolve_restaurant`**（`tools/resolve_restaurant.go`）：
   - Schema：`name`（必填）、`address_hint`、`borough`、`limit`（1..5）；
   - 实现：`RestaurantRepository.MatchByText`（M3 已有，相似度 [0,1]）+ 可选 borough 过滤；
   - 结果：`{status:"resolved"|"ambiguous"|"not_found", restaurant:{id,name,address,borough,rating}, candidates:[...], similarity}`；
   - 判据可配：`AGENT_RESOLVE_MIN_SIMILARITY`（默认 0.55）、`AGENT_RESOLVE_AMBIGUITY_GAP`
     （top1 与 top2 的相似度差 < 0.10 判为 ambiguous）。
2. **澄清节点 `clarify`**（`agent/nodes.go`）：
   - 触发：`Plan.NeedClarification` 或 `resolve_restaurant` 返回 `ambiguous`；
   - 行为：输出固定澄清模板（列出 2–3 个候选：名称 / 地址 / 评分），
     写 `State=awaiting_clarification`、`PendingAction="resolve_restaurant"`、`MissingSlots=["restaurant_id"]`，
     调 M4-08 已有的 `persistPendingCheckpoint` 中途落库，发 `state.awaiting_input` 事件；
   - 澄清轮次上限 `AGENT_MAX_CLARIFICATIONS`（默认 3）：超限则"取最相似的一家并在回答里说明假设"，
     避免模型与用户来回澄清到死循环。
3. **证据充分性**：
   - `get_restaurant_evidence` 结果中，至少 **2 条不同文档类型**的证据才构成"可作答"；
   - 不足 2 条时 composer 必须显式声明"目前只有 N 条资料，以下结论仅基于它"，
     并降低断言强度（不得用单条证据描述"这家店服务很好"这类整体判断）；
   - 组装预算（M3 既有的 token 预算与 three-per-type 规则）不得把可用证据压到 1 条以下——
     预算冲突时优先保留 ≤2 条不同 doc_type 的证据，其余整块丢弃（沿用"不许半句引用"的既有红线）。
4. **回答与引用**：指定餐厅问答的 citations 必须全部属于该 `restaurant_id`（M3 证据层已保证，
   本任务补一条切片级断言）。

**实现要点：**

- `resolve_restaurant` 只做"名字 → id"，不做推荐；即使命中唯一，也不得跳过 `get_restaurant_evidence`。
- 歧义候选列表同时写进 checkpoint 的候选（M5-05 的 `conversation_candidates`），
  这样用户回"第二家"时指代解析有据可依。
- 澄清文案是产品文案：先给差异（"一个在 East Village，一个在 SoHo"），再问选择，不要罗列 id。

**测试：**

- 四类路径单测：完全匹配 → `resolved`；模糊 → `resolved` 但带 similarity；同名/近名 → `ambiguous`；
  库中没有 → `not_found`（这是正常结果，不是错误）；
- 澄清路径：ambiguous → `awaiting_clarification` 落库 + `state.awaiting_input` 事件 + 候选写入；
- 证据充分性：只有 1 条证据时回答文本含"资料不足"字样；≥2 条时 citations ≥2；
- 引用边界：citations 全部属于目标餐厅（跨餐厅返回即失败）。

**验收标准：** §1.2 第 4 条；歧义问题在真实语料下可复现（例如同一名称的多家分店）。

**工作量：M。依赖：M5-01、M3-04（证据召回）、M3-07（证据组装）。**

---

### M5-04 Agent 推荐解释切片（工具链回放 + 解释结构）

**目标**：推荐不再是"一堆名字"，而是"为什么是这几家、依据是什么、什么没法确认"；
并且这次 run 的工具调用链可以事后从 API 拉出来核对。

**交付物：**

1. **推荐解释结构**（`answer/compose.go`）：
   - `answer.Input` 扩展为 `{Question, Candidates, Filters, SoftConditions, Evidence}`；
   - 新增"推荐解释" system instruction，规定输出顺序：
     ① 结论（1–3 句，含候选数量）→ ② 匹配原因（硬条件逐条对应 + 软条件标注"评论推断"）
     → ③ 每条推荐的来源与数据时间 → ④ 无法确认的条件 → ⑤ `FOLLOWUPS:` 追问建议；
   - 引用校验保持 M4 的硬规则不变（`citations ⊆ 当轮证据`，越界重试一次后失败）。
2. **run 读路径**（这是"可回放"的落地）：
   - `store.RunRepository` 增加读方法：
     `GetRun(ctx, runID) (run.AgentRun, error)`、
     `ListRuns(ctx, threadID string, limit int, beforeID string) ([]run.AgentRun, error)`、
     `ListToolCalls(ctx, runID string) ([]run.ToolCallRecord, error)`；
   - `shared/store/memory` + `shared/store/postgres` 双实现，`shared/store/contract/audit.go` 扩用例
     （running→succeeded/failed 流转、按 thread 倒序分页、无 run 的工具查询返回空）；
   - HTTP：`GET /v1/conversations/:id/runs?limit=`、`GET /v1/runs/:run_id`
     （含工具调用序列：`tool_name / status / latency_ms / result_summary`）。
   - **只回脱敏摘要**：`arguments` 在 M4-07 入库时已是摘要，读接口不得回显原始参数或密钥。
3. **引用正确性回归**：新增断言"本轮 citations ⊆ 本轮证据"，并明确
   **checkpoint 里的 `evidence_ids` 不参与引用校验**（它是恢复用的历史快照，
   允许模型引用上一轮证据会直接破坏 Gate D 第 6 条）。
4. **演示命令**：附录 B 给出"一次推荐 → 拉 run → 看工具链"的完整 curl 序列。

**实现要点：**

- 不引入节点级 trace（那是 M6-01）：M5-04 只暴露 M4-07 已经落库的 run/tool_calls 事实，
  字段够用即可，不要顺手改 `agent_runs` 的表结构。
- 分页游标沿用会话消息 API 的 `before_id` 风格，保持 API 习惯一致。
- 解释文案里"无法确认"必须来自真实缺失：`Plan.MissingSlots`、软条件无证据、以及
  `Trace.Warnings` 中的降级（例如向量通道未启用），不要写成固定免责声明。

**测试：**

- Mock Provider + 内存仓储的离线端到端：一次推荐问题 → 断言事件序
  `message.start → tool.start(search) → tool.finish → tool.start(evidence) → tool.finish → message.delta → citation → message.end`；
- 解释结构：Mock 返回固定文本时断言 composer 的 system instruction 含解释顺序约束；
- 回放 API：写入一条 run + 两条 tool_call 后，`GET /v1/runs/:id` 返回同名序列与状态；
- 引用回归：构造"上一轮有证据、本轮证据为空"的场景，断言本轮回答走拒答而不是复用历史引用。

**验收标准：** §1.2 第 1 条（工具与检索过程可查）与第 2 条（引用不越界）。

**工作量：L。依赖：M5-01、M5-02、M4-07（审计已落库）。**

---

### M5-05 多轮追问切片（候选持久化 + 指代解析 + 澄清状态）

**目标**：让"上下文"成为数据库里的事实——候选、挂起状态、待确认动作都跨请求存活，
"第二家安静吗"能被确定性解析，进程重启后线程还能继续。

**交付物：**

1. **候选落库**：
   - `shared/domain/conversation/candidate.go`（§2.3.2）；
   - 迁移 `0007_conversation_candidates.sql`：
     `thread_id`(FK CASCADE) / `position` / `restaurant_id` / `name` / `score` / `reasons text[]` /
     `snapshot_at` / `created_at`，主键 `(thread_id, position)`，索引 `(thread_id, position)`；
   - 端口 `ConversationRepository.ReplaceCandidates(ctx, threadID, []Candidate)` 与
     `ListCandidates(ctx, threadID)`；memory + postgres 实现 + 契约用例；
   - 写入时机：`finalize` 时本轮有候选 → **整组覆盖**；本轮没有候选（追问场景）→ 保留上一轮候选。
2. **指代解析**（`slots/reference.go`）：
   - 序数词优先（确定性，不经过模型）：`第一家 / 第二家 / 第三家 / 最后一家 / 第 N 家` → `position`；
   - 指示词：`这家 / 它 / 那家` → `SelectedRestaurantID`（若为空则取 `position=1`）；
   - 修饰指代：`那家安静的 / 布鲁克林那家` → 在候选集内按 `reasons`/地址匹配；
   - 解析结果写 `Plan.SelectedRestaurantID` 与 `Plan.ReferenceNote`，
     并把"按你对第 2 家的追问"这类说明放进回答或 warnings（让用户知道系统理解成了哪家）。
3. **状态机扩展**（`nodes.go` / `persistence.go` / `runner.go`）：
   - `plan` 节点新增分支：澄清 → `clarify`；需确认 → `awaiting_confirmation`（M5-06）；
   - `ingress` 读到的 checkpoint 传给 `slots.Extract`：有 `MissingSlots` 时优先补槽，
     补齐后继续 `PendingAction`（复用 M4-08 的 `Runner.Resume` 语义，不新增恢复端点）；
   - **`persistTurn` 的既有行为要改**：目前它无条件把 state 重置为 `idle` 并清空 pending/missing；
     M5 改为"只有本轮正常结束且无挂起动作时才回到 `idle`"。
4. **SSE 事件扩展**（`httpapi/sse.go`）：
   - 新增 `state.awaiting_input`（§2.3.4）；
   - `agent.Event` / `httpapi.StreamEvent` 增加对应载荷字段（state / missing_slots /
     pending_action / 候选摘要）。**注意顺序**：transport 的 `payload()` 对未知事件类型会直接报错，
     所以必须先扩类型再发事件，两侧同时改、同时测。
5. **线程状态可见**：`GET /v1/conversations/:id` 已返回 checkpoint 摘要（state / pending_action /
   missing_slots / selected_restaurant_id），M5 **不新增 `POST /conversations/:id/resume` 端点**：
   继续对话就是发下一条消息，重连后读状态就是 GET 线程。与 PRD §7.2 的差异记入附录 E。

**实现要点：**

- 候选是"最近一次搜索结果"，不是历史累积：整组覆盖 + 位置语义清晰，比追加历史更好解释。
- 并发保护沿用 checkpoint 的乐观锁思路：同线程并发写候选时以 `thread_id` 行为单位事务化，
  冲突返回 `conflict`（MVP 不做线程队列）。
- 指代解析**不调用模型**：它是可复现的规则，性能与确定性都更好；解析不了才让模型看候选列表。
- 历史回放仍只取 user/assistant 文本（`replayTranscript` 不变）；候选与挂起状态走 checkpoint，
  两条路径互不干扰。

**测试：**

- 两轮追问：第一轮推荐 5 家 → 第二轮"第二家安静吗" → 断言 `SelectedRestaurantID == candidates[1].restaurant_id`，
  且第二轮 evidence/citations 只属于该店；
- 澄清到底：模糊餐厅 → `awaiting_clarification`（落库）→ 用户答"第二家" → 继续原 `PendingAction`；
- 澄清超限：连续 3 轮无法补齐 → 使用默认假设继续并记 warning；
- 恢复：新建 Runner（模拟重启）→ 发消息 → 能读到 pending 状态并继续；
- 状态重置回归：正常一轮结束后 state 回到 `idle` 且 pending 清空；挂起一轮结束后 state 保持 `awaiting_*`；
- 并发：同线程并发发消息 → 一个成功一个 `conflict`。

**验收标准：** §1.2 第 5 条；`0007` 迁移可重复执行；`make test-postgres` 覆盖候选读写。

**工作量：L（计划为 M，本文件上调：候选存储 + 指代解析 + 状态机 + 事件契约四项）。**
**依赖：M5-03（澄清路径）、M4-08（checkpoint）、M4-09（记忆/上下文）。**

---

### M5-06 可选 Mock 预约切片（确认闸门 + 幂等）

**目标**：把"写操作必须先经用户明确确认"做成**通用机制**（不只服务预约），
并在默认配置下完全不参与主链路。

**交付物：**

1. **DTO 与迁移**：`shared/domain/reservation/reservation.go`（`Slot` / `Reservation` / 状态常量：
   `held / confirmed / cancelled / expired`）；迁移 `0008_mock_reservation.sql`：
   - `reservation_slots`：`slot_id` PK、`restaurant_id`、`slot_date`、`slot_time`、`capacity`、
     `booked`、`policy_version`、`created_at`，索引 `(restaurant_id, slot_date, slot_time)`；
   - `reservations`：`reservation_id` PK、`thread_id`、`user_id`、`restaurant_id`、`slot_id`、
     `party_size`、`status`、`hold_expires_at`、`idempotency_key`（**UNIQUE**）、`created_at` / `updated_at`；
   - **同一迁移**给 `conversation_checkpoints` 加 `pending_tool_call_id text` 与
     `pending_arguments jsonb NOT NULL DEFAULT '{}'::jsonb`（§2.3.3）。
2. **端口与应用服务**：`store.ReservationRepository`（`ListSlots / HoldSlot / ConfirmReservation /
   GetByIdempotencyKey / ReleaseExpiredHolds`）+ memory/postgres 双实现 + `shared/store/contract/reservation.go`；
   `chat-service/internal/reservation/service.go` 承载业务规则（容量校验、hold TTL、幂等、状态流转）。
3. **工具**（仅 `RESERVATION_ENABLED=true` 时注册，关闭时模型根本看不到它们）：
   - `get_availability`（只读）：`restaurant_id`、`date`、`party_size` → 可用时段；
   - `request_reservation`（确认类写入）：`restaurant_id`、`slot_id`、`party_size`。
4. **确认闸门（通用机制，M5 的核心新增）**：
   - `toolreg.Entry` 增加 `RequiresConfirmation bool`；`Register` 校验：
     `Spec.ReadOnly == false` 的条目必须显式声明该字段（默认非只读 ⇒ 需要确认），
     否则注册失败——把"忘记加确认"变成启动期错误，而不是线上的一次误写。
   - `Runner.runTools` 拦截：需要确认的工具**不执行**，而是写
     `PendingAction = <tool name>`、`PendingToolCallID`、`PendingArguments`、
     `State = awaiting_confirmation`，中途保存 checkpoint，发 `confirmation.required`（含人类可读摘要），
     answer 输出"摘要 + 请回复确认"。
   - `POST /v1/conversations/:id/confirm`：`{"decision":"confirm"|"cancel"}`。
     confirm → 从 checkpoint 读出挂起动作与参数 → 执行 → 清 pending（state 回 `idle`/`completed`）；
     cancel → 清 pending 并回复已取消；无挂起动作 → `agent_no_pending_action`(400)。
5. **幂等与并发**：
   - `idempotency_key` 由服务端派生：`hash(thread_id + action + 参数摘要 + checkpoint_version)`，
     **不由模型提供**；
   - 重复确认（网络重试、用户连点）必须返回同一预约号，而不是第二条预约；
   - 容量：`HoldSlot` 在事务内做条件更新（`booked + party_size <= capacity`），失败返回
     `reservation_unavailable`(409)；hold 过期（`RESERVATION_HOLD_TTL`，默认 10m）后需重新查询。
6. **关闭即安全**：`RESERVATION_ENABLED=false`（默认）时工具不注册、`/confirm` 返回 400、
   M5 其余任务的测试完全不涉及预约。

**实现要点：**

- **模型不能直接写库**：预约只能经类型化工具，且执行点在 `hitl/confirm.go`（服务端），
  不在模型手里；模型能做的只是"请求"，不能"确认"。
- 摘要文案要包含：餐厅名、日期时间、人数、政策版本——这正是 PRD §3.4 要求的"最终摘要"。
- 副作用边界：本任务**不做**取消/改期（PRD P2，MVP 可延后）、不做故障注入、不做等候名单；
  这些写进文档而不是顺手实现。

**测试：**

- 确认闸门：非只读工具被调用后**没有**任何写入（断言仓储调用计数为 0）+ checkpoint 处于
  `awaiting_confirmation` + 收到 `confirmation.required`；
- confirm 后：恰好一条预约、状态 `confirmed`、唯一键存在；重复 confirm 仍是同一条（200 同体）；
- cancel 后：无预约、pending 清空、state 回 `idle`；
- 无挂起动作 confirm → `agent_no_pending_action`(400)；
- 容量不足 → `reservation_unavailable`(409) 且无写入；hold 过期 → 提示重新查询；
- 关闭开关时：`get_availability` / `request_reservation` 不在 `Registry.Specs()` 中。

**验收标准：** §1.2 第 6 条；`make test-postgres` 覆盖预约契约。

**工作量：L。依赖：M5-05（挂起状态与事件）、M4-04（注册表）、M4-07（审计）。**

---

### M5-07 记忆写入与用户可见管理（计划外补充）

> 补充理由：M4-09 明确把"只保存用户明确要求记住的内容"留给 M5；技术 PRD §3.5 把
> 「保存用户明确要求记住的菜系/区域/预算/饮食偏好」「支持查看、修改和删除偏好」
> 「不把单次搜索条件自动升级为长期偏好」全部列为 **P0**。计划 §6 的 M5 行没有覆盖它，
> 因此在 M5 内补充本任务（M5-01..06 编号不动，避免与计划错位）。

**目标**：让记忆从"只读注入"变成"用户明确授权的可管理资产"，并且"明确"这件事是可验证的输入，
而不是模型的自我声明。

**交付物：**

1. **工具 `save_memory`**（`tools/save_memory.go`）：
   - 参数：`content`（必填，≤200 字符）、`memory_type`（`preference|constraint|fact`）、
     `quoted_user_text`（必填，用户原话片段）；
   - **护栏（可离线单测的确定性规则）**：`quoted_user_text` 必须是**当轮用户消息的子串**
     （归一空白与大小写后比较），否则返回 `memory_write_not_requested`(400)；
     这条规则把"用户是否明确要求"变成输入校验，而不是一句 prompt 约束；
   - 写入：同 `user_id` + 同 `memory_type` + **完全相同** `content` 视为刷新（Upsert，更新 `updated_at`），
     不新增行；`Source="user_request"`；`Confidence` 由类型规则给定（constraint 1.0 / preference 0.9 / fact 0.8）。
2. **用户可见管理**：
   - `GET /v1/memories`（已有）、`DELETE /v1/memories/:id`（已有，软删）；
   - 新增 `PATCH /v1/memories/:memory_id`：修改 `content` / `memory_type`，走归属校验
     （不是本人的 id → `not_found`，不泄露存在性），`Upsert` 语义；
   - 三处都要求 `X-User-ID`（沿用 M4 的占位身份约定）。
3. **事件与提示**：SSE 新增 `memory.saved`（`memory_id / memory_type / content`），
   回答里用自然语言确认"已记住：不吃辣"，让用户不必去看接口。
4. **配置**：`AGENT_MEMORY_WRITE_ENABLED`（默认 `true`，仅当 Memories 端口存在时注册工具；
   设为 `false` 可整体关闭写入）。

**实现要点：**

- **不做自动记忆**：不推断、不汇总、不把本轮条件升级为长期偏好（PRD §3.5 红线）。
  唯一入口是用户原话 + 子串校验。
- 记忆的 `embedding` 列（M4 已建，`vector(1024)` NULL）本任务**不使用**：
  记忆条数在注入窗口内（默认 10 条），按类型分组注入即可；语义检索留到 M6 之后按需评估。
- 修改记忆不产生新行：软删 + 新增会使用户看到两条状态，与"可修改"的直觉不符。

**测试：**

- 护栏：`quoted_user_text` 不在用户消息中（模型自行编造的条件）→ 拒绝且无写入；大小写/空白差异 → 通过；
- 幂等刷新：同一句"记住我不吃辣"两次 → 1 行且 `updated_at` 前进；
- 用户可见：`PATCH` 改 `content` 后 `GET /v1/memories` 立即反映；别人的 `memory_id` → `not_found`；
- 注入闭环：写入 constraint 后下一轮 system 段包含它（复用 M4 已有的注入测试）；删除后不再出现；
- 关闭开关：`save_memory` 不在 `Registry.Specs()` 中，且 `AgentConfig` 校验通过。

**验收标准：** §1.2 第 7 条。

**工作量：M。依赖：M4-09（记忆端口与注入）、M5-05（事件机制）。**

---

## 5. 推荐执行顺序与并行化

### 5.1 单人执行

```text
M5-01                        # 切片 0：意图与槽位（后面全部任务的输入质量取决于此）
M5-02 → M5-04                # 切片 1：软条件 → 解释结构与回放
M5-03 → M5-05                # 切片 2：实体消歧 → 候选/指代/状态机
M5-07                        # 切片 4：记忆写入（可与切片 2 并行，先做也可）
M5-06                        # 切片 3：可选预约（最后，默认关闭）
```

每完成一个任务跑一次 `make test && make vet`；涉及仓储的任务追加 `make test-postgres`。

### 5.2 并行建议

- M5-02 与 M5-03 无依赖，可并行（前者改检索/回答层，后者加工具与澄清）。
- M5-07 只依赖 M4-09 与事件机制，可在 M5-03 期间并行推进。
- M5-06 的迁移与仓储实现不经过 Agent，可与 M5-05 的指代/状态机部分并行。

### 5.3 纵向切片（每片都可独立演示）

| 切片 | 任务 | 演示方式（不需要 UI） |
| --- | --- | --- |
| 理解片 | M5-01 | `POST /v1/restaurants/interpret` 看结构化条件；再走一次搜索看候选卡片 |
| 推荐片 | M5-02 + M5-04 | SSE 流里看软条件标注、匹配原因、数据时间；`GET /v1/runs/:id` 看工具链 |
| 问答片 | M5-03 | 模糊餐厅名 → 澄清 → 选一家 → 证据 ≥2 条的回答 |
| 多轮片 | M5-05 | 推荐 5 家 → "第二家安静吗" → 只看该店的证据与引用 |
| 预约片 | M5-06 | 查库存 → 收到 `confirmation.required` → confirm → 查 `reservations` 表 |
| 记忆片 | M5-07 | "记住我不吃辣" → `memory.saved` → 下一轮回答自动排除辣味 → 删除后失效 |

---

## 6. M5 完成定义（DoD）检查清单

### 6.1 每个任务通用 DoD（继承实施计划 §10）

- [ ] 任务目标对应的验收标准全部满足，并有对应测试。
- [ ] 新增公开接口有用法注释；包注释说明分层位置。
- [ ] 领域层无厂商/框架依赖；Eino 类型不跨出 `agent/{einomodel,toolreg}`；新增 `shared/domain` 包只依赖标准库。
- [ ] 新增配置有默认值、校验、启动摘要与 `.env.example` 文档；密钥只从环境读取且日志脱敏。
- [ ] 应用层错误使用 `*errs.Error`，HTTP 层只做 code→status 映射；新错误码在 `errs` 中追加并配映射。
- [ ] 单测覆盖成功路径与主要失败路径；数据库行为进 `shared/store/contract`，memory + postgres 双实现通过。
- [ ] 涉及 SQL 的任务补迁移文件与索引；迁移可重复执行（`make migrate` 两次结果一致）。
- [ ] `/v1` 响应体变更按客户端契约处理（新增字段可，改语义不可）。
- [ ] `make test && make vet && make build` 全绿；`make test-postgres` 在真库全绿。
- [ ] 会话/记忆/候选/预约的 `user_id`、`thread_id` 隔离在测试中断言（不串线程、不串用户）。

### 6.2 里程碑门（§1.2 的可勾选版本）

- [ ] 1. 七条切片本机可演示；工具与检索过程可查（M5-01…M5-07）。
- [ ] 2. 硬条件 100% 正确 + `make eval-retrieval` 不回归（M5-01）。
- [ ] 3. 软条件不落硬过滤且标注评论推断（M5-02）。
- [ ] 4. 指定餐厅问答证据 ≥2 条、引用不越界（M5-03）。
- [ ] 5. 候选落库 + 指代可解析 + 状态跨请求存活 + 重启可续（M5-05）。
- [ ] 6. 确认前零写入、幂等键唯一（M5-06）。
- [ ] 7. 记忆只在用户明确要求时写入且可管理（M5-07）。
- [ ] 8. 全量离线测试全绿；真实渠道 + 本地 embedding 下跑一遍附录 B 的六条演示。

---

## 7. 与后续里程碑的衔接

- **M6-01（Trace 与日志）**：M5-04 提供的 run 读路径是起点；M6-01 在其上补节点级耗时、token、
  检索候选明细（不推翻 M5 的 HTTP 形状，只扩展字段）。
- **M6-02/03（评测）**：M5 的七条切片各留 2–3 个固定查询（`interpret` 用例 + Agent 场景），
  直接作为 RAG 与 Agent 评测集的种子；Agent 数据集还要覆盖"缺槽澄清 / 禁止工具 / 数据缺失回答未知"。
- **M6-05（性能）**：`Plan` 抽取（一次结构化调用）与工具轮次是新增延迟来源，
  需要与首 token 延迟一起测量。
- **M6-06（演示 UI）**：新的 3 个 SSE 事件（`state.awaiting_input` / `confirmation.required` /
  `memory.saved`）是前端对话流的契约；澄清与确认按钮直接打 `/confirm` 端点。
- **M7（部署）**：`RESERVATION_ENABLED`、`AGENT_MEMORY_WRITE_ENABLED`、`AGENT_RESOLVE_*`
  进部署模板；Mock 预约默认关闭必须写进运行手册。

---

## 8. 风险与注意事项

1. **槽位抽取是模型输出**：必须"结构化输出 + 校验 + 规则兜底"三层，任何一层失败都退化为全文检索，
   绝不因为抽取失败拒绝服务；抽取结果永远经 `Filter.Validate()`。
2. **中英混杂与口语地名**：`中城 / Midtown / 下东城` 这类映射只能靠别名表；
   未识别的片段保留在 `Query` 走文本与向量通道，并在 warnings 说明"未能识别为结构化条件"。
3. **软条件被当成硬事实**（产品红线）：软条件不得进入 `RestaurantFilter`；
   回答中由评论推断的结论必须显式标注。这条要用测试断言，而不是靠 prompt 自觉。
4. **澄清死循环**：模型反复要求澄清会让用户烦、也会烧 token；用 `AGENT_MAX_CLARIFICATIONS`
   给上限，超限走"默认假设 + 说明"。
5. **指代解析歧义**：`第二家` 依赖候选顺序，而候选顺序会随检索配置变化；解析结果必须回显给用户
   （"按你对第 2 家的追问"），让错误可被察觉。
6. **确认闸门被绕过**：只读标志与确认标志都在服务端注册期确定；模型无法伪造"已确认"。
   任何"直接执行写工具"的代码路径都必须在评审中被否决。
7. **幂等键设计错误**：键必须包含线程、动作、参数摘要与版本；键太松会吞掉不同的请求，
   太紧会让重试产生两条预约。重复确认返回同一结果要有测试。
8. **Mock 库存并发**：MVP 用单库事务 + 条件更新，不引入分布式锁；并发结果只需"不超过容量"，
   这一点要在测试里明确断言，并在文档里标注为 Mock 能力的边界。
9. **候选覆盖时机**：只有"本轮确实搜到了候选"才覆盖；追问轮把上一轮的候选清掉会让指代解析瞬间失效。
10. **记忆污染**：`quoted_user_text` 校验是硬门；不要为了体验放松它——一条错误记忆会污染之后所有轮次。
11. **审计与隐私**：新增的 run 读接口只回脱敏摘要；预约与记忆不存联系方式等 PII；
    `memory.saved` 事件只回内容与类型，不回 `user_id`。
12. **事件契约是破坏性变更**：transport 对未知事件类型直接报错，新增事件必须
    `agent.Event` → `StreamEvent` → `sse.payload` 三处同时改，并同步 README 与 M6-06 前端。
13. **离线优先**：所有新机制（槽位、指代、确认闸门、幂等、记忆护栏）都必须能在
    Mock Provider + 内存仓储下断言；需要真库的用例走 `PLATEPILOT_REQUIRE_DB=1` 门禁，
    不允许"因为没库所以静默通过"。
14. **不做的事**：预约取消/改期、故障注入、真实库存、记忆自动推断、节点级 trace、演示 UI ——
    全部留到 M6/M7，写在这里是为了避免实施时顺手扩范围。

---

## 附录 A：环境变量（M5 相关）

```bash
# --- 既有必需（M3/M4，M5 复用，不改语义）-----------------------------------
# POSTGRES_DSN=postgres://platepilot:platepilot@localhost:55432/platepilot?sslmode=disable
# EMBEDDING_PROVIDER=ollama
# OLLAMA_BASE_URL=http://localhost:11434
# EMBEDDING_MODEL=qwen3-embedding:0.6b
# EMBEDDING_DIMENSIONS=1024
# RETRIEVAL_TOP_K=5
# RETRIEVAL_ENABLE_VECTOR=true          # 软条件切片依赖向量通道
# RETRIEVAL_WEIGHT_VECTOR=1.0
# CHAT_PROVIDER=openai_compatible
# CHAT_BASE_URL=<OpenAI 兼容端点>
# CHAT_API_KEY=<只进 .env>
# CHAT_MODEL=<支持 function calling 的模型>
# CHAT_SUPPORTS_TOOLS=true
# CHAT_SUPPORTS_JSON_SCHEMA=false       # 关：走 json_object 退化路径
# AGENT_MAX_TOOL_ROUNDS=5
# AGENT_TOOL_TIMEOUT=15s

# --- M5 新增 ----------------------------------------------------------------
# 实体消歧（M5-03）
# AGENT_RESOLVE_MIN_SIMILARITY=0.55     # 名称相似度达到此值才算命中
# AGENT_RESOLVE_AMBIGUITY_GAP=0.10      # top1 与 top2 差距小于此值判为歧义，需澄清
# 澄清与追问（M5-03/M5-05）
# AGENT_MAX_CLARIFICATIONS=3            # 单线程连续澄清上限，超限用默认假设继续
# 记忆写入（M5-07）
# AGENT_MEMORY_WRITE_ENABLED=true       # 仅在配置了记忆仓储时注册 save_memory
# Mock 预约（M5-06，默认关闭）
# RESERVATION_ENABLED=false
# RESERVATION_HOLD_TTL=10m
# RESERVATION_PARTY_SIZE_MAX=8
```

## 附录 B：常用命令速查

```bash
# 0. 基线（全程离线）
make test && make vet && make build

# 1. 起服务（先 ensure 数据库已迁移）
make pg-up && make migrate
make run-chat            # :8080；启动日志中的 configuration loaded 即配置自检

# 2. 切片 0：意图与槽位（硬条件抽取，不依赖聊天渠道也能看到 rules 结果）
curl -s -X POST http://localhost:8080/v1/restaurants/interpret \
  -H 'Content-Type: application/json' \
  -d '{"query":"曼哈顿中城 4 星以上、适合约会的意大利菜"}'
#   期望：intent=recommend；hard_filters 含 borough=manhattan、min_rating=4、cuisines=[italian]；
#        soft_conditions 含 {text:"适合约会", topic:"ambience"}；source=model|rules

# 3. 建线程（后续切片共用）
curl -s -X POST http://localhost:8080/v1/conversations \
  -H 'Content-Type: application/json' -H 'X-User-ID: demo-user' \
  -d '{"title":"nyc slice demo"}'
THREAD_ID=<上一步返回的 thread_id>

# 4. 切片 1：推荐 + 解释（SSE）
#    期望帧序：message.start → tool.start(search_restaurants) → tool.finish
#             → tool.start(get_restaurant_evidence) → tool.finish
#             → message.delta*（含匹配原因、评论推断标注、数据时间）→ citation → message.end
curl -N -X POST "http://localhost:8080/v1/conversations/${THREAD_ID}/messages" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream' -H 'X-User-ID: demo-user' \
  -d '{"content":"帮我找曼哈顿中城安静、适合约会的意大利餐厅，4 星以上"}'

# 5. 切片 2：指定餐厅问答（歧义时会先澄清）
curl -N -X POST "http://localhost:8080/v1/conversations/${THREAD_ID}/messages" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream' -H 'X-User-ID: demo-user' \
  -d '{"content":"Katz 的服务怎么样？"}'
#   若收到 event: state.awaiting_input（missing_slots=[restaurant_id]），回答候选序号后继续：
curl -N -X POST "http://localhost:8080/v1/conversations/${THREAD_ID}/messages" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream' -H 'X-User-ID: demo-user' \
  -d '{"content":"第一家"}'

# 6. 切片 3：多轮追问（候选由 0007 落库，位置语义稳定）
curl -N -X POST "http://localhost:8080/v1/conversations/${THREAD_ID}/messages" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream' -H 'X-User-ID: demo-user' \
  -d '{"content":"第二家安静吗？"}'
curl -s "http://localhost:8080/v1/conversations/${THREAD_ID}" -H 'X-User-ID: demo-user'
#   期望：checkpoint.selected_restaurant_id == 第二轮候选的第 2 家；state=idle

# 7. 切片 1 的回放：工具链与引用可核对
curl -s "http://localhost:8080/v1/conversations/${THREAD_ID}/runs?limit=5" -H 'X-User-ID: demo-user'
curl -s "http://localhost:8080/v1/runs/<run_id>" -H 'X-User-ID: demo-user'
#   期望：tool_calls 序列（search_restaurants → get_restaurant_evidence）、状态、耗时、结果摘要

# 8. 切片 4：Mock 预约（需 RESERVATION_ENABLED=true 重启服务）
curl -N -X POST "http://localhost:8080/v1/conversations/${THREAD_ID}/messages" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream' -H 'X-User-ID: demo-user' \
  -d '{"content":"帮我订周六晚上 7 点、两个人的位置"}'
#   期望：event: confirmation.required（含餐厅/时间/人数摘要），此时数据库不应有预约行
curl -s -X POST "http://localhost:8080/v1/conversations/${THREAD_ID}/confirm" \
  -H 'Content-Type: application/json' -H 'X-User-ID: demo-user' \
  -d '{"decision":"confirm"}'
#   再发一次同样的 confirm：应返回同一 reservation_id（幂等）
psql "$POSTGRES_DSN" -c \
  "SELECT reservation_id,status,slot_id,party_size,idempotency_key FROM reservations ORDER BY created_at DESC LIMIT 5;"

# 9. 切片 5：记忆写入与管理
curl -N -X POST "http://localhost:8080/v1/conversations/${THREAD_ID}/messages" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream' -H 'X-User-ID: demo-user' \
  -d '{"content":"记住我不吃辣"}'
#   期望：event: memory.saved
curl -s http://localhost:8080/v1/memories -H 'X-User-ID: demo-user'
curl -s -X PATCH http://localhost:8080/v1/memories/<memory_id> \
  -H 'Content-Type: application/json' -H 'X-User-ID: demo-user' \
  -d '{"content":"不吃辣，也不吃香菜"}'
curl -s -o /dev/null -w '%{http_code}\n' -X DELETE \
  http://localhost:8080/v1/memories/<memory_id> -H 'X-User-ID: demo-user'   # 204

# 10. 门禁
make test-postgres        # 契约套件（候选 / 预约 / run 读）
make eval-retrieval       # 硬条件零泄漏 + 软条件 recall 不回归
psql "$POSTGRES_DSN" -c \
  "SELECT position,restaurant_id,name,reasons FROM conversation_candidates ORDER BY thread_id,position LIMIT 10;"
psql "$POSTGRES_DSN" -c \
  "SELECT run_id,status,tool_call_count,latency_ms,token_input,token_output FROM agent_runs ORDER BY started_at DESC LIMIT 5;"
```

## 附录 C：纵向切片验收矩阵

| 切片 | 计划条目 | 关键断言 | 自检方式 |
| --- | --- | --- | --- |
| M5-01 硬条件 | M5-01 | 候选 100% 满足硬过滤；抽取可分硬/软；无渠道时降级 rules | `interpret` curl + 切片级单测 + `make eval-retrieval` |
| M5-02 软条件 | M5-02 | 软条件不进 Filter；向量通道 ran；理由标注"评论推断 + 主题" | 单测断言 + SSE 文本检查 |
| M5-03 指定餐厅 | M5-03 | 歧义 → `awaiting_clarification`；证据 ≥2；引用只属于该店 | 四类消歧单测 + 真库手工演示 |
| M5-04 解释与回放 | M5-04 | 事件序含两类工具；`GET /v1/runs/:id` 回放一致；引用不越界 | 离线端到端 + curl |
| M5-05 多轮 | M5-05 | `selected_restaurant_id == position 2`；状态跨请求存活；重启可续 | 两轮测试 + 重建 Runner 测试 |
| M5-06 预约 | M5-06 | 确认前零写入；幂等键唯一；关闭时工具不可见 | 仓储计数断言 + psql 核对 |
| M5-07 记忆 | M5-07（补充） | 无 `quoted_user_text` 即拒绝；可改可删；不自动升级 | 护栏单测 + `GET/PATCH/DELETE /v1/memories` |
| 计划 §15 「第一个可演示版本」 | — | 自然语言输入 / 搜索+证据各至少一次 / 1–5 候选 / 来源+快照时间 / 可追问 / 工具过程可查 | 附录 B 第 2–7 步依次跑通 |

## 附录 D：参考文档

- 仓库内：`README.md`（产品事实与验收指标）、`docs/platepilot-technical-prd.md`、
  `docs/platepilot-implementation-plan.md`、`docs/platepilot-m4-task-document.md`、
  `AGENTS.md`、`chat-service/AGENTS.md`、`shared/AGENTS.md`。
- 外部：
  - Eino Graph / Lambda 节点：<https://www.cloudwego.io/docs/eino/core_modules/flow_integration_components/>
  - Eino ToolsNode 与工具描述：<https://www.cloudwego.io/docs/eino/core_modules/components/tools_node_guide/>
  - OpenAI Chat Completions（工具调用与结构化输出）：<https://platform.openai.com/docs/api-reference/chat>
  - pgvector（HNSW 与过滤）：<https://github.com/pgvector/pgvector>
  - Hertz SSE：<https://pkg.go.dev/github.com/cloudwego/hertz/pkg/protocol/sse>

## 附录 E：实施记录（M5 实际落地时的发现）

> 按 `AGENTS.md` §8：实施过程中的偏差追加在此处，不回改前面的计划。
> 记录格式：[任务 ID] 计划 vs 实际 + 原因 + 影响。

开工时列出的四项待确认项均已关闭，逐条记录如下（细节见对应任务小节）：

1. `POST /v1/conversations/:id/resume` —— **关闭：不新增该端点。**
   PRD §7.2 里的恢复端点由「继续发下一条消息」+ `GET /v1/conversations/:id`
   （读状态）替代：`ingress` 读到 checkpoint 后补槽并续跑既有 `PendingAction`，
   复用 M4-08 的 `Runner.Resume` 语义。决定见 §M5-05「状态机扩展」第 3 条。
2. 自然语言"附近/近我" —— **关闭：降级为行政区/商圈并留痕。**
   项目没有地理编码器，无法把"附近"变成可度量的中心点；
   `slots.normalize.warnUngeocodableProximity` 把它降级为 borough/neighborhood
   过滤并写入 `Plan.Warnings`，而不是静默丢弃。M6 未引入 geocoder，
   该限制作为已知取舍保留在 `README.md` §Known limitations。
3. `chat-service check-config` —— **关闭：已补做。**
   `chat-service/main.go` 提供 `check-config` 子命令，加载并校验配置后打印摘要并退出
   （`chat-service/Makefile:check-config`），与 `data-pipeline check-config` 对齐。
4. Conversation 未读/离线工具与 `Resume` 语义 —— **关闭：定型为"读 checkpoint 继续"。**
   不重放整轮：回放走只读的 run/trace 端点，续跑走「下一条消息 + checkpoint」。
   两者的分工在 §M5-05「实现要点」中确立——历史回放只取 user/assistant 文本，
   候选与挂起状态走 checkpoint，两条路径互不干扰。

---

### [M5-01] 意图与槽位层 + 硬条件搜索切片

**① 计划 vs 实际：计划把 Plan 注入实现为"重写模型参数"，实际实现为"经 ctx 传递 Plan，处理器内部补齐"。**

计划 §`M5-01` 隐含的做法是：模型调用 `search_restaurants` 前，由运行时把
`slots.Plan` 里的硬条件合并进模型给出的 JSON 参数，让处理器看到的参数永远完整。
实施时改为：Plan 挂在工具调用的 `context` 上（`slots.WithPlan` /
`slots.PlanFromContext`），`searchRestaurants` 处理器自己调用
`applyPlanDefaults(args, plan)` 补齐。

**原因**：`toolreg.Registry.Invoke` 在执行处理器**之前**就用 JSON Schema 校验
参数。若走"先重写参数再校验"，模型给出一份形状错误的参数（例如
`price_levels` 传成字符串）会被理解层的补齐动作"修好"，然后悄悄通过校验——
调用方失去发现模型出错的机会。走 ctx 传递则参数校验仍然只面对模型的原样输出，
模型的错误照旧被报成工具错误，而理解层的补齐只发生在处理器内部、且是
"填缺不覆盖"（`applyPlanDefaults` 只在字段为空时填充）。

**影响**：
- 正向：模型参数错误的可观测性未被削弱；回归测试
  `TestExplicitModelArgumentsAreNotOverridden` 与
  `TestHardConditionsSurviveAModelThatOmitsThem` 分别钉住"模型给出的不被覆盖"与
  "模型漏给的被补齐"两条边界。
- 正向：Plan 是纯值类型（不含 Eino / 驱动类型），可被 `httpapi` 直接投影，
  `POST /v1/restaurants/interpret` 才能做成无状态探针。
- 负向：工具处理器不再能假设"参数即全部条件"，任何新增工具若想消费 Plan
  必须显式读取 ctx；该约定由 `slots_slice_test.go` 中的切片级用例兜底。

**② 计划 vs 实际：「附近/近我」从"待确认"变为"已实现降级"。**

计划见上文已知项 2。实际实现为：`slots.normalize.warnUngeocodableProximity`
在扫描到"附近/近我/near me/nearby/around here"等表述时，不猜测坐标，而是在
`Plan.Warnings` 追加一条"未识别地理位置，已按商圈/borough 处理"的说明，该
warning 随 `interpret` 响应与工具结果一并返回给用户。

**原因**：M5 没有 geocoder，静默丢弃会使用户以为"附近"被理解了。留痕比沉默好。

**影响**：已知项 2 关闭；M6 引入 geocoder 后，只需把该 warning 替换为真实的
地理半径过滤，`Plan` 结构无需改动。

**③ 计划 vs 实际：`chat-service check-config` 从"可选补做"变为"已实现"。**

已知项 3 落地为 `main.go` 的子命令分发器：`serve`（默认，保证既有调用方式不变）、
`check-config`、`version`、`help`。`check-config` 执行 Load → Validate →
输出 `cfg.Redacted().Summary()` 的缩进 JSON，并配 `make check-config`。

**原因**：配置项在 M5 显著增多（新增 4 个 agent 旋钮 + 后续预约/记忆开关），
一个能在部署前验证环境变量并打印脱敏结果的入口，比事后从启动日志里猜要省事。

**影响**：已知项 3 关闭。`ResolveCommand` 的空参数默认 `serve` 行为由
`main_test.go` 表驱动测试固定，避免以后有人改动分发逻辑而破坏既有部署脚本。

**④ 计划外的必要偏差：软条件词汇表下沉为 `shared/domain/review`。**

计划把主题词表放在 `data-pipeline` 的清洗侧。实施时发现 `chat-service` 的抽取
schema 需要同一份枚举（`topic` 字段取值），两端各自维护必然漂移。实际做法是
把 `TopicFood/Service/Ambience/Value/Wait/KidFriendly/GroupFriendly` 与
`TopicLabels`/`CanonicalTopics()` 下沉到 `shared/domain/review`，
`data-pipeline` 改为消费它，并新增 `topics_drift_test.go` 防止清洗侧再长出私有词表。

**原因**：一个枚举出现在两处，就一定会有一处先改。

**影响**：`shared/domain/review` 成为软条件主题的唯一来源；
`buildExtractionSchema()` 的 `topic` enum 由 `review.CanonicalTopics()` 生成，
主题"存在于抽取值域但不存在于清洗侧"在类型层面即不可能。

---

### [M5-02] 软条件推荐与排序解释切片

**① 计划 vs 实际：`retrieval.Request.SoftConditions` 是 `[]SoftCondition{Text,Topic}`，不是 `[]string`。**

计划写的是 `SoftConditions []string`，并在向量通道把 embedding 文本拼成
`Query + 软条件原文 + 主题词`。实施时发现：如果请求里只有原文，
"主题词"就只能在检索层重新推导一遍，而推导规则（`安静 → ambience`）已经在
理解层存在（`slots.softTopicForValue`）。两份表就是两次漂移机会，
且漂移的症状是"召回悄悄少了一半信号"。

实际做法是在 `shared/domain/retrieval` 里声明：

```go
type SoftCondition struct {
    Text  string // 用户原话
    Topic string // 对应评论主题，可能为空
}
```

`slots.SoftCondition` 改为它的类型别名（`type SoftCondition = retrieval.SoftCondition`），
于是从句子到请求到 trace 到提示词，全程是同一个类型，不需要在每一跳做转换。

**原因**：一个"用户词汇 → 语料标签"的映射只应该被做一次。

**影响**：
- 正向：`answer.Input.SoftConditions` 与 `retrieval.Request.SoftConditions` 同类型，
  无需拷贝；主题为空的软条件仍然保留原文，只是无法与语料标签对上。
- 负向：`slots` 因此 import `shared/domain/retrieval`（`answer` 也如此）。
  方向是单向的、无环的，且 `retrieval` 不认识 `slots`，所以没有把检索层
  耦合到对话服务上。若将来需要解耦，别名改回本地声明即可，调用点不用动。
- 记录：`answer.Input.SoftConditions` 写成 `[]retrieval.SoftCondition` 而不是
  计划里的 `[]slots.SoftCondition`。两者是同一个类型，选前者只是为了不让
  回答组装层 import 槽位抽取层。

**② 计划 vs 实际：软条件是否"有证据支持"，由代码判定，不交给模型。**

计划只要求 system instruction 第 6 条约束"没有任何证据支持的条件必须列入
『无法确认』"。实施时发现模型无法执行这条：它看到用户写"安静"，又看到一堆
`topic=ambience` 的资料，而没有任何东西告诉它这两者是同一件事。
把判定留给模型，等于把"无法确认"清单交给一次措辞上的偶然。

实际做法是 composer 在拼提示词时按条件逐条给出结论
（`<soft_conditions>` 块）：命中主题且有 N 条证据 → "可作答但必须标明依据来自评论"；
主题命中但本次没有对应证据 → "必须列入「无法确认」"；
完全对不上任何主题 → "无法与评论主题对应，必须列入「无法确认」"。

**原因**：可枚举的判定放在代码里，措辞交给模型；反过来做，判定的可复现性就没了。

**影响**：§1.2 第 3 条（软条件可解释且永不进 `RestaurantFilter`）有了逐条可核对的
实现，断言落在 `TestComposeSortsSoftConditionsByWhetherEvidenceSupportsThem`。

**③ 计划 vs 实际：`Search` 的"空请求"守卫要算上软条件。**

原守卫是 `text == "" && query == "" && Filter.IsEmpty()` → 报
`retrieval_empty_query`。仅软条件的搜索在这个判据下会被判成空请求并 400，
而它恰恰是 M5-02 要支持的输入。改为再与上 `!req.HasSoftConditions()`。

**影响**：`TestASoftOnlySearchIsNotAnEmptySearch` 与
`TestAnEmptyRequestIsStillRefused` 分别钉住两侧。

**④ 计划外的文案修正：工具结果里的"降级提示"改为"检索说明"。**

§5 要求把软条件说明一并透传到 `message.end.warnings`，实现方式是把它写进
`Trace.Channels[vector].Note`，由 `Fuse` 收进 `Trace.Warnings`。于是这条
warnings 列表同时承载"真降级"和"通道说明"，而工具结果把它渲染为"降级提示："——
把一次成功的向量召回说成降级。改为"检索说明："（`restaurant_evidence.go` 的
证据链路未改，那里的 warnings 仍然只表示降级）。

**影响**：`sse` 契约字段名 `warnings` 未变；`TestSearchRestaurantsReportsDegradations`
的期望字符串随之更新。

**⑤ 补充：system instruction 除第 6 条外还加了第 7 条。**

计划只点名第 6 条。但"每条推荐附数据时间与来源"同样是硬要求，且与第 6 条
约束的对象不同（第 6 条管"由评论推断的结论"，第 7 条管"每条推荐"）。
两条分开写，测试可以分别断言。第 5 条（FOLLOWUPS 格式）保持在原位，
新规则追加在其后，编号即文档里的"第 6 条"。

**待验证（环境所限）**：§测试项 "`make eval-retrieval` 软条件 recall 不低于 M3 基线"
本次未能执行——本机没有运行 Ollama（`localhost:11434` 不可达），
`TestRetrievalFixtures` 因向量通道不可用而 recall 停在 0.467（门槛 0.6），
这与 M5-02 的改动无关（无软条件的用例 embedding 文本与改动前逐字节相同，
由 `TestVectorChannelEmbedsTheBareQueryWhenNothingIsSoft` 钉住）。
需在有 embedding 服务的环境补跑该 gate。

---

### [M5-04] Agent 推荐解释切片（工具链回放 + 解释结构）

**① 计划 vs 实际：`answer.Input` 实际有 7 个字段，不是计划里的 5 个。**

计划写 `{Question, Candidates, Filters, SoftConditions, Evidence}`；实现要点又要求
"解释文案里'无法确认'必须来自真实缺失：`Plan.MissingSlots`、软条件无证据、
以及 `Trace.Warnings` 中的降级"。后两项在 5 字段结构里没有入口。实际加了
`MissingSlots []string` 与 `Warnings []string`，并在提示词里渲染成 `<gaps>` 块：

```
<gaps>
以下内容本次确实无法确认，必须出现在「无法确认」里：
- 用户未提供的条件：party_size（不要替用户假设）
- 检索降级：向量通道已关闭
</gaps>
```

**原因**：把"无法确认"的素材留在 `TurnState` 里而只把 5 个字段交给 composer，
等于要求模型写一段它看不到依据的文字。

**影响**：每个 `<gaps>` 行都可追溯到本轮真实发生的事（缺槽位 / 通道降级），
没有内容时整块不出现（`TestComposeOmitsTheGapsBlockWhenNothingIsMissing`）。
固定免责声明被排除在实现之外，测试也断言了这一点。

**② 计划外的必要下沉：`search.RestaurantFilter.Describe()`。**

第 ① 步要求"硬条件逐条对应"，需要把过滤器渲染成一句中文。原渲染函数
`retrieval.describeFilters` 是 chat-service 内的私有函数。若在 answer 包里再写一份，
两处渲染就可能点名不同的条件子集——而用户看到的理由是唯一能核对的地方。

实际把渲染下沉为 `search.RestaurantFilter.Describe()`，
`retrieval.describeFilters` 改为转发（输出逐字节不变，M3/M4 的断言未受影响），
composer 用它生成 `<filters>` 块。

**原因**：同一句"用户实际要求了什么"被 trace、工具结果卡片和提示词三处引用。

**影响**：`<filters>` 里的条件与 trace 里的 `Filters` 必然一致；
`TestComposeTellsTheModelWhichHardConditionsWereEnforced` 断言了五个字段的渲染。

**③ 计划 vs 实际：回放是独立的 `httpapi.RunService`，不是加进 `ChatService`。**

计划只说"HTTP：`GET /v1/conversations/:id/runs`、`GET /v1/runs/:run_id`"。
实施时把它做成独立接口与独立 `httpapi.Config.Runs` 字段，而不是给 `ChatService`
再加两个方法。

**原因**：能回答"用户看到了什么"的部署不一定能回答"系统做了什么"。run 表只在
配了 Postgres 的部署里存在，把它塞进 `ChatService` 会让"内存装配"（会话仓储可用、
run 仓储缺失）被迫实现两个返回空的方法——那正是"写侧有的读侧不一定有"被抹平的写法。

**影响**：无 `Runs` 时两条路由不存在（`TestRunRoutesAreAbsentWithoutAService`），
且与转录路由互不遮挡（`TestRunRoutesCoexistWithTheTranscriptRoutes`）。

**④ 计划外的补强：run 列表先校验线程存在。**

计划只给了分页约定，没说未知线程怎么办。实际让 `app.runService.ListRuns` 先
`conversations.Get`：未知线程返回 `not_found`，与消息分页一致。

**原因**：run 仓储无法区分"线程存在但没跑过"和"没有这个线程"，
而一个空列表在两种情况下长得一样——回放接口的答案一旦有歧义，就没法用它排障。

**影响**：`TestListRunsRejectsAnUnknownThread` 与
`TestListRunsReturnsAnEmptyPageForAKnownThread` 分别钉住两侧。

**⑤ 计划 vs 实际：`GET /v1/runs/:run_id` 的响应是扁平的。**
run 自身的字段与 `tool_calls` 平铺在同一层（内嵌结构体），而不是
`{"run":{...},"tool_calls":[...]}`。

**原因**：回放是读一个对象，多一层信封只是多一次解包。

**影响**：契约字段名与 `run.AgentRun` 保持一致；`arguments` 原样返回入库时的
脱敏摘要，读路径不做二次加工（`TestGetRunReturnsTheRedactedArgumentsDigest`）。

**⑥ 计划已覆盖，无需改动：附录 B 第 7 步。**
"一次推荐 → 拉 run → 看工具链"的 curl 序列在计划里已经写全（第 7 段），
本次只是让这些命令真的能跑通，未修改文档。

---

### [M5-03] 指定餐厅问答切片

**① 计划 vs 实际：证据充分性阈值下沉到领域层，且"资料不足"由代码拼在回答开头。**

计划 §`M5-03` 交付物 3 说"至少 2 条不同文档类型的证据才构成可作答"，测试要求
"只有 1 条证据时回答文本含『资料不足』字样"。计划没有说这条判据放在哪里，也没说
那句声明由谁写。实施结果为：

- 阈值定义为 `evidence.MinAnswerableDocTypes = 2`（`shared/domain/evidence`），
  并附 `evidence.DocTypesFrom`；三个消费方（组装器、composer、测试）引用同一个常量。
- 判定与文案在 `chat-service/internal/agent/answer/adequacy.go`：`measureAdequacy`
  数文档数与类型数，`lead()` 生成前缀句，`contextBlock()` 生成 `<evidence_adequacy>` 提示块。
- `Compose` 在**引用校验通过之后**把 `lead()` 拼在模型文本之前；`buildMessages` 把
  `contextBlock()` 插在 `<evidence>` 与 `<filters>` 之间。

**原因**：
- 判定放领域层，是因为这条规则同时约束两个阶段的相反方向——组装器据此决定"预算能不能
  把证据砍到只剩一类"，composer 据此决定"能不能不带保留地作答"。两个字面量会漂移。
- 前缀句由代码写，与 `RefusalAnswer` 是常量出于同一个理由：这是一条"绝不能漏"的句子，
  而依赖生成模型句子通顺的规则只是一条"大多数时候成立"的规则。同时它是"本轮证据只有
  N 条、M 类"这种带数字的陈述，而不是每轮都一样的免责声明——重复出现的免责句会被读者
  训练成跳过，包括在它是唯一重要警告的那一轮。
- 拼在校验之后：这句话是对证据集的陈述而不是从证据推出的主张，它不携带引用，因此也不
  应该被重生成打乱顺序或丢掉。
- 另给提示块而非只靠前缀：对模型来说"证据很少"和"所以不许下整体判断"是两条不同指令；
  只给前者，模型会提一句资料少然后照旧写整体判断。

**影响**：
- 正向：`TestComposeStatesInsufficientEvidenceInTheAnswerItself`、
  `TestComposeCountsKindsNotDocuments`、`TestComposeDropsTheCaveatWhenTwoDocTypesSupportIt`、
  `TestComposeTellsTheModelTheEvidenceIsTooThinToGeneralise`、
  `TestComposeOmitsTheAdequacyBlockWhenTheEvidenceIsEnough` 覆盖两侧与阈值。
- 负向（既有测试需改，属预期成本）：`compose_test.go` 有 4 处断言在比较模型文本原样，
  fixture 只有一类文档，因而会带上前缀。改为新增 `adequateEvidence()` 辅助（两类文档），
  让这些用例继续只测它们各自关心的东西（引用解析、重试、FOLLOWUPS 解析），
  充分性本身由独立用例钉住。
- 负向：`TestComposeOrdersTheContextBlocks` 的块序断言必须加上 `<evidence_adequacy>`
  ——这正是块序测试存在的意义：新增一个块必须显式决定它排在哪里。

**② 计划 vs 实际：组装预算增加了第 4 遍"支撑下限"（`floorDocTypes`）。**

计划交付物 3 最后一句要求"组装预算不得把可用证据压到 1 条以下——预算冲突时优先保留
≤2 条不同 doc_type 的证据，其余整块丢弃"。实施为 `assemble.go` 中预算循环之后的第四遍：
若入选集只剩一类文档、且候选里有别的类型，则让"最弱的一条已入选文档"让位给"最强的
那条缺类型文档"，直到新文档装得下或入选集只剩 1 条。

**原因**：预算是按分数顺序放行的——对相关性正确，对支撑性错误：同一家店的 3 条评论摘要
是同一句话的 3 次复述，据此写出的回答分不清"资料记载的事实"和"评论普遍的印象"。
准入顺序不动，只在结果上补一次换位。

**影响**：
- 正向：`TestAssemblyTradesABudgetCutForDocTypeDiversity` 钉住换位；
  `TestAssemblyNeverTradesBelowOneDocument` 钉住"换不过去就放弃"（一条强引用好过一条弱引用）；
  `TestAssemblyDoesNotTradeWhenTheCorpusHasOneKind` 钉住"没得换时不造假 drop"；
  `TestAssemblyKeepsScoreOrderAfterTheTrade` 钉住换位后仍是分数序且可复现。
- 计划 vs 实际（口径）：换位产生的丢弃记在**新的**原因键 `doc_type_floor` 下，而不是
  并进 `token_budget`。因此 `AssemblyReport.Dropped`（净损失 = Considered − Kept）可能
  小于 `DroppedByReason` 各键之和——后者计事件，前者计净额。已在 `AssemblyReport` 的
  注释里写明；全仓库无消费方假设两者相等（`grep DroppedByReason` 只有测试与透传）。
- 中性：既有 14 个组装用例的候选集都只有一类文档，第 4 遍对它们恒为空操作，未改动任何断言。

**③ 计划 vs 实际：`resolve_restaurant` 的 `limit` 参数此前是死参数，改为"配置上限内的模型选择"。**

计划 Schema 列了 `limit`（1..5），`ResolveConfig` 也有 `Limit`，但实施第一版里处理器只用了
`cfg.Limit`，模型传的 `limit` 被完全忽略——schema 里宣传了一个不生效的旋钮。

**原因**：留着它是谎报能力；直接改用 `args.Limit` 又会让模型越过运营方设定的上限，
而那个上限决定"澄清问题能列几个选项"。折中为
`clampTopK(args.Limit, 1, cfg.Limit, cfg.Limit)`：模型在运营方给的天花板内选择，
不传则取天花板。

**影响**：`TestResolveRestaurantLimitIsClampedAndDefaulted` 断言默认值、合法值、
超限值（被压回天花板）与抬高的天花板四种情况。

**④ 计划 vs 实际：澄清候选的落库在 M5-03 就接上了（计划把它记在 M5-05 名下）。**

计划 M5-03 交付物 2 的测试要求"ambiguous → `awaiting_clarification` 落库 +
`state.awaiting_input` 事件 + 候选写入"，但候选持久化（`conversation_candidates`、
migration 0007、两个仓储实现、`ReplaceCandidates`/`ListCandidates`）在计划里属于 M5-05。
实施时把**写侧**（`persistence.go` 的 `persistCandidates`）在本任务落地，M5-05 只补**读侧**
（指代解析）。

**原因**：澄清问题问的就是这 2–3 家，"用户回『第二家』"必须以这次问的那张有序列表为参照。
把写入推迟到 M5-05，会让 M5-03 的澄清状态在库里缺少它自身语义所需的那一半事实；
而且同一函数（`persistTurn`）要在下一个任务里再改一次，属于把一次改动拆成两次。
快照采用"整表替换、且只在本轮真产出候选时替换"（沿用 `conversation.Candidate` 的既有约定），
顺序敏感的位置语义因此稳定。优先级为 `st.Candidates`（一次检索的结果）优先，
为空时回落 `st.ClarificationOptions`（澄清问的那几家）。

**影响**：
- 正向：`TestAnAmbiguousRestaurantNameParksTheThreadOnAQuestion` 一次断言四处耦合——
  用户读到的文案、客户端赖以路由的事件、另一个请求读到的 `CurrentState`、
  以及用户回答将被解析所依据的候选快照。
- 正向：`persistPendingCheckpoint` 也写候选，使"收到 `state.awaiting_input` 后立刻读线程"
  的客户端看到的是问题所问的那张列表，而不是上一轮留下的。
- 负向：M5-05 的交付物相应缩小（只需读侧 + 指代解析），需在 M5-05 记录里对齐。

**⑤ 计划 vs 实际：澄清次数上限的收尾语义（`settleTerminalState`）在计划里没有明确。**

计划只说"超限则取最相似的一家并在回答里说明假设"。实施时补了两条规则：
超限且有候选 → 取最高分者、把假设写进 `ReferenceNote` 与 `Warnings`；
超限但无候选（计划要的 slot 用户始终没给）→ 不假设、停止追问，让回答如实报告缺口。
以及终局状态：只有"正常结束且没有挂起动作"才回到 `idle` 并把澄清计数清零。

**原因**：计划里的 `PersistTurn` 原本无条件把状态重置成 `idle`。那会**抹掉用户尚未回答的
澄清**，并且每轮把计数器归零、使上限永远不可达——即上限形同不存在。计数还必须跨请求
存活（每轮澄清是独立请求，只有最新 checkpoint 留存），因此新增 migration 0009
（`conversation_checkpoints.clarification_count`）并把契约用例扩为"置 2 → 断言 2 →
新版本置 0 → 断言复位"。

**影响**：
- 正向：`TestTheClarificationCapAnswersOnAStatedAssumption` 断言"已达上限的那轮不再追问"、
  假设落在 `Warnings` 与 `SelectedRestaurantID`、且线程回到 idle 且计数归零。
- 正向：`TestAnAmbiguousRestaurantNameParksTheThreadOnAQuestion` 断言挂起轮
  `ClarificationCount == 1`（跨请求计数真的写进去了）。
- 中性：`make test-postgres` 通过（`shared/store/postgres` 全量契约，
  含 `ConversationRepository/checkpoint_optimistic_version`），migration 0009 由测试内的
  `Migrate` 实际应用。

**⑥ 计划 vs 实际：`agent.Event` / `httpapi.StreamEvent` / `sse.payload()` 三处必须同时改，
并补了一条"事件类型全覆盖"的断言。**

计划已警告"`payload()` 对未知事件类型会直接报错，所以必须先扩类型再发事件"。实施时除了
按警告推进，还新增 `internal/httpapi/sse_event_test.go` 的
`TestEveryStreamEventTypeHasAPayload`：显式枚举全部 8 个 `StreamEventType`，
逐个断言 `payload()` 不报错。

**原因**：这条约束原本只存在于注释里，靠人记得。把事件类型集合写成显式列表，
就能让"新增一个事件但忘了加 payload 分支"变成单元测试失败而不是线上某轮静默断流。
另有 `TestUnknownStreamEventTypeIsRefused` 钉住未知类型必须报错而不是发无名帧，
`app.TestToStreamEventCarriesThePendingState` 钉住 agent→transport 这一跳不丢字段。

**⑦ 计划已覆盖，无需改动："指定餐厅 citations 必须全部属于该 restaurant_id"保持为切片级断言。**

交付物 4 明确写"（M3 证据层已保证，本任务补一条切片级断言）"，因此**没有**在 composer 里
再实现一遍餐厅范围过滤。`TestANamedRestaurantAnswerCitesOnlyThatRestaurantsEvidence`
按要求在切片层断言：本轮 citations 全部命中本轮证据集，且逐条断言其 `RestaurantID`
等于被点名的 42；同时断言"名字已唯一确定时不得挂起任何澄清"，并在 checkpoint 上断言
`SelectedRestaurantID == 42`——范围是持久化的事实，不只是这一次调用的参数。

**边界说明（留待 M5-05/M5-06 复核）**：该用例的输入证据全部来自 42，因此它能捕获
"证据层把别家文档混进来"（一旦发生，`byID` 里就没有该 id 或映射到别的餐厅，断言失败），
但不覆盖"composer 拿到混合证据时是否应当拒答"——按计划口径那是 M3 检索层的职责，
由 `make eval-retrieval` 的 `cross_restaurant_leak` 门（当前 0）守。

**⑧ 环境相关（非本次改动引入）：`make test` 在 chat-service 仍有一条失败。**

`chat-service/internal/retrieval/eval_test.go` 的 `TestRetrievalFixtures` 报
`retrieval_recall_at_k = 0.467, want >= 0.6`，原因是本机没有运行 Ollama
（`curl http://localhost:11434/api/tags` 返回空），向量通道整体降级，与 M5-02 时记录的现象一致。
本次改动不涉及检索通道，且该用例的其余门（`structured_filter_accuracy = 1.000`、
`citation_precision = 1.000`、`cross_restaurant_leak = 0`）均为绿。
`make eval-retrieval` 同样需要 embedding 服务，继续推迟。

---

### M5-05 多轮追问切片：实施记录

**① 计划 vs 实际：交付物按 M5-03 记录 ⑤ 的口径对齐——本任务只做"读侧 + 指代解析 + 状态机"。**

M5-03 已把候选的**写侧**（`persistCandidates`、`ReplaceCandidates`、0007 迁移、双仓储）落地。
M5-05 实际落地的是：`loadConversationContext` 读候选（`ListCandidates`）→ `slots.ThreadContext`
→ 指代解析 → 解析结果写回状态与 checkpoint。

**原因**：M5-03 的澄清状态在库里缺少"它问的是哪几家"这一半事实就无法自洽（附录 E 的 M5-03 记录 ⑤）。

**影响**：
- 正向：`TestAnAmbiguousRestaurantNameParksTheThreadOnAQuestion`（M5-03）与
  `TestAnOrdinalAnswersAClarificationAndContinuesThePendingAction`（M5-05）构成闭环——
  前者写入的快照正是后者读取并解析的那张。
- 中性：M5-05 工作量从 L 缩为"读侧 + 指代 + 状态机"。

**② 计划 vs 实际：`slots.Extract` 增加 `ThreadContext` 参数。**

计划只说"`ingress` 读到的 checkpoint 传给 `slots.Extract`"。实施时新增
`ThreadContext{Pending *conversation.Checkpoint; Candidates []conversation.Candidate}`，
并把 `Extract(ctx, userInput)` 改为 `Extract(ctx, userInput, thread ThreadContext)`。

**原因**：checkpoint 与候选快照是**同一个事实的两半**——checkpoint 说线程在等一家餐厅，
快照说它刚才被提供了哪几家。只读其一，追问要么无法解析序数，要么无法知道在补哪个槽。
用结构体而不是并排两个参数，是为了让"两半必须一起传"成为类型上的事实。

**影响**：调用点同步为 `nodes.interpret`（传 `st.LoadedCheckpoint` / `st.LoadedCandidates`）
与 `app/interpreter.go`（无线程上下文，传零值）。`slots_test.go` 的 7 处调用点全部更新。

**③ 计划 vs 实际：指代解析全程不调用模型，且"识别得出但满足不了"记 warning 而非选中。**

计划已写"指代解析**不调用模型**"。实施时补了一条计划没写的边界：序数超出候选范围
（"第七家"而只有 5 家）或描述在候选里匹配不到时，**不取最近的候选**，而是把原因写进
`Plan.Warnings`、`SelectedRestaurantID` 保持 0。

**原因**：用户问的是一家店，不是一个序号。取最近的一家会产出"看似正确"的回答，
而它与正确答案在用户眼里无法区分——这比说"本轮只有 5 家"糟得多。

**影响**：`TestUnsatisfiableReferenceWarnsInsteadOfSelecting`、
`TestReferenceReportsAnOrdinalBeyondTheSnapshot` 钉住该行为。

**④ 计划 vs 实际：修饰指代拆成"强/弱"两种形态，弱形态只在恰好命中一家时才成立。**

计划只写"修饰指代：`那家安静的 / 布鲁克林那家` → 在候选集内按 `reasons`/地址匹配"。
实施时发现两种写法的**可信度不同**：带`的`的领属式（"那家安静的"）是显式描述，即使匹配不到也照实报告；
不带`的`的前缀式（"布鲁克林那家"）常常只是句子成分——"我第一次去这家店"里的"第一次去"会被
误读成描述。因此弱形态**只在恰好命中一家候选时**成立，否则回落为它包含的指示词。

另有两处纯实现细节：`modifierAfterPattern` 的描述字符类原本排除了空白，导致
"那家 Shanghai 的"因为 `的` 前有空格而整体匹配失败（退化成裸指示词）；改为允许拉丁字母开头的
描述跨内部空格（`modifierBody` 的第二分支），同时保持"那家不错 我喜欢的"这类带空格中文小句
不被整段吞掉。以及 `modifierTokens` 会经 `canonicalBorough`/`canonicalNeighborhood`/`canonicalCuisines`
展开，这是"用户写 `布鲁克林`、快照存 `brooklyn`"能对上的唯一原因。

**影响**：`reference_test.go` 共 23 个用例，其中
`TestAVerbPhraseBeforeTheMarkerIsNotADescription`、
`TestReferenceMatchesAMultiWordLatinName`、`TestReferenceDoesNotSwallowASpacedChineseClause`
分别钉住上三条。

**⑤ 计划 vs 实际：计划里没有"pin 的取值路径"——解析出的 `Plan.SelectedRestaurantID`
此前从未进入 `TurnState`，因此也从未落库。**

这是实施中发现的**真实缺陷**（不是计划遗漏的便利项）：`saveCheckpoint` 写的是
`st.SelectedRestaurantID`，而指代解析只写 `st.Plan.SelectedRestaurantID`；
唯一同时写两者的地方是 `absorbResolve`（模型主动点名）。所以"第二家安静吗"解析成功、
`referencePrompt` 也会照常注入，但 checkpoint 里 `selected_restaurant_id` 仍是 0——
下一轮请求读不到它，"状态跨请求存活"这条验收标准直接不成立。

实施时补了三条规则：
1. `interpret`：`plan.SelectedRestaurantID != 0` 时同步到 `st.SelectedRestaurantID`
   （初始值，不是守卫——模型后来点名是更强的信号，`absorbResolve` 仍可覆盖）；
2. `loadConversationContext`：线程的 pin 从 checkpoint 继承，作为本轮的起点
   （否则"营业时间呢"这类不含指代的追问会把 pin 写成 0，再下一轮无从恢复）；
3. `dropStalePin`（`absorbToolData` 的 `search_restaurants` 分支）：新列表里不含 pinned 的
   restaurant_id 时释放 pin。

**原因**：pin 是"用户正在看的那张列表"的陈述。"第二家"钉住一行、"这家"再指那一行，
都只在同一张列表内成立；换了列表以后它不再是任何用户可见事物的性质。
不继承会让"它有包间吗"回落到 `position=1`（答错店）；不释放会让换了话题的那一轮
被明确指示"本轮已锁定 restaurant_id=旧店"。

同时 `referencePrompt` 按**谁做的决定**分两档输出：本轮解析出来的是**指令**
（"本轮已锁定 restaurant_id=N"），从上轮继承的只是**默认值**
（"本线程此前的追问对象是 N；若这一轮用户换了条件请重新搜索"）。
分辨"这句是不是追问"恰恰是模型能做而规则不能做的判断，所以规则只陈述事实、把判断留下去。

**影响**：
- 正向：`TestAFollowUpReadsTheSecondRecommendationAndStaysOnIt` 断言 pin 落库、证据请求只含该 id、
  且 citations 逐条反解回该 restaurant_id。
- 正向：`TestAPinSurvivesAFollowUpAndIsReleasedByANewSearch` 三段断言：
  检索轮不得声称有 pin、指代轮必须是指令式、换列表的轮只能是默认值式且 pin 被释放。
- 中性：`TestAPendingStateSurvivesAProcessRestart` 用**新建 Runner**（不同 provider 对象、
  只共享仓储）复现重启，断言继续动作仍命中第二家，且历史消息完整为 4 条。

**⑥ 计划 vs 实际：并发保护不只是"返回 conflict"，还改了写入顺序。**

计划写"并发保护沿用 checkpoint 的乐观锁思路……冲突返回 `conflict`（MVP 不做线程队列）"。
实施时发现当时的代码**把冲突吞掉了**：`persistTurn` 在 `saveCheckpoint` 返回冲突时
只挂一条 warning 就继续写消息，结果是"两轮都成功、两份 transcript、一份 checkpoint"——
线程的历史与状态描述的不是同一个对话，而之后每一个"第二家"都会被解析到一半历史从未见过的列表上。

改动：
1. `persistTurn` 返回 error；冲突时**跳过消息写入**并向上返回 `conversationConflict`（`errs.CodeConflict`，HTTP 409）；
   非冲突的存储故障维持原语义（降级为 warning，不改本轮结论）。
2. `persistPendingCheckpoint` 同样只把**冲突**上抛（存储故障仍不阻断本轮），
   `clarifyNode` 因此改为"先把 checkpoint 存好，再发 delta 与 `state.awaiting_input`"——
   丢锁的那轮不会先stream 一个关于别人线程的问题。
3. `finalize` 把 `Auditor.RunFinish(succeeded)` 移到 `persistTurn` **之后**：
   丢锁的轮次从未真正落库，先记成功再发现冲突会留下一条宣称存在该对话的审计行。

**原因**：冲突不是存储故障，是"这一轮输了"。失败可重试，静默合并不可撤销。

**影响**：
- 正向：`TestConcurrentTurnsOnOneThreadConflict` 用 `sync.WaitGroup` 做屏障
  （两个 turn 都过 ingress、都在模型轮等待），把"同时读到同一版本"从**大概率**变成**确定性**；
  断言恰好一成功一 `conflict`、transcript 只有 2 条、checkpoint version 停在 1。
- 正向：`app.TestToStreamEventCarriesTheErrorCode` 钉住 `conflict` 这个码能穿过
  agent→transport 那一跳——它是客户端唯一能据以决定"重试"的信息。
- 负向（记为已知边界）：SSE 一旦发出首字节就无法再改 HTTP 状态码，因此冲突在流式路径上
  以 `error{code:"conflict"}` 事件呈现，而不是 409 响应体；非流式路径仍由 `errs` 映射为 409。
  这与 M4 既有的中途失败契约一致，不新增端点。
- 中性：`persistCandidates` 仍为 warning-only。checkpoint 的版本就是那把锁，
  且丢锁的轮次已在 `persistCandidates` 之前返回，所以候选写入不会先于锁成功——
  这条因果链本身即是不冲突的保证，故未给 `ReplaceCandidates` 增加版本参数。

**⑦ 计划 vs 实际：`get_restaurant_evidence` 的 `required` 从 `["restaurant_ids","query"]`
缩减为 `["query"]`，由 handler 从 plan 补 id。**

计划的工具表只写"M5-03 增加证据充分性约束"。追问轮的问题是：模型被告知"本轮已锁定
restaurant_id=N"，但它调工具时仍必须自己把 id 抄一遍，抄漏即 `required` 校验失败、
工具根本不会被调用。改为 schema 只要求 `query`，handler 在模型没给 id 时读
`slots.PlanFromContext(ctx).SelectedRestaurantID`。

**原因**：与 `search_restaurants` 完全相同的"填充遗漏、绝不覆盖"规则——
模型显式给的 id 原样使用，plan 只补它没给的部分。这样同一条规则在两个工具上一致，
不再有"一个工具能补、另一个不能"的不对称。

**影响**：`TestAFollowUpReadsTheSecondRecommendationAndStaysOnIt` 的第二轮刻意只传
`{"query":"这家安静吗"}`，被解析到第二家的证据请求即证明该回落生效。

**⑧ 计划 vs 实际：测试清单里两条由 M5-03 覆盖，未在 M5-05 重复。**

- "澄清超限：连续 3 轮无法补齐 → 使用默认假设继续并记 warning" ——
  由 M5-03 的 `TestTheClarificationCapAnswersOnAStatedAssumption` 覆盖。
- "状态重置回归"的"挂起轮保持 `awaiting_*`"一半 ——
  由 M5-03 的 `TestAnAmbiguousRestaurantNameParksTheThreadOnAQuestion` 覆盖；
  M5-05 补的是另一半与其**过渡**：新增
  `TestASecondClarificationCountsOnTheFirst`，断言第二次挂起后 `ClarificationCount == 2`
  而不是重新从 1 开始。

**原因**：计数若在挂起轮结束时被清零，上限永远不可达——这是同一个"无条件重置"缺陷的
另一种表现，而它对应的正是"回应了才复位"的语义，放在 M5-05（本任务改的就是
`settleTerminalState` 的调用时机）更贴切。

**影响**：M5-05 的测试清单全部有对应断言，无遗留项。

**⑨ 环境相关（同上，非本次改动引入）**：`make test` 在 chat-service 仍只有 ⑧ 号
（M5-03 记录）那条 Ollama 相关失败；`shared` 与 `data-pipeline` 全绿。

---

### M5-06 可选 Mock 预约切片：实施记录

**① 计划 vs 实际：确认策略在 `toolreg.Entry` 上是**强制**的，且双向校验。**

计划只写"非只读工具必须声明确认策略"。实施时把声明做成独立的
`Confirmed` 字符串（`""` / `none` / `required` / `implicit`）而不是 `bool`，并在
`Registry.Register` 里双向校验：
- 非只读工具声明空或 `none` → 拒绝注册；
- **只读**工具声明 `required` / `implicit` → 也拒绝注册。

**原因**：`bool` 的零值让"这个工具要确认"与"作者没想到它"无法区分——两者都是 `false`，
而后者的后果是一个没有任何人批准过的写入。字符串让"省略"变成空值，`Register` 可以拒绝。
反向的那条同样必要：给查询类工具挂确认闸门，会把一次查找变成用户必须回答的问题。

**影响**：`toolreg/registry_test.go` 的 `TestRegisterRefusesAWriteWithNoConfirmationPolicy`
用 8 个用例（4 种声明 × 只读/非只读）钉住整张真值表。

**② 计划 vs 实际：幂等键的原料从"checkpoint 版本"改为"服务端铸造的请求 ID"——设计纠正。**

计划 §M5-06 写 `idempotency_key` 由"线程 + 动作 + 参数摘要"派生（隐含可含版本）。
实施到 `hitl` 时发现：**记录确认结果这个动作本身会推进 checkpoint 版本**，因此
"重试"恰好无法重算出同一个键——而重试正是幂等唯一存在的场景。改为：
`parkForConfirmation` 在挂起时用 `idgen.NewUUID()` 铸一个 `PendingToolCallID` 存进
checkpoint，键的原料是 `thread_id + action + request_id + sha256(arguments)`。

**原因**：键必须由"用户批准的那一次请求"唯一决定，而这必须是**在这条键被用于写入之后
仍然存在**的值。版本号不满足这一点；服务端铸造的请求 ID 满足。

**影响**：
- 正向：`reservation.IdempotencyKey` 的文档同步改写（`shared/domain/reservation/reservation.go`），
  `TestARepeatedConfirmationIsTheSameBookingAndSpendsNoSecondTable` 断言第二次
  `Confirm` 返回同一个 `ReservationID` 且 `slot.booked` 仍是 2。
- 正向：`TestTheIdempotencyKeyDependsOnTheApprovedRequest` 逐项验证键对
  `request_id` / `arguments` / `thread_id` 都敏感。
- 中性：`hitl.Approval` 因此携带 `RequestID` 而非 `CheckpointVersion`。

**③ 计划 vs 实际：挂起后 `plan` 节点必须**直接返回**，否则发出悬空 `tool_calls`——真实缺陷。**

计划只写"挂起后本轮结束于提问"。实施时发现图是 `tools → plan` **无条件**回边，
而 `runTools` 的 `break` 只跳出**本轮工具循环**、不改变图的走向：挂起后 `plan` 会再调一次
模型。这不只是多一次调用——`store` 里那条 assistant 消息带着 `tool_calls` 却**没有**配对的
tool 结果（挂起工具不产生 tool 消息），这在 OpenAI 兼容接口上是**非法 transcript**，
真实部署会直接报错。

修法：`plan` 顶部增加 `if st.ConfirmationRequired { st.PendingToolCalls = false; return st, nil }`，
与既有的 `needsClarification` 早退同一个位置、同一个理由。

**原因**：挂起轮剩下的唯一工作就是提问，而"问什么"已经由工具渲染好了；
再让模型看一眼，either 多花一次调用，either 必须把一条非法 transcript 送上线。

**影响**：`TestAWriteToolIsParkedWithoutWriting` 正是靠这条才能收敛——
修复前该用例以 `scripted provider: no more ChatWithTools responses queued` 失败，
暴露出多出来的第三次模型调用。

**④ 计划 vs 实际：挂起不写 `tool.start` / `tool.finish`，也不写 `tool_calls` 审计行。**

计划只写"确认闸门：非只读工具被调用后没有任何写入"。实施时明确了审计口径：
被挂起的调用**没有运行**，因此不产生 `tool.start`/`tool.finish` 事件对，
也不产生 `tool_calls` 审计行。这次请求记录在**两处**：线程的 checkpoint
（`pending_action` / `pending_tool_call_id` / `pending_arguments`）与
`confirmation.required` 事件。

**原因**：审计表的主题是"运行过的工具"。为一次没人运行的调用记一行，会让
"这次预约是通过什么路径产生的"这个问题得到一个错误答案。而 checkpoint 与事件
已经完整记录了"用户被问过什么"。

**影响**：`TestAWriteToolIsParkedWithoutWriting` 遍历事件断言不存在
`request_reservation` 的 start/finish。

**⑤ 计划 vs 实际：确认完成后 pending 块**保留**（状态转 `completed`），以便重放自答。**
**取消在完成后返回 `agent_no_pending_action`，而不是静默成功。**

计划只写"重复 confirm 仍是同一条（200 同体）"。实施时补了三条边界：
1. `execute` 成功后**不清空** pending 块，只把 `State` 改为 `completed`；
   重放据此识别"这条确认已经决定过"，重新调用工具拿回同一笔预约。
   重放**重新调用**而不是缓存结果——幂等是策略的要求而非指望，重放拿到的是工具
   针对这次请求的真实输出，而不是可能与它漂移的记忆副本。
2. `cancel` 在**已确认**的线程上返回 `agent_no_pending_action`（400）。一个想撤销
   预约的人不该从一个无法表达撤销的端点收到"成功"。
3. 工具执行失败（如容量不足）时**保留** pending 动作与 `awaiting_confirmation` 状态：
   写入没发生，清掉 pending 会丢掉用户正做到一半的请求。

**原因**：`pending_action != ""` 与 `state == completed` 的组合是"已决定的请求"这一
事实的唯一载体，它在重试窗口内必须存在；而窗口过后自然会被下一轮的写入覆盖。

**影响**：`hitl/confirm_test.go` 的 `TestARepeatedConfirmationReplaysTheSameBooking`、
`TestCancellingAfterACompletedConfirmationIsRefused`、
`TestAFailedToolKeepsThePendingActionAndReportsTheReason` 分别钉住三条。

**⑥ 计划 vs 实际：预约写入是**两阶段**（先 hold 后 promote），不是直接写 confirmed。**

计划 §M5-06 只要求"确认后恰好一条预约、状态 confirmed"。实施时把写入拆成
"先写 held（带幂等键）→ 再原地提升为 confirmed"。

**原因**：被中断的写入必须可被重试识别。一个只写了 hold 就崩掉的操作，
仍然占着座位、仍然持有那条幂等键，因此重试能按键找到它并完成提升；
崩溃后无人重试的则由 TTL 扫描回收。直接写 confirmed 行的话，
"响应丢了"就等于"预约丢了"——没有任何行可供重试认出。

**影响**：`TestAConfirmationWritesAConfirmedBookingUnderAHoldFirst` 断言按
`IdempotencyKey` 能反查回同一笔；`TestAnApprovedCallWritesOneBooking` 断言
`SaveReservation` 恰好被调用 2 次（hold + 提升）、`slot.booked == 2`。

**⑦ 计划 vs 实际：新增 SSE 事件类型需要在**三处同时**登记，否则帧在序列化时失败。**

计划只写"新增 `confirmation.required` 事件"。实施时确认了既有的封闭集合约定：
`agent.EventConfirmationRequired`（`internal/agent/state.go`）、
`httpapi.StreamConfirmationRequired`（`internal/httpapi/sse.go`）与 `sse.payload()`
的 `case` 分支必须同时添加——`payload()` 对未知类型返回错误，因此漏掉任意一处
都会让**整条流**在该帧处失败，而不是静默丢一个字段。事件类型总数因此从 8 增至 9。

**原因**：这是 M4 就定下的契约（用编译期封闭集合换取"客户端不会收到它不认识的事件"）。
三处登记的代价换来的是新增事件无法被漏接。

**影响**：`sse_event_test.go` 的 `everyStreamEventType()` 同步扩展为 9 个，
该表驱动用例断言每种类型都能被 `payload()` 序列化且 `event:` 名与常量一致；
`app/chatservice_test.go` 新增 `TestToStreamEventCarriesTheConfirmationSummary`
钉住摘要能穿过 agent→transport 那一跳。

**⑧ 计划 vs 实际：关闭开关的语义是"从工具表中移除"，以及无线程仓储时的降级。**

计划只写"关闭开关时 `get_availability` / `request_reservation` 不在 `Registry.Specs()` 中"。
实施时补了两条：
1. `RESERVATION_ENABLED=true` 而**没有**预约仓储 → 启动失败（而不是运行期第一次请求才炸）；
2. `RESERVATION_ENABLED=true` 而**没有**会话仓储 → 降级为"只读可用性"：
   `get_availability` 照常注册，但不构建 `hitl.Service`（没有线程可供挂起），
   `POST /v1/conversations/:id/confirm` 返回 `invalid_argument`（"未启用"），
   而**不是** `agent_no_pending_action`——"这个部署没有该能力"与"这个线程没有待办"
   是两个不同的答案，客户端据以采取的动作也不同。

**原因**：把能力挪出模型视野（而非注册后必然失败）是"模型不该看到做不到的事"的
同一条原则；而把"未启用"与"无待办"混为一谈，会让客户端在不支持的部署上
把用户引向一个永远不会成功的确认流程。

**影响**：`app/reservation_assembly_test.go` 覆盖四种组合
（关 / 开+全依赖 / 开+无预约仓储 / 开+无会话仓储）。

**⑨ 测试清单对照（计划 §M5-06 §5 的逐条落点）。**

| 计划要求 | 落点 |
|---|---|
| 确认闸门：无任何写入 + `awaiting_confirmation` + `confirmation.required` | `agent.TestAWriteToolIsParkedWithoutWriting`（三重断言：`bookingWrites()` 为空、事件含 `confirmation.required`、checkpoint 状态与挂起块） |
| confirm 后恰好一条、`confirmed`、唯一键存在、重复 confirm 同一条 | `reservation.TestAConfirmationWritesAConfirmedBookingUnderAHoldFirst` + `TestARepeatedConfirmationIsTheSameBookingAndSpendsNoSecondTable`；`agent.TestAnApprovedCallWritesOneBooking` |
| cancel 后无预约、pending 清空、回 `idle` | `hitl.TestCancellingRunsNothingAndClearsThePendingAction` |
| 无挂起动作 confirm → `agent_no_pending_action`(400) | `hitl.TestDecidingAThreadWithNoPendingActionIsRefused` + `httpapi.TestConfirmEndpointMapsTheServicesErrorCode` |
| 容量不足 → `reservation_unavailable`(409) 且无写入 | `reservation.TestAConfirmationIsRefusedWhenTheSlotIsGone` + `httpapi.TestConfirmEndpointMapsTheServicesErrorCode` |
| hold 过期 → 提示重新查询 | `reservation.TestAvailabilityReturnsTheSeatsOfAnExpiredHold`（双向：过期归还、未过期仍占用） |
| 关闭开关时两个工具不在 `Registry.Specs()` | `app.TestReservationToolsAreAbsentWhenTheCapabilityIsOff` / `TestReservationToolsArePresentAndGatedWhenTheCapabilityIsOn` |

计划未列但本次补的用例（均由上文 ②③⑤⑦⑧ 的偏差带出）：
`hitl.TestConfirmingRunsTheApprovedToolOnceAndCompletesTheThread`、
`TestDecidingWithAnUnknownAnswerIsRefused`、`TestAPendingActionInTheWrongStateIsNotDecided`、
`TestAConfirmationThatLosesTheThreadReportsAConflict`；
`agent.TestTheWriteToolRefusesToRunWithoutAnApproval`、
`TestAWriteWithNoDescribableSummaryIsNotParked`、`TestTheAvailabilityLookupIsNotGated`；
`httpapi.TestConfirmEndpointPassesTheDecisionThrough`、
`TestConfirmEndpointMatchesTheDecisionRatherThanParsingIt`、
`TestConfirmEndpointRequiresADecision`、`TestConfirmEndpointCarriesACancel`、
`TestConfirmEndpointAnswersJsonNotAStream`；
`config.TestReservationIsDisabledByDefault`、`TestLoadParsesReservationOverrides`、
`TestValidateChecksTheReservationTTLOnlyWhenEnabled`。

**⑩ 计划 vs 实际：`hitl` 与 `reservation` 是**两个**包，不是一个。**

计划把"确认闸门"与"预约规则"写在同一个小节里。实施时拆成：
`internal/hitl`（谁有权写入：pending 动作、批准、决定、重放）与
`internal/reservation`（批准之后写入意味着什么：容量、hold、TTL、幂等）。
`hitl` 通过 `CheckpointStore` + `ToolRunner` 两个窄接口与外界交互，
**不 import** 预约相关的任何东西。

**原因**：这直接对应包的文档里那句话——"预约是第一个需要确认的写操作，
而不是这个机制存在的原因"。闸门若与预约同处一包，第二个需要确认的写操作
要么复制一遍闸门逻辑，要么把闸门改成预约的形状。

**影响**：`hitl.NewService` 只接受 `CheckpointStore` 与 `ToolRunner`；
`ToolRunner` 恰好是 `*toolreg.Registry`（`Invoke` 方法签名一致），
因此确认路径复用了推理循环**同一个**工具入口，闸门本身无需认识任何工具名。

**⑪ 环境相关（非本次改动引入）**：`make test` 在 chat-service 仍只有 M5-03 记录 ⑧
那条 Ollama 相关失败（`retrieval_recall_at_k = 0.467 < 0.6`，Ollama 未运行），
其余 15 个包全绿；`shared` 与 `data-pipeline` 全绿。

---

### M5-07 记忆写入与用户可见管理：实施记录

**① 计划 vs 实际：`save_memory` 在 `toolreg` 里声明 `ConfirmationImplicit`——这是三值策略中
`implicit` 的唯一实际用途，需要记录它为什么不等于绕过闸门。**

M5-06 引入的 `toolreg.Confirmation` 有三个值：`none` / `required` / `implicit`。
`save_memory` 是**写**操作（`ReadOnly: false`），因此不能声明 `none`；但它也不能声明
`required`——用户说"记住我不吃辣"就是在下指令，再回问"要我记住吗？"是一个用户刚刚
回答过的问题。于是它声明 `implicit`。

`implicit` 不是"不需要授权"，而是"授权以另一种形式存在"：M5-07 用**输入校验**替代批准——
模型必须引用用户原话（`quoted_user_text`），而这句话必须是**当轮用户消息的子串**。
一个自作主张决定"这条偏好值得记住"的模型没有东西可引用。

**原因**：这与 PRD §3.5 的红线一一对应。红线是"不把单次搜索条件自动升级为长期偏好"，
而对"用户是否明确要求"做判断的，不能是模型自己的一句声明（prompt 无法强制）。
把它变成一次子串比较之后，"明确"就成了可离线单测的输入事实。

**影响**：
- 正向：`memorywrite.TestAFabricatedRequestIsRefusedAndNothingIsWritten` 与
  `agent.TestAFabricatedMemoryIsRefusedAtTheToolBoundary` 分别在策略层与切片层钉住它；
  后者还断言工具确实**运行并失败**，因此模型会被明确告知（而不是静默无操作）。
- 中性：`hitl`（`required`）与 `memorywrite`（`implicit`）成为两个并列的写入闸门，
  分别对应"模型请求、用户批准"与"用户指令、输入校验"两种授权来源。
  两者都在 `chat-service/internal` 内，都不被对方 import。

**② 计划 vs 实际：真实缺陷——`MemoryRepository.Upsert` 按值接收并自行分配 ID，
调用方拿不到刚写入行的 ID。**

`memorywrite` 初版实现是"ID 留空 → `Upsert` 分配 → 按返回行 reload"。但仓储接口是
`Upsert(ctx, mem) error`：它接收 `mem` 的**副本**，给副本赋 ID。于是调用方手里的
`memory.ID` 仍是空串，紧接着的 `reload` 报 `not_found: memory "" not found`。
计划与既有 M4 代码都没暴露这一点，因为此前没有任何调用方需要知道写入结果的 ID。

修法：ID 由服务端铸造（`Service.newID`，默认 `idgen.NewUUID`），仓储的"ID 为空则分配"
退化为给直接调用方的兜底。`reload` 仍然保留——`Upsert` 会把 `created_at` / `updated_at`
盖在**它自己的副本**上，同样不回传，因此时间戳只能回读。

**原因**：调用方必须能说出它刚写了什么。`save_memory` 的返回值与 `memory.saved` 事件
都要带 `memory_id`（用户要能据此编辑或删除），而"回答一个我无法命名的行"是自相矛盾的。

**影响**：`memorywrite/policy_test.go` 的 8 个用例在修复前全部以
`not_found: memory "" not found` 失败——这正是该缺陷的签名；
修复后 `TestAUserRequestedMemoryIsStampedAndWeightedByItsType` 等断言
`result.Memory.ID != ""`。

**③ 计划 vs 实际：用户原话与用户身份**一起**经 context 传递，且是同一个值。**

计划只写"`quoted_user_text` 必须是当轮用户消息的子串"。实施时把"谁在问"和"用什么话问"
合成一个 `memorywrite.Turn{UserID, Message}`，经 `memorywrite.WithTurn` 挂在工具调用的
context 上（与 `slots.WithPlan` 同样的机制与理由）。

**原因**：这两半是同一个事实——"这个用户在**他的**消息里**这么**说了"。一个能只收到一半的
守卫必须回答"缺的那一半算通过还是算失败"，而两种答案都是错的：缺身份会导致匿名轮写入
一条没人能列出或删除的记忆；缺消息会让子串校验退化成"任何引用都通过"。

**影响**：`TestSavingOutsideATurnIsRefused`（无轮次 → `memory_write_not_requested`）与
`TestSavingWithoutAUserIsRefused` 分别钉住两半的缺失；
匿名轮的本轮工具调用在 `agent.TestMemoriesAreNotInjectedWithoutAUser` 里被一并覆盖。

**④ 计划 vs 实际：`memory.saved` 由 `absorbToolData` 发出，因此该函数新增 `ctx` 参数。**

计划只写"SSE 新增 `memory.saved`（`memory_id / memory_type / content`）"。
实施时把它接在既有的"工具返回 `Data`、图节点吸收并 emit"的模式上：
`tools.SaveMemoryEntry` 返回 `SavedMemory` 的 JSON，`agent.absorbToolData` 解出它并
`emitFromContext`。为此 `absorbToolData` 从 `(st, name, result)` 改为
`(ctx, st, name, result)`。

**原因**：工具不能发事件——`emitFromContext` 是 `agent` 包的未导出函数，且工具本就
不该知道传输层的事件词汇。这个方向也保证了审计链路只有一条：事件由拥有 `RunID` /
`ThreadID` 的图节点发出，工具仍然只是一个"返回数据的函数"。

**影响**：`SaveMemoryToolName` 成为 `absorbToolData` 的第四个 `case`；
`httpapi/sse_event_test.go` 的封闭集合从 9 个事件增至 10 个，
`TestEveryStreamEventTypeHasAPayload` 覆盖新增类型的可序列化性。

**⑤ 计划 vs 实际：`PATCH` 的字段用指针，空 body 是 422 而不是 200。**

计划只写"新增 `PATCH /v1/memories/:memory_id`：修改 `content` / `memory_type`"。
实施时把请求体两个字段都做成 `*string`，并在 `Validate` 里拒绝"两个都没给"。

**原因**：只有指针能区分"没提到"与"清空"。若用值类型，一个只发 `memory_type` 的请求
会以空 `content` 到达服务，把记忆内容抹掉——这是 PATCH 语义最常见的一类事故。
而"两个都没给"返回 200 会让一个本意要改东西的客户端无从得知什么都没改，
所以它是请求缺陷（422，与 `validation_failed` 一致）而不是幂等成功。

**影响**：`httpapi.TestUpdateMemoryEndpointScopesTheEditToTheCaller` 断言
"body 没发的字段以 nil 到达服务"；`TestUpdateMemoryEndpointRejectsAnEmptyEdit` 覆盖空 body。

**⑥ 计划 vs 实际：记忆的编辑规则落在写策略包里，`chatService` 因此同时持有仓储与写服务。**

计划把"归属校验（不是本人的 id → not_found）"写在端点小节里。实施时把校验放进
`memorywrite.Service.Update`，与写入路径共用同一套规则（ID 保持、置信度随类型、
内容长度按字符计）。

`chatService` 于是有两个记忆相关字段：`memories`（仓储，供 `ListMemories` /
`DeleteMemory` 直读）与 `memoryWrite`（写策略，供 `UpdateMemory`）。

**原因**：编辑不是"改一个字符串"——它必须保持 `created_at`、让置信度跟随类型、
把归属做成 `not_found`。这些规则若在 HTTP 层重写一遍，就会与写入路径漂移；
而"列出"和"删除"确实只是存储操作，为它们绕一层策略是多余的。
两个字段的分工有明确边界（读/删走仓储，改走策略），不是同一件事的两个入口。

**影响**：`app.TestAnEditIsVisibleOnTheNextList` 走真实仓储断言"改完立刻在
`GET /v1/memories` 反映，且 ID 不变（不是软删+新增）"；
`TestEditingAnotherUsersMemoryIsNotFound` 与 `httpapi.TestUpdateMemoryEndpointHidesAnotherUsersMemory`
分别在服务层与 HTTP 层钉住 `not_found`（而非 403——403 会确认该 id 存在）。

**⑦ 计划 vs 实际：开关同时取决于"开关打开"与"仓储存在"，两个条件各有一个后果。**

计划只写"`AGENT_MEMORY_WRITE_ENABLED`（默认 `true`，仅当 `Memories` 端口存在时注册工具）"。
实施时把两种"不注册"的原因分开记录并分别覆盖：
- 开关关闭 → 工具不在模型视野内（这是开关的目的）；
- 开关打开但无 `Memories` 端口 → 同样不注册，并**记录一条 warning**，
  因为这是配置矛盾（打开了能力却没有可写的地方），而启动不失败——
  一个只做检索的部署是受支持的运行方式。

**原因**：把能力挪出模型视野（而非注册后必然失败）是"模型不该看到做不到的事"的同一条
原则，M5-06 的预约开关已确立该口径，记忆侧沿用。

**影响**：`app.TestSaveMemoryIsRegisteredOnlyWhenTheSwitchIsOn`（两个方向）、
`TestSaveMemoryIsNotOfferedWithoutAMemoryStore`、
`config.TestMemoryWritesAreEnabledByDefault` / `TestLoadParsesTheMemoryWriteSwitch` /
`TestTurningMemoryWritesOffIsValidConfiguration`。

**⑧ 计划 vs 实际：`AGENT_MEMORY_WRITE_ENABLED` 挂在 `AgentConfig` 上，不新开一个配置段。**

计划未指定归属。实施时放入 `config.AgentConfig.MemoryWriteEnabled`。

**原因**：环境变量名带 `AGENT_` 前缀，而配置段与变量前缀对齐是既有约定
（`AgentConfig` ↔ `AGENT_*`、`AdminConfig` ↔ `ADMIN_*`）；且"这个开关不需要新的校验规则"
（布尔量没有取值域），所以它也不该换来一个只有一项的配置结构体。

**影响**：`Config.Summary()` 新增 `agent_memory_write_enabled`，由
`config.TestLoadParsesTheMemoryWriteSwitch` 断言其被脱敏汇总输出。

**⑨ 测试清单对照（计划 §M5-07 §测试 的逐条落点）。**

| 计划要求 | 落点 |
|---|---|
| 护栏：编造的条件 → 拒绝且无写入 | `memorywrite.TestAFabricatedRequestIsRefusedAndNothingIsWritten` + `agent.TestAFabricatedMemoryIsRefusedAtTheToolBoundary`（含"工具确实运行并失败"） |
| 护栏：大小写/空白差异 → 通过 | `memorywrite.TestTheGuardNormalizesWhitespaceAndCaseButNotWording`（6 子例：4 通过 + 2 拒绝，含"改了措辞仍被拒"） |
| 幂等刷新：同一句两次 → 1 行且 `updated_at` 前进 | `memorywrite.TestSavingTheSameMemoryTwiceRefreshesItInPlace` + `agent.TestARepeatedRequestRefreshesAndSaysSo`（含 `refreshed` 事件标志） |
| `PATCH` 改 `content` 后 `GET /v1/memories` 立即反映 | `app.TestAnEditIsVisibleOnTheNextList` |
| 别人的 `memory_id` → `not_found` | `memorywrite.TestEditingAnotherUsersMemoryIsNotFound` + `app.TestEditingAnotherUsersMemoryIsNotFound` + `httpapi.TestUpdateMemoryEndpointHidesAnotherUsersMemory` |
| 注入闭环：写入 constraint 后下一轮 system 段包含它；删除后不再出现 | `agent.TestASavedConstraintReachesTheNextTurnsPrompt`（三段断言，含段内出现"约束"分类词） |
| 关闭开关：`save_memory` 不在 `Registry.Specs()` 中，且 `AgentConfig` 校验通过 | `app.TestSaveMemoryIsRegisteredOnlyWhenTheSwitchIsOn` + `config.TestTurningMemoryWritesOffIsValidConfiguration` |

计划未列但本次补的用例（均由上文 ②③⑤⑥⑦ 的偏差带出）：
`memorywrite.TestAnEmptyQuoteIsRefused`（空引用不得被当成"包含于任何消息"）、
`TestSavingOutsideATurnIsRefused`、`TestSavingWithoutAUserIsRefused`、
`TestAUserRequestedMemoryIsStampedAndWeightedByItsType`（三种类型的置信度/来源）、
`TestTheRefreshKeyIsTheTypeAndTheExactContent`、`TestSaveRejectsMalformedRequests`、
`TestTheContentLimitCountsCharacters`（200 个汉字应通过）、
`TestAnEditKeepsTheRowAndFollowsTheType`、`TestAPartialEditLeavesTheOtherFieldAlone`、
`TestUpdateRejectsMalformedEdits`；
`agent.TestAUserRequestedMemoryIsWrittenAndAnnounced`（行与事件指向同一条）、
`TestMemoriesAreNotInjectedWithoutAUser`；
`httpapi.TestUpdateMemoryEndpointCarriesATypeOnlyEdit`、
`TestUpdateMemoryEndpointRejectsAnEmptyEdit`、`TestUpdateMemoryEndpointRequiresTheCaller`；
`app.TestMemoryManagementReportsADisabledCapability`、
`TestToStreamEventCarriesTheSavedMemory`、
`TestToStreamEventLeavesTheSavedMemoryEmptyOnAnAnswer`。

**⑩ 环境相关（非本次改动引入）**：`make test` 在 chat-service 仍只有 M5-03 记录 ⑧
那条 Ollama 相关失败，其余 16 个包全绿（M5-06 记录 ⑪ 时的 15 个包 + 本任务新增的
`internal/memorywrite`）；`shared` 全绿（含 439s 的 Postgres 契约套件），
`data-pipeline` 全绿。

### M5 范围内补充：Agent Console 流式过程可见性（计划外加入的 M5-08）

不在原 M5-01…M5-07 七片计划里，由用户在 M5 收口后单独触发（2026-10-05）。
**问题**：发出消息后前端只显示「运行中」，中间的 plan / 工具调用 / 检索过程没有任何
视觉提示，最后整段答案突然出现。**根因**有三个：

1. `message.start` 在 `ingress` 节点**末尾**才发出（`nodes.go:30-59`），意味着 TTFB 期间
   （3×DB 读 + memory embedding + LLM Extract）浏览器看不到任何字节。
2. `planModel.Generate` 是同步非流式调用，5-30s 没有任何事件。
3. 既有的 `tool.start` / `tool.finish` 虽在流里，但只渲染在右侧「工具链」tab 的
   `ToolTimeline` 中，**对话区内不可见**。`useAgentTurn` 的 50ms 节流进一步把已经到达
   的事件压成「每 50ms 一次」视觉。

**修法**：在 SSE 协议上新增 4 个事件（`phase.started` / `phase.finished` /
`step.started` / `step.finished`），由 `instrumented` 包装器在每个图节点前后发出，
ingress 内部三个子动作各发一对 step 事件；前端把 phase/step 渲染为对话区**内联**
的步骤列表，并在状态条显示当前活跃 phase 的标题。`message.start` 提前到 `ingress`
顶部，让前端一收到请求就知道已开始。

**关键改动**：

- **后端**：`httpapi/sse.go` 新增 4 个 `StreamEventType` 常量与对应 payload 形状
  （`Phase/PhaseID/Step/StepID/Title/StartedAt/FinishedAt/Outcome`），`payload()` 加
  4 个 case；`agent/state.go` 给 `Event` 加同名字段；`agent/nodes_instrument.go`（新增）
  提供 `instrumented` 包装器 + `emitStep` helper + `phaseCounter`（每个 phase 名单独
  计数，使 plan<->tools 循环的多次进入各有独立 id）；`agent/runner.go` 在
  `compile()` 里把每个 Lambda 包成 `traced(r, name, instrumented(r, phase, title, fn), detail)`；
  `nodes.go` 的 `ingress` 把 `EventStart` 提前到顶部，三个子动作改用 `emitStep` 包起来。
- **配置**：`AgentConfig.PhaseEvents bool`（env `AGENT_PHASE_EVENTS`，默认 true）——
  关闭时 `instrumented` 直接 pass-through，`sse.payload()` 不发出 4 个新事件，
  前端 reducer 的 default 分支忽略未知事件，部署退化为老行为。
- **前端**：`api/chat.ts` 的 `StreamEvent` union 加 4 个成员；`hooks/useAgentTurn.ts`
  的 `TurnState.phases` 字段 + reducer 4 个 case + 节流分流（关键事件
  —— `phase.*` / `step.*` / `message.start|end|replace` / `tool.start|finish` / 状态/错误——
  立即 flush；文本 delta 维持 50ms 批处理）；`components/agent/step-timeline.tsx`
  （新增）展示 phase + 嵌套 step 行；`message-list.tsx` 在 assistant Bubble **之前**
  渲染 `<StepTimeline>`（仅当 `streaming === true`，turn 结束后不显示——右栏
  ToolTimeline 已永久记录）；`turn-status-bar.tsx` 在「运行中」徽章旁追加活跃
  phase 的中文标题。
- **测试**：后端 `nodes_instrument_test.go`（6 个用例覆盖开关、id 配对、错误时
  也发 finished、多次 visit id 递增、step 嵌套、无 parent phase 时降级运行）+
  `sse_event_test.go` 扩展封闭集合与 3 个 payload 断言；前端 `useAgentTurn.test.ts`
  加 5 个 phase/step reducer case，`step-timeline.test.tsx` 4 个组件 case，
  `AgentConsole.test.tsx` 端到端脚本化断言「步骤列表可见 + 关闭后消失」。

**测试结果**：`chat-service/internal/agent` 包 7 个包全绿（含新增 6 个测试），
`httpapi` 82s 全绿（含新增 3 个测试 + 封闭集合从 11 增至 15），`app` /
`config` 全绿；`shared` / `data-pipeline` 编译通过；前端 `npm test` 117 个
用例全绿（含新增 9 个）。

**影响**：
- 正向：TTFB 期间前端立刻看到 phase.started；plan 阶段显示「正在制定下一步计划」占位
  + 转圈；工具调用边调用边出现在对话区（不再只藏在右栏）；总耗时不变（不改 planModel
  内部、不动 ingestion / embedding / retrieval 实现）。
- 中性：`turn-status-bar` 多了一行副标题、`message-list` 多了一个内联步骤列表；
  关闭开关 `AGENT_PHASE_EVENTS=false` 部署退化为老行为，无破坏性。
- 中性：事件总数从 11 增至 15（`message.*` 5 + `tool.*` 2 + `citation` 1 +
  `state.awaiting_input` / `confirmation.required` / `memory.saved` 3 +
  `phase.*` / `step.*` 4）；M5-07 总结的"每个新事件都要在四处登记"约定继续生效
  —— `agent.EventType` 常量、`agent.Event` 字段、`httpapi.StreamEventType` 常量、
  `sse.payload()` 分支，外加 `httpapi.StreamEvent` 字段与 `app.toStreamEvent` 映射。

---

## 里程碑收口（M5-01 … M5-07）

七个任务全部实现完成，附录 E 共记录 ①…⑪（M5-06）/ ①…⑩（M5-07）等偏差条目。
§1.2 的八条验收标准与 §6.2 门禁的落点见各任务小节，此处只记三条**跨任务**结论：

1. **两个写入闸门，一个原则。** `hitl`（`required`：模型请求、用户批准）与
   `memorywrite`（`implicit`：用户指令、输入校验）覆盖了 M5 的全部副作用。
   两者的共同点是"授权来自服务端已有的一个事实"——前者是用户对某个具体请求的决定，
   后者是用户自己的原话。没有第三条路径。
2. **`Plan` 与 turn context 是同一套机制。** 硬条件（M5-01）、指代 pin（M5-05）、
   用户原话与身份（M5-07）都经 context 传给工具，且都遵守"填缺不覆盖"。
   这是"模型给的不被改写、模型漏的被补齐"这条规则在三个任务上的同一次实现。
3. **每个新事件都要在四处登记。** `agent.EventType` 常量、`agent.Event` 字段、
   `httpapi.StreamEventType` 常量、`sse.payload()` 分支，外加 `httpapi.StreamEvent`
   字段与 `app.toStreamEvent` 映射。M5 期间事件从 8 增至 10，
   遗漏任意一处的失效模式是"整条流在该帧处失败"而非静默丢字段——
   `sse_event_test.go` 的封闭集合是这条约定的执行者。


