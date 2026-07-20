// Drag-edge resize: the pure zone classifier and target-size math
// (resize.ts), plus a jsdom wiring smoke over initResize -- the
// element-free document listeners must claim edge pointerdowns,
// stream rAF-coalesced ResizeDrag frames, and commit exactly once on
// release, while pointerdowns anywhere else stay untouched.

import { beforeAll, describe, expect, it, vi } from "vitest";
import {
  CORNER_REACH,
  EDGE_BAND,
  dragTarget,
  edgeZone,
  initResize,
  zoneCursors,
} from "./resize";

const W = 1000;
const H = 600;

describe("edgeZone", () => {
  it("classifies the straight bands", () => {
    expect(edgeZone(0, 300, W, H)).toBe("left");
    expect(edgeZone(EDGE_BAND - 1, 300, W, H)).toBe("left");
    expect(edgeZone(EDGE_BAND, 300, W, H)).toBeNull();
    expect(edgeZone(W - 1, 300, W, H)).toBe("right");
    expect(edgeZone(W - EDGE_BAND, 300, W, H)).toBe("right");
    expect(edgeZone(500, H - 1, W, H)).toBe("bottom");
    expect(edgeZone(500, H - EDGE_BAND, W, H)).toBe("bottom");
  });

  it("excludes the top edge (query row + anchor)", () => {
    expect(edgeZone(500, 0, W, H)).toBeNull();
    expect(edgeZone(500, EDGE_BAND - 1, W, H)).toBeNull();
  });

  it("classifies the L-shaped bottom corners", () => {
    // Along the bottom band, the last CORNER_REACH px are corner.
    expect(edgeZone(CORNER_REACH - 1, H - 1, W, H)).toBe("bottom-left");
    expect(edgeZone(CORNER_REACH, H - 1, W, H)).toBe("bottom");
    expect(edgeZone(W - CORNER_REACH, H - 1, W, H)).toBe("bottom-right");
    // Along the side bands, the last CORNER_REACH px are corner too.
    expect(edgeZone(0, H - CORNER_REACH, W, H)).toBe("bottom-left");
    expect(edgeZone(0, H - CORNER_REACH - 1, W, H)).toBe("left");
    expect(edgeZone(W - 1, H - CORNER_REACH, W, H)).toBe("bottom-right");
    // The corner zones never reach deeper than the bands.
    expect(edgeZone(CORNER_REACH - 1, H - EDGE_BAND - 1, W, H)).toBeNull();
  });

  it("returns null outside the viewport", () => {
    expect(edgeZone(-1, 300, W, H)).toBeNull();
    expect(edgeZone(W, 300, W, H)).toBeNull();
    expect(edgeZone(500, H, W, H)).toBeNull();
  });

  it("names a cursor for every zone", () => {
    expect(zoneCursors.left).toBe("ew-resize");
    expect(zoneCursors.right).toBe("ew-resize");
    expect(zoneCursors.bottom).toBe("ns-resize");
    expect(zoneCursors["bottom-left"]).toBe("nesw-resize");
    expect(zoneCursors["bottom-right"]).toBe("nwse-resize");
  });
});

describe("dragTarget", () => {
  it("resizes about center horizontally: width grows by twice the travel", () => {
    expect(dragTarget("right", 800, 500, 50, 0)).toEqual({ w: 900, h: 500 });
    expect(dragTarget("right", 800, 500, -50, 0)).toEqual({ w: 700, h: 500 });
    expect(dragTarget("left", 800, 500, -50, 0)).toEqual({ w: 900, h: 500 });
    expect(dragTarget("left", 800, 500, 50, 0)).toEqual({ w: 700, h: 500 });
  });

  it("grows 1:1 downward from the anchored top", () => {
    expect(dragTarget("bottom", 800, 500, 0, 40)).toEqual({ w: 800, h: 540 });
    expect(dragTarget("bottom", 800, 500, 0, -40)).toEqual({ w: 800, h: 460 });
  });

  it("combines both axes at the corners", () => {
    expect(dragTarget("bottom-right", 800, 500, 30, 20)).toEqual({ w: 860, h: 520 });
    expect(dragTarget("bottom-left", 800, 500, -30, 20)).toEqual({ w: 860, h: 520 });
  });

  it("floors at the config minimums", () => {
    expect(dragTarget("right", 800, 500, -1000, 0)).toEqual({ w: 320, h: 500 });
    expect(dragTarget("bottom", 800, 500, 0, -1000)).toEqual({ w: 800, h: 240 });
  });

  it("is absolute over the drag-start size (no accumulation)", () => {
    // The same delta always yields the same target no matter how many
    // intermediate frames were dropped.
    const a = dragTarget("right", 800, 500, 25, 0);
    const b = dragTarget("right", 800, 500, 25, 0);
    expect(a).toEqual(b);
  });
});

describe("initResize wiring", () => {
  const frame = (): Promise<void> =>
    new Promise((resolve) => {
      window.requestAnimationFrame(() => {
        resolve();
      });
    });

  // Synthesized pointer events: jsdom listeners fire by event TYPE,
  // so MouseEvent stands in for PointerEvent (pointerId and capture
  // plumbing are optional-guarded in resize.ts).
  const pev = (type: string, x: number, y: number, sx: number, sy: number): MouseEvent =>
    new MouseEvent(type, {
      bubbles: true,
      cancelable: true,
      clientX: x,
      clientY: y,
      screenX: sx,
      screenY: sy,
      button: 0,
    });

  const drags: Array<[number, number]> = [];
  const commits: Array<[number, number]> = [];
  const app = {
    ResizeDrag: (w: number, h: number) => {
      drags.push([w, h]);
      return Promise.resolve();
    },
    ResizeCommit: (w: number, h: number) => {
      commits.push([w, h]);
      return Promise.resolve();
    },
  } as unknown as WailsAppBindings;

  beforeAll(() => {
    initResize(app);
  });

  it("streams drag frames and commits once on release", async () => {
    const vw = window.innerWidth; // jsdom: 1024
    const down = pev("pointerdown", vw - 2, 300, 500, 300);
    const spy = vi.spyOn(down, "preventDefault");
    document.body.dispatchEvent(down);
    expect(spy).toHaveBeenCalled(); // the edge press was claimed

    document.body.dispatchEvent(pev("pointermove", vw - 2, 300, 530, 300));
    await frame();
    expect(drags.length).toBe(1);
    expect(drags[0]).toEqual([vw + 60, window.innerHeight]); // right edge: 2 * 30px

    document.body.dispatchEvent(pev("pointerup", vw - 2, 300, 540, 300));
    expect(commits).toEqual([[vw + 80, window.innerHeight]]);
    expect(document.documentElement.style.cursor).toBe("");
  });

  it("ignores pointerdowns outside the bands and moveless clicks", () => {
    const before = commits.length;
    const down = pev("pointerdown", 400, 300, 400, 300);
    const spy = vi.spyOn(down, "preventDefault");
    document.body.dispatchEvent(down);
    expect(spy).not.toHaveBeenCalled();
    document.body.dispatchEvent(pev("pointerup", 400, 300, 400, 300));

    // An edge press released without movement commits nothing.
    document.body.dispatchEvent(pev("pointerdown", 2, 300, 100, 300));
    document.body.dispatchEvent(pev("pointerup", 2, 300, 100, 300));
    expect(commits.length).toBe(before);
  });

  it("swaps the cursor on edge hover", () => {
    document.body.dispatchEvent(pev("pointermove", 2, 300, 100, 300));
    expect(document.documentElement.style.cursor).toBe("ew-resize");
    document.body.dispatchEvent(pev("pointermove", 400, 300, 400, 300));
    expect(document.documentElement.style.cursor).toBe("");
  });
});
