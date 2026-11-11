package station

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/workspacefs"
)

type localWechatMemoryAudit struct {
	Account         string    `json:"account_id"`
	Thread          string    `json:"thread_id"`
	Fish            string    `json:"fish_id"`
	Owner           string    `json:"owner_id"`
	Request         string    `json:"request_hash"`
	Revision        int       `json:"grant_revision"`
	Searches        int       `json:"searches"`
	Reads           int       `json:"reads"`
	InjectedItems   int       `json:"injected_items"`
	EstimatedTokens int       `json:"estimated_tokens"`
	Updated         time.Time `json:"updated_at"`
}

func (t *localToolSet) auditWechatMemory(proof localWechatMemoryProof, search bool, count int, result string) error {
	worker, err := t.wechat.worker(proof.Source.Connection, false)
	if err != nil || worker == nil {
		return errors.New("跨来源读取审计暂不可保存")
	}
	worker.ownerMu.Lock()
	defer worker.ownerMu.Unlock()
	root, err := workspacefs.OpenPath(worker.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	dir, err := workspacefs.EnsureDirectory(root, "memory-query-audit")
	if err != nil {
		return err
	}
	defer dir.Close()
	key := localCheckpointKey(t.access.fishID, t.access.ownerID, t.traceID, proof.Source)
	expected := localWechatMemoryAudit{Account: proof.Source.Account, Thread: proof.Source.Thread, Fish: t.access.fishID, Owner: t.access.ownerID, Request: key, Revision: proof.Revision}
	raw, err := readLegacyImportFile(dir, key+".json", 16<<10)
	audit := expected
	if err == nil {
		if json.Unmarshal(raw, &audit) != nil || audit.Account != expected.Account || audit.Thread != expected.Thread ||
			audit.Fish != expected.Fish || audit.Owner != expected.Owner || audit.Request != key || audit.Revision != expected.Revision {
			return errors.New("跨来源审计版本已变化")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if search {
		audit.Searches++
	} else {
		audit.Reads++
	}
	audit.InjectedItems += count
	audit.EstimatedTokens += companion.EstimateTokens(result)
	audit.Updated = time.Now().UTC()
	temp := ".audit-" + identity.RandomToken()
	defer dir.Remove(temp)
	if err := writeLegacyStagedFile(dir, temp, audit); err != nil {
		return err
	}
	return dir.Rename(temp, key+".json")
}
