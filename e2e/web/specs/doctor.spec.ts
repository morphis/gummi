import { expect, test } from '../fixtures/test';
import type { Page } from '@playwright/test';
import { shot } from '../fixtures/shots';

// Doctor: the readiness checklist `gummi doctor` prints, opened from the
// rail's More menu. The quick run never probes a backend; the deep run
// asks first, because it contacts every model the profiles name.

async function openDoctor(page: Page) {
  await page.getByTestId('rail-more').click();
  await page.getByTestId('menu-doctor').click();
  await expect(page.getByTestId('view-doctor')).toBeVisible();
}

test('the checklist renders with an overall verdict', async ({ pairedPage: page, api }, info) => {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  await openDoctor(page);
  const want = (await api('GET', '/api/doctor')).json;
  await expect(page.getByTestId('doctor-checks')).toBeVisible();
  await expect(page.getByTestId('doctor-ready')).toHaveAttribute('data-ready', String(want.ready));
  await expect(page.getByTestId('doctor-ready')).toHaveText(want.ready ? /Ready/ : /Not ready/);
  const rows = page.getByTestId('doctor-checks').locator('li');
  await expect(rows).toHaveCount(want.checks.length);
  const first = want.checks[0];
  await expect(page.getByTestId(`doctor-check-${first.name}`)).toContainText(first.detail);
  await expect(page.getByTestId(`doctor-check-${first.name}`)).toHaveAttribute('data-status', first.status);
  // an unprobed model reachability check says how to probe it
  const unknown = want.checks.find((c: any) => c.status === 'unknown');
  if (unknown) await expect(page.getByTestId(`doctor-check-${unknown.name}`)).toContainText(/deep/);
  await shot(page, info, 'doctor');
  expect(errors).toEqual([]);
});

test('deep checks warn before contacting the backends, then probe', async ({ pairedPage: page }, info) => {
  await openDoctor(page);
  await expect(page.getByTestId('doctor-checks')).toBeVisible();
  await page.getByTestId('doctor-deep').click();
  const confirm = page.getByTestId('doctor-deep-confirm');
  await expect(confirm).toContainText(/contact/);
  await confirm.getByTestId('doctor-deep-confirm-no').click();
  await expect(confirm).toHaveCount(0);

  await page.getByTestId('doctor-deep').click();
  const deep = page.waitForResponse((r) => r.url().includes('/api/doctor?deep=1'), { timeout: 60_000 });
  await page.getByTestId('doctor-deep-confirm-yes').click();
  await deep;
  // every model the profiles name was probed: none is left "not probed"
  await expect(page.getByTestId('doctor-checks').locator('li[data-status="unknown"]')).toHaveCount(0, { timeout: 60_000 });
  await shot(page, info, 'doctor-deep');
});
