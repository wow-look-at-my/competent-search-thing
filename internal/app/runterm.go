package app

import (
	"log"

	"github.com/wow-look-at-my/competent-search-thing/internal/plugin"
	"github.com/wow-look-at-my/competent-search-thing/internal/terminal"
)

// terminalRunner resolves the machine's terminal emulator into the
// plugin layer's run-in-terminal capability: typing the name of a
// program on PATH offers a row that runs it in a terminal window, so
// "htop" is one Enter away instead of unreachable.
//
// nil (no terminal emulator found, or no PATH lookup seam) means the
// builtin source is not registered at all -- there is no fallback
// that would run a command somewhere the user cannot see it. The
// outcome is logged ONCE per app run, not per registry reload.
func (a *App) terminalRunner() *plugin.TerminalRunner {
	if a.plat.lookPath == nil {
		return nil
	}
	term, ok := terminal.Detect(terminal.Options{
		GOOS:     a.plat.goos,
		Getenv:   a.plat.getenv,
		LookPath: a.plat.lookPath,
	})
	if !ok {
		a.termOnce.Do(func() {
			log.Printf("run: no terminal emulator found; typing a program name offers no run-in-terminal result")
		})
		return nil
	}
	a.termOnce.Do(func() {
		log.Printf("run: %s runs PATH commands typed into the bar (%s)", term.Name, term.Path)
	})
	return &plugin.TerminalRunner{
		Name:     term.Name,
		LookPath: a.plat.lookPath,
		Command:  term.Command,
	}
}
