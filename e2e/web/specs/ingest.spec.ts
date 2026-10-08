import { expect, test } from '../fixtures/test';
import type { Page } from '@playwright/test';
import { shot } from '../fixtures/shots';

// Import spec: paste a document, watch the architect decompose it (the
// scripted agent proposes one card per `## ` heading, and reports a heading
// starting "Unmapped:" as a requirement nothing covers), review the
// proposals, and approve them into todo.

const DOC = [
  '# Greeting kit',
  '',
  '## Add a shrug helper',
  'Shrug returns a shrug.',
  '',
  '## Add a nod helper',
  'Nod returns a nod.',
  '',
  '## Add a bow helper',
  'Bow returns a bow.',
  '',
  '## Unmapped: localise every greeting',
  'Nobody has decided which locales.',
  '',
].join('\n');

async function openIngest(page: Page) {
  await page.getByTestId('rail-more').click();
  await page.getByTestId('menu-ingest').click();
  await expect(page.getByTestId('view-ingest')).toBeVisible();
}

async function decompose(page: Page, name: string) {
  await expect(page.getByTestId('ingest-form')).toBeVisible();
  await page.getByTestId('ingest-source-paste').click();
  await page.getByTestId('ingest-name').fill(name);
  await page.getByTestId('ingest-markdown').fill(DOC);
  await page.getByTestId('ingest-start').click();
  await expect(page.getByTestId('ingest-review')).toBeVisible({ timeout: 45_000 });
  await expect(page.getByTestId('ingest-proposals').locator('> li')).toHaveCount(3);
}

test('paste, review, rename and drop, approve: the cards land on the rail', async ({ pairedPage: page }, info) => {
  const phone = info.project.name === 'phone';
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  await openIngest(page);
  await shot(page, info, 'ingest-form');
  await decompose(page, 'greeting kit');

  // the coverage map says what nothing covers, before anything is approved
  await expect(page.getByTestId('ingest-unmapped-count')).toContainText('1');
  await expect(page.getByTestId('ingest-unmapped')).toContainText('localise every greeting');
  await expect(page.getByTestId('ingest-proposal-0')).toContainText('Add a shrug helper');
  await shot(page, info, 'ingest-review');

  // rename the first
  await page.getByTestId('ingest-rename-0').click();
  const title = page.getByTestId('ingest-edit-rename');
  await title.fill('Add a wave helper');
  await title.press('Enter');
  await expect(page.getByTestId('ingest-proposal-0').getByTestId('ingest-title')).toHaveText('Add a wave helper');

  // drop the last
  await page.getByTestId('ingest-drop-2').click();
  await expect(page.getByTestId('ingest-proposal-2')).toHaveAttribute('data-dropped', 'true');
  await expect(page.getByTestId('ingest-kept')).toHaveText('2 kept');

  // the confirm repeats the gap loudly
  await page.getByTestId('ingest-approve').click();
  await expect(page.getByTestId('ingest-confirm')).toContainText('Create 2 cards in todo?');
  await expect(page.getByTestId('ingest-confirm-unmapped')).toContainText('1 source requirement UNMAPPED');
  await shot(page, info, 'ingest-confirm');
  await page.getByTestId('ingest-confirm-yes').click();

  const done = page.getByTestId('ingest-done');
  await expect(done).toBeVisible({ timeout: 30_000 });
  const links = done.getByTestId('ingest-created').locator('li');
  await expect(links).toHaveCount(2);
  await expect(done).toContainText('Add a wave helper');
  await expect(done).toContainText('Add a nod helper');
  await expect(done).not.toContainText('Add a bow helper');
  await shot(page, info, 'ingest-done');

  // a created card opens from its link, and both are on the rail
  const ids = (await links.locator('button').allTextContents()).map((t) => t.trim());
  const first = ids[0];
  await links.first().locator('button').click();
  await expect(page.getByTestId('view-ingest')).toHaveCount(0);
  await expect(page.getByTestId('card-id')).toHaveText(first);
  await expect(page.getByTestId('card-title')).toHaveText('Add a wave helper');
  if (phone) await page.getByTestId('card-back').click();
  for (const id of ids) await expect(page.getByTestId(`rail-row-${id}`)).toBeVisible();
  await expect(page.getByTestId('rail').getByText('Add a nod helper')).toBeAttached();
  expect(errors).toEqual([]);
});

test('merge, one-liner, undrop and discard', async ({ pairedPage: page, api }) => {
  await openIngest(page);
  await decompose(page, 'greeting kit two');

  await page.getByTestId('ingest-oneliner-1').click();
  const line = page.getByTestId('ingest-edit-oneLiner');
  await line.fill('nods, politely');
  await page.getByTestId('ingest-edit-save').click();
  await expect(page.getByTestId('ingest-proposal-1')).toContainText('nods, politely');

  // the first has nothing above it to merge into
  await expect(page.getByTestId('ingest-merge-0')).toHaveCount(0);
  await page.getByTestId('ingest-merge-2').click();
  await expect(page.getByTestId('ingest-proposals').locator('> li')).toHaveCount(2);

  await page.getByTestId('ingest-drop-0').click();
  await expect(page.getByTestId('ingest-proposal-0')).toHaveAttribute('data-dropped', 'true');
  await page.getByTestId('ingest-undrop-0').click();
  await expect(page.getByTestId('ingest-proposal-0')).toHaveAttribute('data-dropped', 'false');

  // merging a kept proposal into a dropped one keeps the survivor, rather
  // than dropping what was merged with it
  await page.getByTestId('ingest-drop-0').click();
  await page.getByTestId('ingest-merge-1').click();
  await expect(page.getByTestId('ingest-proposals').locator('> li')).toHaveCount(1);
  await expect(page.getByTestId('ingest-proposal-0')).toHaveAttribute('data-dropped', 'false');

  await page.getByTestId('ingest-discard').click();
  await page.getByTestId('ingest-discard-confirm-yes').click();
  // the pass is gone and the form is back for the next one
  await expect(page.getByTestId('ingest-form')).toBeVisible();
  const run = await api('GET', '/api/ingest');
  expect(run.json.state).toBe('discarded');
});

// A per-card envelope that minting would refuse is refused before the
// pass runs — on the page, and by the server for a client that skips it —
// not at approve, after the review's edits.
test('a negative envelope is refused before anything decomposes', async ({ pairedPage: page, api }) => {
  await openIngest(page);
  await page.getByTestId('ingest-source-paste').click();
  await page.getByTestId('ingest-markdown').fill(DOC);
  await page.getByTestId('ingest-envelope').fill('-50');
  await page.getByTestId('ingest-start').click();
  await expect(page.getByTestId('toast').filter({ hasText: 'is negative' })).toHaveCount(1);
  await expect(page.getByTestId('ingest-form')).toBeVisible();
  const refused = await api('POST', '/api/ingest', { markdown: DOC, envelope: -50 });
  expect(refused.status).toBe(400);
  const none = await api('GET', '/api/ingest');
  expect(none.json?.state ?? 'none').not.toBe('running');
});
