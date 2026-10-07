package fts

import (
	"testing"
)

// newTestEngine 构建一个包含固定文档集的内存引擎。
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	e.Add(
		Document{ID: "d1", Fields: []Field{
			{Name: "body", Kind: FieldKindText, Value: "the quick brown fox jumps over the lazy dog"},
			{Name: "tag", Kind: FieldKindKeyword, Value: "animal"},
		}},
		Document{ID: "d2", Fields: []Field{
			{Name: "body", Kind: FieldKindText, Value: "the quick red fox runs quick"},
			{Name: "tag", Kind: FieldKindKeyword, Value: "animal"},
		}},
		Document{ID: "d3", Fields: []Field{
			{Name: "body", Kind: FieldKindText, Value: "a lazy dog sleeps all day"},
			{Name: "tag", Kind: FieldKindKeyword, Value: "pet"},
		}},
		Document{ID: "d4", Fields: []Field{
			{Name: "body", Kind: FieldKindText, Value: "quick quick quick"},
			{Name: "tag", Kind: FieldKindKeyword, Value: "pet"},
		}},
	)
	if _, err := e.Commit(); err != nil {
		t.Fatal(err)
	}
	return e
}

func hitIDs(hits []Hit) []string {
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.DocID
	}
	return ids
}

func assertIDs(t *testing.T, hits []Hit, want ...string) {
	t.Helper()
	got := hitIDs(hits)
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestBooleanQueries(t *testing.T) {
	e := newTestEngine(t)

	hits, err := e.Search("quick AND fox", 0)
	if err != nil {
		t.Fatal(err)
	}
	// d2 词频更高且文档更短，BM25 得分高于 d1
	assertIDs(t, hits, "d2", "d1")

	hits, err = e.Search("sleeps OR jumps", 0)
	if err != nil {
		t.Fatal(err)
	}
	// d3 更短，得分高于 d1
	assertIDs(t, hits, "d3", "d1")

	hits, err = e.Search("quick NOT brown", 0)
	if err != nil {
		t.Fatal(err)
	}
	assertIDs(t, hits, "d4", "d2")

	// 纯 NOT：命中除含 dog 外的全部文档
	hits, err = e.Search("NOT dog", 0)
	if err != nil {
		t.Fatal(err)
	}
	assertIDs(t, hits, "d2", "d4")
}

func TestNoDuplicateHits(t *testing.T) {
	e := newTestEngine(t)
	// 同一词重复出现、且与多个词组合，不得重复命中
	hits, err := e.Search("quick quick OR quick AND fox", 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, h := range hits {
		seen[h.DocID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("doc %s hit %d times", id, n)
		}
	}
	// 得分：d2 > d1 > d4（AND 分支得分叠加）
	assertIDs(t, hits, "d2", "d1", "d4")
}

func TestPhraseQuery(t *testing.T) {
	e := newTestEngine(t)

	// 相邻短语命中
	hits, err := e.Search(`"lazy dog"`, 0)
	if err != nil {
		t.Fatal(err)
	}
	// d3 更短，短语得分更高
	assertIDs(t, hits, "d3", "d1")

	// 词都在但不相邻，不得命中
	hits, err = e.Search(`"quick fox"`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected no hits, got %v", hitIDs(hits))
	}

	// 顺序相反不得命中
	hits, err = e.Search(`"dog lazy"`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected no hits, got %v", hitIDs(hits))
	}
}

func TestKeywordField(t *testing.T) {
	e := newTestEngine(t)

	// 关键字字段需显式 field: 精确匹配
	hits, err := e.Search("tag:pet", 0)
	if err != nil {
		t.Fatal(err)
	}
	assertIDs(t, hits, "d3", "d4")

	// 裸词不搜索关键字字段
	hits, err = e.Search("pet", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("bare term must not match keyword field, got %v", hitIDs(hits))
	}

	// 关键字字段与正文条件组合
	hits, err = e.Search("tag:animal AND quick", 0)
	if err != nil {
		t.Fatal(err)
	}
	assertIDs(t, hits, "d2", "d1")
}

func TestRankingByRelevance(t *testing.T) {
	e := newTestEngine(t)
	// d4 中 quick 出现 3 次且文档极短，应排最前
	hits, err := e.Search("quick", 0)
	if err != nil {
		t.Fatal(err)
	}
	assertIDs(t, hits, "d4", "d2", "d1")
	if !(hits[0].Score > hits[1].Score && hits[1].Score > hits[2].Score) {
		t.Fatalf("scores not strictly decreasing: %v", hits)
	}
}

func TestRankingDeterministicAcrossBatches(t *testing.T) {
	docs := []Document{
		{ID: "a", Fields: []Field{{Name: "body", Kind: FieldKindText, Value: "apple banana apple"}}},
		{ID: "b", Fields: []Field{{Name: "body", Kind: FieldKindText, Value: "apple cherry banana grape melon"}}},
		{ID: "c", Fields: []Field{{Name: "body", Kind: FieldKindText, Value: "banana grape"}}},
		{ID: "d", Fields: []Field{{Name: "body", Kind: FieldKindText, Value: "apple"}}},
		{ID: "e", Fields: []Field{{Name: "body", Kind: FieldKindText, Value: "cherry melon"}}},
	}
	queries := []string{"apple", "apple OR banana", "banana AND grape", "NOT apple"}

	// 基准：单批提交
	base, _ := Open("")
	base.Add(docs...)
	if _, err := base.Commit(); err != nil {
		t.Fatal(err)
	}

	// 对照：多篇并发地、分批提交
	alt, _ := Open("")
	done := make(chan bool, len(docs))
	for i := range docs {
		go func(d Document) {
			alt.Add(d)
			done <- true
		}(docs[i])
	}
	for range docs {
		<-done
	}
	if _, err := alt.Commit(); err != nil {
		t.Fatal(err)
	}
	// 再分批提交一遍（逐篇提交）
	seq, _ := Open("")
	for _, d := range docs {
		seq.Add(d)
		if _, err := seq.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	for _, q := range queries {
		want, err := base.Search(q, 0)
		if err != nil {
			t.Fatal(err)
		}
		for name, e := range map[string]*Engine{"concurrent": alt, "sequential": seq} {
			got, err := e.Search(q, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(want) {
				t.Fatalf("%s query %q: got %v, want %v", name, q, hitIDs(got), hitIDs(want))
			}
			for i := range want {
				if got[i].DocID != want[i].DocID || got[i].Score != want[i].Score {
					t.Fatalf("%s query %q: got %v, want %v", name, q, got, want)
				}
			}
		}
	}
}

func TestSearchLimit(t *testing.T) {
	e := newTestEngine(t)
	hits, err := e.Search("quick", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}
}
