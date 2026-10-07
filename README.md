# full-text-search-engine-go

一个可本地稳定复现的全文检索引擎：支持文档增删改、布尔与短语查询、
BM25 相关性排序、读写并发与索引落盘重启复用。

## 快速开始

```go
e, _ := fts.Open("/path/to/index") // 传 "" 则为纯内存引擎

e.Add(fts.Document{ID: "1", Fields: []fts.Field{
    {Name: "body", Kind: fts.FieldKindText, Value: "the quick brown fox"},
    {Name: "tag",  Kind: fts.FieldKindKeyword, Value: "animal"},
}})
res, _ := e.Commit() // 提交后查询立即可见；非法文档记入 res.Failures

hits, _ := e.Search(`tag:animal AND "quick brown"`, 10)
```

写入模型为「暂存 + 提交」：`Add` / `Delete` 只写入暂存区，`Commit`
把暂存变更应用为一个新版本并原子发布。重复 `Add` 同一 ID 视为整体更新，
只重建该文档的索引；`Delete` 后文档不再出现在任何查询结果中。

## 分析流程

所有文档进入索引前经过统一的 `Analyzer`：

- **正文字段（FieldKindText）**：按 Unicode 字母/数字切分并小写化；
  CJK 表意文字逐字成词（unigram）；每个词记录字段内位置（从 0 开始），
  供短语查询使用。
- **关键字字段（FieldKindKeyword）**：整个值小写化后作为单个词项，
  仅支持 `field:value` 精确匹配，不参与位置与长度统计。

分析规则是确定的：同一批文档与同一份配置下，无论建索引的批次划分与
并发度如何，索引内容与查询结果逐条一致（内部文档编号不参与评分与排序，
命中按「得分降序、文档 ID 升序」排列）。

## 查询语义

| 语法 | 含义 |
| --- | --- |
| `term` | 词项查询（在所有正文字段中匹配） |
| `field:term` | 限定字段的词项查询；关键字字段必须用这种形式 |
| `"a b c"` | 短语查询：词必须按相邻顺序出现（基于位置判定） |
| `field:"a b"` | 限定字段的短语；关键字字段按整体值精确匹配 |
| `A AND B` / `A B` | 与（相邻词项默认 AND） |
| `A OR B` | 或 |
| `NOT A` / `A NOT B` | 非（排除） |
| `( ... )` | 括号分组 |

说明：

- 操作符大小写不敏感；查询词项与索引侧走同一分析器（小写化、CJK 切分）。
- 命中集合按文档去重：同一词重复出现或多路组合不会产生重复命中，
  得分只按文档汇总一次。
- 短语命中完全基于位置信息；缺少位置信息的关键字字段不会被当成
  位置短语命中（仅按整体值精确匹配）。
- 空查询返回 `ErrEmptyQuery`；语法非法返回 `*QueryError`（含
  `unclosed_phrase` / `unclosed_paren` / `missing_operand` /
  `unexpected_token` 等原因），不会静默返回全部文档。

## 评分规则（BM25）

每个（字段, 词项）对文档的得分为：

```
idf    = ln(1 + (N - df + 0.5) / (df + 0.5))
score  = idf * tf*(k1+1) / (tf + k1*(1 - b + b*docLen/avgDocLen))
```

- `tf`：词在文档该字段中的词频；`df`：包含该词的文档数；`N`：文档总数。
- `docLen`：文档该字段的词数；`avgDocLen`：该字段全部文档的平均词数
  （缺失该字段的文档按 0 计入平均）。
- 默认参数 `k1 = 1.2`、`b = 0.75`（`DefaultBM25Params`）。
- 文档得分 = 各字段得分之和；AND/OR 组合时子查询得分相加；
  NOT 分支只作过滤、贡献 0 分；短语得分 = 各组成词项得分之和。
- 关键字字段精确匹配的得分为该词项的 `idf`（无词频/长度概念）。

评分只依赖词频、文档频率与文档长度等顺序无关的统计量，因此同一份
数据下评分与排序完全可复现。

## 并发模型

索引是不可变快照：查询通过原子指针读取当前已提交快照，写入在快照的
写时拷贝上修改后原子切换。因此：

- 查询永远读到某个已提交的完整版本，不会读到写了一半的文档或字段级混合；
- 慢查询不阻塞写入，写入也不影响进行中查询所持有的快照；
- 多个写方的提交被串行化，暂存区操作（Add/Delete）本身也是并发安全的。

## 持久化与崩溃恢复

目录布局：

```
MANIFEST         当前已提交版本号、数据文件名与 SHA-256 校验和
index-<ver>.db   已完整写入的快照数据
*.tmp            写入中的临时文件（未提交）
```

提交流程：写数据 tmp → fsync → 重命名为 `.db` → 同样方式更新 MANIFEST →
清理旧版本文件。任一环节中断，MANIFEST 仍指向上一完整版本，重启后要么
完整使用旧版本、要么完整使用新版本；残留的 tmp 文件在下次 `Open` 时清理。
加载时校验 SHA-256，损坏的已提交文件会返回明确错误，不会产生部分加载。

## 文档校验与错误

单篇文档不合法不会导致整次提交失败，而是被跳过并记入
`CommitResult.Failures`（`*DocError`，含文档 ID 与可区分的原因）：

| 原因 | 说明 |
| --- | --- |
| `empty_id` | 文档 ID 为空 |
| `no_fields` | 没有任何字段 |
| `empty_field_name` | 字段名为空 |
| `field_too_large` | 单字段超过 `MaxFieldBytes`（256 KiB） |
| `doc_too_large` | 全文档超过 `MaxDocumentBytes`（1 MiB） |
| `invalid_utf8` | 字段内容不是合法 UTF-8 |

## 配置项

| 项 | 位置 | 默认 |
| --- | --- | --- |
| 持久化目录 | `Open(dir)` | `""`（纯内存） |
| BM25 k1 / b | `BM25Params`（`DefaultBM25Params()`） | 1.2 / 0.75 |
| 单字段最大字节 | `MaxFieldBytes` | 256 KiB |
| 单文档最大字节 | `MaxDocumentBytes` | 1 MiB |
| 结果条数上限 | `Search(query, limit)` 的 `limit` | `0` 表示不限 |

## 测试

```sh
go test ./...        # 全部测试
go test -race ./...  # 含并发读写的竞态检测
```

覆盖：增量更新/删除与全量重建一致性、并发读写快照隔离、布尔与短语查询、
排序跨批次一致性、重启恢复与崩溃清理、非法文档与非法查询。
