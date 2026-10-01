# PlatePilot 实施计划

> 版本：v0.7  
> 日期：2026-09-30  
> 依据文档：`Docs/platepilot-technical-prd.md` v0.12  
> 项目定位：Go + Eino Agent + OpenAI-Compatible Chat Provider + 本地 Qwen Embedding + PostgreSQL（pgvector + PostGIS + pg_trgm）  
> 计划目标：把技术 PRD 拆成可独立执行、可验证、可并行推进的任务

## 1. 项目最终形态

第一阶段完成后，系统应具备：

1. 从 Google Local 2021 Meta 和 Review 数据生成可查询的餐厅知识库。
2. 使用 PostgreSQL（pgvector + PostGIS + pg_trgm）保存餐厅、评论、知识文档、向量、会话和 Agent 运行数据。
3. 使用本地 `qwen3-embedding:0.6b` 生成 1024 维向量。
4. 使用 pgvector 向量检索完成餐厅级语义召回和佐证召回。
5. 使用 Eino 编排 Agent 节点、工具和检查点。
6. 使用可配置的 OpenAI-Compatible Chat Provider 调用聊天和工具模型。
7. 支持中英文自然语言搜索、推荐、解释和追问。
8. 返回证据 ID、来源、快照时间和引用。
9. 预约只作为可选 Mock 工具，不影响 Agent/RAG 主链路。
10. 提供基本评测、运行 trace 和演示界面。

## 2. 当前起点

当前项目中已有：

- Google Local Meta 数据。
- Google Local Review 数据。
- 5 家餐厅 Demo JSON。
- Python 数据探索脚本。
- 技术 PRD。

当前还没有：

- Go 工程骨架。
- PostgreSQL 数据。
- 表结构和索引。
- Go 数据导入链路。
- Ollama Embedding Adapter。
- 文档构建和向量化链路。
- RAG 检索服务。
- Eino Agent。
- OpenAI-Compatible Chat Adapter。
- Agent 状态、记忆和工具系统。
- 评测、trace 和前端。

## 3. 架构边界

代码分为**两个可独立运行的进程 + 一份共享库**（详见 §3.2）：数据生产负责写入链路，聊天服务负责读取与对话链路。

```text
[chat-service] HTTP / SSE
  -> Eino Agent Runtime
       -> Tool Registry
       -> ChatProvider
            -> OpenAICompatibleAdapter
       -> EmbeddingProvider
            -> OllamaQwenAdapter
       -> Retrieval Service
            -> Structured Filter
            -> Restaurant Recall
            -> Evidence Recall
            -> Fusion / Rerank / Context Builder
       -> Memory / Checkpoint / Guardrails
  -> PostgreSQL
       -> restaurants
       -> reviews
       -> review_summaries
       -> knowledge_documents
       -> boundaries            # 行政区划几何，M2 用于地名检索
       -> conversations
       -> conversation_checkpoints
       -> agent_runs
       -> tool_calls
       -> user_memories
```

### 3.1 不可破坏的设计原则

- Chat API、Ollama、数据库驱动渠道类型不能进入领域层。
- ChatProvider 和 EmbeddingProvider 是两个独立接口。
- 餐厅召回和佐证召回必须分开。
- `knowledge_documents` 使用 `retrieval_scope` 区分 `restaurant` 和 `evidence`。
- 预约不是核心链路，默认排在 Agent/RAG 之后。
- 所有检索结果必须带来源和快照时间。
- Agent 状态必须持久化，不能只存在 Eino 内存中。
- 所有写工具必须可审计、可幂等。
- 两个服务（数据生产、聊天服务）互不 import，只通过 `shared/` 共享代码。

### 3.2 服务划分（两个可独立运行的进程）

仓库按**两个服务 + 一份共享库**组织：同一个 Go module，但可以独立构建、运行和部署，并预留前端位置。

```text
PlatePilot/
├── data-pipeline/      # 服务一：数据生产（批处理 CLI）
├── chat-service/       # 服务二：聊天服务（HTTP / SSE）
├── shared/             # 两个服务共享的领域、端口、适配器与工具
└── web/                # 预留前端项目
```

| 目录 | 形态 | 负责 | 主要里程碑 |
|---|---|---|---|
| `data-pipeline` | 批处理 CLI（`import` / `build-documents` / `embed`） | 写入链路：raw → curated → knowledge → embedding | M1、M2 |
| `chat-service` | 常驻 HTTP / SSE 服务 | 读取链路：检索 → 证据 → Agent → 回答 | M3、M4、M5 |
| `shared` | 库（无 `main`） | 领域 DTO、端口接口、适配器、配置原语、日志、测试工具 | 贯穿全部 |
| `web` | 前端（未来） | 聊天界面、候选卡片、引用与 trace 展示 | M6 |

补充原则：

- 写入（`data-pipeline`）与读取（`chat-service`）严格分离，通过 `shared/domain` 与存储层达成一致的语义。
- 共享库不依赖任一服务的 `internal/`；服务可以依赖共享库，反之不行。
- `shared/domain` 仍然遵守 §3.1 的纯净性约束：不依赖数据库驱动、Eino、Hertz 或厂商 SDK。
- 常见的可运行命令：`make run-chat`、`make run-pipeline`、`make build`。

## 4. 里程碑总览

| 里程碑 | 目标 | 主要产出 | 退出条件 |
|---|---|---|---|
| M0 | 工程基础 | 双服务 Go 骨架（data-pipeline / chat-service）、共享库、配置、接口、测试基线 | 两个服务可独立启动，接口和测试骨架完整 |
| M1 | PostgreSQL 数据底座 | 表结构、导入器、统计聚合 | Meta/Review 可稳定导入数据库 |
| M2 | Embedding 与文档 | Qwen Adapter、文档构建器、向量写入 | 5,000 家样本餐厅可向量检索 |
| M3 | 两级检索 | 餐厅召回、佐证召回、融合与引用 | 能返回候选餐厅和证据 |
| M4 | Agent 运行时 | Eino、Chat Provider、工具、状态、记忆 | Agent 能自主选择搜索和证据工具 |
| M5 | 纵向切片 | 搜索、推荐、问答、追问、可选预约 | 端到端演示链路可运行 |
| M6 | 评测与展示 | Trace、评测、性能、前端 | 可回放、可评分、可演示 |

## 5. 工作流划分

### Workstream A：Go 工程与领域边界

负责：

- Go Module。
- 配置和日志。
- DTO 和 Provider。
- Repository interface。
- 错误模型。
- 测试基线。

### Workstream B：数据写入 PostgreSQL

负责：

- 表结构设计。
- 索引设计。
- Meta 导入。
- Review 导入。
- 去重和归一化。
- 评论统计。
- 数据审计。

### Workstream C：Embedding 与知识文档

负责：

- Ollama Adapter。
- Embedding 版本管理。
- 餐厅级摘要文档。
- 佐证级文档。
- 批量向量化。
- pgvector 索引验证。

### Workstream D：检索与 RAG

负责：

- 结构化餐厅过滤。
- 全文检索（pg_trgm）。
- 餐厅级向量召回。
- 佐证向量召回。
- 混合融合。
- 引用和上下文组装。

### Workstream E：Agent 运行时

负责：

- Eino Graph。
- OpenAI-Compatible Chat Provider。
- 工具注册。
- 槽位抽取。
- 状态机。
- Checkpoint。
- 记忆。
- Guardrail。
- SSE。

### Workstream F：质量与展示

负责：

- Trace。
- RAG 评测。
- Agent 评测。
- 性能测试。
- Prompt/模型版本。
- UI 和演示脚本。

## 6. 详细任务拆分

工作量级定义：

- `S`：半天以内。
- `M`：约 1–2 天。
- `L`：约 3–5 天。
- `XL`：需要拆成更小任务后再执行。

### M0：工程基础

| ID | 任务 | 交付物 | 依赖 | 工作量 | 验收标准 |
|---|---|---|---|---|---|
| M0-01 | 初始化 Go 工程 | `go.mod`、目录结构、入口程序 | 无 | S | `go test ./...` 和本地启动通过 |
| M0-02 | 配置与密钥管理 | 环境变量、配置文件、启动校验 | M0-01 | S | 缺少关键配置时启动失败并给出明确错误 |
| M0-03 | 日志和错误模型 | `slog`、错误码、请求 ID | M0-01 | S | JSON 日志包含 trace/request ID |
| M0-04 | 领域 DTO | Chat、Tool、Search、Evidence、Memory DTO | M0-01 | M | 领域包不依赖数据库驱动、Eino、具体 Chat API、Ollama |
| M0-05 | Provider 接口 | ChatProvider、EmbeddingProvider、RerankProvider | M0-04 | M | Mock Provider 可用于测试 |
| M0-06 | Repository 接口 | Restaurant、Knowledge、Conversation、Memory、Run Repository | M0-04 | M | 接口可由 PostgreSQL 和内存实现 |
| M0-07 | Hertz HTTP/API 骨架 | Hertz Router、健康检查和统一错误响应 | M0-01 | S | `/healthz` 和统一错误响应可用 |
| M0-07A | Hertz 中间件与验证 | binding、validation、recovery、request ID、CORS | M0-07 | M | 非法请求返回统一错误，request ID 可贯穿 trace |
| M0-08 | 测试基线 | `go test`、接口 Mock、测试夹具 | M0-04 | M | 核心 DTO 和 Provider 有单元测试 |

### M1：PostgreSQL 数据底座

| ID | 任务 | 交付物 | 依赖 | 工作量 | 验收标准 |
|---|---|---|---|---|---|
| M1-01 | 数据库连接 | PostgreSQL 连接池、超时和健康检查 | M0-02 | S | 本地服务能连接数据库并执行 ping |
| M1-02 | 表结构定义 | `restaurants`、`reviews`、`review_summaries` | M1-01 | M | 迁移脚本可重复执行 |
| M1-03 | 基础索引 | 唯一索引、时间索引、复合索引 | M1-02 | M | 关键查询无全表扫描 |
| M1-04 | Meta 流式导入 | Go gzip JSONL Reader 和 batch writer | M1-02 | L | 可导入有限样本并输出批次统计 |
| M1-05 | Review 流式导入 | Review Reader、关联、批量写入 | M1-04 | L | 可导入样本评论并正确关联 restaurant_id |
| M1-06 | 清洗与归一化 | category、price、hours、state、MISC 转换 | M1-04 | L | 转换规则有单元测试和审计样本 |
| M1-07 | 去重和幂等 | `source_record_id`、唯一索引 `(restaurant_id, text_hash, rating, reviewed_at)`、upsert 规则 | M1-04, M1-05 | M | 重复执行同一批次不产生重复数据，原始 user_id 不进入 curated review |
| M1-08 | 评论统计聚合 | source/stored/text/embedded count、评分统计 | M1-05 | M | `restaurant.review_stats` 可重建和校验 |
| M1-09 | 数据审计报告 | 行数、拒绝数、缺失字段、分布统计 | M1-04, M1-05 | M | 每次导入生成可查询报告 |
| M1-10 | 精选餐厅集合 | knowledge_score、is_active_for_demo | M1-06, M1-08 | M | 能稳定选出 2,000–5,000 家餐厅 |
| M1-11 | 全文检索索引 | 名称、地址、类别和描述字段的 trigram GIN 索引 | M1-02, M1-03 | M | 名称和地址模糊查询可返回可解释结果 |

### M2：Embedding 与知识文档

| ID | 任务 | 交付物 | 依赖 | 工作量 | 验收标准 |
|---|---|---|---|---|---|
| M2-01 | Ollama Embedding Adapter | HTTP Client、批量 embedding、超时和重试 | M0-05 | M | 可调用 `qwen3-embedding:0.6b` 并返回 1024 维向量 |
| M2-02 | Embedding 元数据 | model_id、维度、版本、content_hash | M2-01 | S | 模型或维度变化时不会被静默复用 |
| M2-03 | 餐厅级文档构建 | `retrieval_scope=restaurant` 摘要文档 | M1-10, M0-04 | L | 每家餐厅只生成一个稳定餐厅级摘要 |
| M2-04 | 佐证级文档构建 | `retrieval_scope=evidence` 知识 chunk，规则摘要优先，LLM 摘要可选 | M1-08, M0-04 | L | 事实、属性、评论摘要和代表评论分别成 chunk，并记录生成版本 |
| M2-05 | 文档去重与版本 | content_hash、version、is_active | M2-03, M2-04 | M | 更新生成新版本，不覆盖旧证据 |
| M2-06 | 批量向量化 Worker | worker pool、batch、失败重试 | M2-01, M2-05 | L | 可对样本批次生成并写入向量 |
| M2-07 | 向量索引 | vector + 过滤字段的 HNSW 索引定义 | M1-01, M2-06 | M | 数据库可按 retrieval_scope 执行向量检索 |
| M2-08 | 向量质量检查 | 空向量、维度、NaN、重复检测 | M2-06 | S | 异常向量被拒绝并进入报告 |

### M3：两级检索

| ID | 任务 | 交付物 | 依赖 | 工作量 | 验收标准 |
|---|---|---|---|---|---|
| M3-01 | 结构化餐厅查询 | 菜系、价格、评分、距离、状态过滤 | M1-10 | M | 过滤条件可由 API 参数执行 |
| M3-02 | 名称和地址检索 | 全文检索查询和结果归一化 | M1-10, M1-11 | M | 支持名称和地址模糊匹配 |
| M3-03 | 餐厅级向量召回 | `retrieval_scope=restaurant` 查询 | M2-07 | M | 软条件可影响候选排序 |
| M3-04 | 佐证召回 | 按 restaurant_id 和问题召回 evidence | M2-07 | L | 不返回其他餐厅的 chunk |
| M3-05 | 混合融合 | 结构化、关键词、向量分数融合 | M3-01, M3-02, M3-03 | M | 分数和来源可解释 |
| M3-06 | Rerank 接口 | 可选的 RerankProvider 和 Mock 实现 | M0-05 | S | 无 Rerank 时系统仍可运行 |
| M3-07 | 证据组装 | evidence ID、来源、时间和去重 | M3-04 | M | 输出可直接交给 LLM 和引用 UI |
| M3-08 | 检索测试夹具 | 固定查询、预期餐厅和证据 | M3-01, M3-04 | M | 可运行 Recall@K 和引用精度测试 |

### M4：Agent 运行时

| ID | 任务 | 交付物 | 依赖 | 工作量 | 验收标准 |
|---|---|---|---|---|---|
| M4-01 | Eino Graph 骨架 | ingress、extract、route、tool、answer、finalize | M0-04, M0-05 | L | 使用 Mock Provider 可完成一轮简单对话 |
| M4-02 | OpenAI-Compatible Chat Adapter | Chat、Stream、Tools、JSON 输出 | M0-05 | L | 配置 base_url/key/model 后可完成普通回答和工具调用 |
| M4-03 | 模型能力表 | supports_tools、JSON、stream、context | M4-02 | M | 模型能力由配置声明，不支持的工具调用会被拒绝 |
| M4-04 | 工具注册表 | ToolSpec、JSON Schema、权限和超时 | M0-05, M4-01 | L | 工具参数非法时返回结构化错误 |
| M4-05 | 搜索工具 | `search_restaurants` | M3-05, M4-04 | M | Agent 可获得候选餐厅 ID |
| M4-06 | 证据工具 | `get_restaurant_evidence` | M3-07, M4-04 | M | Agent 可获取佐证和引用 |
| M4-07 | 状态与 Checkpoint | conversation、checkpoint Repository | M0-06, M1-01 | L | 重启后可恢复等待确认或继续执行 |
| M4-08 | 记忆服务 | user_memories 写入、读取、删除 | M0-06, M1-01 | M | 长期偏好可管理和删除 |
| M4-09 | 上下文管理 | 消息裁剪、证据预算、摘要 | M4-01, M4-07 | M | 超长上下文有确定裁剪策略 |
| M4-10 | Guardrail | 输入注入、工具权限、输出事实边界 | M4-01, M4-04 | L | 评论指令不能改变系统行为 |
| M4-11 | Hertz SSE 事件输出 | `hertz-contrib/sse`、文本、节点、工具、引用和错误事件 | M0-07, M4-01 | M | UI 可按序展示执行过程并支持客户端取消 |

### M5：纵向切片

| ID | 任务 | 交付物 | 依赖 | 工作量 | 验收标准 |
|---|---|---|---|---|---|
| M5-01 | 硬条件搜索切片 | 用户查询 -> 过滤 -> 餐厅列表 | M3-01, M0-07 | M | 返回至少 3 个符合硬条件的餐厅 |
| M5-02 | 软条件推荐切片 | 餐厅级向量召回 -> 排序解释 | M3-03, M3-05 | M | “安静、适合约会”可影响召回 |
| M5-03 | 指定餐厅问答切片 | 名称 -> 实体 -> 佐证 -> 回答 | M3-04, M3-07, M4-01 | M | 回答至少返回两条有效证据 |
| M5-04 | Agent 推荐解释切片 | Eino 调搜索和证据工具 | M4-02, M4-05, M4-06 | L | 工具调用链可回放且引用正确 |
| M5-05 | 多轮追问切片 | 保存候选和上下文后继续追问 | M4-07, M4-09 | M | 用户可追问“第二家安静吗” |
| M5-06 | 可选 Mock 预约切片 | 预约工具、确认中断、Mock 状态 | M4-04, M4-07 | L | 仅作为可选工具，不阻塞主链路 |

### M6：质量、评测与展示

| ID | 任务 | 交付物 | 依赖 | 工作量 | 验收标准 |
|---|---|---|---|---|---|
| M6-01 | Trace 与日志 | run、node、tool、retrieval、token | M4-01 | M | 任意失败可按 trace_id 定位 |
| M6-02 | RAG 数据集 | 50–100 个查询和证据标注 | M3-08 | M | 可计算 Recall、引用精度和 grounded rate |
| M6-03 | Agent 数据集 | 意图、期望工具、禁止工具、最终状态 | M4-10, M5-04 | M | 可计算工具选择和任务成功率 |
| M6-04 | 运行回放 | 固定请求重放和多模型比较 | M6-01, M6-03 | M | 相同输入可重复执行和比较 |
| M6-05 | 性能测试 | 搜索、向量、Agent 延迟 | M5-04 | M | 记录 p50/p95 和瓶颈 |
| M6-06 | 演示 UI | Chat、候选卡片、引用、trace | M4-11, M5-05 | L | 浏览器可完成完整演示 |
| M6-07 | README/运行手册 | 启动、配置、导入、评测、故障排查 | 全部核心任务 | M | 新环境可按文档启动 |

## 7. 关键路径

```text
M0-01
  -> M0-02
  -> M0-04
  -> M0-05
  -> M0-07
  -> M1-01
  -> M1-02/M1-11
  -> M1-04
  -> M1-05
  -> M1-08
  -> M2-01
  -> M2-03/M2-04
  -> M2-06
  -> M2-07
  -> M3-03/M3-04
  -> M3-05/M3-07
  -> M4-01
  -> M4-02
  -> M4-04
  -> M4-05/M4-06
  -> M5-04
  -> M6-01
  -> M6-02/M6-03
  -> M6-06
```

建议先完成的第一条最小链路：

```text
M0-01 -> M0-02 -> M0-07 -> M0-07A
  -> M1-01 -> M1-02 -> M1-04
  -> M3-01 -> M5-01
```

这条链路不依赖 Embedding 和 Agent 模型，能最早证明：

- Go 服务可启动。
- 数据库可连接。
- 数据可导入。
- 餐厅查询 API 可用。

## 8. 并行执行建议

以下任务可以在依赖允许后并行：

### Track 1：数据

- M1-04、M1-05、M1-06、M1-08。

### Track 2：Agent 基础

- M4-01、M4-02、M4-03、M4-04 可先用 Mock 检索服务开发。

### Track 3：Embedding

- M2-01、M2-03、M2-04 可先用少量样本餐厅开发。

### Track 4：前端和可观测性

- M6-01、M6-06 可在 API 稳定后提前搭建骨架。

不要同时推进太多大任务。单人项目建议每次保持：

- 1 个数据任务。
- 1 个 Agent/检索任务。
- 1 个测试或文档任务。

## 9. 阶段质量门

### Gate A：数据门

- 至少 1,000 家餐厅成功导入。
- 至少 100,000 条评论成功关联。
- 重复执行导入不产生重复数据。
- 数据审计报告可生成。

### Gate B：Embedding 门

- 至少 500 家餐厅生成餐厅级文档。
- 至少 5,000 条 evidence chunk 生成成功。
- 所有向量均为 1024 维。
- 向量检索能返回正确 scope。

### Gate C：检索门

- 硬条件过滤正确率 100%。
- 餐厅召回结果可解释。
- 佐证不会跨餐厅返回。
- 引用包含 source 和 observed_at。

### Gate D：Agent 门

- Agent 能区分搜索、证据和澄清。
- 工具参数全部通过 Schema 校验。
- 运行状态可保存和恢复。
- 评论中的恶意指令不能影响工具调用。

### Gate E：展示门

- 浏览器可完成搜索、追问、引用查看。
- 至少 50 个 RAG 问题可评分。
- 至少 20 个 Agent 场景可回放。
- 关键延迟和失败原因可见。

## 10. 每项任务的完成定义

每个任务都要满足：

- 代码已实现。
- 单元测试或集成测试已添加。
- 错误路径有明确错误码。
- 日志中包含 trace_id。
- 不泄露密钥和 PII。
- 不绕过领域接口直接调用厂商 API。
- 文档或注释说明关键设计。
- 通过 `go test ./...`。
- 通过 `go vet ./...` 和 `golangci-lint`。
- 相关验收标准可以实际演示。

## 11. 测试策略

### 单元测试

- 配置校验。
- DTO 转换。
- 价格和属性归一化。
- Review ID。
- 评论统计。
- 文档构建。
- 工具参数校验。
- 状态机。
- Checkpoint 序列化。
- Provider Adapter 请求和响应转换。

### 集成测试

- 表结构与索引。
- 导入批次幂等。
- 向量检索。
- 餐厅召回不混入 evidence。
- 佐证不跨餐厅。
- OpenAI-Compatible Adapter 使用 Mock Server，Hertz Client 负责出站请求。
- Ollama Adapter 使用固定测试响应。
- Agent 工具链和检查点恢复。

### RAG 评测

至少覆盖：

- 菜系。
- 价格。
- 地区。
- 适合约会。
- 安静。
- 服务体验。
- 菜品体验。
- 排队问题。
- 负面评论。
- 数据缺失。
- prompt injection。

### Agent 评测

至少覆盖：

- 简单搜索。
- 模糊餐厅名。
- 缺槽位澄清。
- 搜索后追问。
- 需要佐证的推荐。
- 工具超时。
- 模型输出非法 JSON。
- 记忆写入和删除。
- 禁止工具调用。
- 数据缺失时正确回答未知。

## 12. 风险与控制

| 风险 | 触发点 | 控制措施 |
|---|---|---|
| 索引配置错误 | 向量查询失败或结果异常 | 索引版本化、启动时验证、固定样本测试 |
| 本地数据库未启动 | 开发和演示不可用 | 超时、重试、健康检查、可替换 Repository |
| 模型工具调用不一致 | 远端模型或渠道更换后失败 | 能力表、冒烟测试、模型固定和回退 |
| Embedding 与文档不匹配 | 引用无法支持回答 | content_hash、版本号、source_record_ids |
| 评论噪声 | RAG 归纳错误 | 过滤短评、聚合摘要、代表性证据 |
| 写入量和磁盘占用 | 导入或索引超出本地磁盘 | 先跑样本批次，记录表/索引大小，分层决定全量数据范围 |
| Agent 状态丢失 | 重启后无法继续 | 数据库 checkpoint，Eino 内存不作为事实来源 |
| 上下文过长 | 延迟上升和成本失控 | evidence top-k、token 预算和摘要 |
| 过度投入预约 | 偏离 Agent 核心 | 预约保持 P2 和可选工具 |
| 任务过大 | 难以验证进度 | 以纵向切片为交付单位，不按纯层级拆任务 |

## 13. MVP 最小交付范围

MVP 必须完成：

- M0 全部。
- M1 的核心导入和查询。
- M2 的样本 Embedding 链路。
- M3 的两级召回。
- M4 的 Agent 核心。
- M5-01 至 M5-05。
- M6-01 至 M6-03。

MVP 可以延后：

- Mock 预约。
- Rerank 模型。
- 复杂 UI。
- 全量评论向量化。
- 高级长期记忆。
- 多模型自动路由。

## 14. 推荐执行顺序

### 第 1 组：可运行底座

1. M0-01
2. M0-02
3. M0-03
4. M0-04
5. M0-05
6. M1-01
7. M1-02

### 第 2 组：第一条纵向切片

1. M1-04
2. M3-01
3. M0-07
4. M5-01

### 第 3 组：RAG 写入

1. M1-05
2. M1-08
3. M2-01
4. M2-03
5. M2-04
6. M2-06
7. M2-07

### 第 4 组：RAG 读取

1. M3-03
2. M3-04
3. M3-05
4. M3-07
5. M5-02
6. M5-03

### 第 5 组：Agent 化

1. M4-01
2. M4-02
3. M4-04
4. M4-05
5. M4-06
6. M5-04
7. M4-07
8. M5-05

### 第 6 组：质量与展示

1. M6-01
2. M6-02
3. M6-03
4. M6-05
5. M6-06

## 15. 里程碑验收清单

### 第一个可演示版本

- 用户可以输入自然语言。
- Agent 至少调用一次餐厅搜索和一次佐证检索。
- 返回 1–5 个候选餐厅。
- 每个推荐有来源和快照时间。
- 可以继续追问。
- 运行 trace 可查看工具和检索过程。

### 完整练手版本

- 2,000–5,000 家餐厅进入知识库。
- 10 万–30 万个 evidence chunk 可检索。
- 中文和英文查询均可工作。
- Chat Provider 可以通过 base_url、key 和 model 配置替换。
- 本地 Qwen Embedding 独立于聊天模型。
- RAG 和 Agent 均有评测集。
- 浏览器端可以完整演示。
- Mock 预约是可选项，不是系统启动前置条件。

## 16. 下一步

最推荐的开工任务是：

1. M0-01：初始化 Go 工程。
2. M0-04：定义领域 DTO。
3. M0-05：定义 Provider 接口。
4. M1-01：连接 PostgreSQL。
5. M1-02：建立第一批表和索引。

完成这五项后，就可以开始第一条端到端纵向切片，而不是先编写大量孤立模块。

## 17. 尚未锁定的决策

以下事项不阻塞 M0/M1，但应在对应任务开始前明确：

1. **远端 Chat 模型**：确定首个支持 Tool Calling 和 JSON Schema 的模型 ID。
2. **本地数据库规格**：确认向量数量、索引大小和磁盘预算。
3. **评论存储范围**：完整保存评论，还是只保存精选评论和摘要。
4. **评论摘要方式**：规则统计优先，还是由远端模型生成主题摘要。
5. **Rerank 策略**：第一版使用分数融合，还是接入独立 Rerank Provider。
6. **中文检索方案**：`pg_trgm` 够用，还是需要引入专用分词器。
7. **Mock 预约优先级**：确认是否放在完整 Agent/RAG 演示之后。
