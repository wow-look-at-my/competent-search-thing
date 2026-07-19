//go:build windows

package native

import (
	"fmt"
	"sync"

	"golang.design/x/hotkey"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// startLibHotkey registers a hotkey through golang.design/x/hotkey
// (Windows only: RegisterHotKey. The Linux backend of that library is
// avoided on purpose, see the package comment, and macOS moved to
// Carbon RegisterEventHotKey in hotkey_darwin.go because the
// library's CGEventTap path errors without the Accessibility
// permission and never prompts for it) and pumps Keydown events to
// onDown from a background goroutine. The returned stop function
// unregisters the hotkey and ends the goroutine; it is safe to call
// more than once.
func startLibHotkey(mods []hotkey.Modifier, key hotkey.Key, onDown func()) (func(), error) {
	hk := hotkey.New(mods, key)
	if err := hk.Register(); err != nil {
		return nil, err
	}
	stopc := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopc:
				return
			case <-hk.Keydown():
				onDown()
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stopc)
			_ = hk.Unregister()
		})
	}, nil
}

// mapSpec translates a neutral platform.Hotkey into the library's
// constants using the tables from hotkey_windows.go.
func mapSpec(hk platform.Hotkey, mods map[platform.Mod]hotkey.Modifier, keys map[string]hotkey.Key) ([]hotkey.Modifier, hotkey.Key, error) {
	key, ok := keys[hk.Key]
	if !ok {
		return nil, 0, fmt.Errorf("hotkey: key %q is not mapped on this platform", hk.Key)
	}
	out := make([]hotkey.Modifier, 0, len(hk.Mods))
	for _, m := range hk.Mods {
		lm, okMod := mods[m]
		if !okMod {
			return nil, 0, fmt.Errorf("hotkey: modifier %q is not mapped on this platform", m)
		}
		out = append(out, lm)
	}
	return out, key, nil
}
