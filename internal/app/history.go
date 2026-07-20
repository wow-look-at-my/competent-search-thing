package app

import (
	"log"
	"path/filepath"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/history"
)

// historyFileName is the query-history file, next to config.json.
const historyFileName = "history.json"

// startHistory brings the query-history store up once, at Startup.
// The store lives at <configDir>/history.json and persists unless
// config's history.persistEnabled = false opted out (Options carries
// the flag, inverted, like TrayDisabled). Failures degrade, never block: an
// unresolvable config dir or an unreadable/corrupt file is logged
// once with a "history: " prefix and the app runs on -- a nil store
// turns the bound methods into safe no-ops.
func (a *App) startHistory() {
	dir, err := config.Dir()
	if err != nil {
		log.Printf("history: %v (history disabled)", err)
		return
	}
	st := history.New(filepath.Join(dir, historyFileName), !a.opt.HistoryPersistDisabled)
	if err := st.Load(); err != nil {
		log.Printf("history: %v (starting empty)", err)
	}
	a.mu.Lock()
	a.history = st
	a.mu.Unlock()
}

// historyStore returns the store; nil before Startup.
func (a *App) historyStore() *history.Store {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.history
}

// applyHistory is the config live-apply path for
// history.persistEnabled: build a fresh store at the same path with
// the new persist flag, seed it -- from disk when persistence turns
// on, so older entries survive -- then replay the current in-memory
// entries on top (Add's move-to-newest dedup keeps the recall order
// sane), and swap it in. In-session recall survives the flip either
// way; turning persistence off leaves history.json on disk untouched
// (persist off means "stop writing", not "delete").
func (a *App) applyHistory(next *config.Config) error {
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	st := history.New(filepath.Join(dir, historyFileName), config.Enabled(next.History.PersistEnabled))
	if err := st.Load(); err != nil {
		log.Printf("history: %v (starting from the in-memory entries)", err)
	}
	old := a.historyStore()
	if old != nil {
		logged := false
		for _, e := range old.Entries() {
			// Add updates the in-memory list even when the persist
			// write fails, so the replay always completes; the first
			// write failure is logged once (the rest would repeat it).
			if err := st.Add(e); err != nil && !logged {
				logged = true
				log.Printf("history: %v", err)
			}
		}
	}
	a.mu.Lock()
	a.history = st
	a.mu.Unlock()
	return nil
}

// GetHistory returns the committed query history, oldest to newest,
// always as a non-nil private copy. The frontend fetches it at
// wire-up and refetches after every AddHistory.
func (a *App) GetHistory() []string {
	st := a.historyStore()
	if st == nil {
		return []string{}
	}
	return st.Entries()
}

// AddHistory records one executed query -- the frontend calls it
// after an activation actually ran (a file opened or revealed, a
// plugin action executed). The store trims the entry, skips blanks,
// and moves an exact repeat to the newest slot; persistence problems
// are logged here and never surface to the frontend (the in-memory
// list is updated regardless).
func (a *App) AddHistory(entry string) {
	st := a.historyStore()
	if st == nil {
		return
	}
	if err := st.Add(entry); err != nil {
		log.Printf("history: %v", err)
	}
}
