# PlatePilot M3 两级检索任务文档

> 日期：2026-10-01
> 依据：`docs/platepilot-implementation-plan.md`（§4 里程碑总览、§6 M3、§7 关键路径、§9 Gate C、§10 完成定义）与 `docs/platepilot-technical-prd.md` v0.12（§6.3 两级召回边界、§6.4 Restaurant RAG 链路、§11 测试与评测）
> 里程碑目标：把 M2 已落库的 11,775 篇知识文档，变成**可过滤、可召回、可融合、可引用**的检索层——第一次让 `chat-service` 返回候选餐厅和带来源的佐证
> 退出条件（Gate C）：硬条件过滤正确率 100%；餐厅召回结果可解释；佐证不会跨餐厅返回；引用包含 source 和 observed_at

M3 是第一个**读取链路**里程碑。M0 建了端口，M1/M2 把写路径跑通，M3 才第一次真正消费它们。
因此本里程碑有两类工作：补齐读侧适配器（M3-01/M3-02/M3-03/M3-04 的存储部分），
以及在 `chat-service` 里建立检索应用层（M3-05/M3-07）。M3 不涉及 Agent、LLM 调用或 SSE。

存储层沿用 M1/M2 的自建 PostgreSQL（pgvector + PostGIS + pg_trgm），检索能力由
HNSW 部分索引、`pg_trgm` GIN 索引和结构化 B-tree 索引共同提供。

## 0. 如何使用本文档

- 本文档是 M3 的**可执行任务清单**，每个任务独立成节，包含目标、交付物、实现要点、依赖、工作量和可验证的验收标准。
- 任务粒度对齐实施计划的 M3 表格（M3-01 ~ M3-08）。任务 ID 与实施计划保持一致，便于交叉引用。
- 每个任务的**完成定义（DoD）**默认继承实施计划 §10：代码实现 + 测试 + 错误码 + `trace_id` 日志 + 不泄露密钥/PII + 不经领域接口直连厂商 API + 关键设计有注释 + 通过 `go test ./...`、`go vet ./...`、`golangci-lint` + 验收标准可实际演示。
- 本文档中的 Go 类型、接口签名和 SQL 是**约定形状**，允许在不破坏领域边界的前提下微调；一旦调整，必须同步更新本文件、`port` 接口和所有实现（含内存实现与契约测试）。
- 模块路径沿用 M0 约定：`github.com/zed/platepilot`。
- 服务边界沿用 M2：M3 **全部读路径**落在 `chat-service` 与 `shared/adapter`，**不得**在 `chat-service` 中写库；`data-pipeline` 与 `chat-service` 互不 import（`shared/domain/architecture_test.go:TestServicesDoNotDependOnEachOther` 强制）。

### 0.1 起点状态（2026-10-01，M0/M1/M2 已交付）

M3 直接建立在 M1/M2 的产物之上。启动 M3 前，以下已就绪，**M3 不重复实现，只做增量**：

| 起点产物 | 位置 | M3 如何使用 |
|---|---|---|
| `restaurants` 表（36,133 行，3,000 家 `is_active_for_demo`） | `migrations/0001_init.sql:24-100` | 结构化过滤的数据源 |
| `knowledge_documents`（11,775 篇活跃：3,000 profile + 8,775 evidence） | `migrations/0001_init.sql:217-249` | 两级召回的唯一文档源 |
| 全局 + 5 个 borough partial HNSW 索引 | `migrations/0001_init.sql:277-295` | M3-03/M3-04 的向量召回路径 |
| `restaurants_name_trgm` / `restaurants_address_trgm` GIN 索引（后者是 `WHERE address IS NOT NULL` 的 partial 索引） | `migrations/0001_init.sql:140-146` | M3-02 名称/地址模糊匹配 |
| `EmbeddingProvider` + Ollama 实现 | `shared/port/embedding.go`、`shared/adapter/embedding/ollama/client.go` | M3 查询向量化（在线同步调用） |
| `KnowledgeStore.VectorSearch`（写侧，已验证） | `shared/port/writer.go`、`postgres/knowledge.go:489` | SQL 逻辑可参考，但**读侧需新写**（见下） |
| `evidence` 领域 DTO | `shared/domain/evidence/evidence.go` | `Evidence` / `KnowledgeDocument` / `RetrievalScope` / `DocType` 已定义 |
| `search` 领域 DTO | `shared/domain/search/search.go` | `SearchQuery` / `RestaurantFilter` / `RestaurantCandidate` 已定义 |
| `RerankProvider` 端口 | `shared/port/rerank.go` | M0 已定义，M3-06 实现 |
| `MockRerankProvider` / `MockEmbeddingProvider` | `shared/testkit/mock_provider.go` | 离线测试用，M3 复用 |
| 错误码族（8 基础 + 8 embedding） | `shared/domain/errs/errs.go` | M3 追加 `retrieval_*` 族 |
| Hertz 骨架 + 中间件 + 统一错误响应 | `chat-service/internal/transport/httpapi/` | M3 挂载 `/v1/restaurants/search` 等只读端点 |

**必须知道的两个现状缺口**（不了解会写出编译不过或语义错的实现）：

1. **读侧端口无任何实现，且签名已漂移**。`shared/port/repository.go` 定义了
   `RestaurantRepository` 与 `KnowledgeRepository` 两个**读侧**端口，但：
   - `GetByID(ctx, restaurantID string)` 与 `FindEvidenceByRestaurant(ctx, restaurantID string)`
     的参数类型是 **`string`**，而 M1/M2 落地后 `restaurants.id` 是 `bigint`，
     内存实现用的是 `int64`；
   - `VectorSearch(ctx, scope, query, topK, filter map[string]any)`
     用 `map[string]any` 表达过滤条件，而 postgres 写侧的
     `port.VectorFilter{Borough string; RestaurantID int64}` 是强类型；
   - `RestaurantRepository.Search` 的 `RestaurantFilter` 只有 5 个字段，
     缺 M3-01 需要的 `borough`（`knowledge_documents` 按 borough 反规范化、
     HNSW 按 borough 分区，borough 过滤是**索引选择的前提**，不是可选字段）。

   实测：`var _ port.RestaurantRepository = (*memory.RestaurantRepository)(nil)` 编译失败。
   **M3 的第一个动作就是收敛这个接口**（M3-01/M3-02 交付物），
   对齐到 `int64` 与强类型 filter，并让 memory 与 postgres 两个实现都满足它。

2. **postgres 只有写侧适配器，读侧要从零写**。`postgres.RestaurantStore` 实现的是
   `port.RestaurantStore`（写入 + 少量读），`postgres.KnowledgeStore` 实现的是
   `port.KnowledgeStore`（文档生命周期），**都不是**读侧端口。M3 需要新增
   `postgres.RestaurantSearchRepository` 与 `postgres.KnowledgeRepository`，
   与写侧 store 共享 `*postgres.Client`，但不复用其方法集——写侧 SQL 面向批量与幂等，
   读侧 SQL 面向单条与 topK，形状不同。

**M3 明确不做的事**（避免范围蔓延）：

- 不做 Agent / 工具注册 / Eino / SSE（M4）。M3 只交付可被工具调用的应用层函数。
- 不做在线 embedding 服务（query embedding 在请求时同步调用 `EmbedQuery`）。
- 不接真实 Rerank 模型（M3-06 只交付接口 + Mock + 无 rerank 时的降级路径）。
- 不做 trace 持久化与评测看板（M6-01/M6-02）。M3-08 只交付可运行的评测**骨架**与夹具。
- 不实现前端（M6-06）。
- 不为 `retrieval_scope` 新增 partial HNSW——先测，M3-03/M3-04 有实测数据再决定（M2-07 已明确此决策）。

### 0.2 工作量级定义

沿用实施计划 §6：

- `S`：半天以内。
- `M`：约 1–2 天。
- `L`：约 3–5 天。
- `XL`：需要拆成更小任务后再执行。

M3 总量：1 个 S + 6 个 M + 1 个 L。按实施计划 §14 第 4 组执行。

### 0.3 前置条件

```bash
# 1. 数据库已就绪（M1/M2 产物，不可跳过）
make pg-up && make migrate
# 2. Ollama embedding 模型已就位（M3 在线查询向量化依赖它）
ollama pull qwen3-embedding:0.6b
# 3. 确认 11,775 篇活跃文档存在
psql "$POSTGRES_DSN" -c "SELECT retrieval_scope, count(*) FROM knowledge_documents WHERE is_active GROUP BY 1"
#    restaurant | 3000
#    evidence   | 8775
```

> 第 3 步若返回 0 行，**停止 M3**：M3 没有文档可召回。
> 按 M2 文档附录 B 依次跑 `build-documents` 与 `embed`。

## 1. M3 目标与退出条件

### 1.1 里程碑目标

M3 只解决"检索与引用"，不承载 Agent 逻辑；它要交付一条**可被 M4/M5 直接调用**的检索应用层：

1. 结构化餐厅过滤：菜系、价格、评分、borough、状态，过滤正确率 100%（M3-01）。
2. 名称与地址模糊检索：`pg_trgm` 查询与结果归一化（M3-02）。
3. 餐厅级向量召回：`retrieval_scope=restaurant`，软条件影响候选排序（M3-03）。
4. 佐证召回：按 `restaurant_id` + 问题召回 evidence，**不跨餐厅**（M3-04）。
5. 混合融合：结构化 / 关键词 / 向量三路分数融合，分数与来源可解释（M3-05）。
6. Rerank 接口：可选 `RerankProvider` + Mock；无 rerank 时系统仍可运行（M3-06）。
7. 证据组装：evidence ID、来源、快照时间、去重，输出可直接交给 LLM 与引用 UI（M3-07）。
8. 检索测试夹具：固定查询、预期餐厅与证据，可运行 Recall@K 与引用精度测试（M3-08）。

### 1.2 退出条件（Milestone Exit Criteria / Gate C）

实施计划 §9 Gate C 的 4 条，逐条对应到可执行的验证：

| # | 退出条件 | 验证方式 |
|---|---|---|
| 1 | 硬条件过滤正确率 100% | 夹具中每条带硬条件的查询，返回结果 100% 满足条件；`M3-08` 的 `structured_filter_accuracy` = 1.0 |
| 2 | 餐厅召回结果可解释 | 每个候选携带 `reasons[]`，逐条可回溯到触发的过滤条件或召回通道（`M3-05` 契约） |
| 3 | 佐证不会跨餐厅返回 | 对候选集召回 evidence，`restaurant_id` 100% 落在候选集内；单餐厅召回 100% 等于该餐厅 |
| 4 | 引用包含 source 和 observed_at | 每条 `Evidence` 的 `Source` 非空、`SnapshotAt` 非零且落在合理区间；与 `restaurants.observed_at` 一致 |

**扩充分项门**（本文档补充，实施计划未列，但对 M4/M6 至关重要）：

| # | 条件 | 验证方式 |
|---|---|---|
| 5 | 两级召回不混 scope | `ScopeRestaurant` 召回结果 100% 为 `restaurant_profile`；`ScopeEvidence` 100% 为 evidence |
| 6 | 过滤在召回前执行 | 带 borough 的召回 `EXPLAIN` 命中 `knowledge_documents_hnsw_<borough>`，非 Seq Scan |
| 7 | query 向量维度校验 | `EmbedQuery` 返回非 1024 维时返回 `embedding_dimension_mismatch`，不发起检索 |
| 8 | 融合分数可复现 | 同一查询两次调用分数完全一致（无随机性、无时间依赖） |
| 9 | 无 rerank 可运行 | 不配置 `RERANK_PROVIDER` 时全链路可用，`rerank_applied=false` |
| 10 | 证据去重生效 | 同一 `content_hash` 的多条 chunk 在组装后只保留 1 条 |
| 11 | 端到端延迟可接受 | `POST /v1/restaurants/search` p95 < 2s（含在线 embedding）；`/v1/restaurants/{id}/evidence` p95 < 1s |
| 12 | 空结果不报错 | 库中不存在的餐厅返回 `not_found`；过滤后为空集返回 200 + 空数组，不返回 500 |

## 2. 目标数据形态

### 2.1 分层与依赖方向

```text
restaurants ──────┬─> [M3-01] 结构化过滤 ──┐
                  │                          │
                  └─> [M3-02] pg_trgm 名称/地址 ─┤
                                             ├─> [M3-05] 融合 ─> [M3-06] rerank(可选) ─> 候选餐厅
knowledge_documents(restaurant scope) ─> [M3-03] 向量召回 ────┘
knowledge_documents(evidence scope)  ─> [M3-04] 向量召回 ─> [M3-07] 证据组装 ─> 引用
```

依赖方向不可逆：

- `chat-service` **只读** `restaurants` 与 `knowledge_documents`，不产生也不修改任何行。
- `data-pipeline` 不感知检索；M3 不在 `data-pipeline` 里加子命令。
- 两级召回严格分离：第一级输出 `restaurant_id`，第二级**必须**以第一级的输出为输入，
  绝不独立发起全局 evidence 检索。

### 2.2 目标目录结构（M3 结束时）

在 M2 结构上的**增量**（不重列 M2 已有文件）：

```text
chat-service/
├── internal/
│   ├── app/
│   │   └── app.go                       # 改：装配读侧 repository + 检索服务
│   ├── retrieval/                       # 新：检索应用层（纯业务，无 Hertz、无 pgx）
│   │   ├── service.go                   # Search / RecallEvidence 两个入口
│   │   ├── fusion.go                    # M3-05：三路分数融合
│   │   ├── rerank.go                    # M3-06：可选 rerank + 降级
│   │   ├── assemble.go                  # M3-07：证据组装与去重
│   │   ├── service_test.go
│   │   ├── fusion_test.go
│   │   └── assemble_test.go
│   └── transport/httpapi/
│       ├── router.go                    # 改：挂载 /v1 只读路由
│       ├── restaurants.go               # 新：搜索 + 佐证 handler
│       └── restaurants_test.go
shared/
├── domain/
│   ├── search/search.go                 # 改：RestaurantFilter 加 Borough/MaxDistanceMeters；
│   │                                    #     RestaurantCandidate 加 RetrievalReasons
│   ├── evidence/evidence.go             # 改：Evidence 加 RestaurantName（引用 UI 需要）
│   └── retrieval/                       # 新：检索领域 DTO（纯 stdlib）
│       ├── retrieval.go                 # RetrievalRequest / RetrievalTrace / ChannelScore
│       └── retrieval_test.go
├── port/
│   ├── repository.go                    # 改：收敛读侧端口签名（int64 + 强类型 filter）
│   └── rerank.go                        # 不改签名，补文档
└── adapter/
    ├── repository/postgres/
    │   ├── restaurant_search.go         # 新：读侧 RestaurantRepository
    │   ├── restaurant_search_test.go
    │   ├── knowledge_read.go            # 新：读侧 KnowledgeRepository
    │   ├── knowledge_read_test.go
    │   └── knowledge_read_index_test.go # 新：EXPLAIN 断言（读侧路径）
    ├── repository/contract/
    │   └── read_contract.go             # 新：读侧行为契约（memory + postgres 共用）
    └── repository/memory/
        ├── restaurant.go                # 改：对齐 int64 与新 filter
        └── knowledge.go                 # 改：对齐强类型 filter
```

**架构约束**：新增的 `shared/domain/retrieval` 与 `shared/domain/architecture_test.go:TestDomainLayerHasNoFrameworkOrVendorDependencies`
兼容——只 import 标准库与 `shared/domain`。`TestDomainLayerDoesNotNameAStorageEngine`
更严格：该包内**不得出现** `postgres`/`sql`/`pgvector` 等词，**包括注释**。
写融合算法时不要说"SQL"、"index"、"seq scan"，用"通道"、"召回通道"表述。

### 2.3 目标接口形状（约定）

以下是 M3 收敛后的读侧端口与领域 DTO 形状。**先定形状再并行开发**，否则 M3-01 ~ M3-07 会各自发明一套签名。

```go
// shared/port/repository.go（读侧，M3 收敛）
type RestaurantRepository interface {
    GetByID(ctx context.Context, restaurantID int64) (search.RestaurantDetail, error)
    Search(ctx context.Context, query search.SearchQuery) ([]search.RestaurantCandidate, error)
}

type KnowledgeRepository interface {
    FindEvidenceByRestaurant(ctx context.Context, restaurantID int64, topic string) ([]evidence.Evidence, error)
    VectorSearch(ctx context.Context, req VectorSearchRequest) ([]ScoredDocument, error)
}

// VectorSearchRequest makes the filter an explicit, typed struct rather than
// map[string]any. A map lets a caller pass a filter the database silently
// ignores — "restaurant_id" as a string, "borough" with a different case — and
// the mistake only shows up as a wrong answer, not an error.
type VectorSearchRequest struct {
    Scope       evidence.RetrievalScope
    Query       []float32
    TopK        int
    Borough     string        // 空 = 不限制；同时决定命中哪个 partial 索引
    RestaurantIDs []int64     // 非空 = 限定在这些餐厅内（M3-04 的跨餐厅防线）
    DocTypes    []evidence.DocType
    Topic       string        // 限定 review_summaries 的 metadata.topic
    Topics      []string
}
```

`RestaurantIDs` 与 `Borough` 是**两个独立的下推**：borough 决定索引选择，
`RestaurantIDs` 决定正确性。M3-04 的"不跨餐厅"靠后者，不靠前者。

```go
// shared/domain/retrieval/retrieval.go（检索领域 DTO）
type Channel string
const (
    ChannelStructured Channel = "structured"
    ChannelKeyword    Channel = "keyword"
    ChannelVector     Channel = "vector"
)

// ChannelScore is one recall channel's contribution to one candidate, kept so
// a ranking can be explained rather than merely reported.
type ChannelScore struct {
    Channel Channel
    Score   float64
    Weight  float64
    Detail  string // 人类可读的一句话，进 reasons
}

// RetrievalTrace records what every channel did, including the ones that
// contributed nothing. A rank with no trace is not auditable.
type RetrievalTrace struct {
    Query           string
    Channels        []ChannelScore // 每个通道的总分（无结果时也在）
    Candidates      []search.RestaurantCandidate
    CandidatePool   int           // 融合前的候选池大小
    RerankApplied   bool
    RerankModelID   string
    EmbeddingModelID string
    QueryEmbeddingDim int
    Scope           evidence.RetrievalScope
}
```

## 3. 任务清单总览

| ID | 任务 | 交付物 | 依赖 | 工作量 | 验收摘要 |
|---|---|---|---|---|---|
| M3-01 | 结构化餐厅查询 | 读侧端口收敛、`Search` 的 postgres 实现 | M1-10 | M | 过滤条件可由 API 参数执行，正确率 100% | **已交付** |
| M3-02 | 名称和地址检索 | `pg_trgm` 查询和结果归一化 | M1-10, M1-11 | M | 支持名称和地址模糊匹配 | **已交付** |
| M3-03 | 餐厅级向量召回 | `retrieval_scope=restaurant` 查询 | M2-07 | M | 软条件可影响候选排序 | **已交付** |
| M3-04 | 佐证召回 | 按 restaurant_id 和问题召回 evidence | M2-07 | L | 不返回其他餐厅的 chunk | **已交付** |
| M3-05 | 混合融合 | 结构化、关键词、向量分数融合 | M3-01, M3-02, M3-03 | M | 分数和来源可解释 | **已交付**（三通道 + 硬条件复检） |
| M3-06 | Rerank 接口 | 可选的 RerankProvider 和 Mock 实现 | M0-05 | S | 无 Rerank 时系统仍可运行 | **已交付** |
| M3-07 | 证据组装 | evidence ID、来源、时间和去重 | M3-04 | M | 输出可直接交给 LLM 和引用 UI | **已交付** |
| M3-08 | 检索测试夹具 | 固定查询、预期餐厅和证据 | M3-01, M3-04 | M | 可运行 Recall@K 和引用精度测试 | **已交付**（Gate 全绿） |

### 3.1 依赖图

```text
M3-01 (结构化查询 + 读侧端口收敛) ─┬─> M3-02 (名称/地址检索)
                                   ├─> M3-05 (混合融合) ─> M3-06 (rerank 可选)
                                   │
M2-07 (VectorSearch 写侧已验证) ────┼─> M3-03 (餐厅级召回) ─┘
                                   │
                                   └─> M3-04 (佐证召回) ─> M3-07 (证据组装)

M3-01 ─┐
       ├─> M3-08 (检索夹具与评测骨架)
M3-04 ─┘
```

### 3.2 推荐执行顺序

1. **M3-01 结构化餐厅查询**（先收敛端口，这是所有任务的共同地基；同时它不需要 embedding，可独立验证）
2. **M3-02 名称和地址检索**（同一 repository 文件，早做早复用连接与投影）
3. **M3-03 餐厅级向量召回**（需要在线 embedding，是第一条真正依赖 M2 的链路）
4. **M3-05 混合融合**（把前三路合起来，第一次能返回"可解释的候选"）
5. **M3-06 Rerank 接口**（S，随时可插入 M3-05 之后）
6. **M3-04 佐证召回**（工作量最大，独立于前三路，可与 M3-05 并行）
7. **M3-07 证据组装**（M3-04 之后立刻做）
8. **M3-08 检索测试夹具**（收口，产出 Gate C 的可执行证据）

> **最小可演示链路**：`M3-01 → M3-05(仅结构化通道) → /v1/restaurants/search`。
> 完成后即可用 HTTP 验证"给定菜系+价格+borough，返回符合硬条件的餐厅"。
> 这是实施计划 §7 推荐的第一条链路的读侧终点（对应 M5-01），
> **不依赖 embedding、不依赖 Agent 模型**，能最早证明读路径正确。
>
> **M3-08 需要真实数据才有意义**：夹具的预期餐厅必须来自真实的 3,000 家 demo 集合，
> 否则 Recall@K 会全部命中空集而"通过"。因此 M3-08 必须在 M3-01/M3-04 之后，
> 且必须连真实数据库跑（`PLATEPILOT_REQUIRE_DB=1`）。

## 4. 详细任务

### M3-01 结构化餐厅查询

**目标**：让 `chat-service` 能用结构化条件在 `restaurants` 上做确定性过滤，并**顺手把 M0 遗留的读侧端口签名收敛到与 M1/M2 落地一致**。

**交付物**

- **`shared/port/repository.go`（改）**：读侧端口签名收敛为 `int64` + 强类型 filter。
  这是硬性交付物，理由见 §0.1 第 1 条：
  ```go
  type RestaurantRepository interface {
      // GetByID returns one restaurant by its internal id.
      GetByID(ctx context.Context, restaurantID int64) (search.RestaurantDetail, error)
      // Search applies the hard filters in query.Filter and returns candidates
      // ordered by score. An empty Text means "no keyword channel"; scoring is
      // then the structured channel alone.
      Search(ctx context.Context, query search.SearchQuery) ([]search.RestaurantCandidate, error)
  }
  ```
  **删除** `Upsert`——读侧端口不写库，写入只有 `RestaurantStore.Upsert*` 一条路。
  内存实现的 `Upsert` 保留（内存实现同时给测试播种用），但不进接口。
- **`shared/domain/search/search.go`（改）**：
  ```go
  type RestaurantFilter struct {
      Cuisines          []string `json:"cuisines,omitempty"`
      PriceLevels       []int    `json:"price_levels,omitempty"`
      MinRating         *float64 `json:"min_rating,omitempty"`
      Neighborhood      string   `json:"neighborhood,omitempty"`
      OpenNow           *bool    `json:"open_now,omitempty"`
      // Borough is a hard filter that also selects the vector index. It is not
      // an optional refinement: a query with a borough must be answered by the
      // borough partial index, and a query without one must not accidentally
      // land on it.
      Borough           string   `json:"borough,omitempty"`
      // MaxDistanceMeters, with QueryOrigin, is the "near me" filter. Both must
      // be set for it to apply: a radius without a centre is meaningless, and
      // a centre without a radius is a viewport, not a distance.
      QueryOrigin       *GeoPoint `json:"query_origin,omitempty"`
      MaxDistanceMeters *int      `json:"max_distance_meters,omitempty"`
  }

  type GeoPoint struct {
      Longitude float64 `json:"longitude"`
      Latitude  float64 `json:"latitude"`
  }
  ```
- **`shared/adapter/repository/postgres/restaurant_search.go`（新）**：`RestaurantSearchRepository`。
- **`shared/adapter/repository/memory/restaurant.go`（改）**：对齐签名与新过滤字段。
- **`shared/adapter/repository/contract/read_contract.go`（新）**：读侧行为契约，
  memory 与 postgres 跑同一套（这是 M3-01 的核心交付物，不只是一个 SQL）。

**读侧 SQL 的关键点**

- **投影复用**：读侧要的是 `search.RestaurantDetail`（轻量），不是
  `restaurant.Restaurant`（含 `attributes_raw`、`hours`、`relative_results`）。
  写侧 `restaurantColumns` 投影太重，**不要复用**——它会为一次 Top-20 搜索
  把 36k 行的 jsonb 全读一遍。新写一个轻投影：
  ```sql
  id, name, address, borough, cuisine_tags, price_level,
  rating_computed_avg, rating_count, source, observed_at, snapshot_status,
  is_active_for_demo
  ```
  `rating_computed_avg` 而不是 `rating_source_avg`：M2 已确认来源站评分可能截顶
  （`rating_source_avg` 上限 9998），用截顶值做 `min_rating` 过滤会把 4.2 分的店判为达标。
- **数组过滤必须用 `unnest`，不能 `= ANY`**：`cuisine_tags` 是 `text[]`，
  `cuisine_tags && ARRAY['italian']` 可用；但"多菜系任一命中"要用重叠运算符，
  "多菜系全部命中"要 `EXISTS (SELECT 1 FROM unnest(cuisine_tags) t WHERE t = ANY($n))`。
  统一写成后者，语义明确且命中 `restaurants_cuisine_gin`。
  **不要拼字符串**——`$1`..`$n` 的编号必须连续（M2 E.12 已在可选过滤器上踩过"占位符编号留空洞"）。
- **borough 白名单校验**：`restaurants_borough_check` 只允许 5 个值。
  传入 `"Manhattan"` 或 `"new york"` 必须在应用层拒绝并返回 `invalid_argument`，
  **不是**返回空集——空集会被误解为"这个区没有餐厅"，而真实原因是参数拼错。
- **`OpenNow` 的三态语义**：`attributes->>'open_now'` 缺失 = `unknown`，
  不能当作 `false`。`OpenNow=true` 必须走 `attributes->>'open_now' = 'true'`，
  `OpenNow=false` 走 `= 'false'`。**用 `COALESCE(...,'unknown') = $n`**，
  缺失行在 `OpenNow=false` 时会被正确排除。
- **距离过滤必须用 `ST_DWithin`**：`location` 是 `geography`，
  `ST_DWithin(location, $point::geography, $radius)` 走 GiST 索引；
  `ST_Distance` 只能做 ORDER BY，不能做 WHERE。
  坐标系是 EPSG:4326，`geography` 的距离单位是**米**，符合产品语义。
- **`is_active_for_demo`**：默认加 `is_active_for_demo = true`（3,000 家）。
  全量 36,133 家里有大量从未进过 demo 集合的长尾，加过滤是性能与质量的共同要求。
  但**必须可关闭**——评测集（M6-02）可能需要全量。
- **ORDER BY 与 LIMIT**：默认 `ORDER BY knowledge_score DESC, id ASC LIMIT $k`。
  `id ASC` 是终局 tie-breaker，保证分页与测试可复现。

**实现要点**

- **TopK 上下界要在应用层夹紧**：`TopK <= 0` 用默认 10；`TopK > 100` 夹到 100。
  不夹紧的话，一个 `top_k=100000` 的请求会把整个表读进内存。
- **`SearchQuery.Text` 在 M3-01 里不做语义处理**。它只用于 M3-02 的关键词通道
  和 M3-05 的融合输入。M3-01 忽略 `Text` 但**不能因此把它置零**——
  否则 M3-05 拿不到原始 query。两者的边界是：M3-01 消费 `Filter`，M3-05 消费 `Text`。
- **空结果不是错误**。过滤后 0 行返回 `nil, nil`，由 handler 转成 200 + `[]`。
  只有"餐厅 ID 不存在"才是 `not_found`。
- **契约测试要覆盖三态与边界**：`OpenNow=false` 不应排除缺失行以外的任何行；
  `price_level IS NULL` 的餐厅在 `PriceLevels` 过滤下必须被排除
  （"价格未知"不能算作"价格符合"）。

**依赖**：M1-10（精选餐厅集合）。
**工作量**：M。

**验收标准**

```bash
# 用真实库跑（默认 DSN 是开发库，不要用 go test 的默认 scratch DSN）
PLATEPILOT_REQUIRE_DB=1 go test ./shared/adapter/repository/... -run 'ReadContract|Search' -count=1 -v
```

- memory 与 postgres 通过同一套读侧契约测试。
- `Search` 带 `cuisines=[italian]` 且 `price_levels=[1,2]` 时，返回行 100% 同时满足两个条件。
- `Borough: "manhattan"` 有效；`Borough: "Manhattan"` 返回 `invalid_argument`，不是空集。
- `MinRating: 4.5` 使用 `rating_computed_avg`，且截顶的 `rating_source_avg` 不参与过滤。
- `MaxDistanceMeters` 配合 `QueryOrigin` 生效，单用其一不报错也不生效（返回全量）。
- `TopK=0` 返回 10 条；`TopK=1000` 夹到 100 条。
- 36,133 行全表条件下，单次 `Search` 的 `EXPLAIN` 不出现 Seq Scan + Sort 全表返回。

---

### M3-02 名称和地址检索

**目标**：支持"Joe's Pizza"这类名称模糊匹配与"7 Carmine St"这类地址匹配，并让结果可归一化（同一餐厅不因多种写法重复出现）。

**交付物**

- **`shared/adapter/repository/postgres/restaurant_search.go`（续 M3-01）**：新增两个方法。
  ```go
  // MatchByText finds restaurants by name or address using pg_trgm similarity.
  // It is the keyword channel of M3-05 and the entity-resolution step of
  // M5-03, so its output must be a normal candidate list, not a special shape.
  MatchByText(ctx context.Context, text string, limit int) ([]search.RestaurantCandidate, error)
  ```
- **`shared/domain/search/search.go`（改）**：`RestaurantCandidate` 增加可解释字段。
  ```go
  type RestaurantCandidate struct {
      RestaurantID int64   `json:"restaurant_id"`
      Name         string  `json:"name"`
      Address      string  `json:"address,omitempty"`
      Score        float64 `json:"score"`
      Reasons      []string `json:"reasons,omitempty"`
      // SnapshotAt travels with the candidate so a recommendation can always
      // show when the data was observed, without a second lookup.
      SnapshotAt   time.Time `json:"snapshot_at,omitempty"`
      // Rating is the computed average over the reviews that are actually in
      // the knowledge base, plus its sample size. A rating with no count is a
      // claim the system cannot support.
      Rating       *float64  `json:"rating,omitempty"`
      RatingCount  int       `json:"rating_count,omitempty"`
      PriceLevel   *int      `json:"price_level,omitempty"`
      Cuisines     []string  `json:"cuisines,omitempty"`
      Borough      string    `json:"borough,omitempty"`
  }
  ```
  > `RatingCount` 是刻意加的：`rating 4.8` 背后可能是 30 条评论，也可能是 2 条。
  > 没有计数，UI 就无法区分"口碑好"与"两条评论的偶然"。
- **`shared/adapter/repository/memory/restaurant.go`（改）**：同步实现 `MatchByText`（子串匹配即可，行为契约只要求语义一致，不要求 pg_trgm 的模糊度）。

**pg_trgm 的关键点**

- **索引已存在**：`restaurants_name_trgm` 与 `restaurants_address_trgm`
  （`migrations/0001_init.sql:140-146`），GIN + `gin_trgm_ops`。
  用 `name % $1` 或 `name ILIKE '%'||$1||'%'` 都能命中索引；
  **`ILIKE '%...%'` 不带 `pg_trgm.similarity_threshold` 时，
  planner 仍能用 GIN，但打分要自己算 `similarity()`**。建议两者都写：
  ```sql
  WHERE is_active_for_demo
    AND (name ILIKE '%' || $1 || '%' OR address ILIKE '%' || $1 || '%')
  ORDER BY GREATEST(similarity(name, $1), similarity(address, $1)) DESC
  ```
  `ILIKE` 负责召回（大小写不敏感、可用索引），`similarity()` 负责排序。
- **`%` 与 `_` 必须转义**：查询文本里的 `%` 会让 `ILIKE` 变成通配符，
  用户搜 "50%" 会匹配全表。用 `ILIKE '%' || $1 || '%' ESCAPE '\'` 并在应用层
  转义 `\ % _`，或者改用 `strpos(lower(name), lower($1)) > 0`（不走索引但不会误召回）。
  **推荐前者**：转义 3 个字符的收益远大于失去索引。
- **归一化去重**：同一餐厅可能同时因 name 和 address 命中一次。
  SQL 用 `DISTINCT ON (id)` 或在应用层按 `RestaurantID` 去重并取最高分，
  **不要**把两个通道的分数相加（地址命中不代表名字也命中）。
- **短查询必须拒绝**：`pg_trgm` 的 trigram 对长度 < 3 的字符串无法建索引匹配
  （`pg_trgm` 默认只索引 ≥3 字符的词）。`len < 3` 时直接返回空结果 + `reasons`
  说明，而不是退化成全表 `ILIKE`（那会让 `top_k=10` 变成全表扫描）。
  更友好的做法：`len < 3` 时直接 `invalid_argument`，因为这几乎总是输入错误。
- **中英文混合**：语料里餐厅名多为英文/拼音，中文查询会走 trigram 分词失效。
  **M3 不解决**——M6-02 评测会量化这个问题（实施计划 §17 第 6 条已列为未锁定决策）。
  M3 只保证英文与数字查询正确，中文留给后续分词器方案。
- **`knowledge_documents` 侧不建 trgm 索引**：M3-04 的主题召回靠向量，
  不靠关键词；不要在 M3 顺手加索引（避免过早优化）。

**实现要点**

- `MatchByText` 与 `Search` 的区别是**前者不做硬过滤、只做文本匹配**。
  M3-05 会在两者交集上融合。
- 返回的 `RestaurantCandidate.Score` 是 `GREATEST(similarity(name), similarity(address))`，
  范围 0..1。**不要归一化到 0..1 之外**，M3-05 的权重设计假设这个范围。
- `address` 可能为 NULL：`similarity(NULL, x)` 返回 NULL，
  `GREATEST` 在 Postgres 里对 NULL 也会返回 NULL（不是跳过 NULL），
  必须用 `COALESCE(similarity(address, $1), 0)` 包起来，否则**地址为空的餐厅
  永远排在最后**（这正是 E.34 同类问题：读了但不给 → 结果静默失真）。

**依赖**：M1-10, M1-11。
**工作量**：M。

**验收标准**

- `"joe's pizza"` 命中 `Joe's Pizza`（大小写与撇号无关）。
- `"carmine"` 命中该地址上的餐厅，`Score > 0`。
- 同一餐厅同时命中 name 与 address 时，**只出现一次**。
- 搜索 `"50%"` 不会匹配全表（返回结果数 < 100 且无转义 bug）。
- 查询 `"ab"`（2 字符）返回 `invalid_argument`，不是全表扫描。
- 地址为 NULL 的餐厅在纯地址查询中不被 `GREATEST` 丢弃（`COALESCE` 生效）。
- `EXPLAIN` 显示命中 `restaurants_name_trgm` 或 `restaurants_address_trgm`。

---

### M3-03 餐厅级向量召回

**目标**：用 `retrieval_scope=restaurant` 的向量召回，让"安静、适合约会"这类**软条件**影响候选排序——这是硬过滤做不到的部分。

**交付物**

- **`shared/adapter/repository/postgres/knowledge_read.go`（新）**：读侧 `KnowledgeRepository`。
  ```go
  func (r *KnowledgeRepository) VectorSearch(ctx context.Context, req port.VectorSearchRequest) ([]port.ScoredDocument, error)
  ```
- **`shared/port/repository.go`（改）**：加 `port.VectorSearchRequest`（形状见 §2.3）。
- **`chat-service/internal/retrieval/service.go`（新，M3-03 部分）**：
  ```go
  // RecallRestaurant runs the restaurant-scope vector channel: it embeds the
  // query and returns at most topK restaurant documents. It is deliberately a
  // separate entry point from Search, because the two channels have different
  // failure modes and a caller must be able to run one without the other.
  func (s *Service) RecallRestaurant(ctx context.Context, query string, filter search.RestaurantFilter, topK int) ([]search.RestaurantCandidate, error)
  ```
- **`shared/adapter/repository/postgres/knowledge_read_index_test.go`（新）**：`EXPLAIN` 断言。

**实现要点**

- **读侧 `VectorSearch` 不复用写侧 SQL**。写侧 `postgres.KnowledgeStore.VectorSearch`
  （`knowledge.go:489`）已经验证过 scope + borough + topK 的正确性，读侧要在此之上
  加：`RestaurantIDs` 下推、`DocTypes` 下推、`Topic` 下推。
  两份 SQL 必须**共享同一个投影常量** `knowledgeColumns`，否则会重演 E.34
  （读侧投影缺列 → 返回的文档悄悄少字段）。建议把 `knowledgeColumns` 提到
  `knowledge_support.go` 并由两边共用。
- **borough 决定索引，必须下推到 SQL**：
  ```sql
  WHERE is_active
    AND retrieval_scope = $1
    AND borough = $2          -- 命中 knowledge_documents_hnsw_<borough>
  ORDER BY embedding <=> $3
  LIMIT $4
  ```
  **不要**把 borough 放进 `metadata->>'borough' = $2`——那会让 planner 看到
  一个非索引谓词，退化成 Seq Scan（这正是 M1-03 实测的 `Rows Removed by Filter: 49759`）。
- **`RestaurantIDs` 用 `= ANY($n)`**：一次查询召回多家餐厅的 profile，
  M3-05 需要它。数组为空时**不构造该子句**（`= ANY('{}')` 匹配不到任何行，
  会静默返回空集——这是最危险的一类 bug）。
- **余弦距离转相似度**：`ScoredDocument.Distance` 是距离（0 = 相同方向，越小越近），
  而 `RestaurantCandidate.Score` 需要"越大越好"。转换保留原始值：
  ```go
  // similarity = 1 - distance, clamped: pgvector can report a distance slightly
  // above 2 for vectors that are not perfectly normalised, which would make
  // similarity negative and silently reorder a candidate against a zero-score
  // structured hit.
  sim := 1 - doc.Distance
  if sim < 0 { sim = 0 }
  ```
  **必须同时保留 `Distance`**（进 `trace`），否则"分数可解释"无法验证——无法区分
  "余弦相似度低"与"融合权重把它压低了"。
- **query 向量化在线同步调用**：`s.embedding.EmbedQuery(ctx, query)`。
  - **维度先校验**：`len(vec) != s.embedding.Dimensions()` → `embedding_dimension_mismatch`，
    **且不发起数据库查询**（省一次无用的往返，且错误信息更准）。
  - **`len(vec) == 0` 且 query 为空字符串**：短路返回空结果，不调用 provider
    （M2-01 已确立"空输入短路"的规则，读侧必须一致）。
  - **空 query 但有过滤条件**：这是合法的"只按硬条件搜"请求，
    此时**跳过向量通道**（记 `channels` 里 `vector` 分数 0 并说明），
    不调用 `EmbedQuery`——给空串做 embedding 得到的是无意义的向量。
- **provider 不可用时的降级**：向量通道失败（`provider_unavailable` / `provider_timeout`）
  **不应该让整个搜索失败**。M3-05 的融合设计允许某通道缺额，
  此时降级为纯结构化 + 关键词结果，并在 `trace.Reasons` 里显式标注降级原因。
  **只有当所有通道都失败时**才返回错误。这条是 M3-05 的前提，不是 M3-03 的可选项。
- **topK 要放大后裁剪**：一次向量召回可能返回 10 家，其中 6 家被硬过滤排除。
  应召回 `topK * 3`（上限 50）再在应用层应用硬过滤——**但这与"过滤在召回前执行"冲突**。
  **决策：硬过滤下推到 SQL**（`cuisine_tags && ...`、`price_level = ANY(...)`），
  在向量排序结果上直接过滤，而不是先召回再过滤。副作用：过滤掉太多时结果不足 topK，
  此时**放宽 topK 重试一次**（召回 `topK * 5`），仍不足则如实返回不足的条数。
  必须在代码注释里写清这个取舍：**宁少不脏**——返回不满足硬条件的候选比返回 3 条
  而非 5 条更糟。

**依赖**：M2-07。
**工作量**：M。

**验收标准**

```bash
# 在线 embedding 前置
export EMBEDDING_PROVIDER=ollama EMBEDDING_MODEL=qwen3-embedding:0.6b EMBEDDING_DIMENSIONS=1024
PLATEPILOT_REQUIRE_DB=1 go test ./chat-service/internal/retrieval/... -count=1 -v
```

- `ScopeRestaurant` 召回结果 100% 为 `restaurant_profile`，且每个餐厅恰好 1 条。
- 查询某家已知餐厅的 profile 原文，Top-1 是该餐厅自身（sanity check，同文近文）。
- 带 `Borough=manhattan` 的召回 `EXPLAIN` 命中 `knowledge_documents_hnsw_manhattan`，非 Seq Scan。
- `RestaurantIDs` 非空时，结果 100% 落在给定集合内；为空数组时**不**返回空集（不构造子句）。
- query 向量维度不符时返回 `embedding_dimension_mismatch`，且日志显示未发起 DB 查询。
- provider 不可用时，返回 `provider_unavailable` **但不中断融合**（由 M3-05 验证）。
- 硬过滤下推到 SQL：带 `cuisine_tags` 过滤的召回 `EXPLAIN` 中过滤发生在索引扫描之后、
  `LIMIT` 之前，而非先取 50 行再在应用层丢。

---

### M3-04 佐证召回

**目标**：按 `restaurant_id`（或候选集）+ 用户问题召回 evidence chunk，**绝对不返回其他餐厅的内容**。这是 Gate C 第 3 条，也是整个 RAG 的可信度基础。

**交付物**

- **`shared/adapter/repository/postgres/knowledge_read.go`（续）**：
  ```go
  // RecallEvidence returns evidence chunks for the given restaurants that match
  // the query. restaurantIDs must be non-empty: an evidence search without a
  // restaurant scope is a global review search, which is both off-brief for the
  // caller and the single most likely way for a citation to point at the wrong
  // restaurant.
  func (r *KnowledgeRepository) RecallEvidence(ctx context.Context, restaurantIDs []int64, query string, topic string, topK int) ([]evidence.Evidence, error)
  ```
- **`chat-service/internal/retrieval/service.go`（续）**：`Evidence` 入口。
- **迁移 `migrations/0004_evidence_recall.sql`（新，见下）**。
- **`shared/adapter/repository/postgres/knowledge_read_index_test.go`（续）**：跨餐厅断言。

**为什么需要一个 scope + borough 之外的索引 —— 本节的结论已被实测推翻**

> **实施后的更正**：本节原本论证"需要 scope partial 索引"的三条理由，
> 实测**全部不成立**。真实缺陷与真正的修复理由见附录 **E.11**。
> 结论不变（索引必须加），但加它的理由与这里写的完全不同。

原计划的三条理由，逐条对照实测：

| 原计划的理由 | 实测结论 |
|---|---|
| 证据召回是高频路径 | 成立，但高频不等于慢：实测 1.6ms / 4.9ms |
| evidence 与 profile 混在同一 ANN 里互相挤掉 | **成立，但方向错了** —— 被挤掉的是 profile 召回，不是 evidence |
| `restaurant_id` 过滤不是索引谓词，造成 `Rows Removed by Filter` | **不成立** —— `restaurant_id` 有选择性，实测走 Bitmap Heap Scan + top-N heapssort，ANN 无优势 |

**证据召回路径本身不需要 scope ANN**：按 `restaurant_ids` 有界查询，
3 餐厅 1.6ms、20 餐厅 + topic 过滤 4.9ms，已是正确的查询计划。

**真正需要 scope 索引的是餐厅向量召回（M3-03 的路径）**，且原因是正确性而非性能：
borough 分区里 90% 是 evidence，HNSW 的有界 beam（`ef_search`=40）在 scope
过滤生效前就被 evidence 占满，导致 restaurant scope 召回 **0 条**
（实测 `rows=0, Rows Removed by Filter: 40`），而 exact search 能找到 5 行。
详见 E.11 第 1、2 条。

**实际交付的迁移**是 `migrations/0004_scope_partitioned_hnsw.sql`，
按 `(borough, retrieval_scope)` 建 10 个索引而不是原计划的 2 个 scope 级索引 ——
borough 保留在谓词里是因为 `Migrate` 框架在事务里跑，
`CREATE INDEX CONCURRENTLY` 无法在该框架内使用；10 个窄索引的替代方案是
在 30k 行表上做一次不加锁重建，这笔代价不值得。
0001 的 borough-only 索引**保留不删**（理由见 E.11 第 3 条）。

**本节原定的验收标准作废**："`EXPLAIN` 命中 `..._scope_evidence`，
若未命中就删掉索引"这条结论是错的 —— 正确的验收是"restaurant scope 召回
不再返回 0 条"，实测 5 行 / 0.6ms。

**实现要点**

- **`restaurant_id` 过滤必须下推且必须非空**。空 `restaurantIDs` 直接返回
  `invalid_argument`，**不**降级为全局检索。这条要在应用层和 SQL 层各挡一次。
- **主题（topic）是二级过滤，不是主过滤**。`review_summaries` 文档的
  `metadata.topic` 让"有哪些评论提到排队"变成 `topic='wait'` + 向量排序。
  但**不要**在 topic 已知时跳过向量召回——同一主题下不同餐厅的措辞差异
  仍然需要语义匹配来排序。
- **查询为空时的行为**：无 query 但有 `restaurantIDs` 时，
  返回该餐厅的**全部活跃 evidence**（按 `doc_type` + `topic` 排序），
  这是"这家店怎么样"这类问题的合理退路。上限由 topK 控制。
- **去重在读侧就做一次粗粒度**：同一 `(restaurant_id, doc_type, content_hash)`
  只保留相似度最高的一条。这只是粗筛，**最终去重在 M3-07**（那里还要按 token 预算裁剪）。
- **`source` 必须能解析**：`KnowledgeDocument.ToEvidence` 从
  `metadata["source"]` 取来源。M2 的所有 builder 都写了这个键，
  但**迁移历史数据或手工插入的行可能没有**。读侧必须在 `Source` 为空时
  回退到 `"unknown"`，并记一条 warning——**空来源的引用比没有引用更危险**。

**依赖**：M2-07。
**工作量**：L。

**验收标准**

```bash
PLATEPILOT_REQUIRE_DB=1 go test ./shared/adapter/repository/postgres/... -run 'Evidence|Recall' -count=1 -v
```

- 对候选集 `{A, B}` 召回 evidence，结果的 `restaurant_id` 100% ∈ `{A, B}`。
- 对单餐厅 A 召回，Top-20 中不含任何其他餐厅的 `document_id`。
- `restaurantIDs=[]` 返回 `invalid_argument`，不返回全局结果。
- 8,775 行规模下，`EXPLAIN` 命中 `knowledge_documents_hnsw_scope_evidence`；
  若未命中，记录实测结论并回退到全局索引 + 过滤（附 `EXPLAIN` 证据）。
- 查询某餐厅的 `topic='wait'` 摘要，`Top-1` 是该餐厅的 `restaurant_review_summary`。
- 无 query 但有餐厅 ID 时，返回该餐厅全部活跃 evidence，不报错。
- `is_active=false` 的文档不出现在任何结果中。
- 迁移在 `data-pipeline migrate` 下可重复执行（`IF NOT EXISTS`），且 `CONCURRENTLY` 版本不锁表。

### M3-05 混合融合

**目标**：把结构化、关键词、向量三路召回合成一个有序候选列表，且**每一分的来源都能解释**。这是 Gate C 第 2 条。

**交付物**

- **`chat-service/internal/retrieval/fusion.go`（新）**：
  ```go
  // Fuse merges the recall channels into one ranked candidate list.
  //
  // A candidate missing from a channel scores zero in that channel rather than
  // being dropped: a restaurant the structured filter kept but the vector
  // channel did not surface is still a valid answer, and dropping it would let
  // a soft-condition miss erase a hard-condition hit.
  func Fuse(query string, channels map[Channel][]ChannelHit, opts FusionOptions) ([]search.RestaurantCandidate, *retrieval.RetrievalTrace, error)
  ```
- **`chat-service/internal/retrieval/service.go`（续）**：`Search` 编排入口。
- **`chat-service/internal/config/config.go`（改）**：`RetrievalConfig`（权重、topK、通道开关）。
- **`shared/config/config.go`（改）**：`RetrievalConfig` 基础结构 + `Loader` 读取。

**融合算法（必须显式，不是 RRF）**

实施计划 §6 M3-05 要求"分数和来源可解释"。RRF（Reciprocal Rank Fusion）
只看排名不看分数，`score = Σ 1/(k + rank_i)`——它可解释性差，
且**丢弃了分数本身的量纲**（余弦相似度 0.92 和 0.31 差别很大，
RRF 把它们变成 rank 1 和 rank 20 的差别）。因此 M3 用**加权线性融合**：

```go
final = w_structured * norm(structured_score)
      + w_keyword    * norm(keyword_score)
      + w_vector     * norm(vector_score)
      + w_quality    * quality_prior
```

- **权重必须可配置**（`RETRIEVAL_WEIGHT_STRUCTURED` 等，默认 `1.0 / 0.5 / 1.0 / 0.2`）。
  权重是产品决策，藏在代码里的常量意味着调不动 M6-02 的评测结果。
- **`quality_prior` 是餐厅自身的静态质量分**（`knowledge_score` 归一化到 0..1）。
  它是唯一一个"不是检索通道"的项，权重必须最小（默认 0.2），
  否则等价于"按口碑排序"而不是"按问题排序"。
- **归一化按通道分别做 min-max**：`score' = (s - min) / (max - min)`。
  **分母为零时（该通道全部同分）返回 0.5**，不是 0 也不是 1——
  全部同分时无法区分，把它们都映射到 0 会让这个通道整体失效。
  `max == min` 是"所有候选这一通道得分一样"的正常情况，不是 bug。
- **融合在并集上进行**：先取三路的并集作为候选池，
  对池中每个 `restaurant_id` 计算上式。池大小进 `Trace.CandidatePool`。
- **排序的终局 tie-breaker**：`score` 相同时按 `RestaurantID` 升序。
  没有终局 tie-breaker，`sort.Slice` 的不稳定性会让相同查询返回不同顺序，
  直接摧毁 M3-08 的可复现性与 M6-04 的回放。
- **`Reasons` 逐通道生成**：每个非零通道贡献一条人类可读理由，
  例如 `"向量召回匹配：安静、适合约会（相似度 0.82）"`。
  **理由必须包含触发它的具体内容**，不是"语义匹配"——后者无法验证，
  前者可以在 M6-02 的人工评审里被检查。

**降级路径（M3-03 前提的实现）**

| 情况 | 行为 |
|---|---|
| vector 通道 provider 失败 | 结构化 + 关键词结果照常返回，`trace` 记 `"vector channel unavailable: provider_unavailable"` |
| 结构化通道失败（DB 错误） | **返回错误**，不降级——结构化是唯一的确定性来源，DB 出问题不该假装有结果 |
| 关键词通道无结果 | 正常，不是错误 |
| 空 query | 只跑结构化通道，`trace` 记 `"keyword and vector channels skipped: empty query"` |

**三路并发的正确性**

三路通道必须**并发执行**（它们互不依赖，且 DB 与 provider 都有 I/O 等待），
但要用 `errgroup` + `context.WithCancel`：
- 任一通道**硬失败**（结构化）→ 取消其余，返回错误。
- 任一通道**软失败**（向量）→ 记录并继续。
- 顺序执行的耗时是三路之和；并发后是最大值。M6-05 的 p95 目标依赖这个。

**实现要点**

- **TopK 在融合后应用，不在单通道应用**。单通道 `topK * 2`（过采样），
  融合后裁到 `topK`。单通道直接裁 `topK` 会让某通道永远看不到其他通道排在前面的餐厅。
- **过滤在融合前**：硬过滤（M3-01）只作用在结构化通道，
  矢量/关键词通道的结果也要过一遍硬过滤（用户说"曼哈顿的意大利餐厅"，
  向量召回的布鲁克林餐厅不能进最终结果）。
- **`knowledge_score` 不能直接当 `quality_prior`**：它是 M1 的 `-log10` 打分，
  量纲不是 0..1。必须做 `min-max` 或用固定映射，并在注释里说明。
- **融合是纯函数**：`Fuse` 不碰 context、数据库、provider，只吃切片吐切片。
  这样它的测试可以完全离线，也保证 M6-04 回放时融合逻辑不变。

**依赖**：M3-01, M3-02, M3-03。
**工作量**：M。

**验收标准**

- 同一查询两次调用，候选顺序与分数**完全一致**。
- 每个候选的 `reasons[]` 至少一条，且每条能对应到一个具体通道与具体内容。
- 关闭 vector 通道（配置 `RETRIEVAL_ENABLE_VECTOR=false`）后全链路可用，
  `trace.RerankApplied=false` 且有降级说明。
- `quality_prior` 权重为 0 时，两个只有 `knowledge_score` 差异的候选得分相同。
- 单通道全同分时，归一化返回 0.5，该通道不改变排序。
- 融合耗时 < 5ms（10,000 候选规模），证明它是纯计算不是瓶颈。
- 三路并发总耗时 ≈ max(单路) 而非 sum(单路)（用 mock provider 延迟验证）。

---

### M3-06 Rerank 接口

**目标**：把 Rerank 做成**可选增强**——接口、Mock 实现、降级路径。无 Rerank 时系统必须正常运行，且不假装 rerank 发生过。

**交付物**

- **`shared/port/rerank.go`（改）**：签名不变，补文档说明**调用契约**。
  ```go
  // RerankProvider optionally reorders candidates. The system must work without
  // one: when no RerankProvider is configured, fusion order is used as-is.
  //
  // Contract for implementations:
  //   - It receives the fused candidate list and the original query.
  //   - It MUST return a permutation of the input: same length, same
  //     RestaurantIDs, no additions and no drops. A provider that filters or
  //     dedups violates the contract silently — the caller cannot tell a bad
  //     ranking from a dropped candidate.
  //   - It MAY rewrite Score and Reasons. It MUST NOT change RestaurantID.
  //   - It SHOULD preserve the input order for candidates it cannot judge,
  //     so an unavailable rerank degrades to fusion order.
  RerankProvider interface {
      ModelID() string
      Rerank(ctx context.Context, query string, candidates []search.RestaurantCandidate) ([]search.RestaurantCandidate, error)
  }
  ```
- **`shared/testkit/mock_provider.go`（改）**：`MockRerankProvider` 增加
  `Reverse bool` 与 `FailFor`（对特定 ID 返回错误），便于测降级。
- **`chat-service/internal/retrieval/rerank.go`（新）**：
  ```go
  // ApplyRerank reranks when a provider is configured and degrades cleanly when
  // it is not. It never returns an error for "no provider" or "provider failed":
  // both are recorded in the trace and the fused order is kept.
  func ApplyRerank(ctx context.Context, query string, candidates []search.RestaurantCandidate, provider port.RerankProvider, timeout time.Duration) ([]search.RestaurantCandidate, *retrieval.RetrievalTrace)
  ```
- **`chat-service/internal/config/config.go`（改）**：`RERANK_PROVIDER` / `RERANK_MODEL` / `RERANK_TIMEOUT`。

**实现要点**

- **校验 permutation**：这是本任务最容易漏的一点。
  `ApplyRerank` 在 provider 返回后**必须**检查返回列表是输入列表的一个排列，
  不是就**丢弃 rerank 结果**（记 trace warning）并保留融合顺序。
  一个把候选过滤掉的 rerank 实现会让"3 家候选"变成"1 家"，而系统不会报错。
- **超时必须独立于 provider**：即使 provider 内部不处理超时，
  `ApplyRerank` 也要用 `context.WithTimeout` 兜底。
  rerank 挂住不能让整个搜索接口挂住——**降级比超时更重要**。
- **`trace.RerankModelID` 为空 = 从未配置**，非空但 `RerankApplied=false`
  = 配置了但失败/超时。两者在 M6-01 的 trace 里含义完全不同，不能合并成一个布尔。
- **不做真实 rerank 模型**。实施计划 §13 明确"Mock 预约、Rerank 模型"是
  MVP 可延后项。M3-06 交付接口 + Mock + 降级，不交付模型接入。

**依赖**：M0-05。
**工作量**：S。

**验收标准**

- 不配置 `RERANK_PROVIDER` 时，`ApplyRerank` 原样返回，`trace.RerankApplied=false`、`RerankModelID=""`。
- 配置 Mock 且 `Reverse=true` 时，顺序反转，`trace.RerankApplied=true`、`RerankModelID="mock-rerank"`。
- Mock 返回非排列（如少一个候选）时，rerank 结果被丢弃，融合顺序保留，trace 有 warning。
- provider 返回错误或超时时，搜索正常返回，`trace` 记降级原因，HTTP 仍 200。
- `go test ./...` 离线全绿（Mock 不依赖网络）。

---

### M3-07 证据组装

**目标**：把召回到的 evidence chunk 组装成可直接交给 LLM（M4）与引用 UI（M6）的结构：**每条引用可定位、可去重、可裁剪、有来源、有时间**。

**交付物**

- **`chat-service/internal/retrieval/assemble.go`（新）**：
  ```go
  // EvidenceBundle is the assembled, deduplicated, budgeted evidence set that
  // is handed to the LLM and rendered as citations.
  type EvidenceBundle struct {
      Items     []evidence.Evidence
      TotalTokens int
      DroppedCount int      // 因预算或去重被丢弃的条数
      Deduplicated int
  }

  func AssembleEvidence(items []evidence.Evidence, opts AssembleOptions) (EvidenceBundle, error)
  ```
- **`shared/domain/evidence/evidence.go`（改）**：`Evidence` 增加引用 UI 需要的字段。
  ```go
  type Evidence struct {
      EvidenceID      int64
      RestaurantID    int64
      // RestaurantName travels with the evidence so a citation can be rendered
      // without a second lookup; M5-03 answers about one restaurant, and a
      // citation that says "该餐厅" instead of its name is not a citation.
      RestaurantName  string  `json:"restaurant_name,omitempty"`
      DocType         DocType
      Title           string
      Content         string
      SourceRecordIDs []string
      Source          string
      SnapshotAt      time.Time
      Score           float64
      // Topic is the review topic for summary documents, empty otherwise.
      Topic string `json:"topic,omitempty"`
      // ContentHash lets the UI deduplicate and lets M6-02 trace a citation
      // back to the exact document version that produced it.
      ContentHash string `json:"content_hash,omitempty"`
  }
  ```
- **`chat-service/internal/transport/httpapi/restaurants.go`（新）**：`/v1` 只读端点。

**去重的三层规则（顺序固定，不可交换）**

1. **同文档重复**：同一 `EvidenceID` 只保留一次（防御性，通常不会发生）。
2. **同内容重复**：同一 `(RestaurantID, DocType, ContentHash)` 只保留
   **分数最高**的一条。这是主力规则——M3-04 已做粗筛，这里做精筛。
3. **同餐厅同类型超额**：同一餐厅同一 `DocType` 最多保留
   `MaxPerDocTypePerRestaurant`（默认 3）条。
   **理由**：一家餐厅 4 条评论主题摘要已经足够支撑回答，
   20 条只会挤掉其他餐厅的证据并让 LLM 抓不住重点。

去重后**按分数降序、EvidenceID 升序**排序，保证可复现。

**Token 预算**

- `MaxEvidenceTokens`（默认 2000，`RETRIEVAL_MAX_EVIDENCE_TOKENS`）。
- **超预算时整条丢弃**，不做截断——半句引用比没有引用更糟。
- 保留分数最高的直到预算用尽，记录 `DroppedCount`。
- **中文按 1 字 ≈ 1 token，英文按 ≈ 4 字符/token 的保守估计**。
  这是估算，不要引入 tokenizer 依赖（M4 若要精确预算再用）。
- **至少保留 1 条**：预算小到装不下任何一条时，返回 `invalid_argument`
  并说明"预算过小"，而不是返回空 bundle（空 bundle 会让 M4 静默地
  产生一个无依据的回答）。

**每条证据的完整性检查（Gate C 第 4 条）**

组装时逐条校验，任一不满足则**丢弃该条并记 warning**：
- `Source` 非空（缺失时回退 `"unknown"` 并保留，但**不算完整**）。
- `SnapshotAt` 非零。
- `Content` 非空（纯空白视为空）。

**`Source` 与 `observed_at` 必须一致**：证据的 `SnapshotAt` 来自
`restaurants.observed_at`（M2 已保证），不是 `now()`。
组装时若发现 `SnapshotAt` 晚于当前时间或早于 2000 年，丢弃并告警——
这通常意味着某处把生成时间误当成了观测时间。

**端点设计（M3 只交付只读、无副作用的三个）**

| 端点 | 用途 | 依赖 |
|---|---|---|
| `POST /v1/restaurants/search` | 硬条件 + 文本查询 → 候选列表 + reasons | M3-01/02/03/05/06 |
| `POST /v1/restaurants/{id}/evidence` | 指定餐厅 + 问题 → 证据 bundle | M3-04/07 |
| `POST /v1/restaurants/evidence` | 候选集 + 问题 → 证据 bundle（M5-03 的形态） | M3-04/07 |

- 三个端点都用 `BindAndValidate` + `httperr.Write`，错误码统一。
- 响应必须包含 `trace`（M3-05 的 `RetrievalTrace`），M4/M6 靠它做回放。
- **不实现 SSE**（M4-11）。M3 的响应是一次性 JSON。
- **request body 里的 `restaurant_id` 是 `int64`**，路径参数也是。
  Hertz 的 path 参数默认是 string，需显式 `strconv.ParseInt` 并处理错误——
  解析失败返回 `invalid_argument`，不是 500。

**依赖**：M3-04。
**工作量**：M。

**验收标准**

- 同一 `(restaurant, doc_type, content_hash)` 的 5 条输入 → 输出 1 条。
- 同一餐厅同一 `doc_type` 超过 3 条 → 保留分数最高的 3 条。
- 预算设为 100 token 时 `TotalTokens <= 100`，且 `DroppedCount > 0`。
- `Source` 为空的证据被丢弃或标记不完整，`trace` 有记录。
- `SnapshotAt` 为零值的证据被丢弃。
- 每条返回的证据都有 `evidence_id`、`source`、`snapshot_at`（Gate C 第 4 条）。
- `POST /v1/restaurants/search` 带非法 borough 返回 400 + `{"error":{"code":"invalid_argument"}}`。
- `POST /v1/restaurants/{abc}/evidence` 返回 400，不是 500。
- 三道 `/v1` 端点在 provider 与 DB 都不可用时返回明确错误码，不是 HTML 错误页。

---

### M3-08 检索测试夹具

**目标**：交付一组**固定查询 + 预期餐厅 + 预期证据**，以及能算出 Recall@K、引用精度、结构化过滤正确率的评测骨架。这是 M6-02 的前置。

**交付物**

- **`chat-service/internal/retrieval/testdata/`（新）**：`retrieval_cases.yaml`（或 `.json`）。
  每条 case：
  ```yaml
  - id: hard_filter_italian_cheap_manhattan
    kind: structured
    query: "曼哈顿的平价意大利餐厅"
    filter:
      cuisines: [italian]
      price_levels: [1, 2]
      borough: manhattan
    expect:
      min_results: 1
      all_satisfy_filter: true
    # 餐厅 ID 从真实库采样后写入；不能用空集合
  - id: soft_condition_quiet_date
    kind: restaurant
    query: "quiet restaurant good for a date"
    expect:
      restaurant_ids: [1234, 5678]   # Recall@5 的真值集
  - id: evidence_wait_topic
    kind: evidence
    restaurant_ids: [1234]
    query: "is there usually a wait"
    expect:
      doc_types: [restaurant_review_summary]
      topics: [wait]
  ```
- **`chat-service/internal/retrieval/eval_test.go`（新）**：指标计算。
  ```go
  // TestRetrievalFixtures computes the Gate C metrics against the real
  // database. It is skipped without PLATEPILOT_REQUIRE_DB=1 because a fixture
  // whose expected ids come from nowhere passes trivially against an empty
  // database — the same trap as E.19.
  func TestRetrievalFixtures(t *testing.T)
  ```
- **`shared/adapter/repository/contract/read_contract.go`（续）**：跨餐厅断言加入契约。

**指标定义（必须与 PRD §11 对齐）**

| 指标 | 定义 | Gate C 阈值 |
|---|---|---|
| `structured_filter_accuracy` | 返回结果 100% 满足硬条件的查询比例 | = 1.0 |
| `retrieval_recall_at_k` | `expect.restaurant_ids` 中出现在 Top-K 的比例 | ≥ 0.6（k=5） |
| `citation_precision` | 返回证据中 `doc_type` 与 `topic` 符合预期的比例 | ≥ 0.7 |
| `cross_restaurant_leak` | 证据中 `restaurant_id` 不在请求集合内的条数 | = 0 |
| `empty_result_rate` | 返回 0 条的合法查询比例 | 报告项，不设阈值 |

**夹具必须用真实数据**（否则整个任务无意义）：

- 从 `restaurants WHERE is_active_for_demo` 采样若干家，
  **用它们的真实 `cuisine_tags` / `price_level` / `borough` 生成夹具**，
  不要凭想象写。这样 `all_satisfy_filter` 断言才是真的。
- 至少覆盖实施计划 §11 RAG 评测清单中属于检索层的项：
  菜系、价格、地区、适合约会、安静、服务体验、排队问题、数据缺失。
- **必须包含反例**：
  - 一个"该数据集中不存在的事实"查询（如"这家店的电话号码"），
    预期是空证据而非编造。
  - 一个"名字部分匹配"查询，验证 M3-02 的模糊匹配。
- 夹具**记录生成时间与语料规模**，因为 Recall 依赖语料：
  语料从 3,000 扩到 5,000 时同一夹具的分数会变，
  不记录就无法判断是检索退化还是语料变了。

**实现要点**

- **测试必须有 `PLATEPILOT_REQUIRE_DB=1` 才跑**，与 M2 E.19 的教训一致：
  跳过时必须显式 `t.Skip` 并打印原因，**不能静默 PASS**。
  `make test` 保持离线可跑，`make test-postgres` 才带 DB。
- **Recall@K 的真值集不能来自检索本身**。用"该查询下人工认为正确的餐厅"
  或"该条件下 100% 满足的集合"作为真值。若真值来自同一套检索，
  评测只是在证明检索和自己一致。
- **指标输出为可读的表**（`t.Log`），便于 M6-02 直接复用与对比。
- **`cross_restaurant_leak` 是硬断言**（必须 = 0），
  其余是软断言（打印 + 失败阈值）。

**依赖**：M3-01, M3-04。
**工作量**：M。

**验收标准**

```bash
PLATEPILOT_REQUIRE_DB=1 go test ./chat-service/internal/retrieval/... -run TestRetrievalFixtures -count=1 -v
```

- 夹具覆盖 ≥ 20 条查询，包含菜系/价格/地区/软条件/排队/数据缺失。
- `structured_filter_accuracy` = 1.0。
- `cross_restaurant_leak` = 0。
- `retrieval_recall_at_k`（k=5）打印在测试输出中，且 ≥ 0.6。
- 不带 `PLATEPILOT_REQUIRE_DB=1` 时该测试 **SKIP 并打印原因**，不 PASS。
- 夹具文件记录语料规模与生成时间。

## 5. 推荐执行顺序与并行化

### 5.1 单人执行（串行）

```text
M3-01 ─┬─> M3-02 ─┐
       │          ├─> M3-05 ─> M3-06
       └─> M3-03 ─┘
M3-01 ─┐
       ├─> M3-04 ─> M3-07
M3-08 ─┘
```

推荐串行路径：`M3-01 → M3-02 → M3-03 → M3-05 → M3-06 → M3-04 → M3-07 → M3-08`。

这条路径的好处是：**在 M3-04（工作量最大的任务）之前，已经有一条可演示的
候选检索链路**（`M3-01 → M3-05 → /v1/restaurants/search`）。
如果 M3-04 卡在数据或迁移问题上，前面五个任务的成果已经可用。

### 5.2 并行建议

M3 的并行空间比 M2 大，因为三路召回通道互相独立：

| 轨道 | 任务 | 说明 |
|---|---|---|
| 轨道 A：读侧适配器 | M3-01, M3-02 | 同一批文件，必须串行（后者复用前者的投影与连接） |
| 轨道 B：向量召回 | M3-03, M3-04 | 共享 `knowledge_read.go`，但**分文件写**可并行；M3-04 的迁移独立 |
| 轨道 C：应用层 | M3-05, M3-06, M3-07 | 纯逻辑，可用 memory 适配器 + fake provider 开发，**不依赖真实库** |
| 轨道 D：评测 | M3-08 | 依赖 A/B 产出，但夹具文件的**编写**可提前（需先采样真实数据） |

单人项目建议同时保持：**1 个读侧适配器任务 + 1 个向量任务 + 1 个应用层任务**。
应用层（轨道 C）是唯一可以完全离线开发的，值得与适配器并行。

### 5.3 三条纵向切片

实施计划 §14 第 4 组是"M3-03/04 → M3-05/07 → M5-02/03"。
M3 内部也应按纵向切分交付：

```text
切片 1（不依赖 embedding）：
  M3-01 + M3-02 + M3-05(仅结构化+关键词) + /v1/restaurants/search
  → 可演示：给定硬条件 + 名称，返回候选与 reasons

切片 2（依赖 embedding）：
  M3-03 + M3-05(完整) + M3-06 + M3-04 + M3-07 + 三个 /v1 端点
  → 可演示："安静适合约会"影响排序；指定餐厅返回带引用的证据

切片 3（收口）：
  M3-08
  → 可演示：Gate C 的 12 条门槛逐条有数
```

## 6. M3 完成定义（DoD）检查清单

### 6.1 每个任务通用 DoD（继承实施计划 §10）

- [ ] 代码已实现。
- [ ] 单元测试或集成测试已添加。
- [ ] 错误路径有明确错误码（新增 `retrieval_*` 族，见下）。
- [ ] 日志中包含 `trace_id`（读路径的 trace 等价物是 `trace.CandidatePool` / 通道明细，
      需进结构化日志，格式与 M1/M2 一致）。
- [ ] 不泄露密钥和 PII（检索只读；评论正文来自已脱敏的 M1 数据）。
- [ ] 不绕过领域接口直连厂商 API（`ollama` 类型不逃逸 `shared/adapter/embedding/ollama`；
      `pgx` 不进入 `chat-service/internal/retrieval`）。
- [ ] 文档或注释说明关键设计（尤其是 borough 下推、permutation 校验、去重顺序、预算策略）。
- [ ] 通过 `go test ./...`、`go vet ./...`、`go test -race ./...`。
- [ ] 相关验收标准可以实际演示。

**M3 新增错误码**（`shared/domain/errs/errs.go` 追加，遵循该文件"只追加、不改语义"的约定）：

| 码 | HTTP | 场景 |
|---|---|---|
| `retrieval_empty_query` | 400 | query 与 filter 均为空 |
| `retrieval_invalid_filter` | 400 | borough 不在 5 个白名单值内、price_level 越界 |
| `retrieval_no_scope` | 400 | evidence 召回未给 restaurant_ids |
| `retrieval_budget_exceeded` | 400 | 证据预算小到装不下任何一条 |
| `retrieval_channel_unavailable` | 200 | 某通道失败但已降级（**只在 trace 里，不作为 HTTP 错误**） |
| `retrieval_rerank_invalid` | 422 | rerank 返回非排列（结果被丢弃，走降级） |

前四个是客户端能修正的问题，最后两个是系统状态——**`retrieval_rerank_invalid`
永远不会作为 HTTP 状态返回**，它只出现在 trace 里；保留这个码是为了让
M6-01 的 trace 聚合能统计 rerank 实现的质量。

### 6.2 M3 里程碑门（Gate C）

实施计划 §9 的 4 条：

- [x] 硬条件过滤正确率 100%。（M3-08 `structured_filter_accuracy` = 1.0）
- [x] 餐厅召回结果可解释。（每个候选 `reasons[]` 可回溯到通道与具体内容）
- [x] 佐证不会跨餐厅返回。（`cross_restaurant_leak` = 0）
- [x] 引用包含 source 和 observed_at。（每条 `Evidence` 字段完整）

本文档补充的扩充分项：

- [x] 两级召回不混 scope（restaurant 召回 100% profile；evidence 召回 100% evidence）
- [x] 过滤在召回前执行（borough 召回 `EXPLAIN` 命中 partial HNSW，非 Seq Scan）
- [x] query 向量维度校验（不符则 `embedding_dimension_mismatch`，不发 DB 查询）
- [x] 融合分数可复现（同查询两次调用分数与顺序完全一致）
- [x] 无 rerank 可运行（`RERANK_PROVIDER` 未配置时全链路可用）
- [x] 证据去重生效（同 `content_hash` 只保留 1 条）
- [x] 端到端延迟（search p95 < 2s；evidence p95 < 1s）
- [x] 空结果不报错（过滤后空集返回 200 + `[]`；餐厅不存在返回 `not_found`）
- [x] 读侧端口收敛完成（memory 与 postgres 都满足 `port.RestaurantRepository` / `port.KnowledgeRepository`）
- [x] scope partial 索引的**实测结论**已记录 —— 见 **E.11**：结论与原计划相反（必需，但理由完全不同；且必需的是 restaurant scope，不是 evidence scope）

## 7. 与后续里程碑的衔接

M3 完成后按实施计划 §14 第 5 组进入 M4（Agent 运行时）。衔接点：

| 后续里程碑 | 依赖 M3 的什么 | 说明 |
|---|---|---|
| M4-05 搜索工具 | `Service.Search` + `RetrievalTrace` | `search_restaurants` 直接包一层，返回候选 ID + reasons |
| M4-06 证据工具 | `Service.Evidence` + `EvidenceBundle` | `get_restaurant_evidence` 返回 bundle，不重新实现去重 |
| M4-09 上下文管理 | `AssembleOptions.MaxEvidenceTokens` | token 预算已在 M3-07 实现，M4 只调参 |
| M4-10 Guardrail | `retrieval_no_scope` 错误码 | "无餐厅上下文时禁止返回证据"是工具层规则，不是检索层 |
| M5-01 硬条件搜索切片 | `POST /v1/restaurants/search` | M5-01 只需在这个端点上加最薄的 Agent 层 |
| M5-02 软条件推荐切片 | `Service.RecallRestaurant` + `ChannelScore` | "安静、适合约会"可影响召回，且能解释为什么 |
| M5-03 指定餐厅问答切片 | `Service.Evidence` + `EvidenceBundle` | 名称 → 实体（M3-02）→ 佐证（M3-04）→ 回答 |
| M6-01 Trace 与日志 | `RetrievalTrace` 已在响应中 | M6-01 只需把它落到 `run_retrievals` 表 |
| M6-02 RAG 数据集 | M3-08 夹具 + 指标骨架 | 50–100 个场景在 M3 的 20 条基础上扩充 |
| M6-05 性能测试 | M6-03 各端点已存在 | p50/p95 直接测 M3 的三个端点 |

**接口稳定性要求**：M3 对 `port` 的收敛是 M4/M5 的契约。若必须再调整，需同步更新：

- 本文档 §2.3、§4 各任务的接口形状。
- `docs/platepilot-implementation-plan.md` 对应任务的依赖与验收。
- 所有实现（postgres / memory）与 `read_contract.go`。
- `shared/domain/search/search.go` 与 `shared/domain/evidence/evidence.go` 的 DTO 字段。

**M3 明确不做的事**（避免范围蔓延）：

- 不做 Agent / 工具注册 / Eino / SSE（M4）。
- 不接真实 Rerank 模型（M3-06 只交付接口 + Mock + 降级；实施计划 §13 已把 Rerank 列为可延后）。
- 不做 trace 持久化（M6-01）；M3 只在响应里返回 `trace`。
- 不实现中文分词检索（实施计划 §17 第 6 条未锁定决策）。
- 不实现前端（M6-06）。
- 不为 `retrieval_scope` 之外的维度做索引优化（只测 `scope` 与 `restaurant_id`）。

## 8. 风险与注意事项

| 风险 | 触发点 | 控制措施 |
|---|---|---|
| 读侧端口签名继续漂移 | M4 开工时又改一次 | M3-01 收敛后加编译期断言 `var _ port.RestaurantRepository = ...`，memory 与 postgres 各一行 |
| borough 过滤退化成 Seq Scan | 用 `metadata->>'borough'` 过滤 | 只用 `borough` 列；`EXPLAIN` 断言守住（M1-03 已实测过这个坑） |
| scope partial 索引不被选中 | 3,000 行 restaurant scope 规模太小 | 实测后**删除**无用索引，不留只占磁盘的结构（E.35 已记录这个交叉点） |
| 融合权重藏在代码里 | M6-02 评测要调参才发现改不了 | 权重全部走配置，`check-config` 打印（M2 E.30 的教训） |
| rerank 实现悄悄丢候选 | provider 返回子集 | `ApplyRerank` 校验 permutation，不符则丢弃 rerank 结果 |
| 证据跨餐厅泄漏 | 空 restaurant_ids 降级为全局检索 | 应用层 + SQL 层各挡一次；`cross_restaurant_leak` 硬断言 |
| 分数不可解释 | 融合后只返回一个数字 | 每通道 `ChannelScore` 保留，`Distance` 与归一化前后值都进 trace |
| `GREATEST` 遇 NULL 失效 | 地址为 NULL 的餐厅永远排最后 | `COALESCE(similarity(...), 0)`（E.34 同类） |
| `ILIKE` 通配符注入 | 用户搜 "50%" | 转义 `\ % _` + `ESCAPE` |
| pg_trgm 短查询全表扫描 | 2 字符查询 | `len < 3` 直接 `invalid_argument` |
| 硬过滤在召回后才做 | 候选不足 topK | 过滤下推 SQL；不足时放宽 topK 重试一次；**宁少不脏** |
| 在线 embedding 成为延迟瓶颈 | 每次请求一次 `EmbedQuery` | 复用 client（M2-01 已是长生命周期对象）；M6-05 测 p95 后决定是否加短 TTL 缓存 |
| 预算裁剪把关键证据裁掉 | 预算过小或排序错 | 整条丢弃不截断；保留分数最高；`DroppedCount` 可见 |
| 评测在空库上假通过 | 夹具真值集为空 | `PLATEPILOT_REQUIRE_DB=1` + 显式 SKIP（M2 E.19 的教训） |
| 数据是 2021 快照被当成实时 | 引用未显示时间 | 每条证据带 `snapshot_at`；M3-07 校验非零；M4 guardrail 禁止表述为"现在" |
| 分词器缺失导致中文查询失效 | 中文查询走 trigram | M3 只保证英文/数字；中文在 M6-02 量化后再决策 |

## 附录 E：实施记录（实施过程中的实际发现）

E.1 – E.10 记录切片 1（M3-01 / M3-02 / M3-05 结构化+关键词部分 / M3-06）
落地时的实测发现；E.11 – E.15 记录 M3-03 / M3-04 / M3-07 / M3-08 落地时的发现。
每条都已按实际情况改代码。

其中**五条是契约测试或评测夹具在真实数据库上抓到的真实缺陷**，不是文档问题：

| 编号 | 缺陷 | 后果 |
|---|---|---|
| E.11.1 | borough 分区的 HNSW beam 被 evidence 占满 | 向量通道召回 **0 条**（exact search 有 5 行） |
| E.12 | 融合层不复检硬条件 | 用户明确排除的餐厅回到结果里（准确率 0.714） |
| E.13 | structured 常数分归一化后变成 0.5 | cut 边界处 ±0.5 的虚假排名差 |
| E.11.5 | `similarityOf` 单侧 clamp | 负相似度排在 0 分命中之前 |
| E.11.4 | 迁移幂等性判据写错 | 断言本身无法失败（见该条） |

### E.1 `knowledge_score` 在全部 3,000 家 demo 餐厅上是同一个常数（已修，非代码缺陷）

融合最初用 `knowledge_score` 作 `quality_prior`，真实数据上完全失效：

```sql
SELECT count(DISTINCT knowledge_score), min(knowledge_score), max(knowledge_score)
FROM restaurants WHERE is_active_for_demo;
--  1 | 5.5 | 5.5
```

原因是 M1 的 score 阶段：`SelectActiveForDemo` 用 `KnowledgeScore` **排序选出**
3,000 家，然后写库时统一写 `-log10(target)`。也就是说这一列记录的是"我们在 36,133
家里挑了 3,000 家"这个**决策**，而不是"这家店本身知识量多少"。

后果不是排序不准，是**排序完全不存在**：融合结果的 `knowledge_score` 归一化后
span = 0，按 `Fuse` 的规则全部映射到中性 0.5，每一家都贡献 0.1。17/20 个候选
并列在 0.6000，只有 id 兜底排序在起作用——而 id 顺序毫无意义。

修法：prior 改用**评分先验**，并按评论样本量做贝叶斯收缩：

```go
// shrinkage = n / (n + 50)
// prior = 4.0 + (rating - 4.0) * shrinkage
```

理由是这一列原本要回答"哪家店更值得推荐"，`knowledge_score` 答不了，
`rating` 能答，但必须带样本量惩罚——3 条评论的 4.9 和 300 条评论的 4.5
不是同一个主张。收缩后的效果（真实库）：

```
 0 Vezzo              4.53 (1161条)  0.6820
 1 99 Cent Fresh Pizza 4.44 (797条)   0.6721
 2 Sfilatino          4.52 (188条)    0.6715
```

**保留 `knowledge_score` 在投影里的理由**：它仍是管线声明的意图，且若将来
score 阶段改为逐店计算真实分数，检索层无需改动即可受益。注释里已写明它当前
测出来是常数。

### E.2 写侧 upsert 不写评分和打分列，契约测试的种子因此"看起来对、实际全空"（已修）

契约测试第一次在真库上跑，红了四条：

```
read_contract.go:107: rating and its sample size must both survive: <nil> / 0
read_contract.go:370: want restaurant 1, got []
restaurant_search_test.go:141: knowledge_score = 0
```

根因是 `restaurantUpsertSQL` 的 update 列表**故意不含** `rating_computed_avg`、
`knowledge_score`、`is_active_for_demo`——那些由 stats / score 阶段写，
meta 重跑不能回退它们（`restaurant.go:39-45` 有注释说明）。

所以种子只调 `UpsertRestaurant` 时，读侧依赖的三列全是默认值 0/NULL，
而 `is_active_for_demo = false` 直接让**所有**查询返回空。测试自己没发现，
因为断言的是"结果里应该有 1"，而它诚实地报告了"结果是空的"。

修法：种子按真实管线顺序跑三个阶段——`UpsertRestaurant` → `UpdateReviewStats`
→ `UpdateScores`。**这不是测试的将就，而是必须的**：契约要验证的是"读侧能读出
管线会产出的东西"，用管线产不出的语料去验证它，验证的是不存在的东西。

### E.3 `likePattern` 把"太短"和"为空"合并成一个错误码（已修）

```go
// 改前：两个失败共用一个返回值 false
trimmed == ""          -> retrieval_empty_query
len([]rune(trimmed)) < 3 -> retrieval_empty_query   ← 错
```

"你什么都没发"和"你发的东西无法匹配"是两条不同的消息，重试其中一个的人
不该去重试另一个。拆成两个错误码后契约测试立刻定位到。

### E.4 通道降级把客户端错误也吞成了 200 空结果（已修）

关键词通道失败时服务会降级继续返回，这对基础设施故障（超时、连接失败）是对的。
但契约测试和真机验证都发现：**"查询太短"也是从同一个通道返回的错误**，
于是 `{"text":"ab"}` 返回 `200 + []`，读起来像"没有叫 ab 的餐厅"，
而真实原因是"你的查询无法匹配"。现在请求缺陷（`retrieval_query_too_short`、
`retrieval_empty_query`）直接上抛，只有真正的可用性故障才降级。

**这条的教训**：降级路径必须有边界，否则它会安静地吞掉所有错误，
包括那些降级也救不了的客户端错误。

### E.5 读侧端口无任何实现且签名已漂移（M3-01 第一条交付物的由来）

`port.RestaurantRepository` / `port.KnowledgeRepository` 是 M0 定义的，
此后**没有任何实现满足它们**，且签名与 M1/M2 落地后不一致：

```go
GetByID(ctx, restaurantID string)  // 而 restaurants.id 是 bigint
VectorSearch(..., filter map[string]any)  // 而写侧是强类型 port.VectorFilter
```

实测 `var _ port.RestaurantRepository = (*memory.RestaurantRepository)(nil)` 编译失败。
文档 §0.1 第 1 条已提前记录，此处只补一句：**这不是遗漏，是 M0 定义端口时
没有真实语料可对照**。收敛动作放在 M3-01，因为 M3 是第一个真正的读侧消费者。

### E.6 `rating_source_avg` 不能用于 `min_rating` 过滤（已在 M3-01 落实）

来源站评分在评论数达到上限时被截顶（`rating_source_avg` 上限 9998），
用它做 `min_rating` 会把来源没打分的店判为达标。读侧投影只用
`rating_computed_avg`（入库评论样本的均值）。这一条在 M2 文档里已记录，
M3-01 只是把它变成 SQL 里的实际选择。

### E.7 通配符转义是必要的，不是洁癖

真实库实测：

```sql
-- 未转义：'%50%%' 匹配 144 行（% 当通配符）
SELECT count(*) FROM restaurants WHERE name ILIKE '%50%%';
-- 144

-- 转义后：'%50!%%' ESCAPE '!' 匹配 0 行（按字面量搜 "50%"）
SELECT count(*) FROM restaurants WHERE name ILIKE '%50!%%' ESCAPE '!';
-- 0
```

两个 trigram 索引都命中（`restaurants_name_trgm` + `restaurants_address_trgm`，
`EXPLAIN` 确认是 `Bitmap Index Scan`）。

### E.8 结构化查询的真实执行计划（36,133 行 + 3,000 demo 行）

```sql
EXPLAIN SELECT id, name FROM restaurants
WHERE is_active_for_demo AND borough = 'manhattan' AND price_level = ANY(ARRAY[1,2])
ORDER BY knowledge_score DESC, id ASC LIMIT 5;
```

命中 `restaurants_active_score` 的 `Index Scan`，`Filter` 施加在索引扫描之后、
`Limit` 之前，**没有全表 Seq Scan**。这满足了 M3-01 验收标准里的最后一条。

`ORDER BY knowledge_score DESC, id ASC` 里的 `knowledge_score` 当前是常数（E.1），
所以实际排序等价于 `id ASC`。等 score 阶段改成真实分数后，这条索引直接开始起作用。

### E.9 切片 1 的实测指标（真实库，3,000 家 demo 餐厅）

| 指标 | 实测 |
|---|---|
| `/v1/restaurants/search` 延迟 | p50 ≈ 9ms，p95 < 11ms |
| 同查询 5 次的候选与分数 | 逐位相同（SHA-256 一致） |
| 候选池（`top_k=12`，两通道各过采样 2 倍） | 48 |
| 关键词命中在融合结果中的位置 | 前 11 名（`Mj Pizza` 1.1358 > `Gotham Pizza` 0.8656 > `La Mia Pizza` 0.8503 …） |

关键词命中的餐厅排在硬条件命中之前，说明融合确实在按相关性重排，
而不是把两路结果简单拼接。

### E.10 未实现但已接线的部分

- 本节记录的是**切片 1 交付时**的状态。向量通道与 `KnowledgeRepository`
  已在 M3-03 / M3-04 交付并接线，见 E.11。此处保留原文，因为它记下了
  "用一条 `Ran: false` 的 trace 诚实标注未运行的通道"这个做法——
  M3-03 沿用了同一套通道状态语义。

### E.11 scope-partitioned HNSW：M3-04 建议的迁移理由是错的，但迁移本身是必需的

**计划说的**：M3-04 原计划论证"8,775 篇 evidence 与 3,000 篇 profile 混在同一个
ANN 结构里，一个 profile 会在 topK 里挤掉 review chunk"，因此建议按
`retrieval_scope` 建 partial 索引。**这个理由站不住**，理由见下面的第 2 条；
但迁移仍然必须做，理由完全不同。

**1. 实测的真实缺陷（这才是迁移的正当理由）**

M3-03 实现向量通道后，向量通道在真实库上**召回 0 条**。这不是"慢"，是"错"：
同一查询关掉索引做 exact search 返回 5 行（42ms）。

原因在 M3-04 原文里没有被写出来，测出来是这样的 —— HNSW 的扫描是**有界 beam**：
它访问约 `hnsw.ef_search`（默认 40）个候选，给它们排名，然后停止；
**scope 过滤在 beam 返回之后才施加**。而 0001 的 borough 分区里：

```text
retrieval_scope | manhattan 分区行数
----------------+---------------
restaurant     |           2,010
evidence       |          18,287   <- 90%
```

一个 restaurant scope 的召回下降进一个 90% 是 evidence 的分区，取到 40 个
几乎全是 evidence 的邻居，scope 过滤把它们全部拒掉：

```text
Index Scan using knowledge_documents_hnsw_manhattan
  rows=0 ... Rows Removed by Filter: 40
```

实测有 8 篇文档距离 < 0.5，而 exact search 返回 5 行。
**提高 `hnsw.ef_search` 只能降低复现频率，不能消除它** —— beam 的名额仍然在和
18,287 行 evidence 竞争。因此修复是 **schema 变更**（把 scope 放进索引谓词），
不是一个调优旋钮。

**2. 证据召回路径不需要 scope ANN（原计划第 2 条理由错误）**

M3-04 实测：证据召回按 `restaurant_ids` 有界查询，
**Bitmap Heap Scan + top-N heapssort** 已经是正确计划 —— 3 餐厅 1.6ms，
20 餐厅 + topic 过滤 4.9ms。`restaurant_id` 有选择性，ANN 的优势不存在。

所以真正的原因是：**restaurant 与 evidence 混在同一 borough 分区导致 beam 被
evidence 吃光**，而不是"ANN 本身慢"。给 evidence scope 单独建 HNSW 索引
在这个工作负载上既无必要也无收益；给 **restaurant** scope 建索引才是必需的。
0004 因此按 `(borough, retrieval_scope)` 建 10 个索引，而不是原计划的 2 个。

**3. 保留 0001 的 borough-only 索引，不 DROP**

删除它们需要在 30k 行表上做 ACCESS EXCLUSIVE 锁下的全量重建，
且 unscoped 召回仍需要它们（那是它们唯一合适的查询形状）。
planner 已经优先选更窄的索引。实测 0004 之后：5 行 0.6ms，无 Rows Removed by Filter。

**4. 迁移契约的幂等性判据是 `AppliedAt` 不是 `Applied`**

`Migrate` 对"本次应用的"和"之前已记录的"都返回 `Applied: true` ——
它的语义是"库已在这个版本"，不是"这次跑了它"。
判别第二次运行为 no-op 的证据是 `AppliedAt`：只有读自账本的行才会填充它，
本次执行的行 `AppliedAt` 为 nil。`TestMigrateIsIdempotent` 用的就是这个判据，
0004 的测试最初写成检查 `Applied`，被 0004 自己的迁移跑出来的第二次结果证伪。

**5. `similarityOf` 双侧 clamp**

pgvector 对非完美归一化的向量可能报告略大于 2 的距离（余弦距离上界是 2），
使 `1 - distance` 为负。负相似度会静默地把一个向量命中排到分数为 0 的
结构化命中之前。`similarityOf` 现在双侧 clamp 到 `[0, 1]`。

### E.12 融合层不复检硬条件，让用户明确排除的餐厅回到了结果里

M3-08 夹具第一次跑出 `structured_filter_accuracy = 0.714`：
"chinese" 的查询里出现了 McDonald's。

根因是软条件通道（keyword / vector）召回的候选**没有重新校验硬条件** ——
融合把三个通道的候选合并成一个池排序，没有回头用 `Filter.Matches(candidate)`
过滤合并后的池。融合层现在按 `Filter.Matches` 重新校验合并池并记一条 warning。
新增域层方法 `RestaurantFilter.Matches`，其中 **unknown 不等于 match**
（未知字段不足以判定满足条件）。

**教训**：召回层按硬条件过滤是不够的；每个通道都过滤之后，
融合（任何跨通道合并）仍然必须再过滤一次，因为通道的召回策略各不相同。

### E.13 structured 通道的常数分在 min-max 后变成 0.5，制造虚假排名差

`structured_filter_accuracy` 修好后暴露出第三个缺陷。
structured 通道的分数是常数（命中即满分），在 min-max 归一化后变成 0.5。
落在其任意 Top-N cut 内/外会造成 **±0.5 的虚假排名差** —— 一个
thin-sample 4.5 的餐厅（`588/640/835`）几乎在每个结果里都出现，
纯粹因为常数分在 cut 边界两侧翻转。

修复：structured 通道加 `structuredOverread = 10` / `maxStructuredDepth = 500` 超读。
**理由**：structured 通道的行是无序的，page size 只决定融合能看到多少正确答案，
超读让融合的候选池里始终有足够的结构化命中去支撑排序，而不是让一个常数分
在 cut 边界上做无意义的翻转。

### E.14 向量通道的降级边界：配置缺陷上抛，可用性故障降级

M3-03 落地时把向量通道的失败分成两类处理：

- **请求缺陷**（`embedding_empty` / `embedding_dimension_mismatch`）**上抛**。
  这是配置缺陷，每次都会失败；静默降级会掩盖它，让搜索"看起来在工作"
  而实际上向量通道从未参与过排序。
- **transient 故障**（provider 不可用 / 超时）**降级**为结构化 + 关键词，
  并在 `trace` 里记降级原因。这条是 M3-05 融合"允许通道缺额"的前提。

store 失败时走 `vectorFailureReason`，不再把错误渲染成裸的 "internal" ——
一个不可读的 note 无法让运维判断是配置错了还是 provider 挂了。

### E.15 M3-08 夹具的实测指标与两个 HTTP 端点

25 个夹具（structured 7 / restaurant 8 / evidence 10），全部从真实语料采样：

| 指标 | 实测 | Gate | 结论 |
|---|---|---|---|
| `structured_filter_accuracy` | 1.000 | = 1.0 | 通过 |
| `retrieval_recall_at_k` | 0.792 | >= 0.6 | 通过 |
| `citation_precision` | 1.000 | >= 0.7 | 通过 |
| `cross_restaurant_leak` | 0 | = 0 | 通过 |
| `empty_result_rate` | 0.040 | 仅报告 | — |

`TestRetrievalFixtures` 需要 `PLATEPILOT_REQUIRE_DB=1`，否则**显式 skip 并打印
"跳过不等于通过"** —— 一个在没有数据库时安静通过的评测套件比红的更危险。
`TestRetrievalFixtureShape` 离线可跑，守住夹具文件本身的形状。

夹具同时交付两个端点（单数与复数形态）：

- `POST /v1/restaurants/{id}/evidence` —— path id 优先于 body，同 id 去重。
- `POST /v1/restaurants/evidence` —— body 必须含 `restaurant_ids`。

`EvidenceQuery.Validate()` 故意返回 nil：scope 校验放在 path 合并之后，
否则 `BindAndValidate` 会先跑，拒掉无 body 的单餐厅路由。

## 附录 A：环境变量（M3 相关）

```bash
# --- 检索（M3 新增）------------------------------------------------------
# 是否启用向量通道。关闭后搜索退化为结构化 + 关键词，
# 用于 embedding provider 不可用时的应急开关与 A/B 对比。
# RETRIEVAL_ENABLE_VECTOR=true

# 融合权重。结构化默认 1.0；关键词与向量都是"辅助排序"，
# 权重低于结构化是为了保证硬条件命中永远排在前面。
# 调这些值不需要改代码 —— M6-02 的评测要靠它们。
# RETRIEVAL_WEIGHT_STRUCTURED=1.0
# RETRIEVAL_WEIGHT_KEYWORD=0.5
# RETRIEVAL_WEIGHT_VECTOR=1.0
# RETRIEVAL_WEIGHT_QUALITY=0.2

# 单通道过采样倍数（融合前）。默认 2，防止单通道裁剪把
# 其他通道排在前面的餐厅裁掉。
# RETRIEVAL_OVERSAMPLE=2

# 最终返回条数。默认 5，上限 100。
# RETRIEVAL_TOP_K=5

# 证据 token 预算（整条丢弃，不截断）。默认 2000。
# RETRIEVAL_MAX_EVIDENCE_TOKENS=2000

# 同一餐厅同一 doc_type 最多保留几条证据。默认 3。
# RETRIEVAL_MAX_PER_DOC_TYPE=3

# --- Rerank（可选，M3-06）-----------------------------------------------
# 不配置即整个功能关闭，系统正常运行。MVP 不要求接真实模型。
# RERANK_PROVIDER=
# RERANK_MODEL=
# RERANK_TIMEOUT=3s

# --- 复用的 M2 配置（M3 在线查询向量化依赖）----------------------------
# EMBEDDING_PROVIDER=ollama
# OLLAMA_BASE_URL=http://localhost:11434
# EMBEDDING_MODEL=qwen3-embedding:0.6b
# EMBEDDING_DIMENSIONS=1024
# 注意：REQUEST_TIMEOUT 默认 180s 是为 M2-06 的批量 embedding 设的。
# 在线查询只发一条短文本，但要显式给检索单独的上限，
# 否则一个慢的 provider 会让 HTTP 请求挂到 180s：
# RETRIEVAL_EMBEDDING_TIMEOUT=5s
```

## 附录 B：常用命令速查

```bash
# 0. 前置（M1/M2 产物，缺一不可）
make pg-up && make migrate
ollama pull qwen3-embedding:0.6b
psql "$POSTGRES_DSN" -c \
  "SELECT retrieval_scope, count(*) FROM knowledge_documents WHERE is_active GROUP BY 1"

# 1. 配置检查（含 retrieval 段）
make run-chat   # 启动前确认 retrieval 配置被解析

# 2. 离线测试（不连数据库，验证读侧端口收敛与应用层逻辑）
make test-offline

# 3. 读侧契约测试（memory + postgres 跑同一套）
PLATEPILOT_REQUIRE_DB=1 make test-postgres

# 4. EXPLAIN 断言（读侧召回路径）
PLATEPILOT_REQUIRE_DB=1 go test ./shared/adapter/repository/postgres/... \
  -run 'Explain|Index' -count=1 -v

# 5. 检索夹具与指标（Gate C 的可执行证据）
PLATEPILOT_REQUIRE_DB=1 go test ./chat-service/internal/retrieval/... \
  -run TestRetrievalFixtures -count=1 -v

# 6. 端到端手工验证
make run-chat &
curl -s localhost:8080/v1/restaurants/search \
  -H 'content-type: application/json' \
  -d '{"filter":{"cuisines":["italian"],"borough":"manhattan"},"text":"trattoria"}' \
  | jq '{count: (.candidates|length), first: .candidates[0], trace: .trace}'

curl -s localhost:8080/v1/restaurants/1234/evidence \
  -H 'content-type: application/json' \
  -d '{"query":"is there usually a wait","top_k":5}' \
  | jq '{count: (.evidence|length), sources: [.evidence[].source]}'

# 7. 负例（应返回 400 + invalid_argument，不是 500）
curl -s localhost:8080/v1/restaurants/search \
  -d '{"filter":{"borough":"Manhattan"}}' | jq .error.code

# 8. 竞态与静态检查
make test-race
make vet
make lint
```

## 附录 C：Gate C 自检查询

```sql
-- 1. 语料前置（M3 依赖 M2 产物）
SELECT retrieval_scope, count(*)
FROM knowledge_documents
WHERE is_active
GROUP BY 1;
-- 期望 restaurant 3000 / evidence 8775

-- 2. scope 不混（M3-03/M3-04）
-- 把参数换成 restaurant 与 evidence 各跑一次，结果必须 100% 命中本 scope
SELECT retrieval_scope, count(*) FROM knowledge_documents
WHERE is_active AND retrieval_scope = 'restaurant'
  AND doc_type <> 'restaurant_profile';

-- 3. 每家恰好 1 个活跃 profile（候选不会重复）
SELECT restaurant_id, count(*) FROM knowledge_documents
WHERE is_active AND retrieval_scope = 'restaurant'
GROUP BY 1 HAVING count(*) <> 1;

-- 4. 引用字段完整性（Gate C 第 4 条）
SELECT count(*) FILTER (WHERE metadata->>'source' IS NULL) AS missing_source,
       count(*) FILTER (WHERE snapshot_at IS NULL)        AS missing_snapshot
FROM knowledge_documents
WHERE is_active AND retrieval_scope = 'evidence';

-- 5. content_hash 为空的文档会让 M3-07 的去重失效
SELECT count(*) FROM knowledge_documents
WHERE is_active AND (content_hash IS NULL OR content_hash = '');

-- 6. scope partial 索引是否真的被选中（M3-04 的实测结论）
EXPLAIN (ANALYZE, BUFFERS)
SELECT document_id FROM knowledge_documents
WHERE is_active AND retrieval_scope = 'evidence'
ORDER BY embedding <=> (SELECT embedding FROM knowledge_documents WHERE document_id = 1)
LIMIT 10;
-- 期望 Index Scan using knowledge_documents_hnsw_scope_evidence
-- 若为 Seq Scan：记录实测结论，并考虑删除未被使用的 partial 索引

-- 7. borough partial 索引（读侧同样必须命中）
EXPLAIN (ANALYZE)
SELECT document_id FROM knowledge_documents
WHERE is_active AND retrieval_scope = 'restaurant' AND borough = 'manhattan'
ORDER BY embedding <=> (SELECT embedding FROM knowledge_documents WHERE document_id = 1)
LIMIT 10;
-- 期望 Index Scan using knowledge_documents_hnsw_manhattan

-- 8. 结构化过滤的索引可用性（M3-01）
EXPLAIN
SELECT id FROM restaurants
WHERE is_active_for_demo AND borough = 'manhattan' AND price_level = ANY(ARRAY[1,2])
ORDER BY knowledge_score DESC, id ASC
LIMIT 5;
-- 不应出现全表 Seq Scan + Sort

-- 9. 检索覆盖度：有多少 demo 餐厅有可召回的 profile
SELECT count(DISTINCT d.restaurant_id)
FROM knowledge_documents d
JOIN restaurants r ON r.id = d.restaurant_id AND r.is_active_for_demo
WHERE d.is_active AND d.retrieval_scope = 'restaurant';
-- 期望 3000；小于 3000 说明有 demo 餐厅无法被召回

-- 10. 去重依据：是否存在重复 content_hash（M3-07 的前提）
SELECT restaurant_id, doc_type, content_hash, count(*)
FROM knowledge_documents
WHERE is_active AND retrieval_scope = 'evidence'
GROUP BY 1,2,3 HAVING count(*) > 1
LIMIT 10;
-- 有结果说明去重规则会实际生效，不是空转
```

## 附录 D：参考文档

- `docs/platepilot-implementation-plan.md` §6 M3、§7 关键路径、§9 Gate C、§10 完成定义、§14 第 4 组
- `docs/platepilot-technical-prd.md` §6.3 两级召回边界、§6.4 Restaurant RAG 链路、§11.3 RAG 评测
- `docs/platepilot-m2-task-document.md` §7 与后续里程碑的衔接表、M2-07 向量索引决策、附录 E 实施记录
- `shared/port/repository.go`（读侧端口，M3-01 收敛）
- `shared/port/writer.go`（`KnowledgeStore.VectorSearch`，写侧验证版本）
- `shared/port/rerank.go`（`RerankProvider`）
- `shared/domain/search/search.go`、`shared/domain/evidence/evidence.go`（M3 扩展的 DTO）
- `shared/adapter/repository/postgres/migrations/0001_init.sql`（`restaurants` / `knowledge_documents` / trgm 与 HNSW 索引）
- `shared/adapter/repository/postgres/knowledge_support.go`（`knowledgeColumns` / `vectorLiteral` / `parseVectorLiteral`）
- `shared/testkit/mock_provider.go`（`MockEmbeddingProvider` / `MockRerankProvider`）
