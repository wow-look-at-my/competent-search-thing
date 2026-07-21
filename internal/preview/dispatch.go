package preview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Per-request hard timeouts. Every provider runs under one of these,
// derived from the request's cancellable context, so a hung filesystem
// can delay one preview but never wedge the pane.
const (
	metaTimeout  = 2 * time.Second  // metadata, directory and text previews
	imageTimeout = 4 * time.Second  // image decode + downscale
	webTimeout   = 10 * time.Second // Kagi web search (network)
	aiTimeout    = 90 * time.Second // AI answer (network, slow models)
)

// headSniffBytes is how much of a file the binary sniff reads.
const headSniffBytes = 8 * 1024

// Options configures a Dispatcher.
type Options struct {
	// TextMaxKB caps text reads in KiB (config preview.textMaxKB).
	TextMaxKB int
	// ImageMaxEdge caps a thumbnail's longest edge in pixels (config
	// preview.imageMaxEdge).
	ImageMaxEdge int
	// DirMaxEntries caps directory listings (config
	// preview.dirMaxEntries).
	DirMaxEntries int
	// Emit delivers payloads to the frontend. It is called from
	// request goroutines and MUST be goroutine-safe; it is never
	// called for a request whose context was already cancelled.
	Emit func(Payload)

	// KagiAPIKey enables the explicit web-search provider; empty
	// leaves it unconfigured (FetchWeb answers with a no-key error).
	// The key is confined to the provider client -- never logged or
	// emitted.
	KagiAPIKey string
	// KagiBaseURL overrides the web-search API base; empty = the
	// official endpoint (the spec's server URL,
	// https://kagi.com/api/v1). A non-empty value replaces that WHOLE
	// base verbatim -- the client appends only "/search" -- so a
	// compatible server is named by its full base, /api/v1-style
	// prefix included. Normalized by normalizeBaseURL: one trailing
	// "/" is trimmed, and an invalid value (not http(s) with a host)
	// leaves the provider unavailable -- FetchWeb answers with a
	// terse invalid-baseUrl error, never a broken client. The value
	// itself is never logged or emitted (it may carry userinfo).
	KagiBaseURL string
	// KagiMaxResults caps one web search (non-positive = 8).
	KagiMaxResults int
	// AIProvider picks which client answers FetchAI: "openai" (the
	// default, incl. ""), "anthropic", or "custom" (see
	// aiprovider.go). Only the selected provider's option group below
	// is consulted.
	AIProvider string
	// OpenAIAPIKey enables the OpenAI answer provider; empty leaves
	// it unconfigured (FetchAI answers with a no-key error).
	// Never logged or emitted.
	OpenAIAPIKey string
	// OpenAIBaseURL overrides the answer API origin; empty = the
	// official endpoint. Same normalization and invalid-value
	// handling as KagiBaseURL (the app layer resolves the config
	// value / OPENAI_BASE_URL before it lands here).
	OpenAIBaseURL string
	// OpenAIModel names the answering model.
	OpenAIModel string
	// OpenAIMaxOutputTokens caps one answer.
	OpenAIMaxOutputTokens int
	// AnthropicAPIKey enables the Anthropic answer provider (the app
	// layer resolves config / ANTHROPIC_API_KEY first). Never logged
	// or emitted.
	AnthropicAPIKey string
	// AnthropicBaseURL overrides the Anthropic API origin; empty =
	// the official endpoint (the app layer resolves config /
	// ANTHROPIC_BASE_URL first). Same normalization rules.
	AnthropicBaseURL string
	// AnthropicModel names the answering model.
	AnthropicModel string
	// AnthropicMaxOutputTokens caps one answer.
	AnthropicMaxOutputTokens int
	// CustomAPIKey is the custom endpoint's key -- OPTIONAL (local
	// servers usually need none; empty sends no Authorization
	// header). Never logged or emitted.
	CustomAPIKey string
	// CustomBaseURL names the custom OpenAI-compatible endpoint --
	// REQUIRED for the custom provider (no official fallback exists).
	CustomBaseURL string
	// CustomModel names the answering model -- required (no default
	// is invented for an unknown server).
	CustomModel string
	// CustomMaxOutputTokens caps one answer.
	CustomMaxOutputTokens int
	// AICachePath is the persistent AI answer cache file ("" =
	// memory-only for this run).
	AICachePath string
	// Logf receives provider-layer degradations (AI cache load/save
	// problems, and one line per failed Kagi request carrying the
	// Kagi trace id for support); nil = silent. Never receives key
	// material.
	Logf func(format string, v ...any)
}

// Dispatcher serves preview requests: synchronous bookkeeping on the
// caller (cancel the previous request, remember the generation), one
// goroutine per request for everything that touches the disk, and an
// in-memory LRU of computed rich payloads. A new request -- or a
// TargetNone request -- cancels the in-flight one; cancelled requests
// never emit.
type Dispatcher struct {
	opt    Options
	parent context.Context
	cache  *payloadCache

	mu     sync.Mutex // guards gen, cancel
	gen    int
	cancel context.CancelFunc

	// webErr/aiErr say WHY an unavailable provider (nil webFn/aiFn)
	// is unavailable -- the no-key message or the invalid-baseUrl
	// message, emitted verbatim by the fetch path. Empty once the
	// provider is wired. Messages name config keys and env vars only,
	// never values.
	webErr string
	aiErr  string

	// Provider seams; New fills in the real implementations and unit
	// tests inject slow or failing fakes. webFn/aiFn stay nil while
	// the matching API key is unconfigured.
	lstat    func(path string) (os.FileInfo, error)
	readlink func(path string) (string, error)
	readHead func(path string, n int) ([]byte, error)
	textFn   func(ctx context.Context, path string, maxKB int) (*TextPreview, error)
	imageFn  func(ctx context.Context, path string, maxEdge int) (*ImagePreview, error)
	dirFn    func(ctx context.Context, path string, maxEntries int) (*DirPreview, error)
	webFn    func(ctx context.Context, query string) (*WebPreview, error)
	aiFn     func(ctx context.Context, query string) (*AIPreview, error)
}

// Fetch-path messages for an unavailable web-search provider (the AI
// provider messages live in aiprovider.go). They name the config
// knobs (and environment fallbacks) but never quote values: the keys
// are secret and a base URL may carry userinfo.
const (
	errWebNoKey   = "kagi: no API key (preview.kagi.apiKey or KAGI_API_KEY)"
	errWebBadBase = "kagi: invalid baseUrl (preview.kagi.baseUrl)"
)

// normalizeBaseURL prepares a configured provider base URL: empty
// stays empty (the client's official default), ONE trailing "/" is
// trimmed (the clients join "<base><path>", so a trailing slash would
// double up), and anything that does not parse as http(s) with a host
// is rejected -- New then leaves the provider unavailable instead of
// installing a client that can only fail. The returned error is
// deliberately value-free (url.Parse errors quote their input, which
// may carry userinfo).
func normalizeBaseURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	base := strings.TrimSuffix(raw, "/")
	u, err := url.Parse(base)
	if err != nil {
		return "", errors.New("unparsable base URL")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("base URL must be http(s) with a host")
	}
	return base, nil
}

// New builds a Dispatcher. parent bounds every request the dispatcher
// will ever run: cancelling it (app shutdown) aborts the in-flight
// request and makes every later Preview call a no-op.
func New(parent context.Context, opt Options) *Dispatcher {
	if parent == nil {
		parent = context.Background()
	}
	d := &Dispatcher{
		opt:      opt,
		parent:   parent,
		cache:    newPayloadCache(),
		lstat:    os.Lstat,
		readlink: os.Readlink,
		readHead: readHead,
		textFn: func(ctx context.Context, path string, maxKB int) (*TextPreview, error) {
			content, truncated, size, err := ReadCapped(path, maxKB)
			if err != nil {
				return nil, err
			}
			return &TextPreview{
				Content:   content,
				Lang:      LangHint(path),
				Truncated: truncated,
				SizeBytes: size,
			}, nil
		},
		imageFn: Thumbnail,
		dirFn: func(_ context.Context, path string, maxEntries int) (*DirPreview, error) {
			return ListCapped(path, maxEntries)
		},
	}
	d.webErr = errWebNoKey
	if opt.KagiAPIKey != "" {
		if base, err := normalizeBaseURL(opt.KagiBaseURL); err != nil {
			d.webErr = errWebBadBase
		} else {
			kagi := NewKagiClient(opt.KagiAPIKey, opt.KagiMaxResults)
			kagi.BaseURL = base
			// One "preview: kagi: HTTP <code> (trace <id> ...)" line
			// per failed request -- the trace id Kagi support asks
			// for; the pane error stays terse.
			kagi.Logf = func(format string, v ...any) { d.logf("preview: "+format, v...) }
			d.webErr = ""
			d.webFn = func(ctx context.Context, query string) (*WebPreview, error) {
				results, cached, err := kagi.Search(ctx, query)
				if err != nil {
					return nil, err
				}
				return &WebPreview{Query: query, Results: results, Cached: cached}, nil
			}
		}
	}
	d.wireAI(opt)
	return d
}

// logf logs through Options.Logf when set.
func (d *Dispatcher) logf(format string, v ...any) {
	if d.opt.Logf != nil {
		d.opt.Logf(format, v...)
	}
}

// WebConfigured reports whether the web-search provider is usable (a
// key and, when overridden, a valid base URL).
func (d *Dispatcher) WebConfigured() bool { return d.webFn != nil }

// AIConfigured reports whether the AI answer provider is usable (a
// key and, when overridden, a valid base URL).
func (d *Dispatcher) AIConfigured() bool { return d.aiFn != nil }

// readHead reads at most n bytes from the start of path.
func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	m, err := io.ReadFull(f, buf)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	return buf[:m], nil
}

// runUnder runs f on its own goroutine and races it against ctx: at
// cancellation or deadline the result is abandoned (f's goroutine
// finishes into a buffered channel and is collected), so a hung
// filesystem call cannot hold a preview past its hard timeout.
func runUnder[T any](ctx context.Context, f func() (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := f()
		ch <- result{v: v, err: err}
	}()
	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case r := <-ch:
		return r.v, r.err
	}
}

// Preview starts serving target under generation gen. The call itself
// is synchronous bookkeeping only -- it cancels the previous request
// and spawns one goroutine -- so it is safe on any input path. A
// TargetNone (or empty) Kind cancels without starting anything.
func (d *Dispatcher) Preview(target Target, gen int) {
	d.mu.Lock()
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	d.gen = gen
	if target.Kind == "" || target.Kind == TargetNone {
		d.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(d.parent)
	d.cancel = cancel
	d.mu.Unlock()
	go d.serve(ctx, target, gen)
}

// emit delivers p unless the request was cancelled (a newer request
// superseded it, or the dispatcher's parent context ended).
func (d *Dispatcher) emit(ctx context.Context, p Payload) {
	if ctx.Err() != nil {
		return
	}
	if d.opt.Emit != nil {
		d.opt.Emit(p)
	}
}

// errorPayload builds a KindError payload.
func errorPayload(gen int, target Target, msg string, start time.Time) Payload {
	title := target.Title
	if title == "" && target.Path != "" {
		title = filepath.Base(target.Path)
	}
	return Payload{
		Gen:   gen,
		Kind:  KindError,
		Title: title,
		Path:  target.Path,
		Err:   msg,
		DurMS: time.Since(start).Milliseconds(),
	}
}

// serve computes and emits the payload(s) for one request. It runs on
// its own goroutine under the request's cancellable context.
func (d *Dispatcher) serve(ctx context.Context, target Target, gen int) {
	start := time.Now()
	switch target.Kind {
	case TargetPlugin:
		d.emit(ctx, pluginCard(target, gen, start))
	case TargetFile:
		d.serveFile(ctx, target, gen, start)
	default:
		d.emit(ctx, errorPayload(gen, target, fmt.Sprintf("unknown preview target kind %q", target.Kind), start))
	}
}

// pluginCard renders a plugin result as a metadata card from the
// target fields alone -- no IO.
func pluginCard(target Target, gen int, start time.Time) Payload {
	rows := make([]MetaRow, 0, 2)
	if target.Subtitle != "" {
		rows = append(rows, MetaRow{Label: "Detail", Value: target.Subtitle})
	}
	if target.PluginName != "" {
		rows = append(rows, MetaRow{Label: "Source", Value: target.PluginName})
	}
	return Payload{
		Gen:   gen,
		Kind:  KindMeta,
		Title: target.Title,
		Meta:  rows,
		DurMS: time.Since(start).Milliseconds(),
	}
}

// cacheKey identifies one rich payload: the path plus the lstat
// identity (mtime, size) plus the provider kind, so any change to the
// file -- or a different provider decision -- misses.
func cacheKey(path string, fi os.FileInfo, kind string) string {
	return fmt.Sprintf("%s\x00%d\x00%d\x00%s", path, fi.ModTime().UnixNano(), fi.Size(), kind)
}

// serveFile previews one filesystem entry: validate, lstat, emit a
// fast metadata card, then compute and emit the rich payload (listing,
// text, thumbnail, or a final metadata card for binaries). Cache hits
// skip straight to the rich payload.
func (d *Dispatcher) serveFile(ctx context.Context, target Target, gen int, start time.Time) {
	path := target.Path
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		d.emit(ctx, errorPayload(gen, target, fmt.Sprintf("preview needs a clean absolute path, got %q", path), start))
		return
	}
	fi, err := d.lstat(path)
	if err != nil {
		d.emit(ctx, errorPayload(gen, target, err.Error(), start))
		return
	}
	title := filepath.Base(path)

	if fi.Mode()&os.ModeSymlink != 0 {
		// Symlinks are described, never followed (matching the index,
		// which never descends them).
		extra := []MetaRow{}
		if dest, err := d.readlink(path); err == nil {
			extra = append(extra, MetaRow{Label: "Target", Value: dest})
		}
		d.emit(ctx, Payload{
			Gen:   gen,
			Kind:  KindMeta,
			Title: title,
			Path:  path,
			Meta:  MetaFor(path, fi, extra...),
			DurMS: time.Since(start).Milliseconds(),
		})
		return
	}

	providerKind := KindText
	switch {
	case fi.IsDir():
		providerKind = KindDir
	case isImageExt(path):
		providerKind = KindImage
	}
	key := cacheKey(path, fi, providerKind)
	if p, ok := d.cache.get(key); ok {
		p.Gen = gen
		p.DurMS = time.Since(start).Milliseconds()
		d.emit(ctx, p)
		return
	}

	// Fast first payload: the metadata card, before any content IO.
	d.emit(ctx, Payload{
		Gen:   gen,
		Kind:  KindMeta,
		Title: title,
		Path:  path,
		Meta:  MetaFor(path, fi),
		DurMS: time.Since(start).Milliseconds(),
	})

	rich, err := d.richPayload(ctx, providerKind, path, title, fi)
	if err != nil {
		d.emit(ctx, errorPayload(gen, target, err.Error(), start))
		return
	}
	rich.DurMS = time.Since(start).Milliseconds()
	d.cache.put(key, rich)
	rich.Gen = gen
	d.emit(ctx, rich)
}

// richPayload computes the content payload for one file-system entry
// under the provider's hard timeout. The returned payload carries no
// Gen (the caller stamps it) so the cached copy is generation-free.
func (d *Dispatcher) richPayload(ctx context.Context, providerKind, path, title string, fi os.FileInfo) (Payload, error) {
	switch providerKind {
	case KindDir:
		tctx, cancel := context.WithTimeout(ctx, metaTimeout)
		defer cancel()
		dp, err := runUnder(tctx, func() (*DirPreview, error) {
			return d.dirFn(tctx, path, d.opt.DirMaxEntries)
		})
		if err != nil {
			return Payload{}, err
		}
		return Payload{Kind: KindDir, Title: title, Path: path, Dir: dp}, nil
	case KindImage:
		tctx, cancel := context.WithTimeout(ctx, imageTimeout)
		defer cancel()
		ip, err := runUnder(tctx, func() (*ImagePreview, error) {
			return d.imageFn(tctx, path, d.opt.ImageMaxEdge)
		})
		if err != nil {
			return Payload{}, err
		}
		return Payload{Kind: KindImage, Title: title, Path: path, Image: ip}, nil
	default:
		tctx, cancel := context.WithTimeout(ctx, metaTimeout)
		defer cancel()
		return runUnder(tctx, func() (Payload, error) {
			head, err := d.readHead(path, headSniffBytes)
			if err != nil {
				return Payload{}, err
			}
			if IsBinary(head) {
				// Binary non-image: the final answer is the metadata
				// card again, now with the sniffed kind.
				return Payload{
					Kind:  KindMeta,
					Title: title,
					Path:  path,
					Meta:  MetaFor(path, fi, MetaRow{Label: "Content", Value: "binary"}),
				}, nil
			}
			tp, err := d.textFn(tctx, path, d.opt.TextMaxKB)
			if err != nil {
				return Payload{}, err
			}
			return Payload{Kind: KindText, Title: title, Path: path, Text: tp}, nil
		})
	}
}

// arm is the shared bookkeeping for the explicit web/AI fetches: it
// cancels the in-flight request (file preview or fetch alike -- both
// live in the SAME cancel/generation space, so each supersedes the
// other), stores gen, and returns the new request context.
func (d *Dispatcher) arm(gen int) context.Context {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancel != nil {
		d.cancel()
	}
	d.gen = gen
	ctx, cancel := context.WithCancel(d.parent)
	d.cancel = cancel
	return ctx
}

// FetchWeb starts one explicit web search for query under gen -- the
// ONLY path that reaches the web-search provider (nothing automatic
// ever calls it). Exactly one payload answers an accepted fetch: kind
// "web" on success, kind "error" otherwise (blank query, missing key,
// provider failure, timeout).
func (d *Dispatcher) FetchWeb(query string, gen int) {
	start := time.Now()
	ctx := d.arm(gen)
	if strings.TrimSpace(query) == "" {
		d.emit(ctx, Payload{Gen: gen, Kind: KindError, Err: "empty query"})
		return
	}
	if d.webFn == nil {
		d.emit(ctx, Payload{Gen: gen, Kind: KindError, Title: query, Err: d.webErr})
		return
	}
	go d.serveWeb(ctx, query, gen, start)
}

// serveWeb runs the web-search provider under its hard timeout and
// emits the single answer payload.
func (d *Dispatcher) serveWeb(ctx context.Context, query string, gen int, start time.Time) {
	tctx, cancel := context.WithTimeout(ctx, webTimeout)
	defer cancel()
	wp, err := runUnder(tctx, func() (*WebPreview, error) {
		return d.webFn(tctx, query)
	})
	if err != nil {
		d.emit(ctx, Payload{Gen: gen, Kind: KindError, Title: query,
			Err: fetchErrMsg(err, "web search", webTimeout), DurMS: time.Since(start).Milliseconds()})
		return
	}
	d.emit(ctx, Payload{Gen: gen, Kind: KindWeb, Title: query, Web: wp,
		DurMS: time.Since(start).Milliseconds()})
}

// FetchAI starts one explicit AI answer for query under gen -- the
// ONLY path that reaches the AI provider. Same contract as FetchWeb:
// one payload per accepted fetch, kind "ai" or kind "error".
func (d *Dispatcher) FetchAI(query string, gen int) {
	start := time.Now()
	ctx := d.arm(gen)
	if strings.TrimSpace(query) == "" {
		d.emit(ctx, Payload{Gen: gen, Kind: KindError, Err: "empty query"})
		return
	}
	if d.aiFn == nil {
		d.emit(ctx, Payload{Gen: gen, Kind: KindError, Title: query, Err: d.aiErr})
		return
	}
	go d.serveAI(ctx, query, gen, start)
}

// serveAI runs the AI provider under its hard timeout and emits the
// single answer payload.
func (d *Dispatcher) serveAI(ctx context.Context, query string, gen int, start time.Time) {
	tctx, cancel := context.WithTimeout(ctx, aiTimeout)
	defer cancel()
	ap, err := runUnder(tctx, func() (*AIPreview, error) {
		return d.aiFn(tctx, query)
	})
	if err != nil {
		d.emit(ctx, Payload{Gen: gen, Kind: KindError, Title: query,
			Err: fetchErrMsg(err, "AI answer", aiTimeout), DurMS: time.Since(start).Milliseconds()})
		return
	}
	d.emit(ctx, Payload{Gen: gen, Kind: KindAI, Title: query, AI: ap,
		DurMS: time.Since(start).Milliseconds()})
}

// fetchErrMsg turns a provider error into the pane message, spelling
// the hard timeout out instead of "context deadline exceeded".
func fetchErrMsg(err error, what string, limit time.Duration) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("%s timed out after %s", what, limit)
	}
	return err.Error()
}
