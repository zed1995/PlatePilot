# data-pipeline — AGENTS.md

通用约定在 [../AGENTS.md](../AGENTS.md)；本文件只写这个 CLI。

批处理侧：**导入餐厅语料的写侧**——把 Google Local 的餐馆与评论读进来，清洗、
生成知识文档与向量。它写的是内容表：`restaurants`、`reviews`、
`review_summaries`、`knowledge_documents`，以及入库审计用的
`ingestion_batches` / `ingestion_rejections`。在线侧的运行时数据（会话、
Agent run 审计、记忆、预约）归 `chat-service` 写，两边表集不重叠；`web/` 不直接连库。
（`boundaries` 目前没有代码写入——`0001_init` 建表，几何由
`data/boundaries/*.geojson` 手工导入，应用侧只读。）
`migrate` 也归它——**DDL 与 schema 变更只在这里**，不要在任何服务启动时自动应用迁移。

```bash
make check-config    # 打印解析后的配置
make migrate         # 应用 ../shared/store/postgres/migrations
make import-sample   # meta(2万) → review(20万) → stats → score → report
make build           # ./bin/data-pipeline
```

## 子命令

| 命令 | 作用 |
|---|---|
| `check-config` | 打印配置并退出 |
| `migrate` | 应用 SQL 迁移（幂等） |
| `import --stage=meta\|review\|stats\|score\|all` | 流式导入 + 派生状态 |
| `prefilter` | 在本地先把不可 join 的评论过滤掉，产出精简语料 |
| `build-documents` | 由清洗后的行生成知识文档 |
| `build-digests` | 生成餐厅级评论理解文档（`DIGEST_ENABLED=true` 才可运行；`DIGEST_PROMPT_VERSION` 选 llm/rules 生成器） |
| `embed` | 调 provider 给活跃文档生成向量 |
| `report --last=N` / `--batch-id=<id>` | 查看运行报告 |

## 包职责

| 路径 | 职责 |
|---|---|
| `main.go` | 命令行表面（flag 解析、子命令分派） |
| `internal/config` | 本 pipeline 的环境变量面 |
| `internal/pipeline` | 阶段编排（import / migrate / documents / embed / prefilter / progress） |
| `internal/pipeline/raw` | gzip JSONL 流式读取 + 原始 schema |
| `internal/pipeline/curate` | **纯函数**：归一化、PII 脱敏、去重、价格/时段/菜系、统计、打分、行政区边界 |
| `internal/pipeline/knowledge` | 知识文档： facts / profile / summary / representative / quality / hash / keywords |
| `internal/pipeline/report` | 每次运行的批次报告 |

## 规则

- `curate` 保持**无 IO 的纯转换**，才能在离线 `go test` 里被逐条验证。
  边界/校验和这类外部输入属于例外，但要显式传入而不是在函数里读文件。
- **PII 必须先脱敏再落库/导出**；拒绝原因只存行号与原因码，**不存评论正文**。
- 迁移**必须幂等**，`shared/store/postgres` 的测试会断言二次运行不施加任何版本。
- 全量 review 场景很贵（原始 2.5 GB / 3350 万行，约 12.7% 可入库）：先 `prefilter`
  再 `import --stage=review --data-dir=data/processed --review-file=review-filtered.json.gz`。
  输出先写 `.partial` 再 rename，中断不留半个语料——**新增写文件的 stage 沿用这个模式**。
- 长任务的进度打到 **stderr**，按**已读压缩字节**算百分比（总行数事先未知），
  节流到 32 MiB 且不快于 2 s；正常报告走 stdout。加 `--quiet` 开关。
- `--limit` 限制的是**从源文件读的行数**，不是写入量：源数据是全美的，保留五个行政区
  后再按 `gmap_id` join，实际写入远少于 limit。别拿它估算产量。
- 行政区标注：几何文件缺 → 降级为近似包围盒并告警（差异率在入库存活行上是 24%，
  所以这个降级不能静默）；正式/定时跑批用 `--require-boundaries`
  / `PIPELINE_REQUIRE_BOUNDARIES=true` 让它直接失败。校验和钉在
  `curate.DefaultBoundarySHA256`，改数据源要同时改它。
- `data/raw/`、`data/processed/` 都是 git-ignored，重导 restaurants 之后 `prefilter`
  要重跑（join 集合来自当前 `restaurants`）。
- 每个 stage 写一份可查询的批次报告；拒收要能追溯。
