// Ambient declarations for the globals that the Wails v2 runtime
// injects into the page. window.go carries the bound Go methods
// (window.go.<package>.<Struct>.<Method>); window.runtime carries the
// Wails runtime API. Both appear shortly after page load, so code must
// tolerate them being briefly undefined. Kept minimal for the scaffold
// phase; extend in later phases as more methods are bound.

interface WailsSearchResult {
  path: string;
  name: string;
  isDir: boolean;
}

interface WailsAppBindings {
  Search(query: string): Promise<WailsSearchResult[]>;
  Open(path: string): Promise<void>;
  Reveal(path: string): Promise<void>;
  Hide(): Promise<void>;
}

interface WailsGo {
  app: {
    App: WailsAppBindings;
  };
}

interface Window {
  go?: WailsGo;
  runtime?: Record<string, unknown>;
}
