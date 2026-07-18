package preview

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeClock is an injectable clock for the rate limiter and cache.
type fakeClock struct{ t time.Time }

func newFakeClock() *fakeClock               { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }
func (f *fakeClock) now() time.Time          { return f.t }
func (f *fakeClock) advance(d time.Duration) { f.t = f.t.Add(d) }

// newKagiTestClient wires a client at srv with a fake clock.
func newKagiTestClient(srv *httptest.Server, maxResults int) (*KagiClient, *fakeClock) {
	c := NewKagiClient("kagi-secret-key", maxResults)
	c.BaseURL = srv.URL
	c.HTTPClient = srv.Client()
	clk := newFakeClock()
	c.Now = clk.now
	return c, clk
}

const kagiV1Body = `{
  "meta": {"id": "x", "node": "us", "ms": 12},
  "data": {
    "search": [
      {"url": "https://one.example", "title": "One", "snippet": "first hit"},
      {"url": "https://two.example", "title": "Two", "snippet": ""},
      {"url": "", "title": "keyless row dropped", "snippet": ""},
      {"url": "https://three.example", "title": "Three", "snippet": "third"}
    ],
    "related_search": [{"query": "ignored"}]
  }
}`

func TestKagiSearchV1RequestAndParse(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/v1/search", r.URL.Path)
		require.Equal(t, "how to previews", r.URL.Query().Get("q"))
		require.Equal(t, "8", r.URL.Query().Get("limit"))
		require.Equal(t, "Bot kagi-secret-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(kagiV1Body))
	}))
	defer srv.Close()
	c, _ := newKagiTestClient(srv, 0) // non-positive cap = default 8

	results, cached, err := c.Search(context.Background(), "how to previews")
	require.NoError(t, err)
	require.False(t, cached)
	require.Equal(t, []WebResult{
		{Title: "One", URL: "https://one.example", Snippet: "first hit"},
		{Title: "Two", URL: "https://two.example"},
		{Title: "Three", URL: "https://three.example", Snippet: "third"},
	}, results)
	require.Equal(t, int64(1), hits.Load())

	// The exact same query is a cache hit: zero network, cached=true.
	again, cached, err := c.Search(context.Background(), "how to previews")
	require.NoError(t, err)
	require.True(t, cached)
	require.Equal(t, results, again)
	require.Equal(t, int64(1), hits.Load(), "a cache hit never dials")
}

func TestKagiLegacyV0ArrayShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"t":0,"url":"https://a.example","title":"A","snippet":"sa"},
			{"t":1,"url":"","title":"related, dropped"},
			{"t":0,"url":"https://b.example","title":"B"}
		]}`))
	}))
	defer srv.Close()
	c, _ := newKagiTestClient(srv, 8)

	results, cached, err := c.Search(context.Background(), "legacy")
	require.NoError(t, err)
	require.False(t, cached)
	require.Equal(t, []WebResult{
		{Title: "A", URL: "https://a.example", Snippet: "sa"},
		{Title: "B", URL: "https://b.example"},
	}, results, "only t==0 rows survive the legacy shape")
}

func TestKagiMaxResultsCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "2", r.URL.Query().Get("limit"), "the cap rides the limit param")
		_, _ = w.Write([]byte(kagiV1Body))
	}))
	defer srv.Close()
	c, _ := newKagiTestClient(srv, 2)

	results, _, err := c.Search(context.Background(), "capped")
	require.NoError(t, err)
	require.Len(t, results, 2, "client-side cap even when the server over-answers")
}

func TestKagiHTTPErrorsAreTerse(t *testing.T) {
	status := 401
	body := `{"error":[{"code":1,"msg":"Invalid token"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c, clk := newKagiTestClient(srv, 8)

	_, _, err := c.Search(context.Background(), "q1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "kagi: HTTP 401")
	require.Contains(t, err.Error(), "Invalid token")
	require.NotContains(t, err.Error(), "kagi-secret-key", "the key never leaks into errors")

	// v1 spells the message field "message"; both spellings parse.
	status, body = 400, `{"error":[{"code":2,"message":"Bad query"}]}`
	clk.advance(time.Second)
	_, _, err = c.Search(context.Background(), "q2")
	require.ErrorContains(t, err, "kagi: HTTP 400: Bad query")

	// A huge server message is capped, never quoted wholesale.
	status, body = 500, `{"error":[{"code":3,"msg":"`+strings.Repeat("x", 5000)+`"}]}`
	clk.advance(time.Second)
	_, _, err = c.Search(context.Background(), "q3")
	require.Error(t, err)
	require.Less(t, len(err.Error()), 300)

	// A non-JSON error body yields just the status line.
	status, body = 503, "<html>Service Unavailable</html>"
	clk.advance(time.Second)
	_, _, err = c.Search(context.Background(), "q4")
	require.EqualError(t, err, "kagi: HTTP 503")

	// Errors are never cached: the same query redials.
	status, body = 200, `{"data":{"search":[{"url":"https://ok.example","title":"OK"}]}}`
	clk.advance(time.Second)
	results, cached, err := c.Search(context.Background(), "q4")
	require.NoError(t, err)
	require.False(t, cached)
	require.Len(t, results, 1)
}

func TestKagiMalformedJSON(t *testing.T) {
	bodies := []string{"not json at all", `{"data": 42}`, `{"data":{"search": "nope"}}`, `{"data":[{"t":"zero"}]}`}
	for _, b := range bodies {
		body := b
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		c, _ := newKagiTestClient(srv, 8)
		_, _, err := c.Search(context.Background(), "q")
		require.Error(t, err, "body %q", body)
		require.Contains(t, err.Error(), "kagi: malformed response", "body %q", body)
		srv.Close()
	}
}

func TestKagiRateLimitBurstAndRefill(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"data":{"search":[]}}`))
	}))
	defer srv.Close()
	c, clk := newKagiTestClient(srv, 8)

	// The burst allows 3 back-to-back distinct queries...
	for _, q := range []string{"a", "b", "c"} {
		_, _, err := c.Search(context.Background(), q)
		require.NoError(t, err)
	}
	// ...the 4th fails fast without dialing.
	_, _, err := c.Search(context.Background(), "d")
	require.EqualError(t, err, "kagi: rate limited, retry shortly")
	require.Equal(t, int64(3), hits.Load(), "the limited call never reached the network")

	// One second refills one token.
	clk.advance(time.Second)
	_, _, err = c.Search(context.Background(), "d")
	require.NoError(t, err)
	require.Equal(t, int64(4), hits.Load())

	// A long idle refills to the burst cap, never beyond it.
	clk.advance(time.Hour)
	for _, q := range []string{"e", "f", "g"} {
		_, _, err := c.Search(context.Background(), q)
		require.NoError(t, err)
	}
	_, _, err = c.Search(context.Background(), "h")
	require.EqualError(t, err, "kagi: rate limited, retry shortly")

	// A cache hit spends no token: still limited for new queries, but
	// the cached one answers fine.
	_, cached, err := c.Search(context.Background(), "e")
	require.NoError(t, err)
	require.True(t, cached)
}

func TestKagiCacheTTLExpiry(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"data":{"search":[{"url":"https://x.example","title":"X"}]}}`))
	}))
	defer srv.Close()
	c, clk := newKagiTestClient(srv, 8)

	_, cached, err := c.Search(context.Background(), "ttl")
	require.NoError(t, err)
	require.False(t, cached)

	clk.advance(kagiCacheTTL - time.Second)
	_, cached, err = c.Search(context.Background(), "ttl")
	require.NoError(t, err)
	require.True(t, cached, "still fresh just inside the TTL")
	require.Equal(t, int64(1), hits.Load())

	clk.advance(2 * time.Second) // now past the TTL
	_, cached, err = c.Search(context.Background(), "ttl")
	require.NoError(t, err)
	require.False(t, cached, "expired entries refetch")
	require.Equal(t, int64(2), hits.Load())
}

func TestKagiCacheEvictsOldestPastCap(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"data":{"search":[]}}`))
	}))
	defer srv.Close()
	c, clk := newKagiTestClient(srv, 8)

	// Fill the cache to its cap plus one, spacing calls so the rate
	// limiter always has a token; the oldest entry falls out.
	for i := 0; i < kagiCacheMax+1; i++ {
		clk.advance(time.Second)
		_, _, err := c.Search(context.Background(), "query-"+strconv.Itoa(i))
		require.NoError(t, err)
	}
	require.Equal(t, int64(kagiCacheMax+1), hits.Load())
	require.Len(t, c.order, kagiCacheMax)
	require.Len(t, c.cache, kagiCacheMax)

	// The first query was evicted: asking again redials (and that
	// re-store evicts the next-oldest entry, query-1).
	clk.advance(time.Second)
	_, cached, err := c.Search(context.Background(), "query-0")
	require.NoError(t, err)
	require.False(t, cached)
	require.Equal(t, int64(kagiCacheMax+2), hits.Load())

	// The oldest SURVIVOR is still served from cache.
	_, cached, err = c.Search(context.Background(), "query-2")
	require.NoError(t, err)
	require.True(t, cached)
}

func TestCapString(t *testing.T) {
	require.Equal(t, "abc", capString("abc", 10))
	require.Equal(t, "ab", capString("abcd", 2))
	// A multi-byte rune is dropped whole, never split.
	s := "ab\u00e9cd" // the e-acute occupies bytes 2-3
	require.Equal(t, "ab", capString(s, 3))
	require.Equal(t, "ab\u00e9", capString(s, 4))
}
