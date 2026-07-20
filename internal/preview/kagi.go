package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Kagi Search API client. Coded against the published OpenAPI spec
// (https://kagi.redocly.app/_spec/openapi.yaml, fetched 2026-07-20):
// the one production server is https://kagi.com/api/v1 (servers[0]),
// searches are POST /search with a JSON body ({"query", "limit"}, per
// the request schema), and auth is HTTP Bearer
// (components.securitySchemes.kagi: type http, scheme bearer; keys
// from https://kagi.com/api/keys). The pre-fix client did
// GET {base}/api/v1/search?q=&limit= with an "Authorization: Bot"
// header -- v0-era conventions the current API does not serve: the
// /search route exists only for POST, so every GET earned a plain 404
// (verified live 2026-07-20: GET = 404, POST = 401 without a key).
const (
	// kagiDefaultBaseURL is the spec's production server URL. A
	// configured preview.kagi.baseUrl REPLACES it verbatim (any
	// /api/v1-style prefix included) -- the client appends only
	// kagiSearchPath, so requests go to <base>/search.
	kagiDefaultBaseURL = "https://kagi.com/api/v1"
	kagiSearchPath     = "/search"
	// kagiDefaultMaxResults mirrors config.DefaultPreviewKagiMax for
	// callers that pass a non-positive cap.
	kagiDefaultMaxResults = 8
	// kagiMaxBody caps how much of a response body is ever read.
	kagiMaxBody = 2 << 20
	// kagiTraceCap bounds the logged trace id (the observed ids are
	// 32 hex chars; never quote unbounded server bytes).
	kagiTraceCap = 64
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
	// BaseURL is the API base (default https://kagi.com/api/v1, the
	// spec's server URL). A non-empty value replaces that WHOLE base;
	// the client appends only "/search".
	BaseURL string
	// HTTPClient performs the requests (default http.DefaultClient;
	// callers bound requests with their context).
	HTTPClient *http.Client
	// Now is the clock behind the rate limiter and the result cache
	// (default time.Now).
	Now func() time.Time
	// Logf receives ONE line per failed (non-2xx) request carrying
	// the Kagi trace id when the response supplied one (the
	// X-Kagi-Trace header, else meta.trace -- the id Kagi support
	// asks for). nil = silent. Never receives the key, the base URL,
	// or the query.
	Logf func(format string, v ...any)

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

// kagiSearchRequest is the POST /search JSON body -- the subset of
// the spec's request schema this client uses. query is the one
// required field; limit ("Maximum number of results to return", spec
// range 1..1024) carries the configured cap.
type kagiSearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
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

// logf logs through Logf when set.
func (c *KagiClient) logf(format string, v ...any) {
	if c.Logf != nil {
		c.Logf(format, v...)
	}
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
	// A two-field struct of string+int cannot fail to marshal.
	reqBody, _ := json.Marshal(kagiSearchRequest{Query: query, Limit: c.maxResults})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+kagiSearchPath, bytes.NewReader(reqBody))
	if err != nil {
		return nil, false, fmt.Errorf("kagi: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
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
		// One log line per failure carrying the trace id, so users
		// can quote it to Kagi support (the docs' guidance); the
		// returned error stays terse for the pane.
		if trace := kagiTrace(resp.Header, body); trace != "" {
			c.logf("kagi: HTTP %d (trace %s -- quote this id to Kagi support)", resp.StatusCode, trace)
		} else {
			c.logf("kagi: HTTP %d (no trace id in the response)", resp.StatusCode)
		}
		return nil, false, kagiHTTPError(resp.StatusCode, body)
	}
	results, err := parseKagiData(body, c.maxResults)
	if err != nil {
		return nil, false, err
	}
	c.store(query, results)
	return results, false, nil
}

// kagiTrace extracts the request trace id from a response: the
// X-Kagi-Trace header, else the body envelope's meta.trace (both
// documented under the spec's support guidance). Empty when the
// response carries neither.
func kagiTrace(h http.Header, body []byte) string {
	if t := strings.TrimSpace(h.Get("X-Kagi-Trace")); t != "" {
		return capString(t, kagiTraceCap)
	}
	var envelope struct {
		Meta struct {
			Trace string `json:"trace"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	return capString(strings.TrimSpace(envelope.Meta.Trace), kagiTraceCap)
}

// kagiHTTPError builds the terse non-2xx error: "kagi: HTTP <code>"
// plus at most one short parsed error message -- never the raw body,
// never the key. The spec's errorEnvelope carries error[]{code, url,
// message, location} with "message" as the human-readable field; the
// legacy "msg" spelling is still read as a fallback.
func kagiHTTPError(code int, body []byte) error {
	var envelope struct {
		Error []struct {
			Message string `json:"message"`
			Msg     string `json:"msg"`
		} `json:"error"`
	}
	msg := ""
	if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Error) > 0 {
		msg = envelope.Error[0].Message
		if msg == "" {
			msg = envelope.Error[0].Msg
		}
	}
	if msg == "" {
		return fmt.Errorf("kagi: HTTP %d", code)
	}
	return fmt.Errorf("kagi: HTTP %d: %s", code, capString(msg, providerErrMsgCap))
}

// parseKagiData extracts the web results from a 2xx body. The v1 API
// answers {"meta":{...},"data":{"search":[{url,title,snippet},...],
// ...}} (only data.search rows are web results; image/news/etc. ride
// sibling arrays); the long-dead v0 API answered
// {"data":[{"t":0,url,title,snippet},...]} with t==0 marking a search
// result. Both shapes are accepted so a BaseURL pointed at a
// legacy-compatible server keeps working.
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
