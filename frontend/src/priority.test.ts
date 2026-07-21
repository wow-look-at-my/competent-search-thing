// The frontend ordering gate for "file results default to last": ALL
// plugin sections -- weak and strong alike -- render in the zone
// ABOVE the file rows (the 2026-07-21 ruling; sectionAboveFiles is
// the one predicate deciding it, and the below zone is retained
// empty as the weak-sections-below veto variant's home), sections
// order among themselves by compareSections (priority desc, max
// score desc, plugin id -- strong promoted sections still lead), the
// flat selection traverses sections -> files -> below-zone in
// document order, and a late-arriving emission takes row 0 only
// while the user has not navigated. Deterministic, DOM-light: the
// real index.html markup is loaded by test-setup.ts, the pure
// split/comparator/reconcile rules are exercised directly.

import { describe, expect, it } from "vitest";
import {
  compareSections,
  renderPluginSections,
  renderResults,
  sectionAboveFiles,
  splitByPriority,
} from "./render";
import type { PluginSection, RowHandlers } from "./render";
import { reconcileSelection } from "./selection";

const handlers: RowHandlers = { onActivate: () => {} };

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

describe("files-last zones", () => {
  it("index.html nests the priority zone above the file zone", () => {
    const { priorityEl, fileEl, pluginEl } = zones();
    expect(precedes(priorityEl, fileEl)).toBe(true);
    expect(precedes(fileEl, pluginEl)).toBe(true);
  });

  it("sectionAboveFiles routes EVERY section above the files", () => {
    // The one predicate behind the files-last default: weak
    // (priority 0) and strong (priority 1) sections alike render
    // above the file rows. The veto variant flips exactly this
    // predicate back to `s.priority > 0` -- these asserts are the
    // pins that variant rewrites.
    expect(sectionAboveFiles(section("windows", 0, ["Weak win"]))).toBe(true);
    expect(sectionAboveFiles(section("apps-search", 1, ["Strong app"]))).toBe(
      true,
    );
  });

  it("renders weak and strong section rows above the file rows", () => {
    const { priorityEl, fileEl, pluginEl } = zones();
    const fileRows = renderResults(fileEl, [fileItem("firefly.txt")], handlers);
    const split = splitByPriority([
      section("windows", 0, ["Firefox -- window"]),
      section("apps-search", 1, ["Firefox"]),
    ]);
    // Both sections -- the weak priority-0 one included -- land in
    // the above-files group; the below zone renders empty (retained
    // for the veto variant).
    expect(split.normal).toHaveLength(0);
    const above = renderPluginSections(priorityEl, split.priority, handlers);
    const below = renderPluginSections(pluginEl, split.normal, handlers);

    expect(above.rows).toHaveLength(2);
    expect(below.rows).toHaveLength(0);
    // compareSections still leads with the strong section.
    expect(above.refs[0].pluginId).toBe("apps-search");
    expect(above.refs[1].pluginId).toBe("windows");
    // Files come LAST: every section row precedes the first file row.
    expect(precedes(above.rows[above.rows.length - 1], fileRows[0])).toBe(
      true,
    );
  });

  it("flat selection order is sections then files -- row 0 is a plugin row", () => {
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

    // The combined selection array (section rows, file rows, the
    // empty below zone) must already BE document order -- the
    // invariant the flat ArrowUp/Down model relies on.
    const combined = above.rows.concat(fileRows, below.rows);
    expect(combined).toHaveLength(6);
    for (let i = 0; i + 1 < combined.length; i++) {
      expect(precedes(combined[i], combined[i + 1])).toBe(true);
    }
    // With sections present, row 0 is the FIRST PLUGIN ROW (the
    // Spotlight-style consequence of files-last) and the file rows
    // sit at the traversal's tail; auto-select therefore lands on
    // the first plugin row while the user has not navigated.
    expect(combined[0]).toBe(above.rows[0]);
    expect(combined[combined.length - 1]).toBe(fileRows[1]);
    expect(
      reconcileSelection({
        prevItemIndex: -1,
        prevSelected: -1,
        rowCount: combined.length,
        queryBlank: false,
        userNavigated: false,
      }),
    ).toBe(0);
  });

  it("orders apps + tabs + sites + a weak section deterministically", () => {
    // Three strong (priority 1) sections and one weak (priority 0)
    // section all render above the files together: priority desc
    // first (strong sections lead even against a higher-scoring weak
    // one), then max score desc, then plugin id.
    const { priorityEl, fileEl } = zones();
    const fileRows = renderResults(
      fileEl,
      [fileItem("tampermonkey.txt")],
      handlers,
    );
    const apps = section("apps-search", 1, ["Tamper App"], 73);
    const tabs = section("firefox-tabs", 1, ["Tampermonkey - Open tab"], 83);
    const sites = section("firefox-frequent", 1, ["tampermonkey.net"], 83);
    const wins = section("windows", 0, ["Tampermonkey window"], 85);

    const split = splitByPriority([tabs, apps, wins, sites]);
    expect(split.priority.map((s) => s.plugin)).toEqual([
      "firefox-tabs",
      "apps-search",
      "windows",
      "firefox-frequent",
    ]);
    expect(split.normal).toHaveLength(0);

    // Priority 1 leads over priority 0 regardless of score (the
    // 85-scoring windows section renders after every promoted one);
    // equal priority orders by max score desc (tabs/sites 83 over
    // apps 73), score ties by plugin id ("firefox-frequent" <
    // "firefox-tabs").
    const ordered = [tabs, apps, wins, sites].sort(compareSections);
    expect(ordered.map((s) => s.plugin)).toEqual([
      "firefox-frequent",
      "firefox-tabs",
      "apps-search",
      "windows",
    ]);

    // The rendered zone applies the same order, above the file rows.
    const above = renderPluginSections(priorityEl, split.priority, handlers);
    expect(above.refs.map((r) => r.pluginId)).toEqual([
      "firefox-frequent",
      "firefox-tabs",
      "apps-search",
      "windows",
    ]);
    expect(precedes(above.rows[above.rows.length - 1], fileRows[0])).toBe(
      true,
    );
  });

  it("splitByPriority routes all sections above; compareSections is unchanged", () => {
    const apps = section("apps-search", 1, ["Firefox"], 73);
    const tabs = section("firefox-tabs", 0, ["Fire tab"], 85);
    const wins = section("windows", 0, ["Fire win"], 85);
    const sites = section("firefox-frequent", 0, ["fire.dev"], 95);

    const split = splitByPriority([tabs, apps, wins, sites]);
    expect(split.priority.map((s) => s.plugin)).toEqual([
      "firefox-tabs",
      "apps-search",
      "windows",
      "firefox-frequent",
    ]);
    expect(split.normal).toEqual([]);

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

describe("late emission selection", () => {
  const base = { queryBlank: false, userNavigated: false };

  it("auto-selects row 0 while the user has not navigated", () => {
    // Files rendered first: row 0 is the first file.
    expect(
      reconcileSelection({ ...base, prevItemIndex: 0, prevSelected: 0, rowCount: 1 }),
    ).toBe(0);
    // A section emission prepends rows above the files; the
    // previously selected file is now index 1, but auto-select
    // re-runs: the first plugin row takes row 0.
    expect(
      reconcileSelection({ ...base, prevItemIndex: 1, prevSelected: 0, rowCount: 2 }),
    ).toBe(0);
  });

  it("preserves a navigated user's item by identity", () => {
    // The user arrowed onto the file; a section row prepends; the
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
