package app

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// aiProbeServer answers the chat-completions shape and records the
// last Authorization header seen (empty when none was sent -- the
// keyless local-server case).
func aiProbeServer(t *testing.T) (*httptest.Server, *atomic.Value) {
	t.Helper()
	var bearer atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer.Store(r.Header.Get("Authorization"))
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"model":"m-resolved","choices":[{"finish_reason":"stop","message":{"content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &bearer
}

func TestPreviewProviderTestUsesCandidateValues(t *testing.T) {
	srv, bearer := aiProbeServer(t)
	a, _ := newTestApp(t, nil, Options{})
	// An environment key exists, but the CANDIDATE key wins -- the
	// button tests what is in the editor, not what is saved.
	a.plat.getenv = func(key string) string {
		if key == envAIAPIKey {
			return "env-key"
		}
		return ""
	}
	res := a.TestPreviewProvider(PreviewProviderTest{
		Provider: "ai", APIKey: "candidate-key", BaseURL: srv.URL, Model: "m",
	})
	require.True(t, res.OK, res.Message)
	require.Equal(t, "ok: model m-resolved answered", res.Message)
	require.Equal(t, "Bearer candidate-key", bearer.Load())
}

func TestPreviewProviderTestEnvKeyFallback(t *testing.T) {
	srv, bearer := aiProbeServer(t)
	a, _ := newTestApp(t, nil, Options{})
	a.plat.getenv = func(key string) string {
		if key == envAIAPIKey {
			return "ai-env"
		}
		return ""
	}
	// An empty candidate key resolves through the env fallback,
	// exactly like the live dispatcher.
	res := a.TestPreviewProvider(PreviewProviderTest{Provider: "ai", BaseURL: srv.URL, Model: "m"})
	require.True(t, res.OK, res.Message)
	require.Equal(t, "Bearer ai-env", bearer.Load())
}

func TestPreviewProviderTestKeylessSendsNoAuth(t *testing.T) {
	// The local-server shape: no key anywhere, so no Authorization
	// header at all -- a header carrying an empty bearer would be
	// rejected by servers that parse it.
	srv, bearer := aiProbeServer(t)
	a, _ := newTestApp(t, nil, Options{})
	res := a.TestPreviewProvider(PreviewProviderTest{Provider: "ai", BaseURL: srv.URL, Model: "m"})
	require.True(t, res.OK, res.Message)
	require.Equal(t, "", bearer.Load())
}

func TestPreviewProviderTestEndpointIsRequired(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	res := a.TestPreviewProvider(PreviewProviderTest{Provider: "ai", Model: "m"})
	require.False(t, res.OK)
	require.Equal(t, "ai: no API base URL (preview.ai.baseUrl)", res.Message,
		"the endpoint is the user's to name -- no environment and no default fills it in")

	res = a.TestPreviewProvider(PreviewProviderTest{Provider: "ai", BaseURL: "http://localhost:1234/v1"})
	require.False(t, res.OK)
	require.Equal(t, "ai: no model (preview.ai.model)", res.Message)
}

func TestPreviewProviderTestKagi(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/search", r.URL.Path)
		_, _ = w.Write([]byte(`{"data":{"search":[{"url":"https://r.example","title":"R","snippet":"s"}]}}`))
	}))
	defer srv.Close()
	a, _ := newTestApp(t, nil, Options{})
	res := a.TestPreviewProvider(PreviewProviderTest{Provider: "kagi", APIKey: "k", BaseURL: srv.URL})
	require.True(t, res.OK)
	require.Equal(t, "ok: search answered with 1 result (1 credit spent)", res.Message)
}

func TestPreviewProviderTestHonestFailures(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	res := a.TestPreviewProvider(PreviewProviderTest{Provider: "watson"})
	require.False(t, res.OK)
	require.Equal(t, `test: unknown provider "watson"`, res.Message)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided"}}`))
	}))
	defer srv.Close()
	res = a.TestPreviewProvider(PreviewProviderTest{Provider: " AI ", APIKey: "bad", BaseURL: srv.URL, Model: "m"})
	require.False(t, res.OK)
	require.Equal(t, "ai: HTTP 401: Incorrect API key provided", res.Message,
		"the outcome carries the HTTP status and terse provider message; provider names case-fold")
	require.NotContains(t, res.Message, "bad")
}
