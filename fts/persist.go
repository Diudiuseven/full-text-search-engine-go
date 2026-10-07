package fts

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// 落盘格式：
//   <dir>/snapshot-<version>.tmp  写入中（未提交，加载时忽略）
//   <dir>/snapshot-<version>      完整快照（gob 编码 + CRC32 尾校验）
//   <dir>/CURRENT                 指向最近可用版本号（先写 .tmp 再 rename）
// 任何中断只会留下 .tmp 残文件或未被 CURRENT 指向的孤儿快照，
// 加载时一律忽略，不会出现半提交状态。

type snapshotFile struct {
	Version uint64
	Docs    map[string]Document
}

const magic = "FTS1"

func encodeSnapshot(sf *snapshotFile) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(magic)
	if err := gob.NewEncoder(&buf).Encode(sf); err != nil {
		return nil, err
	}
	crc := crc32.ChecksumIEEE(buf.Bytes())
	var tail [4]byte
	tail[0] = byte(crc >> 24)
	tail[1] = byte(crc >> 16)
	tail[2] = byte(crc >> 8)
	tail[3] = byte(crc)
	buf.Write(tail[:])
	return buf.Bytes(), nil
}

func decodeSnapshot(data []byte) (*snapshotFile, error) {
	if len(data) < len(magic)+4 || string(data[:len(magic)]) != magic {
		return nil, fmt.Errorf("fts: bad snapshot magic")
	}
	body, tail := data[:len(data)-4], data[len(data)-4:]
	want := crc32.ChecksumIEEE(body)
	got := uint32(tail[0])<<24 | uint32(tail[1])<<16 | uint32(tail[2])<<8 | uint32(tail[3])
	if want != got {
		return nil, fmt.Errorf("fts: snapshot checksum mismatch")
	}
	var sf snapshotFile
	if err := gob.NewDecoder(bytes.NewReader(body[len(magic):])).Decode(&sf); err != nil {
		return nil, fmt.Errorf("fts: decode snapshot: %w", err)
	}
	return &sf, nil
}

// saveSnapshot 原子地写入新版本：先写临时文件并 rename，再更新 CURRENT。
func saveSnapshot(dir string, docs map[string]Document, version uint64) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := encodeSnapshot(&snapshotFile{Version: version, Docs: docs})
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, fmt.Sprintf("snapshot-%d.tmp", version))
	final := filepath.Join(dir, fmt.Sprintf("snapshot-%d", version))
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return err
	}
	// 更新 CURRENT（同样先写临时文件再 rename）
	curTmp := filepath.Join(dir, "CURRENT.tmp")
	if err := os.WriteFile(curTmp, []byte(strconv.FormatUint(version, 10)), 0o644); err != nil {
		return err
	}
	if err := os.Rename(curTmp, filepath.Join(dir, "CURRENT")); err != nil {
		return err
	}
	// 清理旧版本与残留临时文件
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if name == fmt.Sprintf("snapshot-%d", version) || name == "CURRENT" {
			continue
		}
		if strings.HasPrefix(name, "snapshot-") || strings.HasSuffix(name, ".tmp") {
			os.Remove(filepath.Join(dir, name))
		}
	}
	return nil
}

// loadSnapshot 加载最近一次完整提交；中断留下的部分数据一律忽略。
// 返回 (nil, 0, nil) 表示没有可用快照。
func loadSnapshot(dir string) (map[string]Document, uint64, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, 0, nil
	}
	// 优先按 CURRENT 指向加载
	if data, err := os.ReadFile(filepath.Join(dir, "CURRENT")); err == nil {
		if v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil {
			if sf, err := loadVersion(dir, v); err == nil {
				return sf.Docs, sf.Version, nil
			}
			// CURRENT 指向的版本损坏：回退到扫描可用版本
		}
	}
	// 扫描所有完整快照，取版本最高且校验通过者
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, nil
	}
	var versions []uint64
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "snapshot-") || strings.HasSuffix(name, ".tmp") {
			continue
		}
		if v, err := strconv.ParseUint(strings.TrimPrefix(name, "snapshot-"), 10, 64); err == nil {
			versions = append(versions, v)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] > versions[j] })
	for _, v := range versions {
		if sf, err := loadVersion(dir, v); err == nil {
			return sf.Docs, sf.Version, nil
		}
	}
	return nil, 0, nil
}

func loadVersion(dir string, v uint64) (*snapshotFile, error) {
	data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("snapshot-%d", v)))
	if err != nil {
		return nil, err
	}
	return decodeSnapshot(data)
}
