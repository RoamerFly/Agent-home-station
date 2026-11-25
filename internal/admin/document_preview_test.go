package admin

import (
	"archive/zip"
	"bytes"
	"testing"
)

func TestDocumentPreviewRejectsActiveRelationships(t *testing.T) {
	for _, tc := range []struct {
		name, extra, body string
		ok                bool
	}{
		{"plain", "", "", true}, {"macro", "word/vbaProject.bin", "macro", false}, {"external", "word/_rels/document.xml.rels", `<Relationship TargetMode="External"/>`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			z := zip.NewWriter(&b)
			f, _ := z.Create("word/document.xml")
			f.Write([]byte("<document/>"))
			if tc.extra != "" {
				f, _ = z.Create(tc.extra)
				f.Write([]byte(tc.body))
			}
			z.Close()
			if err := validatePreviewDocument(b.Bytes()); (err == nil) != tc.ok {
				t.Fatalf("unexpected validation %v", err)
			}
		})
	}
}
