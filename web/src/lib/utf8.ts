// SPDX-License-Identifier: Apache-2.0
//
// The API reports query positions as UTF-8 byte offsets (SPEC §10.6 tql.Error, §10.7
// Completion); the editor works in UTF-16 code units. These helpers convert between the two.

/** Number of UTF-8 bytes needed for the code point at `cp`. */
function utf8Length(cp: number): number {
  if (cp < 0x80) return 1;
  if (cp < 0x800) return 2;
  if (cp < 0x10000) return 3;
  return 4;
}

/** Converts a UTF-8 byte offset into a UTF-16 index of `text` (clamped to the text). */
export function byteToUtf16(text: string, bytePos: number): number {
  if (bytePos <= 0) return 0;
  let bytes = 0;
  let i = 0;
  while (i < text.length) {
    const cp = text.codePointAt(i) ?? 0;
    const next = bytes + utf8Length(cp);
    if (next > bytePos) return i;
    bytes = next;
    i += cp > 0xffff ? 2 : 1;
  }
  return text.length;
}

/** Converts a UTF-16 index of `text` into a UTF-8 byte offset. */
export function utf16ToByte(text: string, index: number): number {
  let bytes = 0;
  let i = 0;
  const end = Math.min(Math.max(index, 0), text.length);
  while (i < end) {
    const cp = text.codePointAt(i) ?? 0;
    bytes += utf8Length(cp);
    i += cp > 0xffff ? 2 : 1;
  }
  return bytes;
}

/** Converts a byte range [pos, pos+len) into a UTF-16 range (at least one character wide). */
export function byteRangeToUtf16(text: string, pos: number, len: number): { from: number; to: number } {
  const from = byteToUtf16(text, pos);
  let to = byteToUtf16(text, pos + Math.max(len, 0));
  if (to <= from) to = Math.min(text.length, from + 1);
  return { from, to };
}
