package priors

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"time"
)

// Tolerant readers for the two local data sources. Both parse ONLY
// the fields this package needs, ignore unknown fields, skip
// malformed entries, and treat a missing file as empty data with a
// nil error. Errors are reserved for real IO problems the app should
// log once.

const (
	// maxSourceFileBytes bounds one source file read: the telemetry
	// writer rotates at 4 MiB and frecency.json is 4096 entries, so
	// anything bigger is a hand-grown anomaly -- ignored (empty data)
	// rather than swallowed into memory.
	maxSourceFileBytes = 64 << 20
)

// PickRecord is the minimal projection of one telemetry log line this
// package consumes: when the pick was recorded, the query as logged
// ("" when the user opted query retention off), the shown FILE rows'
// paths in delivered order, and the picked path ("" when the pick was
// not a file row -- such records still contribute impressions).
type PickRecord struct {
	TS         time.Time
	Query      string
	ShownFiles []string
	PickedPath string
}

// telemetryLine mirrors just the record fields the projection needs;
// json.Unmarshal drops everything else (the tolerance contract).
type telemetryLine struct {
	V      int    `json:"v"`
	TS     string `json:"ts"`
	Query  string `json:"query"`
	Shown  []telemetryRow `json:"shown"`
	Picked telemetryRow   `json:"picked"`
}

type telemetryRow struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// ReadTelemetryFile reads one telemetry JSONL file into PickRecords,
// oldest first (file order). A missing file returns (nil, nil); an
// oversized file is ignored the same way. Lines that are not valid v1
// records -- torn final lines included -- are skipped silently: the
// log is loss-tolerant by design.
func ReadTelemetryFile(path string) ([]PickRecord, error) {
	data, err := readBounded(path)
	if data == nil || err != nil {
		return nil, err
	}
	var out []PickRecord
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var rec telemetryLine
		if json.Unmarshal(line, &rec) != nil || rec.V != 1 {
			continue
		}
		p := PickRecord{Query: rec.Query}
		if ts, terr := time.Parse(time.RFC3339, rec.TS); terr == nil {
			p.TS = ts
		}
		// The rank is the shown slice index by contract; the pick
		// carries its own path, so ranks need no storing here.
		for _, row := range rec.Shown {
			if row.Kind != "file" || row.Path == "" {
				continue
			}
			p.ShownFiles = append(p.ShownFiles, row.Path)
		}
		if rec.Picked.Kind == "file" && rec.Picked.Path != "" {
			p.PickedPath = rec.Picked.Path
		}
		if len(p.ShownFiles) == 0 && p.PickedPath == "" {
			continue // nothing this package can learn from
		}
		out = append(out, p)
	}
	return out, nil
}

// frecencyFile mirrors internal/frecency's persisted shape
// ({"v":1,"entries":{path:{c,t}}}) -- the on-disk format is the
// contract, deliberately re-declared here.
type frecencyFile struct {
	V       int `json:"v"`
	Entries map[string]struct {
		C float64   `json:"c"`
		T time.Time `json:"t"`
	} `json:"entries"`
}

// ReadFrecencyWeights reads frecency.json and returns each stored
// path's open count decayed to now (the same half-life this package
// uses everywhere). Missing or oversized files return (nil, nil);
// corrupt files return the reason once for logging; garbage entries
// (empty path, non-positive/NaN count, zero time) are dropped like
// frecency.Load drops them.
func ReadFrecencyWeights(path string, now time.Time) (map[string]float64, error) {
	data, err := readBounded(path)
	if data == nil || err != nil {
		return nil, err
	}
	var raw frecencyFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("priors: %s is not a frecency JSON file: %v", path, err)
	}
	if raw.V != 1 {
		return nil, fmt.Errorf("priors: %s has unsupported frecency version %d (want 1)", path, raw.V)
	}
	out := make(map[string]float64, len(raw.Entries))
	for p, e := range raw.Entries {
		if p == "" || e.C <= 0 || math.IsNaN(e.C) || math.IsInf(e.C, 0) || e.T.IsZero() {
			continue
		}
		if w := decayedWeight(e.C, e.T, now); w > 0 {
			out[p] = w
		}
	}
	return out, nil
}

// readBounded reads path entirely, mapping "missing" and "absurdly
// large" both onto (nil, nil).
func readBounded(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("priors: reading %s: %w", path, err)
	}
	if st.Size() > maxSourceFileBytes {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("priors: reading %s: %w", path, err)
	}
	return data, nil
}
