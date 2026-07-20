package app

import "strings"

// The frontend's query + file-activation entry points, split out of
// app.go (which holds the App struct and lifecycle) for the file-size
// budget.

// Search returns index entries whose name contains query,
// case-insensitively, best matches first (limit: the configured
// MaxResults). It always returns a non-nil slice so the frontend can
// iterate without null checks. An absolute-path query with zero index
// results may yield one synthetic outside-indexed-roots hint result
// instead of nothing (see hint.go).
func (a *App) Search(query string) []Result {
	q := strings.TrimSpace(query)
	if q == "" || a.manager == nil {
		return []Result{}
	}
	// Exactly Manager.Query while ranking telemetry is off (the
	// default); enabled, the query also captures its ranking signals
	// for a later RecordPick join (see telemetry.go).
	res := a.queryWithTelemetry(q)
	if len(res) == 0 {
		if r, ok := a.outsideRootsHint(q); ok {
			return []Result{r}
		}
		return []Result{}
	}
	return res
}

// Open launches path (or URL) with the operating system's default
// handler -- on linux through the credentialed launch path, so the
// target application's window ends focused and raised (see launch.go)
// -- and hides the bar on success. A successful open of an absolute
// path is recorded as a frecency signal (recordOpen filters the
// open_url values that share this method).
func (a *App) Open(path string) error {
	if err := a.openTarget(path); err != nil {
		return err
	}
	a.recordOpen(path)
	a.kickPriorsRefresh()
	a.Hide()
	return nil
}

// Reveal shows path selected in the operating system's file manager
// (credentialed on linux, like Open) and hides the bar on success. A
// successful reveal counts as a frecency open too -- the user went
// for that exact file.
func (a *App) Reveal(path string) error {
	if err := a.revealTarget(path); err != nil {
		return err
	}
	a.recordOpen(path)
	a.kickPriorsRefresh()
	a.Hide()
	return nil
}
