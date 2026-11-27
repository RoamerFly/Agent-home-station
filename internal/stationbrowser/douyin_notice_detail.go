package stationbrowser

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
)

const douyinNoticeDetailPath = "/aweme/v1/web/notice/detail/"

// DouyinNoticeDetail is normalized evidence from a page-issued detail response.
// Even a unique CID here is only a candidate; no detail field has been
// calibrated against a real notice and its displayed target comment.
type DouyinNoticeDetail struct {
	NoticeID             string
	RequestType          int
	NoticeType           int
	CreatedAt            int64
	WorkID               string
	Candidates           []DouyinNoticeCandidate
	CandidateFieldsValid bool
}

type noticeDetailRef struct {
	id          string
	requestType int
}

// noticeDetailRequest admits only a page request for IDs already observed in
// the bound notification list. Other query parameters may carry the site's
// changing signature and are never retained or returned.
func noticeDetailRequest(raw, origin string, known map[string]int) ([]noticeDetailRef, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Scheme+"://"+u.Host != origin || u.EscapedPath() != douyinNoticeDetailPath || u.Fragment != "" {
		return nil, false
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(values["aid"]) != 1 || values["aid"][0] != "6383" {
		return nil, false
	}
	// Observed and published web implementations use batched id_list and
	// single notice_id_str requests respectively. Request recognition does not
	// establish the single form's response shape. Never combine both forms.
	if _, batch := values["id_list"]; !batch {
		ids := values["notice_id_str"]
		if len(ids) != 1 || !douyinVideoID.MatchString(ids[0]) {
			return nil, false
		}
		listType, found := known[ids[0]]
		if !found || listType < 0 {
			return nil, false
		}
		return []noticeDetailRef{{id: ids[0], requestType: listType}}, true
	}
	if len(values["notice_id_str"]) != 0 || len(values["id_list"]) != 1 || len(values["id_list"][0]) > 2048 {
		return nil, false
	}
	var items []json.RawMessage
	if json.Unmarshal([]byte(values["id_list"][0]), &items) != nil || len(items) == 0 || len(items) > 10 {
		return nil, false
	}
	refs := make([]noticeDetailRef, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		fields, ok := noticeObject(item)
		if !ok || len(fields) != 2 {
			return nil, false
		}
		var wire struct {
			ID   string `json:"notice_id_str"`
			Type int    `json:"type"`
		}
		if json.Unmarshal(item, &wire) != nil || !douyinVideoID.MatchString(wire.ID) || wire.Type < 0 || seen[wire.ID] {
			return nil, false
		}
		listType, found := known[wire.ID]
		if !found || listType < 0 || wire.Type != 0 && wire.Type != listType {
			return nil, false
		}
		seen[wire.ID] = true
		refs = append(refs, noticeDetailRef{id: wire.ID, requestType: wire.Type})
	}
	return refs, true
}

func parseDouyinNoticeDetail(ctx context.Context, raw []byte, refs []noticeDetailRef) ([]DouyinNoticeDetail, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(raw) > 256<<10 || !json.Valid(raw) || len(refs) == 0 || len(refs) > 10 {
		return nil, ErrDouyinNoticeUnavailable
	}
	fields, unique := noticeObject(raw)
	if !unique {
		return nil, ErrDouyinNoticeUnavailable
	}
	status, ok := fields["status_code"]
	if !ok || !bytes.Equal(bytes.TrimSpace(status), []byte("0")) {
		return nil, ErrDouyinNoticeUnavailable
	}
	var items []json.RawMessage
	if json.Unmarshal(fields["notice_list_v2"], &items) != nil || len(items) != len(refs) {
		return nil, ErrDouyinNoticeUnavailable
	}
	wanted := make(map[string]int, len(refs))
	for _, ref := range refs {
		if !douyinVideoID.MatchString(ref.id) || ref.requestType < 0 {
			return nil, ErrDouyinNoticeUnavailable
		}
		wanted[ref.id] = ref.requestType
	}
	seen := make(map[string]bool, len(items))
	out := make([]DouyinNoticeDetail, 0, len(items))
	for _, item := range items {
		if !json.Valid(item) {
			return nil, ErrDouyinNoticeUnavailable
		}
		object, unique := noticeObject(item)
		if !unique {
			return nil, ErrDouyinNoticeUnavailable
		}
		var row struct {
			ID        string `json:"nid_str"`
			Type      int    `json:"type"`
			CreatedAt int64  `json:"create_time"`
		}
		if json.Unmarshal(item, &row) != nil || !douyinVideoID.MatchString(row.ID) || row.Type < 0 || row.CreatedAt < 0 || seen[row.ID] {
			return nil, ErrDouyinNoticeUnavailable
		}
		requestType, found := wanted[row.ID]
		if !found || requestType != 0 && row.Type != 0 && row.Type != requestType {
			return nil, ErrDouyinNoticeUnavailable
		}
		seen[row.ID] = true
		candidates, valid := noticeCandidates(object)
		workID, workValid := noticeWorkID(object)
		out = append(out, DouyinNoticeDetail{
			NoticeID: row.ID, RequestType: requestType, NoticeType: row.Type,
			CreatedAt: row.CreatedAt, WorkID: workID,
			Candidates: candidates, CandidateFieldsValid: valid && workValid,
		})
	}
	return out, nil
}
