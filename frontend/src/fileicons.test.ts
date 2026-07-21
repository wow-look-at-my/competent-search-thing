// The file-type icon gate, frontend half: pins the fileicons matcher
// semantics (pack-order first-match: special filenames and compound
// suffixes beat generic extensions), the install/fallback wiring
// around the GetFileIcons bridge table, the light/dark motif rule,
// and the committed fonts' byte budgets. The committed data.bin
// artifact itself is gated Go-side (internal/fileicons: decoded
// through the SAME first-party binpazer reader the runtime uses,
// counts + shapes + pack-content pins + the font-cmap cross-check).
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { beforeAll, describe, expect, it, vi } from "vitest";
import {
  fileIcon,
  initFileIcons,
  installFileIcons,
  isLightBackground,
} from "./fileicons/fileicons";

const glyph = (cp: number): string => String.fromCodePoint(cp);

// A small fixture in the wire-table shape, mirroring the pack rules
// the semantics tests exercised when the whole table lived here:
// rule ORDER is pack priority order (first match wins).
const fixture: FileIconsTable = {
  fileRules: [
    { font: "fi", cp: 60001, regex: "webpack\\.config\\.", flags: "i", dark: "#519aba", light: "#519aba" },
    { font: "fi", cp: 128054, suffix: ".huskyrc.json", dark: "#e37933", light: "#945036" },
    { font: "oct", cp: 61558, regex: "^Makefile" },
    { font: "fi", cp: 60078, suffix: ".go", dark: "#6a9fb5", light: "#6a9fb5" },
    { font: "mf", cp: 61737, suffix: ".js", dark: "#f5de19", light: "#b7a542" },
    { font: "fi", cp: 60094, suffix: ".json", dark: "#f5de19", light: "#b7a542" },
  ],
  dirRules: [{ font: "oct", cp: 61450, suffix: ".github", dark: "#66757f", light: "#66757f" }],
  defFile: { font: "oct", cp: 61457 },
  defDir: { font: "oct", cp: 61462 },
};

describe("fileIcon matching", () => {
  beforeAll(() => {
    installFileIcons(fixture);
  });

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
    expect(fileIcon("makefile.txt", false).glyph).toBe(glyph(61457));
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

describe("table install wiring", () => {
  beforeAll(() => {
    installFileIcons(fixture);
  });

  it("initFileIcons installs the bridge answer and clears the memo", async () => {
    // Resolve once against the fixture, then swap tables through the
    // bound-method path: the memo must not serve the stale icon.
    expect(fileIcon("swap.go", false).glyph).toBe(glyph(60078));
    const app = {
      GetFileIcons: () =>
        Promise.resolve({
          fileRules: [{ font: "fi", cp: 60002, suffix: ".go" }],
          dirRules: null,
          defFile: { font: "oct", cp: 61457 },
          defDir: { font: "oct", cp: 61462 },
        }),
    } as unknown as WailsAppBindings;
    await initFileIcons(app);
    expect(fileIcon("swap.go", false).glyph).toBe(glyph(60002));
    installFileIcons(fixture); // restore for later describes
  });

  it("tolerates a rejecting bound method", async () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    try {
      const app = {
        GetFileIcons: () => Promise.reject(new Error("no bridge")),
      } as unknown as WailsAppBindings;
      await initFileIcons(app);
      expect(spy).toHaveBeenCalled();
      // The previous table survives.
      expect(fileIcon("keep.go", false).glyph).toBe(glyph(60078));
    } finally {
      spy.mockRestore();
    }
  });

  it("refuses a table with malformed defaults", () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    try {
      installFileIcons({
        fileRules: [],
        dirRules: [],
        defFile: { font: "", cp: 0 },
        defDir: { font: "oct", cp: 61462 },
      });
      expect(spy).toHaveBeenCalled();
      expect(fileIcon("still.go", false).glyph).toBe(glyph(60078));
    } finally {
      spy.mockRestore();
    }
  });

  it("skips malformed rules and tolerates null rule arrays", () => {
    installFileIcons({
      fileRules: [
        { font: "fi", cp: 0x110000, suffix: ".bad" }, // codepoint out of range
        { font: "fi", cp: 60078, regex: "(" }, // invalid regex
        { font: "fi", cp: 60078, suffix: ".ok" },
      ],
      dirRules: null,
      defFile: { font: "oct", cp: 61457 },
      defDir: { font: "oct", cp: 61462 },
    });
    expect(fileIcon("a.bad", false).glyph).toBe(glyph(61457));
    expect(fileIcon("a.ok", false).glyph).toBe(glyph(60078));
    expect(fileIcon("anydir", true).glyph).toBe(glyph(61462));
    installFileIcons(fixture);
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

/* --- committed fonts ------------------------------------------------ */

// Byte budgets: the committed sizes plus slack -- a re-vendor that
// balloons a font trips here before it rides the embedded dist. (The
// mapping artifact's budget lives with it, Go-side.)
const fontFiles: Record<string, string> = {
  fi: "file-icons.woff2",
  fa: "fontawesome.woff2",
  mf: "mfixx.woff2",
  oct: "octicons.woff2",
  di: "devopicons.woff2",
};
const fontByteCap: Record<string, number> = {
  fi: 240000,
  fa: 90000,
  mf: 40000,
  oct: 32000,
  di: 60000,
};
const totalByteCap = 410000;

describe("committed fonts", () => {
  it("fonts stay inside their byte budgets", () => {
    // Paths resolve from the vitest root (frontend/) -- import.meta.url
    // is a rootless serve URL inside transformed test modules.
    const fontsDir = join(process.cwd(), "src", "fileicons", "fonts");
    let total = 0;
    for (const [cls, file] of Object.entries(fontFiles)) {
      const n = readFileSync(join(fontsDir, file)).length;
      total += n;
      expect(n, file).toBeLessThan(fontByteCap[cls]);
    }
    expect(total).toBeLessThan(totalByteCap);
  });
});
