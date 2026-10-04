import { expect, test } from '../fixtures/test';
import type { Page } from '@playwright/test';
import { shot } from '../fixtures/shots';

// Import bugs: list a repository's issues through gh (fake-gh answers from
// fixtures/gh/issues.json: #101 and #102 open and labelled bug, #103
// closed), choose some, and mint them into todo as `gummi bugs ingest`
// does. An issue already on the board lists as such; a gh failure is shown
// as gh said it.

async function openBugs(page: Page) {
  await page.getByTestId('rail-more').click();
  await page.getByTestId('menu-bugs').click();
  await expect(page.getByTestId('view-bugs')).toBeVisible();
}

test('list the issues, import two, and they land on the rail', async ({ pairedPage: page, workspace }, info) => {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  await openBugs(page);
  await page.getByTestId('bugs-repo').fill('e2e/tiny');
  await page.getByTestId('bugs-fetch').click();
  const list = page.getByTestId('bugs-list');
  await expect(list.locator('> li')).toHaveCount(2);
  await expect(page.getByTestId('bugs-issue-101')).toContainText('Greet returns a trailing space for an empty name');
  await expect(page.getByTestId('bugs-issue-101')).toContainText('high');
  await expect(page.getByTestId('bugs-issue-103')).toHaveCount(0);
  expect(workspace.ghCalls().some((a) => a[0] === 'issue' && a.includes('--repo') && a.includes('e2e/tiny'))).toBe(true);
  await shot(page, info, 'bugs-list');

  await page.getByTestId('bugs-pick-101').check();
  await page.getByTestId('bugs-pick-102').check();
  await expect(page.getByTestId('bugs-import')).toHaveText('Import 2 bugs');
  await page.getByTestId('bugs-import').click();

  const result = page.getByTestId('bugs-result');
  await expect(result.getByTestId('bugs-created').locator('li')).toHaveCount(2, { timeout: 30_000 });
  // the list comes back with both on the board
  await expect(page.getByTestId('bugs-onboard-101')).toBeVisible();
  await expect(page.getByTestId('bugs-onboard-102')).toBeVisible();
  await expect(page.getByTestId('bugs-import')).toBeDisabled();
  await shot(page, info, 'bugs-imported');

  const created = (await result.getByTestId('bugs-created').locator('li button').allTextContents()).map((t) => t.trim());
  expect(created).toHaveLength(2);
  for (const id of created) expect(id).toMatch(/^BG-\d+$/);
  await page.getByTestId('modal-close').click();
  for (const id of created) await expect(page.getByTestId(`rail-row-${id}`)).toBeVisible();
  await expect(page.getByTestId('rail').getByText('The greeting ignores the locale')).toBeAttached();
  expect(errors).toEqual([]);
});

test('closed issues by filter, and gh’s own failure shown plainly', async ({ pairedPage: page }, info) => {
  await openBugs(page);
  await page.getByTestId('bugs-repo').fill('e2e/tiny');
  await page.getByTestId('bugs-state').selectOption('closed');
  await page.getByTestId('bugs-fetch').click();
  await expect(page.getByTestId('bugs-list').locator('> li')).toHaveCount(1);
  await expect(page.getByTestId('bugs-issue-103')).toContainText('Old crash on startup');

  await page.getByTestId('bugs-repo').fill('e2e/missing');
  await page.getByTestId('bugs-fetch').click();
  await expect(page.getByTestId('bugs-gh-error')).toContainText("Could not resolve to a Repository with the name 'e2e/missing'");
  await shot(page, info, 'bugs-gh-error');
});
