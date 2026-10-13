// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { byteRangeToUtf16, byteToUtf16, utf16ToByte } from './utf8';

// "a" 1 byte, "é" 2 bytes, "€" 3 bytes, "😀" 4 bytes (2 UTF-16 units).
const text = 'aé€😀b';

describe('utf8 offsets', () => {
  it.each([
    [0, 0],
    [1, 1],
    [3, 2],
    [6, 3],
    [10, 5],
    [11, 6],
    [99, 6],
    [-3, 0],
  ])('byte %i → utf16 %i', (b, u) => {
    expect(byteToUtf16(text, b)).toBe(u);
  });

  it('maps an offset inside a multi-byte character to its start', () => {
    expect(byteToUtf16(text, 2)).toBe(1);
  });

  it.each([
    [0, 0],
    [2, 3],
    [3, 6],
    [5, 10],
    [6, 11],
    [99, 11],
    [-1, 0],
  ])('utf16 %i → byte %i', (u, b) => {
    expect(utf16ToByte(text, u)).toBe(b);
  });

  it('converts ranges and widens empty ones to one character', () => {
    expect(byteRangeToUtf16(text, 3, 7)).toEqual({ from: 2, to: 5 });
    expect(byteRangeToUtf16(text, 1, 0)).toEqual({ from: 1, to: 2 });
    expect(byteRangeToUtf16(text, 11, 4)).toEqual({ from: 6, to: 6 });
  });
});
