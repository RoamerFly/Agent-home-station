package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

type JobSpec struct {
	IDPath          string      `json:"id_path"`
	DeadlineSeconds int         `json:"deadline_seconds,omitempty"`
	MaxPolls        int         `json:"max_polls,omitempty"`
	Cancel          *CancelSpec `json:"cancel,omitempty"`
}
type CancelSpec struct {
	URL          string            `json:"url"`
	Method       string            `json:"method,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	BodyTemplate json.RawMessage   `json:"body_template,omitempty"`
}
type JobTicket struct {
	Tool        string `json:"tool"`
	Fingerprint string `json:"fingerprint"`
	RemoteID    string `json:"remote_id,omitempty"`
	State       string `json:"state"`
	Output      Output `json:"output,omitempty"`
}

var jobIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$`)
var fieldPathPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
var ErrJobConfigChanged = errors.New("plugin_job_configuration_changed")

func validateJob(t APITool) error {
	j := t.Job
	if j == nil {
		return nil
	}
	if t.Poll == nil || len(j.IDPath) > 128 || !fieldPathPattern.MatchString(j.IDPath) || j.MaxPolls < 0 || j.MaxPolls > 1440 || j.DeadlineSeconds < 0 || j.DeadlineSeconds > 86400 || j.DeadlineSeconds > 0 && j.DeadlineSeconds < 30 {
		return errors.New("invalid background job settings")
	}
	check := func(raw string) bool {
		if !strings.Contains(raw, "{{result."+j.IDPath+"}}") {
			return false
		}
		for _, m := range placeholder.FindAllStringSubmatch(raw, -1) {
			if m[1] != "result" || m[2] != j.IDPath {
				return false
			}
		}
		return true
	}
	if !check(t.Poll.URL) {
		return errors.New("background poll URL must use the declared job id only")
	}
	for _, value := range t.Poll.Headers {
		if strings.Contains(value, "{{arg.") || strings.Contains(value, "{{result.") {
			return errors.New("background poll headers may use secrets only")
		}
	}
	if c := j.Cancel; c != nil {
		if validEndpoint(c.URL, true) != nil || !check(c.URL) || (c.Method != "" && c.Method != http.MethodPost && c.Method != http.MethodDelete) || len(c.Headers) > 32 || len(c.BodyTemplate) > maxSchema || len(c.BodyTemplate) > 0 && !json.Valid(c.BodyTemplate) {
			return errors.New("invalid cancellation endpoint")
		}
		for key, value := range c.Headers {
			if !validHeader(key, value) || strings.Contains(value, "{{arg.") || strings.Contains(value, "{{result.") {
				return errors.New("invalid cancellation headers")
			}
		}
		for _, m := range placeholder.FindAllStringSubmatch(string(c.BodyTemplate), -1) {
			if m[1] == "arg" || m[1] == "result" && m[2] != j.IDPath {
				return errors.New("cancellation template may only use job id and secrets")
			}
		}
	}
	return nil
}
func toolFingerprint(t APITool) string {
	b, _ := json.Marshal(t)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (r *Runtime) Background(name string) bool { t, ok := r.tools[name]; return ok && t.Job != nil }
func (r *Runtime) CheckJobArgs(name string, args json.RawMessage) error {
	t, ok := r.tools[name]
	if !ok || t.Job == nil {
		return errors.New("not a background tool")
	}
	var input map[string]any
	if len(args) > maxArgs || json.Unmarshal(args, &input) != nil || input == nil {
		return errors.New("invalid background arguments")
	}
	parts := strings.SplitN(name, "__", 2)
	secrets := r.installed[parts[0]].Secrets
	if _, err := renderBody(t.BodyTemplate, secrets, input, nil); err != nil {
		return safeErr(err)
	}
	for _, value := range t.Headers {
		if _, err := renderString(value, secrets, input, nil); err != nil {
			return safeErr(err)
		}
	}
	return nil
}
func (r *Runtime) JobLimits(name string) (interval, deadline, polls int) {
	t := r.tools[name]
	if t.Job == nil {
		return
	}
	interval, deadline, polls = t.Poll.IntervalSeconds, t.Job.DeadlineSeconds, t.Job.MaxPolls
	if interval == 0 {
		interval = 10
	}
	if deadline == 0 {
		deadline = 3600
	}
	if polls == 0 {
		polls = 240
	}
	return
}
func (r *Runtime) jobTool(ticket JobTicket) (APITool, Installed, error) {
	t, ok := r.tools[ticket.Tool]
	parts := strings.SplitN(ticket.Tool, "__", 2)
	if !ok || t.Job == nil || len(parts) != 2 || toolFingerprint(t) != ticket.Fingerprint {
		return APITool{}, Installed{}, ErrJobConfigChanged
	}
	return t, r.installed[parts[0]], nil
}
func (r *Runtime) NewJobTicket(name string) (JobTicket, error) {
	t, ok := r.tools[name]
	if !ok || t.Job == nil {
		return JobTicket{}, errors.New("not a background tool")
	}
	return JobTicket{Tool: name, Fingerprint: toolFingerprint(t), State: "submitting"}, nil
}
func jobSeed(path, id string) any {
	parts := strings.Split(path, ".")
	var value any = id
	for i := len(parts) - 1; i >= 0; i-- {
		value = map[string]any{parts[i]: value}
	}
	return value
}
func followHeaders(createURL, targetURL string, explicit, creation map[string]string) map[string]string {
	if explicit != nil {
		return explicit
	}
	a, _ := url.Parse(createURL)
	b, _ := url.Parse(targetURL)
	if a.Scheme == b.Scheme && strings.EqualFold(a.Host, b.Host) {
		return creation
	}
	return nil
}
