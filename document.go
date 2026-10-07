package fts

import (
	"fmt"
	"unicode/utf8"
)

// FieldKind 区分正文字段与需要单独匹配的字段。
type FieldKind int

const (
	// FieldKindText 正文字段：分词并记录位置，参与短语查询与相关性评分。
	FieldKindText FieldKind = iota
	// FieldKindKeyword 关键字字段：整体作为一个词项，仅支持精确匹配。
	FieldKindKeyword
)

// Field 是文档中的一个命名字段。
type Field struct {
	Name  string
	Kind  FieldKind
	Value string
}

// Document 是待索引的文档。ID 由调用方提供，全局唯一。
type Document struct {
	ID     string
	Fields []Field
}

// 文档校验限制。
const (
	MaxDocumentBytes = 1 << 20 // 单文档所有字段合计最大字节数
	MaxFieldBytes    = 1 << 18 // 单字段最大字节数
)

// DocErrorReason 区分文档被拒绝的原因。
type DocErrorReason string

const (
	ReasonEmptyID        DocErrorReason = "empty_id"
	ReasonDuplicateID    DocErrorReason = "duplicate_id"
	ReasonNoFields       DocErrorReason = "no_fields"
	ReasonEmptyFieldName DocErrorReason = "empty_field_name"
	ReasonDocTooLarge    DocErrorReason = "doc_too_large"
	ReasonFieldTooLarge  DocErrorReason = "field_too_large"
	ReasonInvalidUTF8    DocErrorReason = "invalid_utf8"
)

// DocError 描述单篇文档被拒绝的原因，可定位到具体文档。
type DocError struct {
	DocID  string
	Reason DocErrorReason
	Detail string
}

func (e *DocError) Error() string {
	return fmt.Sprintf("document %q rejected: %s (%s)", e.DocID, e.Reason, e.Detail)
}

// validate 校验文档是否可进入索引，不合法时返回带原因的 DocError。
func validateDocument(doc Document) *DocError {
	if doc.ID == "" {
		return &DocError{DocID: doc.ID, Reason: ReasonEmptyID, Detail: "document ID must not be empty"}
	}
	if len(doc.Fields) == 0 {
		return &DocError{DocID: doc.ID, Reason: ReasonNoFields, Detail: "document has no fields"}
	}
	total := 0
	for _, f := range doc.Fields {
		if f.Name == "" {
			return &DocError{DocID: doc.ID, Reason: ReasonEmptyFieldName, Detail: "field name must not be empty"}
		}
		if len(f.Value) > MaxFieldBytes {
			return &DocError{DocID: doc.ID, Reason: ReasonFieldTooLarge,
				Detail: fmt.Sprintf("field %q is %d bytes, limit is %d", f.Name, len(f.Value), MaxFieldBytes)}
		}
		if !utf8.ValidString(f.Value) {
			return &DocError{DocID: doc.ID, Reason: ReasonInvalidUTF8,
				Detail: fmt.Sprintf("field %q contains invalid UTF-8", f.Name)}
		}
		total += len(f.Value)
	}
	if total > MaxDocumentBytes {
		return &DocError{DocID: doc.ID, Reason: ReasonDocTooLarge,
			Detail: fmt.Sprintf("document is %d bytes, limit is %d", total, MaxDocumentBytes)}
	}
	return nil
}
