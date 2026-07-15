package app

import (
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/theme"
)

const (
	// eventThemeChanged tells the frontend to refetch GetTheme and
	// GetCustomCSS; no payload.
	eventThemeChanged = "theme:changed"
	// themeDebounce coalesces bursts of filesystem events (editors
	// write-then-rename, npm-style multi-file drops) into one reload.
	themeDebounce = 300 * time.Millisecond
	// customCSSMaxBytes caps the custom.css escape hatch; anything
	// larger is ignored with a log line.
	customCSSMaxBytes = 64 * 1024
)

// GetTheme is bound to the frontend: it re-reads the config file's
// theme field (only that field is consumed live; other config fields
// still apply at startup) and returns the fully resolved token map for
// it -- every internal/theme.TokenNames key mapped to a validated CSS
// value. Resolution errors are logged once per distinct message and
// fall back to the builtin dark theme, so the frontend always gets a
// complete, usable map.
func (a *App) GetTheme() map[string]string {
	cfg, _ := config.Load() // errors yield usable defaults (theme=dark)
	dir, derr := config.Dir()
	if derr != nil {
		a.logThemeErr(derr)
		return theme.Dark()
	}
	tokens, err := theme.Resolve(cfg.Theme, dir)
	a.logThemeErr(err)
	return tokens
}

// GetCustomCSS is bound to the frontend: it returns the contents of
// <configDir>/themes/custom.css when the file exists and is at most
// 64KB, else "". This is the deliberately UNVALIDATED escape hatch --
// the stylesheet is injected verbatim into the page (use at your own
// risk); the validated way to restyle the bar is a theme JSON file.
func (a *App) GetCustomCSS() string {
	dir, err := config.Dir()
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, "themes", "custom.css")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > customCSSMaxBytes {
		log.Printf("theme: ignoring %s: %d bytes exceeds the %d byte cap", path, len(data), customCSSMaxBytes)
		return ""
	}
	return string(data)
}

// logThemeErr logs a theme resolution failure once per distinct
// message (a broken theme file would otherwise spam the log on every
// hot reload); a nil error resets the dedup so the same problem is
// reported again if it comes back.
func (a *App) logThemeErr(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	a.mu.Lock()
	changed := msg != a.lastThemeErr
	a.lastThemeErr = msg
	a.mu.Unlock()
	if changed && msg != "" {
		log.Printf("theme: %s (falling back to the dark builtin)", msg)
	}
}

// themeWatcher owns the fsnotify watcher behind theme hot reload; stop
// closes it and waits for the loop goroutine to drain.
type themeWatcher struct {
	fsw  *fsnotify.Watcher
	done chan struct{}
}

func (w *themeWatcher) stop() {
	_ = w.fsw.Close()
	<-w.done
}

// startThemeWatch brings up theme hot reload: one small fsnotify
// watcher on the config directory (for config.json edits -- switching
// the theme field applies live) and on its themes/ subdirectory (theme
// JSON files and custom.css). Events are debounced and surface as the
// eventThemeChanged runtime event; the frontend then refetches
// GetTheme/GetCustomCSS. Any failure is logged and the app runs on
// without hot reload -- themes still apply at startup.
func (a *App) startThemeWatch() {
	cfgPath, err := config.Path()
	if err != nil {
		log.Printf("theme: config dir unavailable, hot reload disabled: %v", err)
		return
	}
	cfgDir := filepath.Dir(cfgPath)
	themesDir := filepath.Join(cfgDir, "themes")
	// Materialize themes/ so it is watchable from the first run and
	// users can see where their theme files go.
	if err := os.MkdirAll(themesDir, 0o755); err != nil {
		log.Printf("theme: creating %s: %v", themesDir, err)
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("theme: watcher unavailable, hot reload disabled: %v", err)
		return
	}
	for _, dir := range []string{cfgDir, themesDir} {
		if err := fsw.Add(dir); err != nil {
			log.Printf("theme: watching %s failed: %v", dir, err)
		}
	}
	w := &themeWatcher{fsw: fsw, done: make(chan struct{})}
	go a.themeWatchLoop(w, cfgPath, themesDir)

	a.watchMu.Lock()
	if a.shuttingDown {
		a.watchMu.Unlock()
		w.stop()
		return
	}
	a.themeW = w
	a.watchMu.Unlock()
}

// themeWatchLoop debounces relevant filesystem events into
// eventThemeChanged emissions until the watcher is closed.
func (a *App) themeWatchLoop(w *themeWatcher, cfgPath, themesDir string) {
	defer close(w.done)
	timer := time.NewTimer(time.Hour)
	stopThemeTimer(timer)
	defer timer.Stop()
	relevant := func(path string) bool {
		return path == cfgPath || path == themesDir || filepath.Dir(path) == themesDir
	}
	for {
		select {
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if ev.Name == themesDir && ev.Has(fsnotify.Create) {
				// themes/ was deleted and recreated: re-arm its watch.
				_ = w.fsw.Add(themesDir)
			}
			if !relevant(ev.Name) {
				continue
			}
			stopThemeTimer(timer)
			timer.Reset(themeDebounce)
		case _, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			// Event overflow means lost events: refresh regardless.
			stopThemeTimer(timer)
			timer.Reset(themeDebounce)
		case <-timer.C:
			a.emitEvent(eventThemeChanged)
		}
	}
}

// stopThemeTimer stops and drains a timer owned by a single goroutine
// so it is safe to Reset.
func stopThemeTimer(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}
