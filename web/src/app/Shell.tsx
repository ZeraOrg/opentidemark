// SPDX-License-Identifier: Apache-2.0
//
// Application shell (SPEC §16.1, DESIGN §5.16, §5.20, §8): top bar with the global time picker,
// refresh, Live toggle, status text, theme toggle and user menu; collapsible left nav; banners
// (default credentials); mobile top bar and bottom tab bar; global shortcuts.

import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Activity,
  BarChart3,
  Bell,
  ChevronDown,
  ChevronsLeft,
  ChevronsRight,
  Database,
  KeyRound,
  LayoutGrid,
  List,
  LogOut,
  Menu,
  Monitor,
  Moon,
  MoreHorizontal,
  RefreshCw,
  SlidersHorizontal,
  Sun,
  TriangleAlert,
  User,
  type LucideIcon,
} from 'lucide-react';
import { useEffect, useState, type ReactNode } from 'react';
import { Link, NavLink, Outlet, useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import { admin } from '../api/endpoints';
import { Kbd, Toggle } from '../components/controls';
import { LogoMark, Wordmark } from '../components/Logo';
import { MenuItem, MenuSeparator, Modal, Popover, overlayStyles as o, useToast } from '../components/overlay';
import { cx } from '../lib/cx';
import { STORAGE_KEYS } from '../lib/storage';
import { CHEATSHEET } from '../tql/cheatsheet';
import { TqlText } from '../components/TqlText';
import { useGlobalHotkeys } from './hotkeys';
import { useApi, useServices } from './services';
import { useMe, useSession } from './session';
import { ShellProvider, useShell } from './shellContext';
import { useTheme, type ThemePref } from './theme';
import { TimePicker } from './TimePicker';
import { patchParams, useTimeRange } from './url';
import s from './Shell.module.css';

interface NavItem {
  to: string;
  label: string;
  icon: LucideIcon;
}

const NAV: readonly NavItem[] = [
  { to: '/events', label: 'Events', icon: List },
  { to: '/traces', label: 'Traces', icon: Activity },
  { to: '/metrics', label: 'Metrics', icon: BarChart3 },
  { to: '/dashboards', label: 'Dashboards', icon: LayoutGrid },
  { to: '/alerts', label: 'Alerts', icon: Bell },
  { to: '/streams', label: 'Streams', icon: Database },
];

const TABS: readonly NavItem[] = [
  { to: '/events', label: 'Events', icon: List },
  { to: '/traces', label: 'Traces', icon: Activity },
  { to: '/dashboards', label: 'Dashboards', icon: LayoutGrid },
  { to: '/alerts', label: 'Alerts', icon: Bell },
];

/** Routes whose data depends on the global time range. */
export function usesTimeRange(pathname: string): boolean {
  return (
    /^\/(events|traces|metrics)(\/|$)/.test(pathname) ||
    (/^\/dashboards\/[^/]+/.test(pathname) && pathname !== '/dashboards/')
  );
}

function pageTitle(pathname: string): string {
  const seg = pathname.split('/')[1] ?? '';
  const item = NAV.find((n) => n.to === `/${seg}`);
  if (item !== undefined) return item.label;
  return seg === 'settings' ? 'Settings' : 'Tidemark';
}

const THEME_ICON: Record<ThemePref, LucideIcon> = { dark: Moon, light: Sun, system: Monitor };

function ThemeMenu() {
  const { pref, theme, setPref } = useTheme();
  const Icon = pref === 'system' ? Monitor : THEME_ICON[theme];
  return (
    <Popover
      align="right"
      trigger={({ open, toggle }) => (
        <button
          type="button"
          className={s.iconButton}
          aria-label={`Theme: ${pref}`}
          aria-haspopup="menu"
          aria-expanded={open}
          onClick={toggle}
        >
          <Icon size={16} aria-hidden="true" />
        </button>
      )}
    >
      {(close) => (
        <div role="menu" aria-label="Theme">
          {(['dark', 'light', 'system'] as const).map((p) => {
            const I = THEME_ICON[p];
            return (
              <MenuItem
                key={p}
                checked={pref === p}
                icon={<I size={14} aria-hidden="true" />}
                onClick={() => {
                  setPref(p);
                  close();
                }}
              >
                {p === 'dark' ? 'Dark' : p === 'light' ? 'Light' : 'System'}
              </MenuItem>
            );
          })}
        </div>
      )}
    </Popover>
  );
}

function UserMenu({ onHelp }: { onHelp: () => void }) {
  const me = useMe();
  const { signOut } = useSession();
  const navigate = useNavigate();
  const name = me.displayName !== '' ? me.displayName : me.username;
  return (
    <Popover
      align="right"
      trigger={({ open, toggle }) => (
        <button
          type="button"
          className={s.userButton}
          aria-haspopup="menu"
          aria-expanded={open}
          aria-label={`User menu for ${me.username}`}
          onClick={toggle}
        >
          <span className={s.avatar} aria-hidden="true">
            {(name[0] ?? '?').toUpperCase()}
          </span>
          <span className={s.userName}>{me.username}</span>
          <ChevronDown size={14} aria-hidden="true" />
        </button>
      )}
    >
      {(close) => (
        <div role="menu" aria-label="User">
          <div className={o.menuLabel}>
            Signed in as <strong>{me.username}</strong> · {me.role}
          </div>
          <MenuItem
            icon={<User size={14} aria-hidden="true" />}
            onClick={() => {
              close();
              navigate('/settings/users#account');
            }}
          >
            Account &amp; password
          </MenuItem>
          <MenuItem
            icon={<KeyRound size={14} aria-hidden="true" />}
            onClick={() => {
              close();
              navigate('/settings/tokens');
            }}
          >
            API tokens
          </MenuItem>
          <MenuItem
            onClick={() => {
              close();
              onHelp();
            }}
          >
            Keyboard shortcuts <Kbd>?</Kbd>
          </MenuItem>
          <MenuSeparator />
          <MenuItem
            icon={<LogOut size={14} aria-hidden="true" />}
            onClick={() => {
              close();
              void signOut();
            }}
          >
            Sign out
          </MenuItem>
        </div>
      )}
    </Popover>
  );
}

function LiveToggle({ compact }: { compact?: boolean }) {
  const [params, setParams] = useSearchParams();
  const live = params.get('live') === '1';
  const set = (v: boolean): void =>
    setParams((p) => patchParams(p, { live: v ? '1' : null }), { replace: true });
  if (compact === true) {
    return (
      <button
        type="button"
        className={cx(s.liveIcon, live && s.liveOn)}
        aria-pressed={live}
        aria-label={live ? 'Live tail on' : 'Live tail off'}
        onClick={() => set(!live)}
      >
        <span className={s.liveDot} aria-hidden="true" />
      </button>
    );
  }
  return (
    <Toggle checked={live} onChange={set} dot>
      Live
    </Toggle>
  );
}

function TimeControls({ compact }: { compact?: boolean }) {
  const { range, setRange } = useTimeRange();
  const qc = useQueryClient();
  const { refreshLabel } = useShell();
  return (
    <>
      <TimePicker range={range} onChange={setRange} compact={compact} />
      {compact === true ? null : (
        <button
          type="button"
          className={cx(s.iconButton, refreshLabel !== null && s.refreshWithLabel)}
          aria-label="Refresh"
          title="Refresh"
          onClick={() => void qc.invalidateQueries({ type: 'active' })}
        >
          <RefreshCw size={16} aria-hidden="true" />
          {refreshLabel !== null ? <span>{refreshLabel}</span> : null}
        </button>
      )}
    </>
  );
}

function Banners() {
  const me = useMe();
  if (!me.defaultCredentials) return null;
  return (
    <div className={cx(s.banner, s.bannerDanger)} role="alert">
      <TriangleAlert size={16} aria-hidden="true" />
      <span>
        The default <code>admin</code> / <code>admin</code> password is still in use. Anyone who can reach
        this server can sign in.
      </span>
      <Link to="/settings/users#account" className={s.bannerLink}>
        Change password
      </Link>
      <span className={s.bannerNote}>· cannot be dismissed</span>
    </div>
  );
}

function NavFooter({ collapsed }: { collapsed: boolean }) {
  const api = useApi();
  const product = useQuery({ queryKey: ['product'], queryFn: () => admin.product(api), staleTime: Infinity });
  const ready = useQuery({
    queryKey: ['readyz'],
    queryFn: () => api.get<unknown>('/readyz', { absolute: true, skipAuthRedirect: true }),
    refetchInterval: 60_000,
    retry: false,
  });
  if (collapsed) return null;
  return (
    <div className={s.navFooter}>
      <div>{product.data !== undefined ? `v${product.data.Version}` : ''}</div>
      <div className={cx(ready.isError && s.unhealthy)}>
        {ready.isPending ? '' : ready.isError ? 'ClickHouse unavailable' : 'ClickHouse healthy'}
      </div>
    </div>
  );
}

function HelpModal({ onClose, initial }: { onClose: () => void; initial: 'keys' | 'tql' }) {
  const [tab, setTab] = useState(initial);
  return (
    <Modal title={tab === 'keys' ? 'Keyboard shortcuts' : 'TQL cheat-sheet'} onClose={onClose} wide>
      <div className={s.helpTabs} role="tablist">
        <button type="button" role="tab" aria-selected={tab === 'tql'} onClick={() => setTab('tql')}>
          TQL cheat-sheet
        </button>
        <button type="button" role="tab" aria-selected={tab === 'keys'} onClick={() => setTab('keys')}>
          Shortcuts
        </button>
      </div>
      {tab === 'keys' ? (
        <dl className={s.shortcuts}>
          <dt>
            <Kbd>/</Kbd>
          </dt>
          <dd>Focus the query bar</dd>
          <dt>
            <Kbd>Ctrl</Kbd> <Kbd>↵</Kbd>
          </dt>
          <dd>Run the query</dd>
          <dt>
            <Kbd>j</Kbd> <Kbd>k</Kbd>
          </dt>
          <dd>Next / previous event</dd>
          <dt>
            <Kbd>↵</Kbd>
          </dt>
          <dd>Expand / collapse the selected event</dd>
          <dt>
            <Kbd>Esc</Kbd>
          </dt>
          <dd>Close a drawer, dialog or popover</dd>
          <dt>
            <Kbd>g</Kbd> then <Kbd>e</Kbd> <Kbd>t</Kbd> <Kbd>m</Kbd> <Kbd>d</Kbd> <Kbd>a</Kbd> <Kbd>s</Kbd>
          </dt>
          <dd>Go to Events, Traces, Metrics, Dashboards, Alerts, Streams</dd>
          <dt>
            <Kbd>?</Kbd>
          </dt>
          <dd>This help</dd>
        </dl>
      ) : (
        <ul className={s.cheats}>
          {CHEATSHEET.map((c) => (
            <li key={c.q}>
              <TqlText q={c.q} />
              <span>{c.note}</span>
            </li>
          ))}
        </ul>
      )}
    </Modal>
  );
}

function MoreSheet({ onClose, onHelp }: { onClose: () => void; onHelp: () => void }) {
  const me = useMe();
  const { signOut } = useSession();
  const { pref, setPref } = useTheme();
  return (
    <Modal title="More" onClose={onClose}>
      <nav className={s.moreNav} aria-label="More">
        {[
          ...NAV.filter((n) => !TABS.some((t) => t.to === n.to)),
          { to: '/settings', label: 'Settings', icon: SlidersHorizontal },
        ].map((n) => (
          <NavLink key={n.to} to={n.to} className={s.navItem} onClick={onClose}>
            <n.icon size={16} aria-hidden="true" />
            {n.label}
          </NavLink>
        ))}
      </nav>
      <div className={s.moreRow}>
        Theme
        <select value={pref} onChange={(e) => setPref(e.target.value as ThemePref)} aria-label="Theme">
          <option value="dark">Dark</option>
          <option value="light">Light</option>
          <option value="system">System</option>
        </select>
      </div>
      <div className={s.moreRow}>
        <span>
          {me.username} · {me.role}
        </span>
        <button
          type="button"
          className={s.textButton}
          onClick={() => {
            onClose();
            onHelp();
          }}
        >
          Shortcuts
        </button>
        <button type="button" className={s.textButton} onClick={() => void signOut()}>
          Sign out
        </button>
      </div>
    </Modal>
  );
}

function SignInPrompt() {
  const me = useMe();
  const { justSignedIn, acknowledgeSignIn } = useSession();
  const toast = useToast();
  const navigate = useNavigate();
  useEffect(() => {
    if (!justSignedIn) return;
    acknowledgeSignIn();
    if (!me.defaultCredentials && !me.mustChangePassword) return;
    toast.push({
      kind: 'warning',
      title: 'Set a new admin password',
      body: 'You are signed in with the seeded credentials. Change them now; this prompt returns on every login until you do.',
      duration: 0,
      icon: <KeyRound size={16} aria-hidden="true" />,
      actions: [
        { label: 'Change now', onClick: () => navigate('/settings/users#account') },
        { label: 'Later', onClick: () => undefined },
      ],
    });
  }, [justSignedIn, acknowledgeSignIn, me.defaultCredentials, me.mustChangePassword, toast, navigate]);
  return null;
}

function ShellFrame({ children }: { children: ReactNode }) {
  const loc = useLocation();
  const navigate = useNavigate();
  const { status, focusTarget } = useShell();
  const { store } = useServices();
  const [collapsed, setCollapsed] = useState(() => store.get(STORAGE_KEYS.nav) === '1');
  const [help, setHelp] = useState<'keys' | 'tql' | null>(null);
  const [more, setMore] = useState(false);
  const [mobileNav, setMobileNav] = useState(false);
  const timed = usesTimeRange(loc.pathname);
  const events = loc.pathname.startsWith('/events');
  const tv = new URLSearchParams(loc.search).get('tv') === '1';

  useGlobalHotkeys({
    navigate,
    focusQuery: () => {
      const f = focusTarget.current;
      if (f === null) return false;
      f();
      return true;
    },
    help: () => setHelp(events ? 'tql' : 'keys'),
  });

  const toggleNav = (): void => {
    store.set(STORAGE_KEYS.nav, collapsed ? '0' : '1');
    setCollapsed(!collapsed);
  };

  const navItems = [...NAV];
  return (
    <div className={cx(s.shell, collapsed && s.collapsed, tv && s.tv)}>
      <header className={s.topbar}>
        <div className={s.brandSlot}>
          <Link to="/events" className={s.brandLink} aria-label="Tidemark home">
            <Wordmark />
          </Link>
        </div>
        <div className={s.topControls}>
          {timed ? <TimeControls /> : null}
          {events ? <LiveToggle /> : null}
        </div>
        <div className={s.topRight}>
          {status !== null ? <span className={s.status}>{status}</span> : null}
          <ThemeMenu />
          <UserMenu onHelp={() => setHelp('keys')} />
        </div>
      </header>
      <header className={s.mobileBar}>
        <button type="button" className={s.iconButton} aria-label="Menu" onClick={() => setMobileNav(true)}>
          <Menu size={18} aria-hidden="true" />
        </button>
        <LogoMark size={26} />
        <span className={s.mobileTitle}>{pageTitle(loc.pathname)}</span>
        <span className={s.mobileSpacer} />
        {timed ? <TimeControls compact /> : null}
        {events ? <LiveToggle compact /> : null}
      </header>
      <Banners />
      <div className={s.body}>
        <nav className={s.nav} aria-label="Main">
          <ul>
            {navItems.map((n) => (
              <li key={n.to}>
                <NavLink to={n.to} className={s.navItem} title={collapsed ? n.label : undefined}>
                  <n.icon size={16} aria-hidden="true" />
                  <span className={s.navLabel}>{n.label}</span>
                </NavLink>
              </li>
            ))}
            <li className={s.navSep} role="separator" />
            <li>
              <NavLink to="/settings" className={s.navItem} title={collapsed ? 'Settings' : undefined}>
                <SlidersHorizontal size={16} aria-hidden="true" />
                <span className={s.navLabel}>Settings</span>
              </NavLink>
            </li>
          </ul>
          <div className={s.navBottom}>
            <NavFooter collapsed={collapsed} />
            <button
              type="button"
              className={s.collapseButton}
              aria-label={collapsed ? 'Expand navigation' : 'Collapse navigation'}
              onClick={toggleNav}
            >
              {collapsed ? <ChevronsRight size={14} /> : <ChevronsLeft size={14} />}
            </button>
          </div>
        </nav>
        <main className={s.main} id="main">
          {children}
        </main>
      </div>
      <nav className={s.tabbar} aria-label="Tabs">
        {TABS.map((t) => (
          <NavLink key={t.to} to={t.to} className={s.tab}>
            <t.icon size={20} aria-hidden="true" />
            <span>{t.label}</span>
          </NavLink>
        ))}
        <button type="button" className={s.tab} onClick={() => setMore(true)}>
          <MoreHorizontal size={20} aria-hidden="true" />
          <span>More</span>
        </button>
      </nav>
      {mobileNav ? (
        <Modal title="Navigation" onClose={() => setMobileNav(false)}>
          <nav className={s.moreNav} aria-label="Main (mobile)">
            {[...navItems, { to: '/settings', label: 'Settings', icon: SlidersHorizontal }].map((n) => (
              <NavLink key={n.to} to={n.to} className={s.navItem} onClick={() => setMobileNav(false)}>
                <n.icon size={16} aria-hidden="true" />
                {n.label}
              </NavLink>
            ))}
          </nav>
        </Modal>
      ) : null}
      {more ? <MoreSheet onClose={() => setMore(false)} onHelp={() => setHelp('keys')} /> : null}
      {help !== null ? <HelpModal initial={help} onClose={() => setHelp(null)} /> : null}
      <SignInPrompt />
    </div>
  );
}

export function Shell() {
  return (
    <ShellProvider>
      <ShellFrame>
        <Outlet />
      </ShellFrame>
    </ShellProvider>
  );
}
