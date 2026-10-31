package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/plugins"
)

func TestAssetHistoryRetainsEveryAssociationAcrossReloadAndStaleSave(t *testing.T) {
	f := setup(t)
	f.manager.config.Companion.Permissions.Remember = true
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "alice"}
	original := assetOriginal{Data: []byte("synthetic repeated original"), MIME: "text/plain", Filename: "fixture.txt"}
	var stale mediaAsset
	for i := 0; i < 25; i++ {
		in.TraceID = "message-" + strconv.Itoa(i)
		in.MessageTime = "2026-10-01T10:00:" + func() string {
			if i < 10 {
				return "0" + strconv.Itoa(i)
			}
			return strconv.Itoa(i)
		}() + "Z"
		a, err := f.manager.rememberAssetFormats(in, original, nil)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			stale = a
		}
	}
	// A stale catalogue snapshot cannot truncate the separately committed history.
	if err := f.manager.saveAssets([]mediaAsset{stale}); err != nil {
		t.Fatal(err)
	}
	items, err := f.manager.loadAssets()
	if err != nil || len(items) != 1 || len(items[0].Requests) != 25 {
		t.Fatal(items, err)
	}
	if items[0].Requests[4].TraceID != "message-4" || items[0].Requests[4].MessageTime != "2026-10-01T10:00:04Z" {
		t.Fatal(items[0].Requests)
	}
	index, _ := os.ReadFile(filepath.Join(f.manager.dir, "media-assets.json"))
	var compact []mediaAsset
	if json.Unmarshal(index, &compact) != nil || len(compact[0].Requests) != 8 || !strings.Contains(string(index), `"request_count":25`) {
		t.Fatal("history bloated index", string(index))
	}
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(in)
	var page struct {
		Items []assetRequest
		Total int
		Next  int  `json:"next_offset"`
		More  bool `json:"has_more"`
	}
	result, err := p.Call(context.Background(), "media_history", json.RawMessage(`{"asset_id":"`+stale.ID+`","offset":3,"limit":10}`))
	if err != nil || json.Unmarshal([]byte(result), &page) != nil || page.Total != 25 || len(page.Items) != 10 || page.Next != 13 || !page.More || page.Items[0].TraceID != "message-3" {
		t.Fatal(result, err)
	}
	p.trace.Thread = "other@chatroom"
	if _, err = p.assetHistoryPage(stale.ID, 0, 100); err == nil {
		t.Fatal("other conversation read history")
	}
	p.trace.Thread, p.trace.Account = in.Thread, "other"
	if _, err = p.assetHistoryPage(stale.ID, 0, 100); err == nil {
		t.Fatal("other account read history")
	}
	if err := os.Remove(f.manager.assetHistoryPath(items[0])); err != nil {
		t.Fatal(err)
	}
	if _, err = f.manager.loadAssets(); err == nil {
		t.Fatal("missing committed history silently discarded")
	}
}

func TestAssetHistoryLazilyMigratesFirstAndLegacyAssociations(t *testing.T) {
	f := setup(t)
	first := assetRequest{TraceID: "first", RequesterID: "alice"}
	a := mediaAsset{ID: "synthetic", Scope: "scope", Account: "test", FirstRequest: &first, Requests: []assetRequest{{TraceID: "later", RequesterID: "bob"}}}
	b, _ := json.Marshal([]mediaAsset{a})
	if err := writePrivate(filepath.Join(f.manager.dir, "media-assets.json"), b); err != nil {
		t.Fatal(err)
	}
	items, err := f.manager.loadAssets()
	if err != nil || len(items[0].Requests) != 2 {
		t.Fatal(items, err)
	}
	if err = f.manager.saveAssets(items); err != nil {
		t.Fatal(err)
	}
	items, err = f.manager.loadAssets()
	if err != nil || len(items[0].Requests) != 2 || !items[0].HistoryStored {
		t.Fatal(items, err)
	}
	// The file's account/scope envelope is validated before using associations.
	if err = writePrivate(f.manager.assetHistoryPath(items[0]), []byte(`{"asset_id":"synthetic","account":"other","scope":"scope","requests":[]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.manager.loadAssets(); err == nil {
		t.Fatal("tampered identity accepted")
	}
}

func TestAssetHistoryRecoversUniqueVerifiedLegacyFilename(t *testing.T) {
	f := setup(t)
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "alice", TraceID: "first"}
	original := assetOriginal{Data: []byte("synthetic history recovery"), MIME: "text/plain", Filename: "fixture.txt"}
	a, err := f.manager.rememberAssetFormats(in, original, nil)
	if err != nil {
		t.Fatal(err)
	}
	in.TraceID = "second"
	if _, err = f.manager.rememberAssetFormats(in, original, nil); err != nil {
		t.Fatal(err)
	}
	canonical := f.manager.assetHistoryPath(a)
	legacy := filepath.Join(filepath.Dir(canonical), "legacy-name.json")
	if err = os.Rename(canonical, legacy); err != nil {
		t.Fatal(err)
	}
	items, err := f.manager.loadAssets()
	if err != nil || len(items) != 1 || len(items[0].Requests) != 2 {
		t.Fatalf("verified history not recovered: %v, %+v", err, items)
	}
	if _, err = os.Stat(canonical); err != nil {
		t.Fatal("canonical history not restored", err)
	}
	if err = os.Remove(canonical); err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(legacy, []byte(`{"asset_id":"wrong","account":"test","scope":"scope","requests":[]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.manager.loadAssets(); err == nil {
		t.Fatal("wrong identity recovered")
	}
}

func TestRecoveredHistoryAllowsVoiceStagingAndReuse(t *testing.T) {
	f := setup(t)
	f.manager.native = &fakeTools{fakeNative: f.native}
	f.manager.config.Companion.Permissions.Remember = true
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "alice", TraceID: "older"}
	a, err := f.manager.rememberAssetFormats(in, assetOriginal{Data: []byte("older synthetic media"), MIME: "audio/wav", Filename: "older.wav"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(f.manager.assetHistoryPath(a), filepath.Join(f.manager.dir, "media-history", "legacy-filename.json")); err != nil {
		t.Fatal(err)
	}
	p, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	in.TraceID = "current"
	p.SetTrace(in)
	result, err := p.stageOutputWithAsset(context.Background(), plugins.Output{Media: []plugins.MediaResult{{Kind: "voice", Data: []byte("synthetic prepared voice"), MIMEType: "audio/wav", Filename: "speech.wav"}}}, &assetOriginal{VoiceComplete: true, Data: []byte("synthetic full voice"), MIME: "audio/wav", Filename: "mom.wav"})
	if err != nil {
		t.Fatal("staging blocked after history recovery", err)
	}
	var staged struct {
		Media []companion.GeneratedMedia `json:"media"`
	}
	if json.Unmarshal([]byte(result), &staged) != nil || len(staged.Media) != 1 || staged.Media[0].AssetID == "" {
		t.Fatal("voice alias missing", result)
	}
	if _, err = p.reuseAsset(context.Background(), staged.Media[0].AssetID, "voice"); err != nil {
		t.Fatal("voice alias cannot be reused", err)
	}
}
