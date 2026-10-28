package admin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type interruptedStream struct{ remaining int }

func (r *interruptedStream) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errors.New("synthetic copy interruption")
	}
	n := min(len(p), r.remaining)
	clear(p[:n])
	r.remaining -= n
	return n, nil
}
func TestVerifiedStreamPublishesOnlyCompleteBytesAndPreservesConflicts(t *testing.T) {
	dir := t.TempDir()
	root, e := os.OpenRoot(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	data := bytes.Repeat([]byte("synthetic original\n"), 2048)
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	for _, src := range []io.Reader{&interruptedStream{512}, bytes.NewReader(data[:512]), bytes.NewReader(append(append([]byte{}, data...), 1))} {
		if e = publishVerifiedStream(root, "original.bin", src, int64(len(data)), hash); e == nil {
			t.Fatal("incomplete stream accepted")
		}
		if _, e = root.Stat("original.bin"); !os.IsNotExist(e) {
			t.Fatal("partial final file exposed", e)
		}
		files, _ := os.ReadDir(dir)
		if len(files) != 0 {
			t.Fatal("pending file leaked", files)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := publishVerifiedStream(root, "original.bin", bytes.NewReader(data), int64(len(data)), hash); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	got, e := os.ReadFile(filepath.Join(dir, "original.bin"))
	if e != nil || !bytes.Equal(got, data) {
		t.Fatal("concurrent publish changed bytes", e)
	}
	if e = os.WriteFile(filepath.Join(dir, "original.bin"), []byte("preserve this modified file"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = publishVerifiedStream(root, "original.bin", bytes.NewReader(data), int64(len(data)), hash); e == nil {
		t.Fatal("modified existing file silently accepted")
	}
	got, _ = os.ReadFile(filepath.Join(dir, "original.bin"))
	if string(got) != "preserve this modified file" {
		t.Fatal("conflict overwritten")
	}
}

func TestReceivedFileRejectsDamagedInboxAndRetainsOriginalCache(t *testing.T) {
	f, _, n := forwardFixture(t)
	text, e := (mediaUnderstanding{manager: f.manager}).receiveFile(t.Context(), n.message, "notes.txt")
	if e != nil || text == "" {
		t.Fatal(text, e)
	}
	assets, e := f.manager.loadAssets()
	if e != nil || len(assets) != 1 {
		t.Fatal(e)
	}
	path := filepath.Join(f.manager.managedRoot(), "pri_workspace", accountDirectory(n.message.AccountID), "conversations", assets[0].Scope, "inbox", agentID(n.message.ID)[:24]+".txt")
	if e = os.WriteFile(path, []byte("edited inbox"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = (mediaUnderstanding{manager: f.manager}).receiveFile(t.Context(), n.message, "notes.txt"); e == nil {
		t.Fatal("damaged inbox accepted")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "edited inbox" {
		t.Fatal("edited inbox replaced")
	}
	original, e := f.manager.cachedAssetData(assets[0])
	if e != nil || string(original) != "synthetic attachment bytes" {
		t.Fatal("inbox edit altered cached original", e)
	}
}
