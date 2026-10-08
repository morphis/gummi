import type { Page } from '@playwright/test';
import { expect, test } from '../fixtures/test';
import { seedGoalRunning } from '../fixtures/goals';
import { shot } from '../fixtures/shots';

// The goals view against a real `gummi web`: the list, one goal's page
// (its state, the budget ledger, done-when, its cards, the lead's log) and
// the goal's own verbs, each confirmed on the page before it runs. The goal
// is seeded through the CLI and left at implement for this board to
// conduct; its cards are [slow] so it stays there while the test works.

async function openGoals(page: Page) {
  await page.getByTestId('rail-more').click();
  await page.getByTestId('menu-goals').click();
  await expect(page.getByTestId('view-goals')).toBeVisible();
}

test.describe('a running goal', () => {
  let goal: string;
  test.use({
    workspaceEnv: { GUMMI_E2E_SLOW_SECONDS: '900' },
    seed: {
      run: async (ws) => {
        goal = await seedGoalRunning(ws, 'Greet in two languages', {
          cards: ['[slow] Add a hola helper', '[slow] Add a bonjour helper'],
        });
      },
    },
  });

  test('the list, the page, the ledger, a raise, a note and a stop', async ({ pairedPage: page }, info) => {
    test.setTimeout(120_000);
    await openGoals(page);
    const row = page.getByTestId(`goal-row-${goal}`);
    await expect(row).toContainText('Greet in two languages');
    await expect(row).toHaveAttribute('data-state', 'running');
    await expect(row.getByTestId('goal-row-met')).toContainText('0/2');
    await shot(page, info, 'goals-list');

    await row.click();
    const view = page.getByTestId('view-goal');
    await expect(view.getByTestId('goal-title')).toHaveText('Greet in two languages');
    await expect(view.getByTestId('goal-state')).toHaveText('running');
    // the ledger is the report's budget tree, laid out as it adds up
    await expect(view.getByTestId('ledger-envelope')).toHaveText('$30.00');
    await expect(view.getByTestId('ledger-held')).toContainText(/\d/);
    await expect(view.getByTestId('ledger-reserve')).toContainText('450');
    await expect(view.getByTestId('ledger-seg-held')).toBeVisible();
    await expect(view.getByTestId('done-when-DW-1')).toContainText('the module builds');
    await expect(view.getByTestId('done-when-DW-2')).toHaveAttribute('data-status', 'not checked');
    const cards = view.getByTestId('goal-cards');
    await expect(cards.locator('[data-testid^="goal-card-FD-"]')).toHaveCount(2);
    await expect(view.getByTestId('goal-log')).toContainText('minted');
    await shot(page, info, 'goal-page');

    // raise the budget: the panel asks, the ledger moves
    await view.getByTestId('goal-action-budget').click();
    const budget = view.getByTestId('goal-panel-budget');
    await expect(budget).toBeVisible();
    await budget.getByTestId('goal-action-input').fill('20');
    await budget.getByTestId('goal-action-confirm').click();
    await expect(budget.getByTestId('goal-action-error')).toContainText('only raised');
    await budget.getByTestId('goal-action-input').fill('36');
    await shot(page, info, 'goal-raise');
    await budget.getByTestId('goal-action-confirm').click();
    await expect(page.getByTestId('toast').last()).toContainText('raised to 3600');
    await expect(view.getByTestId('ledger-envelope')).toHaveText('$36.00');
    await expect(view.getByTestId('ledger-reserve')).toContainText('540');

    // a note to the lead lands in its log
    await view.getByTestId('goal-action-note').click();
    const note = view.getByTestId('goal-panel-note');
    await note.getByTestId('goal-action-input').fill('prefer short helper names');
    await note.getByTestId('goal-action-confirm').click();
    await expect(page.getByTestId('toast').last()).toContainText('note added');
    await expect(view.getByTestId('goal-log').locator('[data-action="note"]')).toContainText('prefer short helper names');

    // stop: confirmed on the page, never by a browser dialog
    page.on('dialog', (d) => { throw new Error(`unexpected browser dialog: ${d.message()}`); });
    await view.getByTestId('goal-action-stop').click();
    const stop = view.getByTestId('goal-panel-stop');
    await expect(stop).toContainText('Stop');
    await stop.getByTestId('goal-action-cancel').click();
    await expect(stop).toHaveCount(0);
    await expect(view.getByTestId('goal-state')).toHaveText('running');
    await view.getByTestId('goal-action-stop').click();
    await view.getByTestId('goal-panel-stop').getByTestId('goal-action-confirm').click();
    await expect(view.getByTestId('goal-state')).toHaveText(/wrapping up|ready for you/, { timeout: 30_000 });
    await expect(view.getByTestId('goal-partial')).toContainText('you stopped the goal', { timeout: 30_000 });
    await expect(view.getByTestId('goal-action-land')).toBeVisible();
    await shot(page, info, 'goal-stopped');

    // the land panel carries the goal's drafted merge message
    await view.getByTestId('goal-action-land').click();
    await expect(view.getByTestId('goal-panel-land').getByTestId('goal-action-input')).toHaveValue(/Merge .*Greet in two languages/);
    await view.getByTestId('goal-panel-land').getByTestId('goal-action-cancel').click();
  });

  test('a goal card opens on the board, and its head leads back to the goal', async ({ pairedPage: page }) => {
    await openGoals(page);
    await page.getByTestId(`goal-row-${goal}`).click();
    const card = page.getByTestId('view-goal').locator('[data-testid^="goal-card-FD-"]').first();
    const id = (await card.getAttribute('data-testid'))!.replace('goal-card-', '');
    await card.click();
    await expect(page.getByTestId('view-goal')).toHaveCount(0);
    await expect(page.getByTestId('card-id')).toHaveText(id);
    await page.getByTestId('card-goal').click();
    await expect(page.getByTestId('view-goal').getByTestId('goal-id')).toHaveText(goal);
  });

  // A verb the lead refuses is said on the panel, in its words, and the
  // panel stays open for another try; ctrl+enter confirms and escape
  // closes, as in the composer.
  test('a refused goal verb is said on its panel, and the keyboard drives it', async ({ pairedPage: page }, info) => {
    test.skip(info.project.name !== 'desktop', 'the keyboard paths are the same on every viewport');
    await openGoals(page);
    await page.getByTestId(`goal-row-${goal}`).click();
    const view = page.getByTestId('view-goal');
    await expect(view.getByTestId('goal-title')).toBeVisible();

    let posts = 0;
    await page.route(`**/api/goals/${goal}/actions/note`, (route) => {
      posts++;
      return route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: 'the lead is between turns' }) });
    });
    await view.getByTestId('goal-action-note').click();
    const panel = view.getByTestId('goal-panel-note');
    const confirm = panel.getByTestId('goal-action-confirm');
    // nothing said: the page asks before it sends anything
    await confirm.click();
    await expect(panel.getByTestId('goal-action-error')).toContainText('Say something first');
    expect(posts).toBe(0);

    await panel.getByTestId('goal-action-input').fill('hold the second card');
    await confirm.click();
    await expect(panel.getByTestId('goal-action-error')).toContainText('the lead is between turns');
    await expect(confirm).toBeEnabled();
    // typing takes the message away; ctrl+enter sends again
    await panel.getByTestId('goal-action-input').press('End');
    await panel.getByTestId('goal-action-input').type('!');
    await expect(panel.getByTestId('goal-action-error')).toBeHidden();
    await panel.getByTestId('goal-action-input').press('Control+Enter');
    await expect.poll(() => posts).toBe(2);
    await expect(panel.getByTestId('goal-action-error')).toContainText('the lead is between turns');

    await panel.getByTestId('goal-action-input').press('Escape');
    await expect(panel).toHaveCount(0);
    await expect(view.getByTestId('goal-title')).toBeVisible();
  });

  // A goal's page that cannot be read says which goal, and why.
  test('a goal page that fails to load names the goal and the reason', async ({ pairedPage: page }, info) => {
    test.skip(info.project.name !== 'desktop', 'one viewport is enough for an error state');
    await openGoals(page);
    await page.route((u) => u.pathname === `/api/goals/${goal}`, (route) =>
      route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'the store is locked' }) }));
    await page.getByTestId(`goal-row-${goal}`).click();
    const err = page.getByTestId('goal-error');
    await expect(err).toContainText(`${goal} did not load`);
    await expect(err).toContainText('the store is locked');
  });

  // Deleting the goal from its card's menu asks the board's own question —
  // which says its two cards go with it — before anything is sent with a
  // yes; the page never confirms ahead of the server's question.
  test('deleting the goal shows the server’s question, cards and all', async ({ pairedPage: page, server, api }, info) => {
    await page.goto(`${server.url}/#${goal}`);
    await expect(page.getByTestId('card-id')).toHaveText(goal);
    if (info.project.name === 'phone') await page.getByTestId('tab-thread').click();
    const sent: any[] = [];
    page.on('request', (r) => { if (r.url().includes('/actions/delete') && r.method() === 'POST') sent.push(r.postDataJSON() || {}); });
    await page.getByTestId('card-actions').click();
    await page.getByTestId('action-delete').click();
    const q = page.getByTestId('action-question');
    await expect(q).toContainText(`Delete ${goal}?`);
    await expect(q).toContainText('and the same for its 2 cards');
    expect(sent.length).toBe(1);
    expect(sent[0].confirm).toBeUndefined();
    await shot(page, info, 'goal-delete-question');
    await page.getByTestId('action-cancel').click();
    expect((await api('GET', `/api/cards/${goal}`)).status).toBe(200);
  });

  // A goal deleted while its page is open says so, and asks the board for
  // nothing more: a refetch of a card that is gone is a 404 in the console.
  test('a goal deleted under its open page says so and fetches nothing', async ({ pairedPage: page, api }, info) => {
    test.skip(info.project.name !== 'desktop', 'one viewport is enough for a page that stops fetching');
    await page.getByTestId('rail-more').click();
    await page.getByTestId('menu-goals').click();
    await page.getByTestId(`goal-row-${goal}`).click();
    await expect(page.getByTestId('view-goal').getByTestId('goal-id')).toHaveText(goal);
    const failed: string[] = [];
    page.on('console', (m) => { if (m.type() === 'error' && /Failed to load resource/.test(m.text())) failed.push(m.text()); });
    const card = (await api('GET', `/api/cards/${goal}`)).json;
    const ask = await api('POST', `/api/cards/${goal}/actions/delete`, { against: card.decision?.against?.token });
    expect(ask.status).toBe(202);
    const done = await api('POST', `/api/cards/${goal}/actions/delete`, { against: card.decision?.against?.token, confirm: ask.json.confirm });
    expect(done.status).toBe(200);
    await expect(page.getByTestId('goal-gone')).toContainText(`${goal} was deleted`);
    await page.waitForTimeout(1500);
    expect(failed).toEqual([]);
  });
});

test('a goal is created from the form', async ({ pairedPage: page }, info) => {
  await openGoals(page);
  await expect(page.getByTestId('goals-empty')).toBeVisible();
  await page.getByTestId('goals-new').click();
  const form = page.getByTestId('goal-form');
  await form.getByTestId('goal-form-submit').click();
  await expect(form.getByTestId('goal-form-error')).toContainText('Describe the objective');
  await form.getByTestId('goal-form-desc').fill('Say goodbye in three languages');
  await form.getByTestId('goal-form-budget').fill('25');
  await form.getByTestId('goal-form-refs').fill('README.md');
  await shot(page, info, 'goal-form');
  await form.getByTestId('goal-form-submit').click();

  const view = page.getByTestId('view-goal');
  await expect(view.getByTestId('goal-title')).toHaveText('Say goodbye in three languages');
  await expect(view.getByTestId('goal-state')).toHaveText('todo');
  await expect(view.getByTestId('ledger-envelope')).toHaveText('$25.00');
  await expect(view.getByTestId('goal-notebook').getByTestId('goal-reference')).toContainText('README.md');
  await expect(view.getByTestId('goal-done-when')).toContainText('Nothing agreed yet');
  await shot(page, info, 'goal-new');

  await view.getByTestId('goal-back').click();
  await expect(page.getByTestId('view-goals').locator('[data-testid^="goal-row-GL-"]')).toHaveCount(1);
});

// The list says why it could not be read, and a board that does not serve
// goals at all says that instead of showing a raw status.
for (const c of [
  { status: 500, body: { error: 'the store is locked' }, says: 'the store is locked' },
  { status: 501, body: { error: 'not built' }, says: 'this board does not serve goals yet' },
]) {
  test(`the goals list on a ${c.status} says why it did not load`, async ({ pairedPage: page }, info) => {
    test.skip(info.project.name !== 'desktop', 'one viewport is enough for an error state');
    await page.route((u) => u.pathname === '/api/goals', (route) =>
      route.fulfill({ status: c.status, contentType: 'application/json', body: JSON.stringify(c.body) }));
    await openGoals(page);
    const err = page.getByTestId('goals-error');
    await expect(err).toContainText('Goals did not load');
    await expect(err).toContainText(c.says);
  });
}
