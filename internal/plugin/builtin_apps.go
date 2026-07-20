package plugin

import (
	"context"
	"math"
	"strings"

	"github.com/wow-look-at-my/competent-search-thing/internal/launch"
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
	usage     func(string) float64
}

func newAppsProvider(installed func() []InstalledApp, usage func(string) float64) *appsProvider {
	return &appsProvider{
		builtinBase: builtinBase{pid: builtinAppsID, name: "Launch", bangs: []string{"app", "launch"}},
		installed:   installed,
		usage:       usage,
	}
}

func (p *appsProvider) limit() int { return maxAppResults }

func (p *appsProvider) candidates(_ context.Context, req Request) ([]match.Candidate, error) {
	if !req.Targeted || p.installed == nil {
		return nil, nil
	}
	return appCandidates(p.installed(), p.usage), nil
}

// appCandidates builds the launch candidates shared by the targeted
// launcher (!app / !launch) and the untargeted apps-search source:
// the match field is the app NAME, entries whose Exec parses to
// nothing launchable are dropped, and every row launches its app via
// a run_command action carrying the parsed .desktop Exec argv. No
// scores, no filtering -- the engine owns both. An installed app with
// an icon ref additionally carries the internal-only IconKey
// ("app:<ref>") so the frontend can swap the glyph for the real app
// icon once ResolveIcons answers.
//
// Action.DesktopID is stamped ONLY when the snapshot id actually is a
// bare *.desktop file name (launch.ValidDesktopID -- the same check
// the app layer re-applies before executing): the darwin scan fills
// ID with the ".app" bundle name, which is not a desktop entry and
// used to fail that re-validation, erroring every macOS app launch.
// Off linux the credentialed desktop-id path does not exist anyway.
//
// usage (Options.AppUsage; nil = all zero) supplies the decayed
// launch count recorded under the row's AppUsageKey, carried as the
// candidate's TieBreak: the engine orders equal-tier, equal-score
// rows by it before the name, so the apps the user actually launches
// beat alphabetical coincidences of the same match class -- and
// because the tier stays the primary sort key, usage can never lift
// a row across match classes.
func appCandidates(installed []InstalledApp, usage func(string) float64) []match.Candidate {
	out := make([]match.Candidate, 0, len(installed))
	for _, a := range installed {
		argv := parseDesktopExec(a.Exec)
		if len(argv) == 0 || argv[0] == "" {
			continue // nothing launchable
		}
		exec := strings.Join(argv, " ")
		act := &Action{Type: ActionRunCommand, Argv: argv}
		if launch.ValidDesktopID(a.ID) == nil {
			act.DesktopID = a.ID
		}
		res := Result{
			Title:    a.Name,
			Subtitle: exec,
			Icon:     "app",
			Action:   act,
		}
		if a.Icon != "" {
			res.IconKey = "app:" + a.Icon
		}
		var tie int64
		if usage != nil {
			if key := AppUsageKey(act.DesktopID, argv); key != "" {
				tie = usageTieBreak(usage(key))
			}
		}
		out = append(out, match.Candidate{
			Display:  a.Name,
			Texts:    []string{a.Name},
			TieBreak: tie,
			SortKey:  exec,
			Payload:  res,
		})
	}
	return out
}

// appUsagePrefix namespaces app-launch keys inside the frecency
// store: keys start "app:" and can therefore never collide with the
// absolute file paths the file-ranking blend looks up (exact-key
// lookups on both sides).
const appUsagePrefix = "app:"

// usageTieBreakScale converts a decayed launch count to the integer
// TieBreak domain at 1/1000 resolution (counts within a milli-launch
// of each other tie and fall back to the name order).
const usageTieBreakScale = 1000

// AppUsageKey derives the stable frecency-store key for one
// launchable app, computable identically from the installed-app
// snapshot (lookup time) and from the echoed run_command action
// (record time): the desktop id when one is stamped (the linux
// .desktop world), else the parsed Exec argv joined with single
// spaces -- the darwin shape, where Exec is `open -a "<bundle>"` and
// no desktop id exists. Empty when neither part is available.
func AppUsageKey(desktopID string, argv []string) string {
	if desktopID != "" {
		return appUsagePrefix + desktopID
	}
	if len(argv) == 0 {
		return ""
	}
	return appUsagePrefix + strings.Join(argv, " ")
}

// AppPickKey returns the usage key to record for one successfully
// executed plugin action: non-empty only for a run_command launch
// from one of the two builtin app sources (the targeted !app /
// !launch launcher and the untargeted apps-search section, which
// share appCandidates and therefore the key shape). run_commands
// from external plugins are not app launches and record nothing.
func AppPickKey(pluginID string, action *Action) string {
	if action == nil || action.Type != ActionRunCommand {
		return ""
	}
	if pluginID != builtinAppsID && pluginID != builtinAppsSearchID {
		return ""
	}
	return AppUsageKey(action.DesktopID, action.Argv)
}

// usageTieBreak maps a decayed launch count onto the candidate
// TieBreak domain. Non-positive (and NaN) counts are 0 -- the cold
// pure-name ordering.
func usageTieBreak(count float64) int64 {
	if !(count > 0) {
		return 0
	}
	return int64(math.Round(count * usageTieBreakScale))
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
