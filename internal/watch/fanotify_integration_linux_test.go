package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestFanotifyRealIntegration constructs the REAL notifier -- real
// fanotify_init, real FAN_MARK_FILESYSTEM, real handle resolution --
// on a scratch directory. On CI runners (no CAP_SYS_ADMIN) and on
// null-fsid filesystems the constructor fails and the test skips;
// construct-and-fail IS the documented fallback contract, and the
// construction attempt covers the production seams' probe path. On a
// capable machine it must deliver a real create event end-to-end.
func TestFanotifyRealIntegration(t *testing.T) {
	root := t.TempDir()
	nn, err := newFanotifyNotifier([]string{root})
	if err != nil {
		t.Skipf("fanotify unavailable: %v (needs CAP_SYS_ADMIN + a fid-capable filesystem)", err)
	}
	defer nn.Close()

	p := filepath.Join(root, "real.txt")
	require.NoError(t, os.WriteFile(p, nil, 0o644))
	deadline := time.After(15 * time.Second)
	for {
		select {
		case ev, ok := <-nn.Events():
			require.True(t, ok, "notifier closed before the event arrived")
			if ev.Name == p {
				return // the whole pipeline resolved the parent and named the entry
			}
			// Other events inside the scratch dir (temp-dir churn) are
			// possible and fine; everything outside the root is
			// filtered before the channel.
			require.True(t, filepath.Dir(ev.Name) == root || pathWithin(ev.Name, root),
				"events outside the marked scratch root must have been dropped: %s", ev.Name)
		case err := <-nn.Errors():
			t.Fatalf("notifier error: %v", err)
		case <-deadline:
			t.Fatal("no event for the created file")
		}
	}
}
