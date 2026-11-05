// wechat-memory inspects, migrates and verifies private memory without exposing
// conversation bodies. Run mutating operations with the Web service stopped.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"wechat-bot/internal/companion"
)

func run() error {
	dir := flag.String("dir", "", "companion memory directory")
	action := flag.String("action", "inspect", "inspect|dry-run|migrate|verify|backup|index|source-audit|quarantine")
	review := flag.String("review", "", "reviewed source-conflict JSON file, with exact revisions and hashes")
	out := flag.String("out", "", "new SQLite backup path")
	scope := flag.String("scope", "", "scope ID for archive indexing")
	allScopes := flag.Bool("all-scopes", false, "index all scopes, reporting aggregate progress only")
	batches := flag.Int("batches", 20, "archive batches to process in this call")
	flag.Parse()
	if *dir == "" {
		return errors.New("--dir is required")
	}
	ctx := context.Background()
	print := func(v any) error { return json.NewEncoder(os.Stdout).Encode(v) }
	if *action == "source-audit" {
		items, err := companion.AuditSources(ctx, *dir)
		if err != nil {
			return err
		}
		return print(map[string]any{"read_only": true, "items": items, "content_included": false})
	}
	if *action == "dry-run" {
		v, e := companion.DryRunMemory(ctx, *dir)
		if e != nil {
			return e
		}
		return print(v)
	}
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
	var s *companion.Store
	var e error
	if *action == "quarantine" || *action == "index" || *action == "migrate" {
		s, e = companion.NewOfflineStore(*dir)
	} else {
		s, e = companion.NewStore(*dir)
	}
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
	case "quarantine":
		if *review == "" {
			return errors.New("--review is required; originals are preserved")
		}
		file, err := os.Open(*review)
		if err != nil {
			return err
		}
		defer file.Close()
		decoder := json.NewDecoder(io.LimitReader(file, 4<<20))
		decoder.DisallowUnknownFields()
		var items []companion.SourceAudit
		if err = decoder.Decode(&items); err != nil {
			return err
		}
		if decoder.Decode(new(any)) != io.EOF || len(items) == 0 {
			return errors.New("one nonempty source review array required")
		}
		if err = s.QuarantineSources(ctx, items); err != nil {
			return err
		}
		if err = s.Verify(ctx); err != nil {
			return err
		}
		return print(map[string]any{"state": "quarantined", "count": len(items), "originals_preserved": true, "wechat_sends": 0})
	case "index":
		if *batches < 1 || (*scope == "" && !*allScopes) || (*scope != "" && *allScopes) {
			return errors.New("one of --scope or --all-scopes and positive --batches required")
		}
		if *allScopes {
			complete, total := 0, 0
			for _, item := range s.ScopeIdentities() {
				done, err := s.IndexArchives(ctx, item.ID, *batches)
				if err != nil {
					return err
				}
				total++
				if done {
					complete++
				}
			}
			return print(map[string]any{"complete": complete == total, "scopes": total, "complete_scopes": complete, "batches_per_scope": *batches})
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
