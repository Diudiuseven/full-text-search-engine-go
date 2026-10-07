package fts_test

import (
	"fmt"

	fts "github.com/Diudiuseven/full-text-search-engine-go"
)

func Example() {
	// 纯内存引擎；Open("/path/to/dir") 可启用落盘与重启复用
	e, _ := fts.Open("")

	e.Add(
		fts.Document{ID: "1", Fields: []fts.Field{
			{Name: "body", Kind: fts.FieldKindText, Value: "the quick brown fox"},
			{Name: "tag", Kind: fts.FieldKindKeyword, Value: "animal"},
		}},
		fts.Document{ID: "2", Fields: []fts.Field{
			{Name: "body", Kind: fts.FieldKindText, Value: "the lazy dog sleeps"},
			{Name: "tag", Kind: fts.FieldKindKeyword, Value: "pet"},
		}},
	)
	if _, err := e.Commit(); err != nil {
		panic(err)
	}

	hits, _ := e.Search(`tag:animal AND "quick brown"`, 10)
	for _, h := range hits {
		fmt.Printf("doc=%s score=%.3f\n", h.DocID, h.Score)
	}
	// Output:
	// doc=1 score=2.079
}
