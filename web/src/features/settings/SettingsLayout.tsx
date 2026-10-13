// SPDX-License-Identifier: Apache-2.0
//
// Settings area (SPEC §16.1, DESIGN §5.20): a 200 px sub-nav (Connections, Users, API tokens,
// Channels, Audit log, System, Import) next to the page. Admin-only entries are hidden for other
// roles; Import is shown only when the server offers the import API (GET /imports ≠ 404).

import { useQuery } from '@tanstack/react-query';
import { NavLink, Outlet } from 'react-router-dom';
import { isApiError } from '../../api/client';
import { imports } from '../../api/endpoints';
import type { Role } from '../../api/types';
import { useApi } from '../../app/services';
import { hasRole, useMe } from '../../app/session';
import s from './settings.module.css';

interface Item {
  to: string;
  label: string;
  role: Role;
}

export const SETTINGS_NAV: readonly Item[] = [
  { to: 'connections', label: 'Connections', role: 'admin' },
  { to: 'users', label: 'Users', role: 'viewer' },
  { to: 'tokens', label: 'API tokens', role: 'viewer' },
  { to: 'channels', label: 'Channels', role: 'viewer' },
  { to: 'audit', label: 'Audit log', role: 'admin' },
  { to: 'system', label: 'System', role: 'admin' },
];

/** Whether the import API exists: true / false, or undefined while unknown. */
export function useImportsAvailable(enabled: boolean): boolean | undefined {
  const api = useApi();
  const q = useQuery({
    queryKey: ['imports'],
    queryFn: () => imports.list(api),
    enabled,
    retry: false,
    staleTime: 60_000,
  });
  if (!enabled) return false;
  if (q.isSuccess) return true;
  if (q.isError) return !(isApiError(q.error) && (q.error.status === 404 || q.error.status === 405));
  return undefined;
}

export default function SettingsLayout() {
  const me = useMe();
  const admin = hasRole(me.role, 'admin');
  const importsAvailable = useImportsAvailable(admin);
  const items = [
    ...SETTINGS_NAV.filter((i) => hasRole(me.role, i.role)).map((i) =>
      i.to === 'users' && !admin ? { ...i, label: 'Account' } : i,
    ),
    ...(importsAvailable === true ? [{ to: 'import', label: 'Import', role: 'admin' as const }] : []),
  ];
  return (
    <div className={s.layout}>
      <nav className={s.subnav} aria-label="Settings">
        {items.map((i) => (
          <NavLink key={i.to} to={i.to} className={s.subItem}>
            {i.label}
          </NavLink>
        ))}
      </nav>
      <div className={s.content}>
        <Outlet />
      </div>
    </div>
  );
}
