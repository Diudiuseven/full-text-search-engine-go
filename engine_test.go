package fts

import (
	"strings"
	"testing"
)

func doc(id, body string) Document {
	return Document{ID: id, Fields: []Field{{Name: "body", Kind: FieldKindText, Value: body}}}
}

// searchAll 返回查询的完整命中（ID 与得分）。
func searchAll(t *testing.T, e *Engine, q string) []Hit {
	t.Helper()
	hits, err := e.Search(q, 0)
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

func assertHitsEqual(t *testing.T, got, want []Hit) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i].DocID != want[i].DocID || got[i].Score != want[i].Score {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestIncrementalMatchesFullRebuild(t *testing.T) {
	v1 := []Document{
		doc("d1", "red apple sweet"),
		doc("d2", "green apple sour"),
		doc("d3", "yellow banana sweet"),
		doc("d4", "red cherry sour"),
	}
	// 基准：一次性全量提交 v1
	full, _ := Open("")
	full.Add(v1...)
	if _, err := full.Commit(); err != nil {
		t.Fatal(err)
	}

	// 增量：先提交一部分，再更新、删除、新增，最终达到与 v1 不同的状态 v2
	inc, _ := Open("")
	inc.Add(v1...)
	if _, err := inc.Commit(); err != nil {
		t.Fatal(err)
	}
	// v2：更新 d2，删除 d3，新增 d5
	inc.Add(doc("d2", "green apple sweet"))
	inc.Delete("d3")
	inc.Add(doc("d5", "red apple fresh"))
	if _, err := inc.Commit(); err != nil {
		t.Fatal(err)
	}

	// 对照：从空开始直接全量构建 v2
	v2 := []Document{
		doc("d1", "red apple sweet"),
		doc("d2", "green apple sweet"),
		doc("d4", "red cherry sour"),
		doc("d5", "red apple fresh"),
	}
	rebuilt, _ := Open("")
	rebuilt.Add(v2...)
	if _, err := rebuilt.Commit(); err != nil {
		t.Fatal(err)
	}

	queries := []string{
		"apple", "sweet", "red AND apple", "sweet OR sour",
		"red NOT apple", `"red apple"`, "banana", "NOT apple",
	}
	for _, q := range queries {
		assertHitsEqual(t, searchAll(t, inc, q), searchAll(t, rebuilt, q))
	}
}

func TestDeleteRemovesFromAllQueries(t *testing.T) {
	e, _ := Open("")
	e.Add(doc("d1", "alpha beta"), doc("d2", "beta gamma"))
	if _, err := e.Commit(); err != nil {
		t.Fatal(err)
	}
	e.Delete("d1")
	if _, err := e.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"alpha", "beta", "alpha OR beta", "NOT gamma"} {
		for _, h := range searchAll(t, e, q) {
			if h.DocID == "d1" {
				t.Fatalf("query %q still returns deleted doc", q)
			}
		}
	}
	// 删除不存在的 ID 不报错
	e.Delete("no-such")
	res, err := e.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 0 {
		t.Fatalf("expected 0 deletions, got %d", res.Deleted)
	}
}

func TestDuplicateSubmission(t *testing.T) {
	e, _ := Open("")
	// 同一文档重复提交（同批与跨批）
	e.Add(doc("d1", "same content here"))
	e.Add(doc("d1", "same content here"))
	if _, err := e.Commit(); err != nil {
		t.Fatal(err)
	}
	e.Add(doc("d1", "same content here"))
	if _, err := e.Commit(); err != nil {
		t.Fatal(err)
	}
	hits := searchAll(t, e, "same")
	if len(hits) != 1 {
		t.Fatalf("expected exactly 1 hit, got %v", hitIDs(hits))
	}
	// 与单次提交相比得分必须一致（无重复计分）
	single, _ := Open("")
	single.Add(doc("d1", "same content here"))
	if _, err := single.Commit(); err != nil {
		t.Fatal(err)
	}
	assertHitsEqual(t, hits, searchAll(t, single, "same"))
}

func TestInvalidDocuments(t *testing.T) {
	e, _ := Open("")
	badUTF8 := string([]byte{0xff, 0xfe, 0x41})
	oversized := strings.Repeat("x", MaxFieldBytes+1)
	e.Add(
		Document{ID: "", Fields: []Field{{Name: "body", Kind: FieldKindText, Value: "x"}}},
		Document{ID: "no-fields"},
		Document{ID: "bad-name", Fields: []Field{{Name: "", Kind: FieldKindText, Value: "x"}}},
		Document{ID: "bad-utf8", Fields: []Field{{Name: "body", Kind: FieldKindText, Value: badUTF8}}},
		Document{ID: "too-big", Fields: []Field{{Name: "body", Kind: FieldKindText, Value: oversized}}},
		doc("good", "perfectly fine document"),
	)
	res, err := e.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if res.Indexed != 1 {
		t.Fatalf("expected 1 indexed doc, got %d", res.Indexed)
	}
	if len(res.Failures) != 5 {
		t.Fatalf("expected 5 failures, got %v", res.Failures)
	}
	wantReasons := map[string]DocErrorReason{
		"":          ReasonEmptyID,
		"no-fields": ReasonNoFields,
		"bad-name":  ReasonEmptyFieldName,
		"bad-utf8":  ReasonInvalidUTF8,
		"too-big":   ReasonFieldTooLarge,
	}
	for _, f := range res.Failures {
		want, ok := wantReasons[f.DocID]
		if !ok {
			t.Fatalf("unexpected failure: %v", f)
		}
		if f.Reason != want {
			t.Fatalf("doc %q: got reason %q, want %q", f.DocID, f.Reason, want)
		}
	}
	// 合法文档不受坏文档影响，可以检索到
	assertIDs(t, searchAll(t, e, "perfectly"), "good")
}

func TestInvalidQueriesRejected(t *testing.T) {
	e := newTestEngine(t)
	for _, q := range []string{"", "   ", "(", ")", "\"open", "a AND", "OR b"} {
		hits, err := e.Search(q, 0)
		if err == nil {
			t.Fatalf("query %q: expected error, got hits %v", q, hitIDs(hits))
		}
		if _, ok := err.(*QueryError); !ok {
			t.Fatalf("query %q: expected *QueryError, got %T", q, err)
		}
		if hits != nil {
			t.Fatalf("query %q: expected nil hits on error", q)
		}
	}
}
