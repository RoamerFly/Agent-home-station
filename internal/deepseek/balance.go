package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type BalanceInfo struct {
	Currency string `json:"currency"`
	Total    string `json:"total_balance"`
	Granted  string `json:"granted_balance"`
	ToppedUp string `json:"topped_up_balance"`
}
type Balance struct {
	Available bool          `json:"is_available"`
	Infos     []BalanceInfo `json:"balance_infos"`
}

// The key is sent only to the configured official origin. Compatible services
// have no shared balance contract; do not guess their private billing routes.
func (c *Client) Balance(ctx context.Context) (Balance, error) {
	u, err := url.Parse(c.endpoint)
	if err != nil || u.Scheme != "https" || u.Host != "api.deepseek.com" || (u.Path != "/chat/completions" && u.Path != "/v1/chat/completions") {
		return Balance{}, errors.New("balance_unsupported")
	}
	u.Path = "/user/balance"
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Balance{}, errors.New("balance_request_invalid")
	}
	r.Header.Set("Authorization", "Bearer "+c.key)
	response, err := c.http.Do(r)
	if err != nil {
		return Balance{}, errors.New("balance_network_failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return Balance{}, errors.New("balance_http_" + strconv.Itoa(response.StatusCode))
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, 16*1024+1))
	if err != nil || len(b) > 16*1024 {
		return Balance{}, errors.New("balance_response_invalid")
	}
	var result Balance
	if json.Unmarshal(b, &result) != nil || len(result.Infos) == 0 || len(result.Infos) > 2 {
		return Balance{}, errors.New("balance_response_invalid")
	}
	var present struct {
		Available *bool `json:"is_available"`
	}
	if json.Unmarshal(b, &present) != nil || present.Available == nil {
		return Balance{}, errors.New("balance_response_invalid")
	}
	for _, info := range result.Infos {
		if info.Currency != "CNY" && info.Currency != "USD" {
			return Balance{}, errors.New("balance_response_invalid")
		}
		for _, raw := range []string{info.Total, info.Granted, info.ToppedUp} {
			v, e := strconv.ParseFloat(raw, 64)
			if len(raw) > 32 || e != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1e12 {
				return Balance{}, errors.New("balance_response_invalid")
			}
		}
	}
	return result, nil
}
