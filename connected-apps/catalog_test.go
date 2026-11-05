package catalog

import (
	"path/filepath"
	"testing"

	wechat "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/pluginhost"
)

func TestOfficialAppSource(t *testing.T) {
	contract, err := Lookup("wechat")
	if err != nil {
		t.Fatal(err)
	}
	p, err := pluginhost.VerifyDescriptor(filepath.Join("wechat"), "app.json")
	if err != nil || p.ID != contract.Package.ID || p.Status != "migration-pending" || p.Runtime.Transport != "unbound" {
		t.Fatalf("invalid source bundle: %#v %v", p, err)
	}
	if _, err := Lookup("../wechat"); err == nil {
		t.Fatal("untrusted catalog path accepted")
	}
	if _, err := pluginhost.VerifyDescriptor("wechat", "../app.json"); err == nil {
		t.Fatal("invalid descriptor accepted")
	}
	// Existing connection keys must survive moving their implementation.
	if wechat.ConnectionID("synthetic-account") != "wechat-50a5ee17cb005388e7f31e1ed28043dda745bb3d2b65d8042afaead6fd4af575" {
		t.Fatal("connection identity changed")
	}
	if wechat.ConnectionRef("synthetic-account").InstallationID != "wechat-legacy" {
		t.Fatal("legacy installation changed")
	}
}
