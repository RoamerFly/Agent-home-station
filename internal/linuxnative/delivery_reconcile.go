package linuxnative

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Expose only matching metadata, never encrypted media URLs or keys.
func addVoiceMatchMetadata(row, raw map[string]any) {
	body, _ := raw["body"].(string)
	if len(body) > 64<<10 {
		return
	}
	d := xml.NewDecoder(strings.NewReader(body))
	for {
		t, e := d.Token()
		if e != nil {
			return
		}
		tag, ok := t.(xml.StartElement)
		if !ok || tag.Name.Local != "voicemsg" {
			continue
		}
		for _, a := range tag.Attr {
			switch a.Name.Local {
			case "clientmsgid":
				row["voice_client_id"] = a.Value
			case "md5":
				row["voice_md5"] = strings.ToLower(a.Value)
			case "voicelength":
				row["voice_duration_ms"] = a.Value
			}
		}
		return
	}
}

type DeliveryReconciliation struct {
	Complete  bool     `json:"scan_complete"`
	Matched   []int    `json:"matched_parts"`
	Confirmed [][2]any `json:"confirmed_voice_parts"`
	// This is a read observation, not proof that absence means non-delivery.
	Evidence string `json:"evidence"`
}

// Same-thread actual profile records are the same source as conversation view.
// Local queued intents and media-memory entries are deliberately not consulted.
func ReconcileMedia(ctx context.Context, n interface {
	Catalog(context.Context, string, string, string, string) (CatalogPage, error)
}, status ToolStatus) (DeliveryReconciliation, error) {
	out := DeliveryReconciliation{Matched: []int{}, Confirmed: [][2]any{}, Evidence: "synchronized_thread_messages"}
	seen := map[string]bool{}
	cursor := ""
	used := map[string]bool{}
	start, _ := time.Parse(time.RFC3339, status.CreatedAt)
	for _, p := range status.VoiceChunks {
		if p.SubmittedAtMS > 0 && (start.IsZero() || time.UnixMilli(p.SubmittedAtMS).Before(start)) {
			start = time.UnixMilli(p.SubmittedAtMS)
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		page, err := n.Catalog(ctx, "messages", status.ThreadID, "", cursor)
		if err != nil {
			return out, err
		}
		older := false
		for _, m := range page.Items {
			at, _ := time.Parse(time.RFC3339, str(m, "createdAt"))
			if !at.IsZero() && !start.IsZero() && at.Before(start.Add(-5*time.Second)) {
				older = true
				continue
			}
			direction, _ := m["direction"].(string)
			server, _ := m["serverId"].(string)
			thread, _ := m["threadId"].(string)
			sid, e := strconv.ParseUint(server, 10, 64)
			if direction != "outgoing" || e != nil || sid == 0 || thread != status.ThreadID || used[server] {
				continue
			}
			for _, part := range status.VoiceChunks {
				if part.HasReceipt || at.IsZero() || start.IsZero() {
					continue
				}
				client, hash := str(m, "voice_client_id"), str(m, "voice_md5")
				matched := part.ClientID != "" && client == part.ClientID || len(part.EncodedMD5) == 32 && hash == part.EncodedMD5
				if !matched || seen[strconv.Itoa(part.Index)] {
					continue
				}
				seen[strconv.Itoa(part.Index)] = true
				used[server] = true
				out.Matched = append(out.Matched, part.Index)
				out.Confirmed = append(out.Confirmed, [2]any{part.Index, server})
				break
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" || older {
			out.Complete = true
			return out, nil
		}
		if seen["cursor:"+*page.NextCursor] {
			return out, errors.New("消息分页游标循环，未完成核对")
		}
		cursor = *page.NextCursor
		seen["cursor:"+cursor] = true
	}
}
func str(m map[string]any, key string) string { v, _ := m[key].(string); return v }

// Reuse the original encrypted plan and accepted chunks. Read failure never
// grants a mutation. The explicit retry policy allows replay of unknown parts
// after a healthy scan, accepting the residual risk of delayed synchronization.
func (s *Sender) ReconcileMediaJob(ctx context.Context, id string) (ToolStatus, error) {
	status, err := s.ToolSendStatus(ctx, id)
	if err != nil {
		return status, err
	}
	if status.CancelRequested || status.State == "accepted" {
		return status, nil
	}
	if status.State == "queued" || status.State == "running" {
		return status, nil
	}
	proof, err := ReconcileMedia(ctx, s, status)
	if err != nil || !proof.Complete {
		return status, errors.New("群聊天记录核对未完成；等待连接恢复后继续")
	}
	body, _ := json.Marshal(map[string]any{"reconcileUncertain": true, "confirmedVoiceParts": proof.Confirmed})
	raw, err := s.toolJSON(ctx, "POST", s.accountPath()+"/jobs/"+url.PathEscape(id)+"/retry", "", bytes.NewReader(body), "application/json")
	if err != nil {
		return status, err
	}
	return s.decodeToolStatus(raw)
}
