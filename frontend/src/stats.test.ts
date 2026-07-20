// The stats-row formatting gate: the pure formatters plus renderStats'
// per-metric dash-vs-value rules, most importantly the swap zero-total
// rule -- a LIVE swap reading of total 0 (no swap configured on Linux,
// macOS dynamic swap while empty) renders "0M", never the dash; only
// swapOk=false (dead source) dashes. That distinction is the fix for
// the macOS field report where the startup log showed swap=vm.swapusage
// wired yet SWP rendered a dash.
//
// Plus the WIDTH CONTRACT (the metric layout-stability gate): jsdom
// has no layout engine, so the mechanical pin is formatter-width +
// CSS-structure -- (a) sweeps proving no formatter can emit more
// characters than its stats.ts *_MAX_CHARS constant over the
// documented input domain, and (b) assertions that style.css actually
// reserves each value slot at MAX + 1ch with tabular-nums on a grid
// row. Either side drifting alone fails here.

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  BYTES_PAIR_MAX_CHARS,
  NET_MAX_CHARS,
  PCT_MAX_CHARS,
  RATE_MAX_CHARS,
  formatBytesPair,
  formatPct,
  formatRate,
  renderStats,
} from "./stats";
import type { StatsNodes } from "./stats";

// ASCII-only source: the expected glyphs live as escapes (the
// stats.ts convention).
const DASH = "\u2014";
const DOWN = "\u2193";
const UP = "\u2191";
const GI = 1024 * 1024 * 1024;
const MI = 1024 * 1024;
const KI = 1024;

function nodes(): StatsNodes {
  return {
    cpu: document.createElement("span"),
    gpu: document.createElement("span"),
    ram: document.createElement("span"),
    swap: document.createElement("span"),
    net: document.createElement("span"),
  };
}

function snap(over: Partial<StatsSnapshot>): StatsSnapshot {
  return {
    enabled: true,
    cpuPct: 0,
    cpuOk: false,
    gpuPct: 0,
    gpuOk: false,
    memUsed: 0,
    memTotal: 0,
    memOk: false,
    swapUsed: 0,
    swapTotal: 0,
    swapOk: false,
    netRxBps: 0,
    netTxBps: 0,
    netOk: false,
    ...over,
  };
}

function renderedSwap(over: Partial<StatsSnapshot>): string {
  const n = nodes();
  renderStats(snap(over), n);
  return n.swap.textContent ?? "";
}

describe("formatters", () => {
  it("formatPct rounds to a whole percent", () => {
    expect(formatPct(12.4)).toBe("12%");
    expect(formatPct(99.5)).toBe("100%");
    expect(formatPct(0)).toBe("0%");
  });

  it("formatBytesPair puts both values in the total's unit", () => {
    expect(formatBytesPair(6.2 * GI, 15.9 * GI)).toBe("6.2/16G");
    expect(formatBytesPair(0.5 * GI, 12 * GI)).toBe("0.5/12G");
    expect(formatBytesPair(100 * MI, 512 * MI)).toBe("100/512M");
  });

  it("formatRate humanizes bytes/second with binary units", () => {
    expect(formatRate(0)).toBe("0B");
    expect(formatRate(512)).toBe("512B");
    expect(formatRate(2.5 * 1024)).toBe("2.5K");
    expect(formatRate(20 * MI)).toBe("20M");
    expect(formatRate(3 * GI)).toBe("3.0G");
  });
});

describe("renderStats swap rule", () => {
  it("renders 0M for a live reading with total 0 (the field report)", () => {
    // macOS with empty dynamic swap / Linux with no swap configured:
    // the sampler reports SwapOK true with zero totals, and that is a
    // real value, not an unavailable metric.
    expect(renderedSwap({ swapOk: true, swapTotal: 0, swapUsed: 0 })).toBe(
      "0M",
    );
  });

  it("renders the used/total pair when a total exists, even at used 0", () => {
    expect(renderedSwap({ swapOk: true, swapUsed: 0, swapTotal: 8 * GI })).toBe(
      "0.0/8.0G",
    );
    expect(
      renderedSwap({ swapOk: true, swapUsed: 0.5 * GI, swapTotal: 8 * GI }),
    ).toBe("0.5/8.0G");
  });

  it("keeps the dash for a dead source regardless of the totals", () => {
    expect(renderedSwap({ swapOk: false })).toBe(DASH);
    expect(renderedSwap({ swapOk: false, swapTotal: 8 * GI })).toBe(DASH);
    expect(renderedSwap({ swapOk: false, swapUsed: GI })).toBe(DASH);
  });
});

describe("renderStats other metrics", () => {
  it("dashes every metric whose Ok flag is false", () => {
    const n = nodes();
    renderStats(snap({}), n);
    expect(n.cpu.textContent).toBe(DASH);
    expect(n.gpu.textContent).toBe(DASH);
    expect(n.ram.textContent).toBe(DASH);
    expect(n.swap.textContent).toBe(DASH);
    expect(n.net.textContent).toBe(DASH);
  });

  it("renders live values per metric", () => {
    const n = nodes();
    renderStats(
      snap({
        cpuOk: true,
        cpuPct: 12.4,
        gpuOk: true,
        gpuPct: 37,
        memOk: true,
        memUsed: 6.2 * GI,
        memTotal: 15.9 * GI,
        netOk: true,
        netRxBps: 1.2 * MI,
        netTxBps: 5.6 * 1024,
      }),
      n,
    );
    expect(n.cpu.textContent).toBe("12%");
    expect(n.gpu.textContent).toBe("37%");
    expect(n.ram.textContent).toBe("6.2/16G");
    expect(n.net.textContent).toBe(DOWN + "1.2M " + UP + "5.6K");
  });

  it("builds the net value as tinted arrow spans (never innerHTML)", () => {
    const n = nodes();
    renderStats(
      snap({ netOk: true, netRxBps: 1.2 * MI, netTxBps: 5.6 * 1024 }),
      n,
    );
    const down = n.net.querySelector(".net-down");
    const up = n.net.querySelector(".net-up");
    expect(down?.textContent).toBe(DOWN);
    expect(up?.textContent).toBe(UP);
    // The spans wrap ONLY the arrows; the rates stay plain text nodes.
    expect(n.net.textContent).toBe(DOWN + "1.2M " + UP + "5.6K");
    // A later dash render replaces the spans wholesale.
    renderStats(snap({ netOk: false }), n);
    expect(n.net.querySelector(".net-down")).toBeNull();
    expect(n.net.textContent).toBe(DASH);
  });
});

/* --- the width contract -------------------------------------------- */
/* Formatter side: over the documented input domains (stats.ts), no
   formatter may emit more characters than its *_MAX_CHARS constant.
   These sweeps include the true worst cases: num() rounds values >=
   10, so the widest sub-unit value is "1024" (e.g. total = GI - 1 in
   the M band), and the widest in-domain G values are 4 digits. */

// pctDomain: the Go sampler clamps percentages to 0..100.
const pctDomain = [0, 0.4, 9.4, 9.5, 10, 50, 99.4, 99.5, 100];

// byteTotals: totals across both unit bands up to the documented
// 9999 GiB domain edge, including the band-edge rounding worst cases.
const byteTotals = [
  1,
  KI,
  MI - 1,
  MI,
  10 * MI,
  512 * MI,
  GI - 1, // "1024/1024M" -- the widest pair the M band can emit
  GI,
  8 * GI,
  15.9 * GI,
  100 * GI,
  999 * GI,
  9999 * GI, // the documented domain edge
];

// rateDomain: bytes/second across all four unit bands up to the
// documented 9999 GiB/s domain edge, band edges included.
const rateDomain = [
  0,
  1,
  1023,
  1023.6, // rounds to "1024B"
  KI,
  MI - 1, // "1024K"
  MI,
  GI - 1, // "1024M"
  GI,
  42 * GI,
  9999 * GI, // the documented domain edge
];

describe("width contract: formatters", () => {
  it("formatPct never exceeds PCT_MAX_CHARS over 0..100", () => {
    for (const pct of pctDomain) {
      expect(formatPct(pct).length, `formatPct(${pct})`).toBeLessThanOrEqual(
        PCT_MAX_CHARS,
      );
    }
  });

  it("formatBytesPair never exceeds BYTES_PAIR_MAX_CHARS for used <= total <= 9999 GiB", () => {
    for (const total of byteTotals) {
      for (const used of [0, total / 3, total]) {
        const out = formatBytesPair(used, total);
        expect(
          out.length,
          `formatBytesPair(${used}, ${total}) = ${out}`,
        ).toBeLessThanOrEqual(BYTES_PAIR_MAX_CHARS);
      }
    }
  });

  it("formatRate never exceeds RATE_MAX_CHARS below 9999 GiB/s", () => {
    for (const bps of rateDomain) {
      const out = formatRate(bps);
      expect(out.length, `formatRate(${bps}) = ${out}`).toBeLessThanOrEqual(
        RATE_MAX_CHARS,
      );
    }
  });

  it("rendered extreme snapshots stay within every slot", () => {
    const n = nodes();
    renderStats(
      snap({
        cpuOk: true,
        cpuPct: 100,
        gpuOk: true,
        gpuPct: 100,
        memOk: true,
        memUsed: GI - 1,
        memTotal: GI - 1, // the widest RAM pair: "1024/1024M"
        swapOk: true,
        swapUsed: 9999 * GI,
        swapTotal: 9999 * GI, // the widest in-domain G pair
        netOk: true,
        netRxBps: GI - 1, // "1024M"
        netTxBps: 9999 * GI, // "9999G"
      }),
      n,
    );
    expect(n.cpu.textContent).toBe("100%");
    expect((n.cpu.textContent ?? "").length).toBeLessThanOrEqual(
      PCT_MAX_CHARS,
    );
    expect((n.gpu.textContent ?? "").length).toBeLessThanOrEqual(
      PCT_MAX_CHARS,
    );
    expect(n.ram.textContent).toBe("1024/1024M");
    expect((n.ram.textContent ?? "").length).toBeLessThanOrEqual(
      BYTES_PAIR_MAX_CHARS,
    );
    expect(n.swap.textContent).toBe("9999/9999G");
    expect((n.swap.textContent ?? "").length).toBeLessThanOrEqual(
      BYTES_PAIR_MAX_CHARS,
    );
    expect(n.net.textContent).toBe(DOWN + "1024M " + UP + "9999G");
    expect((n.net.textContent ?? "").length).toBeLessThanOrEqual(
      NET_MAX_CHARS,
    );
  });

  it("the dash and the 0M swap reading fit every slot too", () => {
    expect(DASH.length).toBeLessThanOrEqual(PCT_MAX_CHARS);
    expect("0M".length).toBeLessThanOrEqual(BYTES_PAIR_MAX_CHARS);
  });
});

/* --- the width contract: CSS structure ------------------------------ */
/* jsdom computes no layout, so the CSS side is pinned structurally:
   style.css must lay the row out as a grid, reserve each value slot's
   min-width at exactly MAX + 1ch, and set tabular-nums -- the numbers
   here and the constants in stats.ts move together or this fails. */

const cssPath = join(dirname(fileURLToPath(import.meta.url)), "style.css");
const css = readFileSync(cssPath, "utf-8");

// ruleBody returns the declaration block of the first rule whose
// selector list contains the given selector text.
function ruleBody(selector: string): string {
  const esc = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const m = new RegExp(esc + "[^{}]*\\{([^}]*)\\}").exec(css);
  if (m === null) {
    throw new Error(`style.css has no rule for ${selector}`);
  }
  return m[1];
}

function reservedCh(selector: string): number {
  const m = /min-width:\s*(\d+)ch/.exec(ruleBody(selector));
  if (m === null) {
    throw new Error(`style.css rule for ${selector} reserves no ch min-width`);
  }
  return Number(m[1]);
}

describe("width contract: style.css structure", () => {
  it("lays the stats row out as a grid and keeps [hidden] working", () => {
    expect(ruleBody("#stats")).toMatch(/display:\s*grid/);
    expect(ruleBody("#stats[hidden]")).toMatch(/display:\s*none/);
  });

  it("gives values tabular figures", () => {
    expect(ruleBody("#stats .stat-value")).toMatch(
      /font-variant-numeric:\s*tabular-nums/,
    );
  });

  it("reserves each value slot at its formatter maximum + 1ch slack", () => {
    expect(reservedCh("#stat-cpu")).toBe(PCT_MAX_CHARS + 1);
    expect(reservedCh("#stat-ram")).toBe(BYTES_PAIR_MAX_CHARS + 1);
    expect(reservedCh("#stat-net")).toBe(NET_MAX_CHARS + 1);
    // The comma-list selectors cover all five ids: cpu+gpu share a
    // rule, ram+swap share a rule.
    expect(css).toMatch(/#stat-cpu,\s*#stat-gpu\s*\{/);
    expect(css).toMatch(/#stat-ram,\s*#stat-swap\s*\{/);
  });

  it("tints the labels and net arrows from existing tokens only", () => {
    // Subtle color arrives via color-mix over --sb-* tokens -- no new
    // token, no literal colors (the sync_test.go :root contract).
    expect(ruleBody("#stats .stat-label")).toMatch(
      /color:\s*color-mix\(in srgb, var\(--sb-accent\)/,
    );
    expect(ruleBody("#stats .net-down")).toMatch(
      /color:\s*color-mix\(in srgb, var\(--sb-accent\)/,
    );
    expect(ruleBody("#stats .net-up")).toMatch(
      /color:\s*color-mix\(in srgb, var\(--sb-warning\)/,
    );
  });
});
