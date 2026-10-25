# 第 7 章 向量检索第一课：pgvector 与 HNSW

> 上一章：[第 6 章 把文字变成向量](06-把文字变成向量-embedding-与知识文档.md) ｜ 下一章：[第 8 章 单通道为什么不够](08-单通道为什么不够-多路召回与融合.md)
>
> 本章涉及的仓库真实文件、函数与实测数字集中在文末 [§7.6 参考代码与目录](#76-参考代码与目录)。

---

## 7.1 坏 case 开场：一句「加个 WHERE」把索引全废了

第 6 章结束时，`knowledge_documents` 里躺着 3 万篇带着 1024 维向量的文档。现在写
第一条真正的检索 SQL：

```sql
SELECT document_id FROM knowledge_documents
WHERE is_active AND embedding IS NOT NULL
ORDER BY embedding <=> $1
LIMIT 10;
```

`<=>` 是 pgvector 的 cosine 距离运算符，`ORDER BY … LIMIT 10` 就是「找最近的 10 篇」。
建好 HNSW 索引后，`EXPLAIN` 显示它真的走了索引——很好。

然后需求来了：用户要「**曼哈顿**的意大利菜」，于是顺手加一个过滤条件：

```sql
WHERE is_active AND borough = 'manhattan'
ORDER BY embedding <=> $1
LIMIT 10;
```

看起来只是「多筛掉一些行」。但 `EXPLAIN` 会给你一个**让人背脊发凉**的结果：

```sql
ORDER BY embedding <=> q                 -- Index Scan using embedding_hnsw
WHERE borough = 'manhattan' ORDER BY ..  -- Seq Scan, 49759 rows filtered by hand
```

**加一个 `WHERE`，索引扫描变成了顺序扫描。** 系统仍然会给你「正确」的答案（顺序扫
全表、逐行判断、再排序），50k 行的实测里 **49,759 行被逐条过滤掉**——结果对，但慢得
离谱，而且**随着语料增长会线性恶化**。

要理解为什么会这样，得先理解 HNSW 这类**近似最近邻（ANN）索引**到底是什么。

---

## 7.2 概念与原理

### 7.2.1 为什么需要 ANN 索引

暴力做法是：把查询向量和库里**每一篇**文档算一次距离，排序取前 k。这叫**精确最近邻**，
结果 100% 正确，但复杂度是 `O(N)`——3 万篇还行，300 万篇每查一次要算 300 万次 1024 维
点积，不可接受。

**ANN（Approximate Nearest Neighbor，近似最近邻）** 的思路是：**放弃「保证找到最近
的那一个」，换取「通常能找到很近的那几个」**。用一个能在亚线性时间内导航的数据结构，
把候选集从「全部」缩小到「可能相关的一小撮」。这正是「近似」二字的含义——**它不是
一个精确的过滤器，而是一个快速的导航器。**

### 7.2.2 HNSW 核心思想：分层可导航小世界图

HNSW（Hierarchical Navigable Small World）是目前最主流的 ANN 索引之一。它的核心
思想可以用一句话概括：

> **建一张多层图，上层稀疏（长跳），下层稠密（短跳）；查询时从上层的「高速公路」
> 快速逼近，再逐层下沉做「精细搜索」。**

想象你在一座城市里找某个地址：

```text
第 2 层（最稀疏）：只有寥寥几个节点，边很长  → 从全国视角快速跳到「大概在这个区」
第 1 层（中等）  ：节点变多，边变短          → 从区缩小到「大概在这条街」
第 0 层（最稠密） ：几乎是所有节点，边很短    → 在街区内逐个比对，找到最近的门牌
```

查询从最高层的入口点出发，贪心地往「离目标更近」的邻居走；走到走不动了，**下沉**到
下一层继续；如此直到第 0 层，得到一个近邻。

这里有一个**决定成败的工程细节**：每次搜索要维护多大的候选队列？这个参数叫
`hnsw.ef_search`（默认 40）。它不是「只看 40 个邻居就再也不回头」的固定候选数，而是
搜索过程中**动态候选队列的宽度**：每轮从队列里取最近的一个展开，把更近的邻居补进来，
队列满且没有更近的候选时才停。

所以正确的图像是**一条有界的束（beam）**：**束宽由 `ef_search` 控制，调大只会降低漏
召回的概率，不改变「近似」这个性质**——它仍然可能错过真正的最近邻，只是错过的概率
更小、代价是每次查询多算一些距离。记住「有界的束」这个图像，它是 §7.2.4 那个事故的
全部原因。

### 7.2.3 三种距离度量：为什么选 cosine

pgvector 支持三种距离运算符：

| 运算符 | 度量 | 直觉 | 适合 |
|---|---|---|---|
| `<=>` | cosine 距离 | 只看方向 | **文本 embedding（本项目）** |
| `<#>` | 负内积 | 方向 + 长度 | 已归一化、且想利用模长 |
| `<->` | L2 欧氏距离 | 直线距离 | 图像、坐标类向量 |

第 6 章讲过：文本 embedding 关心「意思像不像」（方向），而不关心向量有多长（长度是
噪声），所以选 **cosine**。在 pgvector 里它落到索引的 `vector_cosine_ops` 操作符类上。

> 一个必须记住的坑：**距离度量决定了索引，索引决定了运算符。** 你建了
> `vector_cosine_ops` 的 HNSW，就必须用 `<=>` 查询；用 `<->` 查同一列，索引**不会
> 被用上**。度量选错，不是「差一点」，是「索引直接失效」。

### 7.2.4 关键事故：为什么 `WHERE` 会毁掉索引

回到 §7.1。问题的根源是一个很多人没意识到的概念：**HNSW 不是「过滤器」，它是
「排序结构」。**

传统 btree 索引的工作方式是「**先过滤后取行**」：`WHERE borough='manhattan'` 直接把
不满足的行排除，只访问剩下的。

HNSW 索引的工作方式是「**先按向量距离导航、后返回行**」：`ORDER BY embedding <=> q`
让索引沿着束走，**按「离查询向量由近到远」的顺序**吐出候选行。它返回的是**一个已经
排好序的流**。

这两件事在**没加 `WHERE` 时完美契合**（索引直接按距离给前 10 个）。但一旦加了
`WHERE borough='manhattan'`，PostgreSQL 面临一个两难：

- 走 HNSW 索引：它按距离顺序吐行，但**没有按 borough 排序**。要应用过滤，只能一行行
  检查「这行是曼哈顿吗？不是就丢掉」——可 HNSW 的束**只往前走一小段**（默认
  `ef_search=40` 的候选队列），如果你要的 borough 恰好只占这一小撮里的几行，索引吐完
  束就停了，**你拿不到足够的结果**。
- 走顺序扫描：全表逐行过滤 borough，把满足的挑出来，再排序——**正确，但 `O(N)`**。

PostgreSQL 的规划器会在两条路之间按代价估。**在本次 5 万行、过滤器选择性较低的实测
里**，它保守地选了顺序扫描——把「可能出错」换成「一定很慢」。注意这**不是「加 WHERE
必然退化为 Seq Scan」的普遍规律**：过滤选择性很高时，规划器完全可能走索引、再逐行
过滤，代价是**返回不足 k 条**——因为束被过滤抽干，而它不会回头补。那是比「慢」更隐蔽
的失败，§7.2.7 会看到它的极端版本。

> **一句话总结这次事故**：`WHERE` 让「按距离排序的流」和「按属性过滤」两个需求打架；
> 在本项目的语料和这次实测的选择性下，规划器宁可全表扫描也不愿冒漏结果的风险。

### 7.2.5 解法一：硬过滤先于向量召回

事故的根源既然是「过滤和向量排序在同一个查询里打架」，第一层解法就是把它们**分开**：

> **硬过滤先于向量召回（hard filters run before vector recall）。**

也就是说：**先用结构化条件把候选集缩小，再在这个小集合里做向量检索。** 这在本项目里
是一条**贯穿全局的纪律**，不只是这一个查询的技巧。它的推论是：

- 「曼哈顿的意大利菜」这句话里，`borough=manhattan` 和 `cuisine=italian` 先在
  **结构化通道**里被应用（第 8 章），得到一个候选餐厅集合；
- 向量通道**只在这个集合范围里**做语义排序。

这样做还有一个额外的好处：**它让「硬条件必须 100% 正确」成为可能**——因为硬条件不再
依赖向量索引的近似行为，而是走普通的 btree/`=`/`ANY`，结果是确定的。

### 7.2.6 解法二：把过滤条件「放进索引」——partial HNSW

「先过滤」解决了「过滤和排序打架」，但还有一种情况：过滤范围**本身仍然很大**，大到
向量检索还是得在一个大集合上跑。这时需要第二层解法：**把过滤条件编进索引。**

PostgreSQL 的 **partial index（部分索引）** 允许你这样建索引：

```sql
CREATE INDEX knowledge_documents_hnsw_manhattan
  ON knowledge_documents USING hnsw (embedding vector_cosine_ops)
  WHERE is_active AND borough = 'manhattan';
```

这个索引**只包含曼哈顿的行**。于是「曼哈顿的语义检索」这个查询**谓词完全匹配索引的
谓词**，规划器可以放心地走它——因为**索引里每一行都是曼哈顿的，不存在「过滤把索引的
束抽干」的问题**。

注意这里**没有**任何 `USE INDEX` 提示，查询代码也不认识索引名：**查询不需要指定索引，
规划器会按谓词自己匹配**。所以索引叫什么名字无所谓，**索引谓词写成什么样才有所谓**
——谓词一改，原来的查询就匹配不上，悄悄退回另一种计划。这也是下面的测试为什么断言
「计划里出现了某个具名索引」，而不是断言「代码里写了索引名」。

README 里的实测印证了这个转变：

```sql
-- 之前：加 WHERE → 顺序扫描
WHERE borough = 'manhattan' ORDER BY ..  -- Seq Scan

-- 之后：谓词匹配 partial index → 索引扫描
WHERE is_active AND borough = 'manhattan' ORDER BY embedding <=> q
-- Index Scan using knowledge_documents_hnsw_manhattan
```

### 7.2.7 这个事故的「续集」：scope 过滤下的静默错误

上面那个事故是**慢**。项目后面又发现了一个更可怕的版本——它**不慢，而是给出错误答案**。

第 6 章讲过，知识文档分 `restaurant` 和 `evidence` 两个 scope，而**任何一次检索都必须
只查其中一个 scope**。加一个 `retrieval_scope = 'restaurant'` 到查询里，本该是小事。
但在 **30,198 篇 active 文档、曼哈顿分区**里：

```text
retrieval_scope | rows
----------------+------
restaurant      |  2,010
evidence        | 18,287     ← 90% 是 evidence
```

HNSW 的束（默认 `ef_search=40` 时）只看到约 40 个候选。当你要 `restaurant` scope 的
召回时，索引下沉到这个**90% 是 evidence** 的分区，捞上来的 40 个邻居**几乎全是
evidence**，随后的 scope 过滤**把它们全部拒绝**——于是：

```text
Index Scan using knowledge_documents_hnsw_manhattan
  rows=0 ... Rows Removed by Filter: 40
```

**返回 0 行。** 而把索引关掉、走精确搜索，同样的问题能找到 5 篇文档（42ms）。
**「召回返回空」和「精确搜索有结果」放在一起，就是一个错误答案，而不是一个慢答案。**

更糟的是：这个问题**靠调参治不好**。把 `hnsw.ef_search` 调大只是降低发生频率，并不
消除——因为束**仍然**在和那 18,287 行 evidence 抢位置。

解法与 §7.2.6 同理，只是把分区键从「一个」变成「两个」：**按 `(borough, retrieval_scope)`
组合建 partial HNSW 索引**。迁移 `0004` 里用一段 PL/pgSQL 循环，为**五个区 × 两个 scope**
各建一个索引，共**十个**；旧索引**保留不删**（见下文），加上 0001 留下的一个不分区与
五个按区分区的索引，全库此时一共**十六个** HNSW 索引。每个新索引的谓词都写成：

```sql
WHERE is_active AND borough = 'manhattan' AND retrieval_scope = 'restaurant'
```

于是**召回自己的谓词进了索引**，图里只包含能满足它的行，再也不会出现「束被抽干」。
同一个查询的实测：慢查询的 **42ms → 0.6ms**，而且**没有一行被过滤丢弃**。

> **这个事故最大的教训是它的「静默性」。** §7.2.4 的版本慢但正确，你迟早会发现；
> 这个版本**快而且错**——它返回空列表，看起来像「这个区没有符合条件的店」，只有
> 对着精确搜索对拍才能发现。**「返回错误答案的索引问题必须用 schema 改，不能用调参
> 绕过」**，这就是迁移注释里那句话：「the fix is a schema change rather than a tuning knob」。

### 7.2.8 已知边界：三重过滤仍会退化成 filter-then-sort

诚实地说清楚：**解法一和解法二没有覆盖所有情况。** 当一个查询同时叠加
**行政区 + 半径 + 菜系**三个过滤时，仍然会退化成 filter-then-sort。项目**接受**这个
边界，并把「语料涨一个数量级时重新评估」记在 schema 里，而不是假装它不存在。

这是工程上很成熟的态度：**已知的、被记录在案的降级，好过被遗忘的隐患。** 第 10 章的
覆盖率棘轮 `TestScopeFilteredQueryStillUsesAnIndex` 也是同一个思路——把「当前计划的
形状」本身也写成测试，这样**将来它变了，是一个决策，而不是一个惊喜**。

### 7.2.9 一个必须记住的测试经验：小表测不出索引

写这条索引的测试时，一个陷阱是：**小表上规划器根本不会选索引。** 项目实测（同一份
schema）：

```text
 5,000 行 → 全局索引和 borough 索引都退化成顺序扫描
10,000 行 → 全局 HNSW 被选中，borough 索引仍是顺序扫描
50,000 行 → 两者都被选中
```

所以一句「断言走了索引」的测试，如果写在几百行的表上，**断言的是规划器的成本模型，
不是你想要的索引**——它会静默地永远不会失败。项目因此把测试语料固定成 **50,000 行**
（`indexSearchSeedRows`），并且说明这个数量级「正是本语料实际会到达的（3,000 profile +
约 15,000 evidence）」。**测试语料的规模，本身就是一个必须用真实数量级校准的参数。**

---

## 7.3 承载思路的最小示例

把 §7.2 的解法写成教学骨架：

```sql
-- ① 建索引：把「每次检索都会带上的过滤条件」编进索引谓词
CREATE INDEX IF NOT EXISTS knowledge_documents_hnsw_manhattan_restaurant
  ON knowledge_documents USING hnsw (embedding vector_cosine_ops)
  WHERE is_active AND borough = 'manhattan' AND retrieval_scope = 'restaurant';

-- ② 查询：WHERE 谓词要与索引谓词一致，规划器才会选它
SELECT document_id, title
FROM knowledge_documents
WHERE is_active
  AND borough = 'manhattan'
  AND retrieval_scope = 'restaurant'
  AND embedding IS NOT NULL
ORDER BY embedding <=> $1     -- 用 cosine，和 vector_cosine_ops 对齐
LIMIT 10;
```

两部分必须**成对**出现：

1. **索引谓词**（`WHERE is_active AND borough=… AND retrieval_scope=…`）决定了索引里
   有哪些行；
2. **查询谓词**必须包含同一组条件，规划器才能证明「这个查询只用到索引覆盖的行」，
   从而放心走索引。**少一个条件、或者换个 borough 值，都会退回顺序扫描。**

而 §7.2.9 的测试经验落成一条断言原则：

```text
种子语料规模必须 ≈ 真实语料规模（本项目 50k 行），
否则「断言走了索引」的测试会因为规划器选择顺序扫描而永远失败不了。
并且断言不能只查「用了哪个索引」，还要断言「没有 Seq Scan」——
因为一个「用了索引 + 又顺序扫描一遍」的计划是更慢的另一种计划。
```

---

## 7.4 取舍与边界

- **ANN 是近似的，不要把它当过滤器。** HNSW 本质是 `ORDER BY` 结构；一旦在同一个
  查询里叠加 `WHERE`，要么规划器退缩成顺序扫描（慢但正确），要么束被过滤抽干
  （快但可能返回不足 k 条）。**这两种在本项目的语料上都真实出现过。** 这条认知是
  整个第二篇的枢纽。
- **两种解法分工**：过滤范围小且能用 `=`/`ANY` 表达 → 「先过滤后召回」；过滤是查询的
  固有维度（如 borough + scope）→ 「partial index」。
- **返回空不一定是「没有」，可能是索引抽干了。** 凡是 ANN 索引上的空结果，都要用
  精确搜索对拍一次。这个「对拍」意识在第 8 章的池内重排、第 10 章的评测里反复出现。
- **已知降级要写进 schema，不要写进心里。** 三重过滤仍会 filter-then-sort，被明确
  记录并附上「何时重新评估」的触发条件。
- **十六个索引不是浪费。** 0001 建了一个不分区 + 每区一个（共六个），0004 又为
  每区每 scope 各建一个（共十个），旧索引保留不删，是为了让多种查询谓词都能命中
  索引。索引有写入与维护成本，但对「读多写少、且读法高度固定」的语料，这是划算的。
- **索引选择的测试要用真实规模。** 5k/10k/50k 的差异说明：小语料上的索引断言是假的。
- **度量、索引、运算符三者必须一致。** cosine ↔ `vector_cosine_ops` ↔ `<=>`。任何
  一个错位，索引都会静默失效。

---

## 7.5 课后实验

**跑（需要本地 Postgres）**

```bash
make pg-up && make migrate
# Postgres 适配器契约套件：断言 0001 的六个 HNSW 索引等具名索引真的存在
make test-postgres
# 索引选择测试（会 seed 约 5 万行，几分钟）。
# 根目录没有 go.mod，必须在 shared/ 模块内跑；0004 的十个分区索引由
# TestMigration0004* 守护，一并跑掉。
cd shared && PLATEPILOT_REQUIRE_DB=1 go test ./store/postgres/ \
  -run 'TestBoroughQueryUsesTheBoroughPartialIndex|TestMigration0004' -v
```

**看 EXPLAIN（亲手复现那个事故）**

```sql
-- ① 不带 WHERE：应该走索引
EXPLAIN ANALYZE SELECT document_id FROM knowledge_documents
WHERE is_active ORDER BY embedding <=> $1 LIMIT 10;

-- ② 带上 borough 但不匹配任何 partial index 的谓词：观察是否退化成 Seq Scan
EXPLAIN ANALYZE SELECT document_id FROM knowledge_documents
WHERE is_active AND borough = 'manhattan' ORDER BY embedding <=> $1 LIMIT 10;

-- ③ 对比：谓词与 partial index 一致时又变回 Index Scan
--    （把上面的 scope 条件补齐，观察计划变化）
```

**改配置 / 改行为，观察变化**

- 把 `hnsw.ef_search` 从默认 40 调大再跑 §7.2.7 那个 scope 过滤查询，观察「返回 0 行」
  出现频率是不是下降——然后想清楚：**为什么它只是降低频率、不是消除问题？**
- 把一个 partial 索引的 borough 换个值（或删一个），看 `TestBoroughQueryUsesTheBoroughPartialIndex`
  会不会红，理解「索引谓词与查询谓词必须成对」。
- 读 `0004_scope_partitioned_hnsw.sql` 的注释全文，里面有这次事故完整的实测数字
  （`rows=0 / Rows Removed by Filter: 40`、42ms vs 0.6ms）。

---

## 7.6 参考代码与目录

> 本节是本章涉及的真实实现清单。正文只讲思路，想深入实现的读者从这里进去。

**schema 与索引**

- 迁移 [0001_init.sql](../../shared/store/postgres/migrations/0001_init.sql)：`knowledge_documents`
  建表，`embedding vector(1024)`，初始的 borough partial HNSW 索引。
- 迁移 [0004_scope_partitioned_hnsw.sql](../../shared/store/postgres/migrations/0004_scope_partitioned_hnsw.sql)：
  **本章的重头戏**。注释逐字记录了 scope 过滤下「返回 0 行」的事故（束宽 40、
  曼哈顿分区 2,010 restaurant vs 18,287 evidence、`Rows Removed by Filter: 40`、
  索引禁用后 42ms 找到 5 行、迁移后 0.6ms）；PL/pgSQL 循环为「五区 × 两 scope」
  建 partial HNSW；**旧索引保留而不删除**的理由（删除要在 3 万行表上持 ACCESS EXCLUSIVE
  锁重建，且规划器本就更偏好窄索引，旧索引对「不带 scope 的召回」仍然正确）。

**行为验证**

- [shared/store/postgres/knowledge_index_test.go](../../shared/store/postgres/knowledge_index_test.go)：
  `TestBoroughQueryUsesTheBoroughPartialIndex`（borough 谓词必须进索引、
  且不得有 `Seq Scan`）、`TestUnfilteredQueryUsesTheGlobalIndex`、
  `TestScopeFilteredQueryStillUsesAnIndex`（记录当前计划形状）、
  `TestVectorSearchFindsTheExactMatch`（自查询必须返回自己、距离 ≈0）；
  `indexSearchSeedRows = 50000` 及其注释（5k/10k/50k 的规划器选择实测）；
  `planIndexNames` 用**整词**而非子串比较（否则 `knowledge_documents_hnsw` 会被
  `knowledge_documents_hnsw_manhattan` 蒙混过关）。

**README 的检索小节**

- [README.md](../../README.md)：四类查询能力表（btree / PostGIS / pgvector / pg_trgm）；
  「hard filters before vector recall」的实测 EXPLAIN；partial HNSW 的解法；
  三重过滤仍 filter-then-sort 的**已知限制**。

**本章关键数字**

| 数字 | 含义 |
|---|---|
| **49,759** | 50k 行实测中，加 `WHERE` 后被逐条手工过滤的行数 |
| **40** | `hnsw.ef_search` 默认束宽（候选队列宽度） |
| **2,010 / 18,287** | 曼哈顿分区里 restaurant / evidence 文档数（90% 是 evidence） |
| **0 行** | scope 未分区时的错误答案；`Rows Removed by Filter: 40` |
| **42ms → 0.6ms** | 分区前后同一查询的耗时 |
| 5k / 10k / 50k | 规划器在三种规模下的索引选择（只有 50k 两者都选中） |
| **16 个 HNSW 索引** | 0001 建 6 个（1 个不分区 + 5 区各 1），0004 再建 10 个（5 区 × 2 scope），旧索引保留；契约套件断言 0001 那 6 个存在，0004 那 10 个由迁移文件/幂等测试守护 |

> 索引这条路走到了边界。下一章要面对一个更根本的问题：**单靠向量一路召回，为什么
> 还是不够**——精确店名、硬条件、高分店沉底，这些坑逼出四个通道和一套融合机制。
