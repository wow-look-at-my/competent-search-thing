# frontend/

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`frontend/` -- vanilla TypeScript + Vite. No framework. Tiny vitest
+ jsdom suite (`npm test` = `vitest run`; vitest.config.ts +
src/test-setup.ts, which loads the REAL index.html body into jsdom
before render.ts's module-load template grabs, so the DOM-order
tests fail if the zones/templates change shape; src/priority.test.ts
pins the files-last rendering (all sections above the file rows,
the sectionAboveFiles predicate, the empty below zone), the flat
traversal order, and
the reconcileSelection rules, src/stats.test.ts pins the stats
formatters + renderStats' dash-vs-value rules + the stats-row WIDTH
CONTRACT (formatter-maxima sweeps and the style.css ch-reservation
structure -- jsdom computes no layout, so those two sides ARE the
mechanical gate), src/hover.test.ts drives the REAL main.ts
over faked Wails bindings to pin the hover-vs-selection model
(hover changes nothing, Enter runs the keyboard-selected row, click
runs the clicked row), and src/ffext-logic.test.ts drives
../../webextension/logic.mjs (typed via its sibling logic.d.mts)
with scripted browser/timer fakes -- listTabs/activate shapes, the
tab-then-window call order, stale-tab rejection, reconnect backoff,
push debounce -- all run in the CI linux job's frontend step).
`index.html`
(query row with inline SVG magnifier + hidden bang chip; #results
split into #priority-results (ALL plugin sections, ABOVE the files
-- file results default to LAST, the 2026-07-21 ruling) /
#file-results / static #empty ("No matches") / #plugin-results
(renders EMPTY by default; retained as the home of the
weak-sections-below veto variant) zones; status bar + degraded
chip + backend chip;
the #stats row
BELOW the status bar -- the bottom-most chrome, five STATIC
label/value span pairs (CPU GPU RAM SWP NET, value ids
stat-cpu/-gpu/-ram/-swap/-net), starts hidden, JS only ever writes
INSIDE the value spans (plain text, except the NET value's two
tinted arrow spans stats.ts builds -- see the stats.ts entry below);
#preview-pane
(spinner + #preview-body + command strip with the web/AI buttons
and the pane flash) as one more #bar child, display:none unless
body.with-preview; #config-pane (header with #config-title +
#config-filter, #config-notices, then #config-main = the ToC
sidebar nav #config-toc beside the scrollable #config-body --
sidebar before body, so Tab runs filter -> ToC -> controls --
and #config-strip with
flash + dirty note + Open config.json / Close / Save buttons) as
the last #bar child, display:none unless body.with-config -- all
editor rows/controls/ToC entries are built dynamically by
config.ts, so no
templates; <template>s for
folder/file icons AND plugin section/row skeletons) + `src/main.ts`
(search as-you-type: 15ms debounce + sequence-number stale-response
drop; every generation also fire-and-forgets QueryPlugins(query,
seq) -- INCLUDING the empty query, which is the Go-side cancel
signal -- and updates the bang chip from the returned TargetInfo;
an EMPTY query additionally fetches CheatSheet() and renders it as
the single plugin section (dropped if the generation moved on or
anything was typed), so the bar lists the available commands before
you type -- with NO auto-selected row: both auto-select-first
fallbacks are gated on a non-blank query so Enter on an empty bar
stays a no-op, and moveSelection enters the unselected list
explicitly (Down -> first row, Up -> last); wire() kicks the
pipeline once at startup so the sheet is already rendered before
the first summon (an app:shown emitted while EventsOn registration
is still in flight is missed -- observed on cold WebKit starts) and
fetches GetHistory into histEntries (refetched after each
successful AddHistory). QUERY HISTORY modality (histCursor: -1 =
not browsing, 0 = newest): Up recalls older entries when the input
is blank OR histCursor >= 0 (the input is still exactly a recall's
text -- every 'input' event and setQueryLocal reset histCursor to
-1, so typing or picking a completion exits browse mode and the
arrows navigate the result list again); Down while browsing moves
forward, and forward past the newest entry clears the bar back to
the empty state (cheat sheet); recall = replace the input, caret
to end, re-run the pipeline (the recalled query renders its
results live, Enter activates as usual; programmatic value writes
fire no 'input' event, so the cursor survives). History COMMITS
(AddHistory(state.query), fire-and-forget, then refetch) only when
an activation actually executed: a file row's Open/Reveal resolved
without error, or RunPluginAction resolved without error --
set_query and blank queries never commit; the SAME two success
sites also fire reportPick (RecordPick, fire-and-forget, errors to
console.warn only -- the ranking log must never break an
activation) with
a report SNAPSHOTTED at activation time via pickReport (the flat
state.items as {kind, path | plugin+score+title} identity rows,
the picked rank, the action kind + revealed flag), appended to the
always-on local ranking log Go-side;
"plugin:results" emissions are dropped unless gen === seq, else
upsert that plugin's section (keyed by id; priority = e.priority ??
0) and renderPluginArea re-renders BOTH plugin zones
(render.ts splitByPriority over the ONE sectionAboveFiles
predicate: EVERY section -> #priority-results above the file rows
-- file results default to LAST -- and #plugin-results renders
empty, retained as the veto variant's home (the variant = the
predicate returning s.priority > 0 again + the priority.test.ts
zone pins); compareSections is UNCHANGED = priority desc, max
score desc, plugin id, so strong priority-1 sections still order
ahead of weak ones);
selection is one flat list in DOM order -- plugin section rows,
then file rows, then the empty-by-default below zone: ArrowUp/Down
wrap, Home/End, and with any section present the auto-selected row
0 is the first plugin row.
TWO DISTINCT POINTER STATES (the hover-steals-selection field
report): the ACTIVE selection (state.selected) moves ONLY through
keyboard navigation and the auto-select/reconcile paths and is the
single source of truth for Enter, the pick report, AND the preview
pane, while mouse HOVER is a purely decorative CSS :hover wash
(style.css .result:not(.selected):hover -- render.ts registers NO
hover listener at all, RowHandlers has no onHover), so sweeping the
cursor can never change what Enter runs, mark the generation
navigated, or retarget the preview; a CLICK is the explicit mouse
choice and activates the clicked row (src/hover.test.ts pins all
three). Row handlers resolve their index at EVENT time
(rows.indexOf(row) -- render-time captured indices went stale when
a late priority emission PREPENDED rows above the files), and every
re-render reconciles the selection through selection.ts
reconcileSelection: userNavigated (set by arrows/Home/End,
cleared per generation in runSearch) preserves the selected item BY
IDENTITY at its shifted index, while an un-navigated bar re-runs
auto-select on row 0 so a late apps section takes the selection
Spotlight-style (never at a blank query -- the cheat sheet stays
unselected, an ordinary priority-0 section rendered above the
EMPTY file list, pixel-identical to its old below-zone painting);
selection scrollIntoView fires ONLY for keyboard/auto-
select navigation (applySelection/select carry a scroll flag;
the plugin-area re-render selects without scrolling, so
it never moves the viewport), and wheel input on #results is
handled manually OFF-MAC ONLY (wheel.ts shouldInterceptWheel,
vitest-pinned: navigator.platform "Mac*" = NO listener at all --
a non-passive always-preventDefault wheel listener forces WebKit's
synchronous main-thread scroll path there, pinning scroll motion
to the Low-Power-Mode-halvable rendering-update clock, while
native async overflow scrolling runs compositor-side at display
rate with momentum; linux/windows register the listener exactly
as before): a non-passive listener preventDefault()s and
applies deltaMode-normalized deltas (40px/line, clientHeight/page;
WebKitGTK sends pixels) straight to scrollTop -- WebKitGTK's
default-on smooth-scroll animator otherwise eats fast detents and
Wails exposes no setting for it; ctrl+wheel stays native on both
paths; file
rows Enter=Open / Ctrl/Cmd+Enter=Reveal; plugin rows run
their action on Enter/click (Ctrl+Enter identical): set_query stays
frontend-local (replace input, caret to end, re-run the pipeline),
everything else goes to RunPluginAction -- Go owns bar-hide per
action type; copy_text and run_builtin "version" stay open and flash
"Copied" ~1.2s in the status bar, run_builtin "config" stays open
WITHOUT the flash (Go summons the editor instead of hiding, so the
visible flag must survive), action errors -- plugin actions
AND file-row open/reveal failures -- flash ~2s; #empty
shows only when a non-blank query has neither files nor sections;
Tab/Shift+Tab are preventDefaulted no-ops reserved for future use
(the default focus traversal would leave the input -- the bar's
only focusable element -- and the webview, tripping the blur-hide);
Esc + window blur -> Hide -- BOTH gated on !configModeActive()
(config.ts): in config mode the document keydown handler
early-returns entirely (arrows/Enter/Tab must behave like form
keys; config.ts's own window handler owns Esc + Ctrl/Cmd+S) and the
blur auto-hide is suppressed (users alt-tab away mid-edit) --
those two plus the app:shown focus skip below are the ONLY three
main.ts config gates; runtime events: "app:shown" -> CLEAR
the input (the bar always summons empty; the pre-hide text is
deliberately dropped) + reset histCursor + focus + refresh (renders
the cheat sheet; plugins re-query through the same path) + a
refreshStats re-render (GetStats is the instant cached snapshot;
the summon's fresh samples follow as events) -- the reset runs
even when config.ts is RESTORING the editor (hide-while-editing;
see its app:shown handler) so the search layer underneath stays
fresh for the eventual Esc-out, but the inputEl.focus() steal is
skipped while configModeActive() (the restored editor re-asserts
its own focused control),
"index:progress" -> status text, "watch:degraded" -> warning chip,
"watch:backend" -> the PERSISTENT #backend-chip when full=false
("Partial file watching" for inotify / "File watching off" for
none, the Go hint on hover via title; full=true keeps it hidden;
independent of the degraded chip -- both can show, one shared
--sb-warning chip rule in style.css),
"stats:update" -> applyStats. STATS ROW wiring (all in main.ts):
applyStats hides the whole #stats row when snapshot.enabled is
false (stats.enabled=false) and otherwise unhides + delegates to
stats.ts renderStats; wire() calls refreshStats once at startup
(same missed-app:shown reasoning as the cheat-sheet prefetch --
pre-first-summon the enabled snapshot renders all dashes) and
events keep it live while visible)
+ `src/stats.ts` (the stats row formatters + renderStats(snap,
nodes): formatPct "12%" (rounded); formatBytesPair "6.2/15.9G" --
BOTH values in the unit the TOTAL picks, GiB else MiB below 1 GiB,
shared decimal rule one-decimal-below-10-else-none; formatRate
humanizes bytes/sec B/K/M/G (binary) with the same rule, net
renders as "<down>rx <up>tx" arrow pairs whose arrows renderNet
builds as .net-down/.net-up SPANS (element+text-node building, the
render.ts convention -- style.css tints the two directions; the
rates stay plain text); any *Ok=false -> em-dash
placeholder, while swapOk=true with swapTotal 0 (no swap configured
/ empty dynamic macOS swap) renders the live "0M" -- a real zero is
a value, only a dead source dashes (the macOS SWP field report);
the WIDTH CONTRACT constants (PCT_MAX_CHARS 4 "100%",
BYTES_PAIR_MAX_CHARS 10 "1024/1024M", RATE_MAX_CHARS 5,
NET_MAX_CHARS 13 = 2 rates + arrows + space) are the formatters'
maximum emit widths over documented input domains (pcts 0..100,
byte pairs used <= total <= 9999 GiB, rates < 9999 GiB/s);
style.css reserves each value slot at MAX + 1ch and stats.test.ts
pins BOTH sides -- bump a constant and the CSS reservation
together;
glyphs (em dash, arrows) are \uXXXX escapes -- ASCII-only source;
src/stats.test.ts pins the formatters + the swap dash-vs-0M rules)
+ `src/fpsmeter.ts` (the dev-only fps meter; wire() ends with
initFPSMeter(app), which asks the bound FPSEnabled ONCE and
registers NOTHING when it answers false -- zero cost off. On: a
rAF loop collects frame deltas (deltas > 250ms = gaps -- hidden
window, summon resume, debugger -- excluded; visibilitychange to
hidden resets the baseline so no hidden-state work happens),
summarizes every ~5s of ACCUMULATED visible time (first report at
~2.5s for CI) through the PURE exported summarize (avg/max fps,
>20ms long-frame pct, inferredHz = inverted 10th-percentile delta
snapped within 10% to the common panel rates -- JS cannot read the
refresh rate; the Go context line supplies the hardware truth),
and fire-and-forgets RecordFPSSample (console.warn on error, the
reportPick pattern); src/fpsmeter.test.ts pins summarize incl. the
30fps-throttle and gap-discard shapes) + `src/wheel.ts`
(shouldInterceptWheel(platform), the pure mac gate for main.ts's
manual wheel interception -- see the wheel passage above)
+ `src/render.ts` (pure text-node DOM builders, no innerHTML
anywhere: appendHighlighted renders the Go-minted matchRanges
(half-open RUNE pairs; the walk counts code points because JS
strings are UTF-16) as .hl spans -- LETTER COLOR ONLY via
--sb-highlight, on file-row names AND plugin titles, no frontend
re-matching (the old indexOf highlight is gone; renderResults no
longer takes the query) -- file rows with a per-file-type glyph
icon (buildFileGlyph: span.icon.file-glyph.<font> with
textContent = the fileicons.ts-resolved glyph, coloured EXCLUSIVELY
via the per-row --fi-dark/--fi-light custom properties -- the
--plugin-accent precedent; SYNCHRONOUS, no ResolveIcons for file
rows -- the index.html tpl-icon-* templates stay for the preview
pane's dir listing) + the highlighted match + dim parent dir (a
non-empty result hint replaces the parent-dir text -- the
outside-indexed-roots note); plugin
sections -- unselectable header, rows with icon/title/dim
subtitle/badge/"label: value" fields; the builtin icon-name -> glyph
map (calculator globe clock star info warning link terminal text
hash bolt app puzzle; unknown/absent -> puzzle, non-name values
render as literal glyphs); REAL app icons: a row whose result
carries the internal-only iconKey renders its glyph, requestIcon
batches the pending keys per render tick (queueMicrotask) through
the bound ResolveIcons(keys, 64), and setIconImage swaps the glyph
span's content for an <img class="plugin-icon-img"> whose src is
the Go-minted data URI -- a DOM-node build with a property-assigned
src, NOT a second innerHTML sink; answers (misses included) land in
a module-level cache (cleared past 512 keys), stale rows are
skipped via isConnected, and a missing binding/miss leaves the
glyph standing; accent_color is ONLY ever applied by
setting the `--plugin-accent` custom property on the row -- never
inline color styles) + `src/fileicons/` (the per-file-type icon
layer's frontend half -- the mapping artifact is Go-side now:
internal/fileicons/data.bin, a compact binpazer container
(wow-look-at-my/bin-file-fmt) holding the generated rule set from
the vendored file-icons/atom pack (2,363 file + 51 dir rules --
incl. the Devicons-face rules over the vendored
file-icons/DevOpicons font and the Atom-builtin octicon classes
resolved from the octicons checkout's codepoints.json; pinned
pack commit, per-font licenses and the devopicons provenance
receipts in LICENSES.md; regeneration via tools/convert.mjs -> the
tools/emitbin.mjs payload encoder -> the first-party `binpazer`
CLI pack+validate -- see its README.md), decoded by
internal/fileicons and fetched ONCE at wire-up over the
GetFileIcons bound method (initFileIcons, the initTheme
fire-and-forget pattern; installFileIcons compiles the wire table,
refuses malformed defaults whole, and clears the memo cache so
pre-install resolutions self-correct; until the answer lands the
matcher serves the pack's octicon defaults). fonts/ = the five
committed woff2 icon fonts (file-icons ISC, FontAwesome 4.7 OFL
1.1, MFixx MIT, Octicons v4.4.0 MIT, DevOpicons -- see LICENSES.md
for its provenance receipts), fileicons.ts = the matcher
(`fileIcon(name, isDir)`: pack-order first-match -- rules
pre-sorted priority-desc so special filenames/compound suffixes
beat generic extensions; string rule = case-insensitive basename
suffix, regex rule = raw basename with authored flags ("g"
stripped -- .test statefulness); memoized, SYNCHRONOUS, never
throws, no match = the pack's octicons file-text/file-directory
defaults, uncolored -> fg-dim) + the pure `isLightBackground`
(the pack's own HSL-lightness >= 0.5 motif rule over
hex/rgb()/hsl(); unparseable = dark), fileicons.css = the five
@font-face decls (vite emits the woff2 assets; wails serves them
from the embedded dist) + .file-glyph slot styling (16px column,
per-font pack sizings) consuming --fi-dark/--fi-light with the
html.icons-light class selecting the light variants;
src/fileicons.test.ts is the frontend gate -- matcher semantics
over a fixture table, install/fallback wiring (null rule arrays,
rejecting bound method, malformed defaults refused), motif forms,
font byte budgets -- while the committed data.bin itself is gated
Go-side in internal/fileicons) + `src/theme.ts` (initTheme called
first in
wire(): fetches GetTheme and sets each token as `--sb-<k>` on
<html>, injects GetCustomCSS as the text of the single managed
`<style id="sb-custom-css">`, refetches on "theme:changed";
applyTokens ALSO toggles the html.icons-light motif class from the
applied bg token via fileicons.ts isLightBackground, so every
theme apply/change re-selects the file-icon colour variants with
zero re-renders) +
`src/style.css` (Spotlight-ish bar, dark by default; dir ellipsizes
before the name; thin scrollbar; ALL colors/sizes/effects flow
through var(--sb-*) -- the :root block holds the dark fallbacks and
MUST stay identical to internal/theme/builtin/dark.json, enforced
by internal/theme/sync_test.go; the decorative hover wash
.result:not(.selected):hover (a 40% color-mix of --sb-selection-bg,
clearly weaker than the full .selected style -- the :not() guard
keeps the selected row's look authoritative under the pointer); the
#stats row block (a single nowrap flex-0-0-auto line so it can
never squeeze the results area: a five-track grid
(repeat(5, auto) + space-between) whose value spans carry RESERVED
min-widths -- 5ch pct / 11ch byte-pair / 14ch net = the stats.ts
*_MAX_CHARS + 1ch slack, stats.test.ts-pinned -- plus tabular-nums,
so a value changing rendered width never shifts its neighbors;
~0.85x small font, fg-dim on a --sb-border top border; the subtle
metric hues are color-mix derivations of EXISTING tokens --
labels = accent 45% into fg-dim under the 0.7 opacity, .net-down =
accent 60%, .net-up = warning 60% -- deliberately NO new theme
token, so the sync_test.go :root contract is untouched and both
builtin themes stay legible -- and an explicit
#stats[hidden]{display:none} because the author-level display:grid
would defeat the UA sheet's [hidden] rule); appended namespaced
plugin block
(.plugin-*, .bang-chip, .status-flash) where every accent rule
consumes var(--plugin-accent, var(--accent, #89b4fa)) and a :root
bridge defines --accent: var(--sb-accent, #89b4fa), so the theming
design tokens apply when present and the standalone default
otherwise, merge order irrelevant; plus the appended .preview-* /
body.with-preview block: with-preview turns #bar into a grid --
the left column (query row/results/status/stats row exactly as
before; four explicit rows, the pane spans 1 / 5 and a hidden
#stats collapses its row to zero) keeps the FLAG-OFF bar width via
minmax(0, min(var(--preview-results-col, 680px), 100%)) -- the
min() cap makes the column give way instead of overflowing when
the clamp-to-screen rule or a drag shrinks the window below the
configured column width, the pane taking whatever remains -- the
custom property preview.ts
sets on <body> from GetPreviewConfig.resultsWidth (= config
window.width; the 680px fallback is the pre-knob constant), pane
in the rest behind
a border-left divider, minmax(0,..)
tracks so pane content scrolls instead of growing the window --
and without the class every preview rule is inert, so flag-off
layout is behavior-identical to the classic bar; CI screenshots run
preview-off and must stay that way, the 780x550 default-geometry
window regex in
screenshots.ts depends on it -- since the pane turned default-ON
(config v8) the script's temp config opts out EXPLICITLY with a
rootsVersion >= 8 stamp, because an unstamped false would be
migrated back to on) + `src/preview.ts` (ALL pane logic;
initPreview is called once from wire() with the GetPreviewConfig
answer and wires elements + listeners UNCONDITIONALLY -- each
handler gates on the live `enabled` flag -- then hands the answer
to the exported `applyPreviewConfig(cfg)`, which config.ts
re-invokes with a fresh GetPreviewConfig after every GUI save and
on every "config:changed" (the backend applies preview config
live, so the pane mounts/unmounts/resizes without a relaunch):
enabled toggles body.with-preview + renderIdle on mount, updates
--preview-results-col, and setTrigger reflects each provider's key
state on its strip button in BOTH directions; while disabled every
hook/handler no-ops. The subscription
"preview:result" (drop unless enabled AND payload.gen === its own
previewGen
counter, cancel the 150ms-delayed spinner on the first accepted
payload, REPLACE the pane content per emission -- a fast meta card
precedes the rich payload, cache hits skip it) and its
OWN window keydown handler for Ctrl/Cmd+K (web) / Ctrl/Cmd+I (AI)
-- main.ts's document handler is untouched, Tab and Ctrl+Enter
stay reserved. previewOnSelectionChange (called from select(), the
single selection choke point) paints an instant zero-IO header and
debounces QueryPreview 90ms so held arrows stay free, dedupes
same-row re-selects by target key, and maps rows to targets: file
-> {kind:"file", path, isDir}, plugin -> {kind:"plugin", title,
subtitle, pluginName}, null -> idle card + a debounced
{kind:"none"} cancel; previewOnQueryChange feeds the strip labels
('Search web for "<q>"') and idles the pane on a cleared query.
The strip buttons + hotkeys are the ONLY FetchWebPreview /
FetchAIPreview call sites (never automatic; unconfigured providers
render disabled with a hint naming their config knobs -- the AI
button follows GetPreviewConfig's aiConfigured, which is
preview.ai baseUrl AND model). Renderers are
text-node-only: meta dl, text (header + highlighted <pre><code> +
truncation footer), image (<img src=dataUri> + WxH/size caption),
dir (rows cloning the folder/file icon templates + "N more..."),
web (rows whose click runs RunPluginAction("preview", open_url) --
Go validates, opens, hides the bar), ai (answer + model/cached
badges + a Copy button through copy_text, <= 8 KiB Go-side, with a
"Copied"/error flash in the pane strip), error card) +
`src/config.ts` + `src/config.css` + `src/toc.ts` (the CONFIG
EDITOR, the whole UI of the settings-window process --
main.ts's wire() asks GetStartupMode first and, on "config", runs
wireConfigWindow (initConfig + openConfigWindow) instead of the
searchbar wiring; openConfigWindow: fetch + cache GetConfigSchema (embedded, immutable)
and JSON.parse GetConfigForEdit's configJson into a WORKING COPY,
set body.with-config (config.css hides every normal bar region via
two-id selectors that out-rank the with-preview grid rules
regardless of bundle order; #config-pane fills the bar as flex
child OR spanning grid item), render the whole settings UI from
the schema's top-level properties IN SCHEMA ORDER and focus the
filter. LAYOUT (VS Code settings pattern): #config-main = the ToC
sidebar #config-toc (~176px, own scroll) beside the controls
column #config-body; the ToC is generated by the SAME walk that
renders the controls (makeSection is the one registration point,
so sidebar and column can never disagree): one entry per top-level
section in schema order + indented sub-entries for nested object
sections (search.frecency/priors/telemetry/arbiter,
firefox.frequentSites/openTabs, preview.kagi/ai), while
top-level LEAF settings group under a synthetic "General" section
(leading run; a leaf after the first real section -- rewrites --
gets its own group named after itself, schema order never
reshuffled). Entries are buttons (ids config-toc-<dotted>; Tab
order filter -> ToC -> controls, Enter/Space jumps): click =
INSTANT scroll of #config-body to the section (never smooth --
WebKitGTK's animator, the main.ts wheel-handler enemy), and the
scroll listener highlights the entry whose section sits at the
viewport top via toc.ts activeSectionIndex -- a PURE function over
(offsets, scrollTop, viewport, contentHeight) with a
bottom-of-scroll rule so a short trailing section can win;
vitest-pinned (toc.test.ts), rAF-coalesced, measured through
getBoundingClientRect. The renderer is a generic schema walk
(resolve() follows
"#/$defs/" refs; classify() picks the control): object-with-
properties = nested section (dotted-path header + title +
description note), boolean = checkbox, string enum = select,
integer/number = number input carrying schema min/max as UX ONLY
(Go owns validation; unparseable input marks the row invalid and
blocks save by name), string = text input -- except descriptions
starting "SECRET:" = password input + show/hide toggle, never
echoed elsewhere -- and description URLs render as real anchors
(linkSegments splits the text, descNode builds text nodes +
<a> elements -- no innerHTML) whose clicks preventDefault and route
through the OpenExternalURL bound method, so the get-an-API-key doc
links open the system browser and the webview never navigates,
while the two preview provider sections (preview.kagi and
preview.ai) each append a Test row: the button reads the WORKING COPY's
candidate values (providerTestRequest -- unsaved edits testable),
calls TestPreviewProvider, disables itself while in flight, and
renders the honest ok/error outcome inline beside the button (the
Kagi hint names the 1-credit cost) -- and the watcher section appends
a "Set up full-filesystem watching" action row (appendWatchSetupRow,
#cfg-watchsetup) that calls the SetupWatch bound method (the in-app
retry for a declined fanotify-capability prompt; Go runs pkexec+setcap
and returns a human message, applied at the next launch) and shows the
outcome inline; array-of-string = one-per-line textarea
(trimmed, blanks dropped); object whose patternProperties values
are all strings = key/value row editor with add/remove
(bangs.aliases); EVERYTHING else (plugins.entries, rewrites, any
future shape) = raw-JSON textarea that must JSON.parse before save
("invalid JSON" marks + blocks). HIDING is schema-annotation
driven: a node carrying "x-editor-hidden": true -- checked on the
property node AND its resolved $ref target (editorHidden) -- is
skipped, leaf or whole subtree, at every depth; rootsVersion,
"$schema", window.width/height, and preview.windowWidth/
windowHeight carry the annotation in schemas/config.schema.json --
the window sizes are set by DRAGGING the bar's edges (resize.ts),
so editor rows would fight the drag (no
hard-coded key list; vendor keys are invisible to the lockstep
schema tests, which compare property-NAME lists only; toc.test.ts
pins all six annotations). The filter
hides rows by dotted path
+ description, then sections left empty -- and mirrors into the
ToC: zero-match entries dim (.config-toc-dim), matching entries
show a visible-row count badge (parent counts include their
sub-sections); clicking a dimmed entry whose section the filter
hid clears the filter first, then jumps. Controls write through
setVal -> setPath into the working copy (dirty note + accent Save
button); Save (button/Ctrl+S) = SaveConfig(JSON.stringify(doc)) ->
error strips verbatim on failure, else "Saved" flash + notices
(Applied live list, per-knob "<knob> takes effect at next launch"
for nextLaunch/pending -- NEVER "restart" wording -- applyErrors
as warnings), re-fetch (Normalize's repaired truth; fresh doc
clears the summary slate first) + re-render preserving scroll/
filter, and refreshPreviewConfig (GUI saves fire no config:changed
-- self-write suppression -- so the applyPreviewConfig refresh
runs here). GetConfigForEdit's unknownKeys render a persistent
warning strip (dropped-if-saved; points at Open config.json,
which calls OpenConfigFile and keeps the editor open).
"config:changed" (external edit): open + clean = silent
re-fetch/re-render + transient
"changed on disk -- reloaded" flash + the event's own summary;
open + dirty = keep the edits, show a "changed on disk" strip
with a Reload button; event
error = error strip, doc kept. EXIT: Esc and Close both call the
CloseConfigWindow bound method, which quits this process -- clean
= immediately, dirty = the first press flashes "unsaved changes --
press Esc again to discard" and a second within 2s closes.
Own window keydown handler (Esc + Ctrl/Cmd+S);
configModeActive() is still exported (main.ts's searchbar wiring
never runs here, so nothing consumes it in this process). All DOM is
text-node-only; config.css consumes existing --sb-* tokens with
literal dark fallbacks -- NO new --sb-* token, no :root block) +
`src/resize.ts` (DRAG-EDGE WINDOW RESIZING, wired by wire()'s
initResize: deliberately ELEMENT-FREE -- document-level pointer
listeners classify positions against ~6px left/right/bottom bands
+ L-shaped bottom corners (edgeZone, pure + vitest-pinned in
resize.test.ts; the top edge is excluded, it hosts the query row
and the anchor), so NO overlay strips exist to intercept wheel/
hover/selection/preview/sidebar events (WebKitGTK dispatches no
DOM pointer events for native scrollbar interaction, so the
results scrollbar at the right edge stays usable -- and the flip
side is a documented yield: where a scrollbar hugs the right edge
(overflowing results, the config editor body) it owns that strip
and right-edge drags yield to it; left/bottom always work); hover
swaps
documentElement.style.cursor (ew/ns/nesw/nwse-resize), a
capture-phase pointerdown inside a band preventDefault+
stopPropagation-claims the drag + pointer capture (guarded --
jsdom has none), pointermove computes ABSOLUTE targets from the
drag-start size (dragTarget: horizontal = 2x the travel, about
center; vertical = 1:1 downward; no accumulation across dropped
frames) rAF-coalesced into ResizeDrag(w, h), and pointerup/cancel
commits ONCE via ResizeCommit (moveless edge clicks commit
nothing); the settings window has no drag wiring at all -- it is
an ordinary resizable window) +
`src/highlight.ts` (hljs lib/core + explicitly registered grammars
covering every LangHint name in the hljs distribution plus
shell/plaintext -- never import the full highlight.js bundle;
highlightInto: hinted registered language first, highlightAuto only
for unhinted content <= 64KB, plain text beyond or on any error;
`setHighlighted` is the frontend's ONE sanctioned innerHTML-style
sink -- createContextualFragment into the single <code> node, fed
EXCLUSIVELY hljs output, which HTML-escapes all content text by
documented contract; the invariant comment on it is load-bearing,
never route other strings through it) + `src/hljs-theme.css` (hljs
token classes -> var(--sb-*) with literal dark fallbacks, scoped
under .preview-code, imported from highlight.ts; NO new --sb-*
token -- the :root block is a sync_test.go contract) +
`src/wails.d.ts` (ambient
types for the Wails-injected `window.go` / `window.runtime` incl.
EventsOn, the event payload shapes (incl. StatsSnapshot -- the
GetStats return AND "stats:update" payload, field names lockstep
with internal/sysstats.Snapshot json tags), and the plugin wire
contract TargetInfo/PluginAction (incl. activate_window + its
window field, activate_tab + its tab token field, and the internal
desktop_id -- all echoed back
unchanged)/PluginResult/PluginEmission plus the preview contract
Preview{Target,Payload,ConfigInfo,MetaRow,Text,Image,Dir,DirEntry,
Web,WebResult,AI}, ResolveIcons, the four preview bound
methods, and the provider-UX pair TestPreviewProvider
(PreviewProviderTest -> PreviewProbeResult) + OpenExternalURL
(ConfigInfo carries kagiConfigured + aiConfigured), plus the
config-editor contract ConfigForEdit/ConfigSaveResult/
ConfigChangedEvent (Go nil slices arrive as null -- the applied/
pending fields are `string[] | null`) and the four config bound
methods GetConfigSchema/GetConfigForEdit/SaveConfig/OpenConfigFile
plus the settings-window pair GetStartupMode/CloseConfigWindow
plus the drag-resize pair ResizeDrag/ResizeCommit,
and the telemetry report contract
Telemetry{PickReport,ShownRef,PickedRef} and RecordPick -- keep in
sync with internal/app + internal/plugin + internal/preview +
internal/sysstats + internal/telemetry payload
structs; field names lockstep with configui.go/configapply.go json
tags).
