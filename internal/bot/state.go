package bot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// Checkpoints contain progress and deduplication hashes, never chat bodies or
// model answers. Claims are saved before external calls; interrupted claims
// are deliberately not replayed.
type checkpoint struct {
	Version  int      `json:"version"`
	ServerID string   `json:"server_id"`
	Account  string   `json:"account"`
	Cursor   string   `json:"cursor"`
	Seen     []string `json:"seen_message_hashes,omitempty"`
}

func loadCheckpoint(path, server, account string) (checkpoint, bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return checkpoint{Version: 1, ServerID: server, Account: account, Cursor: "0"}, false, nil
	}
	if err != nil {
		return checkpoint{}, false, errors.New("bot checkpoint unavailable")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (128<<10)+1))
	var c checkpoint
	if err != nil || len(b) > 128<<10 || json.Unmarshal(b, &c) != nil || c.Version != 1 || c.ServerID != server || c.Account != account || len(c.Seen) > 1024 {
		return checkpoint{}, false, errors.New("bot checkpoint invalid or belongs to another daemon/account")
	}
	n, err := strconv.ParseInt(c.Cursor, 10, 64)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != c.Cursor {
		return checkpoint{}, false, errors.New("invalid bot checkpoint cursor")
	}
	for _, hash := range c.Seen {
		if decoded, err := hex.DecodeString(hash); err != nil || len(decoded) != 32 {
			return checkpoint{}, false, errors.New("invalid bot checkpoint deduplication hash")
		}
	}
	return c, true, nil
}

func saveCheckpoint(path string, c checkpoint) error {
	b, err := json.Marshal(c)
	if err != nil {
		return errors.New("cannot encode bot checkpoint")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".checkpoint-*")
	if err != nil {
		return errors.New("cannot create bot checkpoint")
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("cannot save bot checkpoint")
	}
	if os.Rename(f.Name(), path) != nil {
		return errors.New("cannot replace bot checkpoint")
	}
	return syncDirectory(filepath.Dir(path))
}

func messageHash(id string) string { h := sha256.Sum256([]byte(id)); return hex.EncodeToString(h[:]) }
func (c *checkpoint) contains(id string) bool {
	hash := messageHash(id)
	for _, v := range c.Seen {
		if v == hash {
			return true
		}
	}
	return false
}
func (c *checkpoint) remember(id string) {
	c.Seen = append(c.Seen, messageHash(id))
	if len(c.Seen) > 1024 {
		c.Seen = c.Seen[len(c.Seen)-1024:]
	}
}
