package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Models fetches the authenticated provider catalogue without generating text.
func (c *Client) Models(ctx context.Context) ([]string, error) {
	u, err := url.Parse(c.endpoint)
	if err != nil || u.Scheme != "https" {
		return nil, errors.New("model_catalogue_address_invalid")
	}
	u.Path = strings.TrimSuffix(u.Path, "/chat/completions") + "/models"
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("model_catalogue_request_invalid")
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	response, err := c.http.Do(req)
	if err != nil {
		return nil, errors.New("model_catalogue_network_failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, errors.New("model_catalogue_unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, errors.New("model_catalogue_response_invalid")
	}
	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &data) != nil || len(data.Data) == 0 || len(data.Data) > 2048 {
		return nil, errors.New("model_catalogue_response_invalid")
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, item := range data.Data {
		id := item.ID
		if id == "" || len(id) > 128 || strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return nil, errors.New("model_catalogue_response_invalid")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
