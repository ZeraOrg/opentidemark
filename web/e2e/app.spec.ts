// SPDX-License-Identifier: Apache-2.0
//
// End-to-end smoke tests against the mock-mode build served with the production CSP (SPEC §18):
// login, query, detail, analytics, traces, dashboards (create + edit), alerts, streams,
// connection creation, settings, light theme and the 390 px layout. Every test also fails on a
// Content-Security-Policy violation or an uncaught page error. Screenshots for the design review
// are written to e2e/screenshots/.

import { expect, test, type Page } from '@playwright/test';

const PASSWORD = 'correct horse battery staple';
const shot = (name: string): string => `e2e/screenshots/${name}.png`;

/** Screenshot without Playwright's injected caret-hiding <style>, which the CSP would block. */
async function snap(page: Page, name: string, fullPage = false): Promise<void> {
  await page.screenshot({ path: shot(name), caret: 'initial', fullPage });
}

async function guard(page: Page): Promise<string[]> {
  const problems: string[] = [];
  page.on('console', (m) => {
    if (m.type() === 'error' && /Content Security Policy|Refused to/i.test(m.text())) problems.push(m.text());
  });
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
  return problems;
}

async function login(page: Page, password = PASSWORD): Promise<void> {
  await page.goto('/login');
  await page.getByLabel('Username').fill('admin');
  await page.getByLabel('Password', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Sign in' }).click();
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
    await login(page);
    await expect(page).toHaveURL(/\/events/);
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

  test('metrics browser', async ({ page }) => {
    await page.goto('/metrics');
    await expect(page.getByRole('img', { name: /http.server.request.duration/ })).toBeVisible();
    await settle(page);
    await snap(page, '14-metrics');
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
    await settle(page);
    await snap(page, '07-alerts');
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
    await page.getByRole('button', { name: 'Next' }).click();
    await page.getByLabel('Allowed source CIDRs').fill('10.4.0.0/16');
    await page.getByLabel('Minimum level').selectOption('Information');
    await page.getByRole('button', { name: 'Create connection' }).click();
    await expect(page.getByText('Connection key — shown once')).toBeVisible();
    await expect(page.locator('pre').first()).toContainText('tmk_');
    await settle(page);
    await snap(page, '09-connections', true);
  });

  test('settings pages', async ({ page }) => {
    for (const [path, name, heading] of [
      ['/settings/users', '16-users', 'Users'],
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
    await login(page);
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
});
