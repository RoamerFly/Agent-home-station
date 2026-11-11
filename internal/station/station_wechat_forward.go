package station

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
)

const stationWechatForwardToolName = "wechat_forward_file"

var stationWechatForwardWords = regexp.MustCompile(`^(?:请\s*)?把群\s*(.+?)\s*的\s*(.+?)\s*(?:转发|转)到(?:这个群|本群)[。！!]?$`)

type stationWechatForwardSelection struct{ Group, File, Message string }

func stationWechatForwardCommand(in companion.Input) (stationWechatForwardSelection, bool) {
	if !in.IsGroup || in.OriginalText == nil || len(*in.OriginalText) > 2048 {
		return stationWechatForwardSelection{}, false
	}
	text := strings.TrimSpace(*in.OriginalText)
	if strings.ContainsAny(text, "\r\n") {
		return stationWechatForwardSelection{}, false
	}
	if strings.HasPrefix(text, "@") {
		// A native group mention is separated by U+2005. The actual sender and
		// directed turn are rechecked before the tool can read or send anything.
		if _, body, ok := strings.Cut(text, "\u2005"); ok {
			text = strings.TrimSpace(body)
		}
	}
	text = strings.TrimSpace(strings.TrimPrefix(text, "/ai "))
	if fields := strings.Fields(text); len(fields) == 4 && fields[0] == "转发原件" && fields[3] == "到这个群" && strings.HasSuffix(fields[1], "@chatroom") && identity.ValidPathID(fields[1]) && len(fields[2]) <= 512 {
		return stationWechatForwardSelection{Group: fields[1], Message: fields[2]}, true
	}
	if match := stationWechatForwardWords.FindStringSubmatch(text); len(match) == 3 {
		group := strings.TrimSpace(strings.Trim(match[1], "「」“”\""))
		file := strings.TrimSpace(strings.Trim(match[2], "「」“”\""))
		if group != "" && file != "" && len(group) <= 256 && len(file) <= 256 {
			return stationWechatForwardSelection{Group: group, File: file}, true
		}
	}
	return stationWechatForwardSelection{}, false
}

func stationWechatForwardTool() mcptools.Tool {
	return mcptools.NewTool(stationWechatForwardToolName, "执行本人这条原生消息明确授权的同账号文件原件转发。来源与当前目标群都由原话确定，无需参数。若需澄清，按实际提示请本人提供群编号和原消息编号；已提交后只核对回执。", `{"type":"object","properties":{},"additionalProperties":false}`)
}

func (*stationAttachmentTools) ToolTimeout(name string) time.Duration {
	if name == stationWechatForwardToolName {
		return 3 * time.Minute
	}
	return 0
}

// exactForwardMatch never selects the first fuzzy match. Every provider page
// must be read successfully before using a unique exact identity.
func exactForwardMatch(ctx context.Context, native *linuxnative.Sender, kind, thread, query string, check func() error, match func(map[string]any) string) (string, error) {
	var chosen, cursor string
	seen := map[string]bool{}
	for {
		if err := check(); err != nil {
			return "", err
		}
		page, err := native.Catalog(ctx, kind, thread, query, cursor)
		if err != nil {
			return "", err
		}
		if coverage, ok := page.Coverage.(map[string]any); ok && coverage["partial"] == true {
			return "", errForwardAmbiguous
		}
		for _, item := range page.Items {
			id := match(item)
			if id == "" {
				continue
			}
			if chosen != "" && chosen != id {
				return "", errForwardAmbiguous
			}
			chosen = id
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = *page.NextCursor
		if seen[cursor] {
			return "", apphost.ErrUnavailable
		}
		seen[cursor] = true
	}
	if chosen == "" {
		return "", errForwardAmbiguous
	}
	return chosen, nil
}

var errForwardAmbiguous = errors.New("转发来源不唯一或未找到，请提供来源群编号和文件原消息编号")

func (t *stationAttachmentTools) forwardOriginalFile(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct{}
	selection, ok := stationWechatForwardCommand(t.trace)
	if !ok || !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") || strictLocalMemoryDecode(raw, &args) != nil || t.crossDirectionAuthorization(false) == "" {
		return "", apphost.ErrObservationForbidden
	}
	worker, err := t.crossHost.worker(t.crossConnection, false)
	if err != nil || worker == nil {
		return "", apphost.ErrUnavailable
	}
	key := "cross-forward-" + localCheckpointKey(t.trace.Account, t.trace.Thread, t.trace.TraceID)[:40]
	// Serialize forward attempts without blocking the receipt list during a
	// download or upload. Only attempted rows are ever refreshed by readers.
	worker.forwardMu.Lock()
	defer worker.forwardMu.Unlock()
	root, err := openWechatCrossDelivery(worker, true)
	if err != nil {
		return "", err
	}
	defer root.Close()
	row, err := readWechatCrossDelivery(root, key, t.trace.Account)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err == nil && (row.Trace != t.trace.TraceID || row.Origin != t.trace.Thread || row.Target != t.trace.Thread) {
		return "", apphost.ErrObservationForbidden
	}
	sourceThread := selection.Group
	if row.Original != nil {
		sourceThread = row.Original.Thread
	} else if !strings.HasSuffix(sourceThread, "@chatroom") {
		// The directory is account data, so verify the native owner before
		// making even this metadata lookup.
		grants, _, policy, err := worker.crossAuthority(t.trace.Account)
		if err != nil {
			return "", err
		}
		eligible := ""
		for thread, grant := range grants.Sources {
			if thread != t.trace.Thread && grant.Equal(policy.HistoryGrants[thread]) && !policy.AttachmentGrants[thread].IsZero() {
				eligible = thread
				break
			}
		}
		if eligible == "" {
			return "", apphost.ErrObservationForbidden
		}
		catalogProof, err := t.crossScopeProof(ctx, stationWechatCrossProof{Thread: eligible}, true)
		if err != nil {
			return "", err
		}
		checkCatalog := func() error { _, err := t.crossScopeProof(ctx, catalogProof, true); return err }
		sourceThread, err = exactForwardMatch(ctx, t.native, "groups", "", selection.Group, checkCatalog, func(item map[string]any) string {
			if item["displayName"] != selection.Group && item["name"] != selection.Group && item["remark"] != selection.Group {
				return ""
			}
			id, _ := item["id"].(string)
			if id == "" {
				id, _ = item["roomId"].(string)
			}
			if identity.ValidPathID(id) && strings.HasSuffix(id, "@chatroom") {
				return id
			}
			return ""
		})
		if err != nil {
			return forwardSelectionResult(err)
		}
	}
	proof := stationWechatCrossProof{Thread: sourceThread}
	if row.Original != nil {
		proof = row.Proofs[0]
	}
	proof, err = t.crossScopeProof(ctx, proof, true)
	if err != nil {
		return "", err
	}
	if row.Original != nil && row.Parts[0].Attempted {
		worker.crossDeliveryMu.Lock()
		defer worker.crossDeliveryMu.Unlock()
		row, err = readWechatCrossDelivery(root, key, t.trace.Account)
		if err != nil {
			return "", err
		}
		return t.forwardReceipt(ctx, root, row)
	}
	checkProof := func() error { _, err := t.crossScopeProof(ctx, proof, true); return err }
	messageID := selection.Message
	if row.Original != nil {
		messageID = row.Original.Message
	} else if messageID == "" {
		messageID, err = exactForwardMatch(ctx, t.native, "messages", sourceThread, selection.File, checkProof, func(item map[string]any) string {
			if item["file_name"] != selection.File || item["threadId"] != sourceThread || item["direction"] != "incoming" {
				return ""
			}
			id, _ := item["id"].(string)
			return id
		})
		if err != nil {
			return forwardSelectionResult(err)
		}
		if err := checkProof(); err != nil {
			return "", err
		}
	}
	_, binding, policy, err := worker.crossAuthority(t.trace.Account)
	if err != nil {
		return "", err
	}
	message, err := t.native.Message(ctx, messageID)
	if err != nil {
		return "", err
	}
	kind, filename := wechatapp.AttachmentKind(message)
	if kind != "file" || message.AccountID != t.trace.Account || message.ThreadID != sourceThread || message.ID != messageID || selection.File != "" && filename != selection.File {
		return forwardSelectionResult(errForwardAmbiguous)
	}
	grant := policy.AttachmentGrants[sourceThread]
	targetGrant := policy.MediaReplyGrants[t.trace.Thread]
	// Streaming only needs a cheap live policy read. Before each transition,
	// the full check below also reopens the exact source and native request.
	allowed := func() bool {
		if ctx.Err() != nil || t.blocked() {
			return false
		}
		g, currentBinding, p, err := worker.crossAuthority(t.trace.Account)
		return err == nil && currentBinding.Revision == binding.Revision && g.OwnerRevision == binding.Revision && g.Revision == proof.GrantRevision && grant.Equal(p.AttachmentGrants[sourceThread]) && targetGrant.Equal(p.MediaReplyGrants[t.trace.Thread]) && g.Sources[sourceThread].Equal(p.HistoryGrants[sourceThread]) && g.Origins[t.trace.Thread].Equal(p.Grants[t.trace.Thread])
	}
	ref := &companion.SourceRef{MessageID: message.ID, ServerID: message.ServerID, Account: message.AccountID, Thread: message.ThreadID, Sender: message.SenderID, MessageType: message.Type, OccurredAt: message.CreatedAt, BodyHash: hexDigest([]byte(message.Body))}
	access := stationAttachmentAccess{native: t.native, dir: worker.dir, thread: sourceThread, message: messageID, grant: grant, allowed: allowed, ref: ref, store: t.store, history: true}
	original, err := access.source(ctx, sourceThread, messageID)
	if err != nil {
		return "", err
	}
	check := func() error {
		if err := checkProof(); err != nil {
			return err
		}
		return access.confirmObservationSource(ctx, original, proof.Epoch)
	}
	if row.Original == nil {
		row = stationWechatCrossDelivery{Version: 1, Key: key, Account: t.trace.Account, Origin: t.trace.Thread, Trace: t.trace.TraceID, Target: t.trace.Thread, Created: time.Now().UTC(), Proofs: []stationWechatCrossProof{proof}, Parts: []stationWechatCrossDeliveryPart{{Key: key + "-0"}}, Original: &original}
		if err := saveWechatCrossDelivery(root, row); err != nil {
			return "", err
		}
	} else if *row.Original != original {
		return "", wechatapp.ErrAttachmentDenied
	}
	if err := check(); err != nil {
		return "", err
	}
	if stopped, err := worker.deliveryCancelled(t.trace); err != nil || stopped {
		return "", apphost.ErrObservationForbidden
	}
	part := &row.Parts[0]
	if status, lookupErr := t.native.LookupSend(ctx, part.Key); lookupErr == nil {
		if !row.matchesStatus(*part, status) {
			return "", apphost.ErrUnavailable
		}
		part.Attempted, part.Status = true, status
		if err := saveWechatCrossDelivery(root, row); err != nil {
			return "", err
		}
		return t.forwardResult(row)
	} else if !errors.Is(lookupErr, linuxnative.ErrToolNotFound) {
		return "", lookupErr
	}
	if part.Upload == "" {
		record, file, err := access.open(ctx, original)
		if errors.Is(err, os.ErrNotExist) {
			if err = access.DownloadAttachment(ctx, sourceThread, messageID); err != nil {
				return "", err
			}
			record, file, err = access.open(ctx, original)
		}
		if err != nil {
			return "", err
		}
		defer file.Close()
		if err := check(); err != nil {
			return "", err
		}
		part.Upload, err = wechatapp.StageMediaUpload(ctx, t.native, wechatapp.MediaUpload{Account: row.Account, Filename: original.Filename, MIME: record.MIME, Bytes: record.Bytes, SHA256: record.SHA256}, file, allowed)
		if err != nil {
			return "", err
		}
		if err := saveWechatCrossDelivery(root, row); err != nil {
			return "", err
		}
	}
	worker.crossDeliveryMu.Lock()
	defer worker.crossDeliveryMu.Unlock()
	err = worker.withDeliverySubmit(ctx, t.trace, func() error {
		if err := check(); err != nil {
			return err
		}
		part.Attempted = true
		if err := saveWechatCrossDelivery(root, row); err != nil {
			return err
		}
		if err := check(); err != nil {
			return err
		}
		status, err := t.native.Submit(ctx, linuxnative.ToolSend{ThreadID: row.Target, Kind: "file", SourceID: part.Upload, IdempotencyKey: part.Key})
		if err != nil || !row.matchesStatus(*part, status) {
			return apphost.ErrUnavailable
		}
		part.Status = status
		return saveWechatCrossDelivery(root, row)
	})
	if err != nil && !part.Attempted {
		return "", err
	}
	return t.forwardResult(row)
}

func forwardSelectionResult(err error) (string, error) {
	if !errors.Is(err, errForwardAmbiguous) {
		return "", err
	}
	return localMemoryJSON(map[string]string{"state": "needs_clarification", "message": err.Error()})
}

func (t *stationAttachmentTools) forwardReceipt(ctx context.Context, root *os.Root, row stationWechatCrossDelivery) (string, error) {
	part := &row.Parts[0]
	if status, err := t.native.LookupSend(ctx, part.Key); err == nil && row.matchesStatus(*part, status) {
		part.Status = status
		if err := saveWechatCrossDelivery(root, row); err != nil {
			return "", err
		}
	}
	return t.forwardResult(row)
}

func (t *stationAttachmentTools) forwardResult(row stationWechatCrossDelivery) (string, error) {
	return localMemoryJSON(t.crossHost.crossMemoryReceipt(t.crossConnection, row))
}
