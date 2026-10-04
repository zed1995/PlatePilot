# PlatePilot Agent 验证台（Web）任务文档

> 生成日期：2026-10-04
> 依据文档：
> - `docs/platepilot-m5-task-document.md`（§2.3.4 SSE 事件契约、§2.3.5 HTTP 路由、§2.3.6 工具清单、§7 与 M6 的衔接）
> - `docs/platepilot-implementation-plan.md`（§9 Gate E 展示门）
> - `docs/platepilot-technical-prd.md`（§3.1–3.5 Agent 能力、§7 API）
> - `web/AGENTS.md`（只读运维台的既有约定与规则）
> - `AGENTS.md`（依赖方向、测试政策、提交与文档约定）
> - 代码现状：`chat-service/internal/httpapi/{router,conversation_handler,sse,run_handler,confirmation_handler,memory_handler,interpret_handler,restaurants}.go`
>
> 里程碑归属：M5 之后的 **M6 展示门（Gate E）前置**。M5 已把 `/v1` 对话契约做到 ready，
> 本文档把这套契约做成一个人能在浏览器里操作的界面，用于**初步验证 Agent 能力**。
> 当前状态：**待开始**。M5 已交付并提交在 `main`（`ea706e7`）。
> 首批执行：**W-01（后端配套缺口）或 W-02（前端 API 层）**，取决于 §0.4 D1 的决策。
>
> 本文只描述**前端**工作为主；W-01 是例外，它在 `chat-service` 里改，因为有三件事
> 前端自己做不到（见 §2.4）。除此之外本文档不改动任何 Go 侧行为。

---

## 0. 如何使用本文档

本文档是 Agent 验证台的唯一执行清单。每个任务给出：目标、交付物、实现要点、依赖、工作量级和验收标准。
实施时按 §3.2 的顺序拆提交；一个任务完成后对照 §5.1 的通用 DoD 自检。
与计划的偏差追加到文末「附录 E：实施记录」，不要回头改原计划。

代码契约以仓库现状为准；本文档中的接口形状是从现有 Go 代码抄下来的**事实**，
不是约定（端点、字段名均已核对）。若后端字段改名，按 `web/AGENTS.md` 的规则先改
`src/api/types.ts` 再改页面。

### 0.1 起点状态

**后端已 ready、前端可直接消费的事实（2026-10-04 实测）：**

| 能力 | 端点 | 实测状态 |
| --- | --- | --- |
| 建线程 | `POST /v1/conversations` | ✅ 201 |
| 读线程 + checkpoint | `GET /v1/conversations/:id` | ✅ |
| 历史消息（游标分页） | `GET /v1/conversations/:id/messages?limit=&before_id=` | ✅ |
| 发消息（SSE，POST） | `POST /v1/conversations/:id/messages` | ✅ 事件完整 |
| 确认闸门 | `POST /v1/conversations/:id/confirm` | ✅ 代码在（预约默认关） |
| 记忆查看/改/删 | `GET` / `PATCH` / `DELETE /v1/memories[/:id]` | ✅ |
| 工具链回放 | `GET /v1/conversations/:id/runs`、`GET /v1/runs/:run_id` | ✅ |
| 检索 / 证据 / 槽位探针 | `POST /v1/restaurants/{search,evidence,interpret}` | ✅ 实测 |
| 开发代理 | Vite 已把 `/v1` 代理到 `127.0.0.1:8080` | ✅ 无需 CORS |

**前端现状：**

| 项 | 现状 |
| --- | --- |
| 定位 | `web/AGENTS.md` 定义为「只读运维控制台」，只有 `/admin/v1/*` |
| 页面 | 8 个：Dashboard、Restaurants(+Detail)、Documents(+Detail)、Ingestion(+Detail)、RetrievalDebug |
| 导航 | `components/sidebar.tsx` 里一个 5 项的 `items` 数组 |
| API 层 | `api/client.ts`（`adminApi`，**无** `X-User-ID`、**无** SSE）、`api/types.ts` |
| 可复用组件 | `trace-panel`、`json-block`、`key-set-table`、`status-tag`、`error-state`、`empty-state`、`page-header`、`stat-card` + `components/ui/*` 13 个原子件 |
| 依赖 | 无 `react-markdown`、无 SSE 客户端库；有 TanStack Query、Radix、Motion、Lucide |
| 测试 | `vitest` + React Testing Library，每个页面一个同名 `.test.tsx` |

### 0.2 工作量级定义

沿用 M2–M5 定义：`S` 约 0.5 天以内；`M` 约 1 天；`L` 约 1.5–2 天。

| 任务 | 量级 | 说明 |
| --- | --- | --- |
| W-01 后端配套三端点 | M | 三个只读端点 + Go 侧契约测试；选降级方案则为 0 |
| W-02 前端 API 层与类型 | M | `chat.ts` + `types.ts` 扩展 + `X-User-ID` 注入 |
| W-03 SSE 流式客户端与轮次状态机 | M | 自研 `fetch` + `ReadableStream` 解析；10 种事件归约 |
| W-04 验证台骨架 | M | 路由、导航、线程列表、消息区、输入框 |
| W-05 工具链时间线 + 引用/证据 + run 回放 | M | 本页面的核心差异价值 |
| W-06 闸门 UI（澄清 / 确认 / 记忆） | M | 三个 `awaiting_*` 与写入类事件的可操作面 |
| W-07 槽位探针卡片 | S | 复用 `interpret`，验证 Agent 最好用的一屏 |
| W-08 测试、构建与演示脚本 | S | 页面测试 + 解析器单测 + 附录 B 验收脚本 |

合计约 **6.5 天**（不含 W-01 为 5.5 天）。

### 0.3 前置条件

```bash
# 1. 前端基线
cd web && npm install && npm test && npm run build

# 2. 后端起来（必须带 .env，直接跑 bin 读不到配置会启动失败）
make pg-up && make migrate     # 库必须到 0009；停在 0006 时第一条消息就 502
make -C chat-service build
make run                        # 或 HTTP_ADDR=127.0.0.1:8080 带 .env 起 bin
curl -s http://127.0.0.1:8080/healthz

# 3. 软条件召回要完整（可选；不起则向量通道降级，链路照跑）
ollama serve && ollama pull qwen3-embedding:0.6b

# 4. 前端（Vite 已代理 /v1）
cd web && npm run dev           # http://localhost:5173
```

**必须记住的两条环境事实**（实测踩到过）：

1. `make migrate` 是硬前置。本地库停在 0006 时，发第一条消息报
   `column "pending_tool_call_id" does not exist`（迁移 0007/0008/0009 是纯新增，幂等）。
2. `chat-service` 必须带 `.env` 启动（`make run` / `make dev`），
   裸跑 `./bin/chat-service` 会因读不到配置启动失败。

### 0.4 三个待定决策（开工前先定，影响任务边界）

| 编号 | 决策 | 推荐 | 影响 |
| --- | --- | --- | --- |
| **D1** | 后端配套缺口（§2.4 G1–G3）是否纳入本文档？ | **纳入，走 W-01** | 不做则线程列表、候选卡片、引用正文三处只能降级（见 §2.4 的降级列） |
| **D2** | 页面定位：混入运维导航，还是独立「Agent 验证台」？ | **独立路由 `/agent`，sidebar 单独分组** | `web/AGENTS.md` 现定位是只读运维台，对话页是定位扩展，混进去会让「只读」这个承诺变模糊 |
| **D3** | 回答渲染：引 `react-markdown` 还是自研极简渲染？ | **自研**（只处理段落、列表、`[^id]` 上标） | 回答是固定的五段式结构（`①结论 ②匹配原因 ③来源与数据时间 ④无法确认 ⑤FOLLOWUPS:`），只需把 `[^n]` 变成可点上标；不引新依赖符合 `web/AGENTS.md` 的规则 |

---

## 1. 目标与退出条件

### 1.1 目标

M5 把七条纵向切片做完了，但**它现在只能通过 curl 和 SQL 验证**——一个 2 分钟的 SSE 流、
10 种事件、三个需要用户接手的闸门，靠读终端输出判断不了「这个 Agent 到底行不行」。
本里程碑要交付一个能让人**用嘴验证**的界面：

1. **能对话**：自然语言输入 → 逐字流式输出 → 一轮完整回答，中途不像是卡死。
2. **能看见过程**：工具调用一条条实时出现（名字、状态、耗时），而不是只在结束后给结果。
3. **能核对**：回答里的 `[^n]` 能点到原文，知道这句话依据哪条资料、数据时间是什么。
4. **能续**：多轮追问（「第二家安静吗」）在同一线程里连续可用，刷新后还能接上。
5. **能接手闸门**：澄清、确认、记忆三类"系统在等你/刚替你写了东西"的信号有明确的操作入口，
   而不是消失在流里。
6. **能定位问题**：一句话输进去，先看懂系统把它理解成了什么（硬条件 / 软条件分离），
   再决定是模型的问题还是检索的问题。

### 1.2 非目标（明确不做）

- **不做真实鉴权**：`X-User-ID` 仍是明文占位头（M6 才做登录）。页面提供一个可编辑的输入框即可。
- **不做移动端适配 / 不换设计语言**：沿用苹果风灰度单色。
- **不改任何 Go 侧行为**（W-01 的三个只读端点除外，它们是新增，不是改语义）。
- **不做预约主链路**：`RESERVATION_ENABLED` 默认 `false`，确认闸门 UI 做好但默认不出现；
  开启时用 `get_availability` / `request_reservation` 走同一套 UI。
- **不做节点级 trace**、不做地理编码、不做预约取消/改期——这三项 M5 文档已明确留给 M6。

### 1.3 退出条件（Exit Criteria）

以下七条全部满足才算交付：

1. **一轮完整对话可用**：在页面里输入一句中文硬+软条件问题，能看到流式回答、
   至少一次 `search_restaurants` 与一次 `get_restaurant_evidence` 的实时出现与耗时。
2. **引用可核对**：回答中的 `[^n]` 与 `citation` 事件的 `evidence_ids` 一致，点击后能看到
   证据原文、餐厅名、`doc_type`、来源与快照时间。
3. **工具链可回放**：回答结束后能在同一屏拉出本轮 run 的工具序列（含 `result_summary`），
   与流式里看到的顺序一致。
4. **多轮可续**：同一线程连发「第二家安静吗」，页面不丢上下文；刷新页面后仍能列出历史消息并继续。
5. **闸门可操作**：`state.awaiting_input` 出现时页面明确显示"系统在等你说 XX"；
   `confirmation.required` 出现时有 确认/取消 两个按钮且点击后状态变化；
   `memory.saved` 出现时有可见提示，并能在记忆面板里改、删。
6. **槽位可诊断**：输入任意句子都能看到 `interpret` 的意图、硬条件、软条件、`source`（model/rules）与耗时。
7. **质量基线**：`npm test` / `npm run build` 全绿；每个新页面有同名 `.test.tsx`；
   SSE 解析器有独立单测（含心跳、跨 chunk 拆帧、未知事件三类边界）；
   后端侧 `make test && make vet && make build` 未被这次改动破坏。

---

## 2. 目标形态

### 2.1 前端要消费的后端契约（事实，已核对代码）

#### 2.1.1 HTTP 端点

| 方法 | 路径 | 请求 | 响应要点 |
| --- | --- | --- | --- |
| POST | `/v1/conversations` | `{title?}`（≤200 rune） | 201 `{thread_id,user_id,title,current_state,created_at,updated_at,checkpoint?}` |
| GET | `/v1/conversations/:id` | — | 同上；`checkpoint` 含 `state / pending_action / missing_slots / evidence_ids / selected_restaurant_id / version` |
| GET | `/v1/conversations/:id/messages` | `?limit=1..100&before_id=` | `{messages:[{message_id,role,content,evidence_ids,seq,created_at}]}`，按 seq 升序 |
| POST | `/v1/conversations/:id/messages` | `{content}`（1–4000 rune） | **SSE 流**；线程不存在是 JSON 404，不是流内错误 |
| POST | `/v1/conversations/:id/confirm` | `{decision:"confirm"\|"cancel"}` | `{thread_id,decision,pending_action,state,output,summary,replayed,message}` |
| GET | `/v1/memories` | 需 `X-User-ID` | `{memories:[{id,memory_type,content,source,confidence,created_at,updated_at}]}` |
| PATCH | `/v1/memories/:memory_id` | `{content?}` / `{memory_type?}`（至少一项） | 更新后的 `MemoryView` |
| DELETE | `/v1/memories/:memory_id` | 需 `X-User-ID` | 204 |
| GET | `/v1/conversations/:id/runs` | `?limit=&before_id=` | `{runs:[RunView]}`，新→旧 |
| GET | `/v1/runs/:run_id` | — | `{run:RunView, tool_calls:[ToolCallView]}` |
| POST | `/v1/restaurants/search` | `{query?,text?,filter?,top_k?}` | `{candidates:[...],trace}` |
| POST | `/v1/restaurants/evidence` | `{restaurant_ids:[...]}`（**必填**） | `{evidence:[...],trace}` |
| POST | `/v1/restaurants/:id/evidence` | 可选 body | 同上，路径 id 优先 |
| POST | `/v1/restaurants/interpret` | `{text}` | `{intent,query,hard_filters,soft_conditions,named_restaurants,missing_slots,need_clarification,source,extract_latency_ms,warnings}` |

身份：`X-User-ID` 明文头。记忆三个端点**强制要求**（缺失直接 400）；
对话端点允许匿名（匿名时跳过记忆注入）。`GET /v1/conversations/:id` **不校验归属**——
知道 `thread_id` 就能读，这在 M6 鉴权落地前是已知状态，页面不要把它当"私有会话"来承诺。

#### 2.1.2 SSE 事件（10 种，封闭集合）

| 事件 | payload | 前端该做什么 |
| --- | --- | --- |
| `message.start` | `{run_id, thread_id}` | 开一条 assistant 气泡，记下 run_id（回放要用） |
| `message.delta` | `{delta}` | 追加文本（高频，注意批量 setState） |
| `tool.start` | `{call_id, tool}` | 时间线插入一条"进行中" |
| `tool.finish` | `{call_id, status, latency_ms}` | 时间线该条收尾（成功/失败 + 耗时） |
| `citation` | `{evidence_ids:[n]}` | 记入本轮引用集合，触发正文预取 |
| `state.awaiting_input` | `{state, pending_action?, missing_slots?}` | 显示"系统在等你"，提示往哪答 |
| `confirmation.required` | `{state, pending_action, summary}` | 渲染 确认/取消（summary 是给人读的句子） |
| `memory.saved` | `{memory_id, memory_type, content, refreshed?}` | 提示"已记住/已更新"，可跳转记忆面板 |
| `message.end` | `{finish_reason, usage?, warnings?}` | 收尾；`warnings` 必须显示（降级信息在这里） |
| `error` | `{code, message}` | 该气泡标记失败，不吞掉 |

两个传输细节：

- **心跳是 SSE comment**（`: keep-alive`，15s 一次）。解析器必须忽略以 `:` 开头的行，
  否则会把它当成一个没有事件名的帧。
- **帧可能被 TCP 拆开**。必须按 `\n\n` 累积缓冲再切帧，不能假设一次 `chunk` 就是一整帧。
- `tool.finish` **不带** `result_summary`（只有回放接口有）。实时流里能看到"跑了多久、成功没"，
  看不到"返回了什么"——这是现状，不要在 UI 上假装能显示。

### 2.2 前端目标目录结构

```text
web/src/
  api/
    chat.ts                 # 新增：/v1 对话面（含 X-User-ID、SSE POST）
    types.ts                # 扩展：Thread / Message / Memory / Run / ToolCall / Checkpoint
    client.ts               # 不改动
  lib/
    sse.ts                  # 新增：POST SSE 解析（fetch + ReadableStream，含心跳与拆帧）
    answer.ts               # 新增：[^n] 上标解析 + 五段式结构的极简渲染数据
  hooks/
    useAgentTurn.ts         # 新增：一轮对话的状态归约（idle→streaming→done/error）
    useThreads.ts           # 新增：线程列表 / 当前线程（TanStack Query）
  components/
    agent/
      thread-list.tsx       # 线程列表 + 新建
      message-list.tsx      # 气泡流（含流式追加、引用上标）
      composer.tsx          # 输入框（Enter 发送、进行中禁用）
      tool-timeline.tsx     # 工具调用实时时间线
      run-replay-panel.tsx  # run / tool_calls 回放（复用 json-block + trace-panel）
      citation-drawer.tsx   # 引用原文抽屉
      gate-banner.tsx       # awaiting_input / confirmation.required 的操作条
      memory-panel.tsx      # 记忆列表 + PATCH + DELETE
      slot-probe-card.tsx   # interpret 探针
      turn-status-bar.tsx   # run 元信息（模型、耗时、token、warnings）
  pages/
    AgentConsole.tsx        # 新增：主页面
    AgentConsole.test.tsx   # 新增
```

### 2.3 页面信息架构与轮次状态机

```text
/agent
├── 左：线程列表（新建 / 切换 / 显示 current_state 徽标）
├── 中：对话区
│   ├── 历史消息（挂载时 GET messages）
│   ├── 当前轮气泡（流式追加）
│   ├── 轮次状态栏：模型 · 耗时 · token · warnings
│   └── 底部输入（进行中禁用 + 停止按钮）
└── 右：可折叠侧栏（三选一 tab）
    ├── 工具链（本轮实时 + 结束后 run 回放）
    ├── 引用（citation 集合 → 原文）
    └── 记忆 / 槽位探针
```

**一轮对话的状态机（`useAgentTurn`）**——这是页面唯一的核心逻辑，其余都是渲染：

```text
idle ──send──> streaming ──┬── message.end ──> done
                           ├── error ────────> failed
                           ├── state.awaiting_input ──> awaiting_user（仍以 message.end 收尾）
                           ├── confirmation.required ─> awaiting_confirm（仍以 message.end 收尾）
                           └── 用户点停止 ────> canceled（abort fetch）
```

关键约束：

- `awaiting_*` 不是失败。两种闸门事件之后**仍会有 `message.end`**，气泡要正常收尾，
  只是线程停在等待态（`GET /v1/conversations/:id` 的 `current_state` 会体现）。
- 一次 `send` 只允许一个进行中的流。进行中禁用输入框（或点发送先 abort 前一个）。
- 流结束后用 `run_id` 拉一次 `/v1/runs/:run_id` 补齐 `result_summary`——
  实时流给不了的东西由回放补，两者合并成同一条时间线。

### 2.4 缺口清单（前端自己解决不了的三件事）

| 编号 | 缺口 | 证据 | 降级方案（不推荐） | 推荐方案 |
| --- | --- | --- | --- | --- |
| **G1** | **没有会话列表端点**。只有 `POST /v1/conversations` 与 `GET /v1/conversations/:id`，没有按用户列的列表 | `registerChatRoutes` | 前端把 `thread_id` 存 localStorage；换浏览器/清缓存即丢，且无法验证"重启后续接" | 新增 `GET /v1/conversations?user_id=&limit=&before=`（按 `X-User-ID` 过滤，只读） |
| **G2** | **候选列表没有 HTTP 面**。M5-05 已落库 `conversation_candidates`，但没有任何端点暴露它 | `httpapi` 内无 candidates 路由；`checkpointView` 只有 `evidence_ids` 与 `selected_restaurant_id` | 不显示候选卡片，只能从回答文本里读名字 | 新增 `GET /v1/conversations/:id/candidates`（`{position,restaurant_id,name,score}`，只读） |
| **G3** | **引用正文反查不了**。`citation` 只给 `evidence_ids:[]`，而 `/v1/restaurants/evidence` **必填 `restaurant_ids`**；`evidence_ids` 是 `knowledge_documents.document_id`，前端无从得知它属于哪家餐厅 | `restaurants.go` 的 `requireScope()`、`evidence.Evidence.EvidenceID = d.DocumentID` | 只显示"引用 #3"编号，点开是空的——等于没有引用核对 | 让 `POST /v1/restaurants/evidence` 额外接受 `evidence_ids:[]`（与 `restaurant_ids` 二选一，按 id 直接取文档），或新增 `GET /v1/evidence?ids=1,2,3` |

**这不是设计瑕疵，是顺序问题**：M5 的验收是靠 curl + SQL 完成的，候选与引用正文当时不需要 HTTP 面。
前端一上来要"可核对"，这三处就成了硬阻塞。**建议 W-01 一次补齐**，三个端点都是只读、
不改既有语义、不动迁移。

---

## 3. 任务清单总览

| 任务 | 名称 | 主要交付 | 依赖 | 量级 |
| --- | --- | --- | --- | --- |
| W-01 | 后端配套三端点 | `GET /v1/conversations`、`GET /v1/conversations/:id/candidates`、证据按 id 反查 | M5-05 候选表 | M |
| W-02 | 前端 API 层与类型 | `api/chat.ts`、`types.ts` 扩展、`X-User-ID` 注入 | — | M |
| W-03 | SSE 流式客户端与轮次状态机 | `lib/sse.ts`、`hooks/useAgentTurn.ts` | W-02 | M |
| W-04 | 验证台骨架 | 路由 `/agent`、导航分组、线程列表、消息区、输入框 | W-02、W-03 | M |
| W-05 | 工具链 + 引用 + run 回放 | `tool-timeline`、`citation-drawer`、`run-replay-panel` | W-04、W-01(G3) | M |
| W-06 | 闸门 UI | `gate-banner`（澄清/确认）、`memory-panel`（列改删） | W-04 | M |
| W-07 | 槽位探针卡片 | `slot-probe-card`（interpret） | W-02 | S |
| W-08 | 测试、构建与演示脚本 | 页面/解析器测试、附录 B 验收脚本 | W-04…W-07 | S |

### 3.1 依赖图

```text
W-02 ──> W-03 ──> W-04 ──┬──> W-05 ──> W-08
  │                       ├──> W-06 ──> W-08
  └──> W-07 ──────────────┴────────────> W-08

W-01 ──(G1)──> W-04 的线程列表
     ──(G2)──> W-05 的候选卡片
     ──(G3)──> W-05 的引用抽屉（硬阻塞）
```

W-01 与 W-02/W-03 可并行（不同仓库目录）。W-05 的引用抽屉是唯一被 W-01 硬阻塞的项。

### 3.2 推荐执行顺序

1. **W-02 + W-03**（可离线，用 mock 流验证解析器）——先把最难的一段（POST SSE）做对。
2. **W-01**（若采纳 D1）——与上面并行，做完 W-05 才不被卡。
3. **W-04**——骨架能跑一轮对话，此时页面已经"能用"。
4. **W-05**——补齐工具链、引用、回放，此时页面才"有价值"。
5. **W-06**——三个闸门。
6. **W-07**——探针卡片（小，可任何时候插队）。
7. **W-08**——测试与验收脚本收口。

单人按此顺序即可，无需并行。

---

## 4. 详细任务

### W-01 后端配套三端点（chat-service）

**目标**：补齐「前端可核对」所缺的三个只读端点，不改任何既有语义。

**交付物：**

1. `GET /v1/conversations`：按 `X-User-ID` 返回该用户的线程，新→旧，`?limit=&before_id=` 游标
   （复用消息 API 的分页习惯，`before_id` 为 exclusive cursor）。缺失身份头时按记忆端点的
   既有做法返回 400，不要返回全部人的会话。
2. `GET /v1/conversations/:id/candidates`：返回该线程当前候选组
   `[{position, restaurant_id, name, score}]`，按 `position` 升序。空数组而非 null。
3. 证据反查：`POST /v1/restaurants/evidence` 的 `EvidenceQuery` 增加可选 `evidence_ids []int64`，
   与 `restaurant_ids` 二选一；两者都给时以 `restaurant_ids` 为准（保持"路径/显式范围优先"的既有规则），
   两者都缺时仍走既有的 `CodeRetrievalNoScope` 错误。
4. 三个端点各自进 `ChatService` / `EvidenceService` 接口，契约测试补进
   `shared/store/contract/`（若涉及新仓储方法）或 `httpapi` 的 handler 测试。

**实现要点：**

- **只读，不落库、不改状态**。这三个端点是一面镜子，不是新能力。
- 候选端点读的是 M5-05 已落库的 `conversation_candidates`（整组覆盖写入的表），
  不要新造一份"当前候选"的推导逻辑。
- 证据按 id 取时**仍要过既有的组装与过滤**（`is_active`、scope），不要因为给了 id 就绕开——
  否则前端会拿到已下线文档的正文。
- 遵循 M5 的三处登记习惯：新端点要同时出现在 `httpapi` 路由、`ChatService` 接口注释、
  以及本文档附录 B 的 curl 清单里。

**测试：**

- Handler 层：伪造 service 断言三个端点的响应形状与空数组行为；
- 归属：另一个 `X-User-ID` 看不到线程（列表端点）；
- 证据：`evidence_ids` 命中存在/不存在/已下线三种 id 的行为；
- `make test && make vet && make build` 全绿，架构测试未放宽。

**验收标准：** §1.3 第 2、4 条；附录 B 三条 curl 全部返回预期。

**工作量：M。依赖：M5-05（候选表）、M3-07（证据组装）。**

---

### W-02 前端 API 层与类型

**目标**：把 `/v1` 对话面封装成一个有身份、能发 SSE 的模块，与只读运维台分开。

**交付物：**

1. **`src/api/chat.ts`**（新增，与 `client.ts` 并列，不合并）：
   - 模块级 `currentUserId`（默认 `demo-user`）+ `setUserId/getUserId`；
   - `request()` 封装：所有请求带 `X-User-ID`，错误一律走 `client.ts` 已有的 `ApiError`
     （复用 `error.code / message / request_id` 信封，不要造第二套错误类型）；
   - 方法：`createThread`、`getThread`、`listThreads`（W-01 后）、`listMessages`、
     `listCandidates`、`listMemories`、`updateMemory`、`deleteMemory`、`confirm`、
     `listRuns`、`getRun`、`interpret`、`searchRestaurants`、`fetchEvidence`；
   - `sendMessage()` 单独导出：返回 `AbortController` + 事件回调，不返回 Promise 结果
     （SSE 是过程不是结果）。
2. **`src/api/types.ts` 扩展**：`Thread`、`CheckpointView`、`MessageView`、`MemoryView`、
   `RunView`、`ToolCallView`、`CandidateView`、`InterpretResult`；
   字段名与 Go JSON tag 一一对应（`thread_id`、`evidence_ids`、`memory_type`、`latency_ms`…）。
3. **`src/lib/sse.ts`**（可归入 W-03，但类型在此定义）：`parseSSEStream(response, onEvent, signal)`。

**实现要点：**

- `client.ts` 是"无身份的只读运维台"，`chat.ts` 是"有身份的对话面"，**分成两个模块**。
  硬凑成一个会让 `adminApi` 的每个调用都得传 user id。
- `base` 仍取 `import.meta.env.VITE_API_BASE ?? ''`：开发靠 Vite 代理，不需要 CORS。
- 不要在 `chat.ts` 里做重试。一轮 2 分钟，自动重试会静默产生第二次模型调用。
- 类型就是契约边界：后端改字段先改这里（`web/AGENTS.md` 既有规则）。

**测试：**

- `chat.test.ts`：用 `vi.stubGlobal('fetch', ...)` 断言 (a) 每个请求都带 `X-User-ID`、
  (b) 4xx 时抛出带 `code` 的 `ApiError`、(c) `sendMessage` 走 POST 且 body 正确。

**验收标准：** §1.3 第 7 条；类型与 Go tag 逐字对齐（`grep` 可核对）。

**工作量：M。依赖：无（可与 W-01 并行）。**

---

### W-03 SSE 流式客户端与轮次状态机

**目标**：把 POST SSE 流的解析和一轮对话的状态归约做对。这是全页面唯一有技术难度的部分。

**交付物：**

1. **`src/lib/sse.ts`**——`fetch` + `ReadableStream` 的 SSE 解析器：
   - 不能用 `EventSource`：端点是 **POST** 且要带 `X-User-ID` 头，`EventSource` 两样都做不到；
   - 按 `\n\n` 切帧，**跨 chunk 缓冲**（一个 `data:` 可能被 TCP 拆成两次 `enqueue`）；
   - 忽略 `:` 开头的 comment 帧（15s 一次的心跳），忽略空行；
   - 解析出 `{event, data}`，`data` 非 JSON 时丢弃该帧而不是整条流失败；
   - `AbortController` 支持（用户点停止 → 后端 `WithSenseClientDisconnection` 会取消这一轮）。
2. **`src/hooks/useAgentTurn.ts`**——把 10 种事件归约成一个可渲染的轮次状态：
   `idle | streaming | done | failed | awaiting_user | awaiting_confirm | canceled`；
   同时累积 `text`、`tools[]`（含 call_id→tool/status/latency 的配对）、
   `citations[]`、`warnings[]`、`runId`、`usage`。
3. **`src/lib/answer.ts`**——回答文本的极简结构化：`[^n]` → 上标 token，
   按空行切段落，识别 `FOLLOWUPS:` 段（可渲染成建议追问按钮）。

**实现要点：**

- `tool.start` 与 `tool.finish` 用 `call_id` 配对，**不要用数组下标**：并发工具调用时下标会错配。
- `message.delta` 是高频事件：按 ~50ms 批量 flush 到 state，不要每个 delta 一次 `setState`
  （一轮可能上千个 delta）。
- 未知事件名必须忽略而不是抛错：后端事件集是封闭的，但**未来会加**（M6 就计划加节点级 trace），
  前端对未知帧报错会让升级后端等于破坏前端。
- `[^n]` 的 n 与 `citation.evidence_ids` 是同一套 id（compose 层已保证），
  渲染时直接把上标映射到 evidence_id。
- 不要把 `warnings` 埋起来：向量通道降级这类信息只在 `message.end.warnings` 里出现一次，
  看不到就会把降级结果当成正常结果。

**测试：**

- 解析器单测（纯函数，喂字符串数组）：
  ① 一帧完整；② 一帧被拆成三个 chunk；③ 心跳 comment 被忽略；
  ④ `data` 是坏 JSON 时跳过而不中断；⑤ abort 后不再回调。
- 状态归约单测：喂一段脚本化事件序列，断言
  `tool.start` 与 `tool.finish` 按 `call_id` 正确配对、
  `awaiting_*` 后收到 `message.end` 时状态是 `awaiting_*` 而不是 `done`、
  `error` 事件后是 `failed` 且保留已收到的文本。
- 未知事件：喂一个 `trace.node` 帧，断言不抛错、不影响其他帧。

**验收标准：** §1.3 第 1 条；解析器单测全绿（这是 W-08 门禁的一部分）。

**工作量：M。依赖：W-02。**

---

### W-04 验证台骨架

**目标**：做一个能跑完一轮对话的页面——路由、导航、线程、消息、输入。

**交付物：**

1. **路由与导航**：`App.tsx` 增加 `/agent`；`sidebar.tsx` 的 `items` 增加
   `{to:'/agent', label:'Agent Console', icon: MessagesSquare }`，
   **并在视觉上分组**（运维台一组、Agent 验证台一组），对应 §0.4 D2。
2. **`pages/AgentConsole.tsx`**：三栏布局（线程列表 / 对话区 / 右侧栏壳）。
3. **`thread-list.tsx`**：列出线程（W-01 前退化成"新建 + 手动输入 thread_id"，
   W-01 后接 `GET /v1/conversations`）；每条显示标题 + `current_state` 徽标
   （`awaiting_clarification` / `awaiting_confirmation` 要用不同颜色——线程在等你，这是最重要的信息）。
4. **`message-list.tsx`**：挂载时 `GET /v1/conversations/:id/messages` 拉历史（按 `seq` 升序），
   流式气泡追加在末尾；user / assistant 区分；assistant 气泡渲染 `[^n]` 上标。
5. **`composer.tsx`**：Textarea（复用 `ui/textarea`），Enter 发送、Shift+Enter 换行，
   进行中禁用并给出"停止"按钮；空内容与超长（>4000 字符）前端先拦一次。
6. **`turn-status-bar.tsx`**：一轮结束后显示模型名、耗时、token、以及 `warnings`。

**实现要点：**

- 线程切换/刷新后必须能续接：这是 §1.3 第 4 条的落点。历史消息 + `current_state` 徽标
  是"跨请求存活"在 UI 上的唯一体现。
- 错误一律走既有的 `error-state` / `empty-state`，不要自造。
- 首屏空态要明确告诉用户先做什么（"新建会话 → 说一句像『布鲁克林 4 星以上安静的拉面』的话"）。
- 页面顶部放一行"当前身份：X-User-ID = xxx（可改）"——没有鉴权，身份就是全部隔离机制，
  必须让用户看得见自己是谁。
- 单轮 2–2.5 分钟是实测常态：输入框禁用期间要有明确的"运行中"指示（工具时间线本身就是，
  W-05 之前用一个带计时器的状态条）。

**测试：**

- `AgentConsole.test.tsx`：mock `api/chat`，断言
  ① 发送后输入框禁用、② 事件序列驱动下气泡文本逐步出现、
  ③ `error` 事件后显示 `error-state`、④ 历史消息按 seq 顺序渲染。
- 路由测试：`/agent` 渲染出页面（可并入上面）。

**验收标准：** §1.3 第 1、4 条。

**工作量：M。依赖：W-02、W-03；线程列表的完整形态依赖 W-01(G1)。**

---

### W-05 工具链 + 引用 + run 回放

**目标**：把"过程可见"和"结论可核对"做出来——这是这个页面区别于一个普通聊天框的全部价值。

**交付物：**

1. **`tool-timeline.tsx`**：实时时间线。`tool.start` 插入一条"运行中"（带 spinner 与已耗时），
   `tool.finish` 收尾显示 `status` + `latency_ms`；同名工具多次调用分行显示（不合并计数，
   用户要看的是"它到底跑了几次"）。结束后用 `run_id` 拉 `/v1/runs/:run_id`，
   把 `result_summary` 补到对应 `call_id` 上（实时流给不了，回放给）。
2. **`citation-drawer.tsx`**：点 `[^n]` 打开抽屉，显示该条证据的
   `restaurant_name`、`doc_type`、`content` 全文、`source`、快照时间。
   数据来自 `POST /v1/restaurants/evidence`（W-01 的 G3 之前拿不到——这是硬阻塞）。
3. **`run-replay-panel.tsx`**：本轮/历史 run 列表（`GET /v1/conversations/:id/runs`）
   + 选中后的工具序列；`arguments` 是脱敏摘要，用 `json-block` 原样折叠展示，**不要美化**。
4. **候选卡片**（依赖 W-01 G2）：`GET /v1/conversations/:id/candidates` 渲染成带 position 的卡片
   ——这样"第二家"指的是谁，页面上是看得见的。

**实现要点：**

- 时间线**不是日志**：按发生顺序、给人看。工具名直接显示（用户看得懂 `search_restaurants`，
  不需要翻译成"搜索餐厅"）。
- `result_summary` 只在回放里有，所以"结束后的时间线"比"进行中的时间线"信息更全——
  两次渲染共用一份数据结构，只差有没有 summary。
- 引用正文要**按需取**（点开才取），不要一轮结束时把全部引用都拉一遍：一轮可能 6–10 条证据。
- `trace` 与 `reasons` 是产品核心信息（`web/AGENTS.md` 既有规则）：
  候选卡片若带 `reasons`，必须显示，不能省略。
- 复述一遍红线：**引用越界是硬失败**。页面上看到的引用必须全部来自本轮证据；
  若某条 `[^n]` 在证据集里查不到，显示"引用不可用"而不是留一个死链。

**测试：**

- `tool-timeline.test.tsx`：串行的 start/finish 配对、并发（两个 start 交错 finish）按 `call_id` 正确配对；
- `citation-drawer.test.tsx`：点击上标后触发一次 evidence 请求，且请求带的是解析出的 id；
- `run-replay-panel.test.tsx`：run 列表 + 工具序列渲染，空态走 `empty-state`。

**验收标准：** §1.3 第 2、3 条；一轮真实对话里能看到 ≥2 次工具调用与耗时。

**工作量：M。依赖：W-04、W-01(G3 硬阻塞、G2 用于候选卡片)。**

---

### W-06 闸门 UI（澄清 / 确认 / 记忆）

**目标**：三种"系统在等你 / 系统刚替你写了东西"的信号必须有可操作入口，不能消失在流里。

**交付物：**

1. **`gate-banner.tsx`**：
   - `state.awaiting_input`：显示"系统在等你说 XX"。文案由 `pending_action` +
     `missing_slots` 决定，**但不要把内部槽位名直接念给用户**（实测出现过把
     `restaurant_id` 原样显示的情况）。澄清态下输入框保持可用——用户下一条消息就是回答。
   - `confirmation.required`：显示 `summary`（后端给的就是给人读的句子）+ 确认/取消两个按钮，
     点击调 `POST /v1/conversations/:id/confirm`，把返回的 `state` 与 `message` 渲染成一条系统消息；
     `replayed=true` 时明确提示"这条已经处理过"（幂等）。
2. **`memory-panel.tsx`**：`GET /v1/memories` 列表 + 行内改（`PATCH` content / memory_type）
   + 删除（`DELETE`，204）；`memory.saved` 事件出现时在对话区插一条"已记住：xxx"的提示，
   带"查看/修改"跳转；`refreshed=true` 时文案是"已更新"而不是"已记住"。
3. 三个面板都挂在 §2.2 的右侧栏 tab 里，不新开页面。

**实现要点：**

- 确认是**写操作**：按钮要二次确认或明显区分（确认主色、取消次级）；
  `RESERVATION_ENABLED=false` 时这个 banner 默认不出现，但代码路径要完整（开启即可用）。
- 记忆的 PATCH 语义是"至少给一项"：两个字段都空时前端先拦（后端也会 400）。
- 删除是不可逆的：`ui/dialog` 已存在，走一个确认弹窗。
- 记忆面板是 §1.3 第 5 条"可查看/修改/删除"的落点，也是 M5-07 验收门第 7 条的可见证据。

**测试：**

- `gate-banner.test.tsx`：澄清态文案不含原始槽位名；点击确认/取消各触发一次 `confirm`
  且 body 的 `decision` 正确；`replayed` 时显示提示。
- `memory-panel.test.tsx`：列表渲染、PATCH 只发改动字段、DELETE 后从列表移除、空态。

**验收标准：** §1.3 第 5 条。

**工作量：M。依赖：W-04。**

---

### W-07 槽位探针卡片

**目标**：一句话输进去，先看懂系统把它理解成了什么——验证 Agent 时最好用的一屏。

**交付物：**

1. **`slot-probe-card.tsx`**：输入框 + `POST /v1/restaurants/interpret`，结果分块显示：
   - `intent`（discover / recommend / restaurant_qa / reservation / chit_chat）
   - `hard_filters`（`borough / neighborhood / cuisines / price_levels / min_rating / name`）——
     结构化解，**整体用 `json-block` 兜底**；
   - `soft_conditions`（`text` + `topic`）——必须与硬条件视觉上明确分开；
   - `need_clarification` + `missing_slots`；
   - `source`（`model` / `rules`）+ `extract_latency_ms` + `warnings`。
2. 挂在右侧栏第三个 tab；提供"把这句话发给 Agent"的按钮，方便对照"理解"和"回答"。

**实现要点：**

- **硬软分离是这个卡片存在的全部意义**。UI 上两者不能用同一种样式、不能放在同一个列表里——
  "安静"进软条件、"4 星以上"进硬条件，是 M5-01/02 的核心不变量，这张卡片就是它的可视化验收。
- `source=rules` 要显式标注：没有模型时槽位来自规则兜底，看到它才知道结果为什么"偏机械"。
- 这个端点**不建线程、不落库、不花工具调用**，可以随便点，性能上也安全。

**测试：**

- `slot-probe-card.test.tsx`：mock 返回（硬条件 2 项 + 软条件 1 项），
  断言渲染分区正确、软条件不与硬条件混排、`source` 显示出来。

**验收标准：** §1.3 第 6 条；输入「曼哈顿中城 4 星以上意大利菜，要安静适合约会」时
能看到硬软分离正确。

**工作量：S。依赖：W-02。**

---

### W-08 测试、构建与演示脚本

**目标**：收口，并把"怎么手工演示一次"写清楚。

**交付物：**

1. 补齐每个新组件的测试（组件级）+ `AgentConsole.test.tsx`（页面级）；
   `lib/sse.ts` 与 `lib/answer.ts` 的纯函数单测。
2. `npm test` / `npm run build`（含 `tsc`）全绿。
3. **附录 B 的演示脚本**：curl 序列 + 页面操作步骤，覆盖 §1.3 七条退出条件。
4. 更新 `web/AGENTS.md`：页面数从 8 变 9，说明新增 `/agent` 是「Agent 验证台」、
   与只读运维台定位不同（对应 §0.4 D2）。

**实现要点：**

- 测试里不要打真实后端：mock `fetch`（与既有 `.test.tsx` 的做法保持一致）。
- 别为了覆盖率去测样式。
- `AGENTS.md` 说"每个页面配一个同名 `.test.tsx`"，新增页面必须遵守。

**测试：** 本身就是测试任务；出口是 `npm test && npm run build` 全绿 + 附录 B 脚本可照着跑通。

**验收标准：** §1.3 第 7 条。

**工作量：S。依赖：W-04 … W-07。**

---

## 5. DoD 检查清单

### 5.1 每个任务通用 DoD（继承 `AGENTS.md` §10 与 `web/AGENTS.md` 规则）

- [ ] 后端字段先落 `src/api/types.ts`，再写页面；
- [ ] 新增页面/组件有同名 `.test.tsx`；改了页面就改测试；
- [ ] 错误/空态走 `error-state` / `empty-state` / `status-tag`，不自造；
- [ ] 数据展示优先复用 `json-block` / `trace-panel` / `key-set-table`；
- [ ] `trace` 与 `reasons` 没有被弱化或省略；
- [ ] 新增依赖前确认 `components/ui/` 已有原子件不够用（D3 的结论是**不引** `react-markdown`）；
- [ ] 样式 token 走 `tailwind.config.ts`，组件里不写死颜色；
- [ ] W-01 另需满足 Go 侧：`gofmt` / `go vet` / `go test` 全绿，架构测试未放宽。

### 5.2 交付门（§1.3 的可勾选版本）

- [ ] 1 一轮完整对话可用（流式 + 至少一次 search 与一次 evidence 实时可见）
- [ ] 2 `[^n]` 与 `citation.evidence_ids` 一致，点击可见原文、餐厅名、doc_type、来源、快照时间
- [ ] 3 run 回放的工具序列与流式看到的顺序一致，含 `result_summary`
- [ ] 4 同线程连发「第二家安静吗」不丢上下文；刷新后能列出历史并继续
- [ ] 5 澄清/确认/记忆三类信号有可见入口且可操作（确认能发、记忆能改能删）
- [ ] 6 槽位探针能看到意图、硬/软分离、`source`、耗时
- [ ] 7 `npm test` + `npm run build` 全绿；后端 `make test/vet/build` 未被破坏

---

## 6. 风险与注意事项

| 风险 | 实测/依据 | 应对 |
| --- | --- | --- |
| **单轮 2–2.5 分钟** | 真实模型 + 真库实测 | 页面必须实时显示工具时间线；没有它，两分钟空白会被当成卡死。别做"转圈等待" |
| **必须先 `make migrate`** | 库停在 0006 时发消息 502 | 写进 §0.3 与附录 B 的第一步；页面遇到 502 时提示"库可能未迁移" |
| **Ollama 未起 → 向量通道降级** | 实测两条 warning：`向量通道不可用`、`evidence 退化成按文档顺序排序` | 这是官方支持的降级；`message.end.warnings` 必须在 UI 上显示，否则会把降级结果当正常结果 |
| **OpenRouter 偶发 EOF** | 实测有轮次无 `message.end`、候选不落库 | 页面对"流中断且无 end/error"要有兜底状态（显示"本轮未正常结束"），不要停在 running |
| **evidence scope 缺 `restaurant_profile`** | 库里只有 review_summary / attributes / representative_reviews / hours | 指定餐厅问答答不出地址、菜系、整体评分，一律进"无法确认"。这是数据缺口不是 bug，页面不要掩盖 |
| **无鉴权** | `GET /v1/conversations/:id` 不校验归属 | 页面顶部常驻显示 `X-User-ID`；不要把会话承诺成"私有" |
| **偶发：指代已解析成功仍进澄清** | 实测一次「第二家」被拦，且把内部槽位名 `restaurant_id` 念给用户 | W-06 的文案层要做槽位名映射兜底（这是前端能独立修的一层，不必等后端） |
| **候选表出现重复项** | 实测同一家店占两个 position | 候选卡片按 position 去重渲染；后端修之前不要静默丢数据 |

---

## 7. 与后续里程碑的衔接

- **M6-01 节点级 trace**：本文档的 `tool-timeline` 是它的挂载点，届时只需在时间线里
  插入新层级，不需要重做。前端对未知事件必须忽略（W-03 已强制），就是为这次升级留的口子。
- **M6 真实鉴权**：`X-User-ID` 会被替换成真正的 principal。本文档把它收敛在
  `chat.ts` 的模块级状态里，替换时只改一处。
- **M6 展示门（Gate E）**：本验证台是它的前置——Gate E 要求"能演示"，
  而演示的载体就是这个页面。本文档不新造 Gate。
- **留给人类的后续**：候选重复、澄清误触发、`restaurant_profile` 缺失，
  三项都记录在 §6，属于"接口 ready、质量待调"，按用户约定**不在本任务里修**。

---

## 附录 A：环境变量

| 变量 | 用途 | 备注 |
| --- | --- | --- |
| `VITE_API_BASE` | 直连后端时的绝对源（需后端 `HTTP_CORS_ALLOW_ORIGINS` 允许） | 开发留空，走 Vite 代理 |
| `VITE_BACKEND_TARGET` | 改代理目标地址 | 默认 `http://127.0.0.1:8080` |
| `HTTP_ADDR` | 后端监听地址 | 需 `127.0.0.1:8080`（admin 限制 loopback；对话面无此限制但保持一致） |
| `ADMIN_ENABLED` | 运维台数据（`/admin/v1`） | 验证台本身**不需要**它，只依赖 `/v1` |
| `RESERVATION_ENABLED` | 预约工具与确认闸门 | 默认 `false`；开启后 W-06 的确认 banner 才可能出现 |
| `CHAT_*` / `EMBEDDING_*` | 模型与向量通道 | 见 M5 文档附录 A |

前端侧的 `X-User-ID` 不是环境变量，是页面输入框 + `chat.ts` 的模块状态。

## 附录 B：演示与验收脚本

### B.1 环境（先跑一遍）

```bash
make pg-up && make migrate          # 必须到 0009
ollama serve &                      # 可选，软条件召回更完整
make run                            # chat-service 带 .env，:8080
curl -s http://127.0.0.1:8080/healthz
cd web && npm run dev               # :5173，已代理 /v1
```

### B.2 curl 基线（页面出问题时的对照）

```bash
# 1) 建线程
TID=$(curl -s -X POST http://127.0.0.1:8080/v1/conversations \
  -H 'Content-Type: application/json' -H 'X-User-ID: demo-user' \
  -d '{"title":"验收"}' | python3 -c 'import json,sys;print(json.load(sys.stdin)["thread_id"])')

# 2) 发一轮（SSE）
curl -sN -X POST "http://127.0.0.1:8080/v1/conversations/$TID/messages" \
  -H 'Content-Type: application/json' -H 'X-User-ID: demo-user' \
  -d '{"content":"布鲁克林 4星以上的拉面，要安静，推荐 3 家并说明依据"}'

# 3) 回放
curl -s "http://127.0.0.1:8080/v1/conversations/$TID/runs"
curl -s "http://127.0.0.1:8080/v1/runs/<run_id>"

# 4) 槽位探针
curl -s -X POST http://127.0.0.1:8080/v1/restaurants/interpret \
  -H 'Content-Type: application/json' \
  -d '{"text":"曼哈顿中城 4 星以上意大利菜，要安静适合约会"}'

# 5) 记忆
curl -s http://127.0.0.1:8080/v1/memories -H 'X-User-ID: demo-user'

# 6) W-01 新增（若已实施）
curl -s "http://127.0.0.1:8080/v1/conversations?limit=20" -H 'X-User-ID: demo-user'
curl -s "http://127.0.0.1:8080/v1/conversations/$TID/candidates"
curl -s -X POST http://127.0.0.1:8080/v1/restaurants/evidence \
  -H 'Content-Type: application/json' -d '{"evidence_ids":[123,456]}'
```

### B.3 页面验收步骤（对齐 §1.3 七条）

| # | 操作 | 预期 |
| --- | --- | --- |
| 1 | 打开 `/agent` → 新建会话 → 发「布鲁克林 4星以上的拉面，要安静，推荐 3 家并说明依据」 | 气泡逐字出现；右侧工具时间线出现 `search_restaurants`、`get_restaurant_evidence` 及耗时；底部状态条显示模型/耗时/token |
| 2 | 点回答里的 `[^1]` | 抽屉显示餐厅名、doc_type、原文、来源、快照时间 |
| 3 | 结束后切到「工具链」tab | run 列表有本轮；展开后工具序列与流式一致，多出 `result_summary` |
| 4 | 继续发「第二家安静吗？只说这一家」 | 不丢上下文；刷新页面后历史消息仍在，能继续发 |
| 5 | 触发一次澄清（问一家名字模糊的店）/ 记忆（说"记住我不吃辣"） | 澄清有明确提示且输入框可用；记忆面板能列出、改、删 |
| 6 | 切到「槽位探针」，输入第 B.2-4 条那句中文 | 硬条件 4 项、软条件 2 项分区显示，`source` 可见 |
| 7 | `npm test && npm run build` | 全绿 |

## 附录 C：SSE 事件 → UI 映射矩阵

| 事件 | 对话区 | 工具时间线 | 引用 | 闸门 | 状态条 |
| --- | --- | --- | --- | --- | --- |
| `message.start` | 开气泡 | 清空 | 清空 | — | 记 run_id |
| `message.delta` | 追加文本 | — | — | — | — |
| `tool.start` | — | 插入运行中 | — | — | — |
| `tool.finish` | — | 收尾 + 耗时 | — | — | — |
| `citation` | — | — | 加入集合 | — | — |
| `state.awaiting_input` | — | — | — | 澄清条 | — |
| `confirmation.required` | — | — | — | 确认/取消 | — |
| `memory.saved` | 插入提示 | — | — | — | — |
| `message.end` | 收尾气泡 | 拉回放补 summary | — | 保留 `awaiting_*` | usage + warnings |
| `error` | 标记失败 | 标记失败 | — | — | — |
| 心跳 comment | — | — | — | — | 忽略 |

## 附录 D：参考文档

- `docs/platepilot-m5-task-document.md` —— §2.3.4 SSE 事件、§2.3.5 路由、§7 M6 衔接
- `docs/platepilot-implementation-plan.md` —— §9 Gate E
- `docs/platepilot-technical-prd.md` —— §3.1–3.5、§7
- `web/AGENTS.md` —— 前端既有规则（类型即契约、每页一测试、复用组件）
- `chat-service/internal/httpapi/sse.go` —— 10 种事件与 payload 的唯一事实来源

## 附录 E：实施记录

> 开工后按任务追加偏差与发现，不回头改原计划（沿用 M5 文档的约定）。

执行日期：2026-10-04。顺序：W-01 → W-02 → W-03 → W-04 → W-05 → W-06 + W-07 → W-08。
D1/D2/D3 均按推荐执行（W-01 纳入；`/agent` 独立路由与分组；不引 `react-markdown`）。

### E.1 W-01 的实际形状（与 §4 的差异）

| 计划 | 实际 | 原因 |
| --- | --- | --- |
| `GET /v1/conversations?user_id=&limit=&before=` | `GET /v1/conversations?limit=&before_id=`，身份只从 `X-User-ID` 头取 | 身份走查询参数会让人以为"可以查别人的"。头是唯一来源，与记忆端点一致 |
| `GET /v1/conversations/:id/candidates` | 同计划 | `[{position, restaurant_id, name, score}]`，空数组不是 null |
| 证据反查二选一 | `evidence_ids` 与 `restaurant_ids` 同时给时**以 `restaurant_ids` 为准**，两者都缺仍报 `CodeRetrievalNoScope` | 沿用"显式范围优先"的既有规则；两者都给是调用方的错，不是新语义 |
| 新增仓储方法进 `shared/store/contract/` | `ListThreads` 进 contract；`FindEvidenceByIDs` 另加 `memory` 实现 + `postgres` 断言测试 | 证据反查是 `KnowledgeRepository` 的新方法，契约套件不覆盖 knowledge read |

按 id 取证据**仍过 `is_active` 与组装/去重**（`EvidenceByIDs` 复用 `resolveCitations` / `dedupeBySource`），
已下线文档不会被取回；`evidence_recall_test.go` 的 `TestEvidenceByIDsSkipsRetiredAndUnknownIds`
就是这条的守卫。

### E.2 目录结构偏差（§2.2）

| 计划路径 | 实际 | 说明 |
| --- | --- | --- |
| — | `components/agent/answer-text.tsx` | `[^n]` 上标与段落渲染被两个地方用到（历史气泡、流式气泡），单独成件 |
| — | `components/agent/candidate-cards.tsx` | 候选卡片是 §5 交付物 4，但不在 §2.2 的目录清单里 |
| `hooks/useThreads.ts`（线程列表 / 当前线程） | 再加 `hooks/useMemories.ts` | 记忆按用户作用域而非线程作用域；合在一起会让每次线程失效都重读记忆 |
| — | `lib/` 下未新增 `transcript.ts` | `withoutEchoedTurn` 与 `citableIds` 放在 `pages/AgentConsole.tsx` 里并导出供测试。它们只服务这一个页面 |
| 右侧栏"可折叠" | 固定 380px 的 tab 栏 | 折叠状态是第三个需要持久化的 UI 状态，而这一屏的价值在于同时看见；先不做 |

### E.3 计划里要求、但字段不存在的项

- **候选卡片的 `reasons`**（§4 W-05 交付物 4 与 §5.1）：`CandidateView` 的形状是
  `{position, restaurant_id, name, score}`，端点不返回 `reasons`。卡片上因此没有这一栏——
  这不是弱化，是接口里没有。后端加字段时挂到 `candidate-cards.tsx` 上。
- **`evidence scope 缺 restaurant_profile`**（§6）：属于数据缺口，页面按计划不掩盖，
  照常展示 `doc_type`。

### E.4 三个需要说明的实现选择

1. **进行中禁用输入框**（§2.3 给了"禁用"与"先 abort 前一个"两个选项）：选前者。
   `Composer` 在 `running` 时把 textarea 置灰并只留"停止"，所以"发送"永远不会静默取消上一轮。
2. **确认闸门的结果不另插气泡**：`confirm` 的 `message` 与 `state` 渲染在 `gate-banner` 内部
   （`replayed=true` 时显示"已经处理过（幂等）"）。理由是它紧挨着按钮，比插进历史里更好找；
   且刷新后它本来就该消失——它描述的是这次点击，不是这条会话。
3. **记忆面板的刷新用 token 而不是订阅**：`memory.saved` 事件只会让页面 bump 一个整数，
   面板据此 refetch。事件里的 `{memory_id, content}` 不足以拼出一行 `MemoryView`
   （缺 `created_at` / `confidence` / `source`），所以不直接写缓存。

### E.5 与 §1.3 退出条件的对照

| # | 条件 | 落点 |
| --- | --- | --- |
| 1 | 一轮完整对话 | `useAgentTurn` + `MessageList` + `ToolTimeline`（实时 start/finish 与耗时） |
| 2 | 引用可核对 | `answer-text` 上标 → `CitationDrawer`（按 `evidence_ids` 取原文、餐厅名、`doc_type`、来源、快照时间） |
| 3 | 工具链可回放 | 结束后 `GET /v1/runs/:run_id` 把 `result_summary` 合并回同一时间线（`mergeToolCalls`）；`RunReplayPanel` 另列历史 run |
| 4 | 多轮可续 | 历史消息按 `seq` 渲染；线程列表显示 `current_state`；刷新后 `GET /conversations` 仍在 |
| 5 | 闸门可操作 | `GateBanner`（澄清文案做槽位名映射）→ `POST confirm`；`MemoryPanel` 列 / 改 / 删 |
| 6 | 槽位可诊断 | `SlotProbeCard`（硬条件与软条件分区、`source`、`extract_latency_ms`） |
| 7 | 质量基线 | 见 E.6 |

### E.6 验证记录（2026-10-04）

- 前端：`npx tsc --noEmit` 无输出；`npm test` **19 个文件 96 个用例全绿**；
  `npm run build`（`tsc && vite build`）通过，产物 `dist/assets/index-*.js` 262 kB / gzip 79 kB。
- 后端：`shared` / `chat-service` / `data-pipeline` 三个模块 `go build` 与 `go vet` 均无输出。
  `chat-service` 的 `go test ./...` 只有一处失败：**`TestRetrievalFixtures`，且它在干净 HEAD 上
  同样失败**（用 `git worktree` 在 `ea706e7` 上复现过），是本改动之前就存在的评分门槛问题。
  `shared` 的 `store/postgres` 里 `TestBoroughQueryUsesTheBoroughPartialIndex` 会跑
  **335 秒**（对语料做 `EXPLAIN ANALYZE`），默认 4 分钟的包级超时会让它看起来像挂住——
  单独给足超时它是 PASS；这是环境与数据量的关系，不是本次改动。
  把 `shared` 的超时放到 900 秒后 `go test ./...` **全部 ok**（`store/postgres` 一个包 501 秒）。
- 新增前端测试文件 11 个：`lib/sse.test.ts`、`lib/answer.test.ts`、`hooks/useAgentTurn.test.ts`、
  `pages/AgentConsole.test.tsx`，以及 `components/agent/` 下的
  `tool-timeline / citation-drawer / run-replay-panel / candidate-cards / gate-banner /
  memory-panel / slot-probe-card` 各自的 `.test.tsx`。
- 新增 Go 测试：`httpapi/conversation_list_test.go`（5 条）、`httpapi/evidence_test.go` 增补
  4 条（`evidence_ids` 范围与越界）、`retrieval/evidence_by_id_test.go`、
  `store/postgres/evidence_recall_test.go` 增补 3 条、`store/contract/conversation.go` 增补
  `threads_list_scoped_to_owner`。

### E.7 按 §7 留给人类的后续（本次未做）

候选重复（前端已按 `position` 去重渲染，未静默丢数据）、澄清误触发（前端已做槽位名映射兜底，
后端判定未动）、`restaurant_profile` 缺失（数据缺口）。三项都不在本文档范围内。

### E.8 未验证的部分

本记录里的验证全部是**离线**的：前端测试 mock 掉 `fetch`，Go 测试用内存适配器与既有 postgres 夹具。
§1.3 第 1–6 条里"真实模型 + 真库 + 真 SSE"的那一遍需要按附录 B 手工走一次
（起库 → `make migrate` → 起服务 → `npm run dev`），本文档交付时未执行。
