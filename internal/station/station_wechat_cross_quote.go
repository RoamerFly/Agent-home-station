package station

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type stationCrossQuoteReceipt struct {
	Delivery stationWechatCrossDelivery
	Part     stationWechatCrossDeliveryPart
}

// Find a private reply using account, thread and the native server message ID.
// An ambiguous or pending match only protects the body. It cannot resume a
// request. The ledger is written by atomic replacement, including during send.
func findStationCrossQuote(ctx context.Context, worker *stationWechatWorker, message linuxnative.Message) (*stationCrossQuoteReceipt, bool, error) {
	if message.Direction != "outgoing" || strings.HasSuffix(message.ThreadID, "@chatroom") {
		return nil, false, nil
	}
	root, err := openWechatCrossDelivery(worker, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, true, err
	}
	defer dir.Close()
	var match *stationCrossQuoteReceipt
	protected := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, true, err
		}
		entries, readErr := dir.ReadDir(128)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, true, err
			}
			name := entry.Name()
			if !strings.HasPrefix(name, "cross-reply-") || !strings.HasSuffix(name, ".json") {
				continue
			}
			row, err := readWechatCrossDelivery(root, strings.TrimSuffix(name, ".json"), message.AccountID)
			if err != nil {
				return nil, true, err
			}
			if row.Target != message.ThreadID {
				continue
			}
			for _, part := range row.Parts {
				if !part.Attempted {
					continue
				}
				if part.Status.ServerID == "" || message.ServerID == "" {
					if part.BodyUnverified || strings.TrimSpace(part.Text) == strings.TrimSpace(message.Body) {
						if protected {
							return nil, true, apphost.ErrObservationForbidden
						}
						protected = true
					}
					continue
				}
				if part.Status.ServerID != message.ServerID {
					continue
				}
				if protected {
					return nil, true, apphost.ErrObservationForbidden
				}
				protected = true
				if part.Status.State == "accepted" && !part.BodyUnverified && validCrossDeliveryStatus(part.Status, row.Target) &&
					part.Status.AccountID == row.Account && message.Type == 1 && part.Text == message.Body {
					match = &stationCrossQuoteReceipt{Delivery: row, Part: part}
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return match, protected, nil
		}
		if readErr != nil {
			return nil, true, readErr
		}
	}
}

func (t *stationAttachmentTools) protectCrossReply(ctx context.Context, message linuxnative.Message) bool {
	if t.crossHost == nil || message.Direction != "outgoing" || strings.HasSuffix(message.ThreadID, "@chatroom") {
		return false
	}
	worker, err := t.crossHost.worker(t.crossConnection, false)
	if err != nil || worker == nil || t.native == nil || t.native.Account() != message.AccountID {
		return true
	}
	_, protected, err := findStationCrossQuote(ctx, worker, message)
	return protected || err != nil
}

func (t *stationAttachmentTools) linkedQuoteCandidate() bool {
	in := t.trace
	return t.crossHost != nil && t.crossHost.ownerReader != nil && t.store != nil && t.native != nil && t.blocked != nil && !t.blocked() &&
		in.PrivateQuote != "" && in.Channel == "wechat" && in.Account == t.native.Account() && in.Thread == in.Sender && in.Thread != "filehelper" &&
		!in.IsGroup && !strings.HasSuffix(in.Thread, "@chatroom") && in.Mode == "direct" && in.Directed && in.TraceID != "" &&
		in.OriginalText != nil && in.Source != nil && in.Source.MessageType == 49 && !in.Preview && !in.Surf && !in.ReportOnly &&
		!in.HistoricalReview && !in.BackgroundResult && !in.ConversationDelivery && !in.SourceOnlyContext && in.OriginTraceID == "" && in.TaskID == "" && in.ReplyContext == nil
}

type linkedWechatQuote struct {
	receipt *stationCrossQuoteReceipt
	parent  linuxnative.Message
	current *companion.SourceRef
	dataset string
}

func (t *stationAttachmentTools) verifyLinkedWechatQuote(ctx context.Context) (linkedWechatQuote, error) {
	var verified linkedWechatQuote
	deny := apphost.ErrObservationForbidden
	if !t.linkedQuoteCandidate() || ctx.Err() != nil {
		return verified, deny
	}
	in := t.trace
	host := t.crossHost
	worker, err := host.worker(t.crossConnection, false)
	if err != nil || worker == nil {
		return verified, deny
	}
	owner, err := host.ownerReader()
	if err != nil {
		return verified, deny
	}
	daemon, err := t.native.DaemonStatus(ctx)
	if err != nil {
		return verified, deny
	}
	_, binding, policy, err := worker.wechatFileAuthority(in.Account, stationWechatCrossGrantsFile)
	b := binding.Binding
	stamp, timeErr := time.Parse(time.RFC3339Nano, in.MessageTime)
	grant := policy.Grants[in.Thread]
	if err != nil || timeErr != nil || b == nil || b.Owner != owner.ID || b.Fish != owner.FishID || b.Sender != in.Sender ||
		b.Dataset != daemon.ServerID || grant.IsZero() || stamp.Before(grant) || stamp.Before(b.BoundAt) {
		return verified, deny
	}
	message, err := t.native.Message(ctx, in.TraceID)
	if err != nil || !matchesLinkedWechatInput(message, in, stamp) {
		return verified, deny
	}
	ref, ok := t.store.SourceForInput(in)
	allowed, recallErr := t.store.NativeRecallAllowed(ctx, in)
	if !ok || ref.EventID == "" || recallErr != nil || !allowed {
		return verified, deny
	}
	quoted, err := t.native.QuotedMessage(ctx, in.TraceID)
	if err != nil || quoted.State != "resolved" || quoted.ThreadID != in.Thread || quoted.Message == nil || quoted.Message.ServerID != in.PrivateQuote {
		return verified, deny
	}
	receipt, _, err := findStationCrossQuote(ctx, worker, *quoted.Message)
	if err != nil || receipt == nil || len(receipt.Delivery.Used) == 0 || receipt.Delivery.Trace == in.TraceID || receipt.Delivery.Created.Truncate(time.Second).After(stamp) {
		return verified, deny
	}
	status, err := t.native.LookupSend(ctx, receipt.Part.Key)
	if err != nil || status.ID != receipt.Part.Status.ID || status.ServerID != in.PrivateQuote || status.State != "accepted" || !receipt.Delivery.matchesStatus(receipt.Part, status) {
		return verified, deny
	}
	parent, err := t.native.Message(ctx, receipt.Delivery.Trace)
	priorAt, priorErr := time.Parse(time.RFC3339Nano, parent.CreatedAt)
	if err != nil || priorErr != nil || !priorAt.Before(stamp) || parent.ID != receipt.Delivery.Trace || parent.AccountID != in.Account ||
		parent.SenderID != in.Sender || parent.ThreadID != receipt.Delivery.Origin || parent.Direction != "incoming" || parent.Type != 1 && !parent.IsQuotedText() {
		return verified, deny
	}
	freshOwner, ownerErr := host.ownerReader()
	_, freshBinding, current, grantErr := worker.wechatFileAuthority(in.Account, stationWechatCrossGrantsFile)
	if ownerErr != nil || freshOwner != owner || grantErr != nil || freshBinding.Revision != binding.Revision || !current.Grants[in.Thread].Equal(grant) ||
		!wechatapp.SameNativeOwnership(ctx, t.native, daemon) || t.blocked() {
		return verified, deny
	}
	return linkedWechatQuote{receipt: receipt, parent: parent, current: ref, dataset: daemon.ServerID}, nil
}

func matchesLinkedWechatInput(message linuxnative.Message, in companion.Input, stamp time.Time) bool {
	at, err := time.Parse(time.RFC3339Nano, message.CreatedAt)
	return err == nil && at.Equal(stamp) && message.ID == in.TraceID && message.AccountID == in.Account && message.ThreadID == in.Thread &&
		message.SenderID == in.Sender && message.Direction == "incoming" && message.IsQuotedText() && message.Quote != nil &&
		message.Quote.Type == 1 && message.Quote.ServerID == in.PrivateQuote && message.ServerID == in.Source.ServerID &&
		strings.TrimSpace(message.Body) == *in.OriginalText
}

func sameLinkedMemorySource(used, requested stationWechatCrossProof) bool {
	return used.Thread == requested.Thread && used.Station == requested.Station && used.Douyin == requested.Douyin &&
		used.Archived == requested.Archived && (!used.Douyin || used.DouyinScope != "" && (requested.DouyinScope == "" || used.DouyinScope == requested.DouyinScope))
}

func (t *stationAttachmentTools) linkedCrossProof(ctx context.Context, requested stationWechatCrossProof) (stationWechatCrossProof, error) {
	link, err := t.verifyLinkedWechatQuote(ctx)
	if err != nil {
		return stationWechatCrossProof{}, err
	}
	return t.linkedProofFromReceipt(ctx, link, requested)
}

func (t *stationAttachmentTools) linkedProofFromReceipt(ctx context.Context, link linkedWechatQuote, requested stationWechatCrossProof) (stationWechatCrossProof, error) {
	deny := apphost.ErrObservationForbidden
	for _, source := range link.receipt.Delivery.Used {
		if !sameLinkedMemorySource(source, requested) {
			continue
		}
		parent := link.parent
		text := parent.Body
		prior := *t
		prior.cross = nil
		prior.trace = companion.Input{Channel: "wechat", Account: parent.AccountID, Thread: parent.ThreadID, Sender: parent.SenderID,
			TraceID: parent.ID, MessageTime: parent.CreatedAt, OriginalText: &text, Mode: "direct", Directed: true,
			IsGroup: strings.HasSuffix(parent.ThreadID, "@chatroom"), Source: &companion.SourceRef{MessageID: parent.ID, ServerID: parent.ServerID, MessageType: parent.Type, OccurredAt: parent.CreatedAt}}
		if source.LinkedReceipt != "" && parent.IsQuotedText() && parent.Quote != nil {
			text = strings.TrimSpace(parent.Body)
			prior.trace.PrivateQuote = parent.Quote.ServerID
		} else if parent.Type != 1 || source.LinkedReceipt != "" {
			return stationWechatCrossProof{}, deny
		}
		if previous, err := prior.crossProof(ctx, source); err != nil || previous != source {
			return stationWechatCrossProof{}, deny
		}
		next := source
		next.LinkedReceipt = link.receipt.Delivery.Key
		next.Event, next.EventRevision = link.current.EventID, link.current.Revision
		next.Digest = localCheckpointKey("wechat-native-quote-v1", link.dataset, next.LinkedReceipt, link.receipt.Part.Key,
			t.trace.PrivateQuote, link.current, source)
		if requested.Douyin && requested.DouyinScope == "" {
			next.DouyinKind, next.DouyinScope, next.DouyinBinding, next.DouyinEpoch, next.DouyinRevision = "", "", "", "", 0
		}
		if requested.Digest != "" && next != requested || t.blocked() || ctx.Err() != nil {
			return stationWechatCrossProof{}, deny
		}
		if t.cross != nil {
			t.cross.Target = t.trace.Thread
		}
		return next, nil
	}
	return stationWechatCrossProof{}, deny
}

func (t *stationAttachmentTools) linkedCrossSources(ctx context.Context) (string, error) {
	link, err := t.verifyLinkedWechatQuote(ctx)
	if err != nil {
		return "", err
	}
	threads, archives := []string{}, []string{}
	douyin := []localDouyinMemorySource{}
	station := false
	for _, source := range link.receipt.Delivery.Used {
		request := stationWechatCrossProof{Thread: source.Thread, Station: source.Station, Archived: source.Archived, Douyin: source.Douyin, DouyinScope: source.DouyinScope}
		proof, err := t.linkedProofFromReceipt(ctx, link, request)
		if err != nil {
			return "", err
		}
		if t.cross != nil && !slices.Contains(t.cross.Proofs, proof) {
			t.cross.Proofs = append(t.cross.Proofs, proof)
		}
		switch {
		case source.Station:
			station = true
		case source.Douyin:
			douyin = append(douyin, localDouyinMemorySource{Kind: source.DouyinKind, Scope: source.DouyinScope})
		case source.Archived:
			archives = append(archives, source.Thread)
		default:
			threads = append(threads, source.Thread)
		}
	}
	slices.Sort(threads)
	slices.Sort(archives)
	return localMemoryJSON(map[string]any{"sources": slices.Compact(threads), "archive_sources": slices.Compact(archives), "station_available": station,
		"douyin_sources": douyin, "linked_request": true, "delivery": "owner_private_only"})
}
