// Package preview computes preview-pane payloads for the selected
// search result: file metadata cards, capped text reads with language
// hints, image thumbnails, and directory listings -- plus the wire
// contract for the web-search and AI answer previews that land later.
// The package is pure (no Wails imports) and headless-testable; the
// app layer owns the event emission and the generation gate on top.
package preview

// Target kinds accepted by Dispatcher.Preview.
const (
	// TargetFile previews a file-index result (Path + IsDir set).
	TargetFile = "file"
	// TargetPlugin previews a plugin result (Title/Subtitle/PluginName
	// set); it renders as a no-IO metadata card.
	TargetPlugin = "plugin"
	// TargetNone cancels the in-flight preview without starting a new
	// one (the frontend's deselection signal). An empty Kind acts the
	// same.
	TargetNone = "none"
)

// Payload kinds.
const (
	// KindMeta is a metadata card (the fast first payload for files,
	// the whole payload for plugin targets and binary files).
	KindMeta = "meta"
	// KindText is a capped text-file read with a language hint.
	KindText = "text"
	// KindImage is a downscaled thumbnail as a data URI.
	KindImage = "image"
	// KindDir is a capped directory listing.
	KindDir = "dir"
	// KindWeb is a web-search answer (contract only this phase).
	KindWeb = "web"
	// KindAI is an AI answer (contract only this phase).
	KindAI = "ai"
	// KindError carries a human-readable failure in Err.
	KindError = "error"
)

// Target names what the frontend wants previewed -- the selected
// result, translated to the minimum the providers need.
type Target struct {
	// Kind is one of the Target* constants ("file", "plugin", "none").
	Kind string `json:"kind"`
	// Path is the absolute filesystem path (file targets).
	Path string `json:"path"`
	// IsDir mirrors the search result's directory flag.
	IsDir bool `json:"isDir"`
	// Title is the row's display title (plugin targets).
	Title string `json:"title"`
	// Subtitle is the row's subtitle (plugin targets).
	Subtitle string `json:"subtitle"`
	// PluginName names the providing plugin (plugin targets).
	PluginName string `json:"pluginName"`
}

// MetaRow is one label/value line of a metadata card.
type MetaRow struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// TextPreview is a capped text-file read.
type TextPreview struct {
	// Content is the file head, valid UTF-8 (invalid bytes replaced).
	Content string `json:"content"`
	// Lang is a highlight.js language name ("" = plain text).
	Lang string `json:"lang"`
	// Truncated reports that the file was longer than the cap.
	Truncated bool `json:"truncated"`
	// SizeBytes is the file's full size on disk.
	SizeBytes int64 `json:"sizeBytes"`
}

// ImagePreview is a downscaled thumbnail.
type ImagePreview struct {
	// DataURI is the encoded thumbnail (data:image/png;base64,... or
	// data:image/jpeg;base64,...).
	DataURI string `json:"dataUri"`
	// W and H are the thumbnail dimensions; OrigW and OrigH the
	// source's.
	W     int `json:"w"`
	H     int `json:"h"`
	OrigW int `json:"origW"`
	OrigH int `json:"origH"`
	// SizeBytes is the source file's size on disk.
	SizeBytes int64 `json:"sizeBytes"`
}

// DirEntry is one row of a directory listing.
type DirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

// DirPreview is a capped, sorted directory listing.
type DirPreview struct {
	// Entries lists directories first, then files, each group sorted
	// case-insensitively by name; never nil.
	Entries []DirEntry `json:"entries"`
	// Total counts the whole directory, before the cap.
	Total int `json:"total"`
	// Truncated reports that Entries was capped below Total.
	Truncated bool `json:"truncated"`
}

// WebResult is one web-search hit (contract only this phase).
type WebResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// WebPreview is a web-search answer (contract only this phase).
type WebPreview struct {
	Query   string      `json:"query"`
	Results []WebResult `json:"results"`
	Cached  bool        `json:"cached"`
}

// AIPreview is an AI answer (contract only this phase).
type AIPreview struct {
	Query  string `json:"query"`
	Answer string `json:"answer"`
	Model  string `json:"model"`
	Cached bool   `json:"cached"`
}

// Payload is one preview emission to the frontend. Kind selects which
// optional section is populated; a file target typically produces a
// fast KindMeta payload followed by the rich payload under the same
// Gen (the frontend replaces the pane content per emission).
type Payload struct {
	// Gen echoes the request generation; stale generations are
	// dropped on both sides.
	Gen int `json:"gen"`
	// Kind is one of the Kind* constants.
	Kind string `json:"kind"`
	// Title heads the pane (the entry name or the plugin row title).
	Title string `json:"title"`
	// Path is the previewed path ("" for plugin/web/ai payloads).
	Path  string        `json:"path"`
	Meta  []MetaRow     `json:"meta,omitempty"`
	Text  *TextPreview  `json:"text,omitempty"`
	Image *ImagePreview `json:"image,omitempty"`
	Dir   *DirPreview   `json:"dir,omitempty"`
	Web   *WebPreview   `json:"web,omitempty"`
	AI    *AIPreview    `json:"ai,omitempty"`
	// Err is the human-readable failure for KindError payloads.
	Err string `json:"err,omitempty"`
	// DurMS is how long this payload took to compute.
	DurMS int64 `json:"durMs"`
}
