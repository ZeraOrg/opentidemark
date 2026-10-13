// SPDX-License-Identifier: Apache-2.0
//
// CodeMirror styles under the strict CSP (SPEC §15.7 `style-src 'self'`). CodeMirror mounts its
// themes through style-mod, which writes a <style> element into a document's <head>; the CSP
// blocks that element, so the editor's base theme would never apply and every mount would log a
// violation. The composition root replaces `StyleModule.mount` once with this adapter, which
// keeps the same rule order but publishes the rules as one constructable stylesheet in
// `document.adoptedStyleSheets` (not inline CSS, so the CSP allows it). Adopted sheets cascade
// after the document's own stylesheets, so the rules go into the cascade layer `codemirror`:
// unlayered design CSS (the CSS modules) always wins over the library defaults. Shadow roots and
// browsers without constructable stylesheets keep the library's own mount.

/** The part of a style-mod `StyleModule` the adapter needs. */
export interface StyleSource {
  getRules(): string;
}

/** Where the rules end up: a document (or a fake in tests). */
export interface StyleRoot {
  adoptedStyleSheets: CSSStyleSheet[];
}

/** Cascade layer that holds the library rules (below every unlayered stylesheet). */
export const STYLE_LAYER = 'codemirror';

export type MountFn = (
  root: Document | ShadowRoot | DocumentOrShadowRoot,
  modules: StyleSource | readonly StyleSource[],
  options?: { nonce?: string },
) => void;

/** The static side of `StyleModule` that is patched. */
export interface StyleModuleClass {
  mount: MountFn;
}

/**
 * Inserts the modules that are new to `mounted`, each after the previous module of the same
 * call (so later modules keep precedence, as in style-mod). Returns whether anything changed.
 */
export function mergeModules(mounted: StyleSource[], incoming: readonly StyleSource[]): boolean {
  let changed = false;
  let at = 0;
  for (const mod of incoming) {
    const index = mounted.indexOf(mod);
    if (index === -1) {
      mounted.splice(at, 0, mod);
      at++;
      changed = true;
    } else if (index < at) {
      mounted.splice(index, 1);
      mounted.splice(at - 1, 0, mod);
      changed = true;
    } else {
      at = index + 1;
    }
  }
  return changed;
}

/** Returns a mount function that serves `doc` from one adopted stylesheet. */
export function adoptedMount(doc: StyleRoot, makeSheet: () => CSSStyleSheet, fallback: MountFn): MountFn {
  const mounted: StyleSource[] = [];
  let sheet: CSSStyleSheet | null = null;
  return (root, modules, options) => {
    if (root !== doc) {
      fallback(root, modules, options);
      return;
    }
    const list: readonly StyleSource[] = Array.isArray(modules) ? modules : [modules as StyleSource];
    const changed = mergeModules(mounted, list);
    if (sheet === null) sheet = makeSheet();
    if (changed)
      sheet.replaceSync(`@layer ${STYLE_LAYER} {\n${mounted.map((m) => m.getRules()).join('\n')}\n}`);
    if (!doc.adoptedStyleSheets.includes(sheet)) doc.adoptedStyleSheets = [sheet, ...doc.adoptedStyleSheets];
  };
}

/** Patches `cls.mount` for `doc` when constructable stylesheets are available. */
export function installAdoptedStyles(cls: StyleModuleClass, doc: Document): boolean {
  const Sheet = doc.defaultView?.CSSStyleSheet;
  if (Sheet === undefined || !('adoptedStyleSheets' in doc) || !('replaceSync' in Sheet.prototype))
    return false;
  cls.mount = adoptedMount(doc, () => new Sheet(), cls.mount.bind(cls));
  return true;
}
