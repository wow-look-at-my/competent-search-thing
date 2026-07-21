package icons

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// favPNG is a minimal PNG-magic payload (the sniffers only read the
// prefix; nothing here decodes images).
var favPNG = append(append([]byte(nil), pngMagic...), 0x01, 0x02, 0x03)

func favPNGURI(data []byte) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
}

func TestFaviconResolveViaOfflineLookup(t *testing.T) {
	var calls atomic.Int64
	svc := newFavTestService(t, func(page string, size int) ([]byte, string) {
		calls.Add(1)
		require.Equal(t, "https://a.example/page", page)
		require.Equal(t, 64, size)
		return favPNG, "https://a.example/favicon.ico"
	}, nil)

	key := "favicon:https://a.example/page"
	got := svc.Resolve([]string{key, key}, 64)
	require.Equal(t, map[string]string{key: favPNGURI(favPNG)}, got)
	require.EqualValues(t, 1, calls.Load(), "duplicate keys in one batch resolve once")

	got = svc.Resolve([]string{key}, 64)
	require.Equal(t, favPNGURI(favPNG), got[key])
	require.EqualValues(t, 1, calls.Load(), "the second batch is a positive-cache hit")
}

func TestFaviconNegativeCache(t *testing.T) {
	var calls atomic.Int64
	svc := newFavTestService(t, func(string, int) ([]byte, string) {
		calls.Add(1)
		return nil, ""
	}, nil)

	key := "favicon:https://miss.example/"
	require.Empty(t, svc.Resolve([]string{key}, 64))
	require.Empty(t, svc.Resolve([]string{key}, 64))
	require.EqualValues(t, 1, calls.Load(), "a miss is negative-cached; the lookup runs once")
}

func TestFaviconLookupBytesAreSniffedNotTrusted(t *testing.T) {
	svc := newFavTestService(t, func(string, int) ([]byte, string) {
		return []byte("<html>not an image</html>"), ""
	}, nil)
	require.Empty(t, svc.Resolve([]string{"favicon:https://junk.example/"}, 64),
		"non-image lookup bytes miss into the glyph")

	svg := []byte("\xEF\xBB\xBF  <?xml version=\"1.0\"?><svg xmlns=\"x\"/>")
	svc2 := newFavTestService(t, func(string, int) ([]byte, string) { return svg, "" }, nil)
	got := svc2.Resolve([]string{"favicon:https://svg.example/"}, 64)
	require.Equal(t,
		"data:image/svg+xml;base64,"+base64.StdEncoding.EncodeToString(svg),
		got["favicon:https://svg.example/"], "SVG payloads are detected by content")
}

func TestFaviconDataHintServesDirectly(t *testing.T) {
	svc := newFavTestService(t, func(string, int) ([]byte, string) {
		t.Fatal("the offline lookup must not run when a data: hint resolves")
		return nil, ""
	}, nil)
	// The hint DECLARES gif but carries PNG bytes: the sniffed type
	// wins on the served URI.
	svc.NoteFavicon("https://h.example/p",
		"data:image/gif;base64,"+base64.StdEncoding.EncodeToString(favPNG))
	got := svc.Resolve([]string{"favicon:https://h.example/p"}, 64)
	require.Equal(t, favPNGURI(favPNG), got["favicon:https://h.example/p"])
}

func TestFaviconHintInvalidatesNegativeMiss(t *testing.T) {
	svc := newFavTestService(t, nil, nil)
	key := "favicon:https://late.example/"
	require.Empty(t, svc.Resolve([]string{key}, 64), "nothing known yet: a miss")

	svc.NoteFavicon("https://late.example/",
		"data:image/png;base64,"+base64.StdEncoding.EncodeToString(favPNG))
	got := svc.Resolve([]string{key}, 64)
	require.Equal(t, favPNGURI(favPNG), got[key],
		"a fresh hint un-pins the negative-cached miss")
}

func TestNoteFaviconValidation(t *testing.T) {
	svc := newFavTestService(t, nil, nil)
	long := make([]byte, faviconMaxHintBytes+1)
	for i := range long {
		long[i] = 'a'
	}
	cases := []struct{ page, fav string }{
		{"ftp://nope.example/", "https://nope.example/f.ico"}, // non-http(s) page
		{"", "https://x.example/f.ico"},
		{"https://x.example/", "fake-favicon-uri:https://x.example/"}, // internal scheme
		{"https://x.example/", "javascript:alert(1)"},
		{"https://x.example/", "data:text/html;base64,PGI+"}, // non-image data
		{"https://x.example/", string(long)},                 // oversized hint
	}
	for _, c := range cases {
		svc.NoteFavicon(c.page, c.fav)
	}
	svc.mu.Lock()
	require.Zero(t, svc.favHints.ll.Len(), "invalid hints are never stored")
	svc.mu.Unlock()

	svc.NoteFavicon("https://ok.example/", "https://ok.example/favicon.ico")
	svc.mu.Lock()
	require.Equal(t, 1, svc.favHints.ll.Len())
	svc.mu.Unlock()
}

func TestFaviconMalformedKeysMiss(t *testing.T) {
	svc := newFavTestService(t, func(string, int) ([]byte, string) {
		t.Fatal("malformed keys must never reach the lookup")
		return nil, ""
	}, nil)
	got := svc.Resolve([]string{
		"favicon:",
		"favicon:notaurl",
		"favicon:ftp://f.example/",
		"favicon:https://", // no host
	}, 64)
	require.Empty(t, got)
}

func TestFaviconFetchFromHintURL(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(favPNG)
	}))
	t.Cleanup(srv.Close)

	svc := newFavTestService(t, nil, nil)
	svc.NoteFavicon("https://fetch.example/p", srv.URL+"/fav.png")
	key := "favicon:https://fetch.example/p"
	got := svc.Resolve([]string{key}, 64)
	require.Equal(t, favPNGURI(favPNG), got[key], "an http(s) hint is fetched once and served")
	svc.Resolve([]string{key}, 64)
	require.EqualValues(t, 1, hits.Load(), "the fetched icon is positively cached")
}

func TestFaviconFetchFromLookupIconURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/known.png", r.URL.Path)
		_, _ = w.Write(favPNG)
	}))
	t.Cleanup(srv.Close)

	svc := newFavTestService(t, func(string, int) ([]byte, string) {
		return nil, srv.URL + "/known.png" // the db knows the URL, holds no bytes
	}, nil)
	key := "favicon:https://known.example/"
	got := svc.Resolve([]string{key}, 64)
	require.Equal(t, favPNGURI(favPNG), got[key])
}

func TestFaviconFetchRejections(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/big.png", func(w http.ResponseWriter, r *http.Request) {
		big := make([]byte, faviconFetchMaxBytes+1)
		copy(big, pngMagic)
		_, _ = w.Write(big)
	})
	mux.HandleFunc("/junk", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>404-but-200 page</html>"))
	})
	mux.HandleFunc("/gone.png", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	for i, path := range []string{"/big.png", "/junk", "/gone.png"} {
		svc := newFavTestService(t, nil, nil)
		page := fmt.Sprintf("https://rej%d.example/", i)
		svc.NoteFavicon(page, srv.URL+path)
		require.Empty(t, svc.Resolve([]string{"favicon:" + page}, 64),
			"oversized, non-image, and non-200 fetches all miss (%s)", path)
	}
}

func TestFaviconFetchTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	svc := newFavTestService(t, nil, &Options{favTimeout: 50 * time.Millisecond})
	svc.NoteFavicon("https://slow.example/", srv.URL+"/slow.png")
	start := time.Now()
	require.Empty(t, svc.Resolve([]string{"favicon:https://slow.example/"}, 64))
	require.Less(t, time.Since(start), 2*time.Second, "the client timeout bounds the fetch")
}

func TestFaviconFetchDefaultClientBounds(t *testing.T) {
	c := newFaviconClient(nil, 0)
	require.Equal(t, faviconFetchTimeout, c.Timeout, "the production client carries the 3s hard timeout")
	// The redirect policy: up to faviconMaxRedirects hops, http(s) only.
	reqs := make([]*http.Request, faviconMaxRedirects)
	next, _ := http.NewRequest("GET", "https://a.example/", nil)
	require.NoError(t, c.CheckRedirect(next, reqs), "hops up to the cap are followed")
	require.Error(t, c.CheckRedirect(next, make([]*http.Request, faviconMaxRedirects+1)),
		"one hop past the cap is refused")
	ftp, _ := http.NewRequest("GET", "ftp://a.example/x", nil)
	require.Error(t, c.CheckRedirect(ftp, reqs[:1]), "non-http(s) redirect targets are refused")
}

func TestFaviconFetchRedirectCap(t *testing.T) {
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/hop/", func(w http.ResponseWriter, r *http.Request) {
		var n int
		_, _ = fmt.Sscanf(r.URL.Path, "/hop/%d", &n)
		if n <= 0 {
			_, _ = w.Write(favPNG)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("%s/hop/%d", srv.URL, n-1), http.StatusFound)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	okSvc := newFavTestService(t, nil, nil)
	okSvc.NoteFavicon("https://hops-ok.example/", srv.URL+fmt.Sprintf("/hop/%d", faviconMaxRedirects))
	got := okSvc.Resolve([]string{"favicon:https://hops-ok.example/"}, 64)
	require.Equal(t, favPNGURI(favPNG), got["favicon:https://hops-ok.example/"],
		"a chain within the redirect cap resolves")

	farSvc := newFavTestService(t, nil, nil)
	farSvc.NoteFavicon("https://hops-far.example/", srv.URL+fmt.Sprintf("/hop/%d", faviconMaxRedirects+1))
	require.Empty(t, farSvc.Resolve([]string{"favicon:https://hops-far.example/"}, 64),
		"one hop beyond the cap misses")
}

func TestFaviconSingleFlight(t *testing.T) {
	var hits atomic.Int64
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-gate
		_, _ = w.Write(favPNG)
	}))
	t.Cleanup(srv.Close)

	svc := newFavTestService(t, nil, nil)
	svc.NoteFavicon("https://flight.example/", srv.URL+"/f.png")
	key := "favicon:https://flight.example/"

	var wg sync.WaitGroup
	results := make([]map[string]string, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = svc.Resolve([]string{key}, 64)
		}(i)
	}
	// Let both goroutines reach the resolution (one fetching, one
	// waiting on the single-flight gate), then release the server.
	require.Eventually(t, func() bool { return hits.Load() == 1 }, 2*time.Second, 5*time.Millisecond)
	close(gate)
	wg.Wait()

	require.EqualValues(t, 1, hits.Load(), "concurrent resolutions share one fetch")
	for i := range results {
		require.Equal(t, favPNGURI(favPNG), results[i][key], "both callers get the resolved icon (%d)", i)
	}
}

func TestImageMIME(t *testing.T) {
	cases := []struct {
		data []byte
		want string
	}{
		{favPNG, "image/png"},
		{[]byte("GIF89a...."), "image/gif"},
		{[]byte{0xFF, 0xD8, 0xFF, 0xE0}, "image/jpeg"},
		{[]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), "image/webp"},
		{[]byte{0x00, 0x00, 0x01, 0x00, 0x01, 0x00}, "image/x-icon"},
		{[]byte("BM\x00\x00"), "image/bmp"},
		{[]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), "image/svg+xml"},
		{[]byte(`  <?xml version="1.0"?><!-- c --><svg/>`), "image/svg+xml"},
		{[]byte("plain text"), ""},
		{[]byte("<html><body>no svg here</body></html>"), ""},
		{nil, ""},
	}
	for i, c := range cases {
		require.Equal(t, c.want, imageMIME(c.data), "case %d", i)
	}
}

func TestLRUDeletePrefix(t *testing.T) {
	l := newLRU(8)
	l.put("favicon:https://a.example/|64", "x")
	l.put("favicon:https://a.example/|32", "y")
	l.put("favicon:https://a.example.evil/|64", "z")
	l.put("other", "keep")
	l.deletePrefix("favicon:https://a.example/|")
	_, ok := l.get("favicon:https://a.example/|64")
	require.False(t, ok)
	_, ok = l.get("favicon:https://a.example/|32")
	require.False(t, ok)
	_, ok = l.get("favicon:https://a.example.evil/|64")
	require.True(t, ok, "the trailing separator keeps sibling pages intact")
	_, ok = l.get("other")
	require.True(t, ok)
}

// newFavTestService builds a Service wired for favicon tests: quiet
// env/gsettings seams, the given offline lookup, plus optional extra
// option overrides (only the unexported fetch seams are consulted).
func newFavTestService(t *testing.T, lookup func(string, int) ([]byte, string), extra *Options) *Service {
	t.Helper()
	o := Options{
		Getenv:        func(string) string { return "" },
		RunGsettings:  errGsettings,
		Logf:          t.Logf,
		DataDirs:      []string{t.TempDir()},
		HomeIcons:     t.TempDir(),
		PixmapDirs:    []string{t.TempDir()},
		FaviconLookup: lookup,
	}
	if extra != nil {
		o.favTransport = extra.favTransport
		o.favTimeout = extra.favTimeout
		o.favMaxFetch = extra.favMaxFetch
	}
	return NewService(o)
}
