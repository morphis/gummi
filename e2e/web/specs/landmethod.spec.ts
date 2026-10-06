import { expect, test, type GummiServer } from '../fixtures/test';
import type { Page } from '@playwright/test';
import { shot } from '../fixtures/shots';

// A verified card's landing from the page can keep the branch's commits: the
// landing form's method select sends the merge-commit method, which hides the
// message box and lands at once with git's own message. The base gets a merge
// commit whose second parent is the card's branch tip.

async function open(page: Page, server: GummiServer, id: string, phone: boolean) {
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  if (phone) await page.getByTestId('tab-thread').click();
}

async function menu(page: Page, action: string) {
  await page.getByTestId('card-actions').click();
  await expect(page.getByTestId('card-actions-menu')).toBeVisible();
  await page.getByTestId(`action-${action}`).click();
}

const phone = (info: { project: { name: string } }) => info.project.name === 'phone';

test.describe('a verified card that may keep its commits', () => {
  let id: string;
  test.use({ seed: { run: async (ws) => { id = await ws.seedVerified('Add a farewell helper'); } } });

  test('lands as a merge commit when the merge-commit method is chosen', async ({ pairedPage: page, server, workspace, api }, info) => {
    await open(page, server, id, phone(info));
    await menu(page, 'merge');
    // the method select sits above the message, squash first and selected
    const method = page.getByTestId('land-method');
    await expect(method).toBeVisible();
    await expect(method).toHaveValue('squash');
    await expect(page.getByTestId('action-input')).toBeVisible();
    await expect(page.getByTestId('action-hint')).toContainText('this is what lands');
    await method.selectOption('merge');
    // a merge commit takes git's message: no box, no hint, nothing to draft
    await expect(page.getByTestId('action-input')).toBeHidden();
    await expect(page.getByTestId('action-hint')).toBeHidden();
    await shot(page, info, 'landmethod-merge');
    await page.getByTestId('action-confirm').click();
    await expect(page.getByTestId('action-dialog')).toHaveCount(0, { timeout: 30_000 });
    if (phone(info)) await page.getByTestId('card-back').click();
    await expect(page.getByTestId('rail-group-done').getByTestId(`rail-row-${id}`)).toBeVisible();
    // main's tip is a merge commit: two parents, the second the branch tip
    const parents = (await workspace.git('log', '-1', '--format=%P', 'main')).trim().split(/\s+/);
    expect(parents).toHaveLength(2);
    const branch = (await api('GET', `/api/cards/${id}`)).json.branch as string;
    const tip = (await workspace.git('rev-parse', branch)).trim();
    expect(parents[1]).toBe(tip);
  });
});
