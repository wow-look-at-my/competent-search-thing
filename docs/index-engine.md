# internal/index

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/index` -- the index engine. `Store`: compact
column-oriented data (interned parent-dir table; ONE original-case
name blob with 0x00 separators and one offset table -- deliberately
no lowercased twin of the names or the dir table, case-insensitivity
is folded in at scan time; tombstone removals). fold.go keeps the
BLOB scan machinery (ciScan/ciIndexASCII + the static
name-frequency anchor table) while the FOLD DEFINITION lives in
internal/match and is re-exported under the historical names:
foldPattern picks the regime per query --
all-ASCII queries fold byte-wise (foldTable, 'A'-'Z' only) and scan
the blob with ciIndexASCII (rarest-byte anchor via a static
name-frequency table, bytes.IndexByte over both case variants,
fold-verify around each candidate); queries with non-ASCII runes
fold per rune with unicode.ToLower (decodeRuneAt, foldPrefixLen,
foldContains, foldHasSuffix -- generic over []byte|string) on a
per-entry slow path, O(hay*pat), correct but linear (hundreds of ms
at tens of millions). SEMANTICS (pinned in fold_test.go): ASCII
queries against ASCII data are byte-identical to the old
strings.ToLower behavior; the two runes whose simple lowercase IS
ASCII (U+0130 dotted I -> i, U+212A Kelvin -> k) are NO LONGER
matched by plain-ASCII queries (the fast path never decodes stored
UTF-8), while queries containing them still match both forms;
invalid UTF-8 still compares as U+FFFD per byte. The naive test
reference models share the fold definition (foldPattern + the
testFold helper) but keep independent stdlib-strings matching.
`Store.Query`: case-insensitive substring
search, sharded across NumCPU goroutines with per-shard bounded
top-K heaps; every blob scan maps a hit position back to its entry
through the shared `entryAt(cur, hi, pos)` (search.go, all four scan
sites) -- hits arrive in increasing position order and the cursor
only moves forward, so it walks up to entryProbeSteps (8) entries
sequentially before falling back to the binary search it replaced,
making the dense case one comparison instead of ~log2(shard) closure
calls into a multi-megabyte offset table (1M-entry store: "a" 13.31
-> 9.61 ms, "re" 10.80 -> 9.29 ms; sparse queries unchanged, they
have few hits to pay for). entryat_test.go pins it against that
binary search over every legal (cur, pos) pair;
ranking exact > prefix > substring > fuzzy, dirs before
files, shorter then numeric-aware lexicographic paths (aligned
digit runs DESC -- numorder.go, below). QueryWith dispatches by
match.Terms: whitespace-only = nil, ONE term = the pre-multi engine
byte-identical (a padded query behaves as its trimmed term), 2+
terms = multiterm.go: ALL terms must match the name order-free;
classSub when every term substring-matches, classFuzzy when all
match with >=1 subsequence-only term (never exact/prefix; score =
summed per-term alignment); the ASCII fast path is a DRIVER-term
scan (driver = term whose rarest byte has the fewest blob
occurrences by exact histogram, any zero-count term = nil fast
reject; phase A = the anchored substring scan for the driver
fully judging candidates against the rest -- substring first,
subsequence fallback -- marking every visited entry in the pooled
bitset; phase B = the rarest-byte sweep for driver-subsequence-only
entries, skipping marks, itself skipped when the phase-A classSub
total fills the limit / fuzzy off / single-unit driver); any
non-ASCII term = the sharded per-entry slow path
(queryMultiFold). Every returned Result carries MatchRanges
(half-open RUNE ranges on Name, [][2]int json matchRanges,
computed POST-selection via match.Positions; path mode = the
best-effort final-segment name-prefix range or nothing; the naive
references model ranges via the same fill helpers while keeping
matching/ordering independent -- multiterm_test.go holds the
independent multi-term reference ladder and the "fire fox" /
"my backup" repro pins). fuzzy.go is the fuzzy
(subsequence) tier for name-mode queries: entries holding the query
as an in-order-with-gaps subsequence (same fold regimes) match with
classFuzzy (ordinal 3, shared with classPathSub -- modes never mix;
cand gained score int32, 0 outside the fuzzy class, compared DESC
inside it before the usual tie-breaks). Two-phase queryNamesFuzzy:
phase 1 = the unchanged substring scan plus per-shard live-hit
counts and a pooled per-entry bitset (shards rounded to 64 entries,
word-disjoint writes); SKIP RULE: phase-1 total >= limit means no
fuzzy hit can enter the top-limit, so phase 2 never runs for common
queries (and never for single-unit patterns, whose subsequence ==
substring); phase 2 (ASCII) sweeps the blob via ciScan for the
pattern byte with the fewest ACTUAL blob occurrences (Store.byteFreq,
a 256-entry histogram updated in appendEntry -- the static
nameByteFreq table cannot see corpus-specific rarity), maps hits to
entries like scanRange, skips tombstoned/marked entries, subsequence-
checks survivors and scores passers; non-ASCII patterns take a
per-entry rune subsequence walk. Scoring (only on subsequence
passers, off the hot path): optimal-alignment DP, fzf-v2 style --
match base + bonuses for name start/word boundary ('-','_','.',' ',
letter<->digit)/camelCase step/consecutive run, minus capped affine
gap penalties -- for names <= 512 units, greedy leftmost alignment
beyond; tests pin score ORDERINGS, never absolute values, and the
naive reference (naiveQueryFuzzy in fuzzy_test.go) reuses the score
function but keeps matching/ordering independent.
`QueryWith(q, limit, QueryOptions{FuzzyDisabled, Blend})` is the
options path (the inverse of config search.fuzzyEnabled -> main.go ->
Manager.SetFuzzyDisabled): disabled dispatches to queryNamesSub,
the pre-fuzzy scan, behavior-identical to the old engine. blend.go
is the frecency ranking blend, applied at EXACTLY one stage --
selectTop's post-scan merge over the <= workers*limit heap
survivors, never the per-entry scan: per merged candidate boost =
Signals.Boost, penalty = Signals.Penalty, cwd = Signals.CwdBoost,
and for COLD candidates only (boost 0) one budget-bounded
(RecencyBudget, default 15ms) Signals.Recency batch mapped through
recencyScore (log-scaled age -> [0,1]: ~1 within the hour, ~0.5 a
day, ~0.2 a week, 0 at 30d). Ordering: effective class (class - 1
when boost > TierJump -- one tier max) then blended = score/64 +
wF*boost + wR*recency + cwd - wN*penalty + prior DESC then the
pre-blend
chain; weights <= 0 disable each part. Blend.Prior is the
pick-memory prior seam (internal/priors wired by internal/app
priors.go): a per-query resolver QueryWith calls ONCE with the raw
query on a per-query Blend copy (unexported priorFn -- no scan path
or per-mode signature changes), whose returned func selectBlended
consults once per merged candidate as the additive prior term;
Prior alone activates the blend, nil resolver answers and
zero-returning funcs are byte-identical no-ops
(blendprior_test.go pins absent/zero no-op, within-class-only
reordering, one-resolve-per-query, and the no-resurrection
contract). Blend.Model is the learned-arbitration seam beside it
(internal/arbiter wired by internal/app arbiter.go): the same
resolve-once-per-query contract on the same per-query copy, except
the returned func also receives the candidate's ResultSignals --
filled inline from selectBlended's locals exactly as each
component participated -- and its value joins `blended` AFTER the
prior (within-effective-class only; the caller clamps magnitude,
arbiter.FileDeltaClamp); Model alone activates the blend, and
blendmodel_test.go mirrors the whole blendprior pin family
(absent/nil-resolver/zero no-ops, within-class-only, signals
delivery, prior+trace composition, no-resurrection). The Manager holds the Blend
(SetBlend/Blend; swapped as an IMMUTABLE copy -- the app's cwd
stash swaps fresh ones); nil or zero-value-Signals blends take the
EXACT pre-blend selectTop path, byte-identical ordering pinned by
TestBlendInactiveIsNoOp, and pruning stays pre-blend: a candidate
outside its shard's top-limit heap cannot be resurrected
(TestBlendMergedSetOnly, documented). candCompare's FINAL
tie-break is the numeric-aware lexicographic path order
(numorder.go, always on, every query mode incl. the shard heaps --
selection at exact ties included): aligned digit runs compare
numerically DESCENDING (datestamped/versioned families newest
first -- the strverscmp-style lockstep walk, a true total order;
equal-value runs continue, any other first difference keeps plain
byte order, all-equal walks fall back to compareJoined); the naive
test references share the rule via refPathLess (search_test.go) and
numorder_test.go holds the family/stability pins; internal/match's
Rank (plugin rows) deliberately untouched. signalstrace.go is the
OPT-IN ranking-signals trace seam consumed by the app's telemetry:
QueryOptions.Trace (*[]ResultSignals) + Manager.QueryTraced (Query
itself untouched) fill one ResultSignals per returned Result --
Path/Class/EffClass/Align/Boost/Recency/Cwd/Penalty/IsDir/PathLen,
captured in selectTop's assembly tail and selectBlended exactly as
the components participated (inactive blend = class/align only,
EffClass == Class, signals zero) -- with the buffer riding an
unexported field on a PER-QUERY Blend copy (traceBlend) so no scan
path or per-mode query function changes; nil Trace is zero-cost
byte-identical (TestTraceNilIsByteIdentical) and a non-nil Trace
never changes results or order (TestTraceDoesNotChangeResults).
Blend.Active() exports the participation probe. A query
containing a path
separator (on windows '/' too, normalized) dispatches to path mode
(path.go): matched against the FULL path via a per-query dir-table
prematch (dirs whose folded path+sep contains the query -- every
child matches; matchDirASCII keeps byte-length arithmetic, its
matchDirFold twin decides "covers all of V" by fold-equality
because rune folds shift byte lengths) plus boundary splits
q = S + R at the query's
separators (S a sep-terminated dir suffix, R a name prefix
fold-checked against the name blob); ranking exact-path >
path-suffix >
path-prefix > substring with the same tie-breaks. `Walk`: parallel walker (worker
pool + LIFO queue) with exclude patterns (`Excluder`: bare pattern
= base name, pattern with separator = full path; Match =
MatchBase || MatchFull exactly, split plus HasFullPatterns for the
walker hot path; each half splits AGAIN by
`isLiteralPattern` -- no `*?[\` metacharacter means filepath.Match
degenerates to equality -- so literals answer from a map in one
lookup and only real globs walk the matcher: the shipped defaults are
~13 base + ~6 full patterns, ALL literal, and every walked entry pays
the base check while every file entry pays the full one, so this was
tens of millions of filepath.Match calls per build (MatchBase 394 ->
10.7 ns, MatchFull 175 -> 14.5 ns; exclude checking went from +47% to
+6% of walk time, exclude_test.go pins both halves against a plain
filepath.Match loop as reference), symlinks indexed
but never descended, permission errors counted not fatal, throttled
progress callbacks. WALK ALLOCATION DIET (2026-07, recon-measured
438 B / 4.26 allocs per entry before): base-name excludes are
checked FIRST without any join; FILE entries join their full path
into a per-worker reusable scratch buffer and hand MatchFull an
unsafe.String view (nothing down that call retains or mutates it;
only taken when HasFullPatterns) so no per-file path string is ever
allocated, while DIRECTORY entries materialize the real string once
and walkItem.full carries it into appendEntry -- whose signature is
(pid, name, dirPath, isDir), interning the caller's string instead
of re-joining (AddEntry joins for itself) -- and growChildren
presizes children[pid] to the exact batch size before the append
loop, so walk-built children slices end at cap == len (the
append-ladder's measured 1.32x overshoot and copy churn are gone;
pinned by TestWalkChildrenPresized, the scratch path by
TestWalkFullPathPatternOnFiles + TestAppendJoinDir). The scratch
buffer generalized into `walkBufs` (2026-07): the per-directory
walkItem batch and subdirs slices are per-WORKER too now (walkQueue
.push copies into the queue's own slice, so reuse is safe), with
`keep` zeroing the written elements so a buffer never pins the
previous directory's name strings and handing back any buffer past
walkBufMaxEntries so one pathological directory cannot pin an
oversized batch for the rest of the walk -- 13% less allocated per
walk (BenchmarkWalkExcludes, which unlike BenchmarkWalk passes the
excludes a real install carries: 1.59M -> 2.29M entries/s with the
Excluder literal split above). WALK STRESS
GATE (walkstress_test.go, the v395 field-crash regression rig:
intermittent "growslice: len out of range" in appendName plus GC
scanstack SIGSEGVs during startup indexing -- memory-corruption
signatures): TestWalkStressIntegrity swaps readDirFn for an
in-memory synthetic tree (~113k entries/walk, fresh name strings
per call, base + full-path excludes so the per-file scratch/unsafe
path runs, a stack-pump recursion per readdir so walker stacks
grow-then-park as shrinkstack fodder) and runs 16 concurrent Walks
under the production GOGC=40 window, verifying every store's full
integrity (counts, monotonic offset table, NUL-free names, parent
paths) per iteration; ~3s budget in CI,
COMPETENT_SEARCH_STRESS_SECONDS / COMPETENT_SEARCH_STRESS_CONC
extend investigation runs. The 2026-07-20 investigation (v395
startup crash: intermittent growslice len-out-of-range / GC
scanstack SIGSEGV on one field machine) CLEARED this package: no
app-code defect (race-detector-clean; ~700M entries verified
across plain/checkptr/clobberfree/gccheckmark/novarmake builds at
up to 160 walker goroutines); compiler excluded (v395 disassembly
matches stock go1.25.0 codegen for every crash-relevant function);
and the same day's gosmopolitan cache-poisoning incident EXCLUDED
for this build (stock-keyed action IDs are disjoint from the
fork's go1.26.4cosmo collision namespace; the orchestrator
predates the first-bad; opposite crash signatures -- team memory
competent-search-thing-v395-not-fork-cache-poisoned). Root cause
remains external to this repo: leading hypotheses are
machine-local memory or an unattributed stock-runtime issue
(golang/go#77955/#73259 family). The gate pins the walker/store
concurrency+integrity invariants only -- it cannot detect wrong
bytes linked into a binary -- so keep it green rather than
re-litigating the walker's ownership story. `Manager`: owns the RWMutex contract (queries
RLock, mutations Lock); roots/excludes are LIVE-mutable now
(`SetRoots`/`SetExcludes`, the config editor's index-scope apply;
Roots/Excludes read under the lock and `BuildFromDisk` latches one
consistent copy at entry -- the live store is untouched until the
next rebuild); `BuildFromDisk` walks into a fresh store and
swaps it in, so queries keep working during rebuilds -- and first
recomputes the mount skip list (mounts.go: `SystemMountSkips` reads
/proc/self/mounts, linux-only, nil on any failure; pure
`ParseMountSkips` returns mountpoints strictly under the roots whose
fstype is kernel-virtual or network -- all fuse/fuse.* skipped,
overlay deliberately KEPT (container roots) -- octal escapes
decoded, "/" never returned, glob-metachar mountpoints dropped,
capped at 256, a mountpoint equal to a configured root never
skipped = the index-it-anyway escape hatch), appending it to the
excludes as full-path patterns and logging the list; the `mountSkips`
package var is the test seam; `RealMountpoints(roots)` / pure
`ParseMountpoints` are the inverse view -- mountpoints of WALKABLE
(non-virtual/network/FUSE) filesystems under (or equal to) the
given roots, linux-only/nil elsewhere -- consumed by the watch
sweeper's mount-diff and the fanotify notifier's extra-mount marks.
`Add`/`Remove` are the watcher-phase entry points -- and `Remove`'s
`Store.RemoveByPath` walks DOWN through `children` (each directory
entry names its own subdirectory, interned under that joined path)
via `tombstoneSubtree`, so a removal costs the subtree it actually
tombstones. It used to scan the ENTIRE interned dir table per call
with an allocating isWithin, i.e. one allocation per directory in
the whole index for every deleted directory, under the write lock
that blocks every query, once per vanished child from reconcileDir
(removing one empty directory in a 50k-dir store: 2,750,592 ns /
2.4 MB / 50,000 allocs -> 396 ns / 0 B / 0 allocs, and now flat in
store size). Exactness rests on `entryless`, the dir ids interned
WITHOUT an entry of their own (the walk roots via
`internParentDir`, plus the window where a file event is reconciled
before its parent directory's own event): those seed the walk too,
and any interned dir under the removed root either chains to it
through entries or its chain breaks at a by-definition entryless
dir. `internDir` is the flavor for a directory that has (or is
gaining) its own entry and clears the mark; `entryless` staying tiny
is what keeps the walk cheap (tombstoneSubtree scans it linearly), so
TestWalkLeavesOnlyRootsEntryless pins that a walk leaves exactly the
roots in it. `Add`'s `AddEntry` must find an existing (parent, name)
before appending, and `findChild` scans children only up to
childIndexMin (64); past that it uses `childIdx`, a name -> id lookup
for ONE directory (the last one asked about) -- enough because
scanNewDir and reconcileDir each work through a single directory at a
time, and it bounds the memory to the largest recently-looked-up
directory instead of a map per directory. Filling one directory was
O(n^2) before, under the write lock (50k entries: 3,913 ms -> 34.4
ms; the rate holds at ~1.5-2.3M entries/s instead of collapsing to
13k/s). The lookup keeps the FIRST id for a duplicated name so its
answer is identical to the scan's, and findChild MUTATES the store
(it may build the cache), so it must stay write-path only -- both
callers, AddEntry and RemoveByPath, run under the write lock.
childindex_test.go drives both sides of the threshold against the
scan plus every way the cached directory can change underneath it;
`LiveDirsPage(start, max)` pages through the live (non-tombstoned)
indexed directories releasing the read lock between pages
(DefaultLiveDirsPage = 4096), and `ChildrenOf(dir)` returns a
directory's direct children as Name/IsDir pairs -- the watch
layer's shallow-reconcile and sweep enumeration surface. `Store.Footprint()` /
`Manager.Footprint()` (footprint.go): exact byte accounting of every
column/blob (len-based; 16B string headers) plus documented
approximations for the dirIndex and children maps, and
BytesPerEntry -- diagnostics for the whole-filesystem sizing work.
A bare `Store` is NOT
thread-safe. Benchmarks build synthetic 100k/1M-entry stores in
memory (see bench_test.go) and a ~50k-entry disk tree. An env-gated
measurement harness (measure_test.go + gated benches in
bench_test.go; skip-by-default, CI-invisible) backs the efficiency
numbers in PR bodies: COMPETENT_SEARCH_MEASURE=1 walks the whole
container filesystem (BuildFromDisk-style excludes + mount skips)
and reports Footprint/heap/forced-GC evidence (test phase; timings
labeled coverage-instrumented) plus an un-instrumented walk bench;
COMPETENT_SEARCH_MEASURE_HUGE=1 builds a shared 30M-entry synth
store ENTIRELY in the benchmark phase (the test phase's 30s
per-test budget cannot fit the build) for name+path+fuzzy query
latency
(BenchmarkSearchHuge) then footprint/heap/forced-GC evidence
(BenchmarkHugeStoreMeasure, declared last -- it releases the store);
COMPETENT_SEARCH_MEASURE_OUT writes the JSON + .txt report to a
file.
