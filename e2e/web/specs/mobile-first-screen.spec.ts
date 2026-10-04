import { expect, test } from '../fixtures/test';
import type { Page } from '@playwright/test';
import { shot } from '../fixtures/shots';

// The phone's first screen: the cards are the root screen (mobile.js), so a
// plain open lands on the cards list — never on whichever card boot would
// auto-pick. A deep link still opens the card it names; one naming an id
// that is not on this board falls back to the cards list and says so.

let ids: { gate: string; backlog: string[] };

test.use({
  seed: {
    run: async (ws) => {
      ids = {
        gate: await ws.seedDesignGate('Add a wave helper'),
        backlog: await ws.seedBacklog(['Add a shrug helper', 'Add a nod helper']),
      };
    },
  },
});

test('a plain open starts on the cards list', async ({ pairedPage: page }, info) => {
  test.skip(info.project.name !== 'phone', 'the first screen is a phone question');
  await expect(page.locator('#app')).toHaveAttribute('data-view', 'cards');
  await expect(page.getByTestId('rail')).toBeVisible();
  await expect(page.getByTestId('conversation')).toBeHidden();
  // the cards are the root: nothing of one card is on screen over them
  await expect(page.getByTestId('mobile-card')).toBeHidden();
  await shot(page, info, 'mobile-first-screen');
});

// A goto that only adds a hash is a same-session hash change (the router
// handles those); a cold open is a fresh page whose first URL carries the
// hash, which is what boot has to get right.
async function coldOpen(page: Page, url: string): Promise<Page> {
  const fresh = await page.context().newPage();
  await fresh.goto(url);
  return fresh;
}

test('a deep link opens the card it names', async ({ pairedPage: page, server }, info) => {
  test.skip(info.project.name !== 'phone', 'the first screen is a phone question');
  const fresh = await coldOpen(page, `${server.url}/#${ids.gate}`);
  await expect(fresh.locator('#app')).toHaveAttribute('data-view', 'thread');
  await expect(fresh.getByTestId('card-id')).toHaveText(ids.gate);
  await expect(fresh.getByTestId('conversation')).toBeVisible();
  await fresh.close();
});

test('a deep link naming a card off this board stays on the cards list', async ({ pairedPage: page, server }, info) => {
  test.skip(info.project.name !== 'phone', 'the first screen is a phone question');
  const fresh = await coldOpen(page, `${server.url}/#ZZ-999`);
  await expect(fresh.locator('#app')).toHaveAttribute('data-view', 'cards');
  await expect(fresh.getByTestId('rail')).toBeVisible();
  await expect(fresh.getByTestId('conversation')).toBeHidden();
  await expect(fresh.getByTestId('toast').filter({ hasText: 'ZZ-999 is not on this board' })).toBeVisible();
  await fresh.close();
});