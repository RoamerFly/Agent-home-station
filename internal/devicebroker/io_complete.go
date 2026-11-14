package devicebroker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"
)

func (c *ioCall) Complete(ctx context.Context, result json.RawMessage) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(result, &object) != nil || object == nil {
		return nil, errToolDenied
	}
	var raw json.RawMessage
	switch c.name {
	case "devices":
		var value struct {
			Devices []map[string]json.RawMessage `json:"devices"`
		}
		if json.Unmarshal(result, &value) != nil || len(value.Devices) != len(c.broker.profile.Devices) {
			return nil, errToolDenied
		}
		seen := map[string]bool{}
		for _, item := range value.Devices {
			var id, state string
			_ = json.Unmarshal(item["id"], &id)
			_ = json.Unmarshal(item["state"], &state)
			d, ok := c.broker.profile.Devices[id]
			if !ok || seen[id] || c.states[id] != state || !ioSame(item["packages"], ioJSON(d.Packages)) || !ioSame(item["actions"], ioJSON(d.Actions)) {
				return nil, errToolDenied
			}
			seen[id] = true
		}
		raw = result
	case "observe":
		var value struct {
			Snapshot   map[string]any `json:"snapshot"`
			Screenshot blobRef        `json:"screenshot_ref"`
		}
		if json.Unmarshal(result, &value) != nil || c.shot == nil || !ioSame(ioJSON(value.Screenshot), ioJSON(c.shot)) {
			return nil, errors.New("device_screenshot_ref_unverified")
		}
		s := value.Snapshot
		id, _ := s["id"].(string)
		d, _ := c.deviceFor(c.device)
		at, _ := s["observed_at"].(float64)
		if !ioObservationID.MatchString(id) || s["device_id"] != c.device || s["package"] != c.foreground || s["width"] != float64(c.shot.Width) || s["height"] != float64(c.shot.Height) || s["screenshot_sha256"] != c.shot.SHA256 || !ioSame(ioJSON(s["allowed_packages"]), ioJSON(d.Packages)) || !ioSame(ioJSON(s["browsers"]), ioJSON(ioStrings(d.Browsers))) || !ioSame(ioJSON(s["web_hosts"]), ioJSON(ioStrings(d.WebHosts))) || at < float64(c.started.UnixNano())/1e9-1 || at > float64(time.Now().UnixNano())/1e9+1 {
			return nil, errors.New("device_observation_unverified")
		}
		nodes, ok := s["nodes"].([]any)
		if !ok || len(nodes) > 100 {
			return nil, errToolDenied
		}
		// A package-scoped autonomous observation intentionally omits UI
		// hierarchy. Do not let a plugin manufacture unobserved nodes into the
		// persisted snapshot merely because the optional UI phase was skipped.
		if c.observationPackage != "" && len(nodes) != 0 {
			return nil, errors.New("device_observation_ui_unavailable")
		}
		var foreground string
		var err error
		if c.observationPackage != "" {
			foreground, err = c.observedFocus(ctx, c.device)
		} else {
			foreground, err = c.focus(ctx, c.device)
		}
		if err != nil || foreground != s["package"] {
			if errors.Is(err, ErrObservationAppMismatch) {
				return nil, err
			}
			return nil, errors.New("device_page_changed_during_observation")
		}
		png, err := c.blobData(*c.shot)
		if err != nil {
			return nil, err
		}
		s["_host_ui_sha256"] = ioSHA([]byte(c.ui))
		snapshot := ioJSON(s)
		if err = privateIOWrite(filepath.Join(c.broker.storage, id+".json"), snapshot); err != nil {
			return nil, err
		}
		if err = privateIOWrite(filepath.Join(c.broker.storage, id+".png"), png); err != nil {
			return nil, err
		}
		raw = ioJSON(map[string]any{"text": string(snapshot), "media": []any{map[string]any{"mime_type": "image/png", "data_base64": base64.StdEncoding.EncodeToString(png)}}})
	case "act":
		if len(c.prior) > 0 {
			if !ioSame(c.prior, result) {
				return nil, errToolDenied
			}
			return append(json.RawMessage(nil), c.prior...), nil
		}
		var value struct {
			State  string         `json:"state"`
			Result map[string]any `json:"result"`
		}
		if json.Unmarshal(result, &value) != nil || !c.attempted || value.State != "unknown" && value.State != "submitted" {
			return nil, errors.New("device_action_unverified")
		}
		if value.State == "submitted" {
			if !c.executed || value.Result["device_id"] != c.device || value.Result["action"] != c.request["action"] || value.Result["reobserve_required"] != true {
				return nil, errors.New("device_action_unverified")
			}
			if c.request["action"] == "capture" {
				if !ioSame(ioJSON(value.Result["artifact"]), c.artifact) {
					return nil, errors.New("device_artifact_unverified")
				}
			} else if value.Result["artifact"] != nil {
				return nil, errToolDenied
			}
			key, _ := c.request["request_key"].(string)
			updated, err := c.broker.db.ExecContext(ctx, "UPDATE operation SET state=?,result=? WHERE key=? AND hash=? AND state='unknown'", "submitted", string(ioJSON(value.Result)), key, legacyRequestHash(c.request))
			if err != nil {
				return nil, err
			}
			n, _ := updated.RowsAffected()
			if n != 1 {
				return nil, errors.New("device_journal_changed")
			}
		} else if c.executed || len(value.Result) > 0 {
			return nil, errors.New("device_action_unverified")
		}
		raw = result
	case "read_artifact":
		if len(c.chunk) == 0 || !ioSame(c.chunk, result) {
			return nil, errors.New("device_artifact_unverified")
		}
		var value map[string]json.RawMessage
		_ = json.Unmarshal(c.chunk, &value)
		var ref blobRef
		_ = json.Unmarshal(value["chunk_ref"], &ref)
		data, err := c.blobData(ref)
		if err != nil {
			return nil, err
		}
		delete(value, "chunk_ref")
		value["data_base64"] = ioJSON(base64.StdEncoding.EncodeToString(data))
		raw = ioJSON(value)
	default:
		return nil, errToolDenied
	}
	if err := c.check(ctx); err != nil {
		return nil, err
	}
	return raw, nil
}
