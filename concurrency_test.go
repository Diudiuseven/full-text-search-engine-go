package fts

import (
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentReadWrite 验证读写并发：
// 查询永远读到某个已提交的完整版本，写入不被慢查询阻塞。
// 需配合 go test -race 运行。
func TestConcurrentReadWrite(t *testing.T) {
	e, _ := Open("")
	e.Add(doc("seed", "initial version zero"))
	if _, err := e.Commit(); err != nil {
		t.Fatal(err)
	}

	const writers = 4
	const readers = 8
	const rounds = 50

	var wg sync.WaitGroup
	var wwg sync.WaitGroup
	stop := make(chan struct{})

	// 写方：持续新增/更新/删除并提交
	for w := 0; w < writers; w++ {
		wwg.Add(1)
		go func(w int) {
			defer wwg.Done()
			for r := 0; r < rounds; r++ {
				id := fmt.Sprintf("w%d-doc%d", w, r%10)
				e.Add(doc(id, fmt.Sprintf("concurrent write token%d round%d", w, r)))
				if r%3 == 0 {
					e.Delete(fmt.Sprintf("w%d-doc%d", w, (r+1)%10))
				}
				if _, err := e.Commit(); err != nil {
					t.Errorf("commit failed: %v", err)
					return
				}
			}
		}(w)
	}

	// 读方：持续查询，校验每次结果内部一致（无重复、排序合法）
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				hits, err := e.Search("concurrent OR initial OR token0", 0)
				if err != nil {
					t.Errorf("search failed: %v", err)
					return
				}
				seen := map[string]bool{}
				for i, h := range hits {
					if seen[h.DocID] {
						t.Errorf("duplicate hit %s in one snapshot", h.DocID)
						return
					}
					seen[h.DocID] = true
					if i > 0 {
						prev := hits[i-1]
						if prev.Score < h.Score ||
							(prev.Score == h.Score && prev.DocID > h.DocID) {
							t.Errorf("hits not ordered: %v then %v", prev, h)
							return
						}
					}
				}
			}
		}(r)
	}

	// 写方全部完成后停止读方
	wwg.Wait()
	close(stop)
	wg.Wait()
}

// TestSnapshotIsolation 验证进行中的查询看到的是一致快照：
// 提交新版本不影响已持有快照的查询结果。
func TestSnapshotIsolation(t *testing.T) {
	e, _ := Open("")
	e.Add(doc("d1", "alpha"))
	if _, err := e.Commit(); err != nil {
		t.Fatal(err)
	}
	snap := e.cur.Load()

	// 提交新版本：删除 d1，新增 d2
	e.Delete("d1")
	e.Add(doc("d2", "alpha beta"))
	if _, err := e.Commit(); err != nil {
		t.Fatal(err)
	}

	// 旧快照仍应看到 d1 且看不到 d2
	q, err := ParseQuery("alpha")
	if err != nil {
		t.Fatal(err)
	}
	hits := snap.search(q, e.params, 0)
	assertIDs(t, hits, "d1")

	// 新查询看到新版本
	assertIDs(t, searchAll(t, e, "alpha"), "d2")
}
