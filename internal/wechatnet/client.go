// Package wechatnet is the bounded HTTP carrier for our protocol implementation.
// It never follows redirects, reads proxy credentials, or automatically retries.
package wechatnet

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"wechat-bot/internal/mmtls"
)

const MaxResponse = 2 << 20

type Client struct {
	Host string
	http *http.Client
}

func New(host string) (*Client, error) {
	if !ValidHost(host) {
		return nil, errors.New("unsupported WeChat short hostname")
	}
	return &Client{Host: host, http: &http.Client{Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 8 * time.Second}).DialContext, DisableKeepAlives: true, ResponseHeaderTimeout: 15 * time.Second}}}, nil
}
func ValidHost(host string) bool {
	if host != strings.ToLower(host) || len(host) > 253 || strings.ContainsAny(host, "/:@?#\\ \t\r\n") {
		return false
	}
	if !strings.HasSuffix(host, ".weixin.qq.com") && !strings.HasSuffix(host, ".wechat.com") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				return false
			}
		}
	}
	return true
}

type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("WeChat HTTP carrier status %d", e.Status) }

// PostRecord sends an already-encrypted MMTLS record stream, or an initial
// public-key hello. The body is never logged or persisted by this carrier.
func (c *Client) PostRecord(ctx context.Context, body []byte, now time.Time) ([]byte, error) {
	if c == nil || c.http == nil || !ValidHost(c.Host) || len(body) == 0 || len(body) > MaxResponse {
		return nil, errors.New("invalid carrier request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+c.Host+"/mmtls/"+strconv.FormatInt(now.Unix(), 10), bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("cannot construct carrier request")
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", "MicroMessenger Client")
	req.Close = true
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errors.New("WeChat carrier request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{Status: resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponse+1))
	if err != nil || len(b) > MaxResponse {
		return nil, errors.New("invalid carrier response size")
	}
	return b, nil
}

func (c *Client) Exchange(ctx context.Context, session mmtls.Session, path string, packet []byte) ([]byte, error) {
	now := time.Now()
	exchange, err := mmtls.NewExchange(session, c.Host, path, packet, now, rand.Reader)
	if err != nil {
		return nil, err
	}
	raw, err := c.PostRecord(ctx, exchange.RequestBody(), now)
	if err != nil {
		return nil, err
	}
	plain, err := exchange.DecodeResponse(raw)
	if err != nil {
		// Public record metadata only; no plaintext, ciphertext or key material.
		var metadata []string
		for rest := raw; len(rest) >= 5; {
			n := int(binary.BigEndian.Uint16(rest[3:5]))
			if n > len(rest)-5 {
				metadata = append(metadata, "truncated")
				break
			}
			metadata = append(metadata, fmt.Sprintf("%02x:%d", rest[0], n))
			rest = rest[5+n:]
		}
		return nil, fmt.Errorf("MMTLS response records [%s]: %w", strings.Join(metadata, ","), err)
	}
	return plain, nil
}

func (c *Client) Handshake(ctx context.Context) (mmtls.Session, error) {
	now := time.Now()
	h, err := mmtls.NewHandshake(now, rand.Reader)
	if err != nil {
		return mmtls.Session{}, err
	}
	raw, err := c.PostRecord(ctx, h.RequestBody(), now)
	if err != nil {
		return mmtls.Session{}, err
	}
	return h.Complete(raw)
}
