package characterrefs

import (
	"os"
	"path/filepath"
	"strings"
)

// Directory is the fish's shared visual reference directory beside PERSONA.md.
func Directory(personaPath string) string {
	return filepath.Join(filepath.Dir(personaPath), "character")
}

// Note is small enough for the role context; image bytes stay on disk and are
// only read when a creation task actually needs a reference.
func Note(personaPath string) string {
	root := Directory(personaPath)
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ""
	}
	names := make([]string, 0, 3)
	for _, item := range []struct{ name, label string }{
		{"大肥鱼-mini可爱版.jpg", "mini 可爱版"},
		{"DeepSeek-Q版", "Q 版"},
		{"DeepSeek-原版", "原版"},
	} {
		entry, err := os.Lstat(filepath.Join(root, item.name))
		if err == nil && entry.Mode()&os.ModeSymlink == 0 && (entry.Mode().IsRegular() || entry.IsDir()) {
			names = append(names, item.label)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "\n\n我的形象参考图在 " + root + "（" + strings.Join(names, "、") + "）；需要画我或制作视频时按需读取参考图。"
}

// Append keeps the role document within the existing context size limit.
func Append(personaPath, content string, maxBytes int) string {
	note := Note(personaPath)
	if len(content)+len(note) > maxBytes {
		return content
	}
	return content + note
}

// Contains reports whether an absolute path is inside the shared reference
// directory. Callers open the selected root through os.OpenRoot afterward.
func Contains(personaPath, sourcePath string) bool {
	if !filepath.IsAbs(sourcePath) {
		return false
	}
	root := Directory(personaPath)
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	rel, err := filepath.Rel(root, sourcePath)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
