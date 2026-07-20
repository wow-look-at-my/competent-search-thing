// Config editor mode: a schema-driven settings UI over the single bar
// window, entered via the Go side's "config:open" event (the `config`
// CLI/IPC command, the !config builtin, and the tray item all funnel
// there; a cold-start `competent-search-thing config` emits it at
// DomReady, where a missed event -- the app:shown race -- degrades to
// the normal bar). The editor renders ENTIRELY from the embedded
// config.schema.json (GetConfigSchema) over a working copy of the
// current configuration (GetConfigForEdit): leaves become typed
// controls (toggle/select/number/text, password for the SECRET-marked
// API keys), string arrays a one-per-line list, string->string maps a
// key/value row editor, and any other shape a raw-JSON sub-editor --
// nothing here hard-codes today's key list beyond those display
// choices. A schema node carrying "x-editor-hidden": true (checked on
// the property node AND its resolved $ref target) is skipped -- leaf
// or whole subtree -- at every depth; that annotation, not a
// hard-coded list, is how app-managed keys (rootsVersion, "$schema")
// stay out of the editor. LAYOUT (the VS Code settings pattern): a
// left ToC sidebar built from the SAME schema walk that renders the
// controls -- one entry per top-level section in schema order,
// indented sub-entries for nested object sections; top-level LEAF
// settings group under a synthetic "General" section (leading run;
// a leaf appearing after the first real section gets its own group
// named after itself, so schema order is never reshuffled). Entries
// are buttons (Tab reaches them between the filter and the controls;
// Enter/Space jumps): a click scrolls the controls column to the
// section INSTANTLY (behavior "auto" -- WebKitGTK's smooth-scroll
// animator is the thing main.ts's wheel handler already fights), and
// scrolling the controls column highlights the entry whose section
// sits at the viewport top (toc.ts activeSectionIndex over measured
// offsets -- the pure math is vitest-pinned). The global filter dims
// zero-match ToC entries and shows per-section match counts; clicking
// a dimmed (filtered-out) entry clears the filter first, then jumps.
// Save round-trips through SaveConfig (Go owns validation;
// schema min/max reach the inputs as UX only) and the result --
// applied sections, the honest per-knob next-launch notes, apply
// errors -- is surfaced verbatim; the editor then re-fetches so the
// Normalize-repaired truth is what stays on screen. Mode mechanics:
// main.ts gates its keydown, blur->Hide, and app:shown query-focus
// handlers on configModeActive(); this module owns Esc (dirty-aware
// close) and Ctrl/Cmd+S via its own window keydown handler (the
// preview.ts pattern). HIDE/SHOW RESTORE: hiding the bar while the
// editor is up (hotkey toggle, IPC hide, tray, the darwin Space
// switch -- every hide path; Esc/Close are the only mode EXITS)
// leaves `active` latched, so the next "app:shown" RESTORES the
// editor exactly -- mode, controls-column scroll position, focused
// control (tracked by a focusin listener, re-asserted next frame),
// ToC highlight, and any unsaved dirty edits -- in memory for the
// app run. Exiting via Esc/Close clears the latch: the next summon
// is a fresh search bar, with an unsaved working copy still
// PRESERVED and restored on the next config:open in the same run.
// All DOM building is text-node-only; styling lives in
// config.css over the existing --sb-* tokens.

import "./config.css";
import { applyPreviewConfig } from "./preview";
import { activeSectionIndex } from "./toc";

const ESC_DISCARD_MS = 2000; // second Esc within this window discards
const FLASH_OK_MS = 1500;
const FLASH_NOTE_MS = 2500;
const FLASH_ERROR_MS = 3000;

// The subset of JSON Schema (draft 2020-12) the renderer walks. Refs
// are the schema's own "#/$defs/<name>" form only.
interface SchemaNode {
  $ref?: string;
  type?: string;
  enum?: string[];
  minimum?: number;
  maximum?: number;
  exclusiveMinimum?: number;
  default?: unknown;
  title?: string;
  description?: string;
  properties?: Record<string, SchemaNode>;
  patternProperties?: Record<string, SchemaNode>;
  items?: SchemaNode;
  $defs?: Record<string, SchemaNode>;
  // Vendor annotation (safe: the lockstep schema tests compare
  // property-NAME lists only): true = the editor never renders this
  // leaf or subtree. See editorHidden().
  "x-editor-hidden"?: boolean;
}

// One ToC sidebar entry, registered by the SAME walk that renders the
// controls column (no second source of truth). depth 1 = top-level
// section, deeper = indented sub-entry. button/count are filled in by
// renderToc right after the walk.
interface TocEntry {
  dotted: string;
  label: string;
  depth: number;
  section: HTMLElement;
  button?: HTMLButtonElement;
  count?: HTMLSpanElement;
}

type LeafKind =
  | "section"
  | "boolean"
  | "enum"
  | "integer"
  | "number"
  | "string"
  | "secret"
  | "stringlist"
  | "kvmap"
  | "json";

interface Notice {
  cls: string; // "" | "warn" | "error"
  text: string;
}

let app: WailsAppBindings | null = null;
let active = false; // config mode currently on screen
let dirty = false; // unsaved edits in the working copy
let doc: Record<string, unknown> | null = null; // the working copy
let schema: SchemaNode | null = null; // cached (embedded, immutable)
let unknownKeys: string[] = [];
let loadWarning = "";
let externalChange = false; // config.json changed on disk while dirty
let lastSummary: Notice[] = []; // save / external-reload outcome lines
let escArmedAt = 0; // first dirty-Esc timestamp (discard arm)
let flashHandle: number | undefined;
let tocEntries: TocEntry[] = []; // ToC registry, rebuilt per render
let lastFocusId = ""; // last focused #config-pane element id (restore)
let tocSyncQueued = false; // rAF-coalesced scroll-sync flag

// Unparseable controls (raw JSON that does not parse, non-numeric
// number fields) by dotted path; a save is blocked -- naming them --
// until they parse. Range/semantic validation stays Go-side.
const invalid = new Map<string, string>();

let filterEl: HTMLInputElement;
let noticesEl: HTMLDivElement;
let bodyEl: HTMLDivElement;
let tocEl: HTMLElement;
let paneEl: HTMLDivElement;
let flashEl: HTMLSpanElement;
let dirtyNoteEl: HTMLSpanElement;
let saveBtn: HTMLButtonElement;
let closeBtn: HTMLButtonElement;
let openFileBtn: HTMLButtonElement;
let queryEl: HTMLInputElement;

// configModeActive gates main.ts's keydown and blur->Hide handlers:
// while the editor is up, Esc/arrows/Enter belong to it and an
// alt-tab away must NOT hide the bar.
export function configModeActive(): boolean {
  return active;
}

// initConfig wires the editor -- called once from wire(). Nothing is
// fetched here: the schema and config load lazily on the first
// "config:open".
export function initConfig(a: WailsAppBindings, rt: WailsRuntime): void {
  app = a;
  filterEl = document.getElementById("config-filter") as HTMLInputElement;
  noticesEl = document.getElementById("config-notices") as HTMLDivElement;
  bodyEl = document.getElementById("config-body") as HTMLDivElement;
  tocEl = document.getElementById("config-toc") as HTMLElement;
  paneEl = document.getElementById("config-pane") as HTMLDivElement;
  flashEl = document.getElementById("config-flash") as HTMLSpanElement;
  dirtyNoteEl = document.getElementById("config-dirty-note") as HTMLSpanElement;
  saveBtn = document.getElementById("config-save-btn") as HTMLButtonElement;
  closeBtn = document.getElementById("config-close-btn") as HTMLButtonElement;
  openFileBtn = document.getElementById(
    "config-open-file-btn",
  ) as HTMLButtonElement;
  queryEl = document.getElementById("query") as HTMLInputElement;

  filterEl.addEventListener("input", applyFilter);
  // Scroll-sync: while the controls column scrolls, highlight the ToC
  // entry whose section sits at the viewport top (rAF-coalesced; the
  // math itself is pure, see toc.ts).
  bodyEl.addEventListener("scroll", scheduleTocSync);
  // The hide/show restore needs to know which control held focus:
  // track the last focused id-carrying element inside the pane
  // (controls are cfg-<path>, ToC entries config-toc-<dotted>).
  paneEl.addEventListener("focusin", (ev: FocusEvent) => {
    const t = ev.target;
    if (t instanceof HTMLElement && t.id !== "") {
      lastFocusId = t.id;
    }
  });
  saveBtn.addEventListener("click", () => {
    doSave();
  });
  closeBtn.addEventListener("click", () => {
    requestClose();
  });
  openFileBtn.addEventListener("click", () => {
    if (app === null) {
      return;
    }
    // The hand-edit escape hatch; the editor stays open (an external
    // save then arrives as config:changed).
    app.OpenConfigFile().catch((err: unknown) => {
      flash(String(err), FLASH_ERROR_MS);
    });
  });

  // The editor's OWN keydown handler (the preview.ts pattern);
  // main.ts's document handler early-returns while the mode is
  // active, so there is exactly one owner per key.
  window.addEventListener("keydown", (ev: KeyboardEvent) => {
    if (!active) {
      return;
    }
    if (ev.key === "Escape") {
      ev.preventDefault();
      requestClose();
    } else if (
      (ev.ctrlKey || ev.metaKey) &&
      !ev.altKey &&
      !ev.shiftKey &&
      ev.key.toLowerCase() === "s"
    ) {
      ev.preventDefault();
      doSave();
    }
  });

  rt.EventsOn("config:open", () => {
    void enterMode();
  });

  // HIDE/SHOW RESTORE: `active` still true at app:shown time means
  // the bar hid WHILE the editor was up (hotkey toggle, IPC hide,
  // tray, darwin Space switch -- app:shown only ever fires on a
  // hidden->shown transition, and Esc/Close are the paths that clear
  // `active`). Restore the editor exactly instead of dropping to a
  // fresh search bar: the DOM was never torn down, so the mode,
  // scroll position, dirty edits, and ToC state are all still there
  // -- re-assert the scroll and the focused control one frame later
  // (main.ts skips its query-focus steal while the mode is active,
  // but the platform may have moved focus during the hide). Esc out
  // of a restored editor lands on the normal fresh summoned bar
  // (main.ts's app:shown reset ran underneath). A hide from SEARCH
  // mode arms nothing: `active` was already false.
  rt.EventsOn("app:shown", () => {
    if (!active) {
      return;
    }
    escArmedAt = 0; // a re-summon is a fresh Esc-discard state
    const scrollTop = bodyEl.scrollTop;
    nextFrame(() => {
      if (!active) {
        return; // exited before the frame (unlikely, but cheap)
      }
      bodyEl.scrollTop = scrollTop;
      const el =
        lastFocusId === "" ? null : document.getElementById(lastFocusId);
      if (el !== null && el.isConnected && paneEl.contains(el)) {
        (el as HTMLElement).focus();
      }
      updateTocActive();
    });
  });

  rt.EventsOn("config:changed", (...data: unknown[]) => {
    onConfigChanged(data[0] as ConfigChangedEvent | undefined);
  });
}

/* --- mode transitions ------------------------------------------------ */

async function enterMode(): Promise<void> {
  if (app === null) {
    return;
  }
  try {
    if (schema === null) {
      schema = JSON.parse(await app.GetConfigSchema()) as SchemaNode;
    }
    if (!dirty || doc === null) {
      await fetchDoc(); // fresh truth; a preserved dirty copy skips this
    }
  } catch (err: unknown) {
    console.warn("config editor load failed: " + String(err));
    return;
  }
  lastSummary = []; // never open onto a previous session's summary
  active = true;
  escArmedAt = 0;
  document.body.classList.add("with-config");
  renderEditor();
  filterEl.value = "";
  applyFilter();
  filterEl.focus();
  if (dirty) {
    flash("restored unsaved edits", FLASH_NOTE_MS);
  }
}

// leaveMode drops out of config mode without touching the working
// copy (the app:shown / hide path). Close/Esc decide about the copy
// in requestClose before calling this.
function leaveMode(): void {
  active = false;
  document.body.classList.remove("with-config");
}

// requestClose is the Esc / Close-button semantics: clean = exit;
// dirty = first press warns, a second within ESC_DISCARD_MS discards
// the working copy and exits. Ctrl+S always saves instead.
function requestClose(): void {
  if (dirty) {
    const now = Date.now();
    if (now - escArmedAt > ESC_DISCARD_MS) {
      escArmedAt = now;
      flash("unsaved changes -- press Esc again to discard", FLASH_NOTE_MS);
      return;
    }
    doc = null;
    dirty = false;
  }
  leaveMode();
  queryEl.focus(); // back to the normal bar, query row focused
}

/* --- data plumbing --------------------------------------------------- */

async function fetchDoc(): Promise<void> {
  if (app === null) {
    return;
  }
  const fe = await app.GetConfigForEdit();
  doc = JSON.parse(fe.configJson) as Record<string, unknown>;
  unknownKeys = fe.unknownKeys ?? [];
  loadWarning = fe.loadWarning ?? "";
  dirty = false;
  externalChange = false;
  lastSummary = []; // a fresh document is a fresh slate
}

function getPath(obj: unknown, path: string[]): unknown {
  let cur: unknown = obj;
  for (const k of path) {
    if (typeof cur !== "object" || cur === null) {
      return undefined;
    }
    cur = (cur as Record<string, unknown>)[k];
  }
  return cur;
}

function setPath(
  obj: Record<string, unknown>,
  path: string[],
  value: unknown,
): void {
  let cur = obj;
  for (let i = 0; i < path.length - 1; i++) {
    const next = cur[path[i]];
    if (typeof next === "object" && next !== null && !Array.isArray(next)) {
      cur = next as Record<string, unknown>;
    } else {
      const fresh: Record<string, unknown> = {};
      cur[path[i]] = fresh;
      cur = fresh;
    }
  }
  cur[path[path.length - 1]] = value;
}

// setVal is the one write path from controls into the working copy;
// every successful write marks the session dirty.
function setVal(path: string[], value: unknown): void {
  if (doc === null) {
    return;
  }
  setPath(doc, path, value);
  if (!dirty) {
    dirty = true;
    updateDirtyUI();
  }
}

function markInvalid(dotted: string, control: HTMLElement, msg: string): void {
  invalid.set(dotted, msg);
  control.classList.add("config-invalid");
}

function clearInvalid(dotted: string, control: HTMLElement): void {
  invalid.delete(dotted);
  control.classList.remove("config-invalid");
}

/* --- save + external changes ----------------------------------------- */

function doSave(): void {
  if (app === null || doc === null || !active) {
    return;
  }
  if (invalid.size > 0) {
    const first = Array.from(invalid.entries())
      .slice(0, 3)
      .map(([p, m]) => p + ": " + m)
      .join("; ");
    flash("cannot save -- " + first, FLASH_ERROR_MS);
    return;
  }
  saveBtn.disabled = true;
  app
    .SaveConfig(JSON.stringify(doc, null, 2))
    .then(async (res) => {
      if (!res.ok) {
        lastSummary = [
          { cls: "error", text: res.error ?? "save failed" },
        ];
        renderNotices();
        flash("save failed", FLASH_ERROR_MS);
        return;
      }
      escArmedAt = 0;
      flash("Saved", FLASH_OK_MS);
      // Re-fetch so the editor shows the repaired (Normalize) truth,
      // then re-render preserving scroll and filter. The summary is
      // set AFTER the fetch (which starts a fresh slate).
      try {
        await fetchDoc();
      } catch (err: unknown) {
        console.warn("config refetch after save failed: " + String(err));
      }
      lastSummary = buildSummary(res);
      const scrollTop = bodyEl.scrollTop;
      renderEditor();
      bodyEl.scrollTop = scrollTop;
      // The GUI save does not fire config:changed (self-write
      // suppression), so the preview mount/unmount refresh runs here.
      refreshPreviewConfig();
    })
    .catch((err: unknown) => {
      lastSummary = [{ cls: "error", text: String(err) }];
      renderNotices();
      flash("save failed", FLASH_ERROR_MS);
    })
    .finally(() => {
      saveBtn.disabled = false;
    });
}

// buildSummary turns a SaveConfig / config:changed report into notice
// lines. NextLaunch is the honesty surface: each knob is named with
// the exact "takes effect at next launch" note -- never a generic
// "restart required" badge.
function buildSummary(res: {
  applied: string[] | null;
  pending: string[] | null;
  nextLaunch?: string[] | null;
  applyErrors?: string[] | null;
}): Notice[] {
  const out: Notice[] = [];
  const applied = res.applied ?? [];
  if (applied.length > 0) {
    out.push({ cls: "", text: "Applied live: " + applied.join(", ") });
  } else {
    out.push({ cls: "", text: "Saved -- no settings changed" });
  }
  for (const p of res.pending ?? []) {
    out.push({ cls: "warn", text: p + " takes effect at next launch" });
  }
  for (const k of res.nextLaunch ?? []) {
    out.push({ cls: "warn", text: k + " takes effect at next launch" });
  }
  for (const e of res.applyErrors ?? []) {
    out.push({ cls: "warn", text: "apply error: " + e });
  }
  return out;
}

// onConfigChanged reacts to external config.json edits (hand edits,
// another instance): the backend already hot-applied them. An open
// clean editor silently reloads; an open dirty editor keeps the
// user's edits and offers a Reload strip; a closed editor needs
// nothing. Either way the preview pane's mount/width follow the new
// config.
function onConfigChanged(ev: ConfigChangedEvent | undefined): void {
  refreshPreviewConfig();
  if (!active || ev === undefined) {
    return;
  }
  if (ev.error !== undefined && ev.error !== "") {
    lastSummary = [
      {
        cls: "error",
        text:
          "config.json on disk failed to load: " +
          ev.error +
          " (previous config stays applied)",
      },
    ];
    renderNotices();
    return;
  }
  if (dirty) {
    externalChange = true;
    renderNotices();
    return;
  }
  void (async () => {
    try {
      await fetchDoc();
    } catch (err: unknown) {
      console.warn("config reload failed: " + String(err));
      return;
    }
    lastSummary = buildSummary(ev);
    const scrollTop = bodyEl.scrollTop;
    renderEditor();
    bodyEl.scrollTop = scrollTop;
    flash("config.json changed on disk -- reloaded", FLASH_NOTE_MS);
  })();
}

// refreshPreviewConfig re-reads the live preview state (enabled /
// provider keys / results width) and mounts, unmounts, or resizes the
// pane accordingly -- the backend applies preview config live, so the
// frontend must follow.
function refreshPreviewConfig(): void {
  if (app === null) {
    return;
  }
  app
    .GetPreviewConfig()
    .then((cfg) => {
      applyPreviewConfig(cfg);
    })
    .catch((err: unknown) => {
      console.warn("preview config refetch failed: " + String(err));
    });
}

/* --- notices, flash, dirty ------------------------------------------- */

function renderNotices(): void {
  const nodes: HTMLElement[] = [];
  if (loadWarning !== "") {
    nodes.push(
      notice("warn", "config load warning: " + loadWarning),
    );
  }
  if (unknownKeys.length > 0) {
    nodes.push(
      notice(
        "warn",
        "Unknown keys kept out of the editor: " +
          unknownKeys.join(", ") +
          " -- they will be dropped if you save here; use Open config.json for those.",
      ),
    );
  }
  if (externalChange) {
    const n = notice("warn", "config.json changed on disk.");
    const reload = el("button", "config-btn", "Reload");
    reload.type = "button";
    reload.title = "discard the edits here and load the on-disk file";
    reload.addEventListener("click", () => {
      void (async () => {
        try {
          await fetchDoc();
        } catch (err: unknown) {
          flash("reload failed: " + String(err), FLASH_ERROR_MS);
          return;
        }
        renderEditor();
      })();
    });
    n.append(reload);
    nodes.push(n);
  }
  for (const s of lastSummary) {
    nodes.push(notice(s.cls, s.text));
  }
  noticesEl.replaceChildren(...nodes);
}

function notice(cls: string, text: string): HTMLDivElement {
  const n = el("div", "config-notice");
  if (cls !== "") {
    n.classList.add("config-notice-" + cls);
  }
  n.append(el("span", undefined, text));
  return n;
}

function flash(text: string, ms: number): void {
  flashEl.textContent = text;
  flashEl.hidden = false;
  window.clearTimeout(flashHandle);
  flashHandle = window.setTimeout(() => {
    flashEl.hidden = true;
    flashEl.textContent = "";
  }, ms);
}

function updateDirtyUI(): void {
  dirtyNoteEl.hidden = !dirty;
  saveBtn.classList.toggle("config-btn-dirty", dirty);
}

/* --- filter ----------------------------------------------------------- */

// applyFilter hides rows whose path/description does not contain the
// filter text, then sections left with no visible row -- and mirrors
// the outcome into the ToC: zero-match entries dim, matching entries
// show their visible-row count (a parent section's count includes its
// sub-sections' rows; sub-entries carry their own).
function applyFilter(): void {
  const q = filterEl.value.trim().toLowerCase();
  for (const row of Array.from(
    bodyEl.querySelectorAll<HTMLElement>(".config-row"),
  )) {
    row.hidden = q !== "" && !(row.dataset.search ?? "").includes(q);
  }
  for (const sec of Array.from(
    bodyEl.querySelectorAll<HTMLElement>(".config-section"),
  )) {
    sec.hidden = q !== "" && sec.querySelector(".config-row:not([hidden])") === null;
  }
  for (const e of tocEntries) {
    if (e.button === undefined || e.count === undefined) {
      continue;
    }
    if (q === "") {
      e.button.classList.remove("config-toc-dim");
      e.count.hidden = true;
      e.count.textContent = "";
      continue;
    }
    const n = e.section.querySelectorAll(".config-row:not([hidden])").length;
    e.button.classList.toggle("config-toc-dim", n === 0);
    e.count.textContent = n > 0 ? String(n) : "";
    e.count.hidden = n === 0;
  }
  scheduleTocSync(); // visibility changed the section offsets
}

/* --- the schema-driven renderer --------------------------------------- */

function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className?: string,
  text?: string,
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  if (className !== undefined) {
    node.className = className;
  }
  if (text !== undefined) {
    node.textContent = text;
  }
  return node;
}

// resolve follows the schema's local "#/$defs/<name>" refs.
function resolve(node: SchemaNode): SchemaNode {
  const ref = node.$ref;
  if (ref !== undefined && ref.startsWith("#/$defs/") && schema !== null) {
    const def = (schema.$defs ?? {})[ref.slice("#/$defs/".length)];
    if (def !== undefined) {
      return def;
    }
  }
  return node;
}

// classify picks a control for one (ref-resolved) schema node. The
// walk is generic: only the DISPLAY choices below are typed; any
// shape without a dedicated editor -- objects of objects, arrays of
// objects, whatever the schema grows later -- falls back to the raw
// JSON sub-editor rather than being dropped.
function classify(node: SchemaNode): LeafKind {
  if (node.properties !== undefined) {
    return "section";
  }
  if (node.enum !== undefined) {
    return "enum";
  }
  switch (node.type) {
    case "boolean":
      return "boolean";
    case "integer":
      return "integer";
    case "number":
      return "number";
    case "string":
      return (node.description ?? "").startsWith("SECRET:")
        ? "secret"
        : "string";
    case "array": {
      const items = node.items === undefined ? undefined : resolve(node.items);
      return items !== undefined && items.type === "string"
        ? "stringlist"
        : "json";
    }
    case "object": {
      const pats = node.patternProperties;
      if (pats !== undefined) {
        const vals = Object.values(pats).map(resolve);
        if (vals.length > 0 && vals.every((v) => v.type === "string")) {
          return "kvmap";
        }
      }
      return "json";
    }
    default:
      return "json";
  }
}

// editorHidden reports the "x-editor-hidden": true vendor annotation
// -- checked on the property node AS WRITTEN and on its resolved
// $ref target, so both an annotated property and an annotated $def
// hide. This is the ONE hiding mechanism (rootsVersion and "$schema"
// carry it in schemas/config.schema.json; there is no hard-coded key
// list left), and it prunes whole subtrees at any depth.
function editorHidden(node: SchemaNode): boolean {
  return (
    node["x-editor-hidden"] === true ||
    resolve(node)["x-editor-hidden"] === true
  );
}

// renderEditor rebuilds the whole settings body AND the ToC sidebar
// from the schema's top-level properties IN SCHEMA ORDER (JS objects
// preserve string key insertion order). x-editor-hidden nodes are
// skipped. Top-level LEAF settings are grouped so the ToC covers
// everything: the leading run shares one synthetic "General" section;
// a leaf appearing after the first real section gets its own group
// named after itself -- schema order is never reshuffled.
function renderEditor(): void {
  invalid.clear();
  tocEntries = [];
  if (schema === null || doc === null) {
    return;
  }
  const props = schema.properties ?? {};
  const nodes: HTMLElement[] = [];
  let general: HTMLElement | null = null;
  let sawSection = false;
  for (const key of Object.keys(props)) {
    if (editorHidden(props[key])) {
      continue;
    }
    const node = resolve(props[key]);
    if (classify(node) === "section") {
      sawSection = true;
      nodes.push(renderSection([key], node, 1));
      continue;
    }
    if (!sawSection) {
      if (general === null) {
        general = makeSection("general", "General", "General", 1);
        nodes.push(general);
      }
      general.append(renderLeafRow([key], node, classify(node)));
    } else {
      const g = makeSection(key, key, key, 1);
      g.append(renderLeafRow([key], node, classify(node)));
      nodes.push(g);
    }
  }
  bodyEl.replaceChildren(...nodes);
  renderToc();
  renderNotices();
  updateDirtyUI();
  applyFilter();
}

// makeSection builds one .config-section container (header + optional
// note) and registers its ToC entry -- the ONE registration point, so
// the sidebar and the controls column can never disagree.
function makeSection(
  dotted: string,
  headerText: string,
  tocLabel: string,
  depth: number,
  node?: SchemaNode,
): HTMLElement {
  const sec = el("div", "config-section");
  sec.id = "config-sec-" + dotted;
  const header = el("div", "config-section-header", headerText);
  if (node !== undefined && node.title !== undefined) {
    header.append(el("span", "config-section-title", node.title));
  }
  sec.append(header);
  if (node !== undefined && node.description !== undefined) {
    sec.append(el("div", "config-section-note", node.description));
  }
  tocEntries.push({ dotted, label: tocLabel, depth, section: sec });
  return sec;
}

function renderSection(
  path: string[],
  node: SchemaNode,
  depth: number,
): HTMLElement {
  const sec = makeSection(
    path.join("."),
    path.join("."),
    path[path.length - 1],
    depth,
    node,
  );
  const props = node.properties ?? {};
  for (const key of Object.keys(props)) {
    if (editorHidden(props[key])) {
      continue;
    }
    const child = resolve(props[key]);
    if (classify(child) === "section") {
      sec.append(renderSection([...path, key], child, depth + 1));
    } else {
      sec.append(renderLeafRow([...path, key], child, classify(child)));
    }
  }
  return sec;
}

/* --- the ToC sidebar -------------------------------------------------- */

// renderToc rebuilds the sidebar buttons from the walk's registry.
// Buttons are natively focusable (Tab order: filter -> ToC ->
// controls) and Enter/Space activates them like any button.
function renderToc(): void {
  const items: HTMLElement[] = [];
  for (const e of tocEntries) {
    const b = el(
      "button",
      e.depth > 1 ? "config-toc-entry config-toc-sub" : "config-toc-entry",
    );
    b.type = "button";
    b.id = "config-toc-" + e.dotted;
    b.title = e.dotted;
    b.append(el("span", "config-toc-label", e.label));
    const count = el("span", "config-toc-count");
    count.hidden = true;
    b.append(count);
    b.addEventListener("click", () => {
      jumpToSection(e);
    });
    e.button = b;
    e.count = count;
    items.push(b);
  }
  tocEl.replaceChildren(...items);
}

// jumpToSection scrolls the controls column so the section header
// sits at the viewport top -- INSTANT, never smooth (WebKitGTK's
// smooth-scroll animator eats programmatic jumps; see main.ts's
// wheel handler). A dimmed entry whose section the filter hid clears
// the filter first: that is the only way its target can exist.
function jumpToSection(entry: TocEntry): void {
  if (entry.section.hidden) {
    filterEl.value = "";
    applyFilter();
  }
  const top =
    entry.section.getBoundingClientRect().top -
    bodyEl.getBoundingClientRect().top +
    bodyEl.scrollTop;
  bodyEl.scrollTop = top;
  setTocActive(entry);
}

function setTocActive(entry: TocEntry | null): void {
  for (const e of tocEntries) {
    e.button?.classList.toggle("config-toc-active", e === entry);
  }
}

// updateTocActive measures the VISIBLE sections' content offsets and
// hands the pure selection math (toc.ts) the scroll geometry. Runs
// rAF-coalesced from the scroll listener via scheduleTocSync.
function updateTocActive(): void {
  const visible = tocEntries.filter((e) => !e.section.hidden);
  if (visible.length === 0) {
    setTocActive(null);
    return;
  }
  const bodyTop = bodyEl.getBoundingClientRect().top;
  const offsets = visible.map(
    (e) =>
      e.section.getBoundingClientRect().top - bodyTop + bodyEl.scrollTop,
  );
  const i = activeSectionIndex(
    offsets,
    bodyEl.scrollTop,
    bodyEl.clientHeight,
    bodyEl.scrollHeight,
  );
  setTocActive(i >= 0 ? visible[i] : null);
}

function scheduleTocSync(): void {
  if (tocSyncQueued) {
    return;
  }
  tocSyncQueued = true;
  nextFrame(() => {
    tocSyncQueued = false;
    updateTocActive();
  });
}

// nextFrame = requestAnimationFrame with a setTimeout fallback (jsdom
// under vitest does not implement rAF).
function nextFrame(fn: () => void): void {
  if (typeof window.requestAnimationFrame === "function") {
    window.requestAnimationFrame(() => {
      fn();
    });
  } else {
    window.setTimeout(fn, 0);
  }
}

function renderLeafRow(
  path: string[],
  node: SchemaNode,
  kind: LeafKind,
): HTMLElement {
  const dotted = path.join(".");
  const row = el("div", "config-row");
  row.dataset.search = (dotted + " " + (node.description ?? "")).toLowerCase();
  const top = el("div", "config-row-top");
  const label = el("label", "config-label", path[path.length - 1]);
  label.title = dotted;
  const id = "cfg-" + path.join("-");
  label.htmlFor = id;
  top.append(label);
  const control = buildControl(path, node, kind, id);
  const wide = kind === "stringlist" || kind === "json" || kind === "kvmap";
  row.append(top);
  if (wide) {
    row.append(control); // full-width, under the label
  } else {
    top.append(control);
  }
  if (node.description !== undefined) {
    row.append(el("div", "config-help", node.description));
  }
  return row;
}

function buildControl(
  path: string[],
  node: SchemaNode,
  kind: LeafKind,
  id: string,
): HTMLElement {
  switch (kind) {
    case "boolean":
      return boolControl(path, node, id);
    case "enum":
      return enumControl(path, node, id);
    case "integer":
    case "number":
      return numberControl(path, node, kind, id);
    case "secret":
      return secretControl(path, node, id);
    case "stringlist":
      return stringListControl(path, node, id);
    case "kvmap":
      return kvMapControl(path, id);
    case "json":
      return jsonControl(path, node, id);
    default:
      return stringControl(path, node, id);
  }
}

function boolControl(
  path: string[],
  node: SchemaNode,
  id: string,
): HTMLInputElement {
  const input = el("input", "config-toggle");
  input.type = "checkbox";
  input.id = id;
  const cur = getPath(doc, path);
  input.checked =
    typeof cur === "boolean" ? cur : node.default === true;
  input.addEventListener("change", () => {
    setVal(path, input.checked);
  });
  return input;
}

function enumControl(
  path: string[],
  node: SchemaNode,
  id: string,
): HTMLSelectElement {
  const sel = el("select", "config-select");
  sel.id = id;
  for (const opt of node.enum ?? []) {
    const o = el("option", undefined, opt);
    o.value = opt;
    sel.append(o);
  }
  const cur = getPath(doc, path);
  const init =
    typeof cur === "string"
      ? cur
      : typeof node.default === "string"
        ? node.default
        : "";
  if (init !== "") {
    sel.value = init;
  }
  sel.addEventListener("change", () => {
    setVal(path, sel.value);
  });
  return sel;
}

function numberControl(
  path: string[],
  node: SchemaNode,
  kind: "integer" | "number",
  id: string,
): HTMLInputElement {
  const dotted = path.join(".");
  const input = el("input", "config-input config-number");
  input.type = "number";
  input.id = id;
  input.step = kind === "integer" ? "1" : "any";
  // Schema bounds as input UX only -- Go owns validation/repair.
  if (node.minimum !== undefined) {
    input.min = String(node.minimum);
  } else if (node.exclusiveMinimum !== undefined) {
    input.min = String(
      kind === "integer" ? node.exclusiveMinimum + 1 : node.exclusiveMinimum,
    );
  }
  if (node.maximum !== undefined) {
    input.max = String(node.maximum);
  }
  const cur = getPath(doc, path);
  const init =
    typeof cur === "number"
      ? cur
      : typeof node.default === "number"
        ? node.default
        : 0;
  input.value = String(init);
  input.addEventListener("input", () => {
    const raw = input.value.trim();
    const num = raw === "" ? NaN : Number(raw);
    const ok =
      Number.isFinite(num) && (kind !== "integer" || Number.isInteger(num));
    if (!ok) {
      markInvalid(
        dotted,
        input,
        kind === "integer" ? "not an integer" : "not a number",
      );
      return;
    }
    clearInvalid(dotted, input);
    setVal(path, num);
  });
  return input;
}

function stringControl(
  path: string[],
  node: SchemaNode,
  id: string,
): HTMLInputElement {
  const input = el("input", "config-input");
  input.type = "text";
  input.id = id;
  input.autocomplete = "off";
  input.spellcheck = false;
  const cur = getPath(doc, path);
  input.value =
    typeof cur === "string"
      ? cur
      : typeof node.default === "string"
        ? node.default
        : "";
  input.addEventListener("input", () => {
    setVal(path, input.value);
  });
  return input;
}

// secretControl: a password field with a show/hide toggle. The value
// stays in the field and the working copy only -- never rendered
// elsewhere, never logged.
function secretControl(
  path: string[],
  node: SchemaNode,
  id: string,
): HTMLDivElement {
  const wrap = el("div", "config-secret");
  const input = stringControl(path, node, id);
  input.type = "password";
  const btn = el("button", "config-btn", "show");
  btn.type = "button";
  btn.addEventListener("click", () => {
    const hidden = input.type === "password";
    input.type = hidden ? "text" : "password";
    btn.textContent = hidden ? "hide" : "show";
  });
  wrap.append(input, btn);
  return wrap;
}

function stringListControl(
  path: string[],
  node: SchemaNode,
  id: string,
): HTMLTextAreaElement {
  const ta = el("textarea", "config-textarea");
  ta.id = id;
  ta.spellcheck = false;
  ta.placeholder = "one entry per line";
  const cur = getPath(doc, path);
  const fallback = Array.isArray(node.default) ? node.default : [];
  const src = Array.isArray(cur) ? cur : fallback;
  const list = src.filter((x): x is string => typeof x === "string");
  ta.value = list.join("\n");
  ta.rows = Math.min(Math.max(list.length + 1, 3), 10);
  ta.addEventListener("input", () => {
    setVal(
      path,
      ta.value
        .split("\n")
        .map((s) => s.trim())
        .filter((s) => s !== ""),
    );
  });
  return ta;
}

// kvMapControl: the string->string map editor (bangs.aliases). Rows
// are the source of truth; every change recomposes the whole map
// (blank keys are skipped until typed).
function kvMapControl(path: string[], id: string): HTMLDivElement {
  const wrap = el("div", "config-kv");
  wrap.id = id;
  const rowsEl = el("div", "config-kv-rows");
  const syncMap = (): void => {
    const m: Record<string, string> = {};
    for (const r of Array.from(rowsEl.children)) {
      const ki = r.querySelector<HTMLInputElement>(".config-kv-key");
      const vi = r.querySelector<HTMLInputElement>(".config-kv-val");
      if (ki === null || vi === null) {
        continue;
      }
      const k = ki.value.trim();
      if (k === "") {
        continue;
      }
      m[k] = vi.value.trim();
    }
    setVal(path, m);
  };
  const addRow = (k: string, v: string): void => {
    const r = el("div", "config-kv-row");
    const ki = el("input", "config-input config-kv-key");
    ki.type = "text";
    ki.value = k;
    ki.placeholder = "name";
    ki.spellcheck = false;
    const vi = el("input", "config-input config-kv-val");
    vi.type = "text";
    vi.value = v;
    vi.placeholder = "value";
    vi.spellcheck = false;
    const rm = el("button", "config-btn", "remove");
    rm.type = "button";
    ki.addEventListener("input", syncMap);
    vi.addEventListener("input", syncMap);
    rm.addEventListener("click", () => {
      r.remove();
      syncMap();
    });
    r.append(ki, vi, rm);
    rowsEl.append(r);
  };
  const cur = getPath(doc, path);
  if (typeof cur === "object" && cur !== null && !Array.isArray(cur)) {
    for (const [k, v] of Object.entries(cur as Record<string, unknown>)) {
      if (typeof v === "string") {
        addRow(k, v);
      }
    }
  }
  const add = el("button", "config-btn", "add");
  add.type = "button";
  add.addEventListener("click", () => {
    addRow("", "");
  });
  wrap.append(rowsEl, add);
  return wrap;
}

// jsonControl: the raw-JSON sub-editor for shapes without a dedicated
// control (plugins.entries with its opaque per-plugin settings, the
// rewrites rule list, and anything the schema grows later). The text
// must parse client-side before it reaches the working copy;
// authoritative validation stays Go-side.
function jsonControl(
  path: string[],
  node: SchemaNode,
  id: string,
): HTMLTextAreaElement {
  const dotted = path.join(".");
  const ta = el("textarea", "config-textarea config-json");
  ta.id = id;
  ta.spellcheck = false;
  const cur = getPath(doc, path);
  const init = cur === undefined ? (node.type === "array" ? [] : {}) : cur;
  const text = JSON.stringify(init, null, 2);
  ta.value = text;
  ta.rows = Math.min(Math.max(text.split("\n").length, 4), 14);
  ta.addEventListener("input", () => {
    let v: unknown;
    try {
      v = JSON.parse(ta.value);
    } catch {
      markInvalid(dotted, ta, "invalid JSON");
      return;
    }
    clearInvalid(dotted, ta);
    setVal(path, v);
  });
  return ta;
}
