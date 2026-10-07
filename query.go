package fts

import (
	"fmt"
	"strings"
)

// QueryErrorReason 区分查询被拒绝的原因。
type QueryErrorReason string

const (
	ReasonEmptyQuery     QueryErrorReason = "empty_query"
	ReasonUnexpectedTok  QueryErrorReason = "unexpected_token"
	ReasonUnclosedPhrase QueryErrorReason = "unclosed_phrase"
	ReasonUnclosedParen  QueryErrorReason = "unclosed_paren"
	ReasonMissingOperand QueryErrorReason = "missing_operand"
)

// QueryError 描述查询语法非法的原因。
type QueryError struct {
	Reason QueryErrorReason
	Detail string
}

func (e *QueryError) Error() string {
	return "invalid query: " + string(e.Reason) + ": " + e.Detail
}

// ErrEmptyQuery 在查询为空时返回。
var ErrEmptyQuery = &QueryError{Reason: ReasonEmptyQuery, Detail: "query is empty"}

// Query 是布尔/短语查询的语法树节点。
type Query interface {
	isQuery()
}

// TermQuery 匹配单个词项；Field 为空时匹配所有正文字段。
type TermQuery struct {
	Field string
	Term  string
}

// PhraseQuery 要求多个词按相邻顺序出现，基于位置信息判定。
type PhraseQuery struct {
	Field string
	Terms []string
}

// AndQuery 要求所有子查询都命中。
type AndQuery struct{ Children []Query }

// OrQuery 要求至少一个子查询命中。
type OrQuery struct{ Children []Query }

// NotQuery 排除子查询命中的文档。
type NotQuery struct{ Child Query }

func (*TermQuery) isQuery()   {}
func (*PhraseQuery) isQuery() {}
func (*AndQuery) isQuery()    {}
func (*OrQuery) isQuery()     {}
func (*NotQuery) isQuery()    {}

// 查询侧统一使用与索引侧相同的默认分析器，保证词项归一化一致。
var queryAnalyzer = NewAnalyzer()

// ---- 词法分析 ----

type tokKind int

const (
	tokTerm   tokKind = iota // 词项（可带 field: 前缀）
	tokPhrase                // "..." 引号短语（可带 field: 前缀）
	tokAnd
	tokOr
	tokNot
	tokLParen
	tokRParen
)

type token struct {
	kind  tokKind
	field string // field: 前缀（无则为空）
	text  string // 词项文本或短语原文
}

// lex 把查询字符串切成 token 序列；引号不闭合时报错。
func lex(input string) ([]token, error) {
	var toks []token
	i := 0
	n := len(input)
	field := ""
	for i < n {
		c := input[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(':
			toks = append(toks, token{kind: tokLParen})
			i++
		case c == ')':
			toks = append(toks, token{kind: tokRParen})
			i++
		case c == '"':
			end := strings.IndexByte(input[i+1:], '"')
			if end < 0 {
				return nil, &QueryError{Reason: ReasonUnclosedPhrase, Detail: fmt.Sprintf("phrase starting at offset %d is not closed", i)}
			}
			toks = append(toks, token{kind: tokPhrase, field: field, text: input[i+1 : i+1+end]})
			field = ""
			i += end + 2
		default:
			start := i
			for i < n && !strings.ContainsRune(" \t\n\r()\"", rune(input[i])) {
				i++
			}
			word := input[start:i]
			// field: 前缀（后接词项或引号短语）
			if idx := strings.IndexByte(word, ':'); idx > 0 {
				field = strings.ToLower(word[:idx])
				word = word[idx+1:]
				if word == "" {
					continue // 前缀后紧跟引号，下一轮循环处理
				}
			}
			switch strings.ToUpper(word) {
			case "AND":
				toks = append(toks, token{kind: tokAnd})
			case "OR":
				toks = append(toks, token{kind: tokOr})
			case "NOT":
				toks = append(toks, token{kind: tokNot})
			default:
				toks = append(toks, token{kind: tokTerm, field: field, text: word})
			}
			field = ""
		}
	}
	return toks, nil
}

// ---- 语法分析（递归下降）----
// query   := orExpr
// orExpr  := andExpr (OR andExpr)*
// andExpr := notExpr (AND notExpr | notExpr)*   // 相邻词项默认 AND
// notExpr := NOT notExpr | primary
// primary := '(' orExpr ')' | term | phrase

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() (token, bool) {
	if p.pos < len(p.toks) {
		return p.toks[p.pos], true
	}
	return token{}, false
}

func (p *parser) next() (token, bool) {
	t, ok := p.peek()
	if ok {
		p.pos++
	}
	return t, ok
}

// ParseQuery 解析查询字符串为语法树。
// 语法：term | field:term | "phrase" | field:"phrase" | AND OR NOT | 括号；
// 相邻词项默认 AND。空查询或语法非法时返回 *QueryError。
func ParseQuery(input string) (Query, error) {
	if strings.TrimSpace(input) == "" {
		return nil, ErrEmptyQuery
	}
	toks, err := lex(input)
	if err != nil {
		return nil, err
	}
	if len(toks) == 0 {
		return nil, ErrEmptyQuery
	}
	p := &parser{toks: toks}
	q, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if t, ok := p.peek(); ok {
		return nil, &QueryError{Reason: ReasonUnexpectedTok, Detail: fmt.Sprintf("unexpected token %q", t.text)}
	}
	return q, nil
}

func (p *parser) parseOr() (Query, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	children := []Query{left}
	for {
		if t, ok := p.peek(); ok && t.kind == tokOr {
			p.next()
			right, err := p.parseAnd()
			if err != nil {
				return nil, err
			}
			children = append(children, right)
			continue
		}
		break
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return &OrQuery{Children: children}, nil
}

// startsOperand 判断 token 是否可作为一个操作数的开始。
func startsOperand(t token) bool {
	return t.kind == tokTerm || t.kind == tokPhrase || t.kind == tokNot || t.kind == tokLParen
}

func (p *parser) parseAnd() (Query, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	children := []Query{left}
	for {
		t, ok := p.peek()
		if !ok {
			break
		}
		if t.kind == tokAnd {
			p.next()
			right, err := p.parseNot()
			if err != nil {
				return nil, err
			}
			children = append(children, right)
			continue
		}
		if startsOperand(t) { // 隐式 AND
			right, err := p.parseNot()
			if err != nil {
				return nil, err
			}
			children = append(children, right)
			continue
		}
		break
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return &AndQuery{Children: children}, nil
}

func (p *parser) parseNot() (Query, error) {
	if t, ok := p.peek(); ok && t.kind == tokNot {
		p.next()
		child, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &NotQuery{Child: child}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (Query, error) {
	t, ok := p.next()
	if !ok {
		return nil, &QueryError{Reason: ReasonMissingOperand, Detail: "expected a term, phrase or '(' but reached end of query"}
	}
	switch t.kind {
	case tokLParen:
		q, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		closing, ok := p.next()
		if !ok || closing.kind != tokRParen {
			return nil, &QueryError{Reason: ReasonUnclosedParen, Detail: "parenthesis is not closed"}
		}
		return q, nil
	case tokTerm:
		terms := queryAnalyzer.AnalyzeText(t.text)
		if len(terms) == 0 {
			return nil, &QueryError{Reason: ReasonMissingOperand, Detail: fmt.Sprintf("term %q analyzes to nothing", t.text)}
		}
		if len(terms) == 1 {
			return &TermQuery{Field: t.field, Term: terms[0].Term}, nil
		}
		// 词项被分析为多个 token（如 CJK 混合），按 AND 组合
		children := make([]Query, len(terms))
		for i, tk := range terms {
			children[i] = &TermQuery{Field: t.field, Term: tk.Term}
		}
		return &AndQuery{Children: children}, nil
	case tokPhrase:
		terms := queryAnalyzer.AnalyzeText(t.text)
		if len(terms) == 0 {
			return nil, &QueryError{Reason: ReasonMissingOperand, Detail: fmt.Sprintf("phrase %q analyzes to nothing", t.text)}
		}
		ss := make([]string, len(terms))
		for i, tk := range terms {
			ss[i] = tk.Term
		}
		return &PhraseQuery{Field: t.field, Terms: ss}, nil
	default:
		return nil, &QueryError{Reason: ReasonUnexpectedTok, Detail: fmt.Sprintf("unexpected token %q", t.text)}
	}
}
