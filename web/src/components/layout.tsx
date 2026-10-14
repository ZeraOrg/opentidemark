// SPDX-License-Identifier: Apache-2.0
//
// Layout primitives: page heads, cards, tabs, empty states, notices and code blocks.

import { AlertCircle, AlertTriangle, CheckCircle2, Info, Search } from 'lucide-react';
import { useId, type ReactNode } from 'react';
import { cx } from '../lib/cx';
import s from './layout.module.css';

export { s as layoutStyles };

export function Page({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cx(s.page, className)}>{children}</div>;
}

export function PageHead({
  title,
  note,
  children,
  before,
}: {
  title: ReactNode;
  note?: ReactNode;
  children?: ReactNode;
  before?: ReactNode;
}) {
  return (
    <div className={s.pageHead}>
      <div className={s.pageTitle}>
        {before}
        <h1>{title}</h1>
        {note !== undefined ? <span className={s.pageNote}>{note}</span> : null}
      </div>
      {children !== undefined ? <div className={s.pageActions}>{children}</div> : null}
    </div>
  );
}

export function Card({
  title,
  subtitle,
  actions,
  children,
  className,
  bodyClassName,
  flush,
  headExtra,
}: {
  title?: ReactNode;
  subtitle?: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
  className?: string;
  bodyClassName?: string;
  flush?: boolean;
  headExtra?: ReactNode;
}) {
  const titleId = useId();
  return (
    <section
      className={cx(s.card, className)}
      aria-labelledby={typeof title === 'string' ? titleId : undefined}
    >
      {title !== undefined || actions !== undefined ? (
        <header className={s.cardHead}>
          {title !== undefined ? (
            <h2 id={titleId} className={s.cardTitle}>
              {title}
            </h2>
          ) : null}
          {subtitle !== undefined ? <span className={s.cardSubtitle}>{subtitle}</span> : null}
          {headExtra}
          {actions !== undefined ? <div className={s.cardActions}>{actions}</div> : null}
        </header>
      ) : null}
      <div className={cx(flush === true ? s.flush : s.cardBody, bodyClassName)}>{children}</div>
    </section>
  );
}

export interface TabItem<T extends string> {
  value: T;
  label: ReactNode;
  count?: number;
}

export function Tabs<T extends string>({
  items,
  value,
  onChange,
  label,
  className,
}: {
  items: readonly TabItem<T>[];
  value: T;
  onChange: (v: T) => void;
  label: string;
  className?: string;
}) {
  return (
    <div className={cx(s.tabs, className)} role="tablist" aria-label={label}>
      {items.map((t) => (
        <button
          key={t.value}
          type="button"
          role="tab"
          aria-selected={t.value === value}
          className={s.tab}
          onClick={() => onChange(t.value)}
        >
          {t.label}
          {t.count !== undefined ? <span className={s.tabCount}>{t.count}</span> : null}
        </button>
      ))}
    </div>
  );
}

export function EmptyState({
  title,
  hint,
  icon,
  action,
}: {
  title: ReactNode;
  hint?: ReactNode;
  icon?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className={s.empty}>
      {icon ?? <Search size={20} aria-hidden="true" />}
      <div className={s.emptyTitle}>{title}</div>
      {hint !== undefined ? <div className={s.emptyHint}>{hint}</div> : null}
      {action}
    </div>
  );
}

const NOTICE_ICON = {
  warning: AlertTriangle,
  danger: AlertCircle,
  info: Info,
  success: CheckCircle2,
} as const;

export function Notice({
  kind,
  children,
  className,
}: {
  kind: keyof typeof NOTICE_ICON;
  children: ReactNode;
  className?: string;
}) {
  const Icon = NOTICE_ICON[kind];
  return (
    <div
      className={cx(s.notice, s[`notice-${kind}`], className)}
      role={kind === 'danger' ? 'alert' : 'status'}
    >
      <Icon size={16} aria-hidden="true" />
      <div>{children}</div>
    </div>
  );
}

export function CodeBlock({
  children,
  className,
  label,
}: {
  children: ReactNode;
  className?: string;
  label?: string;
}) {
  return (
    <pre className={cx(s.code, className)} aria-label={label} tabIndex={0}>
      {children}
    </pre>
  );
}

function jsonNodes(value: unknown, indent: number, key: string): ReactNode[] {
  const pad = '  '.repeat(indent);
  if (Array.isArray(value)) {
    if (value.length === 0) return ['[]'];
    const out: ReactNode[] = ['[\n'];
    value.forEach((v, i) => {
      out.push(pad, '  ', ...jsonNodes(v, indent + 1, `${key}.${i}`), i < value.length - 1 ? ',\n' : '\n');
    });
    out.push(pad, ']');
    return out;
  }
  if (typeof value === 'object' && value !== null) {
    const entries = Object.entries(value);
    if (entries.length === 0) return ['{}'];
    const out: ReactNode[] = ['{\n'];
    entries.forEach(([k, v], i) => {
      out.push(
        pad,
        '  ',
        <span key={`${key}.${k}.k`} className={s.jsonKey}>
          {JSON.stringify(k)}
        </span>,
        ': ',
        ...jsonNodes(v, indent + 1, `${key}.${k}`),
        i < entries.length - 1 ? ',\n' : '\n',
      );
    });
    out.push(pad, '}');
    return out;
  }
  const cls =
    typeof value === 'string' ? s.jsonString : typeof value === 'number' ? s.jsonNumber : s.jsonLiteral;
  return [
    <span key={`${key}.v`} className={cls}>
      {JSON.stringify(value) ?? 'null'}
    </span>,
  ];
}

/** Pretty-printed, syntax-coloured JSON (DESIGN §5.10 raw JSON tab). */
export function JsonView({ value, className }: { value: unknown; className?: string }) {
  return (
    <CodeBlock className={className} label="Raw JSON">
      {jsonNodes(value, 0, 'r')}
    </CodeBlock>
  );
}
