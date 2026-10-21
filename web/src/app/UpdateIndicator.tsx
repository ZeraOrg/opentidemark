// SPDX-License-Identifier: Apache-2.0
//
// Top-bar update chip (SPEC §16.1): once the daily release check (§17.7) knows a newer release,
// admins see "Update available · v<x>" linking to the update card of the System page; while an
// update is being applied it reads "Updating…". It is a chip, not a banner: banners stay
// reserved for the default-credentials warning. Other roles render nothing and make no request.

import { CircleArrowUp, Loader2 } from 'lucide-react';
import { Link } from 'react-router-dom';
import { cx } from '../lib/cx';
import { hasRole, useMe } from './session';
import { useUpdateStatus } from './update';
import s from './Shell.module.css';

/** The update card of the System page (its `id` is `update`). */
export const UPDATE_CARD_PATH = '/settings/system#update';

export function UpdateIndicator({ compact }: { compact?: boolean }) {
  const me = useMe();
  const isAdmin = hasRole(me.role, 'admin');
  const status = useUpdateStatus(isAdmin);
  const data = status.data;
  if (!isAdmin || data === undefined) return null;
  const updating = data.state !== 'idle';
  const latest = data.latest;
  let label: string;
  let description: string;
  if (updating) {
    label = 'Updating…';
    description = `OpenTidemark is being updated${latest === undefined ? '' : ` to v${latest.version}`}. Open the update card in System settings to follow it.`;
  } else if (latest !== undefined) {
    label = `Update available · v${latest.version}`;
    description = `OpenTidemark v${latest.version} is available; this server runs v${data.current}. Open the update card in System settings.`;
  } else {
    return null;
  }
  return (
    <Link
      to={UPDATE_CARD_PATH}
      className={cx(s.updateChip, compact === true && s.updateChipCompact)}
      title={description}
      aria-label={compact === true ? label : undefined}
    >
      {updating ? (
        <Loader2 size={14} className={s.updateChipSpinner} aria-hidden="true" />
      ) : (
        <CircleArrowUp size={14} aria-hidden="true" />
      )}
      {compact === true ? null : <span>{label}</span>}
    </Link>
  );
}
