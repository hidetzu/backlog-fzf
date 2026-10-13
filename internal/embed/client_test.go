package embed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type capturedReq struct {
	auth string
	body request
}

// newServer returns an OpenAI-compatible fake that answers each input with
// a vector [index, len(input)] and records requests. status overrides the
// HTTP status for the first `failFirst` calls.
func newServer(t *testing.T, failFirst int, status int) (*httptest.Server, *[]capturedReq, *int32) {
	t.Helper()
	var reqs []capturedReq
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/v1/embeddings" || r.Method != http.MethodPost {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		reqs = append(reqs, capturedReq{auth: r.Header.Get("Authorization"), body: req})
		if int(n) <= failFirst {
			http.Error(w, `{"error":{"message":"boom"}}`, status)
			return
		}
		var resp response
		// Return in reverse order to exercise index-based reordering.
		for i := len(req.Input) - 1; i >= 0; i-- {
			resp.Data = append(resp.Data, struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			}{Index: i, Embedding: []float32{float32(i), float32(len(req.Input))}})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs, &calls
}

func TestClient_BatchesAndReordersByIndex(t *testing.T) {
	srv, reqs, _ := newServer(t, 0, 0)
	c := &Client{BaseURL: srv.URL + "/v1/", Model: "m", Dimensions: 2, APIKey: "k", BatchSize: 2}
	texts := []string{"a", "b", "c", "d", "e"}
	vecs, err := c.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 5 {
		t.Fatalf("got %d vectors", len(vecs))
	}
	if len(*reqs) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(*reqs))
	}
	// Vector for the 3rd text ("c") is index 0 of the 2nd batch of size 2.
	if vecs[2][0] != 0 || vecs[2][1] != 2 {
		t.Errorf("vecs[2] = %v, want [0 2] (index reordering)", vecs[2])
	}
	if vecs[4][0] != 0 || vecs[4][1] != 1 {
		t.Errorf("vecs[4] = %v, want [0 1] (last batch of one)", vecs[4])
	}
	r := (*reqs)[0]
	if r.auth != "Bearer k" || r.body.Model != "m" || r.body.Dimensions != 2 {
		t.Errorf("request = %+v", r)
	}
}

func TestClient_OmitsDimensionsWhenZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		if _, ok := raw["dimensions"]; ok {
			t.Errorf("dimensions should be omitted, got %v", raw["dimensions"])
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("no Authorization header expected without APIKey")
		}
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,2,3]}]}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Model: "m"}
	vecs, err := c.Embed(context.Background(), []string{"x"})
	if err != nil || len(vecs) != 1 || len(vecs[0]) != 3 {
		t.Fatalf("vecs=%v err=%v", vecs, err)
	}
}

func TestClient_RetriesOn5xxThenSucceeds(t *testing.T) {
	srv, _, calls := newServer(t, 2, http.StatusBadGateway)
	c := &Client{BaseURL: srv.URL + "/v1", Model: "m", RetryWait: time.Millisecond}
	if _, err := c.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if *calls != 3 {
		t.Errorf("calls = %d, want 3", *calls)
	}
}

func TestClient_DoesNotRetryOn4xx(t *testing.T) {
	srv, _, calls := newServer(t, 5, http.StatusBadRequest)
	c := &Client{BaseURL: srv.URL + "/v1", Model: "m", RetryWait: time.Millisecond}
	_, err := c.Embed(context.Background(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("expected a 400 error, got %v", err)
	}
	if *calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 4xx)", *calls)
	}
}

func TestClient_GivesUpAfterRetries(t *testing.T) {
	srv, _, calls := newServer(t, 100, http.StatusServiceUnavailable)
	c := &Client{BaseURL: srv.URL + "/v1", Model: "m", Retries: 2, RetryWait: time.Millisecond}
	if _, err := c.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("expected an error")
	}
	if *calls != 3 {
		t.Errorf("calls = %d, want 3 (1 + 2 retries)", *calls)
	}
}

func TestClient_UnreachableIsWrapped(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more
	c := &Client{BaseURL: url, Model: "m", Retries: 1, RetryWait: time.Millisecond}
	_, err := c.Embed(context.Background(), []string{"x"})
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("expected ErrUnreachable, got %v", err)
	}
}

func TestClient_CountMismatchIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1]}]}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Model: "m"}
	_, err := c.Embed(context.Background(), []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "1 vectors for 2 inputs") {
		t.Fatalf("expected count mismatch error, got %v", err)
	}
}

func TestClient_EmptyInputIsNoop(t *testing.T) {
	c := &Client{BaseURL: "http://127.0.0.1:1", Model: "m"}
	vecs, err := c.Embed(context.Background(), nil)
	if err != nil || vecs != nil {
		t.Fatalf("vecs=%v err=%v", vecs, err)
	}
}

func TestClient_HonorsContextCancel(t *testing.T) {
	srv, _, _ := newServer(t, 100, http.StatusServiceUnavailable)
	c := &Client{BaseURL: srv.URL + "/v1", Model: "m", Retries: 10, RetryWait: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Embed(ctx, []string{"x"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestClient_NegativeRetriesNeverRetries(t *testing.T) {
	srv, _, calls := newServer(t, 100, http.StatusServiceUnavailable)
	c := &Client{BaseURL: srv.URL + "/v1", Model: "m", Retries: -1, RetryWait: time.Hour}
	if _, err := c.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("expected an error")
	}
	if *calls != 1 {
		t.Errorf("calls = %d, want 1 (Retries < 0 means never retry)", *calls)
	}
}
