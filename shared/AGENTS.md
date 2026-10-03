# shared — AGENTS.md

通用约定（命令、依赖方向、测试政策）在 [../AGENTS.md](../AGENTS.md)；本文件只写
这个库自己的规则。

模块：`github.com/zed1995/platepilot/shared`。只用 Go 工具链即可构建，
通过 `replace ... => ../shared` 被两个服务消费，**这里不 import 任何服务**。

## 包职责

| 路径 | 放什么 | 不放什么 |
|---|---|---|
| `domain/<name>/` | 纯 DTO + 不变量（restaurant、review、evidence、conversation、memory、tool、run、retrieval、search、chat、admin、errs） | 任何框架 / 驱动 / 厂商 SDK，甚至不许在注释里提到存储引擎 |
| `store/` | 仓储**端口**（`repository.go`、`writer.go`、`admin.go`） | 具体实现（在 `postgres/`、`memory/`） |
| `store/memory/` | 离线用的内存实现 | 伪造 pgvector / PostGIS / trigram 的行为 |
| `store/postgres/` | pgx 适配、`migrate.go`、按 store 拆的读写实现 | 迁移之外的 schema 真相（schema 在 `migrations/`） |
| `store/contract/` | memory 与 postgres 共跑的行为契约 | 单适配器专属断言 |
| `embedding/` `rerank/` `chat/` | Provider 端口 + 客户端（`ollama`、`fake`、`openai`） | 业务逻辑 |
| `config/` | 环境变量加载、校验、**脱敏** | 某个服务私有的配置项 |
| `testkit/` | 两个服务测试共用的 mock / fixture | 生产代码 |
| `idgen` `requestctx` `observability/logging` | 横切小工具 | 领域逻辑 |

## 规则

- **domain 层只准依赖标准库和 `shared/domain` 自身**（`domain/architecture_test.go`
  的两个测试用 AST 强制）：它在持久化之上，不认识任何一种存储引擎。
  连注释里都不许出现 `mongo` / `atlas` / `postgres` / `postgis` / `pgvector` /
  `sql`，这是防止某个具体后端悄悄爬回领域层的手段。
- **迁移由 `data-pipeline migrate` 应用**，不由本库或服务启动时自行执行。
  SQL 只写在 `store/postgres/migrations/`。
- 新增仓储方法要同时在 `store/contract` 加 case，`memory` 和 `postgres` 都要实现，
  保证离线与真库跑同一套断言。
- Postgres 相关测试在连不上库时 **skip**（`PLATEPILOT_TEST_POSTGRES_DSN` 指别的实例），
  让上层 `make test` 保持离线可跑。要真跑：`make test-postgres`。
- 新增 Provider 端口时，同时提供可离线使用的实现（`embedding/fake` 就是范例），
  否则所有调用方测试都会挂上外部依赖。

## 常用命令

```bash
make test           # 离线
make test-postgres  # Postgres 契约套件（需 make -C .. pg-up）
make vet && make cover
```
