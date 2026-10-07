package fts

import (
	"reflect"
	"testing"
)

func TestAnalyzeTextBasic(t *testing.T) {
	a := NewAnalyzer()
	toks := a.AnalyzeText("Hello, World! hello")
	want := []Token{
		{Term: "hello", Position: 0},
		{Term: "world", Position: 1},
		{Term: "hello", Position: 2},
	}
	if !reflect.DeepEqual(toks, want) {
		t.Fatalf("got %+v, want %+v", toks, want)
	}
}

func TestAnalyzeTextCJK(t *testing.T) {
	a := NewAnalyzer()
	toks := a.AnalyzeText("全文检索 engine")
	want := []Token{
		{Term: "全", Position: 0},
		{Term: "文", Position: 1},
		{Term: "检", Position: 2},
		{Term: "索", Position: 3},
		{Term: "engine", Position: 4},
	}
	if !reflect.DeepEqual(toks, want) {
		t.Fatalf("got %+v, want %+v", toks, want)
	}
}

func TestAnalyzeKeyword(t *testing.T) {
	a := NewAnalyzer()
	if got := a.AnalyzeKeyword("  GoLang ").Term; got != "golang" {
		t.Fatalf("got %q", got)
	}
	if got := a.AnalyzeKeyword("   ").Term; got != "" {
		t.Fatalf("expected empty term, got %q", got)
	}
}

func TestParseQueryErrors(t *testing.T) {
	cases := []struct {
		input  string
		reason QueryErrorReason
	}{
		{"", ReasonEmptyQuery},
		{"   ", ReasonEmptyQuery},
		{"\"unclosed", ReasonUnclosedPhrase},
		{"(foo", ReasonUnclosedParen},
		{"foo AND", ReasonMissingOperand},
		{"AND foo", ReasonUnexpectedTok},
		{"foo OR NOT", ReasonMissingOperand},
		{"foo )", ReasonUnexpectedTok},
	}
	for _, c := range cases {
		_, err := ParseQuery(c.input)
		if err == nil {
			t.Fatalf("query %q: expected error", c.input)
		}
		qe, ok := err.(*QueryError)
		if !ok {
			t.Fatalf("query %q: expected *QueryError, got %T", c.input, err)
		}
		if qe.Reason != c.reason {
			t.Fatalf("query %q: got reason %q, want %q", c.input, qe.Reason, c.reason)
		}
	}
}

func TestParseQueryShapes(t *testing.T) {
	q, err := ParseQuery(`golang AND (search OR engine) NOT title:"exact phrase"`)
	if err != nil {
		t.Fatal(err)
	}
	and, ok := q.(*AndQuery)
	if !ok || len(and.Children) != 3 {
		t.Fatalf("expected top-level AND of 3, got %#v", q)
	}
	if _, ok := and.Children[1].(*OrQuery); !ok {
		t.Fatalf("expected OR child, got %#v", and.Children[1])
	}
	not, ok := and.Children[2].(*NotQuery)
	if !ok {
		t.Fatalf("expected NOT child, got %#v", and.Children[2])
	}
	ph, ok := not.Child.(*PhraseQuery)
	if !ok || ph.Field != "title" || len(ph.Terms) != 2 {
		t.Fatalf("expected field phrase, got %#v", not.Child)
	}
}

func TestParseQueryImplicitAnd(t *testing.T) {
	q, err := ParseQuery("foo bar")
	if err != nil {
		t.Fatal(err)
	}
	and, ok := q.(*AndQuery)
	if !ok || len(and.Children) != 2 {
		t.Fatalf("expected implicit AND, got %#v", q)
	}
}

func TestParseQueryNormalizesTerms(t *testing.T) {
	q, err := ParseQuery("GoLang")
	if err != nil {
		t.Fatal(err)
	}
	tq, ok := q.(*TermQuery)
	if !ok || tq.Term != "golang" {
		t.Fatalf("expected lowercased term, got %#v", q)
	}
}
