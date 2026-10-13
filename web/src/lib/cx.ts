// SPDX-License-Identifier: Apache-2.0

/** Joins class names, skipping falsy values. */
export function cx(...names: (string | false | null | undefined)[]): string {
  return names.filter((n): n is string => typeof n === 'string' && n !== '').join(' ');
}
