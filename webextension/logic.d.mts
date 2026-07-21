// Type declarations for logic.mjs, consumed by the vitest suite
// (frontend/src/ffext-logic.test.ts imports the .mjs directly; tsc
// resolves this sibling declaration). Keep in lockstep with logic.mjs.

export declare const HOST_NAME: string;
export declare const PROTOCOL_VERSION: number;
export declare const MSG_LIST_TABS: string;
export declare const MSG_ACTIVATE: string;
export declare const MSG_TABS_CHANGED: string;
export declare const RECONNECT_MIN_MS: number;
export declare const RECONNECT_MAX_MS: number;
export declare const PUSH_DEBOUNCE_MS: number;

export interface WireTab {
  id: number;
  windowId: number;
  title: string;
  url: string;
  pinned: boolean;
  lastAccessed: number;
  active: boolean;
  favIconUrl: string;
}

export interface HostReply {
  id?: number;
  type?: string;
  ok?: boolean;
  error?: string;
  tabs?: WireTab[];
}

// The browser surface is deliberately untyped here: the production
// value is Firefox's `browser`, the tests hand in scripted fakes.
export declare function tabRow(tab: any): WireTab;
export declare function listTabs(browser: any): Promise<WireTab[]>;
export declare function nextReconnectDelay(prevMs: number): number;
export declare function handleMessage(browser: any, msg: unknown): Promise<HostReply>;

export interface ControllerOptions {
  setTimeout?: (fn: () => unknown, ms: number) => unknown;
  clearTimeout?: (handle: unknown) => void;
  log?: (msg: string) => void;
}

export interface Controller {
  start(): void;
  stop(): void;
  schedulePush(): void;
  readonly connected: boolean;
}

export declare function createController(browser: any, opts?: ControllerOptions): Controller;
