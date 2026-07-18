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
}

// One virtual result in a "plugin:results" emission (internal/plugin
// Result, after the Go-side sanitizer).
interface PluginResult {
  title: string; // always non-empty
  subtitle?: string;
  icon?: string; // builtin icon name [a-z0-9_-]+ OR literal glyph/emoji
  badge?: string;
  accent_color?: string; // "#rgb" | "#rrggbb" -- ONLY ever sets --plugin-accent
  score?: number; // 0..100; in practice always present
  fields?: { label: string; value: string }[]; // <= 8
  action?: PluginAction; // absent => Enter/click is a no-op row
}

// Payload of the "plugin:results" event (internal/plugin Emission).
// One emission arrives per provider per generation.
interface PluginEmission {
  plugin: string; // section key + RunPluginAction pluginId
  name: string; // section header display name
  gen: number; // DROP unless === the current frontend seq
  results: PluginResult[]; // non-empty (empty answers never emit)
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
// fanotify whole-filesystem backend; otherwise hint carries a short
// user-facing explanation the status-bar chip shows on hover.
interface WatchBackendEvent {
  backend: "fanotify" | "inotify" | "none";
  full: boolean;
  hint: string;
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
