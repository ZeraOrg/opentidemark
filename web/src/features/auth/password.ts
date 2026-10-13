// SPDX-License-Identifier: Apache-2.0
//
// Client-side hints for the password policy of SPEC §15.2 (≥ 12 and ≤ 256 characters, not the
// username). The server stays authoritative (it also rejects common passwords).

export const MIN_PASSWORD = 12;
export const MAX_PASSWORD = 256;

/** A user-facing problem with `pw`, or null when it passes the client-side checks. */
export function passwordProblem(pw: string, username: string, current?: string): string | null {
  if ([...pw].length < MIN_PASSWORD) return `Use at least ${MIN_PASSWORD} characters.`;
  if ([...pw].length > MAX_PASSWORD) return `Use at most ${MAX_PASSWORD} characters.`;
  if (username !== '' && pw.toLowerCase() === username.toLowerCase())
    return 'The password must not be the username.';
  if (current !== undefined && current !== '' && pw === current)
    return 'Choose a different password than the current one.';
  return null;
}
