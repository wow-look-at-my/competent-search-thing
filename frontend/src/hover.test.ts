// The hover-vs-selection gate (the hover-steals-selection field
// report): mouse hover is DECORATIVE -- a CSS :hover wash with no JS
// listener -- while the ACTIVE selection moves only through keyboard
// navigation and the auto-select paths and alone drives Enter, the
// pick report, and the preview pane; a click activates the clicked
// row explicitly. This suite drives the REAL main.ts in jsdom over
// faked Wails bindings (test-setup.ts already loaded the real
// index.html body): hover must change nothing observable, Enter must
// run the keyboard-selected row, click must run the clicked row.

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { beforeAll, describe, expect, it, vi } from "vitest";

const opened: string[] = [];
const picks: TelemetryPickReport[] = [];
const previewCalls: PreviewTarget[] = [];

const files: WailsSearchResult[] = [
  { path: "/tmp/alpha.txt", name: "alpha.txt", isDir: false },
  { path: "/tmp/beta.txt", name: "beta.txt", isDir: false },
  { path: "/tmp/gamma.txt", name: "gamma.txt", isDir: false },
];

// A full WailsAppBindings fake (typed, so binding drift fails here):
// Search answers the fixed file list, activations and preview
// dispatches are recorded, everything else resolves inertly. The
// preview pane is ENABLED so QueryPreview records which row the pane
// tracks -- the observable for "hover never retargets the preview".
const fakeApp: WailsAppBindings = {
  Search: () => Promise.resolve(files),
  Open: (path) => {
    opened.push(path);
    return Promise.resolve();
  },
  Reveal: (path) => {
    opened.push("reveal:" + path);
    return Promise.resolve();
  },
  Hide: () => Promise.resolve(),
  QueryPlugins: () =>
    Promise.resolve({ targeted: false, plugin: "", name: "", bang: "" }),
  RunPluginAction: () => Promise.resolve(),
  CheatSheet: () =>
    Promise.resolve({ plugin: "", name: "", gen: 0, results: [] }),
  GetHistory: () => Promise.resolve([]),
  AddHistory: () => Promise.resolve(),
  ResolveIcons: () => Promise.resolve({}),
  RecordPick: (report) => {
    picks.push(report);
    return Promise.resolve();
  },
  FPSEnabled: () => Promise.resolve(false),
  RecordFPSSample: () => Promise.resolve(),
  GetTheme: () => Promise.resolve({}),
  GetCustomCSS: () => Promise.resolve(""),
  GetStats: () =>
    Promise.resolve({
      enabled: false,
      cpuPct: 0,
      cpuOk: false,
      gpuPct: 0,
      gpuOk: false,
      memUsed: 0,
      memTotal: 0,
      memOk: false,
      swapUsed: 0,
      swapTotal: 0,
      swapOk: false,
      netRxBps: 0,
      netTxBps: 0,
      netOk: false,
    }),
  QueryPreview: (target) => {
    previewCalls.push(target);
    return Promise.resolve();
  },
  GetPreviewConfig: () =>
    Promise.resolve({
      enabled: true,
      kagiConfigured: false,
      openaiConfigured: false,
      resultsWidth: 680,
    }),
  FetchWebPreview: () => Promise.resolve(),
  FetchAIPreview: () => Promise.resolve(),
  GetConfigSchema: () => Promise.resolve("{}"),
  GetConfigForEdit: () => Promise.resolve({ configJson: "{}", path: "" }),
  SaveConfig: () =>
    Promise.resolve({ ok: true, applied: null, pending: null }),
  OpenConfigFile: () => Promise.resolve(),
};

const fakeRuntime: WailsRuntime = {
  EventsOn: () => () => {},
  EventsOff: () => {},
};

function key(k: string): void {
  document.dispatchEvent(new KeyboardEvent("keydown", { key: k }));
}

// runQuery types a query, runs the debounced pipeline to completion
// (fake timers), and returns the rendered file rows.
async function runQuery(q: string): Promise<HTMLDivElement[]> {
  const input = document.getElementById("query") as HTMLInputElement;
  input.value = q;
  input.dispatchEvent(new Event("input"));
  // Search debounce (15ms) + preview debounce (90ms) + slack.
  await vi.advanceTimersByTimeAsync(300);
  const fileEl = document.getElementById("file-results") as HTMLDivElement;
  return [...fileEl.querySelectorAll<HTMLDivElement>(".result")];
}

function selectedIndex(rows: HTMLDivElement[]): number {
  return rows.findIndex((r) => r.classList.contains("selected"));
}

beforeAll(async () => {
  vi.useFakeTimers();
  // jsdom implements no scrollIntoView; keyboard selection calls it.
  Element.prototype.scrollIntoView = () => {};
  window.go = { app: { App: fakeApp } };
  window.runtime = fakeRuntime;
  // main.ts wires synchronously at import (bindings already present),
  // arming the initial empty-query pipeline; run it to completion so
  // every test starts from the settled wired state.
  await import("./main");
  await vi.advanceTimersByTimeAsync(500);
});

describe("hover is decorative only", () => {
  it("hover changes no state: active index and preview target stay put", async () => {
    const rows = await runQuery("alp");
    expect(rows).toHaveLength(3);
    expect(selectedIndex(rows)).toBe(0); // auto-select on a real query
    previewCalls.length = 0;

    rows[2].dispatchEvent(new MouseEvent("mouseenter"));
    rows[2].dispatchEvent(new MouseEvent("mouseover", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(300);

    expect(selectedIndex(rows)).toBe(0); // the active selection never moved
    expect(previewCalls).toHaveLength(0); // and the pane was not retargeted
  });

  it("preview follows keyboard navigation, never hover", async () => {
    const rows = await runQuery("alp");
    previewCalls.length = 0;

    rows[2].dispatchEvent(new MouseEvent("mouseenter"));
    await vi.advanceTimersByTimeAsync(300);
    expect(previewCalls).toHaveLength(0);

    key("ArrowDown"); // row 0 -> row 1
    await vi.advanceTimersByTimeAsync(300);
    expect(selectedIndex(rows)).toBe(1);
    expect(previewCalls).toHaveLength(1);
    expect(previewCalls[0]).toEqual({
      kind: "file",
      path: "/tmp/beta.txt",
      isDir: false,
    });
  });

  it("hover-then-Enter runs and records the KEYBOARD-selected row", async () => {
    const rows = await runQuery("alp");
    key("ArrowDown"); // the user arrows onto beta.txt
    await vi.advanceTimersByTimeAsync(100);
    expect(selectedIndex(rows)).toBe(1);

    opened.length = 0;
    picks.length = 0;
    rows[2].dispatchEvent(new MouseEvent("mouseenter")); // pointer rests on gamma
    key("Enter");
    await vi.advanceTimersByTimeAsync(100);

    expect(opened).toEqual(["/tmp/beta.txt"]); // the keyboard row ran
    expect(picks).toHaveLength(1);
    expect(picks[0].picked).toEqual({ rank: 1, action: "open", revealed: false });
    expect(picks[0].shown[1]).toEqual({ kind: "file", path: "/tmp/beta.txt" });
  });

  it("click activates the CLICKED row (the explicit mouse choice)", async () => {
    const rows = await runQuery("alp");
    expect(selectedIndex(rows)).toBe(0);

    opened.length = 0;
    picks.length = 0;
    rows[2].dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(100);

    expect(opened).toEqual(["/tmp/gamma.txt"]);
    expect(picks).toHaveLength(1);
    expect(picks[0].picked.rank).toBe(2);
  });

  it("style.css carries the decorative hover wash, distinct from .selected", () => {
    const css = readFileSync(
      join(dirname(fileURLToPath(import.meta.url)), "style.css"),
      "utf-8",
    );
    // The wash exists, guards out the selected row, and derives from
    // the selection token (weaker mix), so both themes stay legible.
    expect(css).toMatch(
      /\.result:not\(\.selected\):hover\s*\{[^}]*background:\s*color-mix\(in srgb, var\(--sb-selection-bg\)/,
    );
  });
});
