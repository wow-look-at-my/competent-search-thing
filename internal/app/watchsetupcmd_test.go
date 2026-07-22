package app

import (
	"context"
	"io"
	"strings"
	"testing"

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
		got := a.SetupWatch()
		if !strings.Contains(got, c.want) {
			t.Fatalf("action %v reason %q: got %q, want substring %q", c.res.Action, c.res.Reason, got, c.want)
		}
		if fake.called != 1 {
			t.Fatalf("Attempt should run exactly once, got %d", fake.called)
		}
	}
}

func TestSetupWatchUnavailable(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	// newTestApp stubs newWatchSetup to a func returning nil.
	if got := a.SetupWatch(); !strings.Contains(got, "unavailable") {
		t.Fatalf("nil runner: got %q", got)
	}
	// A nil seam entirely must also be safe.
	a.newWatchSetup = nil
	if got := a.SetupWatch(); !strings.Contains(got, "unavailable") {
		t.Fatalf("nil seam: got %q", got)
	}
}
