// Type declarations for woff2cmap.mjs, consumed by the vitest suite
// (frontend/src/fileicons.test.ts imports the .mjs directly for the
// cmap-integrity gate; tsc resolves this sibling declaration). Keep
// in lockstep with woff2cmap.mjs.

// cmapCodepoints parses a WOFF2 font (a node Buffer) and returns the
// set of Unicode codepoints its cmap maps to real glyphs.
export declare function cmapCodepoints(woff2: Buffer): Set<number>;
