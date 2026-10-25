package linuxnative

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Status struct {
	Authenticated     bool   `json:"authenticated"`
	Connection        string `json:"connection"`
	IncomingTransport string `json:"incoming_transport"`
}

func (s *Sender) Status(ctx context.Context) (Status, error) {
	var raw struct {
		Authenticated bool   `json:"authenticated"`
		Connection    string `json:"connection"`
		Transport     string `json:"incomingTransport"`
	}
	if err := s.readJSON(ctx, s.accountPath(), &raw); err != nil {
		return Status{Connection: "unavailable"}, err
	}
	return Status{raw.Authenticated, raw.Connection, raw.Transport}, nil
}

type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Sender) Groups(ctx context.Context, search string) ([]Group, error) {
	if len(search) > 256 {
		return nil, errors.New("group search too long")
	}
	var page struct {
		Items []struct {
			ID     string `json:"id"`
			RoomID string `json:"roomId"`
			Name   string `json:"displayName"`
		} `json:"items"`
	}
	if err := s.readJSON(ctx, s.accountPath()+"/groups?limit=100&search="+url.QueryEscape(search), &page); err != nil {
		return nil, err
	}
	out := make([]Group, 0, len(page.Items))
	for _, g := range page.Items {
		if g.RoomID != "" {
			g.ID = g.RoomID
		}
		out = append(out, Group{g.ID, g.Name})
	}
	return out, nil
}

type LoginOperation struct {
	ID           string `json:"id"`
	State        string `json:"state"`
	QRAvailable  bool   `json:"qr_available"`
	QRGeneration int    `json:"qr_generation"`
	Code         string `json:"code"`
}

var ErrLoginOutcomeUnknown = errors.New("login submission outcome unknown")

type nativeOperation struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Result struct {
		QRAvailable  bool            `json:"qrAvailable"`
		QRGeneration int             `json:"qrGeneration"`
		Code         json.RawMessage `json:"code"`
	} `json:"result"`
}

func operationView(op nativeOperation) LoginOperation {
	code := ""
	if len(op.Result.Code) > 0 && len(op.Result.Code) < 128 {
		var value string
		if json.Unmarshal(op.Result.Code, &value) == nil {
			code = value
		} else {
			var n int64
			if json.Unmarshal(op.Result.Code, &n) == nil {
				code = strconv.FormatInt(n, 10)
			}
		}
	}
	return LoginOperation{op.ID, op.State, op.Result.QRAvailable, op.Result.QRGeneration, code}
}

func (s *Sender) BeginLogin(ctx context.Context, key string) (LoginOperation, error) {
	// Create only if this exact account is absent. No arbitrary account/profile
	// mutation endpoints are exposed through the web dashboard.
	response, err := s.do(ctx, http.MethodGet, s.accountPath(), nil, "")
	if err != nil {
		return LoginOperation{}, err
	}
	status := response.StatusCode
	response.Body.Close()
	if status == http.StatusNotFound {
		body, _ := json.Marshal(map[string]string{"id": s.account, "alias": s.account})
		response, err = s.do(ctx, http.MethodPost, "/v1/accounts", body, "")
		if err != nil {
			return LoginOperation{}, err
		}
		response.Body.Close()
		if response.StatusCode != 200 && response.StatusCode != 201 {
			return LoginOperation{}, errors.New("native account creation rejected")
		}
	} else if status != 200 {
		return LoginOperation{}, errors.New("native account unavailable")
	}
	body, _ := json.Marshal(map[string]any{"strategy": "qr", "forceQr": true, "allowQr": true, "timeoutSeconds": 300, "computerName": "WechatBot", "qrRefresh": map[string]any{"mode": "manual", "maxRefreshes": 0}})
	response, err = s.do(ctx, http.MethodPost, s.accountPath()+"/auth/login", body, key)
	if err != nil {
		return LoginOperation{}, ErrLoginOutcomeUnknown
	}
	defer response.Body.Close()
	var op nativeOperation
	if response.StatusCode != 202 {
		return LoginOperation{}, errors.New("login operation unavailable")
	}
	if !decodeBounded(response.Body, &op) || !safeID(op.ID) {
		return LoginOperation{}, ErrLoginOutcomeUnknown
	}
	return operationView(op), nil
}

func (s *Sender) LoginStatus(ctx context.Context, id string) (LoginOperation, error) {
	if !safeID(id) {
		return LoginOperation{}, errors.New("invalid login operation")
	}
	var op nativeOperation
	if err := s.readJSON(ctx, s.accountPath()+"/operations/"+id, &op); err != nil {
		return LoginOperation{}, err
	}
	if op.ID != id {
		return LoginOperation{}, errors.New("login operation mismatch")
	}
	return operationView(op), nil
}

func (s *Sender) LoginQR(ctx context.Context, id string, generation int) ([]byte, error) {
	if !safeID(id) || generation < 0 || generation > 100 {
		return nil, errors.New("invalid QR operation")
	}
	response, err := s.do(ctx, http.MethodGet, s.accountPath()+"/operations/"+id+"/qr?generation="+strconv.Itoa(generation), nil, "")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, errors.New("QR unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		return nil, errors.New("invalid QR image")
	}
	return data, nil
}

func (s *Sender) do(ctx context.Context, method, path string, body []byte, key string) (*http.Response, error) {
	// Caller owns and closes response.Body; this request context stays alive
	// until the response body has been consumed or the parent's deadline ends.
	u := s.base
	relative, err := url.Parse(path)
	if err != nil {
		return nil, errors.New("invalid native path")
	}
	u.Path, u.RawPath, u.RawQuery = relative.Path, relative.RawPath, relative.RawQuery
	request, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid native request")
	}
	request.Header.Set("Authorization", "Bearer "+s.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	request.GetBody = nil
	return s.client.Do(request)
}

// NewAdmin returns a client with the same loopback-only restrictions and an
// overall request deadline for account operations/QR bodies.
func NewAdmin(config Config) (*Sender, error) {
	s, err := New(config)
	if err != nil {
		return nil, err
	}
	s.client.Timeout = 30 * time.Second
	return s, nil
}
