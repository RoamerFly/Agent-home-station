package admin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"wechat-bot/internal/mcptools"
)

// Evidence is private, scoped and bound to audio bytes. A tool argument claiming
// "verified" is never evidence. ASR remains a candidate, not certified lyrics.
type lyricEvidence struct {
	ID           string    `json:"id"`
	Scope        string    `json:"scope"`
	AudioHash    string    `json:"audio_hash"`
	Text         string    `json:"text"`
	Source       string    `json:"source"`
	PublicURL    string    `json:"public_source_url,omitempty"`
	WebURL       string    `json:"lyrics_page,omitempty"`
	BaseEvidence string    `json:"base_asr_evidence,omitempty"`
	Offset       int       `json:"offset_seconds"`
	Seconds      int       `json:"seconds"`
	At           time.Time `json:"at"`
}

var lyricSection = regexp.MustCompile(`(?i)\[(?:verse|chorus|bridge|intro|outro|pre-chorus|instrumental|hook|refrain)(?:\s*\d+)?\]`)

func lyricText(text string) string {
	var b strings.Builder
	for _, r := range lyricSection.ReplaceAllString(text, "") {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func (p *permittedTools) saveLyricEvidence(a mediaAsset, text, source string, offset, seconds int) (lyricEvidence, error) {
	return p.saveLyricEvidenceDetails(a, text, source, offset, seconds, "", "")
}

func (p *permittedTools) saveLyricEvidenceDetails(a mediaAsset, text, source string, offset, seconds int, webURL, base string) (lyricEvidence, error) {
	e := lyricEvidence{Scope: assetScope(p.trace), AudioHash: a.Hash, Text: strings.TrimSpace(text), Source: source, Offset: offset, Seconds: seconds, At: time.Now()}
	e.WebURL, e.BaseEvidence = webURL, base
	if (source == "asr_candidate" || source == "web_aligned_asr") && mcptools.IsPublicVideoPage(a.URL) {
		e.PublicURL = a.URL
	}
	if len(e.Text) > 6000 || lyricText(e.Text) == "" {
		return e, errors.New("没有可用歌词，不创建依据")
	}
	e.ID = agentID(e.Scope, e.AudioHash, e.Text, source, stringMustJSON([]int{offset, seconds}), webURL, base)
	dir := filepath.Join(p.manager.dir, "music-lyrics")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return e, err
	}
	if _, err := os.Stat(filepath.Join(dir, e.ID+".json")); os.IsNotExist(err) {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) >= 1000 {
			return e, errors.New("歌词依据存储已满，请整理后再登记；旧依据未删除")
		}
	}
	b, _ := json.Marshal(e)
	return e, writePrivate(filepath.Join(dir, e.ID+".json"), b)
}

func stringMustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func (p *permittedTools) checkLyricEvidence(a mediaAsset, lyrics string, ids []string) error {
	if len(ids) == 0 || len(ids) > 10 {
		return toolInputError("参考翻唱需要lyrics_evidence_ids；先用music_reference_inspect实际转写原曲或登记用户本条提供的原词；本次未提交生成")
	}
	var texts strings.Builder
	seen := map[string]bool{}
	for _, id := range ids {
		if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" || seen[id] {
			return toolInputError("歌词依据编号无效或重复；本次未提交生成")
		}
		seen[id] = true
		b, err := os.ReadFile(filepath.Join(p.manager.dir, "music-lyrics", id+".json"))
		var e lyricEvidence
		if err != nil || len(b) > 16<<10 || json.Unmarshal(b, &e) != nil || e.ID != id || !p.lyricEvidenceMatches(a, e) {
			return toolInputError("歌词依据与本会话原曲不匹配，请重新读取所选原曲；本次未提交生成")
		}
		texts.WriteString(e.Text)
	}
	if lyricText(lyrics) == "" || lyricText(lyrics) != lyricText(texts.String()) {
		return toolInputError("提交歌词与读取的原曲依据不一致；请读取返回的依据文本，必要时用music_lyrics_compare核对修正后再提交；本次未提交生成")
	}
	return nil
}

func (p *permittedTools) lyricEvidenceMatches(a mediaAsset, e lyricEvidence) bool {
	if e.AudioHash != a.Hash || (e.Source != "asr_candidate" && e.Source != "user_supplied" && e.Source != "web_aligned_asr") {
		return false
	}
	if e.Scope == assetScope(p.trace) {
		return true
	}
	// Reuse observations of the SAME downloaded public recording, never another
	// room's messages, user-provided words or private uploaded audio.
	return (e.Source == "asr_candidate" || e.Source == "web_aligned_asr") && e.PublicURL != "" && e.PublicURL == a.URL && mcptools.IsPublicVideoPage(a.URL)
}

func (p *permittedTools) availableLyricEvidence(a mediaAsset) []map[string]any {
	out := []map[string]any{}
	entries, err := os.ReadDir(filepath.Join(p.manager.dir, "music-lyrics"))
	if err != nil || len(entries) > 1000 {
		return out
	}
	for _, entry := range entries {
		if entry.IsDir() || len(out) >= 10 {
			continue
		}
		b, err := os.ReadFile(filepath.Join(p.manager.dir, "music-lyrics", entry.Name()))
		var e lyricEvidence
		if err != nil || len(b) > 16<<10 || json.Unmarshal(b, &e) != nil || entry.Name() != e.ID+".json" || !p.lyricEvidenceMatches(a, e) {
			continue
		}
		out = append(out, map[string]any{"lyrics_evidence_id": e.ID, "lyrics": e.Text, "source": e.Source, "offset_seconds": e.Offset, "seconds": e.Seconds, "warning": "ASR仍是候选词，复用不代表已人工校对"})
	}
	return out
}
