package station

import "strconv"

// The execution ledger's audio_complete and visual_complete describe retained
// evidence. This turn-local chain separately records which of those pages the
// Agent actually received, in order, for one work and one stable snapshot.
type localDouyinRoundContextRead struct {
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
	delete(t.douyinRoundContextReads, douyinRoundContextReadKey(date, workID))
}

func (t *localToolSet) recordDouyinRoundContextRead(in localDouyinRoundsInput, result *localDouyinExecutionWorkContext) {
	key := douyinRoundContextReadKey(in.Date, in.WorkID)
	result.AgentContextReadComplete = false
	result.AgentPagesRead = 0
	if result.RestartRequired || result.Ownership != "verified_own_work" || result.SnapshotState != "stable" ||
		in.Cursor == "" && in.Offset != 0 {
		delete(t.douyinRoundContextReads, key)
		return
	}
	creatorKey := localCheckpointKey("douyin-context-creator-v1", result.CreatorText, result.CreatorCoverage, result.CreatorObservedAt)
	progress := localDouyinRoundContextRead{Total: result.MediaPageCount, MediaSHA: result.MediaSHA256, CreatorKey: creatorKey}
	if in.Cursor != "" {
		previous, ok := t.douyinRoundContextReads[key]
		if !ok || previous.NextCursor != in.Cursor || previous.Total != result.MediaPageCount ||
			previous.MediaSHA != result.MediaSHA256 || previous.CreatorKey != creatorKey {
			delete(t.douyinRoundContextReads, key)
			return
		}
		progress = previous
	} else {
		delete(t.douyinRoundContextReads, key)
	}
	progress.PagesRead += len(result.MediaPages)
	if progress.PagesRead > progress.Total || progress.PagesRead == 0 && progress.Total > 0 {
		delete(t.douyinRoundContextReads, key)
		return
	}
	if result.NextCursor != "" {
		cursor, err := parseLocalDouyinExecutionContextCursor(result.NextCursor)
		if err != nil || cursor.Date != in.Date || cursor.WorkID != in.WorkID || cursor.Offset != progress.PagesRead ||
			cursor.MediaSHA256 != progress.MediaSHA || cursor.CreatorKey != progress.CreatorKey ||
			(progress.Epoch != "" && (cursor.Epoch != progress.Epoch || cursor.Grant != progress.Grant)) {
			delete(t.douyinRoundContextReads, key)
			return
		}
		progress.NextCursor, progress.Epoch, progress.Grant = result.NextCursor, cursor.Epoch, cursor.Grant
		if t.douyinRoundContextReads == nil {
			t.douyinRoundContextReads = make(map[string]localDouyinRoundContextRead)
		}
		t.douyinRoundContextReads[key] = progress
	} else {
		progress.NextCursor = ""
		result.AgentContextReadComplete = progress.PagesRead == progress.Total && result.AudioComplete && result.VisualComplete
		delete(t.douyinRoundContextReads, key)
	}
	result.AgentPagesRead = progress.PagesRead
	result.Coverage += "；本轮已顺序读取本作品证据 " + strconv.Itoa(progress.PagesRead) + "/" + strconv.Itoa(progress.Total) + " 页；agent_context_read_complete 只在本轮读完且已存音画覆盖完整时为真"
}
