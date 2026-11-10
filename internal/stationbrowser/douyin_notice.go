package stationbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
)

const DouyinNoticePageURL = "https://www.douyin.com/help"
const douyinNoticePath = "/aweme/v1/web/notice/"

var (
	ErrDouyinNoticeUnavailable    = errors.New("抖音通知列表没有返回可核对的结果")
	ErrDouyinNoticeAccountChanged = errors.New("抖音通知账号已变更，请重新核对通知授权")
	ErrDouyinNoticeMarksRead      = errors.New("当前抖音网页读取通知列表会标记已读，需要单独授权")
)

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
}

type DouyinNoticeRequest struct {
	// Account is the opaque identity returned by DouyinNoticeAccount when
	// granting this scope. A remembered browser profile alone is insufficient.
	Account       string
	AllowMarkRead bool
}

func noticeListRequest(raw, origin string) (mayMarkRead, exact bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Scheme+"://"+u.Host != origin || u.EscapedPath() != douyinNoticePath || u.Fragment != "" {
		return false, false
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false, false
	}
	mark, ok := values["is_mark_read"]
	version := values["is_new_notice"]
	if !ok || len(mark) != 1 || len(version) != 1 || version[0] != "1" {
		return false, false
	}
	switch mark[0] {
	case "0":
		return false, true
	case "1":
		return true, true
	}
	return false, false
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
