package fts

import (
	"sort"
	"strings"
)

// posting 是倒排表中的一条记录。
type posting struct {
	docNum    uint32   // 内部文档编号
	positions []uint32 // 词在字段内的位置；关键字字段为 nil
}

// fieldPostings 是某字段下某词项的倒排表，按 docNum 升序。
type fieldPostings []posting

// snapshot 是不可变的索引快照。查询只读快照；写入在快照副本上修改后
// 原子替换，因此读写互不阻塞，查询永远看到某个已提交的完整版本。
//
// 确定性约定：内部编号 docNum 单调分配、不参与排序与评分；
// 评分只依赖词频、文档频率、文档长度等顺序无关的统计量；
// 命中按 (得分降序, 文档 ID 升序) 排列，因此同一批文档无论
// 分批方式与并发度如何，同一查询的结果逐条一致。
type snapshot struct {
	// inverted[field][term] = 倒排表
	inverted map[string]map[string]fieldPostings
	// fieldKinds[field] = 字段类型（正文 / 关键字）
	fieldKinds map[string]FieldKind
	// docIDs[docNum] = 外部文档 ID；docNums[docID] = 内部编号
	docIDs  map[uint32]string
	docNums map[string]uint32
	// docLens[field][docNum] = 文档在该正文字段的词数（长度归一化用）
	docLens map[string]map[uint32]uint32
	// sumLen[field] = 该正文字段全部文档词数之和
	sumLen map[string]uint64
	// docFreq[field][term] = 词项在该字段的文档频率
	docFreq map[string]map[string]int
	numDocs int
}

// newSnapshot 返回空快照。
func newSnapshot() *snapshot {
	return &snapshot{
		inverted:   make(map[string]map[string]fieldPostings),
		fieldKinds: make(map[string]FieldKind),
		docIDs:     make(map[uint32]string),
		docNums:    make(map[string]uint32),
		docLens:    make(map[string]map[uint32]uint32),
		sumLen:     make(map[string]uint64),
		docFreq:    make(map[string]map[string]int),
	}
}

// clone 返回快照的写时拷贝：倒排表切片与内层统计共享，
// 只有在具体词项上增删 posting 时才替换对应切片。
func (s *snapshot) clone() *snapshot {
	c := newSnapshot()
	for f, terms := range s.inverted {
		nt := make(map[string]fieldPostings, len(terms))
		for t, p := range terms {
			nt[t] = p
		}
		c.inverted[f] = nt
	}
	for f, k := range s.fieldKinds {
		c.fieldKinds[f] = k
	}
	for n, id := range s.docIDs {
		c.docIDs[n] = id
	}
	for id, n := range s.docNums {
		c.docNums[id] = n
	}
	for f, lens := range s.docLens {
		nl := make(map[uint32]uint32, len(lens))
		for d, l := range lens {
			nl[d] = l
		}
		c.docLens[f] = nl
	}
	for f, sum := range s.sumLen {
		c.sumLen[f] = sum
	}
	for f, df := range s.docFreq {
		nd := make(map[string]int, len(df))
		for t, n := range df {
			nd[t] = n
		}
		c.docFreq[f] = nd
	}
	c.numDocs = s.numDocs
	return c
}

// ---- 文档分析与增删原语 ----

// analyzedField 是一篇文档中单个字段的分析结果。
type analyzedField struct {
	name   string
	kind   FieldKind
	tokens []Token // 正文字段：带位置；关键字字段：至多一个
}

// analyzedDoc 是一篇文档的完整分析结果。
type analyzedDoc struct {
	id     string
	docNum uint32
	fields []analyzedField
}

// analyzeDocument 对文档执行统一分析流程。
func analyzeDocument(a *Analyzer, doc Document) *analyzedDoc {
	ad := &analyzedDoc{id: doc.ID}
	for _, f := range doc.Fields {
		af := analyzedField{name: f.Name, kind: f.Kind}
		switch f.Kind {
		case FieldKindKeyword:
			t := a.AnalyzeKeyword(f.Value)
			if t.Term != "" {
				af.tokens = []Token{t}
			}
		default:
			af.tokens = a.AnalyzeText(f.Value)
		}
		ad.fields = append(ad.fields, af)
	}
	return ad
}

// insertPosting 把 (docNum, positions) 插入按 docNum 升序的倒排表。
func insertPosting(p fieldPostings, docNum uint32, positions []uint32) fieldPostings {
	i := sort.Search(len(p), func(i int) bool { return p[i].docNum >= docNum })
	np := make(fieldPostings, 0, len(p)+1)
	np = append(np, p[:i]...)
	np = append(np, posting{docNum: docNum, positions: positions})
	np = append(np, p[i:]...)
	return np
}

// removePosting 从倒排表中删除指定 docNum。
func removePosting(p fieldPostings, docNum uint32) fieldPostings {
	i := sort.Search(len(p), func(i int) bool { return p[i].docNum >= docNum })
	if i >= len(p) || p[i].docNum != docNum {
		return p
	}
	np := make(fieldPostings, 0, len(p)-1)
	np = append(np, p[:i]...)
	np = append(np, p[i+1:]...)
	return np
}

// addDoc 把一篇已分析的文档加入快照（调用方保证 docNum 未被占用）。
func (s *snapshot) addDoc(ad *analyzedDoc) {
	s.docIDs[ad.docNum] = ad.id
	s.docNums[ad.id] = ad.docNum
	s.numDocs++
	for _, af := range ad.fields {
		s.fieldKinds[af.name] = af.kind
		terms := s.inverted[af.name]
		if terms == nil {
			terms = make(map[string]fieldPostings)
			s.inverted[af.name] = terms
		}
		df := s.docFreq[af.name]
		if df == nil {
			df = make(map[string]int)
			s.docFreq[af.name] = df
		}
		if af.kind == FieldKindKeyword {
			for _, t := range af.tokens {
				terms[t.Term] = insertPosting(terms[t.Term], ad.docNum, nil)
				df[t.Term]++
			}
			continue
		}
		// 正文字段：按词项聚合位置
		positions := make(map[string][]uint32)
		for _, t := range af.tokens {
			positions[t.Term] = append(positions[t.Term], uint32(t.Position))
		}
		for term, pos := range positions {
			terms[term] = insertPosting(terms[term], ad.docNum, pos)
			df[term]++
		}
		lens := s.docLens[af.name]
		if lens == nil {
			lens = make(map[uint32]uint32)
			s.docLens[af.name] = lens
		}
		lens[ad.docNum] = uint32(len(af.tokens))
		s.sumLen[af.name] += uint64(len(af.tokens))
	}
}

// removeDoc 从快照中移除一篇已分析的文档。
func (s *snapshot) removeDoc(ad *analyzedDoc) {
	delete(s.docIDs, ad.docNum)
	delete(s.docNums, ad.id)
	s.numDocs--
	for _, af := range ad.fields {
		terms := s.inverted[af.name]
		df := s.docFreq[af.name]
		seen := make(map[string]bool)
		for _, t := range af.tokens {
			if seen[t.Term] {
				continue
			}
			seen[t.Term] = true
			np := removePosting(terms[t.Term], ad.docNum)
			if len(np) == 0 {
				delete(terms, t.Term)
			} else {
				terms[t.Term] = np
			}
			if df != nil {
				df[t.Term]--
				if df[t.Term] <= 0 {
					delete(df, t.Term)
				}
			}
		}
		if af.kind != FieldKindKeyword {
			if lens := s.docLens[af.name]; lens != nil {
				s.sumLen[af.name] -= uint64(lens[ad.docNum])
				delete(lens, ad.docNum)
			}
		}
	}
}

// avgFieldLen 返回字段的平均文档长度（缺失该字段的文档按 0 计）。
func (s *snapshot) avgFieldLen(field string) float64 {
	if s.numDocs == 0 {
		return 0
	}
	return float64(s.sumLen[field]) / float64(s.numDocs)
}

// ---- 查询执行 ----

// Hit 是一条查询命中。
type Hit struct {
	DocID string
	Score float64
}

// docSet 是 docNum -> 得分 的集合，天然去重。
type docSet map[uint32]float64

// textFields 返回查询应覆盖的正文字段名（排序保证确定性）。
func (s *snapshot) textFields() []string {
	var fields []string
	for f, k := range s.fieldKinds {
		if k == FieldKindText {
			fields = append(fields, f)
		}
	}
	sort.Strings(fields)
	return fields
}

// evalTerm 在单个字段上执行词项查询。
func (s *snapshot) evalTerm(field, term string, params BM25Params) docSet {
	terms := s.inverted[field]
	if terms == nil {
		return nil
	}
	postings := terms[term]
	if len(postings) == 0 {
		return nil
	}
	out := make(docSet, len(postings))
	df := s.docFreq[field][term]
	idf := bm25IDF(s.numDocs, df)
	if s.fieldKinds[field] == FieldKindKeyword {
		// 关键字字段：精确匹配，得分仅取 idf（无词频/长度概念）
		for _, p := range postings {
			out[p.docNum] = idf
		}
		return out
	}
	avg := s.avgFieldLen(field)
	lens := s.docLens[field]
	for _, p := range postings {
		tf := len(p.positions)
		out[p.docNum] = bm25Score(params, tf, int(lens[p.docNum]), avg, idf)
	}
	return out
}

// phraseMatches 判断一组位置序列中是否存在相邻递增的短语匹配。
func phraseMatches(positions [][]uint32) bool {
	if len(positions) == 0 {
		return false
	}
	current := positions[0]
	for _, next := range positions[1:] {
		set := make(map[uint32]struct{}, len(next))
		for _, p := range next {
			set[p] = struct{}{}
		}
		var shifted []uint32
		for _, p := range current {
			if _, ok := set[p+1]; ok {
				shifted = append(shifted, p+1)
			}
		}
		if len(shifted) == 0 {
			return false
		}
		current = shifted
	}
	return true
}

// evalPhrase 在单个正文字段上执行短语查询。
func (s *snapshot) evalPhrase(field string, terms []string, params BM25Params) docSet {
	if s.fieldKinds[field] == FieldKindKeyword {
		// 关键字字段：短语按整体值精确匹配
		return s.evalTerm(field, strings.Join(terms, " "), params)
	}
	tm := s.inverted[field]
	if tm == nil {
		return nil
	}
	lists := make([]fieldPostings, len(terms))
	for i, t := range terms {
		lists[i] = tm[t]
		if len(lists[i]) == 0 {
			return nil
		}
	}
	// 按 docNum 归并求交集
	out := make(docSet)
	idx := make([]int, len(terms))
	for {
		var maxNum uint32
		for i := range lists {
			if idx[i] >= len(lists[i]) {
				return out
			}
			if lists[i][idx[i]].docNum > maxNum {
				maxNum = lists[i][idx[i]].docNum
			}
		}
		allEqual := true
		positions := make([][]uint32, len(terms))
		for i := range lists {
			for idx[i] < len(lists[i]) && lists[i][idx[i]].docNum < maxNum {
				idx[i]++
			}
			if idx[i] >= len(lists[i]) || lists[i][idx[i]].docNum != maxNum {
				allEqual = false
				break
			}
			positions[i] = lists[i][idx[i]].positions
		}
		if !allEqual {
			continue
		}
		if phraseMatches(positions) {
			// 短语得分 = 各组成词项在该字段的 BM25 得分之和
			var score float64
			avg := s.avgFieldLen(field)
			docLen := int(s.docLens[field][maxNum])
			for i, t := range terms {
				idf := bm25IDF(s.numDocs, s.docFreq[field][t])
				score += bm25Score(params, len(positions[i]), docLen, avg, idf)
			}
			out[maxNum] = score
		}
		for i := range lists {
			idx[i]++
		}
	}
}

// mergeAnd 求交集，得分相加。
func mergeAnd(a, b docSet) docSet {
	if a == nil || b == nil {
		return nil
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	out := make(docSet, len(a))
	for d, sa := range a {
		if sb, ok := b[d]; ok {
			out[d] = sa + sb
		}
	}
	return out
}

// mergeOr 求并集，得分相加。
func mergeOr(a, b docSet) docSet {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	for d, sb := range b {
		a[d] += sb
	}
	return a
}

// eval 递归执行查询，返回命中集合与得分。
func (s *snapshot) eval(q Query, params BM25Params) docSet {
	switch node := q.(type) {
	case *TermQuery:
		if node.Field != "" {
			return s.evalTerm(node.Field, node.Term, params)
		}
		var out docSet
		for _, f := range s.textFields() {
			out = mergeOr(out, s.evalTerm(f, node.Term, params))
		}
		return out
	case *PhraseQuery:
		if node.Field != "" {
			return s.evalPhrase(node.Field, node.Terms, params)
		}
		var out docSet
		for _, f := range s.textFields() {
			out = mergeOr(out, s.evalPhrase(f, node.Terms, params))
		}
		return out
	case *AndQuery:
		var out docSet
		for i, c := range node.Children {
			r := s.eval(c, params)
			if i == 0 {
				out = r
			} else {
				out = mergeAnd(out, r)
			}
			if len(out) == 0 {
				return nil
			}
		}
		return out
	case *OrQuery:
		var out docSet
		for _, c := range node.Children {
			out = mergeOr(out, s.eval(c, params))
		}
		return out
	case *NotQuery:
		excluded := s.eval(node.Child, params)
		out := make(docSet, s.numDocs-len(excluded))
		for docNum := range s.docIDs {
			if _, ok := excluded[docNum]; !ok {
				out[docNum] = 0
			}
		}
		return out
	}
	return nil
}

// search 在快照上执行查询，返回按相关性排序的命中列表。
// 排序规则：得分降序，得分相同按文档 ID 升序，保证可复现。
func (s *snapshot) search(q Query, params BM25Params, limit int) []Hit {
	matched := s.eval(q, params)
	if len(matched) == 0 {
		return nil
	}
	hits := make([]Hit, 0, len(matched))
	for docNum, score := range matched {
		hits = append(hits, Hit{DocID: s.docIDs[docNum], Score: score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].DocID < hits[j].DocID
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}
