# PlatePilot 技术需求文档

> PlatePilot：Evidence-grounded Restaurant Discovery Agent

> 版本：v0.12（Draft）  
> 日期：2026-09-29  
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

MongoDB Atlas 同时承载内容数据、向量数据和 Agent 运行数据，本地只运行 Go 服务、Eino 和 Qwen 模型服务：

```text
Google Local Raw Data
       |
       v
MongoDB Atlas Content Collections
  restaurants
  restaurant_documents   # 已合并进 restaurants（§4.4）
  reviews
  review_summaries
       |
       v
MongoDB Atlas Vector Collections
  knowledge_documents
  user_memories
       |
       v
MongoDB Atlas Agent Collections
  conversations
  conversation_checkpoints
  agent_runs
  tool_calls
  evaluation_cases
       |
       v
Optional Mock Collections
  mock_policies
  mock_inventory
  mock_holds
  mock_reservations
```

### 4.2 Collection 总览

| Collection | 用途 | 主要索引 |
|---|---|---|
| `restaurants` | 餐厅结构化主数据 | `source_record_id` 唯一、地理索引、筛选项 |
| `restaurant_documents` | 营业时间、属性和原始资料（**已合并进 `restaurants`**，见 §4.4） | — |
| `reviews` | 精选评论和必要元数据 | `restaurant_id + reviewed_at`、`text_hash` |
| `review_summaries` | 预计算主题和情绪摘要 | `restaurant_id + topic` |
| `knowledge_documents` | 可嵌入知识 chunk | `vector_index`、`restaurant_id + is_active` |
| `user_memories` | 长期偏好 | `user_id`、可选向量索引 |
| `conversations` | 会话元数据 | `user_id + updated_at` |
| `conversation_checkpoints` | 可恢复 Agent 状态 | `thread_id + version` |
| `agent_runs` | 单次运行和状态轨迹 | `trace_id`、`thread_id + started_at` |
| `tool_calls` | 工具调用审计 | `run_id`、`tool_name` |
| `evaluation_cases` | RAG/Agent 评测样本 | `suite_id`、`case_id` |
| `mock_*` | 可选预约演示 | 按业务场景创建 |

### 4.3 `restaurants`

一个餐厅尽量保存为一个文档，避免读取主信息时多次查询：

```json
{
  "_id": "uuid",
  "source": "google_local_2021",
  "source_record_id": "gmap_id",
  "name": "Joe's Pizza",
  "address": "7 Carmine St, New York, NY",
  "location": {
    "type": "Point",
    "coordinates": [-74.002, 40.730]
  },
  "categories": ["Pizza restaurant", "Restaurant"],
  "cuisine_tags": ["pizza", "italian"],
  "description": "Short summary",
  "price": {
    "raw": "$$",
    "level": 2
  },
  "rating": {
    "source_avg": 4.5,
    "computed_avg": 4.48,
    "rating_count_for_computed_avg": 180
  },
  "review_stats": {
    "source_review_count": 9998,
    "source_review_count_capped": true,
    "stored_review_count": 9998,
    "text_review_count": 5200,
    "representative_review_count": 25,
    "embedded_review_count": 8,
    "last_reviewed_at": "2021-09-01T00:00:00Z",
    "stats_updated_at": "2026-09-29T00:00:00Z"
  },
  "attributes": {
    "accepts_reservations": "unknown",
    "wheelchair_accessible": "true",
    "outdoor_seating": "unknown",
    "takeout": "true",
    "delivery": "true",
    "dine_in": "true",
    "good_for_kids": "true",
    "good_for_groups": "true",
    "atmosphere_tags": ["casual"],
    "popular_for_tags": ["lunch", "dinner"]
  },
  "snapshot_status": "open",
  "is_active_for_demo": true,
  "observed_at": "2021-09-01T00:00:00Z",
  "source_url": "https://www.google.com/maps/...",
  "created_at": "2026-09-29T00:00:00Z",
  "updated_at": "2026-09-29T00:00:00Z"
}
```

关键设计：

- 三态属性使用 `"true"`、`"false"`、`"unknown"`，避免把缺失当 false。
- `location` 使用 GeoJSON，支持 `$near` 和距离筛选。
- `source_review_count` 保留截顶警告，不把 9,998 当精确值。
- `stored_review_count` 表示 Mongo `reviews` collection 中实际保存的评论数。
- `embedded_review_count` 表示实际进入 RAG 的评论数。
- `source_review_count`、`stored_review_count` 和 `embedded_review_count` 语义不同，不能互相覆盖。
- `raw_payload` 可选保存，用于审计和重新加工。

### 4.4 `restaurant_documents`

> **决策更新（2026-09-29，M1 实现）**：本集合已**合并进 `restaurants`**。
> 实测附属文档数量约为主表的 3 倍，且只写不读、读取时总要和餐厅一起 join；
> 按 MongoDB「读在一起的写在一起」的原则改为内嵌：
> `restaurants.hours`（`[]HoursEntry`，保留原始文本）、
> `restaurants.attributes_raw`（原始 MISC）、`restaurants.relative_results`。
> 下面的原始设计保留作为背景，不再是实现目标。

用于保存不适合全部塞进主文档的字段：

```json
{
  "restaurant_id": "uuid",
  "document_type": "hours",
  "raw": [["Monday", "11AM-10PM"]],
  "normalized": [
    {"weekday": 1, "open_minute": 660, "close_minute": 1320, "is_closed": false}
  ],
  "observed_at": "2021-09-01T00:00:00Z",
  "source_record_id": "gmap_id"
}
```

建议的 `document_type`：

- `hours`
- `attributes_raw`
- `description`
- `relative_results`
- `source_snapshot`

### 4.5 `reviews`

只保存经过清洗的评论：

```json
{
  "_id": "sha256:gmap_id+time+text",
  "restaurant_id": "uuid",
  "rating": 5,
  "reviewed_at": "2021-03-01T12:00:00Z",
  "text": "Great pizza and fast service.",
  "language": "en",
  "text_hash": "sha256",
  "is_representative": true,
  "topic_tags": ["food", "service"],
  "source_observed_at": "2021-09-01T00:00:00Z"
}
```

不保存原始用户姓名、`user_id` 和图片链接到 RAG 可检索集合。原始数据如需审计，可放在权限更高的收藏集或对象存储中。

### 4.6 `review_summaries`

```json
{
  "restaurant_id": "uuid",
  "topic": "service",
  "sentiment": 0.72,
  "positive_ratio": 0.81,
  "summary": "服务整体积极，常见正向词包括 friendly、fast、attentive。",
  "evidence_count": 86,
  "valid_from": "2017-01-01T00:00:00Z",
  "valid_to": "2021-09-01T00:00:00Z",
  "generated_by": "<provider>/<model-id>",
  "generated_at": "2026-09-29T00:00:00Z"
}
```

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
| `stored_review_count` | `reviews` collection 聚合 | 表示实际保存的评论数 | 只代表入库数据 |
| `text_review_count` | `reviews` collection 聚合 | 表示有有效文本的评论数 | 更适合 RAG 覆盖度 |
| `representative_review_count` | 代表评论选择逻辑 | 表示可用于展示的证据数 | 不是原始总评论数 |
| `embedded_review_count` | 文档构建结果 | 表示参与 embedding 的数量 | 不是原始总评论数 |
| `source_avg_rating` | Meta `avg_rating` | 来源评分 | 与抽样评论计算值可能不同 |
| `computed_avg_rating` | `reviews` 聚合 | 入库评论平均分 | 仅在评论样本足够时有参考性 |

建议将聚合结果写入 `restaurants.review_stats`，而不是每次搜索时对 `reviews` 做 `$lookup + count`。MongoDB 的数据更新流程可以使用聚合管道 `$merge`，把统计结果物化回餐厅文档或独立的 `restaurant_review_stats` collection。

判断规则：

1. 用户搜索“评论多”“很热门”时，优先使用 `source_review_count`。
2. 用户问“有多少条评论被 RAG 收录”时，使用 `stored_review_count` 或 `embedded_review_count`。
3. 用户问“平均分”时，优先展示 `source_avg_rating`，并标明是 2021 快照。
4. 如果只想展示当前知识库评论样本的平均分，使用 `computed_avg_rating`，不要伪装成原始平均分。
5. `reviews` collection 只负责保存证据，不负责在热查询时实时计算总数。

### 4.7 `knowledge_documents`

这是 Atlas Vector Search 的核心集合：

```json
{
  "_id": "uuid",
  "restaurant_id": "uuid",
  "retrieval_scope": "evidence",
  "doc_type": "review_summary",
  "title": "Service experience",
  "content": "Restaurant-level evidence text",
  "content_hash": "sha256",
  "embedding": [0.012, -0.034, 0.117],
  "embedding_model": "qwen3-embedding:0.6b",
  "embedding_dimensions": 1024,
  "metadata": {
    "cuisine_tags": ["pizza"],
    "price_level": 2,
    "rating": 4.5,
    "topics": ["service"],
    "snapshot_at": "2021-09-01T00:00:00Z",
    "source": "google_local_2021"
  },
  "source_record_ids": ["gmap_id", "review_id"],
  "snapshot_at": "2021-09-01T00:00:00Z",
  "version": 1,
  "is_active": true
}
```

推荐 `doc_type`：

- `restaurant_profile`
- `restaurant_attributes`
- `restaurant_hours`
- `restaurant_review_summary`
- `restaurant_representative_reviews`

同时增加 `retrieval_scope`：

- `restaurant`：每个餐厅一条餐厅级语义摘要，用于召回候选餐厅。
- `evidence`：每家餐厅多条事实或评论证据，用于回答和引用。

两个 scope 可以使用同一个 collection 和同一个向量索引，但查询时必须强制加 scope 过滤。等性能和索引规模成为问题时，再拆成 `restaurant_search_documents` 和 `evidence_documents` 两个 collection。

禁止跨餐厅拼接同一个 chunk。

### 4.8 Agent 运行 Collection

#### `conversations`

保存线程元数据：

```text
_id
user_id
title
current_state
created_at
updated_at
last_message_at
```

#### `conversation_checkpoints`

保存可恢复状态：

```text
thread_id
version
state
pending_action
missing_slots
evidence_ids
selected_restaurant_id
created_at
```

Eino 可以负责运行时图结构，但恢复时必须从该 collection 读取业务状态。

#### `agent_runs`

```text
trace_id
thread_id
run_id
status
model_provider
model_name
started_at
finished_at
latency_ms
token_input
token_output
retrieval_count
tool_call_count
error_code
```

#### `tool_calls`

```text
call_id
run_id
tool_name
arguments
result_summary
status
latency_ms
created_at
```

只能保存脱敏参数，不能保存密钥和完整联系人信息。

#### `user_memories`

```text
user_id
memory_type
content
source
confidence
embedding
created_at
updated_at
deleted_at
```

长期偏好必须由用户明确要求保存，并支持查看、修改和删除。

### 4.9 可选 Mock 预约集合

预约不再是系统核心。如果需要演示工具调用，可使用以下 collection：

- `mock_policies`
- `mock_inventory`
- `mock_holds`
- `mock_reservations`

实现原则：

- 单 slot 文档用条件更新和版本号保证原子性。
- hold 和 reservation 使用唯一幂等键。
- 只有确认后才创建最终预约。
- 如果需要跨多个文档写入，再使用 MongoDB transaction。
- 预约 collection 不影响 RAG 和 Agent 主链路。

### 4.10 Atlas Vector Search 索引

`knowledge_documents` 的向量索引至少包含：

```json
{
  "fields": [
    {"type": "vector", "path": "embedding", "numDimensions": 1024, "similarity": "cosine"},
    {"type": "filter", "path": "restaurant_id"},
    {"type": "filter", "path": "retrieval_scope"},
    {"type": "filter", "path": "doc_type"},
    {"type": "filter", "path": "is_active"},
    {"type": "filter", "path": "metadata.cuisine_tags"},
    {"type": "filter", "path": "metadata.price_level"},
    {"type": "filter", "path": "metadata.rating"}
  ]
}
```

查询流程使用 Atlas `$vectorSearch`：

1. 先执行结构化过滤，限制餐厅 ID、菜系、价格和快照范围。
2. 对过滤后的后台执行向量召回。
3. 同一个 collection 返回文档和筛选元数据。
4. 如果还需要更强的关键词召回，使用独立的 Atlas Search 索引。

本地 Go 服务不保存向量，只保存连接配置和 Provider 接口。

### 4.11 Atlas Search 索引

Vector Search 和 Search 是两种不同的索引，需要分别创建：

- `vector_index`：用于 `knowledge_documents.embedding`。
- `search_index`：用于餐厅名称、地址、描述和必要的关键词字段。

建议 Search 索引覆盖：

```text
restaurants.name
restaurants.address
restaurants.categories
restaurants.description
knowledge_documents.title
knowledge_documents.content
```

名称匹配可以使用：

- 标准分词和大小写折叠。
- `autocomplete` 类型。
- 对地址使用独立字段或 ngram 分析器。

Atlas Search 和 Vector Search 的结果在 Go 服务中做融合，不能假设一次查询可以同时完成两种检索。

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
  -> MongoDB Atlas upsert
  -> Atlas Vector Search index refresh/verification
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

- Meta 按 `gmap_id` 去重。
- Review 使用 `sha256(gmap_id + user_id + time + text_hash)` 生成稳定 `review_id`，作为 Mongo `_id` 或唯一索引；原始 `user_id` 只用于哈希，不持久化到 curated review。
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
- 使用 `content_hash` 和版本做幂等 upsert。
- 将餐厅筛选字段同时写入 `metadata`，供 Atlas Vector Search 的 `filter` 使用。
- 向量写入后再创建或更新 Atlas Vector Search 索引。
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
  -> MongoDB Atlas structured filter
  -> Atlas Search name/address/text search
  -> Atlas Vector Search over knowledge_documents
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

- `restaurants` 结构化过滤。
- Atlas Search 名称和地址检索。
- `retrieval_scope=restaurant` 的餐厅级语义检索。
- 评论主题摘要生成的餐厅 popularity/quality 特征。

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
  -> Atlas $vectorSearch on active knowledge_documents
  -> optional Atlas Search keyword recall
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
  -> hold slot
  -> confirm reservation
  -> publish outbox event
  -> return reservation number
```

写入规则：

- 模型不能直接改数据库。
- 只允许调用类型化工具。
- 未确认最终摘要时不能创建预约。
- hold 过期后必须重新查询。
- 相同幂等键必须返回同一结果。
- 并发预约必须通过 MongoDB 条件更新、版本号或唯一索引保证容量一致。

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

| 工具 | 类型 | 功能 |
|---|---|---|
| `search_restaurants` | 只读 | 结构化过滤和餐厅级语义召回，返回候选餐厅 |
| `get_restaurant_evidence` | 只读 | 按餐厅和问题召回佐证、来源和快照时间 |
| `get_availability` | 只读 | 查询 Mock 时段 |
| `hold_slot` | 临时写入 | 创建短时 hold |
| `confirm_reservation` | 最终写入 | 确认预约 |
| `cancel_reservation` | 写入 | 取消预约 |
| `reschedule_reservation` | 写入 | 新预约加旧预约补偿 |
| `list_user_reservations` | 只读 | 查询预约 |
| `get_policy` | 只读 | 获取 Mock 政策 |

所有工具参数使用 Go struct 和 JSON Schema 校验，输出带 `request_id`、状态、来源和错误码。

### 7.2 API

- `POST /v1/threads/{thread_id}/messages`
- `POST /v1/threads/{thread_id}/resume`
- `GET /v1/restaurants/search`
- `GET /v1/restaurants/{restaurant_id}/evidence`
- `GET /v1/restaurants/{restaurant_id}/availability`
- `POST /v1/reservations/holds`
- `POST /v1/reservations`
- `POST /v1/reservations/{reservation_id}/cancel`
- `POST /v1/reservations/{reservation_id}/reschedule`
- `GET /v1/reservations`

对话接口使用 Hertz + `hertz-contrib/sse` 输出文本增量、节点进度、工具调用、引用和等待确认事件。

## 8. 技术栈

| 层级 | 技术 | 用途 |
|---|---|---|
| 语言 | Go 1.26+ | 数据处理、后端、Agent 和评测 |
| 包管理 | Go Modules | 依赖和构建 |
| API | CloudWeGo Hertz | REST、路由、中间件和统一错误响应 |
| SSE | `hertz-contrib/sse` | 文本、节点、工具和引用流式事件 |
| 数据模型 | Go struct + JSON Schema | 工具输入输出和 API 校验 |
| 数据库访问 | `go.mongodb.org/mongo-driver/v2` | MongoDB Atlas 官方 Go Driver |
| 数据库 | MongoDB Atlas | 餐厅、知识、Agent 状态和向量 |
| 向量检索 | Atlas Vector Search | `$vectorSearch` 和 filter fields |
| 关键词检索 | Atlas Search / 应用层分词 | 名称、地址和文本关键词 |
| Schema 管理 | Atlas CLI / `mongosh` / 版本化 JSON | Collection、索引和 Search Index |
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
| 数据分析 | Go 流式统计 + DuckDB CLI（可选） | 导入审计和数据探索 |
| 数据校验 | Go validator + 自定义规则 | 字段规范化和缺失处理 |
| 缓存 | 进程内缓存（可选） | 模型状态和热门查询 |
| 前端 | `html/template` 或 templ + HTMX | 聊天、候选卡片和 trace 展示 |
| 测试 | Go `testing` + Hertz `ut` + 接口 Mock | 单元、集成和 Agent 回放 |
| 代码质量 | `golangci-lint` + `go vet` | lint、静态检查和格式 |
| 日志 | `log/slog` | 结构化事件 |
| 可观测性 | OpenTelemetry Go + `hertz-contrib/obs-opentelemetry` | HTTP、trace、节点、工具和 token |
| 本地运行 | Go + Ollama | 不需要本地数据库 |

### 8.1 技术选择理由

- Go 很适合流式解析 gzip JSONL、并发清洗和构建高吞吐 API。
- MongoDB Atlas 同时承载餐厅、知识、向量和 Agent 运行数据，本地不需要数据库。
- Atlas Vector Search 支持向量和过滤字段，适合当前文档型 RAG 数据。
- Eino Graph 用于实现显式 Agent 工作流、条件路由和人工确认中断。
- Hertz 与 Eino 同属 CloudWeGo，HTTP 服务、中间件和 Agent 运行时可使用一致的服务治理方式。
- 项目自己的 `conversations` 和 `conversation_checkpoints` collection 仍是状态恢复的最终来源，不能完全依赖 Eino 内存状态。
- 领域层只依赖 Chat、Tool Calling、Structured Output 和 Embedding 接口，具体厂商 SDK 只出现在 Adapter 层。
- 不在 MVP 使用消息队列；outbox collection 加后台 worker 即可演示最终一致性。
- 不在 MVP 使用复杂实体对齐和知识图谱；先把字段和场景做丰富。

### 8.2 Go 生态替换说明

Go 没有与 Python LangGraph 完全等价的“官方一站式”框架，因此采用组合方案：

1. **LLM 与结构化输出**：使用项目自定义 `ChatProvider`，默认由 OpenAI-Compatible Adapter 实现，Eino 只消费该端口。
2. **工作流编排**：使用 Eino Graph，或在核心状态机上实现项目自己的显式状态机。
3. **RAG 检索**：使用官方 Mongo Go Driver 执行结构化过滤，使用 Atlas Vector Search 做向量召回，并在 Go 层进行融合排序。
4. **工具协议**：可选使用官方 MCP Go SDK。
5. **Embedding 与 rerank**：Embedding 默认使用本地 Ollama 的 `qwen3-embedding:0.6b`；rerank 先留空，后续通过同一 Provider 模式接入。
6. **前端**：如果严格限制编程语言为 Go，使用 Go 模板 + HTMX，避免引入 TypeScript 业务代码。

需要接受的主要差异是：Go 的 RAG/Agent 框架成熟度低于 Python，但本项目的核心是 MongoDB Atlas、混合检索、Agent 状态机和工具调用，这些用 Go 实现没有阻塞。

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

Atlas Vector Search 的索引维度固定为 `1024`，并把 `embedding_model`、`embedding_dimensions` 和版本写入 `knowledge_documents`，避免模型切换后混用旧向量。

### 8.6 服务划分

仓库是一个 Go module，包含两个可独立构建、运行和部署的进程，外加一份共享库，并预留前端位置：

```text
data-pipeline/    数据生产：批处理 CLI，负责写入链路（raw → curated → knowledge → embedding）
chat-service/     聊天服务：常驻 HTTP / SSE，负责读取链路（检索 → 证据 → Agent → 回答）
shared/           共享库：领域 DTO、端口接口、适配器、配置原语、日志与测试工具
web/              预留前端（M6-06）
```

- 两个服务互不 import，只通过 `shared/` 共享代码。
- 写入与读取严格分离，但共享同一套领域 DTO 与端口接口，避免数据语义漂移。
- `shared/domain` 仍不依赖 Mongo、Eino、Hertz 或任何厂商 SDK。
- 本地开发可分别启动：`make run-chat`（默认 `:8080`）与 `make run-pipeline`。

## 9. API 框架约定

HTTP 服务统一使用 CloudWeGo Hertz：

- 路由和处理器使用 Hertz。
- API 合约以手写的 Hertz 路由 + Go 请求/响应结构体为唯一事实来源。
- 请求参数使用 Hertz binding 和 validation。
- 通用中间件包括 request ID、recovery、CORS、认证上下文、限流和 OpenTelemetry。
- SSE 使用 `hertz-contrib/sse`。
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

### Phase 1：数据写入 Atlas

- 流式读取 Meta 和 Review。
- 完成去重、字段归一化和餐厅过滤。
- 建立 `restaurants`（含内嵌附属资料）、`reviews` 和 `review_summaries`。
- 导入一批精选餐厅。
- 完成 MongoDB 查询接口和餐厅搜索 API。

### Phase 2：RAG 写入链路

- 生成评论聚合和知识文档。
- 使用本地 `qwen3-embedding:0.6b` 生成向量。
- 写入 `knowledge_documents` 并创建 Atlas Vector Search 索引。
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
- 前端演示、Atlas 数据模型和 Agent 状态图展示。

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
| Atlas 写入量和存储成本 | 导入或索引超预算 | 先导入样本，监控集合和索引大小；全量评论与向量化分离 |
| 评论偏置和恶意文本 | 错误结论或注入 | 聚合、过滤、rerank、引用隔离和输出 guardrail |
| 缺失字段被当成 false | 产生错误事实 | 使用三态 `true/false/unknown` |
| Agent 工具选择错误 | 调错工具或遗漏检索 | 结构化工具 schema、状态机、轨迹评估和回放 |
| Atlas 网络或索引配置错误 | RAG 不可用 | 连接重试、索引版本化、健康检查和降级检索 |
| 远端模型能力不一致 | 工具调用或结构化输出失败 | 维护模型能力表；只允许支持所需能力的模型进入 Agent 路由 |
| 组件过多 | 本地难运行 | MongoDB Atlas + Go HTTP 服务 + Ollama + 远端 Chat API + Eino 即可 |

## 15. 第一条纵向切片

推荐先完成：

> 读取一批 Google Local Meta 和 Review -> 清洗并写入 MongoDB Atlas 的 `restaurants` 和 `reviews` -> 为每家餐厅生成 profile 和 review summary -> 用本地 Qwen3-Embedding 生成向量 -> 使用 Atlas Vector Search 和结构化过滤完成混合检索 -> 返回餐厅候选、证据和快照时间 -> Eino 调用 ChatProvider 生成规划和回答 -> 在 trace 中回放完整 Agent 链路。

这条切片同时验证：

- 数据 Schema
- 写入链路
- RAG chunk 和 embedding
- 读取链路
- Agent 工具
- 人工确认
- 预约事务
- 日志和评测
