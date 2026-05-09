package backlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func newTestClient(serverURL string) *Client {
	return &Client{
		baseURL: serverURL,
		apiKey:  "test",
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

func ptrTime(year int, month time.Month, day int) *time.Time {
	t := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	return &t
}

func TestProjectKeyFromIssueKey(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"PROJ-123", "PROJ"},
		{"AUTH-9", "AUTH"},
		{"FOO-BAR-1", "FOO-BAR"},
		{"NOSEP", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := projectKeyFromIssueKey(tc.in); got != tc.want {
			t.Errorf("projectKeyFromIssueKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestListIssues_UpdatedSinceDateRounding(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	// 14:30:45 UTC timestamp → updatedSince=2026-05-08
	since := time.Date(2026, 5, 8, 14, 30, 45, 0, time.UTC)
	if _, err := c.ListIssues(context.Background(), since, nil); err != nil {
		t.Fatal(err)
	}
	if got := captured.URL.Query().Get("updatedSince"); got != "2026-05-08" {
		t.Errorf("updatedSince = %q, want 2026-05-08", got)
	}
}

func TestListIssues_UpdatedSinceUTCConversion(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	// JST 2026-05-09 05:00 = UTC 2026-05-08 20:00 → updatedSince=2026-05-08.
	jst, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	since := time.Date(2026, 5, 9, 5, 0, 0, 0, jst)
	if _, err := c.ListIssues(context.Background(), since, nil); err != nil {
		t.Fatal(err)
	}
	if got := captured.URL.Query().Get("updatedSince"); got != "2026-05-08" {
		t.Errorf("updatedSince (UTC conv) = %q, want 2026-05-08", got)
	}
}

func TestListIssues_NoUpdatedSinceWhenZero(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	if _, err := c.ListIssues(context.Background(), time.Time{}, nil); err != nil {
		t.Fatal(err)
	}
	if got := captured.URL.Query().Has("updatedSince"); got {
		t.Errorf("updatedSince should be absent when since.IsZero()")
	}
}

func TestListIssues_ProjectIDTranslation(t *testing.T) {
	var capturedIssues *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects":
			w.Write([]byte(`[
				{"id":42,"projectKey":"PROJ","name":"Project A"},
				{"id":99,"projectKey":"OTHER","name":"Other"}
			]`))
		case "/issues":
			capturedIssues = r
			w.Write([]byte("[]"))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	if _, err := c.ListIssues(context.Background(), time.Time{}, []string{"PROJ"}); err != nil {
		t.Fatal(err)
	}
	got := capturedIssues.URL.Query()["projectId[]"]
	if len(got) != 1 || got[0] != "42" {
		t.Errorf("projectId[] = %v, want [42]", got)
	}
}

func TestListIssues_UnknownProjectKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/projects" {
			w.Write([]byte(`[{"id":42,"projectKey":"PROJ","name":"P"}]`))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	if _, err := c.ListIssues(context.Background(), time.Time{}, []string{"UNKNOWN"}); err == nil {
		t.Fatal("expected error for unknown project key, got nil")
	}
}

func TestApiIssue_DueDateParsing(t *testing.T) {
	// The Backlog API returns dueDate as either null or an RFC 3339 datetime (UTC 0:00).
	cases := []struct {
		name    string
		jsonVal string
		wantDue *time.Time
	}{
		{"null", `null`, nil},
		{"datetime UTC midnight", `"2025-02-27T00:00:00Z"`, ptrTime(2025, 2, 27)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(`{
				"id":1,"issueKey":"P-1","summary":"x","description":"",
				"status":{"name":"Open"},
				"created":"2026-01-01T00:00:00Z",
				"updated":"2026-01-01T00:00:00Z",
				"dueDate":` + tc.jsonVal + `
			}`)
			var a apiIssue
			if err := json.Unmarshal(payload, &a); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := a.toIssue().DueDate
			switch {
			case tc.wantDue == nil && got != nil:
				t.Errorf("DueDate = %v, want nil", *got)
			case tc.wantDue != nil && got == nil:
				t.Errorf("DueDate = nil, want %v", *tc.wantDue)
			case tc.wantDue != nil && got != nil && !got.Equal(*tc.wantDue):
				t.Errorf("DueDate = %v, want %v", *got, *tc.wantDue)
			}
		})
	}
}

// An unexpected format must surface as an unmarshal error rather than
// silently becoming nil — debuggability first, so we catch Backlog API
// changes early.
func TestApiIssue_DueDateInvalidFormatErrors(t *testing.T) {
	payload := []byte(`{
		"id":1,"issueKey":"P-1","summary":"x","description":"",
		"status":{"name":"Open"},
		"created":"2026-01-01T00:00:00Z",
		"updated":"2026-01-01T00:00:00Z",
		"dueDate":"not-a-date"
	}`)
	var a apiIssue
	if err := json.Unmarshal(payload, &a); err == nil {
		t.Fatalf("expected unmarshal error for invalid dueDate format, got nil")
	}
}

func TestListIssues_PaginationContinuesUntilUnderfilledPage(t *testing.T) {
	// Expected behavior:
	//   offset=0   -> 100 items (== pageSize), keep going
	//   offset=100 -> 100 items (== pageSize), keep going
	//   offset=200 -> 30  items (< pageSize), stop
	// Assertions: offset sequence is [0, 100, 200] / total 230 items / offset=300 is never requested.
	var observedOffsets []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		observedOffsets = append(observedOffsets, offset)

		var n int
		switch offset {
		case 0, 100:
			n = 100
		case 200:
			n = 30
		default:
			t.Errorf("unexpected next page request: offset=%d", offset)
			n = 0
		}

		items := make([]map[string]interface{}, 0, n)
		for i := 0; i < n; i++ {
			items = append(items, map[string]interface{}{
				"id":          offset + i + 1,
				"issueKey":    "P-" + strconv.Itoa(offset+i+1),
				"summary":     "x",
				"description": "",
				"status":      map[string]string{"name": "Open"},
				"created":     "2026-01-01T00:00:00Z",
				"updated":     "2026-01-01T00:00:00Z",
			})
		}
		_ = json.NewEncoder(w).Encode(items)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	issues, err := c.ListIssues(context.Background(), time.Time{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := len(issues), 230; got != want {
		t.Errorf("total issues = %d, want %d", got, want)
	}
	wantOffsets := []int{0, 100, 200}
	if len(observedOffsets) != len(wantOffsets) {
		t.Fatalf("observed offsets = %v, want %v", observedOffsets, wantOffsets)
	}
	for i, want := range wantOffsets {
		if observedOffsets[i] != want {
			t.Errorf("offset[%d] = %d, want %d", i, observedOffsets[i], want)
		}
	}
}

// ListIssuesEach calls onPage for every fetched page (the foundation for progress reporting).
func TestListIssuesEach_CallbackPerPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		var n int
		switch offset {
		case 0:
			n = 100
		case 100:
			n = 30
		default:
			t.Errorf("unexpected offset %d", offset)
		}
		items := make([]map[string]interface{}, 0, n)
		for i := 0; i < n; i++ {
			items = append(items, map[string]interface{}{
				"id":          offset + i + 1,
				"issueKey":    fmt.Sprintf("P-%d", offset+i+1),
				"summary":     "x",
				"description": "",
				"status":      map[string]string{"name": "Open"},
				"created":     "2026-01-01T00:00:00Z",
				"updated":     "2026-01-01T00:00:00Z",
			})
		}
		_ = json.NewEncoder(w).Encode(items)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	var pageSizes []int
	err := c.ListIssuesEach(context.Background(), time.Time{}, nil, func(page []Issue) error {
		pageSizes = append(pageSizes, len(page))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pageSizes) != 2 || pageSizes[0] != 100 || pageSizes[1] != 30 {
		t.Errorf("page sizes = %v, want [100, 30]", pageSizes)
	}
}

// When onPage returns an error, abort immediately and skip any further page requests.
func TestListIssuesEach_StopsOnCallbackError(t *testing.T) {
	var serverCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCalls++
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		var n int
		if offset == 0 {
			n = 100
		} else {
			t.Errorf("should not request offset %d after callback error", offset)
			n = 0
		}
		items := make([]map[string]interface{}, n)
		for i := range items {
			items[i] = map[string]interface{}{
				"id":          offset + i + 1,
				"issueKey":    fmt.Sprintf("P-%d", offset+i+1),
				"summary":     "x",
				"description": "",
				"status":      map[string]string{"name": "Open"},
				"created":     "2026-01-01T00:00:00Z",
				"updated":     "2026-01-01T00:00:00Z",
			}
		}
		_ = json.NewEncoder(w).Encode(items)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	boom := errors.New("boom")
	calls := 0
	err := c.ListIssuesEach(context.Background(), time.Time{}, nil, func(page []Issue) error {
		calls++
		return boom
	})
	if !errors.Is(err, boom) {
		t.Errorf("expected boom, got %v", err)
	}
	if calls != 1 {
		t.Errorf("callback called %d times, want 1", calls)
	}
	if serverCalls != 1 {
		t.Errorf("server hit %d times, want 1 (no further fetch after callback error)", serverCalls)
	}
}

// ListDocumentsEach orders by order=desc, sort=updated and delivers each page via onPage.
// Also verifies that project_key is filled in via /projects.
func TestListDocumentsEach_PaginationAndProjectKeyResolution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects":
			_ = json.NewEncoder(w).Encode([]map[string]interface{}{
				{"id": 42, "projectKey": "PROJ", "name": "P"},
			})
		case "/documents":
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			if got := r.URL.Query().Get("sort"); got != "updated" {
				t.Errorf("sort = %q, want updated", got)
			}
			if got := r.URL.Query().Get("order"); got != "desc" {
				t.Errorf("order = %q, want desc", got)
			}
			var n int
			switch offset {
			case 0:
				n = 100
			case 100:
				n = 5
			default:
				t.Errorf("unexpected offset %d", offset)
			}
			items := make([]map[string]interface{}, 0, n)
			for i := 0; i < n; i++ {
				items = append(items, map[string]interface{}{
					"id":        fmt.Sprintf("doc%03d", offset+i),
					"projectId": 42,
					"title":     "T",
					"created":   "2026-01-01T00:00:00Z",
					"updated":   "2026-01-01T00:00:00Z",
				})
			}
			_ = json.NewEncoder(w).Encode(items)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	var pageSizes []int
	var firstDoc Document
	err := c.ListDocumentsEach(context.Background(), nil, func(page []Document) error {
		pageSizes = append(pageSizes, len(page))
		if firstDoc.ID == "" && len(page) > 0 {
			firstDoc = page[0]
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pageSizes) != 2 || pageSizes[0] != 100 || pageSizes[1] != 5 {
		t.Errorf("page sizes = %v, want [100, 5]", pageSizes)
	}
	if firstDoc.ProjectKey != "PROJ" {
		t.Errorf("project_key not resolved: got %q, want %q", firstDoc.ProjectKey, "PROJ")
	}
}

// When onPage returns an error, abort immediately and skip any further fetches.
func TestListDocumentsEach_StopsOnCallbackError(t *testing.T) {
	var documentsHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects":
			_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
		case "/documents":
			documentsHits++
			items := make([]map[string]interface{}, 100)
			for i := range items {
				items[i] = map[string]interface{}{
					"id":        fmt.Sprintf("d%d", i),
					"projectId": 1,
					"title":     "x",
					"created":   "2026-01-01T00:00:00Z",
					"updated":   "2026-01-01T00:00:00Z",
				}
			}
			_ = json.NewEncoder(w).Encode(items)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	boom := errors.New("boom")
	var calls int
	err := c.ListDocumentsEach(context.Background(), nil, func(page []Document) error {
		calls++
		return boom
	})
	if !errors.Is(err, boom) {
		t.Errorf("expected boom, got %v", err)
	}
	if calls != 1 {
		t.Errorf("callback called %d times, want 1", calls)
	}
	if documentsHits != 1 {
		t.Errorf("documents endpoint hit %d times, want 1", documentsHits)
	}
}

// GetDocument hits /documents/{id} and fills in body and project_key.
func TestGetDocument_FetchesBodyAndResolvesProjectKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects":
			_ = json.NewEncoder(w).Encode([]map[string]interface{}{
				{"id": 7, "projectKey": "PROJ", "name": "P"},
			})
		case "/documents/abc123":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":        "abc123",
				"projectId": 7,
				"title":     "ガイド",
				"plain":     "本文テキスト",
				"statusId":  1,
				"created":   "2026-01-01T00:00:00Z",
				"updated":   "2026-05-01T12:00:00Z",
			})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	doc, err := c.GetDocument(context.Background(), "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if doc.ID != "abc123" {
		t.Errorf("ID = %q", doc.ID)
	}
	if doc.Title != "ガイド" {
		t.Errorf("Title = %q", doc.Title)
	}
	if doc.Body != "本文テキスト" {
		t.Errorf("Body = %q", doc.Body)
	}
	if doc.ProjectKey != "PROJ" {
		t.Errorf("ProjectKey = %q, want PROJ", doc.ProjectKey)
	}
}

// Auto-retry on 429 → 200. Retry-After: 0 means retry immediately.
func TestClient_RetriesOn429(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"errors":[{"message":"Rate Limit"}]}`))
			return
		}
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	if _, err := c.ListProjects(context.Background()); err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2 (1 fail + 1 retry)", attempts)
	}
}

// After hitting maxRetries consecutive 429s, return an error.
func TestClient_GivesUpAfterMaxRetries(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.ListProjects(context.Background())
	if err == nil {
		t.Fatal("expected error after max retries")
	}
	if attempts != maxRetries+1 {
		t.Errorf("attempts = %d, want %d (initial + %d retries)", attempts, maxRetries+1, maxRetries)
	}
}

// The OnRetry callback fires for every retry.
func TestClient_OnRetryCallbackInvoked(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	var notifications []int
	c.OnRetry = func(attempt int, _ time.Duration, _ error) {
		notifications = append(notifications, attempt)
	}
	if _, err := c.ListProjects(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(notifications) != 2 || notifications[0] != 1 || notifications[1] != 2 {
		t.Errorf("notifications = %v, want [1 2]", notifications)
	}
}

// When the previous response reported Remaining=0, wait until ResetAt before the next request.
func TestClient_ProactiveThrottleOnExhaustedWindow(t *testing.T) {
	resetTime := time.Now().Add(2 * time.Second)
	var requestTimes []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestTimes = append(requestTimes, time.Now())
		// 1st call: Remaining=0 with the reset timestamp.
		// Later calls: Remaining=10 with a normal response.
		if len(requestTimes) == 1 {
			w.Header().Set("X-RateLimit-Limit", "150")
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetTime.Unix(), 10))
		} else {
			w.Header().Set("X-RateLimit-Limit", "150")
			w.Header().Set("X-RateLimit-Remaining", "10")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetTime.Unix(), 10))
		}
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	var throttled []time.Duration
	c.OnThrottle = func(_ int, wait time.Duration) {
		throttled = append(throttled, wait)
	}

	if _, err := c.ListProjects(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 1st call returned Remaining=0 → must wait before the next call.
	if _, err := c.ListProjects(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(throttled) != 1 {
		t.Fatalf("OnThrottle called %d times, want 1", len(throttled))
	}
	if len(requestTimes) != 2 {
		t.Fatalf("requests = %d, want 2", len(requestTimes))
	}
	gap := requestTimes[1].Sub(requestTimes[0])
	if gap < time.Second { // ~2s reset + 1s buffer, but checked loosely
		t.Errorf("gap between requests too short: %v (proactive throttle should pause)", gap)
	}
}

// When Remaining > 0, send the next request immediately without waiting.
func TestClient_NoThrottleWhenRemainingHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "150")
		w.Header().Set("X-RateLimit-Remaining", "100")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(60*time.Second).Unix(), 10))
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	var throttleCount int
	c.OnThrottle = func(_ int, _ time.Duration) {
		throttleCount++
	}

	for i := 0; i < 3; i++ {
		if _, err := c.ListProjects(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if throttleCount != 0 {
		t.Errorf("OnThrottle called %d times, want 0 (healthy remaining)", throttleCount)
	}
}

func TestParseRateLimitHeaders(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		wantNil bool
		wantRem int
	}{
		{"all set", map[string]string{
			"X-RateLimit-Limit":     "150",
			"X-RateLimit-Remaining": "42",
			"X-RateLimit-Reset":     "1700000000",
		}, false, 42},
		{"missing remaining", map[string]string{
			"X-RateLimit-Reset": "1700000000",
		}, true, 0},
		{"missing reset", map[string]string{
			"X-RateLimit-Remaining": "42",
		}, true, 0},
		{"invalid number", map[string]string{
			"X-RateLimit-Remaining": "abc",
			"X-RateLimit-Reset":     "1700000000",
		}, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tc.headers {
				h.Set(k, v)
			}
			got := parseRateLimitHeaders(h)
			if tc.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil || got.remaining != tc.wantRem {
				t.Errorf("got %+v, want remaining=%d", got, tc.wantRem)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		header  string
		attempt int
		want    time.Duration
	}{
		{"0", 0, 0},
		{"5", 0, 5 * time.Second},
		{"", 0, 1 * time.Second},
		{"", 1, 2 * time.Second},
		{"", 4, 16 * time.Second},
		{"invalid", 2, 4 * time.Second},
	}
	for _, tc := range cases {
		if got := parseRetryAfter(tc.header, tc.attempt); got != tc.want {
			t.Errorf("parseRetryAfter(%q, %d) = %v, want %v", tc.header, tc.attempt, got, tc.want)
		}
	}
}

func TestCountIssues_PassesUpdatedSinceAndProjectIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects":
			_, _ = w.Write([]byte(`[{"id":42,"projectKey":"PROJ","name":"P"}]`))
		case "/issues/count":
			if got := r.URL.Query().Get("updatedSince"); got != "2026-05-08" {
				t.Errorf("updatedSince = %q, want 2026-05-08", got)
			}
			if got := r.URL.Query()["projectId[]"]; len(got) != 1 || got[0] != "42" {
				t.Errorf("projectId[] = %v, want [42]", got)
			}
			_, _ = w.Write([]byte(`{"count": 7}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	since := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	got, err := c.CountIssues(context.Background(), since, []string{"PROJ"})
	if err != nil {
		t.Fatal(err)
	}
	if got != 7 {
		t.Errorf("got %d, want 7", got)
	}
}

func TestCountIssues_OmitsUpdatedSinceWhenZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/issues/count" {
			if r.URL.Query().Has("updatedSince") {
				t.Errorf("updatedSince should be absent when since.IsZero()")
			}
			_, _ = w.Write([]byte(`{"count": 100}`))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	got, err := c.CountIssues(context.Background(), time.Time{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != 100 {
		t.Errorf("got %d, want 100", got)
	}
}

func TestCountDocuments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/documents/count" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("projectIdOrKey"); got != "PROJ" {
			t.Errorf("projectIdOrKey = %q, want PROJ", got)
		}
		_, _ = w.Write([]byte(`{"count": 42}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	got, err := c.CountDocuments(context.Background(), "PROJ")
	if err != nil {
		t.Fatal(err)
	}
	if got != 42 {
		t.Errorf("got %d, want 42", got)
	}
}

func TestCountAllDocuments_SumsProvidedProjectKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/documents/count" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		switch r.URL.Query().Get("projectIdOrKey") {
		case "ALPHA":
			_, _ = w.Write([]byte(`{"count": 10}`))
		case "BETA":
			_, _ = w.Write([]byte(`{"count": 5}`))
		default:
			t.Errorf("unexpected project %s", r.URL.Query().Get("projectIdOrKey"))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	got, err := c.CountAllDocuments(context.Background(), []string{"ALPHA", "BETA"})
	if err != nil {
		t.Fatal(err)
	}
	if got != 15 {
		t.Errorf("got %d, want 15", got)
	}
}

func TestCountAllDocuments_EmptyProjectKeysFallsBackToListProjects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects":
			_, _ = w.Write([]byte(`[
				{"id":1,"projectKey":"A","name":"a"},
				{"id":2,"projectKey":"B","name":"b"}
			]`))
		case "/documents/count":
			switch r.URL.Query().Get("projectIdOrKey") {
			case "A":
				_, _ = w.Write([]byte(`{"count": 7}`))
			case "B":
				_, _ = w.Write([]byte(`{"count": 3}`))
			}
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	got, err := c.CountAllDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != 10 {
		t.Errorf("got %d, want 10", got)
	}
}

func TestApiIssue_AssigneeNullable(t *testing.T) {
	cases := []struct {
		name         string
		jsonVal      string
		wantAssignee string
	}{
		{"null assignee", `null`, ""},
		{"with assignee", `{"name":"alice"}`, "alice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(`{
				"id":1,"issueKey":"P-1","summary":"x","description":"",
				"status":{"name":"Open"},
				"created":"2026-01-01T00:00:00Z",
				"updated":"2026-01-01T00:00:00Z",
				"assignee":` + tc.jsonVal + `
			}`)
			var a apiIssue
			if err := json.Unmarshal(payload, &a); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := a.toIssue().Assignee; got != tc.wantAssignee {
				t.Errorf("Assignee = %q, want %q", got, tc.wantAssignee)
			}
		})
	}
}
