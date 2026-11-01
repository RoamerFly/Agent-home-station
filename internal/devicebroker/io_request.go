package devicebroker

func validateIORequest(name string, r map[string]any, p profile) error {
	if name == "devices" {
		if len(r) != 0 {
			return errToolDenied
		}
		return nil
	}
	id, ok := r["device_id"].(string)
	d, found := p.Devices[id]
	if !ok || !found || !d.Enabled {
		return errToolDenied
	}
	allowed := map[string]bool{"device_id": true}
	if name == "observe" {
		if !containsIO(d.Actions, "observe") {
			return errToolDenied
		}
	}
	if name == "read_artifact" {
		if !containsIO(d.Actions, "capture") {
			return errToolDenied
		}
		for _, k := range []string{"artifact_id", "request_key", "offset", "bytes"} {
			allowed[k] = true
		}
		artifact, _ := r["artifact_id"].(string)
		key, _ := r["request_key"].(string)
		offset, err := ioInteger(r["offset"])
		if !ioArtifactID.MatchString(artifact) || !identifier.MatchString(key) || err != nil || offset < 0 {
			return errToolDenied
		}
		if v, ok := r["bytes"]; ok {
			n, err := ioInteger(v)
			if err != nil || n < 1 || int64(n) > ioBinaryLimit {
				return errToolDenied
			}
		}
	}
	if name == "act" {
		action, _ := r["action"].(string)
		key, _ := r["request_key"].(string)
		observation, _ := r["observation_id"].(string)
		if !containsIO(d.Actions, action) || action == "observe" || !identifier.MatchString(key) || !ioObservationID.MatchString(observation) {
			return errToolDenied
		}
		for _, k := range []string{"action", "request_key", "observation_id"} {
			allowed[k] = true
		}
		required := []string{}
		switch action {
		case "tap":
			required = []string{"x", "y"}
		case "swipe":
			required = []string{"x", "y", "x2", "y2"}
			allowed["duration_ms"] = true
		case "text":
			required = []string{"text"}
		case "launch":
			required = []string{"package"}
		case "open_url":
			required = []string{"package", "url"}
		case "capture":
			required = []string{"seconds"}
		case "back", "home":
		default:
			return errToolDenied
		}
		for _, k := range required {
			if _, ok := r[k]; !ok {
				return errToolDenied
			}
			allowed[k] = true
		}
		if action == "capture" {
			n, err := ioInteger(r["seconds"])
			if err != nil || n < 2 || n > 12 {
				return errToolDenied
			}
		}
	}
	for k := range r {
		if !allowed[k] {
			return errToolDenied
		}
	}
	return nil
}
