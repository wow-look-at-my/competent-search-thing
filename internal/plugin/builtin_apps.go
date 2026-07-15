package plugin

import (
	"context"
	"sort"
	"strings"
)

// builtinAppsID is the provider id of the installed-apps launcher.
const builtinAppsID = "apps"

// maxAppResults caps one launcher response.
const maxAppResults = 15

// desktopFieldCodes are the freedesktop Exec field codes stripped by
// parseDesktopExec: %f %F %u %U %d %D %n %N %i %c %k %v %m.
const desktopFieldCodes = "fFuUdDnNickvm"

// appsProvider is the installed-application launcher, reachable only
// via its bangs (!app / !launch). It searches the snapshot supplied by
// the InstalledApps getter (the getter pre-filters terminal apps);
// results launch via a run_command action with the parsed Exec argv.
type appsProvider struct {
	builtinBase
	installed func() []InstalledApp
}

func newAppsProvider(installed func() []InstalledApp) *appsProvider {
	return &appsProvider{
		builtinBase: builtinBase{pid: builtinAppsID, name: "Launch", bangs: []string{"app", "launch"}},
		installed:   installed,
	}
}

func (p *appsProvider) query(_ context.Context, req Request) ([]Result, []string, error) {
	if !req.Targeted || p.installed == nil {
		return nil, nil, nil
	}
	type entry struct {
		app   InstalledApp
		argv  []string
		score float64
	}
	// An empty search lists everything at the default score, which the
	// name tiebreak below turns into "first N alphabetically".
	needle := strings.ToLower(req.Stripped)
	var matches []entry
	for _, a := range p.installed() {
		score := DefaultScore
		if needle != "" {
			lower := strings.ToLower(a.Name)
			switch {
			case strings.HasPrefix(lower, needle):
				score = 100
			case strings.Contains(lower, needle):
				score = 80
			default:
				continue
			}
		}
		argv := parseDesktopExec(a.Exec)
		if len(argv) == 0 || argv[0] == "" {
			continue // nothing launchable
		}
		matches = append(matches, entry{app: a, argv: argv, score: score})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		ni, nj := strings.ToLower(matches[i].app.Name), strings.ToLower(matches[j].app.Name)
		if ni != nj {
			return ni < nj
		}
		return matches[i].app.Name < matches[j].app.Name
	})
	if len(matches) > maxAppResults {
		matches = matches[:maxAppResults]
	}
	results := make([]Result, 0, len(matches))
	for _, m := range matches {
		score := m.score
		results = append(results, Result{
			Title:    m.app.Name,
			Subtitle: strings.Join(m.argv, " "),
			Icon:     "app",
			Score:    &score,
			Action:   &Action{Type: ActionRunCommand, Argv: m.argv},
		})
	}
	return results, nil, nil
}

// parseDesktopExec splits a freedesktop .desktop Exec line into argv:
// arguments separated by spaces/tabs, double quotes group an argument
// (a backslash escapes the next character inside quotes), %% unescapes
// to a literal percent, and the standard field codes (%f %F %u %U %d
// %D %n %N %i %c %k %v %m) are stripped -- launched from the search
// bar they expand to nothing, and a field code forming a whole
// argument disappears entirely. Deliberately small: exotic reserved-
// character quoting from the full spec is out of scope.
func parseDesktopExec(exec string) []string {
	var argv []string
	var cur strings.Builder
	started := false // tracks explicit empty "" arguments
	inQuote := false
	flush := func() {
		if started || cur.Len() > 0 {
			argv = append(argv, cur.String())
		}
		cur.Reset()
		started = false
	}
	for i := 0; i < len(exec); i++ {
		c := exec[i]
		switch {
		case inQuote:
			switch c {
			case '\\':
				if i+1 < len(exec) {
					i++
					cur.WriteByte(exec[i])
				}
			case '"':
				inQuote = false
			default:
				cur.WriteByte(c)
			}
		case c == '"':
			inQuote = true
			started = true
		case c == ' ' || c == '\t':
			flush()
		case c == '%' && i+1 < len(exec):
			next := exec[i+1]
			switch {
			case next == '%':
				cur.WriteByte('%')
				started = true
				i++
			case strings.ContainsRune(desktopFieldCodes, rune(next)):
				i++ // strip the field code
			default:
				cur.WriteByte('%') // unknown code: keep the percent literally
				started = true
			}
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	flush()
	return argv
}
