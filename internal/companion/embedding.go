package companion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"modernc.org/sqlite"
)

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("memory_time", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		v, _ := args[0].(string)
		stamp, e := time.Parse(time.RFC3339Nano, v)
		if e != nil {
			return "", nil
		}
		return memoryTime(stamp), nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("memory_cosine", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		var a, b []float32
		asBytes := func(v driver.Value) []byte {
			switch x := v.(type) {
			case string:
				return []byte(x)
			case []byte:
				return x
			}
			return nil
		}
		if json.Unmarshal(asBytes(args[0]), &a) != nil || json.Unmarshal(asBytes(args[1]), &b) != nil {
			return float64(0), nil
		}
		if len(a) == 0 || len(a) != len(b) {
			return float64(0), nil
		}
		var dot, aa, bb float64
		for i, v := range a {
			x, y := float64(v), float64(b[i])
			dot += x * y
			aa += x * x
			bb += y * y
		}
		if aa == 0 || bb == 0 {
			return float64(0), nil
		}
		return dot / math.Sqrt(aa*bb), nil
	})
}

type EmbeddingConfig struct {
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	Revision   string `json:"revision,omitempty"`
	APIKey     string `json:"api_key,omitempty"`
	Dimensions int    `json:"dimensions,omitempty"`
}

func (c EmbeddingConfig) Configured() bool { return c.BaseURL != "" && c.Model != "" }
func (c EmbeddingConfig) Validate() error {
	if !c.Configured() {
		if c.BaseURL != "" || c.Model != "" || c.APIKey != "" {
			return errors.New("embedding endpoint and model must both be configured")
		}
		return nil
	}
	u, e := url.Parse(c.BaseURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || c.Dimensions < 0 {
		return errors.New("invalid embedding endpoint or dimensions")
	}
	return nil
}
func (c EmbeddingConfig) Space(dim int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", strings.TrimRight(c.BaseURL, "/"), c.Model, c.Revision, dim)))
	return hex.EncodeToString(h[:])
}

type EmbeddingResult struct {
	Vectors  [][]float32
	Space    string
	Tokens   int64
	Reported bool
}

func (c EmbeddingConfig) Embed(ctx context.Context, texts []string) (EmbeddingResult, error) {
	r := EmbeddingResult{}
	if e := c.Validate(); e != nil {
		return r, e
	}
	if !c.Configured() || len(texts) == 0 {
		return r, errors.New("embedding service not configured or empty input")
	}
	body := map[string]any{"model": c.Model, "input": texts, "encoding_format": "float"}
	if c.Dimensions > 0 {
		body["dimensions"] = c.Dimensions
	}
	b, _ := json.Marshal(body)
	endpoint := strings.TrimRight(c.BaseURL, "/")
	if !strings.HasSuffix(endpoint, "/embeddings") {
		endpoint += "/embeddings"
	}
	req, e := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(b))
	if e != nil {
		return r, errors.New("embedding request could not be prepared")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	response, e := (&http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if e != nil {
		return r, errors.New("embedding transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return r, fmt.Errorf("embedding provider HTTP %d", response.StatusCode)
	}
	b, e = io.ReadAll(io.LimitReader(response.Body, 32<<20+1))
	if e != nil || len(b) > 32<<20 {
		return r, errors.New("embedding response too large or unreadable")
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage *struct {
			Total int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(b, &out) != nil || len(out.Data) != len(texts) {
		return r, errors.New("embedding response shape invalid")
	}
	r.Vectors = make([][]float32, len(texts))
	dim := 0
	for _, v := range out.Data {
		if v.Index < 0 || v.Index >= len(texts) || r.Vectors[v.Index] != nil || len(v.Embedding) == 0 {
			return r, errors.New("embedding indices invalid")
		}
		if dim == 0 {
			dim = len(v.Embedding)
		}
		if len(v.Embedding) != dim || c.Dimensions > 0 && dim != c.Dimensions {
			return r, errors.New("embedding dimension mismatch")
		}
		var norm float64
		for _, x := range v.Embedding {
			if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
				return r, errors.New("embedding contains nonfinite value")
			}
			norm += float64(x) * float64(x)
		}
		if norm == 0 {
			return r, errors.New("embedding has zero norm")
		}
		r.Vectors[v.Index] = v.Embedding
	}
	r.Space = c.Space(dim)
	if out.Usage != nil && out.Usage.Total >= 0 {
		r.Tokens = out.Usage.Total
		r.Reported = true
	}
	return r, nil
}

func (s *Store) SaveEmbedding(ctx context.Context, m MemoryEntry, space string, vector []float32) error {
	if space == "" || len(vector) == 0 {
		return errors.New("embedding space/vector required")
	}
	for _, v := range vector {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return errors.New("invalid embedding vector")
		}
	}
	b, _ := json.Marshal(vector)
	res, e := s.db.ExecContext(ctx, `INSERT INTO embeddings SELECT id,?,version,? FROM memories WHERE id=? AND scope=? AND version=? AND status='active' ON CONFLICT(memory_id) DO UPDATE SET space=excluded.space,version=excluded.version,vector=excluded.vector`, space, string(b), m.ID, m.Scope, m.Version)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e == nil && n == 0 {
		return ErrMemoryConflict
	}
	return e
}
