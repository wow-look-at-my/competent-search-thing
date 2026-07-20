package platform

import "fmt"

// PowerInfo is one display/power-state probe result behind the fps
// meter's context lines (internal/app fps.go): the main display's
// maximum refresh rate plus the two OS states WebKit's frame-pacing
// throttles key on. Filled by internal/platform/native's darwin glue
// (NSScreen.maximumFramesPerSecond + NSProcessInfo, macOS 12+ APIs);
// other platforms have no source and the probe seam stays nil.
type PowerInfo struct {
	// MaxFPS is the main display's maximumFramesPerSecond (120 on
	// ProMotion panels, 60 on most others); 0 when unknown (the
	// selector is unavailable below macOS 12, or there is no screen).
	MaxFPS int
	// LowPowerMode reports NSProcessInfo.lowPowerModeEnabled -- the
	// global macOS Low Power Mode state that makes WebKit cap
	// rendering updates and requestAnimationFrame at 30fps.
	LowPowerMode bool
	// ThermalState is NSProcessInfo.thermalState's raw value (see
	// ThermalStateString); at serious and above WebKit may throttle
	// rendering updates too.
	ThermalState int
}

// ThermalStateString renders an NSProcessInfoThermalState raw value
// for the fps meter's log lines.
func ThermalStateString(s int) string {
	switch s {
	case 0:
		return "nominal"
	case 1:
		return "fair"
	case 2:
		return "serious"
	case 3:
		return "critical"
	default:
		return fmt.Sprintf("unknown(%d)", s)
	}
}

// UncapStatus is the outcome of the WebKit near-60 uncap attempt (the
// PreferPageRenderingUpdatesNear60FPSEnabled feature flip;
// internal/platform/native webkit_darwin.go, consumed by internal/app
// fps.go). Values mirror the CS_UNCAP_* codes in platform_darwin.h.
type UncapStatus int

const (
	// UncapApplied: the feature was found and switched off -- WebKit
	// now paces rendering at the display's real refresh rate.
	UncapApplied UncapStatus = 1
	// UncapNoWindow: the app has no NSWindow yet (too early).
	UncapNoWindow UncapStatus = 0
	// UncapNoWebView: the window exists but no WKWebView subview was
	// found under its content view.
	UncapNoWebView UncapStatus = -1
	// UncapSPIMissing: the WKPreferences feature-list SPI is absent
	// (a future WebKit dropped or renamed it).
	UncapSPIMissing UncapStatus = -2
	// UncapFeatureNotFound: the SPI answered but no feature carries
	// the expected key (renamed upstream).
	UncapFeatureNotFound UncapStatus = -3
	// UncapUnavailable: not a macOS build (the !darwin stub).
	UncapUnavailable UncapStatus = -4
)

// String renders the status for the fps meter's one-line outcome log.
func (s UncapStatus) String() string {
	switch s {
	case UncapApplied:
		return "applied"
	case UncapNoWindow:
		return "no window yet"
	case UncapNoWebView:
		return "no webview found"
	case UncapSPIMissing:
		return "WKPreferences feature SPI unavailable"
	case UncapFeatureNotFound:
		return "feature key not found"
	case UncapUnavailable:
		return "unavailable on this platform"
	default:
		return fmt.Sprintf("unknown status %d", int(s))
	}
}
