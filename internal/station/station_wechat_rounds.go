package station

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

const (
	wechatRoundLimit = 2000
	wechatRoundBytes = 4 << 20
	wechatDateLayout = "2006-01-02"
)

var wechatJournalZone = time.FixedZone("CST", 8*60*60)

type wechatRoundStep struct {
	Sequence            uint64    `json:"sequence"`
	At                  time.Time `json:"at"`
	Thread              string    `json:"thread,omitempty"`
	Message             string    `json:"message,omitempty"`
	RelatedMessage      string    `json:"related_message,omitempty"`
	Reason              string    `json:"reason,omitempty"`
	Event               string    `json:"event"`
	Category            string    `json:"category"`
	Status              string    `json:"status"`
	Tool                string    `json:"tool,omitempty"`
	Receipt             string    `json:"receipt,omitempty"`
	CallID              string    `json:"call_id,omitempty"`
	ToolStage           string    `json:"tool_stage,omitempty"`
	ToolCode            string    `json:"tool_code,omitempty"`
	ToolAttempt         int       `json:"tool_attempt,omitempty"`
	ToolRequests        int       `json:"tool_requests,omitempty"`
	ToolRetries         int       `json:"tool_retries,omitempty"`
	ToolHTTPStatus      int       `json:"tool_http_status,omitempty"`
	DurationMS          int64     `json:"duration_ms,omitempty"`
	ToolTrackingVersion int       `json:"tool_tracking_version,omitempty"`
}

type wechatRoundMessage struct {
	Thread  string `json:"thread"`
	Message string `json:"message"`
	State   string `json:"state"`
	Text    string `json:"text,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Sender  string `json:"sender,omitempty"`
}

type wechatRoundDay struct {
	Dropped bool              `json:"dropped"`
	Steps   []wechatRoundStep `json:"steps"`
}

func readWechatRoundDay(root *os.Root, name string) (wechatRoundDay, error) {
	var day wechatRoundDay
	data, err := readLegacyImportFile(root, name, wechatRoundBytes)
	if errors.Is(err, os.ErrNotExist) {
		return day, nil
	}
	if err != nil {
		return day, err
	}
	if json.Unmarshal(data, &day) != nil || len(day.Steps) > wechatRoundLimit {
		return wechatRoundDay{}, errors.New("invalid round journal")
	}
	return day, nil
}

func wechatRoundFile(id, account, date string) string {
	digest := sha256.Sum256([]byte(id + "\x00" + account))
	return "rounds-" + hex.EncodeToString(digest[:]) + "-" + date + ".json"
}

func (w *stationWechatWorker) recordRound(id, account string, at time.Time, entry bot.Record) error {
	// Keep structured evidence only. Model text, prompts, URLs, and
	// retrieved contents do not belong in the round journal.
	if entry.Event == "" || len(entry.ThreadID) > 256 || len(entry.MessageID) > 8192 ||
		len(entry.Event) > 128 || len(entry.Category) > 64 || len(entry.Status) > 128 ||
		len(entry.Tool) > 160 || len(entry.SendID) > 128 {
		return nil
	}
	w.roundMu.Lock()
	defer w.roundMu.Unlock()
	root, err := workspacefs.OpenPath(w.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	name := wechatRoundFile(id, account, at.In(wechatJournalZone).Format(wechatDateLayout))
	day, err := readWechatRoundDay(root, name)
	if err != nil {
		return err
	}
	step := wechatRoundStep{Sequence: 1, At: at.UTC(), Thread: entry.ThreadID, Message: entry.MessageID,
		Event: entry.Event, Category: entry.Category, Status: entry.Status, Tool: entry.Tool, Receipt: entry.SendID}
	if entry.Event == "message_received" {
		step.ToolTrackingVersion = 1
	}
	if len(entry.CallID) <= 128 && identity.ValidPathID(entry.CallID) {
		step.CallID = entry.CallID
	}
	step.ToolStage, step.ToolCode = wechatRoundCode(entry.ToolStage), wechatRoundCode(entry.ToolCode)
	step.ToolAttempt, step.ToolRequests, step.ToolRetries = max(0, entry.ToolAttempt), max(0, entry.ToolRequests), max(0, entry.ToolRetries)
	step.DurationMS = max(0, entry.DurationMS)
	if entry.ToolHTTPStatus >= 100 && entry.ToolHTTPStatus <= 599 {
		step.ToolHTTPStatus = entry.ToolHTTPStatus
	}
	if len(entry.RelatedMessageID) <= 8192 {
		step.RelatedMessage = entry.RelatedMessageID
	}
	// Keep normalized reason codes for the old round projection, never error
	// text, prompt fragments or a model-generated explanation.
	if len(entry.Reason) <= 160 && strings.IndexFunc(entry.Reason, func(r rune) bool { return r != '_' && (r < 'a' || r > 'z') && (r < '0' || r > '9') }) == -1 {
		step.Reason = entry.Reason
	}
	if n := len(day.Steps); n != 0 {
		step.Sequence = day.Steps[n-1].Sequence + 1
		if n == wechatRoundLimit {
			day.Steps, day.Dropped = day.Steps[1:], true
		}
	}
	day.Steps = append(day.Steps, step)
	data, err := json.Marshal(day)
	if err != nil {
		return err
	}
	if len(data) > wechatRoundBytes {
		day.Steps, day.Dropped = day.Steps[len(day.Steps)/2:], true
		data, err = json.Marshal(day)
		if err != nil {
			return err
		}
	}
	temp := ".rounds-" + identity.RandomToken()
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, err = file.Write(data)
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	return root.Rename(temp, name)
}

func wechatRoundCode(value string) string {
	if len(value) > 160 || strings.IndexFunc(value, func(r rune) bool { return r != '_' && (r < 'a' || r > 'z') && (r < '0' || r > '9') }) != -1 {
		return ""
	}
	return value
}

func (s *stationWechat) readExecutionRounds(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("connection")
	if !identity.ValidPathID(id) {
		websession.Fail(w, 400, "连接编号无效")
		return
	}
	query := r.URL.Query()
	date := query.Get("date")
	if date == "" {
		date = time.Now().In(wechatJournalZone).Format(wechatDateLayout)
	}
	_, dateErr := time.Parse(wechatDateLayout, date)
	thread, status := query.Get("thread"), query.Get("status")
	message := query.Get("message")
	onlyErrors := query.Get("errors") == "true"
	before := ^uint64(0)
	var cursorErr error
	if value := query.Get("before"); value != "" {
		before, cursorErr = strconv.ParseUint(value, 10, 64)
	}
	limit := 40
	var limitErr error
	if value := query.Get("limit"); value != "" {
		limit, limitErr = strconv.Atoi(value)
	}
	if dateErr != nil || cursorErr != nil || before == 0 || limitErr != nil || limit < 1 || limit > 100 ||
		len(thread) > 256 || len(status) > 128 || len(message) > 8192 || strings.ContainsRune(thread, 0) {
		websession.Fail(w, 400, "日期、筛选或分页参数无效。每页支持 1–100 条。")
		return
	}
	connection, err := s.stationWechatNative.Inspect(r.Context(), id)
	if err != nil || connection.ID != id || connection.AccountID == "" {
		websession.Fail(w, 404, "无法核对当前微信连接")
		return
	}
	worker, err := s.worker(id, false)
	var day wechatRoundDay
	if err == nil && worker != nil {
		worker.roundMu.Lock()
		root, openErr := workspacefs.OpenPath(worker.dir)
		err = openErr
		if openErr == nil {
			day, err = readWechatRoundDay(root, wechatRoundFile(id, connection.AccountID, date))
			root.Close()
		}
		worker.roundMu.Unlock()
	}
	if err != nil {
		websession.Fail(w, 503, "执行回合日志不可读，原记录保留")
		return
	}
	steps := make([]wechatRoundStep, 0, limit)
	next := ""
	for i := len(day.Steps) - 1; i >= 0; i-- {
		step := day.Steps[i]
		if step.Sequence >= before || thread != "" && step.Thread != thread || message != "" && step.Message != message || status != "" && step.Status != status || onlyErrors && step.Category != "error" {
			continue
		}
		if len(steps) == limit {
			next = strconv.FormatUint(steps[len(steps)-1].Sequence, 10)
			break
		}
		steps = append(steps, step)
	}
	previews := []wechatRoundMessage{}
	if query.Get("include_messages") == "true" && worker != nil {
		previews = s.wechatRoundMessages(r, id, connection.AccountID, worker, steps)
	}
	websession.Respond(w, 200, map[string]any{"date": date, "steps": steps, "next_before": next, "messages": previews,
		"truncated": day.Dropped, "effective_limit": limit, "coverage": "station_runtime"})
}

func (s *stationWechat) wechatRoundMessages(r *http.Request, id, account string, worker *stationWechatWorker, steps []wechatRoundStep) []wechatRoundMessage {
	previews := []wechatRoundMessage{}
	seen := map[string]bool{}
	for _, step := range steps {
		key := step.Thread + "\x00" + step.Message
		if step.Message != "" && !seen[key] {
			seen[key] = true
			previews = append(previews, wechatRoundMessage{Thread: step.Thread, Message: step.Message, State: "source_unavailable"})
		}
	}
	// The owner is viewing a message already accepted by this conversation's
	// worker. This does not grant the Agent a historical-search capability.
	// Resolve only these exact journal message IDs and require their live memory
	// source, so clear/forget/conflict is never bypassed by refetching native data.
	memoryDir := filepath.Join(worker.dir, "memory")
	memoryRoot, rootErr := workspacefs.OpenPath(memoryDir)
	if rootErr != nil {
		return previews
	}
	defer memoryRoot.Close()
	file, fileErr := workspacefs.OpenRegular(memoryRoot, "memory.sqlite")
	if fileErr != nil {
		return previews
	}
	file.Close()
	memory, memoryErr := companion.NewStore(memoryDir)
	if memoryErr != nil {
		return previews
	}
	defer memory.Close()
	native, nativeErr := s.WorkerSender(r.Context(), id)
	if nativeErr != nil || native == nil || native.Account() != account {
		return previews
	}
	pendingInputs := map[int]companion.Input{}
	pendingGrants := map[int]time.Time{}
	for index := range previews {
		preview := &previews[index]
		worker.mu.Lock()
		grant := worker.policy.Grants[preview.Thread]
		worker.mu.Unlock()
		if grant.IsZero() {
			preview.State = "no_conversation_grant"
			continue
		}
		original, err := native.Message(r.Context(), preview.Message)
		if err != nil || original.ID != preview.Message || original.ThreadID != preview.Thread || original.AccountID != account || original.Direction != "incoming" {
			continue
		}
		when, err := time.Parse(time.RFC3339Nano, original.CreatedAt)
		if err != nil || when.Before(grant) {
			preview.State = "no_conversation_grant"
			continue
		}
		text, kind, literal := wechatRoundMessageText(original)
		in := companion.Input{Channel: "wechat", Account: account, Thread: preview.Thread,
			Sender: original.SenderID, TraceID: preview.Message, MessageTime: original.CreatedAt, OriginalText: &literal,
			Source: &companion.SourceRef{MessageID: original.ID, ServerID: original.ServerID}}
		source, visible := memory.SourceForInput(in)
		if !visible || source.MessageType != 0 && source.MessageType != original.Type {
			preview.State = "changed_or_forgotten"
			continue
		}
		in.Source = source
		if kind != "text" {
			// A media event's canonical user text is empty. Display a descriptive
			// native title only when the separately retained transport hash proves
			// this is still the original media packet, rather than changed XML.
			stored, readErr := memory.ReadEvent(r.Context(), []string{companion.ScopeID("wechat", account, preview.Thread)}, source.EventID)
			bodyHash := sha256.Sum256([]byte(original.Body))
			verifiedMedia := false
			for _, media := range stored.IncomingMedia {
				verifiedMedia = verifiedMedia || media.Source.MessageID == original.ID && media.Source.BodyHash == hex.EncodeToString(bodyHash[:]) && media.Source.MessageType == original.Type
			}
			if readErr != nil || !verifiedMedia {
				// Old records can identify the received type but cannot attest a
				// subsequently supplied file/share title. Keep only that type label.
				generic := original
				generic.Body, generic.Link, generic.Attachments = "", nil, nil
				text, kind, _ = wechatRoundMessageText(generic)
			}
		}
		worker.mu.Lock()
		current := !grant.IsZero() && worker.policy.Grants[preview.Thread].Equal(grant)
		worker.mu.Unlock()
		if !current {
			preview.State = "no_conversation_grant"
			continue
		}
		if _, visible := memory.SourceForInput(in); !visible || r.Context().Err() != nil {
			preview.State = "changed_or_forgotten"
			continue
		}
		preview.State, preview.Text, preview.Kind, preview.Sender = "available", text, kind, original.SenderID
		pendingInputs[index], pendingGrants[index] = in, grant
	}
	// Recheck the entire page after the last native read: an earlier message's
	// grant or source may have changed while later originals were being loaded.
	fresh, inspectErr := s.stationWechatNative.Inspect(r.Context(), id)
	accountCurrent := inspectErr == nil && fresh.ID == id && fresh.AccountID == account
	for index, in := range pendingInputs {
		worker.mu.Lock()
		current := worker.policy.Grants[in.Thread].Equal(pendingGrants[index])
		worker.mu.Unlock()
		_, visible := memory.SourceForInput(in)
		if !accountCurrent || !current || !visible || r.Context().Err() != nil {
			previews[index].State, previews[index].Text, previews[index].Kind, previews[index].Sender = "changed_or_forgotten", "", "", ""
			if !current {
				previews[index].State = "no_conversation_grant"
			}
		}
	}
	return previews
}

func wechatRoundMessageText(message linuxnative.Message) (text, kind, literal string) {
	if message.Type == 1 || message.IsQuotedText() {
		if utf8.ValidString(message.Body) {
			return message.Body, "text", message.Body
		}
		return "[文字编码不可读]", "text", message.Body
	}
	switch message.Type {
	case 3:
		return "[收到图片]", "image", ""
	case 34:
		return "[收到微信语音]", "voice", ""
	case 43:
		return "[收到视频]", "video", ""
	case 47:
		return "[收到表情包]", "sticker", ""
	case 49:
		if name := message.FileAttachmentName(); name != "" {
			return "[文件] " + name, "file", ""
		}
		if message.AppMessageType != nil && *message.AppMessageType == 6 {
			return "[收到文件]", "file", ""
		}
		if message.Link != nil && strings.TrimSpace(message.Link.Title) != "" && utf8.ValidString(message.Link.Title) {
			return "[分享] " + message.Link.Title, "share", ""
		}
		return "[收到分享卡片/视频号]", "share", ""
	}
	return "[收到非文字消息]", "other", ""
}
