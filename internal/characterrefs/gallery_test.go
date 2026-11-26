package characterrefs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGalleryNoteAndUploadBoundary(t *testing.T) {
	home := t.TempDir()
	persona := filepath.Join(home, "PERSONA.md")
	image := filepath.Join(home, "character", "大肥鱼-mini可爱版.jpg")
	if note := Note(persona); note != "" {
		t.Fatalf("absent gallery advertised: %q", note)
	}
	if err := os.Mkdir(filepath.Dir(image), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(image, []byte("synthetic image"), 0600); err != nil {
		t.Fatal(err)
	}
	note := Note(persona)
	if !strings.Contains(note, filepath.Dir(image)) || !strings.Contains(note, "mini 可爱版") {
		t.Fatalf("gallery missing from role note: %q", note)
	}
	if got := Append(persona, "role", 32000); !strings.Contains(got, note) {
		t.Fatalf("role did not discover gallery: %q", got)
	}
	if got := Append(persona, "role", 4); got != "role" {
		t.Fatalf("context limit ignored: %q", got)
	}
	for path, want := range map[string]bool{
		image:                             true,
		filepath.Join(home, "PERSONA.md"): false,
		filepath.Join(home, "character", "..", "PERSONA.md"): false,
		filepath.Join(home, "private", "secret.jpg"):         false,
	} {
		if got := Contains(persona, path); got != want {
			t.Errorf("Contains(%q)=%v, want %v", path, got, want)
		}
	}
}
