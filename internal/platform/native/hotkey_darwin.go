//go:build darwin

package native

/*
#cgo LDFLAGS: -framework Cocoa -framework CoreGraphics -framework Carbon
#include "platform_darwin.h"
*/
import "C"

import (
	"fmt"
	"sync"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// Carbon Events modifier masks (HIToolbox Events.h).
const (
	carbonCmdKey     = 0x0100 // cmdKey
	carbonShiftKey   = 0x0200 // shiftKey
	carbonOptionKey  = 0x0800 // optionKey
	carbonControlKey = 0x1000 // controlKey
)

// carbonMods maps neutral modifiers to Carbon modifier masks.
var carbonMods = map[platform.Mod]uint32{
	platform.ModCtrl:  carbonControlKey,
	platform.ModShift: carbonShiftKey,
	platform.ModAlt:   carbonOptionKey,
	platform.ModSuper: carbonCmdKey,
}

// carbonKeys maps canonical key tokens (see platform.ParseHotkey) to
// Carbon virtual keycodes (kVK_*, HIToolbox Events.h). The letter and
// digit codes follow the ANSI keyboard LAYOUT, not the alphabet, so
// every entry is spelled out with its kVK_* name -- never "simplify"
// arithmetically. Ordered by keycode to make the layout story visible.
var carbonKeys = map[string]uint32{
	"a":      0,   // kVK_ANSI_A
	"s":      1,   // kVK_ANSI_S
	"d":      2,   // kVK_ANSI_D
	"f":      3,   // kVK_ANSI_F
	"h":      4,   // kVK_ANSI_H
	"g":      5,   // kVK_ANSI_G
	"z":      6,   // kVK_ANSI_Z
	"x":      7,   // kVK_ANSI_X
	"c":      8,   // kVK_ANSI_C
	"v":      9,   // kVK_ANSI_V
	"b":      11,  // kVK_ANSI_B
	"q":      12,  // kVK_ANSI_Q
	"w":      13,  // kVK_ANSI_W
	"e":      14,  // kVK_ANSI_E
	"r":      15,  // kVK_ANSI_R
	"y":      16,  // kVK_ANSI_Y
	"t":      17,  // kVK_ANSI_T
	"1":      18,  // kVK_ANSI_1
	"2":      19,  // kVK_ANSI_2
	"3":      20,  // kVK_ANSI_3
	"4":      21,  // kVK_ANSI_4
	"6":      22,  // kVK_ANSI_6
	"5":      23,  // kVK_ANSI_5
	"9":      25,  // kVK_ANSI_9
	"7":      26,  // kVK_ANSI_7
	"8":      28,  // kVK_ANSI_8
	"0":      29,  // kVK_ANSI_0
	"o":      31,  // kVK_ANSI_O
	"u":      32,  // kVK_ANSI_U
	"i":      34,  // kVK_ANSI_I
	"p":      35,  // kVK_ANSI_P
	"return": 36,  // kVK_Return
	"l":      37,  // kVK_ANSI_L
	"j":      38,  // kVK_ANSI_J
	"k":      40,  // kVK_ANSI_K
	"n":      45,  // kVK_ANSI_N
	"m":      46,  // kVK_ANSI_M
	"tab":    48,  // kVK_Tab
	"space":  49,  // kVK_Space
	"escape": 53,  // kVK_Escape
	"f5":     96,  // kVK_F5
	"f6":     97,  // kVK_F6
	"f7":     98,  // kVK_F7
	"f3":     99,  // kVK_F3
	"f8":     100, // kVK_F8
	"f9":     101, // kVK_F9
	"f11":    103, // kVK_F11
	"f10":    109, // kVK_F10
	"f12":    111, // kVK_F12
	"f4":     118, // kVK_F4
	"f2":     120, // kVK_F2
	"f1":     122, // kVK_F1
	"left":   123, // kVK_LeftArrow
	"right":  124, // kVK_RightArrow
	"down":   125, // kVK_DownArrow
	"up":     126, // kVK_UpArrow
}

// mapCarbonSpec translates a neutral platform.Hotkey into the virtual
// keycode and modifier mask RegisterEventHotKey wants.
func mapCarbonSpec(hk platform.Hotkey) (keyCode, mods uint32, err error) {
	key, ok := carbonKeys[hk.Key]
	if !ok {
		return 0, 0, fmt.Errorf("hotkey: key %q is not mapped on this platform", hk.Key)
	}
	for _, m := range hk.Mods {
		mask, okMod := carbonMods[m]
		if !okMod {
			return 0, 0, fmt.Errorf("hotkey: modifier %q is not mapped on this platform", m)
		}
		mods |= mask
	}
	return key, mods, nil
}

// hotkeyFired hands presses from the Carbon callback (main thread) to
// the drain goroutine. Buffered(1) + non-blocking send: a press that
// arrives while one is already pending coalesces with it, and the main
// run loop is never blocked.
var hotkeyFired = make(chan struct{}, 1)

// hotkeyMu guards hotkeyActive: only ONE Carbon hotkey is supported at
// a time -- the C side keeps a single static registration, and the app
// registers exactly one. A second concurrent StartHotkey errors.
var (
	hotkeyMu     sync.Mutex
	hotkeyActive bool
)

// csHotkeyFired is called by the C-side Carbon event handler on the
// main run loop for every hotkey press. It must never block.
//
//export csHotkeyFired
func csHotkeyFired() {
	select {
	case hotkeyFired <- struct{}{}:
	default:
	}
}

// StartHotkey registers hk as a global macOS hotkey via Carbon
// RegisterEventHotKey and calls onDown on every press. Unlike the old
// golang.design/x/hotkey CGEventTap path this needs NO Accessibility
// (TCC) permission -- RegisterEventHotKey is the mechanism
// Spotlight-like launcher apps use. Press events arrive through a
// Carbon event handler on the main run loop (which Wails runs) and are
// delivered to onDown on a private goroutine. Errors when the spec
// cannot be mapped, when registration fails (e.g. the combination is
// taken system-wide), or when a hotkey is already registered (only one
// is supported at a time). The returned stop function is idempotent.
// Compiled by the darwin CI job but never exercised there (no GUI
// run).
func StartHotkey(hk platform.Hotkey, onDown func()) (func(), error) {
	keyCode, mods, err := mapCarbonSpec(hk)
	if err != nil {
		return nil, err
	}
	hotkeyMu.Lock()
	defer hotkeyMu.Unlock()
	if hotkeyActive {
		return nil, fmt.Errorf("hotkey: a Carbon hotkey is already registered (only one is supported)")
	}
	// Drop any press a previous registration left buffered so the new
	// one never delivers a stale onDown.
	select {
	case <-hotkeyFired:
	default:
	}
	// The registration hops to the main thread synchronously
	// (runOnMain/dispatch_sync). StartHotkey runs on a startup
	// goroutine that races the start of the Cocoa main loop; the hop
	// simply parks until [NSApp run] begins pumping milliseconds
	// later, then resolves.
	if C.csRegisterHotkey(C.uint32_t(keyCode), C.uint32_t(mods)) == 0 {
		return nil, fmt.Errorf("hotkey: carbon RegisterEventHotKey failed for %s", hk)
	}
	hotkeyActive = true
	stopc := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopc:
				return
			case <-hotkeyFired:
				onDown()
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stopc)
			// Asynchronous by design (dispatch_async in the shim):
			// stop runs during shutdown, when the main loop may
			// already be stopping, and must never block on it.
			C.csUnregisterHotkey()
			hotkeyMu.Lock()
			hotkeyActive = false
			hotkeyMu.Unlock()
		})
	}, nil
}
