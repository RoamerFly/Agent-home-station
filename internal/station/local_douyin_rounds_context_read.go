package station

import (
	"context"
	"strconv"

	"wechat-bot/internal/apphost"
)

// The execution ledger's audio_complete and visual_complete describe retained
// evidence. This turn-local chain separately records which of those pages the
// Agent actually received, in order, for one work and one stable snapshot.
type localDouyinRoundContextRead struct {
	Date       string
	WorkID     string
	NextCursor string
	PagesRead  int
	Total      int
	MediaSHA   string
	CreatorKey string
	Epoch      string
	Grant      string
}

func douyinRoundContextReadKey(date, workID string) string { return date + "\x00" + workID }

func (t *localToolSet) resetDouyinRoundContextRead(date, workID string) {
	key := douyinRoundContextReadKey(date, workID)
	delete(t.douyinRoundContextReads, key)
	delete(t.douyinRoundContextCompleted, key)
}

func (t *localToolSet) recordDouyinRoundContextRead(in localDouyinRoundsInput, result *localDouyinExecutionWorkContext) {
	key := douyinRoundContextReadKey(in.Date, in.WorkID)
	result.AgentContextReadComplete = false
	result.AgentPagesRead = 0
	if result.RestartRequired || result.Ownership != "verified_own_work" || result.SnapshotState != "stable" ||
		result.WorkID != in.WorkID || !validLocalDouyinCommentID(in.WorkID) ||
		result.MediaPageCount < 1 || !localDouyinPublicationHex(result.MediaSHA256) ||
		result.contextEpoch == "" || len(result.contextEpoch) > 128 || !localDouyinPublicationHex(result.contextGrant) ||
		in.Cursor == "" && in.Offset != 0 {
		t.resetDouyinRoundContextRead(in.Date, in.WorkID)
		return
	}
	creatorKey := localCheckpointKey("douyin-context-creator-v1", result.CreatorText, result.CreatorCoverage, result.CreatorObservedAt)
	progress := localDouyinRoundContextRead{Date: in.Date, WorkID: in.WorkID, Total: result.MediaPageCount,
		MediaSHA: result.MediaSHA256, CreatorKey: creatorKey, Epoch: result.contextEpoch, Grant: result.contextGrant}
	if in.Cursor != "" {
		previous, ok := t.douyinRoundContextReads[key]
		if !ok || previous.NextCursor != in.Cursor || previous.Date != in.Date || previous.WorkID != in.WorkID ||
			previous.Total != result.MediaPageCount || previous.MediaSHA != result.MediaSHA256 ||
			previous.CreatorKey != creatorKey || previous.Epoch != result.contextEpoch || previous.Grant != result.contextGrant {
			t.resetDouyinRoundContextRead(in.Date, in.WorkID)
			return
		}
		progress = previous
	} else {
		t.resetDouyinRoundContextRead(in.Date, in.WorkID)
	}
	progress.PagesRead += len(result.MediaPages)
	if progress.PagesRead > progress.Total || progress.PagesRead == 0 && progress.Total > 0 {
		t.resetDouyinRoundContextRead(in.Date, in.WorkID)
		return
	}
	if result.NextCursor != "" {
		cursor, err := parseLocalDouyinExecutionContextCursor(result.NextCursor)
		if err != nil || cursor.Date != in.Date || cursor.WorkID != in.WorkID || cursor.Offset != progress.PagesRead ||
			cursor.MediaSHA256 != progress.MediaSHA || cursor.CreatorKey != progress.CreatorKey ||
			cursor.Epoch != progress.Epoch || cursor.Grant != progress.Grant {
			t.resetDouyinRoundContextRead(in.Date, in.WorkID)
			return
		}
		progress.NextCursor = result.NextCursor
		if t.douyinRoundContextReads == nil {
			t.douyinRoundContextReads = make(map[string]localDouyinRoundContextRead)
		}
		t.douyinRoundContextReads[key] = progress
	} else {
		progress.NextCursor = ""
		result.AgentContextReadComplete = progress.PagesRead == progress.Total && result.AudioComplete && result.VisualComplete
		delete(t.douyinRoundContextReads, key)
		if result.AgentContextReadComplete {
			if t.douyinRoundContextCompleted == nil {
				t.douyinRoundContextCompleted = make(map[string]localDouyinRoundContextRead)
			}
			t.douyinRoundContextCompleted[key] = progress
		} else {
			delete(t.douyinRoundContextCompleted, key)
		}
	}
	result.AgentPagesRead = progress.PagesRead
	result.Coverage += "；本轮已顺序读取本作品证据 " + strconv.Itoa(progress.PagesRead) + "/" + strconv.Itoa(progress.Total) + " 页；agent_context_read_complete 只在本轮读完且已存音画覆盖完整时为真"
}

// A completed read belongs only to this tool turn. Recheck the current local
// works-memory snapshot before a later action in the same turn may use it.
func (t *localToolSet) completedDouyinRoundContextRead(ctx context.Context, date, workID string) (localDouyinRoundContextRead, error) {
	key := douyinRoundContextReadKey(date, workID)
	completed, found := t.douyinRoundContextCompleted[key]
	reject := func() (localDouyinRoundContextRead, error) {
		t.resetDouyinRoundContextRead(date, workID)
		return localDouyinRoundContextRead{}, apphost.ErrObservationForbidden
	}
	if !found || completed.Date != date || completed.WorkID != workID || completed.NextCursor != "" ||
		completed.Total < 1 || completed.PagesRead != completed.Total ||
		!localDouyinPublicationHex(completed.MediaSHA) || !localDouyinPublicationHex(completed.CreatorKey) ||
		!localDouyinPublicationHex(completed.Grant) || completed.Epoch == "" || len(completed.Epoch) > 128 ||
		t.access == nil || t.publications == nil || t.publications.home == nil || t.publications.home.douyinWeb == nil {
		return reject()
	}
	if err := t.checkAppObservationIdentity(); err != nil {
		return reject()
	}
	record, err := t.douyinMemoryState(ctx)
	if err != nil {
		return reject()
	}
	m := t.publications.home.douyinWeb
	scope := localDouyinExecutionScope(t.access.fishID, t.access.ownerID, record)
	current, err := m.executionWorkContextPage(ctx, scope, record, t.access.fishID, t.access.ownerID, date, workID, 0, 1, "")
	if err != nil || current.RestartRequired || current.SnapshotState != "stable" || current.Ownership != "verified_own_work" ||
		!current.AudioComplete || !current.VisualComplete || current.MediaPageCount != completed.Total ||
		current.MediaSHA256 != completed.MediaSHA || current.contextEpoch != completed.Epoch || current.contextGrant != completed.Grant ||
		localCheckpointKey("douyin-context-creator-v1", current.CreatorText, current.CreatorCoverage, current.CreatorObservedAt) != completed.CreatorKey {
		return reject()
	}
	return completed, nil
}
