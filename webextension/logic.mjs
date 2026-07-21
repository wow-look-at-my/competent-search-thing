// logic.mjs -- the pure, importable half of the companion extension.
// Everything here takes the `browser` WebExtension object (and timer
// seams) as parameters, so frontend/src/ffext-logic.test.ts drives it
// under vitest with scripted fakes; background.js is the only place
// with side effects. The constants below are a LOCKSTEP CONTRACT with
// internal/ffext (Go); internal/ffext/sync_test.go parses this file
// and fails the build on drift.

// HOST_NAME is the native-messaging host name passed to
// runtime.connectNative (internal/ffext HostName).
export const HOST_NAME = "competent_search_thing";

// PROTOCOL_VERSION is the bridge protocol version (internal/ffext
// ProtocolVersion).
export const PROTOCOL_VERSION = 1;

// The wire message types (internal/ffext MsgListTabs / MsgActivate /
// MsgTabsChanged).
export const MSG_LIST_TABS = "listTabs";
export const MSG_ACTIVATE = "activate";
export const MSG_TABS_CHANGED = "tabsChanged";

// Reconnect backoff bounds for a lost native port, and the debounce
// window coalescing tab-event bursts into one tabsChanged push.
export const RECONNECT_MIN_MS = 1000;
export const RECONNECT_MAX_MS = 30000;
export const PUSH_DEBOUNCE_MS = 500;

// tabRow projects one tabs.Tab onto the wire shape the Go bridge
// parses (internal/ffext wireTab). lastAccessed is rounded to integer
// milliseconds. favIconUrl is the tab's favicon (the "tabs" permission
// already grants it): an http(s) URL or a data: URI, "" when Firefox
// reports none -- the app feeds it to its favicon resolver so tab rows
// show the real site icon (unknown wire fields are ignored by older
// apps, the tolerance contract).
export function tabRow(tab) {
  return {
    id: tab.id,
    windowId: tab.windowId,
    title: tab.title ?? "",
    url: tab.url ?? "",
    pinned: !!tab.pinned,
    lastAccessed: Math.round(tab.lastAccessed ?? 0),
    active: !!tab.active,
    favIconUrl: tab.favIconUrl ?? "",
  };
}

// listTabs dumps every tab of every window as wire rows.
export async function listTabs(browser) {
  const tabs = await browser.tabs.query({});
  return tabs.map(tabRow);
}

// nextReconnectDelay is the capped exponential backoff decision: the
// first retry waits RECONNECT_MIN_MS, then doubles up to
// RECONNECT_MAX_MS.
export function nextReconnectDelay(prevMs) {
  if (!prevMs || prevMs < RECONNECT_MIN_MS) {
    return RECONNECT_MIN_MS;
  }
  return Math.min(prevMs * 2, RECONNECT_MAX_MS);
}

// handleMessage answers one bridge request. Activation is exactly two
// calls in the documented order: tabs.update(tabId, {active}) selects
// the tab inside its window WITHOUT focusing the window (MDN), then
// windows.update(windowId, {focused}) brings that window to the
// front. Any rejection (stale tab id, closed window) becomes an
// {ok:false, error} reply -- the app falls back to opening the URL.
export async function handleMessage(browser, msg) {
  if (!msg || typeof msg !== "object") {
    return { id: 0, ok: false, error: "malformed request" };
  }
  const id = typeof msg.id === "number" ? msg.id : 0;
  try {
    switch (msg.type) {
      case MSG_LIST_TABS:
        return { id, ok: true, tabs: await listTabs(browser) };
      case MSG_ACTIVATE:
        await browser.tabs.update(msg.tabId, { active: true });
        await browser.windows.update(msg.windowId, { focused: true });
        return { id, ok: true };
      default:
        return { id, ok: false, error: `unknown request type ${String(msg.type)}` };
    }
  } catch (err) {
    return { id, ok: false, error: String(err && err.message ? err.message : err) };
  }
}

// createController wires the whole background behavior around one
// persistent native port: connect (with capped-backoff reconnect on
// disconnect), answer bridge requests through handleMessage, and
// coalesce tab churn into debounced tabsChanged pushes so the app's
// snapshot stays warm. Timer seams (opts.setTimeout/clearTimeout) let
// tests drive time by hand.
export function createController(browser, opts = {}) {
  const setTimeoutFn = opts.setTimeout ?? ((fn, ms) => setTimeout(fn, ms));
  const clearTimeoutFn = opts.clearTimeout ?? ((h) => clearTimeout(h));
  const log = opts.log ?? (() => {});

  let port = null;
  let reconnectDelay = 0;
  let pushTimer = null;
  let stopped = false;

  function connect() {
    if (stopped) {
      return;
    }
    let p;
    try {
      p = browser.runtime.connectNative(HOST_NAME);
    } catch (err) {
      log(`connectNative failed: ${err}`);
      scheduleReconnect();
      return;
    }
    port = p;
    p.onMessage.addListener((msg) => respond(msg));
    p.onDisconnect.addListener((dp) => {
      port = null;
      const why = dp && dp.error && dp.error.message ? dp.error.message : "closed";
      log(`native port disconnected: ${why}`);
      scheduleReconnect();
    });
  }

  async function respond(msg) {
    // Traffic proves the link works; the next reconnect starts from
    // the minimum delay again.
    reconnectDelay = 0;
    post(await handleMessage(browser, msg));
  }

  function post(msg) {
    if (!port) {
      return;
    }
    try {
      port.postMessage(msg);
    } catch (err) {
      log(`postMessage failed: ${err}`);
    }
  }

  function scheduleReconnect() {
    if (stopped) {
      return;
    }
    reconnectDelay = nextReconnectDelay(reconnectDelay);
    setTimeoutFn(connect, reconnectDelay);
  }

  // schedulePush coalesces a burst of tab events into one push: the
  // first event arms the window, later ones ride it, and the dump
  // fires once when it elapses (bounded staleness, never starvation).
  function schedulePush() {
    if (stopped || pushTimer !== null) {
      return;
    }
    pushTimer = setTimeoutFn(() => {
      pushTimer = null;
      return pushNow();
    }, PUSH_DEBOUNCE_MS);
  }

  async function pushNow() {
    if (!port) {
      return;
    }
    try {
      post({ type: MSG_TABS_CHANGED, tabs: await listTabs(browser) });
    } catch (err) {
      log(`tab dump for push failed: ${err}`);
    }
  }

  function start() {
    for (const ev of ["onCreated", "onRemoved", "onUpdated", "onActivated", "onMoved"]) {
      const src = browser.tabs[ev];
      if (src && src.addListener) {
        src.addListener(() => schedulePush());
      }
    }
    connect();
  }

  function stop() {
    stopped = true;
    if (pushTimer !== null) {
      clearTimeoutFn(pushTimer);
      pushTimer = null;
    }
    if (port) {
      try {
        port.disconnect();
      } catch {
        // Already dead; nothing to release.
      }
      port = null;
    }
  }

  return {
    start,
    stop,
    schedulePush,
    get connected() {
      return port !== null;
    },
  };
}
