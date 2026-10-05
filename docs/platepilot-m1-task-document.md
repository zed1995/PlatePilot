# PlatePilot M1 数据底座任务文档

> 日期：2026-09-30
> 依据：`docs/platepilot-implementation-plan.md`（§4 里程碑总览、§6 M1、§7 关键路径、§9 Gate A、§10 完成定义）与 `docs/platepilot-technical-prd.md` v0.12（§2 当前数据基线、§4 数据架构与 Schema、§5 写入链路）
> 里程碑目标：把 Google Local 2021 原始数据变成**可重复导入、可幂等重建、可查询、可审计**的 PostgreSQL 内容底座
> 退出条件（Gate A）：≥1,000 家餐厅、≥100,000 条评论成功关联；重复执行导入不产生重复数据；数据审计报告可生成；名称/地址模糊查询可用

> 存储层为自建 PostgreSQL（pgvector + PostGIS + pg_trgm）。本文描述的是当前实现。

## 0. 如何使用本文档

- 本文档是 M1 的**可执行任务清单**，每个任务独立成节，包含目标、交付物、实现要点、依赖、工作量和可验证的验收标准。
- 任务粒度对齐实施计划的 M1 表格（M1-01 ~ M1-11）。任务 ID 与实施计划保持一致，便于交叉引用。
- 每个任务的**完成定义（DoD）**默认继承实施计划 §10：代码实现 + 测试 + 错误码 + `trace_id` 日志 + 不泄露密钥/PII + 不经领域接口直连厂商 API + 关键设计有注释 + 通过 `go test ./...`、`go vet ./...`、`golangci-lint` + 验收标准可实际演示。
- 本文档中的 Go 类型、接口签名和表结构是**约定形状**，允许在不破坏领域边界的前提下微调；一旦调整，必须同步更新本文件、`port` 接口和所有实现（含内存实现）。
- 模块路径沿用 M0 约定：`github.com/zed/platepilot`。
- 服务边界沿用 M0：写入链路只落在 `data-pipeline` 与 `shared/adapter/repository/postgres`，**不得**在 `chat-service` 中写库；`data-pipeline` 与 `chat-service` 互不 import。

### 0.1 实现状态（2026-09-30）

M1 写入链路已实现并跑通全量语料，代码位于 `data-pipeline` 与
`shared/adapter/repository/postgres`。除特别说明外，全部任务已完成。

**表结构要点**

`hours` / `attributes_raw` / `relative_results` 内嵌在 `restaurants` 行内，不另设附属表
——这些字段与餐厅总是一起读取，拆表只会带来无谓的 join：

- `restaurants.hours`：`jsonb`，每项保留原始文本（如 `"11AM–10PM"`）便于展示
- `restaurants.attributes_raw`：原始 MISC 对象，保留以便重跑清洗规则而无需重读 61MB 源文件
- `restaurants.relative_results`：`text[]`，相似 POI id
- `description` 本就是主字段，不重复存放

内容表 3 张（`restaurants` / `reviews` / `review_summaries`），`migrate` 共创建 8 张表
（含 3 张审计表、`boundaries`、`schema_migrations`，以及 PostGIS 自带的 `spatial_ref_sys`）。

**几处必须知道的 schema 约束**（不了解会写出错误的查询或导入逻辑）：

1. **pgvector HNSW 不能靠 `WHERE` 裁剪**。HNSW 是 `ORDER BY` 结构，附加过滤条件会退化为
   Seq Scan（5 万行实测 `Rows Removed by Filter: 49759`）。因此过滤字段 `borough`
   必须反规范化到 `knowledge_documents`，并为每个取值建一个 partial HNSW。
2. **评分列是 `double precision`，不是 `real`**。`real` 是 float32，表示不了 4.42 这类
   真实评分。
3. **`borough` 可空**。真实边界下 58.9% 的语料落在五个行政区之外，NOT NULL 会把
   "区外"和"未知"混为一谈。
4. **PostgreSQL 拒绝 NUL 字符**（SQLSTATE 22021），原始语料中确实存在。
   `curate/pii.go` 的 `stripControlRunes` 负责剥离，保留 tab / 换行 / 回车。
5. **批量更新不能用 `VALUES` 列表**。扩展协议上限 65,535 个绑定参数，36,133 行 × 2
   就超了。`UpdateScores` 用 `unnest($1::bigint[], $2::double precision[])`，
   语句大小不随语料增长。
6. **`reviews` 的幂等键是唯一索引 `(restaurant_id, text_hash, rating, reviewed_at)`**，
   不是主键——主键由数据库分配。该键与评论的内容哈希键选择完全相同的行
   （`rating` 与 `user_id` 都由 `text_hash` 唯一决定）。

**Postgres adapter 有三层验证，默认无需数据库：**

1. **内存实现**：`shared/adapter/repository/memory` 提供端口级 mock，`go test ./...`
   离线全绿。
2. **共享契约套件**：`shared/adapter/repository/contract/contract.go` 定义端口语义，
   memory 与 postgres 两个 adapter 跑**同一套**断言，任何一方偏离都会被抓住。
3. **真实 PostgreSQL**：`PLATEPILOT_TEST_POSTGRES_DSN` 指向真实实例时运行同一套契约。
   ⚠️ 契约套件会调用 `client.Drop` 清空 schema，**必须指向一次性测试库**，
   否则会清掉已导入的语料。

**全量导入实测结果**（272k meta 源行 / 4.24M review 源行）：

| 阶段 | 读取 | 写入 | 去重 | 拒绝 | 耗时 |
|---|---|---|---|---|---|
| meta | 272,189 | 36,225 | 92 | 0 | 8.6 s |
| review | 4,243,445 | 4,243,445 | 38,269 | 0 | 281 s |
| stats | — | 35,979 | 0 | 0 | 32 s |
| score | — | 36,133 | 0 | 0 | 5.3 s |

落库 `restaurants=36,133` / `reviews=4,156,055` / `is_active_for_demo=3,000`，
孤儿 review 数 0，库体积 2.9 GB。

| 任务 | 状态 | 主要落地文件 |
|---|---|---|
| M1-01 数据库连接 | 完成 | `shared/adapter/repository/postgres/client.go` |
| M1-02 表结构定义 | 完成 | `shared/adapter/repository/postgres/migrations/0001_init.sql`、`migrate.go` |
| M1-03 基础索引 | 完成 | 同上（迁移文件内 `CREATE INDEX`） |
| M1-04 Meta 流式导入 | 完成 | `data-pipeline/internal/pipeline/raw/jsonl.go`、`import.go` |
| M1-05 Review 流式导入 | 完成 | `data-pipeline/internal/pipeline/raw/review.go`、`import.go` |
| M1-06 清洗与归一化 | 完成 | `data-pipeline/internal/pipeline/curate/*` |
| M1-07 去重和幂等 | 完成 | `curate/dedup.go`、`postgres/review.go`、`postgres/restaurant.go` |
| M1-08 评论统计聚合 | 完成 | `curate/stats.go`、`postgres/review.go`、`import.go` |
| M1-09 数据审计报告 | 完成 | `data-pipeline/internal/pipeline/report/report.go`、`postgres/pipeline.go` |
| M1-10 精选餐厅集合 | 完成 | `curate/score.go`、`postgres/restaurant.go` |
| M1-11 全文检索索引 | 完成（`pg_trgm` GIN 索引） | `migrations/0001_init.sql` |
| §4.0 领域与接口 | 完成（形状有调整，见下） | `shared/domain/restaurant`、`shared/domain/review`、`shared/port/writer.go` |

**与本文档正文描述不一致的地方**（以实现为准）：

1. **读写端口分离，而不是扩展 `RestaurantRepository`**：M0 的读侧
   `RestaurantRepository` / `KnowledgeRepository` 保持不变（M3 使用），
   写侧是独立的 `RestaurantStore`、`ReviewStore`、`PipelineStore`
   （`shared/port/writer.go`）。实施计划 §3.2 要求写入与读取严格分离。
2. **`UpdateReviewStats` 带评分参数**：
   `UpdateReviewStats(ctx, restaurantID int64, stats restaurant.ReviewStats, computed restaurant.Rating)`。
   `rating.computed_avg` 与 `review_stats` 来自同一份评论样本，因此一起写入。
3. **`RestaurantStore` 的实际方法集**：`GetByID`、`GetBySourceRecordID`、
   `ListRestaurants`、`ListSourceRecordIDs`、`MapSourceRecordIDs`、`UpsertRestaurant`、
   `UpsertRestaurants`、`UpdateReviewStats`、`UpdateScores`、`SelectForDemo`、
   `CountActiveForDemo`。比 §4.0 多出 `GetByID` / `ListRestaurants` /
   `MapSourceRecordIDs`（统计重建、打分与评论关联需要）。
4. **短评处理**：正文要求"删除超短评论"。实现是**保留该行、清空其文本**，
   这样 `stored_review_count` 仍反映评分样本，而 `text_review_count` 只统计可用文本，
   两个计数口径才有意义。
5. **`review_summaries` 的主键是复合的**：`(restaurant_id, topic)`，同一餐厅按主题各有一行。
6. **`embedded_review_count` 与 `representative_review_count` 在 M1 恒为 0**：
   前者需要 M2-06 的向量，后者依赖 M2-04 产出的 `is_representative` 标记，
   M1 没有任何 stage 会写该列。Gate A 第 5 条按三项计数口径通过。
7. **迁移文件不放入 `initdb.d`**：容器初始化只执行一次，放进去会导致 schema 与代码
   漂移。迁移由 `//go:embed migrations/*.sql` 管理，`schema_migrations` 表记账，
   因此 `migrate` 可重复执行。

### 0.2 工作量级定义

| 级别 | 含义 |
|---|---|
| S | 半天以内 |
| M | 约 1–2 天 |
| L | 约 3–5 天 |

### 0.3 前置条件与起点（M0 已交付）

M1 直接建立在 M0 的双服务骨架上，启动 M1 前应确认以下 M0 产物可用：

| M0 产物 | 位置 | M1 如何使用 |
|---|---|---|
| 双服务骨架与 CLI 分发 | `data-pipeline/main.go`、`chat-service/main.go` | M1 在 `data-pipeline` 中补 `migrate` / `import` 子命令 |
| 共享配置原语 | `shared/config/config.go` | 复用 `PostgresConfig`（`POSTGRES_DSN` / `POSTGRES_DATABASE` / `POSTGRES_TIMEOUT`）、`PipelineConfig` |
| 错误码与日志 | `shared/domain/errs`、`shared/observability/logging` | 批处理错误与批次日志复用统一错误码与 JSON 日志 |
| 领域 DTO 与 Repository 接口 | `shared/domain/*`、`shared/port/repository.go` | M1 按 §4.0 扩展 `restaurant` / `review` 领域与 `RestaurantRepository` |
| Postgres Adapter | `shared/adapter/repository/postgres/*` | M1-01 在此实现连接与客户端 |
| Pipeline 占位 | `data-pipeline/internal/pipeline/pipeline.go` | `Import` 的 `notImplemented` 占位由 M1-04 起逐步替换 |
| 内存 Repository 与契约 | `shared/adapter/repository/memory/*` | M1 新增契约测试同时跑内存实现与 Postgres 实现 |

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

1. Go 服务能连接 PostgreSQL，握手、ping、连接池与超时可控。
2. `restaurants`、`reviews`、`review_summaries` 三张内容表有显式、可重复执行的建表与索引脚本（附属资料内嵌进 `restaurants`，见 §0.1 决策）。
3. 能以流式方式导入 Meta 与 Review 原始 JSONL，产出批次统计。
4. 清洗与归一化规则确定、可测试、可审计（category / price / hours / state / MISC）。
5. 去重与幂等规则确定：重复导入同一批次不产生重复数据。
6. 评论数量与评分口径按 PRD §4.6 物化进 `restaurants.review_stats`。
7. 每次导入生成可查询的审计报告（`ingestion_batches`）。
8. 能稳定选出 2,000–5,000 家高覆盖餐厅作为 `is_active_for_demo`。
9. 餐厅名称 / 地址 / 类别的 trigram 模糊检索可用，查询返回可解释的 `similarity` 分数。

### 1.2 退出条件（Milestone Exit Criteria / Gate A）

> **Gate A 复核（2026-09-30，PostgreSQL 方案下）**
>
> 12 条退出条件逐条在真实全量语料上验证，结果如下。**12 条全部通过**
> （第 5 条按三项计数口径，已就范围达成决策）。
>
> | # | 退出条件 | 结果 | 证据 |
> |---|---|---|---|
> | 1 | `migrate` 可重复执行 | ✅ | 二次运行 `applied now=0`，不重复创建 |
> | 2 | `--stage=meta --limit=N` 导入样本并输出统计 | ✅ | `--limit=5000 --dry-run` → accepted=301, dedup=92, filtered=4,699 |
> | 3 | `--stage=all --limit=N` 正确写入 `restaurant_id` | ✅ | 4,156,055 条 review 全部关联成功，孤儿数 0 |
> | 4 | 同批次连续导入两次行数不增长 | ✅ | 重导 20 万条 review，4,156,055 → 4,156,055；重导 meta 后餐厅 `id` 不变 |
> | 5 | `review_stats` 计数语义清晰可重建 | ✅ 三项 | source/stored/text 完整；另两项计入 M2，见下方说明 |
> | 6 | 每次导入生成可查询审计报告 | ✅ | `ingestion_batches` 6 行，含行数/成功/去重/拒绝/缺失字段/耗时 |
> | 7 | `is_active_for_demo` 落在 2,000–5,000 | ✅ | 3,000 |
> | 8 | 名称/地址模糊查询可解释 | ✅ | `pg_trgm` 命中并返回 `similarity` 分数（"Raffaello" → 0.467 / 0.435 / 0.333） |
> | 9 | ≥1,000 家餐厅、≥100,000 条评论成功关联 | ✅ | 36,133 家 / 4,156,055 条，远超下限 |
> | 10 | 领域层无存储引擎依赖 | ✅ | `TestDomainLayerDoesNotNameAStorageEngine` 通过 |
> | 11 | 原始 `user_id` / `name` / `pics` 不入库 | ✅ | `reviews` 无这些列；全表文本扫描 0 命中 |
> | 12 | 两个 adapter 通过同一套契约测试 | ✅ | memory 与 postgres 各 3 个子测试全绿 |
>
> **第 5 条的缺口**：`embedded_review_count` 与 `representative_review_count`
> 全库为 0。这不是缺陷，而是里程碑边界——前者需要 `knowledge_documents` 的
> 向量（M2-06），后者依赖 `is_representative` 标记，而该标记由 M2-04 的
> 佐证级文档构建产出。目前**没有任何 stage 会写 `is_representative`**，
> 因此该计数无法在 M1 阶段产出真实数值。
>
> 三项计数（`source_` / `stored_` / `text_`）已完整可重建可校验，
> 语义与实现在 `curate/stats.go`。
>
> **已决策（2026-09-30）**：接受该现状。代表评论的选取标准本身依赖 M2-04 的
> chunk 策略，现在定规则大概率到 M2 还要推翻重来；而这两列在 M2 完成后即可
> 补齐，M1 阶段不构成阻塞。Gate A 第 5 条按 **source / stored / text 三项**口径通过，
> `representative_` 与 `embedded_` 计入 M2 交付范围。

- [ ] `data-pipeline migrate` 可重复执行，第二次运行不报错、不重复创建。
- [ ] `data-pipeline import --stage=meta --limit=N` 可导入有限样本并输出批次统计。
- [ ] `data-pipeline import --stage=all --limit=N` 可导入样本评论并正确写入 `restaurant_id`。
- [ ] 同一批次连续导入两次，`restaurants`、`reviews` 行数不增长（幂等）。
- [ ] `restaurants.review_stats` 的 `source_/stored_/text_/embedded_review_count` 语义清晰、可重建、可校验。
- [ ] 每次导入在 `ingestion_batches` 生成一条报告，含行数、成功数、去重数、拒绝数、缺失字段统计、耗时。
- [ ] `is_active_for_demo=true` 的餐厅数量落在 2,000–5,000 区间。
- [ ] 对 `restaurants.name` / `address` 的模糊查询返回结果并可解释（命中字段 + 分数）。
- [ ] 至少 1,000 家餐厅、100,000 条评论成功关联（Gate A 下限）。
- [ ] 领域层仍无存储引擎依赖（`shared/domain/architecture_test.go` 通过）。
- [ ] 原始 `user_id`、`name`、`pics` **不出现**在任何 curated 表中。
- [ ] Postgres adapter 与内存 adapter 通过同一套契约测试。

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
shared/port (RestaurantStore, ReviewStore, PipelineStore ...)
     │
     ▼
shared/adapter/repository/postgres (SQL + 行映射 + upsert)  ← pgx 类型只在此层
     │
     ▼
PostgreSQL 17 + pgvector + PostGIS + pg_trgm
  restaurants / reviews / review_summaries / knowledge_documents / boundaries / ingestion_*
```

依赖方向固定：`pipeline(curate)` → `shared/domain` → `shared/port` ← `shared/adapter/repository/postgres`。
`data-pipeline` 不 import `chat-service`；`shared/domain` 不 import pgx 或任何 SQL 驱动。

`shared/domain/architecture_test.go` 有一条测试守卫这个边界：领域层源码中不得出现
`postgres` / `postgis` / `pgvector` / `sql` 等存储引擎字样。

### 2.2 目标目录结构（M1 结束时）

```text
platepilot/
├── data-pipeline/
│   └── internal/
│       ├── config/                 # PipelineConfig（--limit 等运行参数）
│       └── pipeline/
│           ├── pipeline.go         # Import 编排：meta → curate → review → stats → score
│           ├── import.go           # import 子命令参数解析、分批写入
│           ├── migrate.go          # migrate 子命令（--drop / --status）
│           ├── prefilter.go        # prefilter 子命令：本地裁剪 review 语料
│           ├── progress.go         # 实时进度输出（字节 / 行 / 速率 / ETA）
│           ├── raw/                # 原始 schema 与流式 reader
│           │   ├── meta.go         # Meta 原始 struct（含 MISC）
│           │   ├── review.go       # Review 原始 struct
│           │   └── jsonl.go        # gzip JSONL 流式解码 + 行号/错误定位
│           ├── curate/             # 清洗、归一化、去重、打分（纯函数）
│           │   ├── normalize.go    # category/price/hours/state/MISC → DTO
│           │   ├── cuisine.go      # category → cuisine_tags 映射表
│           │   ├── geo.go          # borough 判定（边界优先，bbox 兜底）
│           │   ├── boundary.go     # 行政区多边形解析与点-多边形判定
│           │   ├── dedup.go        # ReviewDedupKey = sha256(...)
│           │   ├── score.go        # knowledge_score 与 is_active_for_demo
│           │   ├── pii.go          # 控制字符剥离 + 邮箱/电话脱敏
│           │   └── stats.go        # 评论数量与评分口径
│           └── report/             # 批次统计与审计报告
│               └── report.go
├── shared/
│   ├── domain/
│   │   ├── restaurant/             # curated 主数据、hours、attributes、review_stats
│   │   ├── review/                 # curated review、review summary、审计报告 DTO
│   │   └── architecture_test.go    # 领域层不得依赖存储引擎
│   ├── port/
│   │   ├── repository.go           # 读侧端口（M3 使用）
│   │   └── writer.go               # 写侧端口：RestaurantStore / ReviewStore / PipelineStore
│   └── adapter/
│       └── repository/
│           ├── postgres/           # PostgreSQL 实现
│           │   ├── client.go       # pgxpool 连接、配置、超时、错误映射
│           │   ├── migrate.go      # //go:embed 迁移执行与记账
│           │   ├── migrations/
│           │   │   └── 0001_init.sql   # 全部表、约束、索引（单一事实来源）
│           │   ├── rows.go         # 行 → 领域 DTO 的映射
│           │   ├── restaurant.go
│           │   ├── review.go
│           │   ├── pipeline.go
│           │   └── contract_test.go     # 跑共享契约套件（env-gated）
│           ├── memory/             # 内存实现，复用同一套契约测试
│           └── contract/           # 跨实现契约测试套件
└── deploy/
    ├── docker-compose.yml          # 本地 PostgreSQL（:55432）
    └── postgres/Dockerfile         # pgvector + PostGIS 组合镜像
```

> 说明：把原始 schema 放在 `data-pipeline/internal/pipeline/raw`（而非 `shared/domain`），因为原始字段是**输入格式**、不是领域语言；MISC/字段拼写/异常值都应在这一层收敛。
>
> `migrations/0001_init.sql` 是 schema 的**单一事实来源**，用 `//go:embed` 打进二进制。
> 不要把它复制到别处：两份定义迟早会漂移。容器镜像也刻意不把它放进 `initdb.d`。

### 2.3 M1 范围内的表

| 表 | 用途 | 主键 / 唯一键 | 主要索引 |
|---|---|---|---|
| `restaurants` | 餐厅结构化主数据（每餐厅一行） | `id` 自增；`source_record_id` 唯一 | `location` GiST、`name`/`address` trigram、`borough`+demo 偏索引 |
| `reviews` | 清洗后的评论 | `id` 自增 | `(restaurant_id, text_hash, rating, reviewed_at)` 唯一（幂等键）、`{restaurant_id, reviewed_at DESC}` |
| `review_summaries` | 预计算主题 / 情绪摘要 | `(restaurant_id, topic)` 复合主键 | 由主键覆盖 |
| `ingestion_batches` | 导入批次审计（M1-09） | `id` 自增 | `{stage, started_at DESC}` |
| `ingestion_rejections` | 被拒行审计（M1-09） | `id` 自增 | `batch_id` |
| `boundaries` | 行政区多边形（borough 标注的事实数据） | `id` 自增 | `geom` GiST、`name` trigram |

> `knowledge_documents`（含 `vector(1024)` 与 HNSW 索引）由 M1-02 建表、M2-06 写入；
> Agent 运行类表（`conversations` / `agent_runs` / `tool_calls` / `user_memories` 等）属于 M4。
> M1 **不写入**这些表，但表结构与字段语义不得与 PRD §4.7/§4.8 冲突。
>
> ⚠️ **已知缺口**：`boundaries` 表已建好但**尚无加载器**，`import` 阶段不写入它。
> 当前 borough 标签由 `curate` 的进程内点-多边形判定产出（正确且有 SHA-256 锁定），
> 但这无法回答"从地名出发"的反查。填充该表是地名检索需求的前置步骤。

## 3. 任务清单总览

| ID | 任务 | 交付物 | 依赖 | 工作量 | 验收摘要 |
|---|---|---|---|---|---|
| M1-01 | 数据库连接 | pgxpool 连接、连接池、超时、健康检查 | M0-02 | S | 本地能连接 PostgreSQL 并 ping 成功 |
| M1-02 | 表结构定义 | `restaurants`、`reviews`、`review_summaries` | M1-01 | M | `migrate` 可重复执行 |
| M1-03 | 基础索引 | 唯一索引、trigram、GiST、偏索引 | M1-02 | M | 关键查询无全表扫描 |
| M1-04 | Meta 流式导入 | gzip JSONL Reader + batch writer | M1-02 | L | 可导入有限样本并输出批次统计 |
| M1-05 | Review 流式导入 | Review Reader、关联、批量写入 | M1-04 | L | 样本评论正确关联 `restaurant_id` |
| M1-06 | 清洗与归一化 | category/price/hours/state/MISC 转换 | M1-04 | L | 规则有单测与审计样本 |
| M1-07 | 去重和幂等 | `gmap_id` 唯一键、幂等索引、upsert 规则 | M1-04, M1-05 | M | 重复导入不重复；`user_id` 不入库 |
| M1-08 | 评论统计聚合 | source/stored/text count、评分统计 | M1-05 | M | `review_stats` 可重建与校验 |
| M1-09 | 数据审计报告 | 行数、拒绝数、缺失字段、分布统计 | M1-04, M1-05 | M | 每次导入生成可查询报告 |
| M1-10 | 精选餐厅集合 | `knowledge_score`、`is_active_for_demo` | M1-06, M1-08 | M | 稳定选出 2,000–5,000 家 |
| M1-11 | 模糊检索索引 | 名称/地址/类别的 trigram 索引 | M1-02, M1-03 | M | 名称与地址模糊查询可解释 |

### 3.1 依赖图

```text
M1-01 (数据库连接)
  └─> M1-02 (表结构定义)
        ├─> M1-03 (基础索引) ──> M1-11 (模糊检索索引)
        └─> M1-04 (Meta 流式导入)
              ├─> M1-06 (清洗与归一化) ─┐
              └─> M1-05 (Review 流式导入)
                    ├─> M1-07 (去重与幂等)
                    ├─> M1-08 (评论统计聚合) ─┤
                    └─> M1-09 (审计报告)      │
                                              └─> M1-10 (精选餐厅集合)
```

### 3.2 推荐执行顺序

1. M1-01 数据库连接
2. M1-02 表结构定义
3. M1-03 基础索引
4. M1-04 Meta 流式导入（先打通最小样本）
5. M1-06 清洗与归一化（修正第 4 步产出的字段）
6. M1-05 Review 流式导入
7. M1-07 去重和幂等
8. M1-08 评论统计聚合
9. M1-09 数据审计报告
10. M1-10 精选餐厅集合
11. M1-11 模糊检索索引

> 最小可演示链路：`M1-01 → M1-02 → M1-03 → M1-04`，完成后即可验证"Go 服务能连数据库、建表与索引、导入 Meta 样本"。这是实施计划 §7 第一条最小链路的前半段。

> **导入前先跑 `prefilter`**：原始 review 文件 2.5 GB / 3,350 万行，其中只有约 12.7% 可导入。
> 不先裁剪会让 review 阶段多写入数倍行数、并显著拉长导入时间。

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
// 签名与 shared/port/writer.go 保持一致；评审时以该文件为准。
type RestaurantStore interface {
	GetByID(ctx context.Context, restaurantID int64) (restaurant.Restaurant, error)
	GetBySourceRecordID(ctx context.Context, sourceRecordID string) (restaurant.Restaurant, error)
	ListRestaurants(ctx context.Context, limit int) ([]restaurant.Restaurant, error)
	ListSourceRecordIDs(ctx context.Context) ([]string, error)
	MapSourceRecordIDs(ctx context.Context, sourceRecordIDs []string) (map[string]int64, error)
	UpsertRestaurant(ctx context.Context, r restaurant.Restaurant) error
	UpsertRestaurants(ctx context.Context, rs []restaurant.Restaurant) (int, error)
	UpdateReviewStats(ctx context.Context, restaurantID int64, stats restaurant.ReviewStats, computed restaurant.Rating) error
	UpdateScores(ctx context.Context, scores map[int64]float64, active map[int64]bool) error
	SelectForDemo(ctx context.Context, limit int) ([]restaurant.Restaurant, error)
	CountActiveForDemo(ctx context.Context) (int64, error)
}

type ReviewStore interface {
	UpsertReviews(ctx context.Context, items []review.Review) (int, error)
	ListByRestaurant(ctx context.Context, restaurantID int64, limit int) ([]review.Review, error)
	CountByRestaurant(ctx context.Context, restaurantID int64) (review.Counts, error)
	AggregateStats(ctx context.Context, restaurantIDs []int64) (map[int64]review.Counts, error)
	RestaurantIDsWithReviews(ctx context.Context) ([]int64, error)
}

type PipelineStore interface {
	// StartBatch 返回数据库分配的批次 id：运行期间缓存的拒绝记录要靠它回链。
	StartBatch(ctx context.Context, report review.BatchReport) (int64, error)
	FinishBatch(ctx context.Context, report review.BatchReport) error
	RecordRejections(ctx context.Context, items []review.Rejection) error
	ListBatches(ctx context.Context, limit int) ([]review.BatchReport, error)
	BatchDetail(ctx context.Context, batchID int64) (review.BatchReport, []review.Rejection, error)
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
- 内存实现与 Postgres 实现均满足同一套契约测试。
- `search.RestaurantDetail` 仍保留（供 M3 API 投影用），不强行并入 `restaurant.Restaurant`。

---

### M1-01 数据库连接

**目标**：实现 `shared/adapter/repository/postgres` 的连接与健康检查，使服务能安全连接本地 PostgreSQL。

**交付物**

- `shared/adapter/repository/postgres/client.go`：
  - `type Config struct { DSN, Database string; Timeout, ConnectTimeout time.Duration; MaxPoolSize, MinPoolSize int32 }`。
  - `func Connect(ctx context.Context, cfg Config) (*Client, error)`：`pgxpool.New` + `Ping` 握手；失败即返回带 `provider_unavailable` 的错误。
  - `func (c *Client) Pool() *pgxpool.Pool` **仅包内使用**；对外只暴露领域方法，`pgx.*` 类型不得逃逸。
  - `func (c *Client) withTimeout(ctx) (context.Context, context.CancelFunc)`：单次操作的统一超时派生。
  - `func (c *Client) Close(ctx context.Context) error`。
  - `func ConfigFromPostgres(cfg sharedcfg.PostgresConfig) Config`：从共享配置构造。
- 包边界：`shared/adapter/repository/postgres` 的对外接口只接受/返回领域类型，
  `pgx.*` 与 SQL 字符串不得逃逸到 `shared/port` 之外。
- 单测：配置校验与错误映射；连接测试用**环境门控**（见下）。
- `chat-service` 健康检查接线：`/healthz` 在配置了 PostgreSQL 时附加 `postgres: ok/degraded`（未配置则跳过，不阻塞启动）。

**实现要点**

- Driver：`github.com/jackc/pgx/v5` + `pgxpool`。全仓库统一一条线，不得混用 v4/v5。
- 连接串形态：既支持 URI（`postgres://...`），也支持 libpq 的 keyword/value 形式
  （`host=... dbname=...`）。`shared/config.RedactURI` 两者都要能脱敏，且不能把普通字符串
  误判成连接串——加一层"看起来像 keyword DSN"的判定。
- 连接池按"两服务各自持有自己的 Client"设计：`data-pipeline` 的池可小而长寿，
  `chat-service` 的池按并发读调优。
- 超时分层：`ConnectTimeout`（握手）与 `Timeout`（单次操作）分开，单次操作用
  `context` 派生超时，避免一个慢查询占满连接。
- 错误映射：驱动"连不上 / 认证失败 / 超时 / CHECK 冲突"统一映射为 `errs` 的
  `provider_unavailable` / `provider_timeout` / `invalid_argument`；不要泄露连接串。
- `.env.example` 增加 `POSTGRES_TIMEOUT` / `POSTGRES_CONNECT_TIMEOUT` /
  `POSTGRES_MAX_POOL_SIZE` / `POSTGRES_MIN_POOL_SIZE`（可选，带默认值）。

**依赖**：M0-02（配置）。
**工作量**：S。

**验收标准**

```bash
# 配置了 POSTGRES_DSN 时
go run ./data-pipeline check-config         # 摘要显示 postgres enabled=true

# 未配置 POSTGRES_DSN 时
go test ./...
```

- `POSTGRES_DSN` 正确时连接成功；DSN 错误或网络不可达时返回 `provider_unavailable` /
  `provider_timeout`，日志与错误均不含明文凭据。
- `go test ./...` 在没有数据库的环境下全绿（连接测试 env-gated）。
- 日志为 JSON 且包含 `trace_id` / `request_id`。

---

### M1-02 表结构定义

**目标**：定义各表的显式建表逻辑与行映射，使 `migrate` 可重复执行。

**交付物**

- 新增 `data-pipeline migrate` 子命令（扩展 `data-pipeline/main.go` 的 usage 与分发）：
  - `data-pipeline migrate`（建表 + 索引）
  - `data-pipeline migrate --drop`（仅本地/test，显式危险操作）
  - `data-pipeline migrate --status`（只报告已应用的迁移，不改动任何东西）
  - 成功后打印每个迁移的 `applied` / 状态与时间。
- `shared/adapter/repository/postgres/migrations/0001_init.sql`：
  - **schema 的单一事实来源**，用 `//go:embed migrations/*.sql` 打进二进制。
  - 全部 `CREATE TABLE IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS`，天然幂等。
- `shared/adapter/repository/postgres/migrate.go`：
  - `func Migrations() ([]Migration, error)`：读出内嵌的迁移。
  - `func (c *Client) Migrate(ctx) ([]MigrationStatus, error)`：按序执行未应用的迁移，
    并在 `schema_migrations` 表记账。
  - `func (c *Client) Drop(ctx) error`：删除全部业务表（`--drop` 与契约测试用）。
  - `func (c *Client) MigrationStatuses(ctx, migrations) ([]MigrationStatus, error)`。
- `shared/adapter/repository/postgres/rows.go`：行 ↔ 领域 DTO 的映射（**只在此层**）。
- 单测：覆盖 domain↔row 往返（round-trip），保证零值/指针/时间/三态字段不丢语义。

**表结构要点（对齐 PRD §4.3–§4.6）**

`restaurants` 关键列（节选）：

```sql
CREATE TABLE restaurants (
    id                  bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    source              text   NOT NULL,
    source_record_id    text   NOT NULL,          -- Google gmap_id，全局唯一
    name                text   NOT NULL,
    address             text,
    borough             text,                     -- 可空：真实边界下 58.9% 在五区之外
    location            geography(Point,4326),    -- 经度在前（RFC 7946）
    categories          text[] NOT NULL DEFAULT '{}',
    cuisine_tags        text[] NOT NULL DEFAULT '{}',
    price_raw           text,
    price_level         smallint,                 -- CHECK 1..4
    rating_source_avg   double precision,         -- 不用 real：float32 表示不了 4.42
    rating_computed_avg double precision,
    rating_count        integer NOT NULL DEFAULT 0,
    source_review_count integer NOT NULL DEFAULT 0,
    source_review_count_capped boolean NOT NULL DEFAULT false,
    stored_review_count integer NOT NULL DEFAULT 0,
    text_review_count   integer NOT NULL DEFAULT 0,
    representative_review_count integer NOT NULL DEFAULT 0,
    embedded_review_count integer NOT NULL DEFAULT 0,
    last_reviewed_at    timestamptz,
    attributes          jsonb NOT NULL DEFAULT '{}',
    attributes_raw      jsonb NOT NULL DEFAULT '{}',
    hours               jsonb NOT NULL DEFAULT '[]',
    relative_results    text[] NOT NULL DEFAULT '{}',
    knowledge_score     double precision NOT NULL DEFAULT 0,
    is_active_for_demo  boolean NOT NULL DEFAULT false,
    observed_at         timestamptz NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_record_id)
);
```

- `reviews`：`{ id bigint 自增, restaurant_id bigint REFERENCES restaurants ON DELETE CASCADE, rating smallint CHECK 1..5, reviewed_at, text, language, text_hash, is_representative, topic_tags text[], source_observed_at }`，
  外加唯一索引 `(restaurant_id, text_hash, rating, reviewed_at)` 作为**幂等键**。
- `review_summaries`：主键 `(restaurant_id, topic)`，另有 `sentiment` / `positive_ratio` /
  `summary` / `evidence_count` / `valid_from` / `valid_to` / `generated_by` / `generated_at`。
- `knowledge_documents`：`document_id bigint 自增` + `embedding vector(1024)` +
  `retrieval_scope` / `doc_type` / `content_hash` / `version` / `is_active`。
  CHECK 约束 `is_active = false OR embedding IS NOT NULL`。

**实现要点**

- **主键由数据库分配**：`GENERATED BY DEFAULT AS IDENTITY`。写入语句不包含 `id` 列，
  应用层不生成 id。相比 `text` 存 UUID 字符串省约 300 MB（全库 4.2 GB → 2.9 GB 实测），
  且因为值随插入顺序递增，主键 btree 是顺序追加而非随机分裂。
- **迁移不要放进 `initdb.d`**：容器初始化只执行一次，放进去会导致 schema 与代码漂移。
  迁移必须由 `migrate` 子命令经 `schema_migrations` 记账执行。
- **时间统一**：所有时间列用 `timestamptz`；文本时间（如 `hours` 里的 `"11AM-10PM"`）
  保留原文在 jsonb 中，排序用的结构化值放内存。
- **地理类型**：`location` 用 `geography(Point,4326)`（米制距离可直接算）。
  但 `ST_IsValid` / `ST_X` / `ST_Y` 只接受 `geometry`，CHECK 约束里必须写
  `location::geometry`。
- **不要**在 M1 写入 `knowledge_documents`（M2）、`user_memories` 等（M4），
  但字段命名（`cuisine_tags` / `price_level` / `rating_source_avg`）要与 M2 的向量
  过滤字段保持一致。

**依赖**：M1-01。
**工作量**：M。

**验收标准**

```bash
data-pipeline migrate            # 首次：applied 0001_init.sql
data-pipeline migrate            # 再次运行：applied now=0，不报错
data-pipeline migrate --status   # 只报告，不改动
```

- `migrate` 连续执行两次退出码均为 0，第二次不重复创建。
- `restaurants` 行能原样往返 domain（round-trip 测试通过）。
- 未配置 `POSTGRES_DSN` 时 `migrate` 以 `invalid_argument` / 明确提示失败，不静默成功。
- `pgvector` 与 `postgis` 扩展存在（`TestRequiredExtensionsPresent` 守住这条）。

---

### M1-03 基础索引

**目标**：为各表建立唯一索引、trigram 索引、地理索引与偏索引，保证关键查询走索引。

**交付物**

- 索引定义集中在 `migrations/0001_init.sql`，随迁移一起执行。
- `migrate` 执行完建表后自然包含全部索引；`--status` 可确认已应用。
- 另有 `TestVectorIndexStrategyIsPresent` 守住向量索引的形状（见下）。
- 索引定义文档化到本文件附录 C。

**索引定义（M1 建议）**

| 表 | 索引 | 类型 | 用途 |
|---|---|---|---|
| `restaurants` | `{source_record_id}` | unique | 幂等 upsert 键 |
| `restaurants` | `{name}` / `{address}` GIN `gin_trgm_ops` | trigram | 模糊匹配（部分索引，`address` 限非空） |
| `restaurants` | `{location}` GiST | 地理 | `ST_DWithin` 半径筛选 |
| `restaurants` | `{location::geometry}` GiST | 地理 | 配合 `ST_IsValid` / `ST_X` / `ST_Y` |
| `restaurants` | `{cuisine_tags}` GIN | 数组 | `= ANY(cuisine_tags)` |
| `restaurants` | `{price_level}` | 偏索引 | 限非空 |
| `restaurants` | `{rating_source_avg DESC}` | 偏索引 | 限非空 |
| `restaurants` | `{borough}` | 偏索引 | 限 `is_active_for_demo` |
| `restaurants` | `{is_active_for_demo, knowledge_score DESC}` | 偏索引 | 精选集与排序 |
| `reviews` | `{restaurant_id, text_hash, rating, reviewed_at}` | unique | **幂等键** |
| `reviews` | `{restaurant_id, reviewed_at DESC}` | 复合 | 按餐厅取评论 |
| `reviews` | `{restaurant_id, is_representative, rating}` | 偏索引 | 代表评论选择 |
| `reviews` | `{text_hash}` | 单字段 | 去重辅助（非唯一） |
| `review_summaries` | 主键 `(restaurant_id, topic)` | 复合主键 | 每餐厅每主题一条 |
| `ingestion_batches` | `{stage, started_at DESC}` | 复合 | 审计查询 |
| `knowledge_documents` | `{embedding}` HNSW `vector_cosine_ops` | 向量 | 餐厅级召回 |
| `knowledge_documents` | 每 borough 一个 partial HNSW | 向量 | 带地区过滤的召回 |

**实现要点**

- 唯一索引必须在上数据**之前**创建；若已有重复数据，先跑 M1-07 的去重清理，
  否则建索引会失败——`migrate` 应把该错误映射为 `conflict` 并说明。
- 复合索引字段顺序对齐查询谓词顺序（等值字段在前，范围/排序字段在后）。
- **pgvector HNSW 不能靠 `WHERE` 裁剪**：HNSW 是 `ORDER BY` 结构，附加 `WHERE` 会退化为
  Seq Scan（5 万行实测 `Rows Removed by Filter: 49759`）。因此过滤字段必须
  **反规范化**到 `knowledge_documents`（`borough` 列），并为每种过滤值建一个
  partial HNSW。诚实的局限：borough + radius + cuisine 叠加过滤仍会退化为
  filter-then-sort，当前语料规模下可接受，已记录在案。
- 坐标顺序为 `[longitude, latitude]`（RFC 7946 / PostGIS 规范），与原始数据字段顺序相反，
  映射时务必交换。
- 索引名显式命名，便于审计与 diff。

**依赖**：M1-02。
**工作量**：M。

**验收标准**

```bash
data-pipeline migrate --status   # 确认迁移已应用，索引随之存在
```

- 重复运行不报错。
- `restaurants` 的 `{source_record_id}` 唯一索引存在且强制唯一（插入重复键报 `conflict`）。
- 用 `EXPLAIN` 抽查：按 `is_active_for_demo + borough` 过滤命中 `Index Scan` 而非 `Seq Scan`。
- 名称/地址模糊查询命中 trigram 索引，且能返回 `similarity()` 分数。
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
  - `import` 子命令参数：`--stage=meta|review|stats|score|all`、`--limit=N`、`--batch=N`、
    `--data-dir=`、`--review-file=`、`--dry-run`、`--quiet`、`--skip-file-hash`。
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
- 实际导入后 `restaurants` 行数等于接受数（去重后）；`source_record_id` 全部非空且唯一。
- 长行（>64KB）不被截断；截断 JSON 计入拒绝而非 panic。
- 数据文件缺失时返回 `not_found`，错误信息含路径。

---

### M1-05 Review 流式导入

**目标**：实现 Review 流式读取、关联到已导入餐厅、批量写入 `reviews`，并产出批次统计。

**交付物**

- `data-pipeline/internal/pipeline/raw/review.go`：Review 原始 struct（`user_id`、`name`、`time`、`rating`、`text`、`pics`、`resp`、`gmap_id`）。
- `data-pipeline/internal/pipeline/import.go` 扩展 `--stage=review|all`：
  - 先用 `MapSourceRecordIDs` 一次性把 `source_record_id → restaurant.id` 载入映射；
    逐行查询会触发数百万次点查，必须批量预取。
  - 逐行关联、清洗（M1-06）、计算去重键（M1-07）、批量 `ReviewStore.UpsertReviews`。
- 单测：关联命中、关联未命中（餐厅不存在）、时间戳边界、空文本、超短文本、含 PII、重复行。

**实现要点**

- **关联键**：评论只带 `gmap_id`，必须通过 `source_record_id` 关联到 `restaurants.id`；未命中的评论**不写入** `reviews`（计入 `unmatched` 统计）。
- **映射数据量**：17,763 家餐厅可一次性载入内存映射；全量 5 亿条评论不行，评论必须逐行处理。
- **时间转换**：`time`（Unix 毫秒）→ UTC `time.Time`；范围校验（如 2000–2021）外的记录计入拒绝。
- **文本处理**（M1-06 落点）：去空、去超短（< 阈值，建议 20–50 字符，可配置）、去模板化重复；保留原文进 `text`，PII 脱敏。
- **不落库字段**：`user_id`、`name`、`pics` 绝不写入 curated `reviews`；`user_id` 只参与去重键哈希，用后即弃。
- **批次写入**：与 Meta 一致，按 `--batch` 聚合；`ReviewStore.UpsertReviews` 幂等。
  走 `pgx.Batch` 单次往返，且**不开事务**——本地单实例不需要远端集群那样的持久性边界。
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
  - `NormalizeReview(raw raw.Review, restaurantID int64) (review.Review, error)`。
- `curate/cuisine.go`：`category` → `categories` + `cuisine_tags` 映射表（如 `"Pizza restaurant" → "pizza"`）；未命中类别保留原值、标记 `extra`。
- `curate/price.go`：`$`/`$$`/`$$$`/`$$$$` → `price.level 1..4`；异常/货币字符保留 `raw`、`level=nil`。
- `curate/hours.go`：`[["Monday","11AM–10PM"], ...]` → `[{weekday, open_minute, close_minute, is_closed}]`；处理多段营业、闭店日、跨午夜。
- `curate/attributes.go`：`MISC` 主题 → 稳定标签 + 三态属性；未知/缺省 = `"unknown"`。
- `curate/geo.go`：`latitude/longitude` → GeoJSON `[lon, lat]` + `borough_guess`（用 bbox 规则）。
- `curate/pii.go`：剥离控制字符 + 评论中邮箱/电话/地址脱敏（正则 + 掩码）。
  控制字符是**必需**的：PostgreSQL 拒绝 NUL（SQLSTATE 22021），原始语料中确实存在。
  tab / 换行 / 回车保留，只剥离 `\x00-\x08`、`\x0b`、`\x0c`、`\x0e-\x1f` 等。
- 单测 + `testdata/` 审计样本：每个规则至少 1 组「正常 / 边界 / 异常」用例，且**黄金样本**（输入 → 期望输出）纳入版本控制。
- 审计样本：`curate` 包的黄金样本（输入 → 期望输出）纳入版本控制，作为回归基线。

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

- 归一化是**纯函数**：不访问数据库、不读文件，输入原始结构、输出领域 DTO 或错误，方便单测与回归。
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

**目标**：定义并实现稳定的唯一键与 upsert 规则，保证重复执行同一批次不产生重复数据。

**交付物**

- `data-pipeline/internal/pipeline/curate/dedup.go`：
  - `func ReviewDedupKey(gmapID, userID string, unixMilli int64, textHash string) string`：
    `sha256(gmap_id + "\x00" + user_id + "\x00" + strconv(time) + "\x00" + text_hash)`。
    注意它是**去重键**，不是主键——主键由数据库分配（见下）。
  - `func TextHash(text string) string`：`sha256(normalized_text)`。
  - Meta 去重：同 `gmap_id` 多行时保留字段最完整的一条（缺失字段少者优先），冲突记录标记。
- `shared/adapter/repository/postgres/restaurant.go`：
  - upsert 冲突目标 `source_record_id`；**插入列清单不含 `id`**，让 identity 列分配新值、
    冲突时保留既有值，因此重复导入不会改变餐厅 ID，评论外键保持稳定。
  - 更新列表**刻意不含** `created_at`、评论汇总列与评分列：那些由 stats / score 阶段拥有，
    meta 重导不得把它们回滚。
- `shared/adapter/repository/postgres/review.go`：
  - 冲突目标为唯一索引 `(restaurant_id, text_hash, rating, reviewed_at)`；
    同样不写 `id`。
- `data-pipeline/internal/pipeline/dedup.go`：有界的 in-run 去重计数器，
  按去重键统计输入流中的重复；超过上限（200 万）后 `deduped` 降级为**下界**，
  但"不产生重复行"的正确性不依赖它。
- 单测：同一输入两次 → 同一去重键；不同 `user_id` → 不同去重键；文本规范化前后 hash 稳定。

**幂等规则**

| 表 | 幂等键 | 冲突策略 |
|---|---|---|
| `restaurants` | `{source_record_id}` unique | upsert；同 `gmap_id` 取字段最完整版本 |
| `reviews` | `{restaurant_id, text_hash, rating, reviewed_at}` unique | upsert |
| `review_summaries` | 主键 `{restaurant_id, topic}` | upsert |
| `ingestion_batches` | 自增 `id` | 每次导入产生新批次记录 |
| `ingestion_rejections` | 自增 `id` | 追加写 |

**实现要点**

- **主键由数据库分配**（`bigint GENERATED BY DEFAULT AS IDENTITY`），应用层不生成 id，
  写入语句不含 `id` 列。
- **review 的幂等性由唯一索引承担，不是主键**：`ON CONFLICT (restaurant_id, text_hash,
  rating, reviewed_at)`。主键是自增值，无法用于识别"同一条评论的重复导入"。
  该键与评论的内容哈希 `sha256(gmap_id + user_id + time + text_hash)` 选择完全相同的行——
  `rating` 与 `user_id` 都由 `text_hash` 唯一决定，已在全量语料上验证 0 组差异。
  因此重导是"更新原行"而非"插入副本"。
- 原始 `user_id` 只参与哈希，**绝不落库**；`text_hash` 存规范化文本的 hash（非原文）。
- 更新列表必须覆盖全部可变业务字段，避免旧批次残留导致"看似更新实则未更新"。
- 批量写入需处理部分失败：`pgx.Batch` 逐条取结果，汇总后决定重试或报错；不得静默吞错。
- **不依赖事务**：用唯一索引 + upsert 实现幂等，避免长事务带来的锁与膨胀。
- **不能用 `VALUES` 列表做批量更新**：扩展协议上限 65,535 个绑定参数，
  36,133 行 × 2 就超了。`UpdateScores` 用 `unnest($1::bigint[], $2::double precision[])`，
  语句大小不再随语料增长。

**依赖**：M1-04、M1-05。
**工作量**：M。

**验收标准**

```bash
data-pipeline import --stage=all --limit=100000
data-pipeline import --stage=all --limit=100000   # 第二次
# 断言：restaurants 与 reviews 的行数不增长
```

- 连续两次导入同一批次，`restaurants` 与 `reviews` 行数不变。
- 重复导入后餐厅 `id` 不变（评论不孤儿）；孤儿 review 数为 0。
- `reviews` 中不含 `user_id` / `name` / `pics` 字段。

---

### M1-08 评论统计聚合

**目标**：按 PRD §4.6 口径，把评论数量与评分统计物化进 `restaurants.review_stats`，支持重建与校验。

**交付物**

- `data-pipeline/internal/pipeline/curate/stats.go`：
  - `func ComputeStats(restaurantID string, restaurantRev []review.Review, source restaurant.ReviewStats, now time.Time) restaurant.ReviewStats`。
  - 计算：`stored_review_count`、`text_review_count`、`representative_review_count`（M1 可先置 0，由 M2 填 `embedded_review_count`）、评分分布、`last_reviewed_at`、`computed_avg`。
- `shared/adapter/repository/postgres/restaurant.go` 增加：
  - `UpdateReviewStats(ctx, restaurantID string, stats restaurant.ReviewStats) error`。
  - `AggregateStats(ctx, restaurantIDs []int64) (map[int64]review.Counts, error)`：用 `GROUP BY restaurant_id` 计算 count/avg，
    评分直方图走第二条查询（而不是 window function），让主 rollup 保持可读。
  - 全量重算：重新执行 `--stage=stats` 即可整体重建，无需额外接口。
- `data-pipeline import --stage=stats`：重算并写回 `review_stats`。
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

- 统计**物化**进 `restaurants` 的统计列，不要每次搜索实时 `COUNT(*)`（PRD §4.6 明确要求）。
- 计数增量 vs 重建：`--stage=stats` 是全量重算，可重复执行以修复漂移。
- `source_review_count` 与 `stored_review_count` **语义不同，禁止互相覆盖**；`computed_avg` 只在样本足够时展示，且标注"入库样本平均"。
- 截顶判定：`num_of_reviews` 达到 9998（或特定阈值）时置 `source_review_count_capped=true`，不当作精确值。
- 聚合用一条 `UPDATE restaurants … FROM (SELECT restaurant_id, count(*) … GROUP BY restaurant_id)` 写回，按 `restaurant_id` 分批，避免大表全表聚合超时。

**依赖**：M1-05。  
**工作量**：M。

**验收标准**

```bash
data-pipeline import --stage=stats --batch=500
```

- 对同一餐厅，`stored_review_count` 等于 `SELECT count(*) FROM reviews WHERE restaurant_id = $1`。
- `text_review_count` 等于有效文本评论数；`last_reviewed_at` 等于该餐厅最新评论时间。
- 重建前后统计一致（幂等）。
- 评分分布之和等于 `stored_review_count`。
- 无评论餐厅的计数为 0、`last_reviewed_at` 为 null，不报错。

---

### M1-09 数据审计报告

**目标**：每次导入生成可查询的批次报告与拒绝明细，使导入过程可追溯、可复核。

**交付物**

- `shared/domain/review/review.go`（审计 DTO 与领域类型同包）：
  - `type BatchReport struct { BatchID, Stage, CurationVersion, SourceFile, SourceSHA256 string; StartedAt, FinishedAt time.Time; RowsRead, Accepted, Written, Deduped, Rejected, Unmatched, MissingFields int64; DurationMS int64; Status string; ErrorCode string }`。
  - `type FieldMissing struct { Field string; Count int64 }`。
- `data-pipeline/internal/pipeline/report/report.go`：采集与汇总；每次 import 开始/结束写 `ingestion_batches`。
- `shared/adapter/repository/postgres/pipeline.go`：实现 `PipelineStore`（`StartBatch` / `FinishBatch` /
  `RecordRejections` / `ListBatches` / `BatchDetail`），表 `ingestion_batches`、`ingestion_rejections`。
  - `StartBatch` 必须**返回数据库分配的 id**（自增主键在 insert 前未知），
    因为运行期间缓存的拒绝记录要靠它回链；rejection 只在收尾写一次，届时统一盖戳。
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
- 文件哈希对 2.65 GB 文件应**流式计算**（首次计算慢，可用 `--skip-file-hash` 跳过）。
- 拒绝明细限制大小：只记录 `line_no + reason + source_record_id`，**不落原始文本**（避免 PII）。
- 报告可查询是验收点：提供 `report` 子命令（`--last=N` / `--batch-id=N`），
  批次行也可直接用 SQL 查询。
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
- `shared/adapter/repository/postgres/restaurant.go` 增加：
  - `UpdateScores(ctx, scores map[string]float64, active map[string]bool) error`。
  - `SelectForDemo(ctx, limit int) ([]restaurant.Restaurant, error)`。
- `data-pipeline import --stage=score --demo-target=N`：全量重算分数与精选标志。
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
# 断言：SELECT count(*) FROM restaurants WHERE is_active_for_demo ∈ [2000, 5000]
```

- `is_active_for_demo=true` 数量落在 2,000–5,000。
- 所有 `permanently_closed` 默认 `false`。
- 分数排序稳定：重复运行得到相同集合。
- `SelectForDemo` 对样本能给出每个入选者的加分原因。

---

### M1-11 模糊检索索引

**目标**：为餐厅名称、地址提供可解释的模糊检索能力。

**交付物**

- `migrations/0001_init.sql` 中的 trigram 索引（随 M1-03 一起创建）：
  - `restaurants_name_trgm`：`GIN (name gin_trgm_ops)`
  - `restaurants_address_trgm`：`GIN (address gin_trgm_ops) WHERE address IS NOT NULL`
  - `boundaries_name_trgm`：`GIN (name gin_trgm_ops)`（地名 → 行政区，M3 使用）
- `pg_trgm` 扩展在迁移中启用。
- 查询侧约定（供 M3-02 使用）：
  ```sql
  SELECT id, name, address,
         similarity(name, $1)  AS name_score,
         similarity(address, $1) AS address_score
  FROM restaurants
  WHERE name % $1 OR address % $1
  ORDER BY greatest(similarity(name, $1), similarity(address, $1)) DESC;
  ```

**实现要点**

- **`pg_trgm` 而不是独立搜索引擎**：本项目的文本检索需求是"名称/地址模糊匹配"，
  trigram 索引 + `similarity()` 已经足够，且**返回可解释的分数**（命中字段 + 相似度），
  不需要额外的服务进程或控制台操作。类别/菜系是结构化过滤（`cuisine_tags` GIN），
  不走全文检索。
- **名称归一化**：`boundaries.name` 在加载时归一化，使 `'Staten Island'` 与
  `'staten_island'` 命中同一行。
- `similarity()` 的默认阈值是 `pg_trgm.similarity_threshold`（0.3）。需要更严格的
  匹配时用 `set_limit()` 或在查询里显式给出分数门槛，而不是靠"取前 N 条"——
  后者会让低分结果混进来且不可解释。
- `address` 用部分索引（`WHERE address IS NOT NULL`）：约 45 条记录无地址，
  把它们索引进去是浪费。
- trigram 索引**不适合短于 3 字符**的模式（trigram 本身需要 3 字符）。
  一两字符的查询应走前缀索引或结构化过滤，这是已知限制。
- 中文检索依赖 `pg_trgm` 而非专用分词器：对中文**模糊**匹配够用，
  但不具备中文分词能力。若语料日后包含大量中文描述性内容，需要重新评估
  （已记录在 README 的 Known limitations）。

**依赖**：M1-02、M1-03。
**工作量**：M。

**验收标准**

```sql
-- 名称模糊：应返回 "Raffaello Kosher Pizza" 等
SELECT name, similarity(name, 'Raffaello') AS score
FROM restaurants WHERE name % 'Raffaello'
ORDER BY score DESC LIMIT 5;

-- 地址模糊：应能按地址命中
SELECT name, address, similarity(address, 'Carmine') AS score
FROM restaurants WHERE address % 'Carmine'
ORDER BY score DESC LIMIT 5;
```

- 名称与地址模糊查询均返回结果，且**带可解释的 `similarity` 分数**。
- 用 `EXPLAIN` 确认命中 `Bitmap Index Scan on restaurants_name_trgm`，
  而非 `Seq Scan`。
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
- [ ] 不绕过领域接口直接调用驱动 API（pgx 类型不逃逸 `postgres` 包）。
- [ ] 文档或注释说明关键设计。
- [ ] 通过 `go test ./...`、`go vet ./...`、`go test -race ./...`（`golangci-lint` 待安装）。
- [ ] 相关验收标准可以实际演示。

### 6.2 M1 里程碑门（Gate A）

- [ ] `data-pipeline migrate` 重复执行不报错、不重复创建。
- [ ] 各表建立完成，索引清单与附录 C 一致。
- [ ] Meta 导入：`restaurants` ≥ 1,000（样本）且 `source_record_id` 唯一。
- [ ] Review 导入：`reviews` ≥ 100,000（样本）且全部关联到已存在餐厅。
- [ ] 重复导入不产生重复数据（`restaurants` / `reviews` 计数不变）。
- [ ] `user_id` / `name` / `pics` 不出现在任何 curated 表。
- [ ] `restaurants.review_stats` 与 `reviews` 聚合一致、可重建。
- [ ] `ingestion_batches` 每次导入一条报告且可查询。
- [ ] `is_active_for_demo=true` 数量 ∈ [2,000, 5,000]。
- [ ] `restaurants` Search 索引可用，名称/地址模糊查询有结果。
- [ ] 领域层无存储引擎依赖（`shared/domain/architecture_test.go` 通过）。
- [ ] Postgres adapter 与内存 adapter 通过同一套契约测试。
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
| M3-01/02 检索 | `restaurants` 索引 + `pg_trgm` | 结构化过滤与名称/地址检索 |
| M6-02 RAG 评测 | `ingestion_batches` + `reviews` | 评测数据来源与可追溯性 |

**接口稳定性要求**：M1 对 `shared/port/repository.go` 的扩展是 M2/M3 的契约。若后续必须再调整，需同步更新：

- 本文档 §4.0 的接口形状。
- `docs/platepilot-implementation-plan.md` 对应任务的依赖与验收。
- 所有实现（Postgres / 内存 / Mock）与契约测试。

**M1 明确不做的事**（避免范围蔓延）：

- 不生成 `knowledge_documents`、不写任何向量、不建 Vector 索引（M2）。
- 不实现检索服务、召回、融合、rerank（M3）。
- 不接入 Eino / Chat Provider / SSE（M4）。
- 不实现预约功能（可选，M5-06）。
- 不实现前端（M6-06）。
- 不引入 LLM 生成摘要（评论摘要先用规则统计，LLM 摘要为 M2 可选增强）。

---

## 8. 风险与注意事项

| 风险 | 触发点 | 控制措施 |
|---|---|---|
| 数据库依赖 | 本地未起容器时无法验证 | 提供 `--dry-run`；契约测试 env-gated，单测离线全绿。`make pg-up` 一条命令起库 |
| 全量导入巨大 | Review 2.65 GB / 33M 行，内存或耗时失控 | 强制流式 + `--limit`；`bufio.Scanner` 调大 buffer 或 `json.Decoder` |
| `bufio.Scanner` 截断长行 | 长描述/长评论超过 64KB | 显式 `Scanner.Buffer` 或改用 `json.Decoder`；加长行单测 |
| 幂等键设计错误 | `review_id` 不稳定导致重复 | `review_id` 纯函数 + 单测；唯一索引兜底 |
| PII 泄露 | `user_id` / `name` / 邮箱进 curated 集合 | 哈希用后即弃；PII 脱敏；契约测试断言字段不存在 |
| 去重与唯一索引冲突 | 已有重复数据导致建唯一索引失败 | 先跑 M1-07 清理，再建唯一索引；错误映射 `conflict`|
| 统计口径混淆 | `source` / `stored` / `text` 混用 | 字段语义表；重跑 `--stage=stats` 校验；禁止互相覆盖 |
| 评分选择不稳定 | 并列分数导致集合抖动 | 确定性 tie-breaker（`source_record_id`）；重复运行断言集合不变 |
| 索引字段顺序错误 | 复合索引不被命中 | 等值在前、范围/排序在后；`explain()` 抽查 `IXSCAN` |
| trigram 短查询 | 模式短于 3 字符时 trigram 失效 | 已知限制：短查询走结构化过滤或前缀匹配，不依赖 trigram |
| 收录范围误判 | 类别误匹配引入非餐饮 | 白名单 + 坐标 bbox 双重过滤；curated 层可纠正 |
| 服务边界破坏 | `data-pipeline` import `chat-service` | code review + 目录约束；共享只经 `shared/` |
| 驱动版本混用 | v1/v2 import path 并存 | M1-01 锁定单一主线并全局统一 |

---

## 附录 A：环境变量（M1 相关）

在 M0 `.env.example` 基础上，M1 需要/新增：

```dotenv
# --- PostgreSQL（M1 起必需）------------------------------------------------
POSTGRES_DSN=postgres://platepilot:platepilot@localhost:55432/platepilot?sslmode=disable
POSTGRES_DATABASE=platepilot
POSTGRES_TIMEOUT=30s
# 可选：连接池与连接超时
# POSTGRES_CONNECT_TIMEOUT=10s
# POSTGRES_MAX_POOL_SIZE=8
# POSTGRES_MIN_POOL_SIZE=0

# --- data-pipeline（M1 起使用）--------------------------------------------
PIPELINE_DATA_DIR=data/raw/google_local
PIPELINE_BATCH_SIZE=1000
PIPELINE_WORKERS=4
# 精选餐厅目标数量（M1-10），默认裁剪到 [2000, 5000]
# PIPELINE_DEMO_TARGET=3000
# 评论最短文本阈值（M1-06）
# PIPELINE_MIN_REVIEW_CHARS=20
# 导入范围（原始 meta 文件其实是全美数据，靠这个框住纽约）
# PIPELINE_BBOX=40.49,-74.26,40.93,-73.68
# 行政区边界几何：borough 标注的事实数据，checksum 在代码中锁定
# PIPELINE_BOUNDARY_FILE=data/boundaries/nyc-borough-boundaries-water.geojson
# 缺失时是否直接失败，而不是退化成近似 bbox
# PIPELINE_REQUIRE_BOUNDARIES=false
```

> 边界几何文件是外部事实数据，不入库版本控制。首次导入前按
> `data/boundaries/README.md` 的命令下载；checksum 由
> `curate.DefaultBoundarySHA256` 锁定，不匹配会直接失败而不是静默改标整个语料。
>
> 边界缺失时**降级为近似 bbox** 并打警告。在 36,133 家已入库餐厅上实测，
> bbox 与真实边界有 **24.1%** 不一致（含把新泽西、长岛部分区域标成行政区的情况）。
> 定时任务应设 `PIPELINE_REQUIRE_BOUNDARIES=true`。
>
> 每次用哪一版几何都记在 `ingestion_batches.boundary_version`，可追溯。

## 附录 B：常用命令速查

```bash
# 0. 启动本地数据库
make pg-up

# 1. 建表 + 索引（可重复）
go run ./data-pipeline migrate
go run ./data-pipeline migrate --status      # 只看状态，不改动
go run ./data-pipeline migrate --drop        # ⚠️ 删表重来

# 2. 先在本地裁剪 review 语料（强烈建议，见 §3.2）
go run ./data-pipeline prefilter

# 3. 导入样本（先 dry-run 再实跑）
go run ./data-pipeline import --stage=meta --limit=5000 --dry-run
go run ./data-pipeline import --stage=meta --limit=5000
go run ./data-pipeline import --stage=review --limit=200000 \
  --data-dir=data/processed --review-file=review-filtered.json.gz

# 4. 全量导入
go run ./data-pipeline import --stage=meta   --batch=1000
go run ./data-pipeline import --stage=review --batch=2000 \
  --data-dir=data/processed --review-file=review-filtered.json.gz
go run ./data-pipeline import --stage=stats  --batch=500
go run ./data-pipeline import --stage=score  --demo-target=3000

# 5. 审计
go run ./data-pipeline report --last=5
go run ./data-pipeline report --batch-id=1

# 6. 质量门
go test ./... && go vet ./...
make test-postgres      # 契约测试（⚠️ 会清空 schema，务必用一次性测试库）
```

> ⚠️ **契约测试会 `Drop` schema**。`make test-postgres` 默认指向 `platepilot` 库时
> 会清掉已导入的全量语料。指向一次性库：
>
> ```bash
> docker exec platepilot-postgres psql -U platepilot -d platepilot -c "CREATE DATABASE platepilot_test;"
> PLATEPILOT_TEST_POSTGRES_DSN="postgres://platepilot:platepilot@localhost:55432/platepilot_test?sslmode=disable" \
>   go test ./shared/adapter/repository/postgres/
> ```

## 附录 C：索引清单（M1-03 / M1-11 汇总）

```text
restaurants
  restaurants_source_record_id_key   { source_record_id } unique          幂等 upsert 键
  restaurants_location_gist          { location } gist                    geography 半径筛选
  restaurants_location_gist_geom     { (location::geometry) } gist        配合 ST_IsValid / ST_X / ST_Y
  restaurants_name_trgm              { name } gin (gin_trgm_ops)          名称模糊
  restaurants_address_trgm           { address } gin (gin_trgm_ops)
                                        WHERE address IS NOT NULL         地址模糊
  restaurants_cuisine_gin            { cuisine_tags } gin                 = ANY(cuisine_tags)
  restaurants_price_level            { price_level } WHERE price_level IS NOT NULL
  restaurants_rating_source          { rating_source_avg DESC }
                                        WHERE rating_source_avg IS NOT NULL
  restaurants_active_borough         { borough } WHERE is_active_for_demo
  restaurants_active_score           { is_active_for_demo, knowledge_score DESC }
                                        WHERE is_active_for_demo           精选集与排序

reviews
  reviews_idempotency_key            { restaurant_id, text_hash, rating, reviewed_at } unique
                                                                        ⚠️ 幂等键，主键改为自增后由它承担
  reviews_restaurant_reviewed        { restaurant_id, reviewed_at DESC }  按餐厅取评论
  reviews_representative             { restaurant_id, is_representative, rating }
                                        WHERE is_representative            代表评论选择
  reviews_text_hash                  { text_hash }                        去重辅助（非唯一）

review_summaries
  review_summaries_pkey              { restaurant_id, topic }             复合主键

ingestion_batches        (M1-09)
  ingestion_batches_stage_started    { stage, started_at DESC }
  ingestion_batches_started_at       { started_at DESC }

ingestion_rejections     (M1-09)
  ingestion_rejections_batch          { batch_id }

knowledge_documents      (M1 建表，M2-06 写入)
  knowledge_documents_hnsw            { embedding } hnsw (vector_cosine_ops)
                                        WHERE is_active                    餐厅级召回
  knowledge_documents_hnsw_manhattan  同上 + AND borough = 'manhattan'     带地区过滤的召回
  knowledge_documents_hnsw_brooklyn   同上 + AND borough = 'brooklyn'
  knowledge_documents_hnsw_queens     同上 + AND borough = 'queens'
  knowledge_documents_hnsw_bronx      同上 + AND borough = 'bronx'
  knowledge_documents_hnsw_staten_island  同上 + AND borough = 'staten_island'
  knowledge_documents_scope_active    { retrieval_scope } WHERE is_active
  knowledge_documents_restaurant      { restaurant_id, retrieval_scope } WHERE is_active

  ⚠️ HNSW 是 ORDER BY 结构，加 WHERE 会退化为 Seq Scan（5 万行实测
     Rows Removed by Filter: 49759）。因此过滤字段 borough 必须反规范化到本表，
     并为每个取值建一个 partial HNSW。叠加过滤（borough + radius + cuisine）
     仍会退化为 filter-then-sort，当前语料规模下可接受。

boundaries              (M2 填充)
  boundaries_geom_gist                { geom } gist                       ST_Contains 反查
  boundaries_name_trgm                 { name } gin (gin_trgm_ops)         地名模糊
```

## 附录 D：参考文档

- 实施计划：`docs/platepilot-implementation-plan.md`
  - §6 M1 任务表
  - §7 关键路径
  - §9 Gate A：数据门
  - §10 每项任务的完成定义
- 技术 PRD：`docs/platepilot-technical-prd.md` v0.12
  - §2 当前数据基线（§2.2 Meta schema、§2.3 Review schema、§2.5 数据限制）
  - §4 数据架构与 Schema（§4.2 表总览、§4.3/§4.4/§4.5/§4.6、§4.11 模糊检索索引）
  - §5 写入链路（Step 1–9）
- 运维说明：`README.md`（启动命令、导入顺序、已知限制）
- M0 任务文档：`docs/platepilot-m0-task-document.md`（工程骨架、配置、日志、领域 DTO、Repository 接口）
