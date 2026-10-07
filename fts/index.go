package fts

// posting 记录一个词项在一篇文档中的出现情况。
type posting struct {
	docID     uint32
	freq      int
	positions []int
}

// analyzedDoc 是一篇文档经过统一分析流程后的词项视图，可被缓存复用。
type analyzedDoc struct {
	byField map[string][]Token
	total   int
}

// analyzeDocument 对单篇文档执行分析流程。
func analyzeDocument(doc *Document, opts *Options) (analyzedDoc, error) {
	ad := analyzedDoc{byField: map[string][]Token{}}
	for _, f := range doc.Fields {
		toks, err := opts.analyzerFor(f.Kind).Analyze(f)
		if err != nil {
			return ad, err
		}
		ad.byField[f.Name] = append(ad.byField[f.Name], toks...)
		ad.total += len(toks)
	}
	return ad, nil
}

// snapshot 是某一已提交版本的完整不可变索引视图。
// 内部 docID 按外部 ID 字典序分配，因此同一文档集合无论分多少批、
// 以何种并发度提交，构建出的快照逐字节一致。
type snapshot struct {
	version uint64
	// inverted: field -> term -> postings（按 docID 升序）
	inverted map[string]map[string][]posting
	docIDs   []string          // 内部 docID -> 外部 ID
	ext2int  map[string]uint32 // 外部 ID -> 内部 docID
	docLen   []int             // 内部 docID -> 全部字段总词数
	avgLen   float64
	numDocs  int
}

// assembleSnapshot 用已分析好的文档组装不可变快照；ids 必须已按字典序排序。
func assembleSnapshot(ids []string, analyzed []analyzedDoc, version uint64) *snapshot {
	s := &snapshot{
		version:  version,
		inverted: map[string]map[string][]posting{},
		ext2int:  map[string]uint32{},
	}
	totalLen := 0
	for i, id := range ids {
		intID := uint32(i)
		s.docIDs = append(s.docIDs, id)
		s.ext2int[id] = intID
		s.docLen = append(s.docLen, analyzed[i].total)
		totalLen += analyzed[i].total
	}
	s.numDocs = len(s.docIDs)
	if s.numDocs > 0 {
		s.avgLen = float64(totalLen) / float64(s.numDocs)
	}
	// 倒排：按内部 docID 升序遍历，天然得到有序 postings。
	for intID, ad := range analyzed {
		for field, toks := range ad.byField {
			fm := s.inverted[field]
			if fm == nil {
				fm = map[string][]posting{}
				s.inverted[field] = fm
			}
			positions := map[string][]int{}
			order := []string{}
			for _, t := range toks {
				if _, ok := positions[t.Term]; !ok {
					order = append(order, t.Term)
				}
				positions[t.Term] = append(positions[t.Term], t.Position)
			}
			for _, term := range order {
				pos := positions[term]
				fm[term] = append(fm[term], posting{
					docID:     uint32(intID),
					freq:      len(pos),
					positions: pos,
				})
			}
		}
	}
	return s
}

// postingsFor 取某字段某词项的倒排表。
func (s *snapshot) postingsFor(field, term string) []posting {
	if fm, ok := s.inverted[field]; ok {
		return fm[term]
	}
	return nil
}

// DocError 记录被跳过文档及其原因。
type DocError struct {
	DocID string
	Err   error
}
