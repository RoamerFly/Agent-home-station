package linuxnative

import (
	"context"
	"errors"
	"time"
)

// MessageWindow contains exact native messages surrounding Anchor. Before and
// After are chronological; the anchor itself occurs in neither slice.
type MessageWindow struct {
	Anchor   Message   `json:"anchor"`
	Before   []Message `json:"before"`
	After    []Message `json:"after"`
	Coverage string    `json:"coverage"`
}

type windowRow struct {
	id, thread, created, sender, server string
	when                                time.Time
}

// windowRing retains the nearest rows while scanning a newest-first catalog.
// It does not allocate for the whole requested window before rows arrive.
type windowRing struct {
	rows  []windowRow
	start int
	limit int
}

func (r *windowRing) add(row windowRow) {
	if r.limit == 0 {
		return
	}
	if len(r.rows) < r.limit {
		r.rows = append(r.rows, row)
		return
	}
	r.rows[r.start] = row
	r.start = (r.start + 1) % len(r.rows)
}

func (r windowRing) newestFirst() []windowRow {
	out := make([]windowRow, len(r.rows))
	for i := range out {
		out[i] = r.rows[(r.start+i)%len(r.rows)]
	}
	return out
}

func catalogWindowRow(item map[string]any, thread string) (windowRow, error) {
	id, idOK := item["id"].(string)
	gotThread, threadOK := item["threadId"].(string)
	created, timeOK := item["createdAt"].(string)
	when, err := time.Parse(time.RFC3339Nano, created)
	if !idOK || id == "" || len(id) > 8192 || !threadOK || gotThread != thread || !timeOK || err != nil {
		return windowRow{}, errors.New("invalid native message catalog row")
	}
	sender, _ := item["senderId"].(string)
	server, _ := item["serverId"].(string)
	return windowRow{id: id, thread: gotThread, created: created, sender: sender, server: server, when: when.UTC()}, nil
}

func partialNativeCoverage(value any) bool {
	switch v := value.(type) {
	case string:
		return v == "partial" || v == "limited"
	case map[string]any:
		return v["partial"] == true || v["complete"] == false
	}
	return false
}

// MessageWindow reads around an arbitrary native message ID, including an old
// quoted source. The catalog is newest-first and is followed until the anchor
// and requested older rows are found, with no total page or age cutoff. An
// absent catalog anchor produces a partial window using only strict timestamps;
// equal-time peers cannot safely be placed on either side without native order.
func (s *Sender) MessageWindow(ctx context.Context, thread, anchorID string, before, after int) (MessageWindow, error) {
	if !safeThreadID(thread) || anchorID == "" || len(anchorID) > 8192 || before < 0 || after < 0 {
		return MessageWindow{}, errors.New("invalid native message window request")
	}
	if err := ctx.Err(); err != nil {
		return MessageWindow{Coverage: "partial_cancelled"}, err
	}
	anchor, err := s.Message(ctx, anchorID)
	if err != nil {
		if ctx.Err() != nil {
			return MessageWindow{Coverage: "partial_cancelled"}, ctx.Err()
		}
		return MessageWindow{}, err
	}
	anchorTime, err := time.Parse(time.RFC3339Nano, anchor.CreatedAt)
	if anchor.AccountID != s.Account() || anchor.ThreadID != thread || err != nil {
		return MessageWindow{}, errors.New("native message window anchor mismatch")
	}
	anchorTime = anchorTime.UTC()
	out := MessageWindow{Anchor: anchor, Before: []Message{}, After: []Message{}, Coverage: "complete"}
	if before == 0 && after == 0 {
		return out, nil
	}

	newer := windowRing{limit: after}
	strictNewer := windowRing{limit: after}
	older := make([]windowRow, 0)
	seenIDs := make(map[string]string)
	seenCursors := map[string]bool{"": true}
	cursor := ""
	var previous time.Time
	hasPrevious := false
	foundAnchor, nativePartial, orderPartial := false, false, false
	for {
		if err := ctx.Err(); err != nil {
			out.Coverage = "partial_cancelled"
			return out, err
		}
		page, err := s.CatalogWithLimit(ctx, "messages", thread, "", cursor, 50)
		if err != nil {
			if ctx.Err() != nil {
				out.Coverage = "partial_cancelled"
				return out, ctx.Err()
			}
			out.Coverage = "partial_catalog_error"
			return out, err
		}
		nativePartial = nativePartial || partialNativeCoverage(page.Coverage)
		for _, item := range page.Items {
			row, err := catalogWindowRow(item, thread)
			if err != nil {
				return MessageWindow{}, err
			}
			if hasPrevious && row.when.After(previous) {
				// A native time/order contradiction cannot establish a neighbor.
				// Keep the last trustworthy bound and continue through later rows.
				orderPartial = true
				continue
			}
			previous = row.when
			hasPrevious = true
			if oldCreated, exists := seenIDs[row.id]; exists {
				if oldCreated != row.created {
					return MessageWindow{}, errors.New("native message catalog identity changed")
				}
				continue
			}
			seenIDs[row.id] = row.created
			if row.id == anchorID {
				if row.created != anchor.CreatedAt {
					return MessageWindow{}, errors.New("native message window anchor changed")
				}
				foundAnchor = true
				older = older[:0]
				if before == 0 {
					break
				}
				continue
			}
			if foundAnchor {
				if row.when.After(anchorTime) {
					orderPartial = true
					continue
				}
				if len(older) < before {
					older = append(older, row)
				}
			} else {
				if !row.when.Before(anchorTime) {
					newer.add(row)
					if row.when.After(anchorTime) {
						strictNewer.add(row)
					}
				} else if len(older) < before {
					older = append(older, row)
				}
			}
			if foundAnchor && len(older) >= before {
				break
			}
		}
		if foundAnchor && len(older) >= before {
			break
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		if seenCursors[*page.NextCursor] {
			return MessageWindow{}, errors.New("native message catalog cursor stalled")
		}
		cursor = *page.NextCursor
		seenCursors[cursor] = true
	}
	if !foundAnchor {
		newer = strictNewer
		out.Coverage = "partial_anchor_not_cataloged"
	} else if nativePartial {
		out.Coverage = "partial_native_coverage"
	}
	if orderPartial {
		out.Coverage = "partial_native_order"
	}
	partial, err := s.appendWindowMessages(ctx, &out.After, newer.newestFirst(), thread, anchorTime, true)
	if partial {
		out.Coverage = "partial_native_order"
	}
	if err != nil {
		out.Coverage = "partial_catalog_error"
		if ctx.Err() != nil {
			out.Coverage = "partial_cancelled"
			return out, ctx.Err()
		}
		return out, err
	}
	partial, err = s.appendWindowMessages(ctx, &out.Before, older, thread, anchorTime, false)
	if partial {
		out.Coverage = "partial_native_order"
	}
	if err != nil {
		out.Coverage = "partial_catalog_error"
		if ctx.Err() != nil {
			out.Coverage = "partial_cancelled"
			return out, ctx.Err()
		}
		return out, err
	}
	return out, nil
}

func (s *Sender) appendWindowMessages(ctx context.Context, target *[]Message, newestFirst []windowRow, thread string, anchorTime time.Time, after bool) (bool, error) {
	partial := false
	for i := len(newestFirst) - 1; i >= 0; i-- {
		row := newestFirst[i]
		if err := ctx.Err(); err != nil {
			return partial, err
		}
		message, err := s.Message(ctx, row.id)
		if err != nil {
			return partial, err
		}
		when, err := time.Parse(time.RFC3339Nano, message.CreatedAt)
		when = when.UTC()
		if message.AccountID != s.Account() || message.ThreadID != thread || row.sender != "" && message.SenderID != row.sender || row.server != "" && message.ServerID != row.server {
			return partial, errors.New("native message window row scope changed")
		}
		if err != nil || message.CreatedAt != row.created || after && when.Before(anchorTime) || !after && when.After(anchorTime) {
			partial = true
			continue
		}
		*target = append(*target, message)
	}
	return partial, nil
}
