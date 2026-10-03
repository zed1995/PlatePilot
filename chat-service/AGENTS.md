# chat-service — AGENTS.md

通用约定在 [../AGENTS.md](../AGENTS.md)；本文件只写这个服务。

在线侧：HTTP + SSE 对话 API、Agent 运行时、`/admin/v1` 只读运维 API。
**它只读数据库**——任何写库行为属于 `data-pipeline`。

```bash
make run         # :8080
make run-admin   # 额外挂载 /admin/v1/*（loopback 限制）
make test        # 离线：写侧走 shared/store/memory
make eval-retrieval   # 检索指标门禁（需 make -C .. pg-up）
```

## 分层

| 目录 | 职责 | 边界 |
|---|---|---|
| `internal/app` | 组装：config → store → retrieval → HTTP server | **唯一**装配具体实现的包 |
| `internal/httpapi` | 路由、handler、SSE、中间件 | 不含业务规则，只做绑定与错误映射 |
| `internal/httperr` | 统一 HTTP 错误信封 | — |
| `internal/retrieval` | 读路径：三通道检索 → 融合 → rerank → 证据组装 | 不许出现 Eino |
| `internal/agent` | Plan/Tool/Answer 图、工具注册表、模型适配、审计 | **Eino 只准出现在这里** |
| `internal/agent/tools/` | 被 LLM 调用的工具（目前：`search_restaurants`、`restaurant_evidence`） | 复用 retrieval，不重复实现检索 |
| `internal/agent/einomodel`、`toolreg` | 唯一允许碰 Eino message/schema DTO 的两个桥接包 | 图 runtime 本身只传项目自有 DTO |
| `internal/admin` | 只读管理应用层（分页 clamp、keyset cursor） | **不许 import `shared/store/postgres`**，只面向端口 |
| `internal/config` | 本服务的环境变量面 + 脱敏摘要 | — |

这三条由 `internal/agent/architecture_test.go` 和 `internal/admin/architecture_test.go`
（前者还会 walk 整个仓库，防止 `data-pipeline` 将来依赖 Eino）用 AST 强制。
**违反时先想是不是代码放错了包，不要放宽测试。**

## 检索与证据的行为红线

改这块时最容易「看起来更聪明」但产品上是错的：

- 硬过滤（菜系/价格/评分/行政区/半径）在数据库里做，**必须在向量召回之前**；
  只加 `WHERE` 会让 HNSW 退化成顺序扫描，schema 靠每个行政区一个 partial index 兜。
- 每个候选必须能自证：`reasons` 说明是哪个通道、权重多少、贡献多少；
  `trace.channels` 记录**跑过的每个通道**，没跑的记 `ran:false`，不许假装贡献过。
- 既无 filter 又无 text 的请求要拒绝（否则等于按分数返回全库）；未知行政区是
  `400`；查无结果是 `200` + 空数组；文本短于 3 字符拒绝（trigram 匹配不了）。
  这些差别都是刻意的。
- 排序必须**可复现**（禁止 map 顺序 / 时间 / 随机参与）。
- 证据**永不截断**：超 token 预算就整块丢；每家餐厅每类文档最多 3 块（在预算之前应用）；
  无来源返回 `"unknown"` 并进 `warnings`；全部不可用则**报错**，不返回空包
  （空包会被读成「这家没有证据」）。
- `RETRIEVAL_ENABLE_VECTOR=false` 是官方支持的降级方式（没有 embedding provider 时），
  此时 trace 要如实记该通道没跑。

## 其它

- `/v1/**` 的响应体是客户端契约（前端和管理后台直接读 `reasons` / `trace`），
  改字段算 breaking change。
- `ADMIN_ENABLED` 默认 false：关着的时候路由**根本不注册**（404），不是「注册了再拦」。
  `make run-admin` / `ADMIN_ENABLED=true` 只在你本机用。
- 新配置项进 `internal/config`（含默认值、校验、`Redacted()`，密钥不进日志），
  并同步根 `.env.example`。
- 错误一律用 `shared/domain/errs` 的错误码，由 `internal/httperr` 统一映射成 HTTP 状态，
  别在 handler 里自己造状态码语义。
- 面向用户的解释文案用中文。
