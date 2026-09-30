# PlatePilot M0 工程基础任务文档

> 版本：v1.1  
> 日期：2026-09-30  
> 依据：`plans/platepilot-implementation-plan.md`（§6 M0、§7 关键路径、§10 完成定义）与 `plans/platepilot-technical-prd.md` v0.12（§7 API 边界、§8 技术栈、§9 API 框架约定）  
> 里程碑目标：搭建一个**可启动、可测试、边界清晰**的 Go 工程底座  
> 退出条件：服务可本地启动且 `/healthz` 可用；领域 DTO、Provider、Repository 接口和测试骨架完整；**领域层不依赖任何具体中间件或厂商 SDK**

---

## 0. 如何使用本文档

- 本文档是 M0 的**可执行任务清单**，每个任务独立成节，包含目标、交付物、实现要点、依赖、工作量和可验证的验收标准。
- 任务粒度对齐实施计划的 M0 表格（M0-01 ~ M0-08）。任务 ID 与实施计划保持一致，便于交叉引用。
- 每个任务的**完成定义（DoD）**默认继承实施计划 §10：代码实现 + 测试 + 错误码 + trace_id 日志 + 不泄露密钥/PII + 不经领域接口直连厂商 API + 关键设计有注释 + 通过 `go test ./...`、`go vet ./...`、`golangci-lint` + 验收标准可实际演示。
- 本文档中的 Go 接口签名是**约定形状**，允许在不破坏领域边界的前提下微调，但一旦被后续里程碑依赖应先更新本文档。
- 模块路径示例使用 `github.com/zed/platepilot`，落地时可替换为实际仓库路径，替换后需全局统一。

### 0.1 工作量级定义

| 级别 | 含义 |
|---|---|
| S | 半天以内 |
| M | 约 1–2 天 |
| L | 约 3–5 天 |

---

## 0.2 实现状态（2026-09-29）

M0 已完成并通过本地验收。代码按**两个可独立运行的服务**组织：数据生产
（`data-pipeline/`）与聊天服务（`chat-service/`），共享代码放在 `shared/`，
并为后续前端预留 `web/`。

| 任务 | 状态 | 主要落地文件 |
|---|---|---|
| M0-01 | 完成 | `go.mod`、`data-pipeline/main.go`、`chat-service/main.go`、`Makefile`、`README.md` |
| M0-02 | 完成 | `shared/config/config.go`、`chat-service/internal/config/config.go`、`data-pipeline/internal/config/config.go`、`.env.example` |
| M0-03 | 完成 | `shared/domain/errs/errs.go`、`shared/domain/requestctx/requestctx.go`、`shared/observability/logging/logging.go` |
| M0-04 | 完成 | `shared/domain/{chat,tool,search,evidence,memory,conversation,run}`、`shared/domain/architecture_test.go` |
| M0-05 | 完成 | `shared/port/{port,chat,embedding,rerank}.go`、`shared/testkit/mock_provider.go` |
| M0-06 | 完成 | `shared/port/repository.go`、`shared/adapter/repository/memory/*`、`shared/testkit/memory_repo.go` |
| M0-07 | 完成 | `chat-service/internal/transport/httpapi/{router,health,validation,hertzlog}.go`、`chat-service/internal/transport/httperr/httperr.go` |
| M0-07A | 完成 | `chat-service/internal/transport/httpapi/middleware/{requestid,logging,recovery,cors}.go` |
| M0-08 | 完成 | 各包 `*_test.go`、`shared/testkit/*`、`data-pipeline/main_test.go` |

关键实现决策（与正文的差异）：

1. **组织结构**：单一 Go module 承载两个独立进程。`data-pipeline` 是批处理 CLI，`chat-service` 是 HTTP 服务，各自拥有 `main.go` 与 `internal/`；仅共享 `shared/`。两个服务互不 import，只依赖 `shared/port` 与 `shared/domain`。
2. **HTTP 包命名**：目录使用 `chat-service/internal/transport/httpapi`（package `httpapi`）而非 `http`，避免与标准库 `net/http` 冲突；统一错误信封拆到 `transport/httperr`，供 middleware 与 handler 共用，避免 import cycle。
3. **配置拆分**：`shared/config` 提供 `Loader`、`ValidationError`、`APP_ENV`/`LOG_LEVEL`/`POSTGRES_*`/`EMBEDDING_*`/`REQUEST_TIMEOUT` 等共用原语；两个服务各自组合出自己的 `Config`，只校验自己需要的字段。聊天服务校验 `HTTP_*`/`CHAT_*`，数据生产校验 `PIPELINE_*`。
4. **Repository 返回值**：`Get*`/`Load*` 使用值返回 + `error`，未找到时返回 `errs.ErrNotFound`（可用 `errors.Is` 判定），不使用 `nil` 指针表达缺失。
5. **校验落地方式**：M0 尚无业务请求体，因此以「Hertz `WithCustomValidatorFunc` + `Validatable` 接口 + `BindAndValidate` 辅助函数」实现；绑定失败映射为 `invalid_argument`，校验失败映射为 `validation_failed`。
6. **领域纯净性**：由 `shared/domain/architecture_test.go` 强制（解析领域包 import，只允许标准库与 `shared/domain` 前缀），随 `go test ./...` 执行，无需额外工具。
7. **Hertz 日志**：`chat-service/internal/transport/httpapi/hertzlog.go` 把 Hertz 框架日志接入 `slog`，因此框架日志与应用日志同为 JSON，且带 `component=hertz`。
8. **数据生产阶段是显式占位**：`import` / `build-documents` / `embed` 返回「planned in M1-xx/M2-xx」的错误并以非零退出，绝不静默成功；`version`、`help`、`check-config` 已可用。
9. **未验证项**：本机未安装 `golangci-lint`，`make lint` 会给出安装提示；`depguard` 规则暂由架构测试替代，待安装 lint 后补 `.golangci.yml`。

验收记录：

```text
go build ./...                       # OK（两个服务都能编译）
go vet ./...                         # OK
go test ./...                        # OK
go test -race ./...                  # OK
make build VERSION=0.1.0-dev         # 产出 bin/chat-service 与 bin/data-pipeline
bin/data-pipeline version            # platepilot data-pipeline 0.1.0-dev
bin/data-pipeline check-config       # 打印共享 + pipeline 配置摘要
bin/data-pipeline import             # exit 1：planned in M1-04/M1-05
HTTP_ADDR=127.0.0.1:18081 bin/chat-service
                                     # GET /healthz -> 200 {"status":"ok","version":"...","request_id":"..."}
GET /nope                            # 404 {"error":{"code":"not_found",...}}
X-Request-ID: abc-123                # 响应头与日志均回显 abc-123
HTTP_ADDR='::::' bin/chat-service    # exit 1，错误信息含 HTTP_ADDR
```

---

## 1. M0 目标与退出条件

### 1.1 里程碑目标

M0 只解决"工程基础"，不承载任何业务逻辑；它要证明后续所有里程碑都能在同一个骨架上继续生长：

1. Go Module、目录结构、入口程序可编译、可启动、可测试。
2. 配置和密钥从环境变量/配置文件注入，启动时做校验。
3. 结构化日志与统一错误模型贯通 request_id / trace_id。
4. 领域 DTO 与 Provider、Repository 接口稳定。
5. Hertz HTTP/API 骨架、中间件与统一错误响应可用。
6. 测试基线（含 Mock Provider、内存 Repository、测试夹具）就绪。

### 1.2 退出条件（Milestone Exit Criteria）

- [ ] `go build ./...` 与 `go test ./...` 全绿。
- [ ] `go vet ./...` 与 `golangci-lint run` 无错误。
- [ ] 本地启动服务后，`GET /healthz` 返回 200 与结构化 JSON。
- [ ] 缺少关键配置时进程启动失败，且错误信息明确指出缺失项。
- [ ] 日志为 JSON，且包含 `request_id` / `trace_id`。
- [ ] 非法请求返回统一错误响应体与稳定错误码。
- [ ] 领域包 `shared/domain/...` 的依赖闭包中**不出现** `github.com/jackc/pgx`、Eino、Ollama、OpenAI/HTTP 客户端、Hertz 等外部实现包。
- [ ] 每个 Provider / Repository 接口都有可用的 Mock 或内存实现，且有单元测试覆盖。

---

## 2. 目标工程骨架

M0 结束时目录结构应接近如下形态（允许小幅调整，但分层语义必须保留）：

```text
platepilot/                              # 单一 Go module，两个可独立运行的服务
├── go.mod
├── go.sum
├── Makefile                             # build / run-chat / run-pipeline / test / lint
├── README.md
├── .env.example                         # 两个服务共用的本地配置样例
├── data-pipeline/                       # 服务一：数据生产（批处理 CLI）
│   ├── main.go                          # 入口：version / check-config / import / build-documents / embed
│   └── internal/
│       ├── config/                      # PIPELINE_* 与共享配置的组合
│       └── pipeline/                    # M1/M2 落地的导入、清洗、文档、向量化阶段
├── chat-service/                        # 服务二：聊天服务（HTTP / SSE）
│   ├── main.go                          # 入口：加载配置 -> 装配 app -> 启动 Hertz
│   └── internal/
│       ├── config/                      # HTTP_* / CHAT_* 与共享配置的组合
│       ├── app/
│       │   └── app.go                   # 手工依赖装配（config、providers、repos、router）
│       └── transport/
│           ├── httpapi/                 # package httpapi（避免与 net/http 冲突）
│           │   ├── router.go            # Hertz 路由与全局中间件装配
│           │   ├── health.go            # /healthz
│           │   ├── validation.go        # Validatable + BindAndValidate 统一校验入口
│           │   ├── hertzlog.go          # Hertz 日志接入 slog（JSON）
│           │   └── middleware/          # requestid、logging、recovery、cors
│           └── httperr/
│               └── httperr.go           # 统一错误信封（handler 与 middleware 共用）
├── shared/                              # 两个服务共享的代码
│   ├── config/                          # 环境变量加载、ValidationError、密钥脱敏
│   ├── domain/                          # 纯领域层：只依赖标准库与 shared/domain
│   │   ├── chat/                        # ChatMessage、ChatRequest、ChatResponse、TokenUsage
│   │   ├── tool/                        # ToolSpec、ToolCall、ToolResult
│   │   ├── search/                      # 过滤条件、候选餐厅、检索结果
│   │   ├── evidence/                    # Evidence、来源、快照时间
│   │   ├── memory/                      # 长期记忆 DTO
│   │   ├── conversation/                # 会话与 checkpoint DTO
│   │   ├── run/                         # Agent run 与 tool call 审计 DTO
│   │   ├── requestctx/                  # RequestContext（request_id / trace_id / user_id）
│   │   ├── errs/                        # 统一错误码与领域错误
│   │   └── architecture_test.go         # 领域纯净性守卫（禁止非标准库依赖）
│   ├── port/                            # 领域依赖的接口（端口）
│   │   ├── chat.go                      # ChatProvider / ToolCallingProvider / StructuredOutputProvider
│   │   ├── embedding.go                 # EmbeddingProvider
│   │   ├── rerank.go                    # RerankProvider
│   │   └── repository.go                # Restaurant / Knowledge / Conversation / Memory / Run
│   ├── adapter/                         # 实现 port 的外部依赖，厂商 SDK 只允许出现在这里
│   │   ├── chat/openai/                 # M4 填充，M0 留骨架
│   │   ├── embedding/ollama/            # M2 填充，M0 留骨架
│   │   └── repository/                  # 实现 port 的存储实现
│   │       ├── memory/                  # M0 提供内存实现
│   │       └── postgres/                # M1 提供 PostgreSQL 实现
│   ├── observability/logging/           # slog 初始化与字段约定
│   ├── idgen/                           # 无依赖的 ID 生成
│   └── testkit/                         # 测试夹具与 Mock（两个服务共用）
│       ├── mock_provider.go
│       ├── memory_repo.go
│       └── fixtures.go
├── web/                                 # 预留前端项目（M6-06），不放 Go 代码
│   └── README.md
├── testdata/                            # 小型固定测试数据（不要放原始大文件）
├── scripts/                             # 已有 Python 数据探索脚本（保留）
└── plans/                               # 技术 PRD、实施计划、任务文档
```

### 2.1 分层与依赖方向

依赖方向必须单向收敛到领域层：

```text
data-pipeline/main ──> data-pipeline/internal/pipeline ──┐
                                                         ├──> shared/port（接口）──> shared/domain（纯 DTO）
chat-service/main  ──> chat-service/internal/app ────────┘
                             │
                             ├──> chat-service/internal/transport/httpapi（Hertz，实现 API）
                             └──> shared/adapter/*（OpenAI / Ollama / PostgreSQL，实现 port）
```

**禁止**的方向：

- `shared/domain` 或 `shared/port` 反向 import `adapter` / `transport` / 任一服务的 `internal`。
- `shared/domain` import 任何第三方 SDK（数据库驱动、Eino、Hertz、OpenAI 客户端、Ollama 客户端）。
- 业务代码直接 `import` 数据库驱动或厂商 Chat SDK，绕过 `shared/port`。
- `data-pipeline` 与 `chat-service` 互相 import：两个服务只通过 `shared/` 共享代码，不直接依赖对方。


---

## 3. 任务清单总览

| ID | 任务 | 交付物 | 依赖 | 工作量 | 验收摘要 |
|---|---|---|---|---|---|
| M0-01 | 初始化 Go 工程 | `go.mod`、目录结构、入口程序、Makefile | 无 | S | `go test ./...` 与本地启动通过 |
| M0-02 | 配置与密钥管理 | 环境变量、配置文件、启动校验 | M0-01 | S | 缺少关键配置时启动失败并给出明确错误 |
| M0-03 | 日志和错误模型 | `slog`、错误码、请求 ID | M0-01 | S | JSON 日志包含 trace/request ID |
| M0-04 | 领域 DTO | Chat、Tool、Search、Evidence、Memory DTO | M0-01 | M | 领域包不依赖数据库驱动、Eino、具体 Chat API、Ollama |
| M0-05 | Provider 接口 | ChatProvider、EmbeddingProvider、RerankProvider | M0-04 | M | Mock Provider 可用于测试 |
| M0-06 | Repository 接口 | Restaurant、Knowledge、Conversation、Memory、Run Repository | M0-04 | M | 接口可由 PostgreSQL 和内存实现 |
| M0-07 | Hertz HTTP/API 骨架 | Hertz Router、健康检查和统一错误响应 | M0-01 | S | `/healthz` 和统一错误响应可用 |
| M0-07A | Hertz 中间件与验证 | binding、validation、recovery、request ID、CORS | M0-07 | M | 非法请求返回统一错误，request ID 可贯穿 trace |
| M0-08 | 测试基线 | `go test`、接口 Mock、测试夹具 | M0-04 | M | 核心 DTO 和 Provider 有单元测试 |

### 3.1 依赖图

```text
M0-01
 ├─> M0-02 ─┐
 ├─> M0-03  │
 ├─> M0-07 ─┴─> M0-07A
 └─> M0-04
      ├─> M0-05 ──┐
      ├─> M0-06   ├─> M0-08
      └───────────┘
```

### 3.2 推荐执行顺序（对应第 1 组"可运行底座"）

1. M0-01 初始化 Go 工程
2. M0-02 配置与密钥管理
3. M0-03 日志和错误模型
4. M0-04 领域 DTO
5. M0-05 Provider 接口
6. M0-07 Hertz HTTP/API 骨架
7. M0-07A Hertz 中间件与验证
8. M0-06 Repository 接口（可与 M0-05 并行）
9. M0-08 测试基线（贯穿始终）

> 最小可运行链路：`M0-01 -> M0-02 -> M0-07 -> M0-07A`，完成后即可启动服务并验证 `/healthz`。

---

## 4. 详细任务

### M0-01 初始化 Go 工程

**目标**：建立可编译、可启动、可测试的 Go Module 与目录骨架。

**交付物**

- `go.mod`（`module github.com/zed/platepilot`，`go 1.26`）。
- `go.sum`（在加入首批依赖后生成）。
- `chat-service/main.go`：解析配置、装配 `app`、启动 Hertz、处理优雅退出（`context` + `signal.NotifyContext`）。
- `data-pipeline/main.go`：批处理 CLI 入口，提供 `version` / `check-config` / `import` / `build-documents` / `embed` 子命令。
- 第 2 节目录结构中的空包骨架（至少包含 `shared/domain`、`shared/port`、`shared/config`、`chat-service/internal/transport/httpapi`、`data-pipeline/internal/pipeline`）。
- `Makefile`，至少包含以下目标：
  - `make run-chat` → `go run ./chat-service`
  - `make run-pipeline` → `go run ./data-pipeline check-config`
  - `make build` → 产出 `bin/chat-service` 与 `bin/data-pipeline`
  - `make test` → `go test ./...`
  - `make vet` → `go vet ./...`
  - `make lint` → `golangci-lint run`
- `.gitignore` 增补 `bin/`、`*.out`、`.env` 等（保留现有条目）。

**实现要点**

- 引入首批依赖：`github.com/cloudwego/hertz`（HTTP）、`log/slog`（标准库）。数据库驱动与 Eino 依赖推迟到对应里程碑，避免 M0 引入未使用依赖。
- 两个服务的 `main.go` 都只做「读配置 → 构造依赖 → 执行业务」。聊天服务跑 HTTP 服务，数据生产跑批处理子命令。
- 优雅退出：聊天服务监听 `SIGINT`/`SIGTERM`，在 `ShutdownTimeout` 内关闭 HTTP；数据生产复用同一 `signal.NotifyContext` 作为作业取消信号。

**依赖**：无。  
**工作量**：S。

**验收标准**

```bash
go build ./...
go test ./...
make build            # 产出 bin/chat-service 与 bin/data-pipeline
make run-chat         # 能启动，Ctrl-C 能优雅退出
bin/data-pipeline version
```

- 目录结构与第 2 节一致。
- 两个入口均可独立运行：聊天服务只监听端口并打印启动日志，数据生产在无业务依赖时也能执行 `version` / `check-config`。

---

### M0-02 配置与密钥管理

**目标**：集中管理运行配置与密钥，启动时做校验，杜绝硬编码密钥。

**交付物**

- `shared/config/config.go`（两个服务共用的配置原语）：
  - `Loader`：从环境变量读取 `String`/`Duration`/`Int`/`List`/`StringMap`，并累积解析失败而不是首错即停。
  - `ValidationError` 与 `Combine(...)`：把所有问题一次性汇总，错误信息带变量名。
  - 共用子配置：`AppConfig`（`APP_ENV`）、`LogConfig`、`PostgresConfig`、`EmbeddingConfig`、`TimeoutConfig`，各自提供 `Validate()`。
  - `LoadDotEnv`（不覆盖已有环境变量）、`Redact` / `RedactURI`。
- `chat-service/internal/config/config.go`：
  - `type Config struct { App; HTTP; Log; Postgres; Chat; Embedding; Timeout }`
  - `func Load() (Config, error)`：共享原语 + `HTTP_*` / `CHAT_*`。
  - `func (c Config) Validate() error`，以及 `Redacted()` / `Summary()`。
- `data-pipeline/internal/config/config.go`：
  - `type Config struct { App; Log; Postgres; Embedding; Timeout; Pipeline }`
  - `PipelineConfig{ DataDir; BatchSize; Workers }`，对应 `PIPELINE_*`。
  - `func Load() (Config, error)`、`Validate()`、`Redacted()`、`Summary()`。
- 测试：`shared/config/config_test.go`、`chat-service/internal/config/config_test.go`、`data-pipeline/internal/config/config_test.go`。

**配置项约定**（对齐 PRD §8.4、§8.5）

| 变量 | 是否必填 | 默认值 | 所属 | 说明 |
|---|---|---|---|---|
| `APP_ENV` | 否 | `dev` | 共享 | `dev` / `test` / `prod` |
| `LOG_LEVEL` | 否 | `info` | 共享 | `debug`/`info`/`warn`/`error` |
| `POSTGRES_DSN` | M1 起必填 | 空 | 共享 | PostgreSQL 连接串 |
| `POSTGRES_DATABASE` | 否 | `platepilot` | 共享 | 数据库名 |
| `EMBEDDING_PROVIDER` | M2 起必填 | 空 | 共享 | `ollama` |
| `OLLAMA_BASE_URL` | 否 | `http://localhost:11434` | 共享 | Ollama 地址 |
| `EMBEDDING_MODEL` | 否 | `qwen3-embedding:0.6b` | 共享 | 模型 ID |
| `EMBEDDING_DIMENSIONS` | 否 | `1024` | 共享 | 向量维度，需与向量索引一致 |
| `REQUEST_TIMEOUT` | 否 | `15s` | 共享 | 出站请求默认超时 |
| `HTTP_ADDR` | 否 | `:8080` | chat-service | HTTP 监听地址 |
| `HTTP_CORS_ALLOW_ORIGINS` | 否 | 空 | chat-service | CORS 白名单，空则禁用跨域 |
| `CHAT_PROVIDER` | M4 起必填 | 空 | chat-service | Chat 适配器类型 |
| `CHAT_BASE_URL` | M4 起必填 | 空 | chat-service | OpenAI 兼容端点 |
| `CHAT_API_KEY` | M4 起必填 | 空 | chat-service | 密钥（只从环境变量读） |
| `CHAT_MODEL` | M4 起必填 | 空 | chat-service | 模型 ID |
| `CHAT_EXTRA_HEADERS_JSON` | 否 | `{}` | chat-service | 额外请求头 |
| `PIPELINE_DATA_DIR` | 否 | `data/raw/google_local` | data-pipeline | 原始数据目录 |
| `PIPELINE_BATCH_SIZE` | 否 | `1000` | data-pipeline | 批处理大小 |
| `PIPELINE_WORKERS` | 否 | `4` | data-pipeline | 并行 worker 数 |

**实现要点**

- **M0 阶段只强校验 `HTTP_ADDR` 与 `PIPELINE_*`**；`POSTGRES_*`、`CHAT_*`、`EMBEDDING_*` 采用"配置了才校验"，避免 M0 因缺少后续里程碑配置而无法启动。
- 两个服务各自只校验自己需要的字段：聊天服务管 `HTTP_*`/`CHAT_*`，数据生产管 `PIPELINE_*`；共享原语复用同一套 `Loader` 与错误模型。
- 提供 `Redacted()` / `Summary()`，用于日志输出时把密钥替换为 `***`。
- `.env` 仅用于本地开发，且必须在 `.gitignore` 中；生产从真实环境变量注入。
- 校验失败时用 `shared/config.ValidationError` 汇总，例如 `HTTP_ADDR: must be in host:port form (got "::::")`。

**依赖**：M0-01。  
**工作量**：S。

**验收标准**

- 聊天服务设置 `HTTP_ADDR=:9090` 时监听 9090；数据生产设置 `PIPELINE_BATCH_SIZE=50` 时摘要显示 50。
- 故意设置非法 `HTTP_ADDR`（如 `"::::"`）或必填项为空时，对应服务启动/执行失败且错误信息包含具体变量名。
- 日志打印配置摘要时，`CHAT_API_KEY`、`POSTGRES_DSN` 凭据等显示为 `***`。
- 三个 `config_test.go` 覆盖上述场景并通过。

---

### M0-03 日志和错误模型
### M0-03 日志和错误模型

**目标**：结构化日志 + 统一错误码 + request/trace ID 贯通，为后续可观测性打底。

**交付物**

- `shared/observability/logging/logging.go`：
  - `func New(level string) *slog.Logger`，使用 `slog.NewJSONHandler`。
  - 统一字段常量：`request_id`、`trace_id`、`thread_id`、`run_id`、`component`。
  - `func FromContext(ctx context.Context) *slog.Logger`：从 `context` 取出带字段的 logger。
- `shared/domain/errs/errs.go`：
  - `type Code string`，预定义错误码常量。
  - `type Error struct { Code Code; Message string; HTTPStatus int; cause error }`。
  - `func New(code Code, msg string) *Error`、`func Wrap(code Code, msg string, cause error) *Error`、`func (e *Error) Unwrap() error`。
  - `func CodeOf(err error) Code`、`func HTTPStatusOf(err error) int`。
- `shared/domain/requestctx/requestctx.go`：
  - `type RequestContext struct { RequestID, TraceID, UserID string }`。
  - `func New(requestID, traceID string) RequestContext`、`func (rc RequestContext) WithContext(ctx context.Context) context.Context`、`func FromContext(ctx context.Context) (RequestContext, bool)`。
- 单测：`errs_test.go`（错误码映射、wrap/unwrap）、`requestctx_test.go`（context 往返）。

**错误码建议**（M0 只落地基础集合，后续里程碑扩展）

| Code | HTTP | 含义 |
|---|---|---|
| `invalid_argument` | 400 | 请求参数非法 |
| `unauthorized` | 401 | 未认证 |
| `not_found` | 404 | 资源不存在 |
| `conflict` | 409 | 状态冲突 / 幂等冲突 |
| `validation_failed` | 422 | JSON Schema 校验失败 |
| `provider_timeout` | 504 | 上游 Provider 超时 |
| `provider_unavailable` | 502 | 上游 Provider 不可用 |
| `internal` | 500 | 未分类内部错误 |

**实现要点**

- 领域层错误**不依赖 HTTP**；`HTTPStatus` 由错误码的映射函数集中处理，HTTP 层只调用映射。
- 日志与错误中的密钥/PII 必须脱敏。
- 每个请求的 logger 由中间件注入 context，处理器只调用 `logging.FromContext(ctx)`。

**依赖**：M0-01。  
**工作量**：S。

**验收标准**

- 单测通过；`CodeOf`/`HTTPStatusOf` 对已知与未知错误都返回确定结果（未知默认 `internal`/500）。
- 端到端日志为 JSON，且包含 `request_id`（若请求头携带则沿用，否则生成）与 `trace_id`。

---

### M0-04 领域 DTO

**目标**：定义纯领域的 Chat / Tool / Search / Evidence / Memory DTO，成为所有层的公共语言。

**交付物**（分包，避免一个大 `models` 包）

- `shared/domain/chat/chat.go`
- `shared/domain/tool/tool.go`
- `shared/domain/search/search.go`
- `shared/domain/evidence/evidence.go`
- `shared/domain/memory/memory.go`
- 各包的 `*_test.go`：JSON 往返、枚举合法性、零值语义。

**接口形状（约定）**

```go
// chat
type Role string
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ChatMessage struct {
	Role       Role            `json:"role"`
	Content    string          `json:"content"`
	Name       string          `json:"name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []tool.ToolCall `json:"tool_calls,omitempty"`
}

type ChatRequest struct {
	ThreadID        string            `json:"thread_id,omitempty"`
	Messages        []ChatMessage     `json:"messages"`
	Model           string            `json:"model,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	MaxTokens       int               `json:"max_tokens,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	ProviderOptions map[string]any    `json:"provider_options,omitempty"` // 厂商特性只在此透传
}

type TokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type ChatResponse struct {
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
	Usage        TokenUsage  `json:"usage"`
	Model        string      `json:"model"`
}
```

```go
// tool
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema
	ReadOnly    bool            `json:"read_only"`
	TimeoutMS   int             `json:"timeout_ms,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolStatus string
const (
	ToolStatusOK    ToolStatus = "ok"
	ToolStatusError ToolStatus = "error"
)

type ToolResult struct {
	CallID  string          `json:"call_id"`
	Name    string          `json:"name"`
	Status  ToolStatus      `json:"status"`
	Content string          `json:"content,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   *errs.Error     `json:"error,omitempty"`
}
```

```go
// search
type RestaurantFilter struct {
	Cuisines    []string `json:"cuisines,omitempty"`
	PriceLevels []int    `json:"price_levels,omitempty"`
	MinRating   *float64 `json:"min_rating,omitempty"`
	Neighborhood string  `json:"neighborhood,omitempty"`
	OpenNow     *bool    `json:"open_now,omitempty"`
}

type RestaurantCandidate struct {
	RestaurantID string   `json:"restaurant_id"`
	Name         string   `json:"name"`
	Address      string   `json:"address"`
	Score        float64  `json:"score"`
	Reasons      []string `json:"reasons,omitempty"`
}

type SearchQuery struct {
	Text   string           `json:"text,omitempty"`
	Filter RestaurantFilter `json:"filter,omitempty"`
	TopK   int              `json:"top_k,omitempty"`
}
```

```go
// evidence
type Evidence struct {
	EvidenceID      string    `json:"evidence_id"`
	RestaurantID    string    `json:"restaurant_id"`
	DocType         string    `json:"doc_type"`
	Title           string    `json:"title,omitempty"`
	Content         string    `json:"content"`
	SourceRecordIDs []string  `json:"source_record_ids,omitempty"`
	Source          string    `json:"source"`
	SnapshotAt      time.Time `json:"snapshot_at"`
	Score           float64   `json:"score,omitempty"`
}
```

```go
// memory
type MemoryType string
const (
	MemoryTypePreference MemoryType = "preference"
	MemoryTypeConstraint MemoryType = "constraint"
	MemoryTypeFact       MemoryType = "fact"
)

type Memory struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Type       MemoryType `json:"memory_type"`
	Content    string     `json:"content"`
	Source     string     `json:"source"`
	Confidence float64    `json:"confidence"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
}
```

**实现要点**

- 领域包**只 import 标准库与其它 `shared/domain/...`**，不得 import `port`/`adapter`/第三方。
- `ToolSpec.Parameters`、`ToolCall.Arguments` 用 `json.RawMessage`，不在领域层绑定具体 Schema 库。
- 三态字段（`true`/`false`/`unknown`）在 M1 的数据模型中体现；M0 只需在 DTO 注释中约定，不提前建模。
- `ProviderOptions` 是唯一允许透传厂商特性的字段，禁止把厂商结构扩散到其他 DTO。

**依赖**：M0-01。  
**工作量**：M。

**验收标准**

- 提供**架构约束测试**（如 `shared/domain/architecture_test.go`）：
  - 使用 `go list -deps` 或 `go/packages` 遍历 `shared/domain/...`，断言依赖闭包中不含禁用前缀（`github.com/jackc/pgx`、`github.com/cloudwego/eino`、`github.com/cloudwego/hertz`、Ollama/OpenAI 客户端等）。
  - `go test ./shared/domain/...` 通过。
- 每个 DTO 包有 JSON 往返测试。
- 领域层可被任意上层 import 而不引入外部依赖。

---

### M0-05 Provider 接口

**目标**：定义与厂商解耦的 Chat / Embedding / Rerank 端口，并提供 Mock 实现。

**交付物**

- `shared/port/chat.go`：
  - `ChatProvider`、`ToolCallingProvider`、`StructuredOutputProvider`。
  - `ChatStream` 抽象（`Recv() (ChatChunk, error)`、`Close() error`），不暴露 HTTP/厂商类型。
- `shared/port/embedding.go`：`EmbeddingProvider`。
- `shared/port/rerank.go`：`RerankProvider`。
- `shared/testkit/mock_provider.go`：所有接口的确定性 Mock（可注入固定响应与错误）。
- 单测：`shared/port/*_test.go` 断言 Mock 满足接口（`var _ ChatProvider = (*MockChatProvider)(nil)` 形式的编译期断言）。

**接口形状（约定）**

```go
type ChatProvider interface {
	Complete(ctx context.Context, req chat.ChatRequest) (chat.ChatResponse, error)
	Stream(ctx context.Context, req chat.ChatRequest) (ChatStream, error)
}

type ToolCallingProvider interface {
	SupportsTools() bool
	SupportsParallelTools() bool
	ChatWithTools(ctx context.Context, req chat.ChatRequest, tools []tool.ToolSpec) (ToolCallResponse, error)
}

type StructuredOutputProvider interface {
	SupportsJSONSchema() bool
	CompleteStructured(ctx context.Context, req chat.ChatRequest, schema json.RawMessage) (StructuredResponse, error)
}

type EmbeddingProvider interface {
	ModelID() string
	Dimensions() int
	EmbedDocuments(ctx context.Context, docs []string) ([][]float32, error)
	EmbedQuery(ctx context.Context, query string) ([]float32, error)
}

type RerankProvider interface {
	ModelID() string
	Rerank(ctx context.Context, query string, candidates []search.RestaurantCandidate) ([]search.RestaurantCandidate, error)
}
```

**实现要点**

- `ChatProvider` 与 `EmbeddingProvider` 是**两个独立接口**，不得合并（PRD §3.1 不可破坏原则）。
- 能力声明（`SupportsTools`/`SupportsJSONSchema`）由 Provider 暴露，路由层据此选择模型；M0 只需接口与 Mock。
- Adapter 目录 `shared/adapter/chat/openai`、`shared/adapter/embedding/ollama` 在 M0 只放占位（可留空文件 + `// TODO(M4)`），**不实现**真实请求。
- Mock 需支持：
  - 返回固定 `ChatResponse`；
  - 返回预设错误（用于错误路径测试）；
  - 记录调用次数与入参（用于断言）。

**依赖**：M0-04。  
**工作量**：M。

**验收标准**

- 编译期断言：Mock 满足全部接口。
- `MockChatProvider` 可完成一次 `Complete` 并返回固定响应，单测通过。
- `MockEmbeddingProvider` 返回指定维度向量，维度可配置。
- 端口包不 import 任何厂商 SDK。

---

### M0-06 Repository 接口

**目标**：定义数据访问端口，使其同时可由 PostgreSQL 与内存实现，隔离数据库细节。

**交付物**

- `shared/port/repository.go`：Restaurant / Knowledge / Conversation / Memory / Run 五类 Repository 接口。
- `shared/testkit/memory_repo.go`：**内存实现**（至少覆盖读取、写入、按条件查询、更新、删除），供 M0 及后续测试使用。
- 单测：`shared/testkit/memory_repo_test.go` 验证内存实现满足接口并支持幂等 upsert。

**接口形状（约定）**

```go
type RestaurantRepository interface {
	GetByID(ctx context.Context, restaurantID string) (search.RestaurantDetail, error)
	Search(ctx context.Context, query search.SearchQuery) ([]search.RestaurantCandidate, error)
	Upsert(ctx context.Context, restaurant search.RestaurantDetail) error
}

type KnowledgeRepository interface {
	FindEvidenceByRestaurant(ctx context.Context, restaurantID string) ([]evidence.Evidence, error)
	VectorSearch(ctx context.Context, scope evidence.RetrievalScope, query []float32, topK int, filter map[string]any) ([]evidence.Evidence, error)
	UpsertDocuments(ctx context.Context, docs []evidence.KnowledgeDocument) error
}

type ConversationRepository interface {
	Get(ctx context.Context, threadID string) (conversation.Conversation, error)
	Upsert(ctx context.Context, conv conversation.Conversation) error
	SaveCheckpoint(ctx context.Context, checkpoint conversation.Checkpoint) error
	LoadCheckpoint(ctx context.Context, threadID string) (conversation.Checkpoint, error)
}

type MemoryRepository interface {
	List(ctx context.Context, userID string) ([]memory.Memory, error)
	Upsert(ctx context.Context, m memory.Memory) error
	Delete(ctx context.Context, userID, id string) error
}

type RunRepository interface {
	Start(ctx context.Context, run run.AgentRun) error
	Finish(ctx context.Context, run run.AgentRun) error
	RecordToolCall(ctx context.Context, call run.ToolCallRecord) error
}
```

> 说明：`conversation`、`run`、`search.RestaurantDetail`、`evidence.KnowledgeDocument` 等类型在 M0 只需最小字段（可先定义占位结构），字段细节在 M1/M4 补齐。**M0 的核心是接口形状与内存实现可跑通，不是字段完备。**

**实现要点**

- 端口层只依赖 `shared/domain/...`，不依赖任何数据库类型（如 `pgx.Row`、`sql.NullString`）。
- 查询条件用领域 DTO（如 `search.SearchQuery`、`map[string]any` 过滤），不把驱动类型泄漏到端口签名。
- 内存实现要能表达"未找到"（返回 `errs.ErrNotFound`）和"重复键冲突"（返回 `errs.ErrConflict`），供后续 PostgreSQL 实现对齐。
- PostgreSQL 实现（`shared/adapter/repository/postgres`）推迟到 M1-01。

**依赖**：M0-04。  
**工作量**：M。

**验收标准**

- 编译期断言：内存实现满足全部 Repository 接口。
- 内存实现单测覆盖：get/upsert/search/list/delete 与未找到错误路径。
- 重复 `Upsert` 同一主键不产生重复记录（幂等），单测通过。
- 端口包不 import 数据库驱动。

---

### M0-07 Hertz HTTP/API 骨架

**目标**：用 CloudWeGo Hertz 搭起路由、健康检查与统一错误响应。

**交付物**

- `chat-service/internal/transport/httpapi/router.go`：`func NewRouter(cfg Config) *server.Hertz`，注册路由与全局中间件。
- `chat-service/internal/transport/httpapi/health.go`：`GET /healthz` 处理器。
- `chat-service/internal/transport/httperr/httperr.go`：`errs.Error` → 统一 JSON 错误响应（handler 与 middleware 共用，避免 import cycle）。
- `chat-service/internal/transport/httpapi/hertzlog.go`：把 Hertz 框架日志接入 `slog`，保证日志整体为 JSON。

**接口契约（手写 Go 代码）**

- 路由在 `router.go` 集中注册，业务路由统一挂在 `/v1` 前缀下。
- 请求/响应结构体就近定义在所属 handler 文件（如 `health.go` 的 `HealthResponse`），HTTP 层负责与领域 DTO 互转。
- 统一错误信封固定为 `{"error": {"code", "message", "request_id"}}`，由 `httperr.Write` 生成。

**统一响应约定**

```json
// 200 /healthz
{ "status": "ok", "version": "0.1.0", "request_id": "..." }

// 错误响应
{
  "error": {
    "code": "invalid_argument",
    "message": "query parameter 'top_k' must be > 0",
    "request_id": "..."
  }
}
```

**实现要点**

- 路由/处理器使用 Hertz；**领域层与工具不依赖 Hertz Context**，HTTP 层负责把 Hertz Context 转成项目 `RequestContext` 后注入 `context.Context`。
- `/healthz` 不依赖数据库/模型（M1 起可增加 readiness 探针），M0 保证存活探针可用。
- 统一错误响应由 `httperr.Write` 集中生成，所有处理器都通过返回 `errs.Error` 走同一出口。
- 404 由 `NoRoute` 统一处理，保证未匹配路由也返回同一错误信封。
- M0 不实现业务路由（`/v1/...` 留空壳或注释），但需预留版本化前缀 `/v1`。

**依赖**：M0-01（错误模型来自 M0-03，建议先完成 M0-03）。  
**工作量**：S。

**验收标准**

```bash
curl -s localhost:8080/healthz | jq .
# => { "status": "ok", "version": "...", "request_id": "..." }
curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/nope
# => 404
```

- `/healthz` 返回 200 与结构化 JSON。
- 访问不存在的路由返回统一错误体（404 + `not_found`）。

---

### M0-07A Hertz 中间件与验证

**目标**：建立 binding、validation、recovery、request ID、CORS 等通用中间件，保证非法请求统一失败。

**交付物**

- `chat-service/internal/transport/httpapi/middleware/requestid.go`：从 `X-Request-ID` 读取或生成；写入 context 与响应头 `X-Request-ID`。
- `chat-service/internal/transport/httpapi/middleware/recovery.go`：捕获 panic，记录堆栈，返回 `internal` 统一错误。
- `chat-service/internal/transport/httpapi/middleware/logging.go`：为每个请求创建带 `request_id`/`trace_id` 的 `slog.Logger` 注入 context，记录方法、路径、状态码、耗时。
- `chat-service/internal/transport/httpapi/middleware/cors.go`：可配置白名单的 CORS（不使用 `*` + credentials）。
- `chat-service/internal/transport/httpapi/validation.go`：`Validatable` 接口 + `BindAndValidate` / `BindQueryAndValidate`，通过 Hertz `WithCustomValidatorFunc` 接入绑定与校验。
- 中间件与校验单测：request ID 生成/沿用、recovery、非法请求错误体、CORS 头、`invalid_argument` / `validation_failed` 映射。
**实现要点**

- 中间件顺序建议：`requestid -> logging -> recovery -> cors -> validation`（recovery 应包裹后续可能 panic 的处理器与中间件）。
- 请求 ID 与 trace ID 约定：`request_id` 每个 HTTP 请求唯一；`trace_id` 预留 OpenTelemetry 接入（M0 可先用同一 ID 或从 `traceparent` 解析）。
- 校验错误一律转换为 `validation_failed`（422）或 `invalid_argument`（400），并带上具体字段。
- CORS 默认只允许配置中的来源，不默认 `*` + credentials。

**依赖**：M0-07。  
**工作量**：M。

**验收标准**

- 请求头带 `X-Request-ID: abc` 时，响应头回写 `abc`，日志字段也为 `abc`。
- 不带请求 ID 时自动生成 UUID，响应头可见。
- 触发 panic 的测试路由返回 500 + `internal`，且进程不崩溃、日志含堆栈。
- 非法请求（如 `top_k=-1`）返回统一错误体与对应错误码。
- 上述行为均有单测覆盖。

---

### M0-08 测试基线

**目标**：建立可复用的单元测试、Mock 和夹具，让后续每个里程碑都能"先写测试"。

**交付物**

- `shared/testkit/` 完整化：
  - `mock_provider.go`（M0-05 产出的 Mock）。
  - `memory_repo.go`（M0-06 产出的内存仓库）。
  - `fixtures.go`：固定餐厅、证据、会话、工具调用样本。
- `Makefile` 增加：
  - `make cover` → `go test -coverprofile=coverage.out ./...`
  - `make race` → `go test -race ./...`
- 可选的 `golangci-lint` 配置 `.golangci.yml`（启用 `govet`、`staticcheck`、`errcheck`、`revive`、`depguard` 等，并用 `depguard` 固化领域层禁依赖规则）。
- 单元测试覆盖：
  - 配置校验（M0-02）。
  - 错误码映射与 request context（M0-03）。
  - DTO JSON 往返与架构约束（M0-04）。
  - Mock Provider 行为（M0-05）。
  - 内存 Repository 行为（M0-06）。
  - HTTP 健康检查、错误响应与中间件（M0-07 / M0-07A）。

**实现要点**

- 使用标准库 `testing`；Hertz 可用其官方 `ut` 工具或 `httptest` 风格的路由测试。
- 夹具数据放 `shared/testkit` 或 `testdata/`，不要引用 `data/raw` 下的大文件。
- 测试必须可在无网络、无数据库、无 Ollama 的环境下运行（全部依赖 Mock/内存实现）。
- 覆盖率不作为硬门槛，但核心包（config、errs、domain、port、testkit）应有明确断言。

**依赖**：M0-04（Mock/夹具），实际实施贯穿 M0 全程。  
**工作量**：M。

**验收标准**

```bash
go test ./...
go test -race ./...
go vet ./...
golangci-lint run
```

- 全部通过。
- 测试在断网环境可运行（不依赖数据库 / Ollama / 远端模型）。
- 新增测试夹具可被后续里程碑直接 import 复用。

---

## 5. 推荐执行顺序与并行化

### 5.1 单人执行（串行）

```text
M0-01 -> M0-02 -> M0-03 -> M0-04 -> M0-05 -> M0-07 -> M0-07A -> M0-06 -> M0-08(收尾)
```

> 说明：M0-06 也可在 M0-04 后与 M0-05 并行；M0-08 散布在每个任务中，最后统一收口。

### 5.2 多轨并行（依赖满足后）

| 轨道 | 任务 | 前置 |
|---|---|---|
| 骨架轨 | M0-01 → M0-02 → M0-03 | 无 |
| HTTP 轨 | M0-07 → M0-07A | M0-01（建议 M0-03 先完成以复用错误模型） |
| 领域轨 | M0-04 → M0-05 / M0-06 | M0-01 |
| 质量轨 | M0-08（持续） | 随各任务同步推进 |

> 注意：实施计划 §8 提醒"不要同时推进太多大任务"。M0 任务均为 S/M，建议单人在领域轨与 HTTP 轨之间切换，质量轨随手补充测试。

---

## 6. M0 完成定义（DoD）检查清单

### 6.1 每个任务通用 DoD（继承实施计划 §10）

- [ ] 代码已实现。
- [ ] 单元测试或集成测试已添加。
- [ ] 错误路径有明确错误码。
- [ ] 日志中包含 `trace_id` / `request_id`。
- [ ] 不泄露密钥和 PII。
- [ ] 不绕过领域接口直接调用厂商 API。
- [ ] 文档或注释说明关键设计。
- [ ] 通过 `go test ./...`、`go vet ./...`、`go test -race ./...`（`golangci-lint` 待安装）。
- [ ] 相关验收标准可以实际演示。

### 6.2 M0 里程碑门

- [x] 两个服务可独立构建与运行：`bin/chat-service` 启动 HTTP，`bin/data-pipeline` 执行批处理子命令。
- [x] `chat-service` 的 `GET /healthz` 返回 200 与结构化 JSON。
- [x] 缺少关键配置时启动失败，错误信息点名具体变量（`HTTP_ADDR` / `CHAT_*` / `PIPELINE_*`）。
- [x] 非法请求返回统一错误响应与稳定错误码（404 `not_found`、400 `invalid_argument`、422 `validation_failed`、500 `internal`）。
- [x] request ID 在日志与响应头中可追踪，客户端传入值被沿用。
- [x] 领域层无框架/厂商依赖（`shared/domain/architecture_test.go` 保障）。
- [x] ChatProvider / EmbeddingProvider / RerankProvider 接口就绪且有 Mock。
- [x] Restaurant / Knowledge / Conversation / Memory / Run Repository 接口就绪且有内存实现。
- [x] 数据生产阶段显式占位：`import` / `build-documents` / `embed` 非零退出并说明后续里程碑。
- [x] `go test ./...`、`go vet ./...`、`go test -race ./...` 全绿。
- [ ] `golangci-lint run`（本机未安装，待安装后补跑；领域禁依赖已由架构测试覆盖）。
- [x] 所有验收标准均可实际演示。

---

## 7. 与后续里程碑的衔接

M0 完成后，按实施计划 §14 第 1 组进入 M1。服务归属如下：

| 里程碑 | 主要落点 |
|---|---|
| M1 PostgreSQL 数据底座 | `shared/adapter/repository/postgres` + `data-pipeline/internal/pipeline`（导入、清洗、聚合） |
| M2 Embedding 与文档 | `shared/adapter/embedding/ollama` + `data-pipeline/internal/pipeline`（文档构建、批量向量化） |
| M3 两级检索 | `shared/port`（检索接口）+ `chat-service`（读取链路） |
| M4 Agent 运行时 | `shared/adapter/chat/openai` + `chat-service`（Eino、工具、checkpoint、SSE） |
| M5 纵向切片 | `chat-service`（API 与对话切片），必要时读取 `data-pipeline` 写入的知识库 |
| M6 评测与展示 | `shared/testkit` + `web/`（前端） |

进入 M1 的具体衔接：

1. M1-01 数据库连接：实现 `shared/adapter/repository/postgres`，满足 M0-06 定义的 Repository 接口。
2. M1-02 表结构与索引：基于 M0-04 的领域 DTO 定义存储结构。
3. M1-04 Meta 流式导入：在 `data-pipeline/internal/pipeline` 中实现，使用 M0-02 配置与 M0-03 日志/错误模型。

**服务边界要求**：`data-pipeline` 与 `chat-service` 互不 import，只通过 `shared/` 共享代码。数据生产负责写入（raw → curated → knowledge → embedding），聊天服务负责读取（检索 → 证据 → 回答），两侧共用同一套领域 DTO 和端口，避免数据语义漂移。

**接口稳定性要求**：M0 定义的 `port` 接口是 M1–M4 的契约。若后续必须调整，需同步更新：

- 本文档的接口形状章节。
- `plans/platepilot-implementation-plan.md` 中对应任务的依赖与验收。
- 所有实现该接口的 Mock 与内存实现。

**M0 明确不做的事**（避免范围蔓延）：

- 不实现任何真实业务 API（`/v1/restaurants/search` 等留到 M3/M5）。
- 不接入真实 PostgreSQL（M1）。
- 不实现真实 Ollama / OpenAI 请求（M2/M4）。
- 不引入 Eino（M4）。
- 不实现 SSE（M4-11）。
- 不实现向量索引与检索（M2/M3）。
- 不在 `web/` 写任何前端代码（M6-06）。

---

## 8. 风险与注意事项

| 风险 | 触发点 | 控制措施 |
|---|---|---|
| 过早引入重依赖 | M0 引入 Eino / 数据库驱动 / 向量库却未使用 | 首批依赖只加 Hertz 与标准库；其它依赖随里程碑引入 |
| 两个服务互相耦合 | `data-pipeline` 直接 import `chat-service`（或反向） | 只允许经 `shared/` 共享；code review 与目录结构共同保证 |
| 领域层被污染 | 业务代码直接 import 数据库驱动 / 厂商 SDK | 用 `depguard` 或架构测试固化禁依赖规则；Adapter 只做转换 |
| Provider 接口过度设计 | M0 就把 M4 全部能力塞进接口 | 接口只定义 M0 已知形状；能力用 `Supports*` 声明，细节后补 |
| Mock 与真实实现漂移 | 内存实现与数据库语义不一致 | 内存实现对齐 `ErrNotFound`/`ErrConflict` 语义，M1 用同一套契约测试验收 |
| 配置过早强校验 | M0 因缺少 M1–M4 配置无法启动 | 共享原语只提供 `Validate()`；各服务只强校验自己的必填项 |
| 错误码散落 | 各层自定义错误字符串 | 错误码集中在 `shared/domain/errs`，HTTP 映射集中处理 |
| 测试依赖外部环境 | 单测需要数据库 / Ollama | M0 全部测试使用外部依赖的 Mock/内存实现，保证离线可跑 |
| 目录反复重构 | 未定骨架就写业务 | 先落 M0-01 双服务骨架，再逐层填充 |

---

## 附录 A：环境变量示例（本地开发）

`.env` 位于仓库根目录，被两个服务共用；已配置的环境变量优先于 `.env`。

```dotenv
# --- 共享 ---------------------------------------------------------------
APP_ENV=dev
LOG_LEVEL=info

# PostgreSQL（M1 起需要）
# POSTGRES_DSN=postgres://<user>:<pass>@<host>:<port>/<database>?sslmode=disable
# POSTGRES_DATABASE=platepilot

# 本地 Ollama Embedding（M2 起需要）
# EMBEDDING_PROVIDER=ollama
# OLLAMA_BASE_URL=http://localhost:11434
# EMBEDDING_MODEL=qwen3-embedding:0.6b
# EMBEDDING_DIMENSIONS=1024

# --- chat-service -------------------------------------------------------
HTTP_ADDR=:8080
# HTTP_CORS_ALLOW_ORIGINS=http://localhost:3000

# OpenAI 兼容 Chat Provider（M4 起需要）
# CHAT_PROVIDER=openai_compatible
# CHAT_BASE_URL=https://openrouter.ai/api/v1
# CHAT_API_KEY=<secret>
# CHAT_MODEL=<tool-capable-model-id>

# --- data-pipeline ------------------------------------------------------
# PIPELINE_DATA_DIR=data/raw/google_local
# PIPELINE_BATCH_SIZE=1000
# PIPELINE_WORKERS=4
```
## 附录 B：参考文档

- 实施计划：`plans/platepilot-implementation-plan.md`
- 技术 PRD：`plans/platepilot-technical-prd.md`
  - §3.1 不可破坏的设计原则
  - §7 工具与 API 边界
  - §8 技术栈（§8.3 LLM Provider 抽象、§8.4 OpenAI-Compatible Chat Adapter、§8.5 本地 Qwen Embedding）
  - §9 API 框架约定
