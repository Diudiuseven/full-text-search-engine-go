package fts

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
)

func textDoc(id, body string) Document {
	return Document{ID: id, Fields: []Field{{Name: "body", Kind: FieldText, Value: body}}}
}

func mustAddCommit(t *testing.T, e *Engine, docs ...Document) {
	t.Helper()
	for _, d := range docs {
		if err := e.Add(d); err != nil {
			t.Fatalf("Add(%s): %v", d.ID, err)
		}
	}
	e.Commit()
}

func hitIDs(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.DocID
	}
	return out
}

func TestTermSearchAndCaseFolding(t *testing.T) {
	e, _ := Open(Options{})
	mustAddCommit(t, e,
		textDoc("d1", "The quick brown fox"),
		textDoc("d2", "Quick silver"),
		textDoc("d3", "lazy dog"),
	)
	hits, err := e.Search("Quick", 0) // 大小写不敏感
	if err != nil {
		t.Fatal(err)
	}
	got := hitIDs(hits)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"d1", "d2"}) {
		t.Fatalf("got %v", got)
	}
}

func TestBooleanQueries(t *testing.T) {
	e, _ := Open(Options{})
	mustAddCommit(t, e,
		textDoc("d1", "apple banana cherry"),
		textDoc("d2", "apple banana"),
		textDoc("d3", "apple cherry"),
		textDoc("d4", "banana cherry"),
	)
	// 布尔语义按命中集合断言（排序由专门的排序测试覆盖）
	cases := []struct {
		q    string
		want []string
	}{
		{"apple AND banana", []string{"d1", "d2"}},
		{"apple banana", []string{"d1", "d2"}}, // 隐式 AND
		{"apple OR banana", []string{"d1", "d2", "d3", "d4"}},
		{"apple NOT banana", []string{"d3"}},
		{"NOT banana", []string{"d3"}},
		{"(apple OR banana) NOT cherry", []string{"d2"}},
		{"apple AND apple AND apple", []string{"d1", "d2", "d3"}}, // 重复词不重复命中
	}
	for _, c := range cases {
		hits, err := e.Search(c.q, 0)
		if err != nil {
			t.Fatalf("%q: %v", c.q, err)
		}
		got := hitIDs(hits)
		sort.Strings(got)
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%q: got %v want %v", c.q, got, c.want)
		}
	}
}

func TestPhraseQueryUsesPositions(t *testing.T) {
	e, _ := Open(Options{})
	mustAddCommit(t, e,
		textDoc("d1", "the quick brown fox jumps"),
		textDoc("d2", "the brown quick fox"),   // 顺序不符
		textDoc("d3", "quick fox brown quick"), // 不相邻
	)
	hits, err := e.Search(`"quick brown"`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := hitIDs(hits); !reflect.DeepEqual(got, []string{"d1"}) {
		t.Fatalf("got %v", got)
	}
}

func TestKeywordFieldMatching(t *testing.T) {
	e, _ := Open(Options{})
	mustAddCommit(t, e,
		Document{ID: "d1", Fields: []Field{
			{Name: "body", Kind: FieldText, Value: "golang tips"},
			{Name: "tag", Kind: FieldKeyword, Value: "Go Lang"},
		}},
		Document{ID: "d2", Fields: []Field{
			{Name: "body", Kind: FieldText, Value: "golang"},
			{Name: "tag", Kind: FieldKeyword, Value: "go"},
		}},
	)
	// keyword 字段整体匹配
	hits, _ := e.Search("tag:go", 0)
	if got := hitIDs(hits); !reflect.DeepEqual(got, []string{"d2"}) {
		t.Fatalf("tag:go got %v", got)
	}
	hits, _ = e.Search(`tag:"go lang"`, 0)
	if got := hitIDs(hits); !reflect.DeepEqual(got, []string{"d1"}) {
		t.Fatalf(`tag:"go lang" got %v`, got)
	}
	// 默认字段不检索 keyword 字段内容
	hits, _ = e.Search("tips", 0)
	if got := hitIDs(hits); !reflect.DeepEqual(got, []string{"d1"}) {
		t.Fatalf("tips got %v", got)
	}
}

// 同一文档集合，不同批次划分与提交顺序，结果必须逐条一致。
func TestDeterminismAcrossBatches(t *testing.T) {
	mk := func() []Document {
		var docs []Document
		for i := 0; i < 50; i++ {
			docs = append(docs, textDoc(fmt.Sprintf("doc-%02d", i),
				fmt.Sprintf("common word %d frequency filler filler", i%7)))
		}
		return docs
	}
	run := func(batches [][]Document) []Hit {
		e, _ := Open(Options{})
		for _, b := range batches {
			for _, d := range b {
				if err := e.Add(d); err != nil {
					t.Fatal(err)
				}
			}
			e.Commit()
		}
		hits, err := e.Search("common AND word", 0)
		if err != nil {
			t.Fatal(err)
		}
		return hits
	}
	docs := mk()
	oneBatch := run([][]Document{docs})
	var many [][]Document
	for _, d := range docs {
		many = append(many, []Document{d})
	}
	perDoc := run(many)
	// 乱序提交
	shuffled := append([]Document{}, docs...)
	for i, j := 0, len(shuffled)-1; i < j; i, j = i+1, j-1 {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	}
	rev := run([][]Document{shuffled})

	if !reflect.DeepEqual(oneBatch, perDoc) || !reflect.DeepEqual(oneBatch, rev) {
		t.Fatalf("non-deterministic results:\n%v\n%v\n%v", oneBatch, perDoc, rev)
	}
}

// 排序必须同时受词频与文档频率影响，且可复现。
func TestRankingReflectsTFAndDF(t *testing.T) {
	e, _ := Open(Options{})
	mustAddCommit(t, e,
		textDoc("d1", "apple apple apple"), // tf 高
		textDoc("d2", "apple"),
		textDoc("d3", "apple banana cherry date elderberry fig grape"), // 长文档，归一化后应低于 d2
	)
	hits, err := e.Search("apple", 0)
	if err != nil {
		t.Fatal(err)
	}
	got := hitIDs(hits)
	if !reflect.DeepEqual(got, []string{"d1", "d2", "d3"}) {
		t.Fatalf("got %v", got)
	}
	if !(hits[0].Score > hits[1].Score && hits[1].Score > hits[2].Score) {
		t.Fatalf("scores not ordered: %v", hits)
	}
	// 复现性：再查一次分数完全一致
	again, _ := e.Search("apple", 0)
	if !reflect.DeepEqual(hits, again) {
		t.Fatalf("scores not reproducible: %v vs %v", hits, again)
	}
}

func TestTopK(t *testing.T) {
	e, _ := Open(Options{})
	for i := 0; i < 10; i++ {
		if err := e.Add(textDoc(fmt.Sprintf("d%d", i), "word")); err != nil {
			t.Fatal(err)
		}
	}
	e.Commit()
	hits, _ := e.Search("word", 3)
	if len(hits) != 3 {
		t.Fatalf("want 3 hits, got %d", len(hits))
	}
}
