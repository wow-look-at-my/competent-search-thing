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

interface WailsAppBindings {
  Search(query: string): Promise<WailsSearchResult[]>;
  Open(path: string): Promise<void>;
  Reveal(path: string): Promise<void>;
  Hide(): Promise<void>;
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
