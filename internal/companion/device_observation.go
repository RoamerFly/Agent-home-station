package companion

import "time"

// DeviceObservation is local tool evidence, never a sent WeChat message.
type DeviceObservation struct {
	Recordings   []DeviceRecording `json:"recordings,omitempty"`
	TaskID       string            `json:"task_id"`
	DeviceID     string            `json:"device_id"`
	ScreenshotID string            `json:"screenshot_id,omitempty"`
	State        string            `json:"state"`
	Summary      string            `json:"summary"`
}
type DeviceRecording struct {
	ID       string `json:"id"`
	AssetID  string `json:"asset_id,omitempty"`
	Audio    bool   `json:"audio"`
	Original bool   `json:"original_media"`
	Summary  string `json:"summary"`
}

func (s *Store) ObserveDevice(in Input, c Config, o DeviceObservation, now time.Time) error {
	if in.Preview || !c.Permissions.Remember || o.TaskID == "" {
		return nil
	}
	o.Summary = boundedText(o.Summary, 2000)
	return s.transaction(func(data *persisted) error {
		id := ScopeID(in.Channel, in.Account, in.Thread)
		scope, ok := data.Scopes[id]
		if !ok {
			return nil
		}
		for _, t := range scope.Messages {
			if t.DeviceObservation != nil && t.DeviceObservation.TaskID == o.TaskID {
				return nil
			}
		}
		scope.Messages = append(scope.Messages, Turn{Role: "observation", Text: o.Summary, TraceID: "device-result-" + o.TaskID, Time: now, DeviceObservation: &o})
		var err error
		scope, err = s.pruneStored(scope, c, now)
		if err != nil {
			return err
		}
		data.Scopes[id] = scope
		return nil
	})
}
