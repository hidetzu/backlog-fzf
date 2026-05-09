package backlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

// envAPIKey is the environment variable name used to read the API key.
const envAPIKey = "BACKLOG_API_KEY"

// defaultTimeout is the default HTTP request timeout.
const defaultTimeout = 30 * time.Second

// pageSize is the per-page item count for ListIssues paging (API max is 100).
const pageSize = 100

// Client is a Backlog REST API client.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client

	// rateLimit is the X-RateLimit-* state read from the most recent response.
	// nil means unknown. Before the next request, if Remaining <= 0 we wait
	// until ResetAt.
	rateLimit *rateLimitInfo

	// OnRetry is invoked before retrying after a 429 (a safety net).
	OnRetry func(attempt int, wait time.Duration, err error)
	// OnThrottle is invoked when we proactively wait for the rate-limit reset.
	OnThrottle func(remaining int, wait time.Duration)
}

// Rate-limit retry parameters.
const (
	maxRetries       = 5
	defaultRetryWait = time.Second
	maxRetryWait     = 60 * time.Second
)

// rateLimitInfo holds state parsed from X-RateLimit-* response headers.
type rateLimitInfo struct {
	limit     int
	remaining int
	resetAt   time.Time
}

// parseRateLimitHeaders packs Backlog's X-RateLimit-* headers into a struct.
// Returns nil if any required header is missing (a fallback for older APIs).
func parseRateLimitHeaders(h http.Header) *rateLimitInfo {
	rem := h.Get("X-RateLimit-Remaining")
	res := h.Get("X-RateLimit-Reset")
	if rem == "" || res == "" {
		return nil
	}
	remaining, err1 := strconv.Atoi(rem)
	resetUnix, err2 := strconv.ParseInt(res, 10, 64)
	if err1 != nil || err2 != nil {
		return nil
	}
	limit, _ := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	return &rateLimitInfo{
		limit:     limit,
		remaining: remaining,
		resetAt:   time.Unix(resetUnix, 0),
	}
}

// New builds a client from spaceDomain (e.g. "myteam.backlog.com") and the
// BACKLOG_API_KEY environment variable.
func New(spaceDomain string) (*Client, error) {
	if spaceDomain == "" {
		return nil, errors.New("backlog: spaceDomain is empty")
	}
	apiKey := os.Getenv(envAPIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("backlog: %s is not set", envAPIKey)
	}
	return &Client{
		baseURL: "https://" + spaceDomain + "/api/v2",
		apiKey:  apiKey,
		http:    &http.Client{Timeout: defaultTimeout},
	}, nil
}

// ListProjects returns the list of projects in the space (used for key↔id mapping).
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	var raw []struct {
		ID         int    `json:"id"`
		ProjectKey string `json:"projectKey"`
		Name       string `json:"name"`
	}
	if err := c.get(ctx, "/projects", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Project, len(raw))
	for i, p := range raw {
		out[i] = Project{ID: p.ID, ProjectKey: p.ProjectKey, Name: p.Name}
	}
	return out, nil
}

// ListIssuesEach invokes onPage for each page of issues updated since
// `since`. It returns nil after all pages have been fetched, or aborts as
// soon as onPage returns an error.
//
// Use this when you need a running count (the caller can accumulate
// len(page) inside onPage), e.g. for a progress display.
//
// Backlog's updatedSince is date-granular (yyyy-MM-dd), so the caller has
// to be aware of the date boundary. The watermark strategy in the syncer
// layer absorbs any boundary over-fetch.
func (c *Client) ListIssuesEach(ctx context.Context, since time.Time, projectKeys []string, onPage func([]Issue) error) error {
	projectIDs, err := c.resolveProjectIDs(ctx, projectKeys)
	if err != nil {
		return err
	}

	for offset := 0; ; offset += pageSize {
		params := url.Values{}
		params.Set("count", strconv.Itoa(pageSize))
		params.Set("offset", strconv.Itoa(offset))
		params.Set("sort", "updated")
		params.Set("order", "asc")
		if !since.IsZero() {
			params.Set("updatedSince", since.UTC().Format("2006-01-02"))
		}
		for _, id := range projectIDs {
			params.Add("projectId[]", strconv.Itoa(id))
		}

		page, err := c.listIssuesPage(ctx, params)
		if err != nil {
			return err
		}
		if err := onPage(page); err != nil {
			return err
		}
		if len(page) < pageSize {
			return nil
		}
	}
}

// ListIssues is a thin wrapper that concatenates every page returned by
// ListIssuesEach. For memory-conscious bulk fetches or progress reporting,
// call ListIssuesEach directly.
func (c *Client) ListIssues(ctx context.Context, since time.Time, projectKeys []string) ([]Issue, error) {
	var all []Issue
	err := c.ListIssuesEach(ctx, since, projectKeys, func(page []Issue) error {
		all = append(all, page...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return all, nil
}

// resolveProjectIDs translates projectKeys into IDs via ListProjects.
// Returns nil for an empty projectKeys (the API call omits projectId[],
// covering every project).
func (c *Client) resolveProjectIDs(ctx context.Context, projectKeys []string) ([]int, error) {
	if len(projectKeys) == 0 {
		return nil, nil
	}
	projects, err := c.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	keyToID := make(map[string]int, len(projects))
	for _, p := range projects {
		keyToID[p.ProjectKey] = p.ID
	}
	ids := make([]int, 0, len(projectKeys))
	for _, k := range projectKeys {
		id, ok := keyToID[k]
		if !ok {
			return nil, fmt.Errorf("backlog: unknown project key %q", k)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// listIssuesPage fetches a single page of issues and converts them to Issue.
func (c *Client) listIssuesPage(ctx context.Context, params url.Values) ([]Issue, error) {
	var raw []apiIssue
	if err := c.get(ctx, "/issues", params, &raw); err != nil {
		return nil, err
	}
	out := make([]Issue, len(raw))
	for i, r := range raw {
		out[i] = r.toIssue()
	}
	return out, nil
}

// apiIssue maps the JSON response of GET /issues. It's an internal type
// that decouples the public Issue from JSON tags and API-shape details.
//
// dueDate is decoded as *time.Time because the API returns either `null`
// or an RFC 3339 datetime (e.g. "2025-02-27T00:00:00Z" — Backlog stores
// it as a date but responds with a UTC 0:00 datetime). time.Time's
// UnmarshalJSON handles RFC 3339 natively.
type apiIssue struct {
	ID          int    `json:"id"`
	IssueKey    string `json:"issueKey"`
	ProjectID   int    `json:"projectId"`
	Summary     string `json:"summary"`
	Description string `json:"description"`
	Status      struct {
		Name string `json:"name"`
	} `json:"status"`
	Assignee *struct {
		Name string `json:"name"`
	} `json:"assignee"`
	DueDate *time.Time `json:"dueDate"`
	Created time.Time  `json:"created"`
	Updated time.Time  `json:"updated"`
}

func (a apiIssue) toIssue() Issue {
	out := Issue{
		ID:          a.ID,
		Key:         a.IssueKey,
		ProjectKey:  projectKeyFromIssueKey(a.IssueKey),
		Summary:     a.Summary,
		Description: a.Description,
		Status:      a.Status.Name,
		DueDate:     a.DueDate,
		CreatedAt:   a.Created,
		UpdatedAt:   a.Updated,
	}
	if a.Assignee != nil {
		out.Assignee = a.Assignee.Name
	}
	return out
}

// ListDocumentsEach invokes onPage for each page of documents tied to
// projectKeys. The page is sorted with order=desc, sort=updated and does
// not include bodies (fetch them separately via GetDocument).
//
// There is no updatedSince, so server-side filtering is not possible. The
// caller (the syncer) compares each doc's UpdatedAt to the watermark and
// breaks early once it reaches the older side, achieving incremental sync.
func (c *Client) ListDocumentsEach(ctx context.Context, projectKeys []string, onPage func([]Document) error) error {
	projectIDs, err := c.resolveProjectIDs(ctx, projectKeys)
	if err != nil {
		return err
	}
	idToKey, err := c.projectIDToKeyMap(ctx)
	if err != nil {
		return err
	}

	for offset := 0; ; offset += pageSize {
		params := url.Values{}
		params.Set("offset", strconv.Itoa(offset))
		params.Set("count", strconv.Itoa(pageSize))
		params.Set("sort", "updated")
		params.Set("order", "desc")
		for _, id := range projectIDs {
			params.Add("projectId[]", strconv.Itoa(id))
		}

		page, err := c.listDocumentsPage(ctx, params, idToKey)
		if err != nil {
			return err
		}
		if err := onPage(page); err != nil {
			return err
		}
		if len(page) < pageSize {
			return nil
		}
	}
}

// CountIssues returns the number of issues updated since `since`.
// `GET /api/v2/issues/count` accepts the same filters as list, so calling
// it right before list yields the denominator for that sync run.
func (c *Client) CountIssues(ctx context.Context, since time.Time, projectKeys []string) (int, error) {
	projectIDs, err := c.resolveProjectIDs(ctx, projectKeys)
	if err != nil {
		return 0, err
	}

	params := url.Values{}
	if !since.IsZero() {
		params.Set("updatedSince", since.UTC().Format("2006-01-02"))
	}
	for _, id := range projectIDs {
		params.Add("projectId[]", strconv.Itoa(id))
	}

	var resp struct {
		Count int `json:"count"`
	}
	if err := c.get(ctx, "/issues/count", params, &resp); err != nil {
		return 0, err
	}
	return resp.Count, nil
}

// CountDocuments returns the total document count for a project.
// `GET /api/v2/documents/count?projectIdOrKey=KEY`
func (c *Client) CountDocuments(ctx context.Context, projectKey string) (int, error) {
	var resp struct {
		Count int `json:"count"`
	}
	params := url.Values{}
	params.Set("projectIdOrKey", projectKey)
	if err := c.get(ctx, "/documents/count", params, &resp); err != nil {
		return 0, err
	}
	return resp.Count, nil
}

// CountAllDocuments returns the sum of document counts across projectKeys.
// If projectKeys is empty, it enumerates every project via ListProjects
// and sums those. Intended as the denominator for progress display.
func (c *Client) CountAllDocuments(ctx context.Context, projectKeys []string) (int, error) {
	keys := projectKeys
	if len(keys) == 0 {
		projects, err := c.ListProjects(ctx)
		if err != nil {
			return 0, err
		}
		keys = make([]string, len(projects))
		for i, p := range projects {
			keys[i] = p.ProjectKey
		}
	}
	total := 0
	for _, k := range keys {
		n, err := c.CountDocuments(ctx, k)
		if err != nil {
			return 0, fmt.Errorf("count documents for %s: %w", k, err)
		}
		total += n
	}
	return total, nil
}

// GetDocument fetches the full document, including its body.
func (c *Client) GetDocument(ctx context.Context, id string) (*Document, error) {
	var raw apiDocument
	if err := c.get(ctx, "/documents/"+id, nil, &raw); err != nil {
		return nil, err
	}
	doc := raw.toDocument()
	if doc.ProjectKey == "" {
		// The detail response only carries projectId, so look up the key separately.
		idToKey, err := c.projectIDToKeyMap(ctx)
		if err != nil {
			return nil, err
		}
		doc.ProjectKey = idToKey[raw.ProjectID]
	}
	return &doc, nil
}

// projectIDToKeyMap turns the ListProjects result into an id→key map.
func (c *Client) projectIDToKeyMap(ctx context.Context) (map[int]string, error) {
	projects, err := c.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int]string, len(projects))
	for _, p := range projects {
		out[p.ID] = p.ProjectKey
	}
	return out, nil
}

// listDocumentsPage converts a /documents page response into Document values.
// The list endpoint only returns projectId, so we fill project_key via idToKey.
func (c *Client) listDocumentsPage(ctx context.Context, params url.Values, idToKey map[int]string) ([]Document, error) {
	var raw []apiDocument
	if err := c.get(ctx, "/documents", params, &raw); err != nil {
		return nil, err
	}
	out := make([]Document, len(raw))
	for i, r := range raw {
		out[i] = r.toDocument()
		out[i].ProjectKey = idToKey[r.ProjectID]
	}
	return out, nil
}

// apiDocument maps the JSON response of GET /documents endpoints.
type apiDocument struct {
	ID        string    `json:"id"`
	ProjectID int       `json:"projectId"`
	Title     string    `json:"title"`
	Plain     string    `json:"plain"`
	StatusID  int       `json:"statusId"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
}

func (a apiDocument) toDocument() Document {
	return Document{
		ID:        a.ID,
		Title:     a.Title,
		Body:      a.Plain,
		StatusID:  a.StatusID,
		CreatedAt: a.Created,
		UpdatedAt: a.Updated,
	}
}

// projectKeyFromIssueKey extracts "PROJ" from "PROJ-123".
func projectKeyFromIssueKey(issueKey string) string {
	for i := len(issueKey) - 1; i >= 0; i-- {
		if issueKey[i] == '-' {
			return issueKey[:i]
		}
	}
	return ""
}

// get performs a GET request and JSON-decodes the response body into out.
//
// Rate-limit handling:
//   - Each response's X-RateLimit-* headers are stored on the client.
//   - Before the next request, if Remaining <= 0, wait until ResetAt + 1s
//     (proactive throttle).
//   - If a 429 still occurs, retry up to maxRetries times honoring
//     Retry-After (safety net).
func (c *Client) get(ctx context.Context, path string, params url.Values, out interface{}) error {
	if params == nil {
		params = url.Values{}
	}
	params.Set("apiKey", c.apiKey)
	fullURL := c.baseURL + path + "?" + params.Encode()

	for attempt := 0; ; attempt++ {
		// Proactive throttle: if the previous response had Remaining=0, wait for the window reset.
		if err := c.waitForRateWindow(ctx); err != nil {
			return err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
		if err != nil {
			return err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(resp.Body)
		// Rate-limit headers are refreshed regardless of status (they're sent on 429 as well).
		if rl := parseRateLimitHeaders(resp.Header); rl != nil {
			c.rateLimit = rl
		}
		resp.Body.Close()
		if err != nil {
			return err
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			if attempt >= maxRetries {
				return fmt.Errorf("backlog: GET %s: %s after %d retries", path, resp.Status, attempt)
			}
			wait := parseRetryAfter(resp.Header.Get("Retry-After"), attempt)
			if wait > maxRetryWait {
				wait = maxRetryWait
			}
			if c.OnRetry != nil {
				c.OnRetry(attempt+1, wait, fmt.Errorf("backlog: rate limited (429) on %s", path))
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
				continue
			}
		}

		if resp.StatusCode/100 != 2 {
			snippet := string(body)
			if len(snippet) > 200 {
				snippet = snippet[:200] + "…"
			}
			return fmt.Errorf("backlog: GET %s: %s: %s", path, resp.Status, snippet)
		}
		return json.Unmarshal(body, out)
	}
}

// waitForRateWindow inspects the cached rateLimit state and, when
// Remaining <= 0, sleeps until ResetAt + 1s (proactive throttle).
func (c *Client) waitForRateWindow(ctx context.Context) error {
	if c.rateLimit == nil || c.rateLimit.remaining > 0 {
		return nil
	}
	wait := time.Until(c.rateLimit.resetAt) + time.Second
	if wait <= 0 {
		return nil
	}
	if c.OnThrottle != nil {
		c.OnThrottle(c.rateLimit.remaining, wait)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// parseRetryAfter interprets the Retry-After header (seconds).
// Falls back to exponential backoff per attempt (1s, 2s, 4s, 8s, 16s) on parse failure.
func parseRetryAfter(s string, attempt int) time.Duration {
	if s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultRetryWait << attempt
}
