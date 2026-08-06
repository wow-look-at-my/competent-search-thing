# internal/frecency

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/frecency` -- the PURE half of result prioritization:
the signal sources the ranking blend consumes (the blend itself is
internal/index blend.go, the app wiring internal/app frecency.go,
the production ProcTree internal/appctx proctree.go, the knobs
config search.frecency). Modeled on internal/history
throughout: mutex-guarded (RWMutex, queries RLock), injectable
clocks/seams, atomic temp-file-then-rename 0600 persistence,
missing file = empty + nil error, corrupt file = empty + ONE
returned error for logging, defensive copies, nil-receiver-safe
no-ops. Pieces: `Store` (frecency.go; New(path, Options{HalfLife
default 14d, Cap default 4096, Now, Persist}); RecordOpen lazily
decays the stored count to now (0.5^(dt/halfLife)), adds 1,
persists {v:1,entries:{path:{c,t}}} -- the in-memory update
survives a failed write; Boost = the READ-ONLY decayed count, 0
unknown, no write amplification; the cap keeps the highest decayed
counts (ties: newer touch, then path) on record AND load; Load
keeps stored values verbatim (decay applies on read), drops
garbage entries (empty path, c <= 0, zero t), rejects wrong
versions; empty paths skipped, never trimmed). `PathPenalty`
(penalty.go; pure location-noise score in [0, 1], a rank NUDGE
never an exclusion: noise-class dir components (.cache/cache/.git/
node_modules/tmp/.tmp/temp/.temp, case-insensitive -- /tmp and
/var/tmp arrive via their own "tmp" component) cost 0.3 each,
other dot-DIRS 0.1, depth past 6 components 0.05 each capped at
0.25 so deep-but-clean < /tmp workspace < deep cache noise (the
motivating log.txt report, pinned as ordering tests); the BASE
name is exempt (dotfiles like .bashrc are prime targets); / and \
both split). `Probe` (probe.go; NewProbe(ProbeOptions{TTL 5m,
Lstat, Now, Workers 8}); BatchRecency(ctx, paths, budget) =
max(atime, mtime) per path -- linux reads Stat_t.Atim
(recency_linux.go), darwin/windows fall back to mtime-only
(recency_other.go, keeps the windows/amd64 cross-compile pure) --
behind a TTL cache (failures cached negatively; bounded 4096:
sweep expired, then reset), bounded worker fan-out, and a HARD
wall-clock budget/ctx cutoff: unstatted paths are simply absent
(zero time on lookup), stragglers finish their one stat into the
cache for the next query; the relatime atime-coarseness caveat
lives in the package doc, mtime still catches just-downloaded).
`DeriveCwd(tree ProcTree, rootPID)` (cwd.go; the focused-app
working-directory heuristic over the injectable
{Children,Cwd,Foreground} seam: the tpgid-style Foreground hint on
the root wins when its cwd is meaningful, else the deepest
meaningful cwd depth-first (root included, ties keep the earlier
find, 32-deep cap + visited set so cycles are harmless); "/", the
user home, and unreadable are NOT meaningful (ok=false when
nothing qualifies) -- a terminal parks at ~, the shell/editor
CHILD holds the real signal; the production /proc wiring is
appctx.ProcTree, injected through the app's plat.procTree seam)
plus `CwdBoost(path, cwd, weight)` (full
weight for the cwd itself and direct children, halved per extra
component, component-wise containment so /projX is not under
/proj; weight 0 = disabled, "/" or "" cwd = no signal). `Signals`
(signals.go) bundles Boost/Penalty/Recency/CwdBoost plus the
Cwd/CwdWeight fields for the engine blend; the zero value
degrades to all-no-ops (appctx.Cache pattern) AND deactivates the
blend outright (index.Blend.active). frecency.json
deliberately has NO schema in schemas/ (like history.json).
Exhaustively unit-tested, headless, table-driven: fake clocks,
counting/blocking lstat fakes, scripted ProcTree fakes, real
tempdir files (os.Chtimes) for the atime/mtime max path.
