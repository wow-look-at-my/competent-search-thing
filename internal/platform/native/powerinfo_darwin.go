//go:build darwin

package native

/*
#cgo LDFLAGS: -framework Cocoa -framework CoreGraphics
#include "platform_darwin.h"
*/
import "C"

import (
	"sync"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// powerChanged hands power/thermal-state change notifications from the
// Cocoa observers to the drain goroutine -- the csSpaceChanged
// pattern: buffered(1) + non-blocking send, so bursts coalesce and the
// posting thread is never blocked.
var powerChanged = make(chan struct{}, 1)

// csPowerChanged is called by the NSProcessInfo observers for every
// power-state or thermal-state change. It must never block.
//
//export csPowerChanged
func csPowerChanged() {
	select {
	case powerChanged <- struct{}{}:
	default:
	}
}

// powerWatchOnce guards the single app-lifetime registration (the
// spaceWatchOnce pattern): the fps meter arms the observer exactly
// once at Startup, and the observers plus their drain goroutine
// deliberately live until process exit.
var (
	powerWatchOnce sync.Once
	powerWatchOK   bool
)

// DisplayPowerInfo reads the main display's maximum refresh rate plus
// the Low Power Mode and thermal states (macOS 12+ APIs behind
// availability guards in the shim; older systems answer 0Hz/off).
// ok=false when NSProcessInfo itself is unreachable. Compiled by the
// darwin CI job but never exercised there (no GUI run).
func DisplayPowerInfo() (platform.PowerInfo, bool) {
	var maxFPS, lowPower, thermal C.int
	if C.csPowerInfo(&maxFPS, &lowPower, &thermal) == 0 {
		return platform.PowerInfo{}, false
	}
	return platform.PowerInfo{
		MaxFPS:       int(maxFPS),
		LowPowerMode: lowPower != 0,
		ThermalState: int(thermal),
	}, true
}

// WatchPowerChanges installs the NSProcessInfo power/thermal change
// observers and delivers every change to onChange on a private
// goroutine. Returns whether an observer is installed; only the FIRST
// caller's onChange is ever wired (the app registers exactly once).
func WatchPowerChanges(onChange func()) bool {
	powerWatchOnce.Do(func() {
		if C.csObservePowerChanges() == 0 {
			return
		}
		powerWatchOK = true
		go func() {
			for range powerChanged {
				onChange()
			}
		}()
	})
	return powerWatchOK
}
