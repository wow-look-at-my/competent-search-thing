// DOM construction for the results list: one row per hit with a
// folder/file glyph (cloned from the <template> elements in
// index.html), the entry name with the matched substring highlighted,
// and the dimmed parent directory.

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
// and returns the row elements for the selection model to drive.
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
    dir.textContent = parentDir(item.path, item.name);
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
  if (items.length === 0 && query.trim() !== "") {
    const empty = document.createElement("div");
    empty.className = "empty";
    empty.textContent = "No matches";
    frag.append(empty);
  }
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
