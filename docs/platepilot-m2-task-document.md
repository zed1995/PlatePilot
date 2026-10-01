# PlatePilot M2 Embedding 与知识文档任务文档

> 日期：2026-09-30
> 依据：`docs/platepilot-implementation-plan.md`（§4 里程碑总览、§6 M2、§7 关键路径、§9 Gate B、§10 完成定义）与 `docs/platepilot-technical-prd.md` v0.12（§4.7 `knowledge_documents`、§5 Step 7–9）
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
- **批量大小**：`EMBEDDING_MAX_BATCH`（默认 32，范围 1..2048）。太大则单次请求
  超时风险上升，太小则 HTTP 往返占比过高。**批量大小直接影响吞吐，
  必须实测后固定**；`--batch` 可临时覆盖配置以免每次实测都改环境变量。
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

- [x] 代码已实现。
- [x] 单元测试或集成测试已添加（`fake` provider 保证离线可测）。
- [x] 错误路径有明确错误码（`embedding_*` 族）。
- [x] 日志中包含 `batch_id`（批处理阶段的 trace 等价物）。
- [x] 不泄露密钥和 PII（评论正文已由 M1 脱敏；向量不进日志）。
- [x] 不绕过领域接口直接调用厂商 API（`ollama` 类型不逃逸 `shared/adapter/embedding/ollama`）。
- [x] 文档或注释说明关键设计（尤其是版本切换顺序与 HNSW 过滤限制）。
- [x] 通过 `go test ./...`、`go vet ./...`、`go test -race ./...`。
- [x] 相关验收标准可以实际演示。已在真实 PostgreSQL（36,133 家餐厅 /
      8,727,344 条评论）与真实 `qwen3-embedding:0.6b` 上跑通，见 §6.3.0。

> **`batch_id` 日志为什么是硬要求**：审计行是一次运行唯一的持久记录，
> 日志里没有 `batch_id` 就无法把一行输出对应回某次具体的运行。
> 两次运行交叠时，没有 id 的日志行是不可读的。

### 6.2 M2 里程碑门（Gate B）

实施计划 §9 的 4 条：

- [x] `retrieval_scope='restaurant'` 的活跃文档覆盖 ≥ 500 家餐厅。**实测 3,000。**
- [x] `retrieval_scope='evidence'` 的活跃文档 ≥ 5,000 条。**实测 8,775。**
- [x] 所有向量均为 1024 维（`vector_dims` 全为 1024）。**实测 11,775 全部为 1024。**
- [x] 向量检索能返回正确 scope（两个 scope 各自 100% 命中）。
      **实测：restaurant 查询返回 20/20 restaurant，evidence 查询返回 3/3 evidence。**

本文档补充的扩充分项。**行为已由契约/单元测试覆盖，但计数类结论需要真实全量数据**，
两者在下面分开标注：

**行为正确性（测试已覆盖，`go test ./...` 通过）**

- [x] 每个餐厅恰好 1 个活跃的 `restaurant_profile`（契约：`runSupersession` 断言组内活跃数）
- [x] `build-documents` 重跑幂等（契约：`UpsertDocuments` 二次调用 `skipped` 不新增行）
- [x] `embed` 重跑幂等（`TestRunEmbedRerunIsANoOp`：第二次 `documents=0`）
- [x] `content_hash` 保留文档的换行结构，只做计划规定的四条归一化
      （`TestContentHashPreservesLineStructure` 等三条，并因此修掉 E.23）
- [x] 同批同组文档版本号不重复（契约：`runSameGroupBatch`；memory 与 postgres 跑同一套）
- [x] 模型切换不被静默复用（`TestRunEmbedRefusesAModelChange` 拒绝；
      `TestForceModelChangeRetiresTheOldDocuments` 确认旧文档被下线且保留向量；
      `TestRunAfterAModelChangeNeedsNoForce` 确认切换后不再需要 flag）
- [x] `embedded_review_count` 的回写与文档一致（`TestRunEmbedWritesBackTheEmbeddedReviewCount`）
- [x] 整页多行时 `SupersededDocumentIDs` 不返回调用方自己的行（E.27）
- [x] `embed --restaurant-id` 只处理指定餐厅，且在 inactive 文档上有效（E.25）
- [x] `build-documents` 的测试检查实际产出而非仅审计行（E.26）
- [x] 重复向量在并发下也能检出（`TestRunEmbedRejectsDuplicatesAcrossWorkers`，
      并因此修掉 E.24 的每 worker 独立集合）
- [x] 质量门拒绝异常向量（`TestRunEmbedRecordsQualityRejections`、并发版本）
- [x] KnowledgeStore 的 memory 与 postgres adapter 通过同一套契约测试
- [x] `report --stage` 过滤与 M2 批次列往返（`runEmbeddingStageReport`）
- [x] 中途失败后可续跑，已完成的文档不重算（`TestRunEmbedResumesAfterAMidRunFailure`，
      并因此修掉 E.18 的孤儿文档）
- [x] 模型不存在时报 `provider_unavailable` 且不写入任何向量
      （`TestRunEmbedWritesNothingWhenTheModelIsMissing`）

**已在真实数据库上确认（11,775 篇活跃文档 / 3,000 家餐厅）**

- [x] `retrieval_scope='restaurant'` 覆盖 ≥ 500 家餐厅 —— **3,000**
- [x] `retrieval_scope='evidence'` 活跃文档 ≥ 5,000 条 —— **8,775**
- [x] 所有向量均为 1024 维 —— `vector_dims <> 1024` 为 **0**
- [x] 零向量检测 —— `vector_norm = 0` 为 **0**
- [x] 活跃文档都有模型元数据 —— 缺口 **0**
- [x] 组内版本号不重复 —— 重复组 **0**
- [x] borough partial HNSW 命中 —— 真实规模下 `Index Scan using
      knowledge_documents_hnsw_manhattan`（11,775 行时）
- [x] `representative_review_count` 与库一致 —— 不一致行 **0**
- [x] `knowledge_documents.content` 无邮箱命中（**0**）/ 无真实电话命中（**0**）
- [x] `reviews.is_representative` 不再恒为 false —— **51,313** 条被选中
- [x] 向量检索返回正确 scope —— restaurant 20/20、evidence 3/3
- [x] `embedded_review_count` 已回写且与文档一致
- [x] 代表评论选择可重复 —— 对餐厅 6751 二次 `build-documents --scope=evidence`
      后，`is_representative` 的 id 集合哈希不变（`8a642e38...`），
      文档 `content_hash` 集合哈希也不变（没有产生新版本），
      仍是 4 活跃 / 4 总数

**唯一的已知局限（非缺陷）**：`retrieval_scope='restaurant'` 只有 3,000 行，
这个规模下 planner 对该 scope 选择顺序扫描；`evidence`（8,775 行）与
borough 分区查询都会走 HNSW。这是 E.35 实测的交叉点决定的，
M2-07 已明确"不新增 scope partial 索引"，等 M3 出现真实性能问题再加。

---

### 6.3 当前状态：代码完成，待联调验证

M2-01 ~ M2-08 的**代码**已全部实现，`go build` / `go vet` / `gofmt` 干净，
`go test ./...` 与 `go test -race ./...` 除一项外全部通过。

### 6.3.0 恢复完整权限后的验证结果

权限恢复为完全访问后，以下几项从"未验证"转为"已验证"：

- **迁移 0003 已在真实库上 apply**（36133 家餐厅 / 872 万条评论的库），
  6 个审计列的类型与可空性全部正确，两条历史 `ingestion_batches` 行读出
  NULL 而非 0 —— 正是设计意图（"本阶段未参与"而不是"跑了但结果是零"）。
- **契约测试全绿**，过程中查出并修掉两个真实缺陷（见 E.33、E.34）。
- **三个 HNSW 索引断言在真实 planner 上全绿**（见 E.35），并做过故障注入：
  把 borough 断言改成全局索引名，测试变红。
- **ollama 全部 18 个测试通过**，含此前因无法绑定端口而从未跑过的
  `httptest` 版本；`go test -race` 亦通过。E.31 补的 `RoundTripper` 版本
  仍然保留，两套互为对照。
- **端到端链路跑通**：`build-documents` 产出 3000 篇 profile + 8842 篇
  evidence，`embed` 用真实 `qwen3-embedding:0.6b` 写入 1024 维向量，
  重跑为 no-op（0 篇），200 篇样本的向量互不相同且无 NaN/Inf。

期间发现 `REQUEST_TIMEOUT` 默认值在真实数据上必然超时，已修（见 E.32）。

### 6.3.2 补跑 `review_summaries`：四类 evidence chunk 全部落地（见 E.36）

上一节记的"8842 篇 evidence"只有三类——`restaurant_review_summary` 是 0 篇。
根因是时序而非缺陷：09:06 导入 872 万条评论时 `curate/topics.go` 还不存在，
`topic_tags` 从未落库。重跑 `import --stage=review`（509 秒，计数与首次逐项一致）
后补齐，再 `build-documents --scope=evidence`（68 秒）+ `embed`（22 分钟）。

| 指标 | 补跑前 | 补跑后 |
|---|---|---|
| `reviews.topic_tags` 非空 | 0 | 3,375,350（覆盖率 38.68%） |
| `review_summaries` | 0 行 | 18,531 行 |
| `restaurant_review_summary` 文档 | **0 篇** | 18,356 篇活跃 |
| evidence chunk 总数 | 8,775 | **27,198** |

主题分布（真实语料）：food 2,544,707 / service 1,818,283 / value 697,613 /
ambience 691,449 / wait 542,424 / group_friendly 98,361 / kid_friendly 50,652。
覆盖率 38.68% 是设计使然——命中不了任何主题词的评价返回空而不是兜底。

**Gate B 最终自检（真实库 36,133 家餐厅 / 8,727,344 条评论 / 3,000 家 demo）**：

| 检查 | 阈值 | 实测 |
|---|---|---|
| Q1 restaurant 覆盖 | ≥500 | **3,000** |
| Q2 evidence chunk | ≥5,000 | **27,198** |
| Q3 每餐厅 profile 恰好 1 个 | 0 违规 | **0** |
| Q4 向量维度 | 全 1024 | **0 违规** |
| Q5 活跃文档模型元数据 | 0 缺口 | **0** |
| Q7 组内版本号重复 | 0 | **0** |
| Q8 零向量 | 0 | **0** |
| Q9 rep-count 一致 | 0 不一致 | **0** |
| Q10 PII | 0 | 邮箱 **0**，真实电话 **0**（1 处 `[redacted-phone]` 源占位符，见 E.36） |
| scope 隔离 | 不串 | restaurant→profile、evidence→evidence，**越界 0** |
| 活跃无向量 | 0 | **0** |

scope 隔离用真实 `VectorSearch` 代码路径验证（非手写 SQL）：以一篇 evidence
文档的向量查询，restaurant scope 返回跨餐厅 profile（d=0.1227），
evidence scope 返回同餐厅 attributes（**d=0.0000 精确命中自身**）。

`go test ./...` 全绿（含 190 秒 postgres 契约测试，真库 + 真 socket）。

原"唯一失败项"记录的是 ollama 的 `httptest` 用例受沙箱端口限制；
权限恢复后该限制不复存在，这条记录已随之失效。

E.29 补完后的最终回归：35 个包（除 ollama）全通过，
`gofmt` / `go build` / `go vet` / `go test` / `go test -race` 均干净。
测试必须带 `GOCACHE=/tmp/platepilot-gocache`——默认 GOCACHE 路径
在沙箱内不可写，报错与代码无关。

> **zsh 陷阱（已踩）**：`PKGS=$(go list ./... | grep -v ollama)` 之后
> `go test $PKGS` 会失败并报 `malformed import path ... invalid char '\n'`，
> 看起来像代码问题，实际是 zsh 不对未加引号的变量做分词，整串包名被当成
> 一个 import path。正确写法是数组：
> `PKGS=(${(f)"$(go list ./... | grep -v ollama)"})`。
> 同样的原因还会产生假的 `[setup failed]`，指向一个其实完全正常的包
> （当时指向 `shared/testkit`）——排查方向会被完全带偏。

### 6.3.1 覆盖率驱动的补充审计

M2-01~M2-08 逐个 review 之后，又用覆盖率做了一轮定点审计，
目标是"**从未被执行过的代码**"——覆盖率 0% 意味着某条路径从未被验证过，
而它是否正确只能靠猜。结果补上了三处此前无人测过的暴露面：

| 函数 | 原覆盖率 | 说明 |
|---|---|---|
| `buildEvidenceDocuments` | 0% | Gate B「≥5,000 条 evidence」的实际产出路径 |
| `selectRestaurants` | 0% | 决定哪些餐厅被处理（见 E.26：原测试里它从未返回过餐厅） |
| `embedOneRestaurant` | 0% | `--restaurant-id` 调试路径（见 E.25：真实数据上恒为空转） |
| `runBuildDocuments` / `runEmbed`（`main.go`） | 0% | 运维实际敲的命令，含 flag 与前置校验 |
| `boroughOf` / `optionalString` / `orEmptyMap` | 0% | borough 决定文档进哪个 HNSW partial 索引 |

`data-pipeline` 主包 8.4% → 12.9%，`pipeline` 包 73.3% → 80.4%。
`postgres` 包 5.5% 是沙箱限制（需要真实库），其中两个纯函数
（`vectorSearchStatement`、`vectorLiteral`）已 100%。

最后一处 0% 覆盖是 `ParseEmbedOptions` 里 `EMBEDDING_MAX_BATCH` 的
接线：配置项在计划里被写了三遍，代码里一次都没有（见 E.29）。
审计这个旋钮时顺带发现批量大小**不可配置**——而计划自己写着
"必须实测后固定"，实测的前提就是能改这个值而不重新编译。
这是覆盖率审计的第四个"0% 才是真正的信号"的例子：
前三个是**功能没写**，这个是**功能写了计划文档就以为写了**。

### 6.4 原本未验证的部分（现已全部在真实环境验证）

以下三项曾在"必须真实环境才能确认"清单上。权限恢复后全部跑过，
结论记录在此，静态验证（`migration_audit_test.go`）作为回归防线保留。

| 项 | 当时的结论 | 实际结果 |
|---|---|---|
| 迁移 `0003_embedding_audit.sql` | 从未 apply 过 | 已在 36,133 家餐厅的库上 apply，耗时 <1s。6 列类型与可空性全部正确；两条 M1 历史行读出 NULL 而非 0，正是"本阶段未参与"的设计意图 |
| EXPLAIN 索引断言 | 断言修好了但没在真库跑过 | 三个断言全绿，但**首次运行即失败**——它们种的是 500 行而注释承诺 5000 行（E.35）。修正后按真实交叉点（50k 行）验证通过 |
| Gate B 数量门槛 | `--limit=200` 达不到 | 全量 3,000 profile + 8,842 evidence，向量化 11,775 篇活跃文档，全部达标 |

**跑真实环境时额外查出三个缺陷**（静态验证全绿的情况下），
说明静态检查确实不能替代真库：

| 缺陷 | 为什么静态验证看不出来 | 记录 |
|---|---|---|
| `SupersededDocumentIDs` 自连接未钉住调用方 id | SQL 语法完全合法，占位符编号连续 | E.33 |
| `knowledgeColumns` 不含 embedding | 列在 SELECT 里是自洽的，扫描目标数也匹配 | E.34 |
| `REQUEST_TIMEOUT=15s` 覆盖不了最长的文档 | 没有任何测试嵌入过真实长度的文档 | E.32 |

其中 E.33 最值得记：查询能执行、结果非空、类型正确，只是**集合错了**。
契约测试在真库上第一次运行就抓到它，而在只有内存 adapter 时它一直绿。

### 6.5 恢复后的执行顺序

**以下就是实际执行过的顺序，可以照抄**（M2 全量约 1 小时，其中
向量化 55 分钟，瓶颈是本地 CPU 推理，约 35 篇/分钟）：

```bash
# 0. 前置：M1 的两个阶段必须先跑过，否则 selectRestaurants 返回空
go run ./data-pipeline migrate                        # 应用 0003
go run ./data-pipeline import --stage=score           # is_active_for_demo + knowledge_score
go run ./data-pipeline import --stage=stats           # representative_review_count

# 1. 小样本验证，确认无异常再放开
go run ./data-pipeline build-documents --limit=200 --dry-run
go run ./data-pipeline build-documents --limit=200     # 看 inserted / skipped
go run ./data-pipeline embed --limit=200 --batch=16 --workers=4
go run ./data-pipeline embed --limit=200               # 重跑应为 documents=0

# 2. 全量
go run ./data-pipeline build-documents                 # restaurant scope
go run ./data-pipeline build-documents --scope=evidence
go run ./data-pipeline embed --batch=16 --workers=4    # 11,206 篇，约 55 分钟

# 3. 验收
go run ./data-pipeline report --stage=embedding
psql "$DSN" -f <附录 C 的查询>

# 4. 测试（Gate B 必须带 PLATEPILOT_REQUIRE_DB=1，否则需要数据库的测试
#    会静默跳过，见 E.19）
export PLATEPILOT_REQUIRE_DB=1
export PLATEPILOT_TEST_POSTGRES_DSN="postgres://platepilot:platepilot@localhost:55432/platepilot_contract_test?sslmode=disable"
go test ./... && go test -race ./...
```

**第 0 步是这次实际踩到的**：库里有 36,133 家餐厅、872 万条评论，
但 `is_active_for_demo` 全为 false，因为 M1 的 `score` / `stats`
两个阶段从没在这份数据上跑过。`build-documents` 静默返回
`restaurants=0`——不报错，只是没有输出。看起来像 M2 的 bug，
实际是前置数据没就位。

**`--batch=16` 而不是默认的 32**：见 E.32，本机实测吞吐与 batch
大小无关，而 batch 32 的最长文档会撞超时。16 在
"单次请求不超时"和"别把往返开太多"之间。

**全量向量化会拒绝 67 篇**（`embedding_duplicate`）：这些餐厅的代表
评论文档内容完全相同，向量逐位一致。质量门按设计拒绝它们而不是写入
重复向量，`reject_reasons` 里记着 `{"embedding_duplicate": 67}`。

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
- `docs/platepilot-implementation-plan.md` 对应任务的依赖与验收。
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

### E.6 `PendingDocuments` 原本选中了一个不可能的状态（已修）

`0001_init.sql:246` 有 `CHECK (is_active = false OR embedding IS NOT NULL)`，
而 M1-03 定的 `PendingDocuments` 写的是 `WHERE is_active AND embedding IS NULL`。
**这两个条件互相矛盾**：前者禁止"活跃但无向量"，后者只返回"活跃但无向量"。

叠加 M2-03/M2-04 的 builder 全部以 `IsActive: false` 落库（符合计划的四步切换），
结果是 **M2-06 永远查不到任何待处理文档，整条 embedding 链路是一个静默 no-op**：
命令正常退出、批次报 `succeeded`、`embedded=0`，没有任何错误线索。

已把谓词改为 `WHERE embedding IS NULL`。理由：

- 「谁需要向量」与「谁是活跃版本」是两个正交的问题。前者只取决于有没有向量；
- 刚构建的文档恰好都是 `is_active = false`，而它们正是最需要向量的那些；
- 激活是 embedding 之后的独立一步，混进查询谓词会让两个阶段互相等待。

契约测试同步更新，并新增两条断言：向量写入后文档离开 pending 集合；
`limit = 0` 返回空（0 是"取 0 条"，不是"取全部"——调用方必须自己分页）。

### E.7 拒绝的文档会让 embedding 循环不终止（已修）

质量门拒绝的文档**不会拿到向量**，所以它永远留在 pending 集合里。
原循环每轮重新查 pending、重新问 provider、重新拒绝同一个文档，
`rejected` 计数无限增长且循环不退出。

已在本轮内维护 `rejected` 集合，跳过本次运行已拒绝的文档，并在
「剩余 pending 全部已拒绝」时退出。语义是诚实的：这些文档留在库里但没有向量，
审计表里有它们的 id 和原因，重跑会再试一次（provider 修好后可能就通过了）。

### E.8 `EmbedResult` 按值传递导致计数全部丢失（已修）

`embedDocuments` / `embedBatch` / `embedPending` 的 `result EmbedResult`
是值传递，内部 `result.Embedded++` 改的是副本。
结果是 `Documents` 计数正确（它在循环里累加）而 `Embedded`/`Rejected`/`Failed`
恒为 0。已全部改为 `*EmbedResult`。

这个 bug 被 `TestRunEmbedFillsPendingDocuments` 抓到——它断言的是**写进数据库的
向量**而不是返回值，所以如果只断言返回值就会一起漏掉。

### E.9 `UpdateReviewStats` 整行覆盖的风险按计划建议消除

计划 §M2-06 指出 `UpdateReviewStats` 会覆盖 7 个计数列，要求调用前先读出其它 6 列
原样带回，否则会清零 M1/M2-04 的成果。

已按计划给出的"更稳妥的做法"加了窄接口
`RestaurantStore.UpdateEmbeddedReviewCount(ctx, id, count)`（postgres + memory），
只写 `embedded_review_count` 一列。契约测试断言：调用窄接口后
`stored_review_count` / `text_review_count` / `rating_computed_avg` 保持不变。

计数值本身来自 `KnowledgeStore.EmbeddedReviewCounts`——按文档的
`metadata->>'representative_count'` 聚合，**从库里算而不是从本轮写入的累加**。
增量式写法在"写完向量但没来得及更新计数"时会永久漂移，也分不清重嵌入和新嵌入。

### E.10 没有任何代码调用 `ActivateDocuments`（已修）

四步切换的前三步都有实现，**第四步（`is_active = true`）没有**：
`grep -r ActivateDocuments data-pipeline/` 只命中端口定义和测试。

后果比 E.6 更隐蔽：文档拿到了向量，却永远停在 `is_active = false`，
而**所有** partial 索引的谓词都是 `WHERE is_active`。也就是说
6 个 HNSW 索引一个都看不到这些文档，`VectorSearch` 恒返回空，
Gate B 的覆盖率查询全部为 0——但每一步都"成功"。

已在 M2-06 补上激活，顺序严格按计划：

```text
SetEmbedding（向量落库）
-> ActivateDocuments(新版本, true)      # 先开新的
-> SupersededDocumentIDs(找出同组旧版本)
-> ActivateDocuments(旧版本, false)     # 再关旧的
```

「先开后关」不是风格问题：先关会在失败时留下一个**没有活跃版本**的组，
该餐厅该 doc_type 直接检索不到。计划里说的"检索黑洞"就是这个。

为此新增 `KnowledgeStore.SupersededDocumentIDs`：哪些旧版本被顶替，
取决于文档所属的 `(restaurant_id, scope, doc_type)` 组，调用方拿 id 算不出来。

### E.11 memory 的 `identity` 表达不了"组"

memory adapter 的幂等键 `identity` 含 `contentHash`，所以同一事实的两个版本
在它眼里是**两个不同的 identity**。用 `identityOf` 写 supersession 判定会得到
"没有任何文档被顶替"——因为新旧版本的 identity 天然不相等。

已引入独立的 `group`（不含 `contentHash`）。两者的区别是硬性的：
**identity 决定"是不是同一篇"，group 决定"是不是同一件事"**。
幂等性用前者，版本切换用后者。混用会让 supersession 永远失效且不报错。

### E.12 可选过滤器的占位符编号会留下空洞（已修）

`VectorSearch` 的 borough / restaurant_id 都是可选的。第一版按固定编号写
（scope=`$1`、borough=`$2`、vector=`$3`、restaurant=`$4`），
**borough 为空时 `$2` 无人引用但仍然绑定**，PostgreSQL 直接拒绝：

```
could not determine data type of parameter $2
```

这个报错指向一个编号而不是本该使用它的 `AND borough = ...` 子句，
排查成本很高。已改为**边拼子句边编号**（`bind` 闭包），
保证语句里出现的每个 `$n` 都有对应参数、每个参数都被引用。

顺带把 SQL 构造抽成 `vectorSearchStatement`，这样编号规则可以**不连数据库**就测：
`knowledge_sql_test.go` 用正则抽出所有 `$n`，双向断言"无空洞、无未引用"，
四种过滤组合各测一遍。这类 bug 只有真跑一次才暴露，
但它的成因是纯字符串逻辑，不该被数据库的可达性绑住。

### E.13 `--workers` 被解析但从未使用（已修）

`EmbedOptions.Workers` 有解析、有默认值、usage 里也写着"embedding concurrency"，
但 `embedDocuments` 是**纯串行**的 for 循环。计划 §M2-06 明确要求"并发"，
验收标准还有一条"并发 4 与并发 1 的总耗时对比：并发 4 更快（证明并发确实生效，不是空转）"——
这条验收在串行实现下永远无法通过，而它本来正是用来抓这种"参数存在但没接线"的。

已实现 worker pool（`embedPageConcurrently`）。并发度按 `Workers` 而不是按批次数封顶：
provider 是本地 CPU 模型，多发请求不提高总吞吐，只会让每个请求更慢、内存占用更高。

### E.14 并发 worker 会互相把对方的文档下线（已修）

第一版把「激活 + 版本顶替」放在**每个 batch 内部**。但 batch 不是 group：
同一页里多个 batch 经常属于**同一个** `(restaurant_id, scope, doc_type)`
（例如一家餐厅的 9 篇文档按 batch=2 切成 5 批，全是同一个 group）。

于是每个 worker 在激活完自己的文档后，去下线"同组的旧版本"，
而下线的对象正是**其他 worker 刚写好的新文档**。
9 篇文档的测试结果是 `embedded=9` 但 **0 篇活跃**——而且没有任何报错。

已改为**整页 join 之后再切换**（`activatePage`）：

```text
各 worker 并发：SetEmbedding（只写向量，不碰 is_active）
-> join
-> VectoredDocumentIDs(筛出真正拿到向量的)
-> ActivateDocuments(新, true)
-> SupersededDocumentIDs(同组旧版本)
-> ActivateDocuments(旧, false)
```

同时新增 `VectoredDocumentIDs`：切换前必须知道**哪些文档真的拿到了向量**，
被质量门拒绝的文档没有向量，`ActivateDocuments` 会拒绝它（schema 的 CHECK 也一样），
所以不能把整页无脑传进去。

配套的三个并发测试都用 `-race` 跑过：
峰值并发 ≥ 2（证明真的重叠而不是空转）、串行与并行的结果一致、
以及**并发下的拒绝仍然进批次报告**——每个 worker 用自己的 collector 缓冲，
join 后统一并入 run 的 collector，否则并发路径的拒绝会全部丢失。

### E.15 `--force-model-change` 只是跳过了检查，没有真的切换（已修）

计划 §M2-06 对模型切换写得很具体：

```text
把所有 embedding_model <> 当前模型的活跃文档置 is_active = false
-> 重新生成文档
-> 重新 embedding
```

原实现只有 `if opts.ForceModelChange { return nil }`——**纯粹跳过守卫**。
跳过后旧模型的文档仍然 `is_active = true`，run 继续用新模型写入新文档，
最后索引里**两个模型的活跃文档并存**。这正是守卫存在的目的：
不同模型的余弦距离语义不同，混在一起返回的分数无法解释，而且没有任何东西会报错。

已按计划实现真正的切换：新增 `DeactivateStaleModels`，
`--force-model-change` 时把旧模型的活跃文档置 `is_active = false`。
行和向量都保留——它们是历史，不是待办，且切换前签发的引用仍要能解析。

### E.16 模型切换后，每次运行都会被守卫挡住（已修）

`DistinctEmbeddingModels` 原本统计**所有**有向量的行，不管是否活跃。
E.15 把旧模型下线之后，它依然在清单里，于是下一次 `embed`
（哪怕配置已经完全正确）仍然报 `embedding_model_mismatch`。

模型切换因此变成一次性的：第一次能跑，第二次开始永远失败。
已加 `AND is_active`。守卫要回答的是"**活跃集合**里有没有冲突模型"，
已下线的历史正是它应该忽略的东西。

`--force-model-change` 因此是**三步**而不是一步：
强制切换 -> 重新 `build-documents`（生成新版本）-> 再 `embed`。
被下线的文档带着向量，不会重新变成 pending；替换它是重建的职责。
这一点写在 `PendingDocuments` 与 `DeactivateStaleModels` 的接口注释里，
因为"为什么重跑 embed 补不回来"不是看代码能猜到的。

### E.17 JSON 无法承载 NaN/Inf（见 §M2-01）

`encoding/json` 序列化 NaN/Inf 直接失败，所以合规服务端不可能把它们发上线；
而 JSON 里合法的 `null` 分量会被**静默解成 `0.0`**。
结论：Ollama 路径上真正可达的守卫是**全零向量检查**，不是 NaN 检查。

### E.18 批次中途失败会留下"有向量但没激活"的孤儿（已修）

这是写 M2-06 最后两条验收标准的测试时才发现的，也是 E.10 的续集。

E.10 补上了"拿到向量就激活"，但激活被放在了 page 循环里 `err != nil` 的**之后**：

```text
embedPageConcurrently(...)   // 批 1 写入 3 个向量，批 2 provider 报错
if err != nil { return }     // ← 直接返回
activatePage(ctx, stores, page)   // ← 永远到不了
```

page 是分批写的，后一批失败**不会回滚**已经写入的前几批。于是这些文档的状态是
`embedding IS NOT NULL AND is_active = false`。这个状态是**不可恢复的**：

- 它们已经离开 pending 集合（谓词是 `embedding IS NULL`），所以重跑捞不到；
- 它们不在任何 HNSW 索引里（6 个索引谓词都含 `is_active`），所以检索不到；
- 它们的向量已经写进库里，所以既没有报错也没有告警。

也就是说一批文档被永久写进了"存在但不可检索"的状态，而批报告里 `embedded=3` 显示一切正常。

已把 `activatePage` 移到错误检查**之前**。它是幂等的，且内部先用
`VectoredDocumentIDs` 把 page 收窄到"真正拿到向量的"，所以在半完成的 page 上调用
只会激活成功的批次，失败那批仍然留在 pending 集合里等下一轮。

回归测试 `TestRunEmbedResumesAfterAMidRunFailure` 断言三件事：
部分运行后有文档是"活跃且有向量"的（而不是只写了向量）；
续跑时 provider 被问到的文档数恰好等于还没 embedding 的数量（已完成的**没有重算**）；
最终没有"活跃但无向量"的文档。

> 顺带修正一个此前写错的测试假设：fixture 把 9 篇文档放进同一个
> `(restaurant, scope, doc_type)` 组，而 supersession 的语义是组内版本切换，
> 所以"9 篇全活跃"从来不是正确预期。测试因此改为断言不变式
> （无孤儿、活跃必有向量）而不是具体活跃数。

### E.19 索引测试跳过时整体报 PASS（已修）

`indexSearchConn` 在数据库不可达时 `t.Skipf`，沿用 M1 契约套件的模式。
开发时这是对的（本地 `go test ./...` 不该因为没起库而红），**但对 Gate B 是错的**：
"套件通过"和"套件从没跑过"的退出码相同，一个分不清这两者的门不是门。

已改为 `requireDatabase`：默认仍跳过（本地循环保持快），
但 `PLATEPILOT_REQUIRE_DB=1` 时**硬失败**并提示启动命令。
两种模式都已实测：默认报 `ok`（附 skip 说明），`PLATEPILOT_REQUIRE_DB=1` 报 `FAIL`。

> 恢复额度后跑 Gate B **必须**带 `PLATEPILOT_REQUIRE_DB=1`，
> 否则这四条索引测试即使一行没执行也会显示为通过。

### E.20 索引断言用子串匹配，会假阳性（已修）

`assertPlanUses` 原来用 `strings.Contains(plan, indexName)`。
`knowledge_documents_hnsw` 是 `knowledge_documents_hnsw_manhattan` 的**前缀**，
所以 `TestUnfilteredQueryUsesTheGlobalIndex`（断言全局索引）在规划器实际选了
某个 borough 索引时也会**通过**——而"无 borough 过滤时不会掉进 borough 索引"
正是这条测试存在的理由。假阳性比失败更危险：它让一条从没验证过的性质显示为已验证。

已改为 `planIndexNames` 按 token 精确提取 `using <index>` 后的名字。
解析逻辑本身有合成 plan 的单元测试（`TestPlanIndexNames` 四个子用例 +
`TestGlobalIndexAssertionRejectsBoroughIndex`），不连库就能验证：

| 合成 plan | 提取结果 |
|---|---|
| `Index Scan using knowledge_documents_hnsw_manhattan on ...` | `[knowledge_documents_hnsw_manhattan]` |
| `Index Scan using knowledge_documents_hnsw on ...` | `[knowledge_documents_hnsw]` |
| `Seq Scan on knowledge_documents` | `[]` |
| `BitmapAnd` 下两个 Index Scan | 两个索引名 |

> 写这个解析器时它第一版把 `on knowledge_documents` 里的**表名**也当成索引名收进来了，
> 于是任何 plan 看起来都"用过索引"。是上面的合成测试抓到的，不是 review 看出来的。

### E.21 EXPLAIN 断言里传了 3 维向量（已修）

`knowledge_index_test.go` 的三处 `EXPLAIN` 都把查询向量写成 `"[1,0,0]"`，
而列声明是 `vector(1024)`。**这个测试从未真正验证过索引选择**：

- EXPLAIN 虽然不执行查询，但仍要绑定操作数，3 维字面量撞上 `vector(1024)`
  会被 PostgreSQL 直接拒绝；
- 失败会走 `t.Fatalf("EXPLAIN: %v", err)` —— 测试**红**，而不是报告它想验证的 plan；
- 也就是说这四条索引测试在真实库上第一次跑就会全部失败，且失败原因与索引无关。

已改为 `indexVectorLiteral(t, 0)`，用与 seed 相同的 `indexVector` 渲染成 1024 维
pgvector 字面量。字面量仍然是**参数化绑定**的，没有拼进 SQL 字符串。

### E.22 同批同组文档拿到相同版本号（已修）

M2-05 明确写了 `(restaurant_id, scope, doc_type)` 到文档是 **1:N**
（"同一家餐厅可能有两个 `restaurant_hours` chunk"），
而 `next_version` 的写法是相关子查询：

```sql
SELECT max(d.version) + 1 FROM knowledge_documents d
 WHERE d.restaurant_id = i.restaurant_id AND ...
```

子查询只能看到**语句执行前已在表里**的行。一批新建的同组文档彼此都看不见，
于是**每一篇都拿到同一个版本号**。

没有任何东西会拒绝这种数据：`version` 列没有唯一约束，而这几篇的
`content_hash` 各不相同，所以幂等键也拦不住。写入成功、批报告 `inserted=3`、
没有任何警告——直到 Gate B 附录 C 第 7 条查询（组内版本号不得重复）才会暴露。

memory adapter 恰好躲过了：它是逐行插入的 Go 循环，每篇插入后 `nextVersionFor`
就能看到前一篇。**两个 adapter 对同一份契约给出不同结果，正是契约套件要抓的东西**，
所以先补了契约断言（`runSameGroupBatch`）再去修 postgres。

已改为窗口函数，让每一行看到同批中排在自己前面的行：

```sql
), next_version AS (
    SELECT g.ord,
           coalesce(e.max_version, 0)
               + row_number() OVER (
                   PARTITION BY g.restaurant_id, g.retrieval_scope, g.doc_type
                   ORDER BY g.ord
               ) AS version
    FROM (...) g
    LEFT JOIN (
        SELECT d.restaurant_id, d.retrieval_scope, d.doc_type,
               max(d.version) AS max_version
        FROM knowledge_documents d
        GROUP BY d.restaurant_id, d.retrieval_scope, d.doc_type
    ) e ON ...
)
```

`existing_max` 仍然不带 `is_active` 过滤（E.16 那条：组内全部退役后不能把
版本号 2 再发一次）。

**验证过程中的一个教训**：这个断言的第一版是
`strings.Contains(sub, "incoming") || strings.Contains(sub, "row_number()")`。
退回旧 SQL 后测试**依然通过**——因为 `FROM incoming i` 里就有 "incoming"，
判据命中了一个每个版本都有的词。改成要求"存在按批内位置编号的窗口函数"，
旧写法才真的 FAIL。**假阳性的测试比没有测试更危险**，它会把没验证过的性质
显示为已验证。

为此把 `UpsertDocuments` 里的内联 SQL 提成包级常量 `upsertDocumentsSQL`，
测试直接读它——否则测试里存一份 SQL 副本，副本与真实语句各改各的，
静态断言就变成了对影子的断言。

### E.23 文档哈希复用了评论的归一化规则，抹平了段落结构（已修）

M2-02 明确写了归一化"要**极窄**且可测"，并列了四条：统一换行、折叠行尾空白、
3+ 连续空行折叠为 2 个、去首尾空白；并明写"**不做**小写化、不做标点归一、
不做全角半角转换"。

`hash.go` 里 `normalizeForHash` 直接调用了 `curate.NormalizeText`，而它是：

```go
func NormalizeText(text string) string { return strings.Join(strings.Fields(text), " ") }
```

这把**所有**空白折叠成单个空格并删掉换行。对评论去重这是对的——"同样的词"
正是判断两条评论重复的依据；对文档则是错的：**文档的换行结构承载信息**。

后果是静默的数据丢失。一篇逐行列出营业时间的文档被压成一行后，
**另一篇词句相同但排版不同的文档会拿到同一个 `content_hash`**，
而 `content_hash` 是幂等键的一半，于是后者被 `ON CONFLICT DO NOTHING`
当成"已存在"吞掉。它不是写坏了，是根本没被写进去，也没有任何计数异常。

已按计划的四条重写 `normalizeForHash`：CRLF/CR → LF、逐行去行尾空白、
4+ 连续换行折叠为 3 个（3 个空行 → 2 个空行）、`TrimSpace`。
不做其它任何变换。

**原有测试把错误行为固化成了期望**：
`TestContentHashIgnoresWhitespaceOnlyChurn` 断言 `"Mon-Fri  11:00-22:00"`（行内双空格）
与单空格版本哈希相同——但计划四条规则里**没有**折叠行内空白这一条，
所以正确行为是哈希**改变**。这条期望本身就是上一版实现泄漏出来的。
已改正，并补上三条断言：

- `TestContentHashPreservesLineStructure` — 两行文档与同样文字排成一行，哈希必须不同
- `TestContentHashCollapsesRunsOfBlankLines` — 3+ 空行折叠到 2 个
- `TestContentHashDistinguishesBlankLineCounts` — 0/1/2 个空行三者互不相同
  （否则这条规则和评论那条一样，属于过度归一化）

退回旧实现后 `TestContentHashPreservesLineStructure` 确实 FAIL，已验证可证伪。

> **对存量数据无影响**：M2 的文档构建从未全量跑过（§6.4），
> `knowledge_documents` 目前没有真实语料，所以改归一化规则不会造成版本膨胀。
> 若将来库中已有文档，换规则意味着所有 `content_hash` 改变、
> 每篇都会生成"新版本"——那时需要先 truncate 或配合 `--force-model-change` 式的重建。

### E.24 并发 worker 各自持有重复向量集合，跨批次的重复全部漏掉（已修）

M2-08 的重复检测集合 `seen` 原本是 `embedPending` 的**局部变量**。
串行时它覆盖整页的全部 batch，所以工作正常；`embedPageConcurrently` 里
每个 worker 各自调用一次 `embedBatch` → `embedPending`，
**于是每个 worker 拿到一个独立的 `seen`**。

后果：一个恒定返回同一向量的 provider，在 9 篇文档 / batch 2 / workers 4 下
会写入 **5 个**相同向量（每个 worker 的第一批各留一个），只有 worker 内部
检测到了 2 个重复。批报告里每个 batch 都成功，没有任何异常计数——
而 Gate B 扩充分项要求"重复均为 0"。

这正是 E.14 的并发教训在另一处的复现：E.14 是 worker 互相把对方的文档下线，
这次是每个 worker 的去重视野只覆盖自己那一段。**两者都只在 `--workers > 1`
时出现，而串行是默认值**，所以默认配置下永远测不出来。

已把 `seen` 提升为 `duplicateSet`，由 `embedPageConcurrently` 创建一次、
所有 worker 共享：

```go
type duplicateSet struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func (d *duplicateSet) add(fingerprint string) bool { // 检查与插入在同一把锁下
```

检查和插入必须在**一次加锁**内完成。分开写的话两个 worker 会同时观察到
"不存在"并各自保留自己的向量——那正是这个类型要防的情况。
用 `sync.Mutex` 而不是 `sync.Map` 也一样重要：即使每个 key 都不同，
多 goroutine 写普通 map 本身就是数据竞争。

回归测试 `TestRunEmbedRejectsDuplicatesAcrossWorkers`
（9 篇 / batch 2 / workers 4，断言 `embedded=1`、`rejected=8`，
且审计表里 `embedding_duplicate` 计数为 8）。
退回"每 worker 一个 set"后该测试确实 FAIL（`embedded = 5`），已验证可证伪。
`-race -count=3` 无竞争。

### E.25 `embed --restaurant-id` 在真实数据上永远是空转（已修）

用覆盖率找出来的一段代码里叠了两个缺陷，任何一个单独存在都足以让这条命令
"成功但什么都不做"。

**第一个**：`ListByRestaurant(ctx, id, "")` 传空 scope，而
`ListByRestaurant` 是**精确匹配** scope 的——`doc.Scope == scope`。
空串不匹配任何 scope，于是**每一家餐厅都返回空列表**。

**第二个**：`if doc.IsActive && len(doc.Embedding) == 0` 里的 `doc.IsActive`。
这正是 E.6 的同款缺陷：M2-05 的四步切换要求新文档以 `is_active = false` 落库
（表级 CHECK 也禁止"活跃但无向量"），所以真实数据里**所有待 embedding 的文档
都是 inactive**。批量路径的 `PendingDocuments` 早就改成了 `embedding IS NULL`
不看 `is_active`，这个函数却漏改了。

两者叠加的结果：`embed --restaurant-id=123` 报告 `documents=0 embedded=0`、
退出码 0、批次记 `succeeded`——而这是运维在排查单个餐厅时最常用的命令。

顺带发现第三点：这个函数也缺 `activatePage`，即 E.18 修的"写了向量不激活"。
已一并补上，且**同样在错误检查之前**调用——批次成功的部分必须被激活，
失败的留给下一轮。

修好后 `ListByRestaurant` 被显式调用两次（`ScopeRestaurant` + `ScopeEvidence`），
因为一个餐厅两种 scope 都有文档。

回归测试 `TestRunEmbedOneRestaurantTouchesNothingElse` 断言：
目标餐厅 4 篇全部写入向量、**另一家餐厅的 4 篇一个都没被碰**。
退回原写法后该测试 FAIL（`embedded = 0`），已验证可证伪。

### E.26 build-documents 的原有测试只断言审计行，从不检查产出（已修）

覆盖率显示 `buildEvidenceDocuments` 与 `selectRestaurants` 是 **0%**。
补测试时发现原因，而且比"漏写测试"更糟：

`stageStores` 的 fixture 用 `UpsertRestaurant` 插入餐厅，**新插入的行
`is_active_for_demo` 为 false**，而 `selectRestaurants` 只取 demo 集合。
所以那些测试里的 `build-documents` **一篇文档都没构建过**——
`TestRunBuildDocumentsRecordsABatch` / `...RecordsAFailedBatch` /
`...DryRunWritesNoBatch` 断言的全是审计行，而审计行在一个空转的阶段里
同样会被正确地写出来。

已验证：把 fixture 的 demo 标记去掉后，那 5 个测试**依然全部通过**。

这比假阳性的断言更隐蔽——断言本身没错（批次确实被记录了），
缺的是"产出非空"这一半。`is_active_for_demo` 需要 `UpdateScores` 同时
传入 scores 与 active 两个 map，只传 active 是无效的（循环遍历的是 scores），
这个坑也写进了 fixture 的注释。

新增 5 条测试，全部检查**实际产出**而非仅审计行：

- `TestBuildEvidenceDocumentsProducesChunksAndMarksReviews` — 产出 evidence 文档、
  文档在 embed 前必须 inactive 且无向量、`is_representative` 的置位数与运行报告一致
- `TestBuildDocumentsRerunWritesNothingNew` — Gate B 点名要的幂等：重跑不新增行
- `TestBuildDocumentsPagesThroughEveryRestaurant` — `BatchSize=1` 时每家餐厅恰好一次
  （一页覆盖全部时暴露不出的 off-by-one）
- `TestRunEmbedOneRestaurant*`（两条）— 见 E.25

pipeline 包覆盖率 73.3% → 80.4%。

### E.27 `SupersededDocumentIDs` 会把调用方自己列出的行当成"被顶替"（已修）

`activatePage` 传的是**整页**文档 id，而 `SupersededDocumentIDs` 原来只排除
"一个不同的 document_id"（memory 是 `contains(docIDs, id)` 的反面写法，
postgres 是 `d.document_id <> n.document_id`）。

于是一页里同组的兄弟文档被当成"调用方没列出"而被返回，随后被
`ActivateDocuments(..., false)` 下线——**刚写完向量就自己关掉**。

这是 E.14 的另一处变体。E.14 是并发 worker 互相把对方的文档下线，
这次是"排除自己"这个谓词在整页语义下不成立。两个 adapter 一致地错，
所以契约套件也测不出来：契约里只传单行（`[]int64{v2}`），
一页多行的情形没有任何断言覆盖。

已把排除条件改为**集合成员关系**（memory 用 `member` map，postgres 用
`NOT EXISTS ... WHERE incoming.document_id = d.document_id`），
并顺带删掉了 memory 里因此不再被调用的 `contains` 辅助函数。

契约新增断言：两篇同组文档都活跃时，
`SupersededDocumentIDs(ctx, []int64{v1, v2})` **不得**返回 v1 或 v2。

**端到端回归 `TestRunEmbedKeepsEverySiblingOfAGroupLive`**：一家餐厅的三条
`restaurant_hours` 文档放在同一页里 embedding，断言三篇**都**有向量且**都**活跃。
故障注入实测：修复前 `live = 1, want 3`——三分之二的语料写进了向量却不可检索，
而阶段报告每个批次都成功。这条测试比契约层的单元断言更能说明后果：
Gate B 的「≥5,000 条 evidence」会直接少三分之二，且没有任何错误提示。

> 这条断言第一版被我放错了位置——放在"两篇都还没激活"的那一段。
> 那里 `d.is_active` 为假，函数在成员判断之前就 `continue` 了，
> 于是退回旧实现测试**依然通过**。移动到两篇都活跃之后才真正生效，
> 退回旧写法时确实 FAIL（返回了调用方列出的 v1）。

### E.28 `CodeEmbeddingDuplicate` 是一个不可达的错误码（已修）

`embedPending` 记录重复向量的拒绝原因时用的是**字符串字面量**
`"embedding_duplicate"`，而质量门的所有其它原因都走
`string(errs.CodeOf(err))`——也就是错误码常量。

于是 `errs.CodeEmbeddingDuplicate` 这个常量**在生产代码里没有任何产生点**：
它被声明、被映射到 HTTP 状态（422）、出现在错误码列表里，
但没有任何一行代码 `return errs.New(errs.CodeEmbeddingDuplicate, ...)`。
重复检测本身是直接用 `seen.add()` 的布尔返回值走的，根本不经过错误体系。

风险不在于现在会出错，而在于 `reject_reasons` 是审计表里给
人看的 map，键就是这些字符串。字面量与常量一旦漂移（例如有人"修正"拼写），
同一个原因会被拆成两个各自计数、永远不相加的键，而报告里看不出来。

已改为 `var duplicateReason = string(errs.CodeEmbeddingDuplicate)`，
两处使用同一个变量，字面量不再存在。

新增 `TestEveryEmbeddingCodeIsProducedByTheQualityGate`：对 taxonomy 里
每一个 embedding 码构造出**应当触发它的向量**，断言 `knowledge.Check`
真的返回那个码。这比"检查常量非空"强得多——移除任何一条规则
（比如 Inf 检查）测试立刻失败并指名是哪个码没了：

```
去掉 Inf 分支 → Check accepted a vector that should raise embedding_inf
```

`CodeEmbeddingDuplicate` 在该测试里被单独列出并注明理由：
重复是**批次**的属性而不是单个向量的属性，所以质量门不产生它，
由 stage 产生。写清楚这一点比把它塞进 `Check` 更诚实。

### E.29 `EMBEDDING_MAX_BATCH` 在计划里被写了两遍，代码里一次都没有（已修）

计划在三处要求这个配置项：§M2-01 写"以及可选的 `EMBEDDING_MAX_BATCH`
（默认 32）"，§M2-06 写"批量大小：`EMBEDDING_MAX_BATCH`（默认 32）"，
附录 A 把它列进了环境变量清单。三处都把它当作**已存在**的东西。

代码里不存在。`ParseEmbedOptions` 用的是包内常量
`const defaultEmbedBatch = 32`，`EmbeddingConfig` 没有对应字段，
`Loader.Embedding()` 不读这个环境变量。

这条在别的条目里排第 29 而不是更靠前，是因为它**不导致错误结果**，
只导致计划与实现不一致：跑 `--batch=64` 能工作（flag 路径是真的），
但改 `EMBEDDING_MAX_BATCH=64` 不起作用。危险在于排查顺序——运维看到
吞吐不理想时，计划告诉他"调 `EMBEDDING_MAX_BATCH`"，他调了，
重启，什么都没变，于是去调一个更深的参数或干脆认为计划不准。

而且它卡在计划自己指定的动作上：§M2-06 写"批量大小直接影响吞吐，
必须实测后固定"，附录 A 写"需实测后固定"。**实测的前置条件就是
能改这个值而不重新编译**，硬编码常量让这句话无法执行。

已改为：`EmbeddingConfig.MaxBatch int` + `DefaultMaxBatch = 32` +
`BatchSize()`（0 视为未设置，返回默认值），`Loader.Embedding()` 读
`EMBEDDING_MAX_BATCH`，`ParseEmbedOptions` 默认值改用
`cfg.Embedding.BatchSize()`，删除 `defaultEmbedBatch`。`--batch`
仍然覆盖配置，这样单次实测不必改环境变量。

顺带加了范围校验 `0 < MaxBatch <= 2048`：0 是"未设置"哨兵所以放行，
负数与超大值报错而不是静默钳制。上界存在的原因是整个 batch 是**一个
JSON 请求体**，批大小同时也是服务端单次读取的文本量上限，越界的表现
是服务端拒绝而不是变慢——那看起来像一次线上故障。

两个方向各做了一次故障注入确认断言可证伪：
退回硬编码常量 → `TestEmbedOptionsBatchSizeComesFromConfig` 变红；
把 `BatchSize()` 改成直接返回 `MaxBatch` →
`TestEmbeddingBatchSizeFallsBackToDefault` 变红（未设置时得到 0，
而 0 会在切分循环里空转）。

### E.30 `check-config` 不显示 E.29 新增的旋钮（已修）

补完 E.29 之后回头看运维路径，发现新配置项只走通了"能被读到"，
没走通"能被确认"。两处都缺：

- `.env` / `.env.example` 里没有 `EMBEDDING_MAX_BATCH`（只有计划附录 A 有）；
- `ConfigSummary`（`check-config` 的输出）不打印它。

（`--help` 不提它**不算缺陷**：usage 只列 flag，从不列环境变量，
`EMBEDDING_MODEL` / `EMBEDDING_DIMENSIONS` 也都不在里面。环境变量的
文档位置是 `.env.example` 和计划附录 A。）

第二处是要害。`check-config` 的作用正是"确认配置生效了"，而确认
`EMBEDDING_MAX_BATCH=64` 有没有生效，现在只能靠跑一批真实 embedding
并计时——而这恰恰是 M2-06 还没做完的那件事。于是新旋钮的验证成本
和它要优化的那个指标一样高，实际等于不可用。

已把 `batch=%d`（用 `BatchSize()` 取**生效值**而非 `MaxBatch`）加进
`ConfigSummary`，并补 `.env` / `.env.example`。

**断言本身踩了一次假阳性，值得记下来。** 第一版断言是往已有的
substring 列表里加一个 `"batch=64"`，然后做故障注入：把
`BatchSize()` 换成 `MaxBatch`，测试**照样通过**。原因是测试里设了
`MaxBatch=64`，两个实现输出完全相同——断言无法区分。

第二版把配置**故意留空**（`MaxBatch=0`），此时正确实现输出
`batch=32`，错误实现输出 `batch=0`，才真正区分得开。这个版本对
两个故障都变红了：

```
打印 MaxBatch 而非 BatchSize → unset batch should report the default 32, got ... batch=0
从摘要里删掉该字段            → configured batch should be reported, got ...
```

这也解释了为什么 `batch=0` 不只是"不够好"而是**有害**：运维在
`check-config` 里看到 `batch=0`，第一反应是 pipeline 有 bug，
而不是"这个旋钮没设"。留空配置才让两个实现产生分歧——这是
本项目第四次因为"断言放在有值的状态上"而写出假阳性断言
（前三次见 E.20 / E.22 / E.27）。

### E.31 M2-01 的传输层测试全部依赖 socket，等于没有覆盖（已修）

`shared/adapter/embedding/ollama` 有 18 个测试，其中 11 个用
`httptest.NewServer` 起真实监听器。沙箱禁止 `listen()`（`bind: operation
not permitted`），这 11 个**从来没跑过**。

被盖住的是整个 M2-01 传输契约：批量顺序、错误分类（429/5xx 可重试、
4xx 不可重试）、重试上限、调用方取消不重试、数量/维度不匹配。
`go test ./...` 里那个"唯一失败项"看起来像环境问题，实际是**这条契约
在 CI 上零覆盖**——而它是 M2-06 批量向量化的地基。

`Options.HTTPClient` 这个 seam 本来就在（`client.go` 的 `HTTPClient` 字段），
只是测试没用。补了 `roundTripperFunc` / `jsonResponse` / `offlineClient`
/ `echoTripper` 四个无 socket 的辅助函数，8 个关键用例改为直接驱动
`http.RoundTripper`。httptest 版本保留——有 socket 的环境两套都跑，
互为对照。

**比 httptest 更严的地方**：RoundTripper 能断言请求的**精确内容**并回放
**精确响应体**，而 handler 做不到。所以新用例能钉住"一个 4 文档批次
只发一次请求"这类契约，这在真实服务器上只能靠数 `calls` 近似。

故障注入（每条都验证过会红）：

| 注入的错误实现 | 变红的测试 |
|---|---|
| 每篇文档发一次请求（打掉批量） | 4 个（含 `SendsOneRequestPerBatch`） |
| 4xx 也重试 | `DoesNotRetryClientErrors` |
| 去掉重试上限 | `GivesUpAfterMaxRetries` |
| 调用方取消也重试 | `CallerCancellationIsNotRetried` |

**"去掉重试上限"这一条暴露了断言写法的问题**：第一版断言只比对
`calls == 3`，注入后测试**挂死**到包级 timeout（90s）才失败，而不是
立刻失败。挂死也算"能发现"，但没人会等 90 秒——CI 上这看起来就像
卡住了。改成：tripper 里设 `maxCalls = 16` 上限并带 2s context，
于是无限重试在 2 秒内报 `attempts exceeded 16: the retry loop is not
bounded`。**断言不仅要能判错，还要能快速判错。**

### E.32 `REQUEST_TIMEOUT=15s` 对真实文档不够，M2-06 在真实数据上必然超时（已修默认值）

M2-06 的验收摘要写"可对样本批次生成并写入向量"。跑真实数据时，
200 篇 profile 文档（每篇约 738 字符）用 17.6 秒跑完，看着没问题；
换到 `restaurant_representative_reviews` 就炸了：

```
{"documents":500,"embedded":436,"failed":64}
provider_unavailable: ollama: request failed: context deadline exceeded
```

原因是两类文档的体量差了一个数量级。实测本机（`qwen3-embedding:0.6b`，
Q8_0，CPU 推理）：

| 文档类型 | 篇数 | 平均字符 | 最长字符 |
|---|---|---|---|
| `restaurant_representative_reviews` | 3000 | 2878 | 8615 |
| `restaurant_profile` | 2364 | 738 | 1359 |
| `restaurant_attributes` | 3000 | 364 | 920 |
| `restaurant_hours` | 2842 | 107 | 185 |

用**最长的 32 篇**（共 234,997 字符）逐档实测单次请求耗时：

| batch | 字符数 | 耗时 | 15s 超时下 |
|---|---|---|---|
| 4 | 33,062 | 6.6s | 通过 |
| 8 | 64,457 | 14.7s | 勉强通过 |
| 16 | 123,768 | 28.3s | **超时** |
| 32 | 234,997 | 52.2s | **超时** |

**关键发现：吞吐与 batch 大小基本无关**（36 / 33 / 34 / 37 篇/分钟）。
本地 CPU 模型不会因为一个请求里多塞几篇就并行处理，所以
"批量越大越省往返"这个假设在这台机器上**不成立**——加大 batch 只是
线性拉长单次请求，直到撞上超时。

因此正确的应对不是调小 batch，而是把单次请求的超时提到覆盖最坏情况。
`REQUEST_TIMEOUT` 默认从 15s 提到 180s。这也让 E.29 新增的
`EMBEDDING_MAX_BATCH` 有了实测依据：它决定单次请求的字符量，
必须和超时一起看，而不是单独调。

顺带记一条 M2-06 原本没写的前提：**代表评论文档的体量必须被当作容量
规划的输入**。它是唯一一类平均接近 3K 字符的文档，占了 8842 篇 evidence
里的 3000 篇，也就是全量向量化耗时的四分之一强。

### E.33 `SupersededDocumentIDs` 的自连接没有钉住调用方给的行（已修）

契约测试在真实库上第一次跑就红了：

```
contract.go:1084: superseded = [4 5 6 7 8 9 10], want none while no version is live
```

原来的 SQL 是这样把候选行和调用方的页联系起来的：

```sql
FROM knowledge_documents d
JOIN knowledge_documents n
  ON n.restaurant_id  = d.restaurant_id
 AND n.retrieval_scope = d.retrieval_scope
 AND n.doc_type        = d.doc_type
WHERE d.is_active AND d.document_id <> n.document_id
```

join 键全部取自 `d`，`n.document_id` 从头到尾没有和 `$1` 关联过。
于是这个自连接做的是"把每个活跃文档和**它自己那一组**里的每个文档配对"，
与调用方问的是哪一页毫无关系。实测：查 `restaurant_attributes` 的一页，
返回的却是 `document_id = 17`（一个 `restaurant_profile`），
因为 17 恰好是活跃的，而它和它自己组内的 6 个文档都能配成对。

后果正是 E.27 描述的那个，只是原因不同：调用方拿到一份"看起来像被顶替"
的清单，`activatePage` 老老实实把它 deactivate 掉。**索引存在、
查询能返回、结果全错**——这类缺陷在只有内存 adapter 的测试里不会出现。

改成以 `unnest($1)` 为驱动表：

```sql
FROM unnest($1::bigint[]) AS incoming(document_id)
JOIN knowledge_documents n ON n.document_id = incoming.document_id
JOIN knowledge_documents d ON d.restaurant_id = n.restaurant_id
                        AND d.retrieval_scope = n.retrieval_scope
                        AND d.doc_type        = n.doc_type
WHERE d.is_active
  AND NOT EXISTS (SELECT 1 FROM unnest($1::bigint[]) AS listed(document_id)
                  WHERE listed.document_id = d.document_id)
```

改的过程中还发现第二个问题：排除条件如果只写
`d.document_id <> n.document_id`，排除的是"配对上的那一行"，
而不是"调用方列出的整页"。同一组里有兄弟文档时（比如一家餐厅的
三段营业时间），它们会作为 `d` 被返回——而它们正要被这一页打开。
E.27 当时把排除谓词写成了整页语义，但 join 本身还是错的；
**两个缺陷叠在一起，才让 E.27 的修复在真实库上没生效**。

### E.34 `knowledgeColumns` 不含 `embedding`，退役文档因此"丢失"向量（已修）

契约测试的另一条红：

```
contract.go:1261: the retired document lost its vector
```

`DeactivateStaleModels` 只做 `SET is_active = false`，向量确实还在行里。
问题在读侧：`knowledgeColumns` 这个投影常量里**根本没有 embedding 列**，
`scanKnowledgeDocuments` 也就没有扫它。于是 `ListByRestaurant` 返回的
文档永远带一个空向量，调用方无从区分"这文档没向量"和"这文档的向量没被读出来"。

memory adapter 返回的是整个文档结构体（自带 `Embedding`），postgres 是
唯一一个"读了但不给"的实现。契约测试正是靠这个差异把它抓出来的。

修法：`knowledgeColumns` 加上 `embedding`，扫描目标用 `*string`
接住 pgvector 的文本形式，再用新增的 `parseVectorLiteral` 解析——
与写入端的 `vectorLiteral` 互为逆运算，pool 上不注册 pgvector 类型这件事
也就保持了原样（宽度仍然是 schema 的属性，不是 Go 类型的属性）。

`parseVectorLiteral` 对 NULL 和 `[]` 都返回 nil，这样"还没有向量"
和"向量是空的"仍然是两件可区分的事。`VectorSearch` 走同一份投影，
它多带一列 distance，所以 `scanScoredDocuments` 也要跟着多扫一个
`embeddingText`——漏掉的话会得到 `got 17 and 16 destinations` 这种
只在真库上才暴露的错。

### E.35 三个 HNSW 索引断言其实在断言 planner 的成本模型（已修）

`TestBoroughQueryUsesTheBoroughPartialIndex` 等三个测试在真实库上全部红了：

```
plan does not use knowledge_documents_hnsw_manhattan; it used []:
  -> Seq Scan on knowledge_documents
       Filter: (is_active AND (embedding IS NOT NULL) AND ...)
```

断言本身写对了（E.19/E.20/E.21 修的就是它），错的是**它问的问题太小**：

```go
const restaurantsToSeed = 500   // 实际只种了 500 行
const indexSearchSeedRows = 5000 // 注释说 5000，实际只当 slice 容量用
```

注释详细论证了"要 5000 行 planner 才会选索引"，而 `indexSearchSeedRows`
**只被用作容量和分页上限，从没当过行数**。真实行数是硬编码的 500。

实测这个 schema 上的真实交叉点（1024 维，`ANALYZE` 已跑）：

| 行数 | 全局索引 | borough 分区索引 |
|---|---|---|
| 500 | 顺序扫描 | 顺序扫描 |
| 5,000 | 顺序扫描 | 顺序扫描 |
| 10,000 | **HNSW** | 顺序扫描 |
| 50,000 | **HNSW** | **HNSW** |

borough 是最紧的约束：每个分区索引只覆盖五分之一行，而 pgvector 0.8
在 1024 维上建的索引相当大（5,000 行 → 39MB，而表本身只有 4.4MB），
随机读成本高到 planner 要等到表更大才愿意走它。

改成 `restaurantsToSeed = indexSearchSeedRows` 并把常量提到 50,000——
这个量级也正是 Gate B 真实语料的规模（3000 profile + ~8800 evidence），
所以断言的是**将要依赖的行为**，而不是一个人造的最优情形。

两个附带修正：

- `SetEmbedding` / `ActivateDocuments` 按 2000 一批分批调用。单次 50,000
  向量的 UPDATE 撞的是语句级超时（60s），分批之后 25 批全部通过。
  这也正是 pipeline 真实运行时的形状。
- 整个 fixture 改为 `sync.Once` 共享。种一次要 3 分钟，原来四个测试各
  种一次共 12 分钟；现在 1 次 setup + 3 个瞬间完成的断言。测试之间只读，
  互不干扰。

改完之后做了故障注入：把 borough 断言改成全局索引名，
`TestBoroughQueryUsesTheBoroughPartialIndex` 变红——说明它现在真的在
断言索引选择，而不是断言一个 500 行的表上顺序扫描是合理的。

**同一批测试里还藏着一个自己跳过自己的用例**：
`TestVectorSearchFindsTheExactMatch` 开头调 `PendingDocuments`，
而 fixture 里所有文档都已带向量都已激活，返回空 → `t.Skip("no seeded documents")`。
它一直是 SKIP，看起来无害，实际是 E.19 那一类问题的残留：断言放在
一个不成立的前提上，于是永远不执行。改成直接读一篇活跃文档。

### E.36 数据比代码早 8 小时：`ClassifyTopics` 上线时语料已经导完了（已修，非代码缺陷）

M2-04 验收要求四类 evidence chunk，补跑后实测只落地三类：

```
restaurant_attributes             2978
restaurant_hours                  2797
restaurant_representative_reviews 3000
restaurant_review_summary           0   ← 缺
review_summaries 表                  0 行
reviews.topic_tags            0 / 8,727,344
```

代码路径是完整的（`NormalizeReview` → `ClassifyTopics` → `reviewUpsertSQL`
→ `SummarizeTopics` → `BuildSummaryDocuments`），逐段review 没找到断点。
根因是时序：

| 时间 | 事件 |
|---|---|
| 09:06 | `import --stage=review` 导入 872 万条评论，**当时 `curate/topics.go` 还不存在** |
| 17:14 | commit `39d9017`，`ClassifyTopics` 首次进入代码库 |

`topic_tags` 在导入时逐条算好并落库，代码后来补上不会回填已有行。
每条评论的 `topic_tags` 都是 nil，于是 `SummarizeTopics` 分组为空、
`BuildSummaryDocuments` 无输入——**每一层都正确，只是输入是空的**。

修法是重跑 `import --stage=review`（upsert 覆盖，不删表，509秒），
再 `build-documents --scope=evidence` + `embed`。

**这次重跑推翻了两个此前的担心**：

1. **"`is_representative` 会被重置为 false"** —— 没有发生。review 重跑后
   `build-documents --scope=evidence` 重新执行了代表评论选择并写回，
   最终仍是 51,313 条。副作用被下游步骤自动覆盖。
2. **"文档 `content_hash` 会变化导致全部重新向量化"** —— 只有 profile 变了
   （v1 是评分回填前建的，v2 标题多了 `，4.4星`，见 `profile.go:281`）。
   其余三类 8,842 篇 `content_hash` 未变，被 `skipped`，原向量完好。

> **`build-documents` 默认只跑 restaurant scope**（`documents.go:55`）。
> 不带 `--scope=evidence` 会13 秒跑完、退出码 0、日志里只有
> `restaurant_profile 3000`——看起来像"跑完了"，实际evidence 一篇没建。
> 这个默认在运维路径上很容易误判，值得单独记一笔。

**幂等不是一次到位，是收敛**。补跑后连续执行 `embed`：

```
第1 次  documents=21598 embedded=21548 rejected=50
第2 次  documents=50    embedded=26   rejected=24
第3 次  documents=24    embedded=15   rejected=9
第4 次  documents=9     embedded=7    rejected=2
第5 次  documents=2     embedded=1    rejected=1
第6 次  documents=1     embedded=1    rejected=0
第7 次  documents=0     ← 收敛
```

50→24→9→2→1→0 不是缺陷，是 `activatePage`（`embed.go:795`）整页切换的
必然结果：新版本激活后，被它替换的旧版本才从"待嵌入"队列里退出来。
每轮都在处理上一轮替换掉的上一轮。**判断幂等要看连续两次 `documents=0`，
而不是第一次重跑就是 0。**

**50篇 `embedding_duplicate` 全部未激活**，符合设计：连锁店共用描述导致
多篇文档文本逐字相同，质量门拒绝重复向量。退役文档 131 条中 13 条无向量
（正是被替换掉、从未嵌入的旧版本），其余 118 条保留原向量。

**Q10 的一处误报值得记**：电话正则命中 1 条，内容是
`Where long time delivery service 0000000000000000[redacted-phone]`。
查源评论原文确认 Google 导出时即已脱敏为 `[redacted-phone]` 占位符，
**不是管线泄漏**。排除占位符后真实电话为 0。断言若要长期有效，
应写成`AND content NOT LIKE '%redacted-phone%'`，否则每次都会假红。


## 附录 A：环境变量（M2 相关）

```bash
# --- Embedding（M2 起必需）-------------------------------------------------
# Provider 选择，目前只有 ollama 一个实现
# EMBEDDING_PROVIDER=ollama
# OLLAMA_BASE_URL=http://localhost:11434
# EMBEDDING_MODEL=qwen3-embedding:0.6b
# EMBEDDING_DIMENSIONS=1024

# 单次 embedding 请求的批量大小（M2-06，需实测后固定）
# 生效范围 1..2048，0 或不设置等价于默认值；--batch 优先级更高
# 注意：本机实测吞吐与 batch 大小基本无关（见 E.32），调大它主要是在
# 拉长单次请求、抬高超时风险，不是提速手段
# EMBEDDING_MAX_BATCH=32

# 单次出站请求的超时（M2-06）
# 必须覆盖最长的一批文档：代表评论文档平均 2878 字符、最长 8615，
# 本机实测 32 篇约需 52s。默认 180s，不要调回 15s（见 E.32）
# REQUEST_TIMEOUT=180s

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

- `docs/platepilot-implementation-plan.md` §6 M2、§7 关键路径、§8 并行建议、§9 Gate B
- `docs/platepilot-technical-prd.md` §4.7 `knowledge_documents`、§5 Step 6–9
- `docs/platepilot-m1-task-document.md` §0.1 实现状态、§4.0 领域与接口（写侧端口约定）
- `shared/domain/evidence/evidence.go`（`KnowledgeDocument` / `Evidence` / `DocType`）
- `shared/port/embedding.go`（`EmbeddingProvider`）
- `shared/port/writer.go`（`RestaurantStore` / `ReviewStore` / `PipelineStore`，M2 追加 `KnowledgeStore`）
- `shared/adapter/repository/postgres/migrations/0001_init.sql`（`knowledge_documents` / `review_summaries` / HNSW 索引）
