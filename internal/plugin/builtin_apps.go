package plugin

import (
	"context"
	"strings"

	"github.com/wow-look-at-my/competent-search-thing/internal/match"
)

// builtinAppsID is the provider id of the installed-apps launcher.
const builtinAppsID = "apps"

// maxAppResults caps one launcher response.
const maxAppResults = 15

// desktopFieldCodes are the freedesktop Exec field codes stripped by
// parseDesktopExec: %f %F %u %U %d %D %n %N %i %c %k %v %m.
const desktopFieldCodes = "fFuUdDnNickvm"

// appsProvider is the installed-application launcher, reachable only
// via its bangs (!app / !launch). It is a candidate SOURCE: the whole
// snapshot goes to the engine, which lists everything for an empty
// rest (ScoreListed, alphabetical) and text-gates/ranks a needle --
// launch actions carry the parsed Exec argv.
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

func (p *appsProvider) limit() int { return maxAppResults }

func (p *appsProvider) candidates(_ context.Context, req Request) ([]match.Candidate, error) {
	if !req.Targeted || p.installed == nil {
		return nil, nil
	}
	return appCandidates(p.installed()), nil
}

// appCandidates builds the launch candidates shared by the targeted
// launcher (!app / !launch) and the untargeted apps-search source:
// the match field is the app NAME, entries whose Exec parses to
// nothing launchable are dropped, and every row launches its app via
// a run_command action carrying the parsed .desktop Exec argv. No
// scores, no filtering -- the engine owns both.
func appCandidates(installed []InstalledApp) []match.Candidate {
	out := make([]match.Candidate, 0, len(installed))
	for _, a := range installed {
		argv := parseDesktopExec(a.Exec)
		if len(argv) == 0 || argv[0] == "" {
			continue // nothing launchable
		}
		exec := strings.Join(argv, " ")
		out = append(out, match.Candidate{
			Display: a.Name,
			Texts:   []string{a.Name},
			SortKey: exec,
			Payload: Result{
				Title:    a.Name,
				Subtitle: exec,
				Icon:     "app",
				Action:   &Action{Type: ActionRunCommand, Argv: argv},
			},
		})
	}
	return out
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
