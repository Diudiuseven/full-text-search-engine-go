package fts

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestPersistenceRestart 验证落盘后重启可复用索引，且结果与内存一致。
func TestPersistenceRestart(t *testing.T) {
	dir := t.TempDir()

	e1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e1.Add(doc("d1", "persistent alpha data"), doc("d2", "persistent beta"))
	if _, err := e1.Commit(); err != nil {
		t.Fatal(err)
	}
	e1.Add(doc("d3", "second commit gamma"))
	e1.Delete("d2")
	if _, err := e1.Commit(); err != nil {
		t.Fatal(err)
	}
	want := searchAll(t, e1, "persistent OR gamma")
	if err := e1.Close(); err != nil {
		t.Fatal(err)
	}

	// 重启：应恢复最近完整提交的版本
	e2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if e2.Version() != 2 {
		t.Fatalf("expected version 2, got %d", e2.Version())
	}
	assertHitsEqual(t, searchAll(t, e2, "persistent OR gamma"), want)
	// 被删除的文档不得恢复
	for _, h := range searchAll(t, e2, "beta") {
		if h.DocID == "d2" {
			t.Fatal("deleted doc resurrected after restart")
		}
	}

	// 重启后仍可继续增量提交
	e2.Add(doc("d4", "third commit delta"))
	if _, err := e2.Commit(); err != nil {
		t.Fatal(err)
	}
	assertIDs(t, searchAll(t, e2, "delta"), "d4")

	// 再次重启验证第三次提交也完整
	e3, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if e3.Version() != 3 {
		t.Fatalf("expected version 3, got %d", e3.Version())
	}
	assertIDs(t, searchAll(t, e3, "delta"), "d4")
}

// TestTmpFilesCleanedOnOpen 验证未完成的写入（tmp 文件）在打开时被清理，
// 且不影响已提交版本。
func TestTmpFilesCleanedOnOpen(t *testing.T) {
	dir := t.TempDir()
	e1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e1.Add(doc("d1", "committed content"))
	if _, err := e1.Commit(); err != nil {
		t.Fatal(err)
	}

	// 模拟崩溃残留的未提交临时文件
	if err := os.WriteFile(filepath.Join(dir, "index-2.db.tmp"), []byte("partial garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MANIFEST.tmp"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}

	e2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if e2.Version() != 1 {
		t.Fatalf("expected version 1, got %d", e2.Version())
	}
	assertIDs(t, searchAll(t, e2, "committed"), "d1")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range entries {
		if filepath.Ext(en.Name()) == ".tmp" {
			t.Fatalf("tmp file %s not cleaned", en.Name())
		}
	}
}

// TestOldVersionDataFileRemoved 验证提交新版本后旧数据文件被清理。
func TestOldVersionDataFileRemoved(t *testing.T) {
	dir := t.TempDir()
	e, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		e.Add(doc(fmt.Sprintf("d%d", i), "rolling content"))
		if _, err := e.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	dbs := 0
	for _, en := range entries {
		if filepath.Ext(en.Name()) == ".db" {
			dbs++
			if en.Name() != "index-3.db" {
				t.Fatalf("stale data file %s not removed", en.Name())
			}
		}
	}
	if dbs != 1 {
		t.Fatalf("expected exactly 1 data file, got %d", dbs)
	}
}

// TestCorruptDataFileDetected 验证数据文件损坏被明确识别，不会部分加载。
func TestCorruptDataFileDetected(t *testing.T) {
	dir := t.TempDir()
	e, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e.Add(doc("d1", "some content"))
	if _, err := e.Commit(); err != nil {
		t.Fatal(err)
	}
	// 破坏已提交的数据文件
	if err := os.WriteFile(filepath.Join(dir, "index-1.db"), []byte("corrupted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("expected error on corrupt data file")
	}
}

// TestEmptyDirOpensClean 验证空目录与不存在目录都能正常打开。
func TestEmptyDirOpensClean(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-yet-created")
	e, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if e.Version() != 0 {
		t.Fatalf("expected version 0, got %d", e.Version())
	}
	e.Add(doc("d1", "fresh index"))
	if _, err := e.Commit(); err != nil {
		t.Fatal(err)
	}
	assertIDs(t, searchAll(t, e, "fresh"), "d1")
}
