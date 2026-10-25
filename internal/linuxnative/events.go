package linuxnative

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Event contains metadata only. Message content is fetched only after its
// conversation passes the bot's allowlist.
type Event struct {
	Cursor string `json:"cursor"`
	Kind   string `json:"kind"`
	Data   struct {
		MessageID string `json:"messageId"`
		ThreadID  string `json:"threadId"`
		Direction string `json:"direction"`
		Origin    string `json:"origin"`
	} `json:"data"`
}

type EventPage struct {
	Items      []Event `json:"items"`
	NextCursor string  `json:"nextCursor"`
}

type Message struct {
	ReplyToSelf    bool            `json:"-"`
	Quote          *QuoteReference `json:"quote,omitempty"`
	AppMessageType *int            `json:"appMessageType,omitempty"`
	Link           *LinkCard       `json:"link,omitempty"`
	ID             string          `json:"id"`
	AccountID      string          `json:"accountId"`
	ThreadID       string          `json:"threadId"`
	ServerID       string          `json:"serverId"`
	SenderID       string          `json:"senderId"`
	Type           int             `json:"type"`
	Direction      string          `json:"direction"`
	Body           string          `json:"body"`
	CreatedAt      string          `json:"createdAt"`
	Mentions       struct {
		State string `json:"state"`
		Self  *bool  `json:"self"`
	} `json:"mentions"`
}
type QuoteReference struct {
	ServerID string `json:"serverId"`
	Type     int    `json:"type"`
}

func (m Message) IsQuotedText() bool {
	return m.Type == 49 && (m.Quote != nil || m.AppMessageType != nil && *m.AppMessageType == 57)
}

type LinkCard struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	SourceName  string `json:"sourceName"`
}

func (s *Sender) Account() string { return s.account }

func (s *Sender) Identity(ctx context.Context) (string, error) {
	var status struct {
		ServerID string `json:"serverId"`
	}
	if err := s.readJSON(ctx, "/v1/status", &status); err != nil || !safeID(status.ServerID) {
		return "", errors.New("native server identity unavailable")
	}
	return status.ServerID, nil
}

func (s *Sender) Events(ctx context.Context, after string) (EventPage, error) {
	var page EventPage
	previous, err := cursor(after)
	if err != nil {
		return page, err
	}
	if err := s.readJSON(ctx, s.accountPath()+"/events?after="+after+"&limit=100", &page); err != nil {
		return page, err
	}
	for _, event := range page.Items {
		n, err := cursor(event.Cursor)
		if err != nil || n <= previous {
			return EventPage{}, errors.New("invalid native event order")
		}
		previous = n
	}
	if len(page.Items) > 100 || page.NextCursor != strconv.FormatInt(previous, 10) {
		return EventPage{}, errors.New("invalid native page cursor")
	}
	return page, nil
}

func (s *Sender) Message(ctx context.Context, id string) (Message, error) {
	var message Message
	if len(id) == 0 || len(id) > 8192 {
		return message, errors.New("invalid message ID")
	}
	if err := s.readJSON(ctx, s.accountPath()+"/messages/"+url.PathEscape(id), &message); err != nil {
		return message, err
	}
	if message.ID != id || message.AccountID != s.account {
		return Message{}, errors.New("native message identity mismatch")
	}
	return message, nil
}

func cursor(raw string) (int64, error) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != raw {
		return 0, errors.New("invalid native cursor")
	}
	return n, nil
}

func (s *Sender) readJSON(ctx context.Context, path string, value any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	u := s.base
	relative, err := url.Parse(path)
	if err != nil {
		return errors.New("invalid native path")
	}
	u.Path, u.RawPath, u.RawQuery = relative.Path, relative.RawPath, relative.RawQuery
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return errors.New("invalid native request")
	}
	request.Header.Set("Authorization", "Bearer "+s.token)
	response, err := s.client.Do(request)
	if err != nil {
		return errors.New("native read unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("native read rejected (check account, login or event cursor)")
	}
	// Event metadata and one text message fit within this bound; large media is
	// deliberately not read by the text bot.
	data, err := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	if err != nil || len(data) > 256<<10 || !json.Valid(data) {
		return errors.New("invalid native read response")
	}
	if err := json.Unmarshal(data, value); err != nil {
		return errors.New("invalid native read response")
	}
	return nil
}
