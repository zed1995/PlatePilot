# PlatePilot Admin 控制台实施计划

> 依据：`docs/platepilot-admin-prd.md` v0.2
> 范围：chat-service 只读 `/admin/v1` 端点 + `web/` 单前端工程（8 个页面）

## 一、代码库调研结论

### 现有架构

- `chat-service` 是纯读服务，Hertz 引擎，全局中间件链：request-id → logging → recovery → CORS（[router.go](file:///Users/zed/Codes/PlatePilot/chat-service/internal/transport/httpapi/router.go)）。
- 装配集中在 [app.go](file:///Users/zed/Codes/PlatePilot/chat-service/internal/app/app.go)：`Connect` 连 postgres（同一 pool 挂 `RestaurantSearchRepository` + `KnowledgeReadRepository`），`New` 装配 `retrieval.Service` 和 router。
- 分层规则：`shared/domain/*` 纯 DTO；`shared/port/*` 端口；`shared/adapter/repository/postgres/*` 适配。
- `postgres.Client.pool` 未导出（虽有 `Pool()` 方法），且 pgx 类型不得出包——InspectStore 必须建在 postgres 包内。

### 可直接复用

- `retrieval.Service.Search(ctx, retrieval.Request) → SearchResult{Candidates, Trace}`；Trace 含 `Channels / Candidates / Warnings / CandidatePool / Returned / TopK`。
- `httpapi.NewEvidenceService(svc)` → `EvidenceService.Evidence(ctx, EvidenceQuery)`。
- Handler 工具：`BindAndValidate`、`WriteAndAbort`、`httperr.Write`、`pathID`。
- 扫描基础：`batchRow`/`batchColumns`、`reviewRow`/`reviewColumns`、`restaurantRow`/`restaurantColumns`、`parseVectorLiteral`、`orEmptyObject/Array`、`operationError`、`withTimeout`。
- 配置 Loader 提供 `Bool/Int/Duration/List` 等方法（[config.go](file:///Users/zed/Codes/PlatePilot/shared/config/config.go)）。

### 必须遵守的硬约束

1. **领域层禁词测试**（[architecture_test.go](file:///Users/zed/Codes/PlatePilot/shared/domain/architecture_test.go#L128-L161)）：`shared/domain/` 下任何 `.go` 文件（含注释）不得出现 `mongo/atlas/postgres/postgis/pgvector/sql`。新包 `shared/domain/inspect/` 的注释必须规避这些词。
2. **领域层零第三方依赖**：新 DTO 包只能 import stdlib 与 `shared/domain/*`（可用 `encoding/json`）。
3. **只读**：InspectStore 无写方法；`/admin/v1` 只注册 GET + 两个 POST（检索语义）；架构测试断言。
4. **keyset 分页，无 OFFSET**；空列表返回 `[]` 而非 `null`；资源不存在返回 `not_found`。
5. **错误码注意**：`CodeUnauthorized` 默认映射 HTTP 401，而 PRD §7.5 要求非 loopback 返回 **403**——loopback 中间件需直接写 403 状态码 + `unauthorized` 错误体。
6. 集成测试跳过约定：`PLATEPILOT_REQUIRE_DB` 未设置时 skip。
7. 前端 `web/` 目录不得出现 Go 代码。

## 二、文件与模块清单

### 新增（后端）

- `shared/domain/inspect/inspect.go`：Admin 视图 DTO（Overview / 各 Query / Page / 列表项 / Detail 等）。
- `shared/port/inspect.go`：`InspectStore` 只读接口（11 个方法，形状按 PRD §6.2）。
- `shared/port/inspect_architecture_test.go`：AST 断言 InspectStore 无写语义方法名（Create/Update/Delete/Upsert/Insert/Exec/Write/Put 前缀）。
- `shared/adapter/repository/postgres/inspect.go`：`InspectStore` 实现（列表/详情/统计 SQL、投影常量、扫描）。
- `shared/adapter/repository/postgres/inspect_test.go`：DB 集成测试（分页不重不漏、向量健康=0、scope 过滤、索引命中）。
- `chat-service/internal/inspect/service.go`：应用层 Service（DTO 组装、注入环境信息）。
- `chat-service/internal/inspect/cursor.go` / `cursor_test.go`：base64(JSON) 不透明游标，三种键（id / reviewed_at+id / started_at+id）。
- `chat-service/internal/inspect/service_test.go`：limit 夹紧、游标错误、环境注入、默认不过滤。
- `chat-service/internal/inspect/architecture_test.go`：断言本包不 import postgres 适配包。
- `chat-service/internal/transport/httpapi/admin.go`：`AdminService` 接口、GET 参数解析、11 个 handler。
- `chat-service/internal/transport/httpapi/admin_test.go`：参数绑定/错误码/空结果为 `[]`。
- `chat-service/internal/transport/httpapi/middleware/loopback.go` / `loopback_test.go`：LoopbackOnly。

### 修改（后端）

- `chat-service/internal/config/config.go`：新增 `Admin AdminConfig`（Enabled / DefaultPageSize=25 / MaxPageSize=100 / MaxRejections=200），读取 `ADMIN_*` 环境变量。
- `chat-service/internal/app/app.go`：`Deps` 增加 `Inspect port.InspectStore`；`Connect` 中 `postgres.NewInspectStore(client)`；`New` 装配 inspect 应用服务（embedding model/dim 作为环境信息注入）；Admin 启用时向 router 传 Admin 配置；HTTP_ADDR 非 loopback 绑定时打 warning。
- `chat-service/internal/transport/httpapi/router.go`：`Config` 增加 `Admin *AdminRouteConfig`；新增 `registerAdminRoutes`（单一函数集中注册，受开关控制；默认不注册 → NoRoute 404）。

### 新增（前端，均在 `web/` 下）

- 工程配置：`package.json`、`tsconfig.json`、`tsconfig.node.json`、`vite.config.ts`（dev proxy：`/admin` 与 `/v1` → `http://127.0.0.1:8080`）、`index.html`、`.env.example`、`.gitignore`、`vitest.setup.ts`。
- `src/main.tsx`、`src/App.tsx`（Sider 导航 + Content + Routes；M6 的 Chat 位置预留但不实现）。
- `src/api/client.ts`（fetch 封装、`{error}` 解包抛带 code 的错误）、`src/api/types.ts`（对齐后端 DTO）。
- `src/components/KeySetTable.tsx`、`JsonBlock.tsx`、`TracePanel.tsx` 及对应 `*.test.tsx`。
- `src/pages/`：Dashboard、Restaurants、RestaurantDetail、Documents、DocumentDetail、Ingestion、IngestionDetail、RetrievalDebug（8 个）。
- 更新 `web/README.md`：真实启动说明、`ADMIN_ENABLED`、端点清单、M6 复用说明。

## 三、实施步骤（依赖序）

### 阶段 A：后端只读底座

1. **ADM-01**：写 `shared/domain/inspect/inspect.go` 全部 DTO；确认无禁词、仅 stdlib 依赖。
2. **ADM-02**：写 `shared/port/inspect.go` 接口 + 架构测试（写方法名扫描）。
3. **ADM-03**：写 `postgres/inspect.go`：
   - Overview 用 7 条轻查询：餐厅总数/active 一条；reviews 估算走 `pg_class.reltuples`；文档 active/缺向量一条；batches 计数；`GROUP BY retrieval_scope, doc_type`；最近 5 批次；schema_migrations。
   - 列表统一 `LIMIT n+1` 判定 next_cursor；投影不含 `content` / 向量本体 / `attributes_raw`。
   - 餐厅：`ORDER BY id DESC`，borough 白名单校验、cuisine 走 unnest EXISTS、q 走转义 ILIKE（trgm）、active 三态。
   - 评论：`(reviewed_at, id)` 行值比较，走 `reviews_restaurant_reviewed`。
   - 文档：`ORDER BY document_id DESC`；scope/doc_type/is_active 三态/has_embedding 三态；列表投影自定常量。
   - 批次：`(started_at, id)` 行值比较，stage 过滤；详情二次查 rejections（上限 + truncated）。
   - 文档详情支持 `include_vector_preview`（复用 parseVectorLiteral，截前 16 维）。
   - 单餐厅文档（不分页、小集合）：`ORDER BY is_active DESC, document_id DESC`。
4. **ADM-04**：`internal/inspect` 游标编解码 + Service（limit 夹紧 0→默认、>max→max；Overview 填充环境信息；透传各查询）。
5. **ADM-05**：loopback 中间件（SplitHostPort → ParseIP → IsLoopback；不读 XFF；403 + `unauthorized` 体）。

### 阶段 B：后端端点与装配

6. **ADM-06**：`httpapi/admin.go`（AdminService 接口 + GET 参数手动绑定 + bool 三态解析 + 11 个 handler，空切片保证）；router 新增 `registerAdminRoutes`。
7. **ADM-07**：Debug 端点直接在 admin 组复用 `SearchHandler(cfg.Search)` 与 `EvidenceHandler(cfg.Evidence)`——天然与 `/v1` 同形同行为。
8. **ADM-08**：config 新增 Admin 段；app.go 装配 InspectStore/inspect.Service/路由开关；非 loopback 绑定 warning；`.env.example` 补充注释样例。
9. **ADM-09**：补齐全部后端测试（单元 + 架构；集成测试写好但默认 skip）。

### 阶段 C：前端

10. **ADM-10**：检查 node/npm；在 `web/` 建 Vite+React18+TS+AntD5+TanStack Query+React Router 工程；`npm install`；空布局跑通；更新 README。
11. **ADM-11**：api client（base 取 `VITE_API_BASE`，默认空走 proxy）+ types。
12. **ADM-12**：KeySetTable（内部维护 cursor 栈、next_cursor 终止、只提供下一页/上一页）、JsonBlock（Collapse + pre）、TracePanel（通道摘要表 + Warnings Alert + 候选通道分数展开）+ Vitest。
13. **ADM-16（优先页面）**：RetrievalDebug——左表单（query/text 分输入、borough/cuisine/price/minRating/top_k）右结果（TracePanel + 候选展开 + 佐证按钮调 evidence + 餐厅详情链接）。
14. **ADM-13**：Dashboard（统计卡片、分布表、向量健康 Alert、最近批次、环境信息）+ Restaurants（筛选 + KeySetTable）。
15. **ADM-14**：RestaurantDetail（4 区块并行）+ Documents（筛选、inactive 灰显+表头说明）+ DocumentDetail。
16. **ADM-15**：Ingestion（批次表）+ IngestionDetail（全计数、missing_fields、reject_reasons 简易条形、拒绝明细表）。
17. **ADM-17**：`.env.example`、CORS 白名单说明；前后端联调。

## 四、依赖与注意事项

- Debug 端点复用现有 handler 是关键决策：零重写、保证输入输出与线上链路完全一致（PRD §7.3）。
- 全局文档列表坚持 `document_id DESC` 单键 keyset（避免复合游标成本）；"active 置前"仅用于单餐厅小集合；inactive 的误读由 UI 灰显 + 表头说明承担，符合"能用就行"取舍。
- 时间字段 DTO 全部 `time.Time`（JSON 序列化为 RFC3339 UTC）。
- jsonb 字段在 DTO 中用 `json.RawMessage` 透传（attributes / hours / metadata / missing_fields / reject_reasons 中的 map 除外），减少解析假设。
- 前端仅桌面宽度，不做响应式；不引入 Redux，列表靠 TanStack Query，页内用 useState。
- Hertz 静态托管前端不在本次实施（PRD §5.7 为可选项）；开发与手工验收走 Vite proxy。

## 五、验证方式

- 后端：`go build ./...`、`go vet ./...`、`go test ./...`（含新增架构/单元测试）；有 DB 时 `PLATEPILOT_REQUIRE_DB=1 go test ./shared/adapter/repository/postgres/... -run Inspect`。
- 前端：`npm run build`（tsc + vite build）、`npm test`（Vitest 全绿）。
- 手工：启动 chat-service（`ADMIN_ENABLED=true`）+ `npm run dev`；验证 Dashboard 加载、Debug 工作台查询真实 trace；非 loopback 来源返回 403；关闭开关返回 404。
- 只读审查：grep 确认 InspectStore 与路由注册无写语义。

## 六、风险与处理

- **任务量大（17 项）**：按阶段推进，Debug 切片（A+B + ADM-16）先形成可用价值，其余页面随后补齐；每阶段结束跑测试门禁。
- **npm 环境/网络**：先探测 node/npm；install 失败则记录错误并请用户检查网络/镜像源。
- **禁词测试误伤**：新领域文件注释统一用"数据存储/持久化"等中性表述，写完立即跑架构测试。
- **403 状态码**：中间件不用 httperr.Write（它会给 401），直接构造同一信封形状的 JSON 写 403，保证 body 一致。
- **无 DB 环境**：集成测试默认 skip 并给出提示，不阻塞离线门禁。
