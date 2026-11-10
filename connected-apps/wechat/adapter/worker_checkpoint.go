package adapter

import (
	"encoding/json"
	"errors"
	"io"
	"os"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/workspacefs"
)

// openWorkerCheckpoint pins the runtime and queue directories without
// following a substituted directory, including a Windows junction.
func openWorkerCheckpoint(runtimeDir string) (*os.File, error) {
	root, err := workspacefs.OpenPath(runtimeDir)
	if err != nil {
		return nil, apphost.ErrUnavailable
	}
	defer root.Close()
	queue, err := workspacefs.OpenExistingDirectory(root, "queue")
	if err != nil {
		return nil, err
	}
	defer queue.Close()
	const name = "messages-v2.json"
	info, err := queue.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, apphost.ErrUnavailable
	}
	return workspacefs.OpenRegular(queue, name)
}

// WorkerCheckpointAccount identifies local evidence while the native service
// is offline. This is not a credential for native operations.
func WorkerCheckpointAccount(runtimeDir, connectionID string) (string, error) {
	f, err := openWorkerCheckpoint(runtimeDir)
	if err != nil {
		return "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, bot.MaxDeliveryCheckpointBytes+1))
	var header struct {
		Version int
		Account string
	}
	if err != nil || len(raw) > bot.MaxDeliveryCheckpointBytes || json.Unmarshal(raw, &header) != nil ||
		!bot.SupportedDeliveryCheckpointVersion(header.Version) || header.Account == "" || ConnectionID(header.Account) != connectionID {
		return "", apphost.ErrUnavailable
	}
	return header.Account, nil
}

func ReadWorkerDeliveryPage(runtimeDir, account, after string, limit int) (bot.DeliveryEvidencePage, error) {
	if limit < 1 {
		return bot.DeliveryEvidencePage{}, apphost.ErrUnavailable
	}
	f, err := openWorkerCheckpoint(runtimeDir)
	if errors.Is(err, os.ErrNotExist) {
		return bot.DeliveryEvidencePage{Keys: []string{}, Records: map[string]bot.DeliveryEvidence{}}, nil
	}
	if err != nil {
		return bot.DeliveryEvidencePage{}, err
	}
	defer f.Close()
	return bot.ReadDeliveryEvidencePageFrom(f, account, after, limit)
}

func ReadWorkerDelivery(runtimeDir, account, key string) (bot.DeliveryEvidence, bool, error) {
	f, err := openWorkerCheckpoint(runtimeDir)
	if errors.Is(err, os.ErrNotExist) {
		return bot.DeliveryEvidence{}, false, nil
	}
	if err != nil {
		return bot.DeliveryEvidence{}, false, err
	}
	defer f.Close()
	rows, err := bot.ReadDeliveryEvidenceFrom(f, account, []string{key})
	row, found := rows[key]
	return row, found, err
}
