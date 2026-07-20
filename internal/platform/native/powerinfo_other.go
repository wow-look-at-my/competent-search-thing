//go:build !darwin

package native

import "github.com/wow-look-at-my/competent-search-thing/internal/platform"

// DisplayPowerInfo reports that no display/power probe exists: only
// macOS has one (see powerinfo_darwin.go). The app's seam is nil off
// darwin, so this stub exists purely so the shared wiring compiles.
func DisplayPowerInfo() (platform.PowerInfo, bool) {
	return platform.PowerInfo{}, false
}

// WatchPowerChanges reports that power-state observation is
// unavailable off macOS (see powerinfo_darwin.go).
func WatchPowerChanges(func()) bool {
	return false
}
