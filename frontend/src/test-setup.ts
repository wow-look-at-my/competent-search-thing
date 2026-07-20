// Vitest setup: load the REAL index.html markup into jsdom before the
// test modules import render.ts (which grabs its <template> elements
// at module load; setup files run first). Reading the shipped file --
// never a copy -- keeps the DOM-order tests honest: they fail if
// index.html's result zones or templates change shape. innerHTML
// assignment never executes script elements, so the Vite module
// script tag is inert here.

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const html = readFileSync(join(here, "..", "index.html"), "utf-8");
const body = /<body>([\s\S]*)<\/body>/.exec(html);
if (body === null) {
  throw new Error("index.html has no <body> block");
}
document.body.innerHTML = body[1];
