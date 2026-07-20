// The pure half of the flat selection model: which index should be
// selected after the combined row set changes -- a fresh render, or a
// late plugin emission, including a priority section PREPENDING rows
// above the file results. Pure and DOM-free so the vitest suite pins
// the rules directly (main.ts supplies the inputs).

export interface ReconcileInput {
  // The previously selected item's index in the NEW combined list,
  // matched by item identity (-1 = nothing was selected, or the item
  // is gone from the list).
  prevItemIndex: number;
  // The raw previous selection index: the clamp fallback when the
  // selected item itself vanished.
  prevSelected: number;
  rowCount: number;
  queryBlank: boolean;
  // Whether the user has navigated (arrows / Home / End) since the
  // current query generation started. Mouse hover is decorative
  // (CSS :hover) and deliberately never counts as navigation.
  userNavigated: boolean;
}

// reconcileSelection: a user who has navigated keeps their item -- by
// IDENTITY, so priority rows prepended above it shift its index
// without stealing the selection -- falling back to a clamped index
// when the item is gone. A user who has NOT navigated gets
// auto-select re-run on row 0, so a late-arriving prioritized apps
// section takes the selection Spotlight-style; the blank-query cheat
// sheet stays unselected either way (Enter on an empty bar is a
// no-op until the list is entered explicitly).
export function reconcileSelection(inp: ReconcileInput): number {
  if (inp.userNavigated) {
    if (inp.prevItemIndex >= 0) {
      return inp.prevItemIndex;
    }
    if (inp.rowCount === 0) {
      return -1;
    }
    return Math.min(Math.max(inp.prevSelected, 0), inp.rowCount - 1);
  }
  if (!inp.queryBlank && inp.rowCount > 0) {
    return 0;
  }
  return -1;
}
