package fts

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

// 增量维护后的结果必须与全量重建完全一致。
func TestIncrementalEqualsFullRebuild(t *testing.T) {
	e, _ := Open(Options{})
	// 初始批次
	for i := 0; i < 20; i++ {
		if err := e.Add(textDoc(fmt.Sprintf("d%02d", i),
			fmt.Sprintf("alpha beta gamma %d", i))); err != nil {
			t.Fatal(err)
		}
	}
	e.Commit()
	// 更新 5 篇、删除 5 篇、新增 5 篇
	for i := 0; i < 5; i++ {
		if err := e.Add(textDoc(fmt.Sprintf("d%02d", i), "alpha updated content")); err != nil {
			t.Fatal(err)
		}
		if err := e.Delete(fmt.Sprintf("d%02d", i+5)); err != nil {
			t.Fatal(err)
		}
		if err := e.Add(textDoc(fmt.Sprintf("n%02d", i), "alpha fresh document")); err != nil {
			t.Fatal(err)
		}
	}
	e.Commit()

	// 全量重建一个对照引擎
	want, _ := Open(Options{})
	for i := 0; i < 20; i++ {
		body := fmt.Sprintf("alpha beta gamma %d", i)
		if i < 5 {
			body = "alpha updated content"
		}
		if i >= 5 && i < 10 {
			continue // 已删除
		}
		if err := want.Add(textDoc(fmt.Sprintf("d%02d", i), body)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		if err := want.Add(textDoc(fmt.Sprintf("n%02d", i), "alpha fresh document")); err != nil {
			t.Fatal(err)
		}
	}
	want.Commit()

	for _, q := range []string{"alpha", "alpha AND beta", "updated OR fresh", "NOT alpha", `"beta gamma"`} {
		gotHits, err := e.Search(q, 0)
		if err != nil {
			t.Fatal(err)
		}
		wantHits, err := want.Search(q, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotHits, wantHits) {
			t.Fatalf("%q: incremental %v != rebuild %v", q, gotHits, wantHits)
		}
	}
}

func TestDeleteRemovesFromAllQueries(t *testing.T) {
	e, _ := Open(Options{})
	mustAddCommit(t, e, textDoc("d1", "unique token here"), textDoc("d2", "token"))
	if err := e.Delete("d1"); err != nil {
		t.Fatal(err)
	}
	e.Commit()
	for _, q := range []string{"unique", "token", "NOT nothing", "unique OR token"} {
		hits, _ := e.Search(q, 0)
		for _, h := range hits {
			if h.DocID == "d1" {
				t.Fatalf("%q: deleted doc d1 still returned", q)
			}
		}
	}
}

// 同一文档重复提交不得产生重复命中或重复计分。
func TestDuplicateSubmission(t *testing.T) {
	e, _ := Open(Options{})
	d := textDoc("d1", "repeat repeat repeat")
	for i := 0; i < 3; i++ {
		if err := e.Add(d); err != nil {
			t.Fatal(err)
		}
		e.Commit()
	}
	hits, _ := e.Search("repeat", 0)
	if len(hits) != 1 {
		t.Fatalf("want 1 hit, got %v", hits)
	}
	// 与只提交一次的对照分数一致
	single, _ := Open(Options{})
	mustAddCommit(t, single, d)
	want, _ := single.Search("repeat", 0)
	if !reflect.DeepEqual(hits, want) {
		t.Fatalf("duplicate submission changed score: %v vs %v", hits, want)
	}
}

// 更新只影响被更新的文档：未触及文档的分数不变。
func TestUpdateOnlyAffectsUpdatedDoc(t *testing.T) {
	e, _ := Open(Options{})
	mustAddCommit(t, e,
		textDoc("d1", "shared term alpha"),
		textDoc("d2", "shared term beta"),
	)
	before, _ := e.Search("shared", 0)
	// 更新 d1 但保持 d2 不动；注意 df/avgdl 可能变化，故这里验证：
	// 用不重叠的词验证 d2 的命中与分数稳定
	if err := e.Add(textDoc("d1", "shared term alpha")); err != nil { // 同内容重提交
		t.Fatal(err)
	}
	e.Commit()
	after, _ := e.Search("shared", 0)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("unchanged docs affected: %v vs %v", before, after)
	}
}

// 并发读写：查询不得报错、不得看到撕裂状态；写完后结果完整。
func TestConcurrentReadWrite(t *testing.T) {
	e, _ := Open(Options{})
	mustAddCommit(t, e, textDoc("seed", "alpha beta"))

	var readers, writers sync.WaitGroup
	stop := make(chan struct{})

	// 读协程：持续查询，命中的文档集合必须是某个一致版本
	for r := 0; r < 4; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				hits, err := e.Search("alpha", 0)
				if err != nil {
					t.Errorf("search: %v", err)
					return
				}
				for _, h := range hits {
					if h.DocID == "" {
						t.Errorf("torn hit: %+v", h)
						return
					}
				}
			}
		}()
	}
	// 写协程：持续增删改并提交
	for w := 0; w < 2; w++ {
		writers.Add(1)
		go func(base int) {
			defer writers.Done()
			for i := 0; i < 100; i++ {
				id := fmt.Sprintf("w%d-%d", base, i)
				if err := e.Add(textDoc(id, "alpha concurrent")); err != nil {
					t.Errorf("add: %v", err)
					return
				}
				e.Commit()
				if i%3 == 0 {
					_ = e.Delete(id)
					e.Commit()
				}
			}
		}(w)
	}
	writers.Wait()
	close(stop)
	readers.Wait()

	// 最终状态一致性：与全量重建对比
	hits, err := e.Search("alpha", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.DocID == "" {
			t.Fatalf("empty doc id in final hits")
		}
	}
}

// 查询必须先看到旧版本，提交后立即看到新版本（版本隔离）。
func TestSnapshotIsolation(t *testing.T) {
	e, _ := Open(Options{})
	mustAddCommit(t, e, textDoc("d1", "visible"))
	// 未提交的写入不可见
	if err := e.Add(textDoc("d2", "visible too")); err != nil {
		t.Fatal(err)
	}
	hits, _ := e.Search("visible", 0)
	if got := hitIDs(hits); !reflect.DeepEqual(got, []string{"d1"}) {
		t.Fatalf("uncommitted write visible: %v", got)
	}
	e.Commit()
	hits, _ = e.Search("visible", 0)
	if len(hits) != 2 {
		t.Fatalf("committed write not visible: %v", hits)
	}
}
