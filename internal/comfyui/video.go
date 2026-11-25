package comfyui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
)

const maxMP4Bytes = 64 << 20

var ErrVideoUnavailable = errors.New("job has no verified MP4 output")
var savedMP4Name = regexp.MustCompile(`^video_[0-9]{5,}_\.mp4$`)

type MP4 struct {
	Data   []byte
	SHA256 string
}

type VideoStatus struct {
	State      State
	VideoCount int
	file       savedFile
}

// PrepareMP4 directs a host-approved SaveVideo node to its own output scope.
func PrepareMP4(raw []byte, approvedSHA256 string, ref JobRef) (Prepared, error) {
	return prepareSaveWorkflow(raw, approvedSHA256, ref, "SaveVideo", "video")
}

func (c *Client) VideoStatus(ctx context.Context, ref JobRef) (VideoStatus, error) {
	state, raw, err := c.readJob(ctx, ref)
	if err != nil || state != Completed {
		return VideoStatus{State: state}, err
	}
	// ComfyUI's SaveVideo reports animated results under the images field.
	var output struct {
		Files []savedFile `json:"images"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &output) != nil {
		return VideoStatus{}, ErrProtocol
	}
	if len(output.Files) != 1 {
		return VideoStatus{State: state, VideoCount: len(output.Files)}, nil
	}
	file := output.Files[0]
	if file.Type != "output" || file.Subfolder != ref.subfolder() || !savedMP4Name.MatchString(file.Filename) {
		return VideoStatus{}, ErrProtocol
	}
	return VideoStatus{State: state, VideoCount: 1, file: file}, nil
}

// FetchMP4 restricts readback to the selected node of the original job. The
// caller still verifies media metadata and a decoded frame before publishing.
func (c *Client) FetchMP4(ctx context.Context, ref JobRef) (MP4, error) {
	status, err := c.VideoStatus(ctx, ref)
	if err != nil {
		return MP4{}, err
	}
	if status.State != Completed || status.VideoCount != 1 {
		return MP4{}, ErrVideoUnavailable
	}
	file := status.file
	query := url.Values{"filename": {file.Filename}, "type": {"output"}, "subfolder": {file.Subfolder}}
	code, body, err := c.request(ctx, http.MethodGet, "/view?"+query.Encode(), nil, maxMP4Bytes)
	if err != nil {
		return MP4{}, err
	}
	if code != http.StatusOK || len(body) < 12 || string(body[4:8]) != "ftyp" {
		return MP4{}, ErrVideoUnavailable
	}
	hash := sha256.Sum256(body)
	return MP4{Data: body, SHA256: hex.EncodeToString(hash[:])}, nil
}
