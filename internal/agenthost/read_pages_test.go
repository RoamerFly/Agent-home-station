package agenthost

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadToolsContinueBeyondFormerTotalCeilings(t *testing.T) {
	c := Defaults()
	c.Mode = "read"
	c.Roots = []string{t.TempDir()}
	h := Host{c}
	text := strings.Repeat("中文正文", 110000)
	file := filepath.Join(c.Roots[0], "large.txt")
	os.WriteFile(file, []byte(text), 0600)
	var hash string
	for _, offset := range []int{0, 1050000, len(text) - 12} {
		value, e := invoke(t, h, "server_read", Input{Path: file, Offset: offset, Length: 1024})
		if e != nil {
			t.Fatal(e)
		}
		row := value.(map[string]any)
		content := row["content"].(string)
		if !utf8.ValidString(content) || content == "" || row["next_offset"].(int64) <= int64(offset) {
			t.Fatal("invalid continuation", row)
		}
		if hash != "" && hash != row["sha256"] {
			t.Fatal("hash changed across pages")
		}
		hash = row["sha256"].(string)
	}
	os.WriteFile(file, []byte("changed"), 0600)
	if _, e := invoke(t, h, "server_read", Input{Path: file, Expected: hash}); e == nil {
		t.Fatal("mixed file versions")
	}
	for i := 0; i < 215; i++ {
		os.WriteFile(filepath.Join(c.Roots[0], fmt.Sprintf("item-%04d.txt", i)), []byte("needle"), 0600)
	}
	offset, total := 0, 0
	for {
		v, e := invoke(t, h, "server_list", Input{Path: c.Roots[0], Offset: offset, Limit: 37})
		if e != nil {
			t.Fatal(e)
		}
		r := v.(map[string]any)
		total += len(r["items"].([]map[string]any))
		if r["complete"].(bool) {
			break
		}
		next := r["next_offset"].(int)
		if next <= offset {
			t.Fatal("directory did not advance")
		}
		offset = next
	}
	if total != 216 {
		t.Fatal("directory truncated", total)
	}
	offset, total = 0, 0
	for {
		v, e := invoke(t, h, "server_search", Input{Path: c.Roots[0], Content: "needle", Offset: offset, Limit: 31})
		if e != nil {
			t.Fatal(e)
		}
		r := v.(map[string]any)
		total += len(r["matches"].([]map[string]any))
		if !r["has_more"].(bool) {
			break
		}
		offset = r["next_offset"].(int)
	}
	if total != 215 {
		t.Fatal("search truncated", total)
	}
}

func TestExplicitReadLengthCanExceedDefaultAndExplainsTransportSplit(t *testing.T) {
	c := Defaults()
	c.Mode = "read"
	c.Roots = []string{t.TempDir()}
	h := Host{c}
	path := filepath.Join(c.Roots[0], "long.txt")
	os.WriteFile(path, []byte(strings.Repeat("a", 200000)), 0600)
	v, e := invoke(t, h, "server_read", Input{Path: path, Length: 40000})
	if e != nil {
		t.Fatal(e)
	}
	if len(v.(map[string]any)["content"].(string)) != 40000 {
		t.Fatal("explicit length clipped to default")
	}
	v, e = invoke(t, h, "server_read", Input{Path: path, Length: 1 << 20})
	if e != nil {
		t.Fatal(e)
	}
	r := v.(map[string]any)
	if r["length_adjustment_reason"] == nil || !r["truncated"].(bool) || r["next_offset"].(int64) <= 40000 {
		t.Fatal("transport continuation missing", r)
	}
}

func TestSearchReadsPastFormerFileSizeAndScanCount(t *testing.T) {
	c := Defaults()
	c.Mode = "read"
	c.Roots = []string{t.TempDir()}
	h := Host{c}
	for i := 0; i < 2002; i++ {
		os.WriteFile(filepath.Join(c.Roots[0], fmt.Sprintf("a-%04d", i)), nil, 0600)
	}
	file := filepath.Join(c.Roots[0], "z-large.txt")
	os.WriteFile(file, []byte(strings.Repeat("line\n", 220000)+"late-match\n"), 0600)
	unlimited := 0
	unlimitedTime := 0.0
	v, e := invoke(t, h, "server_search", Input{Path: c.Roots[0], Content: "late-match", MaxFiles: &unlimited, TimeoutSeconds: &unlimitedTime})
	if e != nil {
		t.Fatal(e)
	}
	r := v.(map[string]any)
	if len(r["matches"].([]map[string]any)) != 1 || r["files_examined"].(int) != 2003 {
		t.Fatal("old scan ceiling retained", r)
	}
}
