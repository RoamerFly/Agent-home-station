// Package adapter owns the WeChat application identity. Private connections
// and credentials are resolved by the station host.
package adapter

import (
	"crypto/sha256"
	"encoding/hex"

	"wechat-bot/internal/apphost"
)

const ID = "wechat"
const LegacyInstallationID = "wechat-legacy"

func Application() apphost.Application {
	return apphost.Application{ID: ID, Name: "微信", InstallationID: LegacyInstallationID, Registration: "legacy"}
}

func ConnectionID(account string) string {
	sum := sha256.Sum256([]byte(account))
	return ID + "-" + hex.EncodeToString(sum[:])
}

func ConnectionRef(account string) apphost.ConnectionRef {
	return apphost.ConnectionRef{AppID: ID, InstallationID: LegacyInstallationID, ConnectionID: ConnectionID(account), AccountID: account}
}
