// Package telemetry is the local ranking log (on by default): one
// JSON-line record per PICK -- the query, the whole delivered result
// list with its ranking signals at impression time, and which row was
// activated -- appended to a size-capped, LOCAL-ONLY file at
// <configDir>/telemetry.jsonl. It is a log, not telemetry in the
// phone-home sense: nothing here ever touches the network, and the
// only place the file goes is a debugging chat if the user pastes it.
// The log exists so the ranking layers (per-user priors, the learned
// arbiter) can train and evaluate offline on the user's own picks;
// deleting the file erases everything at any time, and config
// search.telemetry.disabled is the debug escape hatch that stops
// recording.
//
// Storage is append-only JSONL, a deliberate divergence from
// internal/history's whole-file rewrite: records are a few KB each,
// immutable, and append-heavy, so rewriting per event would be O(n^2)
// bytes. One write() per single-line record keeps appends
// non-interleaving; a torn final line (crash mid-append) is simply
// skipped by readers -- this is a loss-tolerant log, not a ledger.
// Rotation keeps at most two generations: an append that would cross
// the size cap first renames telemetry.jsonl to telemetry.jsonl.1
// (replacing the previous .1). The app only ever APPENDS; only
// offline tooling reads, so there is no Load step and corrupt lines
// are never an error. Deliberately NO schema in schemas/ -- an
// internal single-party format, the history.json / frecency.json
// precedent.
package telemetry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// Shown-row kinds.
const (
	// KindFile marks a file/directory row from the index engine.
	KindFile = "file"
	// KindPlugin marks a plugin-section row.
	KindPlugin = "plugin"
)

// File-pick action kinds (plugin picks carry the plugin action type
// instead: copy_text, open_url, run_command, ...).
const (
	ActionOpen   = "open"
	ActionReveal = "reveal"
)

const (
	// recordVersion stamps every appended record ("v").
	recordVersion = 1
	// defaultMaxSizeKB is the rotation threshold used when the caller
	// passes a non-positive one (config.Normalize repairs the knob
	// too; this is the defensive twin): 64 MiB live plus one rotated
	// generation -- generous on purpose, the cap bounds disk, it
	// never trims what is recorded.
	defaultMaxSizeKB = 65536
	// MaxShownRows caps one report's delivered list. The real flat
	// list tops out around ~70 rows (50 file rows plus the capped
	// plugin sections); the headroom tolerates config changes without
	// ever accepting an unbounded payload.
	MaxShownRows = 256
	// maxQueryBytes / maxPathBytes / maxTitleBytes bound the string
	// fields a report may carry -- wire-abuse defense (a bounded
	// payload), never a redaction of what gets recorded.
	maxQueryBytes = 4096
	maxPathBytes  = 4096
	maxTitleBytes = 4096
)

// pluginIDPattern is the plugin-id shape a report may reference
// (manifest ids and builtin provider ids all match it).
var pluginIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// pluginActionPattern is the action-KIND shape a plugin pick may
// carry. A charset gate rather than an exact list, so a future action
// type never silently breaks telemetry -- nothing is ever executed
// from this value, it is only logged.
var pluginActionPattern = regexp.MustCompile(`^[a-z_]{1,32}$`)

// Record is one appended log line (v1): the delivered impression plus
// the pick. The Store stamps V and TS on append when unset.
type Record struct {
	V  int    `json:"v"`
	TS string `json:"ts"` // RFC3339 UTC
	// Query is the trimmed query text, always recorded in full (the
	// log is local-only).
	Query string `json:"query"`
	// BlendActive reports whether the frecency blend participated in
	// this impression's ordering -- whether the Boost/Recency/Cwd/
	// Penalty components below are real or structurally zero.
	BlendActive bool `json:"blendActive"`
	// Joined reports whether the impression's ranking signals were
	// still in the app's query ring at pick time; false means the
	// file rows carry no feature values (flagged, harmless).
	Joined bool `json:"joined"`
	// Refined is reserved for a future async re-rank: true would mean
	// a refined ordering was applied after first paint. Always false
	// today.
	Refined bool       `json:"refined"`
	Shown   []ShownRow `json:"shown"`
	Picked  PickedRow  `json:"picked"`
}

// ShownRow is one delivered row. File rows carry the feature vector
// at impression time; plugin rows carry the plugin id, wire score,
// rank, and the row title as rendered (full-fidelity capture -- the
// log is local-only, so nothing is redacted). MarshalJSON emits
// exactly the fields that belong to the row's kind.
type ShownRow struct {
	Rank int
	Kind string

	// File rows.
	Path     string
	Class    int
	EffClass int
	Align    int
	Boost    float64
	Recency  float64
	Cwd      float64
	Penalty  float64
	IsDir    bool
	Depth    int
	Ext      string

	// Plugin rows.
	Plugin string
	Score  int
	Title  string
}

// fileRowJSON / pluginRowJSON are the per-kind wire shapes.
type fileRowJSON struct {
	Rank     int     `json:"rank"`
	Kind     string  `json:"kind"`
	Path     string  `json:"path"`
	Class    int     `json:"class"`
	EffClass int     `json:"effClass"`
	Align    int     `json:"align"`
	Boost    float64 `json:"boost"`
	Recency  float64 `json:"recency"`
	Cwd      float64 `json:"cwd"`
	Penalty  float64 `json:"penalty"`
	IsDir    bool    `json:"isDir"`
	Depth    int     `json:"depth"`
	Ext      string  `json:"ext"`
}

type pluginRowJSON struct {
	Rank   int    `json:"rank"`
	Kind   string `json:"kind"`
	Plugin string `json:"plugin"`
	Score  int    `json:"score"`
	Title  string `json:"title"`
}

// MarshalJSON emits the kind-appropriate field set: plugin rows never
// carry (misleading, all-zero) file feature fields, and file rows
// keep every feature explicit so a zero (class 0 = exact) is never
// ambiguous with an omitted value.
func (r ShownRow) MarshalJSON() ([]byte, error) {
	if r.Kind == KindPlugin {
		return json.Marshal(pluginRowJSON{Rank: r.Rank, Kind: r.Kind, Plugin: r.Plugin, Score: r.Score, Title: r.Title})
	}
	return json.Marshal(fileRowJSON{
		Rank: r.Rank, Kind: r.Kind, Path: r.Path,
		Class: r.Class, EffClass: r.EffClass, Align: r.Align,
		Boost: r.Boost, Recency: r.Recency, Cwd: r.Cwd, Penalty: r.Penalty,
		IsDir: r.IsDir, Depth: r.Depth, Ext: r.Ext,
	})
}

// PickedRow is the activated row: its rank into Shown, the identity
// copied from the shown row, and the action kind that ran.
type PickedRow struct {
	Rank     int    `json:"rank"`
	Kind     string `json:"kind"`
	Path     string `json:"path,omitempty"`
	Plugin   string `json:"plugin,omitempty"`
	Action   string `json:"action"`
	Revealed bool   `json:"revealed"`
}

// PickReport is what the frontend reports after an activation
// actually ran -- deliberately the MINIMAL wire shape: row identities
// and the pick, never feature values (the app joins those from its
// own query ring, so a compromised or buggy frontend cannot forge
// signal data into the log).
type PickReport struct {
	Query  string     `json:"query"`
	Shown  []ShownRef `json:"shown"`
	Picked PickedRef  `json:"picked"`
}

// ShownRef is one delivered row's identity as the frontend reports
// it; the slice index is the rank. Plugin rows also carry the title
// as rendered -- the one row field only the frontend knows (file-row
// FEATURE values still never ride the wire; the app joins those from
// its own ring).
type ShownRef struct {
	Kind   string `json:"kind"`
	Path   string `json:"path,omitempty"`   // file rows
	Plugin string `json:"plugin,omitempty"` // plugin rows
	Score  int    `json:"score,omitempty"`  // plugin rows: the engine wire score
	Title  string `json:"title,omitempty"`  // plugin rows: the rendered title
}

// PickedRef names the activated row and what ran.
type PickedRef struct {
	Rank     int    `json:"rank"`
	Action   string `json:"action"`
	Revealed bool   `json:"revealed"`
}

// ValidatePickReport re-validates everything a frontend-echoed report
// claims (defense in depth, the RunPluginAction stance): bounded
// sizes, per-kind field consistency, an in-range pick, and an action
// kind that matches the picked row's kind. The query may be anything
// up to the byte cap -- the caller decides blank-query policy.
func ValidatePickReport(r PickReport) error {
	if len(r.Query) > maxQueryBytes {
		return fmt.Errorf("telemetry: query longer than %d bytes", maxQueryBytes)
	}
	if len(r.Shown) == 0 {
		return errors.New("telemetry: empty shown list")
	}
	if len(r.Shown) > MaxShownRows {
		return fmt.Errorf("telemetry: %d shown rows exceeds the %d cap", len(r.Shown), MaxShownRows)
	}
	for i, row := range r.Shown {
		switch row.Kind {
		case KindFile:
			if row.Path == "" || !filepath.IsAbs(row.Path) {
				return fmt.Errorf("telemetry: shown[%d]: file row path %q is not absolute", i, row.Path)
			}
			if len(row.Path) > maxPathBytes {
				return fmt.Errorf("telemetry: shown[%d]: path longer than %d bytes", i, maxPathBytes)
			}
			if row.Plugin != "" || row.Score != 0 || row.Title != "" {
				return fmt.Errorf("telemetry: shown[%d]: file row carries plugin fields", i)
			}
		case KindPlugin:
			if !pluginIDPattern.MatchString(row.Plugin) {
				return fmt.Errorf("telemetry: shown[%d]: invalid plugin id %q", i, row.Plugin)
			}
			if row.Path != "" {
				return fmt.Errorf("telemetry: shown[%d]: plugin row carries a path", i)
			}
			if row.Score < 0 || row.Score > 100 {
				return fmt.Errorf("telemetry: shown[%d]: score %d outside 0..100", i, row.Score)
			}
			if len(row.Title) > maxTitleBytes {
				return fmt.Errorf("telemetry: shown[%d]: title longer than %d bytes", i, maxTitleBytes)
			}
		default:
			return fmt.Errorf("telemetry: shown[%d]: unknown row kind %q", i, row.Kind)
		}
	}
	p := r.Picked
	if p.Rank < 0 || p.Rank >= len(r.Shown) {
		return fmt.Errorf("telemetry: picked rank %d outside the %d shown rows", p.Rank, len(r.Shown))
	}
	switch r.Shown[p.Rank].Kind {
	case KindFile:
		if p.Action != ActionOpen && p.Action != ActionReveal {
			return fmt.Errorf("telemetry: file pick action %q is not open/reveal", p.Action)
		}
		if p.Revealed != (p.Action == ActionReveal) {
			return errors.New("telemetry: revealed flag inconsistent with the action")
		}
	default: // KindPlugin (validated above)
		if !pluginActionPattern.MatchString(p.Action) {
			return fmt.Errorf("telemetry: invalid plugin pick action %q", p.Action)
		}
		if p.Revealed {
			return errors.New("telemetry: plugin picks cannot be revealed")
		}
	}
	return nil
}

// Store appends records to the JSONL log. All methods are safe for
// concurrent use and nil-receiver-safe (a nil Store -- the feature
// disabled -- no-ops everything), the internal/history stance.
type Store struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	now     func() time.Time // injectable clock (tests)
}

// New creates a store appending to the JSONL file at path, rotating
// it to path+".1" when an append would cross maxSizeKB KiB
// (non-positive selects the 65536 default).
func New(path string, maxSizeKB int) *Store {
	if maxSizeKB <= 0 {
		maxSizeKB = defaultMaxSizeKB
	}
	return &Store{path: path, maxSize: int64(maxSizeKB) * 1024, now: time.Now}
}

// Path returns the log file location (for the one startup log line).
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Append writes rec as one JSON line, stamping V and TS when unset,
// creating the parent directory and the 0600 file as needed, and
// rotating first when the append would cross the size cap. A nil
// store is a silent no-op.
func (s *Store) Append(rec Record) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.V == 0 {
		rec.V = recordVersion
	}
	if rec.TS == "" {
		rec.TS = s.now().UTC().Format(time.RFC3339)
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("telemetry: encoding a record: %w", err)
	}
	line = append(line, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("telemetry: creating %s: %w", dir, err)
	}
	// Rotate BEFORE the append that would cross the cap, so the live
	// file plus one predecessor bound the disk footprint at two
	// generations. A rename on the same filesystem is atomic and
	// replaces any previous .1. An empty live file is never rotated
	// (a single over-cap record still lands somewhere).
	if st, err := os.Stat(s.path); err == nil && st.Size() > 0 && st.Size()+int64(len(line)) > s.maxSize {
		if err := os.Rename(s.path, s.path+".1"); err != nil {
			return fmt.Errorf("telemetry: rotating %s: %w", s.path, err)
		}
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("telemetry: opening %s: %w", s.path, err)
	}
	// Tighten a pre-existing wider file best-effort (O_CREATE's mode
	// only applies at creation); the append itself is what matters.
	_ = f.Chmod(0o600)
	_, werr := f.Write(line)
	cerr := f.Close()
	if werr != nil {
		return fmt.Errorf("telemetry: appending to %s: %w", s.path, werr)
	}
	if cerr != nil {
		return fmt.Errorf("telemetry: closing %s: %w", s.path, cerr)
	}
	return nil
}
