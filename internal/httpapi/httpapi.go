// Package httpapi exposes a small, authenticated local API for requesting text
// messages from a sending.Engine. An accepted response only means that the
// configured engine returned a validated acknowledgement; it does not claim a
// message was delivered to, read by, or otherwise seen by a WeChat user.
package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"wechat-bot/internal/sending"
)

const (
	maxBodyBytes = 16 << 10
	maxTextBytes = 4096
)

// Config controls the public boundary of the sender. Token must be a secret
// of at least 24 UTF-8 bytes. An empty allowlist intentionally permits nobody.
type Config struct {
	Token             string
	AllowedRecipients []string
	SendTimeout       time.Duration
}

type api struct {
	token   []byte
	allowed map[string]struct{}
	timeout time.Duration
	engine  sending.Engine
}

// New builds an HTTP handler. It does not establish a WeChat session or send a
// message. Callers must provide an engine that owns the transport lifecycle.
func New(config Config, engine sending.Engine) (http.Handler, error) {
	if engine == nil {
		return nil, errors.New("httpapi: engine is required")
	}
	if !utf8.ValidString(config.Token) || len(config.Token) < 24 {
		return nil, errors.New("httpapi: token must be valid UTF-8 and at least 24 bytes")
	}
	if config.SendTimeout <= 0 {
		return nil, errors.New("httpapi: send timeout must be positive")
	}

	allowed := make(map[string]struct{}, len(config.AllowedRecipients))
	for _, recipient := range config.AllowedRecipients {
		if !utf8.ValidString(recipient) || strings.TrimSpace(recipient) == "" {
			return nil, errors.New("httpapi: configured recipient must be nonblank valid UTF-8")
		}
		allowed[recipient] = struct{}{}
	}

	a := &api{
		token:   []byte(config.Token),
		allowed: allowed,
		timeout: config.SendTimeout,
		engine:  engine,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("GET /readyz", a.ready)
	mux.HandleFunc("POST /v1/messages/text", a.sendText)
	return requestID(mux), nil
}

func (a *api) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *api) ready(w http.ResponseWriter, _ *http.Request) {
	if !a.engine.Ready() {
		writeError(w, http.StatusServiceUnavailable, "transport_not_ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *api) sendText(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, "content_type_must_be_application_json")
		return
	}

	body, err := readBody(w, r)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request_body_too_large")
		return
	}
	if !utf8.Valid(body) {
		writeError(w, http.StatusBadRequest, "invalid_utf8")
		return
	}
	request, err := parseTextRequest(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	if strings.TrimSpace(request.Recipient) == "" || strings.TrimSpace(request.Text) == "" ||
		!utf8.ValidString(request.Recipient) || !utf8.ValidString(request.Text) {
		writeError(w, http.StatusUnprocessableEntity, "recipient_and_text_must_be_nonblank_utf8")
		return
	}
	if len(request.Text) > maxTextBytes {
		writeError(w, http.StatusUnprocessableEntity, "text_too_large")
		return
	}
	if _, ok := a.allowed[request.Recipient]; !ok {
		writeError(w, http.StatusForbidden, "recipient_not_allowed")
		return
	}
	if !a.engine.Ready() {
		writeError(w, http.StatusServiceUnavailable, "transport_not_ready")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), a.timeout)
	defer cancel()
	receipt, err := a.engine.SendText(ctx, request)
	if err != nil {
		a.writeSendError(w, ctx, err)
		return
	}
	if !validReceipt(receipt, request.Recipient) {
		writeError(w, http.StatusBadGateway, "invalid_upstream_acknowledgement")
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		Status          string `json:"status"`
		Recipient       string `json:"recipient"`
		ClientMessageID string `json:"client_message_id"`
		ServerMessageID string `json:"server_message_id"`
	}{"accepted", receipt.Recipient, receipt.ClientMessageID, receipt.ServerMessageID})
}

func (a *api) writeSendError(w http.ResponseWriter, ctx context.Context, err error) {
	switch {
	case errors.Is(err, sending.ErrNotReady):
		writeError(w, http.StatusServiceUnavailable, "transport_not_ready")
	case errors.Is(err, sending.ErrRejected):
		writeError(w, http.StatusUnprocessableEntity, "message_rejected")
	case errors.Is(err, sending.ErrOutcomeUnknown), errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
		// The caller must not automatically retry: the upstream may have accepted it.
		writeError(w, http.StatusGatewayTimeout, "outcome_unknown")
	default:
		// Do not return transport errors because they can contain secrets or payloads.
		writeError(w, http.StatusBadGateway, "upstream_failure")
	}
}

func (a *api) authorized(r *http.Request) bool {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return false
	}
	provided := []byte(strings.TrimPrefix(values[0], "Bearer "))
	if len(provided) != len(a.token) {
		return false
	}
	return subtle.ConstantTimeCompare(provided, a.token) == 1
}

func isJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func parseTextRequest(body []byte) (sending.TextRequest, error) {
	if len(body) == 0 || !strings.HasPrefix(strings.TrimSpace(string(body)), "{") {
		return sending.TextRequest{}, errors.New("request is not an object")
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var request sending.TextRequest
	if err := decoder.Decode(&request); err != nil {
		return sending.TextRequest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return sending.TextRequest{}, errors.New("request contains multiple JSON values")
	}
	return request, nil
}

func validReceipt(receipt sending.Receipt, recipient string) bool {
	return receipt.Recipient == recipient && strings.TrimSpace(receipt.Recipient) != "" &&
		strings.TrimSpace(receipt.ClientMessageID) != "" && strings.TrimSpace(receipt.ServerMessageID) != "" &&
		utf8.ValidString(receipt.Recipient) && utf8.ValidString(receipt.ClientMessageID) && utf8.ValidString(receipt.ServerMessageID)
}

func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var random [12]byte
		if _, err := rand.Read(random[:]); err == nil {
			w.Header().Set("X-Request-ID", hex.EncodeToString(random[:]))
		}
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
