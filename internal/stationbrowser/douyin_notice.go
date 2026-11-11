package stationbrowser

import (
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
}

type DouyinNoticePage struct {
	Account          string
	Items            []DouyinNotice
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

func parseDouyinNoticePage(ctx context.Context, raw []byte) (DouyinNoticePage, error) {
	if err := ctx.Err(); err != nil {
		return DouyinNoticePage{}, err
	}
	var wire struct {
		Status *int `json:"status_code"`
		Items  *[]struct {
			ID        string `json:"nid_str"`
			Type      int    `json:"type"`
			CreatedAt int64  `json:"create_time"`
			WorkID    string `json:"aweme_id"`
		} `json:"notice_list_v2"`
		HasMore json.RawMessage `json:"has_more"`
		MinTime json.Number     `json:"min_time"`
		MaxTime json.Number     `json:"max_time"`
	}
	if len(raw) > 256<<10 || json.Unmarshal(raw, &wire) != nil || wire.Status == nil || *wire.Status != 0 ||
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
	for _, row := range *wire.Items {
		if !douyinVideoID.MatchString(row.ID) || row.Type < 1 || row.CreatedAt < 0 || seen[row.ID] {
			return DouyinNoticePage{}, ErrDouyinNoticeUnavailable
		}
		seen[row.ID] = true
		workID := ""
		if douyinVideoID.MatchString(row.WorkID) {
			workID = row.WorkID
		}
		page.Items = append(page.Items, DouyinNotice{ID: row.ID, Type: row.Type, CreatedAt: row.CreatedAt, WorkID: workID})
	}
	return page, nil
}
