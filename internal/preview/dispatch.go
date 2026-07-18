package preview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Per-request hard timeouts. Every provider runs under one of these,
// derived from the request's cancellable context, so a hung filesystem
// can delay one preview but never wedge the pane.
const (
	metaTimeout  = 2 * time.Second // metadata, directory and text previews
	imageTimeout = 4 * time.Second // image decode + downscale
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

	// Provider seams; New fills in the real implementations and unit
	// tests inject slow or failing fakes.
	lstat    func(path string) (os.FileInfo, error)
	readlink func(path string) (string, error)
	readHead func(path string, n int) ([]byte, error)
	textFn   func(ctx context.Context, path string, maxKB int) (*TextPreview, error)
	imageFn  func(ctx context.Context, path string, maxEdge int) (*ImagePreview, error)
	dirFn    func(ctx context.Context, path string, maxEntries int) (*DirPreview, error)
}

// New builds a Dispatcher. parent bounds every request the dispatcher
// will ever run: cancelling it (app shutdown) aborts the in-flight
// request and makes every later Preview call a no-op.
func New(parent context.Context, opt Options) *Dispatcher {
	if parent == nil {
		parent = context.Background()
	}
	return &Dispatcher{
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
}

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
