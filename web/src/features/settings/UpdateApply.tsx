// SPDX-License-Identifier: Apache-2.0
//
// One-click update of the Updates card (SPEC §16.4, §17.7): the confirmation dialog (typed
// confirmation for a release that may contain breaking changes), the progress view that follows
// `POST /system/update/apply` through the status states and the restart, and the words for a
// refused request and an unsupported installation. The version being installed is remembered in
// the preference store (`tm.update.target`) so the view survives a reload or a new sign-in.

import { ExternalLink, Loader2, Check } from 'lucide-react';
import { useState } from 'react';
import { errorMessage, isApiError } from '../../api/client';
import type { UpdateApplyReason, UpdateProgress, UpdateState, UpdateStatus } from '../../api/types';
import { Button, Field, TextInput } from '../../components/controls';
import { layoutStyles as l, Notice } from '../../components/layout';
import { Modal } from '../../components/overlay';
import { cx } from '../../lib/cx';
import { formatBytes } from '../../lib/format';
import { readJSON, STORAGE_KEYS, type KeyValueStore } from '../../lib/storage';
import s from './settings.module.css';

/** After this long without an answer from the server the card says so and points at the logs. */
export const APPLY_STALL_MS = 15 * 60_000;

/** The update being applied from this browser, kept while the view follows it. */
export interface ApplyTarget {
  version: string;
  /** The version that was running when the update started. */
  from: string;
  /** `lastResult.at` before the update started: a different value is this attempt's outcome. */
  seenResultAt?: string;
  startedAt: number;
}

export function readApplyTarget(store: KeyValueStore): ApplyTarget | null {
  return readJSON<ApplyTarget | null>(
    store,
    STORAGE_KEYS.updateTarget,
    null,
    (v): v is ApplyTarget =>
      typeof v === 'object' &&
      v !== null &&
      typeof (v as ApplyTarget).version === 'string' &&
      typeof (v as ApplyTarget).from === 'string' &&
      typeof (v as ApplyTarget).startedAt === 'number',
  );
}

export function writeApplyTarget(store: KeyValueStore, target: ApplyTarget | null): void {
  if (target === null) store.remove(STORAGE_KEYS.updateTarget);
  else store.set(STORAGE_KEYS.updateTarget, JSON.stringify(target));
}

/** The first three numbers of a §17.6 version. */
function versionParts(v: string): [number, number, number] | null {
  const m = /^(\d+)\.(\d+)\.(\d+)/.exec(v);
  return m === null ? null : [Number(m[1]), Number(m[2]), Number(m[3])];
}

/**
 * Whether installing `target` over `current` may bring breaking changes and so needs the
 * version typed: a major bump, or a minor bump before 1.0 (SemVer; CHANGELOG preamble).
 */
export function needsTypedConfirmation(current: string, target: string): boolean {
  const a = versionParts(current);
  const b = versionParts(target);
  if (a === null || b === null) return true;
  if (a[0] !== b[0]) return true;
  return a[0] === 0 && a[1] !== b[1];
}

/** Why one-click install is not available, in words (`apply.reason`). */
export function unsupportedText(reason: UpdateApplyReason | undefined): string {
  switch (reason) {
    case 'container':
      return 'Containers update by pulling the new image.';
    case 'ha':
      return 'One-click update is off in HA mode; upgrade the nodes one at a time.';
    case 'not-a-service':
      return 'One-click update needs the service install (systemd or the Windows service).';
    case 'helper-missing':
      return 'The update helper is not installed. Run the installer of a current release once (keeping the configuration); it adds the helper, and future releases install from here.';
    case 'unsupported-platform':
      return 'There is no release archive for this platform.';
    case 'disabled':
      return 'Update checks are turned off (TIDEMARK_UPDATE_CHECK=false).';
    case 'dev':
      return 'This is a development build.';
    case undefined:
      return 'One-click update is not available on this installation.';
  }
}

/** Human text for a refused `POST /system/update/apply` (400 and the 409 reasons). */
export function applyFailureText(e: unknown): string {
  if (isApiError(e) && e.status === 400 && e.details.field === 'version') {
    return 'The latest release changed since this page was loaded; the card was refreshed. Check the new version and try again.';
  }
  if (isApiError(e) && e.status === 409) {
    switch (e.details.reason) {
      case 'draining':
        return 'The server is shutting down (graceful drain); no update can start now.';
      case 'wal-failed':
        return 'A write-ahead log has failed. Repair it before updating, so no data is at risk during the restart.';
      case 'in-progress':
        return 'An update is already being applied.';
      default:
        return unsupportedText(e.details.reason as UpdateApplyReason | undefined);
    }
  }
  return errorMessage(e);
}

export function isVersionMismatch(e: unknown): boolean {
  return isApiError(e) && e.status === 400 && e.details.field === 'version';
}

export function isInProgress(e: unknown): boolean {
  return isApiError(e) && e.status === 409 && e.details.reason === 'in-progress';
}

/** A failed poll that means the server is restarting: no connection, a gateway error. */
export function isUnreachable(e: unknown): boolean {
  return isApiError(e) && (e.status === 0 || e.status === 502 || e.status === 503 || e.status === 504);
}

export function isUnauthorized(e: unknown): boolean {
  return isApiError(e) && e.status === 401;
}

// ---- the view derived from the status --------------------------------------------------------

/** A step of the progress view; `unknown` while the server has not reported the state yet. */
export type ApplyStep = Exclude<UpdateState, 'idle'> | 'unknown';

export type ApplyView =
  | { kind: 'none' }
  | { kind: 'progress'; step: ApplyStep; progress?: UpdateProgress; unreachable: boolean }
  | { kind: 'installed'; from: string; to: string }
  | { kind: 'ended' };

/**
 * What the card shows for `target` given the latest status and whether the last poll reached
 * the server: `installed` once the running version is the target, `ended` once an outcome for
 * this attempt is in `lastResult` (the card shows it from there), else the progress.
 */
export function applyView(
  target: ApplyTarget | null,
  data: UpdateStatus | undefined,
  unreachable: boolean,
): ApplyView {
  if (target === null) return { kind: 'none' };
  if (data === undefined) return { kind: 'progress', step: 'unknown', unreachable };
  if (data.current === target.version) return { kind: 'installed', from: target.from, to: target.version };
  if (unreachable) return { kind: 'progress', step: 'restarting', unreachable: true };
  if (data.state !== 'idle')
    return { kind: 'progress', step: data.state, progress: data.progress, unreachable: false };
  const r = data.lastResult;
  if (r !== undefined && r.to === target.version && r.at !== target.seenResultAt) return { kind: 'ended' };
  return { kind: 'progress', step: 'unknown', unreachable: false };
}

// ---- the confirmation dialog -----------------------------------------------------------------

export function InstallDialog({
  data,
  version,
  notesUrl,
  busy,
  error,
  onConfirm,
  onClose,
}: {
  data: UpdateStatus;
  version: string;
  notesUrl: string;
  busy: boolean;
  error: string | null;
  onConfirm: () => void;
  onClose: () => void;
}) {
  const [typed, setTyped] = useState('');
  const typedNeeded = needsTypedConfirmation(data.current, version);
  const ready = !typedNeeded || typed.trim() === version;
  const mismatch = typed !== '' && typed.trim() !== version;
  return (
    <Modal
      title={`Install v${version}`}
      onClose={onClose}
      sheet
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!ready} busy={busy} onClick={onConfirm}>
            Install v{version}
          </Button>
        </>
      }
    >
      <p className={s.dialogText}>
        OpenTidemark downloads the release, verifies its signature and checksum, installs it and restarts the
        service. <strong>Ingestion pauses for about 15–30 seconds</strong>; events already accepted are kept,
        and clients retry while the service restarts.
      </p>
      <p className={s.dialogText}>
        If v{version} is not healthy within 10 minutes, the previous version is put back automatically.
      </p>
      <p className={s.dialogText}>
        <a
          href={notesUrl}
          target="_blank"
          rel="noopener noreferrer"
          className={s.extLink}
          aria-label={`Release notes for v${version} (opens in a new tab)`}
        >
          Release notes for v{version}
          <ExternalLink size={12} aria-hidden="true" />
        </a>
      </p>
      {typedNeeded ? (
        <>
          <Notice kind="warning">
            v{data.current} → v{version} is not a patch release: before 1.0, minor releases may contain
            breaking changes. Read the release notes first.
          </Notice>
          <Field
            label={`Type ${version} to confirm`}
            error={mismatch ? 'Confirmation does not match yet.' : undefined}
          >
            {(id) => (
              <TextInput
                id={id}
                mono
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                aria-invalid={mismatch}
                autoComplete="off"
                spellCheck={false}
                data-autofocus
              />
            )}
          </Field>
        </>
      ) : null}
      {error !== null ? <Notice kind="danger">{error}</Notice> : null}
    </Modal>
  );
}

// ---- the progress view -----------------------------------------------------------------------

const STEPS: { key: ApplyStep; label: string }[] = [
  { key: 'downloading', label: 'Download' },
  { key: 'verifying', label: 'Verify' },
  { key: 'installing', label: 'Install' },
  { key: 'restarting', label: 'Restart' },
];

/** The step index of `step` in STEPS (`staged` belongs to the install step). */
function stepIndex(step: ApplyStep): number {
  if (step === 'staged') return 2;
  if (step === 'unknown') return -1;
  return STEPS.findIndex((x) => x.key === step);
}

/** The one-line description of the current step. */
export function stepText(
  step: ApplyStep,
  progress: UpdateProgress | undefined,
  unreachable: boolean,
): string {
  if (unreachable)
    return 'Restarting the service… the server is coming back; this page reconnects on its own.';
  switch (step) {
    case 'downloading':
      return progress !== undefined && progress.total > 0
        ? `Downloading ${formatBytes(progress.bytes)} of ${formatBytes(progress.total)}`
        : 'Downloading the release…';
    case 'verifying':
      return 'Verifying the signature and checksum…';
    case 'staged':
      return 'Handing over to the update helper…';
    case 'installing':
      return 'Installing the new binary…';
    case 'restarting':
      return 'Restarting the service…';
    case 'unknown':
      return 'Waiting for the update to start…';
  }
}

export function ApplyProgress({
  version,
  step,
  progress,
  unreachable,
  stalled,
}: {
  version: string;
  step: ApplyStep;
  progress?: UpdateProgress;
  unreachable: boolean;
  stalled: boolean;
}) {
  const current = unreachable ? 3 : stepIndex(step);
  const percent =
    progress !== undefined && progress.total > 0 ? Math.round((100 * progress.bytes) / progress.total) : null;
  return (
    <div className={s.applyBox} role="status" aria-live="polite" aria-label={`Installing v${version}`}>
      <div className={s.applyHead}>
        <Loader2 size={16} className={s.applySpinner} aria-hidden="true" />
        <strong>Installing v{version}</strong>
      </div>
      <ol className={s.applySteps} aria-hidden="true">
        {STEPS.map((x, i) => (
          <li
            key={x.key}
            className={cx(s.applyStep, i < current && s.applyStepDone, i === current && s.applyStepCurrent)}
          >
            <span className={s.applyStepMark}>{i < current ? <Check size={12} /> : i + 1}</span>
            {x.label}
          </li>
        ))}
      </ol>
      <div className={s.applyText}>{stepText(step, progress, unreachable)}</div>
      {step === 'downloading' && percent !== null && !unreachable ? (
        <span
          className={s.applyBar}
          role="progressbar"
          aria-label="Download progress"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={percent}
        >
          <span style={{ width: `${percent}%` }} />
        </span>
      ) : null}
      <div className={l.subtle}>
        Keep this page open: it follows the update and shows the result. Ingestion pauses briefly while the
        service restarts.
      </div>
      {stalled ? (
        <Notice kind="warning">
          This is taking longer than expected. Check the helper&apos;s log:{' '}
          <code>journalctl -u tidemark-update</code> on Linux, or the Windows Application event log (source{' '}
          <code>TidemarkUpdate</code>). The card keeps following the update.
        </Notice>
      ) : null}
    </div>
  );
}
