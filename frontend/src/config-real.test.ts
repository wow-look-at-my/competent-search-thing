// The ToC over the SHIPPED schemas/config.schema.json (a separate
// test file: vitest gives it a fresh module registry, so config.ts's
// cached schema is this one). Pins the real-world sidebar: every
// top-level section in schema order, sub-entries for the nested
// object sections, the synthetic General group for the leading
// top-level leaves, rewrites (a leaf AFTER the sections) as its own
// trailing group, and the annotated rootsVersion/$schema absent.

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { beforeAll, describe, expect, it } from "vitest";
import { configModeActive, initConfig } from "./config";

const here = dirname(fileURLToPath(import.meta.url));
const schemaJson = readFileSync(
  join(here, "..", "..", "schemas", "config.schema.json"),
  "utf-8",
);

function tick(): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, 0);
  });
}

let fire: (name: string) => void;

beforeAll(() => {
  const events = new Map<string, (...data: unknown[]) => void>();
  const app = {
    GetConfigSchema: () => Promise.resolve(schemaJson),
    GetConfigForEdit: () =>
      Promise.resolve({ configJson: "{}", path: "/tmp/config.json" }),
  } as unknown as WailsAppBindings;
  const rt = {
    EventsOn: (name: string, cb: (...data: unknown[]) => void) => {
      events.set(name, cb);
      return () => {};
    },
  } as unknown as WailsRuntime;
  initConfig(app, rt);
  fire = (name) => {
    const cb = events.get(name);
    if (cb === undefined) {
      throw new Error("no handler registered for " + name);
    }
    cb();
  };
});

describe("config editor ToC over the shipped schema", () => {
  it("covers every section and sub-section in schema order", async () => {
    fire("config:open");
    await tick();
    await tick();
    expect(configModeActive()).toBe(true);
    const entries = Array.from(
      document.querySelectorAll<HTMLButtonElement>(
        "#config-toc .config-toc-entry",
      ),
    ).map((b) => ({
      dotted: b.id.replace(/^config-toc-/, ""),
      sub: b.classList.contains("config-toc-sub"),
    }));
    expect(entries).toEqual([
      { dotted: "general", sub: false },
      { dotted: "search", sub: false },
      { dotted: "search.frecency", sub: true },
      { dotted: "search.priors", sub: true },
      { dotted: "search.telemetry", sub: true },
      { dotted: "search.arbiter", sub: true },
      { dotted: "watcher", sub: false },
      { dotted: "plugins", sub: false },
      { dotted: "bangs", sub: false },
      { dotted: "tray", sub: false },
      { dotted: "history", sub: false },
      { dotted: "stats", sub: false },
      { dotted: "window", sub: false },
      { dotted: "firefox", sub: false },
      { dotted: "firefox.frequentSites", sub: true },
      { dotted: "firefox.openTabs", sub: true },
      { dotted: "preview", sub: false },
      { dotted: "preview.kagi", sub: true },
      { dotted: "preview.openai", sub: true },
      { dotted: "rewrites", sub: false },
    ]);
  });

  it("keeps the annotated app-managed keys out of the editor", () => {
    expect(document.getElementById("cfg-rootsVersion")).toBeNull();
    expect(document.getElementById("cfg-$schema")).toBeNull();
    // The leading leaves live in the General group.
    const general = document.getElementById("config-sec-general");
    expect(general).not.toBeNull();
    expect(general?.querySelector("#cfg-roots")).not.toBeNull();
    expect(general?.querySelector("#cfg-theme")).not.toBeNull();
    // rewrites renders as its own trailing group.
    const rewrites = document.getElementById("config-sec-rewrites");
    expect(rewrites?.querySelector("#cfg-rewrites")).not.toBeNull();
  });
});
