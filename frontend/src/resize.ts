// Drag-edge window resizing for the frameless bar: ~6px hit bands
// along the LEFT, RIGHT, and BOTTOM window edges (plus L-shaped
// bottom-corner zones) resize the window -- horizontal drags about
// the center, vertical drags growing downward from the anchored top.
// The TOP edge is deliberately excluded: it hosts the query row and
// is the bar's anchor.
//
// Deliberately ELEMENT-FREE: no overlay strips exist in the DOM, so
// nothing can intercept wheel scrolling, row hover, text selection,
// the preview pane, or the config sidebar. Instead, document-level
// pointer listeners classify each event's viewport position
// (edgeZone, pure and vitest-pinned): hover near an edge only swaps
// the cursor, and a pointerdown inside a band claims the drag
// (capture-phase preventDefault + pointer capture confine it).
// WebKitGTK never dispatches DOM pointer events for native scrollbar
// interaction, so the results scrollbar at the right edge stays
// fully usable.
//
// Per-frame geometry goes to Go as ResizeDrag(w, h) --
// rAF-coalesced, absolute target sizes derived from the drag-start
// size so no error accumulates -- and the release commits ONCE via
// ResizeCommit(w, h), which persists the final size to config.json
// (Go owns clamping, centering, and which fields a drag writes).
// Drags work in every mode, the config editor included: they
// manipulate the window, not the search UI.

/** Width of the straight edge bands, in CSS pixels. */
export const EDGE_BAND = 6;
/** Reach of the corner zones along each edge, in CSS pixels. */
export const CORNER_REACH = 18;

export type EdgeZone =
  | "left"
  | "right"
  | "bottom"
  | "bottom-left"
  | "bottom-right";

/**
 * Classify a viewport position into a resize zone, or null when it
 * is not on a resizable edge. Corners are L-shaped: the last
 * CORNER_REACH pixels of the bottom band plus the bottom end of each
 * side band, so no zone ever reaches deeper than EDGE_BAND into the
 * window content. The top band is never a zone.
 */
export function edgeZone(
  x: number,
  y: number,
  w: number,
  h: number,
  band: number = EDGE_BAND,
  corner: number = CORNER_REACH,
): EdgeZone | null {
  if (x < 0 || y < 0 || x >= w || y >= h) {
    return null;
  }
  const nearLeft = x < band;
  const nearRight = x >= w - band;
  const nearBottom = y >= h - band;
  const cornerDepth = y >= h - corner;
  if ((nearBottom && x < corner) || (nearLeft && cornerDepth)) {
    return "bottom-left";
  }
  if ((nearBottom && x >= w - corner) || (nearRight && cornerDepth)) {
    return "bottom-right";
  }
  if (nearBottom) {
    return "bottom";
  }
  if (nearLeft) {
    return "left";
  }
  if (nearRight) {
    return "right";
  }
  return null;
}

/**
 * The target window size for a drag: absolute, derived from the
 * drag-start size plus the pointer deltas -- never incremental, so a
 * dropped frame loses nothing. Horizontal edges resize ABOUT CENTER:
 * the window grows by twice the pointer travel (the far edge mirrors
 * the near one), so the dragged edge tracks the pointer while the
 * bar stays centered. The bottom edge grows 1:1 downward from the
 * anchored top. Floors mirror the Go-side config minimums (Go clamps
 * authoritatively; these just keep the wire values sane).
 */
export function dragTarget(
  zone: EdgeZone,
  startW: number,
  startH: number,
  dx: number,
  dy: number,
  minW = 320,
  minH = 240,
): { w: number; h: number } {
  let w = startW;
  let h = startH;
  switch (zone) {
    case "right":
      w = startW + 2 * dx;
      break;
    case "left":
      w = startW - 2 * dx;
      break;
    case "bottom":
      h = startH + dy;
      break;
    case "bottom-right":
      w = startW + 2 * dx;
      h = startH + dy;
      break;
    case "bottom-left":
      w = startW - 2 * dx;
      h = startH + dy;
      break;
  }
  w = Math.round(w);
  h = Math.round(h);
  if (w < minW) {
    w = minW;
  }
  if (h < minH) {
    h = minH;
  }
  return { w, h };
}

/** The CSS cursor advertising each zone. */
export const zoneCursors: Record<EdgeZone, string> = {
  left: "ew-resize",
  right: "ew-resize",
  bottom: "ns-resize",
  "bottom-left": "nesw-resize",
  "bottom-right": "nwse-resize",
};

interface DragState {
  zone: EdgeZone;
  startW: number;
  startH: number;
  sx: number;
  sy: number;
  moved: boolean;
  pointerId: number;
}

/**
 * Wire the drag-edge resize behavior. Called once from wire();
 * everything runs on document-level listeners (see the module
 * comment for why no overlay elements exist).
 */
export function initResize(app: WailsAppBindings): void {
  let drag: DragState | null = null;
  let pending: { w: number; h: number } | null = null;
  let rafId = 0;

  const setCursor = (c: string): void => {
    document.documentElement.style.cursor = c;
  };

  const flush = (): void => {
    rafId = 0;
    if (drag !== null && pending !== null) {
      const p = pending;
      pending = null;
      app.ResizeDrag(p.w, p.h).catch(() => {
        // Per-frame calls are best-effort; the commit reports errors.
      });
    }
  };

  const targetFor = (ev: PointerEvent | MouseEvent): { w: number; h: number } | null => {
    if (drag === null) {
      return null;
    }
    return dragTarget(
      drag.zone,
      drag.startW,
      drag.startH,
      ev.screenX - drag.sx,
      ev.screenY - drag.sy,
    );
  };

  document.addEventListener(
    "pointermove",
    (ev: PointerEvent) => {
      if (drag !== null) {
        const t = targetFor(ev);
        if (t === null) {
          return;
        }
        if (!drag.moved && t.w === drag.startW && t.h === drag.startH) {
          return; // a click that has not actually dragged yet
        }
        drag.moved = true;
        pending = t;
        if (rafId === 0) {
          rafId = window.requestAnimationFrame(flush);
        }
        return;
      }
      // Hover feedback only: swap the cursor near an edge. No element
      // sits under the pointer, so row hover and selection behave
      // exactly as without this module.
      const z = edgeZone(ev.clientX, ev.clientY, window.innerWidth, window.innerHeight);
      setCursor(z === null ? "" : zoneCursors[z]);
    },
    { passive: true },
  );

  document.addEventListener(
    "pointerdown",
    (ev: PointerEvent) => {
      if (drag !== null || ev.button !== 0) {
        return;
      }
      const z = edgeZone(ev.clientX, ev.clientY, window.innerWidth, window.innerHeight);
      if (z === null) {
        return;
      }
      // Claim the gesture before anything below sees it: no focus
      // steal, no text selection, no row activation from an edge
      // press.
      ev.preventDefault();
      ev.stopPropagation();
      drag = {
        zone: z,
        startW: window.innerWidth,
        startH: window.innerHeight,
        sx: ev.screenX,
        sy: ev.screenY,
        moved: false,
        pointerId: ev.pointerId,
      };
      setCursor(zoneCursors[z]);
      try {
        // Keep receiving moves when the pointer leaves the window --
        // dragging an edge OUTWARD does exactly that. Guarded: jsdom
        // and synthesized events have no capture plumbing.
        document.documentElement.setPointerCapture?.(ev.pointerId);
      } catch {
        // Capture is an optimization; the drag still works while the
        // pointer stays inside the window.
      }
    },
    { capture: true },
  );

  const finish = (ev: PointerEvent): void => {
    if (drag === null) {
      return;
    }
    const d = drag;
    const t = targetFor(ev) ?? pending;
    drag = null;
    pending = null;
    if (rafId !== 0) {
      window.cancelAnimationFrame(rafId);
      rafId = 0;
    }
    setCursor("");
    try {
      document.documentElement.releasePointerCapture?.(d.pointerId);
    } catch {
      // Nothing was captured; fine.
    }
    if (!d.moved || t === null) {
      return; // an edge click without movement changes nothing
    }
    app.ResizeCommit(t.w, t.h).catch((err: unknown) => {
      console.warn("resize commit failed: " + String(err));
    });
  };
  document.addEventListener("pointerup", finish);
  document.addEventListener("pointercancel", finish);
}
