# PlatePilot 技术需求文档

> PlatePilot：Evidence-grounded Restaurant Discovery Agent

> 版本：v0.14  
> 日期：2026-10-05  
> 项目定位：练手型、Agent 核心、Go 实现、远端 Chat Provider 负责聊天模型、PostgreSQL（pgvector + PostGIS）负责数据和向量  
> 本次重点：Eino Agent 编排、OpenAI-Compatible Chat Provider、本地 Qwen Embedding、PostgreSQL（pgvector + PostGIS）和 Go 技术栈
> 实施计划：[platepilot-implementation-plan.md](platepilot-implementation-plan.md)

## 1. 项目目标与边界

### 1.1 一句话定义

构建一个支持中英文对话的餐厅搜索、推荐与证据问答 Agent：

1. 使用自然语言理解用户的餐厅需求和预约条件。
2. 先通过结构化数据和混合检索召回餐厅，再通过 RAG 生成有来源的解释。
3. 可选地通过 Mock Provider 演示库存、锁桌、创建、取消和改期预约。
4. 模型负责理解、规划和表达，数据库与领域服务负责确定性查询和写入。
5. 整个流程可回放、可观测、可评测，方便展示 Agent 工程能力。

### 1.2 核心目标

- **Agent 优先**：覆盖规划、工具选择、状态恢复、记忆、RAG、人工确认和错误恢复。
- **RAG 为核心能力**：支持结构化过滤、向量检索、证据组装、引用和评测。
- **预约作为可选工具**：保留 Mock 预约能力，但不让预约交易复杂度主导架构。
- **字段丰富**：充分利用当前数据中的分类、价格、评分、营业时间、属性、描述和评论。
- **链路清晰**：明确 raw -> curated -> knowledge -> embedding -> retrieval 的写入链路。
- **RAG 可解释**：回答餐厅体验类问题时返回证据、来源和数据时间。
- **交易可安全演示**：把库存和预约实现为可选 Mock 工具，不伪装成真实预订。
- **技术栈不过度**：优先使用 Go、PostgreSQL（pgvector + PostGIS）、Eino 和本地 Qwen；模型、RAG 与 Agent 状态保持清晰的模块边界。

### 1.3 非目标

- 不接入真实 OpenTable、Resy、餐厅 POS 或电话预约系统。
- 不把预约事务作为 MVP 的核心验收目标。
- 不处理真实支付、押金、短信和信用卡。
- 不追求 2021 年数据的实时正确性。
- 不建立生产级多城市、多租户、权限和合规平台。
- 不引入独立搜索引擎或向量数据库；知识文档、向量、地理检索与 Agent 运行数据统一存放在 PostgreSQL。
- 不在 MVP 阶段 Fine-tune 模型。
- 不在 MVP 阶段引入 Kafka、Elasticsearch、Neo4j 等额外基础设施。
- 不把评论文本中的指令当作系统指令执行。

### 1.4 正确性策略

由于项目重点不是生产级事实真实性，采用以下折中策略：

- 所有快照事实统一标记 `observed_at=2021-09`。
- 实时营业、桌位、预约和取消政策统一标记为 `mock`。
- 字段缺失按“未知”处理，不强行从模型常识补全。
- 对可能影响预约结果的硬条件，仍必须经过确定性校验。
- 检索结果必须带来源和证据 ID，保证演示时能够解释。

## 2. 当前数据基线

### 2.1 数据文件与规模

| 数据 | 文件 | 类型 | 规模 | 用途 |
|---|---|---|---:|---|
| Google Local Meta | `data/raw/google_local/meta-New_York.json.gz` | gzip JSONL | 272,189 行，270,720 个唯一 `gmap_id` | POI、餐厅事实、属性、营业时间 |
| Google Local Review | `data/raw/google_local/review-New_York.json.gz` | gzip JSONL | 33,459,761 条；约 499 万条可关联目标餐饮场所 | 评论体验、主题、代表性证据 |
| Demo Restaurant | `data/demo/demo_restaurants.json` | JSON Array | 5 家餐厅，每家 3 条样例评论 | 展示 curated schema，不作为正式语料 |
| 数据处理脚本 | `scripts/explore_meta.py` 等 | Python | 3 个探索/样例脚本 | 当前仅用于数据理解；正式导入 pipeline 用 Go 重写 |

按当前业务筛选口径，可得到约 **17,763 家餐饮相关场所**。该数字包含餐厅、酒吧、咖啡馆、面包店等，可作为练手项目的搜索范围。

评论侧有：

- 约 **4,989,572 条**评论可关联到上述餐饮场所。
- 约 **2,757,510 条**评论具有非空文本。
- 文本总字符数约 **4.25 亿**。
- 文本中位数约 85 字符，约 187 万条不少于 50 字符。
- 评论时间主要集中在 2017–2021 年。

### 2.2 Meta 原始 Schema

每行对应一个 Google Maps POI：

| 字段 | 类型 | 示例/内容 | 用途 |
|---|---|---|---|
| `name` | string | `Joe's Pizza` | 餐厅名称 |
| `address` | string/null | `7 Carmine St, New York, NY...` | 地址、展示和简单位置解析 |
| `gmap_id` | string | `0x89c...:0x...` | 外部稳定 ID |
| `description` | string/null | 短描述 | 语义检索、餐厅简介 |
| `latitude` | float | `40.73` | 坐标 |
| `longitude` | float | `-74.00` | 坐标 |
| `category` | string[] | `["Pizza restaurant", "Restaurant"]` | 菜系、场所类型和过滤 |
| `avg_rating` | float | `4.5` | 排序和筛选 |
| `num_of_reviews` | int | `9998` | 热度、排序；头部可能截顶 |
| `price` | string/null | `$`、`$$`，少数异常货币字符 | 价格等级 |
| `hours` | array[array] | `[["Monday", "11AM–10PM"]]` | 营业时间快照 |
| `MISC` | object/null | 属性主题到字符串数组 | 场景、设施、服务、支付等 |
| `state` | string/null | `Open`、`Closed`、`Permanently closed` | 2021 快照状态 |
| `relative_results` | string[] | 相似 POI ID | 近邻关系，可选增强 |
| `url` | string | Google Maps URL | 来源链接 |

`MISC` 中可利用的主题包括：

```text
Service options
Accessibility
Amenities
Planning
Payments
Offerings
Dining options
Atmosphere
Crowd
Popular for
Highlights
Health & safety
From the business
```

### 2.3 Review 原始 Schema

| 字段 | 类型 | 用途 | 处理策略 |
|---|---|---|---|
| `user_id` | string | 原始评论人标识 | 不进入 RAG 文档和向量库 |
| `name` | string | 评论人姓名 | 不进入 RAG 文档 |
| `time` | int | Unix 毫秒时间戳 | 转为 `reviewed_at` |
| `rating` | int | 1–5 星 | 评分分布、过滤和重排 |
| `text` | string/null | 评论正文 | 主题抽取、摘要和代表性证据 |
| `pics` | array/null | 图片链接 | MVP 不索引 |
| `resp` | object/null | 商家回复 | 可选衍生字段，不默认展示 |
| `gmap_id` | string | 关联 Meta | 关联餐厅 |

由于原始评论没有 `review_id`，curated 层可以通过 `sha256(gmap_id + user_id + time + text_hash)` 生成确定性评论 ID。`user_id` 只参与哈希计算，不写入 RAG 可检索数据。

### 2.4 可衍生字段

在当前数据基础上可生成：

- `cuisine_tags`
- `price_level`
- `rating_bucket`
- `review_count_bucket`
- `borough_guess`
- `neighborhood_guess`
- `attribute_flags`
- `atmosphere_tags`
- `popular_for_tags`
- `service_option_tags`
- `accessibility_tags`
- `hours_weekday`
- `review_rating_distribution`
- `review_recency`
- `review_sentiment`
- `review_topic_summary`
- `representative_reviews`
- `snapshot_status`
- `source_provenance`

这些字段是提升 RAG 检索质量和展示丰富度的核心，不需要额外引入外部数据源。

### 2.5 数据限制

以下限制不阻塞练手项目，但必须在文档、接口和 UI 中显式说明：

- 数据是 2021-09 快照，不代表当前营业状态。
- 地理位置是近似 bbox，不是严格的曼哈顿行政边界。
- 部分类别存在误匹配，需要 curated 层纠正。
- 价格、描述、营业时间和属性存在缺失。
- 原始数据没有电话、官网、预约链接和取消政策。
- 原始数据没有实时桌位库存。
- 评论带有用户 PII、短文本、偏置和潜在 prompt injection。
- `num_of_reviews` 头部有截顶风险，不应当作精确评论数。

## 3. 功能需求

优先级：P0 为 MVP 必须完成，P1 为增强项，P2 为后续扩展。

### 3.1 Agent 核心能力

**P0**

- 解析用户意图、任务类型、槽位和约束。
- 根据当前状态选择下一步工具，而不是固定执行整条预约流程。
- 支持搜索、RAG 证据查询和可选预约工具之间的路由。
- 使用显式状态和 checkpoint 保存当前节点、待执行动作和错误状态。
- 支持工具失败后的重试、降级、澄清和终止。
- 支持短期上下文和长期偏好，但必须区分“当前会话条件”和“长期记忆”。
- 对模型输出做结构化校验，不能直接执行自然语言中的副作用。
- 每次运行记录 `trace_id`、节点、模型、工具、证据和错误。

**P1**

- 支持多步计划、计划修改和上下文压缩。
- 支持模型能力路由：简单任务用本地小模型，复杂任务切换云端大模型。
- 支持候选方案比较和可解释的工具选择。
- 支持运行回放和失败用例自动归类。

### 3.2 餐厅搜索与推荐

**P0**

- 支持按餐厅名、菜系、地点、价格、评分、评论数和关键词搜索。
- 支持结构化条件过滤，例如：
  - `cuisine_tags`
  - `price_level`
  - `avg_rating`
  - `source_review_count`
  - `stored_review_count`
  - `attribute_flags`
  - `location_scope`
- 支持中文和英文自然语言条件抽取。
- 支持“适合约会”“安静”“适合带孩子”等软条件，并明确标记为评论推断。
- 支持按来源评论量、入库评论量、评分、距离、价格和语义相关性排序。
- 返回餐厅摘要、匹配原因、数据时间和来源链接。

**P1**

- 支持候选解释：满足哪些条件、哪些条件未知、排序依据是什么。
- 支持同名餐厅消歧。
- 支持相似餐厅推荐。
- 支持候选结果的分页和会话内继续筛选。

### 3.3 Restaurant RAG 与证据问答

**P0**

- 支持回答餐厅特色、菜品、环境、服务、价格感受、等待时间和适合场景。
- 对体验类问题使用评论和描述作为证据。
- 对结构化事实优先查询数据库，不由模型凭空生成。
- 每条关键回答返回证据 ID、来源、评论时间和餐厅 ID。
- 支持原始评论中的恶意内容隔离，检索文本不得改变 Agent 指令。
- 没有足够证据时返回“无法从现有数据确认”。

**P1**

- 生成评论主题摘要，例如菜品、服务、环境、性价比、排队和儿童友好。
- 支持按时间窗口检索评论。
- 支持正负面观点并列和冲突证据展示。
- 支持引用代表性评论片段而不是整篇长评论。

### 3.4 可选 Mock 库存与预约

**P1**

- 根据餐厅、日期、人数和时间窗口查询 Mock 库存。
- 返回 `slot_id`、时间、容量、状态、hold 过期时间和政策版本。
- 创建预约前必须展示最终摘要并等待明确确认。
- 使用短时 hold，确认后生成预约编号。
- 支持取消和改期。
- 所有写操作用 `idempotency_key` 保证重复请求安全。
- 明确区分：
  - 无库存
  - 餐厅关闭
  - 参数错误
  - Provider 超时
  - 过期 hold

**P2**

- 故障注入：延迟、超时、错误响应、库存冲突和重复回调。
- 改期使用“新预约成功后取消旧预约”的补偿式流程。
- 并发抢同一桌位时，最多允许满足容量的请求成功。

### 3.5 记忆与偏好

**P0**

- 保存当前线程状态、槽位、候选和待确认动作。
- 保存用户明确要求记住的菜系、区域、预算和饮食偏好。
- 每条长期偏好保留来源和更新时间。
- 支持查看、修改和删除偏好。
- 不把单次搜索条件自动升级为长期偏好。

### 3.6 演示与管理

**P0**

- 展示数据快照时间、导入批次、记录数和处理状态。
- 展示餐厅证据、RAG chunk、工具调用和预约状态。
- 提供 Mock 库存查看和重置能力。

**P1**

- 展示 trace、节点耗时、token、检索候选和最终引用。
- 支持故障注入开关。
- 支持对话回放和评测结果查看。

## 4. 数据架构与 Schema

### 4.1 存储分层

一个 PostgreSQL 实例同时承载内容数据、向量数据、地理检索和 Agent 运行数据，本地只运行 Go 服务、PostgreSQL 和 Qwen 模型服务，不需要独立的搜索引擎或向量数据库：

```text
Google Local Raw Data
       |
       v
内容表
  restaurants            # 含内嵌的 hours / attributes_raw / relative_results（§4.4）
  reviews
  review_summaries
       |
       v
知识表（含向量）
  knowledge_documents    # pgvector，按 borough × retrieval_scope 分区索引
  user_memories
       |
       v
Agent 运行表
  conversations
  conversation_checkpoints
  conversation_messages
  conversation_candidates
  agent_runs
  tool_calls
  run_nodes
       |
       v
可选 Mock 预约表
  reservation_slots
  reservations
       |
       v
地理与导入审计
  boundaries
  ingestion_batches / ingestion_rejections
```

扩展由 `0001_init.sql` 建立：`vector`（pgvector）、`postgis`、`pg_trgm`。

### 4.2 表总览

Schema 由 `shared/store/postgres/migrations/` 下的版本化 SQL 建立，`make migrate` 幂等执行
（已应用版本记在 `schema_migrations`）。

| 表 | 用途 | 主要索引 |
|---|---|---|
| `restaurants` | 餐厅结构化主数据（hours / attributes_raw / relative_results 为内嵌列） | `source_record_id` 唯一、`location` GiST、`cuisine_tags` GIN、`price_level`、`rating_source_avg DESC`、`is_active_for_demo` 部分索引 |
| `reviews` | 精选评论和必要元数据 | `(restaurant_id, reviewed_at DESC)`、`(restaurant_id, text_hash, rating, reviewed_at)` 唯一 |
| `review_summaries` | 预计算主题和情绪摘要 | 主键 `(restaurant_id, topic)` |
| `knowledge_documents` | 可嵌入知识 chunk（pgvector） | `(restaurant_id, retrieval_scope, doc_type, content_hash)` 唯一、按 borough × retrieval_scope 的 HNSW 部分索引 |
| `user_memories` | 长期偏好 | `(user_id, updated_at DESC) WHERE deleted_at IS NULL` |
| `conversations` | 会话元数据 | `(user_id, updated_at DESC)` |
| `conversation_checkpoints` | 可恢复 Agent 状态（每线程一行，含挂起动作） | 主键 `thread_id` |
| `conversation_messages` | 线程消息，供历史回放与分页 | 唯一 `(thread_id, seq)`、`(thread_id, seq DESC)` |
| `conversation_candidates` | 最近一次搜索的候选快照，"第二家"的位置语义靠它 | 主键 `(thread_id, position)` |
| `agent_runs` | 单次运行与状态轨迹 | `trace_id` 唯一、`(thread_id, started_at DESC)`、`started_at DESC` |
| `tool_calls` | 工具调用审计 | `(run_id, created_at)` |
| `run_nodes` | 节点级 trace：一次运行经过的每个图节点 | 唯一 `(run_id, seq)`、`(trace_id, seq)` |
| `reservation_slots` | Mock 库存 | `(restaurant_id, slot_date, slot_time)`、`CHECK (booked <= capacity)` |
| `reservations` | Mock 预约 / hold | `idempotency_key` 唯一、`(hold_expires_at) WHERE status='held'` |
| `boundaries` | 行政区多边形（NYC DCP） | `geom` GiST、`name` GIN(pg_trgm) |
| `ingestion_batches` / `ingestion_rejections` | 导入审计与拒绝记录 | `(stage, started_at DESC)`、`batch_id` |

RAG 与 Agent 的评测样本是仓库内的测试数据
（`chat-service/internal/retrieval/testdata`、`chat-service/internal/agent/testdata`），
不落库为 `evaluation_cases` 表；导入审计的拒绝原因按开放式 `jsonb` 计数存
`ingestion_batches.reject_reasons`，不为每种原因加列。

### 4.3 `restaurants`

一行一店，读取主信息不需要第二次查询：

| 列 | 类型 | 来源 / 说明 |
|---|---|---|
| `id` | `bigint identity` | 内部代理键；外部标识见下一行 |
| `source` / `source_record_id` | `text` | `google_local_2021` / Google `gmap_id`，后者唯一（重导入是 upsert） |
| `name` / `address` | `text` | 名称、地址；两列都有 `pg_trgm` GIN 支持模糊匹配 |
| `borough` | `text` | 导入时由 DCP 多边形判定；仅 `manhattan`/`brooklyn`/`queens`/`bronx`/`staten_island`，区外为 `NULL` |
| `location` | `geography(Point,4326)` | PostGIS；距离以米为单位，`ST_DWithin` 走 GiST |
| `categories` / `cuisine_tags` | `text[]` | 原始 `category` 与归一化菜系；后者有 GIN 索引 |
| `description` | `text` | 短描述，进语义文档 |
| `price_raw` / `price_level` | `text` / `smallint` | 异常货币字符保留在 `price_raw`；`price_level` 限 1–4 |
| `rating_source_avg` | `double precision` | Meta `avg_rating` |
| `rating_computed_avg` / `rating_count` | `double precision` / `integer` | 由入库评论聚合；样本足够时才可引用 |
| `source_review_count` | `integer` | Meta `num_of_reviews` |
| `source_review_count_capped` | `boolean` | 头部截顶标识（9,998 不是精确值） |
| `stored_review_count` | `integer` | `reviews` 表中实际保存的评论数 |
| `text_review_count` | `integer` | 有有效文本的评论数 |
| `representative_review_count` | `integer` | 被选为代表证据的评论数 |
| `embedded_review_count` | `integer` | 进入 RAG 文档的评论数 |
| `last_reviewed_at` / `stats_updated_at` | `timestamptz` | 评论时间下界与统计刷新时间 |
| `attributes` / `attributes_raw` | `jsonb` | 规范化属性（三态）与原始 `MISC` |
| `hours` | `jsonb` | 营业时间快照，保留原始文本 |
| `relative_results` | `text[]` | 原始近邻 POI ID，未用于召回 |
| `snapshot_status` | `text` | 2021 快照状态，不当作实时状态 |
| `knowledge_score` | `double precision` | 字段与评论覆盖度打分，决定谁进 embedding 批次 |
| `is_active_for_demo` | `boolean` | 演示集开关；检索的硬过滤与多个部分索引都挂在它上面 |
| `observed_at` | `timestamptz` | 统一 `2021-09` |
| `source_url` / `created_at` / `updated_at` | `text` / `timestamptz` | 来源链接与审计时间 |

关键设计：

- 三态属性使用 `"true"`、`"false"`、`"unknown"`，避免把缺失当 false。
- `location` 用 `geography` 而不是 `geometry`，因为产品推理的单位是米而不是度数。
- `source_review_count`、`stored_review_count`、`text_review_count`、`embedded_review_count`
  语义不同，不能互相覆盖。
- 表上的 `CHECK` 约束限定 `price_level` 的取值、`borough` 的枚举，以及 `location`
  必须落在服务区 bbox 内（区外坐标在导入时就被拒，约束是第二道防线）。

### 4.4 附属资料：内嵌进 `restaurants`

> **决策（2026-09-29，M1 实现）**：原设计的独立 `restaurant_documents` 表**不再存在**。
> 实测附属文档数量约为主表的 3 倍，而且只写不读、读取时总要和餐厅一起取，
> 因此按"读在一起的放在一起"改为内嵌列：
> `restaurants.hours`（`jsonb`，保留原始营业时间文本）、
> `restaurants.attributes_raw`（原始 `MISC`）、
> `restaurants.relative_results`（`text[]`）。
> 规范化后的属性写 `restaurants.attributes`，供检索过滤使用。

原设计里 `document_type` 的取值（`hours` / `attributes_raw` / `description` /
`relative_results` / `source_snapshot`）也随之失效：它们现在分别对应上面三个内嵌列、
`restaurants.description`，以及 `source` / `source_record_id` / `observed_at` 这三个来源列。

### 4.5 `reviews`

只保存经过清洗的评论；评论人姓名、`user_id` 和图片链接不进入本表。

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | `bigint identity` | 代理键，不是外部 ID |
| `restaurant_id` | `bigint` | 外键，`ON DELETE CASCADE` |
| `rating` | `smallint` | 1–5，`CHECK` 约束 |
| `reviewed_at` | `timestamptz` | 由原始 `time`（Unix 毫秒）转换 |
| `text` | `text` | 正文；空文本默认 `''` |
| `language` | `text` | 标记语言 |
| `text_hash` | `text` | 正文摘要，参与幂等键 |
| `is_representative` | `boolean` | 是否被选为代表证据（部分索引挂在它上面） |
| `topic_tags` | `text[]` | 主题标签（`food` / `service` / `ambience` …） |
| `source_observed_at` | `timestamptz` | 统一 `2021-09` |

幂等键这里有一处纠正。早期设计把评论主键取为
`sha256(gmap_id + user_id + time + text_hash)`，并靠 `ON CONFLICT (id)` 去重；
改用数据库自增 id 后主键不再可用来去重，幂等因此改为唯一索引
`(restaurant_id, text_hash, rating, reviewed_at)`。在全量 4.15M 行语料上验证过：
两种键选出的行完全一致（`rating` 与 `user_id` 都由正文决定，没有任何分组出现分歧），
所以这次替换没有语义损失。

原始数据如需审计，放在权限更高的对象存储或受限表中，不进本表。

### 4.6 `review_summaries`

按主题聚合，主键是 `(restaurant_id, topic)`。整批重算、整批读取，因此独立于 `reviews`：

| 列 | 类型 | 说明 |
|---|---|---|
| `restaurant_id` / `topic` | `bigint` / `text` | 复合主键 |
| `sentiment` | `double precision` | 该主题的情绪得分 |
| `positive_ratio` | `double precision` | 正面占比 |
| `summary` | `text` | 主题摘要文本 |
| `evidence_count` | `integer` | 参与本主题的评论数 |
| `valid_from` / `valid_to` | `timestamptz` | 有效时间窗（`valid_to` 为空表示仍在生效） |
| `generated_by` / `generated_at` | `text` / `timestamptz` | 生成者（`<provider>/<model-id>` 或规则版本）与生成时间 |

主题建议包括：

- `food`
- `service`
- `ambience`
- `value`
- `wait`
- `kid_friendly`
- `group_friendly`
- `accessibility`
- `outdoor`

#### 评论数量和评分口径

评论数量必须区分以下字段：

| 字段 | 来源 | 用途 | 限制 |
|---|---|---|---|
| `source_review_count` | Meta `num_of_reviews` | 热度、筛选和展示参考 | 头部可能截顶，例如 9,998 |
| `source_review_count_capped` | 加工规则 | 标识是否可能截顶 | 不能用于精确统计 |
| `stored_review_count` | `reviews` 表聚合 | 表示实际保存的评论数 | 只代表入库数据 |
| `text_review_count` | `reviews` 表聚合 | 表示有有效文本的评论数 | 更适合 RAG 覆盖度 |
| `representative_review_count` | 代表评论选择逻辑 | 表示可用于展示的证据数 | 不是原始总评论数 |
| `embedded_review_count` | 文档构建结果 | 表示参与 embedding 的数量 | 不是原始总评论数 |
| `rating_source_avg` | Meta `avg_rating` | 来源评分 | 与抽样评论计算值可能不同 |
| `rating_computed_avg` | `reviews` 表聚合 | 入库评论平均分 | 仅在评论样本足够时有参考性 |

这些聚合直接物化为 `restaurants` 上的列，而不是每次搜索时对 `reviews` 做 `COUNT(*)`
或 join：它们在几乎每次取餐厅时都要读，join 的成本超过那点存储节省。
由 pipeline 在导入/回填时用一条 `UPDATE … FROM (SELECT …)` 刷新，并同步写
`stats_updated_at`。

判断规则：

1. 用户搜索“评论多”“很热门”时，优先使用 `source_review_count`。
2. 用户问“有多少条评论被 RAG 收录”时，使用 `stored_review_count` 或 `embedded_review_count`。
3. 用户问“平均分”时，优先展示 `rating_source_avg`，并标明是 2021 快照。
4. 如果只想展示当前知识库评论样本的平均分，使用 `rating_computed_avg`，不要伪装成原始平均分。
5. `reviews` 表只负责保存证据，不负责在热查询时实时计算总数。

### 4.7 `knowledge_documents`

这是向量检索的核心表，维数在表定义上固定：

| 列 | 类型 | 说明 |
|---|---|---|
| `document_id` | `bigint identity` | 主键 |
| `restaurant_id` | `bigint` | 外键 |
| `retrieval_scope` | `text` | `restaurant` 或 `evidence`，`CHECK` 约束 |
| `doc_type` | `text` | 见下方取值 |
| `title` / `content` | `text` | 可读标题与正文（正文是 embedding 的输入） |
| `content_hash` | `text` | 内容摘要，参与幂等键 |
| `embedding` | `vector(1024)` | pgvector；`qwen3-embedding:0.6b` |
| `embedding_model` / `embedding_dimensions` | `text` / `integer` | 记录生成向量的模型与维数，防止切换模型后混用旧向量 |
| `borough` | `text` | 从餐厅反规范化下来，使部分索引的谓词是本地列判断而不是回表 join |
| `metadata` | `jsonb` | 检索过滤用的餐厅字段（菜系、价格、评分、主题、快照时间、来源） |
| `source_record_ids` | `text[]` | 来源 `gmap_id` 与评论标识 |
| `snapshot_at` | `timestamptz` | 数据观测时间 |
| `version` | `integer` | 同一文档内容变化时递增，旧版本留在表里但 `is_active=false` |
| `is_active` | `boolean` | 只有 active 文档可被召回；`CHECK` 强制 active 的行必须有向量 |

推荐 `doc_type`：

- `restaurant_profile`
- `restaurant_attributes`
- `restaurant_hours`
- `restaurant_review_summary`
- `restaurant_representative_reviews`

两个 scope 共用一张表，靠 `retrieval_scope` 分开：

- `restaurant`：每个餐厅一条餐厅级语义摘要，用于召回候选餐厅。
- `evidence`：每家餐厅多条事实或评论证据，用于回答和引用。

**常见做法在这里不成立**：直觉上"一个向量索引 + 查询时加 scope 过滤"就够了，
实测却会**返回空结果**而不是慢结果。在 30,198 条 active 文档、manhattan 分区上，
`restaurant` 只有 2,010 条而 `evidence` 有 18,287 条；HNSW 是定宽束搜索，
它取回约 `ef_search`（默认 40）个近邻就停，不会为了过滤条件继续往下找。
于是 scope=`restaurant` 的召回扎进一个 90% 是 evidence 的分区，取回的 40 个近邻几乎
全是 evidence，scope 过滤把它们全部丢掉，查询返回 0 行——而同一条查询禁用索引后
能在 42ms 内返回 5 行。所以索引必须按 `(borough, retrieval_scope)` 分区（§4.10），
把召回的谓词本身放进索引。

禁止跨餐厅拼接同一个 chunk。批内 upsert 的幂等键是
`(restaurant_id, retrieval_scope, doc_type, content_hash)`：内容没变就跳过，
内容变了就新增一个版本而不是覆盖旧行。这个键刻意不含 `is_active` 和 `version`，
因为这两列在文档生命周期里会变，把它们纳入键就正好废掉了重建所依赖的幂等性。

### 4.8 Agent 运行表

会话模型是**规范化**的，没有塞进一个 JSON blob：消息回放与 checkpoint 恢复是两条独立
路径，`(thread_id, seq)` 给历史分页一个走索引的顺序；每个查询都带 `thread_id`
或 `user_id`，一个会话的行不可能出现在另一个会话里。

#### `conversations`

线程元数据：`thread_id`（主键）、`user_id`、`title`、`current_state`
（`idle` / `awaiting_clarification` / `awaiting_confirmation` / `completed` / `failed`，
`CHECK` 约束）、`created_at`、`updated_at`、`last_message_at`。

#### `conversation_checkpoints`

每线程一行，是状态恢复的最终来源：

| 列 | 说明 |
|---|---|
| `thread_id` | 主键，外键指向 `conversations` |
| `version` | 单调递增；保存时的 `UPDATE` 带 `WHERE version < $new`，过期写入影响 0 行 |
| `state` | 与 `conversations.current_state` 同一枚举 |
| `pending_action` / `missing_slots` | 待执行动作与待补槽位 |
| `evidence_ids` / `selected_restaurant_id` | 本轮证据与指代解析出的餐厅 |
| `pending_tool_call_id` / `pending_arguments` | 已请求但未获批准的写调用（挂起动作） |
| `clarification_count` | 本线程已澄清次数；连续澄清上限靠它，`CHECK` 不许为负 |
| `created_at` | — |

挂起动作存在这里而不是进程内存里，是因为确认是**后一个请求**，可能跨重启到达。
Eino 负责运行时图结构，但恢复时业务状态必须从这张表读。

#### `conversation_messages`

线程消息，供历史回放与分页：`message_id`（主键）、`thread_id`、`role`
（`user` / `assistant` / `tool`）、`content`、`tool_calls`（`jsonb`）、
`evidence_ids`（`bigint[]`）、`seq`（线程内自增位置，`UNIQUE (thread_id, seq)` 是并发追加的兜底）、
`created_at`。

#### `conversation_candidates`

最近一次搜索的候选快照，一行一个 `(thread_id, position)`：
`restaurant_id`、`name`、`score`、`reasons`（`text[]`）、`snapshot_at`、`created_at`。
复合主键就是全部身份——两个线程不会共用位置，一个线程也不会在位置 2 上放两家餐厅，
"第二家"因此是一个确定性引用。产生候选的那一轮整组覆盖，没产生候选的那一轮不动它。

#### `agent_runs`

一次运行一行：`run_id`（主键）、`trace_id`（唯一）、`thread_id`、`status`
（`running` / `succeeded` / `failed` / `cancelled`）、`model_provider`、`model_name`、
`started_at`、`finished_at`、`latency_ms`、`token_input`、`token_output`、
`retrieval_count`、`tool_call_count`、`error_code`。

#### `tool_calls`

一次工具调用一行：`call_id`（主键）、`run_id`（外键，`ON DELETE CASCADE`）、
`tool_name`、`arguments`（`jsonb`，**只保存脱敏摘要**：白名单业务字段、截断）、
`result_summary`、`status`（`ok` / `error`）、`latency_ms`、`created_at`。
联系方式、自由格式标识和密钥永不进入这一列。

#### `run_nodes`

节点级 trace，补上 `agent_runs` 和 `tool_calls` 都答不了的那个问题——"哪一步失败了"。
不调用工具的节点（`plan` 与 `answer`，恰好是两次模型调用）在 `tool_calls` 里没有行，
于是一次在 `answer` 里失败的运行，在审计上和在 `plan` 里失败的运行长得一模一样。
本表一次运行经过的每个节点一行：`node_id`、`run_id`、`trace_id`、`node`、`seq`、
`status`、`started_at`、`latency_ms`、`detail`（`jsonb`，节点自己写的候选数、工具名、
结束原因）、`error_code`。排序键是 `seq` 而不是 `started_at`：一次快运行的相邻节点会落在
同一毫秒里，依赖时钟精度的顺序是会变的顺序。

#### `user_memories`

`memory_id`（主键）、`user_id`、`memory_type`（`preference` / `constraint` / `fact`）、
`content`、`source`、`confidence`、`embedding`（`vector(1024)`，可空）、
`created_at`、`updated_at`、`deleted_at`。

删除是**软删除**：审计留着行，`List` 只看 `deleted_at IS NULL` 的行。
长期偏好必须由用户明确要求保存，并支持查看、修改和删除。

### 4.9 可选 Mock 预约表

预约不是系统核心。演示工具调用时用两张表：

- `reservation_slots`：Mock 库存。一行一个 `(restaurant_id, slot_date, slot_time)` 时段，
  `capacity` 与 `booked` 都是普通计数——没有真实桌位图可建模，库存由应用按模板生成而不是导入。
- `reservations`：hold 与预约。`status` ∈ `held` / `confirmed` / `cancelled` / `expired`，
  另有 `hold_expires_at`、`party_size`、`idempotency_key`。

实现原则：

- 单 slot 用**条件更新**保证原子性：`UPDATE … SET booked = booked + $n WHERE slot_id = $id AND booked + $n <= capacity`。
- `CHECK (booked >= 0 AND booked <= capacity)` 是第二道防线：即使调用方写错了条件，
  超卖的更新也会被行本身拒绝，而不是只被那条语句的措辞拒绝。
- `reservations.idempotency_key` 唯一。键由服务端铸造（`thread_id + action + request_id +
  sha256(arguments)`），不用 checkpoint 版本——记录确认结果这个动作本身会推进版本，
  于是"重试"恰好重算不出同一个键，而重试正是幂等唯一存在的场景。
- 只有确认后才写 `confirmed`；确认前只有 `held`。
- hold 过期由清扫器把 `status='held'` 且超过 `hold_expires_at` 的行置为 `expired`。
- 预约表不影响 RAG 与 Agent 主链路；`RESERVATION_ENABLED=false`（默认）时相关工具根本不注册。

### 4.10 pgvector HNSW 索引（按 borough × retrieval_scope 分区）

`knowledge_documents.embedding` 是 `vector(1024)`，距离用余弦，索引是 HNSW
（`vector_cosine_ops`）。这里有一条被实测推翻的直觉，值得写下来：

**给向量查询加 `WHERE` 不会保留索引。** pgvector 的 HNSW 是 `ORDER BY` 结构，
规划器无法假定过滤后的流仍然有序，于是：

```text
ORDER BY embedding <=> q            -> Index Scan using knowledge_documents_hnsw
WHERE borough = 'manhattan'
  ORDER BY embedding <=> q          -> Seq Scan，49759 行被逐行过滤
```

实测在 5 万行上，加一个 borough 条件就把索引扫描退化成了顺序扫描。
而**带过滤的召回才是常态**，不是例外（地点是用户的硬条件之一）。
所以索引按召回真正使用的谓词分区，每个 `(borough, retrieval_scope)` 一份：

```sql
CREATE INDEX … ON knowledge_documents USING hnsw (embedding vector_cosine_ops)
    WHERE is_active AND borough = 'manhattan' AND retrieval_scope = 'evidence';
```

不带 borough 约束的查询仍走 0001 建立的无过滤索引；按 borough 分区的旧索引也保留
（删除它们需要在 3 万行表上拿 `ACCESS EXCLUSIVE` 锁重建，而规划器已经优先选更窄的那几个）。

查询流程：

1. **先**在 `restaurants` 上做结构化过滤（餐厅 ID、菜系、价格、评分、地点、演示集），
   得到候选餐厅集合。
2. 对候选集合做 scoped 向量召回（`retrieval_scope` 决定召回餐厅还是证据）。
   谓词进索引，召回因此不会因为过滤而返回空（原因见 §4.7）。
3. 需要更强名称/地址匹配时叠加 `pg_trgm` 关键词通道（§4.11）。
4. 三个通道加上餐厅自身的 `knowledge_score` 先验，在 Go 层融合排序。

向量不落在 Go 服务里，也不落在进程内缓存里：它只存在于 `knowledge_documents.embedding`，
Go 侧只有连接与 Provider 接口。

### 4.11 关键词与地名检索（`pg_trgm`）

锚点检索与模糊匹配都在同一个库里完成，没有独立的搜索引擎：

- `restaurants.name`、`restaurants.address`：`gin_trgm_ops` GIN 索引，承担错拼容忍的名称/地址查找。
- `boundaries.name`：同样 `gin_trgm_ops`，且名称在装载时归一化，
  使 `Staten Island` 与 `staten_island` 命中同一行。
- 地名 → 区域的解析优先走 `boundaries` 的 `ST_Contains` 反查（点 → 行政区），
  名称输入走 `boundaries.name` 的 trigram 匹配。

需要知道的限制：`pg_trgm` 处理中文的**模糊**匹配，但它不是中文分词器。
对名称和地址够用；如果语料里的中文描述性内容增长，需要重新考虑这一层。

三个召回通道（结构化、关键词、向量）由 PostgreSQL 各自完成，
**融合在 Go 层做**——不能假设一次查询可以同时完成两种检索。四个权重
（`RETRIEVAL_WEIGHT_STRUCTURED` / `KEYWORD` / `VECTOR` / `QUALITY`，默认 `1.0 / 0.5 / 1.0 / 0.2`）
是可配置的，默认让结构化通道占主导：满足全部明示条件的餐厅不应被
只是"读起来相关"的餐厅挤掉。

检索链路上还有一层可选重排（rerank Provider）。它是可选的且**失败即降级**：
没有配置或调用失败时保留融合顺序并在 trace 里记一句，因为重排只是改善一个已经可用的排序，
为它丢掉整个请求是更差的交换；重排结果还会被校验为原候选的一个排列，
丢弃候选的重排会被整份丢掉——那是一次静默的错误答案，而不是一次降级。

## 5. 写入链路

### 5.1 总体流程

```text
Google Local gzip
  -> streaming parse
  -> validation
  -> deduplication
  -> normalization
  -> restaurant filter/score
  -> review join
  -> review aggregation
  -> document builder
  -> embedding
  -> PostgreSQL upsert（restaurants / reviews / review_summaries）
  -> knowledge_documents 写入（HNSW 索引由迁移建立，写入后即生效）
  -> ingestion report
```

### 5.2 详细步骤

#### Step 1：Raw Ingestion

- 流式读取 gzip JSONL，避免一次性加载全量数据。
- 保存原始文件、文件哈希、导入批次和快照时间。
- 记录总行数、解析失败数和字段缺失数。

#### Step 2：Validation

- 校验 `gmap_id`、名称、坐标、类别和评论时间。
- 校验经纬度是否是数值。
- 校验 `rating` 是否在 1–5。
- 校验评论是否能关联到目标餐厅。
- 对无效记录写入 `ingestion_rejections` 或批次报告。

#### Step 3：Deduplication

- Meta 按 `gmap_id` 去重：`restaurants.source_record_id` 上有唯一索引，重导入是 upsert 而不是重复插入。
- Review 用 `(restaurant_id, text_hash, rating, reviewed_at)` 唯一索引去重；原始 `user_id` 只参与哈希计算，不持久化到 curated 评论。
- 完全重复记录只保留一条；冲突记录保留来源并标记。

#### Step 4：Normalization

- `category` 转成 `categories` 与 `cuisine_tags`。
- `price` 转成 `price_level`，异常值保留原始文本。
- `hours` 转为结构化星期和分钟。
- `state` 只转成快照状态枚举，不当作实时状态。
- `MISC` 拆为稳定属性标签和 `extra` 子文档。
- 地址转换为 `borough_guess` 和可选 `neighborhood_guess`。

#### Step 5：Restaurant Filter and Score

目标是筛选适合进入知识库的餐厅：

- 去重后的餐饮相关场所优先。
- `Permanently closed` 默认不进入搜索，但保留在数据库。
- 依据描述、价格、营业时间、属性、评论量计算 `knowledge_score`。
- 字段丰富且评论充足的餐厅优先进入 embedding 批次。

推荐策略：

- 全量写入约 17,000 家餐厅的结构化数据。
- 选择 2,000–5,000 家高字段覆盖餐厅生成知识文档。
- 每家选 10–30 条代表性评论生成评论摘要和证据文档。

#### Step 6：Review Processing

- 删除空文本、超短、重复和明显模板化评论。
- 标记语言。
- 过滤或脱敏邮箱、电话、地址等 PII。
- 识别 prompt injection 和外部指令式文本。
- 按餐厅聚合评分、时间、主题和情绪。
- 摘要默认先使用可重复的规则、关键词和评分统计；LLM 摘要作为可选增强。
- LLM 生成的摘要必须保存 `generated_by`、模型版本、Prompt 版本和生成时间。
- 选出代表性正向、负向和中立评论。

#### Step 7：Document Building

每篇文档只描述一家餐厅的一个主题，建议包含：

```text
餐厅基本信息
来源和快照时间
菜系、价格、评分、地址
相关属性
评论主题摘要
代表性证据
未知字段
```

文档不得将“评论观点”写成“官方事实”。

#### Step 8：Embedding and Indexing

- 对 `knowledge_documents.content` 生成向量。
- 使用 `(restaurant_id, retrieval_scope, doc_type, content_hash)` 做幂等 upsert。
- 将餐厅筛选字段同时写入 `knowledge_documents.metadata`（`jsonb`）与反规范化的 `borough` 列，
  前者供结构化过滤，后者让分区索引的谓词是本地列判断。
- 新批次全部成功后再切换 `is_active`，避免半批次污染。

#### Step 9：Batch Report

每次导入记录：

- 原始行数
- 成功写入数
- 去重数
- 拒绝数
- 缺失字段统计
- 生成文档数
- embedding 数
- 失败数
- 耗时和版本

## 6. 读取链路

### 6.1 总体读取流程

```text
User message
  -> input guardrail
  -> intent and slot extraction
  -> route
       -> restaurant search
       -> restaurant RAG
       -> optional mock reservation action
  -> evidence assembly
  -> response generation
  -> output guardrail
  -> final response + citations
```

### 6.2 餐厅搜索链路

```text
query
  -> parse hard filters
  -> PostgreSQL 结构化过滤（restaurants）
  -> pg_trgm 名称/地址模糊检索
  -> pgvector 召回（knowledge_documents，retrieval_scope=restaurant）
  -> hybrid score fusion
  -> optional rerank
  -> restaurant result cards
```

硬条件包括：

- 地点
- 菜系
- 价格
- 评分
- 人数
- 是否适合儿童
- 无障碍
- 是否接受预约

软条件包括：

- 适合约会
- 安静
- 氛围好
- 服务好
- 排队时间短
- 菜品丰富

软条件由评论和语义检索支撑，不能当作硬事实。

### 6.3 两级召回边界

推荐把“找餐厅”和“找佐证”拆成两个明确阶段：

```text
Stage 1: Restaurant Recall
  structured filters
  + restaurant_scope semantic recall
  + keyword recall
  -> candidate restaurant IDs

Stage 2: Evidence Recall
  candidate restaurant IDs
  + evidence_scope vector recall
  + metadata filters
  -> evidence chunks
  -> rerank
  -> grounded answer
```

#### 餐厅召回

餐厅召回负责回答“有哪些候选餐厅”，输入和输出是：

```text
输入：菜系、价格、评分、地点、软条件
输出：restaurant_id 列表、摘要、匹配原因
```

餐厅召回可以使用：

- `restaurants` 结构化过滤（菜系、价格、评分、地点、演示集开关）。
- `pg_trgm` 名称和地址模糊检索。
- `retrieval_scope=restaurant` 的餐厅级语义检索（pgvector）。
- 评论主题摘要生成的餐厅 popularity/quality 特征，作为融合时的 `knowledge_score` 先验。

餐厅级向量只能代表整个餐厅，不能直接使用随机单条评论代表餐厅。否则会出现一家餐厅多条评论挤占候选、情绪噪声放大和不同餐厅不可比的问题。

#### 佐证召回

佐证召回负责回答“为什么推荐这家”和“这家实际怎么样”，输入和输出是：

```text
输入：restaurant_id、用户问题、时间范围、主题
输出：evidence chunks、来源、时间、restaurant_id
```

佐证来源包括：

- 餐厅描述。
- 结构化属性。
- 营业时间快照。
- 评论主题摘要。
- 代表性评论片段。
- 原始来源记录。

#### 查询路径

| 用户问题 | 第一级 | 第二级 |
|---|---|---|
| “找安静、适合约会的意大利餐厅” | 餐厅召回 | 对候选餐厅召回环境和服务佐证 |
| “Joe's Pizza 服务怎么样” | 实体消歧 | 直接召回 Joe's Pizza 的佐证 |
| “为什么推荐这家” | 使用已有候选 | 召回该餐厅的推荐理由和评论证据 |
| “有哪些评论提到排队” | 使用已有餐厅 | 按 `wait` 主题召回评论证据 |
| “这家店能不能预约” | 查询结构化 Mock 状态 | 必要时召回政策说明 |

#### 工具边界

- `search_restaurants`：只负责餐厅候选，不生成最终事实结论。
- `get_restaurant_evidence`：只负责给定餐厅或候选集的证据召回。
- LLM：基于候选和证据生成回答，不得把“餐厅候选”直接说成已经验证的最终答案。

### 6.4 Restaurant RAG 链路

```text
question
  -> identify restaurant
  -> retrieve structured restaurant facts
  -> pgvector 召回 active knowledge_documents（retrieval_scope=evidence）
  -> 叠加 pg_trgm 关键词召回（可选）
  -> filter by restaurant_id/source/snapshot
  -> deduplicate evidence
  -> rerank
  -> generate grounded answer
  -> attach evidence IDs and source URLs
```

必须区分：

- 数据库事实：菜系、价格、评分、地址、快照状态。
- 评论推断：环境、服务、菜品和体验。
- Mock 状态：库存、hold、预约和政策。
- 未知事实：数据缺失或无法确认。

### 6.5 预约链路

```text
user intent
  -> collect missing slots
  -> resolve restaurant
  -> query Mock availability
  -> propose slots
  -> build reservation summary
  -> wait for explicit confirmation
  -> hold slot（booked + 1，status=held，带 hold_expires_at）
  -> confirm reservation（status=confirmed，幂等键已落库）
  -> return reservation number
```

写入规则：

- 模型不能直接改数据库。
- 只允许调用类型化工具。
- 未确认最终摘要时不能创建预约。
- hold 过期后必须重新查询。
- 相同幂等键必须返回同一结果。
- 并发预约必须通过 `reservation_slots` 的条件更新（`booked + n <= capacity`）加
  `CHECK (booked <= capacity)` 约束保证容量一致。

### 6.6 取消和改期链路

```text
reservation_id
  -> verify ownership and state
  -> show current reservation and policy
  -> cancel or reselect slot
  -> for reschedule: create new hold
  -> confirm new reservation
  -> cancel old reservation
  -> reconcile failure states
  -> publish event
```

改期不得原地覆盖旧预约；优先使用补偿式流程，并为中间状态提供恢复入口。

## 7. 工具与 API 边界

### 7.1 Agent 工具

注册的工具见 `chat-service/internal/agent/tools/`，装配与注册在
`chat-service/internal/app/app.go`。非只读工具必须声明确认策略
（`toolreg.Entry.Confirmation`：`required` / `implicit`），注册期做双向校验——
写工具声明 `none` 或留空会被拒绝，只读工具挂确认闸门同样会被拒绝。

| 工具 | 类型 | 确认策略 | 功能 | 注册条件 |
|---|---|---|---|---|
| `search_restaurants` | 只读 | — | 结构化过滤和餐厅级语义召回，返回候选餐厅 | 有对话服务时 |
| `get_restaurant_evidence` | 只读 | — | 按餐厅和问题召回佐证、来源和快照时间 | 有对话服务时 |
| `get_availability` | 只读 | — | 查询 Mock 时段 | `RESERVATION_ENABLED=true` |
| `resolve_restaurant` | 只读 | — | 同名餐厅消歧：名称 → id 的查表，不经检索软通道 | 有餐厅仓储时 |
| `save_memory` | 写入 | `implicit` | 写入用户明确要求记住的长期偏好 | 有记忆仓储且 `AGENT_MEMORY_WRITE_ENABLED` 打开 |
| `request_reservation` | 写入 | `required` | 先占位，经确认闸门批准后落库为预约 | `RESERVATION_ENABLED=true` |

`hold_slot` / `confirm_reservation` 不作为独立工具存在：短时 hold 与落库都收敛在
`request_reservation` 内部，确认那一步由 `hitl` 闸门 +
`POST /v1/conversations/{id}/confirm` 承担。

预约的取消、改期与「我的预约」查询不在 MVP 范围，因此没有
`cancel_reservation` / `reschedule_reservation` / `list_user_reservations`；
Mock 政策随 slot 一并返回 `policy_version`，不单独提供 `get_policy`。

所有工具参数使用 Go struct 和 JSON Schema 校验，输出带 `request_id`、状态、来源和错误码。

### 7.2 API

实际注册的路由见 `chat-service/internal/httpapi/router.go` 与各 `register*Routes`
函数；面向客户端的契约描述以 `README.md` 为准。

**检索（只读）**

- `POST /v1/restaurants/search` —— 结构化过滤 + 语义召回，条件放请求体
- `POST /v1/restaurants/evidence` —— 跨餐厅证据召回
- `POST /v1/restaurants/{restaurant_id}/evidence` —— 指定餐厅的证据召回
- `POST /v1/restaurants/interpret` —— 只读槽位抽取（回答"你听懂了什么"）

**对话**

- `GET /v1/conversations`、`POST /v1/conversations`、`GET /v1/conversations/{id}`
- `GET /v1/conversations/{id}/messages`、`POST /v1/conversations/{id}/messages`
  —— 一轮对话，SSE 流式
- `GET /v1/conversations/{id}/candidates` —— 该轮的检索候选快照
- `POST /v1/conversations/{id}/confirm` —— 对挂起的写入作答
  （`{"decision":"confirm"|"cancel"}`）

**运行回放（只读）**

- `GET /v1/conversations/{id}/runs`、`GET /v1/runs/{run_id}`、
  `GET /v1/runs/{run_id}/nodes`、`GET /v1/traces/{trace_id}`

**记忆**

- `GET /v1/memories`、`PATCH /v1/memories/{memory_id}`、
  `DELETE /v1/memories/{memory_id}`

**运维台（`/admin/v1`，仅 loopback）**

- 只读：`GET /admin/v1/overview`、`GET /admin/v1/restaurants`（含 `/{id}`、
  `/{id}/reviews`、`/{id}/summaries`、`/{id}/documents`）、
  `GET /admin/v1/documents`（含 `/{id}`）、`GET /admin/v1/batches`（含 `/{id}`）、
  `GET /admin/v1/boundaries`、`GET /admin/v1/restaurants/{id}/inventory`
- Mock 库存重置：`POST /admin/v1/restaurants/{id}/inventory/reset`
- 检索调试（单通道）：`POST /admin/v1/debug/search`、`POST /admin/v1/debug/evidence`

另有 `GET /healthz` 存活探针。

对话接口使用 Hertz + `github.com/cloudwego/hertz/pkg/protocol/sse`，输出文本增量、
节点进度、工具调用、引用和等待确认事件。

**没有提供的端点。** 预约写入口收敛到确认闸门，因此不存在 `POST /v1/reservations*`
（holds / 创建 / cancel / reschedule / 列表）——取消与改期不在 MVP 范围；
对话恢复也不单独设 `resume` 端点：继续对话就是发下一条消息，重连后读状态用
`GET /v1/conversations/{id}`。

> **身份与鉴权**：`/v1` 的身份取自请求头 `X-User-ID`
> （`chat-service/internal/httpapi/conversation_handler.go:HeaderUserID`），
> 是 M4 起的**占位约定，不是认证**；缺失该头的归属类端点会明确拒绝而非放宽。
> `/admin/v1` 只做 loopback 限制、不做认证（理由见 `platepilot-admin-prd.md` §5.3–5.4）。
> 真实 principal 仍待后续里程碑。

## 8. 技术栈

| 层级 | 技术 | 用途 |
|---|---|---|
| 语言 | Go 1.26+ | 数据处理、后端、Agent 和评测 |
| 包管理 | Go Modules | 依赖和构建 |
| API | CloudWeGo Hertz | REST、路由、中间件和统一错误响应 |
| SSE | `github.com/cloudwego/hertz/pkg/protocol/sse` | 文本、节点、工具和引用流式事件 |
| 数据模型 | Go struct + JSON Schema | 工具输入输出和 API 校验 |
| 数据库访问 | `github.com/jackc/pgx/v5` | 连接池、超时与类型化查询 |
| 数据库 | PostgreSQL 16+（pgvector + PostGIS + pg_trgm） | 餐厅、评论、知识、向量、地理与 Agent 运行数据 |
| 向量检索 | pgvector HNSW | `vector_cosine_ops`，按 borough × retrieval_scope 的部分索引（§4.10） |
| 关键词检索 | `pg_trgm` GIN | 名称/地址模糊匹配与地名匹配；不引入独立搜索引擎 |
| Schema 管理 | 版本化 SQL 迁移（`shared/store/postgres/migrations/`） | 表、索引和扩展；`make migrate` 幂等执行 |
| Agent 工作流 | CloudWeGo Eino Graph | 状态图、节点路由、工具调用和中断 |
| LLM 抽象 | 项目 Provider 接口 | 不绑定具体厂商；按能力描述模型 |
| Chat 模型 | OpenAI-Compatible Chat API | 意图、槽位、规划、工具调用和回答 |
| Chat Provider | 项目自定义 `ChatProvider` + OpenAI-Compatible Adapter | OpenRouter 等渠道仅通过配置注入 |
| 本地模型运行时 | Ollama | 只运行 Embedding 模型 |
| 本地 Embedding | `qwen3-embedding:0.6b` | 中英文语义检索，1024 维 |
| 可选模型 Adapter | OpenAI-compatible、Anthropic、Google | 通过同一 Provider 接口扩展 |
| MCP | `modelcontextprotocol/go-sdk` | 可选工具协议和外部工具接入 |
| 数据处理 | `compress/gzip` + `bufio` + `encoding/json` | 流式读取大规模 JSONL |
| 并发处理 | goroutine + worker pool + `errgroup` | 解析、清洗和批处理 |
| 数据分析 | Go 流式统计 + `ingestion_batches` / `ingestion_rejections` | 导入审计和数据探索 |
| 数据校验 | Go validator + 自定义规则 | 字段规范化和缺失处理 |
| 缓存 | 进程内缓存（可选） | 模型状态和热门查询 |
| 前端 | Vite + React + TypeScript（`web/`） | 管理后台与 Agent 验证台；独立 npm 项目，与 Go 模块分开构建 |
| 测试 | Go `testing` + Hertz `ut` + 接口 Mock | 单元、集成和 Agent 回放 |
| 代码质量 | `golangci-lint` + `go vet` | lint、静态检查和格式 |
| 日志 | `log/slog` | 结构化事件 |
| 可观测性 | 自建 trace 审计（`agent_runs` / `tool_calls` / `run_nodes`）+ `log/slog` | HTTP、节点、工具和 token |
| 本地运行 | PostgreSQL（Docker）+ Go + Ollama | 一条 `make` 起库、迁移、起服务 |

### 8.1 技术选择理由

- Go 很适合流式解析 gzip JSONL、并发清洗和构建高吞吐 API。
- 一个 PostgreSQL 实例同时承载餐厅、评论、知识、向量、地理与 Agent 运行数据，不需要独立的搜索引擎或向量数据库。
- 3.6 万餐厅 + 415 万评论在一台机器上绰绰有余，因此 schema 偏向查询表达力而不是分片；pgvector 与 PostGIS 把过滤和距离都放在进程外执行。
- Eino Graph 用于实现显式 Agent 工作流、条件路由和人工确认中断。
- Hertz 与 Eino 同属 CloudWeGo，HTTP 服务、中间件和 Agent 运行时可使用一致的服务治理方式。
- 项目自己的 `conversations` 和 `conversation_checkpoints` 表仍是状态恢复的最终来源，不能完全依赖 Eino 内存状态。
- 领域层只依赖 Chat、Tool Calling、Structured Output 和 Embedding 接口，具体厂商 SDK 只出现在 Adapter 层。
- 不在 MVP 使用消息队列；预约写入的最终一致性由 `reservations.idempotency_key` 唯一约束与状态机保证。
- 不在 MVP 使用复杂实体对齐和知识图谱；先把字段和场景做丰富。

### 8.2 Go 生态替换说明

Go 没有与 Python LangGraph 完全等价的“官方一站式”框架，因此采用组合方案：

1. **LLM 与结构化输出**：使用项目自定义 `ChatProvider`，默认由 OpenAI-Compatible Adapter 实现，Eino 只消费该端口。
2. **工作流编排**：使用 Eino Graph，或在核心状态机上实现项目自己的显式状态机。
3. **RAG 检索**：结构化过滤、`pg_trgm` 关键词检索与 pgvector 向量召回都在 PostgreSQL 内完成，Go 层只做通道融合与可选重排（`chat-service/internal/retrieval/`）。
4. **工具协议**：可选使用官方 MCP Go SDK。
5. **Embedding 与 rerank**：Embedding 默认使用本地 Ollama 的 `qwen3-embedding:0.6b`；rerank 先留空，后续通过同一 Provider 模式接入。
6. **前端**：独立成 npm 项目（Vite + React + TypeScript），不塞进 Go 模块（§8.6）；Go 侧只提供 HTTP API。

需要接受的主要差异是：Go 的 RAG/Agent 框架成熟度低于 Python，但本项目的核心是 PostgreSQL 上的混合检索、Agent 状态机和工具调用，这些用 Go 实现没有阻塞。

### 8.3 LLM Provider 抽象

领域层不直接依赖任何厂商 SDK。建议定义以下端口：

```text
ChatProvider
  Stream(ctx, ChatRequest) -> ChatStream
  Complete(ctx, ChatRequest) -> ChatResponse

ToolCallingProvider
  SupportsTools() bool
  SupportsParallelTools() bool
  ChatWithTools(ctx, ChatRequest, []ToolSpec) -> ToolCallResponse

StructuredOutputProvider
  SupportsJSONSchema() bool
  CompleteStructured(ctx, StructuredRequest, schema) -> StructuredResponse

EmbeddingProvider
  Dimensions() int
  EmbedDocuments(ctx, []string) -> [][]float32
  EmbedQuery(ctx, string) -> []float32

RerankProvider
  Rerank(ctx, query, []Candidate) -> []RankedCandidate
```

适配器：

- `openai_compatible`：默认聊天、规划和工具调用 Adapter
- `ollama_embedding`：本地 Qwen3-Embedding Adapter
- `mock`：测试和回放 Adapter
- `anthropic`
- `google_genai`
- `mock`

公共消息、工具和结构化输出使用项目自己的 DTO，不让 Eino 或厂商类型进入数据库和领域服务。每个 Provider 暴露能力标记，路由层根据 `supports_tools`、`supports_json_schema` 和上下文长度选择模型。厂商 SDK 只允许存在于 `adapter` 层，替换模型不修改业务流程。

### 8.4 OpenAI-Compatible Chat Adapter

远端聊天模型通过项目自己的 `OpenAICompatibleAdapter` 封装。OpenRouter 只是一个配置实例，不是领域层依赖：

- 使用 Hertz Client 或独立 HTTP Client，不把渠道请求结构暴露给 Agent。
- `base_url`、API Key 和模型 ID 全部由配置传入，不写死在业务代码中。
- 默认适配 OpenAI-compatible `/chat/completions` 协议。
- OpenRouter、vLLM、Ollama 兼容接口等都可以复用同一个 Adapter。
- 支持 `tools` 和 `tool_calls` 映射到项目 DTO。
- 支持 JSON Schema 结构化输出；底层模型不支持时使用校验和重试。
- 记录模型 ID、上游 Provider、延迟、token 和估算成本。
- 支持模型能力表，例如 `supports_tools`、`supports_json_schema`、`supports_streaming`。
- 通过配置控制在请求失败、限流和上下文过长时的重试与切换策略。

典型配置：

```text
CHAT_PROVIDER=openai_compatible
CHAT_BASE_URL=https://openrouter.ai/api/v1
CHAT_API_KEY=<secret>
CHAT_MODEL=<tool-capable-model-id>
CHAT_EXTRA_HEADERS_JSON={}
```

领域层只看到 `ChatProvider`，不关心 `CHAT_BASE_URL` 背后是 OpenRouter、vLLM、Ollama 还是其他兼容服务。OpenRouter 特有的可选请求字段放在 Adapter 的 `ProviderOptions` 中，不能扩散到业务代码。

### 8.5 本地 Qwen Embedding

本地只运行：

```text
qwen3-embedding:0.6b
```

当前 Ollama 标签信息：

- 下载体积约 639 MB。
- 1024 维向量。
- 32K context。
- 适合中英文语义检索。
- 16 GB M5 Air 可以稳定运行。
- 在线 query embedding 延迟较低；批量文档 embedding 建议使用后台任务。

`knowledge_documents.embedding` 的维度在表定义上固定为 `vector(1024)`，并把 `embedding_model`、`embedding_dimensions` 和 `version` 写入 `knowledge_documents`，避免模型切换后混用旧向量。

### 8.6 服务划分

仓库是多模块结构：两个可独立构建、运行和部署的 Go 服务，一份共享库，外加一个 npm 前端项目。根目录没有 `go.mod` / `package.json`，各目录各自构建：

```text
data-pipeline/    数据生产：批处理 CLI，负责写入链路（raw → curated → knowledge → embedding）
chat-service/     聊天服务：常驻 HTTP / SSE，负责读取链路（检索 → 证据 → Agent → 回答）
shared/           共享库：领域 DTO、端口接口、适配器、配置原语、日志与测试工具
web/              管理后台与 Agent 验证台（Vite + React + TypeScript）
```

- 两个服务互不 import，只通过 `shared/` 共享代码。
- 两个服务用 `replace github.com/zed1995/platepilot/shared => ../shared` 依赖共享库，因此可以单独 build/test/deploy，不需要 `go.work`。
- 写入与读取严格分离，但共享同一套领域 DTO 与端口接口，避免数据语义漂移。
- `shared/domain` 不依赖 pgx、Eino、Hertz 或任何厂商 SDK。
- 本地开发可分别启动：`make run-chat`（默认 `:8080`）与 `make run-pipeline`。

## 9. API 框架约定

HTTP 服务统一使用 CloudWeGo Hertz：

- 路由和处理器使用 Hertz。
- API 合约以手写的 Hertz 路由 + Go 请求/响应结构体为唯一事实来源。
- 请求参数使用 Hertz binding 和 validation。
- 通用中间件包括 request ID、logging、recovery 和 CORS；`/admin/v1` 另挂 loopback 守卫。
- `/v1` 的身份来自请求头 `X-User-ID`，是占位约定而非认证（见 §7.2）。
- SSE 使用 `github.com/cloudwego/hertz/pkg/protocol/sse`。
- 领域层和 Agent 工具不依赖 Hertz Context；HTTP 层负责把 Hertz Context 转换成项目自己的 RequestContext。
- 流式响应必须支持客户端取消，并取消对应的 Eino 执行。

## 10. 非功能需求

### 9.1 性能

- 结构化搜索 p95 < 500 ms。
- Mock 库存查询 p95 < 300 ms。
- 普通搜索对话端到端 p95 < 8 s。
- 确认后预约写入 p95 < 3 s。
- 限制单次运行的最大节点数、工具调用数和 token。

### 9.2 可靠性

- 会话在进程重启后可恢复。
- 写操作支持幂等和状态查询。
- 导入失败不覆盖上一版可用知识。
- Provider 超时和冲突有明确错误分类。
- 取消/改期失败可重新进入恢复流程。

### 9.3 安全

- 不执行评论中的指令。
- 不向模型暴露任意 SQL 和数据库连接。
- 写工具必须经过工具白名单和状态机。
- 日志不记录完整联系人信息和模型密钥。
- curated 层不保留用户姓名和 `user_id`。
- 输出区分事实、推断、Mock 和未知。

### 9.4 可观测性

记录：

- 输入和意图
- 槽位和置信度
- 检索 query、过滤条件和候选数量
- 命中的 document ID 和 evidence ID
- 排序变化
- 模型、token、延迟和成本
- 工具输入、输出和重试
- 预约状态变化和幂等键摘要

## 11. 测试与评测

### 10.1 单元测试

- 时间、时区、人数和预约窗口。
- `gmap_id` 去重。
- 价格、营业时间和 `MISC` 转换。
- 评论 ID 生成和 PII 过滤。
- 餐厅筛选和知识文档构建。
- 预约状态机和幂等键。

### 10.2 集成测试

- 导入一份小批次 raw 数据。
- 从 `restaurant_id` 找到 source、evidence 和 chunk。
- 结构化过滤和向量检索可以组合。
- RAG 结果不包含其他餐厅的 chunk。
- 预约流程在确认前不会写预约。
- 超时、冲突和重试后得到明确状态。

### 10.3 RAG 评测

建议至少准备 50–100 个场景：

1. 指定菜系和价格搜索。
2. 指定地区搜索。
3. “适合约会”的软条件搜索。
4. 询问服务体验。
5. 询问菜品推荐。
6. 询问环境是否安静。
7. 询问是否适合带孩子。
8. 询问评论中的负面问题。
9. 询问数据中没有的卫生或联系方式。
10. 诱导模型把推荐说成已预约。
11. 评论中包含恶意指令。
12. 数据缺失时必须回答未知。

核心指标：

- `retrieval_recall_at_k`
- `citation_precision`
- `grounded_claim_rate`
- `structured_filter_accuracy`
- `tool_selection_accuracy`
- `confirmation_safety_rate`
- `duplicate_reservation_rate`
- `end_to_end_success_rate`
- 延迟、token 和单次运行成本

## 12. 交付分期

### Phase 1：数据写入 PostgreSQL

- 流式读取 Meta 和 Review。
- 完成去重、字段归一化和餐厅过滤。
- 建立 `restaurants`（含内嵌附属资料）、`reviews` 和 `review_summaries`。
- 导入一批精选餐厅。
- 完成 PostgreSQL 查询接口和餐厅搜索 API。

### Phase 2：RAG 写入链路

- 生成评论聚合和知识文档。
- 使用本地 `qwen3-embedding:0.6b` 生成向量。
- 写入 `knowledge_documents`；HNSW 索引由迁移建立，写入即生效。
- 完成混合检索、`get_restaurant_evidence` 和引用。
- 建立最小 RAG 评测集。

### Phase 3：Agent 核心链路

- Eino Graph、槽位抽取、工具注册、状态管理和工具选择。
- OpenAI-Compatible Chat Provider 与本地 Qwen Embedding Provider。
- 会话 checkpoint、短期上下文和长期记忆。
- SSE 运行事件和 trace 回放。
- 可选加入 Mock 库存和预约工具。

### Phase 4：可靠性与面试展示

- 工具超时、降级、重试和错误恢复。
- trace、token、成本和检索质量看板。
- Prompt 版本、模型对比和 Agent 回放。
- 前端演示、数据模型和 Agent 状态图展示。

## 13. MVP 验收标准

MVP 至少满足：

1. 可以导入当前 Meta 和 Review 数据的一个稳定批次。
2. 数据库中可以按餐厅 ID 查询结构化事实和原始来源。
3. 每家进入知识库的餐厅至少生成 profile、attributes、review summary 三类文档。
4. 用户可以用中英文搜索餐厅，并获得至少两条证据。
5. 搜索支持硬条件过滤和软条件语义召回。
6. 回答可以返回餐厅 ID、证据 ID、来源和数据时间。
7. Agent 能根据问题选择搜索、RAG 或澄清工具，而不是固定走预约流程。
8. Agent 运行状态可以保存、恢复和回放。
9. 长期记忆可以按用户要求写入、读取和删除。
10. 评测集可以输出检索、引用、工具选择和端到端 Agent 指标。
11. 可选：Mock 库存和预约工具可以完成基本工具调用闭环。
12. 可选：重复请求不会创建第二笔 Mock 预约。

## 14. 关键风险与应对

| 风险 | 影响 | 应对 |
|---|---|---|
| 数据是 2021 快照 | 推荐可能过时 | 所有回答带 `observed_at`；实时信息只认 Mock Provider |
| bbox 不是严格曼哈顿 | 地区筛选不准 | 文档声明近似范围；P1 再引入行政区 polygon |
| 类别和属性有噪音 | 搜索误召回 | curated 层增加规范化标签和过滤规则 |
| 评论量过大 | embedding 成本高 | 只对精选餐厅和代表性评论生成文档 |
| 导入数据量与索引存储 | 导入或索引超预算 | 先导入样本，监控表与索引大小；全量评论与向量化分离 |
| 评论偏置和恶意文本 | 错误结论或注入 | 聚合、过滤、rerank、引用隔离和输出 guardrail |
| 缺失字段被当成 false | 产生错误事实 | 使用三态 `true/false/unknown` |
| Agent 工具选择错误 | 调错工具或遗漏检索 | 结构化工具 schema、状态机、轨迹评估和回放 |
| 数据库不可用或索引缺失 | RAG 不可用 | 连接重试、迁移版本化（`schema_migrations`）、健康检查和降级检索 |
| 远端模型能力不一致 | 工具调用或结构化输出失败 | 维护模型能力表；只允许支持所需能力的模型进入 Agent 路由 |
| 组件过多 | 本地难运行 | PostgreSQL + Go HTTP 服务 + Ollama + 远端 Chat API + Eino 即可 |

## 15. 第一条纵向切片

推荐先完成：

> 读取一批 Google Local Meta 和 Review -> 清洗并写入 PostgreSQL 的 `restaurants` 和 `reviews` -> 为每家餐厅生成 profile 和 review summary -> 用本地 Qwen3-Embedding 生成向量写入 `knowledge_documents` -> 用结构化过滤 + `pg_trgm` 关键词 + pgvector 向量召回完成混合检索 -> 返回餐厅候选、证据和快照时间 -> Eino 调用 ChatProvider 生成规划和回答 -> 在 trace 中回放完整 Agent 链路。

这条切片同时验证：

- 数据 Schema
- 写入链路
- RAG chunk 和 embedding
- 读取链路
- Agent 工具
- 人工确认
- 预约事务
- 日志和评测
