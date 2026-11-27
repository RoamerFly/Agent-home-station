package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/stationbrowser"
)

const localDouyinNoticeReplyToolName = "douyin_notice_reply"

func localDouyinNoticeReplyTool() mcptools.Tool {
	return mcptools.NewTool(localDouyinNoticeReplyToolName,
		"仅为已保存通知创建限定抖音回复任务。create 前须在本轮完整读取同一作品的简介、音轨与画面证据，并对 locate 得到的作品 content_id 先用 app_observe(comments) 读取一页评论以建立当前作品入口；提供通知目标 CID 路径及回复正文。宿主重读目标、主楼与直接父评论，只接受主评论或直接回复本账号子评论。create 只建待提交任务；submit/resume/status/cancel 使用原 job_id。回执待确认时只查询原任务。",
		`{"type":"object","properties":{"action":{"type":"string","enum":["create","submit","resume","status","cancel"]},"date":{"type":"string","pattern":"^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},"notice_id":{"type":"string","pattern":"^[1-9][0-9]{0,19}$"},"target_path":{"type":"string","enum":["cid","real_cid","comment.cid","comment.comment.cid","reply_comment.cid"]},"target_id":{"type":"string","pattern":"^[1-9][0-9]{0,19}$"},"root_path":{"type":"string","enum":["reply_id","comment.parent_id","comment.reply_id","comment.comment.reply_id","reply_comment.reply_id"]},"root_id":{"type":"string","pattern":"^[1-9][0-9]{0,19}$"},"content_id":{"type":"string","minLength":1,"maxLength":256},"text":{"type":"string","minLength":1},"job_id":{"type":"string","minLength":64,"maxLength":64}},"required":["action"],"additionalProperties":false}`)
}

type localDouyinNoticeReplyInput struct {
	Action     string `json:"action"`
	Date       string `json:"date"`
	NoticeID   string `json:"notice_id"`
	TargetPath string `json:"target_path"`
	TargetID   string `json:"target_id"`
	RootPath   string `json:"root_path"`
	RootID     string `json:"root_id"`
	ContentID  string `json:"content_id"`
	Text       string `json:"text"`
	JobID      string `json:"job_id"`
}

func validLocalDouyinNoticeReplyCreate(in localDouyinNoticeReplyInput) bool {
	date, err := time.Parse("2006-01-02", in.Date)
	if err != nil || date.Format("2006-01-02") != in.Date || in.JobID != "" ||
		!validLocalDouyinCommentID(in.NoticeID) || !validDouyinRoundRelationTargetPath(in.TargetPath) ||
		!validLocalDouyinCommentID(in.TargetID) || (in.RootPath == "") != (in.RootID == "") ||
		in.RootPath != "" && (!validDouyinRoundRelationRootPath(in.RootPath) || !validLocalDouyinCommentID(in.RootID) || in.RootID == in.TargetID) ||
		!strings.HasPrefix(in.ContentID, douyinapp.WebCreatorWorkPrefix) || len(in.ContentID) > 256 ||
		!validLocalDouyinWebID(strings.TrimPrefix(in.ContentID, douyinapp.WebCreatorWorkPrefix)) ||
		strings.TrimSpace(in.Text) == "" || len(in.Text) > 64<<10 {
		return false
	}
	return true
}

func localDouyinReplyCommentFromRelation(row stationbrowser.DouyinCommentRelation) localDouyinReplyComment {
	return localDouyinReplyComment{CID: row.CID, WorkID: row.WorkID, ReplyID: row.ReplyID,
		ReplyToReplyID: row.ReplyToReplyID, AuthorUID: row.AuthorUID}
}

func (t *localToolSet) callDouyinNoticeReply(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.publications == nil || t.projectTools == nil || t.run == nil || !t.run.Tracked ||
		t.access == nil || !t.douyinMemoryRequested || t.checkAppObservationIdentity() != nil {
		return "", apphost.ErrInteractionDenied
	}
	if err := t.run.CheckSource(ctx); err != nil {
		return "", err
	}
	var in localDouyinNoticeReplyInput
	if strictLocalMemoryDecode(raw, &in) != nil {
		return "", mcptools.ArgumentError("$", "通知回复参数无效")
	}
	host, actor, err := t.publications.interactionHost()
	if err != nil {
		return "", err
	}
	if in.Action != "create" {
		if in.Action != "submit" && in.Action != "resume" && in.Action != "status" && in.Action != "cancel" ||
			!localDouyinPublicationHex(in.JobID) || in.Date != "" || in.NoticeID != "" || in.TargetPath != "" ||
			in.TargetID != "" || in.RootPath != "" || in.RootID != "" || in.ContentID != "" || in.Text != "" {
			return "", mcptools.ArgumentError("$", "请仅提供原通知回复任务编号和操作")
		}
		if t.inspectOnly && (in.Action == "submit" || in.Action == "resume") {
			return "", apphost.ErrInteractionDenied
		}
		project, err := t.projectTools.forFish(t.access.fishID)
		if err != nil {
			return "", err
		}
		job, err := project.store.Job(ctx, in.JobID)
		if err != nil || job.Tool != "interaction" || job.Plugin != "application/"+douyinapp.ID ||
			job.Context.Account != actor.Account || job.Context.Requester != actor.Requester || job.Context.Thread != actor.Thread {
			return "", apphost.ErrInteractionOwnership
		}
		var saved struct {
			Draft apphost.InteractionDraft `json:"draft"`
		}
		var origin localDouyinNoticeReplyOrigin
		if json.Unmarshal(job.Arguments, &saved) != nil || json.Unmarshal(saved.Draft.Origin, &origin) != nil ||
			saved.Draft.Target.AppID != douyinapp.ID || saved.Draft.Kind != "reply_comment" ||
			origin.Kind != "douyin_notice_reply_v1" || origin.Version != 1 {
			return "", apphost.ErrInteractionDenied
		}
		view, err := host.Action(ctx, actor, in.JobID, in.Action)
		if err != nil {
			return "", err
		}
		return localProcessJSON(view)
	}
	if t.inspectOnly || !validLocalDouyinNoticeReplyCreate(in) {
		return "", mcptools.ArgumentError("$", "请提供本轮已核对通知、目标评论、本人作品入口及回复正文")
	}
	m := t.publications.home.douyinWeb
	if m == nil {
		return "", apphost.ErrInteractionDenied
	}
	record, profile, err := m.noticeState(t.access.fishID)
	if err != nil || !record.NoticeAgent || record.NoticeAccount == "" {
		return "", apphost.ErrInteractionDenied
	}
	noticeProof := localDouyinNoticeProof{record: record, profile: profile}
	if err := t.checkNoticeProof(ctx, noticeProof); err != nil {
		return "", err
	}
	scope := localDouyinExecutionScope(t.access.fishID, t.access.ownerID, record)
	evidence, found, err := m.readExecutionNotice(ctx, scope, in.NoticeID)
	if err != nil || !found || evidence.observedDate != in.Date {
		return "", apphost.ErrInteractionDenied
	}
	completed, err := t.completedDouyinRoundContextRead(ctx, in.Date, evidence.round.WorkID)
	if err != nil {
		return "", err
	}
	relationIn := localDouyinRoundsInput{Action: "relation", Date: in.Date, NoticeID: in.NoticeID,
		TargetPath: in.TargetPath, TargetID: in.TargetID, RootPath: in.RootPath, RootID: in.RootID}
	relation, contextProof, err := t.readDouyinRoundRelation(ctx, m, record, profile, scope, relationIn, noticeProof)
	if err != nil || contextProof == nil || relation.State != "observed" || relation.CID != in.TargetID {
		return "", apphost.ErrInteractionDenied
	}
	if err := t.checkDouyinRoundContextProof(ctx, *contextProof); err != nil {
		return "", err
	}
	candidates, source, valid := douyinRoundRelationCandidates(evidence.round)
	if !valid || source != relation.CandidateSource || !douyinRoundCandidateMatches(candidates, in.TargetPath, in.TargetID) {
		return "", apphost.ErrInteractionDenied
	}
	policy := localDouyinNoticeReplyProof{NoticeID: in.NoticeID, NoticeType: evidence.round.Type,
		NoticeWorkID: evidence.round.WorkID, CandidateSource: source, DetailStatus: evidence.round.DetailStatus,
		TargetPath: in.TargetPath, RootPath: in.RootPath, RootCandidateID: in.RootID, Candidates: candidates,
		NoticeCreatorAccount: evidence.creatorAccount, WorksCreatorAccount: evidence.creatorAccount,
		CurrentCreatorAccount: evidence.creatorAccount, WorkID: evidence.round.WorkID, ContextReady: true,
		Target: localDouyinReplyCommentFromRelation(relation.targetRow)}
	if source == "notice_detail" {
		policy.DetailID, policy.DetailWorkID, policy.DetailCID = in.NoticeID, evidence.round.WorkID, in.TargetID
	}
	if relation.rootRow != nil {
		root := localDouyinReplyCommentFromRelation(*relation.rootRow)
		policy.Root = &root
	}
	if relation.parentRow != nil {
		parent := localDouyinReplyCommentFromRelation(*relation.parentRow)
		policy.Parent = &parent
	}
	decision := classifyLocalDouyinNoticeReply(policy)
	if !decision.Eligible {
		return "", mcptools.ArgumentError("$.target_id", "该通知的结构化评论关系不符合限定回复范围："+decision.Reason)
	}
	observation := &localDouyinWebObservation{home: m.home}
	access := observation.access(t.access.fishID, t.access.ownerID, record, m.view(record))
	target, ok := observation.creatorCommentTarget(access, in.ContentID)
	if !ok || target.videoID != evidence.round.WorkID || target.creatorAccount != evidence.creatorAccount {
		return "", apphost.ErrInteractionDenied
	}
	draft := apphost.InteractionDraft{Kind: "reply_comment", ContentID: in.ContentID, CommentID: relation.targetRow.CID, Text: in.Text}
	draft.Target, err = host.ResolveTarget(ctx, douyinapp.ID, douyinapp.WebReadConnectionID)
	if err != nil {
		return "", err
	}
	if err := observation.saveCommentProof(access, target.videoID, stationbrowser.DouyinComment{
		ID: relation.targetRow.CID, AuthorID: relation.targetRow.AuthorUID, Text: relation.targetRow.Text}); err != nil {
		return "", err
	}
	commentDigest := sha256.Sum256([]byte(relation.targetRow.Text))
	draft.CommentVersion = hex.EncodeToString(commentDigest[:])
	grant, err := t.access.resolve()
	if err != nil {
		return "", err
	}
	chatSource, ok := t.artifactSource(grant)
	if !ok || chatSource == nil {
		return "", apphost.ErrInteractionDenied
	}
	t.noticeReads = append(t.noticeReads, noticeProof)
	t.douyinRoundProofs = append(t.douyinRoundProofs, *contextProof)
	works, worksGranted := douyinMemorySource(record, t.access.fishID, t.access.ownerID, douyinMemoryWorks)
	comments, commentsGranted := douyinMemorySource(record, t.access.fishID, t.access.ownerID, douyinMemoryComments)
	if !worksGranted || !commentsGranted {
		return "", apphost.ErrInteractionDenied
	}
	origin := localDouyinNoticeReplyOrigin{localPublicationOrigin: t.publicationOrigin(chatSource),
		Kind: "douyin_notice_reply_v1", Version: 1, Date: in.Date, NoticeID: in.NoticeID,
		WorkID: target.videoID, CreatorAccount: evidence.creatorAccount,
		NoticeEvidenceKey: localDouyinNoticeReplyEvidenceKey(evidence), CandidateSource: source,
		TargetPath: in.TargetPath, TargetID: in.TargetID, RootPath: in.RootPath, RootID: in.RootID,
		ThreadState: relation.ThreadState, TargetTextSHA: draft.CommentVersion,
		TargetAuthorDigest: localDouyinNoticeReplyTextSHA(relation.targetRow.AuthorUID),
		MediaSHA:           completed.MediaSHA, CreatorKey: completed.CreatorKey,
		ContextEpoch: completed.Epoch, ContextGrant: completed.Grant,
		WorksScope: works.Scope, CommentsScope: comments.Scope, RecordRevision: record.Revision}
	if relation.parentRow != nil {
		origin.ParentID = relation.parentRow.CID
		origin.ParentAuthorDigest = evidence.creatorAccount
		if origin.RootID == "" {
			origin.RootID = relation.ReplyID
		}
	}
	draft.Origin, _ = json.Marshal(origin)
	actor.Authorization = localCheckpointKey(grant)
	actor.SourceVersions = map[string]int{chatSource.EventID: chatSource.Revision}
	actor.RequestKey, ok = localDouyinNoticeReplyRequestKey(t.access.fishID, t.access.ownerID, evidence.creatorAccount, in.NoticeID)
	if !ok {
		return "", apphost.ErrInteractionDenied
	}
	view, err := host.Create(ctx, actor, draft)
	if err != nil {
		return "", err
	}
	if err := t.run.CheckSource(ctx); err != nil {
		return "", err
	}
	return localProcessJSON(view)
}
