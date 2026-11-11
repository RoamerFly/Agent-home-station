package companion

import (
	"context"
	"database/sql"
	"os"
	"slices"
	"strings"
)

// LegacyMemoryAccounts discovers only verified WeChat scope identities from an
// owner's selected immutable snapshot. It never reads conversation bodies.
func LegacyMemoryAccounts(ctx context.Context, file *os.File) ([]string, error) {
	db, closeSnapshot, err := openLegacyMemorySnapshot(ctx, file)
	if err != nil {
		return nil, err
	}
	defer closeSnapshot()
	rows, err := db.QueryContext(ctx, `SELECT id,json_extract(payload,'$.id'),json_extract(payload,'$.channel'),json_extract(payload,'$.account'),json_extract(payload,'$.thread') FROM scopes ORDER BY id`)
	if err != nil {
		return nil, legacyInventoryError(ctx, err)
	}
	defer rows.Close()
	accounts := []string{}
	for rows.Next() {
		var id string
		var payloadID, channel, account, thread sql.NullString
		if err := rows.Scan(&id, &payloadID, &channel, &account, &thread); err != nil {
			return nil, legacyInventoryError(ctx, err)
		}
		if !payloadID.Valid || !channel.Valid || !account.Valid || !thread.Valid || payloadID.String != id || strings.TrimSpace(account.String) == "" || strings.TrimSpace(thread.String) == "" || ScopeID(channel.String, account.String, thread.String) != id {
			return nil, ErrLegacyMemoryUnsupported
		}
		if channel.String == "wechat" && !slices.Contains(accounts, account.String) {
			accounts = append(accounts, account.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, legacyInventoryError(ctx, err)
	}
	slices.Sort(accounts)
	return accounts, nil
}
