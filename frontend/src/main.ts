// Frontend wiring: as-you-type search over the Go index with stale
// response dropping, a keyboard/mouse selection model, open/reveal
// actions, and the runtime events the Go side emits (app:shown,
// index:progress, watch:degraded, theme:changed). Rendering lives in
// render.ts; theme token/custom-css application lives in theme.ts.

import { applySelection, renderResults } from "./render";
import { initTheme } from "./theme";

const SEARCH_DEBOUNCE_MS = 15;

const inputEl = document.getElementById("query") as HTMLInputElement;
const resultsEl = document.getElementById("results") as HTMLDivElement;
const statusTextEl = document.getElementById("status-text") as HTMLSpanElement;
const degradedChipEl = document.getElementById(
  "degraded-chip",
) as HTMLSpanElement;

interface UIState {
  items: WailsSearchResult[];
  rows: HTMLDivElement[];
  selected: number;
  seq: number; // stale-response guard: only the newest search renders
  visible: boolean; // mirrors the Go side; gates the blur auto-hide
  indexMsg: string; // last index build status, shown while idle
}

const state: UIState = {
  items: [],
  rows: [],
  selected: -1,
  seq: 0,
  visible: false,
  indexMsg: "",
};

let debounceHandle: number | undefined;

function bindings(): WailsAppBindings | null {
  return window.go?.app.App ?? null;
}

function setStatus(text: string): void {
  statusTextEl.textContent = text;
}

// refreshIdleStatus shows the index build message whenever there is no
// query to report on.
function refreshIdleStatus(): void {
  if (inputEl.value.trim() === "") {
    setStatus(state.indexMsg);
  }
}

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

function activate(index: number, reveal: boolean): void {
  const app = bindings();
  const item = state.items[index];
  if (app === null || item === undefined) {
    return;
  }
  state.visible = false; // the Go side hides the bar on success
  const action = reveal ? app.Reveal(item.path) : app.Open(item.path);
  action.catch((err: unknown) => {
    state.visible = true;
    setStatus((reveal ? "reveal" : "open") + " failed: " + String(err));
  });
}

function runSearch(app: WailsAppBindings): void {
  const seq = ++state.seq;
  const query = inputEl.value;
  const t0 = performance.now();
  app
    .Search(query)
    .then((items) => {
      if (seq !== state.seq) {
        return; // a newer query overtook this response
      }
      const ms = performance.now() - t0;
      state.items = items;
      state.rows = renderResults(resultsEl, items, query, {
        onHover: select,
        onActivate: activate,
      });
      select(items.length > 0 ? 0 : -1);
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
    // The index may have changed while hidden; refresh the results.
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
