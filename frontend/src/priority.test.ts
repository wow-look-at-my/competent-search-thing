// The frontend ordering gate for "apps above file results": priority
// sections render in the zone ABOVE the file rows, the flat selection
// traverses priority -> files -> below-zone in document order, and a
// late-arriving priority emission takes row 0 only while the user has
// not navigated. Deterministic, DOM-light: the real index.html markup
// is loaded by test-setup.ts, the pure split/comparator/reconcile
// rules are exercised directly.

import { describe, expect, it } from "vitest";
import {
  compareSections,
  renderPluginSections,
  renderResults,
  splitByPriority,
} from "./render";
import type { PluginSection, RowHandlers } from "./render";
import { reconcileSelection } from "./selection";

const handlers: RowHandlers = { onHover: () => {}, onActivate: () => {} };

function section(
  plugin: string,
  priority: number,
  titles: string[],
  score = 50,
): PluginSection {
  return {
    plugin,
    name: plugin,
    priority,
    results: titles.map((t) => ({ title: t, score })),
  };
}

function fileItem(name: string): WailsSearchResult {
  return { path: "/tmp/" + name, name, isDir: false };
}

// precedes asserts a strictly earlier document position.
function precedes(a: Element, b: Element): boolean {
  return (
    (a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0
  );
}

function zones(): { priorityEl: HTMLElement; fileEl: HTMLElement; pluginEl: HTMLElement } {
  const priorityEl = document.getElementById("priority-results");
  const fileEl = document.getElementById("file-results");
  const pluginEl = document.getElementById("plugin-results");
  if (priorityEl === null || fileEl === null || pluginEl === null) {
    throw new Error("index.html is missing a result zone");
  }
  return { priorityEl, fileEl, pluginEl };
}

describe("priority zones", () => {
  it("index.html nests the priority zone above the file zone", () => {
    const { priorityEl, fileEl, pluginEl } = zones();
    expect(precedes(priorityEl, fileEl)).toBe(true);
    expect(precedes(fileEl, pluginEl)).toBe(true);
  });

  it("renders priority section rows above file rows above plugin rows", () => {
    const { priorityEl, fileEl, pluginEl } = zones();
    const fileRows = renderResults(fileEl, [fileItem("firefly.txt")], handlers);
    const split = splitByPriority([
      section("windows", 0, ["Firefox -- window"]),
      section("apps-search", 1, ["Firefox"]),
    ]);
    const above = renderPluginSections(priorityEl, split.priority, handlers);
    const below = renderPluginSections(pluginEl, split.normal, handlers);

    expect(above.rows).toHaveLength(1);
    expect(below.rows).toHaveLength(1);
    expect(above.refs[0].pluginId).toBe("apps-search");
    expect(precedes(above.rows[0], fileRows[0])).toBe(true);
    expect(precedes(fileRows[0], below.rows[0])).toBe(true);
  });

  it("flat selection order (priority, files, below) is document order", () => {
    const { priorityEl, fileEl, pluginEl } = zones();
    const fileRows = renderResults(
      fileEl,
      [fileItem("a.txt"), fileItem("b.txt")],
      handlers,
    );
    const split = splitByPriority([
      section("apps-search", 1, ["Firefox", "Files"]),
      section("firefox-tabs", 0, ["Tab one", "Tab two"]),
    ]);
    const above = renderPluginSections(priorityEl, split.priority, handlers);
    const below = renderPluginSections(pluginEl, split.normal, handlers);

    // The combined selection array (priority rows, file rows, plugin
    // rows) must already BE document order -- the invariant the flat
    // ArrowUp/Down model relies on.
    const combined = above.rows.concat(fileRows, below.rows);
    expect(combined).toHaveLength(6);
    for (let i = 0; i + 1 < combined.length; i++) {
      expect(precedes(combined[i], combined[i + 1])).toBe(true);
    }
  });

  it("splitByPriority partitions and compareSections orders deterministically", () => {
    const apps = section("apps-search", 1, ["Firefox"], 73);
    const tabs = section("firefox-tabs", 0, ["Fire tab"], 85);
    const wins = section("windows", 0, ["Fire win"], 85);
    const sites = section("firefox-frequent", 0, ["fire.dev"], 95);

    const split = splitByPriority([tabs, apps, wins, sites]);
    expect(split.priority.map((s) => s.plugin)).toEqual(["apps-search"]);
    expect(split.normal.map((s) => s.plugin)).toEqual([
      "firefox-tabs",
      "windows",
      "firefox-frequent",
    ]);

    // priority desc, then max score desc, then plugin id.
    const ordered = [tabs, wins, sites, apps].sort(compareSections);
    expect(ordered.map((s) => s.plugin)).toEqual([
      "apps-search",
      "firefox-frequent",
      "firefox-tabs",
      "windows",
    ]);
  });
});

describe("late priority emission selection", () => {
  const base = { queryBlank: false, userNavigated: false };

  it("auto-selects row 0 while the user has not navigated", () => {
    // Files rendered first: row 0 is the first file.
    expect(
      reconcileSelection({ ...base, prevItemIndex: 0, prevSelected: 0, rowCount: 1 }),
    ).toBe(0);
    // The apps emission prepends a row; the previously selected file
    // is now index 1, but auto-select re-runs: the app takes row 0.
    expect(
      reconcileSelection({ ...base, prevItemIndex: 1, prevSelected: 0, rowCount: 2 }),
    ).toBe(0);
  });

  it("preserves a navigated user's item by identity", () => {
    // The user arrowed onto the file; the apps row prepends; the
    // selection follows the file to its shifted index.
    expect(
      reconcileSelection({
        prevItemIndex: 1,
        prevSelected: 0,
        rowCount: 2,
        queryBlank: false,
        userNavigated: true,
      }),
    ).toBe(1);
    // The selected item vanished: clamp the raw index into range.
    expect(
      reconcileSelection({
        prevItemIndex: -1,
        prevSelected: 5,
        rowCount: 3,
        queryBlank: false,
        userNavigated: true,
      }),
    ).toBe(2);
    // Navigated but the list emptied: nothing to select.
    expect(
      reconcileSelection({
        prevItemIndex: -1,
        prevSelected: 2,
        rowCount: 0,
        queryBlank: false,
        userNavigated: true,
      }),
    ).toBe(-1);
  });

  it("never auto-selects the blank-query cheat sheet", () => {
    expect(
      reconcileSelection({
        prevItemIndex: -1,
        prevSelected: -1,
        rowCount: 4,
        queryBlank: true,
        userNavigated: false,
      }),
    ).toBe(-1);
  });
});
