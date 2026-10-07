package fts

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFlushAndReopen(t *testing.T) {
	dir := t.TempDir()
	e, _ := Open(Options{Dir: dir})
	mustAddCommit(t, e,
		textDoc("d1", "persistent alpha data"),
		textDoc("d2", "persistent beta"),
	)
	before, _ := e.Search("persistent", 0)
	if err := e.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	// 重启后结果与落盘前完全一致
	e2, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	after, err := e2.Search("persistent", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("reopen mismatch: %v vs %v", before, after)
	}
	// 恢复后仍可增量维护
	if err := e2.Add(textDoc("d3", "persistent gamma")); err != nil {
		t.Fatal(err)
	}
	e2.Commit()
	hits, _ := e2.Search("persistent", 0)
	if len(hits) != 3 {
		t.Fatalf("incremental after reopen failed: %v", hits)
	}
}

// 未 Flush 的提交在重启后不可见（已提交与未提交可区分）。
func TestUnflushedCommitNotRecovered(t *testing.T) {
	dir := t.TempDir()
	e, _ := Open(Options{Dir: dir})
	mustAddCommit(t, e, textDoc("d1", "flushed doc"))
	if err := e.Flush(); err != nil {
		t.Fatal(err)
	}
	// 提交但不落盘
	if err := e.Add(textDoc("d2", "unflushed doc")); err != nil {
		t.Fatal(err)
	}
	e.Commit()

	e2, _ := Open(Options{Dir: dir})
	hits, _ := e2.Search("doc", 0)
	if got := hitIDs(hits); !reflect.DeepEqual(got, []string{"d1"}) {
		t.Fatalf("unflushed data leaked: %v", got)
	}
}

// 中断的写入（残留 .tmp / 损坏快照）不得影响恢复。
func TestCrashDuringFlush(t *testing.T) {
	dir := t.TempDir()
	e, _ := Open(Options{Dir: dir})
	mustAddCommit(t, e, textDoc("d1", "safe data"))
	if err := e.Flush(); err != nil {
		t.Fatal(err)
	}
	// 模拟崩溃残留：半成品 tmp 文件 + 损坏的新版本快照
	os.WriteFile(filepath.Join(dir, "snapshot-99.tmp"), []byte("partial"), 0o644)
	os.WriteFile(filepath.Join(dir, "snapshot-100"), []byte("garbage-not-a-snapshot"), 0o644)
	// CURRENT 被指向损坏版本时，应回退到最近可用版本
	os.WriteFile(filepath.Join(dir, "CURRENT"), []byte("100"), 0o644)

	e2, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	hits, _ := e2.Search("safe", 0)
	if got := hitIDs(hits); !reflect.DeepEqual(got, []string{"d1"}) {
		t.Fatalf("crash recovery failed: %v", got)
	}
}

// 损坏的 CURRENT 文件不得导致启动失败。
func TestCorruptCurrentFile(t *testing.T) {
	dir := t.TempDir()
	e, _ := Open(Options{Dir: dir})
	mustAddCommit(t, e, textDoc("d1", "recoverable"))
	if err := e.Flush(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "CURRENT"), []byte("not-a-number"), 0o644)
	e2, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	hits, _ := e2.Search("recoverable", 0)
	if len(hits) != 1 {
		t.Fatalf("got %v", hits)
	}
}
