package arbiter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// Tolerant reader for the telemetry pick log. Like internal/priors,
// this package deliberately does NOT import internal/telemetry: that
// package is an append-only writer whose ShownRow marshals through a
// one-way custom MarshalJSON and whose contract says only offline
// tooling reads the log -- the on-disk JSONL FORMAT is the data
// contract, re-declared here with explicit json tags. Unknown fields
// are ignored, malformed lines (torn final lines included) are
// skipped, and a missing file is empty data with a nil error; errors
// are reserved for real IO problems the app should log once.

// maxSourceFileBytes bounds one log file read: the telemetry writer
// rotates at 4 MiB, so anything bigger is a hand-grown anomaly --
// ignored (empty data) rather than swallowed into memory.
const maxSourceFileBytes = 64 << 20

// Impression is one telemetry record's projection: the delivered
// rows in order (each already shaped for the feature definition) and
// which one was picked.
type Impression struct {
	// TS is the pick's timestamp (zero when the record carried none
	// or it did not parse).
	TS time.Time
	// Query is the query text as logged ("" only in records from
	// builds predating the always-record flip -- the query-shape
	// features then read an empty query).
	Query string
	// Joined reports whether the record's file features were joined
	// from the app's impression ring at log time; only joined records
	// train (unjoined file rows carry structurally-zero features).
	Joined bool
	// Rows is the delivered flat list, oldest ordering preserved
	// (the slice index is the logged rank).
	Rows []Row
	// Picked indexes the activated row in Rows.
	Picked int
}

// logLine / logRow mirror exactly the record fields this package
// consumes; json.Unmarshal drops everything else (the tolerance
// contract).
type logLine struct {
	V      int      `json:"v"`
	TS     string   `json:"ts"`
	Query  string   `json:"query"`
	Joined bool     `json:"joined"`
	Shown  []logRow `json:"shown"`
	Picked struct {
		Rank int    `json:"rank"`
		Kind string `json:"kind"`
	} `json:"picked"`
}

type logRow struct {
	Kind     string  `json:"kind"`
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
	Plugin   string  `json:"plugin"`
	Score    int     `json:"score"`
}

// ReadLogFile reads one telemetry JSONL file into Impressions, oldest
// first (file order -- pass telemetry.jsonl.1 before the live file).
// A missing or oversized file returns (nil, nil). A line survives
// only when it is a valid v1 record whose rows all carry a known
// kind and whose pick is in range; everything else is skipped
// silently -- the log is loss-tolerant by design.
func ReadLogFile(path string) ([]Impression, error) {
	data, err := readBounded(path)
	if data == nil || err != nil {
		return nil, err
	}
	var out []Impression
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var rec logLine
		if json.Unmarshal(line, &rec) != nil || rec.V != 1 {
			continue
		}
		if imp, ok := projectRecord(rec); ok {
			out = append(out, imp)
		}
	}
	return out, nil
}

// projectRecord converts one parsed record into an Impression,
// deriving the per-row fields the log does not carry explicitly:
// SourceRank (this row's index among the SAME provider's rows --
// same-provider rows are consecutive in the delivered list, so the
// count doubles as the within-section index the serve path uses),
// Priority (apps-search is the one prioritized source in production;
// the emission's actual value is used at serve time), and Hour (the
// pick timestamp's local hour).
func projectRecord(rec logLine) (Impression, bool) {
	if len(rec.Shown) == 0 || rec.Picked.Rank < 0 || rec.Picked.Rank >= len(rec.Shown) {
		return Impression{}, false
	}
	imp := Impression{Query: rec.Query, Joined: rec.Joined, Picked: rec.Picked.Rank}
	if ts, terr := time.Parse(time.RFC3339, rec.TS); terr == nil {
		imp.TS = ts
	}
	hour := imp.TS.Local().Hour()
	perPlugin := map[string]int{}
	imp.Rows = make([]Row, len(rec.Shown))
	for i, sr := range rec.Shown {
		row := Row{Kind: sr.Kind, Query: rec.Query, Hour: hour}
		switch sr.Kind {
		case KindFile:
			row.Class = sr.Class
			row.EffClass = sr.EffClass
			row.Align = sr.Align
			row.Boost = sr.Boost
			row.Recency = sr.Recency
			row.Cwd = sr.Cwd
			row.Penalty = sr.Penalty
			row.IsDir = sr.IsDir
			row.Depth = sr.Depth
			row.Ext = sr.Ext
		case KindPlugin:
			row.Plugin = sr.Plugin
			row.Score = sr.Score
			row.SourceRank = perPlugin[sr.Plugin]
			perPlugin[sr.Plugin]++
			if sr.Plugin == prioritizedBuiltin {
				row.Priority = 1
			}
		default:
			// An unknown row kind means a future log version; the whole
			// record is not this reader's to interpret.
			return Impression{}, false
		}
		imp.Rows[i] = row
	}
	return imp, true
}

// prioritizedBuiltin is the one production source with a non-zero
// emission priority (internal/plugin's apps-search, priority 1); the
// log does not record priorities, so training derives them from the
// id while the serve path reads the emission's actual value.
const prioritizedBuiltin = "apps-search"

// readBounded reads path entirely, mapping "missing" and "absurdly
// large" both onto (nil, nil).
func readBounded(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("arbiter: reading %s: %w", path, err)
	}
	if st.Size() > maxSourceFileBytes {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("arbiter: reading %s: %w", path, err)
	}
	return data, nil
}
