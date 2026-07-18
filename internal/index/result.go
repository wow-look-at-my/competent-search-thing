package index

// Result is a single search hit as sent to the frontend. The JSON tags
// are part of the frontend contract (window.go.app.App.Search resolves
// to objects with path/name/isDir and an optional hint field); do not
// change them without updating frontend/src/wails.d.ts and render.ts.
type Result struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	// Hint, when non-empty, is a human-readable note the frontend
	// renders in place of the parent-directory line. Index queries
	// never set it; the app layer uses it for synthetic results (the
	// outside-indexed-roots hint, internal/app hint.go).
	Hint string `json:"hint,omitempty"`
	// MatchRanges are the per-character highlight ranges on Name:
	// half-open [start, end) RUNE index pairs, sorted and merged
	// (match.Range; computed by the shared internal/match engine for
	// the returned rows only, never during the scan). The frontend
	// colors exactly these characters. Path-mode results carry the
	// best-effort name-prefix range of the query's final segment, or
	// nothing when the match lies entirely in the directory.
	MatchRanges [][2]int `json:"matchRanges,omitempty"`
}
