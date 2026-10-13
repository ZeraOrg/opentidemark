// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it, vi } from 'vitest';
import { adoptedMount, installAdoptedStyles, mergeModules, type StyleSource } from './adoptedStyles';

const mod = (rules: string): StyleSource => ({ getRules: () => rules });

class FakeSheet {
  text = '';
  replaceSync(t: string): void {
    this.text = t;
  }
}

describe('mergeModules', () => {
  it('keeps the order of each call and reorders on conflicts', () => {
    const [a, b, c] = [mod('a'), mod('b'), mod('c')];
    const list: StyleSource[] = [];
    expect(mergeModules(list, [a, b])).toBe(true);
    expect(mergeModules(list, [a, b])).toBe(false);
    expect(mergeModules(list, [a, c, b])).toBe(true);
    expect(list.map((m) => m.getRules())).toEqual(['a', 'c', 'b']);
    expect(mergeModules(list, [b, a])).toBe(true);
    expect(list.map((m) => m.getRules())).toEqual(['c', 'b', 'a']);
  });
});

describe('adoptedMount', () => {
  it('publishes the rules as one adopted sheet', () => {
    const doc = { adoptedStyleSheets: [] as CSSStyleSheet[] };
    const sheet = new FakeSheet();
    const make = vi.fn(() => sheet as unknown as CSSStyleSheet);
    const fallback = vi.fn();
    const mount = adoptedMount(doc, make, fallback);
    const [base, theme] = [mod('.cm-editor{}'), mod('.cm-theme{}')];
    mount(doc as unknown as Document, [base, theme]);
    mount(doc as unknown as Document, base);
    expect(make).toHaveBeenCalledTimes(1);
    expect(sheet.text).toBe('@layer codemirror {\n.cm-editor{}\n.cm-theme{}\n}');
    expect(doc.adoptedStyleSheets).toEqual([sheet]);
    expect(fallback).not.toHaveBeenCalled();
  });

  it('leaves other roots to the library', () => {
    const doc = { adoptedStyleSheets: [] as CSSStyleSheet[] };
    const fallback = vi.fn();
    const other = {} as ShadowRoot;
    adoptedMount(doc, () => new FakeSheet() as unknown as CSSStyleSheet, fallback)(other, mod('x'), {
      nonce: 'n',
    });
    expect(fallback).toHaveBeenCalledWith(other, expect.anything(), { nonce: 'n' });
  });
});

describe('installAdoptedStyles', () => {
  it('patches mount only when constructable sheets exist', () => {
    const original = vi.fn();
    const cls = { mount: original };
    const withSheets = {
      adoptedStyleSheets: [],
      defaultView: { CSSStyleSheet: FakeSheet },
    } as unknown as Document;
    expect(installAdoptedStyles(cls, withSheets)).toBe(true);
    expect(cls.mount).not.toBe(original);
    cls.mount({} as ShadowRoot, mod('x'));
    expect(original).toHaveBeenCalled();

    const plain = { mount: original };
    expect(installAdoptedStyles(plain, { defaultView: null } as unknown as Document)).toBe(false);
    expect(plain.mount).toBe(original);
  });
});
