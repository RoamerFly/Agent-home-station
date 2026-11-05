package devicebroker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"wechat-bot/internal/workspacefs"
)

const ioBinaryLimit int64 = 128 << 20

var ioObservationID = regexp.MustCompile(`^obs-[a-f0-9]{32}$`)
var ioArtifactID = regexp.MustCompile(`^clip-[a-f0-9]{32}$`)
var ioFocus = regexp.MustCompile(`mCurrentFocus=.*?\s([A-Za-z0-9_.]+)/[^\s}]+`)

// Preserve the packaged Python ipaddress public-URL boundary. Go's
// IsGlobalUnicast alone also accepts CGNAT, documentation and reserved ranges.
var ioNonPublicNetworks = ioNetworks("0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "::/128", "::1/128", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20", "fc00::/7", "fe80::/10")
var ioPublicExceptions = ioNetworks("192.0.0.9/32", "192.0.0.10/32", "2001:1::1/128", "2001:1::2/128", "2001:3::/32", "2001:4:112::/48", "2001:20::/28", "2001:30::/28")

func ioNetworks(values ...string) []netip.Prefix {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		result = append(result, netip.MustParsePrefix(value))
	}
	return result
}
func ioPublicAddress(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok || !ip.IsGlobalUnicast() {
		return false
	}
	address = address.Unmap()
	for _, exception := range ioPublicExceptions {
		if exception.Contains(address) {
			return true
		}
	}
	for _, network := range ioNonPublicNetworks {
		if network.Contains(address) {
			return false
		}
	}
	return true
}

type blobRef struct {
	Ref    string `json:"ref"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

func ioSHA(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func ioID(prefix string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}
func ioJSON(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }
func ioSame(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return bytes.Equal(ioJSON(x), ioJSON(y))
}
func ioRead(root, name string, limit int64) ([]byte, error) {
	r, err := workspacefs.EnsurePath(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := workspacefs.OpenRegular(r, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("device_io_response_limit")
	}
	return data, nil
}
func (c *ioCall) blob(data []byte) (blobRef, error) {
	r := blobRef{Ref: "blob-" + ioSHA(data), SHA256: ioSHA(data), Bytes: int64(len(data))}
	if err := privateIOWrite(filepath.Join(c.broker.storage, "io-blobs", r.Ref+".bin"), data); err != nil {
		return r, err
	}
	c.refs[r.Ref] = r
	return r, nil
}
func (c *ioCall) blobData(r blobRef) ([]byte, error) {
	if _, ok := c.refs[r.Ref]; !ok {
		return nil, errToolDenied
	}
	data, err := ioRead(filepath.Join(c.broker.storage, "io-blobs"), r.Ref+".bin", ioBinaryLimit)
	if err != nil || int64(len(data)) != r.Bytes || ioSHA(data) != r.SHA256 {
		return nil, errChanged
	}
	return data, nil
}
func (c *ioCall) deviceFor(id string) (profileDevice, error) {
	if c.name != "devices" && id != c.device {
		return profileDevice{}, errToolDenied
	}
	d, ok := c.broker.profile.Devices[id]
	if !ok || !d.Enabled {
		return d, errToolDenied
	}
	return d, nil
}
func (c *ioCall) adb(ctx context.Context, id string, args []string, limit int64, timeout time.Duration) ([]byte, error) {
	d, err := c.deviceFor(id)
	if err != nil {
		return nil, err
	}
	if err = c.check(ctx); err != nil {
		return nil, err
	}
	bounded, cancel := context.WithCancel(c.ctx)
	stop := context.AfterFunc(ctx, cancel)
	defer func() { stop(); cancel() }()
	lease, err := c.broker.acquire(bounded, c.broker.adb)
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	if lease.Endpoint() != c.broker.adbLease.Endpoint() || lease.Binding() != c.broker.adbLease.Binding() {
		return nil, errors.New("device_adb_server_changed")
	}
	argv := append([]string{"-H", "127.0.0.1", "-P", "5037", "-s", d.Serial}, args...)
	data, err := c.broker.run(bounded, c.broker.adb, argv, limit, timeout)
	if err != nil {
		var pending *ioCleanupError
		if errors.As(err, &pending) {
			c.cleanups = append(c.cleanups, pending)
			c.broker.mu.Lock()
			c.broker.closed = true
			c.broker.mu.Unlock()
			registerQuarantine(c.broker)
		}
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("device_io_response_limit")
	}
	if err = c.check(ctx); err != nil {
		return nil, err
	}
	return data, nil
}
func (c *ioCall) focus(ctx context.Context, id string) (string, error) {
	data, err := c.adb(ctx, id, []string{"shell", "dumpsys", "window"}, 2<<20, 12*time.Second)
	if err != nil {
		return "", err
	}
	match := ioFocus.FindSubmatch(data)
	d, _ := c.deviceFor(id)
	if len(match) != 2 || !containsIO(d.Packages, string(match[1])) {
		return "", errors.New("device_foreground_not_permitted")
	}
	return string(match[1]), nil
}
func ioStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
func containsIO(items []string, want string) bool {
	for _, v := range items {
		if v == want {
			return true
		}
	}
	return false
}
func (c *ioCall) screen(ctx context.Context, id string) (blobRef, error) {
	data, err := c.adb(ctx, id, []string{"exec-out", "screencap", "-p"}, ioBinaryLimit, 12*time.Second)
	if err != nil {
		return blobRef{}, err
	}
	if len(data) < 24 || !bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")) {
		return blobRef{}, errors.New("device_screenshot_invalid")
	}
	r, err := c.blob(data)
	if err != nil {
		return r, err
	}
	r.Width = int(binary.BigEndian.Uint32(data[16:20]))
	r.Height = int(binary.BigEndian.Uint32(data[20:24]))
	if r.Width < 1 || r.Width > 4096 || r.Height < 1 || r.Height > 4096 {
		return r, errors.New("device_screen_dimensions_invalid")
	}
	c.refs[r.Ref] = r
	return r, nil
}
func (c *ioCall) readUI(ctx context.Context, id string) (string, error) {
	remote := "/sdcard/whale-ui-" + strings.TrimPrefix(ioID(""), "") + ".xml"
	defer func() { _, _ = c.adb(ctx, id, []string{"shell", "rm", "-f", remote}, 4096, 5*time.Second) }()
	if _, err := c.adb(ctx, id, []string{"shell", "uiautomator", "dump", remote}, 4096, 15*time.Second); err != nil {
		return "", err
	}
	data, err := c.adb(ctx, id, []string{"exec-out", "cat", remote}, 1<<20, 12*time.Second)
	return string(data), err
}

func (c *ioCall) Service(ctx context.Context, step, device string, parameters json.RawMessage) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return nil, err
	}
	var params map[string]json.RawMessage
	if len(parameters) == 0 {
		parameters = json.RawMessage(`{}`)
	}
	if json.Unmarshal(parameters, &params) != nil || len(params) != 0 {
		return nil, errToolDenied
	}
	if step == "profile" {
		if device != "" && device != c.device {
			return nil, errToolDenied
		}
		devices := map[string]any{}
		for alias, d := range c.broker.profile.Devices {
			if c.name != "devices" && alias != c.device {
				continue
			}
			devices[alias] = map[string]any{"serial": alias, "enabled": true, "packages": d.Packages, "actions": d.Actions, "browsers": ioStrings(d.Browsers), "web_hosts": ioStrings(d.WebHosts)}
		}
		out := map[string]any{"scope": c.broker.binding, "config": map[string]any{"devices": devices}}
		if c.name == "act" {
			prior, err := c.previous(ctx)
			if err != nil {
				return nil, err
			}
			if len(prior) > 0 {
				c.prior = prior
				out["previous"] = prior
			}
			if id, ok := c.request["observation_id"].(string); ok && ioObservationID.MatchString(id) {
				raw, err := ioRead(c.broker.storage, id+".json", 1<<20)
				if err == nil {
					out["observation"] = json.RawMessage(raw)
				}
			}
		}
		return ioJSON(out), nil
	}
	if _, err := c.deviceFor(device); err != nil {
		return nil, err
	}
	switch step {
	case "state":
		if c.name != "devices" {
			return nil, errToolDenied
		}
		data, err := c.adb(ctx, device, []string{"get-state"}, 100, 4*time.Second)
		if err != nil {
			if c.states == nil {
				c.states = map[string]string{}
			}
			c.states[device] = "offline"
			return ioJSON(map[string]any{"state": "offline"}), nil
		}
		if c.states == nil {
			c.states = map[string]string{}
		}
		c.states[device] = strings.TrimSpace(string(data))
		return ioJSON(map[string]any{"state": c.states[device]}), nil
	case "foreground":
		if c.name != "observe" && c.name != "act" {
			return nil, errToolDenied
		}
		value, err := c.focus(ctx, device)
		if err != nil {
			return nil, err
		}
		c.foreground = value
		return ioJSON(map[string]any{"package": value}), nil
	case "screenshot":
		if c.name != "observe" && c.name != "act" {
			return nil, errToolDenied
		}
		r, err := c.screen(ctx, device)
		if err != nil {
			return nil, err
		}
		c.shot = &r
		return ioJSON(r), nil
	case "ui":
		if c.name != "observe" && c.name != "act" {
			return nil, errToolDenied
		}
		xml, err := c.readUI(ctx, device)
		if err != nil {
			// UI hierarchy is optional in the original observation protocol.
			// Preserve that case without swallowing a revoked authority,
			// changed resource or unconfirmed process cleanup.
			if checkErr := c.check(ctx); checkErr != nil {
				return nil, checkErr
			}
			c.ui = ""
			return ioJSON(map[string]any{"xml": "", "sha256": ioSHA(nil), "available": false}), nil
		}
		c.ui = xml
		return ioJSON(map[string]any{"xml": xml, "sha256": ioSHA([]byte(xml)), "available": true}), nil
	case "dispatch":
		if c.name != "act" || c.request["action"] == "capture" {
			return nil, errToolDenied
		}
		if err := c.mutate(ctx, false); err != nil {
			return nil, err
		}
		return ioJSON(map[string]any{"submitted": true}), nil
	case "record":
		if c.name != "act" || c.request["action"] != "capture" {
			return nil, errToolDenied
		}
		if err := c.mutate(ctx, true); err != nil {
			return nil, err
		}
		return c.artifact, nil
	case "artifact_read":
		if c.name != "read_artifact" {
			return nil, errToolDenied
		}
		return c.readArtifact(ctx)
	default:
		return nil, errToolDenied
	}
}

func legacyRequestHash(request map[string]any) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(request)
	raw := bytes.TrimSpace(b.Bytes())
	out := make([]byte, 0, len(raw)+32)
	quoted, escaped := false, false
	for _, ch := range raw {
		out = append(out, ch)
		if quoted {
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				quoted = false
			}
		} else if ch == '"' {
			quoted = true
		} else if ch == ':' || ch == ',' {
			out = append(out, ' ')
		}
	}
	return ioSHA(out)
}
func (c *ioCall) previous(ctx context.Context) (json.RawMessage, error) {
	key, _ := c.request["request_key"].(string)
	if !identifier.MatchString(key) {
		return nil, errToolDenied
	}
	var hash, state, result string
	err := c.broker.db.QueryRowContext(ctx, "SELECT hash,state,result FROM operation WHERE key=?", key).Scan(&hash, &state, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if hash != legacyRequestHash(c.request) {
		return nil, errors.New("device_request_key_conflict")
	}
	return ioJSON(map[string]any{"state": state, "result": json.RawMessage(result), "replayed": false}), nil
}
func (c *ioCall) mutationSnapshot(ctx context.Context) (map[string]any, error) {
	id, _ := c.request["observation_id"].(string)
	if !ioObservationID.MatchString(id) {
		return nil, errToolDenied
	}
	raw, err := ioRead(c.broker.storage, id+".json", 1<<20)
	if err != nil {
		return nil, errors.New("device_observation_missing")
	}
	var s map[string]any
	if json.Unmarshal(raw, &s) != nil || s["device_id"] != c.device {
		return nil, errToolDenied
	}
	at, _ := s["observed_at"].(float64)
	if time.Since(time.Unix(0, int64(at*1e9))) > 45*time.Second || at > float64(time.Now().UnixNano())/1e9+1 {
		return nil, errors.New("device_observation_stale")
	}
	pkg, err := c.focus(ctx, c.device)
	if err != nil || pkg != s["package"] {
		return nil, errors.New("device_page_changed_reobserve")
	}
	shot, err := c.screen(ctx, c.device)
	if err != nil {
		return nil, err
	}
	if c.request["action"] != "capture" && shot.SHA256 != s["screenshot_sha256"] {
		xml, err := c.readUI(ctx, c.device)
		if err != nil || ioSHA([]byte(xml)) != s["_host_ui_sha256"] {
			return nil, errors.New("device_page_changed_reobserve")
		}
	}
	return s, nil
}
func ioInteger(v any) (int, error) {
	n, ok := v.(float64)
	if !ok || n != float64(int(n)) {
		return 0, errToolDenied
	}
	return int(n), nil
}
func (c *ioCall) actionArgs(ctx context.Context, s map[string]any) ([]string, error) {
	d, _ := c.deviceFor(c.device)
	action, _ := c.request["action"].(string)
	if !containsIO(d.Actions, action) {
		return nil, errToolDenied
	}
	coordinate := func(key, extent string) (string, error) {
		v, err := ioInteger(c.request[key])
		e, _ := s[extent].(float64)
		if err != nil || v < 0 || float64(v) >= e {
			return "", errToolDenied
		}
		return strconv.Itoa(v), nil
	}
	switch action {
	case "tap", "swipe":
		x, err := coordinate("x", "width")
		if err != nil {
			return nil, err
		}
		y, err := coordinate("y", "height")
		if err != nil {
			return nil, err
		}
		if action == "tap" {
			return []string{"shell", "input", "tap", x, y}, nil
		}
		x2, err := coordinate("x2", "width")
		if err != nil {
			return nil, err
		}
		y2, err := coordinate("y2", "height")
		if err != nil {
			return nil, err
		}
		duration := 350
		if value, ok := c.request["duration_ms"]; ok {
			duration, err = ioInteger(value)
			if err != nil {
				return nil, err
			}
		}
		if duration < 100 || duration > 1500 {
			return nil, errToolDenied
		}
		return []string{"shell", "input", "swipe", x, y, x2, y2, strconv.Itoa(duration)}, nil
	case "back", "home":
		key := "4"
		if action == "home" {
			key = "3"
		}
		return []string{"shell", "input", "keyevent", key}, nil
	case "text":
		text, ok := c.request["text"].(string)
		if !ok || !regexp.MustCompile(`^[A-Za-z0-9 @._-]{1,200}$`).MatchString(text) {
			return nil, errToolDenied
		}
		return []string{"shell", "input", "text", strings.ReplaceAll(text, " ", "%s")}, nil
	case "launch":
		pkg, _ := c.request["package"].(string)
		if !containsIO(d.Packages, pkg) || !packageName.MatchString(pkg) {
			return nil, errToolDenied
		}
		return []string{"shell", "monkey", "-p", pkg, "-c", "android.intent.category.LAUNCHER", "1"}, nil
	case "open_url":
		pkg, _ := c.request["package"].(string)
		raw, _ := c.request["url"].(string)
		if !containsIO(d.Browsers, pkg) || !packageName.MatchString(pkg) || len(raw) > 8192 || !regexp.MustCompile(`^[A-Za-z0-9._~:/?&=%+#@,;!-]+$`).MatchString(raw) {
			return nil, errToolDenied
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" && u.Port() != "443" || !containsIO(d.WebHosts, strings.ToLower(u.Hostname())) {
			return nil, errToolDenied
		}
		var addresses []net.IPAddr
		if literal := net.ParseIP(u.Hostname()); literal != nil {
			addresses = []net.IPAddr{{IP: literal}}
		} else {
			addresses, err = net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
		}
		if err != nil || len(addresses) == 0 {
			return nil, errToolDenied
		}
		for _, a := range addresses {
			if !ioPublicAddress(a.IP) {
				return nil, errToolDenied
			}
		}
		quoted := raw
		if !regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`).MatchString(raw) {
			quoted = "'" + strings.ReplaceAll(raw, "'", "'\"'\"'") + "'"
		}
		return []string{"shell", "am", "start", "-a", "android.intent.action.VIEW", "-d", quoted, "-p", pkg}, nil
	}
	return nil, errToolDenied
}
func (c *ioCall) mutate(ctx context.Context, record bool) error {
	if c.attempted || len(c.prior) > 0 {
		return errors.New("device_request_already_used")
	}
	if previous, err := c.previous(ctx); err != nil {
		return err
	} else if len(previous) > 0 {
		c.prior = previous
		return errors.New("device_request_already_used")
	}
	s, err := c.mutationSnapshot(ctx)
	if err != nil {
		return err
	}
	var args []string
	if !record {
		args, err = c.actionArgs(ctx, s)
		if err != nil {
			return err
		}
	} else {
		d, _ := c.deviceFor(c.device)
		if !containsIO(d.Actions, "capture") {
			return errToolDenied
		}
	}
	key, _ := c.request["request_key"].(string)
	if _, err = c.broker.db.ExecContext(ctx, "INSERT INTO operation VALUES(?,?,?,?,?,?)", key, legacyRequestHash(c.request), c.device, "unknown", "{}", float64(time.Now().UnixNano())/1e9); err != nil {
		return err
	}
	c.attempted = true
	if record {
		err = c.capture(ctx)
	} else {
		_, err = c.adb(ctx, c.device, args, 1<<20, 15*time.Second)
	}
	if err == nil {
		c.executed = true
	}
	return err
}
func (c *ioCall) capture(ctx context.Context) error {
	seconds, err := ioInteger(c.request["seconds"])
	if err != nil || seconds < 2 || seconds > 12 {
		return errToolDenied
	}
	id := ioID("clip-")
	remote := "/sdcard/whale-" + id + ".mp4"
	defer func() { _, _ = c.adb(ctx, c.device, []string{"shell", "rm", "-f", remote}, 4096, 5*time.Second) }()
	if _, err = c.adb(ctx, c.device, []string{"shell", "screenrecord", "--size", "480x800", "--bit-rate", "256000", "--time-limit", strconv.Itoa(seconds), remote}, 4096, time.Duration(seconds+10)*time.Second); err != nil {
		return err
	}
	data, err := c.adb(ctx, c.device, []string{"exec-out", "cat", remote}, ioBinaryLimit, 20*time.Second)
	if err != nil {
		return err
	}
	if len(data) < 32 || string(data[4:8]) != "ftyp" {
		return errors.New("device_capture_invalid")
	}
	if _, err = c.focus(ctx, c.device); err != nil {
		return err
	}
	r, err := c.blob(data)
	if err != nil {
		return err
	}
	key, _ := c.request["request_key"].(string)
	meta := map[string]any{"id": id, "device_id": c.device, "request_key": key, "kind": "screen_recording", "mime": "video/mp4", "bytes": r.Bytes, "sha256": r.SHA256, "requested_seconds": seconds, "audio": false, "original_media": false, "created_at": float64(time.Now().UnixNano()) / 1e9}
	c.artifact = ioJSON(meta)
	if err = privateIOWrite(filepath.Join(c.broker.storage, id+".mp4"), data); err != nil {
		return err
	}
	return privateIOWrite(filepath.Join(c.broker.storage, id+".json"), c.artifact)
}
func (c *ioCall) readArtifact(ctx context.Context) (json.RawMessage, error) {
	id, _ := c.request["artifact_id"].(string)
	key, _ := c.request["request_key"].(string)
	offset, err := ioInteger(c.request["offset"])
	if err != nil || offset < 0 || !ioArtifactID.MatchString(id) {
		return nil, errToolDenied
	}
	size := 8192
	if value, ok := c.request["bytes"]; ok {
		size, err = ioInteger(value)
		if err != nil || size < 1 || int64(size) > ioBinaryLimit {
			return nil, errors.New("device_artifact_page_limit")
		}
	}
	raw, err := ioRead(c.broker.storage, id+".json", 1<<20)
	if err != nil {
		return nil, err
	}
	var meta map[string]any
	if json.Unmarshal(raw, &meta) != nil || meta["device_id"] != c.device || meta["request_key"] != key {
		return nil, errToolDenied
	}
	data, err := ioRead(c.broker.storage, id+".mp4", ioBinaryLimit)
	if err != nil {
		return nil, err
	}
	length, _ := meta["bytes"].(float64)
	at, _ := meta["created_at"].(float64)
	if int(length) != len(data) || meta["sha256"] != ioSHA(data) || float64(time.Now().UnixNano())/1e9-at > 7*86400 {
		return nil, errChanged
	}
	if offset > len(data) {
		return nil, errToolDenied
	}
	end := offset + size
	if end > len(data) {
		end = len(data)
	}
	ref, err := c.blob(data[offset:end])
	if err != nil {
		return nil, err
	}
	c.chunk = ioJSON(map[string]any{"id": id, "offset": offset, "bytes": len(data), "sha256": meta["sha256"], "chunk_ref": ref, "eof": end == len(data)})
	return c.chunk, nil
}

type boundedIOWriter struct {
	buffer   bytes.Buffer
	limit    int64
	overflow bool
}

func (w *boundedIOWriter) Write(p []byte) (int, error) {
	if int64(w.buffer.Len()+len(p)) > w.limit {
		w.overflow = true
		return 0, errors.New("device_io_response_limit")
	}
	return w.buffer.Write(p)
}
func runFixedADB(ctx context.Context, client string, args []string, limit int64, timeout time.Duration) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(bounded, client, args...)
	cmd.WaitDelay = time.Second
	configureIOCommand(cmd)
	for _, key := range []string{"SystemRoot", "WINDIR", "TEMP", "TMP", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	out := &boundedIOWriter{limit: limit}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	err := runIOCommand(cmd)
	var pending *ioCleanupError
	if errors.As(err, &pending) {
		return nil, pending
	}
	if out.overflow {
		return nil, errors.New("device_io_response_limit")
	}
	if err != nil {
		return nil, errors.New("device_command_unavailable")
	}
	return out.buffer.Bytes(), nil
}
