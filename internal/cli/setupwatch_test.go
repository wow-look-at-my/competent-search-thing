package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// setupWatchEnv builds a CLI env whose setup-watch action is the given
// fake, so the tests never touch the real watchsetup path (which would
// probe fanotify and spawn pkexec).
func setupWatchEnv(fake func(context.Context, io.Writer) error) *env {
	return &env{
		version:    testVersion,
		runGUI:     func(RunOptions) error { return nil },
		setupWatch: fake,
	}
}

func TestSetupWatchSuccess(t *testing.T) {
	called := false
	e := setupWatchEnv(func(_ context.Context, w io.Writer) error {
		called = true
		fmt.Fprintln(w, "Done -- full-filesystem watching is enabled.")
		return nil
	})
	// The GUI must never boot for a setup-watch invocation.
	e.runGUI = func(RunOptions) error { t.Fatal("setup-watch must not boot the GUI"); return nil }

	var out, errOut bytes.Buffer
	code := executeEnv(e, []string{"setup-watch"}, &out, &errOut)
	require.Equal(t, 0, code)
	require.True(t, called, "the setup action must run")
	require.Contains(t, out.String(), "full-filesystem watching is enabled")
}

func TestSetupWatchFailureExitsNonzero(t *testing.T) {
	e := setupWatchEnv(func(_ context.Context, w io.Writer) error {
		fmt.Fprintln(w, "Setup did not complete: the request was dismissed")
		return errSetupIncomplete
	})
	var out, errOut bytes.Buffer
	code := executeEnv(e, []string{"setup-watch"}, &out, &errOut)
	require.Equal(t, 1, code, "a failed setup exits nonzero")
	require.Contains(t, out.String(), "did not complete")
	// SilenceErrors: cobra must not add its own "Error:" line on top of
	// the human-readable reason Attempt already printed.
	require.NotContains(t, errOut.String(), "Error:")
}

func TestSetupWatchListedInHelp(t *testing.T) {
	gui := &guiRecorder{}
	code, stdout, _ := run(t, gui, "--help")
	require.Equal(t, 0, code)
	require.Contains(t, stdout, "setup-watch")
	require.Equal(t, 0, gui.count(), "help never boots the GUI")
}
