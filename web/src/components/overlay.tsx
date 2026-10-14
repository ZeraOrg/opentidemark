// SPDX-License-Identifier: Apache-2.0
//
// Drawers, modals, popovers, menus and toasts (DESIGN §5.15, §5.17). Drawers and modals trap
// focus and close on Escape.

import { AlertCircle, AlertTriangle, CheckCircle2, Info, X } from 'lucide-react';
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from 'react';
import { createPortal } from 'react-dom';
import { cx } from '../lib/cx';
import { Button } from './controls';
import s from './overlay.module.css';

export { s as overlayStyles };

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"]), .cm-content';

/** Traps Tab focus inside `ref`, focuses the first control and restores focus on unmount. */
export function useFocusTrap(
  ref: RefObject<HTMLElement | null>,
  active: boolean,
  onEscape?: () => void,
): void {
  const escape = useRef(onEscape);
  escape.current = onEscape;
  useEffect(() => {
    if (!active) return undefined;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const root = ref.current;
    const first =
      root?.querySelector<HTMLElement>('[data-autofocus]') ?? root?.querySelector<HTMLElement>(FOCUSABLE);
    first?.focus();
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape' && escape.current) {
        if (document.querySelector('.cm-tooltip-autocomplete')) return;
        e.stopPropagation();
        escape.current();
        return;
      }
      if (e.key !== 'Tab' || !root) return;
      const items = Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
        (el) => el.offsetParent !== null,
      );
      if (items.length === 0) return;
      const firstEl = items[0];
      const lastEl = items[items.length - 1];
      if (e.shiftKey && document.activeElement === firstEl) {
        e.preventDefault();
        lastEl?.focus();
      } else if (!e.shiftKey && document.activeElement === lastEl) {
        e.preventDefault();
        firstEl?.focus();
      }
    };
    document.addEventListener('keydown', onKey, true);
    return () => {
      document.removeEventListener('keydown', onKey, true);
      previous?.focus();
    };
  }, [active, ref]);
}

export function Drawer({
  title,
  status,
  headActions,
  footer,
  children,
  onClose,
  wide,
  label,
}: {
  title: ReactNode;
  status?: ReactNode;
  headActions?: ReactNode;
  footer?: ReactNode;
  children: ReactNode;
  onClose: () => void;
  wide?: boolean;
  label: string;
}) {
  const ref = useRef<HTMLElement>(null);
  useFocusTrap(ref, true, onClose);
  return (
    <aside
      ref={ref}
      className={cx(s.drawer, wide === true && s.drawerWide)}
      role="dialog"
      aria-modal="true"
      aria-label={label}
    >
      <header className={s.drawerHead}>
        <div className={s.drawerTitle}>{title}</div>
        {status}
        <div className={s.drawerHeadActions}>
          {headActions}
          <Button
            variant="ghost"
            size="sm"
            iconOnly
            icon={<X size={16} />}
            aria-label="Close"
            onClick={onClose}
          />
        </div>
      </header>
      <div className={s.drawerBody}>{children}</div>
      {footer !== undefined ? <footer className={s.drawerFoot}>{footer}</footer> : null}
    </aside>
  );
}

export function DrawerFooter({ left, right }: { left?: ReactNode; right?: ReactNode }) {
  return (
    <>
      {left}
      <div className={s.drawerFootRight}>{right}</div>
    </>
  );
}

export function Modal({
  title,
  children,
  footer,
  onClose,
  wide,
  sheet,
}: {
  title: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  onClose: () => void;
  wide?: boolean;
  /** Below 600 px the dialog becomes a bottom sheet (DESIGN §8). */
  sheet?: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useFocusTrap(ref, true, onClose);
  return createPortal(
    <div
      className={cx(s.backdrop, sheet === true && s.sheetBackdrop)}
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={ref}
        className={cx(s.modal, wide === true && s.modalWide, sheet === true && s.sheet)}
        role="dialog"
        aria-modal="true"
        aria-label={typeof title === 'string' ? title : undefined}
      >
        <header className={s.modalHead}>
          <h2>{title}</h2>
          <span className={s.drawerHeadActions}>
            <Button
              variant="ghost"
              size="sm"
              iconOnly
              icon={<X size={16} />}
              aria-label="Close"
              onClick={onClose}
            />
          </span>
        </header>
        <div className={s.modalBody}>{children}</div>
        {footer !== undefined ? <footer className={s.modalFoot}>{footer}</footer> : null}
      </div>
    </div>,
    document.body,
  );
}

/** Closes when clicking outside `ref` or pressing Escape. */
export function useDismiss(ref: RefObject<HTMLElement | null>, open: boolean, onClose: () => void): void {
  const close = useRef(onClose);
  close.current = onClose;
  useEffect(() => {
    if (!open) return undefined;
    const onDown = (e: MouseEvent): void => {
      if (ref.current && e.target instanceof Node && !ref.current.contains(e.target)) close.current();
    };
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') close.current();
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open, ref]);
}

/** A trigger with an anchored popover panel. */
export function Popover({
  trigger,
  children,
  align = 'left',
  className,
  anchorClassName,
  open: controlledOpen,
  onOpenChange,
}: {
  trigger: (props: { open: boolean; toggle: () => void }) => ReactNode;
  children: ReactNode | ((close: () => void) => ReactNode);
  align?: 'left' | 'right';
  className?: string;
  /** Styles the anchor (e.g. to make a block-level trigger the panel's reference box). */
  anchorClassName?: string;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
}) {
  const [innerOpen, setInnerOpen] = useState(false);
  const open = controlledOpen ?? innerOpen;
  const setOpen = useCallback(
    (v: boolean) => {
      setInnerOpen(v);
      onOpenChange?.(v);
    },
    [onOpenChange],
  );
  const ref = useRef<HTMLSpanElement>(null);
  useDismiss(ref, open, () => setOpen(false));
  return (
    <span ref={ref} className={cx(s.popoverAnchor, anchorClassName)}>
      {trigger({ open, toggle: () => setOpen(!open) })}
      {open ? (
        <div className={cx(s.popover, align === 'right' && s.popoverRight, className)}>
          {typeof children === 'function' ? children(() => setOpen(false)) : children}
        </div>
      ) : null}
    </span>
  );
}

export function MenuItem({
  children,
  onClick,
  icon,
  checked,
}: {
  children: ReactNode;
  onClick: () => void;
  icon?: ReactNode;
  checked?: boolean;
}) {
  return (
    <button
      type="button"
      role={checked === undefined ? 'menuitem' : 'menuitemradio'}
      aria-checked={checked}
      className={s.menuItem}
      onClick={onClick}
    >
      {icon}
      {children}
    </button>
  );
}

export function MenuSeparator() {
  return <div className={s.menuSep} role="separator" />;
}

// ---- Toasts (§5.15) ---------------------------------------------------------------------------

export type ToastKind = 'success' | 'warning' | 'danger' | 'info';

export interface ToastInput {
  kind: ToastKind;
  title: string;
  body?: ReactNode;
  actions?: { label: string; onClick: () => void }[];
  /** Milliseconds; errors persist until closed by default. */
  duration?: number;
  icon?: ReactNode;
}

interface ToastItem extends ToastInput {
  id: number;
}

interface ToastApi {
  push(t: ToastInput): number;
  dismiss(id: number): void;
}

const ToastContext = createContext<ToastApi>({ push: () => 0, dismiss: () => undefined });

const TOAST_ICON = {
  success: CheckCircle2,
  warning: AlertTriangle,
  danger: AlertCircle,
  info: Info,
} as const;
const TOAST_DURATION_MS = 6000;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const next = useRef(1);
  const dismiss = useCallback((id: number) => setItems((xs) => xs.filter((x) => x.id !== id)), []);
  const push = useCallback(
    (t: ToastInput) => {
      const id = next.current++;
      setItems((xs) => [...xs.slice(-4), { ...t, id }]);
      const duration = t.duration ?? (t.kind === 'danger' ? 0 : TOAST_DURATION_MS);
      if (duration > 0) setTimeout(() => dismiss(id), duration);
      return id;
    },
    [dismiss],
  );
  const api = useMemo(() => ({ push, dismiss }), [push, dismiss]);
  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className={s.toasts}>
        {items.map((t) => {
          const Icon = TOAST_ICON[t.kind];
          return (
            <div
              key={t.id}
              className={cx(s.toast, s[`toast-${t.kind}`])}
              role={t.kind === 'danger' ? 'alert' : 'status'}
            >
              {t.icon ?? <Icon size={16} aria-hidden="true" />}
              <div className={s.toastContent}>
                <div className={s.toastTitle}>{t.title}</div>
                {t.body !== undefined ? <div className={s.toastBody}>{t.body}</div> : null}
                {t.actions !== undefined && t.actions.length > 0 ? (
                  <div className={s.toastActions}>
                    {t.actions.map((a) => (
                      <Button
                        key={a.label}
                        size="sm"
                        variant="ghost"
                        onClick={() => {
                          dismiss(t.id);
                          a.onClick();
                        }}
                      >
                        {a.label}
                      </Button>
                    ))}
                  </div>
                ) : null}
              </div>
              <button
                type="button"
                className={s.toastClose}
                aria-label="Dismiss"
                onClick={() => dismiss(t.id)}
              >
                <X size={14} aria-hidden="true" />
              </button>
            </div>
          );
        })}
      </div>
    </ToastContext.Provider>
  );
}

export function useToast(): ToastApi {
  return useContext(ToastContext);
}
