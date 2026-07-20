// Pure helpers behind the config editor's table-of-contents sidebar
// (config.ts). Kept free of DOM access so the vitest suite can pin
// the scroll-sync selection math without a layout engine: config.ts
// measures the section offsets and feeds them in as plain numbers.

// activeSectionIndex picks which ToC entry to highlight for a scroll
// position: the LAST section whose top edge sits at or above the
// viewport top (within `slack` pixels -- sub-pixel scroll positions
// and the section margin must not flicker the highlight), clamped to
// the first section while scrolled above it. One special case: a
// container scrolled to its very bottom highlights the LAST section
// even when that section is too short to ever reach the viewport top
// -- without it a short trailing section could never become active.
// A non-scrollable container (content fits) never triggers the
// bottom rule. offsets are the sections' content offsets (document
// order, ascending); returns -1 only for an empty offsets list.
export function activeSectionIndex(
  offsets: number[],
  scrollTop: number,
  viewport: number,
  contentHeight: number,
  slack = 8,
): number {
  if (offsets.length === 0) {
    return -1;
  }
  const scrollable = contentHeight > viewport + slack;
  if (scrollable && scrollTop + viewport >= contentHeight - slack) {
    return offsets.length - 1;
  }
  let active = 0;
  for (let i = 0; i < offsets.length; i++) {
    if (offsets[i] <= scrollTop + slack) {
      active = i;
    } else {
      break;
    }
  }
  return active;
}
