import { expect, test } from '../fixtures/test';
import type { Page } from '@playwright/test';

// Focus and overlays, for a keyboard and a screen reader: while a dialog
// or a view is up it is the page — what is behind it is inert, Tab stays
// inside it, a redraw inside it never drops focus to <body>, and escape
// closes it wherever focus is. Closing gives focus back to what opened it.

let id: string;
test.use({ seed: { run: async (ws) => { id = await ws.seedVerified('Add a waving helper'); } } });

test.beforeEach(({}, info) => {
  test.skip(info.project.name !== 'desktop', 'keyboard focus is the desktop’s');
});

const inside = (page: Page, tid: string) => page.evaluate((t) => {
  const a = document.activeElement;
  return !!a && a !== document.body && !!a.closest(`[data-testid="${t}"]`);
}, tid);

async function open(page: Page, server: { url: string }) {
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
}

test('the landing dialog makes the page behind it inert and keeps Tab inside', async ({ pairedPage: page, server }) => {
  await open(page, server);
  const land = page.getByTestId('decision-option-advance');
  await land.click();
  const dialog = page.getByTestId('landing-dialog');
  await expect(dialog).toBeVisible();
  await expect(page.getByTestId('app')).toHaveJSProperty('inert', true);
  // nothing behind it is reachable: every Tab lands inside the dialog
  for (let i = 0; i < 8; i++) {
    await page.keyboard.press('Tab');
    expect(await inside(page, 'landing-dialog'), `Tab ${i + 1} stays in the dialog`).toBe(true);
  }
  for (let i = 0; i < 3; i++) {
    await page.keyboard.press('Shift+Tab');
    expect(await inside(page, 'landing-dialog')).toBe(true);
  }
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  await expect(page.getByTestId('app')).toHaveJSProperty('inert', false);
  // focus is back on the answer that opened it
  await expect(page.getByTestId('decision-option-advance')).toBeFocused();
});

test('a view that redraws keeps focus, and escape closes it from anywhere', async ({ pairedPage: page, server }) => {
  await open(page, server);
  await page.getByTestId('rail-fleet').click();
  await expect(page.getByTestId('view-fleet')).toBeVisible();
  const chip = page.getByTestId('fleet-window').locator('button').first();
  await chip.click();
  await page.waitForTimeout(300);
  // the chips redrew under the click: focus is still in the view
  expect(await inside(page, 'view-fleet')).toBe(true);
  await page.keyboard.press('Escape');
  await expect(page.getByTestId('view-fleet')).toHaveCount(0);
  await expect(page.getByTestId('rail-fleet')).toBeFocused();

  // the doctor's deep-checks button is replaced by its confirmation
  await page.getByTestId('rail-more').click();
  await page.getByTestId('menu-doctor').click();
  await expect(page.getByTestId('doctor-checks')).toBeVisible();
  await page.getByTestId('doctor-deep').click();
  await expect(page.getByTestId('doctor-deep-confirm')).toBeVisible();
  expect(await inside(page, 'view-doctor')).toBe(true);
  // even with focus dropped on nothing, escape still closes it
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await page.keyboard.press('Escape');
  await expect(page.getByTestId('view-doctor')).toHaveCount(0);
  await expect(page.getByTestId('app')).toHaveJSProperty('inert', false);
});

test('the first Tab reaches the skip link, then the top bar', async ({ pairedPage: page, server }) => {
  await open(page, server);
  await page.keyboard.press('Tab');
  await expect(page.locator('a.skip')).toBeFocused();
  await page.keyboard.press('Tab');
  expect(await inside(page, 'topbar')).toBe(true);
});

test('a rail row activated from the keyboard keeps focus', async ({ pairedPage: page, server }) => {
  await open(page, server);
  const row = page.getByTestId(`rail-row-${id}`);
  await row.focus();
  await page.keyboard.press('Enter');
  await page.waitForTimeout(300);
  await expect(row).toBeFocused();
  await page.keyboard.press(' ');
  await page.waitForTimeout(300);
  await expect(row).toBeFocused();
});

test('a menu keeps Tab inside it, and escape closes it from anywhere', async ({ pairedPage: page, server }) => {
  await open(page, server);
  await page.getByTestId('rail-more').click();
  const menu = page.getByTestId('rail-more-menu');
  await expect(menu).toBeVisible();
  for (let i = 0; i < 10; i++) {
    await page.keyboard.press('Tab');
    expect(await inside(page, 'rail-more-menu'), `Tab ${i + 1} stays in the menu`).toBe(true);
  }
  // focus dropped on nothing: escape still closes the menu
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await page.keyboard.press('Escape');
  await expect(menu).toHaveCount(0);
  await expect(page.getByTestId('rail-more')).toBeFocused();
});
