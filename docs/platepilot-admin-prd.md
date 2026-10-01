# PlatePilot Admin 控制台技术需求文档

> 版本：v0.2
> 日期：2026-10-01
> 依据：`docs/platepilot-technical-prd.md` v0.12、`docs/platepilot-implementation-plan.md` v0.7（§3.2 服务划分、§9 质量门、§10 完成定义）
> 定位：在 M4 之前，为 M2 写入产物与 M3 读取能力提供一套**只读**的可视化验证与 Debug 工具
> 交付形态：`chat-service` 新增只读管理端点 + 前端工程 `web/`（单工程，后续 M6 的 Agent 聊天界面复用同一工程）

## 0. 如何使用本文档

- 本文档是 **Admin 控制台的实施依据**（HOW to build），不是业务 PRD。
- 第 1~4 章说明背景、现状、范围与用户故事；第 5~7 章是架构决策与接口契约；第 8~9 章是前端页面与数据来源；第 10~13 章是设计要点、测试、范围外与风险。
- 第 14 章给出**任务拆解建议**，用于后续 `/prd-to-issues` 生成可独立领取的工单。
- 本文档中的 Go 类型、端点路径与表字段引用是**约定形状**，允许在不破坏领域边界与只读原则的前提下微调；一旦调整，必须同步更新本文件与对应实现。

### 0.1 为什么放在 M4 之前

M2 已经把 11,775 篇知识文档写入数据库，M3 已经能把它们召回并给出 trace，但**这两条链路目前只能通过 CLI 输出和 `go test` 观察**：

- M2 的写入结果只有 `data-pipeline report` 的文本输出，无法按餐厅、按文档、按向量状态逐条查看。
- M3 的召回质量只有测试夹具的断言，无法用真实查询交互式地看三通道分数。

在 M4 引入 Agent 之前先建立这套可视化，可以让 M4 的工具调用与检索结果有**可对照的基线**：
Agent 说"召回了 3 家餐厅、6 条佐证"时，能立刻在 Admin 里看到同样的数字与同样的 trace。
没有这个基线，M4 的调试会退化成"相信 Agent 的输出"。

## 1. 背景与目标

### 1.1 问题陈述

作为 PlatePilot 的唯一开发者，在推进 M4（Agent 运行时）之前，我需要确认两件事：

1. **M2 的数据写对了没有**：3,000 家餐厅的 profile 文档、8,775 条 evidence chunk、它们的向量、版本与 `is_active` 状态，是否与预期一致？导入批次有没有异常拒绝？
2. **M3 的检索读得对不对**：给定一个真实查询，三个召回通道各自返回了什么？融合后的分数是怎么构成的？降级发生在哪里？

目前这两件事都缺少**面向人的观察界面**：只能靠 `psql` 手写 SQL 和读测试输出。随着 M4 引入更多不确定性（模型选择、工具编排），这个缺口只会变大。

### 1.2 解决方案

在 `chat-service` 内新增一组**只读**管理端点（`/admin/v1/*`），并在已有的 `web/` 目录下建立前端工程：

- 浏览 `restaurants` / `reviews` / `review_summaries` / `knowledge_documents` / `ingestion_batches` 等已有数据表。
- 提供 **检索 Debug 工作台**：输入查询与过滤条件，复用现有 M3 检索服务，可视化展示候选、三通道分数、reasons 与降级警告。
- **不涉及 Agent**，不引入任何写操作，不改变现有 `/v1/*` 客户端契约。

### 1.2.1 前端定位：单工程，够用即可

前端只有一个工程 `web/`，M6 的 Agent 聊天界面也放在同一个工程里，不拆分、不建 workspace。

按此定位，前端遵循"**能用就行**"的标准，明确以下取舍：

- **不做**独立部署、独立鉴权、独立构建产物拆分。
- **不做**复杂状态管理（不引入 Redux/MobX），TanStack Query 的缓存足以覆盖列表页，页面内状态用 `useState`。
- **不做**组件库二次封装与设计系统，直接用 Ant Design 原生组件。
- **不做**完善的响应式适配，按桌面浏览器宽度设计即可。
- **要保证**的是：页面能加载真实数据、错误能显示出来、Debug 工作台能把 trace 展示清楚。
  这三条是本次需求的价值所在，其余一律从简。

> 这个取舍是刻意的：前端不是本次目标，它的作用是把数据库和检索链路**暴露给人看**。
> 任何超出"能看见"的投入都应该推迟到 M6。

### 1.3 目标（可验证）

| # | 目标 | 验证方式 |
|---|---|---|
| 1 | 能在浏览器里查看 5 张核心表的分页数据与统计 | 5 个页面均可加载真实数据，无 500 |
| 2 | 能定位单个餐厅的全部关联数据 | 输入 restaurant_id，一次看到 profile + 评论摘要 + 知识文档 + 向量状态 |
| 3 | 能查看 M2 每次导入/构建/向量化批次的审计报告 | 批次列表与详情页展示 `ingestion_batches` 全字段 + 拒绝原因分布 |
| 4 | 能对任意查询实时查看三通道召回与融合 trace | Debug 工作台展示 `Trace.Channels`、`Trace.Candidates`、`Warnings` |
| 5 | 全程无写操作，不破坏读写分离 | `InspectStore` 接口无写方法；代码审查 + 架构测试 |

### 1.4 非目标

- 不做 Agent 对话界面（M6-06）。
- 不做数据编辑、删除、重跑作业。
- 不做认证与多用户（见 §5.4）。
- 不做 M4 的工具调用回放（M6-04）。

## 2. 现状盘点

### 2.1 可直接复用的资产

| 资产 | 位置 | Admin 如何使用 |
|---|---|---|
| `retrieval.Service.Search` | `chat-service/internal/retrieval/service.go` | Debug 工作台直接调用，不重写检索 |
| `EvidenceService.Evidence` | `chat-service/internal/transport/httpapi/evidence_service.go` | Debug 工作台佐证面板复用 |
| `retrieval.Trace` / `EvidenceTrace` | `shared/domain/retrieval/retrieval.go` | 前端 trace 可视化的数据来源 |
| `port.RestaurantRepository` | `shared/port/repository.go` | 餐厅详情（`GetByID` → `RestaurantDetail`） |
| `port.KnowledgeRepository` | `shared/port/repository.go` | 文档召回（`FindEvidenceByRestaurant`） |
| Hertz Router + 4 个中间件 | `chat-service/internal/transport/httpapi/router.go` | `/admin/v1` 路由组挂在同一引擎 |
| `BindAndValidate` / `httperr.Write` / `WriteAndAbort` | `chat-service/internal/transport/httpapi/` | 统一请求校验与错误响应 |
| `errs` 错误码族 | `shared/domain/errs/errs.go` | 复用 `invalid_argument` / `not_found` / `internal` |

### 2.2 数据表现状

| 表 | 规模（实测） | Admin 用途 |
|---|---|---|
| `restaurants` | 36,133 行，3,000 家 `is_active_for_demo` | 餐厅浏览、Dashboard 统计 |
| `reviews` | 已导入样本 | 单餐厅评论查看 |
| `review_summaries` | M2 生成 | 单餐厅主题摘要查看 |
| `knowledge_documents` | 11,775 篇活跃（3,000 profile + 8,775 evidence） | 文档与向量状态浏览 |
| `ingestion_batches` | M1/M2 每批次一行 | 导入审计 |
| `ingestion_rejections` | append-only | 拒绝原因明细 |
| `boundaries` | 5 个 NYC borough | 行政区划查看（低优先级） |
| `schema_migrations` | 4 个迁移 | Dashboard 版本信息 |

### 2.3 必须知道的两个缺口

1. **`postgres.Client` 的 `pool` 字段未导出**（`shared/adapter/repository/postgres/client.go`）。
   Admin 的列表/统计查询无法在包外发起，**必须在 `postgres` 包内新增只读查询类型**
   （如 `InspectStore`），与现有读侧 repository 并列，共享同一个 `*Client`。

2. **现无任何列表/分页/统计查询**。M3 的读侧 SQL 全是"给定条件取 topK 候选"，
   形状是"窄而深"；Admin 需要的是"宽而浅"（计数、分页、跨表关联），
   **不复用 M3 的 SQL**，但可复用同一连接池与投影常量（如 `knowledgeColumns`）。

### 2.4 表字段的 PII 现状（重要）

- `reviews` 表**没有 `user_id` 列**——M1-07 已确认原始 `user_id` 不进入 curated review。
  Admin 可以安全展示评论正文与评分。
- `restaurants.source_record_id` 是 Google `gmap_id`（外部标识符），可展示但需在 UI 标注其来源。
- `knowledge_documents.metadata` 可能含 `source` 等键，展示前应确认不含个人信息（M2 builder 只写来源标识）。

## 3. 范围

### 3.1 范围内

**后端（chat-service）**

- 新增 `internal/inspect` 只读应用层。
- 新增 `/admin/v1/*` 路由组，**仅接受 loopback 来源**。
- 新增 `postgres.InspectStore`：列表、详情、统计查询，**无写方法**。
- 复用现有 `retrieval.Service` 与 `EvidenceService` 提供 Debug 端点。

**前端（web/）**

- 在现有 `web/` 目录建立单前端工程（Vite + React + TypeScript + Ant Design）。
- 本次交付 5 类页面：Dashboard、餐厅浏览、知识文档浏览、导入审计、检索 Debug 工作台。
- 为 M6 的聊天界面预留页面位置，但**本次不实现聊天功能**。

### 3.2 范围外

- **不做写入能力**：不提供导入、构建文档、向量化、修改、删除的任何触发入口。
  M2 的写入仍通过 `data-pipeline` CLI 完成（`migrate` / `import` / `build-documents` / `embed`）。
- **不做单通道 Debug 端点**：不做"仅结构化/仅关键词/仅向量"的独立端点，
  也不做临时覆盖融合权重。三通道信息通过现有 `Trace.Channels` 已可观察（见 §7.4 说明）。
- **不做 SQL / EXPLAIN 诊断**：不暴露执行计划。
- **不做认证**：见 §5.4。
- **不修改 `/v1/*` 契约**：现有客户端接口零变更。
- **不做数据导出**：不提供 CSV/JSON 下载（可作为后续增强）。

## 4. 用户故事

1. 作为开发者，我想要一个 Dashboard 看到各表行数与最近导入批次状态，以便在开始工作前确认数据底座健康。
2. 作为开发者，我想要按 borough / 菜系 / 价格 / 活跃状态分页浏览餐厅，以便抽查 M1 的导入与归一化结果。
3. 作为开发者，我想要点击一个餐厅看到它的全部关联数据（详情、评论、主题摘要、知识文档、向量状态），以便定位"这家店为什么没被召回"。
4. 作为开发者，我想要按 `retrieval_scope` / `doc_type` / `is_active` / 向量存在与否筛选知识文档，以便确认 M2 生成了预期数量的文档。
5. 作为开发者，我想要查看每条文档的 content_hash、version、embedding_model 与维度，以便验证 M2-05 的版本管理与 M2-02 的元数据正确性。
6. 作为开发者，我想要浏览 `ingestion_batches` 列表与单批次详情，看到 rows/accepted/written/deduped/rejected 与拒绝原因分布，以便判断某次导入是否异常。
7. 作为开发者，我想要查看某个批次的拒绝明细（line_no + reason），以便定位数据质量问题。
8. 作为开发者，我想要在 Debug 工作台输入查询与过滤条件，实时看到候选餐厅列表与它们的分数，以便验证 M3 的检索效果。
9. 作为开发者，我想要看到每个候选的三通道原始分、归一化分、权重与贡献，以便理解排序是怎么来的。
10. 作为开发者，我想要看到 trace 里的 warnings 与 channel notes（如"向量通道不可用：provider_unavailable"），以便识别降级。
11. 作为开发者，我想要看到召回各阶段的计数（candidate_pool / returned / top_k），以便确认过采样与裁剪按预期工作。
12. 作为开发者，我想要在 Debug 工作台对候选餐厅发起佐证召回并看到 evidence 列表与引用来源，以便验证 M3-04 的"不跨餐厅"约束。
13. 作为开发者，我想要一键从 Debug 结果跳转到对应餐厅的详情页，以便在检索异常时快速深入。
14. 作为开发者，我想要在 Dashboard 看到 schema 迁移版本与 embedding 模型/维度配置，以便确认环境与预期一致。

## 5. 架构决策

### 5.1 只读原则（不可破坏）

**`InspectStore` 不提供任何写方法**，与 `port.RestaurantRepository` / `port.KnowledgeRepository` 一致。
`chat-service` 依旧是纯读服务，`data-pipeline` 依旧是唯一写入方（实施计划 §3.1 第 9 条）。

这条原则要在三处同时成立：

- 接口层：`InspectStore` 方法集内无 `Create/Update/Delete/Upsert/Exec` 形状的方法。
- 路由层：`/admin/v1/*` 只注册 `GET` 与两个 `POST`（两者都是**检索**，语义只读）。
- 测试层：架构测试断言 `internal/inspect` 不 import 任何写侧类型（见 §11.3）。

### 5.2 端点挂载方式

Admin 路由挂在**现有 Hertz 引擎**上，作为一个独立路由组 `/admin/v1`，
而不是起第二个 HTTP 服务。理由：复用中间件链（request-id / logging / recovery / CORS）、
复用校验与错误响应，且 Admin 与 `/v1` 共享同一套依赖装配，无需重复构建检索服务。

路由组的注册受 `ADMIN_ENABLED` 开关控制，默认关闭；
未启用时不注册任何 `/admin/v1` 路由，请求命中 `NoRoute` 返回 404（与现有行为一致）。

### 5.3 访问控制：仅 loopback

由于决策为"无认证，仅本地"，安全性由**网络可达性**而非凭据保证：

- 新增中间件 `middleware.LoopbackOnly()`，校验 `c.RemoteAddr()` 解析出的 IP 是 loopback
  （`127.0.0.0/8` 与 `::1`）。非 loopback 返回 `403`。
- **不放行 `X-Forwarded-For`**：反向代理头可被伪造，一旦信任就等于把 Admin 暴露给任意来源。
  这是本地工具，不需要代理场景。
- 启动时若 `ADMIN_ENABLED=true` 而 `HTTP_ADDR` 绑定的不是 loopback（如 `:8080` 或 `0.0.0.0:8080`），
  记录 **warning** 并提示风险。不强制失败——开发者可能故意在局域网内使用，
  但必须让他们知道 loopback 中间件此时已是唯一防线。

> 注意：loopback 中间件是唯一控制点，因此它的实现必须有测试覆盖（见 §11.2）。

### 5.4 为什么不做认证

决策为本地单人调试工具。引入认证会带来会话管理、密码存储、凭证轮换等成本，
而这些成本在"M4 之前快速建立观察能力"的目标下不划算。
若未来要内网共享，升级路径明确：在 `LoopbackOnly` 旁并列一个静态 Token 中间件即可，
`InspectStore` 与前端无需改动（见 §13 风险）。

### 5.5 分页策略

- **餐厅、评论、文档列表一律 keyset 分页**（基于 `id` 或 `(restaurant_id, document_id)`），
  **不用 OFFSET**。`restaurants` 36k 行、`reviews` 规模更大，深 OFFSET 会随页数线性变慢。
- 响应携带 `next_cursor`（不透明字符串，编码最后一行的排序键）。
- 前端只提供"下一页"，不提供"跳到第 N 页"——与 keyset 语义一致。

### 5.6 统计查询策略

Dashboard 的行数**不使用 `SELECT count(*)`**（大表全扫）。改用：

- 精确计数：`restaurants`（36k，可接受）、`knowledge_documents`（11k，可接受）。
- 估算计数：`reviews` 用 `pg_class.reltuples`，并在 UI 标注"估算"。
- 分组计数：`knowledge_documents GROUP BY retrieval_scope, doc_type`（有 `scope_active` 部分索引支撑）。

### 5.7 CORS

Admin 前端开发服务器（默认 `http://localhost:5173`）需要在 `HTTP_CORS_ALLOW_ORIGINS` 中放行。
生产/本地运行时可先 `npm run build` 由 Hertz 静态托管，或继续用 Vite preview。
**默认配置不加通配符**，与其他来源一样走白名单。

### 5.8 向量不返回到浏览器

`knowledge_documents.embedding` 是 1024 维浮点数组，序列化后约 8KB/条，
对浏览器展示无意义。Admin **只返回向量元数据**：

- `has_embedding`（bool）
- `embedding_model`（text）
- `embedding_dimensions`（int）
- 可选：前 N 维的**降采样预览**（默认不返回，需要时通过 `?include_vector_preview=true` 开启，上限 16 维）。

## 6. 模块设计

### 6.1 后端目录结构（增量）

```text
chat-service/
├── internal/
│   ├── app/app.go                    # 改：装配 InspectStore 与 inspect.Service，传入 router
│   ├── config/config.go              # 改：AdminConfig（Enabled、Addr 校验提示）
│   ├── inspect/                      # 新：只读管理应用层（无 Hertz、无 pgx）
│   │   ├── service.go                # Service：把 InspectStore 的原始行整理成视图 DTO
│   │   ├── cursor.go                 # keyset 游标编解码
│   │   ├── service_test.go
│   │   └── cursor_test.go
│   └── transport/httpapi/
│       ├── router.go                 # 改：注册 /admin/v1 路由组（受开关控制）
│       ├── admin.go                  # 新：admin handler 集合 + 请求 DTO
│       ├── admin_test.go
│       └── middleware/
│           └── loopback.go           # 新：LoopbackOnly 中间件
├── shared/
│   ├── port/
│   │   └── inspect.go                # 新：InspectStore 端口（只读）
│   └── adapter/repository/postgres/
│       ├── inspect.go                # 新：InspectStore 的 postgres 实现
│       └── inspect_test.go
└── shared/domain/inspect/            # 新：Admin 视图 DTO（纯 stdlib）
    ├── inspect.go
    └── inspect_test.go
```

### 6.2 后端模块职责

**`port.InspectStore`（端口）**

只读查询接口。方法按"一次查询解决一个页面区块"划分，不做通用 query builder。

```go
type InspectStore interface {
    // Overview returns the dashboard counters.
    Overview(ctx context.Context) (inspect.Overview, error)

    // Restaurants lists restaurants with a keyset cursor, newest id first.
    Restaurants(ctx context.Context, q inspect.RestaurantQuery) (inspect.RestaurantPage, error)
    // RestaurantDetail returns one restaurant's full row for the detail page.
    RestaurantDetail(ctx context.Context, restaurantID int64) (inspect.RestaurantDetail, error)
    // Reviews lists one restaurant's reviews.
    Reviews(ctx context.Context, q inspect.ReviewQuery) (inspect.ReviewPage, error)
    // Summaries lists one restaurant's per-topic review summaries.
    Summaries(ctx context.Context, restaurantID int64) ([]inspect.ReviewSummary, error)

    // Documents lists knowledge documents across restaurants.
    Documents(ctx context.Context, q inspect.DocumentQuery) (inspect.DocumentPage, error)
    // DocumentDetail returns one document including embedding metadata.
    DocumentDetail(ctx context.Context, documentID int64) (inspect.DocumentDetail, error)
    // DocumentsByRestaurant lists one restaurant's documents.
    DocumentsByRestaurant(ctx context.Context, restaurantID int64) ([]inspect.DocumentSummary, error)

    // Batches lists ingestion batches, newest first.
    Batches(ctx context.Context, q inspect.BatchQuery) (inspect.BatchPage, error)
    // BatchDetail returns one batch with its rejection breakdown.
    BatchDetail(ctx context.Context, batchID int64) (inspect.BatchDetail, error)

    // Boundaries lists administrative boundaries.
    Boundaries(ctx context.Context) ([]inspect.Boundary, error)
}
```

> 接口**刻意不做成通用的 `Query(table, filter)`**。泛化查询会把"哪个页面读哪些列"
> 这个产品决策藏进运行时字符串，使 schema 变更无法被编译器发现。
> 每个方法对应一个明确页面区块，schema 变化时编译期即失败。

**`inspect.Service`（应用层）**

- 对 handler 暴露与 `InspectStore` 同形的只读方法，负责：游标编解码、默认分页大小夹紧、DTO 组装。
- 不持有状态，可跨请求共享。
- 不 import Hertz、pgx；只 import `shared/domain/inspect` 与 `shared/port`。

**`httpapi/admin.go`（传输层）**

- 9 个 handler 对应 §7 的端点表。
- 请求参数校验复用 `BindAndValidate`；错误统一走 `httperr.Write`。
- Debug 端点把请求转成 `retrieval.Request` / `EvidenceQuery`，调用**现有** service，不改其行为。

**`middleware/loopback.go`**

- `LoopbackOnly()` 返回 `app.HandlerFunc`，非法来源 `c.AbortWithStatus(403)` 并写统一错误体。

### 6.3 前端工程结构（在现有 web/ 下建立）

单个工程，不做目录切分的差异化设计。Admin 页面与未来 M6 的聊天页面并列在 `pages/` 下。

```text
web/
├── index.html
├── package.json
├── tsconfig.json
├── vite.config.ts            # dev proxy: /admin -> http://127.0.0.1:8081
├── .env.example              # VITE_API_BASE
├── README.md                 # 启动方式、开关、与 chat-service 的关系
└── src/
    ├── main.tsx              # AntD ConfigProvider + Router + QueryClientProvider
    ├── App.tsx               # 布局：侧边导航 + 内容区
    ├── api/
    │   ├── client.ts         # fetch 封装、错误解包、统一 base
    │   └── types.ts          # 与后端 DTO 对齐的 TypeScript 类型
    ├── components/
    │   ├── KeySetTable.tsx   # 基于 AntD Table + next_cursor 的通用分页表
    │   ├── JsonBlock.tsx     # 折叠展示 jsonb（attributes / metadata）
    │   └── TracePanel.tsx    # 三通道分数 + reasons + warnings 可视化
    └── pages/
        ├── Dashboard.tsx
        ├── Restaurants.tsx
        ├── RestaurantDetail.tsx
        ├── Documents.tsx
        ├── DocumentDetail.tsx
        ├── Ingestion.tsx
        ├── IngestionDetail.tsx
        └── RetrievalDebug.tsx
        # M6 的 Chat.tsx 后续直接加在这一层，复用同一 api/ 与 components/
```

**技术栈**

- 构建：Vite
- 框架：React 18 + TypeScript
- 组件库：Ant Design 5（表格、筛选、分页、描述列表、Tag、Collapse 开箱即用）
- 数据获取：TanStack Query（缓存、重试、加载/错误态统一）
- 路由：React Router

**为什么是单工程**

`web/` 目录已有的 `README.md` 说明是给 M6 聊天界面预留的占位符。
本次直接在 `web/` 建立工程并更新该 README 即可，**不建 `web/admin/` 子目录**：

- Admin 与未来聊天界面共用同一套 `api/`、`components/`、构建与依赖，拆开只会带来重复配置。
- 两者都是本地演示工具，没有独立部署、独立发布的诉求（见 §1.2.1）。
- 单工程意味着一次 `npm install`、一条 `npm run dev`，符合"能用就行"。

> 需要同步更新 `web/README.md`：它当前写着 "Reserved for the frontend project.
> Nothing is implemented yet."，并列出 `GET /v1/restaurants/search` 等**尚未存在**的端点。
> 本次要把它改成真实的启动说明与端点清单（Admin 用 `/admin/v1/*`，聊天用 `/v1/*`）。

## 7. 接口契约

### 7.1 端点总览

全部挂在 `/admin/v1`，受 `ADMIN_ENABLED` 与 `LoopbackOnly` 控制。

| 方法 | 路径 | 用途 | 页面 |
|---|---|---|---|
| GET | `/admin/v1/overview` | 表行数、文档分布、最近批次、schema 版本 | Dashboard |
| GET | `/admin/v1/restaurants` | 餐厅分页列表（可筛选） | 餐厅浏览 |
| GET | `/admin/v1/restaurants/:id` | 餐厅完整详情 | 餐厅详情 |
| GET | `/admin/v1/restaurants/:id/reviews` | 餐厅评论分页 | 餐厅详情 |
| GET | `/admin/v1/restaurants/:id/summaries` | 餐厅主题摘要 | 餐厅详情 |
| GET | `/admin/v1/restaurants/:id/documents` | 餐厅知识文档列表 | 餐厅详情 |
| GET | `/admin/v1/documents` | 文档全局分页列表（可筛选） | 知识文档 |
| GET | `/admin/v1/documents/:id` | 单文档详情（含向量元数据） | 知识文档 |
| GET | `/admin/v1/batches` | 批次分页列表 | 导入审计 |
| GET | `/admin/v1/batches/:id` | 批次详情 + 拒绝分布 | 导入审计 |
| GET | `/admin/v1/boundaries` | 行政区划列表 | Dashboard（次要） |
| POST | `/admin/v1/debug/search` | **复用** `retrieval.Service.Search` | Debug 工作台 |
| POST | `/admin/v1/debug/evidence` | **复用** `EvidenceService.Evidence` | Debug 工作台 |

### 7.2 查询参数与响应形状（关键）

**`GET /admin/v1/restaurants`**

查询参数：

```text
cursor    string   不透明游标（可选）
limit     int      默认 25，上限 100
borough   string   可选，5 个白名单值
cuisine   string   可选，单菜系精确匹配
active    bool     可选，是否仅 is_active_for_demo
q         string   可选，名称子串（ILIKE，走 trgm 索引）
```

响应：

```json
{
  "items": [
    {
      "restaurant_id": 1234,
      "name": "Joe's Pizza",
      "address": "7 Carmine St",
      "borough": "manhattan",
      "cuisines": ["pizza", "italian"],
      "price_level": 1,
      "rating_computed_avg": 4.4,
      "rating_count": 3120,
      "is_active_for_demo": true,
      "knowledge_score": 5.5,
      "observed_at": "2021-09-01T00:00:00Z"
    }
  ],
  "next_cursor": "eyJpZCI6MTIzNH0",
  "total_estimate": 3000
}
```

**`GET /admin/v1/restaurants/:id`**

在 `search.RestaurantDetail` 的基础上补齐 Admin 需要的字段：
`attributes`（jsonb）、`hours`（jsonb）、全部 review 计数列、`snapshot_status`、`source_url`。

**`GET /admin/v1/documents`**

查询参数：

```text
cursor           string
limit            int      默认 25，上限 100
restaurant_id    int      可选
scope            string   可选：restaurant | evidence
doc_type         string   可选
is_active        bool     可选（默认不过滤，与召回路径的默认相反）
has_embedding    bool     可选
```

> `is_active` 默认**不过滤**：Admin 的目的之一是查看被 supersede 的旧版本，
> 与召回路径"只读 active"的默认相反。这一点必须在 UI 上明确标注，避免误读。

响应项包含 `document_id / restaurant_id / retrieval_scope / doc_type / title /
content_hash / version / is_active / has_embedding / embedding_model /
embedding_dimensions / snapshot_at`。**不含 `content`**——列表页不返回正文，
正文在详情页按需加载（8,775 条文档的 content 可能很长）。

**`GET /admin/v1/documents/:id`**

在列表字段基础上增加 `content`（全文）、`metadata`（jsonb）、`source_record_ids`，
以及向量元数据。默认不返回向量本体（见 §5.8）。

**`GET /admin/v1/batches`**

查询参数：`cursor`、`limit`、`stage`。

响应项对应 `review.BatchReport` 的持久化字段：
`batch_id / stage / status / started_at / finished_at / duration_ms /
rows_read / accepted / written / deduped / filtered / rejected / unmatched`。

**`GET /admin/v1/batches/:id`**

在列表字段基础上增加：`missing_fields`（jsonb）、`error_code`、
`documents_built / documents_embedded / documents_rejected`（可空，M2 才有）、
`embedding_model / embedding_dimensions`（可空）、`reject_reasons`（jsonb map）、
以及 `rejections[]`（`line_no / reason / stage / source_record_id`，上限 200 条并携带 `truncated` 标记）。

**`POST /admin/v1/debug/search`**

请求体**与现有 `/v1/restaurants/search` 同形**（`query` / `text` / `filter` / `top_k`），
响应体同样为 `{candidates, trace}`。前端在此基础上额外渲染 trace 展开视图。

**`POST /admin/v1/debug/evidence`**

请求体与现有 `/v1/restaurants/evidence` 同形。

### 7.3 为什么 Debug 端点与 `/v1` 请求同形而单独开路由

- 单独开路由：`/admin/v1` 受 loopback 与开关保护，`/v1` 是面向客户端的稳定契约。
  把 Debug 能力挂在 `/v1` 上会让"客户端契约"与"内部调试工具"耦合，
  未来想给 `/v1` 加限流或鉴权时会互相牵制。
- 请求同形：Debug 的价值是"用与真实链路完全相同的输入观察真实链路"。
  如果 Debug 端点接受不同的请求形状，它验证的就不再是线上路径。

### 7.4 关于"不做单通道 Debug 端点"的说明

决策为"现有 API + trace 可视化"。现有 `retrieval.Trace` 已经包含：

- `Channels[]`：每个通道的 `ran / weight / results / note`——**能看出某通道是否运行、返回多少、为何跳过**。
- `Candidates[].Channels[]`：每个候选的 `raw / normalized / weight / contribution / reason`——**能看出排序构成**。
- `Warnings[]`：降级原因。

因此"仅结构化/仅关键词/仅向量"的效果，用 `RETRIEVAL_ENABLE_*` 开关 + 对比两次 Debug 结果即可观察，
无需新增端点。若后续发现对比操作成本过高，再作为增强引入（见 §14 的可选任务）。

### 7.5 错误码

复用现有 `errs` 码族，不新增：

| 场景 | 码 |
|---|---|
| 游标格式非法 | `invalid_argument` |
| `limit` 超上限 | 静默夹紧到 100（不报错，与 `topK` 处理一致） |
| 餐厅/文档/批次不存在 | `not_found` |
| 非 loopback 访问 | `unauthorized` + HTTP 403 |
| Admin 未启用 | 路由不存在 → `not_found` |
| 数据库错误 | `internal` |

## 8. 页面设计

### 8.1 Dashboard

数据来源：`GET /admin/v1/overview` + `GET /admin/v1/boundaries`。

区块：

1. **表规模卡片**：restaurants 总数 / active 数、reviews 总数（标注估算）、
   knowledge_documents 活跃数、ingestion_batches 总数。
2. **文档分布**：按 `retrieval_scope × doc_type` 的分组计数（应为 3,000 profile + 8,775 evidence）。
3. **向量健康**：`has_embedding=false` 的活跃文档数（**必须为 0**，否则违反
   `knowledge_documents_embedded_check` 约束，说明数据被外部改过）。
4. **最近批次**：最近 5 个 `ingestion_batches` 的状态徽标 + 耗时。
5. **环境信息**：schema 迁移版本列表、当前 embedding model 与维度（来自服务配置，非数据库）。

### 8.2 餐厅浏览

筛选栏（borough 下拉 / 菜系输入 / 价格下拉 / active 开关 / 名称搜索）+ keyset 分页表格。
列：id、名称、borough、菜系标签、价格、评分（含评论数）、active、快照时间。
行点击进入详情。

### 8.3 餐厅详情

四个区块（并行请求）：

1. **基础信息**：描述列表 + `attributes`/`hours` 的折叠 JSON。
2. **知识文档**：该餐厅的全部文档（scope / doc_type / version / is_active / 向量状态），
   可点击进入文档详情。
3. **评论**：分页表格（评分 / 时间 / 正文摘要 / 代表性标记 / 主题标签）。
4. **主题摘要**：`review_summaries` 表格（topic / sentiment / positive_ratio / evidence_count）。

### 8.4 知识文档浏览

筛选（scope / doc_type / is_active / has_embedding / restaurant_id）+ 分页表格。
表格列含 version、content_hash 前缀、向量状态徽标。
突出显示 `is_active=false`（灰显），避免与活跃文档混淆。

### 8.5 文档详情

正文全文（等宽字体、可复制）、metadata 折叠 JSON、source_record_ids 列表、
向量元数据（model / dimensions / 是否分块）。

### 8.6 导入审计

批次列表（stage / status 徽标 / 时间 / 关键计数）。点击进入详情：
全部计数字段、missing_fields、reject_reasons 的条形分布、拒绝明细表。

### 8.7 检索 Debug 工作台

布局：左侧查询表单，右侧结果。

查询表单：

- `query`（自然语言）与 `text`（名称/地址）两个独立输入，附说明其区别。
- 过滤器：borough / 菜系 / 价格 / 最低评分 / active。
- `top_k` 数字输入。
- 提交按钮 + "使用当前服务配置"提示（不可在界面上改权重，见 §7.4）。

结果区：

1. **通道摘要表**：`ChannelSummary` 逐行展示 ran / weight / results / note。
2. **候选列表**：按最终分数排序，每行展示名称、总分、评分、reasons 标签。
3. **单个候选展开**：`CandidateScore.Channels` 的 raw / normalized / weight / contribution，
   以及每条 reason。
4. **Warnings 告警条**：trace 中的降级信息用 AntD Alert 高亮。
5. **佐证面板**：点击候选 → 调用 `/admin/v1/debug/evidence` → 展示 evidence 列表与来源。
6. **跳转入口**：候选行提供"查看餐厅详情"链接。

## 9. 数据来源映射

| 页面区块 | 数据表 | 查询要点 |
|---|---|---|
| Dashboard 表规模 | `restaurants` / `reviews` / `knowledge_documents` / `ingestion_batches` | 精确计数 + `reltuples` 估算 |
| Dashboard 文档分布 | `knowledge_documents` | `GROUP BY retrieval_scope, doc_type WHERE is_active` |
| Dashboard 向量健康 | `knowledge_documents` | `WHERE is_active AND embedding IS NULL`（应为 0） |
| Dashboard 迁移版本 | `schema_migrations` | 全量读取 |
| 餐厅列表 | `restaurants` | keyset by `id`；`q` 走 `name` trgm；`active` 走 `restaurants_active_score` |
| 餐厅详情 | `restaurants` | 主键点查，投影含 jsonb 列 |
| 餐厅评论 | `reviews` | keyset by `(restaurant_id, reviewed_at DESC)`，走 `reviews_restaurant_reviewed` |
| 餐厅摘要 | `review_summaries` | 主键前缀查 `restaurant_id` |
| 文档列表 | `knowledge_documents` | keyset by `document_id`；筛选走 `scope_active` / `restaurant` 部分索引 |
| 批次列表 | `ingestion_batches` | keyset by `started_at DESC, id`，走 `ingestion_batches_started_at` |
| 批次详情 | `ingestion_batches` + `ingestion_rejections` | 两次查询，rejections 走 `ingestion_rejections_batch` |

## 10. 关键设计要点

### 10.1 只读的强制手段

- `InspectStore` 方法集内**没有**任何写方法，且实现文件不 import 写侧 store。
- `/admin/v1` 路由表中只出现只读 handler。
- 架构测试（§11.3）断言 `shared/port/inspect.go` 的类型集合不含写语义方法名。

### 10.2 不返回大字段

- 文档列表不返回 `content`（正文可能数千字符 × 25 行）。
- 向量不回传（§5.8）。
- `attributes_raw` / `relative_results` 这类调试用的大 jsonb **默认不返回**，
  仅 `attributes`（清洗后）返回；`attributes_raw` 通过显式参数按需读取。

### 10.3 游标必须不透明

游标编码排序键并做 base64，前端不得解析。这样底层排序键变化
（例如从 `id` 改为 `(borough, id)`）不会成为前端的破坏性变更。

### 10.4 计数与列表的一致性

Dashboard 的计数与列表总数可能因并发写入而不完全一致（`data-pipeline` 可能在跑）。
UI 上以"截至本次查询"表述，不承诺强一致。这对本地调试工具是合理取舍。

### 10.5 时间统一 UTC

所有时间字段在 API 层以 RFC3339 UTC 返回，
前端按浏览器时区渲染并在表头标注时区。避免 debug 时把时区差误判为数据错误。

### 10.6 前端错误态

TanStack Query 统一处理：加载中骨架屏、错误用 AntD `Alert` 展示 `error.code` 与 `message`、
空结果用 `Empty` 组件并区分"无数据"与"筛选后为空"。

## 11. 测试策略

### 11.1 单元测试（后端）

- `inspect/cursor_test.go`：游标编解码往返、非法输入拒绝、空游标语义。
- `inspect/service_test.go`：limit 夹紧（0 → 默认，>100 → 100）、DTO 组装、
  时间归一化、`is_active` 默认不过滤的行为。
- `httpapi/admin_test.go`：9 个 handler 的参数绑定、错误码、空结果返回 `[]` 而非 `null`
  （沿用现有 handler 的约定）。

### 11.2 中间件测试

- `middleware/loopback_test.go`：`127.0.0.1` 放行、`::1` 放行、
  公网 IP 拒绝、伪造 `X-Forwarded-For` 不影响判定、`RemoteAddr` 无端口时的行为。

### 11.3 架构测试

- 断言 `internal/inspect` 不 import `shared/adapter/repository/postgres` 的写侧类型。
- 断言 `shared/domain/inspect` 只依赖标准库（与现有
  `TestDomainLayerHasNoFrameworkOrVendorDependencies` 同族）。
- 断言 `shared/port/inspect.go` 中 `InspectStore` 接口无写方法。

### 11.4 集成测试（需真实数据库）

- `postgres/inspect_test.go`，以 `PLATEPILOT_REQUIRE_DB=1` 运行：
  - 列表查询的 keyset 分页不重不漏（翻完全部页，验证 id 集合等于全量集合）。
  - `documents` 按 `retrieval_scope=restaurant` 过滤返回 3,000 行量级。
  - `WHERE is_active AND embedding IS NULL` 返回 0 行（向量健康断言）。
  - 批次详情能取到 `reject_reasons` 的 map。
  - `EXPLAIN` 断言列表查询命中预期索引，无全表 Seq Scan（对齐 M3 的索引验证惯例）。

### 11.5 前端测试

- Vitest + React Testing Library，覆盖：
  - `KeySetTable` 的翻页与 `next_cursor` 终止。
  - `TracePanel` 在 channel 被跳过（`ran=false` + note）时的渲染。
  - API client 对错误响应的解包。

### 11.6 手工验收

- 从 Dashboard 进入餐厅详情，确认文档数与该餐厅的 profile 文档（应为 1 篇）一致。
- 在 Debug 工作台查询 `manhattan` + `italian` + "适合约会"，
  确认候选 satisfy 硬条件，且 trace 中 vector channel 有结果。
- 关闭 Ollama 后重试 Debug 查询，确认返回结果仍在、`Warnings` 出现降级说明、
  vector channel 的 `ran=false` 且有 note。

## 12. 配置项

```bash
# Admin 控制台（chat-service）。默认关闭。
# ADMIN_ENABLED=true
# ADMIN 路由挂在主 HTTP 引擎上，由 LoopbackOnly 中间件保护。
# 若 HTTP_ADDR 绑定的不是 loopback，启动时会记录 warning。
# ADMIN_DEFAULT_PAGE_SIZE=25
# ADMIN_MAX_PAGE_SIZE=100
# 批次详情里返回的拒绝明细条数上限。
# ADMIN_MAX_REJECTIONS=200
```

前端 `web/.env.example`：

```bash
# Admin 前端访问的后端地址。开发时建议走 Vite proxy，保持同源。
VITE_API_BASE=http://127.0.0.1:8081
```

## 13. 风险与控制

| 风险 | 触发点 | 控制措施 |
|---|---|---|
| Admin 凭据缺失导致被误暴露 | `HTTP_ADDR=0.0.0.0` 且管理员未注意 | 启动时记录 warning；loopback 中间件为默认防线；文档明示 |
| 只读原则被后续破坏 | 有人为"方便"加一个删除按钮 | 端口层无写方法 + 架构测试 + 本文档 §5.1 |
| 大表查询拖慢开发机 | `reviews` 深分页 | keyset 分页 + 禁止 OFFSET + 计数走估算 |
| `is_active=false` 被误读为数据丢失 | 文档页默认展示非活跃 | UI 灰显 + 表头说明 + 默认排序把 active 置前 |
| Debug 端点被当成线上接口依赖 | 前端或脚本硬编码 `/admin/v1` | 路由组与 `/v1` 分离 + 开关默认关闭 |
| 向量元数据与实际不符 | 外部直接改库 | Dashboard 的"向量健康"卡片显式断言 0 异常 |
| 前端工程被过度投入 | "顺手做个好看点儿的" | §1.2.1 已明确取舍：只保证"能加载、能报错、trace 看得清"，其余推迟到 M6 |
| 与 M4 并行时改到同一路由文件 | 同时修改 `router.go` | Admin 路由注册集中在单一函数 `registerAdminRoutes`，减少冲突面 |

## 14. 任务拆解建议（供 `prd-to-issues`）

> 下列任务按 M3 文档的粒度与格式给出（目标 / 交付物 / 依赖 / 工作量 / 验收）。
> 工作量级：`S` 半天以内，`M` 约 1–2 天，`L` 约 3–5 天。

### 后端（chat-service）

| ID | 任务 | 交付物 | 依赖 | 工作量 | 验收摘要 |
|---|---|---|---|---|---|
| ADM-01 | Admin 视图 DTO | `shared/domain/inspect/inspect.go` | 无 | S | 纯 stdlib，无框架依赖 |
| ADM-02 | `InspectStore` 端口 | `shared/port/inspect.go` | ADM-01 | S | 接口无写方法；架构测试通过 |
| ADM-03 | postgres 只读查询实现 | `postgres/inspect.go` | ADM-02 | L | 全部方法返回真实数据；keyset 分页不重不漏 |
| ADM-04 | Admin 应用层与游标 | `inspect/service.go`、`inspect/cursor.go` | ADM-02 | M | limit 夹紧、游标往返、DTO 组装 |
| ADM-05 | loopback 中间件 | `middleware/loopback.go` | 无 | S | 公网 IP 拒绝；伪造 XFF 无效 |
| ADM-06 | Admin 只读路由与 handler | `httpapi/admin.go`、`router.go`（改） | ADM-04, ADM-05 | M | 11 个只读端点可用，空结果为 `[]` |
| ADM-07 | Debug 端点（复用检索） | `httpapi/admin.go`（续） | ADM-06 | S | 与 `/v1` 同形输入，返回相同 trace |
| ADM-08 | Admin 配置与装配 | `internal/config`、`internal/app`（改） | ADM-06 | S | `ADMIN_ENABLED` 生效；非 loopback 绑定时 warning |
| ADM-09 | 后端测试 | 各 `_test.go` | ADM-03~08 | M | 单元 + 集成 + 架构测试全绿 |

### 前端（web/）

| ID | 任务 | 交付物 | 依赖 | 工作量 | 验收摘要 |
|---|---|---|---|---|---|
| ADM-10 | 工程脚手架 | 在 `web/` 建 Vite + React + TS + AntD + TanStack Query + Router；更新 `web/README.md` | 无 | M | `npm run dev` 可启动，空布局可访问 |
| ADM-11 | API client 与类型 | `web/src/api/client.ts`、`types.ts` | ADM-10 | S | 与后端 DTO 对齐；错误统一解包 |
| ADM-12 | 通用组件 | `KeySetTable`、`JsonBlock`、`TracePanel` | ADM-11 | M | 三者有 Vitest 用例 |
| ADM-13 | Dashboard 与餐厅浏览 | `pages/Dashboard.tsx`、`Restaurants.tsx` | ADM-12 | M | 展示真实统计与分页列表 |
| ADM-14 | 餐厅详情与文档页 | `RestaurantDetail.tsx`、`Documents.tsx`、`DocumentDetail.tsx` | ADM-12 | M | 单餐厅关联数据齐全；文档筛选生效 |
| ADM-15 | 导入审计页 | `Ingestion.tsx`、`IngestionDetail.tsx` | ADM-12 | M | 批次与拒绝分布可查看 |
| ADM-16 | 检索 Debug 工作台 | `RetrievalDebug.tsx` | ADM-12 | L | 三通道分数、reasons、warnings、佐证面板可用 |
| ADM-17 | CORS 与联调 | `web/.env.example`、`HTTP_CORS_ALLOW_ORIGINS` | ADM-13~16, ADM-08 | S | 浏览器跨端口访问无 CORS 错误 |

### 依赖图

```text
ADM-01 ─> ADM-02 ─┬─> ADM-03 ─┐
                   └─> ADM-04 ─┼─> ADM-06 ─┬─> ADM-07
                               │            └─> ADM-08
ADM-05 ────────────────────────┘
ADM-03, ADM-04, ADM-06~08 ─> ADM-09

ADM-10 ─> ADM-11 ─> ADM-12 ─┬─> ADM-13
                             ├─> ADM-14
                             ├─> ADM-15
                             └─> ADM-16
ADM-13~16 + ADM-08 ─> ADM-17
```

### 推荐执行顺序

1. **ADM-01 → ADM-02 → ADM-03**：先把只读端口与查询打通（可在 `psql` 验证）。
2. **ADM-04 → ADM-05 → ADM-06 → ADM-08**：端点可用，可用 `curl` 验收。
3. **ADM-07**：Debug 端点（复用现有检索服务，成本最低、价值最高）。
4. **ADM-10 → ADM-11 → ADM-12**：前端地基。
5. **ADM-16（Debug 工作台）优先于其他页面**：它是本次需求的核心价值，
   且依赖的端点（ADM-07）已就绪。
6. **ADM-13/14/15**：其余浏览页。
7. **ADM-17 + ADM-09**：联调与测试收口。

> **最小可用切片**：`ADM-01 → ADM-02 → ADM-07 → ADM-10 → ADM-11 → ADM-16`。
> 这一条链路完成后即可在浏览器里对真实数据做检索 Debug，
> 而其余浏览页可以随后补齐。

### 可选增强（不在本次范围）

| 任务 | 说明 |
|---|---|
| ADM-18 | 单通道 Debug 端点（仅结构化 / 仅关键词 / 仅向量），用于替代"改配置 + 跑两次"的对比方式 |
| ADM-19 | `EXPLAIN` 执行计划查看端点 |
| ADM-20 | 静态 Token 认证中间件（内网共享时启用） |
| ADM-21 | 结果导出（CSV / JSON） |

## 15. 完成定义（DoD）

沿用实施计划 §10，Admin 额外要求：

- `InspectStore` 无写方法，且架构测试断言成立。
- `/admin/v1` 在 `ADMIN_ENABLED=false` 时完全不注册。
- 所有列表端点返回 keyset 游标，不使用 OFFSET。
- 空结果返回 `[]`，不返回 `null`；不存在的资源返回 `not_found`。
- 列表端点不返回大字段（`content`、向量、`attributes_raw`）。
- 前端在无数据、加载中、错误三种状态均有明确展示。
- 文档（本文档 + `web/README.md`）说明启动方式、`ADMIN_ENABLED` 开关、与 chat-service 的关系，
  以及 M6 聊天界面将复用同一前端工程。
- 通过 `go test ./...`、`go vet ./...`、`golangci-lint`；前端通过 `npm run build` 与 `npm test`。
