package station

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/stationservices"
)

func validDouyinDirectionSources(in stationWechatCrossGrantInput, douyin bool) bool {
	if !douyin {
		return len(in.AppSources) == 0
	}
	if (len(in.AppSources) == 0) != (len(in.Origins) == 0) || len(in.AppSources) > len(localDouyinMemoryKinds) {
		return false
	}
	seen := map[string]bool{}
	for _, kind := range in.AppSources {
		if seen[kind] || !slices.Contains(localDouyinMemoryKinds[:], kind) {
			return false
		}
		seen[kind] = true
	}
	return true
}

func mentionsDouyinMemory(original *string) bool {
	return original != nil && (strings.Contains(*original, "抖音") || strings.Contains(strings.ToLower(*original), "douyin"))
}

func (s *stationWechat) douyinMemoryTools(owner stationservices.Owner, check func(context.Context) error) (*localToolSet, error) {
	if s.localChat == nil || s.localChat.home == nil {
		return nil, apphost.ErrUnavailable
	}
	return &localToolSet{publications: s.localChat, douyinMemoryRequested: true, douyinMemoryOrigin: check,
		access: &localWorkspaceAccess{home: s.localChat.home, fishID: owner.FishID, ownerID: owner.ID}}, nil
}

func (s *stationWechat) douyinDirectionSources(ctx context.Context, owner stationservices.Owner) ([]localDouyinMemorySource, error) {
	tools, err := s.douyinMemoryTools(owner, func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		return nil, err
	}
	record, err := tools.douyinMemoryState(ctx)
	if err != nil {
		return nil, err
	}
	db, err := openLocalDouyinMemory(ctx, tools.publications.home, false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if db != nil {
		defer db.Close()
	}
	sources := []localDouyinMemorySource{}
	for _, kind := range localDouyinMemoryKinds {
		if source, ok := douyinMemorySource(record, owner.FishID, owner.ID, kind); ok {
			_, err := douyinMemoryEpoch(ctx, db, source)
			if errors.Is(err, apphost.ErrObservationForbidden) {
				continue
			}
			if err != nil {
				return nil, err
			}
			sources = append(sources, source)
		}
	}
	return sources, nil
}

func (t *stationAttachmentTools) isDouyinMemoryCall(in stationWechatCrossInput) bool {
	if in.Source == "douyin" {
		return true
	}
	if t.cross == nil || t.cross.Douyin == nil {
		return false
	}
	return strings.HasPrefix(in.Reference, "douyin-reference-") || strings.HasPrefix(in.Cursor, "douyin-cursor-")
}

func (t *stationAttachmentTools) callWechatDouyinMemory(ctx context.Context, in stationWechatCrossInput) (string, error) {
	if in.Thread != "" || in.Dataset != "" || in.SourceIndex != nil || in.Source != "" && in.Source != "douyin" || in.SourceOffset != nil || in.SourceLimit != nil {
		return "", apphost.ErrObservationInvalid
	}
	proof, err := t.crossProof(ctx, stationWechatCrossProof{Douyin: true})
	if err != nil {
		return "", err
	}
	if t.cross.Douyin == nil {
		owner, err := t.crossHost.ownerReader()
		if err != nil {
			return "", err
		}
		t.cross.DouyinProof = proof
		t.cross.Douyin, err = t.crossHost.douyinMemoryTools(owner, func(ctx context.Context) error {
			_, err := t.crossProof(ctx, t.cross.DouyinProof)
			return err
		})
		if err != nil {
			return "", err
		}
	}
	if proof != t.cross.DouyinProof {
		return "", apphost.ErrObservationForbidden
	}
	worker, err := t.crossHost.worker(t.crossConnection, false)
	if err != nil || worker == nil {
		return "", apphost.ErrUnavailable
	}
	grants, _, _, err := worker.wechatFileAuthority(t.trace.Account, stationWechatToDouyinGrantsFile)
	if err != nil {
		return "", err
	}
	bridge := t.cross.Douyin
	arguments := localWechatMemoryInput{AppID: "douyin", Action: in.Action, SourceID: in.SourceID, WorkID: in.WorkID, Query: in.Query,
		Since: in.Since, Before: in.Before, Cursor: in.Cursor, Reference: in.Reference, Limit: in.Limit, Offset: in.Offset, Length: in.Length}
	scope := in.SourceID
	switch in.Action {
	case "search":
		if in.Cursor != "" {
			scope = bridge.douyinMemoryPages[in.Cursor].Proof.Source.Scope
		}
	case "read":
		scope = bridge.douyinMemoryRefs[in.Reference].Proof.Source.Scope
	}
	scopeAllowed := false
	for _, allowed := range grants.AppSources {
		scopeAllowed = scopeAllowed || scope != "" && scope == allowed
	}
	if in.Action != "sources" && !scopeAllowed {
		return "", apphost.ErrObservationForbidden
	}
	result, err := bridge.callDouyinMemory(ctx, arguments)
	if err != nil {
		return "", err
	}
	var packet map[string]any
	if json.Unmarshal([]byte(result), &packet) != nil {
		return "", apphost.ErrUnavailable
	}
	if in.Action == "sources" {
		var list struct{ Sources []localDouyinMemorySource }
		if json.Unmarshal([]byte(result), &list) != nil {
			return "", apphost.ErrUnavailable
		}
		sources := []localDouyinMemorySource{}
		for _, source := range list.Sources {
			if grants.AppSources[source.Kind] == source.Scope {
				sources = append(sources, source)
			}
		}
		packet["sources"] = sources
	}
	packet["delivery"] = "owner_private_only"
	result, err = localMemoryJSON(packet)
	if err != nil {
		return "", err
	}
	t.cross.Proofs = append(t.cross.Proofs, proof)
	for sourceProof := range bridge.douyinMemoryProofs {
		stored := proof
		stored.DouyinKind, stored.DouyinScope, stored.DouyinBinding = sourceProof.Source.Kind, sourceProof.Source.Scope, sourceProof.Source.binding
		stored.DouyinEpoch, stored.DouyinRevision = sourceProof.Epoch, sourceProof.Revision
		if !slices.Contains(t.cross.Proofs, stored) {
			t.cross.Proofs = append(t.cross.Proofs, stored)
		}
	}
	if err := t.checkCrossResults(ctx); err != nil {
		return "", err
	}
	if in.Action == "sources" {
		return result, nil
	}
	count := 1
	if in.Action == "search" {
		count = len(packet["items"].([]any))
	}
	return t.finishCrossMemory(ctx, proof, in.Action == "search", count, result)
}

func (t *stationAttachmentTools) checkWechatDouyinStoredProof(ctx context.Context, owner stationservices.Owner, proof stationWechatCrossProof) error {
	worker, err := t.crossHost.worker(t.crossConnection, false)
	if err != nil || worker == nil {
		return apphost.ErrObservationForbidden
	}
	grants, _, _, err := worker.wechatFileAuthority(t.trace.Account, stationWechatToDouyinGrantsFile)
	if err != nil || grants.AppSources[proof.DouyinKind] != proof.DouyinScope {
		return apphost.ErrObservationForbidden
	}
	tools, err := t.crossHost.douyinMemoryTools(owner, func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		return err
	}
	record, err := tools.douyinMemoryState(ctx)
	if err != nil {
		return err
	}
	db, err := openLocalDouyinMemory(ctx, tools.publications.home, false)
	if err != nil {
		return err
	}
	defer db.Close()
	source := localDouyinMemorySource{Kind: proof.DouyinKind, Scope: proof.DouyinScope, binding: proof.DouyinBinding}
	return tools.checkDouyinMemoryProof(ctx, db, record, localDouyinMemoryProof{Source: source, Revision: proof.DouyinRevision, Epoch: proof.DouyinEpoch})
}
