// System stats row rendering: pure formatting helpers plus the one
// update function main.ts calls with each sysstats Snapshot (the
// GetStats return / "stats:update" payload). Text-node writes only
// (textContent), matching render.ts conventions; row visibility (the
// enabled flag) is owned by main.ts.

// The five value nodes of the static #stats row in index.html.
export interface StatsNodes {
  cpu: HTMLElement;
  gpu: HTMLElement;
  ram: HTMLElement;
  swap: HTMLElement;
  net: HTMLElement;
}

// ASCII-only source: the rendered glyphs live as escapes.
const DASH = "\u2014"; // em dash: metric unavailable
const DOWN = "\u2193"; // down arrow: net received
const UP = "\u2191"; // up arrow: net sent

const KI = 1024;
const MI = 1024 * KI;
const GI = 1024 * MI;

// num formats an already-scaled value with the shared decimal rule:
// one decimal below 10, none from 10 up (6.234 -> "6.2", 15.9 -> "16").
function num(v: number): string {
  return v < 10 ? v.toFixed(1) : String(Math.round(v));
}

// formatPct renders a 0..100 percentage as "12%".
export function formatPct(pct: number): string {
  return String(Math.round(pct)) + "%";
}

// formatBytesPair renders a used/total byte pair as "6.2/15.9G": both
// values in the unit the TOTAL picks (GiB, or MiB below 1 GiB), the
// unit suffix once at the end.
export function formatBytesPair(used: number, total: number): string {
  const div = total >= GI ? GI : MI;
  const suffix = total >= GI ? "G" : "M";
  return num(used / div) + "/" + num(total / div) + suffix;
}

// formatRate humanizes one bytes/second figure: B/K/M/G binary units
// with the shared decimal rule.
export function formatRate(bps: number): string {
  if (bps >= GI) {
    return num(bps / GI) + "G";
  }
  if (bps >= MI) {
    return num(bps / MI) + "M";
  }
  if (bps >= KI) {
    return num(bps / KI) + "K";
  }
  return String(Math.round(bps)) + "B";
}

// renderStats writes one snapshot into the five value nodes. Any
// metric whose Ok flag is false renders the dash placeholder (missing
// source, failed read, rate not accumulated yet). Swap with a live
// reading of total 0 -- no swap configured on Linux, or macOS's
// dynamic swap while empty -- renders "0M": a live zero is a value
// (in the unit a sub-GiB total picks, integer like formatRate's
// bottom band), and only a dead source earns the dash. The old rule
// dashed exactly the healthy-vm.swapusage-total-0 state every idle
// Mac sits in (the SWP field report).
export function renderStats(snap: StatsSnapshot, nodes: StatsNodes): void {
  nodes.cpu.textContent = snap.cpuOk ? formatPct(snap.cpuPct) : DASH;
  nodes.gpu.textContent = snap.gpuOk ? formatPct(snap.gpuPct) : DASH;
  nodes.ram.textContent = snap.memOk
    ? formatBytesPair(snap.memUsed, snap.memTotal)
    : DASH;
  nodes.swap.textContent = !snap.swapOk
    ? DASH
    : snap.swapTotal > 0
      ? formatBytesPair(snap.swapUsed, snap.swapTotal)
      : "0M";
  nodes.net.textContent = snap.netOk
    ? DOWN + formatRate(snap.netRxBps) + " " + UP + formatRate(snap.netTxBps)
    : DASH;
}
