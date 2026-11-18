package deepseek

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
)

// Follow internal OOXML relationships. Archive names never become filesystem paths.
type workbookParts struct {
	parts     map[string]*zip.File
	remaining int64
}

func (w *workbookParts) read(name string) ([]byte, error) {
	f := w.parts[name]
	if f == nil {
		return nil, fmt.Errorf("工作簿缺少部件 %s", name)
	}
	if f.UncompressedSize64 > uint64(w.remaining) {
		return nil, fmt.Errorf("工作簿展开超过本次布局读取内存预算，可按工作表拆分读取")
	}
	r, e := f.Open()
	if e != nil {
		return nil, e
	}
	defer r.Close()
	b, e := io.ReadAll(io.LimitReader(r, w.remaining+1))
	if int64(len(b)) > w.remaining {
		return nil, fmt.Errorf("工作簿布局超出内存预算")
	}
	w.remaining -= int64(len(b))
	return b, e
}
func (w *workbookParts) decode(name string, out any) error {
	b, e := w.read(name)
	if e != nil {
		return e
	}
	return xml.Unmarshal(b, out)
}
func (w *workbookParts) relationships(name string) (map[string]string, error) {
	var rs struct {
		List []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
			Mode   string `xml:"TargetMode,attr"`
		} `xml:"Relationship"`
	}
	err := w.decode(path.Join(path.Dir(name), "_rels", path.Base(name)+".rels"), &rs)
	dst := map[string]string{}
	for _, r := range rs.List {
		if r.Mode == "External" {
			continue
		}
		p := path.Join(path.Dir(name), r.Target)
		if strings.HasPrefix(r.Target, "/") {
			p = path.Clean(strings.TrimPrefix(r.Target, "/"))
		}
		if strings.HasPrefix(p, "xl/") {
			dst[r.ID] = p
		}
	}
	return dst, err
}

type pictureAnchor struct {
	From struct {
		Row int `xml:"row"`
		Col int `xml:"col"`
	} `xml:"from"`
	Pic struct {
		Blip struct {
			ID string `xml:"embed,attr"`
		} `xml:"blipFill>blip"`
	} `xml:"pic"`
}

func excelColumn(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('A'+(i-1)%26)) + s
	}
	return s
}

type workbookPicture struct {
	label string
	data  []byte
}

// workbookPictures supplements the provider's text extraction with actual
// floating pictures and their sheet coordinates for layout-sensitive requests.
func workbookPictures(data []byte) ([]workbookPicture, error) {
	z, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if e != nil {
		return nil, e
	}
	w := workbookParts{map[string]*zip.File{}, 128 << 20}
	for _, f := range z.File {
		w.parts[f.Name] = f
	}
	const book = "xl/workbook.xml"
	var wb struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			ID   string `xml:"id,attr"`
		} `xml:"sheets>sheet"`
	}
	if e = w.decode(book, &wb); e != nil {
		return nil, e
	}
	sheets, e := w.relationships(book)
	if e != nil {
		return nil, e
	}
	var images []workbookPicture
	for _, s := range wb.Sheets {
		file := sheets[s.ID]
		if file == "" {
			continue
		}
		var sh struct {
			Drawings []struct {
				ID string `xml:"id,attr"`
			} `xml:"drawing"`
		}
		if e = w.decode(file, &sh); e != nil {
			return nil, e
		}
		if len(sh.Drawings) == 0 {
			continue
		}
		ds, e := w.relationships(file)
		if e != nil {
			return nil, e
		}
		for _, d := range sh.Drawings {
			drawing := ds[d.ID]
			if drawing == "" {
				continue
			}
			var doc struct {
				One []pictureAnchor `xml:"oneCellAnchor"`
				Two []pictureAnchor `xml:"twoCellAnchor"`
				Abs []pictureAnchor `xml:"absoluteAnchor"`
			}
			if e = w.decode(drawing, &doc); e != nil {
				return nil, e
			}
			if len(doc.One)+len(doc.Two)+len(doc.Abs) == 0 {
				continue
			}
			relfile := path.Join(path.Dir(drawing), "_rels", path.Base(drawing)+".rels")
			if w.parts[relfile] == nil {
				continue
			}
			targets, e := w.relationships(drawing)
			if e != nil {
				return nil, e
			}
			anchored := append(doc.One, doc.Two...)
			for i, a := range append(anchored, doc.Abs...) {
				media := targets[a.Pic.Blip.ID]
				if media == "" {
					continue
				}
				b, e := w.read(media)
				if e != nil {
					return nil, e
				}
				position := "绝对定位"
				if i < len(anchored) {
					position = fmt.Sprintf("%s%d", excelColumn(a.From.Col), a.From.Row+1)
				}
				label := fmt.Sprintf("%s!%s (%s)", s.Name, position, media)
				typ := http.DetectContentType(b)
				switch typ {
				case "image/png", "image/jpeg", "image/gif", "image/webp":
				default:
					return nil, fmt.Errorf("%s 图片格式 %s 需先转换", label, typ)
				}
				if len(b) > 32<<20 {
					return nil, fmt.Errorf("%s 超过视觉接口单图 32 MiB 限制", label)
				}
				images = append(images, workbookPicture{label, b})
			}
		}
	}
	return images, nil
}
