// SPDX-License-Identifier: Apache-2.0
//
// Update card of the System page (SPEC §16.4, §17.7): the running and the latest release with
// its notes, the result of the last check, "Check now" with the 429 / 409 answers explained,
// one-click install where `apply.supported` (confirmation dialog, progress through the restart,
// the outcome) and otherwise how to update this installation kind (container commands, the
// rolling upgrade for HA, the installer with the helper otherwise).

import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Copy, Download, ExternalLink, RefreshCw } from 'lucide-react';
import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { useLocation } from 'react-router-dom';
import { errorMessage, isApiError } from '../../api/client';
import { admin } from '../../api/endpoints';
import type { InstallKind, UpdateErrorCode, UpdateResult, UpdateStatus } from '../../api/types';
import { useApi, useServices } from '../../app/services';
import { UPDATE_KEY, UPDATE_POLL_MS, useUpdateStatus } from '../../app/update';
import { Button, Spinner, StatusChip, Tag } from '../../components/controls';
import { Card, CodeBlock, layoutStyles as l, Notice } from '../../components/layout';
import { useToast } from '../../components/overlay';
import { copyText } from '../../lib/download';
import { formatAge, formatDateTime } from '../../lib/time';
import s from './settings.module.css';
import {
  APPLY_STALL_MS,
  ApplyProgress,
  applyFailureText,
  applyView,
  InstallDialog,
  isInProgress,
  isUnauthorized,
  isUnreachable,
  isVersionMismatch,
  readApplyTarget,
  writeApplyTarget,
  type ApplyTarget,
} from './UpdateApply';

const DOCS_URL = 'https://zeraorg.github.io/opentidemark/';
export const UPGRADE_DOCS_URL = `${DOCS_URL}installation/#upgrading`;
export const ROLLING_UPGRADE_DOCS_URL = `${DOCS_URL}manual/operations/#rolling-upgrade-ha`;
export const RELEASES_URL = 'https://github.com/ZeraOrg/opentidemark/releases';
const IMAGE = 'ghcr.io/zeraorg/tidemark';

/** The `id` of the card: the top-bar chip links to `/settings/system#update`. */
export const UPDATE_CARD_ID = 'update';

const INSTALL_KIND_TEXT: Record<InstallKind, string> = {
  systemd: 'Linux service (systemd)',
  'windows-service': 'Windows service',
  container: 'container',
  manual: 'manual (not a service)',
  dev: 'development build',
};

/** What a failed check means and what to do about it (SPEC §17.7 error codes). */
export function updateErrorText(code: UpdateErrorCode): string {
  switch (code) {
    case 'unreachable':
      return 'GitHub could not be reached. The server needs outbound HTTPS to api.github.com; behind a firewall, route the check through a proxy with TIDEMARK_UPDATE_PROXY.';
    case 'rate_limited':
      return 'GitHub rate-limited the check. The next scheduled attempt waits until the limit resets.';
    case 'invalid_response':
      return 'GitHub answered with something unexpected. A proxy or captive portal may be rewriting the response.';
    case 'not_found':
      return 'GitHub reported no releases for ZeraOrg/opentidemark. Try again later.';
  }
}

/** Human text for a refused "Check now": 429 with Retry-After, 409 with `details.reason`. */
export function checkFailureText(e: unknown): string {
  if (isApiError(e)) {
    if (e.status === 429) {
      const wait = e.retryAfter;
      return wait === undefined
        ? 'Checked less than a minute ago. Try again shortly.'
        : `Checked less than a minute ago. Try again in ${wait} s.`;
    }
    if (e.status === 409 && e.details.reason === 'disabled')
      return 'Update checks are turned off (TIDEMARK_UPDATE_CHECK=false).';
    if (e.status === 409 && e.details.reason === 'dev')
      return 'This is a development build; it never checks for releases.';
  }
  return errorMessage(e);
}

/** The Compose commands that move a container installation to `version` (SPEC §17.2). */
export function containerCommands(version: string): { image: string; pull: string; source: string } {
  return {
    image: `TM_IMAGE=${IMAGE}:${version}`,
    pull: 'docker compose pull tidemark && docker compose up -d --no-build tidemark',
    source: `git fetch --tags && git checkout v${version} && docker compose up -d --build`,
  };
}

/** The outcome of the last attempt in words ("installed v0.5.0 (from v0.4.0)"). */
export function resultText(r: UpdateResult): string {
  const from = r.from === undefined || r.from === '' ? '' : ` (from v${r.from})`;
  switch (r.phase) {
    case 'installed':
      return `installed v${r.to}${from}`;
    case 'rolled_back':
      return `rolled back to v${r.from ?? '?'} after installing v${r.to}`;
    case 'failed':
      return `failed to install v${r.to}${from}`;
  }
}

function DocsLink({ href, children }: { href: string; children: string }) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      className={s.extLink}
      aria-label={`${children} (opens in a new tab)`}
    >
      {children}
      <ExternalLink size={12} aria-hidden="true" />
    </a>
  );
}

function Snippet({ text, label }: { text: string; label: string }) {
  const toast = useToast();
  return (
    <div className={s.snippet}>
      <CodeBlock label={label}>{text}</CodeBlock>
      <Button
        size="sm"
        variant="ghost"
        iconOnly
        icon={<Copy size={14} aria-hidden="true" />}
        aria-label={`Copy: ${label}`}
        title="Copy"
        onClick={() =>
          void copyText(text).then((ok) =>
            toast.push({ kind: ok ? 'success' : 'warning', title: ok ? 'Copied' : 'Copy failed' }),
          )
        }
      />
    </div>
  );
}

/** Why the installer is the way for this installation (§17.7 `apply.reason`), before the how-to. */
function installerReason(data: UpdateStatus): ReactNode {
  switch (data.apply.reason) {
    case 'helper-missing':
      return (
        <>
          <strong>One-click update needs the update helper, which this installation does not have.</strong>{' '}
          Run the new release&apos;s installer once: it replaces the binary, keeps the configuration and adds
          the helper, so later releases install from this card.{' '}
        </>
      );
    case 'not-a-service':
      return (
        <>
          <strong>One-click update needs the service install (systemd or the Windows service).</strong>{' '}
          Download the release archive for this platform and run its installer: it replaces the binary and
          keeps the configuration.{' '}
        </>
      );
    case 'unsupported-platform':
      return 'There is no release archive for this platform (archives exist for linux/amd64, linux/arm64 and windows/amd64), so build the new version from source. ';
    default:
      return 'Download the release archive for this platform and run its installer: it replaces the binary and keeps the configuration. ';
  }
}

function HowToUpdate({ data, version }: { data: UpdateStatus; version: string }) {
  let body: ReactNode;
  if (data.installKind === 'container') {
    const c = containerCommands(version);
    body = (
      <>
        <p>
          <strong>Release bundle or published image:</strong> set the new tag in <code>.env</code>, then pull
          and restart the service.
        </p>
        <Snippet text={c.image} label="TM_IMAGE line for .env" />
        <Snippet text={c.pull} label="pull and restart" />
        <p>
          <strong>Built from source:</strong> check out the release tag and rebuild.
        </p>
        <Snippet text={c.source} label="rebuild from source" />
      </>
    );
  } else if (data.apply.reason === 'ha') {
    body = (
      <p>
        This node shares its metadata with other nodes. Upgrade one node at a time: stop it, run the new
        release&apos;s installer, start it and wait until the load balancer marks it healthy before the next
        one. <DocsLink href={ROLLING_UPGRADE_DOCS_URL}>Rolling upgrade (HA)</DocsLink>
      </p>
    );
  } else {
    body = (
      <p>
        {installerReason(data)}
        <DocsLink href={UPGRADE_DOCS_URL}>Upgrade guide</DocsLink>
      </p>
    );
  }
  return (
    <div className={s.updateSteps}>
      <div className={l.sectionLabel}>How to update</div>
      {body}
    </div>
  );
}

function Headline({ data, applying }: { data: UpdateStatus; applying: boolean }) {
  if (!data.enabled) {
    return (
      <Notice kind="info">
        Update checks are turned off (<code>TIDEMARK_UPDATE_CHECK=false</code>). New versions are listed on
        the <DocsLink href={RELEASES_URL}>releases page</DocsLink>.
      </Notice>
    );
  }
  if (data.installKind === 'dev') {
    return <Notice kind="info">Development build: it never checks for releases.</Notice>;
  }
  if (data.latest !== undefined) {
    return (
      <div className={s.updateHead}>
        <StatusChip variant={applying ? 'info' : 'accent'} dot>
          {applying ? 'Updating' : 'Update available'}
        </StatusChip>
        <span className={s.updateVersion}>v{data.latest.version}</span>
        {data.latest.prerelease ? <Tag warning>pre-release</Tag> : null}
      </div>
    );
  }
  if (data.error !== undefined) {
    return (
      <div className={s.updateHead}>
        <StatusChip variant="pending">Check failed</StatusChip>
      </div>
    );
  }
  if (data.checkedAt === undefined) {
    return (
      <div className={s.updateHead}>
        <StatusChip variant="muted">Not checked yet</StatusChip>
        <span className={l.muted}>The first check runs within 15 minutes of start.</span>
      </div>
    );
  }
  return (
    <div className={s.updateHead}>
      <StatusChip variant="ok">Up to date</StatusChip>
    </div>
  );
}

function Facts({ data, now }: { data: UpdateStatus; now: number }) {
  const checked = data.checkedAt === undefined ? undefined : Date.parse(data.checkedAt);
  const result = data.lastResult;
  return (
    <dl className={l.kv}>
      <dt>Running</dt>
      <dd>v{data.current}</dd>
      {data.latest !== undefined ? (
        <div className={s.contents}>
          <dt>Latest release</dt>
          <dd>
            v{data.latest.version} · published {formatDateTime(Date.parse(data.latest.publishedAt))} ·{' '}
            <DocsLink href={data.latest.notesUrl}>Release notes</DocsLink>
          </dd>
        </div>
      ) : null}
      <dt>Last checked</dt>
      <dd>
        {checked === undefined ? (
          'never'
        ) : (
          <>
            {formatDateTime(checked)} <span className={s.noWrap}>({formatAge(checked, now)})</span>
          </>
        )}
      </dd>
      <dt>Installation</dt>
      <dd>{INSTALL_KIND_TEXT[data.installKind]}</dd>
      {result !== undefined ? (
        <div className={s.contents}>
          <dt>Last update</dt>
          <dd>
            {resultText(result)} · {formatDateTime(Date.parse(result.at))}{' '}
            <span className={s.noWrap}>({formatAge(Date.parse(result.at), now)})</span>
          </dd>
        </div>
      ) : null}
    </dl>
  );
}

/** Server messages are lower-case error text ("the new version did not …"); shown as a sentence. */
function asSentence(message: string): string {
  const s = message.charAt(0).toUpperCase() + message.slice(1);
  return /[.!?]$/.test(s) ? s : `${s}.`;
}

/** The outcome of a rolled-back or failed attempt, with what the server said. */
function ResultPanel({ data }: { data: UpdateStatus }) {
  const r = data.lastResult;
  if (r === undefined || r.phase === 'installed') return null;
  return (
    <Notice kind="danger">
      <strong>
        {r.phase === 'rolled_back'
          ? `The update to v${r.to} was rolled back; v${data.current} is running again.`
          : `The update to v${r.to} failed; v${data.current} keeps running.`}
      </strong>{' '}
      {r.message !== undefined && r.message !== '' ? `${asSentence(r.message)} ` : ''}
      The helper&apos;s log has the details (<code>journalctl -u tidemark-update</code> on Linux, the Windows
      Application event log otherwise). You can try again after fixing the cause.
    </Notice>
  );
}

/** `pollMs`: how often the status is polled while an update is applied (tests shorten it). */
export default function UpdateCard({ pollMs = UPDATE_POLL_MS }: { pollMs?: number }) {
  const api = useApi();
  const qc = useQueryClient();
  const { now, store } = useServices();
  const toast = useToast();
  const { hash } = useLocation();
  const [target, setTargetState] = useState<ApplyTarget | null>(() => readApplyTarget(store));
  const setTarget = useCallback(
    (t: ApplyTarget | null): void => {
      writeApplyTarget(store, t);
      setTargetState(t);
    },
    [store],
  );
  const status = useUpdateStatus(true, target !== null ? pollMs : undefined);
  const data = status.data;
  const unreachable = target !== null && status.isError && isUnreachable(status.error);
  const unauthorized = target !== null && status.isError && isUnauthorized(status.error);
  const view = applyView(target, data, unreachable);
  const [unreachableSince, setUnreachableSince] = useState<number | null>(null);
  const [refused, setRefused] = useState<string | null>(null);
  const [dialog, setDialog] = useState(false);

  const start = (version: string): void => {
    setRefused(null);
    setDialog(false);
    setTarget({
      version,
      from: data?.current ?? '',
      seenResultAt: data?.lastResult?.at,
      startedAt: now(),
    });
  };
  const check = useMutation({
    mutationFn: () => admin.checkUpdate(api),
    onSuccess: (next) => {
      setRefused(null);
      qc.setQueryData(UPDATE_KEY, next);
      toast.push({
        kind: next.error !== undefined ? 'warning' : 'success',
        title:
          next.error !== undefined
            ? 'The check failed'
            : next.latest !== undefined
              ? `v${next.latest.version} is available`
              : 'OpenTidemark is up to date',
      });
    },
    onError: (e) => setRefused(checkFailureText(e)),
  });
  const apply = useMutation({
    mutationFn: (version: string) => admin.applyUpdate(api, version),
    onSuccess: (next, version) => {
      qc.setQueryData(UPDATE_KEY, next);
      start(version);
    },
    onError: (e, version) => {
      if (isInProgress(e)) {
        start(version);
      } else if (isVersionMismatch(e)) {
        setDialog(false);
        setRefused(applyFailureText(e));
        void qc.invalidateQueries({ queryKey: UPDATE_KEY });
      }
    },
  });

  useEffect(() => {
    setUnreachableSince((since) => (unreachable ? (since ?? now()) : null));
  }, [unreachable, now]);
  const installedTo = view.kind === 'installed' ? view.to : null;
  const installedFrom = view.kind === 'installed' ? view.from : null;
  const ended = view.kind === 'ended';
  useEffect(() => {
    if (installedTo !== null) {
      toast.push({
        kind: 'success',
        title: `Installed v${installedTo}`,
        body: `OpenTidemark was updated from v${installedFrom ?? '?'}.`,
      });
    }
    if (installedTo !== null || ended) setTarget(null);
  }, [installedTo, installedFrom, ended, toast, setTarget]);
  useEffect(() => {
    if (!unauthorized) return;
    toast.push({
      kind: 'info',
      title: 'Sign in again to keep following the update',
      body: 'Your session ended while the server restarted. The update continues; after signing in, this card picks it up again.',
    });
  }, [unauthorized, toast]);

  const loaded = data !== undefined;
  useEffect(() => {
    if (hash !== `#${UPDATE_CARD_ID}` || !loaded) return;
    document.getElementById(UPDATE_CARD_ID)?.scrollIntoView({ block: 'start' });
  }, [hash, loaded]);

  const applying = view.kind === 'progress';
  const canCheck = data !== undefined && data.enabled && data.installKind !== 'dev' && !applying;
  const latest = data?.enabled === true ? data.latest : undefined;
  const canInstall = data !== undefined && latest !== undefined && data.apply.supported && !applying;
  const stalled =
    view.kind === 'progress' &&
    target !== null &&
    ((unreachableSince !== null && now() - unreachableSince > APPLY_STALL_MS) ||
      (view.step === 'unknown' && now() - target.startedAt > APPLY_STALL_MS));
  const pollError = status.isError && !unreachable && !unauthorized;

  return (
    <Card
      id={UPDATE_CARD_ID}
      title="Updates"
      subtitle="checked once a day · no installation data is sent"
      className={s.wide}
      actions={
        canCheck ? (
          <>
            <Button
              size="sm"
              icon={<RefreshCw size={14} aria-hidden="true" />}
              busy={check.isPending}
              onClick={() => check.mutate()}
            >
              Check now
            </Button>
            {canInstall && latest !== undefined ? (
              <Button
                size="sm"
                variant="primary"
                icon={<Download size={14} aria-hidden="true" />}
                onClick={() => {
                  apply.reset();
                  setDialog(true);
                }}
              >
                Install v{latest.version}
              </Button>
            ) : null}
          </>
        ) : undefined
      }
    >
      <div className={s.updateStack}>
        {status.isPending ? <Spinner /> : null}
        {pollError ? <Notice kind="danger">{errorMessage(status.error)}</Notice> : null}
        {data !== undefined ? (
          <>
            <Headline data={data} applying={applying} />
            {data.error !== undefined && data.enabled && data.installKind !== 'dev' ? (
              <Notice kind="warning">
                {updateErrorText(data.error)}
                {data.latest !== undefined ? ' The last known release is shown.' : ''}
              </Notice>
            ) : null}
            {refused !== null ? <Notice kind="warning">{refused}</Notice> : null}
            {view.kind === 'progress' && target !== null ? (
              <ApplyProgress
                version={target.version}
                step={view.step}
                progress={view.progress}
                unreachable={view.unreachable}
                stalled={stalled}
              />
            ) : (
              <ResultPanel data={data} />
            )}
            <Facts data={data} now={now()} />
            {latest !== undefined && !data.apply.supported ? (
              <HowToUpdate data={data} version={latest.version} />
            ) : null}
          </>
        ) : null}
      </div>
      {dialog && data !== undefined && latest !== undefined ? (
        <InstallDialog
          data={data}
          version={latest.version}
          notesUrl={latest.notesUrl}
          busy={apply.isPending}
          error={apply.isError ? applyFailureText(apply.error) : null}
          onConfirm={() => apply.mutate(latest.version)}
          onClose={() => setDialog(false)}
        />
      ) : null}
    </Card>
  );
}
