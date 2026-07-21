// C interface of the small Cocoa/Carbon shim behind display_darwin.go,
// movewindow_darwin.go, appsource_darwin.go, hotkey_darwin.go,
// panel_darwin.go and appicon_darwin.go. All coordinates crossing this
// boundary are top-left-origin virtual-desktop pixels (the Go side's
// convention); the .m implementation converts from/to Cocoa's
// bottom-left-origin global coordinates.
#ifndef CS_PLATFORM_DARWIN_H
#define CS_PLATFORM_DARWIN_H

#include <stdint.h>

typedef struct {
	double x, y, w, h;     // full frame
	double wx, wy, ww, wh; // visible frame (menu bar / dock excluded)
	int primary;
} csDisplay;

// csCursorPos writes the mouse location; returns 1 on success, 0 on failure.
int csCursorPos(double *x, double *y);

// csGetDisplays fills out with up to max displays; returns the count
// written (0 on failure).
int csGetDisplays(csDisplay *out, int max);

// csMoveWindow moves the app's first window so its top-left corner sits
// at (x, y); returns 1 on success, 0 when there is no window.
int csMoveWindow(double x, double y);

// csAppInfo describes one running application. Strings are UTF-8 and
// always NUL-terminated (truncated to fit).
typedef struct {
	char name[256];  // localizedName
	char exe[1024];  // executable path (bundle path when unavailable)
	int pid;
} csAppInfo;

// csFrontmostApp fills out with the frontmost application; returns 1
// on success, 0 when there is none.
int csFrontmostApp(csAppInfo *out);

// csRunningApps fills out with up to max applications that have a
// regular activation policy (i.e. appear in the Dock and app
// switcher); returns the count written (0 on failure).
int csRunningApps(csAppInfo *out, int max);

// csRegisterHotkey installs (once) the application-level Carbon event
// handler for hotkey presses and registers keyCode (a kVK_* virtual
// keycode) + carbonMods (a cmdKey/shiftKey/optionKey/controlKey mask)
// as the process's single global hotkey; every press calls the Go
// export csHotkeyFired. Synchronous (main-thread hop); returns 1 on
// success, 0 on any failure.
int csRegisterHotkey(uint32_t keyCode, uint32_t carbonMods);

// csUnregisterHotkey removes the registered hotkey, asynchronously (it
// runs during shutdown and must never block on a stopping main loop)
// and idempotently.
void csUnregisterHotkey(void);

// csConfigurePanel applies Spotlight-style panel behavior to the app's
// first window (join all Spaces, show over fullscreen apps, stay out
// of the window cycle, never hide on app deactivation); returns 1 on
// success, 0 when there is no window yet.
int csConfigurePanel(void);

// csSetDockIcon sets the application's Dock/Cmd-Tab icon from size x
// size PREMULTIPLIED-alpha RGBA pixels (NSBitmapImageRep's default
// interpretation); the pixels are copied before returning. Returns 1
// on success.
int csSetDockIcon(const uint8_t *rgba, int size);

// csObserveSpaceChanges installs (once, idempotently) the NSWorkspace
// observer for active-Space changes; every switch calls the Go export
// csSpaceChanged. Returns 1 once the observer is installed.
int csObserveSpaceChanges(void);

// csPowerInfo fills the display/power state behind the fps meter's
// context lines: maxFPS = the main screen's maximumFramesPerSecond
// (0 when the selector is unavailable -- macOS < 12 -- or there is no
// screen), lowPower = 1 while macOS Low Power Mode is active (0 when
// off or unreadable), thermal = NSProcessInfo.thermalState's raw
// value. Returns 1 on success (NSProcessInfo reachable), 0 otherwise.
int csPowerInfo(int *maxFPS, int *lowPower, int *thermal);

// csObservePowerChanges installs (once, idempotently) observers for
// NSProcessInfo's power-state and thermal-state change notifications;
// every change calls the Go export csPowerChanged. Returns 1 once the
// observers are installed.
int csObservePowerChanges(void);

// csWebViewUncapNear60 status codes (mirrored by
// internal/platform.UncapStatus -- keep in lockstep).
#define CS_UNCAP_APPLIED 1
#define CS_UNCAP_NO_WINDOW 0
#define CS_UNCAP_NO_WEBVIEW (-1)
#define CS_UNCAP_SPI_MISSING (-2)
#define CS_UNCAP_FEATURE_NOT_FOUND (-3)

// csWebViewUncapNear60 switches WebKit's stable
// PreferPageRenderingUpdatesNear60FPSEnabled feature OFF on the app's
// WKWebView (found as a subview of the first window's content view --
// the same first-window assumption csMoveWindow/csConfigurePanel
// rely on), so a ProMotion panel renders at its real refresh rate
// instead of the near-60 default. Every SPI touch is
// respondsToSelector-guarded; the return value reports exactly what
// happened (CS_UNCAP_*). Synchronous (main-thread hop).
int csWebViewUncapNear60(void);

// csAppIconPNG renders the icon macOS itself displays for the file or
// bundle at path -- [NSWorkspace iconForFile:], the same image
// Launchpad/Finder/the Dock show, Assets.car asset catalogs included
// -- into a size x size PNG. Returns a malloc'd buffer the caller
// must free() and writes its length to *outLen; NULL on any failure
// (missing path, no image, encode failure). Deliberately NO
// main-thread hop, unlike the window/screen calls above: NSWorkspace
// icon lookup and offscreen NSImage drawing are thread-safe (NSImage
// since macOS 10.6 per the AppKit release notes), the caller is the
// app's icon-resolution goroutine (bulk rasterization must never
// serialize onto the UI thread), and the darwin unit-test binary that
// exercises this has no pumped main queue -- a dispatch_sync there
// would deadlock.
void *csAppIconPNG(const char *path, int size, int *outLen);

#endif
