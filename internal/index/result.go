package index

// Result is a single search hit as sent to the frontend. The JSON tags
// are part of the frontend contract (window.go.app.App.Search resolves
// to objects with path/name/isDir fields); do not change them without
// updating frontend/src/wails.d.ts and main.ts.
type Result struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
}
