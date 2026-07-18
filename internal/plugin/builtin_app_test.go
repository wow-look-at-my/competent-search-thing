package plugin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// targetedReq builds the request a bang-targeted dispatch produces.
func targetedReq(bang, stripped string) Request {
	req := baseRequest("!"+bang+" "+stripped, stripped, 1, nil)
	req.Targeted = true
	req.Bang = bang
	return req
}

func TestAppCommandProviderOneResultPerBang(t *testing.T) {
	p := newAppCommandProvider("1.2.3")
	require.Equal(t, "app", p.id())
	require.Equal(t, "App Commands", p.displayName())
	require.Equal(t, []string{"rescan", "reload", "config", "version", "quit"}, p.bangNames())
	require.Zero(t, p.debounce())
	_, _, ok := p.match("rescan", nil)
	require.False(t, ok, "builtins never match non-targeted queries")

	icons := map[string]string{
		"rescan":  "bolt",
		"reload":  "bolt",
		"config":  "text",
		"version": "info",
		"quit":    "warning",
	}
	for _, bang := range p.bangNames() {
		t.Run(bang, func(t *testing.T) {
			results := srcResults(t, p, targetedReq(bang, ""))
			require.Len(t, results, 1)
			r := results[0]
			require.NotEmpty(t, r.Title)
			require.NotEmpty(t, r.Subtitle)
			require.Equal(t, icons[bang], r.Icon)
			require.Equal(t, float64(100), *r.Score)
			require.Equal(t, &Action{Type: ActionRunBuiltin, Value: bang}, r.Action)
		})
	}
}

func TestAppCommandProviderVersionSubtitle(t *testing.T) {
	p := newAppCommandProvider("v9.9")
	results := srcResults(t, p, targetedReq("version", ""))
	require.Contains(t, results[0].Subtitle, "v9.9")

	dev := newAppCommandProvider("")
	results = srcResults(t, dev, targetedReq("version", ""))
	require.Contains(t, results[0].Subtitle, "dev", "empty version reads as a dev build")
}

func TestAppCommandProviderIgnoresNonTargetedAndUnknown(t *testing.T) {
	p := newAppCommandProvider("x")

	results := srcResults(t, p, baseRequest("rescan", "rescan", 1, nil))
	require.Empty(t, results, "non-targeted requests yield nothing")

	results = srcResults(t, p, targetedReq("nonsense", ""))
	require.Empty(t, results, "unknown bang yields nothing")
}

func TestAppCommandThroughDispatch(t *testing.T) {
	r := New(Options{Version: "2.0", Logf: func(string, ...any) {}})
	defer r.Close()
	emit, ch := collectEmissions()

	info := r.Dispatch(context.Background(), "!version now", 1, nil, emit)
	require.Equal(t, TargetInfo{Targeted: true, Plugin: "app", Name: "App Commands", Bang: "version"}, info)
	e := recvEmission(t, ch)
	require.Equal(t, "app", e.Plugin)
	require.Len(t, e.Results, 1)
	require.Contains(t, e.Results[0].Subtitle, "2.0")
	require.Equal(t, &Action{Type: ActionRunBuiltin, Value: "version"}, e.Results[0].Action)
}
