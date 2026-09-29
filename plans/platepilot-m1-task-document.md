# PlatePilot M1 Atlas 数据底座任务文档

> 版本：v1.0  
> 日期：2026-09-29  
> 依据：`plans/platepilot-implementation-plan.md`（§4 里程碑总览、§6 M1、§7 关键路径、§9 Gate A、§10 完成定义）与 `plans/platepilot-technical-prd.md` v0.12（§2 当前数据基线、§4 数据架构与 Schema、§5 写入链路、§4.11 Atlas Search 索引）  
> 里程碑目标：把 Google Local 2021 原始数据变成**可重复导入、可幂等重建、可查询、可审计**的 MongoDB Atlas 内容底座  
> 退出条件（Gate A）：≥1,000 家餐厅、≥100,000 条评论成功关联；重复执行导入不产生重复数据；数据审计报告可生成；名称/地址模糊查询可用

---

## 0. 如何使用本文档

- 本文档是 M1 的**可执行任务清单**，每个任务独立成节，包含目标、交付物、实现要点、依赖、工作量和可验证的验收标准。
- 任务粒度对齐实施计划的 M1 表格（M1-01 ~ M1-11）。任务 ID 与实施计划保持一致，便于交叉引用。
- 每个任务的**完成定义（DoD）**默认继承实施计划 §10：代码实现 + 测试 + 错误码 + `trace_id` 日志 + 不泄露密钥/PII + 不经领域接口直连厂商 API + 关键设计有注释 + 通过 `go test ./...`、`go vet ./...`、`golangci-lint` + 验收标准可实际演示。
- 本文档中的 Go 类型、接口签名和 BSON 结构是**约定形状**，允许在不破坏领域边界的前提下微调；一旦调整，必须同步更新本文件、`port` 接口和所有实现（含内存实现）。
- 模块路径沿用 M0 约定：`github.com/zed/platepilot`。
- 服务边界沿用 M0：写入链路只落在 `data-pipeline` 与 `shared/adapter/repository/mongo`，**不得**在 `chat-service` 中写库；`data-pipeline` 与 `chat-service` 互不 import。

### 0.2 实现状态（2026-09-29）

M1 写入链路已实现，代码位于 `data-pipeline` 与 `shared/adapter/repository/mongo`。
除特别说明外，全部任务已完成。

**决策：`restaurant_documents` 合并进 `restaurants`（2026-09-29）**

原设计把 `hours` / `attributes_raw` / `description` / `relative_results`
拆到独立的 `restaurant_documents`。实际跑下来这条路有三个问题：文档数是主表的
约 3 倍（23,908 家餐厅对应 73,105 份附属文档）、这些字段**只写不读**、而且读取时
总要和餐厅一起 join。按 MongoDB「读在一起的写在一起」的原则改为内嵌：

- `restaurants.hours`：`[]HoursEntry`，每项保留原始文本（如 `"11AM–10PM"`）便于展示
- `restaurants.attributes_raw`：原始 MISC 对象，保留以便重跑清洗规则而无需重读 61MB 源文件
- `restaurants.relative_results`：相似 POI id
- `description` 本来就已是主字段，不再重复

于是内容 collection 从 4 个减为 3 个（`restaurants` / `reviews` / `review_summaries`），
`migrate` 共创建 5 个 collection（含 2 个审计表）。单文档体积仍在数 KB 量级，
远低于 16MB 上限。

Mongo adapter 有三层验证，默认无需 Atlas：

1. **内存实现**：`shared/adapter/repository/memory` 提供端口级 mock，`go test ./...`
   离线全绿，内存实现覆盖率约 90%。
2. **离线单测**：`mongo/query_unit_test.go` 断言 adapter 生成的 filter / update /
   aggregation 文档，无需服务器（覆盖率约 17%）。
3. **memongo**：`make test-mongo` 会下载并启动一个真实的 `mongod`，
   `TestMain` 把 `MONGO_URI` 指向它，于是同一套 Atlas 契约测试跑在真实服务器上
   （覆盖率约 80%）。设 `MONGO_URI` 时改为使用真实集群。

> 真实服务器验证一共暴露了**五个**只在实跑时才出现的缺陷，全部已修复并补了回归测试：
>
> 1. `EnsureSchema` 依赖 CreateCollection 错误码判断集合是否存在，第二次 `migrate`
>    误报 created —— 改为先 `ListCollectionNames`。
> 2. meta 导入整段 `$set` 了 `rating` 子文档，覆盖统计任务写入的 `rating.computed_avg`
>    —— 改为只写 `rating.source_avg`。
> 3. meta 导入把 `review_stats` 整段放进 `$setOnInsert` 且写的是**空结构**，
>    导致 `source_review_count` / `source_review_count_capped` 永远为 0
>    （3748 家餐厅无一命中）—— 改为 `$set` 写入 Meta 拥有的 source 字段，
>    `$setOnInsert` 只初始化采样计数字段。这是本次最严重的一个数据丢失缺陷。
> 4. `written` 指标把 MatchedCount 与 ModifiedCount 相加，命中且被修改的文档被
>    重复计数（二次导入 3937 条记录报出 7873）—— 改为 `upserted + matched`。
> 5. `deduped` 恒为 0，从未统计输入流中的重复记录 —— 新增有界的 in-run
>    dedup tracker（按 `source_record_id` / `review_id`），超过上限时降级为下界，
>    不影响"不产生重复文档"的正确性。
>
> 修好后再跑真实数据：meta `accepted=3937 / written=3937 / deduped=189`，
> review `accepted=26910 / written=26910 / deduped=4169`，落库
> `restaurants=3748`（distinct `source_record_id` 同为 3748）、`reviews=22741`，
> 二次导入所有计数完全不变。

| 任务 | 状态 | 主要落地文件 |
|---|---|---|
| M1-01 Atlas 连接 | 完成 | `shared/adapter/repository/mongo/client.go`、`errors.go` |
| M1-02 Collection 定义 | 完成 | `shared/adapter/repository/mongo/schema.go`、`data-pipeline/internal/pipeline/migrate.go` |
| M1-03 基础索引 | 完成 | `shared/adapter/repository/mongo/indexes.go` |
| M1-04 Meta 流式导入 | 完成 | `data-pipeline/internal/pipeline/raw/{jsonl,meta}.go`、`import.go` |
| M1-05 Review 流式导入 | 完成 | `data-pipeline/internal/pipeline/raw/review.go`、`import.go` |
| M1-06 清洗与归一化 | 完成 | `data-pipeline/internal/pipeline/curate/*` |
| M1-07 去重和幂等 | 完成 | `curate/dedup.go`、`mongo/restaurant.go`、`mongo/review.go` |
| M1-08 评论统计聚合 | 完成 | `curate/stats.go`、`mongo/review.go`、`import.go` |
| M1-09 数据审计报告 | 完成 | `data-pipeline/internal/pipeline/report/report.go`、`mongo/pipeline.go` |
| M1-10 精选餐厅集合 | 完成 | `curate/score.go`、`mongo/restaurant.go` |
| M1-11 Atlas Search 索引 | 完成（定义 + 校验；创建需在 Atlas 控制台执行） | `mongo/search_index.go`、`scripts/atlas/` |
| §4.0 领域与接口 | 完成（形状有调整，见下） | `shared/domain/restaurant`、`shared/domain/review`、`shared/port/writer.go` |

**与本文档正文的差异（实现后记录）**

1. **读写端口分离，而不是改 `RestaurantRepository`**：M0 的读侧
   `RestaurantRepository` / `KnowledgeRepository` 等**保持不变**（M3 使用），
   新增写侧端口 `RestaurantStore`、`ReviewStore`、`PipelineStore`
   （`shared/port/writer.go`）。理由：实施计划 §3.2 要求写入与读取严格分离；
   若把写方法塞进 `RestaurantRepository`，M1 就被迫实现 M3 的 `Search`。
2. **`UpdateReviewStats` 增加了评分参数**：
   `UpdateReviewStats(ctx, restaurantID, stats restaurant.ReviewStats, computed restaurant.Rating)`。
   `rating.computed_avg` 与 `review_stats` 来自同一份评论样本，因此一起写入。
3. **`RestaurantStore` 的实际方法集**：`GetByID`、`GetBySourceRecordID`、
   `ListRestaurants`、`MapSourceRecordIDs`、`UpsertRestaurant`、
   `UpsertRestaurants`、`UpdateReviewStats`、`UpdateScores`、`SelectForDemo`、
   `CountActiveForDemo`、`UpsertDocuments`。比 §4.0 的草案多了
   `GetByID` / `ListRestaurants` / `MapSourceRecordIDs`（统计重建、打分与评论关联需要）。
4. **短评处理**：正文要求"删除超短评论"。实现选择**保留该行、清空其文本**，
   这样 `stored_review_count` 仍反映评分样本，而 `text_review_count` 只统计可用文本，
   两个计数口径才有意义。
5. **餐厅 ID**：仍按正文生成 UUID，但 upsert 对 `_id` / `created_at` 使用
   `$setOnInsert`（内存实现同样保留既有 ID），因此重复导入不会改变餐厅 ID，
   评论外键保持稳定。
6. **索引名显式命名**：`uniq_source_record_id`、`ix_search_filters` 等，便于审计；
   与附录 C 一致。
7. **Atlas Search 索引**：定义已版本化并由单测保证与 `SearchIndexDefinition()`
   一致；**创建索引本身是 Atlas 控制台/Admin API 操作**，Go 驱动不做这件事。

### 0.1 工作量级定义

| 级别 | 含义 |
|---|---|
| S | 半天以内 |
| M | 约 1–2 天 |
| L | 约 3–5 天 |

### 0.2 前置条件与起点（M0 已交付）

M1 直接建立在 M0 的双服务骨架上，启动 M1 前应确认以下 M0 产物可用：

| M0 产物 | 位置 | M1 如何使用 |
|---|---|---|
| 双服务骨架与 CLI 分发 | `data-pipeline/main.go`、`chat-service/main.go` | M1 在 `data-pipeline` 中补 `migrate` / `import` 子命令 |
| 共享配置原语 | `shared/config/config.go` | 复用 `MongoConfig`（`MONGO_URI` / `MONGO_DATABASE` / `MONGO_TIMEOUT`）、`PipelineConfig` |
| 错误码与日志 | `shared/domain/errs`、`shared/observability/logging` | 批处理错误与批次日志复用统一错误码与 JSON 日志 |
| 领域 DTO 与 Repository 接口 | `shared/domain/*`、`shared/port/repository.go` | M1 按 §4.0 扩展 `restaurant` / `review` 领域与 `RestaurantRepository` |
| Mongo Adapter 占位 | `shared/adapter/repository/mongo/doc.go` | M1-01 在此实现，替换 TODO |
| Pipeline 占位 | `data-pipeline/internal/pipeline/pipeline.go` | `Import` 的 `notImplemented` 占位由 M1-04 起逐步替换 |
| 内存 Repository 与契约 | `shared/adapter/repository/memory/*` | M1 新增契约测试同时跑内存实现与 Mongo 实现 |

**当前数据（只读输入）**

| 数据 | 路径 | 规模 |
|---|---|---|
| Meta | `data/raw/google_local/meta-New_York.json.gz` | 272,189 行；270,720 唯一 `gmap_id`；约 17,763 家餐饮相关场所 |
| Review | `data/raw/google_local/review-New_York.json.gz` | 33,459,761 行；约 4,989,572 条可关联；约 2,757,510 条有文本 |
| Demo | `data/demo/demo_restaurants.json` | 5 家餐厅，仅用于对照 curated schema |

> Review 原始文件解压前约 2.65 GB，**必须**流式读取，禁止 `io.ReadAll` 或整文件 `json.Unmarshal`。

---

## 1. M1 目标与退出条件

### 1.1 里程碑目标

M1 只解决"数据底座"，不承载检索与 Agent 逻辑；它要交付一条**可反复运行的写入链路**：

1. Go 服务能连接 MongoDB Atlas，握手、ping、连接池与超时可控。
2. `restaurants`、`reviews`、`review_summaries` 三个内容 collection 有显式、可重复执行的创建与索引脚本（附属资料内嵌进 `restaurants`，见 §0.2 决策）。
3. 能以流式方式导入 Meta 与 Review 原始 JSONL，产出批次统计。
4. 清洗与归一化规则确定、可测试、可审计（category / price / hours / state / MISC）。
5. 去重与幂等规则确定：重复导入同一批次不产生重复数据。
6. 评论数量与评分口径按 PRD §4.6 物化进 `restaurants.review_stats`。
7. 每次导入生成可查询的审计报告（`ingestion_batches`）。
8. 能稳定选出 2,000–5,000 家高覆盖餐厅作为 `is_active_for_demo`。
9. 餐厅名称 / 地址 / 类别 / 描述的 Atlas Search 索引可用，模糊查询返回可解释结果。

### 1.2 退出条件（Milestone Exit Criteria / Gate A）

- [ ] `data-pipeline migrate` 可重复执行，第二次运行不报错、不重复创建。
- [ ] `data-pipeline import --stage=meta --limit=N` 可导入有限样本并输出批次统计。
- [ ] `data-pipeline import --stage=all --limit=N` 可导入样本评论并正确写入 `restaurant_id`。
- [ ] 同一批次连续导入两次，`restaurants`、`reviews` 文档数不增长（幂等）。
- [ ] `restaurants.review_stats` 的 `source_/stored_/text_/embedded_review_count` 语义清晰、可重建、可校验。
- [ ] 每次导入在 `ingestion_batches` 生成一条报告，含行数、成功数、去重数、拒绝数、缺失字段统计、耗时。
- [ ] `is_active_for_demo=true` 的餐厅数量落在 2,000–5,000 区间。
- [ ] 对 `restaurants.name` / `address` 的模糊查询返回结果并可解释（命中字段 + 分数）。
- [ ] 至少 1,000 家餐厅、100,000 条评论成功关联（Gate A 下限）。
- [ ] 领域层仍无 Mongo/BSON 依赖（`shared/domain/architecture_test.go` 通过）。
- [ ] 原始 `user_id`、`name`、`pics` **不出现**在任何 curated collection 中。
- [ ] Mongo adapter 与内存 adapter 通过同一套契约测试。

---

## 2. 目标数据形态

### 2.1 分层与依赖方向

```text
data/pipeline (data-pipeline)
  raw/*  (gzip JSONL reader, raw structs)          ← 只读输入，不导出
     │
     ▼
  curate/* (normalize, dedup, score, stats)        ← 纯函数，可单测
     │  产出 shared/domain 的 curated DTO
     ▼
shared/port (RestaurantRepository, ReviewRepository, PipelineRepository ...)
     │
     ▼
shared/adapter/repository/mongo (BSON 映射 + 索引 + upsert)  ← Mongo 类型只在此层
     │
     ▼
MongoDB Atlas: restaurants / reviews / review_summaries / ingestion_*
```

依赖方向固定：`pipeline(curate)` → `shared/domain` → `shared/port` ← `shared/adapter/repository/mongo`。  
`data-pipeline` 不 import `chat-service`；`shared/domain` 不 import Mongo/BSON。

### 2.2 目标目录结构（M1 结束时）

```text
platepilot/
├── data-pipeline/
│   └── internal/
│       ├── config/                 # 复用 / 扩展 PipelineConfig（--limit 等运行参数）
│       └── pipeline/
│           ├── pipeline.go         # Import 编排：meta → curate → review → stats → report
│           ├── raw/                # 原始 schema 与流式 reader
│           │   ├── meta.go         # Meta 原始 struct（含 MISC）
│           │   ├── review.go       # Review 原始 struct
│           │   └── jsonl.go        # gzip JSONL 流式解码 + 行号/错误定位
│           ├── curate/             # 清洗、归一化、去重、打分（纯函数）
│           │   ├── normalize.go    # category/price/hours/state/MISC → DTO
│           │   ├── cuisine.go      # category → cuisine_tags 映射表
│           │   ├── dedup.go        # review_id = sha256(...)
│           │   ├── score.go        # knowledge_score 与 is_active_for_demo
│           │   ├── pii.go          # 邮箱/电话脱敏
│           │   └── stats.go        # 评论数量与评分口径
│           ├── report/             # 批次统计与审计报告
│           │   └── report.go
│           └── import.go           # import 子命令参数解析、分批写入
├── shared/
│   ├── domain/
│   │   ├── restaurant/             # 新增：curated 主数据、hours、attributes、review_stats
│   │   └── review/                 # 新增：curated review、review summary、审计报告 DTO
│   ├── port/
│   │   └── repository.go           # 扩展 RestaurantRepository + 新增 ReviewRepository / PipelineRepository
│   └── adapter/
│       └── repository/
│           ├── mongo/              # 新增：client、collections、indexes、restaurant/review/pipeline 实现
│           │   ├── client.go
│           │   ├── schema.go       # collection 名与 BSON 模型
│           │   ├── indexes.go      # 索引定义（幂等 ensure）
│           │   ├── search_index.go # Atlas Search 索引 JSON 生成
│           │   ├── restaurant.go
│           │   ├── review.go
│           │   ├── pipeline.go
│           │   └── *_test.go       # 契约测试（env-gated 连 Atlas）
│           ├── memory/             # 扩展：实现新接口以复用契约测试
│           └── contract/           # 新增：跨实现契约测试套件
└── scripts/
    └── atlas/
        ├── search_index_restaurants.json   # M1-11 可提交到 Atlas 的 Search 索引定义
        └── README.md
```

> 说明：把原始 schema 放在 `data-pipeline/internal/pipeline/raw`（而非 `shared/domain`），因为原始字段是**输入格式**、不是领域语言；MISC/字段拼写/异常值都应在这一层收敛。

### 2.3 四个内容 Collection（M1 范围）

| Collection | 用途 | 主键 / 唯一键 | 主要索引 |
|---|---|---|---|
| `restaurants` | 餐厅结构化主数据（每餐厅一文档） | `source_record_id`（= `gmap_id`）唯一 | `location` 2dsphere、筛选项复合 |
| `reviews` | 清洗后的评论 | `_id` = `review_id`（确定性哈希） | `{restaurant_id, reviewed_at}`、`text_hash` |
| `review_summaries` | 预计算主题 / 情绪摘要 | `{restaurant_id, topic}` 唯一 | `restaurant_id` |

> `knowledge_documents`（含向量）属于 M2-07；Agent 运行类 collection（`conversations` / `agent_runs` / `tool_calls` / `user_memories` 等）属于 M4。M1 **不创建**这些 collection，但 collection 命名与字段语义不得与 PRD §4.7/§4.8 冲突。
> 审计所需的 `ingestion_batches`、`ingestion_rejections` 由 M1-09 创建。

---

## 3. 任务清单总览

| ID | 任务 | 交付物 | 依赖 | 工作量 | 验收摘要 |
|---|---|---|---|---|---|
| M1-01 | Atlas 连接 | Mongo Client、连接池、超时、健康检查 | M0-02 | S | 本地能连接 Atlas 并 ping 成功 |
| M1-02 | Collection 定义 | `restaurants`、`reviews`、`review_summaries` | M1-01 | M | `migrate` 脚本可重复执行 |
| M1-03 | 基础索引 | 唯一索引、时间索引、复合索引、2dsphere | M1-02 | M | 关键查询无全表扫描 |
| M1-04 | Meta 流式导入 | gzip JSONL Reader + batch writer | M1-02 | L | 可导入有限样本并输出批次统计 |
| M1-05 | Review 流式导入 | Review Reader、关联、批量写入 | M1-04 | L | 样本评论正确关联 `restaurant_id` |
| M1-06 | 清洗与归一化 | category/price/hours/state/MISC 转换 | M1-04 | L | 规则有单测与审计样本 |
| M1-07 | 去重和幂等 | `gmap_id`、`sha256(...)`、upsert 规则 | M1-04, M1-05 | M | 重复导入不重复；`user_id` 不入库 |
| M1-08 | 评论统计聚合 | source/stored/text/embedded count、评分统计 | M1-05 | M | `review_stats` 可重建与校验 |
| M1-09 | 数据审计报告 | 行数、拒绝数、缺失字段、分布统计 | M1-04, M1-05 | M | 每次导入生成可查询报告 |
| M1-10 | 精选餐厅集合 | `knowledge_score`、`is_active_for_demo` | M1-06, M1-08 | M | 稳定选出 2,000–5,000 家 |
| M1-11 | Atlas Search 索引 | 名称/地址/类别/描述的 Search Index | M1-02, M1-03 | M | 名称与地址模糊查询可解释 |

### 3.1 依赖图

```text
M1-01 (Atlas 连接)
  └─> M1-02 (Collection 定义)
        ├─> M1-03 (基础索引) ──> M1-11 (Atlas Search 索引)
        └─> M1-04 (Meta 流式导入)
              ├─> M1-06 (清洗与归一化) ─┐
              └─> M1-05 (Review 流式导入)
                    ├─> M1-07 (去重与幂等)
                    ├─> M1-08 (评论统计聚合) ─┤
                    └─> M1-09 (审计报告)      │
                                              └─> M1-10 (精选餐厅集合)
```

### 3.2 推荐执行顺序

1. M1-01 Atlas 连接
2. M1-02 Collection 定义
3. M1-03 基础索引
4. M1-04 Meta 流式导入（先打通最小样本）
5. M1-06 清洗与归一化（修正第 4 步产出的字段）
6. M1-05 Review 流式导入
7. M1-07 去重和幂等
8. M1-08 评论统计聚合
9. M1-09 数据审计报告
10. M1-10 精选餐厅集合
11. M1-11 Atlas Search 索引

> 最小可演示链路：`M1-01 → M1-02 → M1-03 → M1-04`，完成后即可验证"Go 服务能连 Atlas、建集合与索引、导入 Meta 样本"。这是实施计划 §7 第一条最小链路的前半段。

---

## 4. 详细任务

### 4.0 M1 前置：领域与接口调整（随 M1-01/M1-02 一并落地）

M0 的 `search.RestaurantDetail` 是最小投影（"Fields are expected to grow in M1"），而 PRD §4.3 的 `restaurants` 文档字段明显更多。为避免把"存储文档"塞进"检索投影"，M1 引入两个新的纯领域包，并相应扩展 `port`：

```go
// shared/domain/restaurant/restaurant.go
package restaurant

type Restaurant struct {
	ID             string
	Source         string        // "google_local_2021"
	SourceRecordID string        // gmap_id
	Name           string
	Address        string
	BoroughGuess   string
	Location       *GeoPoint
	Categories     []string
	CuisineTags    []string
	Description    string
	Price          Price
	Rating         Rating
	ReviewStats    ReviewStats
	Attributes     Attributes
	SnapshotStatus string        // open | closed | permanently_closed | unknown
	KnowledgeScore float64
	IsActiveForDemo bool
	ObservedAt     time.Time
	SourceURL      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type GeoPoint struct{ Longitude, Latitude float64 }

// Document holds auxiliary restaurant payloads that do not belong in the
// main restaurant document (hours, raw attributes, description snapshot...).
type Document struct {
	RestaurantID   string
	DocumentType   string // hours | attributes_raw | description | relative_results | source_snapshot
	Raw            any
	Normalized     any
	ObservedAt     time.Time
	SourceRecordID string
}
type Price struct{ Raw string; Level *int }
type Rating struct{ SourceAvg *float64; ComputedAvg *float64; RatingCountForComputedAvg int }
type Attributes struct {
	TriState map[string]string // "true" | "false" | "unknown"
	AtmosphereTags   []string
	PopularForTags   []string
	ServiceOptionTags []string
	AccessibilityTags []string
}
type ReviewStats struct {
	SourceReviewCount          int
	SourceReviewCountCapped    bool
	StoredReviewCount          int
	TextReviewCount            int
	RepresentativeReviewCount  int
	EmbeddedReviewCount        int
	LastReviewedAt             *time.Time
	StatsUpdatedAt             time.Time
}
```

```go
// shared/domain/review/review.go
package review

type Review struct {
	ID             string    // sha256(gmap_id + user_id + time + text_hash)
	RestaurantID   string
	Rating         int
	ReviewedAt     time.Time
	Text           string
	Language       string
	TextHash       string
	IsRepresentative bool
	TopicTags      []string
	SourceObservedAt time.Time
}

type Summary struct {
	RestaurantID string
	Topic        string
	Sentiment    float64
	PositiveRatio float64
	Summary      string
	EvidenceCount int
	ValidFrom    time.Time
	ValidTo      time.Time
	GeneratedBy  string
	GeneratedAt  time.Time
}

type BatchReport struct { /* M1-09 定义，见该节 */ }
type Rejection struct {
	BatchID  string
	Stage    string   // meta | review
	LineNo   int64
	Reason   string
	SourceRecordID string
}
```

对应的 `shared/port/repository.go` 调整（**约定形状**）：

实际实现采用**读写端口分离**（`shared/port/writer.go`），M0 的读侧
`RestaurantRepository` / `KnowledgeRepository` 保持不变：

```go
type RestaurantStore interface {
	GetByID(ctx context.Context, restaurantID string) (restaurant.Restaurant, error)
	GetBySourceRecordID(ctx context.Context, sourceRecordID string) (restaurant.Restaurant, error)
	ListRestaurants(ctx context.Context, limit int) ([]restaurant.Restaurant, error)
	MapSourceRecordIDs(ctx context.Context, sourceRecordIDs []string) (map[string]string, error)
	UpsertRestaurant(ctx context.Context, r restaurant.Restaurant) error
	UpsertRestaurants(ctx context.Context, rs []restaurant.Restaurant) (int, error)
	UpdateReviewStats(ctx context.Context, restaurantID string, stats restaurant.ReviewStats, computed restaurant.Rating) error
	UpdateScores(ctx context.Context, scores map[string]float64, active map[string]bool) error
	SelectForDemo(ctx context.Context, limit int) ([]restaurant.Restaurant, error)
	CountActiveForDemo(ctx context.Context) (int64, error)
	UpsertDocuments(ctx context.Context, docs []restaurant.Document) error
}

type ReviewStore interface {
	UpsertReviews(ctx context.Context, items []review.Review) (int, error)
	ListByRestaurant(ctx context.Context, restaurantID string, limit int) ([]review.Review, error)
	CountByRestaurant(ctx context.Context, restaurantID string) (review.Counts, error)
	AggregateStats(ctx context.Context, restaurantIDs []string) (map[string]review.Counts, error)
	RestaurantIDsWithReviews(ctx context.Context) ([]string, error)
}

type PipelineStore interface {
	StartBatch(ctx context.Context, report review.BatchReport) error
	FinishBatch(ctx context.Context, report review.BatchReport) error
	RecordRejections(ctx context.Context, items []review.Rejection) error
	ListBatches(ctx context.Context, limit int) ([]review.BatchReport, error)
	BatchDetail(ctx context.Context, batchID string) (review.BatchReport, []review.Rejection, error)
}
```

**实现要点与约束**

- 新增领域包必须继续通过 `shared/domain/architecture_test.go`（只依赖标准库与 `shared/domain` 前缀）。
- 类型名 `Restaurant` 在 `restaurant` 包内会有 `restaurant.Restaurant` 的重复感；若采用 `restaurant.Doc` 等别名，需全局统一并在本文件标注。
- `Attributes.TriState` 使用字符串三态（`"true"`/`"false"`/`"unknown"`），**不**用 `bool`，避免把缺失当 `false`（PRD §4.3）。
- 扩展 `port` 后，必须同步更新：本文档、`shared/adapter/repository/memory/*`（含其单测）、`shared/testkit/*`，并把新接口纳入 `shared/adapter/repository/contract` 契约测试。

**依赖**：M0-04、M0-06。  
**工作量**：并入 M1-01/M1-02。

**验收标准**

- `go test ./...` 通过，含领域纯净性测试。
- 内存实现与 Mongo 实现均满足同一套契约测试。
- `search.RestaurantDetail` 仍保留（供 M3 API 投影用），不强行并入 `restaurant.Restaurant`。

---

### M1-01 Atlas 连接

**目标**：实现 `shared/adapter/repository/mongo` 的客户端与健康检查，使服务能安全连接 Atlas。

**交付物**

- `shared/adapter/repository/mongo/client.go`：
  - `type Config struct { URI, Database string; Timeout time.Duration; MaxPoolSize, MinPoolSize uint64; ConnectTimeout, SocketTimeout time.Duration; RetryWrites bool }`。
  - `func Connect(ctx context.Context, cfg Config) (*Client, error)`：`mongo.Connect` + `client.Ping` 握手；失败即返回带 `provider_unavailable` 的错误。
  - `func (c *Client) Ping(ctx context.Context) error`：供健康检查与 `migrate` 前置校验。
  - `func (c *Client) Database() *mongo.Database` **仅包内使用**；对外只暴露领域方法，`*mongo.*` 类型不得逃逸。
  - `func (c *Client) Close(ctx context.Context) error`。
  - 从 `shared/config.MongoConfig` 构造 `Config` 的适配函数。
- `mongo/doc.go`：移除 M1-01 TODO，写明包边界（"Mongo/BSON types must not escape this package"）。
- 单测：`client_test.go` 覆盖配置校验与错误映射；连接测试用**环境门控**（见下）。
- `chat-service` 健康检查接线：`/healthz` 在配置了 Mongo 时附加 `mongo: ok/degraded`（未配置则跳过，不阻塞启动）。

**实现要点**

- Driver：使用官方 MongoDB Go Driver。落地时在 M1-01 锁定一条主线并全局统一 import path（v1：`go.mongodb.org/mongo-driver/mongo`；若选 v2 线则统一 `.../v2`），**不得在同一仓库混用两条线**。
- 连接池按"两服务各自持有自己的 Client"设计：`data-pipeline` 的池可小而长寿，`chat-service` 的池按并发读调优。默认从环境变量读取，未配置时 `MaxPoolSize` 取驱动默认。
- 超时分层：`ConnectTimeout`（握手）用 `MONGO_TIMEOUT`；单次操作（ping / 查询）用 `context` 派生超时，避免一个慢查询占满连接。
- `RetryWrites` 默认 `true`（Atlas 支持）；写入必须可重试幂等（与 M1-07 的 upsert 规则配合）。
- 错误映射：驱动"连不上 / 认证失败 / 超时"统一映射为 `errs` 的 `provider_unavailable` / `provider_timeout`；不要泄露连接串。
- `.env.example` 增加 `MONGO_CONNECT_TIMEOUT` / `MONGO_MAX_POOL_SIZE` / `MONGO_MIN_POOL_SIZE`（可选，带默认值）。

**依赖**：M0-02（配置）。  
**工作量**：S。

**验收标准**

```bash
# 配置了 MONGO_URI 时
go run ./data-pipeline check-config         # 摘要显示 mongo enabled=true
data-pipeline migrate --ping-only           # 成功 PING，错误时给出清晰错误码

# 未配置 MONGO_URI 时
go test ./...
```

- `MONGO_URI` 正确时 `Ping` 成功；URI 错误或网络不可达时返回 `provider_unavailable` / `provider_timeout`，日志与错误均不含明文凭据。
- `go test ./...` 在没有 Atlas 的环境下全绿（连接测试 env-gated）。
- 日志为 JSON 且包含 `trace_id` / `request_id`。

---

### M1-02 Collection 定义

**目标**：定义四个内容 collection 的显式创建逻辑与 BSON 模型，使 `migrate` 可重复执行。

**交付物**

- 新增 `data-pipeline migrate` 子命令（扩展 `data-pipeline/main.go` 的 usage 与分发）：
  - `data-pipeline migrate`（创建 collection + 索引）
  - `data-pipeline migrate --drop`（仅本地/test，显式危险操作，二次确认参数）
  - 成功后打印每个 collection 的 `created` / `existing` 状态。
- `shared/adapter/repository/mongo/schema.go`：
  - collection 名常量：`CollectionRestaurants = "restaurants"` 等。
  - 每个 collection 的 `ensure` 函数，幂等（`CreateCollection` 在 namespace 存在时忽略 `NamespaceExists`）。
  - BSON 模型（**只在此层**）：`restaurantDoc`、`restaurantDocumentDoc`、`reviewDoc`、`reviewSummaryDoc`。
  - `docToDomain` / `domainToDoc` 映射函数。
- `data-pipeline/internal/pipeline/migrate.go`：编排 ensure + 打印摘要。
- 单测：`schema_test.go` 覆盖 domain↔doc 往返（round-trip），保证零值/指针/时间/三态字段不丢语义。

**BSON 形状（对齐 PRD §4.3–§4.6）**

`restaurants` 关键字段（节选）：

```json
{
  "_id": "uuid",
  "source": "google_local_2021",
  "source_record_id": "gmap_id",
  "name": "...",
  "address": "...",
  "location": { "type": "Point", "coordinates": [-74.002, 40.730] },
  "categories": ["Pizza restaurant"],
  "cuisine_tags": ["pizza"],
  "description": "...",
  "price": { "raw": "$$", "level": 2 },
  "rating": { "source_avg": 4.5, "computed_avg": 4.48, "rating_count_for_computed_avg": 180 },
  "review_stats": { "source_review_count": 9998, "source_review_count_capped": true,
                    "stored_review_count": 0, "text_review_count": 0,
                    "representative_review_count": 0, "embedded_review_count": 0,
                    "last_reviewed_at": null, "stats_updated_at": "..." },
  "attributes": { "accepts_reservations": "unknown", "wheelchair_accessible": "true",
                  "atmosphere_tags": ["casual"], "popular_for_tags": ["lunch"] },
  "snapshot_status": "open",
  "knowledge_score": 0.0,
  "is_active_for_demo": false,
  "observed_at": "2021-09-01T00:00:00Z",
  "source_url": "https://www.google.com/maps/...",
  "created_at": "...", "updated_at": "..."
}
```

- `reviews`：`{ _id: review_id, restaurant_id, rating, reviewed_at, text, language, text_hash, is_representative, topic_tags, source_observed_at }`。
- `review_summaries`：`{ restaurant_id, topic, sentiment, positive_ratio, summary, evidence_count, valid_from, valid_to, generated_by, generated_at }`。

**实现要点**

- **幂等创建**：Mongo 会在首次写入时隐式建 collection，但 `migrate` 要显式创建以固定命名与校验规则；`CreateCollection` 的 `NamespaceExists` 视为成功。
- `_id` 策略：`restaurants` 用 `idgen.NewUUID()`；`reviews._id` 用确定性 `review_id`（见 M1-07）；`review_summaries` 用 `{restaurant_id, topic}` 复合唯一键，可不显式 `_id`。
- **时间统一**：所有时间字段以 UTC BSON `date` 存储；文本时间（如 `hours` 里的 `"11AM-10PM"`）在规范化阶段转成分钟整数。
- **不要**在 M1 创建 `knowledge_documents` / `user_memories`（M2/M4），但 `restaurants.metadata` 风格字段命名（`cuisine_tags` / `price.level` / `rating.source_avg`）要与 M2 向量过滤字段保持一致。
- collection 校验规则（`$jsonSchema`）可选：M1 可先只建 collection + 索引，把校验留到后续；若加校验需保证可幂等更新（`collMod`）。

**依赖**：M1-01。  
**工作量**：M。

**验收标准**

```bash
data-pipeline migrate            # 首次：created restaurants, ... ；第二次：existing ...
data-pipeline migrate            # 再次运行不报错、不重复创建
```

- `migrate` 连续执行两次输出稳定，退出码为 0。
- Atlas 上可见四个 collection 与预期字段；`restaurants` 文档能原样往返 domain（round-trip 测试通过）。
- 未配置 `MONGO_URI` 时 `migrate` 以 `invalid_argument` / 明确提示失败，不静默成功。

---

### M1-03 基础索引

**目标**：为四个内容 collection 建立唯一索引、时间索引与复合过滤索引，保证关键查询走索引。

**交付物**

- `shared/adapter/repository/mongo/indexes.go`：
  - `func EnsureIndexes(ctx context.Context, db *mongo.Database) error`：对每个 collection 调 `Indexes().CreateMany`，幂等（同名同定义重复创建被视为成功）。
  - `func indexSpecs() map[string][]mongo.IndexModel`：索引定义集中声明，便于审计与 diff。
- `migrate` 在 ensure collection 后调用 `EnsureIndexes`。
- `data-pipeline migrate --indexes-only`：只补索引，便于线上追加索引。
- 索引定义文档化到本文件附录 C。

**索引定义（M1 建议）**

| Collection | 索引 | 类型 | 用途 |
|---|---|---|---|
| `restaurants` | `{source_record_id: 1}` | unique | 幂等 upsert 键 |
| `restaurants` | `{location: "2dsphere"}` | geo | `$near` / 距离筛选 |
| `restaurants` | `{is_active_for_demo: 1, cuisine_tags: 1, "price.level": 1, "rating.source_avg": -1}` | 复合 | 硬条件检索 |
| `restaurants` | `{is_active_for_demo: 1, knowledge_score: -1}` | 复合 | 精选集与排序 |
| `restaurants` | `{borough_guess: 1}` | 单字段 | 地区过滤 |
| `reviews` | `{restaurant_id: 1, reviewed_at: -1}` | 复合 | 按餐厅取评论 |
| `reviews` | `{restaurant_id: 1, is_representative: 1, rating: 1}` | 复合 | 代表评论选择 |
| `reviews` | `{text_hash: 1}` | 单字段 | 去重辅助（非唯一） |
| `review_summaries` | `{restaurant_id: 1, topic: 1}` | unique | 每餐厅每主题一条 |

**实现要点**

- 唯一索引必须在上数据**之前**创建；若历史数据已有重复，先跑 M1-07 的去重清理，否则 `CreateMany` 会失败——`migrate` 应把该错误映射为 `conflict` 并说明。
- 复合索引字段顺序对齐查询谓词顺序（等值字段在前，范围/排序字段在后）。
- `2dsphere` 要求坐标顺序为 `[longitude, latitude]`（GeoJSON 规范），与原始 `latitude`/`longitude` 字段相反，映射时务必交换。
- 索引名可省略（由驱动按 key 生成）或显式命名（如 `uniq_source_record_id`）；显式命名便于审计，推荐显式命名。
- 索引创建在 Atlas 上可能耗时；`migrate` 需支持 `--timeout` 并打印每个索引的创建结果。

**依赖**：M1-02。  
**工作量**：M。

**验收标准**

```bash
data-pipeline migrate --indexes-only     # 幂等，输出每个索引 existing/created
```

- 重复运行不报错。
- `restaurants` 的 `{source_record_id}` 唯一索引存在且强制唯一（插入重复键报 `conflict`）。
- 用 Atlas `explain()` 或 `db.collection.find(...).explain("executionStats")` 抽查：按 `is_active_for_demo + cuisine_tags` 过滤命中 `IXSCAN` 而非 `COLLSCAN`。
- 索引清单与附录 C 一致。

---

### M1-04 Meta 流式导入

**目标**：实现 Gzip JSONL 流式读取 Meta 数据，清洗后批量 upsert 到 `restaurants`（附属资料内嵌），并输出批次统计。

**交付物**

- `data-pipeline/internal/pipeline/raw/jsonl.go`：
  - `type Reader struct { ... }`：包装 `gzip.NewReader` + `bufio.Scanner`（或 `json.Decoder` 流式模式），逐行解码到 `json.RawMessage` 再按阶段结构体解析。
  - 行号跟踪 `LineNo`，解析失败时返回 `LineError{LineNo, err}`，便于审计。
  - 大行支持：`bufio.Scanner` 默认 64KB 会截断长评论/长描述，须 `Scanner.Buffer(make([]byte, 0, 1<<20), 1<<24)` 或改用 `json.Decoder`。
- `data-pipeline/internal/pipeline/raw/meta.go`：Meta 原始 struct（`name`、`address`、`gmap_id`、`description`、`latitude`、`longitude`、`category`、`avg_rating`、`num_of_reviews`、`price`、`hours`、`MISC`、`state`、`relative_results`、`url`），缺失字段用指针/`json.RawMessage`。
- `data-pipeline/internal/pipeline/import.go`：
  - `import` 子命令参数：`--stage=meta|review|all`、`--limit=N`、`--batch=N`、`--workers=N`、`--data-dir=`、`--dry-run`、`--since=`（可选）。
  - 编排：open reader → 过滤餐饮相关 → 清洗（M1-06）→ 批量 `UpsertMany` → 累计统计 → 生成报告（M1-09）。
- `pipeline.Import` 替换 `notImplemented`，至少 `--stage=meta` 可用。
- 单测：用 `testdata/` 下的小样本 `meta-*.json.gz` 覆盖：正常行、缺字段、非法坐标、非餐饮类别、超长行、截断 JSON。

**实现要点**

- **流式 + 有界**：默认全量，但 `--limit=N` 允许只读前 N 行用于开发与 CI；`--dry-run` 只统计不写库。
- **餐饮筛选**（PRD Step 5）：M1-04 先做基础筛选（`category` 命中餐厅/咖啡馆/酒吧/面包店等白名单；坐标在 NYC bbox 内），精确的 `knowledge_score` 打分放到 M1-10。
- **批量写入**：按 `--batch` 聚合成 `[]restaurant.Restaurant`，调 `RestaurantRepository.UpsertMany`；`BatchSize` 默认 1000。
- **附属资料内嵌**：`hours`（含原始文本）/ `attributes_raw` / `relative_results` 与 `description` 一起写在 `restaurants` 文档上——它们永远和餐厅一起被读取，单独一张表只会多一次 join。
- **行级失败不中断批次**：解析失败的记录计入拒绝数并写入 `ingestion_rejections`，继续处理后续行；只有写库失败才中止批次（并可重试）。
- **进度日志**：每 N 行输出一次 `rows_read / written / rejected / elapsed`，日志字段含 `trace_id`、`stage=meta`。

**依赖**：M1-02。  
**工作量**：L。

**验收标准**

```bash
data-pipeline import --stage=meta --limit=5000 --dry-run
data-pipeline import --stage=meta --limit=5000
```

- `--dry-run` 输出 `rows_read / accepted / rejected / missing_fields`，不写库。
- 实际导入后 `restaurants` 文档数等于接受数（去重后）；`source_record_id` 全部非空且唯一。
- 长行（>64KB）不被截断；截断 JSON 计入拒绝而非 panic。
- 数据文件缺失时返回 `not_found`，错误信息含路径。

---

### M1-05 Review 流式导入

**目标**：实现 Review 流式读取、关联到已导入餐厅、批量写入 `reviews`，并产出批次统计。

**交付物**

- `data-pipeline/internal/pipeline/raw/review.go`：Review 原始 struct（`user_id`、`name`、`time`、`rating`、`text`、`pics`、`resp`、`gmap_id`）。
- `data-pipeline/internal/pipeline/import.go` 扩展 `--stage=review|all`：
  - 先加载 `restaurants` 的 `source_record_id → restaurant_id` 映射（或按批查询 `GetBySourceRecordID`）。
  - 逐行关联、清洗（M1-06）、生成 `review_id`（M1-07）、批量 `ReviewRepository.UpsertMany`。
- 单测：关联命中、关联未命中（餐厅不存在）、时间戳边界、空文本、超短文本、含 PII、重复行。

**实现要点**

- **关联键**：评论只带 `gmap_id`，必须通过 `source_record_id` 关联到 `restaurants` 的 `_id`；未命中的评论**不计入**证据（可计入 `unmatched` 统计）。
- **映射数据量**：17,763 家餐厅可一次性载入内存映射；全量 5 亿条评论不行，评论必须逐行处理。
- **时间转换**：`time`（Unix 毫秒）→ UTC `time.Time`；范围校验（如 2000–2021）外的记录计入拒绝。
- **文本处理**（M1-06 落点）：去空、去超短（< 阈值，建议 20–50 字符，可配置）、去模板化重复；保留原文进 `text`，PII 脱敏。
- **不落库字段**：`user_id`、`name`、`pics` 绝不写入 curated `reviews`；`user_id` 只参与 `review_id` 哈希，用后即弃。
- **批次写入**：与 Meta 一致，按 `--batch` 聚合；`ReviewRepository.UpsertMany` 幂等。
- **两文件顺序**：`--stage=all` 必须先 Meta 后 Review（Review 依赖餐厅已存在）；`--stage=review` 单独运行时给出明确前置提示。

**依赖**：M1-04（餐厅必须先入库）。  
**工作量**：L。

**验收标准**

```bash
data-pipeline import --stage=review --limit=200000
```

- 导入的评论 `restaurant_id` 全部能在 `restaurants` 中找到（零悬空外键）。
- 未命中餐厅的评论计入 `unmatched`，不出现在 `reviews`。
- 样本中 100% 的评论 `user_id` / `name` / `pics` 字段不落库（抽查 + 断言）。
- 逐行流式处理：内存不随总行数增长（用 `--limit` 对比 RSS 近似恒定）。
- 输出 `rows_read / matched / written / rejected / unmatched` 统计。

---

### M1-06 清洗与归一化

**目标**：把原始字段转成确定、可测试的 curated 字段（category / price / hours / state / MISC / 地址），并建立可审计的规则。

**交付物**

- `data-pipeline/internal/pipeline/curate/normalize.go`：
  - `NormalizeMeta(raw raw.Meta) (restaurant.Restaurant, []restaurant.Document, error)`。
  - `NormalizeReview(raw raw.Review, restaurantID string) (review.Review, error)`。
- `curate/cuisine.go`：`category` → `categories` + `cuisine_tags` 映射表（如 `"Pizza restaurant" → "pizza"`）；未命中类别保留原值、标记 `extra`。
- `curate/price.go`：`$`/`$$`/`$$$`/`$$$$` → `price.level 1..4`；异常/货币字符保留 `raw`、`level=nil`。
- `curate/hours.go`：`[["Monday","11AM–10PM"], ...]` → `[{weekday, open_minute, close_minute, is_closed}]`；处理多段营业、闭店日、跨午夜。
- `curate/attributes.go`：`MISC` 主题 → 稳定标签 + 三态属性；未知/缺省 = `"unknown"`。
- `curate/geo.go`：`latitude/longitude` → GeoJSON `[lon, lat]` + `borough_guess`（用 bbox 规则）。
- `curate/pii.go`：评论中邮箱/电话/地址脱敏（正则 + 掩码）。
- 单测 + `testdata/` 审计样本：每个规则至少 1 组「正常 / 边界 / 异常」用例，且**黄金样本**（输入 → 期望输出）纳入版本控制。
- 审计脚本：`data-pipeline import --stage=meta --sample-out=testdata/curated/meta_sample.json` 导出少量 curated 文档供人工核对。

**规则表（示例，落地时补全）**

| 原始 | 目标字段 | 规则 |
|---|---|---|
| `category[]` | `categories` / `cuisine_tags` | 保留原始类别；映射表给出 cuisine 标签；`Restaurant`/`Food` 等泛类别不产生 cuisine |
| `price` | `price.raw` / `price.level` | `$`→1 … `$$$$`→4；含非 `$` 字符时 `level=nil` 并保留 `raw` |
| `hours[][]` | `restaurants.hours` | 解析星期与时间 → 分钟；`is_closed` 标识闭店日；跨午夜 `close<open` 需 +1440 |
| `state` | `snapshot_status` | `Open`→`open`；`Closed`→`closed`；`Permanently closed`→`permanently_closed`；其它→`unknown` |
| `MISC` | `attributes.*` | 主题映射到稳定字段；有值→`"true"`，明确否定→`"false"`，缺失→`"unknown"` |
| `latitude/longitude` | `location` | GeoJSON `[lon, lat]`；越界/非数值→拒绝该记录 |
| `text` | `reviews.text` | 去首尾空白；PII 脱敏；空/超短删除 |
| `time` | `reviews.reviewed_at` | Unix ms → UTC；越界拒绝 |

**实现要点**

- 归一化是**纯函数**：不访问 Mongo、不读文件，输入原始结构、输出领域 DTO 或错误，方便单测与回归。
- 规则必须**可审计**：每个转换记录来源（原始值 → 规范化值 → 规则版本），拒绝记录带原因。
- 三态属性严格区分 `"false"` 与 `"unknown"`（PRD §4.3 关键设计）。
- 价格、描述、营业时间、属性存在缺失（PRD §2.5），缺失必须显式表达，不得用零值冒充。
- 规则版本号（`curation_version`）写入批次报告，便于数据可追溯。
- 类别误匹配（PRD §2.5）在 curated 层纠正：白名单外或明显误分类的场所不计入餐饮。

**依赖**：M1-04（Meta 结构）。  
**工作量**：L。

**验收标准**

- 四类字段（category / price / hours / state）+ MISC + geo + PII 均有单测。
- 黄金样本目录存在且与实现一致（用作回归基线）。
- 三态属性抽查：缺失字段输出 `"unknown"`，明确否定输出 `"false"`，两者可区分。
- 跨午夜营业时间解析正确（如 `10PM–2AM` → `open=1320, close=1560`）。

---

### M1-07 去重和幂等

**目标**：定义并实现稳定 ID 与 upsert 规则，保证重复执行同一批次不产生重复数据。

**交付物**

- `data-pipeline/internal/pipeline/curate/dedup.go`：
  - `func ReviewID(gmapID, userID string, unixMilli int64, text string) string`：`sha256(gmap_id + "\x00" + user_id + "\x00" + strconv(time) + "\x00" + sha256(text))`，返回 `"sha256:<hex>"` 或裸 hex（全局统一）。
  - `func TextHash(text string) string`：`sha256(normalized_text)`。
  - Meta 去重：同 `gmap_id` 多行时保留字段最完整的一条（缺失字段少者优先），冲突记录标记。
- `shared/adapter/repository/mongo/restaurant.go`：
  - `Upsert`：按 `source_record_id` 唯一键 `UpdateOne(..., options.Update().SetUpsert(true))`，`$setOnInsert` 写 `created_at`，`$set` 写 `updated_at` 与业务字段。
  - `UpsertMany`：`bulkWrite` 或分批 `UpdateMany`；返回 upsert 数。
- `shared/adapter/repository/mongo/review.go`：
  - `UpsertMany`：`_id = review_id`，`ReplaceOne/UpdateOne` upsert；重复 `_id` 覆盖为最新清洗结果。
- `data-pipeline import --stage=*` 的重复运行验证脚本（可放在 `scripts/`）。
- 单测：同一输入两次 → 同 `review_id`；不同 `user_id` → 不同 `review_id`；文本规范化前后一致 hash 稳定。

**幂等规则**

| 集合 | 幂等键 | 冲突策略 |
|---|---|---|
| `restaurants` | `{source_record_id: 1}` unique | upsert；同 `gmap_id` 取字段最完整版本 |
| `reviews` | `_id = review_id`（确定性哈希） | upsert |
| `review_summaries` | `{restaurant_id, topic}` unique | upsert |
| `ingestion_batches` | `batch_id`（UUID） | 只插入；重跑产生新批次记录 |
| `ingestion_rejections` | `{batch_id, stage, line_no}` | upsert |

**实现要点**

- `review_id` 必须**确定性**：同一 `(gmap_id, user_id, time, text)` 永远得到同一 ID，这既是幂等键也是去重键。
- 原始 `user_id` 只参与哈希，**绝不落库**；`text_hash` 存规范化文本的 hash（非原文）。
- upsert 的 `$set` 字段必须覆盖全部可变业务字段，避免旧批次残留导致"看似更新实则未更新"。
- 批量 upsert 需处理部分失败：Atlas 对 `bulkWrite` 返回失败明细，汇总后决定重试或报错；不得静默吞错。
- `--dry-run` 下同样计算 `review_id` 并统计潜在重复，便于验证。
- **不依赖事务**：用唯一索引 + upsert 实现幂等，避免跨文档事务带来的复杂性（与 PRD §4.9 的事务原则一致，M1 场景无需事务）。

**依赖**：M1-04、M1-05。  
**工作量**：M。

**验收标准**

```bash
data-pipeline import --stage=all --limit=100000
data-pipeline import --stage=all --limit=100000   # 第二次
# 断言：restaurants.countDocuments 与 reviews.countDocuments 不增长
```

- 连续两次导入同一批次，`restaurants` 与 `reviews` 文档数不变。
- `reviews._id` 全部为确定性哈希；同一评论重复出现只保留一条。
- `reviews` 中不含 `user_id` / `name` / `pics` 字段。
- `restaurants.source_record_id` 唯一索引生效，人为重复插入返回 `conflict`。

---

### M1-08 评论统计聚合

**目标**：按 PRD §4.6 口径，把评论数量与评分统计物化进 `restaurants.review_stats`，支持重建与校验。

**交付物**

- `data-pipeline/internal/pipeline/curate/stats.go`：
  - `func ComputeStats(restaurantID string, restaurantRev []review.Review, source restaurant.ReviewStats, now time.Time) restaurant.ReviewStats`。
  - 计算：`stored_review_count`、`text_review_count`、`representative_review_count`（M1 可先置 0，由 M2 填 `embedded_review_count`）、评分分布、`last_reviewed_at`、`computed_avg`。
- `shared/adapter/repository/mongo/restaurant.go` 增加：
  - `UpdateReviewStats(ctx, restaurantID string, stats restaurant.ReviewStats) error`。
  - `AggregateReviewStats(ctx, restaurantIDs []string) (map[string]review.Counts, error)`：用聚合管道 `$group` 计算 count/avg/distribution。
  - `RebuildAllReviewStats(ctx) error`：全量重算（`--rebuild-stats`）。
- `data-pipeline import --stage=stats` 或 `--rebuild-stats` 子路径：重算并写回 `review_stats`。
- 单测：给定评论集计算期望 count/avg/distribution；空评论、全无文本、缺失 `last_reviewed_at`。

**口径（对齐 PRD §4.6）**

| 字段 | 来源 | 用途 |
|---|---|---|
| `source_review_count` | Meta `num_of_reviews` | 热度、筛选 |
| `source_review_count_capped` | 规则（`>= 9998` 等） | 标注截顶风险 |
| `stored_review_count` | `reviews` 聚合 | 实际入库评论数 |
| `text_review_count` | `reviews` 聚合（有效文本） | RAG 覆盖度 |
| `representative_review_count` | 代表评论选择 | 展示证据数 |
| `embedded_review_count` | M2 文档构建 | 参与 embedding 数（M1 置 0） |
| `last_reviewed_at` | `reviews` 聚合（max） | 评论新鲜度 |
| `stats_updated_at` | 本次计算时间 | 审计 |

**实现要点**

- 统计**物化**进 `restaurants.review_stats`，不要每次搜索实时 `$lookup + count`（PRD §4.6 明确要求）。
- 计数增量 vs 重建：默认按批次增量更新；提供 `--rebuild-stats` 全量重算以修复漂移。
- `source_review_count` 与 `stored_review_count` **语义不同，禁止互相覆盖**；`computed_avg` 只在样本足够时展示，且标注"入库样本平均"。
- 截顶判定：`num_of_reviews` 达到 9998（或特定阈值）时置 `source_review_count_capped=true`，不当作精确值。
- 聚合管道用 `$match`（按 restaurant_id 分批）→ `$group` → `$merge` 写回，分批避免大集合全表聚合超时。

**依赖**：M1-05。  
**工作量**：M。

**验收标准**

```bash
data-pipeline import --stage=stats --rebuild-stats
```

- 对同一餐厅，`stored_review_count` 等于 `reviews.countDocuments({restaurant_id})`。
- `text_review_count` 等于有效文本评论数；`last_reviewed_at` 等于该餐厅最新评论时间。
- 重建前后统计一致（幂等）。
- 评分分布之和等于 `stored_review_count`。
- 无评论餐厅的计数为 0、`last_reviewed_at` 为 null，不报错。

---

### M1-09 数据审计报告

**目标**：每次导入生成可查询的批次报告与拒绝明细，使导入过程可追溯、可复核。

**交付物**

- `shared/domain/review/report.go`：
  - `type BatchReport struct { BatchID, Stage, CurationVersion, SourceFile, SourceSHA256 string; StartedAt, FinishedAt time.Time; RowsRead, Accepted, Written, Deduped, Rejected, Unmatched, MissingFields int64; DurationMS int64; Status string; ErrorCode string }`。
  - `type FieldMissing struct { Field string; Count int64 }`。
- `data-pipeline/internal/pipeline/report/report.go`：采集与汇总；每次 import 开始/结束写 `ingestion_batches`。
- `shared/adapter/repository/mongo/pipeline.go`：实现 `PipelineRepository`（`StartBatch` / `FinishBatch` / `RecordRejections`），集合 `ingestion_batches`、`ingestion_rejections`。
- 索引：`ingestion_batches {started_at: -1}`、`{stage: 1, started_at: -1}`；`ingestion_rejections {batch_id: 1}`。
- 可选：`data-pipeline report --last=N` 打印最近批次；`--batch-id=<id>` 打印单批次明细。
- 单测：统计累加正确；报告 JSON 可往返；拒绝明细可批量写入。

**报告字段**

```text
batch_id            # UUID
stage               # meta | review | stats | all
curation_version    # 归一化规则版本
source_file         # 文件名（不含敏感路径）
source_sha256       # 原始 gz 文件哈希（可缓存）
started_at / finished_at / duration_ms
rows_read / accepted / written / deduped / rejected / unmatched
missing_fields      # [{field, count}]
status              # succeeded | failed
error_code          # 失败时的统一错误码
```

**实现要点**

- 报告在批次**开始时**写入（`status=running`），结束时更新为 `succeeded`/`failed`；崩溃可通过 `running` 状态发现。
- 文件哈希对 2.65 GB 文件应**流式计算并缓存**（首次计算慢，可跳过 `--skip-hash`）。
- 拒绝明细限制大小：只记录 `line_no + reason + source_record_id`，**不落原始文本**（避免 PII）。
- 报告可查询是验收点：提供最少一种查询方式（`report` 子命令或直接 Mongo 查询示例）。
- 日志字段与报告字段命名一致，便于交叉核对。

**依赖**：M1-04、M1-05。  
**工作量**：M。

**验收标准**

```bash
data-pipeline import --stage=meta --limit=5000
data-pipeline report --last=1
```

- 每次导入在 `ingestion_batches` 生成一条报告，且 `status=succeeded`。
- 统计满足守恒：`rows_read >= accepted + rejected` 且 `accepted >= written - updated`（允许 upsert 覆盖）。
- 拒绝记录可查询且不含原始评论文本/PII。
- 人为制造解析失败（截断 JSON）时报告 `rejected>0` 且含拒绝明细。
- 失败批次 `status=failed` 且 `error_code` 非空。

---

### M1-10 精选餐厅集合

**目标**：建立 `knowledge_score` 与 `is_active_for_demo`，稳定选出 2,000–5,000 家字段覆盖与评论充足的餐厅。

**交付物**

- `data-pipeline/internal/pipeline/curate/score.go`：
  - `func KnowledgeScore(r restaurant.Restaurant) float64`：按字段覆盖 + 评论量 + 属性丰富度加权。
  - `func SelectActiveForDemo(all []restaurant.Restaurant, target int) []string`：按分数排序选取，保证数量落在区间。
- `shared/adapter/repository/mongo/restaurant.go` 增加：
  - `UpdateScores(ctx, scores map[string]float64, active map[string]bool) error`。
  - `SelectForDemo(ctx, limit int) ([]restaurant.Restaurant, error)`。
- `data-pipeline import --stage=score` 或 `--rescore`：全量重算分数与精选标志。
- 单测：分数单调性（字段更全分数更高）；并列时的稳定排序（按 `source_record_id` 兜底）；目标数量裁剪。

**评分维度（建议，可调参）**

| 维度 | 权重来源 | 说明 |
|---|---|---|
| 有 `description` | 覆盖度加分 | 语义检索基础 |
| 有 `hours` | 覆盖度加分 | 营业时间 |
| 有非空 `attributes` | 覆盖度加分 | 场景/设施 |
| `text_review_count` | 对数加权 | 评论越多样本越足 |
| 类别清晰度 | 命中明确菜系加分 | 避免泛 `Restaurant` |
| `permanently_closed` | 排除 | 默认不进入搜索 |
| 坐标有效 | 必要条件 | 缺失则排除 |

**实现要点**

- 目标区间由配置驱动（如 `PIPELINE_DEMO_TARGET=3000`），默认裁剪到 2,000–5,000。
- `permanently_closed` 默认 `is_active_for_demo=false`，但**保留在数据库**（PRD §5.2 Step 5）。
- 分数可解释：`SelectForDemo` 返回时可附带 reason（哪个维度加分），供 M3/M5 展示。
- 评分与选择必须**确定性**：同样输入产生同样集合（并列用稳定 tie-breaker）。
- 全量约 17,763 家，精选 2,000–5,000 家，其余仍可被硬条件检索命中（只是不进 embedding 批次）。

**依赖**：M1-06、M1-08。  
**工作量**：M。

**验收标准**

```bash
data-pipeline import --stage=score
# 断言：restaurants.countDocuments({is_active_for_demo: true}) ∈ [2000, 5000]
```

- `is_active_for_demo=true` 数量落在 2,000–5,000。
- 所有 `permanently_closed` 默认 `false`。
- 分数排序稳定：重复运行得到相同集合。
- `SelectForDemo` 对样本能给出每个入选者的加分原因。

---

### M1-11 Atlas Search 索引

**目标**：为餐厅名称、地址、类别、描述创建 Atlas Search 索引，支持模糊与自动补全查询。

**交付物**

- `scripts/atlas/search_index_restaurants.json`：可提交到 Atlas 的 Search 索引定义（版本控制）。
- `shared/adapter/repository/mongo/search_index.go`：
  - 生成/校验索引定义的函数（Go 结构与 JSON 双向）。
  - `EnsureSearchIndex`（若 Atlas Admin API 可用则调用；否则输出定义并提示手动创建）。
- `chat-service` 或 `data-pipeline` 提供 `search-index` 校验子命令（离线校验字段是否与 `restaurants` schema 一致）。
- 文档：`scripts/atlas/README.md` 说明创建步骤与验证查询。

**索引定义（对齐 PRD §4.11）**

```json
{
  "name": "restaurants_search_index",
  "analyzer": "lucene.standard",
  "mappings": {
    "dynamic": false,
    "fields": {
      "name": [
        { "type": "autocomplete", "tokenization": "edgeGram" },
        { "type": "string", "analyzer": "lucene.standard" }
      ],
      "address": { "type": "string", "analyzer": "lucene.standard" },
      "categories": { "type": "string" },
      "cuisine_tags": { "type": "string" },
      "description": { "type": "string", "analyzer": "lucene.standard" },
      "borough_guess": { "type": "string" }
    }
  }
}
```

**实现要点**

- Search 索引与 Vector 索引是**两种独立索引**（PRD §4.11）：M1-11 只建 `restaurants` 的 Search 索引；`knowledge_documents` 的 Vector 索引属于 M2-07。
- `autocomplete` 用于名称前缀补全；地址与描述用标准分词；中文查询若需要，需评估自定义 analyzer（列为 §17 未锁定决策）。
- 索引定义版本化进仓库，创建/更新通过脚本或文档步骤完成；`chat-service` 在启动时**不**强依赖索引存在（避免本地无 Atlas Search 时无法启动）。
- 验证查询：`$search` 的 `autocomplete`（名称）与 `text`（地址/描述）各一组固定查询 + 期望结果，作为 M3-02 的夹具来源。
- Atlas Search 索引创建是异步的，脚本需轮询索引状态到 `READY` 或给出明确提示。

**依赖**：M1-02、M1-03。  
**工作量**：M。

**验收标准**

```bash
# 按 scripts/atlas/README.md 创建索引后
# 名称模糊：检索约 "pizza"，能返回 "Joe's Pizza" 等
# 地址模糊：检索约 "Carmine"，能按地址命中
```

- 名称自动补全与地址文本查询均返回可解释结果（命中字段 + 分数）。
- 索引定义文件与 Atlas 上的实际索引一致。
- 未配置 Atlas Search 时 `chat-service` 仍可启动（降级到 M3 的结构化过滤）。
- 固定验证查询纳入 M3-02 的检索夹具。

---

## 5. 推荐执行顺序与并行化

### 5.1 单人执行（串行）

```text
M1-01 → M1-02 → M1-03 → M1-04 → M1-06 → M1-05 → M1-07 → M1-08 → M1-09 → M1-10 → M1-11
```

> 说明：先打通 `M1-01..M1-04` 的最小链路（连库、建表、建索引、导入 Meta 样本），再进入清洗与 Review 链路。M1-11 可在 M1-03 后任意时刻插入。

### 5.2 多轨并行（依赖满足后）

| 轨道 | 任务 | 前置 |
|---|---|---|
| 存储轨 | M1-01 → M1-02 → M1-03 → M1-11 | M0-02 |
| 导入轨 | M1-04 → M1-05 / M1-06 → M1-07 | M1-02 |
| 统计轨 | M1-08 → M1-10 | M1-05、M1-06 |
| 审计轨 | M1-09 | M1-04、M1-05 |

> 注意：实施计划 §8 提醒"不要同时推进太多大任务"。M1 有 4 个 L 任务（M1-04/05/06 + 前置领域调整），建议单人每次只推 1 个数据任务 + 1 个测试/文档任务。

---

## 6. M1 完成定义（DoD）检查清单

### 6.1 每个任务通用 DoD（继承实施计划 §10）

- [ ] 代码已实现。
- [ ] 单元测试或集成测试已添加。
- [ ] 错误路径有明确错误码。
- [ ] 日志中包含 `trace_id` / `request_id`（批处理含 `batch_id`）。
- [ ] 不泄露密钥和 PII（`user_id` / `name` / `pics` / 邮箱 / 电话不入库）。
- [ ] 不绕过领域接口直接调用厂商 API（Mongo 类型不逃逸 `mongo` 包）。
- [ ] 文档或注释说明关键设计。
- [ ] 通过 `go test ./...`、`go vet ./...`、`go test -race ./...`（`golangci-lint` 待安装）。
- [ ] 相关验收标准可以实际演示。

### 6.2 M1 里程碑门（Gate A）

- [ ] `data-pipeline migrate` 与 `migrate --indexes-only` 均可重复执行。
- [ ] 四个内容 collection 建立完成，索引清单与附录 C 一致。
- [ ] Meta 导入：`restaurants` ≥ 1,000（样本）且 `source_record_id` 唯一。
- [ ] Review 导入：`reviews` ≥ 100,000（样本）且全部关联到已存在餐厅。
- [ ] 重复导入不产生重复数据（`restaurants` / `reviews` 计数不变）。
- [ ] `user_id` / `name` / `pics` 不出现在任何 curated collection。
- [ ] `restaurants.review_stats` 与 `reviews` 聚合一致、可重建。
- [ ] `ingestion_batches` 每次导入一条报告且可查询。
- [ ] `is_active_for_demo=true` 数量 ∈ [2,000, 5,000]。
- [ ] `restaurants` Search 索引可用，名称/地址模糊查询有结果。
- [ ] 领域层无 Mongo/BSON 依赖（`shared/domain/architecture_test.go` 通过）。
- [ ] Mongo adapter 与内存 adapter 通过同一套契约测试。
- [ ] 所有验收标准均可实际演示。

---

## 7. 与后续里程碑的衔接

M1 完成后，按实施计划 §14 进入 M2（Embedding 与知识文档）。衔接点：

| 后续里程碑 | 依赖 M1 的什么 | 说明 |
|---|---|---|
| M2-03 餐厅级文档 | `restaurants`（含 `is_active_for_demo`、`review_stats`、`attributes`） | 每餐厅一条餐厅级摘要 |
| M2-04 佐证级文档 | `reviews`（`text` / `rating` / `reviewed_at` / `topic_tags`） | 评论 → evidence chunk |
| M2-05 摘要生成 | `review_summaries` | 规则统计 → 主题/情绪 |
| M2-06 批量向量化 | `knowledge_documents`（M2-07 建索引） | 向量写入与 `content_hash` 幂等 |
| M3-01/02 检索 | `restaurants` 索引 + Atlas Search | 结构化过滤与名称/地址检索 |
| M6-02 RAG 评测 | `ingestion_batches` + `reviews` | 评测数据来源与可追溯性 |

**接口稳定性要求**：M1 对 `shared/port/repository.go` 的扩展是 M2/M3 的契约。若后续必须再调整，需同步更新：

- 本文档 §4.0 的接口形状。
- `plans/platepilot-implementation-plan.md` 对应任务的依赖与验收。
- 所有实现（Mongo / 内存 / Mock）与契约测试。

**M1 明确不做的事**（避免范围蔓延）：

- 不生成 `knowledge_documents`、不写任何向量、不建 Vector 索引（M2）。
- 不实现检索服务、召回、融合、rerank（M3）。
- 不接入 Eino / Chat Provider / SSE（M4）。
- 不实现预约 collection（可选，M5-06）。
- 不实现前端（M6-06）。
- 不引入 LLM 生成摘要（评论摘要先用规则统计，LLM 摘要为 M2 可选增强）。

---

## 8. 风险与注意事项

| 风险 | 触发点 | 控制措施 |
|---|---|---|
| Atlas 网络依赖 | 本地无法连 Atlas，开发受阻 | 提供 `--dry-run` 与样本数据路径；集成测试 env-gated，单测离线可跑 |
| 全量导入巨大 | Review 2.65 GB / 33M 行，内存或耗时失控 | 强制流式 + `--limit`；`bufio.Scanner` 调大 buffer 或 `json.Decoder` |
| `bufio.Scanner` 截断长行 | 长描述/长评论超过 64KB | 显式 `Scanner.Buffer` 或改用 `json.Decoder`；加长行单测 |
| 幂等键设计错误 | `review_id` 不稳定导致重复 | `review_id` 纯函数 + 单测；唯一索引兜底 |
| PII 泄露 | `user_id` / `name` / 邮箱进 curated 集合 | 哈希用后即弃；PII 脱敏；契约测试断言字段不存在 |
| 去重与唯一索引冲突 | 历史重复数据导致 `CreateMany` 失败 | 先跑 M1-07 清理，再建唯一索引；错误映射 `conflict`|
| 统计口径混淆 | `source` / `stored` / `embedded` 混用 | 字段语义表；`--rebuild-stats` 校验；禁止互相覆盖 |
| 评分选择不稳定 | 并列分数导致集合抖动 | 确定性 tie-breaker（`source_record_id`）；重复运行断言集合不变 |
| 索引字段顺序错误 | 复合索引不被命中 | 等值在前、范围/排序在后；`explain()` 抽查 `IXSCAN` |
| Atlas Search 异步 | 索引未 READY 就查询 | 脚本轮询状态；`chat-service` 不强依赖 Search 索引 |
| 收录范围误判 | 类别误匹配引入非餐饮 | 白名单 + 坐标 bbox 双重过滤；curated 层可纠正 |
| 服务边界破坏 | `data-pipeline` import `chat-service` | code review + 目录约束；共享只经 `shared/` |
| 驱动版本混用 | v1/v2 import path 并存 | M1-01 锁定单一主线并全局统一 |

---

## 附录 A：环境变量（M1 相关）

在 M0 `.env.example` 基础上，M1 需要/新增：

```dotenv
# --- MongoDB Atlas（M1 起必需）--------------------------------------------
MONGO_URI=mongodb+srv://<user>:<pass>@<cluster>/
MONGO_DATABASE=platepilot
MONGO_TIMEOUT=10s
# 可选：连接池与连接超时
# MONGO_CONNECT_TIMEOUT=10s
# MONGO_MAX_POOL_SIZE=50
# MONGO_MIN_POOL_SIZE=1

# --- data-pipeline（M1 起使用）--------------------------------------------
PIPELINE_DATA_DIR=data/raw/google_local
PIPELINE_BATCH_SIZE=1000
PIPELINE_WORKERS=4
# 精选餐厅目标数量（M1-10），默认裁剪到 [2000, 5000]
# PIPELINE_DEMO_TARGET=3000
# 评论最短文本阈值（M1-06）
# PIPELINE_MIN_REVIEW_CHARS=20
```

## 附录 B：常用命令速查

```bash
# 1. 建集合 + 索引（可重复）
go run ./data-pipeline migrate
go run ./data-pipeline migrate --indexes-only

# 2. 导入样本（先 dry-run 再实跑）
go run ./data-pipeline import --stage=meta   --limit=5000  --dry-run
go run ./data-pipeline import --stage=meta   --limit=5000
go run ./data-pipeline import --stage=all    --limit=100000

# 3. 统计与精选
go run ./data-pipeline import --stage=stats  --rebuild-stats
go run ./data-pipeline import --stage=score

# 4. 审计
go run ./data-pipeline report --last=5
go run ./data-pipeline report --batch-id=<id>

# 5. 质量门
go test ./... && go vet ./...
go test -race ./...
```

## 附录 C：索引清单（M1-03 / M1-11 汇总）

```text
restaurants
  uniq_source_record_id        { source_record_id: 1 } unique
  geo_location                 { location: "2dsphere" }
  ix_search_filters            { is_active_for_demo: 1, cuisine_tags: 1, "price.level": 1, "rating.source_avg": -1 }
  ix_active_score              { is_active_for_demo: 1, knowledge_score: -1 }
  ix_borough                   { borough_guess: 1 }

reviews
  ix_restaurant_time           { restaurant_id: 1, reviewed_at: -1 }
  ix_representative            { restaurant_id: 1, is_representative: 1, rating: 1 }
  ix_text_hash                 { text_hash: 1 }

review_summaries
  uniq_restaurant_topic        { restaurant_id: 1, topic: 1 } unique

ingestion_batches        (M1-09)
  ix_started_at                { started_at: -1 }
  ix_stage_time                { stage: 1, started_at: -1 }

ingestion_rejections     (M1-09)
  ix_batch                     { batch_id: 1 }

Atlas Search (M1-11)
  restaurants_search_index     名称 autocomplete + 地址/描述/类别 standard
```

## 附录 D：参考文档

- 实施计划：`plans/platepilot-implementation-plan.md`
  - §6 M1 任务表
  - §7 关键路径
  - §9 Gate A：Atlas 数据门
  - §10 每项任务的完成定义
- 技术 PRD：`plans/platepilot-technical-prd.md` v0.12
  - §2 当前数据基线（§2.2 Meta schema、§2.3 Review schema、§2.5 数据限制）
  - §4 数据架构与 Schema（§4.2 Collection 总览、§4.3/§4.4/§4.5/§4.6、§4.11 Atlas Search 索引）
  - §5 写入链路（Step 1–9）
- M0 任务文档：`plans/platepilot-m0-task-document.md`（工程骨架、配置、日志、领域 DTO、Repository 接口）
