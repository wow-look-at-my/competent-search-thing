//go:build !darwin

package native

import "github.com/wow-look-at-my/competent-search-thing/internal/platform"

// WebViewUncapNear60 reports that the WebKit near-60 uncap does not
// exist off macOS (see webkit_darwin.go); the app's seam is nil off
// darwin, so this stub exists purely so the shared wiring compiles.
func WebViewUncapNear60() platform.UncapStatus {
	return platform.UncapUnavailable
}
