package app

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
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
	require.NotNil(t, a.buildIcons(), "the production seam value constructs a resolver (no IO)")
}
