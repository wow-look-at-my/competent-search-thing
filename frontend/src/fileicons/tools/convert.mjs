#!/usr/bin/env node
// convert.mjs -- regenerates frontend/src/fileicons/{fonts/,data.json}
// from pinned checkouts of file-icons/atom and primer/octicons (the
// commits are recorded in ../README.md).
//
// Usage:
//   node convert.mjs <file-icons-atom-checkout> <octicons-checkout> <fileicons-dir>
//
// Pipeline (each parser fails loudly on anything it does not
// recognize -- the inputs are pinned, so drift means re-verify):
//   1. styles/icons.less  -> icon class -> {font, codepoint} (929
//      single-line rules; the font mixins .octicons/.fa/.mf/.devicons/
//      .fi name the five faces).
//   2. styles/colours.less + the mixins.less semantics -> colour
//      class -> {dark hex, light hex}: the LESS palette math
//      (lighten/darken/saturate by percentage points of HSL) with the
//      brighten-if-needed / darken-if-needed mixins evaluated for
//      both theme cases (dark = background lightness < 50%).
//   3. config.cson (directoryIcons + fileIcons) -> match rules:
//      string patterns = case-insensitive basename suffixes, regexes
//      kept verbatim; per-rule colour ("auto-X" expands to
//      ["medium-X","dark-X"] = [dark-theme, light-theme] exactly like
//      lib/icons/icon-definition.js); priority desc then source
//      order; matchPath rules dropped (we only match basenames).
//   4. Icon resolution drops rules whose class is not in icons.less
//      and EVERY rule using the Devicons face (fonts/devopicons.woff2
//      carries no license -- see ../LICENSES.md); every surviving
//      {font, codepoint} is verified against the font's actual cmap
//      (woff2cmap.mjs) so no rule can reference a missing glyph.
//   5. Defaults mirror the pack's Atom fallbacks: octicons file-text
//      for files, octicons file-directory for directories, uncolored.
//   6. Emits data.json (sorted, \u-escaped, pure-ASCII bytes) and
//      copies the four licensed woff2 fonts.

import fs from "node:fs";
import path from "node:path";
import { cmapCodepoints } from "./woff2cmap.mjs";

const [, , atomDir, octiconsDir, outDir] = process.argv;
if (!atomDir || !octiconsDir || !outDir) {
  console.error("usage: node convert.mjs <file-icons-atom> <octicons> <fileicons-dir>");
  process.exit(2);
}
const read = (...p) => fs.readFileSync(path.join(...p), "utf8");

/* ---- 1. icons.less: class -> {font, cp} --------------------------------- */

const FONT_MIXINS = { octicons: "oct", fa: "fa", mf: "mf", devicons: "di", fi: "fi" };
const icons = new Map();
{
  const rule = /^\.([A-Za-z0-9_-]+)-icon:before\s*\{([^}]*)\}/;
  for (const line of read(atomDir, "styles/icons.less").split("\n")) {
    const m = rule.exec(line);
    if (!m) {
      continue;
    }
    const font = /\.(octicons|fa|mf|devicons|fi)\b/.exec(m[2]);
    // content is either a CSS hex escape ("\eaae") or ONE literal
    // character ("z" -- the fi font maps styled glyphs at ASCII
    // codepoints for a few icons: curl, regex, stylus, vagrant,
    // yaml, zig, ...).
    const cp = /content:\s*"(?:\\([0-9A-Fa-f]+)|([^"\\]))"/.exec(m[2]);
    if (font && cp) {
      const code = cp[1] !== undefined ? parseInt(cp[1], 16) : cp[2].codePointAt(0);
      icons.set(m[1], { font: FONT_MIXINS[font[1]], cp: code });
    }
  }
  if (icons.size < 900) {
    throw new Error(`icons.less parse suspiciously small: ${icons.size}`);
  }
}

/* ---- 2. colours.less -> class -> {dark, light} -------------------------- */

function hexToRGB(hex) {
  const n = parseInt(hex.slice(1), 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}
function rgbToHSL([r, g, b]) {
  (r /= 255), (g /= 255), (b /= 255);
  const max = Math.max(r, g, b);
  const min = Math.min(r, g, b);
  const l = (max + min) / 2;
  if (max === min) {
    return [0, 0, l];
  }
  const d = max - min;
  const s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
  const h =
    max === r ? ((g - b) / d + (g < b ? 6 : 0)) / 6 : max === g ? ((b - r) / d + 2) / 6 : ((r - g) / d + 4) / 6;
  return [h, s, l];
}
function hslToHex([h, s, l]) {
  const f = (n) => {
    const k = (n + h * 12) % 12;
    const a = s * Math.min(l, 1 - l);
    const v = l - a * Math.max(-1, Math.min(k - 3, 9 - k, 1));
    return Math.round(v * 255)
      .toString(16)
      .padStart(2, "0");
  };
  return "#" + f(0) + f(8) + f(4);
}
const clamp01 = (v) => Math.max(0, Math.min(1, v));
const lighten = (hex, amt) => {
  const hsl = rgbToHSL(hexToRGB(hex));
  return hslToHex([hsl[0], hsl[1], clamp01(hsl[2] + amt / 100)]);
};
const darken = (hex, amt) => {
  const hsl = rgbToHSL(hexToRGB(hex));
  return hslToHex([hsl[0], hsl[1], clamp01(hsl[2] - amt / 100)]);
};
const saturate = (hex, amt) => {
  const hsl = rgbToHSL(hexToRGB(hex));
  return hslToHex([hsl[0], clamp01(hsl[1] + amt / 100), hsl[2]]);
};

const colourClasses = new Map();
{
  const text = read(atomDir, "styles/colours.less");
  const vars = new Map();
  for (const m of text.matchAll(/^@([\w-]+):\s*([^;]+);/gm)) {
    let v = m[2].trim();
    const ref = /^@([\w-]+)$/.exec(v);
    if (ref) {
      v = vars.get(ref[1]);
    } else {
      const fn = /^(lighten|darken)\(@([\w-]+),\s*@([\w-]+)\)$/.exec(v);
      if (fn) {
        const amt = parseFloat(vars.get(fn[3]));
        v = (fn[1] === "lighten" ? lighten : darken)(vars.get(fn[2]), amt);
      }
    }
    if (!/^(#[0-9a-fA-F]{6}|[\d.]+%)$/.test(v)) {
      throw new Error(`colours.less var @${m[1]} unresolved: ${v}`);
    }
    vars.set(m[1], v);
  }
  // Class rules; mixin semantics ported from styles/mixins.less: the
  // dark-theme case is lightness(background) < 50%.
  const rule =
    /^\.([\w-]+):before\s*\{\s*(?:color:\s*@([\w-]+)|\.(darken-if-needed|brighten-if-needed|brighten-grey-if-needed)\(@([\w-]+),\s*([\d.]+)%\))\s*;?\s*\}/gm;
  for (const m of text.matchAll(rule)) {
    const cls = m[1];
    if (m[2] !== undefined) {
      const v = vars.get(m[2]);
      colourClasses.set(cls, { dark: v, light: v });
    } else {
      const base = vars.get(m[4]);
      const amt = parseFloat(m[5]);
      let entry;
      if (m[3] === "darken-if-needed") {
        entry = { dark: base, light: darken(base, amt) };
      } else if (m[3] === "brighten-if-needed") {
        entry = { dark: saturate(lighten(base, amt), amt), light: base };
      } else {
        entry = { dark: lighten(base, amt), light: base };
      }
      colourClasses.set(cls, entry);
    }
  }
  if (colourClasses.size !== 30) {
    throw new Error(`colours.less parsed ${colourClasses.size} classes, expected 30`);
  }
}

/* ---- 3. config.cson ------------------------------------------------------ */

// parseValue reads one string or regex from s at index i, returning
// {value, end} where value is {s: string} or {r, f} -- or null when
// the token is neither (booleans, numbers, object tails).
function parseValue(s, i) {
  while (i < s.length && s[i] === " ") {
    i++;
  }
  const q = s[i];
  if (q === '"' || q === "'") {
    let out = "";
    for (let j = i + 1; j < s.length; j++) {
      if (s[j] === "\\") {
        out += s[j + 1];
        j++;
      } else if (s[j] === q) {
        return { value: { s: out }, end: j + 1 };
      } else {
        out += s[j];
      }
    }
    throw new Error(`unterminated string: ${s}`);
  }
  if (q === "/") {
    let cls = false;
    for (let j = i + 1; j < s.length; j++) {
      if (s[j] === "\\") {
        j++;
      } else if (cls) {
        cls = s[j] !== "]";
      } else if (s[j] === "[") {
        cls = true;
      } else if (s[j] === "/") {
        const fm = /^[a-z]*/.exec(s.slice(j + 1));
        return { value: { r: s.slice(i + 1, j), f: fm[0] }, end: j + 1 + fm[0].length };
      }
    }
    throw new Error(`unterminated regex: ${s}`);
  }
  return null;
}

// collapseHeregexes rewrites CoffeeScript /// ... ///flags extended
// regexes (5 in the pinned config.cson) into ordinary single-line
// /.../flags literals: unescaped whitespace is insignificant and
// dropped, escaped characters pass through verbatim, and internal
// unescaped slashes are escaped so the single-line scanner in
// parseValue finds the right terminator.
function collapseHeregexes(text) {
  return text.replace(/\/\/\/([\s\S]*?)\/\/\/([a-z]*)/g, (_, body, flags) => {
    let out = "";
    for (let i = 0; i < body.length; i++) {
      const ch = body[i];
      if (ch === "\\") {
        out += ch + body[i + 1];
        i++;
      } else if (/\s/.test(ch)) {
        continue;
      } else if (ch === "/") {
        out += "\\/";
      } else {
        out += ch;
      }
    }
    new RegExp(out, flags); // validate the collapse
    return `/${out}/${flags}`;
  });
}

function parseCson(text) {
  const lines = collapseHeregexes(text).split("\n");
  const sections = {};
  let section = null;
  let entry = null;
  let inMatch = false;
  let openItem = false; // a multi-line match item's continuation
  for (let idx = 0; idx < lines.length; idx++) {
    const raw = lines[idx];
    const line = raw.replace(/\s+$/, "");
    if (line === "" || /^\s*#/.test(line)) {
      continue;
    }
    const depth = /^\t*/.exec(line)[0].length;
    const body = line.slice(depth);
    if (inMatch) {
      if (openItem) {
        // Property tail of a multi-line item (alias/scope/priority
        // lines); the pattern and colour always sit on the item's
        // first line, so these are swallowed until the closing "]".
        if (/\](\s+#.*)?$/.test(body)) {
          openItem = false;
        }
        continue;
      }
      if (/^\](\s+#.*)?$/.test(body)) {
        inMatch = false;
        continue;
      }
      if (!body.startsWith("[")) {
        throw new Error(`line ${idx + 1}: expected match array item: ${body}`);
      }
      const first = parseValue(body, 1);
      if (first === null) {
        throw new Error(`line ${idx + 1}: unparseable match pattern: ${body}`);
      }
      let colour;
      let j = first.end;
      while (j < body.length && body[j] === " ") {
        j++;
      }
      if (body[j] === ",") {
        const second = parseValue(body, j + 1);
        if (second !== null && second.value.s !== undefined) {
          colour = second.value.s;
        }
      }
      entry.match.push({ pattern: first.value, colour });
      if (!/\](\s+#.*)?$/.test(body)) {
        openItem = true;
      }
      continue;
    }
    if (depth === 0 && /^[\w]+:$/.test(body)) {
      section = [];
      sections[body.slice(0, -1)] = section;
      entry = null;
      continue;
    }
    if (section === null) {
      continue; // the documentation header
    }
    if (depth === 1) {
      // The entry name, optionally with a trailing "# comment".
      const m = /^(?:"([^"]+)"|'([^']+)'|([^:#]+)):(?:\s+#.*)?$/.exec(body);
      if (!m) {
        throw new Error(`line ${idx + 1}: expected entry name: ${body}`);
      }
      entry = { name: m[1] ?? m[2] ?? m[3], props: {}, match: [] };
      section.push(entry);
      continue;
    }
    if (depth === 2 && entry !== null) {
      const kv = /^([\w]+):\s*(.*)$/.exec(body);
      if (!kv) {
        throw new Error(`line ${idx + 1}: expected property: ${body}`);
      }
      const [, key, rest] = kv;
      if (key === "match" && rest === "[") {
        inMatch = true;
      } else if (key === "match") {
        const v = parseValue(rest, 0);
        if (v === null) {
          throw new Error(`line ${idx + 1}: unparseable match: ${rest}`);
        }
        entry.match.push({ pattern: v.value, colour: undefined });
      } else if (key === "icon" || key === "colour") {
        const v = parseValue(rest, 0);
        entry.props[key] = v === null ? rest : v.value.s;
      } else if (key === "priority") {
        entry.props.priority = parseFloat(rest);
      } else if (key === "matchPath" || key === "noSuffix" || key === "generic") {
        entry.props[key] = /^true\b/.test(rest.trim());
      }
      // alias/scope/interpreter/signature and friends: irrelevant to
      // basename matching, deliberately ignored.
      continue;
    }
    throw new Error(`line ${idx + 1}: unexpected structure: ${raw}`);
  }
  return sections;
}

/* ---- 4. assemble rules --------------------------------------------------- */

const stats = { rules: 0, matchPath: 0, devicons: 0, noIcon: 0, noGlyph: 0, entries: 0 };
const droppedIcons = new Set();
const noGlyph = new Set();

function colourPair(name) {
  if (name === undefined) {
    return undefined;
  }
  // An explicit [darkTheme, lightTheme] pair (10 entries write one;
  // lib setColours keeps arrays verbatim, single element doubled).
  let darkCls;
  let lightCls;
  const pair = /^\[\s*"([\w-]+)"(?:\s*,\s*"([\w-]+)")?\s*\]$/.exec(name);
  if (pair) {
    darkCls = pair[1];
    lightCls = pair[2] ?? pair[1];
  } else {
    const auto = /^auto-(\w+)$/.exec(name);
    [darkCls, lightCls] = auto ? [`medium-${auto[1]}`, `dark-${auto[1]}`] : [name, name];
  }
  const dark = colourClasses.get(darkCls);
  const light = colourClasses.get(lightCls);
  if (!dark || !light) {
    throw new Error(`unknown colour class: ${name}`);
  }
  return [dark.dark, light.light];
}

function buildRules(entries, cmaps) {
  const out = [];
  let order = 0;
  for (const entry of entries) {
    stats.entries++;
    if (entry.props.matchPath) {
      stats.matchPath += entry.match.length;
      continue;
    }
    const cls = entry.props.icon; // noSuffix names (Atom builtin
    // classes) are not in icons.less and drop below
    const icon = icons.get(cls);
    if (!icon) {
      stats.noIcon += entry.match.length;
      droppedIcons.add(String(cls));
      continue;
    }
    if (icon.font === "di") {
      stats.devicons += entry.match.length;
      continue;
    }
    if (!cmaps[icon.font].has(icon.cp)) {
      // The bundled font genuinely lacks the glyph (upstream would
      // render a fallback/tofu there too) -- drop the rules so the
      // entry falls through to the default icon, and say so.
      stats.noGlyph += entry.match.length;
      noGlyph.add(`${entry.name} (U+${icon.cp.toString(16).toUpperCase()} ${icon.font})`);
      continue;
    }
    for (const m of entry.match) {
      const rule = {
        i: [icon.font, icon.cp],
        p: entry.props.priority ?? 1,
        o: order++,
      };
      if (m.pattern.s !== undefined) {
        rule.s = m.pattern.s.toLowerCase();
      } else {
        new RegExp(m.pattern.r, m.pattern.f); // validate
        rule.r = m.pattern.r;
        if (m.pattern.f !== "") {
          rule.f = m.pattern.f;
        }
      }
      const c = colourPair(m.colour ?? entry.props.colour);
      if (c !== undefined) {
        rule.c = c;
      }
      out.push(rule);
      stats.rules++;
    }
  }
  out.sort((a, b) => (a.p !== b.p ? b.p - a.p : a.o - b.o));
  for (const r of out) {
    delete r.p;
    delete r.o;
  }
  return out;
}

/* ---- 5.-6. fonts, defaults, emit ----------------------------------------- */

const FONT_FILES = {
  fi: ["file-icons.woff2", path.join(atomDir, "fonts/file-icons.woff2")],
  fa: ["fontawesome.woff2", path.join(atomDir, "fonts/fontawesome.woff2")],
  mf: ["mfixx.woff2", path.join(atomDir, "fonts/mfixx.woff2")],
  oct: ["octicons.woff2", path.join(octiconsDir, "build/font/octicons.woff2")],
};
const cmaps = {};
for (const [id, [, src]] of Object.entries(FONT_FILES)) {
  cmaps[id] = cmapCodepoints(fs.readFileSync(src));
}
cmaps.di = new Set(); // excluded: never consulted, never shipped

const cson = parseCson(read(atomDir, "config.cson"));
if (!cson.fileIcons || !cson.directoryIcons) {
  throw new Error("config.cson: missing fileIcons/directoryIcons sections");
}
const fileRules = buildRules(cson.fileIcons, cmaps);
const dirRules = buildRules(cson.directoryIcons, cmaps);

const defFile = ["oct", 0xf011]; // octicons file-text (Atom's icon-file-text)
const defDir = ["oct", 0xf016]; // octicons file-directory
for (const [font, cp] of [defFile, defDir]) {
  if (!cmaps[font].has(cp)) {
    throw new Error(`default glyph U+${cp.toString(16)} missing from ${font}`);
  }
}

fs.mkdirSync(path.join(outDir, "fonts"), { recursive: true });
let fontBytes = 0;
for (const [name, src] of Object.values(FONT_FILES)) {
  fs.copyFileSync(src, path.join(outDir, "fonts", name));
  fontBytes += fs.statSync(src).size;
}

const data = { fileRules, dirRules, defFile, defDir };
const json =
  JSON.stringify(data, null, 1).replace(
    /[\u0080-\uffff]/g,
    (c) => "\\u" + c.charCodeAt(0).toString(16).padStart(4, "0"),
  ) + "\n";
fs.writeFileSync(path.join(outDir, "data.json"), json, "ascii");

console.log(
  `converted: ${fileRules.length} file rules + ${dirRules.length} dir rules ` +
    `from ${stats.entries} entries; 4 fonts (${fontBytes} bytes); dropped: ` +
    `${stats.devicons} devicons-face rules (unlicensed font), ` +
    `${stats.matchPath} path-scoped, ${stats.noIcon} without an icon class ` +
    `[${[...droppedIcons].sort().join(", ")}], ${stats.noGlyph} glyphless ` +
    `[${[...noGlyph].sort().join(", ")}]`,
);
