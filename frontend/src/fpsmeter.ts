// Dev-only fps meter (COMPETENT_SEARCH_FPS=1, read Go-side): a
// requestAnimationFrame loop collecting frame deltas while the page is
// visible, summarized every ~5s of ACCUMULATED visible time (first
// report after ~2.5s so CI and impatient humans see output fast) and
// reported through the RecordFPSSample bound method, which validates
// and logs it next to the Go side's display/power context line -- the
// pair diagnoses WebKit's frame-pacing throttles ("rAF ~60Hz" on
// "display 120Hz max" = the near-60 cap; "rAF ~30Hz" with
// lowPowerMode=on = the macOS Low Power Mode throttle). initFPSMeter
// registers NOTHING when the meter is off: zero cost by default.

// Deltas above GAP_MS are gaps, not frames (hidden window, the first
// frame after a summon, a debugger pause): excluded from every stat.
const GAP_MS = 250;
// The long-frame threshold: a 60Hz frame is ~16.7ms, so >20ms means a
// missed 60fps deadline; under a steady 30fps throttle ~100% of
// frames read long -- deliberately loud.
const LONG_FRAME_MS = 20;
const REPORT_MS = 5000;
const FIRST_REPORT_MS = 2500;
// Candidate rAF cadences the inferred rate snaps to (within 10%):
// JS cannot read the panel's refresh rate directly, so cadence
// inference from the 10th-percentile delta is the standard trick; the
// Go context line supplies the hardware truth to compare against.
const SNAP_RATES = [30, 48, 60, 72, 90, 120, 144];

// summarize turns one window of frame deltas (ms) into an FPSSample.
// PURE (vitest-covered): gap and non-positive deltas are dropped
// defensively even though the collection loop already excludes them;
// null when nothing usable remains.
export function summarize(deltas: number[]): FPSSample | null {
  const frames = deltas.filter((d) => d > 0 && d <= GAP_MS);
  if (frames.length === 0) {
    return null;
  }
  let sum = 0;
  let min = Infinity;
  let long = 0;
  for (const d of frames) {
    sum += d;
    if (d < min) {
      min = d;
    }
    if (d > LONG_FRAME_MS) {
      long++;
    }
  }
  const sorted = [...frames].sort((a, b) => a - b);
  const p10 = sorted[Math.floor(sorted.length * 0.1)];
  const rawHz = 1000 / p10;
  let inferredHz = Math.round(rawHz);
  for (const r of SNAP_RATES) {
    if (Math.abs(rawHz - r) <= r * 0.1) {
      inferredHz = r;
      break;
    }
  }
  return {
    avgFps: (frames.length / sum) * 1000,
    maxFps: 1000 / min,
    longFramePct: Math.round((long / frames.length) * 100),
    windowMs: Math.round(sum),
    frames: frames.length,
    inferredHz,
  };
}

// initFPSMeter asks the Go side whether the meter is on and, only
// then, starts the rAF loop. Reports are fire-and-forget (the
// reportPick pattern): a meter failure must never affect the UI.
export function initFPSMeter(app: WailsAppBindings): void {
  app
    .FPSEnabled()
    .then((on) => {
      if (!on) {
        return; // nothing registered, no rAF loop, no listeners
      }
      startMeterLoop(app);
    })
    .catch((err: unknown) => {
      console.warn("fps meter probe failed: " + String(err));
    });
}

function startMeterLoop(app: WailsAppBindings): void {
  let deltas: number[] = [];
  let last = -1;
  let accum = 0;
  let due = FIRST_REPORT_MS;
  // A hidden WKWebView stops servicing rAF anyway; the explicit reset
  // keeps the accounting clean (the resume frame starts a fresh
  // measurement instead of producing a bogus delta) and guarantees
  // zero hidden-state work.
  document.addEventListener("visibilitychange", () => {
    if (document.hidden) {
      last = -1;
    }
  });
  const tick = (ts: number): void => {
    if (last >= 0) {
      const d = ts - last;
      if (d > 0 && d <= GAP_MS) {
        deltas.push(d);
        accum += d;
      }
    }
    last = ts;
    if (accum >= due) {
      const sample = summarize(deltas);
      deltas = [];
      accum = 0;
      due = REPORT_MS;
      if (sample !== null) {
        app.RecordFPSSample(sample).catch((err: unknown) => {
          console.warn("fps report failed: " + String(err));
        });
      }
    }
    window.requestAnimationFrame(tick);
  };
  window.requestAnimationFrame(tick);
}
