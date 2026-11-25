package admin

import (
	"archive/zip"
	"bytes"
	"os"
	"strings"
	"testing"
	"wechat-bot/internal/deepseek"
)

// Explicit local opt-in, synthetic document only, no WeChat dispatch.
func TestLiveNativeDocumentInput(t *testing.T) {
	dir := os.Getenv("DEEPSEEK_DOCUMENT_LIVE_ADMIN")
	if dir == "" {
		t.Skip("opt-in live document API")
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := deepseek.NewWithAPIKey(deepseek.Config{BaseURL: cfg.BaseURL, Model: cfg.Model, MaxTokens: 256}, cfg.APIKey)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseIdleConnections()
	ctx, clear := deepseek.WithImageSession(t.Context())
	defer clear()
	if _, err = c.AttachDocument(ctx, "synthetic.txt", []byte("Validation number: 7319. Amount: 18."), false); err != nil {
		t.Fatal(err)
	}
	reply, err := c.ChatFunctions(ctx, "Read the attached synthetic file; return its validation number only.", []map[string]string{{"role": "user", "content": "What is the validation number?"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "7319") {
		t.Fatal("actual file content not recognized")
	}
	t.Log("actual Files API + model recognition passed; no WeChat sends")
	var doc bytes.Buffer
	z := zip.NewWriter(&doc)
	for name, body := range map[string]string{
		"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"_rels/.rels":         `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml":   `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Document validation number: 8427.</w:t></w:r></w:p><w:sectPr/></w:body></w:document>`,
	} {
		w, e := z.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		w.Write([]byte(body))
	}
	z.Close()
	if _, err = c.AttachDocument(ctx, "synthetic.docx", doc.Bytes(), true); err != nil {
		t.Fatal(err)
	}
	reply, err = c.ChatFunctions(ctx, "Read the attached DOCX; return its validation number only.", []map[string]string{{"role": "user", "content": "What number is in the DOCX?"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "8427") {
		t.Fatal("actual DOCX content not recognized")
	}
	t.Log("actual DOCX rich-layout upload + model recognition passed")
}
