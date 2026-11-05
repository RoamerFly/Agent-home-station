package pluginhost

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func zipFixture(t *testing.T, dir, prefix string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		w, err := z.Create(prefix + filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_, err = w.Write(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestArchiveUsesSnapshotApprovalWithoutActivation(t *testing.T) {
	for _, prefix := range []string{"", "包/"} {
		t.Run(prefix, func(t *testing.T) {
			s, err := OpenStore(t.TempDir(), "synthetic")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			source := readyFixture(t)
			directory, err := s.Prepare(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			archive := zipFixture(t, source, prefix)
			p, err := s.PrepareArchive(context.Background(), bytes.NewReader(archive))
			if err != nil {
				t.Fatal(err)
			}
			if p.Digest != directory.Digest {
				t.Fatal("different snapshot identity")
			}
			items, err := s.List(context.Background())
			if err != nil || len(items) != 0 {
				t.Fatal("preparation installed package")
			}
			clear(archive) // Approval no longer depends on submitted bytes.
			i, err := s.Install(context.Background(), p.Token, p.Digest, nil)
			if err != nil || i.Enabled {
				t.Fatalf("install %v %+v", err, i)
			}
			entries, _ := os.ReadDir(s.dir)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".upload-") || strings.HasPrefix(e.Name(), ".extract-") {
					t.Fatal("temporary files retained")
				}
			}
		})
	}
}

func TestArchiveRejectsTraversalAliasesLinksAndConflicts(t *testing.T) {
	cases := [][]string{{"../escape"}, {"/absolute"}, {"C:/escape"}, {"a\\b"}, {"a/../b"}, {"a:b"}, {"NUL.txt"}, {"a. "}, {"CON"}, {"COM¹.txt"}, {"a/x", "A/y"}, {"a", "a"}, {"A", "a"}, {"a", "a/b"}, {"a/", "a"}, {"link"}}
	for _, names := range cases {
		t.Run(strings.Join(names, ","), func(t *testing.T) {
			var b bytes.Buffer
			z := zip.NewWriter(&b)
			for _, name := range names {
				h := &zip.FileHeader{Name: name, Method: zip.Store}
				if name == "link" {
					h.SetMode(os.ModeSymlink | 0777)
				}
				w, err := z.CreateHeader(h)
				if err != nil {
					t.Fatal(err)
				}
				w.Write([]byte("x"))
			}
			z.Close()
			zr, err := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
			if err != nil {
				t.Fatal(err)
			}
			_, err = extractArchive(context.Background(), zr, t.TempDir())
			if err == nil || !(strings.Contains(err.Error(), "unsafe or repeated") || strings.Contains(err.Error(), "conflicting archive") || strings.Contains(err.Error(), "links and special")) {
				t.Fatalf("must reject unsafe paths before manifest validation: %v", err)
			}
		})
	}
}

func TestArchiveLimitsCancellationAndInvalidDigestLeaveNoApproval(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.PrepareArchive(ctx, strings.NewReader("zip")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = s.PrepareArchive(context.Background(), strings.NewReader("not zip")); err == nil {
		t.Fatal("invalid accepted")
	}
	z := &zip.Reader{File: []*zip.File{{FileHeader: zip.FileHeader{Name: "large", UncompressedSize64: MaxExpandedBytes + 1}}}}
	if _, err = extractArchive(context.Background(), z, t.TempDir()); !errors.Is(err, ErrArchiveLimit) {
		t.Fatal(err)
	}
	source := readyFixture(t)
	os.WriteFile(filepath.Join(source, "README.md"), []byte("bad digest"), 0600)
	if _, err = s.PrepareArchive(context.Background(), bytes.NewReader(zipFixture(t, source, ""))); err == nil {
		t.Fatal("tampered content accepted")
	}
	var count int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM preparations").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed import retained approval", err)
	}
}
