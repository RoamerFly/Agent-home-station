package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var kuaishouPhotoPath = regexp.MustCompile(`^/(?:fw/photo|short-video)/([A-Za-z0-9]{8,40})/?$`)
var kuaishouStateMarker = regexp.MustCompile(`window\.__APOLLO_STATE__\s*=\s*`)

type kuaishouRequestKey struct{}

func kuaishouMediaHost(host string) bool {
	return strings.HasSuffix(host, ".kwaicdn.com") || strings.HasSuffix(host, ".djvod.ndcimgs.com")
}

func kuaishouPhotoID(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || validPublicURL(raw) != nil || len(raw) > 8192 || u.Port() != "" {
		return ""
	}
	if u.Hostname() != "v.m.chenzhongtech.com" && u.Hostname() != "www.kuaishou.com" && u.Hostname() != "kuaishou.com" {
		return ""
	}
	m := kuaishouPhotoPath.FindStringSubmatch(u.Path)
	if len(m) != 2 {
		return ""
	}
	if q := u.Query().Get("photoId"); q != "" && q != m[1] {
		return ""
	}
	return m[1]
}

type kuaishouPhoto struct {
	ID       string          `json:"id"`
	Caption  string          `json:"caption"`
	Duration int64           `json:"duration"`
	PhotoURL string          `json:"photoUrl"`
	Manifest json.RawMessage `json:"manifest"`
}

// Read only the exact received work's public page state. References are bounded;
// recommended works, scripts and arbitrary JSON URLs are never media sources.
func parseKuaishouPhoto(body []byte, id string) (kuaishouPhoto, []string, error) {
	var photo kuaishouPhoto
	missing := func() (kuaishouPhoto, []string, error) {
		return photo, nil, &WebError{Code: "video_page_metadata_unavailable", Stage: "video_metadata"}
	}
	if len(body) > 2<<20 {
		return missing()
	}
	loc := kuaishouStateMarker.FindIndex(body)
	if loc == nil {
		return missing()
	}
	var state struct {
		DefaultClient map[string]json.RawMessage `json:"defaultClient"`
	}
	if json.NewDecoder(bytes.NewReader(body[loc[1]:])).Decode(&state) != nil || len(state.DefaultClient) > 10000 {
		return missing()
	}
	dc := state.DefaultClient
	if json.Unmarshal(dc["VisionVideoDetailPhoto:"+id], &photo) != nil || photo.ID != id || photo.Duration <= 0 || photo.Duration > 3600000 || len(photo.Caption) > 4096 {
		return missing()
	}
	resolve := func(raw json.RawMessage) json.RawMessage {
		seen := map[string]bool{}
		for i := 0; i < 8; i++ {
			var ref struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			}
			if json.Unmarshal(raw, &ref) != nil || ref.Type != "id" {
				return raw
			}
			if ref.ID == "" || seen[ref.ID] {
				return nil
			}
			seen[ref.ID] = true
			raw = dc[ref.ID]
		}
		return nil
	}
	urls := []string{}
	seen := map[string]bool{}
	add := func(raw string) {
		u, err := url.Parse(raw)
		if err == nil && u.Scheme == "https" && kuaishouMediaHost(u.Hostname()) && u.Port() == "" && validPublicURL(raw) == nil && len(raw) <= 8192 && !seen[raw] {
			seen[raw] = true
			urls = append(urls, raw)
		}
	}
	add(photo.PhotoURL)
	var manifest struct {
		Sets []json.RawMessage `json:"adaptationSet"`
	}
	if json.Unmarshal(resolve(photo.Manifest), &manifest) == nil && len(manifest.Sets) <= 20 {
		for _, rawSet := range manifest.Sets {
			var set struct {
				Reps []json.RawMessage `json:"representation"`
			}
			if json.Unmarshal(resolve(rawSet), &set) != nil || len(set.Reps) > 30 {
				continue
			}
			for _, rawRep := range set.Reps {
				var rep struct {
					URL    json.RawMessage `json:"url"`
					Backup json.RawMessage `json:"backupUrl"`
				}
				if json.Unmarshal(resolve(rawRep), &rep) != nil {
					continue
				}
				for _, rawURLs := range []json.RawMessage{rep.URL, rep.Backup} {
					rawURLs = resolve(rawURLs)
					var single string
					if json.Unmarshal(rawURLs, &single) == nil {
						add(single)
						continue
					}
					var list struct {
						JSON []string `json:"json"`
					}
					if json.Unmarshal(rawURLs, &list) == nil && len(list.JSON) <= 10 {
						for _, u := range list.JSON {
							add(u)
						}
					}
				}
			}
		}
	}
	if len(urls) == 0 {
		return photo, nil, &WebError{Code: "video_public_playback_unavailable", Stage: "video_playback"}
	}
	return photo, urls, nil
}

func downloadKuaishouVideo(ctx context.Context, raw string, limit int64, content bool) (PublicVideo, error) {
	var out PublicVideo
	id := kuaishouPhotoID(raw)
	if id == "" {
		return out, &WebError{Code: "video_page_unsupported"}
	}
	if limit <= 0 || limit > MaxPublicMediaBytes {
		return out, &WebError{Code: "media_limit_invalid"}
	}
	ctx = context.WithValue(ctx, kuaishouRequestKey{}, true)
	metaCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Keep original share parameters inside the HTTP request only. Some public
	// works expose no state when these parameters are removed from the request.
	body, _, _, err := publicRead(metaCtx, raw, 2<<20)
	if err != nil {
		return out, err
	}
	photo, urls, err := parseKuaishouPhoto(body, id)
	page := "https://www.kuaishou.com/short-video/" + id
	if err != nil {
		u, _ := url.Parse(raw)
		canonical := page
		if u.RawQuery != "" {
			canonical += "?" + u.RawQuery
		}
		body, _, _, err = publicRead(metaCtx, canonical, 2<<20)
		if err != nil {
			return out, err
		}
		photo, urls, err = parseKuaishouPhoto(body, id)
	}
	if err != nil {
		return out, err
	}
	out = PublicVideo{Title: photo.Caption, PageURL: page, DurationMS: photo.Duration}
	if !content && photo.Duration > 60000 {
		out.LinkOnly = true
		return out, nil
	}
	for _, direct := range urls[:min(3, len(urls))] {
		data, typ, e := GetPublicMedia(ctx, direct, limit)
		if e == nil {
			typ, e = ValidateMediaType("video", typ, data)
		}
		if e == nil {
			out.Data, out.ContentType = data, typ
			return out, nil
		}
		err = e
		if ctx.Err() != nil {
			return PublicVideo{}, ctx.Err()
		}
	}
	return PublicVideo{}, err
}
