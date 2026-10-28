package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Publish a complete verified copy without replacing an existing file. A hard
// link atomically exposes the staged inode; the stage is a copy, never a link
// to the original asset, so editing an inbox cannot alter the cached original.
func publishVerifiedStream(root *os.Root, name string, src io.Reader, size int64, hash string) error {
	verify := func() error {
		f, e := root.Open(name)
		if e != nil {
			return e
		}
		defer f.Close()
		info, e := f.Stat()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return errors.New("existing_file_integrity_failed")
		}
		h := sha256.New()
		n, e := io.Copy(h, io.LimitReader(f, size+1))
		if e != nil || n != size || hex.EncodeToString(h.Sum(nil)) != hash {
			return errors.New("existing_file_integrity_failed")
		}
		return nil
	}
	if e := verify(); e == nil {
		return nil
	} else if !os.IsNotExist(e) {
		return e
	}
	temp := filepath.Join(filepath.Dir(name), ".incoming-pending-"+rand.Text())
	f, e := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer root.Remove(temp)
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(src, size+1))
	if e == nil && (n != size || hex.EncodeToString(h.Sum(nil)) != hash) {
		e = errors.New("incoming_file_integrity_failed")
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = root.Link(temp, name); e != nil {
		if os.IsExist(e) {
			return verify()
		}
		return e
	}
	return nil
}
