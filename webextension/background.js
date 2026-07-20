// The thin MV2 background entry: all logic lives in logic.mjs (pure,
// importable, vitest-covered from frontend/src/ffext-logic.test.ts).
// The persistent background page owns ONE native-messaging port to the
// app's firefox-host relay for the whole browser session.
import { createController } from "./logic.mjs";

createController(browser, {
  log: (msg) => console.log(`[competent-search-thing] ${msg}`),
}).start();
