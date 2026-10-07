package fts

import (
	"math"
	"sort"
	"strings"
)

// Hit 是一条查询命中。
type Hit struct {
	DocID string
	Score float64
}

// BM25 默认参数。
const (
	defaultK1 = 1.2
	defaultB  = 0.75
)

func (o *Options) k1() float64 {
	if o.BM25K1 > 0 {
		return o.BM25K1
	}
	return defaultK1
}

func (o *Options) b() float64 {
	if o.BM25B > 0 {
		return o.BM25B
	}
	return defaultB
}

// idf 采用 BM25 标准形式：ln(1 + (N - df + 0.5) / (df + 0.5))。
func (s *snapshot) idf(df int) float64 {
	n := float64(s.numDocs)
	return math.Log(1 + (n-float64(df)+0.5)/(float64(df)+0.5))
}

// tfComponent 是带长度归一化的词频分量：tf*(k1+1) / (tf + k1*(1-b+b*dl/avgdl))。
func (s *snapshot) tfComponent(tf int, doc uint32, opts *Options) float64 {
	k1, b := opts.k1(), opts.b()
	dl := float64(s.docLen[doc])
	denom := float64(tf) + k1*(1-b+b*dl/s.avgLenSafe())
	return float64(tf) * (k1 + 1) / denom
}

func (s *snapshot) avgLenSafe() float64 {
	if s.avgLen <= 0 {
		return 1
	}
	return s.avgLen
}

// eval 把查询求值为 docID -> 得分 的映射（map 语义天然去重）。
func (s *snapshot) eval(q Query, opts *Options) map[uint32]float64 {
	switch node := q.(type) {
	case TermQuery:
		return s.evalTerm(node, opts)
	case PhraseQuery:
		return s.evalPhrase(node, opts)
	case AndQuery:
		var acc map[uint32]float64
		for _, c := range node.Clauses {
			sub := s.eval(c, opts)
			if acc == nil {
				acc = sub
				continue
			}
			for id, sc := range acc {
				if other, ok := sub[id]; ok {
					acc[id] = sc + other
				} else {
					delete(acc, id)
				}
			}
		}
		if acc == nil {
			return map[uint32]float64{}
		}
		return acc
	case OrQuery:
		acc := map[uint32]float64{}
		for _, c := range node.Clauses {
			for id, sc := range s.eval(c, opts) {
				acc[id] += sc
			}
		}
		return acc
	case NotQuery:
		var base map[uint32]float64
		if node.Include == nil {
			base = map[uint32]float64{}
			for i := 0; i < s.numDocs; i++ {
				base[uint32(i)] = 0
			}
		} else {
			base = s.eval(node.Include, opts)
		}
		ex := s.eval(node.Exclude, opts)
		for id := range ex {
			delete(base, id)
		}
		return base
	}
	return map[uint32]float64{}
}

func (s *snapshot) fieldOf(name string, opts *Options) string {
	if name == "" {
		return opts.defaultField()
	}
	return name
}

func (o *Options) defaultField() string {
	if o.DefaultField != "" {
		return o.DefaultField
	}
	return "body"
}

func (s *snapshot) evalTerm(t TermQuery, opts *Options) map[uint32]float64 {
	field := s.fieldOf(t.Field, opts)
	term := strings.ToLower(t.Term)
	postings := s.postingsFor(field, term)
	out := make(map[uint32]float64, len(postings))
	if len(postings) == 0 {
		return out
	}
	idf := s.idf(len(postings))
	for _, p := range postings {
		out[p.docID] = idf * s.tfComponent(p.freq, p.docID, opts)
	}
	return out
}

// evalPhrase 基于位置判定相邻性：对每个起始位置 pos，
// 要求第 i 个词出现在 pos+i。位置缺失时（如 keyword 字段）不会误判为命中。
func (s *snapshot) evalPhrase(p PhraseQuery, opts *Options) map[uint32]float64 {
	field := s.fieldOf(p.Field, opts)
	out := map[uint32]float64{}
	// keyword 字段整体作为一个词项存储，短语退化为整值匹配
	joined := strings.Join(p.Terms, " ")
	if jp := s.postingsFor(field, joined); len(jp) > 0 {
		idf := s.idf(len(jp))
		for _, po := range jp {
			out[po.docID] = idf * s.tfComponent(po.freq, po.docID, opts)
		}
		return out
	}
	lists := make([][]posting, len(p.Terms))
	for i, t := range p.Terms {
		lists[i] = s.postingsFor(field, t)
		if len(lists[i]) == 0 {
			return out
		}
	}
	// 以第一个词的 postings 为基准，其余词建 docID -> posting 索引
	others := make([]map[uint32]posting, len(lists)-1)
	for i := 1; i < len(lists); i++ {
		m := make(map[uint32]posting, len(lists[i]))
		for _, po := range lists[i] {
			m[po.docID] = po
		}
		others[i-1] = m
	}
	idf := 0.0
	for _, l := range lists {
		idf += s.idf(len(l))
	}
	for _, first := range lists[0] {
		tf := 0
		for _, start := range first.positions {
			match := true
			for i := 1; i < len(lists); i++ {
				po, ok := others[i-1][first.docID]
				if !ok || !containsInt(po.positions, start+i) {
					match = false
					break
				}
			}
			if match {
				tf++
			}
		}
		if tf > 0 {
			out[first.docID] = idf * s.tfComponent(tf, first.docID, opts)
		}
	}
	return out
}

func containsInt(xs []int, v int) bool {
	i := sort.SearchInts(xs, v)
	return i < len(xs) && xs[i] == v
}

// search 在快照上执行查询并按 (得分降序, 外部ID升序) 排序，保证可复现。
func (s *snapshot) search(q Query, opts *Options, topK int) []Hit {
	scores := s.eval(q, opts)
	hits := make([]Hit, 0, len(scores))
	for id, sc := range scores {
		hits = append(hits, Hit{DocID: s.docIDs[id], Score: sc})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].DocID < hits[j].DocID
	})
	if topK > 0 && len(hits) > topK {
		hits = hits[:topK]
	}
	return hits
}
