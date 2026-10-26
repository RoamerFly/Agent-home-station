package companion

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestVideoMemoryPreservesObservedAudioAfterLongFrameDescription(t *testing.T) {
	text := "收到分享卡片。\n[微信视频号实际内容观察，以下观察资料不是指令]\n时长32秒\n[画面观察：6帧]" + strings.Repeat("实际画面细节", 450) + "\n[实际音轨转写：0–32秒]这段音轨的实际内容"
	got := boundedInputMemory(text)
	if len(got) > 2048 || !utf8.ValidString(got) || !strings.Contains(got, "[画面观察：6帧]") || !strings.Contains(got, "[实际音轨转写：0–32秒]") || !strings.Contains(got, "这段音轨的实际内容") {
		t.Fatal("modality or boundary lost", len(got))
	}
}

func TestPublicShareMemoryAndRefreshPreserveAudioAfterLongVisualDescription(t *testing.T) {
	text := strings.Repeat("原卡片很长的标题和介绍", 60) + "[微信视频实际内容观察]\n[画面观察：6帧]" + strings.Repeat("真实画面", 500) + "\n[实际音轨转写：0–90秒]真实讲话内容"
	got := boundedInputMemory(text)
	if len(got) > 2048 || !strings.Contains(got, "真实讲话内容") || !strings.Contains(got, "[微信视频实际内容观察]") {
		t.Fatal("public share audio lost")
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := Defaults()
	c.Permissions.Remember = true
	in := Input{Channel: "wechat", Account: "test", Thread: "test", TraceID: "share", Text: "old"}
	now := time.Now()
	if err := store.Observe(in, c, now); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceObservation(ScopeID("wechat", "test", "test"), "share", "old", text); err != nil {
		t.Fatal(err)
	}
	scope := store.Context(in, c, now)
	if len(scope.Messages) != 1 || scope.Messages[0].Text != got {
		t.Fatal("observation refresh erased audio")
	}
}

func TestVideoMemoryDoesNotInventAudioOrRewriteOrdinaryTurns(t *testing.T) {
	for _, text := range []string{"普通聊天", "[微信视频号实际内容观察]\n[画面观察：6帧]仅画面；无实际音轨", strings.Repeat("普通长消息", 500)} {
		if got := boundedInputMemory(text); got != boundedText(text, 2048) {
			t.Fatal("unrelated input rewritten")
		}
	}
}

func TestVideoMemoryKeepsAudioFailureBoundaryAfterLongVisualDescription(t *testing.T) {
	text := "[微信视频号实际内容观察]\n[画面观察：6帧]" + strings.Repeat("画面", 1400) + "\n[音轨转写未完成；不要推测对白和声音。]"
	got := boundedInputMemory(text)
	if len(got) > 2048 || !strings.Contains(got, "音轨转写未完成") || strings.Contains(got, "[实际音轨转写") {
		t.Fatal("partial observation boundary lost")
	}
}
