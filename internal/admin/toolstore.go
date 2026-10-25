package admin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"wechat-bot/internal/linuxnative"
)

type toolRecord struct {
	Key     string                 `json:"key"`
	Hash    string                 `json:"hash"`
	Account string                 `json:"account"`
	Thread  string                 `json:"thread"`
	Kind    string                 `json:"kind"`
	Status  linuxnative.ToolStatus `json:"status"`
	Created time.Time              `json:"created"`
}
type stagedUpload struct {
	linuxnative.UploadSource
	Account       string `json:"account"`
	Kind          string `json:"kind"`
	AITitle       string `json:"ai_title,omitempty"`
	AIDescription string `json:"ai_description,omitempty"`
	AIEnabled     bool   `json:"ai_enabled"`
}
type toolData struct {
	Version int                     `json:"version"`
	Sends   map[string]toolRecord   `json:"sends"`
	Uploads map[string]stagedUpload `json:"uploads"`
}
type toolStore struct {
	mu   sync.Mutex
	path string
	data toolData
}

func newToolStore(dir string) (*toolStore, error) {
	s := &toolStore{path: filepath.Join(dir, "tool-state.json"), data: toolData{Version: 1, Sends: map[string]toolRecord{}, Uploads: map[string]stagedUpload{}}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil || len(b) > 2<<20 || json.Unmarshal(b, &s.data) != nil || s.data.Version != 1 || s.data.Sends == nil || s.data.Uploads == nil || len(s.data.Sends) > 1000 || len(s.data.Uploads) > 1000 {
		return nil, errors.New("发送任务记录不可用，未自动重置")
	}
	return s, nil
}
func (s *toolStore) change(update func(*toolData) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(s.data)
	rollback := func() { var old toolData; _ = json.Unmarshal(b, &old); s.data = old }
	if err := update(&s.data); err != nil {
		rollback()
		return err
	}
	out, err := json.Marshal(s.data)
	if err != nil || len(out) > 2<<20 {
		rollback()
		return errors.New("发送任务记录达到保存上限")
	}
	if err = writePrivate(s.path, out); err != nil {
		rollback()
		return err
	}
	return nil
}
func (s *toolStore) claim(record toolRecord) (toolRecord, bool, error) {
	var out toolRecord
	created := false
	err := s.change(func(data *toolData) error {
		if old, ok := data.Sends[record.Key]; ok {
			if old.Hash != record.Hash || old.Account != record.Account {
				return errors.New("操作编号已用于不同内容；请先查询原操作")
			}
			out = old
			return nil
		}
		if len(data.Sends) >= 1000 {
			return errors.New("发送任务达到上限，请先归档已完成任务")
		}
		data.Sends[record.Key] = record
		out = record
		created = true
		return nil
	})
	return out, created, err
}
func (s *toolStore) update(key string, status linuxnative.ToolStatus) error {
	return s.change(func(data *toolData) error {
		record, ok := data.Sends[key]
		if !ok || (status.ThreadID != "" && status.ThreadID != record.Thread) {
			return errors.New("发送任务与会话不匹配")
		}
		status.Kind = record.Kind
		status.ThreadID = record.Thread
		record.Status = status
		data.Sends[key] = record
		return nil
	})
}
func (s *toolStore) record(key, account string) (toolRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data.Sends[key]
	return r, ok && r.Account == account
}
func (s *toolStore) byID(id, account string) (toolRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.data.Sends {
		if r.Status.ID == id && r.Account == account {
			return r, true
		}
	}
	return toolRecord{}, false
}
func (s *toolStore) upload(source stagedUpload) error {
	return s.change(func(data *toolData) error {
		if len(data.Uploads) >= 1000 {
			return errors.New("附件记录达到上限")
		}
		data.Uploads[source.ID] = source
		return nil
	})
}
func (s *toolStore) source(id, account, kind string) (stagedUpload, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out, ok := s.data.Uploads[id]
	return out, ok && out.Account == account && out.Kind == kind
}
