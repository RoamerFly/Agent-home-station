package station

import (
	"context"
	"encoding/json"
	"errors"
)

func (h *localChatService) checkPublicationNotices(ctx context.Context, fish, owner string, notices []localPublicationNotice) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, notice := range notices {
		if notice.AppID != "douyin" || !localDouyinPublicationHex(notice.GrantVersion) || h.home.douyinWeb == nil {
			return errors.New("原作品引用的互动通知来源不可用")
		}
		record, _, err := h.home.douyinWeb.noticeState(fish)
		if err != nil || !record.NoticeAgent || record.NoticeAccount == "" || notice.GrantVersion != localNoticeGrantVersion(fish, owner, record) {
			return errors.New("原作品引用的互动通知已撤权或变更")
		}
	}
	return nil
}

// Artifact IDs and original bytes remain immutable. New disclosure follows
// the existing source/parent chain to journaled authorization metadata instead
// of copying old notification text into another request or artifact record.
func (h *localChatService) checkArtifactNoticeSources(ctx context.Context, artifact localArtifact) error {
	store, in, err := h.conversation()
	if err != nil || in.Account != artifact.Fish || in.Sender != artifact.Owner {
		return errors.New("成品来源不属于当前主人")
	}
	fish, owner := artifact.Fish, artifact.Owner
	seen, seenSources := map[string]bool{}, map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if seen[artifact.ID] || artifact.Fish != fish || artifact.Owner != owner {
			return errors.New("成品父来源链无效")
		}
		seen[artifact.ID] = true
		if artifact.Source != nil {
			key := localCheckpointKey(artifact.Source.InstanceID, artifact.Source.EventID, artifact.Source.Revision)
			if !seenSources[key] {
				in.TraceID, in.Source, in.MessageTime = artifact.Request, artifact.Source, artifact.Source.OccurredAt
				operations, err := store.LocalToolEvidence(ctx, in, localInboxToolName)
				if err != nil {
					return errors.New("成品原请求的工具证据无法核对")
				}
				for _, op := range operations {
					var proof localInboxCheckpoint
					// Old/unknown reads have no verifiable source version. Keep the
					// local work, but never invent a grant from today's settings.
					if json.Unmarshal([]byte(op.Result), &proof) != nil || proof.Version != 1 || proof.State != "read" || proof.BodyRetained || len(proof.Notices) == 0 {
						return errors.New("成品的通知来源缺少可核对的授权版本")
					}
					if err := h.checkPublicationNotices(ctx, fish, owner, proof.Notices); err != nil {
						return err
					}
				}
				seenSources[key] = true
			}
		}
		if artifact.Parent == nil {
			return nil
		}
		parent, err := h.home.artifacts.readRecord(artifact.Parent.ID)
		if err != nil || parent.SHA256 != artifact.Parent.SHA256 {
			return errors.New("成品父来源已变化")
		}
		artifact = parent
	}
}
