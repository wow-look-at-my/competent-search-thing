//go:build linux

package native

import (
	"errors"
	"fmt"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// X11 core-protocol modifier masks (X.h).
const (
	x11Shift   uint16 = 1 << 0
	x11Lock    uint16 = 1 << 1 // CapsLock
	x11Control uint16 = 1 << 2
	x11Mod1    uint16 = 1 << 3 // Alt
	x11Mod2    uint16 = 1 << 4 // usually NumLock
	x11Mod4    uint16 = 1 << 6 // Super/Win
)

// x11Mods maps neutral modifiers to X11 masks.
var x11Mods = map[platform.Mod]uint16{
	platform.ModCtrl:  x11Control,
	platform.ModShift: x11Shift,
	platform.ModAlt:   x11Mod1,
	platform.ModSuper: x11Mod4,
}

// x11Keysyms maps canonical key tokens to X11 keysyms (keysymdef.h).
var x11Keysyms = buildX11Keysyms()

func buildX11Keysyms() map[string]uint32 {
	m := map[string]uint32{
		"space":  0x0020, // XK_space
		"tab":    0xff09, // XK_Tab
		"return": 0xff0d, // XK_Return
		"escape": 0xff1b, // XK_Escape
		"left":   0xff51, // XK_Left
		"up":     0xff52, // XK_Up
		"right":  0xff53, // XK_Right
		"down":   0xff54, // XK_Down
	}
	for c := 'a'; c <= 'z'; c++ {
		m[string(c)] = uint32(c) // XK_a..XK_z are the ASCII codes
	}
	for c := '0'; c <= '9'; c++ {
		m[string(c)] = uint32(c) // XK_0..XK_9 likewise
	}
	for i := uint32(1); i <= 12; i++ {
		m[fmt.Sprintf("f%d", i)] = 0xffbe + i - 1 // XK_F1..XK_F12
	}
	return m
}

// StartHotkey registers hk as a global X11 hotkey and calls onDown from
// a background goroutine on every key press (X autorepeat can fire it
// repeatedly while the key is held; callers rate-limit). The returned
// stop function releases the grab. Registration fails -- and the app
// keeps running without a hotkey -- when there is no X display (e.g. a
// Wayland-only session), when the key has no keycode on the current
// keyboard, or when another client already grabbed the combination.
//
// Implementation notes: this is a pure-Go XGrabKey on the root window
// via jezek/xgb. The combination is grabbed in four variants (with and
// without CapsLock/NumLock) because X grabs match the exact modifier
// state. GrabModeAsync keeps normal event flow; owner_events=false
// reports events against the root window.
func StartHotkey(hk platform.Hotkey, onDown func()) (stop func(), err error) {
	sym, found := x11Keysyms[hk.Key]
	if !found {
		return nil, fmt.Errorf("hotkey: key %q is not mapped on linux", hk.Key)
	}
	var mods uint16
	for _, m := range hk.Mods {
		bit, foundMod := x11Mods[m]
		if !foundMod {
			return nil, fmt.Errorf("hotkey: modifier %q is not mapped on linux", m)
		}
		mods |= bit
	}

	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("hotkey: connecting to the X server (Wayland-only sessions have none): %w", err)
	}
	defer func() {
		if err != nil {
			conn.Close()
		}
	}()

	setup := xproto.Setup(conn)
	root := setup.DefaultScreen(conn).Root
	keycode, err := keycodeFor(conn, setup, sym)
	if err != nil {
		return nil, err
	}

	variants := []uint16{mods, mods | x11Lock, mods | x11Mod2, mods | x11Lock | x11Mod2}
	for i, v := range variants {
		gerr := xproto.GrabKeyChecked(conn, false, root, v, keycode,
			xproto.GrabModeAsync, xproto.GrabModeAsync).Check()
		if gerr != nil && i == 0 {
			// The bare combination failed: taken by another client.
			// Losing only a CapsLock/NumLock variant is tolerable.
			return nil, fmt.Errorf("hotkey: grabbing %s failed (already taken by another application?): %w", hk, gerr)
		}
	}

	go func() {
		for {
			ev, xerr := conn.WaitForEvent()
			if ev == nil && xerr == nil {
				return // connection closed by stop (or lost)
			}
			if xerr != nil {
				continue
			}
			if kp, isPress := ev.(xproto.KeyPressEvent); isPress && kp.Detail == keycode {
				onDown()
			}
		}
	}()
	return conn.Close, nil
}

// keycodeFor resolves an X11 keysym to a keycode using the server's
// keyboard mapping, preferring an unshifted (first-column) match.
func keycodeFor(conn *xgb.Conn, setup *xproto.SetupInfo, sym uint32) (xproto.Keycode, error) {
	first, last := setup.MinKeycode, setup.MaxKeycode
	rep, err := xproto.GetKeyboardMapping(conn, first, byte(last-first+1)).Reply()
	if err != nil {
		return 0, fmt.Errorf("hotkey: reading the keyboard mapping: %w", err)
	}
	per := int(rep.KeysymsPerKeycode)
	if per <= 0 {
		return 0, errors.New("hotkey: server returned an empty keyboard mapping")
	}
	for _, columns := range [][2]int{{0, 1}, {1, per}} { // unshifted first, then the rest
		for kc := int(first); kc <= int(last); kc++ {
			base := (kc - int(first)) * per
			for col := columns[0]; col < columns[1]; col++ {
				i := base + col
				if i < len(rep.Keysyms) && uint32(rep.Keysyms[i]) == sym {
					return xproto.Keycode(kc), nil
				}
			}
		}
	}
	return 0, fmt.Errorf("hotkey: keysym 0x%x has no keycode on this keyboard layout", sym)
}
