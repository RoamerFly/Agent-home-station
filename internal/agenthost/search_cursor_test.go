package agenthost

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func searchTestHost(t *testing.T) Host {
	t.Helper()
	c := Defaults()
	c.Mode, c.Roots = "read", []string{t.TempDir()}
	return Host{c}
}

func searchTestWrite(t *testing.T, h Host, rel, text string) string {
	t.Helper()
	path := filepath.Join(h.Config.Roots[0], filepath.FromSlash(rel))
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(text), 0600); e != nil {
		t.Fatal(e)
	}
	return path
}

func searchTestPage(t *testing.T, h Host, in Input) map[string]any {
	t.Helper()
	v, e := invoke(t, h, "server_search", in)
	if e != nil {
		t.Fatal(e)
	}
	return v.(map[string]any)
}

func TestSearchCursorEmptyPagesAdvanceWithoutRescanning(t *testing.T) {
	h := searchTestHost(t)
	for i := 0; i < 207; i++ {
		searchTestWrite(t, h, fmt.Sprintf("%03d.txt", i), "no match\n")
	}
	searchTestWrite(t, h, "999.txt", "needle")
	files := 0
	batch := 17
	in := Input{Path: h.Config.Roots[0], Content: "needle", MaxFiles: &batch}
	for page := 0; ; page++ {
		if page > 20 {
			t.Fatal("cursor did not advance")
		}
		r := searchTestPage(t, h, in)
		files += r["files_examined"].(int)
		if !r["has_more"].(bool) {
			if len(r["matches"].([]map[string]any)) != 1 || !r["complete"].(bool) {
				t.Fatal(r)
			}
			break
		}
		if r["stop_reason"] != "max_files" || r["files_examined"] != 17 {
			t.Fatal(r)
		}
		in.Cursor = r["next_cursor"].(string)
	}
	if files != 208 {
		t.Fatal("earlier files rescanned", files)
	}
}

func TestSearchCursorNestedOrderCRLFAndNoFinalNewline(t *testing.T) {
	h := searchTestHost(t)
	searchTestWrite(t, h, "a/z.txt", "skip\r\n中文 needle一\r\nneedle二")
	searchTestWrite(t, h, "a.txt", "needle三")
	searchTestWrite(t, h, "b/x.txt", "needle四\n")
	in := Input{Path: h.Config.Roots[0], Content: "needle", Limit: 1}
	var found []string
	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("cursor stuck")
		}
		r := searchTestPage(t, h, in)
		for _, m := range r["matches"].([]map[string]any) {
			file, _ := filepath.Rel(h.Config.Roots[0], m["path"].(string))
			found = append(found, fmt.Sprintf("%s:%d:%s", filepath.ToSlash(file), m["line"], m["text"]))
		}
		if !r["has_more"].(bool) {
			break
		}
		in.Cursor = r["next_cursor"].(string)
	}
	want := "a/z.txt:2:中文 needle一|a/z.txt:3:needle二|a.txt:1:needle三|b/x.txt:1:needle四"
	if strings.Join(found, "|") != want {
		t.Fatal(found)
	}
}

func TestSearchCursorBindsScopeFiltersQueryAndFileVersion(t *testing.T) {
	h := searchTestHost(t)
	file := searchTestWrite(t, h, "a.txt", "needle\nneedle\n")
	in := Input{Path: h.Config.Roots[0], Content: "needle", Limit: 1}
	r := searchTestPage(t, h, in)
	in.Cursor = r["next_cursor"].(string)
	other := in
	other.Content = "other"
	if _, e := invoke(t, h, "server_search", other); e == nil {
		t.Fatal("changed query accepted")
	}
	other = in
	none := []string{}
	other.ExcludeDirs = &none
	if _, e := invoke(t, h, "server_search", other); e == nil {
		t.Fatal("changed filter accepted")
	}
	other = in
	other.Offset = 1
	if _, e := invoke(t, h, "server_search", other); e == nil {
		t.Fatal("ambiguous offset accepted")
	}
	data, _ := base64.RawURLEncoding.DecodeString(in.Cursor)
	var forged searchCursor
	json.Unmarshal(data, &forged)
	forged.Path = "../outside.txt"
	data, _ = json.Marshal(forged)
	other = in
	other.Cursor = base64.RawURLEncoding.EncodeToString(data)
	if _, e := invoke(t, h, "server_search", other); e == nil {
		t.Fatal("escaping cursor accepted")
	}
	os.WriteFile(file, []byte("changed version"), 0600)
	if _, e := invoke(t, h, "server_search", in); e == nil {
		t.Fatal("mixed versions accepted")
	}
}

func TestSearchCursorByteBudgetDoesNotLosePendingMatch(t *testing.T) {
	h := searchTestHost(t)
	h.Config.OutputBytes = 1024
	searchTestWrite(t, h, "a.txt", strings.Repeat("needle "+strings.Repeat("长", 300)+"\n", 8))
	in := Input{Path: h.Config.Roots[0], Content: "needle", Limit: 1000}
	count := 0
	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("output-byte cursor stuck")
		}
		r := searchTestPage(t, h, in)
		for _, m := range r["matches"].([]map[string]any) {
			count++
			if m["line"] != count {
				t.Fatal("repeated or missing line", count, m)
			}
		}
		if r["effective_limit"] != 1000 {
			t.Fatal("explicit page parameter clipped")
		}
		if !r["has_more"].(bool) {
			break
		}
		if r["stop_reason"] != "output_bytes" {
			t.Fatal(r)
		}
		in.Cursor = r["next_cursor"].(string)
	}
	if count != 8 {
		t.Fatal(count)
	}
}

func TestSearchDefaultsExplainOmittedResourcesAndAllowOverride(t *testing.T) {
	h := searchTestHost(t)
	searchTestWrite(t, h, ".git/config", "needle")
	searchTestWrite(t, h, "runtimes/env/source.py", "needle")
	searchTestWrite(t, h, "projects/code.py", "needle")
	searchTestWrite(t, h, "weights.bin", "\x00needle"+strings.Repeat("x", 1<<20))
	in := Input{Path: h.Config.Roots[0], Content: "needle"}
	r := searchTestPage(t, h, in)
	if len(r["matches"].([]map[string]any)) != 1 || r["excluded_directories"] != 2 || r["skipped_binary_files"] != 1 || r["coverage_complete"] != false {
		t.Fatal(r)
	}
	none := []string{}
	in.ExcludeDirs = &none
	r = searchTestPage(t, h, in)
	if len(r["matches"].([]map[string]any)) != 3 || r["excluded_directories"] != 0 {
		t.Fatal(r)
	}
	// Naming the runtime explicitly is sufficient; its own name is not pruned.
	in = Input{Path: filepath.Join(h.Config.Roots[0], "runtimes"), Content: "needle"}
	r = searchTestPage(t, h, in)
	if len(r["matches"].([]map[string]any)) != 1 {
		t.Fatal(r)
	}
}

func TestSearchTimeSliceContinuesWithinFileAndPreservesCoverage(t *testing.T) {
	h := searchTestHost(t)
	searchTestWrite(t, h, "a.bin", "\x00")
	searchTestWrite(t, h, "b.txt", strings.Repeat("needle\r\n", 7))
	seconds := 0.000000001
	in := Input{Path: h.Config.Roots[0], Content: "needle", TimeoutSeconds: &seconds}
	count := 0
	for page := 0; ; page++ {
		if page > 15 {
			t.Fatal("time slice did not advance")
		}
		r := searchTestPage(t, h, in)
		count += len(r["matches"].([]map[string]any))
		if !r["has_more"].(bool) {
			if count != 7 || r["skipped_binary_files"] != 1 || r["complete"] != false {
				t.Fatal(count, r)
			}
			break
		}
		if r["stop_reason"] != "time_slice" {
			t.Fatal(r)
		}
		in.Cursor = r["next_cursor"].(string)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	args, _ := json.Marshal(in)
	if _, e := h.Call(ctx, "server_search", args); e != context.Canceled {
		t.Fatal(e)
	}
}

// Explicit opt-in, read-only acceptance against the managed public tree. No
// private configuration, credentials, conversation data or network is used.
func TestLiveManagedSearchPages(t *testing.T) {
	path := os.Getenv("WECHAT_LIVE_PUBLIC_WORKSPACE")
	if path == "" {
		t.Skip("set WECHAT_LIVE_PUBLIC_WORKSPACE for read-only local acceptance")
	}
	st, e := os.Lstat(path)
	if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || filepath.Base(path) != "public_workspace" {
		t.Fatal("expected real managed public_workspace directory")
	}
	c := Defaults()
	c.Mode = "read"
	c.Roots = []string{path}
	h := Host{c}
	batch := 64
	in := Input{Path: path, Content: "wechat-read-only-pagination-acceptance-no-match-284c7", MaxFiles: &batch}
	started := time.Now()
	for i := 0; i < 3; i++ {
		r := searchTestPage(t, h, in)
		if r["files_examined"].(int) > batch {
			t.Fatal("batch parameter ignored")
		}
		t.Logf("page=%d files=%v stop=%v elapsed_ms=%v skipped=%v excluded=%v", i+1, r["files_examined"], r["stop_reason"], r["elapsed_ms"], r["skipped_files"], r["excluded_directories"])
		if !r["has_more"].(bool) {
			break
		}
		cursor := r["next_cursor"].(string)
		if cursor == "" || cursor == in.Cursor {
			t.Fatal("continuation did not advance")
		}
		in.Cursor = cursor
	}
	t.Logf("read-only managed-tree check finished in %s; no WeChat sends", time.Since(started))
}
