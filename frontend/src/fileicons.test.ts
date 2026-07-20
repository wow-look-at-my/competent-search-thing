// The file-type icon gate: pins the fileicons matcher semantics
// (pack-order first-match: special filenames and compound suffixes
// beat generic extensions), the light/dark motif rule, and the
// INTEGRITY of the committed data: every data.json rule's codepoint
// must exist in its committed font's cmap (parsed by the same
// tools/woff2cmap.mjs the generator used -- a hand-edit of either
// side fails here), the fonts stay inside their byte budget, and
// data.json stays pure ASCII.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import data from "./fileicons/data.json";
import { fileIcon, isLightBackground } from "./fileicons/fileicons";
import { cmapCodepoints } from "./fileicons/tools/woff2cmap.mjs";

const glyph = (cp: number): string => String.fromCodePoint(cp);

describe("fileIcon matching", () => {
  it("resolves a simple extension rule with its colour pair", () => {
    const icon = fileIcon("main.go", false);
    expect(icon.font).toBe("fi");
    expect(icon.glyph).toBe(glyph(60078));
    expect(icon.colorDark).toBe("#6a9fb5");
    expect(icon.colorLight).toBe("#6a9fb5");
  });

  it("suffix matching is case-insensitive", () => {
    expect(fileIcon("MAIN.GO", false)).toEqual(fileIcon("main.go", false));
  });

  it("special-filename rules beat the generic extension", () => {
    // webpack.config.js hits the webpack regex, not the plain .js
    // suffix rule that sits later in pack-priority order.
    const webpack = fileIcon("webpack.config.js", false);
    expect(webpack.font).toBe("fi");
    expect(webpack.glyph).toBe(glyph(60001));
    const js = fileIcon("app.js", false);
    expect(js.font).toBe("mf");
    expect(js.glyph).toBe(glyph(61737));
  });

  it("compound suffixes beat the plain extension", () => {
    // .huskyrc.json outranks .json in the pack's priority order.
    expect(fileIcon("x.huskyrc.json", false).glyph).toBe(glyph(128054));
    expect(fileIcon("data.json", false).glyph).toBe(glyph(60094));
  });

  it("regex rules test the raw basename with authored flags", () => {
    // ^Makefile is authored case-sensitive and unanchored at the end.
    const mk = fileIcon("Makefile", false);
    expect(mk.font).toBe("oct");
    expect(mk.glyph).toBe(glyph(61558));
  });

  it("unknown files get the uncolored octicons default", () => {
    const icon = fileIcon("mystery.zzzz", false);
    expect(icon.font).toBe("oct");
    expect(icon.glyph).toBe(glyph(61457)); // file-text
    expect(icon.colorDark).toBeUndefined();
    expect(icon.colorLight).toBeUndefined();
  });

  it("directories match dir rules, else the folder default", () => {
    const github = fileIcon(".github", true);
    expect(github.font).toBe("oct");
    expect(github.glyph).toBe(glyph(61450));
    const plain = fileIcon("somedir", true);
    expect(plain.font).toBe("oct");
    expect(plain.glyph).toBe(glyph(61462)); // file-directory
    expect(plain.colorDark).toBeUndefined();
  });

  it("dir and file namespaces are distinct", () => {
    // A directory named main.go must not take the file rule.
    expect(fileIcon("main.go", true).glyph).toBe(glyph(61462));
  });

  it("memoizes resolutions", () => {
    expect(fileIcon("repeat.css", false)).toBe(fileIcon("repeat.css", false));
  });
});

describe("isLightBackground", () => {
  it("classifies the builtin theme backgrounds", () => {
    expect(isLightBackground("#18181c")).toBe(false); // dark.json bg
    expect(isLightBackground("#f7f7f9")).toBe(true); // light.json bg
  });

  it("handles the other validator-admitted color forms", () => {
    expect(isLightBackground("#fff")).toBe(true);
    expect(isLightBackground("#000")).toBe(false);
    expect(isLightBackground("rgb(247, 247, 249)")).toBe(true);
    expect(isLightBackground("rgba(24, 24, 28, 0.97)")).toBe(false);
    expect(isLightBackground("hsl(240, 8%, 95%)")).toBe(true);
    expect(isLightBackground("hsl(240, 8%, 9%)")).toBe(false);
  });

  it("treats unparseable values as dark (the safe default)", () => {
    expect(isLightBackground("")).toBe(false);
    expect(isLightBackground("bogus")).toBe(false);
    expect(isLightBackground("url(x)")).toBe(false);
  });
});

describe("theme wiring", () => {
  it("initTheme toggles html.icons-light from the applied bg token", async () => {
    const { initTheme } = await import("./theme");
    const rt = { EventsOn: () => () => {} } as unknown as WailsRuntime;
    const appWith = (bg: string): WailsAppBindings =>
      ({
        GetTheme: () => Promise.resolve({ bg }),
        GetCustomCSS: () => Promise.resolve(""),
      }) as unknown as WailsAppBindings;
    const settle = (): Promise<void> =>
      new Promise((resolve) => setTimeout(resolve, 0));
    initTheme(appWith("#f7f7f9"), rt); // the light builtin's bg
    await settle();
    expect(document.documentElement.classList.contains("icons-light")).toBe(
      true,
    );
    initTheme(appWith("#18181c"), rt); // the dark builtin's bg
    await settle();
    expect(document.documentElement.classList.contains("icons-light")).toBe(
      false,
    );
  });
});

/* --- committed-data integrity -------------------------------------- */

interface RawRule {
  i: [string, number];
  r?: string;
  f?: string;
  s?: string;
  c?: [string, string];
}

const fontFiles: Record<string, string> = {
  fi: "file-icons.woff2",
  fa: "fontawesome.woff2",
  mf: "mfixx.woff2",
  oct: "octicons.woff2",
};

// Byte budgets: the committed sizes plus slack -- a re-vendor that
// balloons a font (or sneaks a fifth in through data.json) trips here
// before it rides the embedded dist.
const fontByteCap: Record<string, number> = {
  fi: 240000,
  fa: 90000,
  mf: 40000,
  oct: 32000,
};
const totalByteCap = 350000;

// Paths resolve from the vitest root (frontend/) -- import.meta.url
// is a rootless serve URL inside transformed test modules, so the
// test-setup.ts fileURLToPath pattern does not apply here.
const fileiconsDir = join(process.cwd(), "src", "fileicons");

function fontBytes(name: string): Buffer {
  return readFileSync(join(fileiconsDir, "fonts", name));
}

describe("committed data integrity", () => {
  const cmaps = new Map<string, Set<number>>();
  for (const [cls, file] of Object.entries(fontFiles)) {
    cmaps.set(cls, cmapCodepoints(fontBytes(file)));
  }

  it("every rule's codepoint exists in its font's cmap", () => {
    const rules = [
      ...(data.fileRules as RawRule[]),
      ...(data.dirRules as RawRule[]),
    ];
    const missing: string[] = [];
    for (const r of rules) {
      const [cls, cp] = r.i;
      const cmap = cmaps.get(cls);
      if (cmap === undefined) {
        missing.push(`unknown font class ${cls}`);
      } else if (!cmap.has(cp)) {
        missing.push(`${cls} U+${cp.toString(16)} (${r.r ?? r.s ?? "?"})`);
      }
    }
    expect(missing).toEqual([]);
    for (const def of [data.defFile, data.defDir] as [string, number][]) {
      expect(cmaps.get(def[0])?.has(def[1])).toBe(true);
    }
  });

  it("rule counts and shapes match the vendoring receipts", () => {
    expect((data.fileRules as RawRule[]).length).toBe(2158);
    expect((data.dirRules as RawRule[]).length).toBe(42);
    for (const r of [
      ...(data.fileRules as RawRule[]),
      ...(data.dirRules as RawRule[]),
    ]) {
      expect(r.r !== undefined || r.s !== undefined).toBe(true);
      expect(Object.keys(fontFiles)).toContain(r.i[0]);
    }
  });

  it("fonts stay inside their byte budgets", () => {
    let total = 0;
    for (const [cls, file] of Object.entries(fontFiles)) {
      const n = fontBytes(file).length;
      total += n;
      expect(n, file).toBeLessThan(fontByteCap[cls]);
    }
    expect(total).toBeLessThan(totalByteCap);
  });

  it("data.json is pure ASCII", () => {
    const raw = readFileSync(join(fileiconsDir, "data.json"), "latin1");
    // eslint-style char sweep: printable ASCII + newline only.
    expect(/^[\n\x20-\x7e]*$/.test(raw)).toBe(true);
  });
});
