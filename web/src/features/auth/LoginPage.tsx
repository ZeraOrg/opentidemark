// SPDX-License-Identifier: Apache-2.0
//
// Sign in and the first-login password change (SPEC §11.2, §15.2, DESIGN §9.10). The error
// message is identical for unknown users and wrong passwords; the change step is offered but
// never forced (testing-phase decision in §15.2).

import { useQuery } from '@tanstack/react-query';
import { AlertCircle, Eye, EyeOff } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { Navigate, useNavigate, useSearchParams } from 'react-router-dom';
import { isApiError } from '../../api/client';
import { admin, auth } from '../../api/endpoints';
import { useApi } from '../../app/services';
import { useSession } from '../../app/session';
import { Button, Field, TextInput } from '../../components/controls';
import { LogoMark } from '../../components/Logo';
import { passwordProblem } from './password';
import s from './LoginPage.module.css';

/** Only same-origin paths are accepted as `next` (no open redirect). */
export function safeNext(next: string | null): string {
  if (next === null || !next.startsWith('/') || next.startsWith('//') || next.startsWith('/\\'))
    return '/events';
  return next;
}

function loginError(e: unknown): string {
  if (isApiError(e)) {
    if (e.status === 401 || e.code === 'unauthorized') return 'Incorrect username or password.';
    if (e.code === 'rate_limited') return 'Too many sign-in attempts. Wait a few minutes and try again.';
    if (e.code === 'network' || e.code === 'timeout') return 'The server could not be reached.';
    return e.message;
  }
  return 'Sign-in failed.';
}

function Waterlines() {
  return (
    <svg className={s.waves} viewBox="0 0 1440 400" preserveAspectRatio="none" aria-hidden="true">
      <path
        className={s.waveFill}
        d="M0 120 C 240 40 480 200 720 130 S 1200 60 1440 150 L1440 400 L0 400 Z"
      />
      <path className={s.waveLine} d="M0 200 C 260 120 520 300 760 210 S 1220 140 1440 240" />
      <path className={s.waveLine} d="M0 280 C 300 200 560 360 820 290 S 1240 230 1440 320" />
      <path className={s.waveAccent} d="M0 120 C 240 40 480 200 720 130 S 1200 60 1440 150" />
    </svg>
  );
}

export function LoginPage() {
  const api = useApi();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const { status, signedIn } = useSession();
  const next = safeNext(params.get('next'));
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [show, setShow] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [step, setStep] = useState<'login' | 'change'>('login');
  const [newPassword, setNewPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const product = useQuery({ queryKey: ['product'], queryFn: () => admin.product(api), staleTime: Infinity });
  const ready = useQuery({
    queryKey: ['readyz'],
    queryFn: () => api.get<unknown>('/readyz', { absolute: true, skipAuthRedirect: true }),
    retry: false,
  });

  if (status === 'authenticated' && step === 'login' && !busy) return <Navigate to={next} replace />;

  const finish = async (): Promise<void> => {
    await signedIn();
    navigate(next, { replace: true });
  };

  const submit = async (e: FormEvent): Promise<void> => {
    e.preventDefault();
    if (username === '' || password === '') {
      setError('Enter your username and password.');
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const res = await auth.login(api, username, password);
      if (res.mustChangePassword) {
        setStep('change');
        setBusy(false);
        return;
      }
      await finish();
    } catch (err) {
      setError(loginError(err));
      setBusy(false);
    }
  };

  const problem = passwordProblem(newPassword, username, password);
  const mismatch = confirm !== '' && confirm !== newPassword;

  const change = async (e: FormEvent): Promise<void> => {
    e.preventDefault();
    if (problem !== null || mismatch || confirm === '') return;
    setBusy(true);
    setError(null);
    try {
      await auth.changePassword(api, password, newPassword);
      await finish();
    } catch (err) {
      setError(isApiError(err) ? err.message : 'The password could not be changed.');
      setBusy(false);
    }
  };

  return (
    <div className={s.page}>
      <Waterlines />
      <div className={s.column}>
        <div className={s.brand}>
          <LogoMark size={36} />
          <div>
            <div className={s.word}>OpenTidemark</div>
            <div className={s.tagline}>logs · traces · metrics</div>
          </div>
        </div>
        {step === 'login' ? (
          <form className={s.card} onSubmit={(e) => void submit(e)} noValidate>
            <h1 className={s.title}>Sign in</h1>
            <p className={s.note}>
              Use your OpenTidemark account. Sessions expire after a period of inactivity.
            </p>
            <Field label="Username">
              {(id) => (
                <TextInput
                  id={id}
                  name="username"
                  autoComplete="username"
                  autoFocus
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                />
              )}
            </Field>
            <Field label="Password">
              {(id) => (
                <div className={s.passwordWrap}>
                  <TextInput
                    id={id}
                    name="password"
                    type={show ? 'text' : 'password'}
                    autoComplete="current-password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                  />
                  <button
                    type="button"
                    className={s.eye}
                    aria-label={show ? 'Hide password' : 'Show password'}
                    aria-pressed={show}
                    onClick={() => setShow(!show)}
                  >
                    {show ? <EyeOff size={16} /> : <Eye size={16} />}
                  </button>
                </div>
              )}
            </Field>
            {error !== null ? (
              <div className={s.error} role="alert">
                <AlertCircle size={16} aria-hidden="true" />
                <span>{error}</span>
              </div>
            ) : null}
            <Button type="submit" variant="primary" className={s.submit} busy={busy}>
              Sign in
            </Button>
            <div className={s.cardFoot}>Open source · Apache-2.0</div>
          </form>
        ) : (
          <form className={s.card} onSubmit={(e) => void change(e)} noValidate>
            <h1 className={s.title}>Set a new password</h1>
            <p className={s.note}>
              Your account still uses its initial password. Choose a new one now, or continue and change it
              later under Settings → Users.
            </p>
            <Field
              label="New password"
              help="At least 12 characters, not your username, not a common password."
              error={newPassword !== '' ? (problem ?? undefined) : undefined}
            >
              {(id) => (
                <TextInput
                  id={id}
                  type="password"
                  autoComplete="new-password"
                  autoFocus
                  value={newPassword}
                  aria-invalid={newPassword !== '' && problem !== null}
                  onChange={(e) => setNewPassword(e.target.value)}
                />
              )}
            </Field>
            <Field label="Confirm password" error={mismatch ? 'The passwords do not match.' : undefined}>
              {(id) => (
                <TextInput
                  id={id}
                  type="password"
                  autoComplete="new-password"
                  value={confirm}
                  aria-invalid={mismatch}
                  onChange={(e) => setConfirm(e.target.value)}
                />
              )}
            </Field>
            {error !== null ? (
              <div className={s.error} role="alert">
                <AlertCircle size={16} aria-hidden="true" />
                <span>{error}</span>
              </div>
            ) : null}
            <Button
              type="submit"
              variant="primary"
              className={s.submit}
              busy={busy}
              disabled={problem !== null || mismatch || confirm === ''}
            >
              Change password and continue
            </Button>
            <Button variant="ghost" onClick={() => void finish()} disabled={busy}>
              Not now
            </Button>
          </form>
        )}
        <div className={s.footer}>
          OpenTidemark{product.data !== undefined ? ` v${product.data.Version}` : ''} · Status:{' '}
          <span className={ready.isError ? s.down : s.up}>
            {ready.isPending ? '…' : ready.isError ? 'storage unavailable' : 'ready'}
          </span>
        </div>
      </div>
    </div>
  );
}
