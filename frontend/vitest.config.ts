import { defineConfig } from "vitest/config";

// The frontend DOM-ordering gate: a tiny, deterministic jsdom suite
// (src/*.test.ts) pinning the priority-zone layout and the flat
// selection rules. The setup file loads the REAL index.html markup
// before the test modules import render.ts (which grabs its
// <template> elements at module load).
export default defineConfig({
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
  },
});
