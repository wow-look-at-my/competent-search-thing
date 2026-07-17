// Frontend wiring: as-you-type search over the Go index with stale
// response dropping, a keyboard/mouse selection model, open/reveal
// actions, the async plugin pipeline (fire-and-forget QueryPlugins,
// "plugin:results" sections below the file rows, the bang-target
// chip, plugin action dispatch), and the runtime events the Go side
// emits (app:shown, index:progress, watch:degraded, theme:changed).
// Rendering lives in render.ts; theme token/custom-css application
// lives in theme.ts.

import {
  applySelection,
  renderPluginSections,
  renderResults,
} from "./render";
import type { PluginRowRef, PluginSection } from "./render";
import { initTheme } from "./theme";

const SEARCH_DEBOUNCE_MS = 15;
const FLASH_COPIED_MS = 1200;
const FLASH_ERROR_MS = 2000;

const inputEl = document.getElementById("query") as HTMLInputElement;
const bangChipEl = document.getElementById("bang-chip") as HTMLSpanElement;
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

// A selectable row: a file hit or one plugin result. The flat
// keyboard/hover selection runs over files first, then plugin rows.
type SelectableItem =
  | { kind: "file"; file: WailsSearchResult }
  | { kind: "plugin"; pluginId: string; result: PluginResult };

interface UIState {
  items: SelectableItem[]; // combined selectables, parallel to rows
  rows: HTMLDivElement[]; // combined file + plugin rows
  fileItems: WailsSearchResult[];
  fileRows: HTMLDivElement[];
  sections: PluginSection[]; // plugin emissions for the current seq
  pluginRefs: PluginRowRef[];
  pluginRows: HTMLDivElement[];
  selected: number;
  seq: number; // stale-response guard: only the newest generation renders
  visible: boolean; // mirrors the Go side; gates the blur auto-hide
  indexMsg: string; // last index build status, shown while idle
  query: string; // query of the current generation (empty-state check)
}

const state: UIState = {
  items: [],
  rows: [],
  fileItems: [],
  fileRows: [],
  sections: [],
  pluginRefs: [],
  pluginRows: [],
  selected: -1,
  seq: 0,
  visible: false,
  indexMsg: "",
  query: "",
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

/* --- selection ------------------------------------------------------ */

const rowHandlers = {
  onHover: select,
  onActivate: activate,
};

function select(index: number): void {
  state.selected = index;
  applySelection(state.rows, index);
}

function moveSelection(delta: number): void {
  const n = state.items.length;
  if (n === 0) {
    return;
  }
  select((((state.selected + delta) % n) + n) % n); // wraps both ways
}

// syncCombined rebuilds the flat selection model (file rows first,
// then plugin rows) after either area re-renders.
function syncCombined(): void {
  const items: SelectableItem[] = [];
  for (const file of state.fileItems) {
    items.push({ kind: "file", file });
  }
  for (const ref of state.pluginRefs) {
    items.push({ kind: "plugin", pluginId: ref.pluginId, result: ref.result });
  }
  state.items = items;
  state.rows = state.fileRows.concat(state.pluginRows);
}

// The "No matches" message shows only when a non-blank query produced
// neither file results nor plugin sections.
function updateEmptyState(): void {
  emptyEl.hidden =
    state.query.trim() === "" ||
    state.fileItems.length > 0 ||
    state.sections.length > 0;
}

// renderPluginArea re-renders the plugin sections below the file rows
// and reconciles the flat selection with the new combined row set. It
// runs after every file render too, so plugin row handler indices
// always offset from the file rows currently on screen.
function renderPluginArea(): void {
  const out = renderPluginSections(
    pluginResultsEl,
    state.sections,
    state.fileRows.length,
    rowHandlers,
  );
  state.pluginRows = out.rows;
  state.pluginRefs = out.refs;
  syncCombined();
  updateEmptyState();
  let sel = state.selected;
  if (sel >= state.rows.length) {
    sel = state.rows.length - 1; // the area shrank under the selection
  }
  if (sel < 0 && state.rows.length > 0) {
    sel = 0; // first content to arrive takes the selection
  }
  select(sel);
}

/* --- activation ------------------------------------------------------ */

function activate(index: number, reveal: boolean): void {
  const app = bindings();
  const item = state.items[index];
  if (app === null || item === undefined) {
    return;
  }
  if (item.kind === "file") {
    state.visible = false; // the Go side hides the bar on success
    const action = reveal
      ? app.Reveal(item.file.path)
      : app.Open(item.file.path);
    action.catch((err: unknown) => {
      state.visible = true;
      flashStatus(
        (reveal ? "reveal" : "open") + " failed: " + String(err),
        FLASH_ERROR_MS,
      );
    });
    return;
  }
  activatePlugin(app, item.pluginId, item.result);
}

// activatePlugin runs a plugin row's action (Ctrl/Cmd+Enter behaves
// like Enter here). set_query never reaches Go: it replaces the input
// locally and re-runs the normal debounced pipeline. Everything else
// goes through RunPluginAction, which re-validates and hides the bar
// itself per the action type -- copy_text and run_builtin "version"
// keep it open and flash "Copied".
function activatePlugin(
  app: WailsAppBindings,
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
  const staysOpen =
    action.type === "copy_text" ||
    (action.type === "run_builtin" && action.value === "version");
  if (!staysOpen) {
    state.visible = false; // the Go side hides the bar on success
  }
  app
    .RunPluginAction(pluginId, action)
    .then(() => {
      if (staysOpen) {
        flashStatus("Copied", FLASH_COPIED_MS);
      }
    })
    .catch((err: unknown) => {
      state.visible = true;
      flashStatus(String(err), FLASH_ERROR_MS);
    });
}

// setQueryLocal replaces the input (bang-suggestion completion), puts
// the caret at the end, and re-runs the debounced search + plugin
// pipeline with a fresh generation.
function setQueryLocal(app: WailsAppBindings, value: string): void {
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
  state.sections = []; // plugin sections are per-generation
  const t0 = performance.now();
  app
    .Search(query)
    .then((items) => {
      if (seq !== state.seq) {
        return; // a newer query overtook this response
      }
      const ms = performance.now() - t0;
      state.fileItems = items;
      state.fileRows = renderResults(fileResultsEl, items, query, rowHandlers);
      renderPluginArea(); // re-offset plugin rows below the new file rows
      select(state.rows.length > 0 ? 0 : -1);
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
  switch (ev.key) {
    case "ArrowDown":
      ev.preventDefault();
      moveSelection(1);
      break;
    case "ArrowUp":
      ev.preventDefault();
      moveSelection(-1);
      break;
    case "Home":
      ev.preventDefault();
      if (state.items.length > 0) {
        select(0);
      }
      break;
    case "End":
      ev.preventDefault();
      if (state.items.length > 0) {
        select(state.items.length - 1);
      }
      break;
    case "Enter":
      ev.preventDefault();
      if (state.selected >= 0) {
        activate(state.selected, ev.ctrlKey || ev.metaKey);
      }
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
    inputEl.focus();
    inputEl.select();
    // The index may have changed while hidden; refresh the results
    // (plugins re-query naturally through the same path).
    scheduleSearch(app);
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

  rt.EventsOn("plugin:results", (...data: unknown[]) => {
    const e = data[0] as PluginEmission | undefined;
    if (e === undefined || e.gen !== state.seq) {
      return; // stale generation (or malformed payload)
    }
    const section: PluginSection = {
      plugin: e.plugin,
      name: e.name,
      results: e.results,
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
  inputEl.addEventListener("input", () => {
    scheduleSearch(app);
  });
  document.addEventListener("keydown", (ev: KeyboardEvent) => {
    onKeydown(app, ev);
  });
  window.addEventListener("blur", () => {
    if (state.visible) {
      hideBar(app);
    }
  });
  wireEvents(app, rt);
  state.indexMsg = "ready";
  refreshIdleStatus();
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
