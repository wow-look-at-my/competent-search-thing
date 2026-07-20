// File-row icon resolution: the key construction ("dir" /
// "file:<basename>"), the one batched ResolveIcons request the
// rendered rows fire, the img swap-in on a hit, the standing template
// glyph on a miss, and clearIconCache forcing a re-request (the
// theme:changed hook -- Go answers are theme-variant aware).

import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { clearIconCache, fileIconKey, renderResults } from "./render";
import type { RowHandlers } from "./render";

const handlers: RowHandlers = { onActivate: () => {} };

function zone(): HTMLElement {
  const el = document.getElementById("file-results");
  if (el === null) {
    throw new Error("index.html is missing #file-results");
  }
  return el;
}

function item(name: string, isDir = false): WailsSearchResult {
  return { path: "/tmp/" + name, name, isDir };
}

// settle lets the queueMicrotask batch and the ResolveIcons promise
// chain run to completion.
async function settle(): Promise<void> {
  await new Promise((r) => setTimeout(r, 0));
}

let calls: string[][] = [];

function install(answers: Record<string, string>): void {
  calls = [];
  const app = {
    ResolveIcons: (keys: string[]) => {
      calls.push([...keys]);
      const out: Record<string, string> = {};
      for (const k of keys) {
        const uri = answers[k];
        if (uri !== undefined) {
          out[k] = uri;
        }
      }
      return Promise.resolve(out);
    },
  } as unknown as WailsAppBindings;
  window.go = { app: { App: app } };
}

const URI = "data:image/svg+xml;base64,c3Zn";

describe("file row icons", () => {
  beforeEach(() => {
    clearIconCache();
  });
  afterEach(() => {
    window.go = undefined;
    zone().replaceChildren();
  });

  it("builds the wire keys", () => {
    expect(fileIconKey("main.go", false)).toBe("file:main.go");
    expect(fileIconKey("Documents", true)).toBe("dir");
  });

  it("batches one request per render and swaps resolved images in", async () => {
    install({ "file:main.go": URI });
    const rows = renderResults(zone(), [item("main.go"), item("Docs", true)], handlers);
    expect(rows).toHaveLength(2);
    // The template glyph stands synchronously on every row.
    expect(rows[0].querySelector(".file-icon svg.icon")).not.toBeNull();
    expect(rows[1].querySelector(".file-icon svg.icon")).not.toBeNull();

    await settle();
    expect(calls).toHaveLength(1);
    expect([...calls[0]].sort()).toEqual(["dir", "file:main.go"]);
    const img = rows[0].querySelector<HTMLImageElement>(".file-icon img.plugin-icon-img");
    expect(img?.src).toBe(URI);
    // The dir key missed: its glyph stands.
    expect(rows[1].querySelector(".file-icon svg.icon")).not.toBeNull();
    expect(rows[1].querySelector("img")).toBeNull();
  });

  it("serves repeat renders from the cache, and clearIconCache re-asks", async () => {
    install({ "file:main.go": URI });
    renderResults(zone(), [item("main.go")], handlers);
    await settle();
    expect(calls).toHaveLength(1);

    const rows = renderResults(zone(), [item("main.go")], handlers);
    await settle();
    expect(calls).toHaveLength(1);
    expect(
      rows[0].querySelector<HTMLImageElement>(".file-icon img.plugin-icon-img")?.src,
    ).toBe(URI);

    clearIconCache(); // the theme:changed hook
    renderResults(zone(), [item("main.go")], handlers);
    await settle();
    expect(calls).toHaveLength(2);
  });
});
