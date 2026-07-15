package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// httpRedirectLimit is the maximum number of redirect hops an HTTP
// plugin response may take; every hop must stay on http/https.
const httpRedirectLimit = 3

// newHTTPClient builds the ONE http.Client a Registry shares across
// all its HTTP plugins, tuned for connection reuse against a handful
// of local endpoints. Per-request timeouts come from the request
// context, not the client.
func newHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
		},
		CheckRedirect: checkRedirect,
	}
}

// checkRedirect enforces the redirect policy: at most
// httpRedirectLimit hops, each to an http(s) URL.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > httpRedirectLimit {
		return fmt.Errorf("stopped after %d redirects", httpRedirectLimit)
	}
	if s := strings.ToLower(req.URL.Scheme); s != "http" && s != "https" {
		return fmt.Errorf("redirect to non-http(s) URL %q refused", req.URL)
	}
	return nil
}

// httpTransport POSTs the request JSON to the manifest URL and decodes
// the JSON response. client is the Registry-shared client.
type httpTransport struct {
	m      *Manifest
	client *http.Client
}

func (t *httpTransport) roundTrip(ctx context.Context, req Request) (*Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.m.HTTP.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range t.m.HTTP.Headers {
		httpReq.Header.Set(k, v)
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("http status %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	return decodeResponse(data, "")
}
