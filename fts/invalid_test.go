package fts

import (
	"strings"
	"testing"
)

func expectErrKind(t *testing.T, err error, kind ErrKind) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error kind %d, got nil", kind)
	}
	ee, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if ee.Kind != kind {
		t.Fatalf("expected kind %d, got %d (%v)", kind, ee.Kind, err)
	}
}

func TestEmptyAndInvalidQueries(t *testing.T) {
	e, _ := Open(Options{})
	mustAddCommit(t, e, textDoc("d1", "some content"))
	// 空查询必须被拒绝，不得静默返回全部文档
	for _, q := range []string{"", "   ", "\t\n"} {
		_, err := e.Search(q, 0)
		expectErrKind(t, err, ErrEmptyQuery)
	}
	// 语法非法
	for _, q := range []string{"(apple", "apple)", `"unclosed`, "field:", "AND apple"} {
		_, err := e.Search(q, 0)
		expectErrKind(t, err, ErrQuerySyntax)
	}
}

func TestInvalidDocuments(t *testing.T) {
	e, _ := Open(Options{MaxDocBytes: 64})
	// 编码异常
	err := e.Add(textDoc("bad-utf8", string([]byte{0xff, 0xfe, 0xfd})))
	expectErrKind(t, err, ErrDocBadEncoding)
	if ee := err.(*Error); ee.DocID != "bad-utf8" {
		t.Fatalf("error not located to doc: %v", ee)
	}
	// 超长
	err = e.Add(textDoc("too-big", strings.Repeat("x", 100)))
	expectErrKind(t, err, ErrDocTooLarge)
	// 空 ID
	err = e.Add(textDoc("", "content"))
	expectErrKind(t, err, ErrDocInvalid)
	// 坏文档不影响好文档：整次建索引不失败
	if err := e.Add(textDoc("good", "fine content")); err != nil {
		t.Fatal(err)
	}
	e.Commit()
	hits, _ := e.Search("fine", 0)
	if len(hits) != 1 || hits[0].DocID != "good" {
		t.Fatalf("good doc rejected alongside bad ones: %v", hits)
	}
	hits, _ = e.Search("content", 0)
	for _, h := range hits {
		if h.DocID == "bad-utf8" || h.DocID == "too-big" {
			t.Fatalf("invalid doc indexed: %v", h)
		}
	}
}
