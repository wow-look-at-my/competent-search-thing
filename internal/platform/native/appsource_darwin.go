//go:build darwin

package native

/*
#cgo LDFLAGS: -framework Cocoa -framework CoreGraphics
#include "platform_darwin.h"
*/
import "C"

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wow-look-at-my/competent-search-thing/internal/appctx"
)

// maxRunningApps caps one RunningApps snapshot; plugin payloads never
// need more.
const maxRunningApps = 64

// appSource is the macOS appctx.Source: NSWorkspace via the Cocoa
// shim (platform_darwin.h/.m, main-thread dispatched like the rest of
// the shim) plus a plain /Applications scan for installed apps.
// Window titles are not collected -- reading other applications'
// titles needs the Accessibility AX API, out of scope here -- so
// Title stays empty on macOS. Untested in CI (linux/amd64 only).
type appSource struct{}

// AppSource returns this OS's implementation of appctx.Source.
func AppSource() appctx.Source { return appSource{} }

// FocusedApp identifies the frontmost application.
func (appSource) FocusedApp() (appctx.AppInfo, bool) {
	var out C.csAppInfo
	if C.csFrontmostApp(&out) == 0 {
		return appctx.AppInfo{}, false
	}
	info := appInfoFromC(&out)
	if info.Name == "" && info.PID == 0 {
		return appctx.AppInfo{}, false
	}
	return info, true
}

// RunningApps lists the regular-activation-policy applications (the
// ones in the Dock and app switcher), sorted by Name.
func (appSource) RunningApps() ([]appctx.AppInfo, bool) {
	var out [maxRunningApps]C.csAppInfo
	n := int(C.csRunningApps(&out[0], maxRunningApps))
	if n <= 0 {
		return nil, false
	}
	apps := make([]appctx.AppInfo, 0, n)
	for i := 0; i < n; i++ {
		apps = append(apps, appInfoFromC(&out[i]))
	}
	sort.Slice(apps, func(i, j int) bool {
		if apps[i].Name != apps[j].Name {
			return apps[i].Name < apps[j].Name
		}
		return apps[i].PID < apps[j].PID
	})
	return apps, true
}

// InstalledApps scans /Applications and ~/Applications for .app
// bundles, best-effort: Name is the bundle name without .app, ID the
// bundle directory name, and Exec an `open -a "<path>"` line in the
// quoted syntax the plugin layer's Exec parser understands. ok is
// false only when neither directory is readable.
func (appSource) InstalledApps() ([]appctx.InstalledApp, bool) {
	dirs := []string{"/Applications"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, "Applications"))
	}
	var apps []appctx.InstalledApp
	seen := make(map[string]bool)
	readable := false
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		readable = true
		for _, e := range entries {
			id := e.Name()
			if !strings.HasSuffix(id, ".app") || seen[id] {
				continue
			}
			full := filepath.Join(dir, id)
			// Bundles are directories; Stat follows symlinked ones.
			if fi, err := os.Stat(full); err != nil || !fi.IsDir() {
				continue
			}
			seen[id] = true
			apps = append(apps, appctx.InstalledApp{
				Name: strings.TrimSuffix(id, ".app"),
				Exec: "open -a " + desktopQuote(full),
				ID:   id,
			})
		}
	}
	if !readable {
		return nil, false
	}
	sort.Slice(apps, func(i, j int) bool {
		if apps[i].Name != apps[j].Name {
			return apps[i].Name < apps[j].Name
		}
		return apps[i].ID < apps[j].ID
	})
	return apps, true
}

// appInfoFromC converts the shim's csAppInfo (NUL-terminated fixed
// buffers) into an appctx.AppInfo.
func appInfoFromC(a *C.csAppInfo) appctx.AppInfo {
	return appctx.AppInfo{
		Name: C.GoString(&a.name[0]),
		Exe:  C.GoString(&a.exe[0]),
		PID:  int(a.pid),
	}
}

// desktopQuote wraps s in the double-quoted .desktop Exec argument
// syntax the plugin layer parses (backslashes and quotes escaped).
func desktopQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
