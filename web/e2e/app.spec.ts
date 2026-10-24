// SPDX-License-Identifier: Apache-2.0
//
// End-to-end smoke tests against the mock-mode build served with the production CSP (SPEC §18):
// login, query, detail, analytics, traces, dashboards (create + edit), alerts, streams,
// connection creation, settings, teams and stream access (scope picker, a restricted viewer,
// deny by default), light theme and the 390 px layout. Every test also fails on a
// Content-Security-Policy violation or an uncaught page error. Screenshots for the design review
// are written to e2e/screenshots/.

import { expect, test, type Page } from '@playwright/test';

const PASSWORD = 'correct horse battery staple';
const shot = (name: string): string => `e2e/screenshots/${name}.png`;

/** Screenshot without Playwright's injected caret-hiding <style>, which the CSP would block. */
async function snap(page: Page, name: string, fullPage = false): Promise<void> {
  await page.screenshot({ path: shot(name), caret: 'initial', fullPage });
}

/** Records `securitypolicyviolation` events in the page (the console guard below sees them too). */
async function watchCsp(page: Page): Promise<void> {
  await page.evaluate(() => {
    const w = window as unknown as { cspViolations: string[] };
    w.cspViolations = [];
    document.addEventListener('securitypolicyviolation', (e) => w.cspViolations.push(e.violatedDirective));
  });
}

async function cspViolations(page: Page): Promise<string[]> {
  return page.evaluate(() => (window as unknown as { cspViolations: string[] }).cspViolations);
}

async function guard(page: Page): Promise<string[]> {
  const problems: string[] = [];
  page.on('console', (m) => {
    if (m.type() === 'error' && /Content Security Policy|Refused to/i.test(m.text())) problems.push(m.text());
  });
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
  return problems;
}

async function login(page: Page, password = PASSWORD, username = 'admin'): Promise<void> {
  await page.goto('/login');
  await page.getByLabel('Username').fill(username);
  await page.getByLabel('Password', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Sign in' }).click();
}

/**
 * Switches to another seeded user without a page load: the mock server keeps its state (teams,
 * settings) in the page, so the sign-out goes through the user menu and the sign-in through the
 * form that the client-side redirect shows.
 */
async function switchUser(page: Page, username: string): Promise<void> {
  await page.getByRole('button', { name: /^User menu for / }).click();
  await page.getByRole('menuitem', { name: 'Sign out' }).click();
  await expect(page).toHaveURL(/\/login/);
  await page.getByLabel('Username').fill(username);
  await page.getByLabel('Password', { exact: true }).fill(PASSWORD);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).toHaveURL(/\/events/);
}

/** Client-side navigation through the left nav (keeps the mock state). */
async function navTo(page: Page, label: string): Promise<void> {
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: label }).click();
}

/**
 * Signs in and waits for the redirect to the explorer. In mock mode the login handler stores the
 * session from inside the page, so navigating away before the redirect can lose the sign-in.
 */
async function signIn(page: Page): Promise<void> {
  await login(page);
  await expect(page).toHaveURL(/\/events/);
}

async function settle(page: Page): Promise<void> {
  await page.waitForLoadState('networkidle');
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(300);
}

let problems: string[] = [];
test.beforeEach(async ({ page }) => {
  problems = await guard(page);
});
test.afterEach(() => {
  expect(problems, 'CSP violations or page errors').toEqual([]);
});

test('login, default-credentials banner and password prompt', async ({ page }) => {
  await page.goto('/events');
  await expect(page).toHaveURL(/\/login\?next=/);
  await login(page, 'wrong');
  await expect(page.getByRole('alert')).toContainText('Incorrect username or password');
  await settle(page);
  await snap(page, '10-login');
  await page.getByLabel('Password', { exact: true }).fill('admin');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('heading', { name: 'Set a new password' })).toBeVisible();
  await page.getByRole('button', { name: 'Not now' }).click();
  await expect(page.getByRole('alert').filter({ hasText: 'default' })).toContainText('admin / admin');
  await expect(page.getByText('Set a new admin password')).toBeVisible();
  await settle(page);
  await snap(page, '10-banner');
});

test.describe('signed in', () => {
  test.beforeEach(async ({ page }) => {
    await signIn(page);
  });

  test('log explorer: query, facets, autocomplete, detail', async ({ page }) => {
    await page.goto('/events?signals=sig-errors,sig-slow');
    await expect(page.getByRole('grid', { name: 'Events' }).getByRole('row').first()).toBeVisible();
    await expect(page.getByText(/events · \d+ ms/)).toBeVisible();
    await settle(page);
    await snap(page, '01-explorer');

    const editor = page.getByRole('textbox', { name: 'Query' });
    await editor.click();
    await page.keyboard.type('level >= warning and ');
    await expect(page.locator('.cm-tooltip-autocomplete')).toBeVisible();
    await settle(page);
    await snap(page, '01-autocomplete');
    await page.keyboard.press('Escape');

    await page.goto('/events');
    const row = page.getByRole('row').filter({ hasText: 'declined by provider' }).first();
    await row.click();
    await expect(page.getByRole('region', { name: /^Event / })).toBeVisible();
    await expect(page).toHaveURL(/\/events\/[0-9a-f-]+/);
    await settle(page);
    await snap(page, '02-event-detail');

    // Show a built-in field and a property as columns, then hide one from its header.
    const header = page
      .locator('[role="row"]')
      .filter({ has: page.getByRole('columnheader', { name: 'Timestamp' }) });
    // Row actions appear on hover, as for a person using the mouse.
    const hoverProp = (name: string) =>
      page
        .getByRole('term')
        .filter({ hasText: new RegExp(`^${name}$`) })
        .hover();
    await hoverProp('host');
    await page.getByRole('button', { name: 'Show host in column' }).click();
    await hoverProp('Provider');
    await page.getByRole('button', { name: 'Show Provider in column' }).click();
    await expect(page.getByRole('button', { name: 'Hide column Provider' })).toBeVisible();
    await expect(header.getByRole('columnheader', { name: /^host/ })).toBeVisible();
    await expect(header.getByRole('columnheader', { name: /^Provider/ })).toBeVisible();
    // The provider now appears twice in the row: highlighted in the message and in its column.
    await expect(row.getByText('stripe', { exact: true })).toHaveCount(2);
    await settle(page);
    await snap(page, '02-show-in-column');
    await header.getByRole('button', { name: 'Remove column host' }).click();
    await expect(header.getByRole('columnheader', { name: /^host/ })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Show host in column' })).toBeVisible();

    await hoverProp('service');
    await page
      .getByRole('button', { name: /^Filter service = / })
      .first()
      .click();
    await expect(page).toHaveURL(/q=service%3Apayments-svc/);
  });

  test('analytics result and add to dashboard', async ({ page }) => {
    await page.goto(
      `/events?q=${encodeURIComponent('level:error | stats count() by service span 5m')}&from=now-6h&to=now`,
    );
    await expect(page.getByRole('img', { name: /count\(\) by service/ })).toBeVisible();
    await settle(page);
    await snap(page, '03-analytics');
    await page.getByRole('button', { name: 'Add to dashboard' }).click();
    const dialog = page.getByRole('dialog', { name: 'Add to dashboard' });
    await dialog
      .getByLabel('Dashboard', { exact: true })
      .selectOption({ label: 'Checkout — service overview' });
    await dialog.getByRole('button', { name: 'Add panel' }).click();
    await expect(page.getByText('Panel added to Checkout — service overview')).toBeVisible();
  });

  test('save a signal (CSRF header on mutations)', async ({ page }) => {
    await page.goto(`/events?q=${encodeURIComponent('service:checkout-api and level >= error')}`);
    await page.getByRole('button', { name: 'Save as signal' }).click();
    await page.getByRole('dialog').getByLabel('Name').fill('Checkout errors');
    await page.getByRole('button', { name: 'Save signal' }).click();
    await expect(page.getByRole('button', { name: 'Checkout errors', pressed: true })).toBeVisible();
  });

  test('trace waterfall with span drawer', async ({ page }) => {
    await page.goto('/traces');
    await expect(page.getByRole('link', { name: /POST \/api\/v1\/orders/ }).first()).toBeVisible();
    await settle(page);
    await snap(page, '04-traces');
    await page.goto('/traces/4bf92f3577b34da6a3ce929d0e0e4736?span=7a3c0f91b2e4d5c6');
    await expect(page.getByRole('complementary', { name: 'Span inventory.reserve' })).toBeVisible();
    await settle(page);
    await snap(page, '04-trace-waterfall');
  });

  test('metrics browser charts a histogram as its mean or p95', async ({ page }) => {
    await page.goto('/metrics');
    const chart = page.getByRole('img', { name: /http.server.request.duration/ });
    await expect(chart).toBeVisible();
    const aggregate = page.getByRole('group', { name: 'Aggregate' });
    await expect(aggregate.getByRole('button', { name: 'Mean', pressed: true })).toBeVisible();
    await expect(
      page.getByTitle(
        'source metrics | name:http.server.request.duration | stats hist_mean() by service span auto',
      ),
    ).toBeVisible();
    await settle(page);
    await snap(page, '14-metrics');
    await aggregate.getByRole('button', { name: 'p95' }).click();
    await expect(aggregate.getByRole('button', { name: 'p95', pressed: true })).toBeVisible();
    await expect(
      page.getByTitle(
        'source metrics | name:http.server.request.duration | stats histogram_quantile(0.95) by service span auto',
      ),
    ).toBeVisible();
    await expect(chart).toBeVisible();
    await settle(page);
    await snap(page, '14-metrics-p95');
    await page.getByRole('button', { name: /^Theme:/ }).click();
    await page.getByRole('menuitemradio', { name: 'Light' }).click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
    await settle(page);
    await snap(page, '14-metrics-p95-light');
    // A summary has no buckets, so it only offers the mean; a gauge the plain aggregates.
    await page.getByRole('button', { name: /db\.client\.operation\.duration/ }).click();
    await expect(aggregate.getByRole('button', { name: 'Mean', pressed: true })).toBeVisible();
    await expect(aggregate.getByRole('button')).toHaveCount(1);
    await page.getByRole('button', { name: /process\.cpu\.utilization/ }).click();
    await expect(aggregate.getByRole('button', { name: 'avg', pressed: true })).toBeVisible();
    await expect(aggregate.getByRole('button')).toHaveCount(5);
    // An up-down sum is a level like a gauge: averaged, no rate button.
    await page.getByRole('button', { name: /http\.server\.active_requests/ }).click();
    await expect(aggregate.getByRole('button', { name: 'avg', pressed: true })).toBeVisible();
    await expect(aggregate.getByRole('button')).toHaveCount(5);
    // A counter (monotonic sum) is a running total: its per-second rate is the default.
    await page.getByRole('button', { name: /tidemark\.ingest\.events/ }).click();
    await expect(aggregate.getByRole('button', { name: 'rate/s', pressed: true })).toBeVisible();
    await expect(aggregate.getByRole('button')).toHaveCount(6);
    await expect(
      page.getByTitle(
        'source metrics | name:tidemark.ingest.events | stats rate(value) by service span auto',
      ),
    ).toBeVisible();
    await expect(page.getByRole('img', { name: /tidemark\.ingest\.events/ })).toBeVisible();
    await settle(page);
    await snap(page, '14-metrics-sum-rate-light');
  });

  test('panel builder with histogram measures', async ({ page }) => {
    await page.goto('/dashboards/d-checkout/edit?panel=logvol');
    await expect(page.getByRole('heading', { name: 'Edit panel' })).toBeVisible();
    await page.getByRole('group', { name: 'Source' }).getByRole('button', { name: 'metrics' }).click();
    const filter = page.getByRole('textbox', { name: 'Panel filter' });
    await filter.click();
    // Typing over a select-all: the editor applies the edit itself (SPEC §15.7), so the CSP
    // reports nothing (Chrome's native edit would set a style attribute on the line).
    await watchCsp(page);
    await page.keyboard.press('Control+A');
    await page.keyboard.type('name:http.server.request.duration');
    await page.keyboard.press('Escape');
    await expect(filter).toHaveText('name:http.server.request.duration');
    expect(await cspViolations(page)).toEqual([]);
    await page.getByLabel('Measure 1 function').selectOption('histogram_quantile');
    const quantile = page.getByLabel('Measure 1 quantile');
    await expect(quantile).toHaveValue('0.95');
    await expect(page.getByTitle(/stats histogram_quantile\(0\.95\) by/).first()).toBeVisible();
    await page.getByRole('button', { name: 'measure' }).click();
    await expect(page.getByLabel('Measure 2 function')).toHaveValue('hist_mean');
    await expect(page.getByLabel('Measure 2 field')).toBeHidden();
    await page.getByLabel('Measure 2 function').selectOption('count');
    await expect(page.getByText(/can only be combined with each other/)).toBeVisible();
    await expect(page.getByRole('button', { name: 'Apply' })).toBeDisabled();
    await page.getByLabel('Measure 2 function').selectOption('hist_mean');
    await quantile.fill('95');
    await expect(page.getByText(/needs a quantile between 0 and 1/)).toBeVisible();
    await quantile.fill('0.5');
    await expect(page.getByText(/needs a quantile between 0 and 1/)).toBeHidden();
    await expect(page.getByRole('button', { name: 'Apply' })).toBeEnabled();
    await expect(page.getByTitle(/stats histogram_quantile\(0\.5\), hist_mean\(\) by/).first()).toBeVisible();
    // The live preview answers the histogram query (one series per service and measure).
    await expect(page.getByRole('img', { name: /^Log volume by service/ })).toBeVisible();
    await expect(page.getByRole('list', { name: 'Legend' })).toContainText('checkout-api · hist_mean');
    await settle(page);
    await snap(page, '06-panel-editor-histogram');
    await page.getByRole('button', { name: /^Theme:/ }).click();
    await page.getByRole('menuitemradio', { name: 'Light' }).click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
    await settle(page);
    await snap(page, '06-panel-editor-histogram-light');
  });

  test('dashboards: view, edit panel, create from scratch', async ({ page }) => {
    await page.goto('/dashboards');
    await expect(page.getByRole('link', { name: 'Checkout — service overview' })).toBeVisible();
    await settle(page);
    await snap(page, '15-dashboards');
    await page.goto('/dashboards/d-checkout');
    await expect(page.getByRole('group', { name: 'Variables' })).toBeVisible();
    await expect(page.getByRole('img', { name: /^Request rate/ })).toBeVisible();
    await settle(page);
    await snap(page, '05-dashboard');
    await page.goto('/dashboards/d-checkout/edit?panel=logvol');
    await expect(page.getByRole('heading', { name: 'Edit panel' })).toBeVisible();
    await expect(page.getByRole('radio', { name: 'Time series' })).toHaveAttribute('aria-checked', 'true');
    await settle(page);
    await snap(page, '06-panel-editor');
    await page.getByRole('button', { name: 'Apply' }).click();
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.getByText('Dashboard saved')).toBeVisible();

    await page.goto('/dashboards');
    await page.getByRole('button', { name: 'New dashboard' }).click();
    await page.getByRole('dialog').getByLabel('Name').fill('E2E board');
    await page.getByRole('button', { name: 'Create' }).click();
    await expect(page).toHaveURL(/\/dashboards\/d-\d+\/edit/);
  });

  test('alerts with editor drawer', async ({ page }) => {
    await page.goto('/alerts/a-checkout');
    await expect(page.getByRole('dialog', { name: 'Edit rule Checkout errors spike' })).toBeVisible();
    await expect(page.getByRole('img', { name: /Alert preview/ })).toBeVisible();
    await expect(page.getByText('Recent transitions')).toBeVisible();
    await expect(page.getByRole('checkbox', { name: 'All streams' })).toBeChecked();
    await settle(page);
    await snap(page, '07-alerts');
    // Limiting the rule to streams reveals the picker; an empty choice cannot be saved.
    await page.getByRole('checkbox', { name: 'All streams' }).uncheck();
    await expect(page.getByText('Choose at least one stream, or read every stream.')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Save rule' })).toBeDisabled();
    await page.getByRole('button', { name: /^Streams:/ }).click();
    await page.getByRole('dialog', { name: 'Streams' }).getByLabel('payments').check();
    await page.getByRole('dialog', { name: 'Streams' }).getByRole('button', { name: 'Done' }).click();
    await expect(page.getByRole('button', { name: 'Save rule' })).toBeEnabled();
  });

  test('streams: retention, mappings and purge dry run', async ({ page }) => {
    await page.goto('/streams/api');
    await expect(page.getByRole('region', { name: 'Stream api' })).toBeVisible();
    await page.getByRole('button', { name: 'Dry run' }).click();
    await expect(page.getByText(/Dry run:/)).toBeVisible();
    await page.getByLabel('Type purge api to confirm').fill('purge ap');
    await settle(page);
    await snap(page, '08-streams', true);
  });

  test('connection wizard shows the key once with snippets', async ({ page }) => {
    await page.goto('/settings/connections');
    await page.getByRole('button', { name: 'New connection' }).click();
    await page.getByLabel('Name', { exact: true }).fill('payments-svc (prod)');
    await page.getByRole('button', { name: 'Next' }).click();
    await page.getByRole('button', { name: 'Next' }).click();
    await page.getByLabel('Default stream').fill('payments');
    // Allowed streams must include the default stream (SPEC §9.1).
    await page.getByRole('button', { name: /^Allowed streams:/ }).click();
    const allowed = page.getByRole('dialog', { name: 'Allowed streams' });
    await allowed.getByLabel('api').check();
    await allowed.getByRole('button', { name: 'Done' }).click();
    await expect(page.getByText('Must include the default stream payments.')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Next' })).toBeDisabled();
    await page.getByRole('button', { name: /^Allowed streams:/ }).click();
    await allowed.getByLabel('payments').check();
    await allowed.getByRole('button', { name: 'Done' }).click();
    await page.getByRole('button', { name: 'Next' }).click();
    await page.getByLabel('Allowed source CIDRs').fill('10.4.0.0/16');
    await page.getByLabel('Minimum level').selectOption('Information');
    await page.getByRole('button', { name: 'Create connection' }).click();
    await expect(page.getByText('Connection key — shown once')).toBeVisible();
    await expect(page.locator('pre').first()).toContainText('tmk_');
    await expect(page.getByText('api, payments', { exact: true })).toBeVisible();
    await settle(page);
    await snap(page, '09-connections', true);
  });

  test('settings pages', async ({ page }) => {
    for (const [path, name, heading] of [
      ['/settings/users', '16-users', 'Users'],
      ['/settings/teams', '22-teams', 'Teams'],
      ['/settings/tokens', '17-tokens', 'API tokens'],
      ['/settings/channels', '18-channels', 'Channels'],
      ['/settings/audit', '19-audit', 'Audit log'],
      ['/settings/system', '20-system', 'System'],
      ['/settings/import', '21-import', 'Import'],
    ] as const) {
      await page.goto(path);
      await expect(page.getByRole('heading', { level: 1, name: heading, exact: true })).toBeVisible();
      await settle(page);
      await snap(page, name);
    }
    await expect(
      page.getByRole('navigation', { name: 'Settings' }).getByRole('link', { name: 'Import' }),
    ).toBeVisible();
  });

  test('update chip and the system update card (admin only)', async ({ page }) => {
    await page.goto('/settings/system');
    const chip = page.getByRole('link', { name: /^Update available · v/ });
    await expect(chip).toBeVisible();
    const card = page.getByRole('region', { name: 'Updates' });
    await expect(card.getByText('Update available')).toBeVisible();
    const notes = card.getByRole('link', { name: /^Release notes/ });
    await expect(notes).toHaveAttribute('href', /github\.com\/ZeraOrg\/opentidemark\/releases\/tag\/v/);
    await expect(notes).toHaveAttribute('rel', 'noopener noreferrer');
    await expect(card.getByRole('button', { name: /^Install v/ })).toBeVisible();
    await settle(page);
    await snap(page, '25-update-card');

    // Check now: the first check succeeds, the second within a minute is refused with the wait.
    await card.getByRole('button', { name: 'Check now' }).click();
    await expect(page.getByText(/is available$/)).toBeVisible();
    await card.getByRole('button', { name: 'Check now' }).click();
    await expect(card.getByText(/Try again in \d+ s\./)).toBeVisible();

    // The chip is a link to the card (also from another page).
    await navTo(page, 'Events');
    await chip.click();
    await expect(page).toHaveURL(/\/settings\/system#update$/);
    await expect(card).toBeVisible();

    await page.getByRole('button', { name: /^Theme:/ }).click();
    await page.getByRole('menuitemradio', { name: 'Light' }).click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
    await settle(page);
    await snap(page, '25-update-card-light');
    await page.getByRole('button', { name: /^Theme:/ }).click();
    await page.getByRole('menuitemradio', { name: 'Dark' }).click();

    // A container install gets copyable Compose commands.
    await page.evaluate(() => sessionStorage.setItem('tm.mock.update', 'container'));
    await page.reload();
    await expect(card.getByLabel('pull and restart', { exact: true })).toContainText(
      'docker compose pull tidemark',
    );
    await expect(card.getByRole('button', { name: /^Copy: / })).toHaveCount(3);
    await settle(page);
    await snap(page, '25-update-container');

    // Without the update helper there is no install button, only the installer guide.
    await page.evaluate(() => sessionStorage.setItem('tm.mock.update', 'helper-missing'));
    await page.reload();
    await expect(card.getByText(/needs the update helper/)).toBeVisible();
    await expect(card.getByRole('link', { name: /^Upgrade guide/ })).toBeVisible();
    await expect(card.getByRole('button', { name: /^Install v/ })).toHaveCount(0);
    await page.evaluate(() => sessionStorage.removeItem('tm.mock.update'));

    // An editor sees no chip, no System entry and the UI never asks for the status.
    const requests: string[] = [];
    page.on('request', (r) => {
      if (r.url().includes('/api/v1/system/update')) requests.push(r.url());
    });
    await switchUser(page, 'maria');
    await navTo(page, 'Settings');
    await expect(page.getByRole('heading', { level: 1, name: 'Account' })).toBeVisible();
    await expect(page.getByRole('link', { name: /^Update available/ })).toHaveCount(0);
    await expect(page.getByRole('link', { name: 'System' })).toHaveCount(0);
    expect(requests).toEqual([]);
  });

  test('one-click update: dialog, progress through the restart, the result and a rollback', async ({
    page,
  }) => {
    await page.goto('/settings/system');
    const card = page.getByRole('region', { name: 'Updates' });
    const install = card.getByRole('button', { name: /^Install v/ });
    await expect(install).toBeVisible();
    const version = ((await install.textContent()) ?? '').replace(/^Install v/, '');
    const dialog = page.getByRole('dialog', { name: `Install v${version}` });
    const confirm = dialog.getByRole('button', { name: `Install v${version}` });
    const typed = dialog.getByLabel(`Type ${version} to confirm`);

    // The dialog explains the restart and the rollback; a minor bump needs the typed version.
    await install.click();
    await expect(dialog.getByText(/15–30 seconds/)).toBeVisible();
    await expect(dialog.getByText(/within 10 minutes/)).toBeVisible();
    await expect(dialog.getByRole('link', { name: /^Release notes/ })).toHaveAttribute(
      'rel',
      'noopener noreferrer',
    );
    await expect(confirm).toBeDisabled();
    await settle(page);
    await snap(page, '26-update-dialog');
    // Escape closes it and returns focus to the button.
    await page.keyboard.press('Escape');
    await expect(dialog).toHaveCount(0);
    await expect(install).toBeFocused();

    await install.click();
    await typed.fill(version);
    await expect(confirm).toBeEnabled();
    await confirm.click();
    await expect(dialog).toHaveCount(0);
    const progress = card.getByRole('status', { name: `Installing v${version}` });
    await expect(progress.getByText(/^Downloading/)).toBeVisible();
    await expect(progress.getByRole('progressbar', { name: 'Download progress' })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Updating…' })).toBeVisible();
    await expect(card.getByRole('button', { name: 'Check now' })).toHaveCount(0);
    await snap(page, '26-update-progress');
    // The mock takes the server away for a few seconds around the restart; the card stays calm.
    await expect(progress.getByText(/coming back/)).toBeVisible({ timeout: 15_000 });
    await snap(page, '26-update-restarting');
    await expect(card.getByText('Up to date')).toBeVisible({ timeout: 15_000 });
    await expect(page.getByRole('status').filter({ hasText: `Installed v${version}` })).toBeVisible();
    await expect(card.getByText(/^Last update/).locator('xpath=following-sibling::dd[1]')).toContainText(
      `installed v${version}`,
    );
    await expect(page.getByRole('link', { name: /^Updat/ })).toHaveCount(0);
    await settle(page);
    await snap(page, '26-update-installed');

    // A rollback, in the light theme and on a phone: the dialog, the progress and the result.
    await page.evaluate(() => sessionStorage.setItem('tm.mock.update', 'apply-rollback'));
    await page.reload();
    await page.getByRole('button', { name: /^Theme:/ }).click();
    await page.getByRole('menuitemradio', { name: 'Light' }).click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
    await install.click();
    await settle(page);
    await snap(page, '26-update-dialog-light');
    await page.keyboard.press('Escape');
    await page.setViewportSize({ width: 390, height: 844 });
    await install.click();
    await settle(page);
    await snap(page, '26-update-dialog-mobile');
    await typed.fill(version);
    await confirm.click();
    await expect(progress.getByText(/^Downloading/)).toBeVisible();
    await snap(page, '26-update-progress-mobile');
    const alert = card.getByRole('alert');
    await expect(alert).toContainText(`The update to v${version} was rolled back`, { timeout: 20_000 });
    await expect(alert).toContainText('did not become ready within 10 min');
    await expect(card.getByRole('button', { name: `Install v${version}` })).toBeEnabled();
    await settle(page);
    await snap(page, '26-update-rollback-mobile', true);
    await page.setViewportSize({ width: 1440, height: 900 });
    await settle(page);
    await snap(page, '26-update-rollback-light');
    await page.evaluate(() => sessionStorage.removeItem('tm.mock.update'));
  });

  test('scope picker narrows the explorer and is remembered', async ({ page }) => {
    await page.goto('/events');
    const grid = page.getByRole('grid', { name: 'Events' });
    await expect(grid.getByRole('row').filter({ hasText: 'checkout-api' }).first()).toBeVisible();
    const trigger = page.getByRole('button', { name: /^Data scope: all streams, all environments/ });
    await trigger.click();
    const dialog = page.getByRole('dialog', { name: 'Data scope' });
    await expect(dialog.getByLabel('production')).toBeVisible();
    await settle(page);
    await snap(page, '23-scope-picker');
    await dialog.getByLabel('payments').check();
    await dialog.getByLabel('production').check();
    await dialog.getByRole('button', { name: 'Apply' }).click();
    await expect(page).toHaveURL(/streams=payments&env=production/);
    await expect(grid.getByRole('row').filter({ hasText: 'payments-svc' }).first()).toBeVisible();
    await expect(grid.getByRole('row').filter({ hasText: 'checkout-api' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: /^Data scope: streams payments/ })).toHaveText(
      /payments · production/,
    );
    // The choice follows into the other views and comes back after a reload without parameters.
    await navTo(page, 'Traces');
    await expect(page).toHaveURL(/\/traces\?.*streams=payments/);
    await page.goto('/events');
    await expect(page).toHaveURL(/streams=payments/);
    await page.getByRole('button', { name: /^Data scope/ }).click();
    await page.getByRole('dialog', { name: 'Data scope' }).getByRole('button', { name: 'Reset' }).click();
    await page.getByRole('dialog', { name: 'Data scope' }).getByRole('button', { name: 'Apply' }).click();
    await expect(page).not.toHaveURL(/streams=/);
  });

  test('teams: the admin creates Payments, grants it to a viewer who then sees only payments', async ({
    page,
  }) => {
    await page.goto('/settings/teams');
    await expect(page.getByRole('switch', { name: 'Deny by default' })).toHaveAttribute(
      'aria-checked',
      'false',
    );
    await page.getByRole('button', { name: 'New team' }).click();
    await page.getByRole('dialog', { name: 'New team' }).getByLabel('Name').fill('Payments');
    await page.getByRole('button', { name: 'Create team' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Payments' })).toBeVisible();

    const members = page.getByRole('region', { name: 'Members' });
    await members.getByRole('button', { name: /^Users:/ }).click();
    await page.getByRole('dialog', { name: 'Users' }).getByLabel('Sam Lindqvist').check();
    await page.getByRole('dialog', { name: 'Users' }).getByRole('button', { name: 'Done' }).click();
    await expect(members.getByText('Unsaved changes')).toBeVisible();
    await members.getByRole('button', { name: 'Save' }).click();
    await expect(members.getByText('Saved')).toBeVisible();

    const streams = page.getByRole('region', { name: 'Streams' });
    await expect(streams.getByText('This team grants no streams')).toBeVisible();
    await streams.getByRole('button', { name: /^Streams:/ }).click();
    await page.getByRole('dialog', { name: 'Streams' }).getByLabel('payments').check();
    await page.getByRole('dialog', { name: 'Streams' }).getByRole('button', { name: 'Done' }).click();
    await streams.getByRole('button', { name: 'Save' }).click();
    await expect(streams.getByText('Saved')).toBeVisible();
    await settle(page);
    await snap(page, '22-team-detail');

    await switchUser(page, 'sam');
    await expect(page.getByTitle('The only stream you can see').first()).toHaveText('payments');
    await expect(page.getByRole('button', { name: /^Data scope/ })).toHaveCount(0);
    const grid = page.getByRole('grid', { name: 'Events' });
    await expect(grid.getByRole('row').filter({ hasText: 'payments-svc' }).first()).toBeVisible();
    await expect(grid.getByRole('row').filter({ hasText: 'checkout-api' })).toHaveCount(0);
    await settle(page);
    await snap(page, '23-viewer-events');

    await navTo(page, 'Traces');
    const rows = page.getByRole('table').getByRole('row');
    await expect(rows.filter({ hasText: 'payments-svc' }).first()).toBeVisible();
    await expect(rows.filter({ hasText: 'checkout-api' })).toHaveCount(0);
    await page
      .getByRole('link', { name: /POST \/api\/v1\/orders/ })
      .first()
      .click();
    await expect(page.getByText('parent span not visible').first()).toBeVisible();
    await settle(page);
    await snap(page, '23-viewer-trace');

    await navTo(page, 'Settings');
    await expect(page.getByText('You see 1 stream.')).toBeVisible();
  });

  test('deny by default: a user in no team gets the empty state and no query', async ({ page }) => {
    await page.goto('/settings/teams');
    await page.getByRole('switch', { name: 'Deny by default' }).click();
    const confirm = page.getByRole('dialog', { name: 'Turn on deny by default?' });
    await expect(confirm).toContainText('in no team will see no logs');
    await confirm.getByRole('button', { name: 'Turn on' }).click();
    await expect(page.getByRole('switch', { name: 'Deny by default' })).toHaveAttribute(
      'aria-checked',
      'true',
    );
    await settle(page);
    await snap(page, '22-teams-deny');
    const queries: string[] = [];
    page.on('request', (r) => {
      if (/\/api\/v1\/(query|histogram|facets|traces|fields|tail)/.test(r.url())) queries.push(r.url());
    });
    await switchUser(page, 'sam');
    await expect(page.getByText("You don't have access to any streams yet")).toBeVisible();
    await expect(page.getByRole('button', { name: /^Data scope/ })).toHaveCount(0);
    await settle(page);
    await snap(page, '24-no-access');
    await navTo(page, 'Traces');
    await expect(page.getByText("You don't have access to any streams yet")).toBeVisible();
    expect(queries).toEqual([]);
  });

  test('light theme with a query error and comfortable rows', async ({ page }) => {
    await page.goto('/events');
    await page.getByRole('button', { name: /^Theme:/ }).click();
    await page.getByRole('menuitemradio', { name: 'Light' }).click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
    await page.getByRole('button', { name: 'Comfortable' }).click();
    const editor = page.getByRole('textbox', { name: 'Query' });
    await editor.click();
    await page.keyboard.type('level >= warnign and service:checkout*');
    await expect(page.getByText("unknown level 'warnign'")).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.locator('.cm-tooltip-autocomplete')).toBeHidden();
    await settle(page);
    await snap(page, '11-explorer-light');
    await page.getByRole('button', { name: 'warning', exact: true }).click();
    await expect(page.getByText("unknown level 'warnign'")).toBeHidden();
  });

  test('keyboard: / focuses the query, j/k move, Enter expands', async ({ page }) => {
    await page.goto('/events');
    await expect(page.getByRole('grid', { name: 'Events' })).toBeVisible();
    await page.locator('body').click({ position: { x: 5, y: 600 } });
    await page.keyboard.press('j');
    await page.keyboard.press('j');
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(/\/events\/[0-9a-f-]+/);
    await page.keyboard.press('/');
    await expect(page.getByRole('textbox', { name: 'Query' })).toBeFocused();
  });
});

test.describe('phone (390 px)', () => {
  test.use({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, hasTouch: true });

  test('explorer on a phone', async ({ page }) => {
    await signIn(page);
    await page.goto('/events?signals=sig-errors,sig-slow');
    await expect(page.getByRole('navigation', { name: 'Tabs' })).toBeVisible();
    await expect(page.getByRole('grid', { name: 'Events' }).getByRole('row').first()).toBeVisible();
    await settle(page);
    await snap(page, '12-explorer-mobile');
    await page.goto('/dashboards/d-checkout');
    await expect(page.getByRole('img', { name: /^Request rate/ })).toBeVisible();
    await settle(page);
    await snap(page, '12-dashboard-mobile');
  });

  test('scope sheet and teams on a phone', async ({ page }) => {
    await signIn(page);
    await page.goto('/events');
    await page.getByRole('button', { name: /^Data scope/ }).click();
    const sheet = page.getByRole('dialog', { name: 'Data scope' });
    await expect(sheet.getByLabel('All my streams')).toBeChecked();
    await settle(page);
    await snap(page, '12-scope-mobile');
    await sheet.getByLabel('payments').check();
    await sheet.getByRole('button', { name: 'Apply' }).click();
    await expect(page).toHaveURL(/streams=payments/);
    await expect(sheet).toBeHidden();
    await page.goto('/settings/teams/tm-platform');
    await expect(page.getByRole('heading', { level: 1, name: 'Platform' })).toBeVisible();
    await settle(page);
    await snap(page, '12-team-mobile', true);
  });

  test('metrics and the panel builder on a phone', async ({ page }) => {
    await signIn(page);
    await page.goto('/metrics');
    const aggregate = page.getByRole('group', { name: 'Aggregate' });
    await expect(aggregate.getByRole('button', { name: 'Mean', pressed: true })).toBeVisible();
    await aggregate.getByRole('button', { name: 'p95' }).click();
    await expect(page.getByRole('img', { name: /http.server.request.duration/ })).toBeVisible();
    await settle(page);
    await snap(page, '12-metrics-mobile', true);
    await page.goto('/dashboards/d-checkout/edit?panel=logvol');
    await page.getByRole('group', { name: 'Source' }).getByRole('button', { name: 'metrics' }).click();
    await page.getByLabel('Measure 1 function').selectOption('histogram_quantile');
    await expect(page.getByLabel('Measure 1 quantile')).toHaveValue('0.95');
    await settle(page);
    await snap(page, '12-panel-editor-mobile', true);
  });

  test('update chip and card on a phone', async ({ page }) => {
    await signIn(page);
    const chip = page.getByRole('link', { name: /^Update available · v/ });
    await expect(chip).toBeVisible();
    await chip.click();
    await expect(page).toHaveURL(/\/settings\/system#update$/);
    const card = page.getByRole('region', { name: 'Updates' });
    await expect(card.getByText('Update available')).toBeVisible();
    await settle(page);
    await snap(page, '12-update-mobile', true);
  });
});
