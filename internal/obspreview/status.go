package obspreview

import (
	"context"
	"errors"
	"time"

	"github.com/gorilla/websocket"
)

const (
	invalidResourceState = requestRejection(604)
	maxPreviewInputs     = 128
)

// PreviewStatus describes this OBS instance. A missing output value means its
// state could not be read. Input volume is a fader setting, not a signal meter.
type PreviewStatus struct {
	ObservedAt    time.Time     `json:"observed_at"`
	VirtualCamera *bool         `json:"virtual_camera,omitempty"`
	Streaming     *StreamStatus `json:"streaming,omitempty"`
	Recording     *bool         `json:"recording,omitempty"`
	Audio         *AudioStatus  `json:"audio,omitempty"`
}

type AudioStatus struct {
	Inputs   []AudioInput `json:"inputs"`
	Total    int          `json:"total"`
	Checked  int          `json:"checked"`
	Complete bool         `json:"complete"`
}

type AudioInput struct {
	Name   string   `json:"name"`
	Muted  bool     `json:"muted"`
	Volume *float64 `json:"volume,omitempty"`
}

func readPreviewStatus(ctx context.Context, conn *websocket.Conn) *PreviewStatus {
	status := &PreviewStatus{ObservedAt: time.Now().UTC()}
	for _, query := range []struct {
		name   string
		active **bool
	}{
		{"GetVirtualCamStatus", &status.VirtualCamera},
		{"GetRecordStatus", &status.Recording},
	} {
		var result struct {
			Active *bool `json:"outputActive"`
		}
		err := request(ctx, conn, query.name, struct{}{}, &result)
		if err == nil {
			*query.active = result.Active
		} else if !errors.Is(err, ErrRequestRejected) {
			return status
		}
	}
	stream, err := streamStatus(ctx, conn)
	if err == nil {
		status.Streaming = &stream
	} else if !errors.Is(err, ErrRequestRejected) {
		return status
	}
	status.Audio = readAudioStatus(ctx, conn)
	return status
}

func readAudioStatus(ctx context.Context, conn *websocket.Conn) *AudioStatus {
	var result struct {
		Inputs []struct {
			Name string `json:"inputName"`
		} `json:"inputs"`
	}
	if err := request(ctx, conn, "GetInputList", struct{}{}, &result); err != nil || result.Inputs == nil {
		return nil
	}
	audio := &AudioStatus{Inputs: []AudioInput{}, Total: len(result.Inputs), Complete: len(result.Inputs) <= maxPreviewInputs}
	seen := make(map[string]bool)
	for _, input := range result.Inputs[:min(len(result.Inputs), maxPreviewInputs)] {
		if !validScene(input.Name) || seen[input.Name] {
			audio.Complete = false
			continue
		}
		seen[input.Name] = true
		ref := map[string]string{"inputName": input.Name}
		var mute struct {
			Muted *bool `json:"inputMuted"`
		}
		err := request(ctx, conn, "GetInputMute", ref, &mute)
		var rejected requestRejection
		if errors.As(err, &rejected) && rejected == invalidResourceState {
			// OBS reports this for inputs without audio output.
			audio.Checked++
			continue
		}
		if err != nil || mute.Muted == nil {
			audio.Complete = false
			if err != nil && !errors.Is(err, ErrRequestRejected) {
				break
			}
			continue
		}
		source := AudioInput{Name: input.Name, Muted: *mute.Muted}
		var volume struct {
			Multiplier *float64 `json:"inputVolumeMul"`
		}
		err = request(ctx, conn, "GetInputVolume", ref, &volume)
		if err == nil && volume.Multiplier != nil && *volume.Multiplier >= 0 {
			source.Volume = volume.Multiplier
			audio.Checked++
		} else {
			audio.Complete = false
		}
		audio.Inputs = append(audio.Inputs, source)
		if err != nil && !errors.Is(err, ErrRequestRejected) {
			break
		}
	}
	return audio
}
