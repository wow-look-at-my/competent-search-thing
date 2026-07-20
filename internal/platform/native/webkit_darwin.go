//go:build darwin

package native

/*
#cgo LDFLAGS: -framework Cocoa -framework CoreGraphics -framework WebKit
#include "platform_darwin.h"
*/
import "C"

import "github.com/wow-look-at-my/competent-search-thing/internal/platform"

// WebViewUncapNear60 switches WebKit's stable
// PreferPageRenderingUpdatesNear60FPSEnabled feature OFF on the app's
// WKWebView (private WKPreferences SPI, every touch guarded in the
// shim), so ProMotion panels render at their real refresh rate
// instead of WebKit's near-60 default. The returned status says
// exactly what happened; the caller (internal/app fps.go) logs it
// once. The -framework WebKit link lives here -- the shim's one
// WebKit-touching entry point. Compiled by the darwin CI job but
// never exercised there (no GUI run).
func WebViewUncapNear60() platform.UncapStatus {
	return platform.UncapStatus(C.csWebViewUncapNear60())
}
