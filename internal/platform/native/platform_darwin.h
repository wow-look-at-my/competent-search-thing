// C interface of the small Cocoa shim behind display_darwin.go and
// movewindow_darwin.go. All coordinates crossing this boundary are
// top-left-origin virtual-desktop pixels (the Go side's convention);
// the .m implementation converts from/to Cocoa's bottom-left-origin
// global coordinates.
#ifndef CS_PLATFORM_DARWIN_H
#define CS_PLATFORM_DARWIN_H

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

#endif
