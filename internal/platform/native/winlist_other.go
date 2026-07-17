//go:build !linux

package native

import (
	"errors"

	"github.com/wow-look-at-my/competent-search-thing/internal/appctx"
)

// OpenWindows reports that open-window enumeration is not implemented
// on this OS yet (the Win32/Cocoa paths exist but are out of scope for
// now); the builtin Open Windows search simply never registers.
func (appSource) OpenWindows() ([]appctx.WindowInfo, bool) {
	return nil, false
}

// ActivateWindow is unavailable where OpenWindows is: no window list
// means no activate_window actions are ever produced, so this is only
// reachable via a hand-crafted frontend call.
func ActivateWindow(uint32) error {
	return errors.New("window activation is not supported on this platform")
}
