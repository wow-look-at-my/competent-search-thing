// Ambient declarations for the globals that the Wails v2 runtime
// injects into the page. window.go carries the bound Go methods
// (window.go.<package>.<Struct>.<Method>); window.runtime carries the
// Wails runtime API. Both appear shortly after page load, so code must
// tolerate them being briefly undefined.

interface WailsSearchResult {
  path: string;
  name: string;
  isDir: boolean;
  // Optional note rendered in place of the parent-dir line (the
  // outside-indexed-roots hint; internal/index Result.Hint).
  hint?: string;
  // Per-character highlight ranges on name: half-open [start, end)
  // RUNE (code point) index pairs, sorted and merged, minted by the
  // Go matching engine (internal/index Result.MatchRanges). JS
  // strings are UTF-16: convert while walking code points.
  matchRanges?: [number, number][];
}

// Bang-target info returned by QueryPlugins (internal/plugin
// TargetInfo). targeted false means the query is not bang-targeted
// and the other fields are zero values (chip off).
interface TargetInfo {
  targeted: boolean;
  plugin: string; // provider id ("calc")
  name: string; // display name ("Calculator")
  bang: string; // canonical bang name ("calc")
}

// An activation action carried by a plugin result (internal/plugin
// Action). set_query is frontend-local (replace the input and
// re-search); every other type is executed by RunPluginAction, which
// re-validates it Go-side.
interface PluginAction {
  type:
    | "open_path"
    | "open_url"
    | "copy_text"
    | "run_command"
    | "set_query"
    | "run_builtin"
    | "activate_window";
  value?: string; // every type except run_command and activate_window
  argv?: string[]; // run_command only
  window?: string; // activate_window only: the window id to focus
  // run_command only, set by the builtin app launchers: the .desktop
  // entry behind the launch; Go resolves it for launch capabilities
  // (D-Bus activation, startup notification) so the launched app's
  // window ends up focused. Echo it back unchanged.
  desktop_id?: string;
}

// One virtual result in a "plugin:results" emission (internal/plugin
// Result, after the Go-side sanitizer).
interface PluginResult {
  title: string; // always non-empty
  subtitle?: string;
  icon?: string; // builtin icon name [a-z0-9_-]+ OR literal glyph/emoji
  // INTERNAL-ONLY icon-resolution key ("app:<ref>"), stamped by
  // trusted builtin sources and stripped from external plugins by the
  // Go sanitizer: render.ts batches visible keys through ResolveIcons
  // and swaps the glyph for the resolved image; misses keep the glyph.
  iconKey?: string;
  badge?: string;
  accent_color?: string; // "#rgb" | "#rrggbb" -- ONLY ever sets --plugin-accent
  score?: number; // 0..100; in practice always present (engine-minted)
  fields?: { label: string; value: string }[]; // <= 8
  action?: PluginAction; // absent => Enter/click is a no-op row
  // Extra engine match texts (plugin authors' findability field);
  // present on the wire but not rendered.
  keywords?: string[];
  // Per-character highlight ranges on title: half-open [start, end)
  // RUNE index pairs (engine-minted, or plugin-supplied and
  // sanitized). Same rendering as file-row matchRanges.
  matchRanges?: [number, number][];
}

// Payload of the "plugin:results" event (internal/plugin Emission).
// One emission arrives per provider per generation.
interface PluginEmission {
  plugin: string; // section key + RunPluginAction pluginId
  name: string; // section header display name
  gen: number; // DROP unless === the current frontend seq
  results: PluginResult[]; // non-empty (empty answers never emit)
  // Source priority, stamped registry-side for builtin sources only
  // (internal/plugin Emission.Priority, json omitempty: absent means
  // 0; external plugins can never set it). Sections with priority > 0
  // render in the #priority-results zone ABOVE the file rows, and
  // the magnitude orders prioritized sections among themselves.
  priority?: number;
}

// The preview target QueryPreview sends (internal/preview Target).
// kind selects which fields matter: "file" carries path/isDir,
// "plugin" carries title/subtitle/pluginName, "none" cancels the
// in-flight preview. Absent fields marshal as Go zero values.
interface PreviewTarget {
  kind: "file" | "plugin" | "none";
  path?: string;
  isDir?: boolean;
  title?: string;
  subtitle?: string;
  pluginName?: string;
}

// One label/value line of a preview metadata card (internal/preview
// MetaRow).
interface PreviewMetaRow {
  label: string;
  value: string;
}

// A capped text-file read (internal/preview TextPreview).
interface PreviewText {
  content: string;
  lang: string; // highlight.js language name; "" = plain text
  truncated: boolean;
  sizeBytes: number;
}

// A downscaled thumbnail (internal/preview ImagePreview).
interface PreviewImage {
  dataUri: string; // data:image/png;base64,... or data:image/jpeg;...
  w: number;
  h: number;
  origW: number;
  origH: number;
  sizeBytes: number;
}

// One row of a directory listing (internal/preview DirEntry).
interface PreviewDirEntry {
  name: string;
  isDir: boolean;
  size: number;
}

// A capped, sorted directory listing (internal/preview DirPreview).
interface PreviewDir {
  entries: PreviewDirEntry[];
  total: number; // whole-directory count, before the cap
  truncated: boolean;
}

// One web-search hit (internal/preview WebResult).
interface PreviewWebResult {
  title: string;
  url: string;
  snippet: string;
}

// A web-search answer (internal/preview WebPreview).
interface PreviewWeb {
  query: string;
  results: PreviewWebResult[];
  cached: boolean;
}

// An AI answer (internal/preview AIPreview).
interface PreviewAI {
  query: string;
  answer: string;
  model: string;
  cached: boolean;
}

// Payload of the "preview:result" event (internal/preview Payload).
// kind selects which optional section is set. Payloads whose gen is
// not the current preview generation are DROPPED, and multiple
// emissions per gen REPLACE the pane content -- a fast "meta" card
// often precedes the rich payload (cache hits skip the meta card).
interface PreviewPayload {
  gen: number;
  kind: "meta" | "text" | "image" | "dir" | "web" | "ai" | "error";
  title: string;
  path: string;
  meta?: PreviewMetaRow[];
  text?: PreviewText;
  image?: PreviewImage;
  dir?: PreviewDir;
  web?: PreviewWeb;
  ai?: PreviewAI;
  err?: string; // human-readable failure (kind "error")
  durMs: number;
}

// One delivered row's identity in a RecordPick report (internal/
// telemetry ShownRef): the slice index is the rank. File rows carry
// only the path; plugin rows carry the provider id, the engine wire
// score, and the rendered title (the one display field only the
// frontend knows). Ranking FEATURE values are NEVER sent from the
// frontend -- the Go side joins them from its own query ring.
interface TelemetryShownRef {
  kind: "file" | "plugin";
  path?: string; // file rows
  plugin?: string; // plugin rows
  score?: number; // plugin rows: the engine wire score
  title?: string; // plugin rows: the rendered title
}

// The activated row of a RecordPick report (internal/telemetry
// PickedRef): its index into shown, the action kind that ran, and the
// reveal flag (file rows only).
interface TelemetryPickedRef {
  rank: number;
  action: string;
  revealed: boolean;
}

// A RecordPick report (internal/telemetry PickReport): the query, the
// delivered flat row list, and the pick. Sent fire-and-forget after
// an activation actually ran; a Go-side no-op when the local ranking
// log is off (search.telemetry.disabled, the debug escape hatch).
interface TelemetryPickReport {
  query: string;
  shown: TelemetryShownRef[];
  picked: TelemetryPickedRef;
}

// One fps meter report (internal/app FPSSample; field names lockstep
// with its json tags). Built by fpsmeter.ts summarize from one window
// of visible rAF frame deltas; sent through RecordFPSSample, a
// Go-side no-op unless COMPETENT_SEARCH_FPS=1.
interface FPSSample {
  avgFps: number;
  maxFps: number;
  longFramePct: number;
  windowMs: number;
  frames: number;
  inferredHz: number;
}

// GetPreviewConfig answer (internal/app PreviewConfigInfo): whether
// the pane is on, whether the web/AI providers have credentials
// (config key or environment variable), and the pixel width the left
// results column keeps while the pane is on (the flag-off bar width,
// config window.width). The key values themselves never cross to the
// frontend.
interface PreviewConfigInfo {
  enabled: boolean;
  kagiConfigured: boolean;
  openaiConfigured: boolean;
  resultsWidth: number;
}

// GetConfigForEdit answer (internal/app ConfigForEdit): the current
// configuration, freshly loaded and normalized, as indented JSON --
// the editor's starting document -- plus the file path, a non-fatal
// load warning, and the on-disk file's unknown keys (they survive
// hand edits but are DROPPED by a GUI save; the editor warns).
interface ConfigForEdit {
  configJson: string;
  path: string;
  loadWarning?: string;
  unknownKeys?: string[] | null;
}

// SaveConfig answer (internal/app SaveResult). error carries the
// strict-decode or write failure (ok false); applied/pending mirror
// the live-apply pass (Go nil slices arrive as null); applyErrors are
// per-section apply failures (the save itself landed); nextLaunch
// lists the ruled next-launch knobs by name -- today only
// "window.translucent" -- surfaced verbatim, never as a generic
// "restart" notion.
interface ConfigSaveResult {
  ok: boolean;
  error?: string;
  applied: string[] | null;
  pending: string[] | null;
  applyErrors?: string[] | null;
  nextLaunch?: string[] | null;
}

// Payload of the "config:changed" event (internal/app
// configChangedEvent): an external config.json edit hot-applied
// (applied/pending/nextLaunch as in ConfigSaveResult) or failed to
// load (error; the previous config stays applied).
interface ConfigChangedEvent {
  applied: string[] | null;
  pending: string[] | null;
  nextLaunch?: string[] | null;
  error?: string;
}

interface WailsAppBindings {
  Search(query: string): Promise<WailsSearchResult[]>;
  Open(path: string): Promise<void>;
  Reveal(path: string): Promise<void>;
  Hide(): Promise<void>;
  QueryPlugins(query: string, gen: number): Promise<TargetInfo>;
  RunPluginAction(pluginId: string, action: PluginAction): Promise<void>;
  // The bang command cheat sheet shown for an empty query: the
  // suggestions provider's answer for a bare primary sigil, as one
  // synchronous Emission with gen 0 and results possibly empty
  // (never null Go-side; main.ts still tolerates it defensively).
  CheatSheet(): Promise<PluginEmission>;
  // The committed query history, oldest -> newest (never null
  // Go-side; main.ts still tolerates it defensively). Fetched at
  // wire-up and refetched after every AddHistory.
  GetHistory(): Promise<string[] | null>;
  // Record one executed query (called after an activation actually
  // ran; Go trims it, skips blanks, and dedups exact repeats).
  AddHistory(entry: string): Promise<void>;
  // Icon resolution (internal/app icons.go over internal/icons):
  // maps icon keys ("app:<ref>") to data URIs at the wanted physical
  // pixel size; keys that miss are absent (never null Go-side;
  // render.ts still tolerates it defensively). Batched per render
  // tick -- rows keep their glyph until the answer lands.
  ResolveIcons(keys: string[], size: number): Promise<Record<string, string>>;
  // Report one activated result for the opt-in ranking telemetry log
  // (called beside AddHistory at the same activation-success sites,
  // fire-and-forget). Go re-validates the echoed report, joins the
  // ranking signals from its own query ring, and appends locally; a
  // silent no-op while search.telemetry is off or the query is blank.
  RecordPick(report: TelemetryPickReport): Promise<void>;
  // Dev-only fps meter (fpsmeter.ts; COMPETENT_SEARCH_FPS=1 Go-side).
  // FPSEnabled gates the whole frontend loop -- false registers
  // nothing; RecordFPSSample logs one validated summary line and is a
  // silent no-op while the meter is off.
  FPSEnabled(): Promise<boolean>;
  RecordFPSSample(sample: FPSSample): Promise<void>;
  // Resolved theme tokens: every internal/theme.TokenNames key mapped
  // to a validated CSS value (theme.ts sets each as --sb-<key>).
  GetTheme(): Promise<Record<string, string>>;
  // Contents of <configDir>/themes/custom.css (<= 64KB), else "".
  GetCustomCSS(): Promise<string>;
  // The system-stats sampler's cached snapshot -- instant Go-side
  // (never IO), so it is safe to call on the show path. enabled false
  // (stats.disabled / no sampler) = hide the row entirely.
  GetStats(): Promise<StatsSnapshot>;
  // Preview pane (internal/app preview.go). QueryPreview asks for
  // target under generation gen; answers arrive asynchronously as
  // "preview:result" events carrying gen. A {kind: "none"} target
  // cancels the in-flight request without starting a new one.
  // Fetch{Web,AI}Preview are the EXPLICIT web-search / AI triggers --
  // never called from a keystroke or selection path. All three are
  // safe no-ops while the pane is disabled.
  QueryPreview(target: PreviewTarget, gen: number): Promise<void>;
  GetPreviewConfig(): Promise<PreviewConfigInfo>;
  FetchWebPreview(query: string, gen: number): Promise<void>;
  FetchAIPreview(query: string, gen: number): Promise<void>;
  // Config editor (internal/app configui.go). GetConfigSchema returns
  // the embedded config.schema.json document (the editor renders from
  // it); GetConfigForEdit starts an edit session over the current
  // configuration; SaveConfig strictly validates the composed JSON,
  // saves atomically, and live-applies the result; OpenConfigFile
  // opens config.json itself with the OS default handler (the hand-
  // edit escape hatch -- external edits hot-apply via config:changed).
  GetConfigSchema(): Promise<string>;
  GetConfigForEdit(): Promise<ConfigForEdit>;
  SaveConfig(raw: string): Promise<ConfigSaveResult>;
  OpenConfigFile(): Promise<void>;
  // Drag-edge window resizing (internal/app resize.go, driven by
  // resize.ts). ResizeDrag applies one rAF-coalesced drag frame
  // (clamped, centered, never persisted); ResizeCommit applies the
  // final geometry and persists it to config.json in ONE atomic,
  // self-write-suppressed save. Go owns clamping and which config
  // fields a drag writes (window.* or preview.window* while the
  // preview pane is mounted).
  ResizeDrag(w: number, h: number): Promise<void>;
  ResizeCommit(w: number, h: number): Promise<void>;
}

// The subset of the Wails runtime API this app uses (see the wails v2
// runtime.d.ts). EventsOn returns an unsubscribe function.
interface WailsRuntime {
  EventsOn(
    eventName: string,
    callback: (...data: unknown[]) => void,
  ): () => void;
  EventsOff(eventName: string, ...additionalEventNames: string[]): void;
}

// Payload of the "index:progress" event (internal/app indexProgress).
interface IndexProgressEvent {
  indexed: number;
  done: boolean;
  seconds: number;
}

// Payload of the "watch:degraded" event (internal/app watchDegraded).
interface WatchDegradedEvent {
  watched: number;
  dropped: number;
  overflows: number;
}

// Payload of the "watch:backend" event (internal/app watchBackend),
// emitted once when the watch layer is up. full is true only for the
// whole-filesystem backends (fanotify on Linux, fsevents on macOS);
// otherwise hint carries a short user-facing explanation the
// status-bar chip shows on hover. The per-directory model reports its
// honest per-OS label (inotify / kqueue / windows).
interface WatchBackendEvent {
  backend: "fanotify" | "fsevents" | "inotify" | "kqueue" | "windows" | "none";
  full: boolean;
  hint: string;
}

// Payload of the "stats:update" event AND the GetStats return
// (internal/sysstats Snapshot; keep field names in lockstep with its
// json tags). enabled false means the feature is off (stats.disabled):
// hide the #stats row entirely and skip rendering. enabled true with a
// metric's *Ok false means that one metric has no live value (missing
// source, non-Linux, failed read, rate not accumulated yet): render a
// dash. Sizes are bytes, rates bytes/second, percentages 0..100.
// Events fire only while the bar is visible, every ~1.5s, plus one at
// summon and a ~300ms follow-up.
interface StatsSnapshot {
  enabled: boolean;
  cpuPct: number;
  cpuOk: boolean;
  gpuPct: number;
  gpuOk: boolean;
  memUsed: number;
  memTotal: number;
  memOk: boolean;
  swapUsed: number;
  swapTotal: number;
  swapOk: boolean;
  netRxBps: number;
  netTxBps: number;
  netOk: boolean;
}

interface WailsGo {
  app: {
    App: WailsAppBindings;
  };
}

interface Window {
  go?: WailsGo;
  runtime?: WailsRuntime;
}
