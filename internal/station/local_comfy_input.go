package station

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"path/filepath"
	"wechat-bot/internal/fishidentity"
)

// The Station host stages a verified image into its own private ComfyUI input
// directory. The isolated WeChat script process never needs that directory.
func (s *localProjectTools) comfyInputUpload(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Data string `json:"data_base64"`
		SHA  string `json:"sha256"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 28<<20)
	if json.NewDecoder(r.Body).Decode(&in) != nil || len(in.Data) > 27<<20 {
		http.Error(w, "comfy_input_invalid", http.StatusBadRequest)
		return
	}
	data, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil || len(data) == 0 || len(data) > 20<<20 {
		http.Error(w, "comfy_input_invalid", http.StatusBadRequest)
		return
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != in.SHA {
		http.Error(w, "comfy_input_digest_mismatch", http.StatusBadRequest)
		return
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") {
		http.Error(w, "comfy_input_image_invalid", http.StatusBadRequest)
		return
	}
	view, err := s.readComfyDeployment()
	if err != nil || !view.Configured {
		http.Error(w, "comfy_deployment_unavailable", http.StatusConflict)
		return
	}
	fish, _, err := fishidentity.New(stationAuthorityDir(s.home.dataDir)).Bindings()
	if err != nil || !fishidentity.ValidFishID(fish) {
		http.Error(w, "comfy_identity_unavailable", http.StatusConflict)
		return
	}
	root := filepath.Join(stationToolPrivateDir(s.home.dataDir, "comfyui"), localComfyFishHash(fish), "input")
	name, err := stageComfyInput(root, in.SHA, data, format)
	if err != nil {
		http.Error(w, "comfy_input_unavailable", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"filename": name, "sha256": in.SHA})
}

func stageComfyInput(root, sha string, data []byte, format string) (string, error) {
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != sha || (format != "jpeg" && format != "png") {
		return "", errors.New("comfy input identity invalid")
	}
	if err := os.MkdirAll(root, 0700); err != nil || !localComfySafeDirectory(root) {
		return "", errors.New("comfy input directory unavailable")
	}
	ext := ".png"
	if format == "jpeg" {
		ext = ".jpg"
	}
	name := "wechat-" + sha + ext
	path := filepath.Join(root, name)
	var err error
	if old, readErr := os.ReadFile(path); readErr == nil {
		if sha256.Sum256(old) != digest {
			err = errors.New("image mismatch")
		}
	} else if errors.Is(readErr, os.ErrNotExist) {
		var file *os.File
		file, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, err = file.Write(data)
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				_ = os.Remove(path)
			}
		}
	} else {
		err = readErr
	}
	if err != nil {
		return "", err
	}
	return name, nil
}
