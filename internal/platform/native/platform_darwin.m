// Cocoa/Carbon shim for display_darwin.go / movewindow_darwin.go /
// appsource_darwin.go / hotkey_darwin.go / panel_darwin.go. Kept
// minimal and conventional: CI compiles this (darwin job) but never
// runs it, so nothing here is exercised before a real macOS session.
#import <Cocoa/Cocoa.h>
#import <CoreGraphics/CoreGraphics.h>
#import <Carbon/Carbon.h>
#import <WebKit/WebKit.h>

#include <string.h>

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

int csConfigurePanel(void) {
	__block int ok = 0;
	runOnMain(^{
		// Same window selection as csMoveWindow: the app's first (and
		// only) NSWindow.
		NSArray<NSWindow *> *windows = [NSApp windows];
		if (windows == nil || windows.count == 0) {
			return;
		}
		NSWindow *w = windows[0];
		// Spotlight-style panel behavior. Wails only sets the floating
		// window level; without CanJoinAllSpaces a hidden always-on-top
		// window orders back in on the Space it was created on -- not
		// the one the user is looking at -- and without
		// FullScreenAuxiliary it cannot appear over fullscreen apps.
		// IgnoresCycle keeps the panel out of the window cycle, and
		// hidesOnDeactivate stays off because the frontend's blur
		// handler owns hiding.
		w.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces
			| NSWindowCollectionBehaviorFullScreenAuxiliary
			| NSWindowCollectionBehaviorIgnoresCycle;
		[w setHidesOnDeactivate:NO];
		ok = 1;
	});
	return ok;
}

int csSetDockIcon(const uint8_t *rgba, int size) {
	__block int ok = 0;
	runOnMain(^{
		// A fresh rep owning its own buffer (planes NULL allocates);
		// the caller's pixels are copied in, so the Go slice may be
		// collected the moment this returns. hasAlpha without an
		// explicit bitmapFormat means PREMULTIPLIED alpha -- the
		// contract in the header.
		NSBitmapImageRep *rep = [[NSBitmapImageRep alloc]
			initWithBitmapDataPlanes:NULL
			              pixelsWide:size
			              pixelsHigh:size
			           bitsPerSample:8
			         samplesPerPixel:4
			                hasAlpha:YES
			                isPlanar:NO
			          colorSpaceName:NSCalibratedRGBColorSpace
			             bytesPerRow:(NSInteger)size * 4
			            bitsPerPixel:32];
		if (rep == nil || rep.bitmapData == NULL) {
			return;
		}
		memcpy(rep.bitmapData, rgba, (size_t)size * (size_t)size * 4);
		NSImage *img = [[NSImage alloc] initWithSize:NSMakeSize(size, size)];
		[img addRepresentation:rep];
		[NSApp setApplicationIconImage:img];
		ok = 1;
	});
	return ok;
}

// --- active-Space change observation (spacewatch_darwin.go) ---

// csSpaceChanged is exported from Go (spacewatch_darwin.go); cgo emits
// the definition, this file only references it.
extern void csSpaceChanged(void);

// The installed observer token. Retained manually (this file compiles
// without ARC) and deliberately never removed: the observation is
// app-lifetime, matching the Go side's forever-drain goroutine.
static id csSpaceObserver = nil;

int csObserveSpaceChanges(void) {
	__block int ok = 0;
	runOnMain(^{
		if (csSpaceObserver != nil) {
			ok = 1; // already observing
			return;
		}
		id token = [[[NSWorkspace sharedWorkspace] notificationCenter]
			addObserverForName:NSWorkspaceActiveSpaceDidChangeNotification
			            object:nil
			             queue:nil
			        usingBlock:^(NSNotification *note) {
			            (void)note;
			            csSpaceChanged();
			        }];
		if (token == nil) {
			return;
		}
		csSpaceObserver = [token retain];
		ok = 1;
	});
	return ok;
}

// fillAppInfo copies one NSRunningApplication into the C struct.
// Every accessor is nil-tolerant (messaging nil yields nil/NULL) and
// the memset guarantees NUL termination after the bounded copies.
static void fillAppInfo(NSRunningApplication *app, csAppInfo *out) {
	memset(out, 0, sizeof(*out));
	const char *cname = app.localizedName.UTF8String;
	if (cname != NULL) {
		strncpy(out->name, cname, sizeof(out->name) - 1);
	}
	NSURL *url = app.executableURL;
	if (url == nil) {
		url = app.bundleURL;
	}
	const char *cpath = url.path.UTF8String;
	if (cpath != NULL) {
		strncpy(out->exe, cpath, sizeof(out->exe) - 1);
	}
	out->pid = (int)app.processIdentifier;
}

int csFrontmostApp(csAppInfo *out) {
	__block int ok = 0;
	runOnMain(^{
		NSRunningApplication *app = [NSWorkspace sharedWorkspace].frontmostApplication;
		if (app == nil) {
			return;
		}
		fillAppInfo(app, out);
		ok = 1;
	});
	return ok;
}

int csRunningApps(csAppInfo *out, int max) {
	__block int n = 0;
	runOnMain(^{
		NSArray<NSRunningApplication *> *apps = [NSWorkspace sharedWorkspace].runningApplications;
		if (apps == nil) {
			return;
		}
		for (NSUInteger i = 0; i < apps.count && n < max; i++) {
			NSRunningApplication *app = apps[i];
			// Regular activation policy = shows in the Dock and app
			// switcher; skips daemons, status items, and helpers.
			if (app.activationPolicy != NSApplicationActivationPolicyRegular) {
				continue;
			}
			fillAppInfo(app, &out[n]);
			n++;
		}
	});
	return n;
}

// --- Carbon global hotkey (hotkey_darwin.go) ---
//
// RegisterEventHotKey needs no Accessibility/TCC permission, unlike a
// CGEventTap. Events arrive through the Carbon event dispatcher, which
// [NSApp run] pumps as part of the main run loop.

// csHotkeyFired is exported from Go (hotkey_darwin.go); cgo emits the
// definition, this file only references it.
extern void csHotkeyFired(void);

// One registration at a time (the Go side guards): the installed
// handler and the active hotkey are main-thread-only static state.
static EventHandlerRef csHotkeyHandler = NULL;
static EventHotKeyRef csHotkeyRef = NULL;

// csHotkeyCallback runs on the main run loop for every press of a
// hotkey registered by this process -- only ever the single one
// csRegisterHotkey installed, so no id dispatch is needed.
static OSStatus csHotkeyCallback(EventHandlerCallRef next, EventRef event, void *userData) {
	csHotkeyFired();
	return noErr;
}

int csRegisterHotkey(uint32_t keyCode, uint32_t carbonMods) {
	__block int ok = 0;
	runOnMain(^{
		if (csHotkeyRef != NULL) {
			// Already holding a registration; the Go side prevents
			// this, but never stack a second one.
			return;
		}
		if (csHotkeyHandler == NULL) {
			EventTypeSpec spec;
			spec.eventClass = kEventClassKeyboard;
			spec.eventKind = kEventHotKeyPressed;
			OSStatus st = InstallEventHandler(GetEventDispatcherTarget(),
				NewEventHandlerUPP(csHotkeyCallback), 1, &spec, NULL,
				&csHotkeyHandler);
			if (st != noErr) {
				csHotkeyHandler = NULL;
				return;
			}
		}
		EventHotKeyID hkID;
		// 'CSTH' as a numeric FourCharCode: multi-character char
		// constants trip -Wfour-char-constants.
		hkID.signature = (OSType)0x43535448; // 'CSTH'
		hkID.id = 1;
		EventHotKeyRef ref = NULL;
		OSStatus st = RegisterEventHotKey(keyCode, carbonMods, hkID,
			GetEventDispatcherTarget(), 0, &ref);
		if (st != noErr || ref == NULL) {
			return;
		}
		csHotkeyRef = ref;
		ok = 1;
	});
	return ok;
}

void csUnregisterHotkey(void) {
	// Async on purpose: this runs during shutdown, when the main run
	// loop may already be stopping, and a synchronous hop could block
	// forever. Idempotent: without a registration the block no-ops.
	dispatch_async(dispatch_get_main_queue(), ^{
		if (csHotkeyRef != NULL) {
			UnregisterEventHotKey(csHotkeyRef);
			csHotkeyRef = NULL;
		}
	});
}

// --- display/power probe (powerinfo_darwin.go) ---

// csPowerChanged is exported from Go (powerinfo_darwin.go); cgo emits
// the definition, this file only references it.
extern void csPowerChanged(void);

// The installed observer tokens. Retained manually (this file compiles
// without ARC) and deliberately never removed: the observation is
// app-lifetime, matching the Go side's forever-drain goroutine (the
// csSpaceObserver pattern).
static id csPowerObserver = nil;
static id csThermalObserver = nil;

int csPowerInfo(int *maxFPS, int *lowPower, int *thermal) {
	*maxFPS = 0;
	*lowPower = 0;
	*thermal = 0;
	__block int ok = 0;
	runOnMain(^{
		NSProcessInfo *info = [NSProcessInfo processInfo];
		if (info == nil) {
			return;
		}
		// thermalState exists since macOS 10.10.3; Low Power Mode and
		// maximumFramesPerSecond arrived on macOS 12 -- older systems
		// keep the defaults (off / 0 = unknown).
		*thermal = (int)info.thermalState;
		if (@available(macOS 12.0, *)) {
			*lowPower = info.lowPowerModeEnabled ? 1 : 0;
			NSScreen *screen = [NSScreen mainScreen];
			if (screen != nil) {
				*maxFPS = (int)screen.maximumFramesPerSecond;
			}
		}
		ok = 1;
	});
	return ok;
}

int csObservePowerChanges(void) {
	__block int ok = 0;
	runOnMain(^{
		if (csPowerObserver != nil || csThermalObserver != nil) {
			ok = 1; // already observing
			return;
		}
		NSNotificationCenter *nc = [NSNotificationCenter defaultCenter];
		// The blocks only poke a buffered Go channel (csPowerChanged
		// never blocks), so queue:nil -- deliver on the posting thread
		// -- is safe.
		if (@available(macOS 12.0, *)) {
			id token = [nc addObserverForName:NSProcessInfoPowerStateDidChangeNotification
			                           object:nil
			                            queue:nil
			                       usingBlock:^(NSNotification *note) {
			                           (void)note;
			                           csPowerChanged();
			                       }];
			if (token != nil) {
				csPowerObserver = [token retain];
				ok = 1;
			}
		}
		id token = [nc addObserverForName:NSProcessInfoThermalStateDidChangeNotification
		                           object:nil
		                            queue:nil
		                       usingBlock:^(NSNotification *note) {
		                           (void)note;
		                           csPowerChanged();
		                       }];
		if (token != nil) {
			csThermalObserver = [token retain];
			ok = 1;
		}
	});
	return ok;
}

// --- WebKit near-60 uncap (webkit_darwin.go) ---
//
// WKPreferences' feature-list SPI, verified against WebKit
// WKPreferencesPrivate.h: +_features (macOS 13.3+) paired with
// -_setEnabled:forFeature:, plus the older
// +_experimentalFeatures/+_internalDebugFeatures splits. Declared as
// a category so the compiler emits ordinary (BOOL, id) calls; every
// use below is respondsToSelector-gated, so a WebKit that drops or
// renames the SPI degrades to an honest status code, never a crash.
@interface WKPreferences (CSFeatureSPI)
+ (NSArray *)_features;
+ (NSArray *)_experimentalFeatures;
+ (NSArray *)_internalDebugFeatures;
- (void)_setEnabled:(BOOL)value forFeature:(id)feature;
- (void)_setEnabled:(BOOL)value forExperimentalFeature:(id)feature;
- (void)_setEnabled:(BOOL)value forInternalDebugFeature:(id)feature;
@end

// csFindFeature returns the feature object whose key equals want, or
// nil. _WKFeature.key is read through its plain getter, guarded, so
// unexpected list members are skipped instead of crashing.
static id csFindFeature(NSArray *features, NSString *want) {
	if (features == nil) {
		return nil;
	}
	for (id f in features) {
		if (![f respondsToSelector:@selector(key)]) {
			continue;
		}
		id key = [f performSelector:@selector(key)];
		if (key != nil && [key isKindOfClass:[NSString class]] && [want isEqualToString:(NSString *)key]) {
			return f;
		}
	}
	return nil;
}

int csWebViewUncapNear60(void) {
	__block int result = CS_UNCAP_NO_WINDOW;
	runOnMain(^{
		// Same first-window selection as csMoveWindow/csConfigurePanel;
		// wails adds the WKWebView as a direct subview of its content
		// view (verified in the v2.13.0 darwin frontend sources).
		NSArray<NSWindow *> *windows = [NSApp windows];
		if (windows == nil || windows.count == 0) {
			result = CS_UNCAP_NO_WINDOW;
			return;
		}
		WKWebView *webview = nil;
		for (NSView *v in windows[0].contentView.subviews) {
			if ([v isKindOfClass:[WKWebView class]]) {
				webview = (WKWebView *)v;
				break;
			}
		}
		if (webview == nil || webview.configuration == nil || webview.configuration.preferences == nil) {
			result = CS_UNCAP_NO_WEBVIEW;
			return;
		}
		WKPreferences *prefs = webview.configuration.preferences;
		NSString *want = @"PreferPageRenderingUpdatesNear60FPSEnabled";
		int sawSPI = 0;
		if ([WKPreferences respondsToSelector:@selector(_features)]
			&& [prefs respondsToSelector:@selector(_setEnabled:forFeature:)]) {
			sawSPI = 1;
			id f = csFindFeature([WKPreferences _features], want);
			if (f != nil) {
				[prefs _setEnabled:NO forFeature:f];
				result = CS_UNCAP_APPLIED;
				return;
			}
		}
		if ([WKPreferences respondsToSelector:@selector(_experimentalFeatures)]
			&& [prefs respondsToSelector:@selector(_setEnabled:forExperimentalFeature:)]) {
			sawSPI = 1;
			id f = csFindFeature([WKPreferences _experimentalFeatures], want);
			if (f != nil) {
				[prefs _setEnabled:NO forExperimentalFeature:f];
				result = CS_UNCAP_APPLIED;
				return;
			}
		}
		if ([WKPreferences respondsToSelector:@selector(_internalDebugFeatures)]
			&& [prefs respondsToSelector:@selector(_setEnabled:forInternalDebugFeature:)]) {
			sawSPI = 1;
			id f = csFindFeature([WKPreferences _internalDebugFeatures], want);
			if (f != nil) {
				[prefs _setEnabled:NO forInternalDebugFeature:f];
				result = CS_UNCAP_APPLIED;
				return;
			}
		}
		result = sawSPI ? CS_UNCAP_FEATURE_NOT_FOUND : CS_UNCAP_SPI_MISSING;
	});
	return result;
}
