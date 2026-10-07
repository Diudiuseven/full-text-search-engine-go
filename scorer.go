package fts

import "math"

// BM25Params 是相关性评分参数，评分规则见 README。
type BM25Params struct {
	K1 float64 // 词频饱和参数
	B  float64 // 文档长度归一化参数
}

// DefaultBM25Params 返回默认评分参数。
func DefaultBM25Params() BM25Params {
	return BM25Params{K1: 1.2, B: 0.75}
}

// bm25IDF 计算逆文档频率：ln(1 + (N - df + 0.5) / (df + 0.5))。
// N 为文档总数，df 为包含该词的文档数；结果恒为正，保证可复现。
func bm25IDF(numDocs, docFreq int) float64 {
	return math.Log(1 + (float64(numDocs)-float64(docFreq)+0.5)/(float64(docFreq)+0.5))
}

// bm25Score 计算单篇文档对单个词项的 BM25 得分：
// idf * tf*(k1+1) / (tf + k1*(1 - b + b*docLen/avgDocLen))
// 同时考虑词频（tf）、文档频率（经 idf）与文档长度归一化。
func bm25Score(p BM25Params, tf int, docLen int, avgDocLen float64, idf float64) float64 {
	if avgDocLen <= 0 {
		avgDocLen = 1
	}
	tff := float64(tf)
	den := tff + p.K1*(1-p.B+p.B*float64(docLen)/avgDocLen)
	return idf * tff * (p.K1 + 1) / den
}
