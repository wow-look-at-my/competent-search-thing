import { describe, expect, it } from "vitest";

import { summarize } from "./fpsmeter";
import { shouldInterceptWheel } from "./wheel";

// Build n copies of delta (ms).
function steady(delta: number, n: number): number[] {
  return new Array<number>(n).fill(delta);
}

describe("summarize", () => {
  it("returns null for no usable deltas", () => {
    expect(summarize([])).toBeNull();
    expect(summarize([0, -5, 300, 5000])).toBeNull(); // gaps + junk only
  });

  it("reads a steady 60Hz stream as ~60 avg with no long frames", () => {
    const s = summarize(steady(1000 / 60, 300));
    expect(s).not.toBeNull();
    expect(s!.avgFps).toBeCloseTo(60, 0);
    expect(s!.maxFps).toBeCloseTo(60, 0);
    expect(s!.longFramePct).toBe(0);
    expect(s!.inferredHz).toBe(60);
    expect(s!.frames).toBe(300);
    expect(s!.windowMs).toBe(Math.round((1000 / 60) * 300));
  });

  it("reads a steady 120Hz stream as ~120", () => {
    const s = summarize(steady(1000 / 120, 600));
    expect(s!.avgFps).toBeCloseTo(120, 0);
    expect(s!.longFramePct).toBe(0);
    expect(s!.inferredHz).toBe(120);
  });

  it("reads a steady 33.3ms throttle as ~30 avg, 100% long, inferred 30", () => {
    const s = summarize(steady(1000 / 30, 150));
    expect(s!.avgFps).toBeCloseTo(30, 0);
    expect(s!.longFramePct).toBe(100);
    expect(s!.inferredHz).toBe(30);
  });

  it("discards gap deltas (> 250ms) from every stat", () => {
    const deltas = steady(1000 / 60, 100);
    deltas.push(5000); // window hidden mid-run
    const s = summarize(deltas);
    expect(s!.frames).toBe(100);
    expect(s!.avgFps).toBeCloseTo(60, 0);
    expect(s!.windowMs).toBe(Math.round((1000 / 60) * 100));
  });

  it("snaps a near-rate cadence within 10% (58 -> 60)", () => {
    const s = summarize(steady(1000 / 58, 200));
    expect(s!.inferredHz).toBe(60);
  });

  it("keeps an off-grid cadence unsnapped (25 stays 25)", () => {
    // 25 vs the nearest candidate 30 is 16.7% off -- beyond the 10%
    // snap tolerance, so the rounded raw rate stands.
    const s = summarize(steady(40, 100));
    expect(s!.inferredHz).toBe(25);
  });

  it("counts mixed windows correctly", () => {
    // Half 60Hz frames, half 33.3ms frames: avg between, long ~50%.
    const deltas = steady(1000 / 60, 100).concat(steady(1000 / 30, 100));
    const s = summarize(deltas);
    expect(s!.longFramePct).toBe(50);
    expect(s!.avgFps).toBeGreaterThan(30);
    expect(s!.avgFps).toBeLessThan(60);
    expect(s!.maxFps).toBeCloseTo(60, 0);
  });
});

describe("shouldInterceptWheel", () => {
  it("keeps the WebKitGTK interception on linux", () => {
    expect(shouldInterceptWheel("Linux x86_64")).toBe(true);
  });
  it("keeps windows on the interception path", () => {
    expect(shouldInterceptWheel("Win32")).toBe(true);
  });
  it("disables the interception on every Mac platform string", () => {
    expect(shouldInterceptWheel("MacIntel")).toBe(false);
    expect(shouldInterceptWheel("MacPPC")).toBe(false);
  });
  it("fails open to intercepting when the platform is unknown", () => {
    expect(shouldInterceptWheel("")).toBe(true);
  });
});
