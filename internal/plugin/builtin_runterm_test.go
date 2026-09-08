package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/match"
	"github.com/wow-look-at-my/go-containers/set"
)

// testRunner builds a TerminalRunner resolving the named executables
// under /usr/bin and wrapping commands the way "xterm -e" does.
func testRunner(found ...string) *TerminalRunner {
	set := set.New[string]()
	for _, f := range found {
		set.Add(f)
	}
	return &TerminalRunner{
		Name: "xterm",
		LookPath: func(name string) (string, error) {
			if set.Contains(name) {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		Command: func(argv []string) []string {
			return append([]string{"/usr/bin/xterm", "-e"}, argv...)
		},
	}
}

// runTermRows dispatches one query through the provider and returns
// the minted rows plus the emission priority.
func runTermRows(t *testing.T, term *TerminalRunner, query string) ([]Result, int) {
	t.Helper()
	p := newRunTermProvider(term)
	stripped, _, ok := p.match(query, nil)
	if !ok {
		return nil, 0
	}
	rows, best, err := sourceResults(p, context.Background(), Request{Query: query, Stripped: stripped}, false)
	require.NoError(t, err)
	return rows, p.priority(best)
}

func TestRunTermExactPathMatchIsOffered(t *testing.T) {
	rows, priority := runTermRows(t, testRunner("htop"), "htop")
	require.Len(t, rows, 1)
	assert.Equal(t, "htop", rows[0].Title)
	assert.Equal(t, "Run in xterm -- /usr/bin/htop", rows[0].Subtitle)
	assert.Equal(t, "terminal", rows[0].Icon)
	require.NotNil(t, rows[0].Action)
	assert.Equal(t, ActionRunCommand, rows[0].Action.Type)
	assert.Equal(t, []string{"/usr/bin/xterm", "-e", "/usr/bin/htop"}, rows[0].Action.Argv)
	assert.Equal(t, sourcePriorityRunTerm, priority, "an exact program name is promoted above the file results")
}

func TestRunTermPassesArgumentsThrough(t *testing.T) {
	rows, _ := runTermRows(t, testRunner("htop"), `htop -d 5 "my file.txt"`)
	require.Len(t, rows, 1)
	assert.Equal(t, []string{"/usr/bin/xterm", "-e", "/usr/bin/htop", "-d", "5", "my file.txt"}, rows[0].Action.Argv)
	assert.Equal(t, `htop -d 5 "my file.txt"`, rows[0].Title)
}

func TestRunTermNoRowWithoutAnExactMatch(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{name: "prefix of a program is not a program", query: "hto"},
		{name: "not on PATH at all", query: "definitely-not-installed"},
		{name: "an absolute path is a file result, not a PATH lookup", query: "/usr/bin/htop"},
		{name: "a relative path is a file result too", query: "./htop"},
		{name: "an unterminated quote is a half-typed command", query: `htop "unclosed`},
		{name: "whitespace only", query: "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, priority := runTermRows(t, testRunner("htop"), tt.query)
			assert.Empty(t, rows)
			assert.Zero(t, priority)
		})
	}
}

// A terminal that cannot express the command (darwin's "open -a
// Terminal" with arguments) declines by answering nil, and the row
// must not be offered rather than running something else.
func TestRunTermSkipsCommandsTheTerminalCannotExpress(t *testing.T) {
	term := testRunner("htop")
	term.Command = func(argv []string) []string {
		if len(argv) > 1 {
			return nil
		}
		return append([]string{"/usr/bin/open", "-a", "Terminal"}, argv...)
	}
	rows, _ := runTermRows(t, term, "htop -d 5")
	assert.Empty(t, rows)

	rows, _ = runTermRows(t, term, "htop")
	require.Len(t, rows, 1)
	assert.Equal(t, []string{"/usr/bin/open", "-a", "Terminal", "/usr/bin/htop"}, rows[0].Action.Argv)
}

// The whole invocation must survive the run_command argv cap the
// sanitizer and the app layer's re-validation apply.
func TestRunTermDropsOversizedCommands(t *testing.T) {
	long := "htop"
	for i := 0; i < maxArgvEntries; i++ {
		long += " -x"
	}
	rows, _ := runTermRows(t, testRunner("htop"), long)
	assert.Empty(t, rows)
}

func TestRunTermRegistrationNeedsATerminal(t *testing.T) {
	r := New(Options{})
	_, ok := r.byID[builtinRunTermID]
	assert.False(t, ok, "no terminal on this machine means no provider at all")

	r = New(Options{Terminal: testRunner("htop")})
	_, ok = r.byID[builtinRunTermID]
	assert.True(t, ok)

	r = New(Options{
		Terminal: testRunner("htop"),
		Entries:  map[string]Entry{builtinRunTermID: {Disabled: true}},
	})
	_, ok = r.byID[builtinRunTermID]
	assert.False(t, ok, "plugins.entries disables it like any other builtin")
}

// A half-wired runner (one seam nil) must never register: the
// provider would otherwise be a section that can never answer.
func TestRunTermRegistrationNeedsBothSeams(t *testing.T) {
	half := &TerminalRunner{Name: "xterm", LookPath: testRunner("htop").LookPath}
	r := New(Options{Terminal: half})
	_, ok := r.byID[builtinRunTermID]
	assert.False(t, ok)
	assert.False(t, (*TerminalRunner)(nil).usable())
}

func TestRunTermMatchGate(t *testing.T) {
	p := newRunTermProvider(testRunner("R"))
	// Single-rune program names are real; the exact-PATH gate is what
	// keeps the section quiet, not a minimum query length.
	stripped, _, ok := p.match("R", nil)
	assert.True(t, ok)
	assert.Equal(t, "R", stripped)
	_, _, ok = p.match("", nil)
	assert.False(t, ok)
	assert.Equal(t, 1, p.limit())
	assert.Zero(t, p.priority(match.TierSubstring), "only an exact match earns the promoted zone")
}

func TestSplitCommand(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  []string
		wantB bool
	}{
		{name: "plain words", in: "htop -d 5", want: []string{"htop", "-d", "5"}, wantB: true},
		{name: "collapses runs of whitespace", in: "  htop \t -d  ", want: []string{"htop", "-d"}, wantB: true},
		{name: "double quotes hold spaces", in: `mpv "my film.mkv"`, want: []string{"mpv", "my film.mkv"}, wantB: true},
		{name: "single quotes too", in: `mpv 'my film.mkv'`, want: []string{"mpv", "my film.mkv"}, wantB: true},
		{name: "quotes inside a word", in: `git commit -m"wip"`, want: []string{"git", "commit", "-mwip"}, wantB: true},
		{name: "the other quote is literal", in: `echo "it's"`, want: []string{"echo", "it's"}, wantB: true},
		{name: "an empty quoted argument is an argument", in: `grep "" f`, want: []string{"grep", "", "f"}, wantB: true},
		{name: "unterminated quote", in: `mpv "film`, want: nil, wantB: false},
		{name: "empty", in: "", want: nil, wantB: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := splitCommand(tt.in)
			assert.Equal(t, tt.wantB, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}
