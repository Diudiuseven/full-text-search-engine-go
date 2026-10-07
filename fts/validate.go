package fts

import "unicode/utf8"

const defaultMaxDocBytes = 1 << 20 // 1 MiB

// validateDocument 检查文档合法性，返回可区分原因的错误。
func validateDocument(doc *Document, opts *Options) error {
	if doc.ID == "" {
		return &Error{Kind: ErrDocInvalid, Detail: "empty document id"}
	}
	max := opts.MaxDocBytes
	if max <= 0 {
		max = defaultMaxDocBytes
	}
	total := 0
	for _, f := range doc.Fields {
		if f.Name == "" {
			return errorf(ErrDocInvalid, doc.ID, "empty field name")
		}
		total += len(f.Value)
		if !utf8.ValidString(f.Value) {
			return errorf(ErrDocBadEncoding, doc.ID, "field %q is not valid UTF-8", f.Name)
		}
	}
	if total > max {
		return errorf(ErrDocTooLarge, doc.ID, "document of %d bytes exceeds limit %d", total, max)
	}
	return nil
}
