// The stats-row formatting gate: the pure formatters plus renderStats'
// per-metric dash-vs-value rules, most importantly the swap zero-total
// rule -- a LIVE swap reading of total 0 (no swap configured on Linux,
// macOS dynamic swap while empty) renders "0M", never the dash; only
// swapOk=false (dead source) dashes. That distinction is the fix for
// the macOS field report where the startup log showed swap=vm.swapusage
// wired yet SWP rendered a dash.

import { describe, expect, it } from "vitest";
import { formatBytesPair, formatPct, formatRate, renderStats } from "./stats";
import type { StatsNodes } from "./stats";

// ASCII-only source: the expected glyphs live as escapes (the
// stats.ts convention).
const DASH = "\u2014";
const DOWN = "\u2193";
const UP = "\u2191";
const GI = 1024 * 1024 * 1024;
const MI = 1024 * 1024;

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
});
