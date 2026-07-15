// Ambient declarations for the globals that the Wails v2 runtime
// injects into the page. window.go carries the bound Go methods
// (window.go.<package>.<Struct>.<Method>); window.runtime carries the
// Wails runtime API. Both appear shortly after page load, so code must
// tolerate them being briefly undefined.

interface WailsSearchResult {
  path: string;
  name: string;
  isDir: boolean;
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
    | "run_builtin";
  value?: string; // every type except run_command
  argv?: string[]; // run_command only
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
