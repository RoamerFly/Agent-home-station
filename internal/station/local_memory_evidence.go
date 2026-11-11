package station

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"wechat-bot/internal/companion"
)

// This checkpoint contains authority and source versions only. Search terms,
// summaries, original messages and temporary model references are not kept.
type localMemoryEvidence struct {
	Version int                     `json:"version"`
	Wechat  *localWechatMemoryProof `json:"wechat,omitempty"`
	Douyin  *localDouyinMemoryProof `json:"douyin,omitempty"`
	Binding string                  `json:"binding,omitempty"`
}

func (memory localMemoryEvidence) valid() bool {
	return memory.Version == 1 && (memory.Wechat != nil) != (memory.Douyin != nil) &&
		(memory.Douyin == nil && memory.Binding == "" || memory.Douyin != nil && memory.Binding != "")
}

func memoryEvidenceKey(proof localMemoryEvidence) string {
	return localCheckpointKey("app-memory-evidence-v1", proof)
}
func memoryEvidenceGrant(fish, owner string) string {
	return localCheckpointKey("app-memory-owner-v1", fish, owner)
}

func (t *localToolSet) persistMemoryEvidence(ctx context.Context) error {
	if t.run == nil || !t.run.Tracked {
		return errors.New("跨来源证据缺少原请求检查点")
	}
	proofs := []localMemoryEvidence{}
	for _, proof := range t.wechatMemoryProofs {
		saved := proof
		proofs = append(proofs, localMemoryEvidence{Version: 1, Wechat: &saved})
	}
	for proof := range t.douyinMemoryProofs {
		saved := proof
		proofs = append(proofs, localMemoryEvidence{Version: 1, Douyin: &saved, Binding: proof.Source.binding})
	}
	for _, proof := range proofs {
		raw, err := json.Marshal(proof)
		if err != nil {
			return err
		}
		op := companion.LocalToolCheckpoint{Tool: localWechatMemoryTool, Key: memoryEvidenceKey(proof), Grant: memoryEvidenceGrant(t.access.fishID, t.access.ownerID), Target: "source_versions", Result: string(raw)}
		found := false
		for i, previous := range t.run.Operations {
			if previous.Key != op.Key {
				continue
			}
			found = true
			if previous.Tool != op.Tool || previous.Grant != op.Grant || previous.Target != op.Target || previous.Result != "" && previous.Result != op.Result {
				return errors.New("原跨来源证据记录冲突")
			}
			if previous.Result == "" {
				if err := t.run.RecordResult(ctx, op.Key, op.Result); err != nil {
					return err
				}
				t.run.Operations[i] = op
			}
			break
		}
		if found {
			continue
		}
		reserved := op
		reserved.Result = ""
		if err := t.run.Reserve(ctx, reserved); err != nil {
			return err
		}
		t.run.Operations = append(t.run.Operations, reserved)
		if err := t.run.RecordResult(ctx, op.Key, op.Result); err != nil {
			return err
		}
		t.run.Operations[len(t.run.Operations)-1] = op
	}
	return t.run.CheckSource(ctx)
}

func decodeMemoryEvidence(op companion.LocalToolCheckpoint, fish, owner string) (localMemoryEvidence, error) {
	var proof localMemoryEvidence
	if op.Tool != localWechatMemoryTool || op.Target != "source_versions" || op.Grant != memoryEvidenceGrant(fish, owner) || strictLocalMemoryDecode([]byte(op.Result), &proof) != nil || !proof.valid() || memoryEvidenceKey(proof) != op.Key {
		return proof, errors.New("跨来源证据缺少可核对的版本")
	}
	if proof.Douyin != nil {
		proof.Douyin.Source.binding = proof.Binding
	}
	return proof, nil
}

func (t *localToolSet) restoreMemoryEvidence(ctx context.Context) error {
	if t.run == nil {
		return nil
	}
	for _, op := range t.run.Operations {
		if op.Tool != localWechatMemoryTool {
			continue
		}
		proof, err := decodeMemoryEvidence(op, t.access.fishID, t.access.ownerID)
		if err != nil {
			return err
		}
		if proof.Wechat != nil {
			found := false
			for _, prior := range t.wechatMemoryProofs {
				found = found || prior == *proof.Wechat
			}
			if !found {
				t.wechatMemoryProofs = append(t.wechatMemoryProofs, *proof.Wechat)
			}
		}
		if proof.Douyin != nil {
			t.douyinMemoryRequested = true
			if t.douyinMemoryProofs == nil {
				t.douyinMemoryProofs = map[localDouyinMemoryProof]struct{}{}
			}
			t.douyinMemoryProofs[*proof.Douyin] = struct{}{}
		}
	}
	if err := t.checkWechatMemoryResults(ctx); err != nil {
		return err
	}
	return t.checkDouyinMemoryResults(ctx)
}

func (h *localChatService) requestMemoryEvidence(ctx context.Context, store *companion.Store, in companion.Input) ([]localMemoryEvidence, error) {
	operations, err := store.LocalToolEvidence(ctx, in, localWechatMemoryTool)
	if err != nil {
		return nil, err
	}
	proofs := make([]localMemoryEvidence, 0, len(operations))
	for _, op := range operations {
		proof, err := decodeMemoryEvidence(op, in.Account, in.Sender)
		if err != nil {
			return nil, err
		}
		proofs = append(proofs, proof)
	}
	return proofs, nil
}

func (h *localChatService) checkRequestMemoryEvidence(ctx context.Context, store *companion.Store, in companion.Input) error {
	proofs, err := h.requestMemoryEvidence(ctx, store, in)
	if err != nil {
		return err
	}
	if len(proofs) == 0 {
		return nil
	}
	t := &localToolSet{access: &localWorkspaceAccess{home: h.home, fishID: in.Account, ownerID: in.Sender}, publications: h, douyinMemoryRequested: true,
		douyinMemoryOrigin: func(ctx context.Context) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if _, ok := store.SourceForInput(in); !ok {
				return errors.New("原请求来源已变化")
			}
			return nil
		}}
	for _, proof := range proofs {
		if proof.Wechat != nil {
			if h.wechat == nil {
				return errors.New("微信记忆来源暂不可核对")
			}
			source, _, err := h.wechat.openLocalWechatMemory(ctx, in.Account, in.Sender, *proof.Wechat)
			if err != nil {
				return err
			}
			source.Close()
		} else {
			if t.douyinMemoryProofs == nil {
				t.douyinMemoryProofs = map[localDouyinMemoryProof]struct{}{}
			}
			t.douyinMemoryProofs[*proof.Douyin] = struct{}{}
		}
	}
	return t.checkDouyinMemoryResults(ctx)
}

// Walk exact source revisions through immutable parent links. A child created
// by another request does not shed the authority of its input artifact.
func (h *localChatService) artifactMemoryEvidence(ctx context.Context, a localArtifact) (bool, error) {
	store, in, err := h.conversation()
	if err != nil || in.Account != a.Fish || in.Sender != a.Owner {
		return false, errors.New("成品不属于当前本机主人")
	}
	seen := map[string]bool{}
	found := false
	for {
		if ctx.Err() != nil {
			return found, ctx.Err()
		}
		if seen[a.ID] || a.Fish != in.Account || a.Owner != in.Sender {
			return found, errors.New("成品来源链无法核对")
		}
		seen[a.ID] = true
		if a.Source != nil {
			in.TraceID, in.Source, in.MessageTime = a.Request, a.Source, a.Source.OccurredAt
			proofs, err := h.requestMemoryEvidence(ctx, store, in)
			if err != nil {
				return found, err
			}
			if len(proofs) > 0 {
				found = true
				if err := h.checkRequestMemoryEvidence(ctx, store, in); err != nil {
					return found, err
				}
			}
		}
		if a.Parent == nil {
			return found, nil
		}
		parent, err := h.home.artifacts.readRecord(a.Parent.ID)
		if err != nil || parent.SHA256 != a.Parent.SHA256 {
			return found, errors.New("成品父来源已变化")
		}
		a = parent
	}
}

func (h *localChatService) checkArtifactMemoryEvidence(ctx context.Context, a localArtifact) error {
	_, err := h.artifactMemoryEvidence(ctx, a)
	return err
}

func (h *localChatService) memoryEvidenceTimeout(a localArtifact) error {
	ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
	defer cancel()
	return h.checkArtifactMemoryEvidence(ctx, a)
}
