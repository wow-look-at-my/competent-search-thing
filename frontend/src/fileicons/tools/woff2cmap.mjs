// woff2cmap.mjs -- minimal WOFF2 reader that extracts the set of
// Unicode codepoints a font's cmap covers. Shared by two consumers:
// tools/convert.mjs refuses to generate a mapping rule whose glyph is
// absent from its font, and src/fileicons.test.ts re-checks the
// committed data against the committed fonts so a hand-edit of either
// fails CI. Pure node (zlib brotli), no dependencies.
//
// Format notes (https://www.w3.org/TR/WOFF2/): a header, a variable
// table directory (known-tag index byte or explicit tag,
// UIntBase128 lengths, per-table transform in the top flag bits),
// then ONE Brotli stream holding every table back to back in
// directory order. Only glyf/loca use a non-null transform at
// version 0, so the cmap bytes appear verbatim; each table's slot in
// the stream is transformLength when transformed, origLength
// otherwise. The cmap itself is parsed for format 4 and format 12
// subtables (the only formats these icon fonts use).

import { brotliDecompressSync } from "node:zlib";

// Known table tags in WOFF2 flag order (spec section 5.2).
const KNOWN_TAGS = (
  "cmap head hhea hmtx maxp name OS/2 post cvt  fpgm glyf loca prep CFF  " +
  "VORG EBDT EBLC gasp hdmx kern LTSH PCLT VDMX vhea vmtx BASE GDEF GPOS " +
  "GSUB EBSC JSTF MATH CBDT CBLC COLR CPAL SVG  sbix acnt avar bdat blend " +
  "bloc bsln cvar fdsc feat fmtx fvar gvar hsty just lcar mort morx opbd " +
  "prop trak Zapf Silf Glat Gloc Feat Sill"
).match(/.{1,4}\s?/g).map((t) => t.trimEnd().padEnd(4));

function readUIntBase128(buf, pos) {
  let acc = 0;
  for (let i = 0; i < 5; i++) {
    const b = buf[pos.o++];
    acc = acc * 128 + (b & 0x7f);
    if ((b & 0x80) === 0) {
      return acc;
    }
  }
  throw new Error("UIntBase128 overlong");
}

// cmapCodepoints returns a Set of every codepoint mapped by the
// font's cmap (union over format 4 and 12 subtables).
export function cmapCodepoints(woff2) {
  if (woff2.readUInt32BE(0) !== 0x774f4632) {
    throw new Error("not a WOFF2 file");
  }
  const numTables = woff2.readUInt16BE(12);
  const pos = { o: 48 }; // fixed WOFF2 header size
  const tables = [];
  for (let i = 0; i < numTables; i++) {
    const flags = woff2[pos.o++];
    let tag;
    if ((flags & 0x3f) === 0x3f) {
      tag = woff2.toString("latin1", pos.o, pos.o + 4);
      pos.o += 4;
    } else {
      tag = KNOWN_TAGS[flags & 0x3f];
    }
    const transform = (flags >>> 6) & 0x03;
    const origLength = readUIntBase128(woff2, pos);
    let streamLength = origLength;
    // glyf/loca: transform version 0 IS transformed (null transform
    // is version 3); every other table is transformed when the
    // version is non-zero.
    const transformed = tag === "glyf" || tag === "loca" ? transform !== 3 : transform !== 0;
    if (transformed) {
      streamLength = readUIntBase128(woff2, pos);
    }
    tables.push({ tag, streamLength });
  }
  const stream = brotliDecompressSync(woff2.subarray(pos.o));
  let off = 0;
  let cmap = null;
  for (const t of tables) {
    if (t.tag === "cmap") {
      cmap = stream.subarray(off, off + t.streamLength);
      break;
    }
    off += t.streamLength;
  }
  if (cmap === null) {
    throw new Error("no cmap table");
  }
  return parseCmap(cmap);
}

function parseCmap(cmap) {
  const out = new Set();
  const numSub = cmap.readUInt16BE(2);
  for (let i = 0; i < numSub; i++) {
    const off = cmap.readUInt32BE(8 + i * 8);
    const format = cmap.readUInt16BE(off);
    if (format === 4) {
      // Glyph-aware: a code inside a segment that resolves to glyph 0
      // (.notdef) is NOT mapped -- range-only reading would report
      // false positives (e.g. a trailing 0xFFFD filler segment).
      const segX2 = cmap.readUInt16BE(off + 6);
      const ends = off + 14;
      const starts = ends + segX2 + 2;
      const deltas = starts + segX2;
      const rangeOffs = deltas + segX2;
      for (let s = 0; s < segX2; s += 2) {
        const end = cmap.readUInt16BE(ends + s);
        const start = cmap.readUInt16BE(starts + s);
        if (start === 0xffff) {
          continue;
        }
        const delta = cmap.readInt16BE(deltas + s);
        const rangeOff = cmap.readUInt16BE(rangeOffs + s);
        for (let c = start; c <= end; c++) {
          let glyph;
          if (rangeOff === 0) {
            glyph = (c + delta) & 0xffff;
          } else {
            const gi = rangeOffs + s + rangeOff + (c - start) * 2;
            glyph = cmap.readUInt16BE(gi);
            if (glyph !== 0) {
              glyph = (glyph + delta) & 0xffff;
            }
          }
          if (glyph !== 0) {
            out.add(c);
          }
        }
      }
    } else if (format === 12) {
      const nGroups = cmap.readUInt32BE(off + 12);
      for (let g = 0; g < nGroups; g++) {
        const base = off + 16 + g * 12;
        const start = cmap.readUInt32BE(base);
        const end = cmap.readUInt32BE(base + 4);
        for (let c = start; c <= end; c++) {
          out.add(c);
        }
      }
    }
  }
  return out;
}
