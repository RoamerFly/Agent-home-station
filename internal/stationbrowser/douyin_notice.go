package stationbrowser

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

const DouyinNoticePageURL = "https://www.douyin.com/help"
const douyinNoticePath = "/aweme/v1/web/notice/"

var (
	ErrDouyinNoticeUnavailable    = errors.New("抖音通知列表没有返回可核对的结果")
	ErrDouyinNoticeAccountChanged = errors.New("抖音通知账号已变更，请重新核对通知授权")
	ErrDouyinNoticeMarksRead      = errors.New("当前抖音网页读取通知列表会标记已读，需要单独授权")
	ErrDouyinNoticeCursorChanged  = errors.New("互动通知列表已变化，请从首批重新读取")
)

const MaxDouyinNoticePages = 5

type DouyinNoticeCheckpoint struct {
	Pages  int
	Digest [32]byte
}

type DouyinNotice struct {
	ID        string
	Type      int
	CreatedAt int64
	WorkID    string
	// Candidates are field paths and numeric IDs observed in this notice.
	// Their paths do not establish the target comment, its parent, or a reply grant.
	Candidates []DouyinNoticeCandidate
}

type DouyinNoticeCandidate struct {
	Path string `json:"path"`
	ID   string `json:"id"`
}

type DouyinNoticePage struct {
	Account string
	// CreatorAccount uses the same domain as DouyinCreatorWorksPage.Account.
	// It is a digest of the signed-in UID, never the UID or a session token.
	CreatorAccount string
	Items          []DouyinNotice
	// Details contain only normalized evidence from naturally issued detail
	// GETs for list IDs. They never establish a reply target by themselves.
	Details          []DouyinNoticeDetail
	DetailProgress   string
	MinTime, MaxTime int64
	HasMore          bool
	// RenderedText is separate from item metadata. The official page filters
	// and groups notifications before rendering, so positions cannot be joined.
	RenderedText string
	PagesRead    int
	Next         DouyinNoticeCheckpoint
	Progress     string
}

type DouyinNoticeRequest struct {
	// Account is the opaque identity returned by DouyinNoticeAccount when
	// granting this scope. A remembered browser profile alone is insufficient.
	Account       string
	AllowMarkRead bool
	Pages         int
	After         DouyinNoticeCheckpoint
}

type noticePosition struct {
	min, max int64
	group    string
}

func noticeListRequest(raw, origin string) (position noticePosition, mayMarkRead, exact bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Scheme+"://"+u.Host != origin || u.EscapedPath() != douyinNoticePath || u.Fragment != "" {
		return position, false, false
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return position, false, false
	}
	one := func(key string) string {
		if len(values[key]) == 1 {
			return values[key][0]
		}
		return ""
	}
	mark := one("is_mark_read")
	if (mark != "0" && mark != "1") || one("is_new_notice") != "1" || one("count") != "10" {
		return position, false, false
	}
	for i, key := range []string{"min_time", "max_time"} {
		value := one(key)
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 0 || strconv.FormatInt(n, 10) != value {
			return position, false, false
		}
		if i == 0 {
			position.min = n
		} else {
			position.max = n
		}
	}
	position.group = one("notice_group")
	if len(position.group) > 96 {
		return position, false, false
	}
	for _, value := range strings.Split(position.group, ",") {
		n, err := strconv.ParseUint(value, 10, 31)
		if err != nil || strconv.FormatUint(n, 10) != value {
			return position, false, false
		}
	}
	return position, mark == "1", true
}

func noticeCheckpoint(previous DouyinNoticeCheckpoint, position noticePosition, page DouyinNoticePage) DouyinNoticeCheckpoint {
	raw, _ := json.Marshal([]any{previous.Digest, position.group, position.min, position.max, page.MinTime, page.MaxTime, page.HasMore, page.Items})
	return DouyinNoticeCheckpoint{Pages: previous.Pages + 1, Digest: sha256.Sum256(raw)}
}

// noticeObject retains raw values and rejects duplicate keys. A duplicate key
// could make two parsers disagree about which ID the platform supplied.
func noticeObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok {
			return nil, false
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, false
		}
		fields[name] = value
	}
	end, err := decoder.Token()
	return fields, err == nil && end == json.Delim('}')
}

func noticeCandidateID(raw json.RawMessage) (id string, present, valid bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false, true
	}
	if raw[0] == '"' {
		if json.Unmarshal(raw, &id) != nil {
			return "", false, false
		}
	} else {
		id = string(raw)
	}
	if id == "" || id == "0" {
		return "", false, true
	}
	if !douyinVideoID.MatchString(id) {
		return "", false, false
	}
	return id, true, true
}

func noticeCandidates(fields map[string]json.RawMessage) ([]DouyinNoticeCandidate, bool) {
	candidates := make([]DouyinNoticeCandidate, 0, 13)
	add := func(path string, fields map[string]json.RawMessage, key string) bool {
		raw, exists := fields[key]
		if !exists {
			return true
		}
		id, present, valid := noticeCandidateID(raw)
		if !valid {
			return false
		}
		if present {
			candidates = append(candidates, DouyinNoticeCandidate{Path: path, ID: id})
		}
		return true
	}
	for _, key := range []string{"cid", "real_cid", "reply_id", "reply_to_reply_id"} {
		if !add(key, fields, key) {
			return nil, false
		}
	}
	for _, branch := range []string{"user", "comment", "reply_comment"} {
		raw, exists := fields[branch]
		raw = bytes.TrimSpace(raw)
		if !exists || bytes.Equal(raw, []byte("null")) {
			continue
		}
		object, ok := noticeObject(raw)
		if !ok {
			return nil, false
		}
		if branch == "user" {
			if !add("user.uid", object, "uid") {
				return nil, false
			}
			continue
		}
		for _, key := range []string{"cid", "reply_id", "reply_to_reply_id"} {
			if !add(branch+"."+key, object, key) {
				return nil, false
			}
		}
		if branch == "comment" {
			// A web comment notice can wrap the target row one level deeper.
			// Retain only IDs; the row text, profile and media remain private.
			nestedRaw, exists := object["comment"]
			if exists && !bytes.Equal(bytes.TrimSpace(nestedRaw), []byte("null")) {
				nested, ok := noticeObject(nestedRaw)
				if !ok {
					return nil, false
				}
				for _, key := range []string{"cid", "reply_id", "reply_to_reply_id"} {
					if !add("comment.comment."+key, nested, key) {
						return nil, false
					}
				}
			}
		}
		user, exists := object["user"]
		user = bytes.TrimSpace(user)
		if !exists || bytes.Equal(user, []byte("null")) {
			continue
		}
		object, ok = noticeObject(user)
		if !ok || !add(branch+".user.uid", object, "uid") {
			return nil, false
		}
	}
	return candidates, true
}

// noticeWorkID accepts only consistent work IDs from the notification and its
// comment payload. A nested comment notice may omit the top-level aweme_id.
func noticeWorkID(fields map[string]json.RawMessage) (string, bool) {
	workID := ""
	add := func(object map[string]json.RawMessage) bool {
		raw, exists := object["aweme_id"]
		if !exists {
			return true
		}
		id, present, valid := noticeCandidateID(raw)
		if !valid || present && workID != "" && id != workID {
			return false
		}
		if present {
			workID = id
		}
		return true
	}
	if !add(fields) {
		return "", false
	}
	rawComment, exists := fields["comment"]
	if !exists || bytes.Equal(bytes.TrimSpace(rawComment), []byte("null")) {
		return workID, true
	}
	comment, ok := noticeObject(rawComment)
	if !ok || !add(comment) {
		return "", false
	}
	for _, branch := range []string{"comment", "aweme"} {
		raw, exists := comment[branch]
		if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		nested, ok := noticeObject(raw)
		if !ok || !add(nested) {
			return "", false
		}
	}
	return workID, true
}

func parseDouyinNoticePage(ctx context.Context, raw []byte) (DouyinNoticePage, error) {
	if err := ctx.Err(); err != nil {
		return DouyinNoticePage{}, err
	}
	if len(raw) > 256<<10 {
		return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
	}
	// A duplicated response-level status or list could let the page renderer
	// and this parser select different notifications. Reject that ambiguity.
	if _, unique := noticeObject(raw); !unique {
		return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
	}
	var wire struct {
		Status  *int               `json:"status_code"`
		Items   *[]json.RawMessage `json:"notice_list_v2"`
		HasMore json.RawMessage    `json:"has_more"`
		MinTime json.Number        `json:"min_time"`
		MaxTime json.Number        `json:"max_time"`
	}
	if json.Unmarshal(raw, &wire) != nil || wire.Status == nil || *wire.Status != 0 ||
		wire.Items == nil || len(*wire.Items) > 100 {
		return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
	}
	page := DouyinNoticePage{Items: make([]DouyinNotice, 0, len(*wire.Items))}
	switch string(wire.HasMore) {
	case "1", "true":
		page.HasMore = true
	case "0", "false":
	default:
		return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
	}
	var err error
	if wire.MinTime != "" {
		page.MinTime, err = strconv.ParseInt(string(wire.MinTime), 10, 64)
		if err != nil || page.MinTime < 0 {
			return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
		}
	}
	if wire.MaxTime != "" {
		page.MaxTime, err = strconv.ParseInt(string(wire.MaxTime), 10, 64)
		if err != nil || page.MaxTime < 0 {
			return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
		}
	}
	if page.HasMore && page.MaxTime == 0 {
		return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
	}
	seen := make(map[string]bool, len(*wire.Items))
	for _, rawItem := range *wire.Items {
		fields, unique := noticeObject(rawItem)
		var row struct {
			ID        string `json:"nid_str"`
			Type      int    `json:"type"`
			CreatedAt int64  `json:"create_time"`
		}
		if json.Unmarshal(rawItem, &row) != nil {
			return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
		}
		if !douyinVideoID.MatchString(row.ID) || row.Type < 0 || row.CreatedAt < 0 || seen[row.ID] {
			return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
		}
		seen[row.ID] = true
		workID, workValid := noticeWorkID(fields)
		var candidates []DouyinNoticeCandidate
		if unique && workValid {
			candidates, _ = noticeCandidates(fields)
		}
		// Candidate parsing never discards notification metadata after a
		// possibly mark-read request. Duplicate or invalid fields leave this
		// item's candidate set empty.
		page.Items = append(page.Items, DouyinNotice{ID: row.ID, Type: row.Type, CreatedAt: row.CreatedAt, WorkID: workID, Candidates: candidates})
	}
	return page, nil
}
