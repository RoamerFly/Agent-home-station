package linuxnative

import (
	"context"
	"net/http"
)

type guardedTransport struct {
	next  http.RoundTripper
	check func(context.Context) error
}

func (t guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	if err := t.check(req.Context()); err != nil {
		return nil, err
	}
	return t.next.RoundTrip(req)
}

// GuardRequests binds a fresh account sender to its verified native instance.
// Call before publishing the sender. The guard must use an independent client.
func (s *Sender) GuardRequests(check func(context.Context) error) {
	client := *s.client
	client.Transport = guardedTransport{next: client.Transport, check: check}
	s.client = &client
}
