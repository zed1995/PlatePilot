# AGENTS.md

给 coding agent 看的仓库手册。先读这一份，再读你要改的那个目录里的
`AGENTS.md`（`shared/`、`chat-service/`、`data-pipeline/`、`web/` 各有一份，
只写各自领域的东西，通用约定都在本文件）。

产品事实、验收指标、设计权衡读 [README.md](README.md)；里程碑清单读 `docs/`。
本文件只写**怎么在这个仓库里正确地改代码**。

---

## 1. 这是什么

PlatePilot 是一个「有据可依」的餐厅发现 Agent：用户的每个结论都要能指回一条
真实评论。技术上是 Go + CloudWeGo Eino + OpenAI 兼容聊天渠道 + 本地 Qwen
embedding + PostgreSQL（pgvector / PostGIS / pg_trgm）。

一条流水线两条路：**离线**由 `data-pipeline` 把原始 Google Local 数据导入
PostgreSQL 并生成知识文档与向量；**在线**由 `chat-service` 做两级检索 +
工具调用 + SSE 回答。`web/` 是只读运维台，不做业务写入。

**当前进度**：M0–M3 在 `README.md` 标注完成；M4（Agent 运行时：Eino 图、工具调用、
Run 审计、会话状态、Chat/SSE）已在 `main` 落地，但 README 的 Status 小节还没同步；
下一个里程碑是 M5 纵向切片。开工前对一下 `docs/platepilot-m4-task-document.md`
和 `git log`，别只看 README。

**环境要求**：Go 1.26+、Docker（本地 Postgres）、Node 18+（前端）。

## 2. 目录地图：四个独立项目

**根目录没有 `go.mod`，也没有 `package.json`。** 每个目录是一个完整项目，有自己的
模块文件和 Makefile，可以单独 build / test / deploy。

| 目录 | 类型 | 职责 | 技术栈 |
|---|---|---|---|
| `shared/` | Go module（库） | 领域 DTO、端口与其适配器、配置、日志、embedding / rerank / chat 客户端、测试夹具 | pgx v5 |
| `chat-service/` | Go module（服务，常驻） | HTTP + SSE 聊天 API、Agent 运行时、两级检索、只读 `/admin/v1` | Hertz、Eino |
| `data-pipeline/` | Go module（CLI，批处理） | 导入、清洗、知识文档、embedding、打分、`migrate` | 标准库为主 |
| `web/` | npm 项目 | 只读管理后台 | Vite + React 18 + TS + Tailwind + Radix |

同级还有这些**不是项目**的东西：

- `deploy/` — 本地 Postgres 的 compose 与镜像（自带 pgvector + PostGIS）。
- `docs/` — PRD、实施计划、各里程碑任务文档（**接大任务前必读**）。
- `data/` — `raw/`（git-ignored，原始 gzip JSONL）、`processed/`、`demo/`、
  `boundaries/`（外部几何数据，同样 git-ignored）。
- `scripts/` — 一次性 Python 探索脚本，是 git-ignored 的临时产物，不属于构建。

### 服务如何依赖 shared

两个服务都用 `replace github.com/zed1995/platepilot/shared => ../shared`，
本地改动立刻生效，不需要发版，也不需要根 `go.mod`。

`make work` 会在根生成一个 `go.work` 供编辑器和跨模块命令使用；它是
**git-ignored 的本地便利品，不是构建输入**。任何一个项目缺了它都要能单独
编译——不要为了省事把它提交上去，也不要认为 CI/他人环境一定有它。

## 3. 依赖方向（有测试强制，不是口头规矩）

```
chat-service  ─┐
               ├─→ shared
data-pipeline ─┘
```

- `shared` **绝不 import 任何服务**；两个服务之间**绝不互相 import**，要共用就
  下沉到 `shared`。
- **database driver、Hertz、Eino、Ollama 这些具体技术不得越过各自的边界**：
  - `shared/domain/**` 只允许依赖标准库和自身：连 `postgres` / `postgis` /
    `pgvector` / `sql` 这些词出现在源码或注释里都会让测试失败；
  - `chat-service/internal/agent/**` 之外不许出现 Eino 类型，其中 Eino 的 message
    / schema DTO 进一步只准出现在 `agent/einomodel` 和 `agent/toolreg`；
  - `chat-service/internal/admin` 不许 import 具体的 store 适配器，只能面向端口。

以上全部是**会失败的测试**（`shared/domain/architecture_test.go`、
`chat-service/internal/agent/architecture_test.go`、
`chat-service/internal/admin/architecture_test.go`）。违反时先想是不是分层放错了，
而不是改测试放宽它。

新增代码的归属按「有几个服务会用它」判断，而不是「它看起来是否通用」：
两个服务都会用的下沉到 `shared/`，只有一个服务会用的留在那个服务的 `internal/`。

## 4. 常用命令

根 Makefile 只做转发，本身不包含任何构建逻辑。

```bash
# 冒烟：起库 → 迁移 → 起服务
cp .env.example .env
make pg-up          # Postgres（pgvector + PostGIS）on :55432
make migrate        # = make -C data-pipeline migrate
make run-chat       # :8080，curl localhost:8080/healthz
make run-chat-admin # 同上，额外开启 /admin/v1/*
make dev            # chat-service(admin) + vite dev server 一起起

# 检查（改完代码至少跑前两个）
make test           # 每个 Go 项目 go test ./...
make vet            # 每个 Go 项目 go vet ./...
make build          # chat-service/bin、data-pipeline/bin
make cover          # 覆盖率写入 <project>/coverage.out
make test-postgres  # Postgres 适配器契约套件（需 make pg-up）
make eval-retrieval # 检索指标 + 门禁（需活的语料）
make lint           # golangci-lint（需自行安装；仓库无 CI、无 lint 配置）

# 前端
make web-install && make web-dev    # 或 cd web && npm run dev
cd web && npm test                  # vitest

make -C <project> help   # 看某个项目自己的 target
```

## 5. 环境与配置

- 根 `.env` 是两个 Go 服务共用的（git-ignored）；`make run*` 的规则是
  **先读项目自己的 `.env`，读不到就回退到根 `.env`**。
- `.env.example` 是**带注释的完整参考**，永远同步它而不是直接写 `.env`；
  `chat-service/.env.example`、`data-pipeline/.env.example` 是各自读到的子集。
- 新配置必须走 `<项目>/internal/config.Load() + Validate()`，打印时用
  `Redacted()`，**任何密钥都不许原样进日志**。
- 放进环境变量的东西通常是「产品决策」而非实现细节：检索权重、各类超时、
  `RETRIEVAL_ENABLE_*`、`ADMIN_ENABLED` 都是为此存在的，方便不重编译就调优。
- `ADMIN_ENABLED` 默认关，且 `/admin/v1/*` **额外限制 loopback**；开着也不要把
  `HTTP_ADDR` 暴露到 `0.0.0.0`。

## 6. 测试约定

1. **默认离线可跑。** 写侧 store 在 `shared/store/memory` 有内存实现，
   `make test` 不需要数据库。
2. **需要 Postgres 的测试在没库时 skip，不 fail。** Postgres 适配器的价值正是
   pgvector / PostGIS / pg_trgm 真的在干活，double 伪造不了，所以它的验证必须
   跑真库（`make test-postgres`，换实例用 `PLATEPILOT_TEST_POSTGRES_DSN`）。
3. **一片绿的套件如果什么都没断言，比一片红更糟。** 涉及语料的检验要在没有
   数据库时「响亮地跳过」，需要当门禁用时用 `PLATEPILOT_REQUIRE_DB=1` 把 skip
   变成 fail。
4. 行为契约写在 `shared/store/contract`，`memory` 和 `postgres` 跑同一套断言——
   **新增仓储方法要在契约里加 case，而不是只在某一个适配器里测。**
5. 检索是**可复现排序**：同样输入必须返回同样结果。不要引入 map 遍历顺序、
   时间、随机数等非确定性因素。
6. 建 test fixture 用 `shared/testkit`（mocks / fixtures / 内存仓储），别再造一套。

## 7. 改代码时要守的几条硬规则

- **`/v1` 下的响应体是客户端契约**（`reasons` 解释、`trace.channels` 通道明细
  会被 UI 直接读）。改字段是破坏性变更，不是「顺手重构」。
- **引用链路不许截断**：证据块超预算就整块丢弃，不许半句引用；同一家餐厅同一
  文档类型最多 3 块；来源缺失要返回 `"unknown"` 并进 `warnings`——无来源的引用
  比没有引用更糟。
- **没有 filter 也没有 text 的搜索要拒绝**，不要退化成「按分数返回全库」；未知
  行政区返回 `400` 而不是空列表；查不到是 `200` + 空数组。这些差别是有意的。
- **硬过滤必须先于向量召回**：pgvector 的 HNSW 是 `ORDER BY` 结构，单加 `WHERE`
  会退化成顺序扫描，schema 靠「每个行政区一个 partial HNSW index」来兜。
- 面向用户的解释性文案（`reasons`、错误提示）是中文。
- 主键全部是数据库赋值的 `bigint GENERATED BY DEFAULT AS IDENTITY`，**应用不生成
  id**；`source_record_id`（Google 的 `gmap_id`）保持 `text`。
- 边界几何文件缺失时行为会**降级**为近似包围盒并告警（在正式跑批里用
  `--require-boundaries` 关掉这个降级），改动这块要意识到它会影响整个语料的
  行政区标注。
- `data/processed/` 是本地产物：重导 restaurants 之后 `prefilter` 要重跑。

## 8. 提交与 PR

- Conventional Commits，`<type>(<scope>): <subject>` 小写英文
  （`feat(web):`、`feat(agent):`、`fix(admin):`、`refactor:`、`chore:`）。
- 每个里程碑任务完成后，把与计划的偏差追加进对应任务文档的
  「附录 E：实施记录」，而不是悄悄改掉原计划。
- 改动用户可见的行为时，同步更新 `README.md` 的相关小节 + `.env.example` 注释。

## 9. 文档地图

| 想查什么 | 去哪 |
|---|---|
| 产品定义、验收指标、已知取舍 | `README.md` |
| 里程碑划分与依赖、Gate 定义 | `docs/platepilot-implementation-plan.md` |
| 技术方案（含 Provider 抽象） | `docs/platepilot-technical-prd.md` |
| 当前/下一个里程碑的执行清单 | `docs/platepilot-m{0..5}-task-document.md` |
| 管理后台需求与前后端约定 | `docs/platepilot-admin-prd.md`、`docs/platepilot-admin_plan.md` |
| Agent 验证台（在 Web 上走一整轮对话）的清单与实施记录 | `docs/platepilot-web-agent-console-task-document.md` |

子项目级约定：`shared/AGENTS.md`、`chat-service/AGENTS.md`、
`data-pipeline/AGENTS.md`、`web/AGENTS.md`。

## 10. 不要做

- 不要加根 `go.mod` / 根 `package.json`，不要把四个项目合并成一个模块。
- 不要提交 `go.work`、`go.work.sum`、`.env`、`data/raw/`、`data/processed/`。
- 不要让两个服务互相 import，也不要因为「就一个函数」在 `shared` 里塞服务专有逻辑。
- 不要用 `interface{}` 传跨域数据；领域类型放 `shared/domain/*`。
- 不要让单元测试依赖外部服务或真实语料（需要时 skip 或写 fixture）。
- 不要在 `web/` 里放 Go 代码——那个目录归 Node 工具链。
