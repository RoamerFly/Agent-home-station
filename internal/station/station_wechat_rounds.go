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

	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
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
	Sequence       uint64    `json:"sequence"`
	At             time.Time `json:"at"`
	Thread         string    `json:"thread,omitempty"`
	Message        string    `json:"message,omitempty"`
	RelatedMessage string    `json:"related_message,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	Event          string    `json:"event"`
	Category       string    `json:"category"`
	Status         string    `json:"status"`
	Tool           string    `json:"tool,omitempty"`
	Receipt        string    `json:"receipt,omitempty"`
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
	if entry.Event == "" || len(entry.ThreadID) > 256 || len(entry.MessageID) > 512 ||
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
	if len(entry.RelatedMessageID) <= 512 {
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
	previews := []map[string]string{}
	if query.Get("include_messages") == "true" && worker != nil {
		memoryDir := filepath.Join(worker.dir, "memory")
		memoryRoot, rootErr := workspacefs.OpenPath(memoryDir)
		if rootErr == nil {
			defer memoryRoot.Close()
			file, fileErr := workspacefs.OpenRegular(memoryRoot, "memory.sqlite")
			if fileErr == nil {
				file.Close()
				memory, memoryErr := companion.NewStore(memoryDir)
				native, nativeErr := s.WorkerSender(r.Context(), id)
				if memoryErr == nil {
					defer memory.Close()
					if nativeErr == nil && native.Account() == connection.AccountID {
						seen := map[string]bool{}
						for _, step := range steps {
							key := step.Thread + "\x00" + step.Message
							worker.mu.Lock()
							grant := worker.policy.HistoryGrants[step.Thread]
							worker.mu.Unlock()
							if seen[key] || step.Message == "" || grant.IsZero() {
								continue
							}
							seen[key] = true
							original, readErr := native.Message(r.Context(), step.Message)
							if readErr != nil || original.ID != step.Message || original.ThreadID != step.Thread ||
								original.AccountID != connection.AccountID || original.Direction != "incoming" || original.Type != 1 {
								continue
							}
							in := companion.Input{Channel: "wechat", Account: original.AccountID, Thread: original.ThreadID,
								Sender: original.SenderID, TraceID: original.ID, MessageTime: original.CreatedAt, OriginalText: &original.Body,
								Source: &companion.SourceRef{MessageID: original.ID, ServerID: original.ServerID}}
							if _, visible := memory.SourceForInput(in); !visible {
								continue
							}
							worker.mu.Lock()
							current := worker.policy.HistoryGrants[step.Thread].Equal(grant)
							worker.mu.Unlock()
							if current {
								previews = append(previews, map[string]string{"thread": step.Thread, "message": step.Message, "text": original.Body})
							}
						}
					}
				}
			}
		}
	}
	websession.Respond(w, 200, map[string]any{"date": date, "steps": steps, "next_before": next, "messages": previews,
		"truncated": day.Dropped, "effective_limit": limit, "coverage": "station_runtime"})
}
