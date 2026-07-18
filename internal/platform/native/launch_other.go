//go:build !linux

package native

import (
	"errors"
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/launch"
)

// The credentialed launch path is linux-only glue (macOS and Windows
// activate launched apps natively); these stubs keep the app seams
// wireable everywhere. The app layer gates on goos and never calls
// them off linux.

// MintLaunchCredential reports no credential off linux.
func MintLaunchCredential(time.Duration) launch.Credential {
	return launch.Credential{Kind: launch.KindNone}
}

// PrepareLaunch is a no-op off linux.
func PrepareLaunch() {}

// ResolveHandler resolves nothing off linux.
func ResolveHandler(launch.Target) (launch.Handler, bool) {
	return launch.Handler{}, false
}

// HandlerByDesktopID resolves nothing off linux.
func HandlerByDesktopID(string) (launch.Handler, bool) {
	return launch.Handler{}, false
}

// WatchState reports no X state off linux.
func WatchState() (launch.XState, bool) {
	return launch.XState{}, false
}

// RemoveStartupSequence has no X sequences to reap off linux.
func RemoveStartupSequence(string) error {
	return errors.New("startup sequences are an X11 concept")
}
