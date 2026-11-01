package devicespec_test

import (
	"encoding/json"
	"testing"

	"wechat-bot/internal/devicespec"
	"wechat-bot/plugins/mobile-control/dev/legacy-sdk"
)

// These assignments must compile without conversion: the old SDK's metadata
// API shares actual type identity with the neutral host contracts.
var (
	_ devicespec.Settings           = mobile.Settings{}
	_ devicespec.Source             = mobile.Source{}
	_ devicespec.Event              = mobile.Event{}
	_ devicespec.Task               = mobile.Task{}
	_ devicespec.Step               = mobile.Step{}
	_ devicespec.Observation        = mobile.Observation{}
	_ devicespec.Artifact           = mobile.Artifact{}
	_ devicespec.RepositorySnapshot = mobile.RepositorySnapshot{}
	_ devicespec.LegacyImport       = mobile.LegacyImport{}
	_ devicespec.Repository         = (mobile.Repository)(nil)
	_ devicespec.Planner            = (mobile.Planner)(nil)
	_ devicespec.ArtifactAnalyzer   = (mobile.ArtifactAnalyzer)(nil)
	_ devicespec.BrokerFactory      = (mobile.BrokerFactory)(nil)
	_ devicespec.Bridge             = (mobile.Bridge)(nil)
)

func TestMetadataPreservesWireJSONAndPrivateObservationFields(t *testing.T) {
	source := mobile.Source{Account: "synthetic-account", Thread: "room", Sender: "owner", MessageID: "original-message", Admin: true}
	raw, err := json.Marshal(source)
	if err != nil || string(raw) != `{"account":"synthetic-account","thread":"room","sender":"owner","message_id":"original-message","admin":true}` {
		t.Fatalf("source wire shape changed: %s %v", raw, err)
	}
	observation := mobile.Observation{ID: "obs-synthetic", DeviceID: "lab", Package: "com.test", Width: 100, Height: 200,
		Nodes: []json.RawMessage{json.RawMessage(`{"text":"synthetic"}`)}, Raw: json.RawMessage(`{"private":true}`), PNG: []byte("host-private-png")}
	raw, err = json.Marshal(observation)
	if err != nil || string(raw) != `{"id":"obs-synthetic","device_id":"lab","package":"com.test","width":100,"height":200,"nodes":[{"text":"synthetic"}]}` {
		t.Fatalf("observation wire shape/private data boundary changed: %s %v", raw, err)
	}
	var decoded devicespec.Observation
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.ID != observation.ID || len(decoded.PNG) != 0 || len(decoded.Raw) != 0 {
		t.Fatal("neutral observation round trip failed", err)
	}
	if mobile.ErrRepositoryConflict != devicespec.ErrRepositoryConflict {
		t.Fatal("repository conflict lost sentinel identity")
	}
}

func TestDefaultsAndValidationRemainLocalMetadata(t *testing.T) {
	first := devicespec.Defaults()
	first.Bridge.Tools[0] = "changed-local-copy"
	if devicespec.Defaults().Bridge.Tools[0] != "devices" {
		t.Fatal("defaults share mutable slices")
	}
	settings := devicespec.Defaults()
	settings.Enabled = true
	settings.Devices, settings.Actions = []string{"lab"}, []string{"tap"}
	settings.Bridge.Command = "synthetic-missing-backend-must-never-start"
	if err := settings.Validate(); err != nil {
		t.Fatal("metadata validation depended on a live backend", err)
	}
	snapshot := devicespec.RepositorySnapshot{Settings: settings, Tasks: map[string]devicespec.Task{
		"device-one": {ID: "device-one", RequestHash: "original-request", State: "running"},
	}}
	if err := devicespec.ValidateRepositorySnapshot(snapshot); err != nil || snapshot.Tasks["device-one"].State != "running" {
		t.Fatal("schema validation changed workflow state", err)
	}
	snapshot.Tasks["device-two"] = devicespec.Task{ID: "device-two", RequestHash: "original-request"}
	if err := devicespec.ValidateRepositorySnapshot(snapshot); err == nil {
		t.Fatal("duplicate request identity passed snapshot validation")
	}
}
