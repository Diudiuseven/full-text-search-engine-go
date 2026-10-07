package fts

import (
	"strings"
	"unicode"
)

// Token 是分析流程产出的词项，Position 为词在字段内的递增位置。
type Token struct {
	Term     string
	Position int
}

// Analyzer 把字段文本切成词项序列。
type Analyzer interface {
	Analyze(field Field) ([]Token, error)
}

// StandardAnalyzer 用于正文：按非字母数字字符切分并统一小写，
// 位置按词序递增（短语查询依赖该位置）。
type StandardAnalyzer struct{}

func (StandardAnalyzer) Analyze(field Field) ([]Token, error) {
	var tokens []Token
	var cur strings.Builder
	pos := 0
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, Token{Term: cur.String(), Position: pos})
			pos++
			cur.Reset()
		}
	}
	for _, r := range field.Value {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			cur.WriteRune(unicode.ToLower(r))
		default:
			flush()
		}
	}
	flush()
	return tokens, nil
}

// KeywordAnalyzer 用于需要单独匹配的字段：整体小写后作为一个词项。
type KeywordAnalyzer struct{}

func (KeywordAnalyzer) Analyze(field Field) ([]Token, error) {
	v := strings.ToLower(strings.TrimSpace(field.Value))
	if v == "" {
		return nil, nil
	}
	return []Token{{Term: v, Position: 0}}, nil
}

// analyzerFor 按字段类型选择分析器；opts.Analyzer 可覆盖正文分析器。
func (o *Options) analyzerFor(kind FieldKind) Analyzer {
	if kind == FieldKeyword {
		return KeywordAnalyzer{}
	}
	if o.Analyzer != nil {
		return o.Analyzer
	}
	return StandardAnalyzer{}
}
