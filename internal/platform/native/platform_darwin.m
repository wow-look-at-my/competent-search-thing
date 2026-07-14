// Cocoa shim for display_darwin.go / movewindow_darwin.go. Kept
// minimal and conventional: CI compiles linux/amd64 only, so nothing
// here is exercised before a real macOS build.
#import <Cocoa/Cocoa.h>
#import <CoreGraphics/CoreGraphics.h>

#include "platform_darwin.h"

// runOnMain executes block on the main thread (AppKit requirement for
// NSScreen/NSWindow), synchronously, without deadlocking when already
// on it.
static void runOnMain(void (^block)(void)) {
	if ([NSThread isMainThread]) {
		block();
	} else {
		dispatch_sync(dispatch_get_main_queue(), block);
	}
}

int csCursorPos(double *x, double *y) {
	// CGEventGetLocation is already top-left-origin global coordinates.
	CGEventRef e = CGEventCreate(NULL);
	if (e == NULL) {
		return 0;
	}
	CGPoint p = CGEventGetLocation(e);
	CFRelease(e);
	*x = p.x;
	*y = p.y;
	return 1;
}

int csGetDisplays(csDisplay *out, int max) {
	__block int n = 0;
	runOnMain(^{
		NSArray<NSScreen *> *screens = [NSScreen screens];
		if (screens == nil || screens.count == 0) {
			return;
		}
		// The first screen owns the menu bar and the Cocoa global
		// origin: its frame is anchored at (0,0), y growing upward.
		// Converting a Cocoa rect to top-left-origin coordinates flips
		// around the primary screen's height.
		double primaryH = screens[0].frame.size.height;
		for (NSUInteger i = 0; i < screens.count && n < max; i++) {
			NSRect f = screens[i].frame;
			NSRect v = screens[i].visibleFrame;
			out[n].x = f.origin.x;
			out[n].y = primaryH - (f.origin.y + f.size.height);
			out[n].w = f.size.width;
			out[n].h = f.size.height;
			out[n].wx = v.origin.x;
			out[n].wy = primaryH - (v.origin.y + v.size.height);
			out[n].ww = v.size.width;
			out[n].wh = v.size.height;
			out[n].primary = (i == 0) ? 1 : 0;
			n++;
		}
	});
	return n;
}

int csMoveWindow(double x, double y) {
	__block int ok = 0;
	runOnMain(^{
		NSArray<NSWindow *> *windows = [NSApp windows];
		if (windows == nil || windows.count == 0) {
			return;
		}
		NSWindow *w = windows[0];
		NSArray<NSScreen *> *screens = [NSScreen screens];
		if (screens == nil || screens.count == 0) {
			return;
		}
		double primaryH = screens[0].frame.size.height;
		// (x, y) is the desired top-left corner in top-left-origin
		// coordinates; setFrameOrigin wants the bottom-left corner in
		// Cocoa coordinates.
		double cocoaY = primaryH - y - w.frame.size.height;
		[w setFrameOrigin:NSMakePoint(x, cocoaY)];
		ok = 1;
	});
	return ok;
}
