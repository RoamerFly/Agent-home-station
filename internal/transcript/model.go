package transcript

import (
	"context"
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode"
)

//go:embed punctuate.py
var runner string

// A formatter process lives for one request. Limit concurrent model loads.
var modelSlot = make(chan struct{}, 1)

// Format returns the actual formatting method along with the text. The model
// and interpreter are installed by the administrator, outside writable workspaces.
func Format(ctx context.Context, raw string) (string, string) {
	exe, err := os.Executable()
	if err == nil && strings.IndexFunc(raw, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 {
		root := filepath.Join(filepath.Dir(exe), "punctuation")
		if _, err := os.Stat(filepath.Join(root, "model.int8.onnx")); err == nil {
			if text, ok := formatModel(ctx, raw, root); ok {
				return text, "ct_punc_int8"
			}
		}
	}
	return Readable(raw), "local_readability_v1"
}

func formatModel(ctx context.Context, raw, root string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	select {
	case modelSlot <- struct{}{}:
		defer func() { <-modelSlot }()
	case <-ctx.Done():
		return "", false
	}
	python := filepath.Join(root, "bin", "python")
	if runtime.GOOS == "windows" {
		python = filepath.Join(filepath.Dir(filepath.Dir(root)), "windows-python-cli", "python.exe")
	}
	cmd := exec.CommandContext(ctx, python, "-I", "-B", "-S", "-c", runner, root)
	hideWindow(cmd)
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(raw)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return preserveWords(raw, strings.TrimSpace(string(out)))
}

var latinTokens = regexp.MustCompile(`[[:alnum:]]+([.][[:digit:]]+)*`)

// Accept inserted clause marks and spacing only. Protect decimal points,
// existing quotes, spelling and numbers even if a provider normalizes them.
func preserveWords(raw, formatted string) (string, bool) {
	if !slices.Equal(latinTokens.FindAllString(raw, -1), latinTokens.FindAllString(formatted, -1)) {
		return "", false
	}
	source := []rune(strings.Join(strings.Fields(raw), ""))
	var result strings.Builder
	i := 0
	for _, r := range formatted {
		if unicode.IsSpace(r) {
			result.WriteRune(r)
			continue
		}
		if i < len(source) && source[i] == r {
			result.WriteRune(r)
			i++
			continue
		}
		if !strings.ContainsRune("，。！？、：；,.!?", r) {
			return "", false
		}
		if (i == 0 || !unicode.IsPunct(source[i-1])) && (i >= len(source) || !unicode.IsPunct(source[i])) {
			result.WriteRune(r)
		}
	}
	return result.String(), i == len(source) && i != 0
}
