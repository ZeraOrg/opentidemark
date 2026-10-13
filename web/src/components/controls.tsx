// SPDX-License-Identifier: Apache-2.0
//
// Basic controls (DESIGN §5.1–§5.6): buttons, chips, tags, inputs, selects, toggles, segmented
// controls and checkboxes. All are real form elements with visible focus.

import { ChevronDown, Loader2, X } from 'lucide-react';
import {
  forwardRef,
  useId,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from 'react';
import type { LevelName } from '../api/types';
import { cx } from '../lib/cx';
import s from './controls.module.css';

export type ButtonVariant = 'default' | 'primary' | 'ghost' | 'danger' | 'dangerSolid' | 'highlight';

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: 'md' | 'sm';
  icon?: ReactNode;
  iconOnly?: boolean;
  busy?: boolean;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  {
    variant = 'default',
    size = 'md',
    icon,
    iconOnly,
    busy,
    className,
    children,
    type = 'button',
    disabled,
    ...rest
  },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      className={cx(s.button, s[variant], size === 'sm' && s.sm, iconOnly && s.iconOnly, className)}
      disabled={disabled === true || busy === true}
      aria-busy={busy === true ? true : undefined}
      {...rest}
    >
      {busy === true ? <Loader2 size={14} className={s.spinner} aria-hidden="true" /> : icon}
      {iconOnly === true ? null : children}
    </button>
  );
});

export function Kbd({ children }: { children: ReactNode }) {
  return <kbd className={s.kbd}>{children}</kbd>;
}

const LEVEL_SHORT: Record<LevelName, string> = {
  Verbose: 'VRB',
  Debug: 'DBG',
  Information: 'INF',
  Warning: 'WRN',
  Error: 'ERR',
  Fatal: 'FTL',
};

export function levelClass(level: string): string {
  const key = `level-${level.toLowerCase()}`;
  return s[key] ?? s['level-information'] ?? '';
}

export function LevelChip({ level }: { level: string }) {
  const short = LEVEL_SHORT[level as LevelName] ?? level.slice(0, 3).toUpperCase();
  return (
    <span className={cx(s.level, levelClass(level))} aria-label={level} title={level}>
      {short}
    </span>
  );
}

export type StatusVariant = 'ok' | 'pending' | 'firing' | 'muted' | 'error' | 'info' | 'danger' | 'accent';

export function StatusChip({
  variant,
  children,
  dot,
}: {
  variant: StatusVariant;
  children: ReactNode;
  dot?: boolean;
}) {
  return (
    <span className={cx(s.status, s[`status-${variant}`])}>
      {dot === true ? <span className={s.dot} aria-hidden="true" /> : null}
      {children}
    </span>
  );
}

export function Tag({
  children,
  warning,
  title,
}: {
  children: ReactNode;
  warning?: boolean;
  title?: string;
}) {
  return (
    <span className={cx(s.tag, warning === true && s.tagWarning)} title={title}>
      {children}
    </span>
  );
}

export function RemovableChip({
  children,
  onRemove,
  label,
}: {
  children: ReactNode;
  onRemove: () => void;
  label: string;
}) {
  return (
    <span className={s.removable}>
      {children}
      <button type="button" onClick={onRemove} aria-label={`Remove ${label}`}>
        <X size={12} aria-hidden="true" />
      </button>
    </span>
  );
}

export const TextInput = forwardRef<
  HTMLInputElement,
  InputHTMLAttributes<HTMLInputElement> & { mono?: boolean }
>(function TextInput({ className, mono, ...rest }, ref) {
  return <input ref={ref} className={cx(s.input, mono === true && s.mono, className)} {...rest} />;
});

export function TextArea({
  className,
  mono,
  ...rest
}: TextareaHTMLAttributes<HTMLTextAreaElement> & { mono?: boolean }) {
  return <textarea className={cx(s.input, mono === true && s.mono, className)} {...rest} />;
}

export interface SelectOption {
  value: string;
  label: string;
}

export function Select({
  options,
  className,
  mono,
  ...rest
}: SelectHTMLAttributes<HTMLSelectElement> & { options: readonly SelectOption[]; mono?: boolean }) {
  return (
    <span className={cx(s.selectWrap, className)}>
      <select className={cx(s.input, s.select, mono === true && s.mono)} {...rest}>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
      <ChevronDown size={14} aria-hidden="true" />
    </span>
  );
}

/** Label + control + help/error text. `children` receives the generated id. */
export function Field({
  label,
  note,
  help,
  error,
  children,
  className,
}: {
  label: ReactNode;
  note?: ReactNode;
  help?: ReactNode;
  error?: ReactNode;
  children: (id: string) => ReactNode;
  className?: string;
}) {
  const id = useId();
  return (
    <div className={cx(s.field, className)}>
      <label className={s.label} htmlFor={id}>
        {label}
        {note !== undefined ? <span className={s.labelNote}> · {note}</span> : null}
      </label>
      {children(id)}
      {error !== undefined && error !== null && error !== false ? (
        <span className={s.error} role="alert">
          {error}
        </span>
      ) : help !== undefined ? (
        <span className={s.help}>{help}</span>
      ) : null}
    </div>
  );
}

export function Checkbox({
  checked,
  onChange,
  children,
  disabled,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  children: ReactNode;
  disabled?: boolean;
}) {
  return (
    <label className={s.checkbox}>
      <input
        type="checkbox"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span>{children}</span>
    </label>
  );
}

export function Toggle({
  checked,
  onChange,
  children,
  dot,
  className,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  children?: ReactNode;
  dot?: boolean;
  className?: string;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      className={cx(s.toggle, className)}
      onClick={() => onChange(!checked)}
    >
      <span className={s.track} aria-hidden="true" />
      {dot === true && checked ? <span className={s.liveDot} aria-hidden="true" /> : null}
      {children}
    </button>
  );
}

export function Segmented<T extends string>({
  options,
  value,
  onChange,
  label,
  className,
}: {
  options: readonly { value: T; label: ReactNode }[];
  value: T;
  onChange: (v: T) => void;
  label: string;
  className?: string;
}) {
  return (
    <div className={cx(s.segmented, className)} role="group" aria-label={label}>
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          className={s.segment}
          aria-pressed={o.value === value}
          onClick={() => onChange(o.value)}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

export function SignalChip({
  active,
  onToggle,
  children,
  title,
}: {
  active: boolean;
  onToggle: () => void;
  children: ReactNode;
  title?: string;
}) {
  return (
    <button type="button" className={s.signal} aria-pressed={active} onClick={onToggle} title={title}>
      {children}
    </button>
  );
}

export function Spinner({ size = 16 }: { size?: number }) {
  return <Loader2 size={size} className={s.spinner} aria-label="Loading" />;
}
