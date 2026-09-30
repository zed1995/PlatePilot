# PlatePilot M2 Embedding 与知识文档任务文档

> 日期：2026-09-30
> 依据：`plans/platepilot-implementation-plan.md`（§4 里程碑总览、§6 M2、§7 关键路径、§9 Gate B、§10 完成定义）与 `plans/platepilot-technical-prd.md` v0.12（§4.7 `knowledge_documents`、§5 Step 7–9）
> 里程碑目标：把 M1 已落库的 36,133 家餐厅与 4,156,055 条评论，变成**可向量化、可按 scope 检索、可审计、可增量重建**的知识文档层
> 退出条件（Gate B）：≥500 家餐厅生成餐厅级文档；≥5,000 条 evidence chunk 生成成功；所有向量均为 1024 维；向量检索能返回正确 scope

> 存储层沿用自建 PostgreSQL（pgvector + PostGIS + pg_trgm）。PRD 中的 Atlas Vector Search 语义，在本项目中由 pgvector HNSW + partial index 实现，语义等价、运维更简单。

## 0. 如何使用本文档

- 本文档是 M2 的**可执行任务清单**，每个任务独立成节，包含目标、交付物、实现要点、依赖、工作量和可验证的验收标准。
- 任务粒度对齐实施计划的 M2 表格（M2-01 ~ M2-08）。任务 ID 与实施计划保持一致，便于交叉引用。
- 每个任务的**完成定义（DoD）**默认继承实施计划 §10：代码实现 + 测试 + 错误码 + `trace_id` 日志 + 不泄露密钥/PII + 不经领域接口直连厂商 API + 关键设计有注释 + 通过 `go test ./...`、`go vet ./...`、`golangci-lint` + 验收标准可实际演示。
- 本文档中的 Go 类型、接口签名和表结构是**约定形状**，允许在不破坏领域边界的前提下微调；一旦调整，必须同步更新本文件、`port` 接口和所有实现（含内存实现与契约测试）。
- 模块路径沿用 M0 约定：`github.com/zed/platepilot`。
- 服务边界沿用 M1：M2 **全部**落在 `data-pipeline` 与 `shared/adapter`，**不得**在 `chat-service` 中写库；`data-pipeline` 与 `chat-service` 互不 import。

### 0.1 起点状态（2026-09-30，M1 已交付）

M2 直接建立在 M1 的写入链路之上。启动 M2 前，以下产物已就绪，**M2 不重复实现，只做增量**：

| 起点产物 | 位置 | M2 如何使用 |
|---|---|---|
| `knowledge_documents` 表结构 | `shared/adapter/repository/postgres/migrations/0001_init.sql:217` | **已建好**，含 `vector(1024)`、`content_hash`、`version`、`is_active`、`borough` 反规范化列 |
| 向量索引定义 | `0001_init.sql:277-295` | **已建好**：1 个全局 HNSW + 5 个 borough partial HNSW，`vector_cosine_ops` |
| `review_summaries` 表 | `0001_init.sql:196` | **已建好**，主键 `(restaurant_id, topic)`，M2-04 首次写入 |
| `EmbeddingProvider` 端口 | `shared/port/embedding.go` | M0 已定义，M2-01 实现，**签名不变** |
| `evidence` 领域 DTO | `shared/domain/evidence/evidence.go` | M0 已定义 `KnowledgeDocument` / `Evidence` / 5 种 `DocType`，M2-03/04 只填内容 |
| `EmbeddingConfig` | `shared/config/config.go:100` | `EMBEDDING_PROVIDER` / `OLLAMA_BASE_URL` / `EMBEDDING_MODEL` / `EMBEDDING_DIMENSIONS` |
| Ollama adapter 占位 | `shared/adapter/embedding/ollama/doc.go` | 一个 `TODO(M2-01)`，是 M2 的起点 |
| CLI 子命令占位 | `data-pipeline/main.go:135,137` | `build-documents` / `embed` 已注册，返回 `notImplemented` |
| `reviews.is_representative` | `migrations/0001_init.sql` + `postgres/review.go` | 列与偏索引已建，**当前恒为 false**，M2-04 负责置位 |
| `ListByRestaurant` | `shared/port/writer.go` | M2-04 取候选评论 |

**数据现状（供容量估算）**：

| 指标 | 数值 |
|---|---|
| `restaurants` 总数 | 36,133 |
| `is_active_for_demo = true` | 3,000 |
| `reviews` 总数 | 4,156,055 |
| `restaurants.representative_review_count` | 恒为 0（M1 遗留缺口，M2-04 补齐） |
| `restaurants.embedded_review_count` | 恒为 0（M1 遗留缺口，M2-06 补齐） |
| 库体积 | 2.9 GB |

**M2 必须补齐的 M1 遗留缺口**（`M1` 文档 §1.2 第 5 条已记录）：

1. `representative_review_count` —— 依赖 M2-04 的代表评论选择规则。
2. `embedded_review_count` —— 依赖 M2-06 的实际 embedding 数。
   这两列在 M1 恒为 0，**不是缺陷而是里程碑边界**。注意 `review_stats` 只是 `restaurant.ReviewStats` 这个
**领域 DTO** 的名字（`shared/domain/restaurant/restaurant.go:84`），落到 `restaurants` 表上时它被拍平成
7 个普通列（`0001_init.sql:64-71`），**没有** `review_stats` 这个 jsonb 列。`import --stage=stats` 会通过
`AggregateStats` 重算，`representative_review_count` 随 `is_representative` 置位自动生效；
`embedded_review_count` 需要 M2-06 显式回写后重跑 `--stage=stats` 校验。

**几处必须知道的 schema / 语义约束**（不了解会写出错误的实现）：

1. **`knowledge_documents` 有 `CHECK (is_active = false OR embedding IS NOT NULL)`**。
   活跃文档必须带向量。这排除了"先写文本、后补向量"的半成品状态：文档要么在未激活版本里待命，
   要么向量已经就位。
2. **`vector(1024)` 是表级定长**。维度不符不是运行时错误，而是 pgx 写入时的序列化失败——
   必须在写入前校验（M2-08），否则错误信息会指向 pgx 而不是模型配置。
3. **`content` 是 `text`，不是 `varchar`**。长描述 + 多条评论拼成的 chunk 可能远超常见长度上限，
   写入时不要截断到未知上限。
4. **`borough` 是反规范化的冗余列**，必须与 `restaurants.borough_guess` 保持一致，
   否则 partial HNSW 选错索引（M1-03 已实测该机制）。
5. **`source_record_ids` 是 `text[] NOT NULL DEFAULT '{}'`**，多值评论 chunk 必须一次性传入数组，
   不能循环 append（这是 M1-07 踩过的 `VALUES` 列表上限坑的同源问题）。
6. **`version` + `is_active` 是两列**，不是一列。含义：新版本插入时先把旧版本 `is_active=false`，
   **再**插入新版本 `is_active=true`。顺序反了会出现"同 scope 无活跃文档"的检索黑洞。
7. **`reviews.is_representative` 当前恒为 false**。M2-04 选出的代表评论需要回写该列，
   否则 M1 的 `{restaurant_id, is_representative, rating}` 偏索引形同虚设，
   `representative_review_count` 永远算不对。

### 0.2 工作量级定义

| 级别 | 含义 |
|---|---|
| S | 半天以内 |
| M | 约 1–2 天 |
| L | 约 3–5 天 |

### 0.3 前置条件与起点（M0/M1 已交付）

| 前置 | 检查方式 | 不满足时的后果 |
|---|---|---|
| Ollama 已安装并已 pull `qwen3-embedding:0.6b` | `ollama list` 能看到该模型 | M2-01 无法验证，M2-06 全部阻塞 |
| Ollama 服务在跑 | `curl localhost:11434/api/tags` 返回 200 | 同上 |
| `restaurants` 有数据 | `SELECT count(*) FROM restaurants` > 0 | M2-03 产出 0 文档，Gate B 无法验证 |
| `is_active_for_demo` 有数据 | 计数 ∈ [2000, 5000] | 同上 |
| `reviews` 已关联到餐厅 | 孤儿 review 数 = 0 | M2-04 无法产出 evidence chunk |
| pgvector 扩展可用 | `SELECT extname FROM pg_extension WHERE extname='vector'` | M2-07 索引无法创建 |

> **本地模型不做降级**。M2 没有 mock embedding 兜底路径：`EmbeddingProvider` 有且只有一个
> 生产实现（Ollama）。单测用 fake provider，Gate B 用真实模型。

---

## 1. M2 目标与退出条件

### 1.1 里程碑目标

M2 只解决"知识文档与向量层"，不承载检索排序与 Agent 逻辑；它要交付一条**可反复运行的文档生产链路**：

1. `EmbeddingProvider` 的 Ollama 实现：批量 embedding、超时、重试、维度自检。
2. Embedding 元数据（`model_id` / `dimensions` / `content_hash` / `version`）随文档一起落库，模型切换不被静默复用。
3. 每家精选餐厅生成**恰好一个** `retrieval_scope=restaurant` 摘要文档。
4. 评论侧生成 `retrieval_scope=evidence` chunk：事实、属性、评论摘要、代表评论分别成 chunk，每条记录生成版本。
5. 文档版本化：内容变化产生新版本，**不覆盖**旧证据。
6. 批量向量化 worker：并发、批处理、失败重试、幂等重跑。
7. 向量索引可用于按 `retrieval_scope` 执行的向量检索。
8. 向量质量检查：空向量、维度不符、NaN、重复向量被拒绝并进入报告。
9. 补齐 `representative_review_count` 与 `embedded_review_count` 两个计数。

### 1.2 退出条件（Milestone Exit Criteria / Gate B）

实施计划 §9 Gate B 的 4 条，逐条对应到可执行的验证：

| # | 退出条件 | 验证方式 |
|---|---|---|
| 1 | 至少 500 家餐厅生成餐厅级文档 | `SELECT count(DISTINCT restaurant_id) FROM knowledge_documents WHERE retrieval_scope='restaurant' AND is_active` ≥ 500 |
| 2 | 至少 5,000 条 evidence chunk 生成成功 | `SELECT count(*) FROM knowledge_documents WHERE retrieval_scope='evidence' AND is_active` ≥ 5000 |
| 3 | 所有向量均为 1024 维 | `SELECT count(*) FROM knowledge_documents WHERE embedding IS NOT NULL AND vector_dims(embedding) <> 1024` = 0；表级 `vector(1024)` 保证物理成立，此查询是语义复核 |
| 4 | 向量检索能返回正确 scope | `ORDER BY embedding <=> $1` + `WHERE retrieval_scope='evidence'` 的结果 100% 为 evidence scope；restaurant scope 同理（详见 M2-07） |

**扩充分项门**（本文档补充，实施计划未列，但对 M3 至关重要）：

| # | 条件 | 验证方式 |
|---|---|---|
| 5 | `retrieval_scope='restaurant'` 每家餐厅恰好 1 个活跃文档 | 分组 `HAVING count(*) <> 1` 为空 |
| 6 | 文档重跑幂等 | 连续两次 `build-documents`，行数与 `content_hash` 分布不变 |
| 7 | 模型切换不被静默复用 | 改 `EMBEDDING_MODEL` 后重跑 `embed`，旧向量被标记失效而非与新模型混存 |
| 8 | 无异常向量 | 空向量 / NaN / Inf / 全零向量计数为 0（M2-08 报告可查；库侧兜底见附录 C 查询 8） |
| 9 | borough partial HNSW 生效 | 带 `borough` 过滤的查询 `EXPLAIN` 命中 `knowledge_documents_hnsw_<borough>`，非 Seq Scan |
| 10 | `representative_review_count` / `embedded_review_count` 有真实值 | 两列 > 0，且与 `knowledge_documents` 实际计数一致 |

---

## 2. 目标数据形态

### 2.1 分层与依赖方向

```text
raw -> curated -> knowledge -> embedding -> retrieval(M3)

restaurants ──> restaurant-level doc ──┐
reviews ─────> evidence-level doc ────┼──> knowledge_documents ──> embedding ──> vector search
review_summaries ─> topic summary ────┘         (M2-03/04)            (M2-06)
```

依赖方向不可逆：

- `data-pipeline` 读 `restaurants` / `reviews` / `review_summaries`，写 `knowledge_documents`。
- `chat-service` 在 M3 **只读** `knowledge_documents`，不产生也不修改文档。
- `EmbeddingProvider` 是 `shared/port` 端口，Ollama 只是它的一个实现。领域层不得出现 ollama 类型。

### 2.2 目标目录结构（M2 结束时）

在 M1 结构上的**增量**（不重列 M1 已有文件）：

```text
data-pipeline/internal/pipeline/
├── documents.go                   # build-documents 子命令：参数解析、编排、进度
├── embed.go                       # embed 子命令：参数解析、编排、进度
├── knowledge/                     # 文档构建（纯函数，无 IO）
│   ├── hash.go                    # content_hash = sha256(归一化 content)
│   ├── profile.go                 # M2-03 餐厅级文档
│   ├── facts.go                   # M2-04 事实 chunk（基本信息）
│   ├── attributes.go              # M2-04 属性 chunk
│   ├── summary.go                 # M2-04 评论摘要 chunk（规则版）
│   ├── representative.go          # M2-04 代表评论选择 + chunk
│   ├── version.go                 # M2-05 版本递增与旧版本失效
│   └── *_test.go
├── embedding/                     # 向量化（纯函数 + IO 编排）
│   ├── quality.go                 # M2-08 向量质量检查（空/维度/NaN/重复）
│   └── quality_test.go
└── ...

shared/
├── adapter/
│   ├── embedding/
│   │   ├── ollama/
│   │   │   ├── doc.go             # 删除 TODO，替换为真实实现
│   │   │   ├── client.go          # HTTP Client、超时、重试
│   │   │   └── client_test.go     # httptest 假服务
│   │   └── fake/
│   │       └── fake.go            # 单测用确定性 provider（无网络）
│   └── repository/
│       ├── postgres/
│       │   ├── knowledge.go       # KnowledgeStore：文档 upsert / 版本 / 向量写入 / 查询
│       │   └── contract_test.go   # 追加 knowledge 相关契约断言
│       ├── memory/
│       │   └── knowledge_store.go # 新增：写侧 KnowledgeStore 内存实现，跑同一套契约
│       │       knowledge.go       # 已存在：读侧 KnowledgeRepository（M0），不在本任务范围
│       └── contract/
│           └── contract.go        # 追加 KnowledgeStore 契约套件
└── port/
    └── writer.go                  # 追加 KnowledgeStore 端口
```

#### 2.2.1 两个 knowledge 的区别（最容易搞混的一处）

仓库里已有一个 `memory.KnowledgeRepository`，它和 M2 要写的 `KnowledgeStore` **不是同一个东西**：

| | 读侧 `KnowledgeRepository`（M0 已有） | 写侧 `KnowledgeStore`（M2 新增） |
|---|---|---|
| 端口位置 | `shared/port/repository.go:22`（读端口） | `shared/port/writer.go`（写端口，紧邻 `PipelineStore`） |
| 已有实现 | `memory/knowledge.go`（147 行，已实现）+ `shared/testkit` 已导出 | 无，M2 从零写 |
| 面向 | chat-service 的 M3 读路径 | data-pipeline 的 M2 写路径 |
| 契约套件 | `memory/knowledge_test.go`（已有） | `contract/contract.go` 需追加（当前只有 Restaurant/Review/Pipeline 三个套件） |

**顺带修一个已存在的签名 bug**：`port.KnowledgeRepository.FindEvidenceByRestaurant`
声明为 `restaurantID string`，而 `memory.KnowledgeRepository` 实现的是 `int64`
（`memory/knowledge.go:40`），**两者不构成接口实现关系**。它现在能编译是因为
`chat-service/internal/app/app.go:26` 只是把 `deps.Knowledge` 声明为接口字段、从未赋值。
M2 写真实 Postgres 实现时若沿用 `string`，会把这个不一致固化下来。
建议 M2 在实现 Postgres 读侧前，先决定统一到 `int64`（与
`knowledge_documents.restaurant_id` 的 `bigint` 一致），并补一条
`var _ port.KnowledgeRepository = (*memory.KnowledgeRepository)(nil)` 断言，
否则这个洞会一直藏着。这属于 M0 遗留，M2 顺手带上，成本一行。

### 2.3 文档类型与 chunk 划分

`evidence.DocType` 在 M0 已定义 5 种，M2 必须全部产出：

| DocType | scope | 内容来源 | 生成规则 | 工作量归属 |
|---|---|---|---|---|
| `restaurant_profile` | restaurant | `restaurants` 全字段 | 每餐厅**恰好一条**，稳定 | M2-03 |
| `restaurant_attributes` | evidence | `restaurants.attributes` | 只在属性非空时生成 | M2-04 |
| `restaurant_hours` | evidence | `restaurants.hours` | 只在营业时间非空时生成 | M2-04 |
| `restaurant_review_summary` | evidence | `review_summaries` + `review_stats` | 每主题一条 | M2-04 |
| `restaurant_representative_reviews` | evidence | `reviews`（代表评论） | 每餐厅**至多一条**（打包选中评论） | M2-04 |

**为什么代表评论打包成一条而不是每评论一条 chunk**：

- 3,000 家餐厅 × 10–30 条评论 = 3 万–9 万条 chunk。逐条成 chunk 会让 evidence 召回
  在"同一餐厅的 30 条评论"之间互相竞争 topK，挤掉真正相关的主题 chunk。
- 打包后每餐厅 1 条，`restaurant_review_summary` 与 `representative_reviews` 形成"结论 + 引证"对，
  M3-04 召回时可解释、可去重。
- 代价：单条 chunk 文本更长，向量语义被稀释。**这是权衡，不是免费**——若 M3/M6 评测显示
  长 chunk 召回质量差，改为"每餐厅 N 条、按主题分片"，届时以 `metadata.topic` 作为
  二级分组键。**本决策在 M3-08 评测后复核，不在 M2 提前优化。**

**每篇文档只描述一家餐厅的一个主题**（PRD §5 Step 7 硬要求）。餐厅级 profile 是唯一的
"多主题"文档，因为它按定义就是这家餐厅的整体画像；即便如此也不得混入评论观点——
评论观点只能出现在 evidence scope。

**不得把"评论观点"写成"官方事实"**：

- `restaurant_profile` 的事实字段来自 `restaurants` 表（来源 `google_local_2021`）。
- 评论相关的任何表述必须落在 evidence scope，且 `metadata.generated_by` 标明来源是评论。
- 建议在 profile 文本末尾加固定来源行：`Source: Google Local (2021 snapshot).`
  快照状态必须写明是 2021 快照，不得表述为实时营业状态（对齐 `restaurant.SnapshotStatus` 的语义）。

---

## 3. 任务清单总览

| ID | 任务 | 交付物 | 依赖 | 工作量 | 验收摘要 |
|---|---|---|---|---|---|
| M2-01 | Ollama Embedding Adapter | HTTP Client、批量 embedding、超时和重试 | M0-05 | M | 可调用 `qwen3-embedding:0.6b` 并返回 1024 维向量 |
| M2-02 | Embedding 元数据 | `model_id`、维度、版本、`content_hash` | M2-01 | S | 模型或维度变化时不会被静默复用 |
| M2-03 | 餐厅级文档构建 | `retrieval_scope=restaurant` 摘要文档 | M1-10, M0-04 | L | 每家餐厅只生成一个稳定餐厅级摘要 |
| M2-04 | 佐证级文档构建 | `retrieval_scope=evidence` 知识 chunk | M1-08, M0-04 | L | 事实、属性、评论摘要和代表评论分别成 chunk，并记录生成版本 |
| M2-05 | 文档去重与版本 | `content_hash`、`version`、`is_active` | M2-03, M2-04 | M | 更新生成新版本，不覆盖旧证据 |
| M2-06 | 批量向量化 Worker | worker pool、batch、失败重试 | M2-01, M2-05 | L | 可对样本批次生成并写入向量 |
| M2-07 | 向量索引 | vector + 过滤字段的 HNSW 索引定义 | M1-01, M2-06 | M | 数据库可按 `retrieval_scope` 执行向量检索 |
| M2-08 | 向量质量检查 | 空向量、维度、NaN、重复检测 | M2-06 | S | 异常向量被拒绝并进入报告 |

### 3.1 依赖图

```text
M2-01 (Ollama Embedding Adapter)
  └─> M2-02 (Embedding 元数据) ─┐
                                 ├─> M2-06 (批量向量化 Worker) ─┬─> M2-07 (向量索引)
M1-10 (精选餐厅集合) ─┐          │                              └─> M2-08 (向量质量检查)
                      ├─> M2-03 (餐厅级文档) ─┐                │
M1-08 (评论统计聚合) ─┼─> M2-04 (佐证级文档) ─┼─> M2-05 (文档去重与版本)
                      │                      │
                      └──────────────────────┴─> M2-06
```

### 3.2 推荐执行顺序

1. M2-01 Ollama Embedding Adapter（先确认本地模型可用，避免 M2-06 才发现模型拉不下来）
2. M2-02 Embedding 元数据（hash 与版本口径，越早定越好，M2-03/04 都依赖）
3. M2-03 餐厅级文档构建（最简单的端到端闭环，产出可验证的文档）
4. M2-04 佐证级文档构建（工作量最大，且依赖 `review_summaries` 规则摘要）
5. M2-05 文档去重与版本
6. M2-06 批量向量化 Worker
7. M2-07 向量索引
8. M2-08 向量质量检查

> **最小可演示链路**：`M2-01 → M2-02 → M2-03 → M2-05 → M2-06 --limit=50 → M2-07`。
> 完成后即可验证"本地模型能出 1024 维向量、50 家餐厅有活跃文档、向量检索能按 scope 返回结果"。
> 这是 M3-03 的前置，M2-04 可以后补——餐厅级召回不依赖 evidence chunk。
>
> **先用少量样本开发**（实施计划 §8 Track 3）：
> M2-01/M2-03/M2-04 都可以先在 `--limit=100` 的样本餐厅上开发通过，再跑全量 3,000 家。

---

## 4. 详细任务

### M2-01 Ollama Embedding Adapter

**目标**：实现 `port.EmbeddingProvider` 的本地 Ollama 实现，调用 `qwen3-embedding:0.6b` 返回 1024 维向量。

**交付物**

- `shared/adapter/embedding/ollama/client.go`：
  ```go
  // New builds a provider from config. It validates the base URL and refuses a
  // zero or negative dimension count, because a mismatch only surfaces much
  // later as an opaque pgx serialization error.
  func New(cfg config.EmbeddingConfig) (*Client, error)

  func (c *Client) ModelID() string
  func (c *Client) Dimensions() int
  func (c *Client) EmbedDocuments(ctx context.Context, docs []string) ([][]float32, error)
  func (c *Client) EmbedQuery(ctx context.Context, query string) ([]float32, error)
  ```
- `shared/adapter/embedding/fake/fake.go`：确定性 provider，`f32(i, j) = sin(float32(i*1024+j)) / 2`，用于单测。
- `client_test.go`：`httptest` 假服务，覆盖正常返回、维度不符、超时、5xx 重试、空输入。
- **`shared/domain/errs/errs.go`：追加 embedding 相关错误码**（这是硬性交付物）。
  当前只有 8 个码：`invalid_argument` / `unauthorized` / `not_found` / `conflict` /
  `validation_failed` / `provider_timeout` / `provider_unavailable` / `internal`。
  按该文件自身的约定——"New codes are appended; existing values must never change meaning"——
  追加是合法的，但**必须同时补齐 `httpStatusByCode` 映射与 `Err*` 哨兵**，否则
  `HTTPStatusOf` 会静默回落到 500。分流原则：

  | 场景 | 使用的码 | 理由 |
  |---|---|---|
  | provider 超时 | 复用 `CodeProviderTimeout` | 语义完全一致，无需新码 |
  | provider 持续 5xx / 连不上 | 复用 `CodeProviderUnavailable` | 同上 |
  | 向量维度不符 | 新增 `CodeEmbeddingDimensionMismatch` → 400 | 是数据契约错误，不是可用性错误 |
  | 向量含 NaN / Inf | 新增 `CodeEmbeddingNaN` / `CodeEmbeddingInf` → 422 | 上游算出了非法值 |
  | 全零向量 | 新增 `CodeEmbeddingZeroVector` → 422 | 上游算出了无意义的值 |
  | 空向量 | 新增 `CodeEmbeddingEmpty` → 400 | |
  | 模型与库中不匹配 | 新增 `CodeEmbeddingModelMismatch` → 409 | 是状态冲突，需要 `--force-model-change` 解开 |
  | 重复向量（精确指纹相同） | 新增 `CodeEmbeddingDuplicate` → 422 | |

  重复/相似向量的区分见 M2-08。**这些码只在 data-pipeline 内部使用，不出现在 HTTP 响应里**，
  但仍按同一套约定维护，避免将来 M3 复用时不一致。

**实现要点**

- Ollama 的 embedding 接口是 `POST /api/embed`（**不是** `ollama/doc.go` 占位注释里写的
  `/api/embeddings`——那是 Ollama 早期的单条接口，只接受 `"prompt"` 且不支持批量，
  新版本已标记废弃）。请求体 `{"model": ..., "input": [...]}`，
  返回 `{"embeddings": [[...], ...]}`。**批量优于单条**：`input` 传数组，服务端并行处理，
  比 N 次 HTTP 往返快一个数量级。
- **超时与重试要分开**：
  - 超时用 `context.WithTimeout` 包裹每次请求，**并把 `context.DeadlineExceeded` 与 `ctx` 自身的取消区分开**
    —— 前者可重试，后者说明是调用方主动放弃，直接返回。
  - 重试只针对 `429` / `5xx` / 网络瞬时错误，指数退避（1s / 2s / 4s），最多 3 次。
  - **不要重试 4xx**（除 429）：`404` 通常意味着模型没 pull，重试只是浪费时间。
- **空输入要短路**：`EmbedDocuments(ctx, nil)` 返回空切片而非报错，
  否则 worker pool 的空批次会变成一次无谓的 HTTP 请求。
- **维度自检**：拿到响应后立刻断言 `len(vec) == c.dimensions`，
  不符则返回 `errs.CodeEmbeddingDimensionMismatch` 并**带上实际维度**（见下方错误码交付物）。
  这是 M2-08 质量检查的第一道防线。
- **NaN / Inf 检查在 JSON 传输下不可达，但仍要保留**。实施时验证到：
  `encoding/json` **无法表示** NaN / Inf——`json.Marshal` 直接报 `unsupported value: NaN`，
  因此任何合规的服务端都不可能把它们放到线上，`client.go` 里的 NaN / Inf 分支
  在 Ollama 这条路径上**永远不会触发**。
  更隐蔽的是反向情况：JSON 里合法的 `null` 分量会被 `json.Unmarshal`
  **静默解成 `0.0`**（实测 `{"embeddings":[[1,null,3]]}` → `[1 0 3]`）。
  也就是说"模型返回了非法数值"最可能的真实表现形态是**全零向量**，
  而不是 NaN。
  所以真正的防线是 M2-08 的**全零向量检查**，它才是这条路径上唯一可达且必要的守卫；
  NaN / Inf 分支保留是为了 `EmbeddingProvider` 的 provider 无关性
  （将来若有非 JSON 的 provider 就能生效），但**不要把它当成 Ollama 路径的保障**。
  单测 `TestNaNAndInfCannotCrossTheJSONTransport` 把这个"不可达"事实钉住，
  避免后来者误以为它在保护生产。
- 不设连接池（HTTP keep-alive 由默认 transport 提供）。**不要**在 batch worker 中为每个文档新建
  Client，Client 是长生命周期对象。
- `EmbedQuery` 复用 `EmbedDocuments` 的单元素路径，不要写第二份请求代码——
  查询与文档的向量必须来自同一模型同一版本，这是 M2-02 正确性的前提。

**依赖**：M0-05（`port.EmbeddingProvider`）。
**工作量**：M。

**验收标准**

```bash
ollama list | grep qwen3-embedding   # 模型已就位
EMBEDDING_PROVIDER=ollama EMBEDDING_MODEL=qwen3-embedding:0.6b EMBEDDING_DIMENSIONS=1024 \
  OLLAMA_BASE_URL=http://localhost:11434 \
  data-pipeline check-config
```

- `EmbedQuery(ctx, "安静的意大利餐厅")` 返回长度 1024 的向量，全部为有限浮点数。
- `EmbedDocuments` 传入 32 条文本返回 32 条 1024 维向量，顺序与输入一致。
- 服务返回 500 两次后成功：重试生效，总耗时符合退避节奏。
- 服务持续 500：最终返回 `errs.CodeProviderUnavailable`，不静默返回部分结果。
- 传入 100 条文本耗时显著低于 100 次单条调用（批量收益可观测）。
- `fake` provider 使 `go test ./...` 离线全绿。

---

### M2-02 Embedding 元数据

**目标**：把模型身份、维度、版本、内容哈希固化进文档行，保证模型或内容变化时不会被静默复用。

**交付物**

- `data-pipeline/internal/pipeline/knowledge/hash.go`：
  ```go
  // ContentHash is the identity of a document's text. It hashes the normalised
  // content (see normalizeForHash) so that whitespace-only churn does not
  // create a new version, but any wording change does.
  func ContentHash(scope evidence.RetrievalScope, docType evidence.DocType, restaurantID int64, content string) string
  ```
- 文档行上的四个元数据字段的写入约定（表已存在，此处定义语义）：
  | 字段 | 取值 | 语义 |
  |---|---|---|
  | `embedding_model` | `cfg.Embedding.Model`，如 `qwen3-embedding:0.6b` | 产生该向量的模型 |
  | `embedding_dimensions` | `cfg.Embedding.Dimensions`，如 `1024` | 向量长度，与表级 `vector(1024)` 交叉校验 |
  | `content_hash` | `sha256(...)` | 文档内容身份，幂等键 |
  | `version` | 该 `(restaurant_id, scope, doc_type)` 下的递增序号，从 1 开始 | 版本序号 |
- `data-pipeline/internal/config/config.go`：补 `Embedding.Timeout`（复用 `TimeoutConfig.Request`），
  以及可选的 `EMBEDDING_MAX_BATCH`（默认 32）。

**实现要点**

- **`content_hash` 必须在归一化后计算**。归一化规则要极窄且可测：
  1. 统一换行 `\r\n` / `\r` → `\n`；
  2. 折叠行尾空白；
  3. 折叠 3 个以上连续空行为 2 个；
  4. 去除首尾空白。
  **不做**小写化、不做标点归一、不做全角半角转换——这些会掩盖真实的文本变化。
- **`content_hash` 要包含 `scope` + `doc_type` + `restaurant_id`**。
  否则两个不同餐厅恰好生成同样的文本时会撞键（"本店提供 Wi-Fi" 这类短 chunk 完全可能重复）。
- **模型变化时不被静默复用**是本任务的核心。具体机制：
  - `embed` 阶段启动时查询库中已有的 distinct `embedding_model` / `embedding_dimensions`；
  - 若与当前配置**不一致**，默认行为是**拒绝写入**并报 `errs.CodeEmbeddingModelMismatch`；
  - 显式覆盖用 `--force-model-change`（写新向量、把旧文档的 `is_active` 置 false）；
  - **绝不**把不同模型的向量混存在同一个 active 集合里——那会产出无法解释的相似度分数。
- `embedding_dimensions` 写入后由 `M2-07` 的测试交叉校验：任何
  `embedding_dimensions <> 1024` 都是配置错误，必须失败而不是写库。
- 哈希算法固定 SHA-256 hex（与 M1 的 `text_hash` 一致），**不引入 CRC32/FNV**——
  哈希值要出现在审计报告里给人看，可读性比速度重要。

**依赖**：M2-01。
**工作量**：S。

**验收标准**

- `ContentHash` 对同一输入稳定；对 CRLF/LF 差异返回相同值；对改一个字返回不同值。
- 两个 `restaurant_id` 相同文本的 chunk 哈希不同。
- 连续两次 `build-documents` 产出的 `content_hash` 集合完全一致（幂等）。
- 把 `EMBEDDING_MODEL` 改成另一个模型后跑 `embed`，报 `embedding_model_mismatch` 且**不写入任何向量**。
- 加 `--force-model-change` 后写入成功，旧的 active 文档全部失效，库中不同时存在两种模型的 active 向量。

---

### M2-03 餐厅级文档构建

**目标**：为每家精选餐厅生成**恰好一个**稳定的 `retrieval_scope=restaurant` 摘要文档。

**交付物**

- `data-pipeline/internal/pipeline/knowledge/profile.go`：
  ```go
  // BuildProfile renders the restaurant-level profile document. It is a pure
  // function: the same Restaurant always yields byte-identical content, which
  // is what makes M2-05's version bump a no-op on unchanged re-runs.
  func BuildProfile(r restaurant.Restaurant, now time.Time) (evidence.KnowledgeDocument, error)
  ```
- `data-pipeline/internal/pipeline/documents.go`：`build-documents` 子命令，参数
  `--limit` / `--batch` / `--restaurant-id` / `--dry-run`，串行或按 `Pipeline.Workers` 并发。
- `profile_test.go`：黄金文本断言（golden file），覆盖字段缺失场景。

**实现要点**

- **只处理 `is_active_for_demo = true` 的餐厅**（PRD §5 Step 5：2,000–5,000 家）。
  用 `RestaurantStore.SelectForDemo` 复用 M1-10 的既有选择，不重新实现打分逻辑。
  通过 `--restaurant-id` 可针对单家餐厅调试。
- **profile 的内容结构**（PRD §5 Step 7 的字段顺序）：
  ```text
  <餐厅名>（<主菜系>，<价格档位>，<评分>）
  地址：<address>（<borough>）
  简介：<description>
  营业时间：<hours 摘要，无则省略>
  特色：<cuisine_tags / 高频 attributes>
  评论概况：<computed_avg，<n> 条入库评论；这是 2021 快照下的样本统计，不是实时口碑>
  来源：Google Local（2021 快照）。<snapshot_status 说明>
  ```
- **未知字段要显式省略，不写空串**。"无描述""无营业时间"写进文档会让 embedding 变成噪声。
  PRD 要求文档包含"未知字段"——指的是**说明性标注**而非填充空白：确实要保留一句
  "暂无官方描述"之类的显式说明，但**每家餐厅固定占位**，避免该短语本身成为高维向量簇。
- **评分口径必须标注**。`source_avg` 来自 Meta 原始字段（可能截顶），
  `computed_avg` 来自入库评论样本，两者含义不同。profile 里用 `computed_avg`（样本真实）
  并注明样本量，同时保留 `source_avg` 并注明可能截顶。**禁止**只写一个"评分"了事。
- **`content` 长度控制**：profile 是纯事实文本，不需要很长。目标 200–600 字符。
  超长 `description` 截断到 1,000 字符并在末尾标 `…`——但**截断逻辑必须只在 profile 里做**，
  不得改动 `restaurants.description` 本身。
- **时间字段用 `snapshot_at`（餐厅的 `observed_at`），不是 `now`**。
  `snapshot_at` 是"这份事实截至何时"，`now` 是"何时生成的"。
  混淆两者会让 2021 的快照看起来是今天抓的——这是引用可信度的根本问题。
  文档生成时间另有 `metadata.generated_at` 承载。
- **一条文档一个 scope**：`Scope = ScopeRestaurant`，`DocType = DocTypeRestaurantProfile`。
- **反规范化 borough**：写入 `borough = r.BoroughGuess`，缺失则留 `NULL`（M1 已确认 58.9% 语料
  在五区之外，NULL 是真实状态，不许填 `""` 或 `"unknown"`，否则 partial HNSW 永远命中不了）。

**依赖**：M1-10, M0-04。
**工作量**：L。

**验收标准**

```bash
data-pipeline build-documents --limit=100 --dry-run   # 先看产出内容
data-pipeline build-documents --limit=100
```

- `SELECT count(*) FROM knowledge_documents WHERE retrieval_scope='restaurant'` = 100。
- 分组校验 `GROUP BY restaurant_id HAVING count(*) <> 1` 为空（**每家恰好一条**）。
- 抽查 3 家餐厅的 `content`：字段齐全、`snapshot_at` 是 2021 而非今天、评分标注了样本量。
- 缺 `description` 的餐厅：文档仍生成，且不含 `简介：` 空段。
- 缺 `hours` 的餐厅：不生成 `restaurant_hours` chunk（该规则在 M2-04）。
- 同一餐厅连续构建两次，`content_hash` 相同（不产生新版本）。

---

### M2-04 佐证级文档构建

**目标**：把评论与派生统计转成 `retrieval_scope=evidence` 的 chunk，四类内容分别成 chunk，并记录生成版本。

**交付物**

| 文件 | 职责 |
|---|---|
| `knowledge/facts.go` | `restaurant_attributes` / `restaurant_hours` 两个 chunk |
| `knowledge/summary.go` | `restaurant_review_summary` chunk + `review_summaries` 规则摘要写入 |
| `knowledge/representative.go` | 代表评论选择 + `restaurant_representative_reviews` chunk |
| `data-pipeline/internal/pipeline/documents.go` | 编排：按餐厅批量取评论 → 生成 chunk |

- `review_summaries` 的写入需要新的 store 方法（表 M1 已建但从未写入）：
  ```go
  // 在 port.ReviewStore 上追加
  UpsertSummaries(ctx context.Context, items []review.Summary) (int, error)
  GetSummaries(ctx context.Context, restaurantID int64) ([]review.Summary, error)
  ```
- `reviews.is_representative` 的回写需要：
  ```go
  // 在 port.ReviewStore 上追加
  MarkRepresentative(ctx context.Context, reviewIDs []int64) (int, error)
  ```

**实现要点**

**（a）事实 / 属性 chunk**

- `restaurant_attributes`：把 `attributes` 里的稳定标签渲染成可读文本
  （"提供 Wi-Fi，可预约，有户外座位"）。**只渲染 M1 已归一化的稳定标签**，不碰
  `attributes_raw`——原始 MISC 里全是拼写变体和 `False:No` 这类噪声，
  渲染进文档等于往向量里灌垃圾。
- `restaurant_hours`：渲染为"周一至周五 11:00–22:00；周六日 11:00–23:00"。
  `hours` 每项保留了原始文本（如 `"11AM–10PM"`），**优先用原始文本**，
  格式更可靠，且用户看到的就是数据库里的真实值。
- 两个 chunk 都在对应字段为空时**不生成**（不生成空文档）。

**（b）评论摘要 chunk（规则版，LLM 为可选增强）**

PRD §5 Step 6 明确："摘要默认先使用可重复的规则、关键词和评分统计；LLM 摘要作为可选增强"。
**M2 的默认路径是规则版，不依赖任何 LLM。**

- 规则输入：`review_summaries`（按 `topic`）+ `review_stats`。
- `review_summaries` 本阶段由 `knowledge/summary.go` 基于 `reviews.topic_tags` /
  `rating` / `reviewed_at` 聚合生成，`generated_by = "rules:v1"`，
  `sentiment` 用该主题下评分相对全店均值的偏移计算（可重复、可解释）。
- 摘要文本模板：
  ```text
  <主题>：<n> 条评论，平均 <avg> 星，<正面占比>% 为 4–5 星。<最高频关键词>。
  ```
- **LLM 增强路径**：`generated_by` 改为 `"llm:<model>"`，并额外记录
  `metadata.prompt_version`。**即使走 LLM 路径，规则版本仍需保留**
  （`valid_from` / `valid_to` 已在表里支持时间有效性）。
  LLM 摘要不是 M2 的阻塞项——**Gate B 4 条全部可在纯规则路径下通过**。
- 每个 `(restaurant_id, topic)` 一条 chunk。若某餐厅有 5 个主题，就是 5 条 chunk。

**（c）代表评论选择 + chunk**

这是 M2-04 里最容易做错的部分，选择规则必须**确定性**（同输入必同输出）：

- 候选池：该餐厅 `text_review_count > 0` 的评论。
- 数量：`min(30, max(10, floor(text_review_count * 0.1)))`，并 clamp 到 `[10, 30]`（PRD §5 Step 5）。
  评论极少的餐厅取全部（少于 10 条时）。
- 情绪分层：按 `rating` 分成正向（4–5）、中立（3）、负向（1–2）三档，
  每档内按 `rating` 极值 → `reviewed_at` 新 → `id` 小 排序取前 N（**N 三档均分**）。
- **确定性 tie-breaker 是强制的**：M1 的 `score.go` 已经因为"并列分数导致集合抖动"付过代价。
  没有最终 tie-breaker 的选择逻辑在重跑时会换一批评论，破坏证据稳定性。
- 选中的评论**回写 `reviews.is_representative = true`**（`MarkRepresentative`）。
  M1 建的 `{restaurant_id, is_representative, rating}` 偏索引才有用。
  同时该餐厅其余评论置 false——`is_representative` 是**相对标记**，不是永久属性。
- chunk 内容：
  ```text
  代表性评论（2021 快照，<n> 条中选 <m> 条）
  [4星] "…"（2021-03-15）
  [5星] "…"（2021-06-02）
  [2星] "…"（2020-11-28）
  ```
  保留 `[N星]` 与日期：让 M3 的引用 UI 能显示评分与时间，这是 `Evidence.SnapshotAt` 之外的信息。
- **评论正文已由 M1 的 `curate/pii.go` 脱敏**（邮箱/电话/控制字符），
  M2 不重复脱敏，但**不得**把 `user_id` / `name` / `pics` 带进 chunk（它们不在 `reviews` 表里，
  天然安全）。prompt injection 文本已在 M1 清洗阶段处理，此处不额外过滤。

**（d）两个 review 计数怎么落地**

- `representative_review_count`：**M2-04 不需要自己写**。它由
  `import --stage=stats` 从 `count(*) FILTER (WHERE is_representative)` 重算
  （`postgres/review.go:132` 的 `countsSelect` → `curate.BuildReviewStats` →
  `UpdateReviewStats`）。所以 M2-04 只要把 `is_representative` 置对，
  收尾时跑一次 `import --stage=stats` 该列自动正确。
  **这也意味着 `contract` 套件的 `PipelineStore` 之外，M2-04 必须同时验证
  memory 与 postgres 的 `AggregateStats` 对 `is_representative` 的计数一致**——
  memory 侧靠 `computeCounts` 的 `r.IsRepresentative` 分支（`memory/store.go:381`），
  两边语义已对齐，但 M2 新增 `MarkRepresentative` 后要重跑一遍。
- `embedded_review_count`：**M2-06 显式回写**，因为它统计的是"实际写入向量的评论数"，
  库里没有可直接重算的来源。收尾同样跑 `--stage=stats` 做一次交叉校验——
  注意 `--stage=stats` 的 `BuildReviewStats` **不会**碰 `EmbeddedReviewCount`
  （`curate/stats.go:13-21` 只赋前 5 个字段），它靠 `previous` 原样透传。
  这意味着 `--stage=stats` **不会**把 M2-06 的回写冲掉，但反过来，
  M2-06 写入的值一旦错了也不会被 stats 阶段发现——
  **所以 M2-06 的回写必须是幂等的，且要单独断言。**

**依赖**：M1-08, M0-04。
**工作量**：L。

**验收标准**

```bash
data-pipeline build-documents --limit=100
```

- 四种 `DocType` 均能在库中找到样本（属性/营业时间若样本餐厅恰好为空，需换餐厅验证）。
- `SELECT count(*) FROM knowledge_documents WHERE retrieval_scope='evidence'` ≥ 100 × 4。
- 每篇 evidence chunk 的 `restaurant_id` 非空，`snapshot_at` 非空，`source` 非空。
- `SELECT count(*) FROM reviews WHERE is_representative` > 0，且每家餐厅的代表评论数落在 `[10, 30]`。
- 代表评论选择**可重复**：连续两次 `build-documents`，选中的 `review id` 集合完全一致。
- `review_summaries` 有数据，`generated_by = 'rules:v1'`，`evidence_count` 等于该主题的评论数。
- 抽查一个餐厅的 `restaurant_review_summary` chunk：主题、评论数、平均分、正面占比齐全。
- 三档情绪都被覆盖：存在 4–5 星、3 星、1–2 星各至少一条（若餐厅评分分布允许）。
- 全文扫描：`knowledge_documents.content` 中 0 命中邮箱 / 电话模式。

---

### M2-05 文档去重与版本

**目标**：用 `content_hash` + `version` + `is_active` 实现文档级幂等；内容变化生成新版本，**不覆盖**旧证据。

**交付物**

- `shared/port/writer.go` 追加：
  ```go
  // KnowledgeStore persists knowledge documents and their vectors.
  type KnowledgeStore interface {
      // UpsertDocuments writes a batch of documents, honouring the
      // (restaurant_id, retrieval_scope, doc_type, content_hash) idempotency
      // key, and returns inserted / updated / skipped counts.
      UpsertDocuments(ctx context.Context, docs []evidence.KnowledgeDocument) (KnowledgeWriteStats, error)
      // DeactivateVersions marks superseded versions inactive except the newest
      // one for each (restaurant_id, scope, doc_type).
      DeactivateSuperseded(ctx context.Context, restaurantIDs []int64) (int, error)
      // PendingDocuments returns active documents that still need a vector.
      PendingDocuments(ctx context.Context, limit int) ([]evidence.KnowledgeDocument, error)
      // SetEmbedding writes the vector plus its model metadata for a batch.
      SetEmbedding(ctx context.Context, docIDs []int64, vectors [][]float32, model string, dimensions int) (int, error)
      // ListByRestaurant / GetByHash 用于契约测试与调试。
      ListByRestaurant(ctx context.Context, restaurantID int64, scope evidence.RetrievalScope) ([]evidence.KnowledgeDocument, error)
  }
  ```
- `shared/adapter/repository/postgres/knowledge.go` 与**新建**的
  `shared/adapter/repository/memory/knowledge_store.go` 实现。
  **不要改 `memory/knowledge.go`**——那是 M0 已交付的读侧 `KnowledgeRepository`，
  与 M2 要加的写侧 `KnowledgeStore` 是两个不同的东西（详见下方"两个 knowledge 的区别"）。
- `shared/adapter/repository/contract/contract.go` 追加 KnowledgeStore 契约套件，
  memory 与 postgres 跑**同一套**断言。
- `data-pipeline/internal/pipeline/knowledge/version.go`：版本号分配与失效逻辑。

**实现要点**

- **幂等键选 `(restaurant_id, retrieval_scope, doc_type, content_hash)`**，理由：
  同一餐厅同一 doc_type 的内容若不变，重跑不应产生任何写入；
  内容变了就是新文档，必须保留旧的（证据可追溯）。
  这与 M1 `reviews` 用 `(restaurant_id, text_hash, rating, reviewed_at)` 的思路一致。
- **版本号分配**：对每个 `(restaurant_id, scope, doc_type)`，
  新版本号 = `SELECT coalesce(max(version), 0) + 1 WHERE is_active OR 全部历史`。
  **必须基于全部历史最大值**，不是仅活跃行——否则同一版本号会被复用两次。
- **切换顺序是硬要求**（PRD §5 Step 8："新批次全部成功后再切换 `is_active`，避免半批次污染"）：
  ```text
  1. 插入新版本，is_active = false      # 此时旧版本仍是唯一活跃版本
  2. 写入向量（M2-06）
  3. 旧版本 is_active = false
  4. 新版本 is_active = true
  ```
  若新版本 embedding 失败，第 3 步不执行，旧版本继续服务。
  **绝不能先关旧再插新**——中间任何失败都会让该餐厅该 scope 出现检索黑洞。
- 表级约束 `CHECK (is_active = false OR embedding IS NOT NULL)` 强制了步骤顺序：
  新版本在步骤 1 必须 `is_active = false`（否则无向量即违反约束）。
  这不是巧合，是 schema 在帮我们把关。
- **批量写入用 `unnest` 而不是 `VALUES` 列表**。M1-07 已实测：扩展协议上限 65,535 个绑定参数，
  36,133 行 × 2 就超了。M2 的文档批量同样必须用
  `unnest($1::bigint[], $2::text[], ...)`，语句大小不随语料增长。
  `source_record_ids` 是数组，天然适合 unnest。
- **一次构建可能产出多条同 key 文档吗**？可能——同一家餐厅可能有两个 `restaurant_hours` chunk
  （拆分长营业时间）。因此 `(restaurant_id, scope, doc_type)` 到文档是 **1:N**，
  版本号按这个组分配，`content_hash` 做组内去重。
- **`metadata` 写 `jsonb`**，至少包含：`source`、`snapshot_at`、`generated_by`、
  `generated_at`、`curation_version`、`borough`、`topic`（摘要 chunk）、
  `representative_count`（代表评论 chunk）。M3 的过滤与引用直接依赖它。

**依赖**：M2-03, M2-04。
**工作量**：M。

**验收标准**

```bash
data-pipeline build-documents --limit=100   # 第一次
data-pipeline build-documents --limit=100   # 第二次，模拟无变化重跑
```

- 第一次：`UpsertDocuments` 统计为 `inserted > 0`。
- 第二次：`inserted = 0, updated = 0, skipped = 第一次的 inserted`；库中行数不变。
- 手动改一条评论文本后重跑该餐厅：`inserted = 1`，新文档 `version = 旧 + 1`，
  旧文档仍在库中且 `is_active = false`，**未被删除**。
- 每个 `(restaurant_id, scope, doc_type)` 的活跃文档数：profile 类为 1，
  其余为 N（与内容一一对应）。
- 断言：同一 `(restaurant_id, scope, doc_type)` 下不存在两个相同 `version` 的行。
- 中途注入失败（如第 3 步报错）：该 `(restaurant, scope)` 仍有活跃版本可检索。
- memory 与 postgres adapter 通过同一套 KnowledgeStore 契约测试。
- 批量 upsert 10,000 条文档成功（验证 unnest 方案不撞绑定参数上限）。

---

### M2-06 批量向量化 Worker

**目标**：用 worker pool 对活跃文档批量生成 1024 维向量并写库，失败可重试，整批幂等。

**交付物**

- `data-pipeline/internal/pipeline/embed.go`：`embed` 子命令，参数
  `--limit` / `--batch` / `--workers` / `--restaurant-id` / `--scope` / `--dry-run` / `--force-model-change`。
- `data-pipeline/internal/pipeline/embedding/`：
  - `worker.go`：worker pool、批量取文档、批量 embedding、批量写回。
  - `quality.go`：M2-08 质量检查。
  - 两者都进 `PipelineStore` 的批次报告（M1-09 机制复用，**但需扩列，见下**）。
- **批次报告需要新的统计字段，而 `ingestion_batches` 表没有这些列**。
  M1 的 14 个计数列（`rows_read` / `accepted` / `written` / `deduped` / `filtered` /
  `rejected` / `unmatched` 等，`0001_init.sql:304-328`）是为 import 阶段设计的，
  **不含** `documents_read` / `embedded` / `skipped` / `failed` 或 M2-08 的拒绝原因计数。
  两个可选方案，**推荐 A**：
  - **A（推荐）**：加迁移 `0002_embedding_stats.sql`，新增
    `embedding` 相关计数列，或加一个 `embedding_stats jsonb` 列，
    由 `review.BatchReport` 同步扩展。M1 的列语义全部保持不变，不破坏已有报告。
  - **B（不推荐）**：硬塞进现有 `missing_fields jsonb`。它语义上是"必填字段缺失次数"，
    拿来放"拒绝原因计数"是滥用，审计时会误导，且 `printReport` 打印成 `missing embedding_nan N`。
  无论哪种，`review.BatchReport` 都要加对应字段，`printReport`（`main.go:351`）要加打印分支。

**实现要点**

- **工作流**：
  ```text
  PendingDocuments(limit)          # is_active AND embedding IS NULL
    -> 按 batch 切分
    -> worker pool 并发 EmbedDocuments
    -> 质量检查（M2-08）
    -> SetEmbedding（单次事务写整批）
    -> 更新批次统计
  循环直到 PendingDocuments 为空或达到 --limit
  ```
- **并发度**：`Pipeline.Workers`（M1 已有，默认 4）。**不要**开太高：
  本地 Ollama 是 CPU 推理，32 个并发只会让每个都变慢而总吞吐不变，
  还会把内存打满。并发压测后固定一个值。
- **批量大小**：`EMBEDDING_MAX_BATCH`（默认 32）。太大则单次请求超时风险上升，
  太小则 HTTP 往返占比过高。**批量大小直接影响吞吐，必须实测后固定**。
- **失败处理分级**：
  - 单条文档内容问题（超长、空文本）→ 该条标记 `rejected`，**不影响整批**；
  - 整批 provider 失败（服务不可用）→ 退避重试，连续失败则本轮结束、批次报 `failed`；
  - 质量检查不过（维度/NaN）→ 该条 `rejected` 并记录原因，**不写入**。
- **重跑幂等**：`PendingDocuments` 只取 `embedding IS NULL` 的活跃文档。
  已写入的不重复 embedding。重跑 `embed` 是安全的空操作。
- **模型切换**：`--force-model-change` 时不是"重算全部"，而是：
  ```text
  把所有 embedding_model <> 当前模型的活跃文档置 is_active = false
  -> 重新生成文档（M2-05 的版本机制天然支持：新版本）
  -> 重新 embedding
  ```
  **不要**就地覆盖旧向量——`version` 机制存在的意义就是保留历史。
- **回写 `embedded_review_count`**：统计实际写入向量的、属于
  `restaurant_representative_reviews` 的文档所打包的评论数，按餐厅聚合后经
  `RestaurantStore.UpdateReviewStats` 写回 `restaurants.embedded_review_count`。
  注意 `UpdateReviewStats` 是**整行覆盖 7 个计数列**（`postgres/restaurant.go:353-362`），
  所以调用前必须把该餐厅当前的 `stored_review_count` / `text_review_count` /
  `representative_review_count` / `last_reviewed_at` 一并读出再原样带回，
  **否则会把 M2-04 与 M1 的成果清零**。这是本任务最容易造成静默数据损坏的地方。
  更稳妥的做法是加一个只更新单列的窄接口，或让 M2-06 在写前先跑一次
  `import --stage=stats` 拿到权威值再合并。
- **进度输出**：复用 M1 的 `progress.go`（字节/行/速率/ETA），
  追加"docs/s、vectors/s"速率——embedding 阶段的耗时几乎全在 provider 侧，
  不报速率就看不出瓶颈在哪。
- **不要在 embed 阶段重新构建文档**。文档构建与向量是两个独立阶段，
  混在一起会让"改文档模板后重跑"变成全量重算。

**依赖**：M2-01, M2-05。
**工作量**：L。

**验收标准**

```bash
data-pipeline embed --limit=100 --batch=32 --workers=4
data-pipeline embed --limit=100            # 重跑：应为 no-op
```

- `SELECT count(*) FROM knowledge_documents WHERE embedding IS NOT NULL AND is_active` = 100。
- 所有向量 `vector_dims(embedding) = 1024`。
- 重跑输出 `pending=0`，库中向量数不变。
- 故意把 `EMBEDDING_MODEL` 改成不存在的模型：报明确错误码，**不写入任何向量**。
- 中途 kill 进程后重跑：能从中断点继续（已完成的不重算），最终数量正确。
- `--dry-run` 不写库，只输出将要生成的文档数与预估耗时。
- `embedded_review_count` 大于 0，且与实际 embedding 成功的评论数一致。
- 批次报告含：documents_read / embedded / rejected / failed / skipped / 耗时 / 模型名 / 维度。
- 并发 4 与并发 1 的总耗时对比：并发 4 更快（证明并发确实生效，不是空转）。

---

### M2-07 向量索引

**目标**：确认并验证 pgvector HNSW 索引可用于按 `retrieval_scope`（及 borough）执行的向量检索。

**交付物**

- 索引定义**已在 M1-02/M1-03 建好**（`0001_init.sql:277-295`）。本任务的工作是：
  1. 用 `contract_test.go` 中已有的 `TestVectorIndexStrategyIsPresent` 守住索引形状（M1 已写）；
  2. **新增** `KnowledgeStore` 的检索方法并跑契约测试；
  3. 新增带真实数据的 `EXPLAIN` 断言。
- `shared/adapter/repository/postgres/knowledge.go` 追加：
  ```go
  // VectorSearch ranks active documents by cosine distance within one scope.
  // borough is optional; when set it must select the matching partial HNSW.
  VectorSearch(ctx context.Context, scope evidence.RetrievalScope, query []float32, topK int, filter VectorFilter) ([]ScoredDocument, error)
  ```
  注意：这是**写侧 store 上的内部验证查询**，M3 的读侧走
  `port.KnowledgeRepository.VectorSearch`（`shared/port/repository.go`，M0 已定义）。
  两者形状相近但不属于同一服务，M2 只实现前者供 Gate B 验证。
- `shared/adapter/repository/postgres/knowledge_index_test.go`：`EXPLAIN` 断言。

**实现要点**

- **HNSW 不能靠 `WHERE` 裁剪**——这是 M1-03 已实测的结论（5 万行，`Rows Removed by Filter: 49759`）。
  因此 borough 过滤必须命中 `knowledge_documents_hnsw_<borough>` partial 索引。
  查询写法：`WHERE is_active AND retrieval_scope = $1 AND borough = $2 ORDER BY embedding <=> $3 LIMIT $4`。
- **`retrieval_scope` 的过滤同理是问题**：现有 6 个 HNSW 索引都只按 `is_active` 或
  `is_active + borough` 划分，**没有按 `scope` 划分的**。scope 只有两个取值，
  在当前规模（3,000 profile + ~15,000 evidence）下全量 HNSW + scope 过滤的代价可接受。
  **决策：M2 不新增 scope partial 索引，M3-03/M3-04 出现真实性能问题后再加**
  （加索引是一行 SQL，但要避免过早优化）。
  若 M3 实测不足，正确做法是加 `knowledge_documents_hnsw_scope_restaurant` /
  `..._scope_evidence`，而非靠 `WHERE` 硬过滤。
- **余弦距离 vs 内积**：`vector_cosine_ops` 与已建索引一致。
  不要在同一列上混用 `vector_l2_ops`——操作符类必须与索引匹配，否则索引不被使用。
- **pgvector 向量格式**：查询参数需从 `[]float32` 转成 pgvector 字面量
  `'[0.1,0.2,...]'`。1024 维约 10KB 文本，**参数化绑定**即可，不要拼进 SQL 字符串
  （拼字符串既慢又有注入面）。
- **验证必须包含"跨 scope 不返回"**：这是 Gate B 第 4 条的核心。
  用 `scope='evidence'` 查询，结果 100% 为 evidence——这是 M3-04"佐证不会跨餐厅返回"的同类保证，
  在 M2 就先立住规则。
- `EXPLAIN` 断言要**对抗规划器的自作聪明**：数据量小的时候规划器会选 Seq Scan，
  测试里应先 `ANALYZE knowledge_documents` 并确保样本量足够（建议 seed 5,000+ 行），
  否则断言会假阴性。

**依赖**：M1-01, M2-06。
**工作量**：M。

**验收标准**

```bash
data-pipeline embed --limit=3000
```

- 6 个 HNSW 索引（1 全局 + 5 borough）存在，`TestVectorIndexStrategyIsPresent` 通过。
- `scope='restaurant'` 的 `VectorSearch` 结果全部为 restaurant scope。
- `scope='evidence'` 的 `VectorSearch` 结果全部为 evidence scope。
- 带 `borough='manhattan'` 的查询 `EXPLAIN` 命中 `knowledge_documents_hnsw_manhattan`，
  且**不是** Seq Scan。
- 不带 borough 的查询命中 `knowledge_documents_hnsw`。
- 自查：查询一个已知的餐厅 profile 文本，Top-1 应是该餐厅本身（sanity check，
  语义检索至少要做到"同文近文"）。
- `is_active = false` 的文档不出现在任何结果中。

---

### M2-08 向量质量检查

**目标**：拦截空向量、维度不符、NaN/Inf、全零向量、重复向量，并把它们记入报告而不是写进索引。

**交付物**

- `data-pipeline/internal/pipeline/embedding/quality.go`：
  ```go
  // Check validates one vector before it is written. It is deliberately pure
  // so every rejection rule can be unit-tested without a database or a model.
  func Check(v []float32, wantDim int) error
  // Fingerprint groups identical vectors for duplicate detection.
  func Fingerprint(v []float32) string
  ```
- 批次报告中的质量字段：`rejected_empty` / `rejected_dimension` / `rejected_nan` /
  `rejected_zero` / `rejected_duplicate`，以及 `quality_sample`（抽样可查）。
- **M2-06 必须先给 `report` 加 `--stage` 过滤**（当前 `runReport` 只有 `--last` 与 `--batch-id`，
  `main.go:237-240`），`build-documents` / `embed` 两个新批次也需要分别可见。
  在加之前，用 `data-pipeline report --last=5` 看 `stage=` 列区分即可。

**实现要点**

- 检查项与判据：
  | 检查 | 判据 | 错误码 |
  |---|---|---|
  | 空 | `len(v) == 0` | `embedding_empty` |
  | 维度 | `len(v) != wantDim` | `embedding_dimension_mismatch` |
  | NaN | `math.IsNaN(float64(x))` | `embedding_nan` |
  | Inf | `math.IsInf(float64(x), 0)` | `embedding_inf` |
  | 全零 | 所有分量的绝对值都 < 1e-12 | `embedding_zero_vector` |
- **重复向量检测要谨慎**。"重复"有两种含义，处理方式不同：
  - **完全相同的指纹**（哈希相同）：几乎必然是 bug 或 provider 缓存异常 → 拒绝并报警；
  - **余弦相似度接近 1 但不完全相同**：合法（例如同一家餐厅的两段高度雷同文本）→ **不拒绝**。
  把第二类当异常会让正常的相似 chunk 被误杀。
  实现上用**精确指纹哈希**，不做近似去重。
- **全零向量检测不能省**：余弦距离对零向量的分母为 0，pgvector 的定义是
  **该行不参与距离计算**——零向量会被 HNSW **静默排除在索引结果之外**，
  既不报错也不返回。后果是该文档永远召不回来，且没有任何日志线索，比直接报错更难排查。
- **报告不写向量内容**。批次报告只记计数与原因，**绝不**把向量数组写进 `ingestion_batches`
  或日志（1024 个浮点数 × 数千条会让审计表爆炸）。要排查具体是哪条文档，
  靠 `rejected` 明细里的 `document_id`。
- 质量检查在 **worker 侧、写库前**执行（快路径），另有一个 **DB 侧兜底审计**
  （M2-07 验收里的 `vector_dims` 查询 + NaN 扫描），防止内存实现或未来新代码绕过检查。

**依赖**：M2-06。
**工作量**：S。

**验收标准**

- `Check` 单测覆盖上表 6 类判据，每类至少一个 case。
- 传入 `[0, 0, 0]`（1024 维全零）→ `embedding_zero_vector`。
- 传入含 `NaN` 的向量 → `embedding_nan`，且该向量**未写入**数据库。
- 传入 512 维向量（配置要求 1024）→ `embedding_dimension_mismatch`，错误信息含实际维度。
- 用 fake provider 返回两条完全相同的向量 → 第二条被标记 `rejected_duplicate`。
- 库中终态：`embedding IS NOT NULL` 的行里，零向量 / NaN 向量计数均为 0。
- `data-pipeline report --stage=embedding` 能看到各拒绝原因的计数
  （**M2-06 交付物之一就是给 `runReport` 加 `--stage` 过滤**）。

---

## 5. 推荐执行顺序与并行化

### 5.1 单人执行（串行）

```text
M2-01 -> M2-02 -> M2-03 -> M2-05 -> M2-06 -> M2-07
                                       \-> M2-08
                        -> M2-04 (可插入 M2-03 之后任意位置)
```

串行路径中 **M2-03 → M2-05 → M2-06 → M2-07** 已经能过 Gate B 第 1、3、4 条与扩充分项 1、5、6、9。
M2-04 决定第 2 条与扩充分项 2、10。

### 5.2 并行建议

M2 的并行空间小于 M1，因为 M2-06 依赖 M2-03/04 的产物。可行的切分：

| 轨道 | 任务 | 说明 |
|---|---|---|
| 轨道 A：provider | M2-01, M2-02 | 独立，模型就绪即可开工 |
| 轨道 B：文档构建 | M2-03, M2-04 | 依赖 A 的 M2-02（hash），但 M2-01 未完成也能开发（用 fake） |
| 轨道 C：向量化 | M2-06, M2-07, M2-08 | 依赖 B 的产物，但可用**手工 seed 的文档行**提前开发 |

单人项目建议同时保持：1 个 provider 任务 + 1 个文档任务 + 1 个测试/文档任务。

---

## 6. M2 完成定义（DoD）检查清单

### 6.1 每个任务通用 DoD（继承实施计划 §10）

- [ ] 代码已实现。
- [ ] 单元测试或集成测试已添加（`fake` provider 保证离线可测）。
- [ ] 错误路径有明确错误码（`embedding_*` 族）。
- [ ] 日志中包含 `batch_id`（批处理阶段的 trace 等价物）。
- [ ] 不泄露密钥和 PII（评论正文已由 M1 脱敏；向量不进日志）。
- [ ] 不绕过领域接口直接调用厂商 API（`ollama` 类型不逃逸 `shared/adapter/embedding/ollama`）。
- [ ] 文档或注释说明关键设计（尤其是版本切换顺序与 HNSW 过滤限制）。
- [ ] 通过 `go test ./...`、`go vet ./...`、`go test -race ./...`。
- [ ] 相关验收标准可以实际演示。

### 6.2 M2 里程碑门（Gate B）

实施计划 §9 的 4 条：

- [ ] `retrieval_scope='restaurant'` 的活跃文档覆盖 ≥ 500 家餐厅。
- [ ] `retrieval_scope='evidence'` 的活跃文档 ≥ 5,000 条。
- [ ] 所有向量均为 1024 维（`vector_dims` 全为 1024）。
- [ ] 向量检索能返回正确 scope（两个 scope 各自 100% 命中）。

本文档补充的扩充分项：

- [ ] 每个餐厅恰好 1 个活跃的 `restaurant_profile`。
- [ ] `build-documents` 重跑幂等（`content_hash` 分布不变）。
- [ ] `embed` 重跑幂等（`pending=0`）。
- [ ] 模型切换不被静默复用（`embedding_model_mismatch` + `--force-model-change` 可显式覆盖）。
- [ ] 无异常向量（空 / NaN / Inf / 全零 / 重复均为 0）。
- [ ] borough partial HNSW 命中（`EXPLAIN` 非 Seq Scan）。
- [ ] `representative_review_count` 与 `embedded_review_count` 有真实值，与库一致。
- [ ] `knowledge_documents.content` 无邮箱 / 电话命中。
- [ ] `reviews.is_representative` 不再恒为 false，且代表评论选择可重复。
- [ ] KnowledgeStore 的 memory 与 postgres adapter 通过同一套契约测试。
- [ ] `data-pipeline report --stage=embedding` 可查质量统计（该 `--stage` 过滤是 M2-06 新增的 flag）。

---

## 7. 与后续里程碑的衔接

M2 完成后按实施计划 §14 进入 M3（两级检索）。衔接点：

| 后续里程碑 | 依赖 M2 的什么 | 说明 |
|---|---|---|
| M3-03 餐厅级召回 | `retrieval_scope='restaurant'` + HNSW | 软条件影响候选排序 |
| M3-04 佐证召回 | `retrieval_scope='evidence'` + `restaurant_id` | 不得跨餐厅返回，靠 scope + 强制 `restaurant_id` 过滤 |
| M3-05 混合融合 | `VectorSearch` 返回的 `score` | 分数需可解释：余弦距离转相似度时保留原始值 |
| M3-07 证据组装 | `evidence.KnowledgeDocument.ToEvidence` | `source` / `snapshot_at` / `source_record_ids` 已就位 |
| M3-08 检索夹具 | 文档 `metadata`（`topic` / `borough` / `generated_by`） | 夹具标注需要这些字段 |
| M4-06 证据工具 | `Evidence` DTO（M0 已定义） | M2 只是把它填满 |
| M6-02 RAG 评测 | `knowledge_documents` + 批次报告 | 评测数据可追溯到生成版本 |

**接口稳定性要求**：M2 对 `port` 的扩展是 M3/M4 的契约。若必须再调整，需同步更新：

- 本文档 §2.2、§4 各任务的接口形状。
- `plans/platepilot-implementation-plan.md` 对应任务的依赖与验收。
- 所有实现（postgres / memory / fake）与契约测试。
- `shared/domain/evidence/evidence.go` 的 DTO 字段。

**M2 明确不做的事**（避免范围蔓延）：

- 不实现召回排序、融合、rerank（M3）。
- 不实现 Agent / 工具 / SSE（M4）。
- 不引入 LLM 摘要作为**必选**路径（规则版是默认，LLM 是可选增强）。
- 不做跨批次增量 embedding（当前每次重跑都是全量 pending 扫描，规模可接受）。
- 不为 `retrieval_scope` 建 partial HNSW（见 M2-07 决策，等 M3 实测）。
- 不实现前端（M6）。
- 不做在线 embedding 服务（query embedding 由 M3 在请求时同步调用）。

---

## 8. 风险与注意事项

| 风险 | 触发点 | 控制措施 |
|---|---|---|
| 本地模型未 pull | M2-01 验证时 404 | 先 `ollama pull`；404 不重试（重试无意义）；文档明写前置命令 |
| 本地推理吞吐低 | M2-06 全量 3,000 家耗时过长 | 批量 32 + 并发 4；`--limit` 先跑样本；不要盲目全量 |
| 维度不匹配晚暴露 | 写入时 pgx 序列化失败 | M2-01 拿到响应立即校验；表级 `vector(1024)` 兜底 |
| 版本切换顺序写反 | 插入新版本前先关旧版本 | M2-05 固定四步顺序；`CHECK (is_active=false OR embedding IS NOT NULL)` 强制约束 |
| 代表评论抖动 | 重跑选出不同评论 | 三层确定性排序 + `id` 终局 tie-breaker；单测断言两次结果集合一致 |
| profile 混入评论观点 | 把评论写进事实文档 | profile 只读 `restaurants` 表；评论只能在 evidence scope；代码层面分离 builder |
| HNSW 被 `WHERE` 废掉 | 带 borough 过滤退化为 Seq Scan | 反规范化 `borough` 列 + partial 索引（M1 已验证）；`EXPLAIN` 断言守住 |
| scope 过滤未命中索引 | 全量 HNSW + scope 过滤 | M2 可接受；M3 实测后加 partial 索引，不靠 `WHERE` 硬扛 |
| 快照时间被写成当前时间 | `snapshot_at = now()` | 一律用 `restaurants.observed_at`；`now` 只进 `metadata.generated_at` |
| 向量污染索引 | NaN / 零向量写入 | M2-08 写前检查 + 库侧兜底审计 |
| 批量绑定参数超限 | 用 `VALUES` 列表写文档 | `unnest` 方案，M1-07 已实测；10,000 条单测覆盖 |
| PII 泄漏 | 评论正文含邮箱 / 电话 | M1 已脱敏；M2 全量扫描断言 0 命中；不引入新的文本来源 |
| 两个计数仍为 0 | 忘记回写 | M2-04 置位 `is_representative`（`--stage=stats` 自动重算 `representative_review_count`）、M2-06 回写 `embedded_review_count`，收尾重跑 `--stage=stats` |
| 报告无处安放 | `ingestion_batches` 没有 embedding 统计列 | M2-06 加迁移扩列（推荐 A 方案），不要硬塞 `missing_fields` jsonb |
| 审计表被向量撑爆 | 把向量写进 `ingestion_batches` | 报告只记计数与 `document_id`；向量只在 `knowledge_documents` |

---

## 附录 E：实施记录（M2 实际落地时的发现）

本节记录实施过程中**实测发现**的、与上文设计不一致之处。每条都已在代码中按实际情况处理。

### E.1 schema 缺一个唯一索引（已补迁移）

`0001_init.sql` 建了 `knowledge_documents` 表和全部索引，但**没有**在
`(restaurant_id, retrieval_scope, doc_type, content_hash)` 上建唯一索引。
而 §4 M2-05 设计的 `ON CONFLICT (restaurant_id, retrieval_scope, doc_type, content_hash) DO NOTHING`
没有对应索引就无法执行（PostgreSQL 会报 `there is no unique or exclusion constraint matching`）。

已补 `migrations/0002_knowledge_document_identity.sql`。**这是 M1 的 schema 缺口，
不是 M2 新需求**——M1 建表时没有写侧消费者，所以没暴露。

唯一键**刻意不含** `is_active` 与 `version`：这两者会随文档生命周期变化，
把它们放进幂等键会让"内容未变的重跑"变成一次写入，正好破坏幂等性。

### E.2 契约套件需要 1024 维（不是任意维）

`knowledge_documents.embedding` 是 `vector(1024)`，数据库会拒绝任何其他宽度
（实测 `ERROR: expected 1024 dimensions, not 4`）。契约套件原本用 4 维占位方便，
结果全部撞上这条约束。已改为使用生产宽度 `knowledgeVectorDimensions = 1024`。

这也**反过来印证了 §2 的判断**：维度不符不是"运行时可捕获的错误"，
而是数据库的硬拒绝——所以必须在写入前校验（M2-08），否则错误只会指向 pgvector。

### E.3 memory adapter 必须复刻 schema 的 CHECK 约束

契约套件断言"无向量文档不得被激活"时，Postgres 通过、memory 通过不了。
如果只让数据库守这条规则，mock 就会放过真实实现不可能出现的状态——
而这正是契约套件要防的那类 bug。已在 `memory.KnowledgeStore.ActivateDocuments`
补上同样的检查。**这条约束现在有两个执行点，契约测试保证它们一致。**

### E.4 契约测试的默认 DSN 原本指向开发库（已改）

`contract_test.go` 的 `dsn()` 默认值是 `.../platepilot`，而 `newStores` 会调用
`client.Drop(ctx)` **删掉全部表**。也就是说一次普通的 `go test ./...` 会清空开发库。
已把默认值改为独立的 `platepilot_contract_test` 库，让"安全"成为默认行为而非需要记得的约定。

### E.5 数组参数的类型推断陷阱

批量写入用 `unnest($1::bigint[], ...)` 时踩到三个连续的类型问题，都由契约套件暴露：

1. `text[][]`（二维）不能写入 `text[]` 列 → `SQLSTATE 42804`；
2. 改用 `jsonb` 中转也不行：`cannot cast type jsonb to text[]`（`SQLSTATE 42846`）；
3. 最终方案：把每个文档的 source ids 用 `chr(31)`（ASCII 单元分隔符，源数据中不可能出现）
   拼成一个字符串放进一维 `text[]`，在 SQL 里用
   `coalesce(string_to_array(i.source_record_ids, chr(31)), '{}')::text[]` 还原。

另外两个非类型问题：
- 不能把 `DocumentID`（此时为 0）一起插入，否则撞主键 `knowledge_documents_pkey`
  而不是幂等键。已改为完全交给 identity column 分配。
- `unnest(...) WITH ORDINALITY` 里的 `ord` 必须显式列出字段（`SELECT u.ord, u.restaurant_id, ...`），
  `SELECT ord, u.*` 会造成列名二义（`SQLSTATE 42702`）。

### E.6 JSON 无法承载 NaN/Inf（见 §M2-01）

`encoding/json` 序列化 NaN/Inf 直接失败，所以合规服务端不可能把它们发上线；
而 JSON 里合法的 `null` 分量会被**静默解成 `0.0`**。
结论：Ollama 路径上真正可达的守卫是**全零向量检查**，不是 NaN 检查。

## 附录 A：环境变量（M2 相关）

```bash
# --- Embedding（M2 起必需）-------------------------------------------------
# Provider 选择，目前只有 ollama 一个实现
# EMBEDDING_PROVIDER=ollama
# OLLAMA_BASE_URL=http://localhost:11434
# EMBEDDING_MODEL=qwen3-embedding:0.6b
# EMBEDDING_DIMENSIONS=1024

# 单次 embedding 请求的批量大小（M2-06，需实测后固定）
# EMBEDDING_MAX_BATCH=32

# --- data-pipeline（M2 复用）-----------------------------------------------
# PIPELINE_WORKERS=4            # 向量化 worker pool 并发度
# PIPELINE_BATCH_SIZE=500        # 文档批量写入大小
```

## 附录 B：常用命令速查

```bash
# 0. 本地模型（前置）
ollama pull qwen3-embedding:0.6b
ollama list | grep qwen3-embedding

# 1. 数据库已就绪（M1 产物）
make pg-up
data-pipeline migrate --status

# 2. 检查配置（含 embedding 段）
EMBEDDING_PROVIDER=ollama data-pipeline check-config

# 3. 先 dry-run 看产出内容
data-pipeline build-documents --limit=100 --dry-run

# 4. 样本构建文档
data-pipeline build-documents --limit=100

# 5. 重跑验证幂等（应 inserted=0）
data-pipeline build-documents --limit=100

# 6. 样本向量化
data-pipeline embed --limit=100 --batch=32 --workers=4

# 7. 全量：3,000 家精选餐厅
data-pipeline build-documents
data-pipeline embed

# 8. 回写并校验计数
data-pipeline import --stage=stats

# 9. Gate B 自检
#   - 餐厅级文档数
#   - evidence chunk 数
#   - 维度一致性
#   - scope 检索正确性
#   - 质量统计

# 10. 质量报告
data-pipeline report --stage=embedding   # --stage 过滤由 M2-06 新增

## 附录 C：Gate B 自检查询

```sql
-- 1. 餐厅级文档覆盖（≥ 500）
SELECT count(DISTINCT restaurant_id)
FROM knowledge_documents
WHERE retrieval_scope = 'restaurant' AND is_active;

-- 2. evidence chunk 数（≥ 5000）
SELECT count(*)
FROM knowledge_documents
WHERE retrieval_scope = 'evidence' AND is_active;

-- 3. 每家餐厅恰好一个 profile（必须为空）
SELECT restaurant_id, count(*)
FROM knowledge_documents
WHERE retrieval_scope = 'restaurant' AND is_active
GROUP BY restaurant_id
HAVING count(*) <> 1;

-- 4. 维度一致性（必须为 0）
SELECT count(*)
FROM knowledge_documents
WHERE embedding IS NOT NULL AND vector_dims(embedding) <> 1024;

-- 5. 元数据一致性（必须为 0）
SELECT count(*)
FROM knowledge_documents
WHERE is_active
  AND (embedding_model IS NULL OR embedding_dimensions IS NULL);

-- 6. scope 分布
SELECT retrieval_scope, doc_type, count(*)
FROM knowledge_documents
WHERE is_active
GROUP BY retrieval_scope, doc_type
ORDER BY 1, 2;

-- 7. 版本卫生：同一组内无重复版本号（必须为空）
SELECT restaurant_id, retrieval_scope, doc_type, version, count(*)
FROM knowledge_documents
GROUP BY 1, 2, 3, 4
HAVING count(*) > 1;

-- 8. 零向量检测（必须为 0）
--    pgvector 0.8.6 提供 vector_norm()；若在旧版本上跑，可用 l2_norm()，语义相同。
SELECT count(*) FROM knowledge_documents
WHERE embedding IS NOT NULL
  AND vector_norm(embedding) = 0;

-- 9. 代表评论计数与库一致（应无差异行）
--    注意 representative_review_count 是 restaurants 上的普通 integer 列，
--    不是 jsonb；review_stats 只是 Go 侧的 DTO 名。
SELECT r.id, r.representative_review_count AS stated,
       (SELECT count(*) FROM reviews rv
         WHERE rv.restaurant_id = r.id AND rv.is_representative) AS actual
FROM restaurants r
WHERE r.is_active_for_demo
  AND r.representative_review_count
      <> (SELECT count(*) FROM reviews rv
            WHERE rv.restaurant_id = r.id AND rv.is_representative);

-- 10. scope 检索正确性（M2-07）
SET LOCAL hnsw.ef_search = 100;
-- 预期：全部为 evidence，且不出现 is_active = false
```

## 附录 D：参考文档

- `plans/platepilot-implementation-plan.md` §6 M2、§7 关键路径、§8 并行建议、§9 Gate B
- `plans/platepilot-technical-prd.md` §4.7 `knowledge_documents`、§5 Step 6–9
- `plans/platepilot-m1-task-document.md` §0.1 实现状态、§4.0 领域与接口（写侧端口约定）
- `shared/domain/evidence/evidence.go`（`KnowledgeDocument` / `Evidence` / `DocType`）
- `shared/port/embedding.go`（`EmbeddingProvider`）
- `shared/port/writer.go`（`RestaurantStore` / `ReviewStore` / `PipelineStore`，M2 追加 `KnowledgeStore`）
- `shared/adapter/repository/postgres/migrations/0001_init.sql`（`knowledge_documents` / `review_summaries` / HNSW 索引）
