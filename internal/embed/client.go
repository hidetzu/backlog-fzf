// Package embed turns text into embedding vectors through an
// OpenAI-compatible HTTP API (`POST {base_url}/embeddings`), which is what
// Ollama, LM Studio, llama.cpp server and the hosted providers all speak.
// It also owns the text that gets embedded (see text.go): how an issue or
// a document is turned into one or more strings, and the content hash
// used to detect changes.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Embedder produces one vector per input text, in input order.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Client is an OpenAI-compatible embeddings client.
type Client struct {
	BaseURL    string // e.g. "http://localhost:11434/v1"
	Model      string
	Dimensions int    // 0 = omit the parameter (model native size)
	APIKey     string // optional bearer token
	// BatchSize caps the inputs per request (0 = DefaultBatchSize).
	BatchSize int
	// HTTP is the client used for requests (nil = a client with DefaultTimeout).
	HTTP *http.Client
	// Retries is the number of retries after a 429 / 5xx / transient
	// network error (0 = DefaultRetries, negative = never retry; interactive
	// callers use that so a down server fails fast). Backoff is RetryWait * attempt.
	Retries   int
	RetryWait time.Duration
}

// Client defaults.
const (
	DefaultBatchSize = 32
	DefaultTimeout   = 120 * time.Second
	DefaultRetries   = 3
	DefaultRetryWait = time.Second
)

// ErrUnreachable wraps connection-level failures so the CLI can add an
// install / start hint (errors.Is).
var ErrUnreachable = errors.New("embedding server unreachable")

type request struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type response struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Embed returns one vector per text. Inputs are sent in batches of
// BatchSize; a failure in any batch aborts the whole call (callers commit
// per batch themselves if they want partial progress).
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	batch := c.BatchSize
	if batch <= 0 {
		batch = DefaultBatchSize
	}
	out := make([][]float32, 0, len(texts))
	for i := 0; i < len(texts); i += batch {
		j := min(i+batch, len(texts))
		vecs, err := c.embedBatch(ctx, texts[i:j])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (c *Client) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	retries := c.Retries
	switch {
	case retries < 0:
		retries = 0
	case retries == 0:
		retries = DefaultRetries
	}
	wait := c.RetryWait
	if wait <= 0 {
		wait = DefaultRetryWait
	}
	var last error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait * time.Duration(attempt)):
			}
		}
		vecs, retry, err := c.post(ctx, texts)
		if err == nil {
			return vecs, nil
		}
		last = err
		if !retry || ctx.Err() != nil {
			break
		}
	}
	return nil, last
}

// post performs one request. retry=true marks errors worth retrying
// (network, 429, 5xx); everything else (4xx, malformed body) is final.
func (c *Client) post(ctx context.Context, texts []string) (vecs [][]float32, retry bool, err error) {
	body, err := json.Marshal(request{Model: c.Model, Input: texts, Dimensions: c.Dimensions})
	if err != nil {
		return nil, false, err
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: DefaultTimeout}
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		// Strip the *url.Error wrapper ("Post <url>: ...") so the message
		// names the server once and stays short enough for a list line.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, true, fmt.Errorf("%w: %s (%v)", ErrUnreachable, c.BaseURL, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, true, fmt.Errorf("embedding: read response: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("embedding: %s returned %s: %s", endpoint, resp.Status, excerpt(raw))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("embedding: %s returned %s: %s", endpoint, resp.Status, excerpt(raw))
	}
	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, false, fmt.Errorf("embedding: decode response from %s: %w", endpoint, err)
	}
	if r.Error != nil && r.Error.Message != "" {
		return nil, false, fmt.Errorf("embedding: %s: %s", endpoint, r.Error.Message)
	}
	if len(r.Data) != len(texts) {
		return nil, false, fmt.Errorf("embedding: %s returned %d vectors for %d inputs", endpoint, len(r.Data), len(texts))
	}
	sort.SliceStable(r.Data, func(i, j int) bool { return r.Data[i].Index < r.Data[j].Index })
	vecs = make([][]float32, len(r.Data))
	for i, d := range r.Data {
		if len(d.Embedding) == 0 {
			return nil, false, fmt.Errorf("embedding: %s returned an empty vector at index %d", endpoint, i)
		}
		if i > 0 && len(d.Embedding) != len(vecs[0]) {
			return nil, false, fmt.Errorf("embedding: %s returned vectors of different sizes (%d and %d)", endpoint, len(vecs[0]), len(d.Embedding))
		}
		vecs[i] = d.Embedding
	}
	return vecs, false, nil
}

// excerpt trims an error body to one short line for messages.
func excerpt(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "..."
	}
	return s
}
