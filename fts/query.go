package fts

import (
	"fmt"
	"strings"
)

// Query 是查询语法树节点。
type Query interface {
	isQuery()
}

// TermQuery 匹配单个词项，Field 为空时检索默认正文字段。
type TermQuery struct {
	Field string
	Term  string
}

// PhraseQuery 要求词项按相邻位置依次出现。
type PhraseQuery struct {
	Field string
	Terms []string
}

// AndQuery / OrQuery / NotQuery 为布尔组合。
// NotQuery.Include 为 nil 时表示“全部文档中排除 Exclude”。
type AndQuery struct{ Clauses []Query }
type OrQuery struct{ Clauses []Query }
type NotQuery struct {
	Include Query
	Exclude Query
}

func (TermQuery) isQuery()   {}
func (PhraseQuery) isQuery() {}
func (AndQuery) isQuery()    {}
func (OrQuery) isQuery()     {}
func (NotQuery) isQuery()    {}

// 查询语法（大小写敏感的关键字）：
//   query := or
//   or    := and ( OR and )*
//   and   := not ( (AND|空白) not )*
//   not   := NOT not | primary ( NOT primary )*   // a NOT b 表示命中 a 且排除 b
//   primary := '(' or ')' | field ':' word | field ':' '"' words '"' | '"' words '"' | word
// 词项在匹配时会经过与索引一致的小写归一化。

type qtoken struct {
	kind string // word, phrase, lparen, rparen, colon, and, or, not
	val  string
}

func lexQuery(input string) ([]qtoken, error) {
	var toks []qtoken
	i := 0
	for i < len(input) {
		c := input[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(':
			toks = append(toks, qtoken{kind: "lparen"})
			i++
		case c == ')':
			toks = append(toks, qtoken{kind: "rparen"})
			i++
		case c == ':':
			toks = append(toks, qtoken{kind: "colon"})
			i++
		case c == '"':
			j := i + 1
			for j < len(input) && input[j] != '"' {
				j++
			}
			if j >= len(input) {
				return nil, &Error{Kind: ErrQuerySyntax, Detail: "unterminated phrase quote"}
			}
			toks = append(toks, qtoken{kind: "phrase", val: input[i+1 : j]})
			i = j + 1
		default:
			j := i
			for j < len(input) && !strings.ContainsRune(" \t\n\r()\":", rune(input[j])) {
				j++
			}
			w := input[i:j]
			switch w {
			case "AND":
				toks = append(toks, qtoken{kind: "and"})
			case "OR":
				toks = append(toks, qtoken{kind: "or"})
			case "NOT":
				toks = append(toks, qtoken{kind: "not"})
			default:
				toks = append(toks, qtoken{kind: "word", val: w})
			}
			i = j
		}
	}
	return toks, nil
}

type qparser struct {
	toks []qtoken
	pos  int
}

func (p *qparser) peek() *qtoken {
	if p.pos < len(p.toks) {
		return &p.toks[p.pos]
	}
	return nil
}

func (p *qparser) next() *qtoken {
	t := p.peek()
	if t != nil {
		p.pos++
	}
	return t
}

// ParseQuery 解析查询字符串；空查询与语法错误返回可区分的错误。
func ParseQuery(input string) (Query, error) {
	if strings.TrimSpace(input) == "" {
		return nil, &Error{Kind: ErrEmptyQuery, Detail: "query is empty"}
	}
	toks, err := lexQuery(input)
	if err != nil {
		return nil, err
	}
	p := &qparser{toks: toks}
	q, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.toks) {
		return nil, &Error{Kind: ErrQuerySyntax, Detail: fmt.Sprintf("unexpected token %q", p.toks[p.pos].val)}
	}
	return q, nil
}

func (p *qparser) parseOr() (Query, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	clauses := []Query{left}
	for p.peek() != nil && p.peek().kind == "or" {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		clauses = append(clauses, right)
	}
	if len(clauses) == 1 {
		return left, nil
	}
	return OrQuery{Clauses: clauses}, nil
}

func (p *qparser) parseAnd() (Query, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	clauses := []Query{left}
	for {
		t := p.peek()
		if t == nil {
			break
		}
		// 显式 AND 或相邻词项的隐式 AND
		if t.kind == "and" {
			p.next()
		} else if t.kind != "word" && t.kind != "phrase" && t.kind != "lparen" && t.kind != "not" {
			break
		}
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		clauses = append(clauses, right)
	}
	if len(clauses) == 1 {
		return left, nil
	}
	return AndQuery{Clauses: clauses}, nil
}

func (p *qparser) parseNot() (Query, error) {
	t := p.peek()
	if t == nil {
		return nil, &Error{Kind: ErrQuerySyntax, Detail: "unexpected end of query"}
	}
	if t.kind == "not" {
		p.next()
		sub, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return NotQuery{Include: nil, Exclude: sub}, nil
	}
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	// 中缀 NOT：a NOT b
	for p.peek() != nil && p.peek().kind == "not" {
		p.next()
		right, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		left = NotQuery{Include: left, Exclude: right}
	}
	return left, nil
}

func (p *qparser) parsePrimary() (Query, error) {
	t := p.next()
	if t == nil {
		return nil, &Error{Kind: ErrQuerySyntax, Detail: "unexpected end of query"}
	}
	switch t.kind {
	case "lparen":
		q, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		closing := p.next()
		if closing == nil || closing.kind != "rparen" {
			return nil, &Error{Kind: ErrQuerySyntax, Detail: "missing closing parenthesis"}
		}
		return q, nil
	case "word":
		// field:value 或 field:"phrase"
		if p.peek() != nil && p.peek().kind == "colon" {
			p.next()
			v := p.next()
			if v == nil || (v.kind != "word" && v.kind != "phrase") {
				return nil, &Error{Kind: ErrQuerySyntax, Detail: "expected term or phrase after field:"}
			}
			return fieldQuery(t.val, *v)
		}
		return TermQuery{Term: t.val}, nil
	case "phrase":
		return makePhrase("", t.val)
	default:
		return nil, &Error{Kind: ErrQuerySyntax, Detail: fmt.Sprintf("unexpected token %q", t.val)}
	}
}

func fieldQuery(field string, v qtoken) (Query, error) {
	if v.kind == "phrase" {
		return makePhrase(field, v.val)
	}
	return TermQuery{Field: field, Term: v.val}, nil
}

func makePhrase(field, text string) (Query, error) {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil, &Error{Kind: ErrQuerySyntax, Detail: "empty phrase"}
	}
	terms := make([]string, len(words))
	for i, w := range words {
		terms[i] = strings.ToLower(w)
	}
	if len(terms) == 1 {
		return TermQuery{Field: field, Term: terms[0]}, nil
	}
	return PhraseQuery{Field: field, Terms: terms}, nil
}
