package fts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 持久化文件布局（位于引擎目录下）：
//   MANIFEST        记录当前已提交版本号、数据文件名与 SHA-256 校验和
//   index-<ver>.tmp 写入中的临时文件（未提交，加载时清理）
//   index-<ver>.db  已完整写入的快照数据文件
// 提交流程：写 tmp -> fsync -> 重命名为 db -> 同样方式更新 MANIFEST ->
// 清理旧版本数据文件。任一环节中断，MANIFEST 仍指向上一完整版本，
// 未完成的 tmp 文件在下次打开时清理，不会留下部分数据。

const manifestFile = "MANIFEST"

type manifest struct {
	Version uint64 `json:"version"`
	File    string `json:"file"`
	SHA256  string `json:"sha256"`
}

// 落盘用的可序列化结构。
type diskPosting struct {
	DocNum    uint32   `json:"d"`
	Positions []uint32 `json:"p,omitempty"`
}

type diskIndex struct {
	Version    uint64                              `json:"version"`
	NextDocNum uint32                              `json:"next_doc_num"`
	FieldKinds map[string]FieldKind                `json:"field_kinds"`
	Inverted   map[string]map[string][]diskPosting `json:"inverted"`
	DocIDs     map[uint32]string                   `json:"doc_ids"`
	DocLens    map[string]map[uint32]uint32        `json:"doc_lens"`
	DocFreq    map[string]map[string]int           `json:"doc_freq"`
	NumDocs    int                                 `json:"num_docs"`
	Docs       map[string]Document                 `json:"docs"`
}

// toDisk 把内存快照转换为可序列化结构。
func toDisk(s *snapshot, docs map[string]Document, nextDocNum uint32, version uint64) *diskIndex {
	di := &diskIndex{
		Version:    version,
		NextDocNum: nextDocNum,
		FieldKinds: s.fieldKinds,
		Inverted:   make(map[string]map[string][]diskPosting, len(s.inverted)),
		DocIDs:     s.docIDs,
		DocLens:    s.docLens,
		DocFreq:    s.docFreq,
		NumDocs:    s.numDocs,
		Docs:       docs,
	}
	for f, terms := range s.inverted {
		dt := make(map[string][]diskPosting, len(terms))
		for t, postings := range terms {
			dp := make([]diskPosting, len(postings))
			for i, p := range postings {
				dp[i] = diskPosting{DocNum: p.docNum, Positions: p.positions}
			}
			dt[t] = dp
		}
		di.Inverted[f] = dt
	}
	return di
}

// fromDisk 把磁盘结构还原为内存快照，并派生出 docNums 与 sumLen。
func fromDisk(di *diskIndex) *snapshot {
	s := newSnapshot()
	s.fieldKinds = di.FieldKinds
	s.docIDs = di.DocIDs
	s.docLens = di.DocLens
	s.docFreq = di.DocFreq
	s.numDocs = di.NumDocs
	for n, id := range di.DocIDs {
		s.docNums[id] = n
	}
	for f, terms := range di.Inverted {
		tm := make(map[string]fieldPostings, len(terms))
		for t, dp := range terms {
			fp := make(fieldPostings, len(dp))
			for i, p := range dp {
				fp[i] = posting{docNum: p.DocNum, positions: p.Positions}
			}
			tm[t] = fp
		}
		s.inverted[f] = tm
	}
	for f, lens := range di.DocLens {
		for _, l := range lens {
			s.sumLen[f] += uint64(l)
		}
	}
	if s.fieldKinds == nil {
		s.fieldKinds = make(map[string]FieldKind)
	}
	if s.docIDs == nil {
		s.docIDs = make(map[uint32]string)
	}
	if s.docLens == nil {
		s.docLens = make(map[string]map[uint32]uint32)
	}
	if s.docFreq == nil {
		s.docFreq = make(map[string]map[string]int)
	}
	return s
}

// writeFileAtomic 先写临时文件并 fsync，再原子重命名为目标文件。
func writeFileAtomic(dir, name string, data []byte) error {
	tmp := filepath.Join(dir, name+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

// saveSnapshot 将快照原子落盘。MANIFEST 最后更新，
// 因此崩溃时重启只会看到上一个完整版本。
func saveSnapshot(dir string, s *snapshot, docs map[string]Document, nextDocNum uint32, version uint64) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	payload, err := json.Marshal(toDisk(s, docs, nextDocNum, version))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	dbName := fmt.Sprintf("index-%d.db", version)
	if err := writeFileAtomic(dir, dbName, payload); err != nil {
		return err
	}
	m := manifest{Version: version, File: dbName, SHA256: hex.EncodeToString(sum[:])}
	mdata, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(dir, manifestFile, mdata); err != nil {
		return err
	}
	// 清单已提交，清理旧版本数据文件与残留临时文件
	cleanupDir(dir, dbName)
	return nil
}

// cleanupDir 删除除 keep 以外的数据文件与所有临时文件（尽力而为）。
func cleanupDir(dir, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if name == keep || name == manifestFile {
			continue
		}
		if strings.HasSuffix(name, ".tmp") ||
			(strings.HasPrefix(name, "index-") && strings.HasSuffix(name, ".db")) {
			os.Remove(filepath.Join(dir, name))
		}
	}
}

// loadSnapshot 加载目录中最近一次完整提交的快照。
// 目录为空或无有效提交时返回空快照与版本 0；
// 校验和不匹配等损坏情况返回错误，不会产生部分加载的状态。
func loadSnapshot(dir string) (*snapshot, map[string]Document, uint32, uint64, error) {
	empty := newSnapshot()
	mdata, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if err != nil {
		if os.IsNotExist(err) {
			cleanupDir(dir, "")
			return empty, make(map[string]Document), 0, 0, nil
		}
		return nil, nil, 0, 0, err
	}
	var m manifest
	if err := json.Unmarshal(mdata, &m); err != nil {
		return nil, nil, 0, 0, fmt.Errorf("corrupt MANIFEST: %w", err)
	}
	payload, err := os.ReadFile(filepath.Join(dir, m.File))
	if err != nil {
		return nil, nil, 0, 0, fmt.Errorf("cannot read committed data file %s: %w", m.File, err)
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != m.SHA256 {
		return nil, nil, 0, 0, fmt.Errorf("checksum mismatch for %s: committed version %d is corrupt", m.File, m.Version)
	}
	var di diskIndex
	if err := json.Unmarshal(payload, &di); err != nil {
		return nil, nil, 0, 0, fmt.Errorf("corrupt data file %s: %w", m.File, err)
	}
	if di.Docs == nil {
		di.Docs = make(map[string]Document)
	}
	cleanupDir(dir, m.File)
	return fromDisk(&di), di.Docs, di.NextDocNum, di.Version, nil
}
