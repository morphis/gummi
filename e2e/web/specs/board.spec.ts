import { expect, showTab, test } from '../fixtures/test';
import type { Page } from '@playwright/test';
import { shot } from '../fixtures/shots';

// The board's shell against a real `gummi web`: the rail, switching cards,
// the panel's tabs, the three-pane layout at each viewport, the theme and
// the keyboard. Routes the server does not answer yet (501) must leave a
// quiet "not available yet", never a broken page.

let ids: { gate: string; backlog: string[]; bug: string };

test.use({
  seed: {
    run: async (ws) => {
      const gate = await ws.seedDesignGate('Add a wave helper');
      const backlog = await ws.seedBacklog(['Add a shrug helper', 'Add a nod helper']);
      const bug = await ws.seedBug('Greet panics on an empty name', { severity: 'high' });
      ids = { gate, backlog, bug };
    },
  },
});

function watchErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  page.on('console', (m) => {
    if (m.type() === 'error' && !/501|Not Implemented|404/.test(m.text())) errors.push(m.text());
  });
  return errors;
}

test('the rail shows the seeded cards and a card opens with its head', async ({ pairedPage: page }, info) => {
  const errors = watchErrors(page);
  // boot names the auto-picked card in the address bar; from there the
  // reload reopens it (the hash persists). A phone opens on the cards,
  // which name no card: a reload stays on them.
  if (info.project.name === 'phone') {
    await expect(page.getByTestId('app')).toHaveAttribute('data-view', 'cards');
    await expect(page).not.toHaveURL(/#/);
    await page.reload();
    await expect(page.getByTestId('rail')).toBeVisible();
    await page.waitForTimeout(300);
    await expect(page.getByTestId('app')).toHaveAttribute('data-view', 'cards');
  } else {
    await expect(page).toHaveURL(/#/);
    await page.reload();
  }
  for (const id of [ids.gate, ...ids.backlog, ids.bug]) {
    await expect(page.getByTestId(`rail-row-${id}`)).toBeVisible();
  }
  await expect(page.getByTestId(`rail-row-${ids.gate}`)).toHaveAttribute('data-status', 'needs');
  await expect(page.getByTestId('rail-group-needs')).toBeVisible();
  await page.getByTestId(`rail-row-${ids.bug}`).click();
  await expect(page.getByTestId('card-title')).toHaveText('Greet panics on an empty name');
  await expect(page.getByTestId('card-id')).toHaveText(ids.bug);
  await expect(page).toHaveURL(new RegExp(`#${ids.bug}`));
  await expect(page.getByTestId('thread')).toBeVisible();
  await shot(page, info, 'board');
  if (info.project.name === 'phone') await page.getByTestId('card-back').click();
  await page.getByTestId(`rail-row-${ids.gate}`).click();
  await expect(page.getByTestId('card-title')).toHaveText('Add a wave helper');
  await expect(page.getByTestId('card-stages')).toContainText('plan');
  await shot(page, info, 'gate');
  expect(errors).toEqual([]);
});

test('a deep link opens the card and the tab it names', async ({ pairedPage: page, server }) => {
  await page.goto(`${server.url}/#${ids.bug}/stats`);
  await expect(page.getByTestId('card-id')).toHaveText(ids.bug);
  await expect(page.getByTestId('tab-stats')).toHaveAttribute('aria-selected', 'true');
});

test('the panel tabs switch, and 501 routes read as not available yet', async ({ pairedPage: page }, info) => {
  // the tabs sit on a card's screen on a phone: enter one first
  if (info.project.name === 'phone') await page.getByTestId(`rail-row-${ids.bug}`).click();
  for (const tab of ['diff', 'pr', 'stats', 'spec']) {
    await showTab(page, tab);
    await expect(page.getByTestId(`tab-${tab}`)).toHaveAttribute('aria-selected', 'true');
    await expect(page.getByTestId('panel-pane')).toHaveAttribute('data-tab', tab);
    await expect(page.getByTestId('panel-pane').locator('.empty, .spec, .sect, .diffhead').first()).toBeVisible();
  }
});

test('the rail, the conversation and one surface sit side by side and never overlap', async ({ pairedPage: page }, info) => {
  test.skip(info.project.name === 'phone', 'the phone shows one view at a time');
  // no surface is open until one is asked for: only its icons stand there
  await expect(page.getByTestId('surface')).toBeHidden();
  const closed = await page.getByTestId('conversation').boundingBox();
  expect((await page.getByTestId('panel').boundingBox())!.width).toBeLessThan(60);
  for (const tab of ['spec', 'diff', 'stats']) {
    await showTab(page, tab);
    await expect(page.getByTestId('surface')).toBeVisible();
    // whichever surface is open, the conversation keeps its 400px
    expect((await page.getByTestId('conversation').boundingBox())!.width).toBeGreaterThanOrEqual(399);
  }
  const rail = await page.getByTestId('rail').boundingBox();
  const conv = await page.getByTestId('conversation').boundingBox();
  const panel = await page.getByTestId('panel').boundingBox();
  expect(rail && conv && panel).toBeTruthy();
  expect(rail!.x + rail!.width).toBeLessThanOrEqual(conv!.x + 1);
  expect(conv!.x + conv!.width).toBeLessThanOrEqual(panel!.x + 1);
  expect(conv!.width).toBeLessThan(closed!.width);
  expect(panel!.width).toBeGreaterThanOrEqual(319);
  const vw = page.viewportSize()!.width;
  expect(panel!.x + panel!.width).toBeLessThanOrEqual(vw + 1);
  // the rail is compact below 1280px, full above
  if (vw < 1280) expect(rail!.width).toBeLessThan(100);
  else expect(rail!.width).toBeGreaterThan(200);
  // ] closes the surface and gives the room to the conversation, as its
  // own icon and its close button do; [ folds the rail
  await page.locator('body').press(']');
  await expect(page.getByTestId('surface')).toBeHidden();
  const wide = await page.getByTestId('conversation').boundingBox();
  expect(wide!.width).toBeGreaterThan(conv!.width);
  await page.locator('body').press(']');
  await expect(page.getByTestId('surface')).toBeVisible();
  await expect(page.getByTestId('tab-stats')).toHaveAttribute('aria-selected', 'true');
  await page.getByTestId('tab-stats').click();
  await expect(page.getByTestId('surface')).toBeHidden();
  await page.getByTestId('tab-stats').click();
  await page.getByTestId('panel-close').click();
  await expect(page.getByTestId('surface')).toBeHidden();
  await expect(page.getByTestId('tab-stats')).toHaveAttribute('aria-selected', 'false');
  const before = (await page.getByTestId('rail').boundingBox())!.width;
  await page.locator('body').press('[');
  await expect.poll(async () => (await page.getByTestId('rail').boundingBox())!.width).not.toBe(before);
});

test('the phone opens on the cards, and a card opens from them with its screen naming it above its tabs', async ({ pairedPage: page }, info) => {
  test.skip(info.project.name !== 'phone', 'phone only');
  // the cards are the root screen: a plain open lands there, cardless
  await expect(page.getByTestId('rail')).toBeVisible();
  await expect(page.getByTestId('conversation')).toBeHidden();
  await expect(page.getByTestId('mobile-card')).toBeHidden();
  await shot(page, info, 'cards');
  await page.getByTestId(`rail-row-${ids.bug}`).click();
  await expect(page.getByTestId('conversation')).toBeVisible();
  await expect(page.getByTestId('rail')).toBeHidden();
  await expect(page.getByTestId('card-id')).toHaveText(ids.bug);
  await expect(page.getByTestId('tab-thread')).toHaveAttribute('aria-selected', 'true');
  await showTab(page, 'diff');
  await expect(page.getByTestId('panel')).toBeVisible();
  await expect(page.getByTestId('rail')).toBeHidden();
  // the documents say whose they are
  await expect(page.getByTestId('card-id')).toHaveText(ids.bug);
  await expect(page.getByTestId('tab-diff')).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByTestId('tab-thread')).toHaveAttribute('aria-selected', 'false');
  await shot(page, info, 'panel');
  await page.getByTestId('tab-thread').click();
  await expect(page.getByTestId('conversation')).toBeVisible();
  await expect(page.getByTestId('tab-thread')).toHaveAttribute('aria-selected', 'true');
});

test('the theme toggles and is kept', async ({ pairedPage: page }, info) => {
  const bg = () => page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  const before = await bg();
  const theme = await page.getByTestId('btn-theme').getAttribute('data-theme');
  await page.getByTestId('btn-theme').click();
  await expect.poll(bg).not.toBe(before);
  await expect(page.locator('html')).toHaveAttribute('data-theme', theme === 'dark' ? 'light' : 'dark');
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-theme', theme === 'dark' ? 'light' : 'dark');
  await shot(page, info, 'theme-toggled');
});

test('j and k walk the rail, ? lists the keys, ⌘K jumps', async ({ pairedPage: page }, info) => {
  test.skip(info.project.name === 'phone', 'keyboard on desktop');
  const selected = () => page.locator('#cards .row[aria-current="true"]').getAttribute('data-id');
  const first = await selected();
  await page.locator('body').press('j');
  await expect.poll(selected).not.toBe(first);
  await page.locator('body').press('k');
  await expect.poll(selected).toBe(first);
  await page.locator('body').press('?');
  await expect(page.getByTestId('keys-help')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.getByTestId('keys-help')).toBeHidden();
  await page.keyboard.press('Control+k');
  await expect(page.getByTestId('palette')).toBeVisible();
  await page.getByTestId('palette-input').fill(ids.bug);
  await page.keyboard.press('Enter');
  await expect(page.getByTestId('card-id')).toHaveText(ids.bug);
});

test('a surface that is not built yet says so', async ({ pairedPage: page }) => {
  await page.getByTestId('rail-fleet').click();
  await expect(page.getByTestId('view-fleet')).toBeVisible();
  await page.keyboard.press('Escape');
  await page.getByTestId('rail-more').click();
  await page.getByTestId('menu-doctor').click();
  await expect(page.getByTestId('view-doctor')).toBeVisible();
});

// The compact rail (a laptop's default) keeps what the full one would
// tell: the landed cards past the fold, as "+N", a folded card that is
// open, highlighted, and a filter still applying, with a way to change it.
test.describe('the compact rail', () => {
  let landed: string[];
  test.use({ seed: { run: async (ws) => {
    landed = [];
    for (const t of ['Add a one helper', 'Add a two helper', 'Add a three helper', 'Add a four helper']) landed.push(await ws.seedLanded(t));
  } } });

  test('keeps the landed overflow, a folded open card and a filter in reach', async ({ pairedPage: page }, info) => {
    test.skip(info.project.name !== 'laptop', 'the compact rail is the laptop’s default');
    test.setTimeout(120_000);
    await expect(page.getByTestId('app')).toHaveClass(/rail-compact/);
    await expect(page.getByTestId('rail-more-done').locator('.cmp')).toBeVisible();
    await expect(page.getByTestId('rail-more-done').locator('.cmp')).toHaveText('+1');
    await expect(page.getByTestId('rail-more-done').locator('.lbl')).toBeHidden();
    const shown = await page.getByTestId('rail-group-done').locator('.row').evaluateAll((rs) => rs.map((r) => (r as HTMLElement).dataset.id));
    const folded = landed.find((id) => !shown.includes(id))!;
    await page.keyboard.press('Control+k');
    await page.keyboard.type(folded);
    await page.keyboard.press('Enter');
    await expect(page.getByTestId(`rail-row-${folded}`)).toHaveClass(/\bsel\b/);

    await page.getByTestId('rail-toggle').click();
    await page.getByTestId('rail-filter').fill('helper');
    await page.getByTestId('rail-toggle').click();
    await expect(page.getByTestId('app')).toHaveClass(/rail-compact/);
    await expect(page.getByTestId('rail-filtered')).toBeVisible();
    await page.getByTestId('rail-filtered').click();
    await expect(page.getByTestId('app')).not.toHaveClass(/rail-compact/);
    await expect(page.getByTestId('rail-filter')).toBeVisible();
  });
});

test('the rail filters from one menu, names what applies, and matches a status in words', async ({ pairedPage: page }, info) => {
  const errors = watchErrors(page);
  // the compact rail has no filter box: open the full one
  if (info.project.name === 'laptop') await page.getByTestId('rail-toggle').click();
  const row = (id: string) => page.getByTestId(`rail-row-${id}`);
  await expect(row(ids.gate)).toBeVisible();
  // a single-repository board has nothing to group or filter by repository
  await page.getByTestId('rail-filters').click();
  await expect(page.getByTestId('rail-group-by-repo')).toHaveCount(0);
  // a kind says how many cards it has, and ticking it leaves the menu open
  await expect(page.getByTestId('rail-kind-BG')).toContainText('1');
  await page.getByTestId('rail-kind-BG').click();
  await expect(page.getByTestId('rail-kind-BG')).toHaveAttribute('aria-checked', 'true');
  await expect(row(ids.bug)).toBeVisible();
  await expect(row(ids.gate)).toHaveCount(0);
  await page.getByTestId('rail-kind-FD').click();
  await page.getByTestId('rail-status-needs').click();
  await page.keyboard.press('Escape');
  await expect(page.getByTestId('rail-filters')).toBeFocused();
  // bugs and features that need you: the gate and nothing else
  await expect(row(ids.gate)).toBeVisible();
  await expect(row(ids.bug)).toHaveCount(0);
  await expect(row(ids.backlog[0])).toHaveCount(0);
  await expect(page.getByTestId('rail-filters')).toContainText('3');
  // each filter is named under the box with its own way off
  await page.getByTestId('rail-token-status-needs').getByRole('button').click();
  await expect(row(ids.bug)).toBeVisible();
  await expect(row(ids.backlog[0])).toBeVisible();
  await page.getByTestId('rail-tokens-clear').click();
  await expect(page.getByTestId('rail-tokens')).toBeHidden();
  // the box matches what a row says, not only its title
  await page.getByTestId('rail-filter').fill('design gate');
  await expect(row(ids.gate)).toBeVisible();
  await expect(row(ids.bug)).toHaveCount(0);
  await page.getByTestId('rail-filter').fill('no such card');
  await page.getByTestId('rail-empty-clear').click();
  await expect(page.getByTestId('rail-filter')).toHaveValue('');
  await expect(row(ids.bug)).toBeVisible();
  expect(errors).toEqual([]);
});
