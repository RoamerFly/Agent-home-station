package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/workspacefs"
)

// The host runs only these fixed conversion commands, never a script supplied
// by the model. Inputs are verified assets of this task. WPS is optional.
const wpsPreviewCommand = `$ErrorActionPreference='Stop'; $wps=$null; $doc=$null
try {
 $wps=New-Object -ComObject KWPS.Application
 $wps.Visible=$false; $wps.DisplayAlerts=0; $wps.AutomationSecurity=3
 $doc=$wps.Documents.Open($env:STATION_RENDER_SOURCE,$false,$true)
 $doc.ExportAsFixedFormat($env:STATION_RENDER_PDF,17)
} finally { if($doc){$doc.Close($false)}; if($wps){$wps.Quit()} }`

const rasterPreviewCommand = `import sys,json
sys.path.insert(0,sys.argv[1])
import pymupdf
d=pymupdf.open(sys.argv[2]); page=int(sys.argv[4])-1
if page<0 or page>=len(d): raise ValueError('page_out_of_range total='+str(len(d)))
d[page].get_pixmap(matrix=pymupdf.Matrix(1.5,1.5),alpha=False).save(sys.argv[3])
print(json.dumps({'page_count':len(d),'page':page+1}))`

func validatePreviewDocument(data []byte) error {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return errors.New("preview_requires_docx")
	}
	found := false
	for _, f := range z.File {
		n := strings.ToLower(f.Name)
		if n == "word/document.xml" {
			found = true
		}
		if strings.Contains(n, "vbaproject") || strings.HasPrefix(n, "word/embeddings/") {
			return errors.New("preview_active_document_unsupported")
		}
		if strings.HasSuffix(n, ".rels") {
			r, e := f.Open()
			if e != nil {
				return e
			}
			b, e := io.ReadAll(io.LimitReader(r, 1<<20))
			r.Close()
			if e != nil {
				return e
			}
			if len(b) >= 1<<20 || bytes.Contains(bytes.ToLower(b), []byte("external")) {
				return errors.New("preview_external_relationship_unsupported")
			}
		}
	}
	if !found {
		return errors.New("preview_requires_docx")
	}
	return nil
}

func (p *permittedTools) renderArtifactDocument(ctx context.Context, raw json.RawMessage) (string, error) {
	if !p.artifactPipelineAllowed() {
		return "", toolInputError("document_preview_not_authorized")
	}
	if runtime.GOOS != "windows" {
		return "", errors.New("document_preview_requires_windows_wps")
	}
	var in struct {
		Asset string `json:"asset_id"`
		Page  int    `json:"page"`
	}
	if json.Unmarshal(raw, &in) != nil || in.Asset == "" || in.Page < 0 {
		return "", toolInputError("document_preview_arguments_invalid")
	}
	if in.Page == 0 {
		in.Page = 1
	}
	a, err := p.MaterializeArtifactForTask(ctx, in.Asset, "", "")
	if err != nil {
		return "", err
	}
	data, hash, err := fileBytesAndHash(ctx, a.InputPath, 32<<20)
	if err != nil {
		return "", err
	}
	if err = validatePreviewDocument(data); err != nil {
		return "", err
	}
	options, err := runtimeDeploymentOptions()
	if err != nil {
		return "", err
	}
	if options.Python == "" || options.DocumentRuntimeRoot == "" {
		return "", errors.New("document_preview_runtime_missing")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// Short temporary paths avoid Office COM long-path limitations. This private
	// conversion directory is removed when the call finishes.
	temp, err := os.MkdirTemp(p.manager.dir, "render-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temp)
	source, pdf, png := filepath.Join(temp, "input.docx"), filepath.Join(temp, "render.pdf"), filepath.Join(temp, "page.png")
	if err = os.WriteFile(source, data, 0600); err != nil {
		return "", err
	}
	ps := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", wpsPreviewCommand)
	ps.Env = append(os.Environ(), "STATION_RENDER_SOURCE="+source, "STATION_RENDER_PDF="+pdf)
	if _, err = ps.CombinedOutput(); err != nil {
		return "", errors.New("wps_docx_render_failed_or_not_installed")
	}
	py := exec.CommandContext(ctx, options.Python, "-I", "-B", "-c", rasterPreviewCommand, filepath.Join(options.DocumentRuntimeRoot, "python-packages"), pdf, png, strconv.Itoa(in.Page))
	b, err := py.CombinedOutput()
	if err != nil {
		return "", errors.New("document_preview_raster_failed_check_pymupdf_and_page")
	}
	var result map[string]any
	if json.Unmarshal(bytes.TrimSpace(b), &result) != nil {
		return "", errors.New("document_preview_invalid_result")
	}
	names := []string{}
	root, err := workspacefs.EnsurePath(a.OutputDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	for _, src := range []string{pdf, png} {
		value, digest, e := fileBytesAndHash(ctx, src, 32<<20)
		if e != nil {
			return "", e
		}
		name := "preview-" + digest[:12] + filepath.Ext(src)
		if e = publishVerifiedStream(root, name, bytes.NewReader(value), int64(len(value)), digest); e != nil {
			return "", e
		}
		names = append(names, name)
	}
	imageName := names[1]
	if p.manager.Config().Behavior.DirectImageInput {
		im, _, e := fileBytesAndHash(ctx, png, 32<<20)
		if e != nil {
			return "", e
		}
		if e = deepseek.AttachImage(ctx, "DOCX经WPS实际渲染 第"+strconv.Itoa(in.Page)+"页", deepseek.Image{Data: im}); e != nil {
			return "", e
		}
		result["image_attached"] = true
	}
	result["pdf_path"], result["image_path"], result["source_sha256"] = names[0], imageName, hash
	result["backend"] = "wps_office+pymupdf"
	result["instruction"] = "实际DOCX页面已渲染；请对照原图检查文字和排版。PDF由同一DOCX经WPS导出，可用artifact_publish登记该pdf_path。结构校验不代表视觉验收。"
	out, _ := json.Marshal(result)
	return string(out), nil
}
