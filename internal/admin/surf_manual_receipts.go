package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

// One-off owner shares have a private claim before the native submission.
// Import only independently verified receipts. This never sends or retries.
func (m *Manager) importOwnerSurfReceipts(ctx context.Context) {
	native, ok := m.native.(nativeTools)
	if !ok {
		return
	}
	entries, err := os.ReadDir(filepath.Join(m.dir, "manual-surf-claims"))
	if err != nil {
		return
	}
	if len(entries) > 50 {
		return
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(m.dir, "manual-surf-claims", entry.Name())
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, (16<<10)+1))
		f.Close()
		if err != nil || len(data) > 16<<10 {
			continue
		}
		var claim struct {
			SessionID   string `json:"session_id"`
			CandidateID string `json:"candidate_id"`
			Thread      string `json:"thread"`
			Key         string `json:"key"`
		}
		if json.Unmarshal(data, &claim) != nil {
			continue
		}
		digest := sha256.Sum256([]byte("manual-surf-v1\x00" + claim.SessionID + "\x00" + claim.CandidateID + "\x00" + claim.Thread))
		if claim.Key != "manual-surf-"+hex.EncodeToString(digest[:])[:40] || entry.Name() != claim.Key+".json" {
			continue
		}
		already := false
		for _, event := range m.Logs() {
			already = already || event.Event == "message_sent" && event.MessageID == claim.SessionID && event.Reason == "owner_surf_share_accepted"
		}
		if already {
			continue
		}
		var title, raw string
		for _, session := range m.surfHistory.snapshot().Sessions {
			if session.ID != claim.SessionID || session.Thread != claim.Thread {
				continue
			}
			for _, item := range session.Candidates {
				if item.ID == claim.CandidateID {
					title, raw = item.Title, item.URL
				}
			}
		}
		if title == "" || raw == "" {
			continue
		}
		status, err := native.LookupSend(ctx, claim.Key)
		if err != nil || status.State != "accepted" || status.ServerID == "" || status.ThreadID != claim.Thread {
			continue
		}
		// The fixed key binds the owner-selected candidate and destination;
		// do not infer a successful send from model memory or a claim file.
		m.surfHistory.mu.Lock()
		for i := range m.surfHistory.data.Sessions {
			session := &m.surfHistory.data.Sessions[i]
			if session.ID == claim.SessionID && session.Thread == claim.Thread {
				session.Selected = claim.CandidateID
			}
		}
		err = m.surfHistory.saveLocked()
		m.surfHistory.mu.Unlock()
		if err != nil {
			continue
		}
		in := companion.Input{Channel: "wechat", Account: m.native.Account(), Thread: claim.Thread, TraceID: claim.SessionID, Mode: "proactive", Surf: true}
		c := m.Config().Companion
		without := c
		without.Permissions.Remember = false
		if err = m.memory.Observe(in, without, time.Now()); err != nil {
			continue
		}
		if err = m.memory.ObserveMedia(in, c, companion.MediaMemory{Key: claim.Key, JobID: status.ID, Kind: "share", Title: title, URL: raw, State: "accepted"}, time.Now()); err != nil {
			continue
		}
		m.record(bot.Record{Event: "message_sent", Category: "sent", ThreadID: claim.Thread, MessageID: claim.SessionID, Content: "[分享卡片] " + title + "\n" + raw, Status: "accepted", Reason: "owner_surf_share_accepted", SendID: status.ID, Origin: "surf"})
	}
}
