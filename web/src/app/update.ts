// SPDX-License-Identifier: Apache-2.0
//
// Update status of this node (GET /system/update, SPEC §11.5, §17.7) as TanStack Query state,
// shared by the top-bar indicator and the System page. The route is admin-only: callers pass
// `enabled=false` for other roles so no request (and so no 403) is ever made for them. While an
// update is being applied the card polls the status (`pollMs`); the indicator shares the result.

import { useQuery, type UseQueryResult } from '@tanstack/react-query';
import { admin } from '../api/endpoints';
import type { UpdateStatus } from '../api/types';
import { useApi } from './services';

export const UPDATE_KEY = ['system', 'update'] as const;

/** The server checks once a day; the UI re-asks it at most every 15 minutes. */
export const UPDATE_STALE_MS = 15 * 60_000;

/** How often the status is polled while an update is being applied (§16.4). */
export const UPDATE_POLL_MS = 2_000;

export function useUpdateStatus(enabled: boolean, pollMs?: number): UseQueryResult<UpdateStatus> {
  const api = useApi();
  return useQuery({
    queryKey: UPDATE_KEY,
    queryFn: () => admin.update(api),
    enabled,
    staleTime: UPDATE_STALE_MS,
    refetchInterval: pollMs,
    retry: false,
  });
}
