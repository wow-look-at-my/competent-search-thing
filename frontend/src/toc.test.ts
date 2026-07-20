// The ToC scroll-sync selection math (toc.ts activeSectionIndex) --
// pure over (offsets, scrollTop, viewport, contentHeight), so the
// boundary behavior is pinned without a layout engine -- plus the
// x-editor-hidden annotations the shipped config.schema.json must
// carry (the editor has no hard-coded hidden-key list anymore).

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { activeSectionIndex } from "./toc";

describe("activeSectionIndex", () => {
  it("returns -1 for an empty section list", () => {
    expect(activeSectionIndex([], 0, 500, 1000)).toBe(-1);
  });

  it("selects the first section at the top", () => {
    expect(activeSectionIndex([0, 100, 200], 0, 500, 1000)).toBe(0);
  });

  it("clamps to the first section while scrolled above it", () => {
    expect(activeSectionIndex([50, 100], 0, 500, 1000)).toBe(0);
  });

  it("selects the last section whose top passed the viewport top", () => {
    expect(activeSectionIndex([0, 100, 200], 120, 100, 1000)).toBe(1);
    expect(activeSectionIndex([0, 100, 200], 100, 100, 1000)).toBe(1);
    expect(activeSectionIndex([0, 100, 200], 210, 100, 1000)).toBe(2);
  });

  it("applies the slack band at a section boundary", () => {
    // Default slack 8: a section within 8px of the viewport top
    // already counts as reached; beyond that it does not.
    expect(activeSectionIndex([0, 100, 200], 95, 100, 1000)).toBe(1);
    expect(activeSectionIndex([0, 100, 200], 91, 100, 1000)).toBe(0);
  });

  it("activates a short trailing section at the scroll bottom", () => {
    // The last section starts at 980 in a 1000px document with a
    // 500px viewport: its top can never reach the viewport top
    // (scrollTop maxes out at 500). At the bottom it must win anyway.
    expect(activeSectionIndex([0, 100, 980], 500, 500, 1000)).toBe(2);
    // Mid-scroll the base rule still applies.
    expect(activeSectionIndex([0, 100, 980], 300, 500, 1000)).toBe(1);
  });

  it("never triggers the bottom rule when the content fits", () => {
    expect(activeSectionIndex([0, 100], 0, 500, 400)).toBe(0);
  });
});

describe("config.schema.json editor annotations", () => {
  interface SchemaDoc {
    properties: Record<string, Record<string, unknown>>;
    $defs: Record<string, { properties: Record<string, Record<string, unknown>> }>;
  }
  const loadSchema = (): SchemaDoc => {
    const here = dirname(fileURLToPath(import.meta.url));
    const raw = readFileSync(
      join(here, "..", "..", "schemas", "config.schema.json"),
      "utf-8",
    );
    return JSON.parse(raw) as SchemaDoc;
  };

  it("marks rootsVersion and $schema x-editor-hidden", () => {
    const doc = loadSchema();
    expect(doc.properties.rootsVersion["x-editor-hidden"]).toBe(true);
    expect(doc.properties.$schema["x-editor-hidden"]).toBe(true);
  });

  it("hides the drag-managed window sizes from the editor", () => {
    // window.width/height and preview.windowWidth/windowHeight are
    // set by dragging the bar's edges (resize.ts); the editor rows
    // would fight the drag, so the schema hides them while the keys
    // stay hand-editable in config.json.
    const doc = loadSchema();
    const win = doc.$defs.windowConfig.properties;
    expect(win.width["x-editor-hidden"]).toBe(true);
    expect(win.height["x-editor-hidden"]).toBe(true);
    expect(win.translucent["x-editor-hidden"]).toBeUndefined();
    const pv = doc.$defs.previewConfig.properties;
    expect(pv.windowWidth["x-editor-hidden"]).toBe(true);
    expect(pv.windowHeight["x-editor-hidden"]).toBe(true);
    expect(pv.enabled["x-editor-hidden"]).toBeUndefined();
  });
});
