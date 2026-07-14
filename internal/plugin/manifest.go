package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Plugin transport types.
const (
	TypeCommand = "command"
	TypeHTTP    = "http"
)

// Timeout bounds (milliseconds).
const (
	defaultTimeoutMS = 1500
	minTimeoutMS     = 100
	maxTimeoutMS     = 10000
)

// idRe validates plugin ids and bang names.
var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// contextParts are the app-context parts a manifest may declare.
var contextParts = map[string]bool{
	"focused":   true,
	"running":   true,
	"installed": true,
}

// Manifest describes one plugin, loaded from
// <configDir>/plugins/<dir>/manifest.json. LoadDir fills defaults and
// validates; a Manifest that came out of LoadDir is ready to use and
// its Trigger (when present) is compiled.
type Manifest struct {
	V    int    `json:"v"`
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	// Trigger gates normal (non-bang) dispatch; nil means the plugin
	// is reachable through its bangs only.
	Trigger *Trigger `json:"trigger"`
	// Bangs lists the bang names targeting this plugin. Missing means
	// [id]; an explicit empty list means "no bangs" (and requires a
	// trigger, otherwise the plugin would be unreachable).
	Bangs []string `json:"bangs"`
	// Context names the app-context parts this plugin receives:
	// focused, running, installed. Undeclared parts are never sent.
	Context   []string     `json:"context"`
	TimeoutMS int          `json:"timeout_ms"`
	Command   *CommandSpec `json:"command"`
	HTTP      *HTTPSpec    `json:"http"`
	// AllowRunCommand must be true for this plugin's results to carry
	// run_command actions; see SanitizeResponse.
	AllowRunCommand bool `json:"allow_run_command"`

	// Dir is the directory containing the manifest. Command argv
	// programs containing a path separator resolve relative to it,
	// and it becomes the working directory of command plugins.
	Dir string `json:"-"`
}

// CommandSpec configures a command (subprocess) plugin.
type CommandSpec struct {
	Argv []string `json:"argv"`
}

// HTTPSpec configures an HTTP plugin.
type HTTPSpec struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// LoadDir loads dir/*/manifest.json in sorted (alphabetical) order.
// A missing plugins directory yields no manifests and no errors, as
// do subdirectories without a manifest.json. Each broken manifest
// contributes one error (prefixed with its path) and is skipped;
// valid manifests load regardless. When two manifests share an id,
// the alphabetically-first directory wins and the duplicate is
// reported as an error.
func LoadDir(dir string) ([]*Manifest, []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, []error{fmt.Errorf("plugins dir %s: %w", dir, err)}
	}
	var (
		manifests []*Manifest
		errs      []error
		seen      = map[string]string{} // id -> path that claimed it
	)
	for _, e := range entries {
		sub := filepath.Join(dir, e.Name())
		if info, err := os.Stat(sub); err != nil || !info.IsDir() {
			continue // stray files (and dangling symlinks) are not plugins
		}
		p := filepath.Join(sub, "manifest.json")
		data, err := os.ReadFile(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // a directory without a manifest is not a plugin
			}
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
			continue
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			errs = append(errs, fmt.Errorf("%s: parsing: %w", p, err))
			continue
		}
		m.Dir = sub
		if err := m.validate(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
			continue
		}
		if first, dup := seen[m.ID]; dup {
			errs = append(errs, fmt.Errorf("%s: duplicate plugin id %q (already loaded from %s); skipped", p, m.ID, first))
			continue
		}
		seen[m.ID] = p
		manifests = append(manifests, &m)
	}
	return manifests, errs
}

// validate checks the manifest and normalizes it in place: defaults
// are filled (v, name, timeout_ms, bangs), bangs are lowercased and
// deduped, context entries are deduped, and the trigger is compiled.
func (m *Manifest) validate() error {
	if m.V == 0 {
		m.V = 1
	}
	if m.V != 1 {
		return fmt.Errorf("unsupported manifest version %d (want 1)", m.V)
	}
	if !idRe.MatchString(m.ID) {
		return fmt.Errorf("id %q: must match %s", m.ID, idRe)
	}
	if m.Name == "" {
		m.Name = m.ID
	}
	switch m.Type {
	case TypeCommand:
		if m.Command == nil || len(m.Command.Argv) == 0 {
			return fmt.Errorf("type %q requires a non-empty command.argv", TypeCommand)
		}
		for i, a := range m.Command.Argv {
			if a == "" {
				return fmt.Errorf("command.argv[%d] is empty", i)
			}
		}
	case TypeHTTP:
		if m.HTTP == nil || m.HTTP.URL == "" {
			return fmt.Errorf("type %q requires http.url", TypeHTTP)
		}
		if !validHTTPURL(m.HTTP.URL) {
			return fmt.Errorf("http.url %q: must be an absolute http(s) URL", m.HTTP.URL)
		}
	default:
		return fmt.Errorf("type %q: must be %q or %q", m.Type, TypeCommand, TypeHTTP)
	}
	if m.TimeoutMS == 0 {
		m.TimeoutMS = defaultTimeoutMS
	}
	if m.TimeoutMS < minTimeoutMS {
		m.TimeoutMS = minTimeoutMS
	}
	if m.TimeoutMS > maxTimeoutMS {
		m.TimeoutMS = maxTimeoutMS
	}
	if m.Bangs == nil {
		m.Bangs = []string{m.ID}
	}
	if len(m.Bangs) == 0 && m.Trigger == nil {
		return errors.New("no trigger and no bangs: the plugin would be unreachable")
	}
	bangs := make([]string, 0, len(m.Bangs))
	seen := make(map[string]bool, len(m.Bangs))
	for _, b := range m.Bangs {
		b = strings.ToLower(b)
		if !idRe.MatchString(b) {
			return fmt.Errorf("bang %q: must match %s", b, idRe)
		}
		if seen[b] {
			continue
		}
		seen[b] = true
		bangs = append(bangs, b)
	}
	m.Bangs = bangs
	if len(m.Context) > 0 {
		ctx := make([]string, 0, len(m.Context))
		seenCtx := make(map[string]bool, len(m.Context))
		for _, c := range m.Context {
			if !contextParts[c] {
				return fmt.Errorf("context %q: must be one of focused, running, installed", c)
			}
			if seenCtx[c] {
				continue
			}
			seenCtx[c] = true
			ctx = append(ctx, c)
		}
		m.Context = ctx
	}
	if m.Trigger != nil {
		if err := m.Trigger.Compile(); err != nil {
			return err
		}
	}
	return nil
}
