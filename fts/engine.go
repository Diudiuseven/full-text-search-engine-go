package fts

import (
	"sort"
	"sync"
	"sync/atomic"
)

// Options 控制引擎行为。
type Options struct {
	Dir          string // 索引落盘目录，空表示纯内存
	MaxDocBytes  int    // 单文档最大字节数，0 用默认值
	DefaultField string // 未指定字段时的默认检索字段，默认 "body"
	Analyzer     Analyzer
	BM25K1       float64
	BM25B        float64
}

// pendingOp 是一条暂存的写操作；Doc 为 nil 表示删除。
type pendingOp struct {
	Doc *Document
}

// Engine 提供索引维护与查询入口。
// 查询读取 atomic.Pointer 指向的不可变快照，永不阻塞写入；
// 写入在 Commit 时构建新快照并原子切换，读写互不卡住。
type Engine struct {
	opts Options

	cur atomic.Pointer[snapshot] // 当前已提交版本，查询侧无锁读取

	mu           sync.Mutex // 只串行化写侧，查询不需要它
	docs         map[string]Document
	analyzed     map[string]analyzedDoc // 每篇文档的分析缓存，更新只重分析受影响文档
	pending      map[string]pendingOp
	pendAnalyzed map[string]analyzedDoc // 暂存操作对应的分析结果
	version      uint64
}

// Open 打开（或创建）一个引擎实例；Dir 非空时尝试恢复最近一次完整落盘的版本。
func Open(opts Options) (*Engine, error) {
	e := &Engine{
		opts:         opts,
		docs:         map[string]Document{},
		analyzed:     map[string]analyzedDoc{},
		pending:      map[string]pendingOp{},
		pendAnalyzed: map[string]analyzedDoc{},
	}
	e.cur.Store(assembleSnapshot(nil, nil, 0))
	if opts.Dir != "" {
		docs, version, err := loadSnapshot(opts.Dir)
		if err != nil {
			return nil, err
		}
		if docs != nil {
			e.version = version
			if err := e.rebuildLocked(docs); err != nil {
				return nil, err
			}
		}
	}
	return e, nil
}

// rebuildLocked 用给定文档集合全量重建（用于启动恢复）。
func (e *Engine) rebuildLocked(docs map[string]Document) error {
	e.docs = map[string]Document{}
	e.analyzed = map[string]analyzedDoc{}
	for id, doc := range docs {
		d := doc
		if err := validateDocument(&d, &e.opts); err != nil {
			continue // 落盘数据已校验过，防御性跳过
		}
		ad, err := analyzeDocument(&d, &e.opts)
		if err != nil {
			continue
		}
		e.docs[id] = d
		e.analyzed[id] = ad
	}
	e.publishLocked()
	return nil
}

// publishLocked 用当前 docs + analyzed 组装新快照并原子发布。
func (e *Engine) publishLocked() {
	ids := make([]string, 0, len(e.docs))
	for id := range e.docs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	ads := make([]analyzedDoc, len(ids))
	for i, id := range ids {
		ads[i] = e.analyzed[id]
	}
	e.version++
	e.cur.Store(assembleSnapshot(ids, ads, e.version))
}

// Add 暂存一篇新文档；ID 已存在时视为整体更新。
// 文档在暂存时即校验与分析，非法文档立即返回带原因的错误。
func (e *Engine) Add(doc Document) error {
	if err := validateDocument(&doc, &e.opts); err != nil {
		if ee, ok := err.(*Error); ok && ee.DocID == "" {
			ee.DocID = doc.ID
		}
		return err
	}
	ad, err := analyzeDocument(&doc, &e.opts)
	if err != nil {
		return &Error{Kind: ErrDocInvalid, DocID: doc.ID, Detail: err.Error()}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pending[doc.ID] = pendingOp{Doc: &doc}
	// 分析结果随操作暂存，Commit 时直接使用
	e.pendAnalyzed[doc.ID] = ad
	return nil
}

// Delete 暂存删除一篇文档。
func (e *Engine) Delete(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pending[id] = pendingOp{Doc: nil}
	return nil
}

// Commit 把暂存的增删改固化为新版本，查询立即可见。
// 只重新分析受影响的文档，其余复用缓存；重复提交同一 ID 天然去重。
func (e *Engine) Commit() []DocError {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, op := range e.pending {
		if op.Doc == nil {
			delete(e.docs, id)
			delete(e.analyzed, id)
			continue
		}
		e.docs[id] = *op.Doc
		e.analyzed[id] = e.pendAnalyzed[id]
	}
	e.pending = map[string]pendingOp{}
	e.pendAnalyzed = map[string]analyzedDoc{}
	e.publishLocked()
	return nil
}

// Search 在当前已提交快照上执行查询；快照不可变，慢查询不阻塞写入。
func (e *Engine) Search(query string, topK int) ([]Hit, error) {
	q, err := ParseQuery(query)
	if err != nil {
		return nil, err
	}
	snap := e.cur.Load()
	return snap.search(q, &e.opts, topK), nil
}

// Flush 把当前已提交版本原子落盘。
func (e *Engine) Flush() error {
	if e.opts.Dir == "" {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return saveSnapshot(e.opts.Dir, e.docs, e.version)
}

// Close 关闭引擎（当前实现无后台资源）。
func (e *Engine) Close() error { return nil }

// Version 返回当前已提交版本号。
func (e *Engine) Version() uint64 { return e.cur.Load().version }
