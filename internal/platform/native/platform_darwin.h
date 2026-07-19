// C interface of the small Cocoa/Carbon shim behind display_darwin.go,
// movewindow_darwin.go, appsource_darwin.go, hotkey_darwin.go and
// panel_darwin.go. All coordinates crossing this boundary are
// top-left-origin virtual-desktop pixels (the Go side's convention);
// the .m implementation converts from/to Cocoa's bottom-left-origin
// global coordinates.
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

#endif
