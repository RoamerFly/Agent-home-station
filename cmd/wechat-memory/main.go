// wechat-memory inspects, migrates and verifies private memory without exposing
// conversation bodies. Run mutating operations with the Web service stopped.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"wechat-bot/internal/companion"
)

func run() error {
	dir := flag.String("dir", "", "companion memory directory")
	action := flag.String("action", "inspect", "inspect|migrate|verify|backup|index")
	out := flag.String("out", "", "new SQLite backup path")
	scope := flag.String("scope", "", "scope ID for archive indexing")
	batches := flag.Int("batches", 20, "archive batches to process in this call")
	flag.Parse()
	if *dir == "" {
		return errors.New("--dir is required")
	}
	ctx := context.Background()
	print := func(v any) error { return json.NewEncoder(os.Stdout).Encode(v) }
	if *action == "inspect" || *action == "verify" {
		v, e := companion.InspectMemory(ctx, *dir)
		if e != nil {
			return e
		}
		return print(v)
	}
	if *action != "migrate" {
		if _, e := os.Stat(filepath.Join(*dir, "memory.sqlite")); e != nil {
			return errors.New("migrate the database first")
		}
	}
	s, e := companion.NewStore(*dir)
	if e != nil {
		return e
	}
	defer s.Close()
	switch *action {
	case "migrate":
		if e = s.Verify(ctx); e != nil {
			return e
		}
		v, e := companion.InspectMemory(ctx, *dir)
		if e != nil {
			return e
		}
		return print(v)
	case "backup":
		if *out == "" {
			return errors.New("--out is required")
		}
		if e = s.Backup(ctx, *out); e != nil {
			return e
		}
		return print(map[string]bool{"ok": true})
	case "index":
		if *scope == "" || *batches < 1 {
			return errors.New("--scope and positive --batches required")
		}
		done, e := s.IndexArchives(ctx, *scope, *batches)
		if e != nil {
			return e
		}
		return print(map[string]any{"complete": done, "scope": *scope})
	default:
		return errors.New("unknown memory action")
	}
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
