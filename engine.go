package fts

import (
	"sort"
	"sync"
	"sync/atomic"
)

// CommitResult 汇总一次提交的结果。
type CommitResult struct {
	Indexed  int         // 成功索引的文档数
	Deleted  int         // 成功删除的文档数
	Failures []*DocError // 被拒绝的文档及原因
	Version  uint64      // 提交产生的新版本号
}

// Engine 是全文检索引擎入口。
// 写入（提交）与查询并发安全：查询读取当前已提交快照（原子指针），
// 写入先构建新快照再原子切换，慢查询不阻塞写入，
// 写入也不会让进行中的查询看到撕裂的状态。
type Engine struct {
	analyzer *Analyzer
	params   BM25Params

	mu         sync.Mutex           // 串行化写入方与暂存区
	pending    map[string]*Document // 暂存的新增/更新（后写覆盖先写）
	deletes    map[string]bool      // 暂存的删除
	docStore   map[string]Document  // 已提交的全部文档（用于增量移除）
	nextDocNum uint32

	cur     atomic.Pointer[snapshot] // 当前已提交快照
	version atomic.Uint64            // 当前已提交版本号

	dir string // 持久化目录；为空则不落盘
}

// Open 打开（或创建）位于 dir 的引擎；dir 为空时为纯内存引擎。
// 若目录中存在已提交索引，则加载最近一次完整提交的版本。
func Open(dir string) (*Engine, error) {
	e := &Engine{
		analyzer: NewAnalyzer(),
		params:   DefaultBM25Params(),
		pending:  make(map[string]*Document),
		deletes:  make(map[string]bool),
		docStore: make(map[string]Document),
		dir:      dir,
	}
	var snap *snapshot
	var err error
	if dir != "" {
		var version uint64
		snap, e.docStore, e.nextDocNum, version, err = loadSnapshot(dir)
		if err != nil {
			return nil, err
		}
		e.version.Store(version)
	} else {
		snap = newSnapshot()
	}
	e.cur.Store(snap)
	return e, nil
}

// Add 将文档加入暂存，Commit 时生效。重复 ID 视为整体更新。
func (e *Engine) Add(docs ...Document) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range docs {
		d := docs[i]
		e.pending[d.ID] = &d
		delete(e.deletes, d.ID)
	}
}

// Delete 将文档 ID 标记为删除，Commit 时生效。
func (e *Engine) Delete(ids ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, id := range ids {
		delete(e.pending, id)
		e.deletes[id] = true
	}
}

// Commit 将暂存的新增/更新/删除应用为一个新版本并原子发布。
// 不合法文档被跳过并记录原因，不会导致整次提交失败。
// 同一批中重复提交的同一 ID 以最后一份为准，先前的记录为重复失败。
func (e *Engine) Commit() (*CommitResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	res := &CommitResult{}

	// 校验暂存文档，分离出可索引集合
	valid := make(map[string]*Document, len(e.pending))
	for id, d := range e.pending {
		if derr := validateDocument(*d); derr != nil {
			res.Failures = append(res.Failures, derr)
			continue
		}
		valid[id] = d
	}

	// 在快照副本上应用变更（只重建受影响的文档）
	next := e.cur.Load().clone()

	for id := range e.deletes {
		old, ok := e.docStore[id]
		if !ok {
			continue
		}
		ad := analyzeDocument(e.analyzer, old)
		ad.docNum = next.docNums[id]
		next.removeDoc(ad)
		delete(e.docStore, id)
		res.Deleted++
	}

	// 按文档 ID 排序处理，保证提交行为确定
	ids := make([]string, 0, len(valid))
	for id := range valid {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		d := valid[id]
		docNum, exists := next.docNums[id]
		if exists {
			// 整体更新：先移除旧版本
			old := analyzeDocument(e.analyzer, e.docStore[id])
			old.docNum = docNum
			next.removeDoc(old)
		} else {
			docNum = e.nextDocNum
			e.nextDocNum++
		}
		ad := analyzeDocument(e.analyzer, *d)
		ad.docNum = docNum
		next.addDoc(ad)
		e.docStore[id] = *d
		res.Indexed++
	}

	newVersion := e.version.Load() + 1

	// 先落盘再发布：落盘失败则本次提交整体不生效
	if e.dir != "" {
		if err := saveSnapshot(e.dir, next, e.docStore, e.nextDocNum, newVersion); err != nil {
			return nil, err
		}
	}

	e.cur.Store(next)
	e.version.Store(newVersion)
	res.Version = newVersion

	e.pending = make(map[string]*Document)
	e.deletes = make(map[string]bool)
	return res, nil
}

// Search 在当前已提交快照上执行查询。
// 查询为空或语法非法时返回 *QueryError，不会静默返回全部文档。
func (e *Engine) Search(input string, limit int) ([]Hit, error) {
	q, err := ParseQuery(input)
	if err != nil {
		return nil, err
	}
	return e.cur.Load().search(q, e.params, limit), nil
}

// Version 返回当前已提交版本号。
func (e *Engine) Version() uint64 {
	return e.version.Load()
}

// Close 关闭引擎。当前实现中落盘在 Commit 内同步完成，无后台任务。
func (e *Engine) Close() error {
	return nil
}
