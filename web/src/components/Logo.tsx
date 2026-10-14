// SPDX-License-Identifier: Apache-2.0
//
// The tide-mark logo (DESIGN §6). Colours come from --tm-logo-tile / --tm-logo-glyph so the
// dark (phosphor tile) and light (ink tile) variants follow the theme.

import s from './Logo.module.css';

export function LogoMark({ size = 24, title }: { size?: number; title?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      width={size}
      height={size}
      role={title === undefined ? undefined : 'img'}
      aria-label={title}
      aria-hidden={title === undefined ? true : undefined}
      className={s.mark}
    >
      <rect x="1" y="1" width="22" height="22" rx="6" className={s.tile} />
      <path d="M12 5v5" className={s.glyph} strokeWidth="2.2" strokeLinecap="round" />
      <path
        d="M4.5 15c2 0 2-2.6 4-2.6s2 2.6 4 2.6 2-2.6 4-2.6 1.5 1.6 3 2.2"
        className={s.glyph}
        strokeWidth="2.2"
        fill="none"
        strokeLinecap="round"
      />
    </svg>
  );
}

export function Wordmark({ size = 24 }: { size?: number }) {
  return (
    <span className={s.brand}>
      <LogoMark size={size} />
      <span className={s.word}>OpenTidemark</span>
    </span>
  );
}
