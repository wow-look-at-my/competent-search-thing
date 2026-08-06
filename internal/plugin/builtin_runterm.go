package plugin

import (
	"context"
	"fmt"
	"strings"

	"github.com/wow-look-at-my/competent-search-thing/internal/match"
)

// builtinRunTermID is the provider id of the run-a-PATH-command
// source (disable via plugins.entries["run-terminal"]).
const builtinRunTermID = "run-terminal"

// sourcePriorityRunTerm places the run-in-terminal row in the
// promoted zone above the file results. It only ever fires for a
// query whose first word EXACTLY names an executable on PATH, so the
// section cannot fill up with weak guesses; and it sorts after the
// apps section on an exact tie (same priority, same exact-tier score,
// "apps-search" < "run-terminal"), so typing the name of a GUI
// application still launches the application rather than a terminal
// holding it.
const sourcePriorityRunTerm = 1

// maxRunTermArgv caps the whole terminal invocation, matching the
// run_command argv limit the sanitizer and the app layer's
// re-validation enforce (maxArgvEntries). A command that would not
// survive that check is not offered at all.
const maxRunTermArgv = maxArgvEntries

// TerminalRunner is the run-in-terminal capability the app layer
// supplies (internal/terminal resolves it; nil in Options means the
// provider is NOT REGISTERED at all -- the OpenWindows convention --
// so machines with no terminal emulator simply never see the row).
type TerminalRunner struct {
	// Name is the terminal's display name, shown in the row subtitle
	// so the user knows what will open.
	Name string
	// LookPath resolves a bare command name against PATH, reporting
	// an error when it is not an executable there (exec.LookPath).
	LookPath func(name string) (string, error)
	// Command builds the argv that runs the given command inside the
	// terminal, or nil when the terminal cannot express it (a
	// command with arguments on darwin's "open -a Terminal" path).
	Command func(argv []string) []string
}

// usable reports whether the runner carries both seams.
func (t *TerminalRunner) usable() bool {
	return t != nil && t.LookPath != nil && t.Command != nil
}

// runTermProvider offers "run this command in a terminal" for a query
// whose first word is an executable on PATH. It exists because a
// searchbar that can launch .desktop applications still could not run
// the terminal programs a PATH holds -- typing "htop" found files
// named htop and no way to run it.
//
// Deliberately exact-match-only: the row appears when the typed word
// IS a program name, never for a prefix or a fuzzy hit. That keeps a
// promoted, command-executing row out of the way of ordinary
// searching, and it is the whole rule a user has to keep in their
// head.
type runTermProvider struct {
	builtinBase
	trigger *Trigger
	term    *TerminalRunner
}

func newRunTermProvider(term *TerminalRunner) *runTermProvider {
	// MinQueryLen 1: real program names are that short ("R", "vi",
	// "ls"), and the exact-PATH-match gate below is what actually
	// keeps the section quiet.
	t := &Trigger{AllQueries: true, MinQueryLen: 1}
	_ = t.Compile() // never fails without a regex
	return &runTermProvider{
		builtinBase: builtinBase{pid: builtinRunTermID, name: "Run"},
		trigger:     t,
		term:        term,
	}
}

// match overrides builtinBase: this source fans out on untargeted
// queries like the other all-queries builtins.
func (p *runTermProvider) match(query string, focused *AppInfo) (string, int, bool) {
	stripped, ok := p.trigger.Match(query, focused)
	if !ok {
		return "", 0, false
	}
	return stripped, p.trigger.Boost(focused), true
}

func (p *runTermProvider) limit() int { return 1 }

// priority implements the optional prioritized extension. The one row
// this source can emit is always an exact match (its match text IS
// the query), so the gate is a statement of that invariant rather
// than a live decision: a future non-exact row would not be promoted.
func (p *runTermProvider) priority(best match.Tier) int {
	if best <= match.TierExact {
		return sourcePriorityRunTerm
	}
	return 0
}

func (p *runTermProvider) candidates(_ context.Context, req Request) ([]match.Candidate, error) {
	if !p.term.usable() {
		return nil, nil
	}
	query := strings.TrimSpace(req.Stripped)
	words, ok := splitCommand(query)
	if !ok || len(words) == 0 {
		return nil, nil
	}
	name := words[0]
	// A path is not a PATH lookup: "./build.sh" and "/usr/bin/htop"
	// are file results (and exec.LookPath would happily resolve them
	// relative to the app's own working directory, which is not
	// where the user typed them).
	if strings.ContainsAny(name, `/\`) {
		return nil, nil
	}
	exe, err := p.term.LookPath(name)
	if err != nil {
		return nil, nil
	}
	// The resolved path runs, not the bare name: the terminal starts
	// with the app's environment, and the row's subtitle already
	// promised this exact program.
	argv := p.term.Command(append([]string{exe}, words[1:]...))
	if len(argv) == 0 || len(argv) > maxRunTermArgv {
		return nil, nil
	}
	subtitle := fmt.Sprintf("Run in %s -- %s", p.term.Name, exe)
	// Match fields: the whole command line, then each of its words.
	// The engine requires every query TERM to match some field, and
	// takes the worst per-term best tier -- so a bare "htop" hits the
	// full-line field exactly, and "htop -d 5" has each of its words
	// hit its own field exactly. Either way the row is minted at the
	// exact tier, which is the truth: this row exists BECAUSE the
	// query names a program.
	texts := append([]string{query}, words...)
	return []match.Candidate{{
		Display: query,
		Texts:   texts,
		SortKey: exe,
		Payload: Result{
			Title:    query,
			Subtitle: subtitle,
			Icon:     "terminal",
			Action:   &Action{Type: ActionRunCommand, Argv: argv},
		},
	}}, nil
}

// splitCommand splits a typed command line into words, honoring
// single and double quotes so an argument with spaces survives
// ("mpv 'my film.mkv'"). Words are passed to the program as separate
// arguments and never re-joined into a shell string, so there is no
// shell to escape: quoting here only decides where one argument ends.
// ok is false for an unterminated quote -- the user is mid-word, and
// guessing where they meant it to close would run a different command
// than the one on screen.
func splitCommand(s string) (words []string, ok bool) {
	var cur strings.Builder
	var quote rune // 0 = unquoted
	started := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			started = true // "" is a real (empty) argument
		case r == ' ' || r == '\t':
			if started {
				words = append(words, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if quote != 0 {
		return nil, false
	}
	if started {
		words = append(words, cur.String())
	}
	return words, true
}
