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

// GetPreviewConfig answer (internal/app PreviewConfigInfo): whether
// the pane is on and whether the web/AI providers have credentials
// (config key or environment variable). The key values themselves
// never cross to the frontend.
interface PreviewConfigInfo {
  enabled: boolean;
  kagiConfigured: boolean;
  openaiConfigured: boolean;
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
  // Resolved theme tokens: every internal/theme.TokenNames key mapped
  // to a validated CSS value (theme.ts sets each as --sb-<key>).
  GetTheme(): Promise<Record<string, string>>;
  // Contents of <configDir>/themes/custom.css (<= 64KB), else "".
  GetCustomCSS(): Promise<string>;
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

interface WailsGo {
  app: {
    App: WailsAppBindings;
  };
}

interface Window {
  go?: WailsGo;
  runtime?: WailsRuntime;
}
