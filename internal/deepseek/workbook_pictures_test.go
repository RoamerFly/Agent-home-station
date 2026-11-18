package deepseek

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"testing"
)

func TestWorkbookPicturesResolvesAnchorRelationships(t *testing.T) {
	image, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jvJkAAAAASUVORK5CYII=")
	var buf bytes.Buffer
	archive := zip.NewWriter(&buf)
	files := map[string]string{
		"xl/workbook.xml":                     `<workbook xmlns:r="urn:rels"><sheets><sheet name="装箱单" sheetId="99" r:id="sheetRef"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels":          `<Relationships><Relationship Id="sheetRef" Target="/xl/worksheets/custom.xml"/></Relationships>`,
		"xl/worksheets/custom.xml":            `<worksheet xmlns:r="urn:rels"><drawing r:id="drawingRef"/></worksheet>`,
		"xl/worksheets/_rels/custom.xml.rels": `<Relationships><Relationship Id="drawingRef" Target="../drawings/custom.xml"/></Relationships>`,
		"xl/drawings/custom.xml":              `<wsDr xmlns:r="urn:rels"><twoCellAnchor><from><col>26</col><row>7</row></from><pic><blipFill><blip r:embed="photo"/></blipFill></pic></twoCellAnchor></wsDr>`,
		"xl/drawings/_rels/custom.xml.rels":   `<Relationships><Relationship Id="photo" Target="../media/item.png"/></Relationships>`,
		"xl/media/item.png":                   string(image)}
	for name, body := range files {
		f, e := archive.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.Write([]byte(body)); e != nil {
			t.Fatal(e)
		}
	}
	if e := archive.Close(); e != nil {
		t.Fatal(e)
	}
	pics, e := workbookPictures(buf.Bytes())
	if e != nil {
		t.Fatal(e)
	}
	if len(pics) != 1 || pics[0].label != "装箱单!AA8 (xl/media/item.png)" || !bytes.Equal(pics[0].data, image) {
		t.Fatal("incorrect sheet, anchor or embedded image")
	}
}
