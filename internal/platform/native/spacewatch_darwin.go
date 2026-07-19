//go:build darwin

package native

/*
#cgo LDFLAGS: -framework Cocoa -framework CoreGraphics
#include "platform_darwin.h"
*/
import "C"

import "sync"

// spaceChanged hands active-Space-change notifications from the Cocoa
// observer (main thread) to the drain goroutine -- the csHotkeyFired
// pattern: buffered(1) + non-blocking send, so back-to-back switches
// coalesce and the main run loop is never blocked.
var spaceChanged = make(chan struct{}, 1)

// csSpaceChanged is called by the NSWorkspace observer block on the
// main thread for every active-Space switch. It must never block.
//
//export csSpaceChanged
func csSpaceChanged() {
	select {
	case spaceChanged <- struct{}{}:
	default:
	}
}

// spaceWatchOnce guards the single app-lifetime registration: the app
// arms the dismiss exactly once at Startup, and the observer (plus its
// drain goroutine) deliberately lives until process exit -- there is
// nothing to tear down that would not die with the process anyway.
var (
	spaceWatchOnce sync.Once
	spaceWatchOK   bool
)

// WatchSpaceChanges installs the NSWorkspace active-Space-change
// observer and delivers every switch to onChange on a private
// goroutine. Returns whether the observer is installed; only the
// FIRST caller's onChange is ever wired (the app registers exactly
// once). Compiled by the darwin CI job but never exercised there (no
// GUI run).
func WatchSpaceChanges(onChange func()) bool {
	spaceWatchOnce.Do(func() {
		if C.csObserveSpaceChanges() == 0 {
			return
		}
		spaceWatchOK = true
		go func() {
			for range spaceChanged {
				onChange()
			}
		}()
	})
	return spaceWatchOK
}
