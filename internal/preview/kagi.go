package preview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

// Kagi Search API client constants. The current (v1) API is a GET on
// /api/v1/search with the query in ?q= and a "Bot <token>" auth
// header -- verified against https://help.kagi.com/kagi/api/search.html
// 2026-07-18. (The task originally described the deprecated v0 API,
// whose flat "data" array this client still accepts on parse; see
// parseKagiData.)
const (
	kagiDefaultBaseURL = "https://kagi.com"
	kagiSearchPath     = "/api/v1/search"
	// kagiDefaultMaxResults mirrors config.DefaultPreviewKagiMax for
	// callers that pass a non-positive cap.
	kagiDefaultMaxResults = 8
	// kagiMaxBody caps how much of a response body is ever read.
	kagiMaxBody = 2 << 20
	// Client-side courtesy rate limit: a token bucket of kagiBurst
	// requests refilled at kagiRefillPerSec. Exceeding it fails fast
	// without touching the network.
	kagiBurst        = 3
	kagiRefillPerSec = 1
	// Result cache: exact-query keyed, kagiCacheTTL fresh, at most
	// kagiCacheMax entries (oldest inserted evicted first).
	kagiCacheTTL = 15 * time.Minute
	kagiCacheMax = 100
)

// providerErrMsgCap bounds how much of a provider's own error message
// is quoted in our terse errors (never the whole body).
const providerErrMsgCap = 200

// KagiClient searches the web through the Kagi Search API. The zero
// exported fields default sensibly; tests point BaseURL at an httptest
// server and Now at a fake clock. The API key is confined to the
// Authorization header -- it never appears in errors, logs, or
// payloads.
type KagiClient struct {
	// BaseURL is the API origin (default https://kagi.com).
	BaseURL string
	// HTTPClient performs the requests (default http.DefaultClient;
	// callers bound requests with their context).
	HTTPClient *http.Client
	// Now is the clock behind the rate limiter and the result cache
	// (default time.Now).
	Now func() time.Time

	key        string
	maxResults int

	mu         sync.Mutex
	tokens     float64
	lastRefill time.Time
	cache      map[string]kagiCacheEntry
	order      []string // cache keys, oldest inserted first
}

type kagiCacheEntry struct {
	results []WebResult
	at      time.Time
}

// NewKagiClient builds a client. maxResults caps one search
// (non-positive means the default 8).
func NewKagiClient(key string, maxResults int) *KagiClient {
	if maxResults <= 0 {
		maxResults = kagiDefaultMaxResults
	}
	return &KagiClient{key: key, maxResults: maxResults}
}

func (c *KagiClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// cached returns the fresh cache entry for query, if any. Expired
// entries are dropped on the way.
func (c *KagiClient) cached(query string) ([]WebResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[query]
	if !ok {
		return nil, false
	}
	if c.now().Sub(e.at) >= kagiCacheTTL {
		c.dropLocked(query)
		return nil, false
	}
	return e.results, true
}

// store caches query's results, evicting the oldest entries past the
// cap.
func (c *KagiClient) store(query string, results []WebResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cache == nil {
		c.cache = map[string]kagiCacheEntry{}
	}
	if _, ok := c.cache[query]; ok {
		c.dropLocked(query)
	}
	c.cache[query] = kagiCacheEntry{results: results, at: c.now()}
	c.order = append(c.order, query)
	for len(c.order) > kagiCacheMax {
		c.dropLocked(c.order[0])
	}
}

// dropLocked removes one cache entry. Callers hold mu.
func (c *KagiClient) dropLocked(query string) {
	delete(c.cache, query)
	for i, k := range c.order {
		if k == query {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

// take consumes one rate-limit token, refilling by elapsed wall time
// first. False means the caller must not touch the network.
func (c *KagiClient) take() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.lastRefill.IsZero() {
		c.tokens = kagiBurst
	} else {
		c.tokens += now.Sub(c.lastRefill).Seconds() * kagiRefillPerSec
		if c.tokens > kagiBurst {
			c.tokens = kagiBurst
		}
	}
	c.lastRefill = now
	if c.tokens < 1 {
		return false
	}
	c.tokens--
	return true
}

// Search runs one web search. The bool reports a cache hit (served
// with zero network IO); cache misses spend a rate-limit token before
// dialing and fail fast with "rate limited" when the bucket is empty.
func (c *KagiClient) Search(ctx context.Context, query string) ([]WebResult, bool, error) {
	if hit, ok := c.cached(query); ok {
		return hit, true, nil
	}
	if !c.take() {
		return nil, false, errors.New("kagi: rate limited, retry shortly")
	}
	base := c.BaseURL
	if base == "" {
		base = kagiDefaultBaseURL
	}
	q := url.Values{}
	q.Set("q", query)
	q.Set("limit", strconv.Itoa(c.maxResults))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+kagiSearchPath+"?"+q.Encode(), nil)
	if err != nil {
		return nil, false, fmt.Errorf("kagi: %w", err)
	}
	req.Header.Set("Authorization", "Bot "+c.key)
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("kagi: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, kagiMaxBody))
	if err != nil {
		return nil, false, fmt.Errorf("kagi: reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, false, kagiHTTPError(resp.StatusCode, body)
	}
	results, err := parseKagiData(body, c.maxResults)
	if err != nil {
		return nil, false, err
	}
	c.store(query, results)
	return results, false, nil
}

// kagiHTTPError builds the terse non-2xx error: "kagi: HTTP <code>"
// plus at most one short parsed error message -- never the raw body,
// never the key.
func kagiHTTPError(code int, body []byte) error {
	var envelope struct {
		Error []struct {
			Msg     string `json:"msg"`
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := ""
	if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Error) > 0 {
		msg = envelope.Error[0].Msg
		if msg == "" {
			msg = envelope.Error[0].Message
		}
	}
	if msg == "" {
		return fmt.Errorf("kagi: HTTP %d", code)
	}
	return fmt.Errorf("kagi: HTTP %d: %s", code, capString(msg, providerErrMsgCap))
}

// parseKagiData extracts the web results from a 2xx body. The v1 API
// answers {"data":{"search":[{url,title,snippet},...],...}}; the
// deprecated v0 API answered {"data":[{"t":0,url,title,snippet},...]}
// with t==0 marking a search result. Both shapes are accepted so a
// BaseURL pointed at a legacy-compatible server keeps working.
func parseKagiData(body []byte, maxResults int) ([]WebResult, error) {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("kagi: malformed response: %w", err)
	}
	type row struct {
		T       int    `json:"t"`
		URL     string `json:"url"`
		Title   string `json:"title"`
		Snippet string `json:"snippet"`
	}
	var rows []row
	switch firstJSONByte(envelope.Data) {
	case '{': // v1: named arrays; only data.search is a web result
		var data struct {
			Search []row `json:"search"`
		}
		if err := json.Unmarshal(envelope.Data, &data); err != nil {
			return nil, fmt.Errorf("kagi: malformed response: %w", err)
		}
		rows = data.Search
	case '[': // legacy v0: flat array discriminated by t (0 = search result)
		var data []row
		if err := json.Unmarshal(envelope.Data, &data); err != nil {
			return nil, fmt.Errorf("kagi: malformed response: %w", err)
		}
		for _, r := range data {
			if r.T == 0 {
				rows = append(rows, r)
			}
		}
	default:
		return nil, errors.New("kagi: malformed response: no data")
	}
	results := make([]WebResult, 0, len(rows))
	for _, r := range rows {
		if r.URL == "" {
			continue
		}
		results = append(results, WebResult{Title: r.Title, URL: r.URL, Snippet: r.Snippet})
		if len(results) >= maxResults {
			break
		}
	}
	return results, nil
}

// firstJSONByte returns the first non-whitespace byte of raw (0 when
// empty).
func firstJSONByte(raw []byte) byte {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		}
		return b
	}
	return 0
}

// capString truncates s to at most max bytes without splitting a
// UTF-8 sequence: a rune cut in half at the boundary is dropped
// whole (continuation bytes AND the dangling lead byte).
func capString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size != 1 {
			break // the trailing rune is complete
		}
		cut = cut[:len(cut)-1] // drop one byte of the broken tail
	}
	return cut
}
