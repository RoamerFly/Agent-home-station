package admin

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
)

const sendLedgerLimit = 2 << 20

// Archived receipts remain executable idempotency evidence. Never expire them
// or interpret an unreadable record as permission to submit again.
type archivedSend struct {
	Version int        `json:"version"`
	Digest  string     `json:"digest"`
	Record  toolRecord `json:"record"`
}

func verifiedSend(r toolRecord) bool {
	id, err := strconv.ParseUint(r.Status.ServerID, 10, 64)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != r.Status.ServerID {
		return false
	}
	for _, chunk := range r.Status.VoiceChunks {
		if chunk.State != "accepted" || !chunk.HasReceipt {
			return false
		}
	}
	return r.Key != "" && r.Hash != "" && r.Account != "" && r.Thread != "" &&
		r.Status.State == "accepted" && r.Status.ID != "" && r.Status.ServerID != "" &&
		!r.Status.Retryable && !r.Status.CancelRequested && r.Status.ThreadID == r.Thread
}

func sendLedgerID(kind, identity string) string {
	h := sha256.Sum256([]byte(kind + "\x00" + identity))
	return hex.EncodeToString(h[:])
}

func (s *toolStore) ledgerPath(kind, identity string, create bool) (string, error) {
	if kind != "requests" && kind != "ids" {
		return "", errors.New("发送索引类型无效")
	}
	id := sendLedgerID(kind, identity)
	root := filepath.Join(filepath.Dir(s.path), "send-ledger")
	dirs := []string{root, filepath.Join(root, kind), filepath.Join(root, kind, id[:2])}
	for _, dir := range dirs {
		if create {
			if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return "", err
			}
		}
		st, err := os.Lstat(dir)
		if err != nil {
			return "", err
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("发送归档目录不安全，未重新发送")
		}
	}
	return filepath.Join(dirs[2], id+".json.gz"), nil
}

func sendIdentity(kind string, r toolRecord) string {
	if kind == "ids" {
		return r.Account + "\x00" + r.Status.ID
	}
	return r.Key
}

func (s *toolStore) archived(kind, identity string) (toolRecord, bool, error) {
	path, err := s.ledgerPath(kind, identity, false)
	if errors.Is(err, os.ErrNotExist) {
		return toolRecord{}, false, nil
	}
	if err != nil {
		return toolRecord{}, false, err
	}
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return toolRecord{}, false, nil
	}
	if err != nil || !st.Mode().IsRegular() || st.Size() > sendLedgerLimit+65536 {
		return toolRecord{}, false, errors.New("发送归档不可读，未重新发送")
	}
	f, err := os.Open(path)
	if err != nil {
		return toolRecord{}, false, err
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		return toolRecord{}, false, errors.New("发送归档损坏，未重新发送")
	}
	b, err := io.ReadAll(io.LimitReader(z, sendLedgerLimit+1))
	z.Close()
	var a archivedSend
	if err != nil || len(b) > sendLedgerLimit || json.Unmarshal(b, &a) != nil || a.Version != 1 || !verifiedSend(a.Record) || sendIdentity(kind, a.Record) != identity {
		return toolRecord{}, false, errors.New("发送归档校验失败，未重新发送")
	}
	raw, _ := json.Marshal(a.Record)
	h := sha256.Sum256(raw)
	if a.Digest != hex.EncodeToString(h[:]) {
		return toolRecord{}, false, errors.New("发送归档校验失败，未重新发送")
	}
	a.Record.archived = true
	return a.Record, true, nil
}

func (s *toolStore) archiveSend(r toolRecord) error {
	if !verifiedSend(r) {
		return errors.New("仅可归档取得明确回执的发送")
	}
	raw, _ := json.Marshal(r)
	h := sha256.Sum256(raw)
	a := archivedSend{Version: 1, Digest: hex.EncodeToString(h[:]), Record: r}
	b, err := json.Marshal(a)
	if err != nil || len(b) > sendLedgerLimit {
		return errors.New("发送归档超过上限")
	}
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, err = z.Write(b); err != nil {
		return err
	}
	if err = z.Close(); err != nil {
		return err
	}
	// Both lookups must be durable and verified before the active copy can go.
	for _, kind := range []string{"requests", "ids"} {
		identity := sendIdentity(kind, r)
		old, exists, err := s.archived(kind, identity)
		if err != nil {
			return err
		}
		if exists {
			previous, _ := json.Marshal(old)
			if !bytes.Equal(previous, raw) {
				return errors.New("发送归档编号冲突，活跃记录保留")
			}
			continue
		}
		path, err := s.ledgerPath(kind, identity, true)
		if err != nil {
			return err
		}
		if err = writePrivate(path, compressed.Bytes()); err != nil {
			return err
		}
		if runtime.GOOS != "windows" {
			// Sync each newly created directory as well as its parent.
			root := filepath.Dir(s.path)
			for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
				d, e := os.Open(dir)
				if e != nil {
					return e
				}
				e = d.Sync()
				d.Close()
				if e != nil {
					return e
				}
				if dir == root {
					break
				}
			}
		}
		if _, exists, err = s.archived(kind, identity); err != nil || !exists {
			return errors.New("发送归档回读失败，活跃记录保留")
		}
	}
	return nil
}

// Compact a block only under capacity pressure. Uncertain/unfinished receipts
// stay hot, as do attachments (which may still be referenced by other tasks).
func (s *toolStore) compactSends(data *toolData, force bool) error {
	if len(data.Sends) < 1000 && !force {
		return nil
	}
	candidates := []toolRecord{}
	for key, r := range data.Sends {
		if key == r.Key && verifiedSend(r) {
			candidates = append(candidates, r)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Created.Equal(candidates[j].Created) {
			return candidates[i].Key < candidates[j].Key
		}
		return candidates[i].Created.Before(candidates[j].Created)
	})
	if len(candidates) == 0 {
		return errors.New("发送记录已满且没有可安全归档的已接受回执，请先核查未完成发送")
	}
	for _, r := range candidates[:min(128, len(candidates))] {
		if err := s.archiveSend(r); err != nil {
			return errors.New("发送归档失败，原记录保留，未提交新发送")
		}
		delete(data.Sends, r.Key)
	}
	return nil
}
