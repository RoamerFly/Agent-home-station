package linuxnative

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

type AdminAction struct {
	Kind      string   `json:"kind"`
	ThreadID  string   `json:"thread_id"`
	Text      string   `json:"text"`
	Members   []string `json:"members"`
	Muted     bool     `json:"muted"`
	MessageID string   `json:"message_id"`
	ServerID  string   `json:"server_id"`
}
type ActionStatus struct {
	ID            string `json:"id"`
	State         string `json:"state"`
	Kind          string `json:"kind"`
	Code          string `json:"code,omitempty"`
	AccountID     string `json:"account_id,omitempty"`
	Action        string `json:"action,omitempty"`
	Target        string `json:"target,omitempty"`
	RemoteOutcome string `json:"remote_outcome,omitempty"`
}

type GroupNotification struct {
	Muted   *bool `json:"muted,omitempty"`
	InGroup *bool `json:"-"`
}

// GroupNotification reads the exact account-scoped, cached group record.
func (s *Sender) GroupNotification(ctx context.Context, id string) (GroupNotification, error) {
	var out GroupNotification
	if !safeThreadID(id) || !strings.HasSuffix(id, "@chatroom") {
		return out, errors.New("invalid group identity")
	}
	raw, err := s.toolJSON(ctx, http.MethodGet, s.accountPath()+"/groups/"+url.PathEscape(id), "", nil, "")
	if err != nil {
		return out, err
	}
	var detail struct {
		ID      string `json:"id"`
		RoomID  string `json:"roomId"`
		Muted   *bool  `json:"muted"`
		InGroup *bool  `json:"isInGroup"`
	}
	if json.Unmarshal(raw, &detail) != nil || detail.ID != id || detail.RoomID != id {
		return out, errors.New("native group identity mismatch")
	}
	return GroupNotification{Muted: detail.Muted, InGroup: detail.InGroup}, nil
}

func (s *Sender) ExecuteAction(ctx context.Context, input AdminAction, key string) (ActionStatus, error) {
	if !safeID(key) {
		return ActionStatus{}, errors.New("invalid action key")
	}
	method, path := "POST", ""
	body := map[string]any{}
	group := s.accountPath() + "/groups/" + url.PathEscape(input.ThreadID)
	switch input.Kind {
	case "rename":
		method, path = "PATCH", group
		body["name"] = input.Text
	case "my_name":
		method, path = "PUT", group+"/my-name"
		body["displayName"] = input.Text
	case "mute":
		method, path = "PUT", s.accountPath()+"/threads/"+url.PathEscape(input.ThreadID)+"/notifications"
		body["muted"] = input.Muted
	case "add_members":
		path = group + "/members"
		body["members"] = input.Members
	case "remove_members":
		method, path = "DELETE", group+"/members"
		body["members"] = input.Members
	case "leave":
		path = group + "/leave"
	case "clear_announcement":
		method, path = "DELETE", group+"/announcement"
	case "create":
		path = s.accountPath() + "/groups"
		body["members"] = input.Members
		if input.Text != "" {
			body["name"] = input.Text
		}
	case "recall":
		path = s.accountPath() + "/messages/" + url.PathEscape(input.MessageID) + "/recall"
		body["serverId"] = input.ServerID
	default:
		return ActionStatus{}, errors.New("unsupported action")
	}
	data, _ := json.Marshal(body)
	raw, err := s.toolJSON(ctx, method, path, key, bytes.NewReader(data), "application/json")
	if err != nil {
		return ActionStatus{}, err
	}
	return s.actionStatus(raw)
}
func (s *Sender) InspectAction(ctx context.Context, id string) (ActionStatus, error) {
	if !safeID(id) {
		return ActionStatus{}, errors.New("invalid operation")
	}
	raw, err := s.toolJSON(ctx, "GET", s.accountPath()+"/operations/"+url.PathEscape(id), "", nil, "")
	if err != nil {
		return ActionStatus{}, err
	}
	return s.actionStatus(raw)
}
func (s *Sender) actionStatus(raw []byte) (ActionStatus, error) {
	var operation struct {
		ID        string `json:"id"`
		AccountID string `json:"accountId"`
		State     string `json:"state"`
		Kind      string `json:"kind"`
		Result    struct {
			Code          string `json:"code"`
			Action        string `json:"action"`
			Target        string `json:"targetId"`
			Thread        string `json:"threadId"`
			RemoteOutcome string `json:"remoteOutcome"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &operation) != nil || !safeID(operation.ID) || operation.AccountID != "" && operation.AccountID != s.account {
		return ActionStatus{}, errors.New("invalid native operation")
	}
	if len(operation.Result.Code) > 128 {
		operation.Result.Code = "native_operation_result"
	}
	result := operation.Result
	if result.Thread != "" && result.Target != result.Thread {
		result.Target = ""
	}
	return ActionStatus{ID: operation.ID, State: operation.State, Kind: operation.Kind, Code: result.Code,
		AccountID: operation.AccountID, Action: result.Action, Target: result.Target, RemoteOutcome: result.RemoteOutcome}, nil
}
