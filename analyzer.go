package fts

import (
	"strings"
	"unicode"
)

// Token 是分析流程产出的一个词项。
type Token struct {
	Term     string // 归一化后的词项
	Position int    // 在字段内的词位置（从 0 开始），关键字字段恒为 0
}

// Analyzer 是统一的分析流程：所有文档进入索引前都经过它。
// 规则（确定性，与批次、并发无关）：
//   - 正文字段：按 Unicode 字母/数字切分，全部小写化；
//     CJK 表意文字逐字成词（unigram），保证中文可检索；
//     词位置按切分顺序连续编号，用于短语查询。
//   - 关键字字段：整体小写化后作为单个词项，仅支持精确匹配。
type Analyzer struct{}

// NewAnalyzer 返回默认分析器。
func NewAnalyzer() *Analyzer {
	return &Analyzer{}
}

// isCJK 判断 rune 是否为 CJK 表意文字。
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

// AnalyzeText 对正文字段做分词，产出带位置的词项序列。
func (a *Analyzer) AnalyzeText(s string) []Token {
	var tokens []Token
	var buf strings.Builder
	pos := 0
	flush := func() {
		if buf.Len() > 0 {
			tokens = append(tokens, Token{Term: buf.String(), Position: pos})
			pos++
			buf.Reset()
		}
	}
	for _, r := range s {
		switch {
		case isCJK(r):
			flush()
			tokens = append(tokens, Token{Term: string(unicode.ToLower(r)), Position: pos})
			pos++
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			buf.WriteRune(unicode.ToLower(r))
		default:
			flush()
		}
	}
	flush()
	return tokens
}

// AnalyzeKeyword 对关键字字段做归一化，产出单个词项。
// 空值归一化为空词项，调用方应跳过索引。
func (a *Analyzer) AnalyzeKeyword(s string) Token {
	return Token{Term: strings.ToLower(strings.TrimSpace(s)), Position: 0}
}
