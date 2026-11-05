package pluginhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCapabilitiesAreBoundToApprovedPackageAndPermissions(t *testing.T) {
	for _, kind := range []string{"valid", "unknown-permission", "pending-discoverable", "duplicate", "tampered", "unknown-field"} {
		t.Run(kind, func(t *testing.T) {
			dir := readyFixture(t)
			raw, e := os.ReadFile(filepath.Join(dir, "plugin.json"))
			if e != nil {
				t.Fatal(e)
			}
			var p Package
			if e = json.Unmarshal(raw, &p); e != nil {
				t.Fatal(e)
			}
			cap := Capability{ID: "speech.synthesize", Implementation: "ready", Description: "Synthetic capability", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`), Discoverable: true}
			if kind == "unknown-permission" {
				cap.Permissions = []string{"invented.host.admin"}
			}
			if kind == "pending-discoverable" {
				cap.Implementation = "migration-pending"
			}
			items := []Capability{cap}
			if kind == "duplicate" {
				items = append(items, cap)
			}
			doc := map[string]any{"version": 1, "capabilities": items}
			if kind == "unknown-field" {
				doc["grant_all"] = true
			}
			raw, _ = json.Marshal(doc)
			if e = os.WriteFile(filepath.Join(dir, "capabilities.json"), raw, 0600); e != nil {
				t.Fatal(e)
			}
			hash := sha256.Sum256(raw)
			p.Files["capabilities.json"] = hex.EncodeToString(hash[:])
			raw, _ = json.Marshal(p)
			if e = os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0600); e != nil {
				t.Fatal(e)
			}
			s, e := OpenStore(t.TempDir(), "a")
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			ctx := context.Background()
			prepared, e := s.Prepare(ctx, dir)
			if e != nil {
				t.Fatal(e)
			}
			i, e := s.Install(ctx, prepared.Token, prepared.Digest, p.Permissions)
			if e != nil {
				t.Fatal(e)
			}
			if kind == "tampered" {
				snapshot, e := s.PackageDirectory(i.Digest)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(snapshot, "capabilities.json"), []byte(`{"version":1,"capabilities":[]}`), 0600); e != nil {
					t.Fatal(e)
				}
			}
			got, e := s.Capabilities(ctx, p.ID)
			if kind == "valid" {
				if e != nil || len(got) != 1 {
					t.Fatal(got, e)
				}
			} else if e == nil {
				t.Fatal("invalid capability accepted", kind)
			}
		})
	}
}
