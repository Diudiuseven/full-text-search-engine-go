package fts

// FieldKind 区分字段的分析方式：正文走完整分析流程，Keyword 字段整体作为一个词项。
type FieldKind int

const (
	FieldText FieldKind = iota
	FieldKeyword
)

// Field 描述一个文档字段。
type Field struct {
	Name  string
	Kind  FieldKind
	Value string
}

// Document 是进入索引的基本单元，ID 由调用方给定且必须唯一。
type Document struct {
	ID     string
	Fields []Field
}

// Get 返回指定名字字段的值。
func (d *Document) Get(name string) (Field, bool) {
	for _, f := range d.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}
