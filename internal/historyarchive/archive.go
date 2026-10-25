// Package historyarchive stores immutable, compressed local history batches.
// It does not restore executable state or remove original data.
package historyarchive

import (
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
	"strings"
	"time"
)

const MaxPayload = 8 << 20

type Store struct{ Root string }
type Info struct {
	ID      string    `json:"id"`
	Bytes   int64     `json:"bytes"`
	Created time.Time `json:"created"`
}
type Batch struct {
	Version int             `json:"version"`
	Kind    string          `json:"kind"`
	Scope   string          `json:"scope"`
	Data    json.RawMessage `json:"data"`
}

func validID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 32 && id == strings.ToLower(id)
}
func (s Store) directory(kind, scope string, create bool) (string, error) {
	if kind != "memory" && kind != "activity" || !validID(scope) {
		return "", errors.New("invalid archive scope")
	}
	paths := []string{s.Root, filepath.Join(s.Root, kind), filepath.Join(s.Root, kind, scope)}
	for _, path := range paths {
		if create {
			if err := os.MkdirAll(path, 0700); err != nil {
				return "", err
			}
		}
		st, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("unsafe archive directory")
		}
	}
	return paths[2], nil
}

// Put is content addressed. Retrying a failed active-state commit does not
// create another copy of the same archived batch.
func (s Store) Put(kind, scope string, value any) (Info, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return Info{}, err
	}
	b, err := json.Marshal(Batch{Version: 1, Kind: kind, Scope: scope, Data: data})
	if err != nil || len(b) > MaxPayload {
		return Info{}, errors.New("archive batch exceeds limit")
	}
	sum := sha256.Sum256(b)
	id := hex.EncodeToString(sum[:])
	dir, err := s.directory(kind, scope, true)
	if err != nil {
		return Info{}, err
	}
	path := filepath.Join(dir, id+".json.gz")
	if _, err := os.Lstat(path); err == nil {
		_, info, err := s.Read(kind, scope, id)
		return info, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return Info{}, err
	}
	f, err := os.CreateTemp(dir, ".archive-*")
	if err != nil {
		return Info{}, err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		z := gzip.NewWriter(f)
		_, err = z.Write(b)
		closeErr := z.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return Info{}, err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return Info{}, err
	}
	if runtime.GOOS != "windows" {
		d, e := os.Open(dir)
		if e != nil {
			return Info{}, e
		}
		err = d.Sync()
		d.Close()
		if err != nil {
			return Info{}, err
		}
	}
	// Read back and verify before a caller is allowed to discard its active copy.
	_, info, err := s.Read(kind, scope, id)
	return info, err
}
func (s Store) Read(kind, scope, id string) (Batch, Info, error) {
	var out Batch
	if !validID(id) {
		return out, Info{}, errors.New("invalid archive id")
	}
	dir, err := s.directory(kind, scope, false)
	if err != nil {
		return out, Info{}, err
	}
	path := filepath.Join(dir, id+".json.gz")
	st, err := os.Lstat(path)
	if err != nil {
		return out, Info{}, err
	}
	if !st.Mode().IsRegular() || st.Size() > MaxPayload+65536 {
		return out, Info{}, errors.New("invalid archive file")
	}
	f, err := os.Open(path)
	if err != nil {
		return out, Info{}, err
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		return out, Info{}, err
	}
	b, err := io.ReadAll(io.LimitReader(z, MaxPayload+1))
	z.Close()
	if err != nil || len(b) > MaxPayload {
		return out, Info{}, errors.New("archive decompression failed")
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != id || json.Unmarshal(b, &out) != nil || out.Version != 1 || out.Kind != kind || out.Scope != scope || !json.Valid(out.Data) {
		return Batch{}, Info{}, errors.New("archive integrity check failed")
	}
	return out, Info{ID: id, Bytes: st.Size(), Created: st.ModTime().UTC()}, nil
}
func (s Store) List(kind, scope, after string, limit int) ([]Info, string, error) {
	if limit < 1 || limit > 50 || after != "" && !validID(after) {
		return nil, "", errors.New("invalid archive page")
	}
	dir, err := s.directory(kind, scope, false)
	if errors.Is(err, os.ErrNotExist) {
		return []Info{}, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", err
	}
	items := []Info{}
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".json.gz")
		if id == entry.Name() || !validID(id) {
			continue
		}
		st, e := entry.Info()
		if e != nil {
			return nil, "", e
		}
		if !st.Mode().IsRegular() {
			return nil, "", errors.New("unsafe archive entry")
		}
		items = append(items, Info{ID: id, Bytes: st.Size(), Created: st.ModTime().UTC()})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Created.Equal(items[j].Created) {
			return items[i].ID > items[j].ID
		}
		return items[i].Created.After(items[j].Created)
	})
	start := 0
	if after != "" {
		found := false
		for i, item := range items {
			if item.ID == after {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return nil, "", errors.New("archive cursor no longer exists")
		}
	}
	end := min(start+limit, len(items))
	next := ""
	if end < len(items) {
		next = items[end-1].ID
	}
	return items[start:end], next, nil
}
