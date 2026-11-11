package station

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
)

const localMemoryLinkTool = "app_memory_link"

// Issued only by an authenticated owner chat submission. No message bodies,
// search terms or temporary model references are carried between requests.
type localMemoryLink struct {
	Version  int                  `json:"version"`
	Request  string               `json:"request_id"`
	Source   *companion.SourceRef `json:"source"`
	Evidence []string             `json:"evidence"`
}

func memoryLinkKey(link localMemoryLink) string {
	return localCheckpointKey("app-memory-link-v1", link)
}

func (h *localChatService) readMemoryLink(ctx context.Context, store *companion.Store, in companion.Input) (*localMemoryLink, error) {
	ops, err := store.LocalToolEvidence(ctx, in, localMemoryLinkTool)
	if err != nil || len(ops) == 0 {
		return nil, err
	}
	if len(ops) != 1 {
		return nil, errors.New("原请求有冲突的关联记录")
	}
	op := ops[0]
	var link localMemoryLink
	if strictLocalMemoryDecode([]byte(op.Result), &link) != nil || link.Version != 1 || !identity.ValidPathID(link.Request) ||
		link.Source == nil || link.Source.MessageID != link.Request || link.Source.Account != in.Account || link.Source.Thread != in.Sender || link.Source.Sender != in.Sender ||
		len(link.Evidence) == 0 || op.Target != link.Request || op.Grant != memoryEvidenceGrant(in.Account, in.Sender) || op.Key != memoryLinkKey(link) {
		return nil, errors.New("原请求关联缺少可核对的来源证明")
	}
	return &link, nil
}

// Walk locally persisted source revisions first; resolve no external content.
// Each link freezes a subset of the parent's proofs. Later parent operations
// never silently widen a continuation's sources. Iteration avoids stack limits.
func (h *localChatService) requestMemoryEvidence(ctx context.Context, store *companion.Store, in companion.Input) ([]localMemoryEvidence, error) {
	in.OriginalText = nil // Canonical source revisions carry the content constraint.
	type node struct {
		own  []localMemoryEvidence
		link *localMemoryLink
	}
	var chain []node
	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if in.Source == nil {
			return nil, errors.New("跨来源请求缺少原事件")
		}
		key := localCheckpointKey(in.Source.InstanceID, in.Source.EventID, in.Source.Revision)
		if seen[key] {
			return nil, errors.New("跨来源请求关联成环")
		}
		seen[key] = true
		own, err := h.ownMemoryEvidence(ctx, store, in)
		if err != nil {
			return nil, err
		}
		link, err := h.readMemoryLink(ctx, store, in)
		if err != nil {
			return nil, err
		}
		chain = append(chain, node{own, link})
		if link == nil {
			break
		}
		in.TraceID, in.Source, in.MessageTime = link.Request, link.Source, link.Source.OccurredAt
	}
	var proofs []localMemoryEvidence
	for index := len(chain) - 1; index >= 0; index-- {
		n := chain[index]
		var selected []localMemoryEvidence
		if n.link != nil {
			for _, key := range n.link.Evidence {
				found := slices.IndexFunc(proofs, func(proof localMemoryEvidence) bool { return memoryEvidenceKey(proof) == key })
				if found < 0 {
					return nil, errors.New("关联请求的原来源证明已变化")
				}
				selected = append(selected, proofs[found])
			}
		}
		for _, proof := range n.own {
			if !slices.ContainsFunc(selected, func(prior localMemoryEvidence) bool { return memoryEvidenceKey(prior) == memoryEvidenceKey(proof) }) {
				selected = append(selected, proof)
			}
		}
		proofs = selected
	}
	return proofs, nil
}

func (h *localChatService) bindMemoryLink(ctx context.Context, store *companion.Store, in companion.Input, run *companion.LocalToolRun, parent string, fresh bool) error {
	if parent == "" {
		return nil
	} // Retrying an existing request retains its saved link.
	existing, err := h.readMemoryLink(ctx, store, in)
	if err != nil {
		return err
	}
	if existing != nil {
		if existing.Request != parent {
			return errors.New("同一请求编号不能更换关联，请新建请求")
		}
		return h.checkRequestMemoryEvidence(ctx, store, in)
	}
	if !fresh || !run.Tracked || parent == in.TraceID || !identity.ValidPathID(parent) {
		return errors.New("只能为新的请求选择已完成的关联回复")
	}
	original := in
	original.Source, original.TraceID, original.OriginalText = nil, parent, nil
	prior, err := store.LocalConversationRequest(ctx, original, parent)
	if err != nil || prior == nil || !prior.Completed {
		return errors.New("关联回复不存在、未完成或已被遗忘")
	}
	original.MessageTime = prior.OccurredAt
	original.Source, err = store.LocalRequestSource(ctx, original, parent)
	if err != nil {
		return err
	}
	proofs, err := h.requestMemoryEvidence(ctx, store, original)
	if err != nil || len(proofs) == 0 {
		return errors.New("关联回复没有可接续的跨来源证明，请主动重新查询")
	}
	if err := h.checkRequestMemoryEvidence(ctx, store, original); err != nil {
		return err
	}
	link := localMemoryLink{Version: 1, Request: parent, Source: original.Source}
	for _, proof := range proofs {
		link.Evidence = append(link.Evidence, memoryEvidenceKey(proof))
	}
	slices.Sort(link.Evidence)
	link.Evidence = slices.Compact(link.Evidence)
	raw, err := json.Marshal(link)
	if err != nil {
		return err
	}
	if len(raw) > 64<<10 {
		return errors.New("关联来源证明超过单条记录容量，请按来源分别接续")
	}
	op := companion.LocalToolCheckpoint{Key: memoryLinkKey(link), Tool: localMemoryLinkTool, Target: parent, Grant: memoryEvidenceGrant(in.Account, in.Sender)}
	if err := run.Reserve(ctx, op); err != nil {
		return err
	}
	run.Operations = append(run.Operations, op)
	if err := run.RecordResult(ctx, op.Key, string(raw)); err != nil {
		return err
	}
	run.Operations[len(run.Operations)-1].Result = string(raw)
	return h.checkRequestMemoryEvidence(ctx, store, in)
}

func (t *localToolSet) memoryEvidenceInput(ctx context.Context) (companion.Input, error) {
	if t.publications == nil || t.store == nil || t.access == nil {
		return companion.Input{}, errors.New("本机来源证明服务不可用")
	}
	in := companion.Input{Channel: "local", Account: t.access.fishID, Thread: t.access.ownerID, Sender: t.access.ownerID, TraceID: t.traceID}
	var err error
	in.Source, err = t.store.LocalRequestSource(ctx, in, t.traceID)
	if err != nil {
		return in, errors.New("本机原请求已变化或被遗忘")
	}
	in.MessageTime = in.Source.OccurredAt
	return in, nil
}

func (t *localToolSet) checkMemoryLink(ctx context.Context) error {
	if !t.memoryLinked {
		return nil
	}
	in, err := t.memoryEvidenceInput(ctx)
	if err != nil {
		return err
	}
	_, err = t.publications.requestMemoryEvidence(ctx, t.store, in)
	return err
}

func (t *localToolSet) linkedMemorySources(ctx context.Context, in localWechatMemoryInput) (string, error) {
	if in != (localWechatMemoryInput{Action: "linked_sources"}) {
		return "", errors.New("关联来源目录不接受页面或来源参数")
	}
	origin, err := t.memoryEvidenceInput(ctx)
	if err != nil {
		return "", err
	}
	link, err := t.publications.readMemoryLink(ctx, t.store, origin)
	if err != nil || link == nil {
		return "", errors.New("请先由主人在聊天中选择关联回复")
	}
	proofs, err := t.publications.requestMemoryEvidence(ctx, t.store, origin)
	if err != nil {
		return "", err
	}
	if err := t.publications.checkRequestMemoryEvidence(ctx, t.store, origin); err != nil {
		return "", err
	}
	sources := []map[string]any{}
	for _, proof := range proofs {
		if proof.Wechat != nil {
			sources = append(sources, map[string]any{"app_id": "wechat", "source": proof.Wechat.Source})
		} else {
			sources = append(sources, map[string]any{"app_id": "douyin", "source": proof.Douyin.Source})
		}
	}
	return localMemoryJSON(map[string]any{"request_id": link.Request, "sources": sources, "content_loaded": false, "data_role": "source_directory"})
}
