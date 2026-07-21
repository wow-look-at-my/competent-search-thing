// Frontend wiring: as-you-type search over the Go index with stale
// response dropping, a keyboard/mouse selection model, open/reveal
// actions, the async plugin pipeline (fire-and-forget QueryPlugins,
// "plugin:results" sections -- priority > 0 sections in the zone
// ABOVE the file rows, the rest below -- the bang-target chip, plugin
// action dispatch), the empty-query command cheat sheet (CheatSheet,
// rendered unselected), the query history (the bar summons empty;
// Up/Down recall committed queries -- see the history section below),
// and the runtime events the Go side emits (app:shown,
// index:progress, watch:degraded, watch:backend, theme:changed,
// stats:update -- the system stats row at the bottom edge). Rendering
// lives in render.ts (results) and stats.ts (the stats row's
// formatting); the selection-reconcile rules live pure in
// selection.ts; theme token/custom-css application lives in theme.ts;
// the opt-in preview pane lives in preview.ts (wired below through
// GetPreviewConfig + the selection/query hooks).

import { configModeActive, initConfig } from "./config";
import { initFileIcons } from "./fileicons/fileicons";
import { initFPSMeter } from "./fpsmeter";
import { initResize } from "./resize";
import {
  initPreview,
  previewOnQueryChange,
  previewOnSelectionChange,
} from "./preview";
import {
  applySelection,
  renderPluginSections,
  renderResults,
  splitByPriority,
} from "./render";
import type { PluginRowRef, PluginSection } from "./render";
import { reconcileSelection } from "./selection";
import { renderStats } from "./stats";
import type { StatsNodes } from "./stats";
import { initTheme } from "./theme";
import { shouldInterceptWheel } from "./wheel";

const SEARCH_DEBOUNCE_MS = 15;
const FLASH_COPIED_MS = 1200;
const FLASH_ERROR_MS = 2000;

const inputEl = document.getElementById("query") as HTMLInputElement;
const bangChipEl = document.getElementById("bang-chip") as HTMLSpanElement;
const resultsEl = document.getElementById("results") as HTMLDivElement;
const priorityResultsEl = document.getElementById(
  "priority-results",
) as HTMLDivElement;
const fileResultsEl = document.getElementById(
  "file-results",
) as HTMLDivElement;
const pluginResultsEl = document.getElementById(
  "plugin-results",
) as HTMLDivElement;
const emptyEl = document.getElementById("empty") as HTMLDivElement;
const statusTextEl = document.getElementById("status-text") as HTMLSpanElement;
const degradedChipEl = document.getElementById(
  "degraded-chip",
) as HTMLSpanElement;
const backendChipEl = document.getElementById(
  "backend-chip",
) as HTMLSpanElement;
const statsEl = document.getElementById("stats") as HTMLDivElement;
const statsNodes: StatsNodes = {
  cpu: document.getElementById("stat-cpu") as HTMLSpanElement,
  gpu: document.getElementById("stat-gpu") as HTMLSpanElement,
  ram: document.getElementById("stat-ram") as HTMLSpanElement,
  swap: document.getElementById("stat-swap") as HTMLSpanElement,
  net: document.getElementById("stat-net") as HTMLSpanElement,
};

// A selectable row: a file hit or one plugin result. The flat
// keyboard selection runs over the priority plugin rows first, then
// files, then the below-zone plugin rows -- the DOM order.
type SelectableItem =
  | { kind: "file"; file: WailsSearchResult }
  | { kind: "plugin"; pluginId: string; result: PluginResult };

interface UIState {
  items: SelectableItem[]; // combined selectables, parallel to rows
  rows: HTMLDivElement[]; // combined priority + file + plugin rows
  fileItems: WailsSearchResult[];
  fileRows: HTMLDivElement[];
  sections: PluginSection[]; // plugin emissions for the current seq
  priorityRefs: PluginRowRef[]; // rendered above the file rows
  priorityRows: HTMLDivElement[];
  pluginRefs: PluginRowRef[]; // rendered below the file rows
  pluginRows: HTMLDivElement[];
  selected: number;
  seq: number; // stale-response guard: only the newest generation renders
  visible: boolean; // mirrors the Go side; gates the blur auto-hide
  indexMsg: string; // last index build status, shown while idle
  query: string; // query of the current generation (empty-state check)
  // Whether the user navigated (arrows/Home/End) since the current
  // generation started: a late priority emission only steals the
  // auto-selection while this is false (see selection.ts). Mouse
  // hover is decorative (CSS :hover) and deliberately not counted.
  userNavigated: boolean;
  histEntries: string[]; // committed query history, oldest -> newest
  histCursor: number; // -1 = not browsing history; 0 = newest entry
}

const state: UIState = {
  items: [],
  rows: [],
  fileItems: [],
  fileRows: [],
  sections: [],
  priorityRefs: [],
  priorityRows: [],
  pluginRefs: [],
  pluginRows: [],
  selected: -1,
  seq: 0,
  visible: false,
  indexMsg: "",
  query: "",
  userNavigated: false,
  histEntries: [],
  histCursor: -1,
};

let debounceHandle: number | undefined;

function bindings(): WailsAppBindings | null {
  return window.go?.app.App ?? null;
}

/* --- status bar ---------------------------------------------------- */

let statusText = ""; // what setStatus last wrote (restored after a flash)
let flashing = false;
let flashHandle: number | undefined;

function setStatus(text: string): void {
  statusText = text;
  if (!flashing) {
    statusTextEl.textContent = text;
  }
}

// flashStatus overlays the status bar for ms milliseconds ("Copied",
// action errors), then restores whatever setStatus last wrote -- which
// may have changed while the flash was up.
function flashStatus(text: string, ms: number): void {
  flashing = true;
  statusTextEl.textContent = text;
  statusTextEl.classList.add("status-flash");
  window.clearTimeout(flashHandle);
  flashHandle = window.setTimeout(() => {
    flashing = false;
    statusTextEl.classList.remove("status-flash");
    statusTextEl.textContent = statusText;
  }, ms);
}

// refreshIdleStatus shows the index build message whenever there is no
// query to report on.
function refreshIdleStatus(): void {
  if (inputEl.value.trim() === "") {
    setStatus(state.indexMsg);
  }
}

/* --- stats row ------------------------------------------------------ */

// applyStats renders one stats snapshot into the bottom row -- or
// hides the whole row when the feature is off (stats.enabled false: the Go
// side sends enabled false). Per-metric dashes are stats.ts's job.
function applyStats(snap: StatsSnapshot | undefined): void {
  if (snap === undefined) {
    return; // malformed payload; keep whatever is on screen
  }
  if (!snap.enabled) {
    statsEl.hidden = true;
    return;
  }
  statsEl.hidden = false;
  renderStats(snap, statsNodes);
}

// refreshStats re-renders from the Go side's cached snapshot --
// instant Go-side (a mutex-guarded copy, never IO), so calling it on
// the show path costs nothing. Live updates then arrive as
// "stats:update" events while the bar stays visible.
function refreshStats(app: WailsAppBindings): void {
  app
    .GetStats()
    .then((snap) => {
      applyStats(snap);
    })
    .catch((err: unknown) => {
      console.warn("stats fetch failed: " + String(err));
    });
}

/* --- selection ------------------------------------------------------ */

// Row handlers resolve the flat index at EVENT time from the row
// element (rows.indexOf): a render-time index would go stale the
// moment a later-arriving priority section prepends rows above the
// file rows. indexOf over <= ~30 rows per event is free.
//
// TWO DISTINCT POINTER STATES (the hover-steals-selection field
// report): the ACTIVE selection (state.selected) moves ONLY through
// keyboard navigation and the auto-select/reconcile paths, and is the
// single source of truth for Enter, the pick report, and the preview
// pane. Mouse HOVER is a decorative CSS :hover wash (style.css) --
// no JS listener at all, so sweeping the cursor across the list can
// never change what Enter runs, mark the generation navigated, or
// retarget the preview. A CLICK is the explicit mouse choice: it
// activates the clicked row directly.
const rowHandlers = {
  onActivate: (row: HTMLDivElement, reveal: boolean) => {
    const i = state.rows.indexOf(row);
    if (i >= 0) {
      activate(i, reveal);
    }
  },
};

function select(index: number, scroll = true): void {
  state.selected = index;
  applySelection(state.rows, index, scroll);
  // The single selection choke point: every path (arrows, Home/End,
  // render reconciles, history recall, app:shown reset) funnels
  // through here, so the preview pane sees them all -- and hover
  // deliberately never lands here, so it can never retarget the
  // pane. A no-op while the pane is disabled.
  previewOnSelectionChange(state.items[index] ?? null);
}

function moveSelection(delta: number): void {
  const n = state.items.length;
  if (n === 0) {
    return;
  }
  state.userNavigated = true;
  if (state.selected < 0) {
    // Entering the list from no selection (the empty-query cheat
    // sheet): Down lands on the first row, Up on the last.
    select(delta > 0 ? 0 : n - 1);
    return;
  }
  select((((state.selected + delta) % n) + n) % n); // wraps both ways
}

// syncCombined rebuilds the flat selection model in DOM order --
// priority plugin rows, then file rows, then below-zone plugin rows
// -- after any area re-renders.
function syncCombined(): void {
  const items: SelectableItem[] = [];
  for (const ref of state.priorityRefs) {
    items.push({ kind: "plugin", pluginId: ref.pluginId, result: ref.result });
  }
  for (const file of state.fileItems) {
    items.push({ kind: "file", file });
  }
  for (const ref of state.pluginRefs) {
    items.push({ kind: "plugin", pluginId: ref.pluginId, result: ref.result });
  }
  state.items = items;
  state.rows = state.priorityRows.concat(state.fileRows, state.pluginRows);
}

// sameItem matches selectables by underlying identity, so a re-render
// can preserve the user's selection even when zone membership shifts
// the flat index (a late priority emission prepends rows above it).
function sameItem(a: SelectableItem, b: SelectableItem): boolean {
  if (a.kind === "file" && b.kind === "file") {
    return a.file === b.file;
  }
  if (a.kind === "plugin" && b.kind === "plugin") {
    return a.pluginId === b.pluginId && a.result === b.result;
  }
  return false;
}

// The "No matches" message shows only when a non-blank query produced
// neither file results nor plugin sections.
function updateEmptyState(): void {
  emptyEl.hidden =
    state.query.trim() === "" ||
    state.fileItems.length > 0 ||
    state.sections.length > 0;
}

// renderPluginArea re-renders BOTH plugin zones -- priority > 0
// sections above the file rows, the rest below -- and reconciles the
// flat selection with the new combined row set (selection.ts): a
// navigated user keeps their item by identity, an un-navigated one
// gets auto-select re-run on row 0, so a late-arriving apps section
// takes the selection Spotlight-style; the blank-query cheat sheet
// stays unselected.
function renderPluginArea(): void {
  const prevItem = state.items[state.selected] ?? null;
  const zones = splitByPriority(state.sections);
  const above = renderPluginSections(
    priorityResultsEl,
    zones.priority,
    rowHandlers,
  );
  state.priorityRows = above.rows;
  state.priorityRefs = above.refs;
  const below = renderPluginSections(
    pluginResultsEl,
    zones.normal,
    rowHandlers,
  );
  state.pluginRows = below.rows;
  state.pluginRefs = below.refs;
  syncCombined();
  updateEmptyState();
  const sel = reconcileSelection({
    prevItemIndex:
      prevItem === null
        ? -1
        : state.items.findIndex((it) => sameItem(it, prevItem)),
    prevSelected: state.selected,
    rowCount: state.rows.length,
    queryBlank: state.query.trim() === "",
    userNavigated: state.userNavigated,
  });
  select(sel, false); // a late emission must never move the viewport
}

/* --- query history ---------------------------------------------------- */

// THE RULE: Up recalls older history entries when the query is blank
// OR still exactly what a previous Up/Down recall filled in (you have
// not typed since). Down then moves forward; moving forward past the
// newest entry clears the bar back to the empty state (the cheat
// sheet). The moment you type or pick a completion, Up/Down go back
// to navigating the result list.
//
// histCursor is the position while browsing (-1 = not browsing, 0 =
// newest, older upward). Programmatic input writes fire no "input"
// event, so a recall keeps the cursor; real typing (and set_query
// completions) reset it to -1.

// historyEligible reports whether ArrowUp should step back through
// history instead of moving the list selection.
function historyEligible(): boolean {
  return inputEl.value.trim() === "" || state.histCursor >= 0;
}

// recallHistory replaces the input with the entry at histCursor --
// or restores the empty bar at -1 -- puts the caret at the end, and
// re-runs the normal pipeline, so the recalled query renders its
// results live (Enter then activates the selected row as usual).
function recallHistory(app: WailsAppBindings): void {
  const value =
    state.histCursor >= 0
      ? state.histEntries[state.histEntries.length - 1 - state.histCursor]
      : "";
  inputEl.value = value;
  inputEl.focus();
  inputEl.setSelectionRange(value.length, value.length);
  scheduleSearch(app);
}

// commitHistory records the query whose activation actually ran
// (fire-and-forget; the Go side trims and skips blanks too), then
// refetches the committed list so the next Up already sees it.
function commitHistory(app: WailsAppBindings): void {
  const query = state.query;
  if (query.trim() === "") {
    return; // blank queries are never history material
  }
  app
    .AddHistory(query)
    .then(() => {
      refreshHistory(app);
    })
    .catch((err: unknown) => {
      console.warn("history add failed: " + String(err));
    });
}

// refreshHistory pulls the committed history (oldest -> newest) and
// leaves browse mode, so the next Up starts from the newest entry.
function refreshHistory(app: WailsAppBindings): void {
  app
    .GetHistory()
    .then((entries) => {
      state.histEntries = entries ?? []; // tolerate a null payload
      state.histCursor = -1;
    })
    .catch((err: unknown) => {
      console.warn("history fetch failed: " + String(err));
    });
}

/* --- activation ------------------------------------------------------ */

// pickReport snapshots the ranking-telemetry report AT ACTIVATION
// TIME (the rendered flat list, the picked rank, the action kind), so
// a re-render racing the activation promise can never skew it. Row
// identities only -- the Go side joins the ranking signals from its
// own query ring and re-validates everything.
function pickReport(
  index: number,
  action: string,
  revealed: boolean,
): TelemetryPickReport {
  const shown: TelemetryShownRef[] = state.items.map((it) =>
    it.kind === "file"
      ? { kind: "file", path: it.file.path }
      : {
          kind: "plugin",
          plugin: it.pluginId,
          score: it.result.score ?? 0,
          title: it.result.title,
        },
  );
  return { query: state.query, shown, picked: { rank: index, action, revealed } };
}

// reportPick sends a snapshotted report after the activation actually
// ran. Fire-and-forget like commitHistory: a telemetry failure must
// never break (or even delay) an activation, and the call is a silent
// Go-side no-op unless search.telemetry opted in.
function reportPick(app: WailsAppBindings, report: TelemetryPickReport): void {
  app.RecordPick(report).catch((err: unknown) => {
    console.warn("telemetry pick report failed: " + String(err));
  });
}

function activate(index: number, reveal: boolean): void {
  const app = bindings();
  const item = state.items[index];
  if (app === null || item === undefined) {
    return;
  }
  if (item.kind === "file") {
    state.visible = false; // the Go side hides the bar on success
    const report = pickReport(index, reveal ? "reveal" : "open", reveal);
    const action = reveal
      ? app.Reveal(item.file.path)
      : app.Open(item.file.path);
    action
      .then(() => {
        commitHistory(app); // the activation actually ran
        reportPick(app, report);
      })
      .catch((err: unknown) => {
        state.visible = true;
        flashStatus(
          (reveal ? "reveal" : "open") + " failed: " + String(err),
          FLASH_ERROR_MS,
        );
      });
    return;
  }
  activatePlugin(app, index, item.pluginId, item.result);
}

// activatePlugin runs a plugin row's action (Ctrl/Cmd+Enter behaves
// like Enter here). set_query never reaches Go: it replaces the input
// locally and re-runs the normal debounced pipeline. Everything else
// goes through RunPluginAction, which re-validates and hides the bar
// itself per the action type -- copy_text and run_builtin "version"
// keep it open and flash "Copied".
function activatePlugin(
  app: WailsAppBindings,
  index: number,
  pluginId: string,
  result: PluginResult,
): void {
  const action = result.action;
  if (action === undefined) {
    return; // rows without an action are inert
  }
  if (action.type === "set_query") {
    setQueryLocal(app, action.value ?? "");
    return;
  }
  const report = pickReport(index, action.type, false);
  const copyFlash =
    action.type === "copy_text" ||
    (action.type === "run_builtin" && action.value === "version");
  // run_builtin "config" summons the in-app config editor: the Go
  // side keeps the bar up (showConfig), so the visible flag must not
  // be dropped -- but it earns no "Copied" flash either.
  const staysOpen =
    copyFlash || (action.type === "run_builtin" && action.value === "config");
  if (!staysOpen) {
    state.visible = false; // the Go side hides the bar on success
  }
  app
    .RunPluginAction(pluginId, action)
    .then(() => {
      if (copyFlash) {
        flashStatus("Copied", FLASH_COPIED_MS);
      }
      commitHistory(app); // the action actually ran
      reportPick(app, report);
    })
    .catch((err: unknown) => {
      state.visible = true;
      flashStatus(String(err), FLASH_ERROR_MS);
    });
}

// setQueryLocal replaces the input (bang-suggestion completion), puts
// the caret at the end, and re-runs the debounced search + plugin
// pipeline with a fresh generation. Picking a completion counts as
// typing: it exits history browse mode (set_query never commits).
function setQueryLocal(app: WailsAppBindings, value: string): void {
  state.histCursor = -1;
  inputEl.value = value;
  inputEl.focus();
  inputEl.setSelectionRange(value.length, value.length);
  scheduleSearch(app);
}

/* --- search + plugin pipeline ---------------------------------------- */

function runSearch(app: WailsAppBindings): void {
  const seq = ++state.seq;
  const query = inputEl.value;
  state.query = query;
  state.userNavigated = false; // navigation state is per-generation
  previewOnQueryChange(query); // no-op while the pane is disabled
  state.sections = []; // plugin sections are per-generation
  if (query.trim() === "") {
    fetchCheatSheet(app, seq);
  }
  const t0 = performance.now();
  app
    .Search(query)
    .then((items) => {
      if (seq !== state.seq) {
        return; // a newer query overtook this response
      }
      const ms = performance.now() - t0;
      state.fileItems = items;
      state.fileRows = renderResults(fileResultsEl, items, rowHandlers);
      renderPluginArea(); // re-render both plugin zones around the new file rows
      // Auto-select the first row only for a real query (the
      // empty-query cheat sheet stays unselected; Enter = no-op) --
      // and only while the user has not already navigated this
      // generation. Unlike the late-emission reconcile, a fresh
      // response scrolls the selection into view.
      if (!state.userNavigated) {
        select(query.trim() !== "" && state.rows.length > 0 ? 0 : -1);
      }
      if (query.trim() === "") {
        refreshIdleStatus();
      } else {
        const noun = items.length === 1 ? "result" : "results";
        setStatus(items.length + " " + noun + " -- " + ms.toFixed(1) + " ms");
      }
    })
    .catch((err: unknown) => {
      if (seq === state.seq) {
        setStatus("search error: " + String(err));
      }
    });
  // Fire-and-forget: file rendering NEVER waits on plugins. An empty
  // query is the Go-side cancel signal and must still be sent.
  queryPlugins(app, query, seq);
}

// fetchCheatSheet renders the bang command cheat sheet for an empty
// query -- the same list a bare "!" shows -- fetched synchronously
// from Go with NO plugin dispatch (QueryPlugins("") stays the
// cancel signal; no provider goroutines or subprocesses run). The
// answer is dropped once a newer generation took over or anything
// was typed, so the sheet vanishes the instant the query is
// non-empty.
function fetchCheatSheet(app: WailsAppBindings, seq: number): void {
  app
    .CheatSheet()
    .then((e) => {
      if (seq !== state.seq || inputEl.value.trim() !== "") {
        return; // typed past the empty state; the sheet no longer applies
      }
      const results = e.results ?? []; // tolerate a null payload
      if (results.length === 0) {
        return; // suggestions disabled or nothing registered
      }
      // The cheat sheet always renders in the classic below zone.
      state.sections = [
        { plugin: e.plugin, name: e.name, results, priority: 0 },
      ];
      renderPluginArea();
    })
    .catch((err: unknown) => {
      console.warn("cheat sheet failed: " + String(err));
    });
}

// queryPlugins asks Go to fan the query out to the matching providers
// (results arrive later via "plugin:results") and updates the bang
// chip from the synchronously computed target info.
function queryPlugins(app: WailsAppBindings, query: string, seq: number): void {
  app
    .QueryPlugins(query, seq)
    .then((target) => {
      if (seq !== state.seq) {
        return; // a newer generation owns the chip now
      }
      updateBangChip(target);
    })
    .catch((err: unknown) => {
      if (seq === state.seq) {
        updateBangChip(null);
      }
      console.warn("plugin query failed: " + String(err));
    });
}

// updateBangChip shows the bang-target display name in the query row,
// or hides the chip when the query is not targeted.
function updateBangChip(target: TargetInfo | null): void {
  if (target !== null && target.targeted) {
    bangChipEl.textContent = target.name;
    bangChipEl.hidden = false;
  } else {
    bangChipEl.textContent = "";
    bangChipEl.hidden = true;
  }
}

function scheduleSearch(app: WailsAppBindings): void {
  window.clearTimeout(debounceHandle);
  debounceHandle = window.setTimeout(() => {
    runSearch(app);
  }, SEARCH_DEBOUNCE_MS);
}

function hideBar(app: WailsAppBindings): void {
  state.visible = false;
  void app.Hide();
}

function onKeydown(app: WailsAppBindings, ev: KeyboardEvent): void {
  if (configModeActive()) {
    // config.ts owns the keys in editor mode (its own window handler
    // covers Esc and Ctrl+S; everything else keeps its default so
    // form controls behave like form controls).
    return;
  }
  switch (ev.key) {
    case "ArrowDown":
      ev.preventDefault();
      if (state.histCursor >= 0) {
        // Browsing history: Down moves forward; past the newest entry
        // (cursor -1) the bar clears back to the empty state.
        state.histCursor--;
        recallHistory(app);
        break;
      }
      moveSelection(1);
      break;
    case "ArrowUp":
      ev.preventDefault();
      if (historyEligible() && state.histCursor + 1 < state.histEntries.length) {
        state.histCursor++;
        recallHistory(app);
        break;
      }
      moveSelection(-1);
      break;
    case "Home":
      ev.preventDefault();
      if (state.items.length > 0) {
        state.userNavigated = true;
        select(0);
      }
      break;
    case "End":
      ev.preventDefault();
      if (state.items.length > 0) {
        state.userNavigated = true;
        select(state.items.length - 1);
      }
      break;
    case "Enter":
      ev.preventDefault();
      if (state.selected >= 0) {
        activate(state.selected, ev.ctrlKey || ev.metaKey);
      }
      break;
    case "Tab":
      // Tab/Shift+Tab are reserved for future use: the default focus
      // traversal would leave the input (the bar's only focusable
      // element) and the webview, tripping the blur -> Hide path.
      ev.preventDefault();
      break;
    case "Escape":
      ev.preventDefault();
      hideBar(app);
      break;
    default:
  }
}

function wireEvents(app: WailsAppBindings, rt: WailsRuntime): void {
  rt.EventsOn("app:shown", () => {
    state.visible = true;
    // The bar always summons empty: the pre-hide text is deliberately
    // dropped (press Up to get past searches back), and any history
    // browsing is reset. The pipeline re-run renders the empty-query
    // cheat sheet and doubles as the plugin cancel signal. This reset
    // runs even when the config editor is being RESTORED (the bar hid
    // while the editor was up -- config.ts keeps the mode; see its
    // app:shown handler): it keeps the search layer underneath fresh
    // for the eventual Esc-out. Only the focus steal is skipped --
    // the restored editor re-asserts its own focused control.
    inputEl.value = "";
    state.histCursor = -1;
    if (!configModeActive()) {
      inputEl.focus();
    }
    scheduleSearch(app);
    // Instant cached snapshot (the summon's fresh samples follow as
    // stats:update events moments later).
    refreshStats(app);
  });

  rt.EventsOn("stats:update", (...data: unknown[]) => {
    applyStats(data[0] as StatsSnapshot | undefined);
  });

  rt.EventsOn("index:progress", (...data: unknown[]) => {
    const p = data[0] as IndexProgressEvent | undefined;
    if (p === undefined) {
      return;
    }
    state.indexMsg = p.done
      ? p.indexed + " entries in " + p.seconds.toFixed(1) + "s"
      : "Indexing... " + p.indexed + " entries";
    refreshIdleStatus();
  });

  rt.EventsOn("watch:degraded", (...data: unknown[]) => {
    const d = data[0] as WatchDegradedEvent | undefined;
    degradedChipEl.hidden = false;
    if (d !== undefined) {
      degradedChipEl.title =
        "watched: " +
        d.watched +
        ", dropped watches: " +
        d.dropped +
        ", event overflows: " +
        d.overflows;
    }
  });

  // The one-time backend announcement: full coverage (fanotify on
  // Linux, fsevents on macOS) needs no notice; anything else keeps a
  // persistent chip up -- "Partial file watching" for the bounded
  // per-directory hot set (inotify/kqueue/windows), "File watching
  // off" for the none backend -- with the Go-side hint on hover.
  // Independent of the degraded chip: both can show.
  rt.EventsOn("watch:backend", (...data: unknown[]) => {
    const b = data[0] as WatchBackendEvent | undefined;
    if (b === undefined || b.full) {
      return;
    }
    backendChipEl.textContent =
      b.backend === "none" ? "File watching off" : "Partial file watching";
    backendChipEl.title = b.hint;
    backendChipEl.hidden = false;
  });

  rt.EventsOn("plugin:results", (...data: unknown[]) => {
    const e = data[0] as PluginEmission | undefined;
    if (e === undefined || e.gen !== state.seq) {
      return; // stale generation (or malformed payload)
    }
    const section: PluginSection = {
      plugin: e.plugin,
      name: e.name,
      results: e.results,
      priority: e.priority ?? 0, // omitempty: absent means 0
    };
    const at = state.sections.findIndex((s) => s.plugin === e.plugin);
    if (at >= 0) {
      state.sections[at] = section; // one emission per provider; replace
    } else {
      state.sections.push(section);
    }
    renderPluginArea();
  });
}

function wire(app: WailsAppBindings, rt: WailsRuntime): void {
  initTheme(app, rt);
  // The per-file-type icon table (fileicons.ts): fetched once from
  // the Go side, fire-and-forget like initTheme -- the bar starts
  // hidden, so the table installs long before the first file row
  // renders (and the matcher serves the pack defaults until then).
  void initFileIcons(app);
  app
    .GetPreviewConfig()
    .then((cfg) => {
      initPreview(app, rt, cfg);
    })
    .catch((err: unknown) => {
      console.warn("preview config fetch failed: " + String(err));
    });
  // The config editor mode (config.ts): wiring only -- the schema and
  // config document load lazily on the first "config:open".
  initConfig(app, rt);
  // Drag-edge window resizing (resize.ts): element-free document
  // listeners, so nothing here can interfere with wheel/hover/
  // selection handling above.
  initResize(app);
  inputEl.addEventListener("input", () => {
    state.histCursor = -1; // typing exits history browse mode
    scheduleSearch(app);
  });
  document.addEventListener("keydown", (ev: KeyboardEvent) => {
    onKeydown(app, ev);
  });
  // Own wheel input on the results list: WebKitGTK's default-on
  // smooth scrolling (no Wails knob) animates wheel deltas, and an
  // instant programmatic scroll cancels the animation mid-flight, so
  // fast detents lost distance. Applying deltas straight to
  // scrollTop interpolates nothing and can swallow nothing.
  // macOS-GATED (wheel.ts): a non-passive always-preventDefault wheel
  // listener forces WebKit's synchronous main-thread scroll path
  // there, pinning scroll motion to the (Low-Power-Mode-halvable)
  // rendering-update clock; with no listener macOS scrolls natively
  // on the async scrolling thread at display rate. Linux behavior is
  // byte-identical -- the listener registers exactly as before.
  if (shouldInterceptWheel(navigator.platform)) {
    resultsEl.addEventListener(
      "wheel",
      (ev: WheelEvent) => {
        if (ev.ctrlKey) {
          return; // leave zoom to the webview
        }
        ev.preventDefault(); // needs { passive: false } to stick
        let dy = ev.deltaY;
        if (ev.deltaMode === 1) {
          dy *= 40; // lines -> px (WebKitGTK sends pixels; defensive)
        } else if (ev.deltaMode === 2) {
          dy *= resultsEl.clientHeight; // pages -> px
        }
        resultsEl.scrollTop += dy;
      },
      { passive: false },
    );
  }
  window.addEventListener("blur", () => {
    // The blur auto-hide is suppressed in config mode: users alt-tab
    // away to check things mid-edit, and losing the editor (plus its
    // unsaved changes' visibility) on focus loss would be hostile.
    if (state.visible && !configModeActive()) {
      hideBar(app);
    }
  });
  wireEvents(app, rt);
  state.indexMsg = "ready";
  refreshIdleStatus();
  refreshHistory(app); // committed history from previous runs
  // Run the pipeline once at wire-up so the empty-query cheat sheet
  // is rendered before the first summon -- an app:shown emitted while
  // EventsOn registration was still in flight would otherwise be
  // missed and leave the bar blank until the first keystroke.
  scheduleSearch(app);
  // Same reasoning for the stats row: render it (or hide it when
  // disabled) before the first summon. Pre-first-summon the enabled
  // snapshot is all dashes -- the sampler has not run yet.
  refreshStats(app);
  // Dev-only fps meter (fpsmeter.ts): registers NOTHING unless
  // COMPETENT_SEARCH_FPS=1 -- the Go side answers the gate.
  initFPSMeter(app);
}

// window.go and window.runtime are injected by the Wails runtime
// shortly after page load; poll until both exist, then wire up.
function waitForBindings(): void {
  const app = bindings();
  const rt = window.runtime;
  if (app !== null && rt !== undefined) {
    wire(app, rt);
    return;
  }
  window.setTimeout(waitForBindings, 50);
}

inputEl.focus();
waitForBindings();

export {};
