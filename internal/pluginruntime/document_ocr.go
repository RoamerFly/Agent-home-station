package pluginruntime

// The OCR broker is deliberately host-owned. It starts Tesseract as the
// initial LPAC process: a no-capability LPAC Python child cannot create a
// second process, while the trusted launcher can mount exactly one input and
// the registered runtime. No model argument reaches argv or a filesystem path.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	documentOCRMaxTSV  = 2 << 20
	documentOCRTimeout = 75 * time.Second
)

type DocumentOCRRequest struct {
	RuntimeRoot string
	InputPath   string
	InputSHA256 string
}

// DocumentOCRObservation is safe to attach to one task's host metadata. TSV
// remains raw measured evidence; the Python runtime parses it rather than
// inventing words, locations, or confidence values.
type DocumentOCRObservation struct {
	InputSHA256   string `json:"input_sha256"`
	RuntimeSHA256 string `json:"runtime_manifest_sha256"`
	Language      string `json:"language"`
	PSM           int    `json:"psm"`
	TSV           string `json:"tsv"`
}

func RunDocumentOCR(ctx context.Context, spec LaunchSpec, request DocumentOCRRequest) (DocumentOCRObservation, error) {
	if runtime.GOOS != "windows" {
		return DocumentOCRObservation{}, errors.New("document_ocr_broker_unavailable")
	}
	if err := validateSpec(spec); err != nil {
		return DocumentOCRObservation{}, errors.New("document_ocr_broker_invalid_scope")
	}
	root, err := absoluteDir(request.RuntimeRoot)
	if err != nil || !inRoots(root, spec.RuntimeRoots) {
		return DocumentOCRObservation{}, errors.New("document_ocr_broker_invalid_runtime")
	}
	input, err := absoluteFile(request.InputPath)
	if err != nil || !containsExactPath(spec.ReadOnlyFiles, input) {
		return DocumentOCRObservation{}, errors.New("document_ocr_broker_invalid_input")
	}
	if !isSHA256(request.InputSHA256) || hashFile(input) != request.InputSHA256 {
		return DocumentOCRObservation{}, errors.New("document_ocr_broker_input_changed")
	}
	manifest := filepath.Join(root, "runtime-manifest.json")
	manifestHash := hashFile(manifest)
	if manifestHash == "" || !verifiedOCRRuntime(root, manifest) {
		return DocumentOCRObservation{}, errors.New("document_ocr_broker_runtime_changed")
	}
	tesseract := filepath.Join(root, "tesseract", "tesseract.exe")
	tessdata := filepath.Join(root, "tesseract", "tessdata")
	if !inside(tesseract, root) || !inside(tessdata, root) || cleanPath(tesseract, false) != nil || cleanPath(tessdata, true) != nil || cleanPath(filepath.Join(tessdata, "chi_sim.traineddata"), false) != nil {
		return DocumentOCRObservation{}, errors.New("document_ocr_broker_runtime_unavailable")
	}
	// The executable, tessdata and only the task's image all live below roots
	// already mounted read-only by isolatedCommand. The fixed psm is for a
	// normal mixed Chinese page; precision changes require a future reviewed
	// host API rather than arbitrary tool arguments.
	argv := []string{tesseract, input, "stdout", "--tessdata-dir", tessdata, "-l", "chi_sim+eng", "--oem", "1", "--psm", "6", "tsv"}
	cmd, cleanup, err := isolatedCommand(spec, argv)
	if err != nil {
		return DocumentOCRObservation{}, errors.New("document_ocr_broker_unavailable")
	}
	defer cleanup()
	deadline := documentOCRTimeout
	if until, ok := ctx.Deadline(); ok && time.Until(until) < deadline {
		deadline = time.Until(until)
	}
	if deadline <= 0 {
		return DocumentOCRObservation{}, context.DeadlineExceeded
	}
	runCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	var output boundedOCRBuffer
	// Tesseract diagnostics can contain local paths and must never be mixed
	// into measured TSV (which begins with a strict header). The host returns
	// only a stable broker failure code, not stderr.
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if err = cmd.Start(); err != nil {
		return DocumentOCRObservation{}, errors.New("document_ocr_broker_unavailable")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-runCtx.Done():
		terminate(cmd)
		<-done
		return DocumentOCRObservation{}, errors.New("document_ocr_broker_timeout")
	}
	if output.overflow {
		return DocumentOCRObservation{}, errors.New("document_ocr_broker_output_too_large")
	}
	if err != nil {
		return DocumentOCRObservation{}, errors.New("document_ocr_broker_failed")
	}
	raw := output.Bytes()
	if !bytes.HasPrefix(raw, []byte("level\tpage_num\t")) {
		return DocumentOCRObservation{}, errors.New("document_ocr_broker_invalid_tsv")
	}
	if !utf8TSV(raw) {
		return DocumentOCRObservation{}, errors.New("document_ocr_broker_invalid_tsv")
	}
	return DocumentOCRObservation{InputSHA256: request.InputSHA256, RuntimeSHA256: manifestHash, Language: "chi_sim+eng", PSM: 6, TSV: string(raw)}, nil
}

type boundedOCRBuffer struct {
	data     bytes.Buffer
	overflow bool
}

func (b *boundedOCRBuffer) Write(p []byte) (int, error) {
	if b.data.Len()+len(p) > documentOCRMaxTSV {
		b.overflow = true
		return 0, io.ErrShortWrite
	}
	return b.data.Write(p)
}
func (b *boundedOCRBuffer) Bytes() []byte { return b.data.Bytes() }

func absoluteDir(value string) (string, error) {
	if err := cleanPath(value, true); err != nil {
		return "", err
	}
	return filepath.Clean(value), nil
}
func absoluteFile(value string) (string, error) {
	if err := cleanPath(value, false); err != nil {
		return "", err
	}
	return filepath.Clean(value), nil
}
func containsExactPath(values []string, want string) bool {
	for _, value := range values {
		if samePath(value, want) {
			return true
		}
	}
	return false
}
func isSHA256(value string) bool {
	_, err := hex.DecodeString(value)
	return len(value) == 64 && err == nil && strings.ToLower(value) == value
}
func hashFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sum := sha256.New()
	if _, err = io.Copy(sum, f); err != nil {
		return ""
	}
	return hex.EncodeToString(sum.Sum(nil))
}
func utf8TSV(raw []byte) bool { return utf8.Valid(raw) }

func verifiedOCRRuntime(root, manifestPath string) bool {
	manifest, err := os.ReadFile(manifestPath)
	if err != nil || len(manifest) > 8<<20 {
		return false
	}
	var record struct {
		Components map[string]any    `json:"components"`
		Files      map[string]string `json:"files"`
	}
	if json.Unmarshal(manifest, &record) != nil || record.Components["tesseract_chi_sim"] != true {
		return false
	}
	for _, relative := range []string{"tesseract/tesseract.exe", "tesseract/tessdata/chi_sim.traineddata"} {
		expected := record.Files[relative]
		full := filepath.Join(root, filepath.FromSlash(relative))
		if !isSHA256(expected) || hashFile(full) != expected {
			return false
		}
	}
	return true
}
