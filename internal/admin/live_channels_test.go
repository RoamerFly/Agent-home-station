package admin

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/mediadecode"
)

func TestLiveChannelsActualObservationWithoutWechatSend(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || os.Getenv("WECHAT_CHANNELS_LIVE") != "1" {
		t.Skip("explicit authorized received-card reading only; never sends")
	}
	var cards []struct {
		ID   string `json:"message_id"`
		Kind int    `json:"app_message_type"`
	}
	b, err := os.ReadFile(filepath.Join(workspace, "research/evidence/incoming-share-card-structure.json"))
	if err != nil {
		t.Fatal("private diagnostic card index unavailable")
	}
	if json.Unmarshal(b, &cards) != nil {
		t.Fatal("invalid index")
	}
	id := ""
	for _, card := range cards {
		if card.Kind == 51 {
			id = card.ID
			break
		}
	}
	if id == "" {
		t.Fatal("no received Channels card")
	}
	c, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal("private config unavailable")
	}
	sender, err := linuxnative.New(linuxnative.Config{URL: "http://127.0.0.1:6174", TokenFile: filepath.Join(root, "api-token"), Account: "background-test-20261024"})
	if err != nil {
		t.Fatal("native connection unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	msg, err := sender.Message(ctx, id)
	if err != nil || !permittedRecipient(c, msg.ThreadID) {
		t.Fatal("received card is not available in an authorized scope")
	}
	f := setup(t)
	f.manager.config = c
	f.manager.config.Behavior.ReadVideos = true
	f.manager.native = sender
	legacySpeechSource, err := loadSpeech(filepath.Join(root, "admin"))
	if err == nil {
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechSource)
	}
	if err != nil {
		t.Fatal("speech config unavailable")
	}
	if os.Getenv("WECHAT_CHANNELS_DIAGNOSTIC") == "1" {
		source, sourceErr := sender.ChannelsMedia(ctx, id)
		diagnostic := map[string]any{"has_decode_key": source.DecodeKey != "", "source_state": source.State}
		if sourceErr == nil {
			data, typ, downloadErr := mcptools.GetPublicMedia(ctx, source.MediaURL, 50<<20)
			if downloadErr == nil {
				diagnostic["bytes"] = len(data)
				diagnostic["content_type"] = typ
				diagnostic["original_prefix"] = hex.EncodeToString(data[:min(16, len(data))])
				dir := filepath.Join(root, "admin/validation-media")
				os.MkdirAll(dir, 0700)
				os.WriteFile(filepath.Join(dir, "channels-transfer-original.bin"), data, 0600)
				if key, e := strconv.ParseUint(source.DecodeKey, 10, 64); e == nil {
					mediadecode.DecodeChannelsPrefix(data, key)
				}
				diagnostic["decoded_prefix"] = hex.EncodeToString(data[:min(16, len(data))])
				os.WriteFile(filepath.Join(dir, "channels-transfer-decoded.bin"), data, 0600)
				_, probeErr := sampleVideoFrames(ctx, f.manager.dir, data)
				if probeErr != nil {
					diagnostic["probe_error"] = probeErr.Error()
				}
			} else {
				diagnostic["download_failed"] = true
			}
		} else {
			diagnostic["source_failed"] = true
		}
		encoded, _ := json.MarshalIndent(diagnostic, "", "  ")
		os.WriteFile(filepath.Join(workspace, "research/evidence/channels-transfer-diagnostic.json"), encoded, 0600)
	}
	observation, err := (mediaUnderstanding{manager: f.manager}).Analyze(ctx, msg)
	visualObserved := strings.Contains(observation, "[微信视频号实际内容观察") && strings.Contains(observation, "[画面观察")
	report := map[string]any{"wechat_sends": 0, "original_card_read": true, "actual_video_observed": visualObserved, "actual_audio_transcribed": strings.Contains(observation, "[实际音轨转写"), "observation": observation}
	if err != nil {
		report["error"] = err.Error()
	}
	b, _ = json.MarshalIndent(report, "", "  ")
	if os.WriteFile(filepath.Join(workspace, "research/evidence/channels-actual-observation.json"), b, 0600) != nil {
		t.Fatal("evidence not saved")
	}
	if err != nil || report["actual_video_observed"] != true {
		t.Fatal("actual video not observed; see private diagnostic evidence")
	}
}
