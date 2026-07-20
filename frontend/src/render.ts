// DOM construction for the results list: one row per file hit with a
// folder/file glyph (cloned from the <template> elements in
// index.html), the entry name with the matched substring highlighted,
// and the dimmed parent directory -- plus the plugin sections that
// render in the two plugin zones (priority > 0 above the file rows,
// everything else below; header + rows cloned from templates, icon
// glyph map, whitelisted --plugin-accent styling hook). Pure
// text-node builders throughout: nothing here can inject markup.

// RowHandlers receive the ROW ELEMENT, not a captured index: handler
// indices are resolved at EVENT time (main.ts does rows.indexOf), so
// rows keep working when a zone rendered later shifts their flat
// position -- a late priority emission PREPENDS rows above the file
// rows, which would silently stale any index captured at render time.
// Deliberately NO hover handler: mouse hover is a decorative CSS
// :hover wash only and never touches the active selection (clicking
// is the explicit mouse choice) -- see the selection model in main.ts.
export interface RowHandlers {
  onActivate(row: HTMLDivElement, reveal: boolean): void;
}

const folderTpl = document.getElementById(
  "tpl-icon-folder",
) as HTMLTemplateElement;
const fileTpl = document.getElementById("tpl-icon-file") as HTMLTemplateElement;

// parentDir strips the trailing name component from a path, tolerating
// both separator styles so Windows paths render sensibly too.
export function parentDir(path: string, name: string): string {
  if (path.length > name.length && path.endsWith(name)) {
    const dir = path.slice(0, path.length - name.length - 1);
    if (dir !== "") {
      return dir;
    }
  }
  const cut = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
  return cut > 0 ? path.slice(0, cut) : "/";
}

// appendHighlighted fills el with text, wrapping the characters inside
// the engine-minted ranges in .hl spans -- per-character letter-color
// highlighting (the .hl class changes ONLY the letter color via
// --sb-highlight; deliberately no background rectangles). Ranges are
// half-open RUNE (code point) index pairs from Go, sorted and merged;
// JS strings are UTF-16, so the walk counts code points. Plain text
// nodes everywhere: nothing here can inject markup, and the spans stay
// inline inside el so ellipsis/flex truncation behaves as before.
export function appendHighlighted(
  el: HTMLElement,
  text: string,
  ranges: [number, number][] | undefined,
): void {
  if (ranges === undefined || ranges.length === 0) {
    el.textContent = text;
    return;
  }
  let rune = 0;
  let ri = 0;
  let plain = "";
  let lit = "";
  const flushPlain = () => {
    if (plain !== "") {
      el.append(plain);
      plain = "";
    }
  };
  const flushLit = () => {
    if (lit !== "") {
      const hl = document.createElement("span");
      hl.className = "hl";
      hl.textContent = lit;
      el.append(hl);
      lit = "";
    }
  };
  for (const ch of text) {
    // for..of iterates code points, matching Go's rune indices.
    while (ri < ranges.length && rune >= ranges[ri][1]) {
      ri++;
    }
    if (ri < ranges.length && rune >= ranges[ri][0] && rune < ranges[ri][1]) {
      flushPlain();
      lit += ch;
    } else {
      flushLit();
      plain += ch;
    }
    rune++;
  }
  flushPlain();
  flushLit();
}

// highlightedName renders a file row's name with the engine-minted
// match ranges lit up.
export function highlightedName(
  name: string,
  ranges: [number, number][] | undefined,
): HTMLSpanElement {
  const span = document.createElement("span");
  span.className = "name";
  appendHighlighted(span, name, ranges);
  return span;
}

// renderResults replaces container's children with one row per item
// and returns the row elements for the selection model to drive. The
// "No matches" empty state is owned by main.ts (the static #empty
// element): it depends on the plugin sections too, which arrive after
// the file response.
export function renderResults(
  container: HTMLElement,
  items: WailsSearchResult[],
  handlers: RowHandlers,
): HTMLDivElement[] {
  const rows: HTMLDivElement[] = [];
  const frag = document.createDocumentFragment();
  for (const item of items) {
    const row = document.createElement("div");
    row.className = "result";
    row.setAttribute("role", "option");

    const tpl = item.isDir ? folderTpl : fileTpl;
    row.append(tpl.content.cloneNode(true));
    row.append(highlightedName(item.name, item.matchRanges));

    const dir = document.createElement("span");
    dir.className = "dir";
    // A non-empty hint (the outside-indexed-roots note) takes the
    // parent-dir slot; everything else about the row stays identical.
    dir.textContent = item.hint ? item.hint : parentDir(item.path, item.name);
    dir.title = item.path;
    row.append(dir);

    row.addEventListener("click", (ev: MouseEvent) => {
      handlers.onActivate(row, ev.ctrlKey || ev.metaKey);
    });

    frag.append(row);
    rows.push(row);
  }
  container.replaceChildren(frag);
  return rows;
}

// applySelection toggles the .selected class and, when scroll is
// true, keeps the selected row scrolled into view. Only intentional
// navigation (keyboard, auto-select-first) scrolls: plugin-area
// re-renders pass false, so they can never move the viewport under
// the user's wheel scrolling.
export function applySelection(
  rows: HTMLDivElement[],
  selected: number,
  scroll = true,
): void {
  rows.forEach((row, i) => {
    row.classList.toggle("selected", i === selected);
  });
  if (!scroll) {
    return;
  }
  const row = rows[selected];
  if (row !== undefined) {
    row.scrollIntoView({ block: "nearest" });
  }
}

/* --- plugin sections ---------------------------------------------- */

// PluginSection is one provider's "plugin:results" emission for the
// current query generation, keyed by plugin id.
export interface PluginSection {
  plugin: string; // provider id (the RunPluginAction pluginId)
  name: string; // section header display name
  results: PluginResult[];
  // Source priority (Emission.priority; 0 when absent on the wire).
  // priority > 0 = the section renders in #priority-results ABOVE
  // the file rows; the value is registry-stamped metadata for builtin
  // sources -- external plugins can never carry one.
  priority: number;
}

// splitByPriority separates the sections for the two render zones:
// priority > 0 renders above the file rows, everything else keeps
// the classic below-files zone. Order within each group is left to
// compareSections at render time.
export function splitByPriority(sections: PluginSection[]): {
  priority: PluginSection[];
  normal: PluginSection[];
} {
  const priority: PluginSection[] = [];
  const normal: PluginSection[] = [];
  for (const s of sections) {
    (s.priority > 0 ? priority : normal).push(s);
  }
  return { priority, normal };
}

// compareSections orders sections within a zone, fully
// deterministically: source priority desc, then max result score
// desc, then plugin id.
export function compareSections(a: PluginSection, b: PluginSection): number {
  if (a.priority !== b.priority) {
    return b.priority - a.priority;
  }
  const byScore = sectionMaxScore(b) - sectionMaxScore(a);
  if (byScore !== 0) {
    return byScore;
  }
  return a.plugin < b.plugin ? -1 : a.plugin > b.plugin ? 1 : 0;
}

// PluginRowRef ties a rendered plugin row back to the data the
// selection model needs to activate it.
export interface PluginRowRef {
  pluginId: string;
  result: PluginResult;
}

const pluginSectionTpl = document.getElementById(
  "tpl-plugin-section",
) as HTMLTemplateElement;
const pluginRowTpl = document.getElementById(
  "tpl-plugin-row",
) as HTMLTemplateElement;

// Builtin icon names -> glyphs (ASCII-only source: escapes only).
// Unknown names fall back to the puzzle piece; an icon value that is
// not a name at all is a literal glyph/emoji (<= 32 bytes, already
// sanitized Go-side) and renders as-is.
const DEFAULT_GLYPH = "\u{1F9E9}"; // puzzle piece
const iconGlyphs = new Map<string, string>([
  ["calculator", "\u{1F9EE}"], // abacus
  ["globe", "\u{1F310}"],
  ["clock", "\u{1F550}"],
  ["star", "\u{2B50}"],
  ["info", "\u{2139}\u{FE0F}"],
  ["warning", "\u{26A0}\u{FE0F}"],
  ["link", "\u{1F517}"],
  ["terminal", "\u{1F4BB}"],
  ["text", "\u{1F4C4}"],
  ["hash", "#"], // plain ASCII: bang suggestions render "# !calc"
  ["bolt", "\u{26A1}"],
  ["app", "\u{1F680}"], // rocket -- the Launch provider
  ["puzzle", DEFAULT_GLYPH],
]);

const iconNameRe = /^[a-z0-9_-]+$/;

// iconGlyph resolves a result's icon field to the text to render.
export function iconGlyph(icon: string | undefined): string {
  if (icon === undefined || icon === "") {
    return DEFAULT_GLYPH;
  }
  if (!iconNameRe.test(icon)) {
    return icon; // literal glyph/emoji
  }
  return iconGlyphs.get(icon) ?? DEFAULT_GLYPH;
}

/* --- resolved icon images ------------------------------------------ */

// Rows whose result carries an iconKey (builtin app sources only; the
// sanitizer strips it from external plugins) render their glyph
// immediately and swap to the real icon once the batched ResolveIcons
// answer arrives. Requests batch per render tick via queueMicrotask,
// answers are cached frontend-side (misses too) so held keystrokes
// re-render without IPC churn, and a row replaced before its answer
// lands is simply skipped (isConnected). The <img> is built as a DOM
// node with a data:-URI src assigned as a property -- NOT a markup
// sink; the no-innerHTML rule is untouched.

// ICON_SIZE is the physical pixel size requested from Go: the row
// icon renders at 16 CSS px, and 64 stays crisp on HiDPI while small
// enough that data URIs stay a few KB.
const ICON_SIZE = 64;

const iconUriCache = new Map<string, string>(); // key -> data URI ("" = known miss)
let pendingIconEls = new Map<string, HTMLSpanElement[]>();
let iconFlushQueued = false;

// setIconImage swaps a glyph span's content for the resolved image.
function setIconImage(el: HTMLSpanElement, uri: string): void {
  const img = document.createElement("img");
  img.className = "plugin-icon-img";
  img.alt = "";
  img.draggable = false;
  img.src = uri;
  el.replaceChildren(img);
}

// requestIcon resolves key into el: synchronously from the cache, or
// via the next batch (the glyph already in el stands meanwhile).
function requestIcon(el: HTMLSpanElement, key: string): void {
  const cached = iconUriCache.get(key);
  if (cached !== undefined) {
    if (cached !== "") {
      setIconImage(el, cached);
    }
    return;
  }
  const els = pendingIconEls.get(key);
  if (els !== undefined) {
    els.push(el);
  } else {
    pendingIconEls.set(key, [el]);
  }
  if (!iconFlushQueued) {
    iconFlushQueued = true;
    queueMicrotask(flushIconRequests);
  }
}

// flushIconRequests ships one batch of pending keys to Go and fills
// the rows in as the answer arrives. Resolution failures (no binding
// yet, resolver unavailable) leave the glyphs standing.
function flushIconRequests(): void {
  iconFlushQueued = false;
  const batch = pendingIconEls;
  pendingIconEls = new Map();
  if (batch.size === 0) {
    return;
  }
  const app = window.go?.app.App;
  if (app === undefined) {
    return;
  }
  const keys = [...batch.keys()];
  app
    .ResolveIcons(keys, ICON_SIZE)
    .then((resolved) => {
      if (iconUriCache.size > 512) {
        iconUriCache.clear(); // crude bound; keys are per-app, small
      }
      for (const key of keys) {
        const uri = resolved?.[key] ?? "";
        iconUriCache.set(key, uri);
        if (uri === "") {
          continue; // known miss: the glyph stands
        }
        for (const el of batch.get(key) ?? []) {
          if (el.isConnected) {
            setIconImage(el, uri);
          }
        }
      }
    })
    .catch(() => {
      /* resolver unavailable: glyphs stand, keys stay uncached */
    });
}

// resultScore mirrors the Go sanitizer's default score.
function resultScore(r: PluginResult): number {
  return r.score ?? 50;
}

function sectionMaxScore(s: PluginSection): number {
  let max = 0;
  for (const r of s.results) {
    max = Math.max(max, resultScore(r));
  }
  return max;
}

// renderPluginSections replaces container's children with the plugin
// sections of ONE zone: an unselectable header per section (no
// handlers, not in the returned rows), then that section's rows.
// Sections order by compareSections (priority desc, max score desc,
// plugin id); results within a section by score desc then response
// order (Array.sort is stable). Rows resolve their selection index at
// event time (see RowHandlers), so zones can render independently.
export function renderPluginSections(
  container: HTMLElement,
  sections: PluginSection[],
  handlers: RowHandlers,
): { rows: HTMLDivElement[]; refs: PluginRowRef[] } {
  const rows: HTMLDivElement[] = [];
  const refs: PluginRowRef[] = [];
  const frag = document.createDocumentFragment();
  const orderedSections = [...sections].sort(compareSections);
  for (const section of orderedSections) {
    const secFrag = pluginSectionTpl.content.cloneNode(
      true,
    ) as DocumentFragment;
    const secEl = secFrag.firstElementChild as HTMLDivElement;
    const header = secEl.querySelector(
      ".plugin-section-header",
    ) as HTMLDivElement;
    header.textContent = section.name;
    const orderedResults = [...section.results].sort(
      (a, b) => resultScore(b) - resultScore(a),
    );
    for (const result of orderedResults) {
      const row = buildPluginRow(result, handlers);
      secEl.append(row);
      rows.push(row);
      refs.push({ pluginId: section.plugin, result });
    }
    frag.append(secEl);
  }
  container.replaceChildren(frag);
  return { rows, refs };
}

// buildPluginRow fills the plugin row template: [icon] title, dim
// subtitle, badge chip on the right, "label: value" fields beneath.
function buildPluginRow(
  result: PluginResult,
  handlers: RowHandlers,
): HTMLDivElement {
  const rowFrag = pluginRowTpl.content.cloneNode(true) as DocumentFragment;
  const row = rowFrag.firstElementChild as HTMLDivElement;

  const iconEl = row.querySelector(".plugin-icon") as HTMLSpanElement;
  iconEl.textContent = iconGlyph(result.icon);
  if (result.iconKey !== undefined && result.iconKey !== "") {
    requestIcon(iconEl, result.iconKey);
  }
  appendHighlighted(
    row.querySelector(".plugin-title") as HTMLSpanElement,
    result.title,
    result.matchRanges,
  );
  (row.querySelector(".plugin-subtitle") as HTMLSpanElement).textContent =
    result.subtitle ?? "";

  const badge = row.querySelector(".plugin-badge") as HTMLSpanElement;
  if (result.badge !== undefined && result.badge !== "") {
    badge.textContent = result.badge;
    badge.hidden = false;
  }

  const fieldsEl = row.querySelector(".plugin-fields") as HTMLDivElement;
  const fields = result.fields ?? [];
  if (fields.length > 0) {
    for (const f of fields) {
      const span = document.createElement("span");
      span.className = "plugin-field";
      span.textContent = f.label + ": " + f.value;
      fieldsEl.append(span);
    }
    fieldsEl.hidden = false;
  }

  // The ONLY styling channel a plugin gets: the --plugin-accent custom
  // property, consumed by style.css with theme-token fallbacks. Never
  // inline literal colors/backgrounds/borders here.
  if (result.accent_color !== undefined && result.accent_color !== "") {
    row.style.setProperty("--plugin-accent", result.accent_color);
  }

  row.addEventListener("click", (ev: MouseEvent) => {
    handlers.onActivate(row, ev.ctrlKey || ev.metaKey);
  });
  return row;
}
