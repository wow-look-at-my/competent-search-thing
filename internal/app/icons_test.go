package app

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
)

// fakeIconResolver records Resolve calls and answers from a fixed map.
type fakeIconResolver struct {
	mu    sync.Mutex
	keys  [][]string
	sizes []int
	out   map[string]string
}

func (f *fakeIconResolver) Resolve(keys []string, size int) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, append([]string(nil), keys...))
	f.sizes = append(f.sizes, size)
	res := make(map[string]string, len(keys))
	for _, k := range keys {
		if v, ok := f.out[k]; ok {
			res[k] = v
		}
	}
	return res
}

func TestResolveIconsBeforeStartup(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	got := a.ResolveIcons([]string{"app:firefox"}, 64)
	require.NotNil(t, got)
	require.Empty(t, got, "no resolver yet: empty map, never nil")
}

func TestResolveIconsNilSeamResult(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	a.Startup(context.Background())
	got := a.ResolveIcons([]string{"app:firefox"}, 64)
	require.NotNil(t, got)
	require.Empty(t, got, "the stubbed seam yields no resolver; ResolveIcons degrades")
}

func TestResolveIconsDelegatesToService(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	fake := &fakeIconResolver{out: map[string]string{"app:firefox": "data:image/png;base64,AAAA"}}
	a.newIcons = func() iconResolver { return fake }
	a.Startup(context.Background())

	got := a.ResolveIcons([]string{"app:firefox", "app:missing"}, 48)
	require.Equal(t, map[string]string{"app:firefox": "data:image/png;base64,AAAA"}, got)

	require.Empty(t, a.ResolveIcons(nil, 48), "no keys short-circuits without a service call")

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Equal(t, [][]string{{"app:firefox", "app:missing"}}, fake.keys)
	require.Equal(t, []int{48}, fake.sizes)
}

func TestBuildIconsProducesService(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	svc := a.buildIcons()
	require.NotNil(t, svc, "the production seam value constructs a resolver (no IO)")
	_, isNoter := svc.(faviconNoter)
	require.True(t, isNoter, "the production resolver accepts favicon hints")
}

func TestFaviconProfileDir(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	require.Empty(t, a.faviconProfileDir(),
		"default config + pinned-nil firefoxBases: no profile anywhere")

	// A config override wins without any discovery: open-tabs first...
	cfg := config.Default()
	cfg.Firefox.OpenTabs.ProfileDir = "/profiles/tabs"
	cfg.Firefox.FrequentSites.ProfileDir = "/profiles/sites"
	require.NoError(t, config.Save(cfg))
	require.Equal(t, "/profiles/tabs", a.faviconProfileDir())

	// ...then frequent-sites.
	cfg.Firefox.OpenTabs.ProfileDir = ""
	require.NoError(t, config.Save(cfg))
	require.Equal(t, "/profiles/sites", a.faviconProfileDir())

	// No overrides: the shared platform discovery answers.
	cfg.Firefox.FrequentSites.ProfileDir = ""
	require.NoError(t, config.Save(cfg))
	base := t.TempDir()
	writeProfilesINI(t, base, "abc.default")
	a.plat.firefoxBases = func() []string { return []string{base} }
	require.Equal(t, filepath.Join(base, "abc.default"), a.faviconProfileDir())
}

func TestNoteFaviconNilAndPlainResolvers(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	a.noteFavicon("https://a.example/", "https://a.example/f.ico") // no resolver: no panic

	a.newIcons = func() iconResolver { return &fakeIconResolver{} }
	a.startIcons()
	a.noteFavicon("https://a.example/", "https://a.example/f.ico") // plain resolver: quietly skipped
	a.noteFavicon("https://a.example/", "")                        // empty hint: short-circuits
}
