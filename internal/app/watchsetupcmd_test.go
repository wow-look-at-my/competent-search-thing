package app

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/watchsetup"
)

// fakeWatchSetup is a recording watchSetupRunner: it never probes
// fanotify or spawns pkexec, it just returns a scripted Result.
type fakeWatchSetup struct {
	res    watchsetup.Result
	called int
}

func (f *fakeWatchSetup) Attempt(context.Context, io.Writer) watchsetup.Result {
	f.called++
	return f.res
}

func TestSetupWatchMessages(t *testing.T) {
	cases := []struct {
		res  watchsetup.Result
		want string
	}{
		{watchsetup.Result{Action: watchsetup.ActionGranted}, "enabled. Restart"},
		{watchsetup.Result{Action: watchsetup.ActionOptimal}, "already enabled"},
		{watchsetup.Result{Action: watchsetup.ActionAttemptFailed}, "did not complete"},
		{watchsetup.Result{Action: watchsetup.ActionSkipped, Reason: "not-linux"}, "Linux-only feature"},
		{watchsetup.Result{Action: watchsetup.ActionSkipped, Reason: "unsupported"}, "not available in this environment"},
		{watchsetup.Result{Action: watchsetup.ActionSkipped, Reason: "other"}, "Nothing to do"},
	}
	for _, c := range cases {
		a, _ := newTestApp(t, nil, Options{})
		fake := &fakeWatchSetup{res: c.res}
		a.newWatchSetup = func() watchSetupRunner { return fake }
		require.Contains(t, a.SetupWatch(), c.want, "action %v reason %q", c.res.Action, c.res.Reason)
		require.Equal(t, 1, fake.called, "Attempt should run exactly once")
	}
}

func TestSetupWatchUnavailable(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	// newTestApp stubs newWatchSetup to a func returning nil.
	require.Contains(t, a.SetupWatch(), "unavailable", "nil runner")
	// A nil seam entirely must also be safe.
	a.newWatchSetup = nil
	require.Contains(t, a.SetupWatch(), "unavailable", "nil seam")
}
