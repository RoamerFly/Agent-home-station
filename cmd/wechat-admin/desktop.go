package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Only the explicitly selected desktop mode reads this inherited private pipe.
// The desktop drains first, then writes stop. EOF also prevents an orphan Web
// service if its owning desktop process disappears. No public stop API is added.
func desktopStop(input io.Reader) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(input)
		for scanner.Scan() {
			if scanner.Text() == "stop" {
				return
			}
		}
	}()
	return done
}

func nativePending(root string) (int, error) {
	if !filepath.IsAbs(root) {
		return 0, errors.New("absolute native root required")
	}
	accounts := filepath.Join(root, "accounts")
	entries, err := os.ReadDir(accounts)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	total := 0
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return 0, errors.New("linked account directory refused")
		}
		if !entry.IsDir() {
			continue
		}
		for _, dir := range []string{accounts, filepath.Join(accounts, entry.Name()), filepath.Join(accounts, entry.Name(), "agent")} {
			st, err := os.Lstat(dir)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil || st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
				return 0, errors.New("linked native control directory refused")
			}
		}
		path := filepath.Join(accounts, entry.Name(), "agent", "control.sqlite")
		st, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
			return 0, errors.New("native control database unavailable")
		}
		uri := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
		if filepath.VolumeName(path) != "" {
			uri.Path = "/" + uri.Path
		}
		uri.RawQuery = "mode=ro"
		db, err := sql.Open("sqlite", uri.String())
		if err != nil {
			return 0, err
		}
		var count int
		err = db.QueryRow("SELECT count(*) FROM media_jobs WHERE state IN ('queued','running')").Scan(&count)
		closeErr := db.Close()
		if err != nil {
			return 0, err
		}
		if closeErr != nil {
			return 0, closeErr
		}
		total += count
	}
	return total, nil
}

func printDesktopBusy(root string) error {
	count, err := nativePending(root)
	if err != nil {
		return err
	}
	data, err := json.Marshal(map[string]int{"pending": count})
	if err == nil {
		_, err = fmt.Fprintln(os.Stdout, string(data))
	}
	return err
}
