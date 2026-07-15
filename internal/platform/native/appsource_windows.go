//go:build windows

package native

import (
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/wow-look-at-my/competent-search-thing/internal/appctx"
)

// maxRunningApps caps one RunningApps snapshot; plugin payloads never
// need more.
const maxRunningApps = 64

// processQueryLimitedInformation is the OpenProcess access right that
// QueryFullProcessImageNameW needs; it works across integrity levels
// where PROCESS_QUERY_INFORMATION would be denied.
const processQueryLimitedInformation = 0x1000

var (
	kernel32                       = windows.NewLazySystemDLL("kernel32.dll")
	procGetForegroundWindow        = user32.NewProc("GetForegroundWindow")
	procGetWindowTextW             = user32.NewProc("GetWindowTextW")
	procGetWindowThreadProcessID   = user32.NewProc("GetWindowThreadProcessId")
	procEnumWindows                = user32.NewProc("EnumWindows")
	procIsWindowVisible            = user32.NewProc("IsWindowVisible")
	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procCloseHandle                = kernel32.NewProc("CloseHandle")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
)

// appSource is the Windows appctx.Source: user32 window queries (the
// same syscall style as display_windows.go, whose user32 handle it
// shares), process image paths via kernel32, and the registry
// uninstall keys for installed software. Untested in CI (linux/amd64
// only); everything is best-effort and degrades to ok=false.
type appSource struct{}

// AppSource returns this OS's implementation of appctx.Source.
func AppSource() appctx.Source { return appSource{} }

// FocusedApp identifies the application owning the foreground window.
// ok is false when there is no foreground window or its process id
// cannot be read.
func (appSource) FocusedApp() (appctx.AppInfo, bool) {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return appctx.AppInfo{}, false
	}
	info := windowAppInfo(hwnd)
	if info.PID == 0 {
		return appctx.AppInfo{}, false
	}
	return info, true
}

// RunningApps lists the applications owning visible, titled top-level
// windows, deduplicated by pid keeping the first window's title,
// capped at maxRunningApps, and sorted by Name.
func (appSource) RunningApps() ([]appctx.AppInfo, bool) {
	var handles []uintptr
	if r, _, _ := procEnumWindows.Call(enumWindowsCallback, uintptr(unsafe.Pointer(&handles))); r == 0 {
		return nil, false
	}
	seen := make(map[int]bool)
	var apps []appctx.AppInfo
	for _, hwnd := range handles {
		if len(apps) >= maxRunningApps {
			break
		}
		info := windowAppInfo(hwnd)
		if info.Title == "" || info.PID == 0 || seen[info.PID] {
			continue
		}
		seen[info.PID] = true
		apps = append(apps, info)
	}
	sort.Slice(apps, func(i, j int) bool {
		if apps[i].Name != apps[j].Name {
			return apps[i].Name < apps[j].Name
		}
		return apps[i].PID < apps[j].PID
	})
	return apps, true
}

// enumWindowsCallback is created ONCE: syscall.NewCallback allocations
// are permanent for the process's lifetime (same pattern as
// enumMonitorsCallback). It collects the visible top-level windows;
// the result slice travels through the LPARAM.
var enumWindowsCallback = syscall.NewCallback(func(hwnd, lparam uintptr) uintptr {
	if r, _, _ := procIsWindowVisible.Call(hwnd); r == 0 {
		return 1 // TRUE: continue enumeration
	}
	hs := (*[]uintptr)(unsafe.Pointer(lparam)) //nolint:govet // standard EnumWindows LPARAM round-trip
	*hs = append(*hs, hwnd)
	return 1
})

// windowAppInfo extracts one window's identity: title via
// GetWindowTextW, pid via GetWindowThreadProcessId, exe via
// OpenProcess + QueryFullProcessImageNameW (empty for windows of more
// privileged processes), Name = exe base name without its extension.
func windowAppInfo(hwnd uintptr) appctx.AppInfo {
	var info appctx.AppInfo
	var title [512]uint16
	if n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&title[0])), uintptr(len(title))); n > 0 {
		info.Title = syscall.UTF16ToString(title[:n])
	}
	var pid uint32
	_, _, _ = procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	info.PID = int(pid)
	info.Exe = processImagePath(pid)
	if info.Exe != "" {
		base := filepath.Base(info.Exe)
		info.Name = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return info
}

// processImagePath resolves a pid to its executable path; empty when
// the process cannot be opened (higher integrity level, exited, ...).
func processImagePath(pid uint32) string {
	if pid == 0 {
		return ""
	}
	h, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer func() { _, _, _ = procCloseHandle.Call(h) }()
	var buf [1024]uint16
	size := uint32(len(buf))
	if r, _, _ := procQueryFullProcessImageNameW.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:size])
}

// uninstallPaths are the registry locations listing installed
// software: the native view and WOW6432Node (32-bit software on
// 64-bit Windows), checked under both HKLM and HKCU.
var uninstallPaths = []string{
	`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`,
	`SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`,
}

// InstalledApps enumerates the registry uninstall keys, best-effort:
// unreadable keys and values are skipped, duplicate subkey names keep
// their first occurrence, and ok is true as long as at least one
// uninstall key could be opened at all.
func (appSource) InstalledApps() ([]appctx.InstalledApp, bool) {
	roots := []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER}
	var apps []appctx.InstalledApp
	seen := make(map[string]bool)
	opened := false
	for _, root := range roots {
		for _, path := range uninstallPaths {
			k, err := registry.OpenKey(root, path, registry.READ)
			if err != nil {
				continue
			}
			opened = true
			names, err := k.ReadSubKeyNames(-1)
			k.Close()
			if err != nil {
				continue
			}
			for _, sub := range names {
				if seen[sub] {
					continue
				}
				app, ok := uninstallEntry(root, path+`\`+sub, sub)
				if !ok {
					continue
				}
				seen[sub] = true
				apps = append(apps, app)
			}
		}
	}
	if !opened {
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

// uninstallEntry reads one uninstall subkey. ok is false for
// nameless entries and for SystemComponent=1 rows (OS plumbing that
// "Programs and Features" hides too). Exec stays empty unless
// DisplayIcon yields a plausible executable.
func uninstallEntry(root registry.Key, path, id string) (appctx.InstalledApp, bool) {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return appctx.InstalledApp{}, false
	}
	defer k.Close()
	name, _, err := k.GetStringValue("DisplayName")
	if err != nil || name == "" {
		return appctx.InstalledApp{}, false
	}
	if sys, _, err := k.GetIntegerValue("SystemComponent"); err == nil && sys == 1 {
		return appctx.InstalledApp{}, false
	}
	app := appctx.InstalledApp{Name: name, ID: id}
	if icon, _, err := k.GetStringValue("DisplayIcon"); err == nil {
		app.Exec = execFromDisplayIcon(icon)
	}
	return app, true
}

// execFromDisplayIcon turns a DisplayIcon registry value into a
// .desktop-style Exec line when it plausibly points at an .exe:
// a trailing ",N" icon index and surrounding quotes are stripped,
// non-.exe targets (icon .dll/.ico references) yield "". Paths with
// spaces are re-quoted in the syntax the plugin layer's Exec parser
// understands (backslashes escaped inside the quotes).
func execFromDisplayIcon(icon string) string {
	p := strings.TrimSpace(icon)
	if i := strings.LastIndexByte(p, ','); i >= 0 && isIconIndex(strings.TrimSpace(p[i+1:])) {
		p = strings.TrimSpace(p[:i])
	}
	p = strings.Trim(p, `"`)
	if p == "" || !strings.EqualFold(filepath.Ext(p), ".exe") {
		return ""
	}
	if strings.ContainsAny(p, " \t") {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(p) + `"`
	}
	return p
}

// isIconIndex reports whether s looks like a DisplayIcon index suffix
// (an optionally negative integer, e.g. "0" or "-101").
func isIconIndex(s string) bool {
	if strings.HasPrefix(s, "-") {
		s = s[1:]
	}
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
