package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/pluginhost"
)

const localDouyinNoticeReplyOriginKind = "douyin_notice_reply_v1"

// Only the trusted notice-reply tool creates this origin. The application job
// manifest keeps it immutable. The host rechecks the saved, account-scoped
// notice and complete work context before each effect boundary. Browser comment
// text is independently checked by the interaction provider before clicking.
type localDouyinNoticeReplyOrigin struct {
	localPublicationOrigin
	Kind               string `json:"kind"`
	Version            int    `json:"version"`
	Date               string `json:"date"`
	NoticeID           string `json:"notice_id"`
	WorkID             string `json:"work_id"`
	CreatorAccount     string `json:"creator_account"`
	NoticeEvidenceKey  string `json:"notice_evidence_key"`
	CandidateSource    string `json:"candidate_source"`
	TargetPath         string `json:"target_path"`
	TargetID           string `json:"target_id"`
	RootPath           string `json:"root_path,omitempty"`
	RootID             string `json:"root_id,omitempty"`
	ThreadState        string `json:"thread_state"`
	ParentID           string `json:"parent_id,omitempty"`
	TargetAuthorDigest string `json:"target_author_digest"`
	ParentAuthorDigest string `json:"parent_author_digest,omitempty"`
	TargetTextSHA      string `json:"target_text_sha256"`
	MediaSHA           string `json:"media_sha256"`
	CreatorKey         string `json:"creator_key"`
	ContextEpoch       string `json:"context_epoch"`
	ContextGrant       string `json:"context_grant"`
	WorksScope         string `json:"works_scope"`
	CommentsScope      string `json:"comments_scope"`
	RecordRevision     uint64 `json:"record_revision"`
}

func localDouyinNoticeReplyEvidenceKey(evidence localDouyinExecutionNoticeEvidence) string {
	round := evidence.round
	return localCheckpointKey("douyin-notice-reply-evidence-v1", evidence.observedDate, evidence.creatorAccount,
		round.NoticeID, round.Type, round.WorkID, round.CreatedAt, round.ObservedAt,
		round.Candidates, round.DetailStatus, round.DetailCandidates)
}

func validLocalDouyinNoticeReplyOrigin(in localDouyinNoticeReplyOrigin, draft apphost.InteractionDraft) bool {
	day, err := time.Parse("2006-01-02", in.Date)
	if err != nil || day.Format("2006-01-02") != in.Date || in.Kind != localDouyinNoticeReplyOriginKind || in.Version != 1 ||
		!validLocalDouyinCommentID(in.NoticeID) || !validLocalDouyinCommentID(in.WorkID) ||
		!validDouyinNoticeAccount(in.CreatorAccount) || !localDouyinPublicationHex(in.NoticeEvidenceKey) ||
		!validDouyinRoundRelationTargetPath(in.TargetPath) || !validLocalDouyinCommentID(in.TargetID) ||
		in.RootPath != "" && (!validDouyinRoundRelationRootPath(in.RootPath) || !validLocalDouyinCommentID(in.RootID)) ||
		!localDouyinPublicationHex(in.TargetAuthorDigest) || in.TargetAuthorDigest == in.CreatorAccount ||
		!localDouyinPublicationHex(in.TargetTextSHA) || !localDouyinPublicationHex(in.MediaSHA) ||
		!localDouyinPublicationHex(in.CreatorKey) || !localDouyinPublicationHex(in.ContextGrant) ||
		in.ContextEpoch == "" || len(in.ContextEpoch) > 128 ||
		!localDouyinPublicationHex(in.WorksScope) || !localDouyinPublicationHex(in.CommentsScope) ||
		in.RecordRevision == 0 || draft.Target.AppID != douyinapp.ID || draft.Kind != "reply_comment" ||
		draft.CommentID != in.TargetID || draft.CommentVersion != in.TargetTextSHA ||
		draft.ContentID == "" || draft.Desired {
		return false
	}
	switch in.ThreadState {
	case "top_level_observed":
		return in.RootPath == "" && in.RootID == "" && in.ParentID == "" && in.ParentAuthorDigest == ""
	case "self_child_observed":
		return validLocalDouyinCommentID(in.RootID) && validLocalDouyinCommentID(in.ParentID) &&
			in.RootID != in.ParentID && in.RootID != in.TargetID && in.ParentID != in.TargetID &&
			in.ParentAuthorDigest == in.CreatorAccount
	default:
		return false
	}
}

func (h *localChatService) validateDouyinNoticeReply(ctx context.Context, job pluginhost.Job, draft apphost.InteractionDraft) error {
	deny := func() error { return apphost.ErrInteractionDenied }
	if len(draft.Origin) == 0 || len(draft.Origin) > 128<<10 || h.home == nil || h.home.douyinWeb == nil {
		return deny()
	}
	var origin localDouyinNoticeReplyOrigin
	if strictLocalMemoryDecode(draft.Origin, &origin) != nil || !validLocalDouyinNoticeReplyOrigin(origin, draft) ||
		job.Context.Account == "" || job.Context.Requester == "" || job.Context.Thread != job.Context.Requester {
		return deny()
	}
	if h.validatePublication(ctx, job, apphost.PublicationDraft{Origin: draft.Origin}) != nil {
		return deny()
	}
	m := h.home.douyinWeb
	record, _, err := m.noticeState(job.Context.Account)
	if err != nil || !record.NoticeAgent || record.Revision != origin.RecordRevision ||
		record.NoticeAccount != origin.CreatorAccount ||
		localNoticeGrantVersion(job.Context.Account, job.Context.Requester, record) != localNoticeGrantVersionFromOrigin(origin) {
		return deny()
	}
	works, worksGranted := douyinMemorySource(record, job.Context.Account, job.Context.Requester, douyinMemoryWorks)
	comments, commentsGranted := douyinMemorySource(record, job.Context.Account, job.Context.Requester, douyinMemoryComments)
	if !worksGranted || !commentsGranted || works.Scope != origin.WorksScope || comments.Scope != origin.CommentsScope {
		return deny()
	}
	accessor := &localDouyinWebObservation{home: h.home}
	access := accessor.access(job.Context.Account, job.Context.Requester, record, m.view(record))
	if !creatorOwnerCurrent(access, origin.CreatorAccount) {
		return deny()
	}
	scope := localDouyinExecutionScope(job.Context.Account, job.Context.Requester, record)
	evidence, found, err := m.readExecutionNotice(ctx, scope, origin.NoticeID)
	if err != nil || !found || evidence.observedDate != origin.Date || evidence.round.WorkID != origin.WorkID ||
		evidence.creatorAccount != origin.CreatorAccount || localDouyinNoticeReplyEvidenceKey(evidence) != origin.NoticeEvidenceKey {
		return deny()
	}
	candidates, source, valid := douyinRoundRelationCandidates(evidence.round)
	if !valid || source != origin.CandidateSource || !douyinRoundCandidateMatches(candidates, origin.TargetPath, origin.TargetID) ||
		origin.RootPath != "" && !douyinRoundCandidateMatches(candidates, origin.RootPath, origin.RootID) {
		return deny()
	}
	current, err := m.executionWorkContextPage(ctx, scope, record, job.Context.Account, job.Context.Requester,
		origin.Date, origin.WorkID, 0, 1, "")
	if err != nil || current.RestartRequired || current.SnapshotState != "stable" || current.Ownership != "verified_own_work" ||
		!current.AudioComplete || !current.VisualComplete || current.MediaPageCount < 1 ||
		current.MediaSHA256 != origin.MediaSHA || current.contextEpoch != origin.ContextEpoch ||
		current.contextGrant != origin.ContextGrant ||
		localCheckpointKey("douyin-context-creator-v1", current.CreatorText, current.CreatorCoverage, current.CreatorObservedAt) != origin.CreatorKey {
		return deny()
	}
	entry, registered := h.applications.Lookup(douyinapp.ID)
	if !registered {
		return deny()
	}
	accessor.installation = entry.InstallationID
	if accessor.ref(access) != draft.Target {
		return deny()
	}
	target, valid := accessor.creatorCommentTarget(access, draft.ContentID)
	if !valid || target.videoID != origin.WorkID || target.creatorAccount != origin.CreatorAccount {
		return deny()
	}
	comment, err := accessor.commentProof(access, origin.WorkID, origin.TargetID)
	if err != nil || comment.SHA256 != origin.TargetTextSHA ||
		localDouyinNoticeReplyTextSHA(comment.AuthorID) != origin.TargetAuthorDigest {
		return deny()
	}
	return ctx.Err()
}

func localNoticeGrantVersionFromOrigin(origin localDouyinNoticeReplyOrigin) string {
	for _, notice := range origin.Notices {
		if notice.AppID == douyinapp.ID {
			return notice.GrantVersion
		}
	}
	return ""
}

func localDouyinNoticeReplyTextSHA(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
