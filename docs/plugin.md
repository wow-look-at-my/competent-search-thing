# internal/plugin

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/plugin` -- the plugin system, pure and headless-testable
(wired into the app by internal/app's plugins.go). INVERTED over the
shared engine (engine.go): builtin providers are candidate SOURCES
(interface candidateSource = provider + candidates(ctx,req)
[]match.Candidate + limit() + preRanked(); payload = the wire
Result MINUS score/ranges) and sourceResults -> match.Rank ->
mintResults is the ONLY path stamping Score/MatchRanges (rogue
payload scores are overwritten; non-Result payloads dropped);
external plugins (resultProvider; production always
*externalProvider) are sanitized then engine-passed by rankExternal:
claimed queries (req.Targeted or Trigger.Claims = prefix/regex path
matched) ride TierTriggered with self-score as the hint and
response order kept on ties, all_queries results are text-gated
against Title+Keywords (misses dropped with a throttled reason) --
dispatch_test fakes implement bare resultProvider and bypass, the
routing test (engine_test.go TestEveryRegisteredSourceRoutesThrough
Engine) pins that every PRODUCTION registration is one of the two
shapes. Old per-provider score ladders/wordStart copies are GONE;
each source declares ordered match Texts instead (apps [name],
windows [title, app], sites [host-sans-www, title, url], tabs
[title, host-sans-www, url]) and the engine's canonical bands
apply. Options gains FuzzyDisabled (the inverse of config
search.fuzzyEnabled,
threaded into every Rank) and Rewrites (builtin_rewrites.go:
"rewrites" preRanked source at the triggered tier -- RE2 rules
compiled at New via compileRewrites, full-match ^(?:pat)$ unless
user-anchored, invalid = one Errors() line + skipped; on match ONE
result per rule in config order, replacement/title expanded via
ExpandString ($1/${name}/$$), open_url ONLY -- non-http(s)
expansions logged + dropped; nothing registers when no rule
compiles). schema.go:
versioned JSON wire protocol
(Request/Response/Result/Action, v=1; Result also carries Keywords
<=8x64 runes -- extra engine match texts -- and MatchRanges <=32
half-open RUNE pairs on Title, normalizeRanges clamps/sorts/merges
against the post-truncation title; Result carries the INTERNAL-ONLY
IconKey json:"iconKey" -- the "app:<ref>" icon-resolution key the
frontend hands to ResolveIcons for a real icon image, stamped by
the builtin app sources and CLEARED by the sanitizer on every
external result (image icons are a trusted-source capability;
InstalledApp mirrors the appctx Icon ref json:"icon" for the
purpose); Action carries the
INTERNAL-ONLY DesktopID json:"desktop_id" -- the .desktop entry
behind a builtin run_command launch, consumed by the app's
credentialed launch path -- and the INTERNAL-ONLY Tab json:"tab"
-- the ffext c<conn>:<tab>:<window> routing token behind a builtin
activate_tab switch, Value doubling as the fallback URL) and
`SanitizeResponse`, which
clamps/validates everything an external plugin returns: 20-result
cap, rune caps (title 200/subtitle 300/badge 24/field 40+200, max 8
fields), control chars -> spaces everywhere, icon = builtin name or
<=32-byte glyph, accent_color regex, score default 50 clamp 0..100,
action validation (open_path abs path, open_url http(s)+host,
copy_text <=8 KiB, run_command 1..16 argv <=1024 B each and the
whole RESULT is dropped unless the manifest sets allow_run_command;
internal-only set_query/run_builtin/activate_window/activate_tab
always stripped
and a stray Action.Window, Action.Tab OR Action.DesktopID on
external types
cleared; anything
removed gets a human-readable reason for logging). trigger.go:
`Trigger` Compile/Match/Boost -- prefix (case-insensitive,
rune-folded) / regex (ci RE2 on the RAW query) / all_queries paths
(first match wins the stripped value), min_query_len in runes of
the STRIPPED query gating all paths (defaults 2 when all_queries),
optional focused-app gate (name/exe ci RE2, both-empty rejected at
Compile, fail-closed) + focused_boost clamped 0..100. manifest.go:
`LoadDir(<configDir>/plugins)` -- one error per broken manifest
(path-prefixed), missing dir = no plugins no error, duplicate id ->
first alphabetical dir wins, defaults (v=1, name=id, timeout_ms
1500 clamp 100..10000, bangs=[id]), bangs lowercased+deduped,
context subset of {focused,running,installed}, empty bangs + nil
trigger rejected as unreachable, trigger compiled on load.
bangs.go: `BangSet` -- config-driven sigils (must be one non-letter/
digit/space rune; invalid ones recorded via Errors(), all-invalid ->
defaults ! / @), Register (dup = error, first wins), Parse (sigil +
[a-zA-Z0-9_-]* name lowercased + end-or-space + raw rest), Resolve
(exact > alias > unique prefix, canonical bang returned), sorted
Candidates(partial), Primary() = first configured sigil.
command.go/http.go: the transports behind the tiny `transport` seam
-- command = one shell-free subprocess per query (request JSON to
stdin then closed, cwd = Manifest.Dir, argv[0] with a separator
resolved against it, stdout capped 1 MiB, stderr capped 8 KiB and
quoted in errors, ctx timeout hard-kills with 250ms WaitDelay);
http = POST to the manifest url (ONE shared keep-alive client per
Registry, max 3 http(s)-only redirect hops, 2xx required, body
capped 1 MiB). Both error on invalid JSON and v != 1. registry.go:
`New(Options) *Registry` wraps manifests in providers (settings
default "{}", request context filtered to the manifest-declared
parts, `SanitizeResponse` applied HERE so trusted builtins bypass
it), registers builtins FIRST (a manifest can never shadow a
builtin bang or id; dup bang/id = recorded error, first wins),
honors the global kill switch + per-id disable entries, and
collects every setup problem for `Errors()`. `Dispatch(ctx, query,
gen, appCtx, emit)` returns `TargetInfo` synchronously and fans out
one goroutine per matching provider: ctx-abortable debounce
(clamped 0..2s here -- DebounceMS arrives unclamped), per-plugin
timeout ctx (manifest timeout_ms; builtins 1.5s), panic recovery,
per-provider 5s-throttled logging (throttle.go), focused boost
added and clamped at 100, emit only with results and only while ctx
is live -- emit runs on provider goroutines and MUST be
goroutine-safe. SOURCE PRIORITY (placement metadata, NEVER a
score): `prioritized` (engine.go, the watch backendInfo
optional-extension pattern) is `priority(best match.Tier) int` --
decided PER EMISSION from the strongest tier the engine minted
(sourceResults returns it beside the rows; TierNone for external/
empty answers); Emission gains Priority (json priority,omitempty)
stamped in dispatchOne and CheatSheet via providerPriority (type
assertion, absent = 0 whatever the tier). THREE sources implement
it -- apps-search (sourcePriorityApps) and the two Firefox web
sources firefox-frequent + firefox-tabs (the shared
sourcePriorityWeb; the "tampermonkey" field report, where an
exact-title open-tab row rendered below every file result, weak
fuzzy matches included) -- each = 1 when best <= strongTier =
TierWordStart -- a STRONG match (triggered/exact/prefix/word-start)
earns priority 1, a weak best (substring/fuzzy) emits at 0
(originally the below-files placement -- the macOS "test" field
report, where scattered-subsequence app hits outranked a directory
literally named "test"; since the 2026-07-21 files-last frontend
default every section renders above the file results and the
priority ORDERS sections, strong ones first) -- and a PROMOTED
emission is cut
to its strong rows inside sourceResults (generic for every
prioritized source: weak rows must never ride
the promoted zone; they render in the whole-section-at-priority-0
shape whenever no strong match exists, all rows kept); the
targeted apps provider stays 0
(bang queries have no files to outrank), and external plugins can
NEVER set it -- the wire Response has no priority field and
*externalProvider does not implement the extension (pinned by
TestSourcePriorityMetadata + TestExternalEmissionPriorityAlwaysZero
+ TestPriorityNeverChangesMintedScores: the mint is byte-identical,
bands untouched; TestAppsSearchWeakMatchesStayBelowFiles +
TestAppsSearchPromotedSectionStrongRowsOnly pin the tier gate, and
builtin_web_priority_test.go mirrors the whole family for the two
web sources incl. TestWebPriorityNeverChangesMintedScores).
APP USAGE TIE-BREAK: Options.AppUsage (the app layer's frecency
store behind a live accessor; nil = cold) feeds appCandidates'
Candidate.TieBreak (decayed launch count x1000, usageTieBreak), so
equal-tier equal-score app rows order by real usage before the
name -- within a match class only, the tier stays the primary sort
key (in the fuzzy band the alignment score still ranks first;
usage breaks exact score ties). Keys: AppUsageKey(desktopID, argv)
= "app:"+desktopID when a *.desktop id is stamped, else "app:"+
argv joined with spaces (the darwin `open -a <bundle>` shape) --
derivable identically from the snapshot (lookup) and the echoed
action (record); AppPickKey(pluginID, action) gates recording to
run_command launches from the two builtin app sources.
Routing: resolved bang (exact/alias/unique-prefix)
+ space => ONLY that provider, all trigger gating bypassed;
bare/partial/ambiguous or resolved-without-space sigil => ONLY the
builtin suggestions provider; bang-shaped text with zero candidates
=> normal trigger fan-out on the raw query. `CheatSheet()` returns
the suggestions provider's answer for a bare primary sigil as ONE
synchronous Emission (Gen 0, no goroutines/fan-out; suggestions
provider disabled = zero Emission) -- the app binds it for the
frontend's empty-query cheat sheet. `Close()` drops idle
HTTP connections; reload = build a new Registry, swap atomically,
Close the old. Builtins (in-process, no sanitizer; targeted-only
except apps-search, windows and the two Firefox providers):
builtin_bangs.go "bangs"/Commands -- bang completions (resolved
bang first, primary-sigil titles, typed-sigil set_query preserving
the query rest, cap 12); builtin_app.go "app"/App Commands --
!rescan/!reload/!config/!version/!quit, one run_builtin result each
(version subtitle from Options.Version); builtin_apps.go
"apps"/Launch -- !app/!launch over the Options.InstalledApps
snapshot (empty query = all 15 listed, usage first then
alphabetical, cap 15, run_command argv via `parseDesktopExec`:
quotes, backslash escapes, %-field codes stripped; the shared
candidate builder is `appCandidates`, whose
actions carry DesktopID = the InstalledApp.ID ONLY when it is a
bare *.desktop name (launch.ValidDesktopID -- the darwin scan's
".app" bundle ids used to fail the app layer's run_command
re-validation and error every macOS launch) so linux launches keep
activation credentials, whose TieBreak carries the AppUsage decayed
launch count (see the registry entry above), and whose Results carry the
internal-only IconKey "app:<Icon ref>" when the installed app has
one -- the frontend's real-icon hook;
AppUsageKey/AppPickKey/usageTieBreak live here too);
builtin_apps_search.go "apps-search"/Apps -- installed apps in
NORMAL results: no bangs, a real all_queries Trigger (match
override on builtinBase, effective min 2 runes), the shared
engine's canonical bands over the app name (words = letter/digit
runs, so spaces, hyphens, dots split), cap 6, same run_command
launch, and a prioritized source (priority(best) = 1 only at
word-start tier or better -> its Emission carries only its strong
rows and orders ahead of priority-0 sections; weak bests emit at 0
with all rows -- the tier-gated promotion the two Firefox web
sources share); bang routing keeps it
exclusive with the targeted !app
path, and a nil/empty snapshot emits nothing;
builtin_openwindows.go "windows"/Open Windows -- also in the normal
fan-out (no bangs; own all-queries match, min 2 runes of
the trimmed query) over the Options.OpenWindows snapshot
(plugin-local WindowInfo, ID as STRING to survive JSON), registered
ONLY when that seam is non-nil (the app layer's session gate);
ranking title word-start 85 > app prefix 80 > title substring 65 >
app substring 60, ties alphabetical, cap 8, rows carry the
internal-only activate_window action (icon "app", subtitle = app
name);
builtin_firefox.go "firefox-frequent"/Frequent Sites -- NO bangs,
all-queries semantics (>= 2 trimmed runes, the shared
allQueriesMatch helper), registered ONLY when Options.FrequentSites
(the app-layer getter yielding []SiteInfo, a plugin-local mirror of
internal/firefox.Site) is non-nil; scores
host prefix 95 (leading "www." ignored) > title word-start 80 >
host substring 70 > title-or-URL substring 60, ties by visit count
then title, cap Options.FrequentSitesMax (<=0 -> 6); result =
title-or-host / URL subtitle / icon "globe" / the internal-only
IconKey "favicon:<pageURL>" (faviconIconKey -- the icons service
resolves it to the site's real favicon, the glyph standing until
then; the sanitizer strips IconKey from external results, favicon
kind pinned) / open_url action; a prioritized source
(sourcePriorityWeb: a word-start-or-better best promotes the
section to priority 1 cut to its strong rows, weak bests emit at 0
with all rows -- the apps-search tier gate);
builtin_tabs.go "firefox-tabs"/Open Tabs -- same NO-bangs
all-queries semantics, registered ONLY when Options.OpenTabs (the
getter yielding []TabInfo, mirror of internal/firefox.Tab) is
non-nil; scores title word-start 85 > host prefix 80 ("www."
ignored) > title substring 65 > URL substring 55 (the TITLE outranks
the host here, unlike frequent-sites), ties by lastAccessed DESC
then title, cap Options.OpenTabsMax (<=0 -> 6); result =
title-or-host / URL subtitle / icon "link" (globe is taken) / the
same internal-only IconKey "favicon:<pageURL>" /
"pinned" badge on pinned tabs / the action: a TabInfo.Token-carrying
row (the app's ffext live snapshot supplied it) gets the
internal-only activate_tab {Tab: token, Value: URL} SWITCH, a
token-less (sessionstore) row keeps the byte-identical open_url --
which re-OPENS the page (the README tab-switching section owns the
user-facing story); a prioritized source exactly like
firefox-frequent (sourcePriorityWeb, the same tier gate -- the
"tampermonkey" fix: a strong open-tab title match renders above
the file results);
builtin_runterm.go "run-terminal"/Run -- run a $PATH program in a
terminal (the field ask: typing "htop" found files named htop and
no way to run it). No bangs, all-queries Trigger with MinQueryLen
1, registered ONLY when Options.Terminal (a *TerminalRunner
carrying Name/LookPath/Command; internal/terminal resolves it,
internal/app runterm.go wires it) is usable -- the OpenWindows
seam convention, so a machine with no terminal emulator never sees
the section. EXACT PATH MATCH ONLY: splitCommand (quote-aware,
unterminated quote = no row) takes the first word, a word carrying
a separator is refused (a path is a file result, and LookPath
would resolve a relative one against the APP's cwd), LookPath must
succeed, and the row runs the RESOLVED exe. Texts = the whole
command line plus each word, so the engine mints the exact tier
for "htop" and for "htop -d 5" alike; priority(best <= TierExact)
= 1 puts it in the promoted zone, and "apps-search" < "run-
terminal" breaks the exact-tier tie so a GUI app still wins its
own name. Action = run_command over Terminal.Command's argv,
capped at maxArgvEntries so the app layer's re-validation can
never reject what was offered.
Exhaustively
unit-tested, table-driven, plus an end-to-end manifest ->
registry -> /bin/sh transport dispatch test.
