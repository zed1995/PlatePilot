# 第 6 章 把文字变成向量：Embedding 与知识文档

> 上一章：[第 5 章 数据从哪里来](05-数据从哪里来-424-万条评论的流式导入.md) ｜ 下一章：[第 7 章 向量检索第一课](07-向量检索第一课-pgvector-与-hnsw.md)
>
> 本章涉及的仓库真实文件、函数与实测数字集中在文末 [§6.6 参考代码与目录](#66-参考代码与目录)。

---

## 6.1 坏 case 开场：数据库里躺着字，用户却搜不到

第 5 章结束时，我们得到了一份干净的语料：3.6 万家餐厅、424 万条评论、字段整整齐齐
躺在 PostgreSQL 里。现在用户问：

> 「曼哈顿有没有安静、适合约会的意大利菜？」

用上一章建立的直觉看，这句话里的「曼哈顿」「意大利菜」是**硬条件**，可以 `WHERE`
出来；但「安静」「适合约会」**根本没出现在任何一列里**。一条评论写
`"intimate, quiet vibe, perfect for a date night"`，它和用户的问题意思完全一致，
但在**字面上**没有任何一个词重合。

这就是传统关键词检索的天花板：**它匹配的是字符串，而用户要匹配的是意思。** 你要么
把「安静」的所有同义表达（quiet / intimate / low noise / cozy…）穷举成词典，要么
承认这条路走不通。

本章要讲的是另一条路：**把文字变成向量，让「意思相近」变成「距离相近」。** 同时，
本章要解决一个第 5 章留下的问题：**我们凭什么让模型引用某段文字？** ——答案是把
语料聚合成**知识文档**，并在其中划出一条 「可引用」与「不可引用」的分水岭。

---

## 6.2 概念与原理

### 6.2.1 核心思想：分布式表示

一句话说明 embedding：**把任意长度的文本映射成一个固定长度的浮点向量，使得语义相近
的文本，向量距离也近。**

它之所以可行，靠的是一个训练目标：**出现在相似上下文里的词，应该得到相似的向量。**
模型见过海量文本，于是「安静」「intimate」「quiet」这些词因为常常出现在同类语境里，
最后落在向量空间中相近的位置。这个过程叫**分布式表示（distributed representation）**
——一个词/一句话的意思，被**分散地**编码在向量的每一维里，而不是对应某个可读的标签。

于是**语言问题变成了几何问题**：

```text
"曼哈顿安静的意大利菜"        ──embedding──→ [0.02, -0.13, …, 0.31]   (1024 维)
"intimate, quiet vibe…"      ──embedding──→ [0.01, -0.11, …, 0.28]   ↘ 距离近 → 相关
"pizza slice two dollars"    ──embedding──→ [0.40,  0.22, …,-0.05]   ↗ 距离远
```

不需要线性代数，只需要记住**两件反直觉的事**（第 2 章已埋过伏笔，这里展开）：

1. **它匹配语义，不匹配字面。** 这是它超越关键词的全部价值，也是它唯一的用处所在。
2. **它永远只能「近似」。** 精确店名、行政区、价格这种**硬约束**，交给它必然翻车——
   因为「Sbarro」和「Sbar」在语义上几乎没差别，但用户要的是**那一家**。这个短板
   正是第 8 章「多路召回」存在的理由。

### 6.2.2 相似度：cosine 就够了

「距离近」需要一个确切的度量。最常用的是 **cosine 相似度**：算两个向量的夹角余弦。
取值 `[-1, 1]`，方向一致取 1，正交取 0，相反取 -1。

为什么文本任务偏爱 cosine，而不是欧氏距离（L2）或内积？直觉是这样的：

- **cosine 只看方向，不看长度。** 它关心的是「两段文字指向的意思像不像」，而不是
  「向量有多长」——而向量的长度往往受文本长度、用词频度影响，是**噪声**。把长度
  这个维度甩掉，度量就更稳。
- 在大多数 embedding 模型里，向量是**归一化**过的（模长接近 1），此时 cosine、
  内积、L2 三种度量在排序上高度相关。选 cosine 是因为它对「没归一化干净」的情况更鲁棒。

本项目 `knowledge_documents.embedding` 是 `vector(1024)`，走 cosine。第 7 章会看到
它在 SQL 里就是那个 `<=>` 运算符。

> 一个必须建立的工程直觉：**入库向量和查询向量必须走同一条计算路径。** 如果文档用
> 一个模型/一种预处理、查询用另一个，所有检索分数都会带上一个系统偏差，而且**极难
> 排查**——因为分数看起来「就是有点不准」，不报错。所以端口层面只有一个
> `EmbedDocuments` 和一个 `EmbedQuery`，两者内部走同一段代码。

### 6.2.3 embedding 走本地，chat 走远端

第 2 章提过一句，这里把理由说透，因为它是本项目一个刻意的架构决策：

| 维度 | chat 模型 | embedding 模型 |
|---|---|---|
| 部署 | **远端**（OpenAI 兼容接口） | **本地**（Ollama，`qwen3-embedding:0.6b`） |
| 为什么 | 要强：规划、表达、遵循工具协议 | 语料英文为主、查询中文为主，需要中英文都稳的模型 |
| 成本 | 按 token 计费 | 本地免费，可重复跑 |
| 负载 | 每轮一次生成 | 离线**批量**向量化数万文档；在线每轮只 embed 一个短查询 |

关键在于**这两件事本来就是独立的**。把它们绑成一个「模型服务」是概念上的偷懒：
你今天想换一个更强的聊天模型，不该被迫重新向量化整个语料库。所以第 4 章的端口抽象里，
embedding 是**完全独立的一个端口**——这是「按能力切接口」在真实选型上的直接回报。

而「本地」也不是没有代价。真实数据：语料的**代表性评论块平均 2,878 字符、最长 8,615
字符**；在一台本地 CPU 上，batch 16 跑一批要 **28 秒**，batch 32 要 **52 秒**。
这个数字直接决定了 `EMBEDDING_MAX_BATCH` 的默认值（32）和超时设置（180s）——**默认值
是从实测里量出来的，不是拍出来的。**

### 6.2.4 端口与它的离线替身

第 4 章建立的纪律在这里兑现：每个端口都要有**至少两个实现，且跑同一套测试**。embedding
端口有：

- **真实实现（Ollama）**：POST `/api/embed`（注意不是老接口 `/api/embeddings`）、
  30s 超时、3 次重试——且**只对 429/5xx/传输错误重试**。
- **fake 实现**：向量是输入文本的**纯函数**（FNV-1a 哈希展开），宽度与真实模型一致
  （1024）。空文本给全零向量，还提供 `Failing(err)` 专门练习失败路径。

这里有一个必须纠正的常见误解：

> **「配了 fake」和「没配 embedding」是两回事。** 把 `EMBEDDING_PROVIDER` 配成 fake，
> 语义通道**照常跑**，只是向量不携带真实语义（排序会退化成伪随机但确定）。真正让
> 语义通道停跑、trace 里记 `ran:false` 的，是**完全不配** embedding provider，或显式
> 关掉 `RETRIEVAL_ENABLE_VECTOR`。这是第 4 章那个「故障分级」判据的又一次应用。

还有一条**校验闭环**的细节值得学：适配器返回的向量要**逐个校验**——空向量、维度
不符、NaN、Inf，全部在**客户端就报错**。为什么？因为一个 NaN 向量如果混进数据库，
它不会在这里报错，而是会在几小时后的某次检索里，以一个**含义不明的排序异常**出现。
**在能报错的地方立刻报错**，是分布式系统里最便宜的一条纪律。

### 6.2.5 知识文档：把「原始语料」变成「检索单元」

现在到了本章的第二个核心问题：**我们检索的单位是什么？**

一条评论太碎（用户问「这家怎么样」，你不可能只给他一条评论）；整家餐厅又太杂。
本项目的答案是**知识文档（knowledge document）**——**按餐厅聚合、按用途分型**的
可向量化文本片段。

文档一共分两类 **scope**（第 9 章会讲为什么这个区分是硬约束）：

- **`restaurant`（面向「谈哪几家」）**：用于**召回餐厅**；
- **`evidence`（面向「凭什么这么说」）**：用于**佐证**。

在这个划分下，文档有六种类型。其中三种是本章的主角（**「三兄弟」**）：

| 文档类型 | scope | 是什么 | 能不能被引用？ |
|---|---|---|---|
| `restaurant_profile` | restaurant | 商家事实：名字、菜系、价格、营业时间、评分 | **否**（是「事实卡片」，用于召回） |
| `restaurant_review_summary` | evidence | 按**话题**聚出的评论摘要 | **能**（核心引用来源） |
| `restaurant_review_digest` | restaurant | 基于评论的**整体理解**，一段散文 | **绝对不能**（排序燃料） |

另外三种是 `restaurant_attributes`、`restaurant_hours`、`restaurant_representative_reviews`，
都是 evidence scope 的细分。

**为什么 digest 不能引用，这是本章最值得记住的一条设计。** digest 是模型**写出来**的
一段理解性散文（下一节细讲怎么生成）。它读起来最像「一段像样的点评」，恰恰因此最危险：
如果允许引用它，用户看到的「有据可依」其实是一句**模型的话被当成证据**——引用闭环
在这里会被**绕过**。所以项目做了三重隔离：

1. **物理隔离**：digest 放在 `restaurant` scope，而**所有 evidence 读取路径只读
   `evidence` scope**——它在代码上根本进不了引用通道；
2. **元数据标注**：它的 metadata 里带 `"citable": false`，这样**只看数据库一行**的人
   也能看懂意图，不需要读代码；
3. **文档注释**：[evidence.go](../../shared/domain/evidence/evidence.go) 里明确写着
   「ranking fuel, never a citation」。

> 这是「**用架构表达意图，而不是靠文档或自觉**」的范例。一个 `citable: false` 字段 +
> 一个物理隔离的 scope，比十句「注意不要引用 digest」的注释都可靠。

### 6.2.6 文档 hash：实现增量更新

语料会重导，模型会换，prompt 会改——文档也会变。但**大部分文档没变**。每次重跑都
全量重算 embedding 是巨大的浪费。做法是给每篇文档算一个 **`content_hash`**：

```text
ContentHash(scope, docType, restaurantID, content)  // 二次哈希，稳定
落库：INSERT ... ON CONFLICT (restaurant_id, retrieval_scope, doc_type, content_hash)
       DO NOTHING
```

于是：**内容没变 → hash 相同 → 冲突 → 跳过（不重算、不重复写）；内容变了 → hash 不同
→ 插入新版本（旧版本退役）。** 增量更新就是这样实现的——不是靠「一切重跑」，而是靠
**内容寻址**。

### 6.2.7 常见 RAG 要切块，这里为什么不切

任何一本 RAG 教程的第一课都是**切块（chunking）**：把长文档按固定长度（比如 512 token）
或语义边界切成小块，再逐块 embedding。理由很充分：文档可能很长，而一个具体问题通常
只命中其中一小段；整篇 embedding 会让这一小段的信息被稀释掉。

**但 PlatePilot 不切块**，原因是这份语料的形状恰好不适合：

1. **单条评论本来就短。** 主流 RAG 切块是因为源文档是「一篇长文」，而这里的原始单位
   是**评论**——一条评论平均几十字，本身就是一个天然语义单元；
2. **知识文档是按餐厅聚合的。** profile / summary / digest 各一份，检索单位天然就是
   「**一家店 + 一类文档**」，而不是「一个文档的第 N 段」；
3. 所以**整篇当一条文档**，不做二次切分。

代价也必须说清楚：**单条偏长、粒度偏粗**。一篇 `restaurant_review_summary` 可能覆盖
几个话题，用户只问「等位要多久」，命中的却是整段摘要。项目用**话题**来缓解这个问题
（`restaurant_review_summary` 是**按话题**分文档的），但这不等于切块。

**什么时候该回来补切块？** 两个信号：① 评论开始被聚合成长篇综述（单文档越写越长）；
② 单文档大到塞不进一个 embedding 窗口（现在的最长 8,615 字符还远没到）。在那之前，
切块只会引入「一条证据被腰斩」的风险——而第 9 章会讲，**引用不许被截断**是本项目的
硬规矩，切块恰恰会制造截断。所以这里的「不切」是一个**有意识的、有代价的**选择，不是
偷懒。

### 6.2.8 重头戏：digest 的生成与溯源（一次真实的 prompt 迭代）

digest 是本项目里唯一一处**让模型来写语料**的地方，因此也是「校验闭环」最该被讲透的
地方。

**先讲它为什么存在。** 用一条条原始评论做向量召回有个问题：单条评论噪声大、覆盖窄。
如果能把一家店的**全部评论**先理解成一段话，再拿这段话去匹配用户问题，召回质量会更好。
这就是 digest——**排序燃料**：它让「这家店大概是什么气质」变得可匹配。

**第一幕：一次真实的收敛。** 早期 digest 的 prompt **没有硬长度上限**，结果是模型
为了「显得详尽」而**注水**——把同一个意思翻来覆去地讲。项目为此提交了一次专门的修改：

```text
commit 09985b7  refactor(digest): shorten digest prompt to 40-100 words
```

收敛后的 prompt 里，有一条规则就是针对这个症状的：
`"Never pad, repeat, hedge, or restate the same point to reach a length"`。
最终的长度约定是：**40–100 词、硬顶 100 词**。这个数字不是拍脑袋——英文散文大约
每词 6 字符，40–100 词约合 240–600 字符，所以代码侧的长度门设成
`150–800 runes` 正好卡在它外面留有余量（长度门只用来抓**被截断或空回复**，不是用来
卡字数本身）。

**第二幕：让模型只写评论真正支撑的东西。** 除了长度，prompt 还约束**内容**：按
**7 个维度**、按优先级写（总体印象；招牌菜/口味；适合的场合/人群；环境氛围；服务；
等位时间；性价比）——**哪个维度评论没支撑就跳过，且不许写「信息缺失」之类的废话**。
这一条把「模型自由发挥」压缩成了「模型在给定材料里做摘要」。

**第三幕：输出不可信——引用必须可回溯。** 这是接第 1 章校验闭环的一步。digest 的
模型输出不是一段自由文本，而是**一个 JSON 对象**：

```json
{
  "content": "……一段 40-100 词的英文理解……",
  "source_review_ids": [12345, 67890]
}
```

其中 `source_review_ids` 声明「我这段结论是grounded 在哪几条评论上」。而模型给的 ID
**可能是幻觉 ID** ——它编一个不存在的评论号。所以解析时（`ParseDigestOutput`）要**逐个
校验**：

- `source_review_ids` **不能为空**（不声明来源的 digest 直接拒绝）；
- 每个 ID **必须在这轮输入 bundle 提供过的评论集合里**（编的 ID 直接拒绝**整份输出**）；
- 返回前**去重、升序**，让落库的来源是确定性的。

> 为什么「编的 ID」要拒绝**整份**输出，而不是丢掉那一个坏 ID？因为来源指向一条模型
> 从未见过的评论，**比没有来源更糟**——它是一条可信外观下的假链接。这就是 `ParseDigestOutput`
> 注释里那句话：「provenance pointing at a review the model never saw is worse than no
> provenance」。校验通过的 ID 落进 `source_review_ids bigint[]` 列（迁移 0014），
> **可回溯到原文**。

**第四幕：代码侧还有一道独立的门。** 除了 prompt 约束，`ValidateDigestOutput` 在代码里
再挡一遍：长度门（150–800 runes）+ 四类正则过滤（URL / 邮箱 / 电话 / 指令覆盖）。
为什么 digest 从不给用户看，还要过滤这些？因为**它要进 embedding 索引**——污染进了
索引，就等于污染了所有检索。这是「在能拦的地方拦」的又一次应用。

**第五幕：缓存与失败隔离。** 重算 digest 是花钱的（要调模型），所以输入不变就跳过：
缓存 key 检查 metadata 里的 `bundle_hash`、`prompt_version`、`generated_by`，LLM 版
再加 `model_id`。**没有独立的缓存表**——所有缓存信息都在 `knowledge_documents.metadata`
里，跟着文档走。另外，**失败按餐厅隔离**：某一家 digest 生成失败，记进 `failed` 计数
继续跑，不会让整批中断；但**store 写失败会中止整个 run**（那是系统性故障，不是单条
的问题）。最后，`DIGEST_ENABLED` **默认关闭**——LLM digest 是要显式打开的，规则基线
`digest:rules:v1` 零模型费用，用来做对照。

### 6.2.9 离线产物与在线检索的分工

本章最后一块拼图，是**离线与在线的边界**：

```text
离线（data-pipeline，批处理）：  原始语料 → 清洗 → 知识文档 → embedding → 落库
在线（chat-service，常驻）：     用户 query → embed → 检索 → 组装 → 生成
```

两条路**读同一份语料**，但**写的表不重叠**：离线写内容表（餐厅/评论/知识文档），
在线写运行时表（会话/审计/记忆/预约）。它们之间只有一个契约：**schema**——而 schema
**只有 `data-pipeline migrate` 能改**。

这条边界带来一个必须记住的运维纪律：**重导 restaurants 之后，prefilter、文档、向量
必须按顺序重跑。** 因为它们之间有顺序依赖：文档依赖清洗后的餐厅，向量依赖文档。只
更新其中一环，检索就会读到一份**自相矛盾**的语料。

---

## 6.3 承载思路的最小示例

把「文档 → 向量 → 落库，且内容不变就跳过」这条链路写成教学骨架：

```go
type EmbeddingProvider interface {
    ModelID() string
    Dimensions() int
    EmbedDocuments(ctx context.Context, docs []string) ([][]float32, error)
    EmbedQuery(ctx context.Context, query string) ([]float32, error)
}

// 增量：先算内容哈希，已存在就跳过，只对变化的部分向量化
func embedAndStore(ctx context.Context, docs []KnowledgeDocument, p EmbeddingProvider) error {
    var fresh []KnowledgeDocument
    for _, d := range docs {
        d.ContentHash = ContentHash(d.Scope, d.DocType, d.RestaurantID, d.Content)
        if !exists(d) { // 依赖唯一索引 (restaurant_id, scope, doc_type, content_hash)
            fresh = append(fresh, d)
        }
    }
    for _, batch := range chunked(fresh, 32) { // EMBEDDING_MAX_BATCH 默认 32
        vecs, err := p.EmbedDocuments(ctx, contentsOf(batch))
        if err != nil {
            return err // provider 失败 = 中止 run（系统性故障）
        }
        for i, v := range vecs {  // 逐个校验：空/维度/NaN/Inf 都在这里挡下
            if err := validate(v, p.Dimensions()); err != nil {
                markRejected(batch[i], err) // 单文档质量门：拒这一篇，继续
                continue
            }
            store(batch[i], v)
        }
    }
    return nil
}
```

三点对照正文：

1. **`ContentHash` + `exists` 就是增量更新**，不是「每次全量重算」；
2. **`chunked(fresh, 32)`** 对应实测调出来的批量上限；
3. **错误分两级**——provider 报错是**中止**，单文档校验失败是**拒绝并继续**。这个
   分级和第 4 章「拒绝 vs 降级」是同一套判据。

---

## 6.4 取舍与边界

- **embedding 永远只能做「语义近似」。** 精确匹配（店名、行政区、价格）是它的死穴，
  必须由别的通道兜（第 8 章）。承认边界，才知道要几条腿走路。
- **本地 embedding 是一个成本/质量的取舍。** 免费、可复现、可离线；代价是本地模型
  弱于顶尖商用 embedding，且吃 CPU（实测 batch 32 要 52s）。语料再大、或质量不够时，
  换远端 embedding 是一个**只改适配器**的动作——这正是端口抽象的价值。
- **不切块是有代价的选择。** 换来「引用不被腰斩」（第 9 章的硬规矩）；代价是粒度粗、
  单文档覆盖多个话题。语料形状变了（评论聚成长综述）就该回头补切块。
- **digest 是排序燃料，不是证据。** 用三重隔离（scope / metadata / 注释）保证它进不了
  引用路径。这个区分**不能靠模型自觉**——所以 `citable: false` 是**数据**，不是约定。
- **模型输出永远是「待校验的输入」。** `source_review_ids` 必须逐个校验、必须非空、
  必须真在输入里。这一条是第 1 章校验闭环在数据管道里的落地。
- **缓存看的是「输入是否变化」**（bundle hash + prompt/model 版本），不是「跑没跑过」。
  prompt 改了、模型换了，就该重算——`generated_by` / `model_id` 在缓存 key 里就是干这个的。
- **模型切换有守卫。** embedding 模型变了，旧向量和新向量**不在同一个空间**，不能混用。
  项目默认**拒绝**直接切换，要求显式 `--force-model-change` 才退役旧文档——因为「悄悄
  混用两个模型的向量」是一种**分数看起来正常、但全是错的**的故障。

---

## 6.5 课后实验

**跑（离线，不需要模型；向量部分需要 Ollama）**

```bash
cd shared && go test ./embedding/...           # fake 的确定性、维度、失败路径
cd ../data-pipeline
go run . build-documents                        # 生成知识文档
go run . embed                                  # 逐文档向量化 + 审计
```

**读（对着真实产物，理解 scope 与 citable）**

```sql
-- 六种文档各有多少？scope 怎么分？
SELECT retrieval_scope, doc_type, count(*) FROM knowledge_documents
WHERE is_active GROUP BY 1, 2 ORDER BY 1, 2;

-- digest 的元数据里 citable 是什么？
SELECT metadata->>'citable', metadata->>'generated_by', source_review_ids
FROM knowledge_documents WHERE doc_type = 'restaurant_review_digest' LIMIT 5;
```

**改配置 / 改行为，观察变化**

- 把 `EMBEDDING_PROVIDER` 从 `ollama` 改成 `fake`，跑一次 embedding 再跑检索：语义
  通道**还在跑**（trace 里 `ran:true`），但排序质量掉下来。对照 §6.2.4 想清楚「fake」
  和「没配」的区别。
- 打开 `DIGEST_ENABLED=true` 并配置好 chat 模型，跑一次 digest；再**故意**把
  `DIGEST_PROMPT_VERSION` 改一个字符串，观察它如何把缓存全部作废、重算一遍。
- 手动跑一次 `ParseDigestOutput` 的单元测试，看它如何**拒绝**一个包含 bundle 外 review
  id 的输出——体会「编的 ID 整份拒绝」这条规则。

---

## 6.6 参考代码与目录

> 本节是本章涉及的真实实现清单。正文只讲思路，想深入实现的读者从这里进去。

**embedding 端口与适配器**

- [shared/embedding/embedding.go](../../shared/embedding/embedding.go)：`EmbeddingProvider`
  端口——只有 `ModelID()` / `Dimensions()` / `EmbedDocuments()` / `EmbedQuery()` 四个方法
  （**没有** 大而全的 `Embed`）；文档与查询走同一段路径。
- [shared/embedding/ollama/client.go](../../shared/embedding/ollama/client.go)：本地
  Qwen 适配器；POST `/api/embed`（**不是** `/api/embeddings`）；`DefaultTimeout=30s`、
  `DefaultMaxRetries=3`；仅 429/5xx/传输错误重试；返回向量逐个校验，错误码
  `CodeEmbeddingEmpty` / `CodeEmbeddingDimensionMismatch` / `CodeEmbeddingNaN` /
  `CodeEmbeddingInf`；Ollama 用 200 + body `error` 表达的**永久性配置故障不重试**。
- [shared/embedding/fake/fake.go](../../shared/embedding/fake/fake.go)：确定性 embedding；
  `ModelID()="fake-embedding-v1"`、默认 1024 维；向量是输入的 FNV-1a 纯函数；空文本 →
  全零向量；`Failing(err)` 练习失败路径。

**知识文档定义**

- [shared/domain/evidence/evidence.go](../../shared/domain/evidence/evidence.go)：两种
  scope（`restaurant` / `evidence`）、六种 `DocType`；`DocTypeRestaurantReviewDigest`
  的注释直接写明「ranking fuel, never a citation」与 `citable=false` 的意图；
  `MinAnswerableDocTypes = 2`（回答可无保留陈述所需的最小文档种类数）。

**文档生成（knowledge 包）**

- [data-pipeline/internal/pipeline/knowledge/profile.go](../../data-pipeline/internal/pipeline/knowledge/profile.go)：
  `restaurant_profile` 的构造。
- [knowledge/summary.go](../../data-pipeline/internal/pipeline/knowledge/summary.go)：
  按话题的 `restaurant_review_summary`。
- [knowledge/representative.go](../../data-pipeline/internal/pipeline/knowledge/representative.go)：
  代表性评论的抽样（已截断、已 PII 脱敏）。
- [knowledge/hash.go](../../data-pipeline/internal/pipeline/knowledge/hash.go)：
  `ContentHash(scope, docType, restaurantID, content)` 与增量更新语义。
- [knowledge/digest.go](../../data-pipeline/internal/pipeline/knowledge/digest.go)：
  digest 的全部契约——`DigestRulesVersion`/`DigestLLMVersion`、长度门
  `minDigestRunes=150`/`maxDigestRunes=800`、四类正则（URL/邮箱/电话/指令覆盖）、
  `ValidateDigestOutput`、`ParseDigestOutput`（来源 ID 非空 + 必须在 bundle 内 + 去重升序）、
  `BuildDigestDocument`（`citable:false` + scope=restaurant）。

**向量化与审计**

- [data-pipeline/internal/pipeline/embed.go](../../data-pipeline/internal/pipeline/embed.go)：
  `RunEmbed` 分页 500、`PIPELINE_WORKERS`（默认 4）；质量门拒绝单文档、provider 失败
  中止 run；`guardModelChange` 默认拒绝模型切换，`--force-model-change` 才退役旧文档。
- 迁移 [0002_knowledge_document_identity.sql](../../shared/store/postgres/migrations/0002_knowledge_document_identity.sql)：
  `knowledge_documents` 列与幂等唯一索引 `(restaurant_id, retrieval_scope, doc_type, content_hash)`；
  约束 `scope IN ('restaurant','evidence')` 与 `is_active=false OR embedding IS NOT NULL`。
- 迁移 [0003_embedding_audit.sql](../../shared/store/postgres/migrations/0003_embedding_audit.sql)：
  `ingestion_batches` 增列（documents_built/embedded/rejected、embedding_model/dimensions、
  `reject_reasons` jsonb）。
- 迁移 [0014_digest_source_reviews.sql](../../shared/store/postgres/migrations/0014_digest_source_reviews.sql)：
  `source_review_ids bigint[]` 落库。

**本章关键数字**

| 数字 | 含义 |
|---|---|
| 1024 维 / `qwen3-embedding:0.6b` | 向量宽度与本地 embedding 模型 |
| `EMBEDDING_MAX_BATCH=32`（上限 2048） | 批量上限，由实测调出 |
| 2,878 / 8,615 字符 | 代表性评论块的平均 / 最长长度 |
| 28s @ batch16、52s @ batch32 | 本地 CPU 上的实测耗时 |
| 150–800 runes / 40–100 words | digest 的长度门（代码侧 / prompt 侧） |
| **30,198** | 当前 active 知识文档数（3,000 profile + 27,198 evidence） |
| `09985b7` | 「缩短 digest prompt 到 40–100 词」的真实提交 |

> 有了向量和文档，下一步就是**真的用它去搜**——第 7 章从一个最小的
> `ORDER BY embedding <=> $1` 开始，然后亲手撞上那个把索引优化全部作废的事故。
