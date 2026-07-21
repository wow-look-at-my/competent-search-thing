// Preview pane logic: reacts to selection changes with an instant
// local header plus a debounced QueryPreview dispatch, renders the
// "preview:result" payloads (metadata cards, highlighted text, image
// thumbnails, directory listings, web/AI answers, errors), and owns
// the explicit web-search / AI command strip (Ctrl+K / Ctrl+I --
// NEVER triggered by a keystroke or selection path). initPreview
// wires the (enabled-gated) listeners once; the effective state --
// pane mounted or not, provider buttons, results-column width --
// comes from applyPreviewConfig, re-applied by config.ts whenever the
// preview config changes live (a GUI save or an external config.json
// edit), so the pane mounts and unmounts without a relaunch. While
// disabled every hook and handler no-ops. All DOM building here is
// text-node-only; the single sanctioned markup sink is highlight.ts's
// setHighlighted (see its invariant comment).

import { highlightInto } from "./highlight";

// Selection -> QueryPreview debounce, so a held arrow key feels free
// (the instant header still updates every move); the spinner appears
// only when a preview takes longer than SPINNER_DELAY_MS; flashes
// mirror main.ts's status-bar timings.
const PREVIEW_DEBOUNCE_MS = 90;
const SPINNER_DELAY_MS = 150;
const FLASH_COPIED_MS = 1200;
const FLASH_ERROR_MS = 2000;
const LABEL_QUERY_MAX = 24; // strip-button query excerpt cap (chars)

// The selected row as main.ts's selection model sees it (structurally
// identical to its SelectableItem union, so main.ts passes its items
// straight through).
export type PreviewSelection =
  | { kind: "file"; file: WailsSearchResult }
  | { kind: "plugin"; pluginId: string; result: PluginResult };

let app: WailsAppBindings | null = null;
let wired = false; // initPreview grabbed elements + wired listeners
let enabled = false;
let kagiConfigured = false;
let aiConfigured = false;
let aiProvider = "openai";

let bodyEl: HTMLDivElement;
let spinnerEl: HTMLDivElement;
let flashEl: HTMLSpanElement;
let webBtn: HTMLButtonElement;
let aiBtn: HTMLButtonElement;
let folderTpl: HTMLTemplateElement;
let fileTpl: HTMLTemplateElement;

let gen = 0; // preview generation; stale payloads are dropped
let debounceHandle: number | undefined;
let spinnerHandle: number | undefined;
let flashHandle: number | undefined;
let query = ""; // current query (strip labels + explicit triggers)
let lastKey: string | null = null; // dedupes re-selects of the same row

// initPreview wires the pane up -- called once from wire() with the
// GetPreviewConfig answer. Elements and listeners are wired
// unconditionally (each handler no-ops while the pane is disabled);
// the effective state is applied by applyPreviewConfig, which
// config.ts re-invokes with a fresh GetPreviewConfig answer whenever
// the config changes live.
export function initPreview(
  a: WailsAppBindings,
  rt: WailsRuntime,
  cfg: PreviewConfigInfo,
): void {
  app = a;

  bodyEl = document.getElementById("preview-body") as HTMLDivElement;
  spinnerEl = document.getElementById("preview-spinner") as HTMLDivElement;
  flashEl = document.getElementById("preview-flash") as HTMLSpanElement;
  webBtn = document.getElementById("preview-web-btn") as HTMLButtonElement;
  aiBtn = document.getElementById("preview-ai-btn") as HTMLButtonElement;
  folderTpl = document.getElementById("tpl-icon-folder") as HTMLTemplateElement;
  fileTpl = document.getElementById("tpl-icon-file") as HTMLTemplateElement;

  webBtn.addEventListener("click", triggerWeb);
  aiBtn.addEventListener("click", triggerAI);

  // The pane's OWN keydown listener -- main.ts's document handler is
  // untouched, and only Ctrl/Cmd+K and Ctrl/Cmd+I are handled here
  // (Tab and Ctrl+Enter are taken elsewhere; plain keys pass through).
  window.addEventListener("keydown", onPreviewKeydown);

  rt.EventsOn("preview:result", (...data: unknown[]) => {
    const p = data[0] as PreviewPayload | undefined;
    if (!enabled || p === undefined || p.gen !== gen) {
      return; // pane off, stale generation, or malformed payload
    }
    cancelSpinner(); // first accepted payload wins over the spinner
    renderPayload(p);
  });

  wired = true;
  applyPreviewConfig(cfg);
}

// applyPreviewConfig mounts, unmounts, or reconfigures the pane from
// one GetPreviewConfig answer. The backend applies preview config
// live (a GUI save or an external config.json edit rebuilds the
// dispatcher and resizes the window), so the frontend follows here:
// the with-preview body class toggles the grid layout, the results
// column tracks config window.width, and the web/AI trigger buttons
// track their providers' key state in both directions.
export function applyPreviewConfig(cfg: PreviewConfigInfo): void {
  if (!wired) {
    return; // boot race: initPreview has not grabbed the elements yet
  }
  const was = enabled;
  enabled = cfg.enabled;
  kagiConfigured = cfg.kagiConfigured;
  aiConfigured = cfg.aiConfigured;
  aiProvider = cfg.aiProvider !== "" ? cfg.aiProvider : "openai";
  if (!enabled) {
    if (was) {
      cancelSpinner();
      lastKey = null;
      document.body.classList.remove("with-preview");
    }
    return;
  }
  document.body.classList.add("with-preview");
  // The left results column keeps the flag-off bar width (config
  // window.width) instead of a hardcoded 680px: the grid consumes
  // this custom property (the --plugin-accent precedent -- a custom
  // property, never an inline width style).
  if (cfg.resultsWidth > 0) {
    document.body.style.setProperty("--preview-results-col", `${cfg.resultsWidth}px`);
  }
  setTrigger(webBtn, kagiConfigured, "preview.kagi.apiKey (or KAGI_API_KEY)");
  setTrigger(aiBtn, aiConfigured, aiKeyHint(aiProvider));
  updateStripLabels();
  if (!was) {
    lastKey = null; // the next selection change repaints the pane
    renderIdle();
  }
}

/* --- hooks main.ts calls -------------------------------------------- */

// previewOnSelectionChange is called from main.ts's select() -- the
// single selection choke point, so every path (arrow move, render
// reconcile, history recall, app:shown reset) lands here; mouse
// hover is decorative-only and never does (the pane tracks the
// ACTIVE selection). null idles the pane and cancels the in-flight
// preview; a row paints an instant zero-IO header and debounces the
// QueryPreview dispatch. Re-selecting the same row (plugin
// re-renders) is a no-op.
export function previewOnSelectionChange(sel: PreviewSelection | null): void {
  if (!enabled || app === null) {
    return;
  }
  const target = sel === null ? null : targetFor(sel);
  const key = target === null ? "none" : targetKey(target);
  if (key === lastKey) {
    return; // same row as the pane already tracks
  }
  lastKey = key;
  if (target === null) {
    idlePane();
    return;
  }
  renderInstantHeader(target);
  schedule(() => {
    dispatchQueryPreview(target);
    scheduleSpinner();
  });
}

// previewOnQueryChange tracks the query for the command-strip labels
// and idles the pane the moment the query is cleared (the selection
// reset lands shortly after via select(-1) and dedupes to a no-op).
export function previewOnQueryChange(q: string): void {
  if (!enabled) {
    return;
  }
  query = q;
  updateStripLabels();
  if (q.trim() === "" && lastKey !== "none") {
    lastKey = "none";
    idlePane();
  }
}

/* --- dispatch plumbing ---------------------------------------------- */

function schedule(fn: () => void): void {
  window.clearTimeout(debounceHandle);
  debounceHandle = window.setTimeout(fn, PREVIEW_DEBOUNCE_MS);
}

// idlePane renders the idle card immediately and sends the (debounced)
// kind "none" cancel so the Go side aborts the in-flight request.
function idlePane(): void {
  cancelSpinner();
  renderIdle();
  schedule(() => {
    dispatchQueryPreview({ kind: "none" });
  });
}

function dispatchQueryPreview(target: PreviewTarget): void {
  if (app === null) {
    return;
  }
  const g = ++gen;
  app.QueryPreview(target, g).catch((err: unknown) => {
    console.warn("preview query failed: " + String(err));
  });
}

function targetFor(sel: PreviewSelection): PreviewTarget {
  if (sel.kind === "file") {
    return { kind: "file", path: sel.file.path, isDir: sel.file.isDir };
  }
  return {
    kind: "plugin",
    title: sel.result.title,
    subtitle: sel.result.subtitle ?? "",
    pluginName: sel.pluginId,
  };
}

function targetKey(t: PreviewTarget): string {
  if (t.kind === "file") {
    return "f\u0000" + (t.path ?? "");
  }
  return (
    "p\u0000" +
    (t.pluginName ?? "") +
    "\u0000" +
    (t.title ?? "") +
    "\u0000" +
    (t.subtitle ?? "")
  );
}

/* --- explicit web / AI triggers ------------------------------------- */

// aiKeyHint names the SELECTED AI provider's config knobs for the
// disabled-button hint (preview.aiProvider decides which section is
// consulted; custom has no key requirement, its base URL + model are
// the credentials).
function aiKeyHint(provider: string): string {
  switch (provider) {
    case "anthropic":
      return "preview.anthropic.apiKey (or ANTHROPIC_API_KEY)";
    case "custom":
      return "preview.custom.baseUrl + preview.custom.model";
    default:
      return "preview.openai.apiKey (or OPENAI_API_KEY)";
  }
}

// setTrigger reflects one provider's key state on its strip button --
// in BOTH directions, since a live config change can add or remove a
// key while the app runs.
function setTrigger(
  btn: HTMLButtonElement,
  configured: boolean,
  keyHint: string,
): void {
  btn.disabled = !configured;
  btn.title = configured ? "" : "add " + keyHint + " to config";
}

function onPreviewKeydown(ev: KeyboardEvent): void {
  if (!enabled) {
    return;
  }
  if (!(ev.ctrlKey || ev.metaKey) || ev.altKey || ev.shiftKey) {
    return;
  }
  const key = ev.key.toLowerCase();
  if (key === "k") {
    ev.preventDefault();
    triggerWeb();
  } else if (key === "i") {
    ev.preventDefault();
    triggerAI();
  }
}

// triggerWeb / triggerAI are the ONLY call sites of FetchWebPreview /
// FetchAIPreview: the strip buttons and their hotkeys. No keystroke,
// selection, or render path may ever call them.
function triggerWeb(): void {
  if (app === null || !enabled || !kagiConfigured || query.trim() === "") {
    return;
  }
  const g = ++gen;
  lastKey = null; // the next selection change repaints the pane
  scheduleSpinner();
  app.FetchWebPreview(query, g).catch((err: unknown) => {
    console.warn("web preview failed: " + String(err));
  });
}

function triggerAI(): void {
  if (app === null || !enabled || !aiConfigured || query.trim() === "") {
    return;
  }
  const g = ++gen;
  lastKey = null;
  scheduleSpinner();
  app.FetchAIPreview(query, g).catch((err: unknown) => {
    console.warn("AI preview failed: " + String(err));
  });
}

function stripQueryLabel(): string {
  const q = query.trim();
  if (q.length <= LABEL_QUERY_MAX) {
    return q;
  }
  return q.slice(0, LABEL_QUERY_MAX - 3) + "...";
}

function updateStripLabels(): void {
  const q = stripQueryLabel();
  webBtn.textContent =
    q === "" ? "Search web (Ctrl+K)" : 'Search web for "' + q + '" (Ctrl+K)';
  aiBtn.textContent =
    q === "" ? "Ask AI (Ctrl+I)" : 'Ask AI about "' + q + '" (Ctrl+I)';
}

/* --- spinner + flash ------------------------------------------------- */

function scheduleSpinner(): void {
  window.clearTimeout(spinnerHandle);
  spinnerHandle = window.setTimeout(() => {
    spinnerEl.hidden = false;
  }, SPINNER_DELAY_MS);
}

function cancelSpinner(): void {
  window.clearTimeout(spinnerHandle);
  spinnerHandle = undefined;
  spinnerEl.hidden = true;
}

function flashPane(text: string, ms: number): void {
  flashEl.textContent = text;
  flashEl.hidden = false;
  window.clearTimeout(flashHandle);
  flashHandle = window.setTimeout(() => {
    flashEl.hidden = true;
    flashEl.textContent = "";
  }, ms);
}

/* --- pane actions (validated Go-side) -------------------------------- */

function openURL(url: string): void {
  if (app === null) {
    return;
  }
  // Go re-validates (http/https + host), opens, and hides the bar.
  app
    .RunPluginAction("preview", { type: "open_url", value: url })
    .catch((err: unknown) => {
      flashPane(String(err), FLASH_ERROR_MS);
    });
}

function copyText(text: string): void {
  if (app === null) {
    return;
  }
  // copy_text is validated Go-side (<= 8 KiB); rejections surface as
  // a pane flash. The bar stays open either way.
  app
    .RunPluginAction("preview", { type: "copy_text", value: text })
    .then(() => {
      flashPane("Copied", FLASH_COPIED_MS);
    })
    .catch((err: unknown) => {
      flashPane(String(err), FLASH_ERROR_MS);
    });
}

/* --- renderers (text-node DOM building only) -------------------------- */

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

function headerNodes(title: string, sub: string): HTMLDivElement {
  const header = el("div", "preview-header");
  header.append(el("div", "preview-title", title));
  if (sub !== "") {
    const p = el("div", "preview-path", sub);
    p.title = sub;
    header.append(p);
  }
  return header;
}

function setBody(...nodes: (HTMLElement | SVGSVGElement)[]): void {
  bodyEl.replaceChildren(...nodes);
}

// fmtSize humanizes a byte count for captions and listing rows.
function fmtSize(n: number): string {
  if (n < 1024) {
    return String(n) + " B";
  }
  const units = ["KB", "MB", "GB", "TB"];
  let v = n;
  let u = -1;
  do {
    v /= 1024;
    u++;
  } while (v >= 1024 && u < units.length - 1);
  return (v >= 100 ? v.toFixed(0) : v.toFixed(1)) + " " + units[u];
}

function baseName(path: string): string {
  const cut = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
  return cut >= 0 ? path.slice(cut + 1) : path;
}

// idleGlyph clones the query row's magnifier as the app glyph (id
// stripped so it stays unique); a missing template degrades to a
// text glyph.
function idleGlyph(): SVGSVGElement | HTMLSpanElement {
  const magnifier = document.getElementById("magnifier");
  if (magnifier instanceof SVGSVGElement) {
    const clone = magnifier.cloneNode(true) as SVGSVGElement;
    clone.removeAttribute("id");
    clone.classList.add("preview-idle-glyph");
    clone.setAttribute("width", "40");
    clone.setAttribute("height", "40");
    return clone;
  }
  return el("span", "preview-idle-glyph", "\u{1F50D}");
}

function renderIdle(): void {
  const card = el("div", "preview-idle");
  card.append(idleGlyph());
  card.append(
    el(
      "div",
      "preview-idle-hint",
      "Select a result to preview -- Ctrl+K web search -- Ctrl+I ask AI",
    ),
  );
  setBody(card);
}

// renderInstantHeader paints the selected row's in-memory data (zero
// IO) the moment the selection moves; the real payload replaces it
// when it arrives.
function renderInstantHeader(target: PreviewTarget): void {
  if (target.kind === "file") {
    const path = target.path ?? "";
    setBody(headerNodes(baseName(path), path));
    return;
  }
  setBody(headerNodes(target.title ?? "", target.subtitle ?? ""));
}

// renderPayload replaces the pane content for one accepted emission.
function renderPayload(p: PreviewPayload): void {
  switch (p.kind) {
    case "meta":
      renderMeta(p);
      break;
    case "text":
      renderText(p);
      break;
    case "image":
      renderImage(p);
      break;
    case "dir":
      renderDir(p);
      break;
    case "web":
      renderWeb(p);
      break;
    case "ai":
      renderAI(p);
      break;
    default:
      renderError(p);
  }
}

function renderMeta(p: PreviewPayload): void {
  const dl = el("dl", "preview-meta");
  for (const row of p.meta ?? []) {
    dl.append(el("dt", undefined, row.label));
    dl.append(el("dd", undefined, row.value));
  }
  setBody(headerNodes(p.title, p.path), dl);
}

function renderText(p: PreviewPayload): void {
  const t = p.text;
  if (t === undefined) {
    renderError({ ...p, err: "malformed text payload" });
    return;
  }
  const pre = el("pre", "preview-code");
  const code = el("code", "hljs");
  highlightInto(code, t.content, t.lang);
  pre.append(code);
  const nodes: HTMLElement[] = [headerNodes(p.title, p.path), pre];
  if (t.truncated) {
    nodes.push(
      el(
        "div",
        "preview-foot",
        "truncated at " +
          String(Math.round(t.content.length / 1024)) +
          " KB -- full size " +
          fmtSize(t.sizeBytes),
      ),
    );
  }
  setBody(...nodes);
}

function renderImage(p: PreviewPayload): void {
  const im = p.image;
  if (im === undefined) {
    renderError({ ...p, err: "malformed image payload" });
    return;
  }
  const img = el("img", "preview-image");
  // A data URI from the Go thumbnailer; an <img>.src assignment is an
  // attribute write, never markup parsing.
  img.src = im.dataUri;
  img.alt = p.title;
  const caption = el(
    "div",
    "preview-image-caption",
    String(im.w) +
      "x" +
      String(im.h) +
      " (orig " +
      String(im.origW) +
      "x" +
      String(im.origH) +
      ", " +
      fmtSize(im.sizeBytes) +
      ")",
  );
  setBody(headerNodes(p.title, p.path), img, caption);
}

function renderDir(p: PreviewPayload): void {
  const d = p.dir;
  if (d === undefined) {
    renderError({ ...p, err: "malformed dir payload" });
    return;
  }
  const list = el("div", "preview-dir");
  for (const entry of d.entries) {
    const row = el("div", "preview-dir-row");
    const tpl = entry.isDir ? folderTpl : fileTpl;
    row.append(tpl.content.cloneNode(true));
    const name = el("span", "preview-dir-name", entry.name);
    name.title = entry.name;
    row.append(name);
    if (!entry.isDir) {
      row.append(el("span", "preview-dir-size", fmtSize(entry.size)));
    }
    list.append(row);
  }
  const nodes: HTMLElement[] = [headerNodes(p.title, p.path), list];
  if (d.truncated) {
    nodes.push(
      el(
        "div",
        "preview-foot",
        String(d.total - d.entries.length) + " more...",
      ),
    );
  }
  setBody(...nodes);
}

function renderWeb(p: PreviewPayload): void {
  const w = p.web;
  if (w === undefined) {
    renderError({ ...p, err: "malformed web payload" });
    return;
  }
  const nodes: HTMLElement[] = [
    headerNodes('Web results for "' + w.query + '"', ""),
  ];
  if (w.cached) {
    nodes.push(el("span", "preview-badge", "cached"));
  }
  const list = el("div", "preview-web");
  for (const r of w.results) {
    const row = el("div", "preview-web-result");
    row.append(el("div", "preview-web-title", r.title));
    if (r.snippet !== "") {
      row.append(el("div", "preview-web-snippet", r.snippet));
    }
    const url = el("div", "preview-web-url", r.url);
    url.title = r.url;
    row.append(url);
    row.addEventListener("click", () => {
      openURL(r.url);
    });
    list.append(row);
  }
  nodes.push(list);
  setBody(...nodes);
}

function renderAI(p: PreviewPayload): void {
  const ai = p.ai;
  if (ai === undefined) {
    renderError({ ...p, err: "malformed ai payload" });
    return;
  }
  const badges = el("div", "preview-ai-badges");
  badges.append(el("span", "preview-badge", ai.model));
  if (ai.cached) {
    badges.append(el("span", "preview-badge", "cached"));
  }
  const copyBtn = el("button", "preview-btn", "Copy");
  copyBtn.type = "button";
  copyBtn.addEventListener("click", () => {
    copyText(ai.answer);
  });
  badges.append(copyBtn);
  setBody(
    headerNodes('AI answer for "' + ai.query + '"', ""),
    badges,
    el("div", "preview-ai-answer", ai.answer),
  );
}

function renderError(p: PreviewPayload): void {
  const card = el("div", "preview-error");
  card.append(el("span", "preview-error-glyph", "\u{26A0}\u{FE0F}"));
  card.append(el("span", undefined, p.err ?? "preview failed"));
  const nodes: HTMLElement[] = [];
  if (p.title !== "" || p.path !== "") {
    nodes.push(headerNodes(p.title, p.path));
  }
  nodes.push(card);
  setBody(...nodes);
}
