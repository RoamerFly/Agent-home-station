package admin

import (
	"os"
	"path/filepath"
	"testing"
	"wechat-bot/internal/companion"
)

func TestPublicSongLibraryFindsOtherConversationAndKeepsPrivateAudioScoped(t *testing.T) {
	f := setup(t)
	first := companion.Input{Channel: "wechat", Account: "test", Thread: "first-room", TraceID: "song"}
	public, err := f.manager.rememberAssetFormats(first, assetOriginal{Data: []byte("synthetic published song"), MIME: "audio/mp4", Filename: "歌手-着魔.m4a", URL: "https://www.bilibili.com/video/BV1234567890"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	private, err := f.manager.rememberAssetFormats(first, assetOriginal{Data: []byte("synthetic private memo"), MIME: "audio/wav", Filename: "私人-着魔.wav"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	other := first
	other.Thread = "second-room"
	rows, total, err := f.manager.searchMusicLibrary(other.Account, "着魔", 0, 12)
	if err != nil || total != 1 || rows[0]["asset_id"] != public.ID {
		t.Fatalf("shared song lookup: %v %d %v", err, total, rows)
	}
	if _, err := os.Stat(filepath.Join(f.manager.musicLibraryDirectory(other.Account), public.Hash+".m4a")); err != nil {
		t.Fatal("verified library copy missing", err)
	}
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(other)
	if _, _, err := p.referenceAsset(public.ID); err != nil {
		t.Fatal("shared song cannot be used by RVC", err)
	}
	if _, _, err := p.referenceAsset(private.ID); err == nil {
		t.Fatal("private conversation audio leaked into shared library")
	}
}

func TestOneVoiceRegistrationSupportsSpeechAndRVCWeights(t *testing.T) {
	v := voiceEntry{ID: "synthetic-fish", Engine: "GPT-SoVITS", ProfileID: "private-synthetic-fish", Weights: []voiceFile{{Kind: "gpt"}, {Kind: "sovits"}, {Kind: "rvc"}}}
	if !voiceSupportsRVC(v) || v.Engine != "GPT-SoVITS" {
		t.Fatal("unified voice lost one synthesis route")
	}
}
