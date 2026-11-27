package stationbrowser

import (
	"bytes"
	"encoding/json"
	"strings"
)

// DouyinCommentRelation is a strictly parsed row from the web comment list or
// reply list. The request URL and the current account still need verification
// by the browser caller; parsing a JSON body alone grants no reply authority.
type DouyinCommentRelation struct {
	CID            string
	WorkID         string
	ReplyID        string
	ReplyToReplyID string
	AuthorUID      string
	Text           string
}

// parseDouyinCommentRelations accepts a response only for the exact work and,
// for a reply page, exact root comment requested by the caller. This parser is
// intentionally independent of rendered cards: the DOM exposes neither the
// numeric author UID nor an unambiguous direct-parent comment ID.
func parseDouyinCommentRelations(raw []byte, workID, rootID string) ([]DouyinCommentRelation, error) {
	if len(raw) == 0 || len(raw) > 512<<10 || !json.Valid(raw) || !douyinCommentID.MatchString(workID) ||
		rootID != "" && !douyinCommentID.MatchString(rootID) {
		return nil, ErrDouyinCommentsUnavailable
	}
	fields, unique := noticeObject(raw)
	if !unique {
		return nil, ErrDouyinCommentsUnavailable
	}
	var status int
	if json.Unmarshal(fields["status_code"], &status) != nil || status != 0 {
		return nil, ErrDouyinCommentsUnavailable
	}
	var rows []json.RawMessage
	if json.Unmarshal(fields["comments"], &rows) != nil || rows == nil || len(rows) > 200 {
		return nil, ErrDouyinCommentsUnavailable
	}
	comments := make([]DouyinCommentRelation, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, rawRow := range rows {
		relation, row, err := parseDouyinCommentRelationRow(rawRow, workID, rootID, seen)
		if err != nil {
			return nil, err
		}
		comments = append(comments, relation)
		if rootID != "" {
			continue
		}
		// The observed /comment/list/ response includes a bounded preview of
		// replies under each main comment. These rows already carry both the
		// root and direct-parent IDs, so the target may be found in this response.
		var children []json.RawMessage
		if rawChildren, present := row["reply_comment"]; present && !bytes.Equal(bytes.TrimSpace(rawChildren), []byte("null")) {
			if json.Unmarshal(rawChildren, &children) != nil || children == nil || len(children) > 200 {
				return nil, ErrDouyinCommentsUnavailable
			}
		}
		for _, rawChild := range children {
			child, fields, err := parseDouyinCommentRelationRow(rawChild, workID, relation.CID, seen)
			if err != nil {
				return nil, err
			}
			if rawGrandchildren, present := fields["reply_comment"]; present &&
				!bytes.Equal(bytes.TrimSpace(rawGrandchildren), []byte("null")) &&
				!bytes.Equal(bytes.TrimSpace(rawGrandchildren), []byte("[]")) {
				return nil, ErrDouyinCommentsUnavailable
			}
			comments = append(comments, child)
		}
	}
	return comments, nil
}

func parseDouyinCommentRelationRow(raw []byte, workID, rootID string, seen map[string]bool) (DouyinCommentRelation, map[string]json.RawMessage, error) {
	row, ok := noticeObject(raw)
	if !ok {
		return DouyinCommentRelation{}, nil, ErrDouyinCommentsUnavailable
	}
	cid, ok := commentRelationID(row["cid"], false)
	if !ok || seen[cid] {
		return DouyinCommentRelation{}, nil, ErrDouyinCommentsUnavailable
	}
	seen[cid] = true
	replyID, ok := commentRelationID(row["reply_id"], true)
	if !ok {
		return DouyinCommentRelation{}, nil, ErrDouyinCommentsUnavailable
	}
	directID, ok := commentRelationID(row["reply_to_reply_id"], true)
	if !ok || directID == cid || replyID == cid || rootID == "" && (replyID != "0" || directID != "0") ||
		rootID != "" && replyID != rootID {
		return DouyinCommentRelation{}, nil, ErrDouyinCommentsUnavailable
	}
	if rawWork, present := row["aweme_id"]; present {
		id, valid := commentRelationID(rawWork, false)
		if !valid || id != workID {
			return DouyinCommentRelation{}, nil, ErrDouyinCommentsUnavailable
		}
	}
	user, ok := noticeObject(row["user"])
	if !ok {
		return DouyinCommentRelation{}, nil, ErrDouyinCommentsUnavailable
	}
	uid, ok := commentRelationID(user["uid"], false)
	if !ok {
		return DouyinCommentRelation{}, nil, ErrDouyinCommentsUnavailable
	}
	var body string
	if json.Unmarshal(row["text"], &body) != nil || len(body) > 8192 || strings.TrimSpace(body) == "" {
		return DouyinCommentRelation{}, nil, ErrDouyinCommentsUnavailable
	}
	return DouyinCommentRelation{CID: cid, WorkID: workID, ReplyID: replyID,
		ReplyToReplyID: directID, AuthorUID: uid, Text: body}, row, nil
}

func commentRelationID(raw json.RawMessage, allowZero bool) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false
	}
	var id string
	if raw[0] == '"' {
		if json.Unmarshal(raw, &id) != nil {
			return "", false
		}
	} else {
		id = string(raw)
	}
	return id, allowZero && id == "0" || douyinCommentID.MatchString(id)
}
