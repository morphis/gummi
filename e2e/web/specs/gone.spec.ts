import { expect, test } from '../fixtures/test';
import { shot } from '../fixtures/shots';

// A card deleted while someone has it open — from another device, or by
// the card's own page — is said to be gone where it was open. The page used
// to move itself onto another card: the line being written was dropped,
// and enter then answered that card's decision (here, a landing) unread.

let verified: string;
let doomed: string;
test.use({ seed: { run: async (ws) => {
  verified = await ws.seedVerified('Add a finished helper');
  [doomed] = await ws.seedBacklog(['Add a doomed helper']);
} } });

test('a card deleted elsewhere stays gone where it was open', async ({ pairedPage: page, server, api }, info) => {
  await page.goto(`${server.url}/#${doomed}`);
  await expect(page.getByTestId('card-id')).toHaveText(doomed);
  if (info.project.name === 'phone') await page.getByTestId('tab-thread').click();
  await page.getByTestId('composer-input').fill('half a thought about the helper');

  // another device deletes it
  const q = await api('POST', `/api/cards/${doomed}/actions/delete`, {});
  await api('POST', `/api/cards/${doomed}/actions/delete`, { confirm: q.json.confirm });
  await expect.poll(async () => (await api('GET', `/api/cards/${doomed}`)).status).toBe(404);

  await expect(page.getByTestId('thread-gone')).toContainText(`${doomed} was deleted`);
  await expect(page.getByTestId('card-title')).toContainText('deleted');
  await expect(page.getByTestId('composer-input')).toHaveValue('half a thought about the helper');
  await expect(page.getByTestId('decision')).toHaveCount(0);
  expect(page.url()).not.toContain(verified);
  await shot(page, info, 'card-gone');

  // enter answers nothing
  await page.getByTestId('composer-input').press('Enter');
  await page.waitForTimeout(500);
  expect((await api('GET', `/api/cards/${verified}`)).json.stage).toBe('verify');
});

// Deleting the open card from its own screen is the reader's own doing, so
// on a phone — where the card's screen covers the cards and only a tap on
// back leaves it — the page goes back to the cards by itself. Deleted
// somewhere else while it was open is different: the reader did nothing,
// and the page stays put (the test above).

test('deleting the open card from its own screen goes back to the cards', async ({ pairedPage: page, server, api }, info) => {
  await page.goto(`${server.url}/#${doomed}`);
  await expect(page.getByTestId('card-id')).toHaveText(doomed);
  if (info.project.name === 'phone') await page.getByTestId('tab-thread').click();
  await page.getByTestId('card-actions').click();
  await page.getByTestId('action-delete').click();
  await expect(page.getByTestId('action-question')).toContainText(`Delete ${doomed}?`);
  await page.getByTestId('action-confirm').click();
  await expect.poll(async () => (await api('GET', `/api/cards/${doomed}`)).status).toBe(404);
  if (info.project.name === 'phone') {
    await expect(page.getByTestId('app')).toHaveAttribute('data-view', 'cards');
    await expect(page.getByTestId('mobile-card')).toBeHidden();
    await expect(page.getByTestId('rail')).toBeVisible();
  } else {
    await expect(page.getByTestId('card-title')).toContainText('deleted');
  }
});

// A card's dialog is about that card: opening another one by a link or a
// hash closes it, rather than leaving it up over a card it does not act
// on with the keys still going into it.
test('a card dialog closes when another card is opened', async ({ pairedPage: page, server }, info) => {
  test.skip(info.project.name === 'phone', 'the phone reaches the menu through its own panel');
  await page.goto(`${server.url}/#${verified}`);
  await expect(page.getByTestId('card-id')).toHaveText(verified);
  await page.getByTestId('card-actions').click();
  await page.getByTestId('action-merge').click();
  await expect(page.getByTestId('action-dialog')).toBeVisible();
  await page.evaluate((id) => { location.hash = '#' + id; }, doomed);
  await expect(page.getByTestId('card-id')).toHaveText(doomed);
  await expect(page.getByTestId('action-dialog')).toHaveCount(0);
});

// A hash naming no card on this board is said to be so, as a fresh load
// says it, and the card that was open stays open — not opened as an empty
// card whose thread "is not available yet". An id typed in lower case
// still names its card.
test('a hash naming no card keeps the open card and says so', async ({ pairedPage: page, server }) => {
  await page.goto(`${server.url}/#${verified}`);
  await expect(page.getByTestId('card-id')).toHaveText(verified);
  await page.evaluate(() => { location.hash = '#nope'; });
  await expect(page.getByTestId('toast').filter({ hasText: 'nope is not on this board' })).toBeVisible();
  await expect(page.getByTestId('card-id')).toHaveText(verified);
  await expect(page.getByTestId('thread-unavailable')).toHaveCount(0);
  expect(page.url()).toContain(`#${verified}`);
  await page.evaluate((id) => { location.hash = '#' + id.toLowerCase(); }, doomed);
  await expect(page.getByTestId('card-id')).toHaveText(doomed);
  await expect(page).toHaveURL(new RegExp(`#${doomed}`));
});
