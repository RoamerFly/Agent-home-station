package station

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
)

const localDouyinExecutionContextCursorMax = 1024

var errLocalDouyinExecutionContextCursor = errors.New("作品上下文续页位置无效")

type localDouyinExecutionContextCursor struct {
	Version     int    `json:"version"`
	Grant       string `json:"grant"`
	Date        string `json:"date"`
	WorkID      string `json:"work_id"`
	Offset      int    `json:"offset"`
	Epoch       string `json:"epoch"`
	CreatorKey  string `json:"creator_key"`
	MediaSHA256 string `json:"media_sha256"`
}

func parseLocalDouyinExecutionContextCursor(raw string) (localDouyinExecutionContextCursor, error) {
	var cursor localDouyinExecutionContextCursor
	if raw == "" || len(raw) > localDouyinExecutionContextCursorMax {
		return cursor, errLocalDouyinExecutionContextCursor
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) > 768 || base64.RawURLEncoding.EncodeToString(decoded) != raw ||
		strictLocalMemoryDecode(decoded, &cursor) != nil || cursor.Version != 1 ||
		!localDouyinPublicationHex(cursor.Grant) || !localDouyinPublicationHex(cursor.CreatorKey) ||
		(cursor.MediaSHA256 != "" && !localDouyinPublicationHex(cursor.MediaSHA256)) ||
		cursor.Offset < 1 || cursor.Epoch == "" || len(cursor.Epoch) > 128 ||
		!validLocalDouyinCommentID(cursor.WorkID) {
		return localDouyinExecutionContextCursor{}, errLocalDouyinExecutionContextCursor
	}
	return cursor, nil
}

func localDouyinExecutionContextRestart(workID string, limit int) localDouyinExecutionWorkContext {
	return localDouyinExecutionWorkContext{WorkID: workID, Ownership: "unverified", MediaPages: []localDouyinWorkContextPage{},
		EffectiveLimit: limit, Coverage: "作品证据或授权作用域已变化，请从 offset=0 重新读取；同一作品记忆作用域内其他作品更新也可能触发保守重启",
		SnapshotState: "restart_required", RestartRequired: true, ReplyEligible: false}
}

func localDouyinExecutionContextEpoch(ctx context.Context, home *localHomeService, source localDouyinMemorySource) (string, error) {
	db, err := openLocalDouyinMemory(ctx, home, false)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer db.Close()
	return douyinMemoryEpoch(ctx, db, source)
}

// A cursor pins one result set across HTTP and Agent pages. The memory epoch
// changes transactionally with any retained work text or media page. It is
// intentionally broader than one work: unrelated works may conservatively
// restart pagination, but no page from another version is silently mixed in.
func (m *localDouyinWebLogin) executionWorkContextPage(ctx context.Context, scope string, record localDouyinWebRecord,
	fish, owner, day, workID string, offset, limit int, rawCursor string) (localDouyinExecutionWorkContext, error) {
	var cursor localDouyinExecutionContextCursor
	if rawCursor != "" {
		var err error
		cursor, err = parseLocalDouyinExecutionContextCursor(rawCursor)
		if err != nil {
			return localDouyinExecutionWorkContext{}, err
		}
		offset = cursor.Offset
	}
	source, granted := douyinMemorySource(record, fish, owner, douyinMemoryWorks)
	if rawCursor != "" && (!granted || cursor.Date != day || cursor.WorkID != workID ||
		cursor.Grant != localCheckpointKey("douyin-context-cursor-v1", scope, source.Scope, record.Revision)) {
		return localDouyinExecutionContextRestart(workID, limit), nil
	}
	var epochBefore string
	if granted {
		var err error
		epochBefore, err = localDouyinExecutionContextEpoch(ctx, m.home, source)
		if err != nil {
			return localDouyinExecutionWorkContext{}, err
		}
	}
	if rawCursor != "" && (epochBefore == "" || cursor.Epoch != epochBefore) {
		return localDouyinExecutionContextRestart(workID, limit), nil
	}
	result, err := m.executionWorkContext(ctx, scope, record, fish, owner, day, workID, offset, limit)
	if err != nil {
		return result, err
	}
	var epochAfter string
	if granted {
		epochAfter, err = localDouyinExecutionContextEpoch(ctx, m.home, source)
		if err != nil {
			return localDouyinExecutionWorkContext{}, err
		}
		if epochAfter != epochBefore {
			return localDouyinExecutionContextRestart(workID, limit), nil
		}
	}
	if rawCursor != "" && (result.Ownership != "verified_own_work" || result.MediaSHA256 != cursor.MediaSHA256 ||
		localCheckpointKey("douyin-context-creator-v1", result.CreatorText, result.CreatorCoverage, result.CreatorObservedAt) != cursor.CreatorKey) {
		return localDouyinExecutionContextRestart(workID, limit), nil
	}
	if offset > 0 && rawCursor == "" {
		result.SnapshotState = "legacy_offset_unpinned"
		result.Coverage += "；旧 offset 续页未绑定证据快照，如需完整连续证据请从 offset=0 使用 next_cursor"
		return result, nil
	}
	if result.Ownership != "verified_own_work" {
		result.SnapshotState = "unverified"
		return result, nil
	}
	result.SnapshotState = "stable"
	if epochAfter != "" {
		result.contextEpoch = epochAfter
		result.contextGrant = localCheckpointKey("douyin-context-cursor-v1", scope, source.Scope, record.Revision)
	}
	if result.NextOffset > 0 {
		if epochAfter == "" {
			return localDouyinExecutionContextRestart(workID, limit), nil
		}
		position := localDouyinExecutionContextCursor{Version: 1,
			Grant: result.contextGrant,
			Date:  day, WorkID: workID, Offset: result.NextOffset, Epoch: epochAfter,
			CreatorKey:  localCheckpointKey("douyin-context-creator-v1", result.CreatorText, result.CreatorCoverage, result.CreatorObservedAt),
			MediaSHA256: result.MediaSHA256}
		raw, err := json.Marshal(position)
		if err != nil {
			return localDouyinExecutionWorkContext{}, err
		}
		result.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return result, nil
}
