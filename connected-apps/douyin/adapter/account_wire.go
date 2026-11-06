package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	authorizationEndpoint = "https://open.douyin.com/platform/oauth/connect"
	tokenEndpoint         = "https://open.douyin.com/oauth/access_token/"
	profileEndpoint       = "https://open.douyin.com/oauth/userinfo/"
)

var (
	ErrOAuthInvalid     = errors.New("抖音授权参数无效")
	ErrOAuthRejected    = errors.New("抖音授权被拒绝或已失效")
	ErrOAuthUnavailable = errors.New("抖音授权服务暂不可用")
)

type OAuthSettings struct {
	ClientKey    string
	ClientSecret string
	RedirectURI  string
}

type OAuthGrant struct {
	AccessToken      string
	RefreshToken     string
	OpenID           string
	Scope            string
	ExpiresAt        time.Time
	RefreshExpiresAt time.Time
}

type AccountProfile struct {
	OpenID   string
	Nickname string
	Avatar   string
}

func AuthorizationURL(settings OAuthSettings, scopes []string, state string) (string, error) {
	if validateOAuthSettings(settings) != nil || len(scopes) == 0 || strings.TrimSpace(state) != state || len(state) > 512 {
		return "", ErrOAuthInvalid
	}
	clean := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if strings.TrimSpace(scope) != scope || scope == "" || len(scope) > 128 || strings.ContainsAny(scope, ",&=") {
			return "", ErrOAuthInvalid
		}
		clean = append(clean, scope)
	}
	query := url.Values{"client_key": {settings.ClientKey}, "response_type": {"code"}, "scope": {strings.Join(clean, ",")}, "redirect_uri": {settings.RedirectURI}}
	if state != "" {
		query.Set("state", state)
	}
	return authorizationEndpoint + "?" + query.Encode(), nil
}

func validateOAuthSettings(value OAuthSettings) error {
	if strings.TrimSpace(value.ClientKey) != value.ClientKey || strings.TrimSpace(value.ClientSecret) != value.ClientSecret || value.ClientKey == "" || value.ClientSecret == "" || len(value.ClientKey) > 256 || len(value.ClientSecret) > 512 {
		return ErrOAuthInvalid
	}
	redirect, err := url.Parse(value.RedirectURI)
	if err != nil || redirect.Scheme != "https" || redirect.Host == "" || redirect.User != nil || redirect.Fragment != "" || redirect.RawQuery != "" {
		return ErrOAuthInvalid
	}
	return nil
}

func (p *SearchApp) ExchangeAuthorizationCode(ctx context.Context, settings OAuthSettings, code string) (OAuthGrant, error) {
	if validateOAuthSettings(settings) != nil || !safeOpaque(code, 1, 1024) {
		return OAuthGrant{}, ErrOAuthInvalid
	}
	form := url.Values{"client_key": {settings.ClientKey}, "client_secret": {settings.ClientSecret}, "code": {code}, "grant_type": {"authorization_code"}}
	var payload oauthTokenResponse
	if err := p.oauthJSON(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", "", &payload); err != nil {
		return OAuthGrant{}, err
	}
	if payload.Data == nil {
		return OAuthGrant{}, ErrOAuthUnavailable
	}
	value := payload.Data
	if value.ErrorCode != 0 {
		return OAuthGrant{}, oauthError(value.ErrorCode)
	}
	if !safeOpaque(value.AccessToken, 1, 1024) || !safeOpaque(value.RefreshToken, 1, 1024) || !safeOpaque(value.OpenID, 1, 512) || value.ExpiresIn <= 0 || value.RefreshExpiresIn <= 0 || value.ExpiresIn > 9223372036 || value.RefreshExpiresIn > 9223372036 {
		return OAuthGrant{}, ErrOAuthUnavailable
	}
	now := p.nowUTC()
	return OAuthGrant{AccessToken: value.AccessToken, RefreshToken: value.RefreshToken, OpenID: value.OpenID, Scope: strings.TrimSpace(value.Scope), ExpiresAt: now.Add(time.Duration(value.ExpiresIn) * time.Second), RefreshExpiresAt: now.Add(time.Duration(value.RefreshExpiresIn) * time.Second)}, nil
}

func (p *SearchApp) ReadAuthorizedProfile(ctx context.Context, grant OAuthGrant) (AccountProfile, error) {
	if !safeOpaque(grant.AccessToken, 1, 1024) || !safeOpaque(grant.OpenID, 1, 512) || !p.nowUTC().Before(grant.ExpiresAt) {
		return AccountProfile{}, ErrOAuthRejected
	}
	var payload oauthProfileResponse
	form := url.Values{"access_token": {grant.AccessToken}, "open_id": {grant.OpenID}}
	if err := p.oauthJSON(ctx, http.MethodPost, profileEndpoint, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", "", &payload); err != nil {
		return AccountProfile{}, err
	}
	if payload.ErrNo != 0 {
		return AccountProfile{}, oauthError(payload.ErrNo)
	}
	if payload.Data == nil {
		return AccountProfile{}, ErrOAuthUnavailable
	}
	if payload.Data.ErrorCode != "0" {
		return AccountProfile{}, ErrOAuthRejected
	}
	if payload.Data.OpenID != grant.OpenID {
		return AccountProfile{}, ErrOAuthRejected
	}
	return AccountProfile{OpenID: payload.Data.OpenID, Nickname: strings.TrimSpace(payload.Data.Nickname), Avatar: safeHTTPSURL(payload.Data.Avatar)}, nil
}

type oauthTokenResponse struct {
	Data *struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		OpenID           string `json:"open_id"`
		Scope            string `json:"scope"`
		ExpiresIn        int64  `json:"expires_in"`
		RefreshExpiresIn int64  `json:"refresh_expires_in"`
		ErrorCode        int64  `json:"error_code"`
	} `json:"data"`
}
type oauthProfileResponse struct {
	ErrNo int64 `json:"err_no"`
	Data  *struct {
		OpenID    string `json:"open_id"`
		Nickname  string `json:"nickname"`
		Avatar    string `json:"avatar"`
		ErrorCode string `json:"error_code"`
	} `json:"data"`
}

func (p *SearchApp) oauthJSON(ctx context.Context, method, endpoint string, body io.Reader, contentType, token string, target any) error {
	if p == nil || p.client == nil {
		return ErrOAuthUnavailable
	}
	callCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(callCtx, method, endpoint, body)
	if err != nil {
		return ErrOAuthUnavailable
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Agent-home-station/1.0 douyin-oauth")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		request.Header.Set("access-token", token)
	}
	response, err := p.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrOAuthUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ErrOAuthUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes || json.Unmarshal(raw, target) != nil {
		return ErrOAuthUnavailable
	}
	return nil
}
func oauthError(code int64) error {
	switch code {
	case 10007, 10008, 10010, 10013, 10014, 2190008:
		return ErrOAuthRejected
	default:
		return ErrOAuthUnavailable
	}
}
func safeOpaque(value string, min, max int) bool {
	return len(value) >= min && len(value) <= max && strings.TrimSpace(value) == value && !strings.ContainsAny(value, string([]byte{13, 10}))
}
func safeHTTPSURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	return parsed.String()
}
