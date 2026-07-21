// emitbin.mjs -- emits data.bin, the committed icon-mapping artifact,
// as a binpazer container (https://github.com/wow-look-at-my/bin-file-fmt).
//
// Split of responsibilities: encodeIconPayload() serializes the rule
// set into the IconRules payload encoding v1 (defined below, decoded
// Go-side by internal/fileicons -- keep the two in lockstep), and
// packIconTable() wraps that payload in the container via the
// format's FIRST-PARTY writer, the `binpazer` CLI (`pack` from a
// JSON manifest, then `validate` as a spec-conformance gate). We
// deliberately do not re-implement the container layer in either
// direction. Install the CLI from pazer.build (project
// bin-file-fmt/binpazer) or build it from the bin-file-fmt repo
// (`cd go && go-toolchain`); point $BINPAZER at the binary or have
// `binpazer` on PATH.
//
// IconRules payload encoding v1 (little-endian, byte-packed):
//   u32 version = 1
//   u8  fontCount, then per font: u8 nameLen + name bytes (the rule
//       fontIdx space, in listed order)
//   defFile: u8 fontIdx + u32 codepoint
//   defDir:  u8 fontIdx + u32 codepoint
//   u32 fileRuleCount
//   u32 dirRuleCount
//   rules (fileRules then dirRules), each:
//     u8  kind: bits0-2 fontIdx, bit3 isRegex, bit4 hasColor,
//               bit5 colorLightSame, bits6-7 reserved (0)
//     u32 codepoint
//     [hasColor] 3 bytes dark RGB; [!colorLightSame] 3 bytes light RGB
//     [isRegex]  u8 flagsLen + ASCII regex flags
//     u16 patternLen + UTF-8 pattern (suffix or regex source)
//
// Both output steps are deterministic: the encoding has no
// timestamps or maps, and binpazer files carry none either (the
// three bin-file-fmt implementations write byte-identical output).

import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

// The fixed container identities. WRITER_GUID names this converter in
// the file header; ICON_RULES_GUID is the IconRules block type's
// global identity -- ../binfmt.ts locates the block by this GUID, so
// the committed-data vitest gate breaks if the two ever diverge.
export const WRITER_GUID = "836ae6ad-b32b-427f-a163-edb297c55e93";
export const WRITER_NAME = "competent-search-thing fileicons convert.mjs";
export const ICON_RULES_GUID = "ea78f6bb-edf3-4b73-b114-a607c767ce0f";

// FONT_ORDER is the fontIdx space written into the payload header.
// The payload self-describes it, so the reader never hardcodes this
// list -- but the encoder pins it for deterministic output.
export const FONT_ORDER = ["fi", "fa", "mf", "oct", "di"];

class ByteWriter {
  constructor() {
    this.bytes = [];
  }
  u8(v) {
    if (!Number.isInteger(v) || v < 0 || v > 0xff) {
      throw new Error(`u8 out of range: ${v}`);
    }
    this.bytes.push(v);
  }
  u16(v) {
    if (!Number.isInteger(v) || v < 0 || v > 0xffff) {
      throw new Error(`u16 out of range: ${v}`);
    }
    this.bytes.push(v & 0xff, (v >> 8) & 0xff);
  }
  u32(v) {
    if (!Number.isInteger(v) || v < 0 || v > 0xffffffff) {
      throw new Error(`u32 out of range: ${v}`);
    }
    this.bytes.push(v & 0xff, (v >> 8) & 0xff, (v >> 16) & 0xff, (v >>> 24) & 0xff);
  }
  raw(arr) {
    for (const b of arr) {
      this.bytes.push(b);
    }
  }
  finish() {
    return Uint8Array.from(this.bytes);
  }
}

function rgb3(w, hex) {
  const m = /^#([0-9a-f]{6})$/i.exec(hex);
  if (!m) {
    throw new Error(`colour is not #rrggbb: ${hex}`);
  }
  const n = parseInt(m[1], 16);
  w.u8((n >> 16) & 0xff);
  w.u8((n >> 8) & 0xff);
  w.u8(n & 0xff);
}

const utf8 = new TextEncoder();

function writeIconRef(w, [font, cp]) {
  const idx = FONT_ORDER.indexOf(font);
  if (idx < 0) {
    throw new Error(`unknown font class: ${font}`);
  }
  if (!Number.isInteger(cp) || cp < 0 || cp > 0x10ffff) {
    throw new Error(`codepoint out of range: ${cp}`);
  }
  w.u8(idx);
  w.u32(cp);
}

function writeRule(w, r) {
  const fontIdx = FONT_ORDER.indexOf(r.i[0]);
  if (fontIdx < 0 || fontIdx > 0b111) {
    throw new Error(`unknown font class: ${r.i[0]}`);
  }
  const isRegex = r.r !== undefined;
  if (isRegex === (r.s !== undefined)) {
    throw new Error(`rule needs exactly one of r/s: ${JSON.stringify(r)}`);
  }
  const hasColor = r.c !== undefined;
  const colorSame = hasColor && r.c[0] === r.c[1];
  w.u8(fontIdx | (isRegex ? 0x08 : 0) | (hasColor ? 0x10 : 0) | (colorSame ? 0x20 : 0));
  if (!Number.isInteger(r.i[1]) || r.i[1] < 0 || r.i[1] > 0x10ffff) {
    throw new Error(`codepoint out of range: ${r.i[1]}`);
  }
  w.u32(r.i[1]);
  if (hasColor) {
    rgb3(w, r.c[0]);
    if (!colorSame) {
      rgb3(w, r.c[1]);
    }
  }
  if (isRegex) {
    const flags = utf8.encode(r.f ?? "");
    w.u8(flags.length);
    w.raw(flags);
  }
  const pattern = utf8.encode(isRegex ? r.r : r.s);
  w.u16(pattern.length);
  w.raw(pattern);
}

// encodeIconPayload serializes {fileRules, dirRules, defFile, defDir}
// (the rule shapes convert.mjs assembles: i=[font,cp], r/f or s,
// c=[dark,light]) into the IconRules payload encoding v1.
export function encodeIconPayload(data) {
  const w = new ByteWriter();
  w.u32(1); // payload version
  w.u8(FONT_ORDER.length);
  for (const name of FONT_ORDER) {
    const b = utf8.encode(name);
    w.u8(b.length);
    w.raw(b);
  }
  writeIconRef(w, data.defFile);
  writeIconRef(w, data.defDir);
  w.u32(data.fileRules.length);
  w.u32(data.dirRules.length);
  for (const r of data.fileRules) {
    writeRule(w, r);
  }
  for (const r of data.dirRules) {
    writeRule(w, r);
  }
  return w.finish();
}

function runBinpazer(args, what) {
  const bin = process.env.BINPAZER || "binpazer";
  const res = spawnSync(bin, args, { encoding: "utf8" });
  if (res.error && res.error.code === "ENOENT") {
    throw new Error(
      `binpazer CLI not found (${bin}); install it from pazer.build ` +
        `(project bin-file-fmt/binpazer) or build wow-look-at-my/bin-file-fmt ` +
        `(cd go && go-toolchain), or set $BINPAZER`,
    );
  }
  if (res.error) {
    throw res.error;
  }
  if (res.status !== 0) {
    throw new Error(`binpazer ${what} failed (exit ${res.status}):\n${res.stdout}${res.stderr}`);
  }
  return res.stdout;
}

// packIconTable wraps a payload in the binpazer container via the
// first-party CLI (pack from a data_base64 manifest, then validate)
// and writes it to outFile. One ancillary IconRules block with a
// CRC-32C trailer; no Block Index (a one-block file gains nothing
// from seek-by-type).
export function packIconTable(payload, outFile) {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "fileicons-pack-"));
  try {
    const manifest = {
      writer_guid: WRITER_GUID,
      writer_name: WRITER_NAME,
      types: [{ type_id: 1, guid: ICON_RULES_GUID, name: "IconRules" }],
      blocks: [
        {
          type_id: 1,
          flags: ["has_crc"],
          data_base64: Buffer.from(payload).toString("base64"),
        },
      ],
    };
    const manifestPath = path.join(tmp, "manifest.json");
    fs.writeFileSync(manifestPath, JSON.stringify(manifest));
    runBinpazer(["pack", manifestPath, "-o", outFile], "pack");
    runBinpazer(["validate", outFile], "validate");
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}
