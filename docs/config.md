# internal/config

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/config` -- config.json load/save (roots, rootsVersion,
excludes, hotkey,
rescanIntervalMinutes, maxResults. BOOLEAN POLARITY (v7, the
tray.enabled convention): every enable/disable-style switch is the
AFFIRMATIVE `enabled` spelling, and the default-ON ones are *bool
fields (`config.Bool` builds pointers, `config.Enabled(p)` is the
nil-safe effective read: nil = absent = ON) so an absent key stays
distinguishable from an explicit false -- Normalize repairs nil to
explicit true for search.fuzzyEnabled, search.frecency/priors/
arbiter.enabled, watcher.sweepEnabled, plugins.enabled, per-entry
plugins.entries.<id>.enabled, tray.enabled, history.persistEnabled,
stats.enabled and -- since v8 -- preview.enabled, while
rewrites[].enabled deliberately stays
nil-able (omitempty; user rule objects never grow keys). search
{fuzzyEnabled -- the
fuzzy-tier kill switch, absent/true = fuzzy ON; main.go wires its
inverse to Manager.SetFuzzyDisabled -- and
frecency {enabled, halfLifeDays 14, weightFrecency/weightRecency/
weightCwd/weightNoise 1.0, tierJumpCount 3.0}, the ranking-blend
knobs main.go wires to app.Options.Frecency; NUMERIC CONVENTION
UNIQUE HERE: Normalize repairs only the EXACT zero to the default
(halfLifeDays repairs <= 0), a NEGATIVE value is the documented
per-signal off switch and passes through, and the schema rejects
the ambiguous literal 0 -- and priors {enabled}, the
pick-memory priors knob (ON by default; enabled=false is a debug
escape hatch) main.go wires to
app.Options.Priors -- and telemetry {maxSizeKB 65536}, the
ALWAYS-ON local ranking log's one knob (deliberately NO off
switch of either polarity: the log is private by staying on the
machine; query text
and plugin titles recorded in full) main.go wires to
app.Options.Telemetry, Normalize repairs maxSizeKB <= 0 while the
schema rejects it -- and arbiter {enabled}, the learned
composition arbitration knob main.go wires to app.Options.Arbiter
(ON by default; enabled=false is a debug
escape hatch / kill switch)},
watcher {maxWatches 0 = auto-budget / negative = unlimited,
sweepMinutes 0 = the 20m default, sweepEnabled (absent = sweeps
ON), watchExcludes
(json omitempty; excluder-syntax patterns never LIVE-WATCHED but
still indexed + swept), backend (json omitempty; the
WatcherBackend* constants "auto"/"fanotify"/"fsevents"/"inotify" --
fanotify (linux) and fsevents (darwin) = STRICT, no per-dir
fallback, and "kqueue" is deliberately NOT a config value (runtime
label only); Normalize trims+lowercases and repairs
empty/unknown to "auto", schema enum in lockstep), setupEnabled
(*bool, ON by default -- the tray.enabled convention, nil repaired to
true; false is the persistent opt-out from the automatic
fanotify-capability setup, internal/watchsetup, read by main.go's
runGUI BEFORE wails.Run, NOT copied into app.Options) -- main.go copies
the first five into app.Options
{WatchMaxWatches, SweepInterval, SweepDisabled (inverted),
WatchExcludes,
WatchBackend}},
theme, plugins {enabled, entries
{<id>: {enabled, settings}}}, bangs {sigils, aliases}, rewrites
[{name, pattern, replacement, title?, icon?, enabled?}] (the regex
rewrite rules; passed to plugin.Options.Rewrites), tray
{enabled}, history {persistEnabled}, stats {enabled -- the
system-stats sampler kill switch, absent = on; internal/app's
buildStats reads it, so it
applies on the next launch}, window {translucent -- the
per-pixel-alpha window flag main.go reads via
app.WindowTranslucent(); zero value = opaque = the safe default,
needs a compositor, README "Translucent window" holds the measured
evidence; width/height -- the bar window size main.go reads via
app.WindowSize(), defaults 780x550: Normalize repairs <= 0 (and
absent) to the defaults and clamps positive values below the
320x240 floors up to them, so the app never builds an unusably
tiny window}, firefox {frequentSites
{minVisitsMonth 11, minVisitsWeek 1, refreshMinutes 10, maxResults
6, profileDir ""}, openTabs {maxResults 6, profileDir ""}} -- the
frequentSites defaults encode ">10 visits in 30 days AND >=1 in 7";
the numeric knobs are Normalize-repaired to defaults when <= 0,
both profileDirs are passed through verbatim), preview {enabled --
*bool, ON by default since v8 (nil repaired to true; explicit false
= the opt-out; the v8 migration resets a PRE-flip stored false as
machine handwriting, see migrate.go) -- windowWidth 1100,
windowHeight 700, textMaxKB 256, imageMaxEdge 800,
dirMaxEntries 200, kagi {apiKey, baseUrl, maxResults 8}, and ai
{apiKey, baseUrl, model, maxOutputTokens 1024} -- the ONE
user-described AI endpoint (v9): baseUrl and model have NO default
(unknowable for an arbitrary server, and a default endpoint would
mean sending queries somewhere nobody chose), so only
maxOutputTokens is Normalize-repaired}} -- the
preview pane; the API keys AND base URLs pass through verbatim
(empty kagi baseUrl = the official endpoint; validation happens in
internal/preview, not here) and are never
logged. Lives under
os.UserConfigDir(); the `COMPETENT_SEARCH_CONFIG_DIR` env var
overrides the directory (tests rely on this); `Dir()` exposes that
directory (the plugins/ and themes/ dirs and history.json live
inside it, next to config.json). The app's OTHER env knobs live with their owners:
`COMPETENT_SEARCH_SOCKET` (internal/ipc, the single-instance socket
path), `COMPETENT_SEARCH_HOTKEY_BACKEND` (internal/app hotkey.go,
backend override), `COMPETENT_SEARCH_NO_SERVICE` (internal/app
service.go, the login-service auto-registration gate) and
`COMPETENT_SEARCH_NO_WATCH_SETUP` (internal/watchsetup, the per-process
gate on the automatic fanotify-capability setup; the persistent
per-user opt-out is config watcher.setupEnabled=false),
`COMPETENT_SEARCH_CONFIG_SOCKET` (internal/ipc, the settings
window's socket) and `COMPETENT_SEARCH_AI_API_KEY` (internal/app
preview.go, the AI endpoint's key when preview.ai.apiKey is empty)
-- all documented in the README. Default
roots are the WHOLE FILESYSTEM (migrate.go: defaultRootsFor -- "/"
on linux/darwin, %SystemDrive% with C:\ fallback on windows; goos +
getenv are parameters so tests cover the windows shape headlessly)
and default excludes = baseExcludes (.git node_modules .cache --
FROZEN as the v2-era set migrations compare against, new defaults
never go there) + noiseExcludes (.hg .svn __pycache__ .mypy_cache
.pytest_cache .ruff_cache .tox .nox .venv, the v3 high-churn set) +
the system trees (/proc /sys /dev /run /tmp /var/tmp full-path +
lost+found by name; unix-likes only -- windows gets the name
patterns without system trees) + firmlinkExcludesFor (darwin only:
/System/Volumes/Data full-path -- the APFS Data volume macOS ALSO
exposes at the firmlinked canonical paths /Users, /Applications,
..., so an unguarded "/" walk indexes ~45% of the disk twice) +
darwinNoiseExcludesFor (darwin only, the v5 set: Caches,
DerivedData, _CodeSignature, CodeResources by name +
/private/var/folders full-path -- macOS's real per-user temp tree,
the /tmp and /var/tmp excludes only cover the symlinked spellings;
wholesale .app/.framework internals and Application Support are
DELIBERATELY not excluded -- they change search semantics and
await an owner decision).
THE $SCHEMA RESERVED KEY: Config's FIRST field is `Schema` (json
`$schema,omitempty`), so Save/Encode emit `"$schema":
"./config.schema.json"` (config.SchemaRef) as the document's first
key -- Default() carries it, Normalize stamps an EMPTY value
(existing configs gain it on their next save; a hand-set value
passes through verbatim), the loader never validates it, the GUI
strict decode accepts it, UnknownKeys knows it (never reported),
and the editor hides it (x-editor-hidden). The referenced sidecar
<configDir>/config.schema.json is written by internal/app's
schemasidecar.go at every Startup (embedded
schemas.ConfigSchemaJSON, byte-equal = skip, atomic temp+rename,
before the config-dir watcher comes up; its file name never
matches the config.json hot-apply path, so no watcher loop).
rootsVersion (0 = legacy, current
9) drives the one-shot Load migration (migrateRootsFor; goos and
the RAW file bytes are parameters so tests cover the darwin shape
and the old-key reads headlessly), each missing
step applied in
order: the v2 step moves configs whose roots are exactly the legacy
home default (or empty) to the new default roots + appends the
missing system excludes (user patterns untouched; customized roots
stamped only); the v3 step appends the MISSING noiseExcludes -- but
ONLY when the exclude list still contains ALL of baseExcludes
(default-shaped); a curated-away or explicitly empty list is
stamped only, with an informational note; the v4 step applies the
identical policy to the darwin firmlink exclude (non-darwin = pure
stamp, nothing added or announced); the v5 step applies it AGAIN to
darwinNoiseExcludesFor -- a NEW version rather than an extension of
v4, so configs a v4-era build already stamped still receive it --
the v6 step (migrateRankingDefaults, reads the RAW bytes) makes
search.telemetry always-on
(every old enabled/retainQueries key dropped outright, an explicit
enabled:false included -- overruled by design and announced) and
flips search.priors/arbiter to on-by-default (absent old key = on;
an explicit pre-v6 enabled value parses straight into the v7-shaped
struct, so the step only announces); the v7 step
(migrateBoolPolarity, migrate_v7.go, reads the RAW bytes because
the old keys left the struct) is the boolean-polarity rename --
every negative disabled-style key explicitly present lands as the
affirmative enabled key with the VALUE INVERTED (one note per key,
"migrated tray.disabled=true -> tray.enabled=false" style), absent
old keys migrate nothing (absent still means ON), and a document
carrying BOTH spellings keeps the new key and drops the old one,
announced (plugins.entries.<id> and rewrites[i] pairs included,
entries sorted by id for deterministic notes); the v8 step
(migratePreviewDefaultOn) flips the preview pane ON by default --
a pre-v8 stored preview.enabled=false is treated as the plain-bool
era's machine handwriting (the feature was opt-in; deliberate off
was expressed by never opting in) and dropped to nil for Normalize
to repair to explicit true, with a loud note either way (absent =
announce-only, false = reset + announce, explicit true = silent);
a false stamped at rootsVersion >= 8 is a real post-flip opt-out
the step never revisits; the v9 step (migrateAIProvider,
migrate_v9.go, RAW bytes -- see that file's header) collapses the
three AI providers into preview.ai, carrying the SELECTED one's
settings over (openai/anthropic gain the endpoint they used to
imply, custom keeps its own) and announcing the collapse, the
changed wire shape and the one key variable; a document already
carrying preview.ai keeps it, and a config that never configured an
AI provider migrates and announces nothing -- and each step is
gated on its
own version so already-fired informational notes never repeat.
Either way version 9 is
Saved back, and every user-visible change lands in the
non-serialized MigrationNotes (json:"-") that internal/app logs
loudly at startup -- the scope never changes silently. `Load` never crashes: missing file -> defaults
written, corrupt file -> current defaults + error returned for
logging, failed migration rewrite -> migrated config + error.
`Normalize` repairs zero values (empty theme -> dark, nil plugin
entries/bang aliases -> empty maps, empty sigils -> the ! / @
defaults; the affirmative *bool switches' nil pointers -> explicit
true, the tray.enabled convention (rewrites[].enabled excepted --
it stays nil-able); non-positive firefox.frequentSites
and firefox.openTabs numbers -> their defaults; negative
watcher.sweepMinutes -> 0, while watcher.maxWatches keeps its sign
and watchExcludes stays as written); entry settings are
opaque json.RawMessage forwarded verbatim to that plugin. `Save` is
ATOMIC (temp-file-then-rename in the config dir, the
internal/history pattern at the file's historical 0644 perms; a
crash never truncates config.json and watchers see one rename per
save); `Encode(c)` returns exactly the bytes Save writes (two-space
indent + trailing newline -- the app's self-write suppression hashes
them); `CurrentRootsVersion()` exposes the build's rootsVersion
stamp (the GUI save path preserves the on-disk stamp so a full-file
rewrite can never re-trigger the Load migrations); and
`UnknownKeys(raw)` (unknown.go) reflectively walks a raw
config.json document against Config's json tags and reports the
dotted paths of keys a full rewrite would drop ("$schema" included;
maps by key, arrays by index, json.RawMessage settings opaque,
wrong-shaped values skipped -- unknown KEYS only, the strict decode
owns type errors), sorted; the config editor warns with it.
