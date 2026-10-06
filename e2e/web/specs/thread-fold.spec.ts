import { expect, test } from '../fixtures/test';

// Once a person toggles a foldable row of the card thread, the row keeps
// their open state across every later redraw for the rest of the page
// session: an activity row is rebuilt when a call appends or its status
// settles, the pinned task list is rebuilt on every live delta, and a stage
// divider is rebuilt by a refetch — a rebuilt <details> starts closed, so
// each builder re-applies what the person last set. Rows they never touched
// keep the defaults, and the memory is scoped per card. Page session only:
// a reload forgets, so cards are switched with rail clicks, never reloads.

test.describe('the fold a person set on a thread row survives redraws', () => {
  test('a folded activity row stays folded as its calls land', async ({ pairedPage: page, server, api }, info) => {
    test.skip(info.project.name !== 'desktop', 'the fold memory is page state; one viewport is enough');
    const c = (await api('POST', '/api/cards', { kind: 'feature', title: '[fold] Keep the row open' })).json;
    const card = (await api('POST', `/api/cards/${c.id}/answer`, { ref: c.decision.ref, option: 'advance', against: c.decision.against.token })).json;
    await page.goto(`${server.url}/#${c.id}`);
    await expect(page.getByTestId('card-id')).toHaveText(c.id);
    await api('POST', `/api/cards/${c.id}/answer`, { ref: card.decision.ref, option: 'run', against: card.decision.against.token });

    // the plan stage's first activity row draws open with its read call
    // running; the person folds it while it is
    const row = page.getByTestId('activity').first();
    await expect(row).toBeVisible({ timeout: 30_000 });
    await expect(row).toContainText('greet.go');
    await expect(row).toHaveAttribute('open', '');
    await row.locator(':scope > summary').click();
    await expect(row).not.toHaveAttribute('open', '');

    // the call settles and a second one appends: the row is redrawn twice,
    // and the person's folded state survives both
    await expect(row.locator('li.running')).toHaveCount(0);
    await expect(row).not.toHaveAttribute('open', '');
    await expect(row).toContainText('Greet');
    await expect(row).not.toHaveAttribute('open', '');

    // answering the question sends the architect on: the new activity row
    // appears open, and the first row is still folded, while the session
    // is live
    await page.getByTestId('decision-option-1').click();
    await expect(page.getByTestId('activity')).toHaveCount(2, { timeout: 15_000 });
    await expect(page.getByTestId('activity').first()).not.toHaveAttribute('open', '');
    await expect(page.getByTestId('activity').last()).toHaveAttribute('open', '');
  });

  test('a folded task list stays folded across the next live delta', async ({ pairedPage: page, server, api }, info) => {
    test.skip(info.project.name !== 'desktop', 'the fold memory is page state; one viewport is enough');
    test.setTimeout(90_000);
    const made = await api('POST', '/api/cards', { kind: 'freeform', title: 'Fold the checklist' });
    const id = String(made.json?.id);
    await page.goto(`${server.url}/#${id}`);
    await expect(page.getByTestId('card-id')).toHaveText(id);
    await expect(page.getByTestId('composer-says')).not.toContainText('stop this turn', { timeout: 30_000 });

    // the checklist is pinned open while anything is left; folding it is
    // the person's choice
    const tasks = page.getByTestId('tasks');
    await expect(tasks).toContainText('tasks 1/2');
    await expect(tasks).toHaveAttribute('open', '');
    await tasks.locator('summary').click();
    await expect(tasks).not.toHaveAttribute('open', '');

    // the next turn rebuilds the pinned list on every live delta; it stays
    // folded through them and after the turn ends
    const input = page.getByTestId('composer-input');
    await input.click();
    await input.fill('note the second fold probe too');
    await page.keyboard.press('Enter');
    await expect(page.getByTestId('thread')).toContainText('note the second fold probe too', { timeout: 30_000 });
    await expect(page.getByTestId('composer-says')).not.toContainText('stop this turn', { timeout: 30_000 });
    await expect(tasks).not.toHaveAttribute('open', '');
  });

  // The pinned list opens by rule while anything is left; that default is
  // the page's own doing, never a person's choice, so a list nobody has
  // touched still folds once everything completes.
  test('a task list nobody touched folds when everything completes', async ({ pairedPage: page, server, api }, info) => {
    test.skip(info.project.name !== 'desktop', 'the fold memory is page state; one viewport is enough');
    test.setTimeout(90_000);
    const made = await api('POST', '/api/cards', { kind: 'freeform', title: 'Complete the checklist' });
    const id = String(made.json?.id);
    await page.goto(`${server.url}/#${id}`);
    await expect(page.getByTestId('card-id')).toHaveText(id);
    await expect(page.getByTestId('composer-says')).not.toContainText('stop this turn', { timeout: 30_000 });

    const tasks = page.getByTestId('tasks');
    await expect(tasks).toContainText('tasks 1/2');
    await expect(tasks).toHaveAttribute('open', '');
    const input = page.getByTestId('composer-input');
    await input.click();
    await input.fill('[tasks-done] wrap it up');
    await page.keyboard.press('Enter');
    await expect(page.getByTestId('thread')).toContainText('wrap it up', { timeout: 30_000 });
    await expect(page.getByTestId('composer-says')).not.toContainText('stop this turn', { timeout: 30_000 });
    await expect(tasks).toContainText('tasks 2/2');
    await expect(tasks).not.toHaveAttribute('open', '');
  });
});

test.describe('a stage divider remembers its person per card', () => {
  let one = '';
  let two = '';
  test.use({
    seed: { run: async (ws) => {
      one = await ws.seedVerified('Divider memory one');
      two = await ws.seedVerified('Divider memory two');
    } },
  });

  test('a past stage stays open for the card it was opened on', async ({ pairedPage: page, server }, info) => {
    test.skip(info.project.name !== 'desktop', 'the fold memory is page state; one viewport is enough');
    await page.goto(`${server.url}/#${one}`);
    await expect(page.getByTestId('card-id')).toHaveText(one);
    // a settled stage folds its divider when nobody has touched it (the
    // stage and its critique pass each draw a divider; the first is the one
    // the work ran in)
    const divider = page.getByTestId('stage-group-implement').first();
    await expect(divider).toBeVisible();
    await expect(divider).not.toHaveAttribute('open', '');
    // the divider's own summary is the first one inside it; the activity
    // rows it folds carry their own
    await divider.locator('summary').first().click();
    await expect(divider).toHaveAttribute('open', '');

    // the same-keyed divider on the other card does not inherit the fold
    await page.getByTestId(`rail-row-${two}`).click();
    await expect(page.getByTestId('card-id')).toHaveText(two);
    await expect(page.getByTestId('stage-group-implement').first()).not.toHaveAttribute('open', '');

    // and the first card's divider still remembers, across the full rebuild
    // the card switch caused
    await page.getByTestId(`rail-row-${one}`).click();
    await expect(page.getByTestId('card-id')).toHaveText(one);
    await expect(page.getByTestId('stage-group-implement').first()).toHaveAttribute('open', '');
  });
});
