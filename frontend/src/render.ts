// DOM construction for the results list: one row per file hit with a
// folder/file glyph (cloned from the <template> elements in
// index.html), the entry name with the matched substring highlighted,
// and the dimmed parent directory -- plus the plugin sections that
// render below the file rows (header + rows cloned from templates,
// icon glyph map, whitelisted --plugin-accent styling hook). Pure
// text-node builders throughout: nothing here can inject markup.

export interface RowHandlers {
  onHover(index: number): void;
  onActivate(index: number, reveal: boolean): void;
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

// highlightedName renders name with the first case-insensitive
// occurrence of query wrapped in a .hl span. Plain text nodes
// everywhere: nothing here can inject markup.
export function highlightedName(name: string, query: string): HTMLSpanElement {
  const span = document.createElement("span");
  span.className = "name";
  const q = query.trim().toLowerCase();
  const at = q === "" ? -1 : name.toLowerCase().indexOf(q);
  if (at < 0) {
    span.textContent = name;
    return span;
  }
  span.append(name.slice(0, at));
  const hl = document.createElement("span");
  hl.className = "hl";
  hl.textContent = name.slice(at, at + q.length);
  span.append(hl, name.slice(at + q.length));
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
  query: string,
  handlers: RowHandlers,
): HTMLDivElement[] {
  const rows: HTMLDivElement[] = [];
  const frag = document.createDocumentFragment();
  items.forEach((item, i) => {
    const row = document.createElement("div");
    row.className = "result";
    row.setAttribute("role", "option");

    const tpl = item.isDir ? folderTpl : fileTpl;
    row.append(tpl.content.cloneNode(true));
    row.append(highlightedName(item.name, query));

    const dir = document.createElement("span");
    dir.className = "dir";
    // A non-empty hint (the outside-indexed-roots note) takes the
    // parent-dir slot; everything else about the row stays identical.
    dir.textContent = item.hint ? item.hint : parentDir(item.path, item.name);
    dir.title = item.path;
    row.append(dir);

    row.addEventListener("mouseenter", () => {
      handlers.onHover(i);
    });
    row.addEventListener("click", (ev: MouseEvent) => {
      handlers.onActivate(i, ev.ctrlKey || ev.metaKey);
    });

    frag.append(row);
    rows.push(row);
  });
  container.replaceChildren(frag);
  return rows;
}

// applySelection toggles the .selected class and keeps the selected
// row scrolled into view.
export function applySelection(rows: HTMLDivElement[], selected: number): void {
  rows.forEach((row, i) => {
    row.classList.toggle("selected", i === selected);
  });
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
// sections: an unselectable header per section (no handlers, not in
// the returned rows), then that section's rows. Sections order by max
// result score desc then plugin id; results within a section by score
// desc then response order (Array.sort is stable). Handler indices
// continue the flat selection model after the file rows, so callers
// pass startIndex = number of file rows currently rendered.
export function renderPluginSections(
  container: HTMLElement,
  sections: PluginSection[],
  startIndex: number,
  handlers: RowHandlers,
): { rows: HTMLDivElement[]; refs: PluginRowRef[] } {
  const rows: HTMLDivElement[] = [];
  const refs: PluginRowRef[] = [];
  const frag = document.createDocumentFragment();
  const orderedSections = [...sections].sort((a, b) => {
    const byScore = sectionMaxScore(b) - sectionMaxScore(a);
    if (byScore !== 0) {
      return byScore;
    }
    return a.plugin < b.plugin ? -1 : a.plugin > b.plugin ? 1 : 0;
  });
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
      const row = buildPluginRow(result, startIndex + rows.length, handlers);
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
  index: number,
  handlers: RowHandlers,
): HTMLDivElement {
  const rowFrag = pluginRowTpl.content.cloneNode(true) as DocumentFragment;
  const row = rowFrag.firstElementChild as HTMLDivElement;

  (row.querySelector(".plugin-icon") as HTMLSpanElement).textContent =
    iconGlyph(result.icon);
  (row.querySelector(".plugin-title") as HTMLSpanElement).textContent =
    result.title;
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

  row.addEventListener("mouseenter", () => {
    handlers.onHover(index);
  });
  row.addEventListener("click", (ev: MouseEvent) => {
    handlers.onActivate(index, ev.ctrlKey || ev.metaKey);
  });
  return row;
}
