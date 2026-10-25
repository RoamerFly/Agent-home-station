package mcptools

import (
	"context"
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Find real file URLs from a public catalog, not guessed extensions appended
// to player pages. Candidate URLs are downloaded and validated separately.
func webAudioSearch(ctx context.Context, args json.RawMessage) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var in struct {
		Query string `json:"query"`
	}
	if json.Unmarshal(args, &in) != nil || strings.TrimSpace(in.Query) == "" || len(in.Query) > 200 {
		return "", &WebError{Code: "query_required", Stage: "arguments"}
	}
	if result, err := incompetechAudio(ctx, in.Query); err == nil && result != "" {
		return result, nil
	}
	query := strings.NewReplacer(`"`, " ", "(", " ", ")", " ", "{", " ", "}", " ", ":", " ").Replace(in.Query)
	values := url.Values{"q": {`mediatype:audio AND (` + query + `)`}, "fl[]": {"identifier", "title"}, "rows": {"3"}, "output": {"json"}}
	b, _, err := publicGet(ctx, "https://archive.org/advancedsearch.php?"+values.Encode())
	if err != nil {
		return "", err
	}
	var search struct {
		Response struct {
			Docs []struct {
				Identifier string `json:"identifier"`
				Title      string `json:"title"`
			} `json:"docs"`
		} `json:"response"`
	}
	if json.Unmarshal(b, &search) != nil {
		return "", &WebError{Code: "catalog_invalid", Stage: "decode"}
	}
	items := []map[string]any{}
	for i, doc := range search.Response.Docs {
		if i >= 3 {
			break
		}
		if doc.Identifier == "" || strings.ContainsAny(doc.Identifier, "/\\?#") {
			continue
		}
		b, _, e := publicGet(ctx, "https://archive.org/metadata/"+url.PathEscape(doc.Identifier))
		if e != nil {
			continue
		}
		var meta struct {
			Files []struct {
				Name    string `json:"name"`
				Size    string `json:"size"`
				Length  string `json:"length"`
				Title   string `json:"title"`
				Private string `json:"private"`
			} `json:"files"`
			Metadata map[string]any `json:"metadata"`
			IsDark   bool           `json:"is_dark"`
		}
		if json.Unmarshal(b, &meta) != nil || meta.IsDark {
			continue
		}
		added := 0
		for _, f := range meta.Files {
			ext := strings.ToLower(path.Ext(f.Name))
			if ext != ".mp3" && ext != ".ogg" && ext != ".wav" && ext != ".m4a" {
				continue
			}
			size, _ := strconv.ParseInt(f.Size, 10, 64)
			if size <= 0 || size > 25<<20 || f.Private == "true" {
				continue
			}
			name := f.Title
			if name == "" {
				name = doc.Title + " / " + f.Name
			}
			items = append(items, map[string]any{"title": name, "url": "https://archive.org/download/" + url.PathEscape(doc.Identifier) + "/" + url.PathEscape(f.Name), "source": "https://archive.org/details/" + url.PathEscape(doc.Identifier), "bytes": size, "duration": f.Length, "license": meta.Metadata["licenseurl"]})
			added++
			if added >= 2 {
				break
			}
		}
		if len(items) >= 4 {
			break
		}
	}
	result, _ := json.Marshal(map[string]any{"candidates": items, "instruction": "这些是公开目录返回的真实音频候选，不保证符合歌曲/风格需求，选择内容匹配的条目，再调用 web_media_download(kind=voice) 下载。没有匹配曲目时不要把其他歌曲冒充指定歌曲。"})
	return string(result), nil
}

var musicCatalogCache struct {
	sync.Mutex
	Body    []byte
	Genres  map[string]string
	At      time.Time
	Loading chan struct{}
}

func incompetechAudio(ctx context.Context, query string) (string, error) {
	for {
		musicCatalogCache.Lock()
		if len(musicCatalogCache.Body) > 0 && time.Since(musicCatalogCache.At) < 30*time.Minute {
			b, g, at := musicCatalogCache.Body, musicCatalogCache.Genres, musicCatalogCache.At
			musicCatalogCache.Unlock()
			return catalogAudioResult(b, g, query, true, at)
		}
		if wait := musicCatalogCache.Loading; wait != nil {
			musicCatalogCache.Unlock()
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-wait:
				continue
			}
		}
		musicCatalogCache.Loading = make(chan struct{})
		musicCatalogCache.Unlock()
		b, g, err := loadMusicCatalog(ctx)
		musicCatalogCache.Lock()
		if err == nil {
			musicCatalogCache.Body = b
			musicCatalogCache.Genres = g
			musicCatalogCache.At = time.Now()
		}
		close(musicCatalogCache.Loading)
		musicCatalogCache.Loading = nil
		musicCatalogCache.Unlock()
		if err != nil {
			return "", err
		}
		return catalogAudioResult(b, g, query, false, time.Now())
	}
}
func catalogAudioResult(b []byte, genres map[string]string, query string, cached bool, at time.Time) (string, error) {
	result, err := selectCatalogAudio(b, genres, query)
	if err != nil || result == "" {
		return result, err
	}
	var value map[string]any
	_ = json.Unmarshal([]byte(result), &value)
	value["catalog_cache_hit"] = cached
	value["catalog_fetched_at"] = at.UTC().Format(time.RFC3339)
	encoded, _ := json.Marshal(value)
	return string(encoded), nil
}
func loadMusicCatalog(ctx context.Context) ([]byte, map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	b, _, _, err := publicReadTimeout(ctx, "https://incompetech.com/music/royalty-free/pieces.json", maxBody, 20*time.Second)
	if err != nil {
		return nil, nil, err
	}
	var tracks []map[string]any
	if json.Unmarshal(b, &tracks) != nil {
		return nil, nil, &WebError{Code: "catalog_invalid", Stage: "decode"}
	}
	genres := map[string]string{}
	// Genre labels enrich broad style queries; their page is optional. Exact
	// song-title searches no longer depend on this second webpage succeeding.
	page, _, _, err := publicReadTimeout(ctx, "https://incompetech.com/music/royalty-free/music.html", maxBody, 10*time.Second)
	if err == nil {
		for _, m := range regexp.MustCompile(`"id"\s*:\s*(\d+)\s*,\s*"genre"\s*:\s*"([^"]+)"`).FindAllStringSubmatch(string(page), 100) {
			genres[m[1]] = strings.ToLower(m[2])
		}
	}
	return b, genres, nil
}
func selectCatalogAudio(body []byte, genres map[string]string, query string) (string, error) {
	var tracks []map[string]any
	if json.Unmarshal(body, &tracks) != nil {
		return "", &WebError{Code: "catalog_invalid", Stage: "decode"}
	}
	q := strings.ToLower(query)
	q = strings.NewReplacer("流行", "pop", "钢琴", "piano", "轻松", "relaxed", "欢快", "bouncy", "古典", "classical", "摇滚", "rock", "电子", "electronica").Replace(q)
	words := strings.Fields(q)
	styleWords := " music song audio public royalty free instrumental pop piano relaxed relaxing cheerful upbeat light bouncy classical rock electronica happy calm ambient gentle easy listening cute romantic bright fun cinematic guitar drums acoustic lofi jazz folk blues dance background short tune morning evening bedtime kevin macleod "
	named := false
	for _, word := range words {
		if len(word) > 1 && !strings.Contains(styleWords, " "+word+" ") {
			named = true
		}
	}
	type match struct {
		track map[string]any
		score int
	}
	matches := []match{}
	for _, t := range tracks {
		filename, _ := t["filename"].(string)
		if filename == "" || !strings.HasSuffix(strings.ToLower(filename), ".mp3") || strings.ContainsAny(filename, "/\\") {
			continue
		}
		title, _ := t["title"].(string)
		desc, _ := t["description"].(string)
		inst, _ := t["instruments"].(string)
		feel, _ := t["feel"].(string)
		genre, _ := t["genre"].(string)
		hay := strings.ToLower(title + " " + desc + " " + inst + " " + feel + " " + genres[genre])
		score := 0
		for _, word := range words {
			if len(word) > 1 && word != "music" && word != "song" && strings.Contains(hay, word) {
				score++
			}
		}
		if named {
			identity := strings.ToLower(title + " Kevin MacLeod")
			for _, word := range words {
				if len(word) > 1 && word != "music" && word != "song" && word != "audio" && !strings.Contains(identity, strings.Trim(word, `"'`)) {
					score = 0
					break
				}
			}
		}
		if score > 0 {
			matches = append(matches, match{t, score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
	items := []map[string]any{}
	for i, m := range matches {
		if i >= 4 {
			break
		}
		t := m.track
		filename, _ := t["filename"].(string)
		isrc, _ := t["isrc"].(string)
		items = append(items, map[string]any{"title": t["title"], "artist": "Kevin MacLeod", "url": "https://incompetech.com/music/royalty-free/mp3-royaltyfree/" + url.PathEscape(filename), "source": "https://incompetech.com/music/royalty-free/index.html?isrc=" + url.QueryEscape(isrc), "duration": t["length"], "description": t["description"], "instruments": t["instruments"], "attribution_required": true})
	}
	if len(items) == 0 {
		return "", nil
	}
	result, _ := json.Marshal(map[string]any{"source_catalog": "https://incompetech.com/music/royalty-free/pieces.json", "candidates": items, "instruction": "真实公开音乐目录候选，以器乐作品为主。必须按需求核对标题、风格及有无人声，不能冒充某首指定流行歌曲。可下载后作为语音分享；文字附上曲名、作者 Kevin MacLeod、来源页与来源标注的 CC 许可，下载前可读取 source 确认。若不是对方要的录音，请继续搜索其他来源，而不是偷换曲目。"})
	return string(result), nil
}
