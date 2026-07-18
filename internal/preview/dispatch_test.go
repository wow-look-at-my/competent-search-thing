package preview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newTestDispatcher builds a Dispatcher whose Emit feeds the returned
// channel. The parent context is cancelled at test end.
func newTestDispatcher(t *testing.T) (*Dispatcher, chan Payload) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := make(chan Payload, 32)
	d := New(ctx, Options{
		TextMaxKB:     4,
		ImageMaxEdge:  100,
		DirMaxEntries: 3,
		Emit:          func(p Payload) { ch <- p },
	})
	return d, ch
}

func waitPayload(t *testing.T, ch <-chan Payload) Payload {
	t.Helper()
	select {
	case p := <-ch:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a preview payload")
		return Payload{}
	}
}

func assertNoPayload(t *testing.T, ch <-chan Payload) {
	t.Helper()
	select {
	case p := <-ch:
		t.Fatalf("unexpected payload: %+v", p)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestPreviewPluginTargetEmitsMetaCard(t *testing.T) {
	d, ch := newTestDispatcher(t)
	d.Preview(Target{
		Kind:       TargetPlugin,
		Title:      "2+2 = 4",
		Subtitle:   "Copy result",
		PluginName: "calc",
	}, 7)
	p := waitPayload(t, ch)
	require.Equal(t, 7, p.Gen)
	require.Equal(t, KindMeta, p.Kind)
	require.Equal(t, "2+2 = 4", p.Title)
	require.Equal(t, []MetaRow{
		{Label: "Detail", Value: "Copy result"},
		{Label: "Source", Value: "calc"},
	}, p.Meta)
	require.Empty(t, p.Path)
	assertNoPayload(t, ch)
}

func TestPreviewNoneCancelsOnly(t *testing.T) {
	d, ch := newTestDispatcher(t)
	d.Preview(Target{Kind: TargetNone}, 1)
	d.Preview(Target{}, 2) // empty kind acts the same
	assertNoPayload(t, ch)
}

func TestPreviewFileEmitsMetaThenText(t *testing.T) {
	d, ch := newTestDispatcher(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	require.NoError(t, os.WriteFile(path, []byte("package main\n"), 0o644))

	d.Preview(Target{Kind: TargetFile, Path: path}, 3)
	meta := waitPayload(t, ch)
	require.Equal(t, KindMeta, meta.Kind)
	require.Equal(t, 3, meta.Gen)
	require.Equal(t, "main.go", meta.Title)
	require.Equal(t, path, meta.Path)
	require.NotEmpty(t, meta.Meta)

	text := waitPayload(t, ch)
	require.Equal(t, KindText, text.Kind)
	require.Equal(t, 3, text.Gen)
	require.NotNil(t, text.Text)
	require.Equal(t, "package main\n", text.Text.Content)
	require.Equal(t, "go", text.Text.Lang)
	require.False(t, text.Text.Truncated)
	require.GreaterOrEqual(t, text.DurMS, int64(0))
	assertNoPayload(t, ch)
}

func TestPreviewFileCacheHitSkipsMeta(t *testing.T) {
	d, ch := newTestDispatcher(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "cached.md")
	require.NoError(t, os.WriteFile(path, []byte("# heading\n"), 0o644))

	d.Preview(Target{Kind: TargetFile, Path: path}, 1)
	require.Equal(t, KindMeta, waitPayload(t, ch).Kind)
	require.Equal(t, KindText, waitPayload(t, ch).Kind)

	d.Preview(Target{Kind: TargetFile, Path: path}, 2)
	hit := waitPayload(t, ch)
	require.Equal(t, KindText, hit.Kind, "a cache hit emits the rich payload directly")
	require.Equal(t, 2, hit.Gen, "cached payloads are re-stamped with the current generation")
	require.Equal(t, "# heading\n", hit.Text.Content)
	assertNoPayload(t, ch)
}

func TestPreviewFileCacheMissesAfterModification(t *testing.T) {
	d, ch := newTestDispatcher(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "changing.txt")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o644))
	d.Preview(Target{Kind: TargetFile, Path: path}, 1)
	require.Equal(t, KindMeta, waitPayload(t, ch).Kind)
	require.Equal(t, "old", waitPayload(t, ch).Text.Content)

	// A different size (and mtime) must miss the cache.
	require.NoError(t, os.WriteFile(path, []byte("newer contents"), 0o644))
	d.Preview(Target{Kind: TargetFile, Path: path}, 2)
	require.Equal(t, KindMeta, waitPayload(t, ch).Kind, "a changed file re-emits the fast meta card")
	require.Equal(t, "newer contents", waitPayload(t, ch).Text.Content)
}

func TestPreviewDirListing(t *testing.T) {
	d, ch := newTestDispatcher(t)
	dir := t.TempDir()
	sub := filepath.Join(dir, "listing")
	require.NoError(t, os.Mkdir(sub, 0o755))
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(sub, n), []byte("x"), 0o644))
	}

	d.Preview(Target{Kind: TargetFile, Path: sub, IsDir: true}, 4)
	require.Equal(t, KindMeta, waitPayload(t, ch).Kind)
	listing := waitPayload(t, ch)
	require.Equal(t, KindDir, listing.Kind)
	require.NotNil(t, listing.Dir)
	require.Len(t, listing.Dir.Entries, 3, "the configured DirMaxEntries caps the listing")
	require.Equal(t, 4, listing.Dir.Total)
	require.True(t, listing.Dir.Truncated)
}

func TestPreviewImageFile(t *testing.T) {
	d, ch := newTestDispatcher(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "pic.png")
	writeImage(t, path, testImage(200, 100))

	d.Preview(Target{Kind: TargetFile, Path: path}, 5)
	require.Equal(t, KindMeta, waitPayload(t, ch).Kind)
	img := waitPayload(t, ch)
	require.Equal(t, KindImage, img.Kind)
	require.NotNil(t, img.Image)
	require.Equal(t, 100, img.Image.W, "downscaled to the configured ImageMaxEdge")
	require.Equal(t, 50, img.Image.H)
	require.Equal(t, 200, img.Image.OrigW)
}

func TestPreviewBinaryFileEndsWithMetaCard(t *testing.T) {
	d, ch := newTestDispatcher(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "blob.bin")
	require.NoError(t, os.WriteFile(path, []byte{0x7f, 'E', 'L', 'F', 0x00, 0x01, 0x02}, 0o644))

	d.Preview(Target{Kind: TargetFile, Path: path}, 6)
	require.Equal(t, KindMeta, waitPayload(t, ch).Kind)
	final := waitPayload(t, ch)
	require.Equal(t, KindMeta, final.Kind, "binary non-image files end on a metadata card")
	require.Equal(t, "binary", final.Meta[len(final.Meta)-1].Value)
	require.Equal(t, "Content", final.Meta[len(final.Meta)-1].Label)
	assertNoPayload(t, ch)
}

func TestPreviewSymlinkDescribedNeverFollowed(t *testing.T) {
	d, ch := newTestDispatcher(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "real.txt")
	require.NoError(t, os.WriteFile(target, []byte("data"), 0o644))
	link := filepath.Join(dir, "alias")
	require.NoError(t, os.Symlink(target, link))

	d.Preview(Target{Kind: TargetFile, Path: link}, 8)
	p := waitPayload(t, ch)
	require.Equal(t, KindMeta, p.Kind)
	require.Equal(t, "symlink", metaValue(t, p.Meta, "Kind"))
	require.Equal(t, target, metaValue(t, p.Meta, "Target"))
	assertNoPayload(t, ch)
}

func TestPreviewErrorPaths(t *testing.T) {
	d, ch := newTestDispatcher(t)

	d.Preview(Target{Kind: TargetFile, Path: "relative/path"}, 1)
	p := waitPayload(t, ch)
	require.Equal(t, KindError, p.Kind)
	require.Contains(t, p.Err, "absolute path")

	d.Preview(Target{Kind: TargetFile, Path: "/x/../unclean"}, 2)
	p = waitPayload(t, ch)
	require.Equal(t, KindError, p.Kind)

	d.Preview(Target{Kind: TargetFile, Path: filepath.Join(t.TempDir(), "missing.txt")}, 3)
	p = waitPayload(t, ch)
	require.Equal(t, KindError, p.Kind)
	require.Equal(t, 3, p.Gen)
	require.Equal(t, "missing.txt", p.Title)

	d.Preview(Target{Kind: "bogus"}, 4)
	p = waitPayload(t, ch)
	require.Equal(t, KindError, p.Kind)
	require.Contains(t, p.Err, "bogus")
}

func TestPreviewProviderErrorEmitsErrorPayload(t *testing.T) {
	d, ch := newTestDispatcher(t)
	d.textFn = func(context.Context, string, int) (*TextPreview, error) {
		return nil, errors.New("provider exploded")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	require.NoError(t, os.WriteFile(path, []byte("text"), 0o644))

	d.Preview(Target{Kind: TargetFile, Path: path}, 1)
	require.Equal(t, KindMeta, waitPayload(t, ch).Kind)
	p := waitPayload(t, ch)
	require.Equal(t, KindError, p.Kind)
	require.Contains(t, p.Err, "provider exploded")

	// Errors are never cached: the next request retries the provider.
	d.textFn = func(context.Context, string, int) (*TextPreview, error) {
		return &TextPreview{Content: "recovered"}, nil
	}
	d.Preview(Target{Kind: TargetFile, Path: path}, 2)
	require.Equal(t, KindMeta, waitPayload(t, ch).Kind)
	require.Equal(t, "recovered", waitPayload(t, ch).Text.Content)
}

func TestPreviewSupersedeCancelsInFlight(t *testing.T) {
	d, ch := newTestDispatcher(t)
	entered := make(chan struct{}, 1)
	d.textFn = func(ctx context.Context, path string, maxKB int) (*TextPreview, error) {
		entered <- struct{}{}
		<-ctx.Done() // block until this generation is cancelled
		return nil, ctx.Err()
	}
	dir := t.TempDir()
	slow := filepath.Join(dir, "slow.txt")
	fast := filepath.Join(dir, "fast.txt")
	require.NoError(t, os.WriteFile(slow, []byte("slow"), 0o644))
	require.NoError(t, os.WriteFile(fast, []byte("fast"), 0o644))

	d.Preview(Target{Kind: TargetFile, Path: slow}, 1)
	require.Equal(t, 1, waitPayload(t, ch).Gen, "the fast meta card for gen 1 arrives")
	<-entered // the provider is now blocked on gen 1's context

	d.textFn = func(context.Context, string, int) (*TextPreview, error) {
		return &TextPreview{Content: "fast"}, nil
	}
	d.Preview(Target{Kind: TargetFile, Path: fast}, 2)
	meta := waitPayload(t, ch)
	require.Equal(t, 2, meta.Gen)
	require.Equal(t, KindMeta, meta.Kind)
	rich := waitPayload(t, ch)
	require.Equal(t, 2, rich.Gen)
	require.Equal(t, KindText, rich.Kind)
	// Gen 1's provider was released by the cancellation; its error
	// result is suppressed and nothing else ever arrives.
	assertNoPayload(t, ch)
}

func TestPreviewStaleEmitSuppressedAfterCancel(t *testing.T) {
	d, ch := newTestDispatcher(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	d.textFn = func(ctx context.Context, path string, maxKB int) (*TextPreview, error) {
		entered <- struct{}{}
		<-release // block through the cancellation, then answer anyway
		return &TextPreview{Content: "stale answer"}, nil
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))

	d.Preview(Target{Kind: TargetFile, Path: path}, 1)
	require.Equal(t, KindMeta, waitPayload(t, ch).Kind)
	<-entered
	d.Preview(Target{Kind: TargetNone}, 2) // cancel
	close(release)                         // the provider now returns its stale answer
	assertNoPayload(t, ch)
}

func TestPreviewAfterParentCancelIsSilent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan Payload, 8)
	d := New(ctx, Options{Emit: func(p Payload) { ch <- p }})
	cancel()
	d.Preview(Target{Kind: TargetPlugin, Title: "t"}, 1)
	assertNoPayload(t, ch)
}

func TestNewNilParentAndNilEmit(t *testing.T) {
	d := New(nil, Options{TextMaxKB: 1})
	require.NotNil(t, d.parent)
	// A nil Emit must not panic.
	d.Preview(Target{Kind: TargetPlugin, Title: "t"}, 1)
	time.Sleep(50 * time.Millisecond)
	d.Preview(Target{Kind: TargetNone}, 2)
}

func TestRunUnder(t *testing.T) {
	v, err := runUnder(context.Background(), func() (int, error) { return 42, nil })
	require.NoError(t, err)
	require.Equal(t, 42, v)

	wantErr := errors.New("boom")
	_, err = runUnder(context.Background(), func() (int, error) { return 0, wantErr })
	require.ErrorIs(t, err, wantErr)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	blocked := make(chan struct{})
	_, err = runUnder(ctx, func() (int, error) { <-blocked; return 0, nil })
	require.ErrorIs(t, err, context.Canceled)
	close(blocked)
}

func TestCacheKeyChangesWithIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(path, []byte("abc"), 0o644))
	fi, err := os.Lstat(path)
	require.NoError(t, err)
	k1 := cacheKey(path, fi, KindText)
	require.NotEqual(t, k1, cacheKey(path, fi, KindImage), "the provider kind is part of the key")
	require.NoError(t, os.WriteFile(path, []byte("abcd"), 0o644))
	fi2, err := os.Lstat(path)
	require.NoError(t, err)
	require.NotEqual(t, k1, cacheKey(path, fi2, KindText), "size/mtime changes miss")
}
