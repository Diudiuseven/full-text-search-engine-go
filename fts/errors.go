package fts

import "fmt"

// ErrKind 区分可区分的失败原因。
type ErrKind int

const (
	ErrEmptyQuery ErrKind = iota
	ErrQuerySyntax
	ErrDocInvalid
	ErrDocTooLarge
	ErrDocBadEncoding
)

// Error 是可分类的引擎错误。
type Error struct {
	Kind   ErrKind
	DocID  string // 文档相关错误时定位到具体文档
	Detail string
}

func (e *Error) Error() string {
	if e.DocID != "" {
		return fmt.Sprintf("fts: kind=%d doc=%q: %s", e.Kind, e.DocID, e.Detail)
	}
	return fmt.Sprintf("fts: kind=%d: %s", e.Kind, e.Detail)
}

// errorf 构造一个带分类的错误。
func errorf(kind ErrKind, docID, format string, args ...any) *Error {
	return &Error{Kind: kind, DocID: docID, Detail: fmt.Sprintf(format, args...)}
}
