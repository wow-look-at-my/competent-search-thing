package cli

import (
	"context"
	"errors"
	"io"

	"github.com/spf13/cobra"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/watchsetup"
)

func init() { registerCommand(newSetupWatchCmd) }

// errSetupIncomplete makes `setup-watch` exit nonzero without cobra
// printing its own "Error:" line -- the human-readable reason was
// already written to stdout by watchsetup.Attempt.
var errSetupIncomplete = errors.New("watch setup did not complete")

// newSetupWatchCmd builds the setup-watch subcommand: it enables the
// optimal full-filesystem watch backend by granting the Linux
// capabilities the raw binary lacks (fanotify's CAP_SYS_ADMIN +
// CAP_DAC_READ_SEARCH), prompting for privileges through pkexec. Unlike
// the automatic startup path it never re-execs -- the capabilities stick
// to the binary and take effect at the next launch -- and it ignores the
// decline marker and the watcher.setupEnabled switch, so it always
// retries when the user explicitly asks.
func newSetupWatchCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "setup-watch",
		Short: "Enable full-filesystem watching (grant fanotify capabilities)",
		Long: "setup-watch grants the Linux capabilities (CAP_SYS_ADMIN,\n" +
			"CAP_DAC_READ_SEARCH) that let the app watch the whole filesystem\n" +
			"through fanotify -- full live coverage at negligible memory cost,\n" +
			"instead of the bounded per-directory inotify fallback. You are\n" +
			"prompted for your password (pkexec). The capabilities stick to the\n" +
			"binary, so this is a one-time step; restart the app afterwards.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		// The reason is printed to stdout by Attempt; a returned error
		// only sets the exit code.
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetupWatch(cmd.Context(), e, cmd.OutOrStdout())
		},
	}
}

// runSetupWatch runs the forced setup, defaulting to the production
// watchsetup path when the env seam is unset (tests inject a fake).
func runSetupWatch(ctx context.Context, e *env, out io.Writer) error {
	if e.setupWatch != nil {
		return e.setupWatch(ctx, out)
	}
	cfg, _ := config.Load()
	dir, _ := config.Dir()
	res := watchsetup.New(watchsetup.Config{
		Backend:   cfg.Watcher.Backend,
		Enabled:   config.Enabled(cfg.Watcher.SetupEnabled),
		ConfigDir: dir,
	}).Attempt(ctx, out)
	if res.Action == watchsetup.ActionAttemptFailed {
		return errSetupIncomplete
	}
	return nil
}
