// SPDX-License-Identifier: Apache-2.0
//
// Splits an exception text into its headline and stack frames and marks framework frames, so the
// detail view can collapse them (DESIGN §5.10 "Collapse frames").

export interface StackFrame {
  text: string;
  framework: boolean;
  /** `file:line` location inside the frame text, when present. */
  location: string | null;
}

export interface ParsedException {
  type: string;
  headline: string;
  frames: StackFrame[];
}

const FRAMEWORK = [
  /^\s*at (System|Microsoft|Npgsql|Grpc|Polly)\./,
  /^\s*at (java|javax|jdk|sun|org\.springframework|org\.apache|io\.netty|kotlin|kotlinx)\./,
  /^\s*at (node:|internal\/|process\.|processTicksAndRejections)/,
  /node_modules/,
  /^\s*File ".*(site-packages|dist-packages|\/lib\/python)/,
  /^\s*(runtime|net\/http|reflect)\./,
  /^\s*---\s*End of/,
];

const LOCATION = /(?:\bin\s+(\S+:line \d+))|\(([^()]+:\d+(?::\d+)?)\)|"([^"]+)", line (\d+)|(\S+\.go:\d+)/;

function location(line: string): string | null {
  const m = LOCATION.exec(line);
  if (!m) return null;
  if (m[3] !== undefined) return `${m[3]}:${m[4] ?? ''}`;
  return m[1] ?? m[2] ?? m[5] ?? null;
}

/** Parses `Type: message\n   at frame…`. */
export function parseException(text: string): ParsedException {
  const lines = text.replace(/\r\n?/g, '\n').split('\n');
  const headline = lines[0]?.trim() ?? '';
  const colon = headline.indexOf(':');
  const type = colon > 0 && !headline.slice(0, colon).includes(' ') ? headline.slice(0, colon) : '';
  const frames = lines
    .slice(1)
    .filter((l) => l.trim() !== '')
    .map((l) => ({
      text: l.replace(/\s+$/, ''),
      framework: FRAMEWORK.some((r) => r.test(l)),
      location: location(l),
    }));
  return { type, headline, frames };
}
