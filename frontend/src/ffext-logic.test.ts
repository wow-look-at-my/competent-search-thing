// The companion-extension logic gate: drives webextension/logic.mjs
// (the pure half of the Firefox tab-activation extension) with
// scripted browser/timer fakes. Runs in the same vitest suite as the
// DOM-ordering tests; internal/ffext/sync_test.go pins the constants
// against the Go side.
import { describe, expect, it } from "vitest";
import {
  HOST_NAME,
  MSG_ACTIVATE,
  MSG_LIST_TABS,
  MSG_TABS_CHANGED,
  PROTOCOL_VERSION,
  PUSH_DEBOUNCE_MS,
  RECONNECT_MAX_MS,
  RECONNECT_MIN_MS,
  createController,
  handleMessage,
  nextReconnectDelay,
  tabRow,
} from "../../webextension/logic.mjs";

// fakeBrowser scripts the WebExtension API surface logic.mjs touches.
interface FakeCall {
  what: string;
  args: unknown[];
}

function fakeBrowser(opts?: {
  tabs?: Array<Record<string, unknown>>;
  updateError?: string;
  connectError?: string;
}) {
  const calls: FakeCall[] = [];
  const listeners: Record<string, Array<(...a: unknown[]) => unknown>> = {};
  const ports: FakePort[] = [];

  class FakePort {
    posted: unknown[] = [];
    messageListeners: Array<(msg: unknown) => unknown> = [];
    disconnectListeners: Array<(p: unknown) => unknown> = [];
    onMessage = {
      addListener: (fn: (msg: unknown) => unknown) => {
        this.messageListeners.push(fn);
      },
    };
    onDisconnect = {
      addListener: (fn: (p: unknown) => unknown) => {
        this.disconnectListeners.push(fn);
      },
    };
    error: { message: string } | null = null;
    postMessage(msg: unknown) {
      this.posted.push(msg);
    }
    disconnect() {
      calls.push({ what: "port.disconnect", args: [] });
    }
    // Test drivers: deliver an inbound message / kill the port.
    async deliver(msg: unknown): Promise<void> {
      for (const fn of this.messageListeners) {
        await fn(msg);
      }
    }
    drop(message?: string) {
      this.error = message ? { message } : null;
      for (const fn of this.disconnectListeners) {
        fn(this);
      }
    }
  }

  const eventSource = () => ({
    addListener: (fn: (...a: unknown[]) => unknown) => {
      (listeners[name] ??= []).push(fn);
    },
  });
  let name = "";
  const tabEvent = (n: string) => {
    name = n;
    return eventSource();
  };

  const browser = {
    runtime: {
      connectNative: (host: string) => {
        calls.push({ what: "connectNative", args: [host] });
        if (opts?.connectError) {
          throw new Error(opts.connectError);
        }
        const p = new FakePort();
        ports.push(p);
        return p;
      },
    },
    tabs: {
      query: async (q: unknown) => {
        calls.push({ what: "tabs.query", args: [q] });
        return opts?.tabs ?? [];
      },
      update: async (tabId: number, props: unknown) => {
        calls.push({ what: "tabs.update", args: [tabId, props] });
        if (opts?.updateError) {
          throw new Error(opts.updateError);
        }
      },
      onCreated: tabEvent("onCreated"),
      onRemoved: tabEvent("onRemoved"),
      onUpdated: tabEvent("onUpdated"),
      onActivated: tabEvent("onActivated"),
      onMoved: tabEvent("onMoved"),
    },
    windows: {
      update: async (windowId: number, props: unknown) => {
        calls.push({ what: "windows.update", args: [windowId, props] });
      },
    },
  };
  return { browser, calls, listeners, ports };
}

// fakeTimers captures scheduled callbacks so tests fire them by hand.
function fakeTimers() {
  const scheduled: Array<{ fn: () => unknown; ms: number; cleared: boolean }> = [];
  return {
    scheduled,
    setTimeout: (fn: () => unknown, ms: number) => {
      scheduled.push({ fn, ms, cleared: false });
      return scheduled.length; // 1-based truthy handle
    },
    clearTimeout: (h: unknown) => {
      const slot = scheduled[(h as number) - 1];
      if (slot) {
        slot.cleared = true;
      }
    },
    async fire(i: number): Promise<void> {
      await scheduled[i].fn();
    },
  };
}

describe("constants", () => {
  it("carry the lockstep contract values", () => {
    expect(HOST_NAME).toBe("competent_search_thing");
    expect(PROTOCOL_VERSION).toBe(1);
    expect(MSG_LIST_TABS).toBe("listTabs");
    expect(MSG_ACTIVATE).toBe("activate");
    expect(MSG_TABS_CHANGED).toBe("tabsChanged");
  });
});

describe("tabRow", () => {
  it("projects a tabs.Tab onto the wire shape", () => {
    expect(
      tabRow({
        id: 7,
        windowId: 3,
        title: "T",
        url: "https://t.example/",
        pinned: true,
        lastAccessed: 1721456789123.75,
        active: false,
        favIconUrl: "https://t.example/favicon.ico",
        cookieStoreId: "ignored-extra-field",
      }),
    ).toEqual({
      id: 7,
      windowId: 3,
      title: "T",
      url: "https://t.example/",
      pinned: true,
      lastAccessed: 1721456789124,
      active: false,
      favIconUrl: "https://t.example/favicon.ico",
    });
  });

  it("defaults absent optional fields", () => {
    expect(tabRow({ id: 1, windowId: 2 })).toEqual({
      id: 1,
      windowId: 2,
      title: "",
      url: "",
      pinned: false,
      lastAccessed: 0,
      active: false,
      favIconUrl: "",
    });
  });
});

describe("handleMessage", () => {
  it("answers listTabs with the full wire dump", async () => {
    const { browser } = fakeBrowser({
      tabs: [
        { id: 1, windowId: 9, title: "A", url: "https://a.example/", pinned: false },
        { id: 2, windowId: 9, title: "B", url: "https://b.example/", pinned: true },
      ],
    });
    const reply = await handleMessage(browser, { id: 41, type: MSG_LIST_TABS });
    expect(reply.id).toBe(41);
    expect(reply.ok).toBe(true);
    expect(reply.tabs).toHaveLength(2);
    expect(reply.tabs?.[1]).toMatchObject({ id: 2, pinned: true });
  });

  it("activates the tab THEN focuses its window", async () => {
    const { browser, calls } = fakeBrowser();
    const reply = await handleMessage(browser, {
      id: 5,
      type: MSG_ACTIVATE,
      tabId: 42,
      windowId: 7,
    });
    expect(reply).toEqual({ id: 5, ok: true });
    const relevant = calls.filter((c) => c.what !== "connectNative");
    expect(relevant).toEqual([
      { what: "tabs.update", args: [42, { active: true }] },
      { what: "windows.update", args: [7, { focused: true }] },
    ]);
  });

  it("maps a stale tab id to ok:false without touching the window", async () => {
    const { browser, calls } = fakeBrowser({ updateError: "Invalid tab ID: 42" });
    const reply = await handleMessage(browser, {
      id: 6,
      type: MSG_ACTIVATE,
      tabId: 42,
      windowId: 7,
    });
    expect(reply.ok).toBe(false);
    expect(reply.error).toContain("Invalid tab ID");
    expect(calls.some((c) => c.what === "windows.update")).toBe(false);
  });

  it("rejects unknown and malformed requests", async () => {
    const { browser } = fakeBrowser();
    expect((await handleMessage(browser, { id: 1, type: "explode" })).ok).toBe(false);
    expect((await handleMessage(browser, null)).ok).toBe(false);
    expect((await handleMessage(browser, "listTabs")).ok).toBe(false);
  });
});

describe("nextReconnectDelay", () => {
  it("starts at the minimum and doubles to the cap", () => {
    expect(nextReconnectDelay(0)).toBe(RECONNECT_MIN_MS);
    expect(nextReconnectDelay(RECONNECT_MIN_MS)).toBe(RECONNECT_MIN_MS * 2);
    expect(nextReconnectDelay(RECONNECT_MAX_MS / 2)).toBe(RECONNECT_MAX_MS);
    expect(nextReconnectDelay(RECONNECT_MAX_MS)).toBe(RECONNECT_MAX_MS);
    expect(nextReconnectDelay(-5)).toBe(RECONNECT_MIN_MS);
  });
});

describe("createController", () => {
  it("connects to the named host and answers requests over the port", async () => {
    const fb = fakeBrowser({ tabs: [{ id: 1, windowId: 2, title: "T", url: "https://t.example/" }] });
    const timers = fakeTimers();
    const ctl = createController(fb.browser, timers);
    ctl.start();
    expect(fb.calls[0]).toEqual({ what: "connectNative", args: [HOST_NAME] });
    expect(ctl.connected).toBe(true);

    const port = fb.ports[0];
    await port.deliver({ id: 3, type: MSG_LIST_TABS });
    expect(port.posted).toHaveLength(1);
    expect(port.posted[0]).toMatchObject({ id: 3, ok: true });
  });

  it("reconnects with growing backoff after disconnects", () => {
    const fb = fakeBrowser();
    const timers = fakeTimers();
    const ctl = createController(fb.browser, timers);
    ctl.start();

    fb.ports[0].drop("No such native application competent_search_thing");
    expect(ctl.connected).toBe(false);
    expect(timers.scheduled).toHaveLength(1);
    expect(timers.scheduled[0].ms).toBe(RECONNECT_MIN_MS);

    // The retry connects; a second immediate death backs off further.
    void timers.fire(0);
    expect(fb.ports).toHaveLength(2);
    fb.ports[1].drop();
    expect(timers.scheduled).toHaveLength(2);
    expect(timers.scheduled[1].ms).toBe(RECONNECT_MIN_MS * 2);
  });

  it("resets the backoff once traffic flows", async () => {
    const fb = fakeBrowser();
    const timers = fakeTimers();
    const ctl = createController(fb.browser, timers);
    ctl.start();
    fb.ports[0].drop();
    await timers.fire(0);
    // Traffic on the new port proves the link.
    await fb.ports[1].deliver({ id: 1, type: MSG_LIST_TABS });
    fb.ports[1].drop();
    expect(timers.scheduled[1].ms).toBe(RECONNECT_MIN_MS);
    void ctl;
  });

  it("schedules a reconnect when connectNative itself throws", () => {
    const fb = fakeBrowser({ connectError: "denied" });
    const timers = fakeTimers();
    createController(fb.browser, timers).start();
    expect(timers.scheduled).toHaveLength(1);
    expect(timers.scheduled[0].ms).toBe(RECONNECT_MIN_MS);
  });

  it("debounces tab-event bursts into one tabsChanged push", async () => {
    const fb = fakeBrowser({ tabs: [{ id: 1, windowId: 1, title: "A", url: "https://a.example/" }] });
    const timers = fakeTimers();
    const ctl = createController(fb.browser, timers);
    ctl.start();
    const port = fb.ports[0];

    // A burst of events arms exactly one push timer.
    for (const fns of Object.values(fb.listeners)) {
      for (const fn of fns) {
        fn();
      }
    }
    ctl.schedulePush();
    const pushTimers = timers.scheduled.filter((s) => s.ms === PUSH_DEBOUNCE_MS);
    expect(pushTimers).toHaveLength(1);

    await timers.fire(timers.scheduled.indexOf(pushTimers[0]));
    const pushes = port.posted.filter(
      (m) => (m as { type?: string }).type === MSG_TABS_CHANGED,
    );
    expect(pushes).toHaveLength(1);
    expect((pushes[0] as { tabs: unknown[] }).tabs).toHaveLength(1);

    // The window elapsed; the next event arms a fresh timer.
    ctl.schedulePush();
    expect(timers.scheduled.filter((s) => s.ms === PUSH_DEBOUNCE_MS)).toHaveLength(2);
  });

  it("drops the push when the port is gone and stops cleanly", async () => {
    const fb = fakeBrowser();
    const timers = fakeTimers();
    const ctl = createController(fb.browser, timers);
    ctl.start();
    const port = fb.ports[0];
    ctl.schedulePush();
    port.drop();
    const pushSlot = timers.scheduled.findIndex((s) => s.ms === PUSH_DEBOUNCE_MS);
    await timers.fire(pushSlot);
    expect(port.posted).toHaveLength(0);

    ctl.stop();
    ctl.schedulePush(); // stopped: arms nothing
    expect(timers.scheduled.filter((s) => s.ms === PUSH_DEBOUNCE_MS)).toHaveLength(1);
  });

  it("stop disconnects a live port and halts reconnects", () => {
    const fb = fakeBrowser();
    const timers = fakeTimers();
    const ctl = createController(fb.browser, timers);
    ctl.start();
    ctl.stop();
    expect(fb.calls.some((c) => c.what === "port.disconnect")).toBe(true);
    // A late disconnect event must not schedule a reconnect.
    fb.ports[0].drop();
    expect(timers.scheduled).toHaveLength(0);
  });
});
