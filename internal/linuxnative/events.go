package linuxnative

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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

type Attachment struct {
	Kind      string `json:"kind"`
	Filename  string `json:"filename"`
	Bytes     int64  `json:"bytes,omitempty"`
	State     string `json:"state"`
	MessageID string `json:"messageId,omitempty"`
}

type MessageMentions struct {
	State string   `json:"state"`
	Self  *bool    `json:"self"`
	IDs   []string `json:"ids,omitempty"`
}

func (m *MessageMentions) UnmarshalJSON(data []byte) error {
	var value struct {
		State   string   `json:"state"`
		Self    *bool    `json:"self"`
		IDs     []string `json:"ids"`
		UserIDs []string `json:"userIds"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*m = MessageMentions{State: value.State, Self: value.Self, IDs: value.IDs}
	if len(m.IDs) == 0 {
		m.IDs = value.UserIDs
	}
	return nil
}

type Message struct {
	PrivateQuote   string          `json:"-"`
	ResolvedQuote  *Message        `json:"-"`
	Attachments    []Attachment    `json:"media,omitempty"`
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
	Mentions       MessageMentions `json:"mentions"`
}
type QuoteReference struct {
	ServerID string `json:"serverId"`
	Type     int    `json:"type"`
}

func (m Message) IsQuotedText() bool {
	return m.Type == 49 && (m.Quote != nil || m.AppMessageType != nil && *m.AppMessageType == 57)
}

func (m Message) IsForwardedConversation() bool {
	return m.Type == 49 && m.AppMessageType != nil && *m.AppMessageType == 19
}

func (m Message) IsPassiveNotice() bool {
	if m.Type == 10000 {
		return true
	}
	if m.Type != 49 || m.AppMessageType == nil {
		return false
	}
	switch *m.AppMessageType {
	case 17, 74, 87:
		return true
	}
	return false
}

// CardContext returns display-only fields and text already present in the
// native projection, including the text carried by a combined forward.
func (m Message) CardContext() (summary, context string) {
	switch {
	case m.Type == 42, m.Type == 48:
		summary = "[微信名片]"
		if m.Type == 48 {
			summary = "[微信位置]"
		}
		context = m.Body
	case m.Type == 10000:
		summary, context = "[微信系统通知]", m.Body
	case m.Type == 49 && m.AppMessageType != nil:
		switch *m.AppMessageType {
		case 1:
			summary = "[微信分享文字]"
		case 17:
			summary = "[微信位置共享通知]"
		case 19:
			summary = "[微信合并转发]"
			body := strings.TrimSpace(m.Body)
			if utf8.ValidString(body) && len(body) <= 64<<10 &&
				(body == "[Chat History]" || strings.HasPrefix(body, "[Chat History] ") || strings.HasPrefix(body, "[Chat History]\n")) {
				return summary, body
			}
			return summary, ""
		case 33, 36:
			summary = "[微信小程序卡片]"
		case 74:
			summary = "[微信文件发送通知]"
		case 87:
			summary = "[微信群公告]"
		default:
			return "", ""
		}
		if m.Link != nil {
			var fields []string
			for _, field := range []struct{ label, value string }{
				{"标题", m.Link.Title}, {"简介", m.Link.Description}, {"来源", m.Link.SourceName},
			} {
				if value := strings.TrimSpace(field.value); value != "" {
					fields = append(fields, field.label+"："+value)
				}
			}
			context = strings.Join(fields, "\n")
		}
	}
	if !utf8.ValidString(context) || len(context) > 12<<10 {
		context = ""
	}
	return summary, context
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
