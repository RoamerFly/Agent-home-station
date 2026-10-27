package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

func TestCoverEvidenceRejectsInventedWordsWrongAudioAndOtherRoom(t *testing.T) {
	f := setup(t)
	p, _, _ := f.manager.toolAccess()
	in := companion.Input{Account: "test", Channel: "wechat", Thread: "filehelper", TraceID: "lyrics"}
	p.SetTrace(in)
	a, e := f.manager.rememberAssetFormats(in, assetOriginal{Data: []byte("synthetic reference"), MIME: "audio/wav", Filename: "source.wav"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := p.saveLyricEvidence(a, "晚风吹过窗台\n我们等花开", "asr_candidate", 0, 30)
	if e != nil {
		t.Fatal(e)
	}
	ids := []string{proof.ID}
	if e = p.checkLyricEvidence(a, "[Verse]\n晚风吹过窗台\n[Chorus]\n我们等花开", ids); e != nil {
		t.Fatal(e)
	}
	for _, words := range []string{"给我一碗新面条", "晚风吹过窗台[偷偷改词]我们等花开"} {
		err := p.checkLyricEvidence(a, words, ids)
		if err == nil {
			t.Fatal("invented words accepted")
		}
		if message := mcptools.FailureDescription(err); !strings.Contains(message, "依据不一致") || strings.Contains(message, "工具连接或执行失败") {
			t.Fatal("actionable lyrics error was masked", message)
		}
	}
	a.Hash = "other-audio"
	if p.checkLyricEvidence(a, proof.Text, ids) == nil {
		t.Fatal("wrong audio accepted")
	}
	a.Hash = proof.AudioHash
	p.trace.Thread = "other"
	if p.checkLyricEvidence(a, proof.Text, ids) == nil {
		t.Fatal("other room accepted")
	}
}

func TestSuppliedLyricsMustExistInCurrentUserText(t *testing.T) {
	f := setup(t)
	f.manager.config.Companion.Permissions.Remember = true
	p, _, _ := f.manager.toolAccess()
	in := companion.Input{Account: "test", Channel: "wechat", Thread: "filehelper", Text: "原词就用：晚风吹过窗台", TraceID: "source"}
	p.SetTrace(in)
	a, e := f.manager.rememberAssetFormats(in, assetOriginal{Data: []byte("synthetic audio"), MIME: "audio/wav", Filename: "a.wav"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, words := range []string{"晚风吹过窗台", "模型编写的词"} {
		b, _ := json.Marshal(map[string]any{"asset_id": a.ID, "supplied_lyrics": words})
		result, e := p.inspectMusicReference(context.Background(), b)
		if (e == nil) != (words == "晚风吹过窗台") {
			t.Fatal(result, e)
		}
	}
}

func TestPublicSongEvidenceReusesSameRecordingWithoutSharingPrivateWords(t *testing.T) {
	f := setup(t)
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "one"})
	a := mediaAsset{Hash: "synthetic-audio-hash", URL: "https://www.bilibili.com/video/BV11J41157ZX/"}
	public, err := p.saveLyricEvidence(a, "合成歌词", "asr_candidate", 0, 30)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.saveLyricEvidence(a, "私人提供的歌词", "user_supplied", 0, 30)
	if err != nil {
		t.Fatal(err)
	}
	p.trace.Thread = "two"
	if err = p.checkLyricEvidence(a, public.Text, []string{public.ID}); err != nil {
		t.Fatal(err)
	}
	available := p.availableLyricEvidence(a)
	if len(available) != 1 || available[0]["lyrics"] != "合成歌词" {
		t.Fatal("private text leaked", available)
	}
	a.URL = "https://www.bilibili.com/video/BV1Vs411H75T/"
	if p.checkLyricEvidence(a, public.Text, []string{public.ID}) == nil {
		t.Fatal("wrong public recording accepted")
	}
}
