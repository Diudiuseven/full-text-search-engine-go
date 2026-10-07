# full-text-search-engine-go

一个可本地复现的嵌入式全文检索引擎。支持文档的增删改、布尔与短语查询、
BM25 相关性排序、并发读写与索引落盘恢复。

## 快速开始

```go
e, _ := fts.Open(fts.Options{Dir: "./index"}) // Dir 为空则纯内存
e.Add(fts.Document{ID: "d1", Fields: []fts.Field{
    {Name: "body", Kind: fts.FieldText, Value: "the quick brown fox"},
    {Name: "tag",  Kind: fts.FieldKeyword, Value: "animals"},
}})
e.Commit()                    // 提交后查询立即可见
hits, _ := e.Search(`"quick brown" AND tag:animals`, 10)
e.Flush()                     // 原子落盘，重启后 Open 自动恢复
```

## 文档模型与分析流程

- 文档由调用方给定唯一 `ID` 与若干字段。同一 `ID` 重复提交视为**整体更新**，
  不会产生重复命中或重复计分。
- 字段分两类，进入索引前走统一的分析流程但区别对待：
  - `FieldText`（正文）：按非字母数字字符切词、统一小写，词位置递增记录，
    供短语查询做位置判定；
  - `FieldKeyword`（独立匹配字段）：整体小写后作为**一个**词项，
    只能整值匹配（`tag:go` 或 `tag:"go lang"`）。
- 非法文档（空 ID、非 UTF-8、超过 `MaxDocBytes`）在 `Add` 时即被拒绝，
  错误带 `ErrKind` 分类与具体文档 ID，不影响同批其他文档。

## 查询语义

```
query := or
or    := and ( OR and )*
and   := not ( (AND | 空白) not )*        // 相邻词项默认 AND
not   := NOT not | primary ( NOT primary )*
primary := '(' or ')' | field ':' word | field ':' "phrase" | "phrase" | word
```

- `AND` / `OR` / `NOT` 为大小写敏感关键字；`a NOT b` 表示命中 a 且排除 b，
  前缀 `NOT a` 表示全部文档中排除 a。
- 布尔组合按文档集合求交/并/差，同一词重复出现或多次组合不会重复命中。
- 短语查询 `"w1 w2 ..."` 基于索引中记录的词位置做相邻性判定；
  缺少位置信息的字段（如 keyword 字段）不会被误判为短语命中。
- 空查询返回 `ErrEmptyQuery`，语法非法返回 `ErrQuerySyntax`，
  不会静默返回全部文档。

## 评分规则（BM25）

每篇命中文档的得分为各命中词项 BM25 得分之和：

```
idf(t)  = ln(1 + (N - df + 0.5) / (df + 0.5))
tf部分  = tf * (k1 + 1) / (tf + k1 * (1 - b + b * dl / avgdl))
score   = Σ idf(t) * tf部分
```

- `tf`：词在文档中的词频；`df`：词出现在多少篇文档中；
  `dl`/`avgdl`：文档长度与平均长度（长度归一化）。
- `k1`、`b` 分别由 `Options.BM25K1`（默认 1.2）、`Options.BM25B`（默认 0.75）控制。
- 短语命中把短语出现次数作为 `tf`，各词 `idf` 之和作为 `idf`。
- 排序按得分降序，同分按文档 ID 字典序升序，结果完全可复现。

## 一致性保证

- **确定性**：内部 docID 按外部 ID 字典序分配，同一文档集合无论分多少批、
  以何种并发度提交，同一查询的命中集合与顺序逐条一致；
  增量维护的结果与全量重建完全一致。
- **快照隔离**：查询读取 `atomic.Pointer` 指向的不可变快照，写入在
  `Commit` 时构建新快照并原子切换。查询永远看到某个已提交的完整版本，
  不会读到写了一半的文档，慢查询不阻塞写入，写入也不影响进行中的查询。
- **落盘原子性**：快照写 `snapshot-<version>.tmp` 后 rename，
  再原子更新 `CURRENT` 指针；文件带 magic 头与 CRC32 校验。
  中断只会留下被忽略的 `.tmp` 残文件或未被指向的孤儿版本；
  `CURRENT` 指向损坏版本时自动回退到最近可用版本。
  已 `Flush` 与未 `Flush` 的提交严格区分，未落盘的提交重启后不可见。

## 配置项（Options）

| 字段 | 默认 | 说明 |
|---|---|---|
| `Dir` | `""` | 落盘目录，空为纯内存 |
| `MaxDocBytes` | 1 MiB | 单文档全部字段总字节上限 |
| `DefaultField` | `"body"` | 未指定字段时的检索字段 |
| `Analyzer` | 标准分析器 | 覆盖正文字段分析器 |
| `BM25K1` / `BM25B` | 1.2 / 0.75 | BM25 参数 |

## 测试

```sh
go test -race ./...
```

覆盖：增量更新与删除、并发读写、布尔与短语查询、排序一致性、
重启恢复（含崩溃残留）与非法输入。
