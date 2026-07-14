package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// httpManifest builds a ready-to-use HTTP manifest for url.
func httpManifest(url string, headers map[string]string) *Manifest {
	return &Manifest{
		ID:   "web",
		Name: "Web",
		Type: TypeHTTP,
		HTTP: &HTTPSpec{URL: url, Headers: headers},
	}
}

// newTestHTTPTransport wires a transport to a fresh shared client and
// cleans the client up with the test.
func newTestHTTPTransport(t *testing.T, m *Manifest) *httpTransport {
	t.Helper()
	client := newHTTPClient()
	t.Cleanup(client.CloseIdleConnections)
	return &httpTransport{m: m, client: client}
}

func TestHTTPTransportRoundTrip(t *testing.T) {
	var gotMethod, gotContentType, gotAPIKey string
	var gotBody Request
	var decodeErr error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		gotAPIKey = r.Header.Get("X-Api-Key")
		decodeErr = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"v":1,"results":[{"title":"#aabbcc","badge":"COLOR"}]}`)
	}))
	defer srv.Close()

	tr := newTestHTTPTransport(t, httpManifest(srv.URL, map[string]string{"X-Api-Key": "sekrit"}))
	req := testRequest()
	req.Context = &RequestContext{FocusedApp: &AppInfo{Name: "firefox", PID: 42}}
	resp, err := tr.roundTrip(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	require.Equal(t, "#aabbcc", resp.Results[0].Title)

	require.NoError(t, decodeErr)
	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "application/json", gotContentType)
	require.Equal(t, "sekrit", gotAPIKey)
	require.Equal(t, req, gotBody, "request survives the wire byte-for-byte")
	require.Equal(t, json.RawMessage("{}"), gotBody.Settings)
	require.NotNil(t, gotBody.Context)
	require.Equal(t, "firefox", gotBody.Context.FocusedApp.Name)
}

func TestHTTPTransportNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "kaboom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	tr := newTestHTTPTransport(t, httpManifest(srv.URL, nil))
	_, err := tr.roundTrip(context.Background(), testRequest())
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
}

func TestHTTPTransportContextTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done(): // client gave up: unblock the server
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	tr := newTestHTTPTransport(t, httpManifest(srv.URL, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := tr.roundTrip(ctx, testRequest())
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 2*time.Second)
}

func TestHTTPTransportInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>oops</html>")
	}))
	defer srv.Close()
	tr := newTestHTTPTransport(t, httpManifest(srv.URL, nil))
	_, err := tr.roundTrip(context.Background(), testRequest())
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid response JSON")
}

func TestHTTPTransportVersionRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"v":3,"results":[]}`)
	}))
	defer srv.Close()
	tr := newTestHTTPTransport(t, httpManifest(srv.URL, nil))
	_, err := tr.roundTrip(context.Background(), testRequest())
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported response version 3")
}

// redirectChain serves /r0 -> /r1 -> ... -> /r<n-1> -> /final.
func redirectChain(n int) http.Handler {
	mux := http.NewServeMux()
	for i := 0; i < n; i++ {
		next := fmt.Sprintf("/r%d", i+1)
		if i == n-1 {
			next = "/final"
		}
		mux.HandleFunc(fmt.Sprintf("/r%d", i), func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, next, http.StatusFound)
		})
	}
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[{"title":"landed"}]}`)
	})
	return mux
}

func TestHTTPTransportRedirects(t *testing.T) {
	t.Run("three hops allowed", func(t *testing.T) {
		srv := httptest.NewServer(redirectChain(3))
		defer srv.Close()
		tr := newTestHTTPTransport(t, httpManifest(srv.URL+"/r0", nil))
		resp, err := tr.roundTrip(context.Background(), testRequest())
		require.NoError(t, err)
		require.Len(t, resp.Results, 1)
		require.Equal(t, "landed", resp.Results[0].Title)
	})
	t.Run("four hops rejected", func(t *testing.T) {
		srv := httptest.NewServer(redirectChain(4))
		defer srv.Close()
		tr := newTestHTTPTransport(t, httpManifest(srv.URL+"/r0", nil))
		_, err := tr.roundTrip(context.Background(), testRequest())
		require.Error(t, err)
		require.Contains(t, err.Error(), "stopped after 3 redirects")
	})
	t.Run("non-http scheme rejected", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "ftp://example.invalid/x")
			w.WriteHeader(http.StatusFound)
		}))
		defer srv.Close()
		tr := newTestHTTPTransport(t, httpManifest(srv.URL, nil))
		_, err := tr.roundTrip(context.Background(), testRequest())
		require.Error(t, err)
		require.Contains(t, err.Error(), "non-http(s)")
	})
}

func TestHTTPTransportBodyCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("x", maxResponseBytes+100))
	}))
	defer srv.Close()
	tr := newTestHTTPTransport(t, httpManifest(srv.URL, nil))
	_, err := tr.roundTrip(context.Background(), testRequest())
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds")
}

func TestHTTPTransportConnectionReuse(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[{"title":"hi"}]}`)
	}))
	var conns atomic.Int32
	srv.Config.ConnState = func(c net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	tr := newTestHTTPTransport(t, httpManifest(srv.URL, nil))
	for i := 0; i < 2; i++ {
		resp, err := tr.roundTrip(context.Background(), testRequest())
		require.NoError(t, err)
		require.Len(t, resp.Results, 1)
	}
	require.Equal(t, int32(1), conns.Load(), "sequential queries share one connection")
}
