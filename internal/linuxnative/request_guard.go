package linuxnative

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

type guardedTransport struct {
	next  http.RoundTripper
	check func(*http.Request) error
}

func (t guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	if err := t.check(req); err != nil {
		return nil, err
	}
	return t.next.RoundTrip(req)
}

// GuardRequests binds a fresh account sender to its verified native instance.
// Call before publishing the sender. The guard must use an independent client.
func (s *Sender) GuardRequests(check func(context.Context) error) {
	s.guardHTTPRequests(func(req *http.Request) error { return check(req.Context()) })
}

// ErrWriteNotAdmitted proves the request did not reach the native transport.
var ErrWriteNotAdmitted = errors.New("native write was not admitted")

// GuardChanges checks account writes at the transport boundary. Reads,
// inbound downloads, and stopping an existing delivery remain available.
func (s *Sender) GuardChanges(check func(context.Context) error) {
	prefix := s.accountPath() + "/"
	s.guardHTTPRequests(func(req *http.Request) error {
		if req.Method == http.MethodGet || req.Method == http.MethodHead {
			return nil
		}
		path, scoped := strings.CutPrefix(req.URL.EscapedPath(), prefix)
		if scoped && req.Method == http.MethodPost {
			parts := strings.Split(path, "/")
			if len(parts) == 3 && parts[1] != "" && (parts[0] == "jobs" && parts[2] == "cancel" ||
				parts[0] == "messages" && (parts[2] == "media-transfers" || parts[2] == "channels-share-url")) {
				return nil
			}
		}
		if err := check(req.Context()); err != nil {
			return ErrWriteNotAdmitted
		}
		return nil
	})
}

func (s *Sender) guardHTTPRequests(check func(*http.Request) error) {
	client := *s.client
	client.Transport = guardedTransport{next: client.Transport, check: check}
	s.client = &client
}
