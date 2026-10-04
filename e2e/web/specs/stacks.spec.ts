import type { Page } from '@playwright/test';
import { expect, test, type Workspace } from '../fixtures/test';
import { shot } from '../fixtures/shots';

// The stacks view against a real `gummi web`: start a stack from a card,
// put cards on it, move one, take one out, and restack after the card at
// the bottom moved — which answers with the replays it made and the push
// lines they need (gummi never pushes). The card head's stack badge opens
// the view on its stack.

let ids: { wave: string; shrug: string; nod: string };

test.use({
  seed: {
    run: async (ws) => {
      const wave = await ws.seedVerified('Add a wave helper');
      const shrug = await ws.seedVerified('Add a shrug helper');
      const nod = await ws.seedDesignGate('Add a nod helper');
      ids = { wave, shrug, nod };
    },
  },
});

async function openStacks(page: Page) {
  await page.getByTestId('rail-more').click();
  await page.getByTestId('menu-stacks').click();
  await expect(page.getByTestId('view-stacks')).toBeVisible();
}

// amend the bottom card's commit, as review feedback on it would: every
// card above it now sits on a commit that no longer exists
async function moveBottom(ws: Workspace, id: string, n: number) {
  const tree = ws.worktree(id);
  await ws.exec('sh', ['-c', `echo "// feedback ${n}" >> NOTES.md && git add NOTES.md && git commit -q --amend --no-edit`], { cwd: tree });
}

test('a stack is started, grown, reordered, trimmed and restacked', async ({ pairedPage: page, workspace }, info) => {
  test.setTimeout(120_000);
  await openStacks(page);
  await expect(page.getByTestId('stacks-empty')).toBeVisible();

  // start one from the card at its bottom
  await page.getByTestId('stacks-new').click();
  const form = page.getByTestId('stack-form');
  await form.getByTestId('stack-form-card').selectOption(ids.wave);
  await form.getByTestId('stack-form-submit').click();
  const box = page.locator('[data-testid^="stack-"].sk-box');
  await expect(box).toHaveCount(1);
  await expect(box.getByTestId(`stack-member-${ids.wave}`)).toBeVisible();
  await expect(box.getByTestId('stack-base')).toContainText('forks from');

  // a second card on top, then a third
  await box.getByTestId('stack-add').click();
  await box.getByTestId('stack-add-card').selectOption(ids.shrug);
  await box.getByTestId('stack-add-submit').click();
  const shrug = box.getByTestId(`stack-member-${ids.shrug}`);
  await expect(shrug).toHaveAttribute('data-pos', '1');
  await expect(shrug.getByTestId('member-blocker')).toContainText(`lands after ${ids.wave}`);
  await box.getByTestId('stack-add').click();
  await box.getByTestId('stack-add-card').selectOption(ids.nod);
  await box.getByTestId('stack-add-submit').click();
  await expect(box.getByTestId(`stack-member-${ids.nod}`)).toHaveAttribute('data-pos', '2');

  // move the top card toward the base: it swaps with the one below it
  await box.getByTestId(`stack-up-${ids.nod}`).click();
  await expect(box.getByTestId(`stack-member-${ids.nod}`)).toHaveAttribute('data-pos', '1');
  await expect(shrug).toHaveAttribute('data-pos', '2');
  await shot(page, info, 'stack-chain');

  // take it out again, confirmed on the page
  await box.getByTestId(`stack-remove-${ids.nod}`).click();
  await expect(box.getByTestId('stack-remove-dialog')).toContainText(`Take ${ids.nod} out`);
  await box.getByTestId('stack-remove-confirm').click();
  await expect(box.getByTestId(`stack-member-${ids.nod}`)).toHaveCount(0);
  await expect(shrug).toHaveAttribute('data-pos', '1');

  // the bottom moves; a restack replays the card above it and prints the
  // push it needs. The board's own tick may replay it first — then the
  // restack finishes that walk and answers for it, or the panel shows the
  // board's replay with its push lines. Either way the lines are there
  // once the restack has answered; the loop only guards a move git read
  // as no change at all.
  let pushed = false;
  for (let n = 1; n <= 4 && !pushed; n++) {
    await moveBottom(workspace, ids.wave, n);
    const answered = page.waitForResponse((r) => r.url().includes('/restack') && r.request().method() === 'POST');
    await box.getByTestId('stack-restack').click();
    await answered;
    const result = box.getByTestId('stack-result');
    await expect(result).toBeVisible();
    pushed = await expect.poll(() => result.getByTestId('stack-push-line').count(), { timeout: 3000 })
      .toBeGreaterThan(0).then(() => true, () => false);
  }
  expect(pushed).toBe(true);
  await expect(box.getByTestId('stack-replayed')).toContainText(ids.shrug);
  // the workspace's repository has no remote: the line says there is
  // nothing to push, rather than naming an origin that is not there
  await expect(box.getByTestId('stack-push-line').first()).toContainText('no remote is configured');
  await expect(box.getByTestId('stack-push-line').first()).toContainText('feat/add-a-shrug-helper');
  await expect(box.getByTestId('stack-push-line').first()).not.toContainText('origin');
  await box.getByTestId('stack-push-copy').first().click();
  await expect(page.getByTestId('toast').filter({ hasText: /copied|selected/ })).toHaveCount(1);
  await shot(page, info, 'stack-restack');
});

test('the card head’s stack badge opens its stack', async ({ pairedPage: page, api }, info) => {
  const made = await api('POST', '/api/stacks', { card: ids.wave, cards: [ids.shrug] });
  expect(made.status).toBe(200);
  const stack = made.json.id as string;
  // boot names the auto-picked card in the address bar; from there the
  // reload reopens it (the hash persists)
  await expect(page).toHaveURL(/#/);
  await page.reload();
  if (info.project.name === 'phone') await page.getByTestId('card-back').click();
  await page.getByTestId(`rail-row-${ids.shrug}`).click();
  const badge = page.getByTestId('card-stack');
  await expect(badge).toHaveText('stack 2 of 2');
  await badge.click();
  const box = page.getByTestId(`stack-${stack}`);
  await expect(box).toHaveClass(/focus/);
  await expect(box.getByTestId(`stack-member-${ids.shrug}`)).toBeVisible();
  // rename, then delete is only offered once it is empty
  await box.getByTestId('stack-rename').click();
  await box.getByTestId('stack-rename-input').fill('greetings');
  await box.getByTestId('stack-rename-save').click();
  await expect(box.getByTestId('stack-name')).toHaveText('greetings');
  await expect(box.getByTestId('stack-delete')).toHaveCount(0);
  await shot(page, info, 'stack-focused');
});

test('an emptied stack is deleted with one notice, and two stacks never share a name', async ({ pairedPage: page, api }) => {
  const made = await api('POST', '/api/stacks', { card: ids.wave, name: 'solo' });
  expect(made.status).toBe(200);
  const stack = made.json.id as string;
  // a second stack, or a rename, onto a name already taken is refused
  const dup = await api('POST', '/api/stacks', { card: ids.shrug, name: 'Solo' });
  expect(dup.status).toBe(409);
  expect(String(dup.json?.error)).toContain('already has that name');

  await openStacks(page);
  const box = page.getByTestId(`stack-${stack}`);
  // the only card has nothing below it to stop forking from
  await box.getByTestId(`stack-remove-${ids.wave}`).click();
  await expect(box.getByTestId('stack-remove-dialog')).toContainText('it just leaves the stack');
  await expect(box.getByTestId('stack-remove-dialog')).not.toContainText('card below');
  await box.getByTestId('stack-remove-confirm').click();
  await expect(box.getByTestId(`stack-member-${ids.wave}`)).toHaveCount(0);
  await expect(box.getByTestId('stack-result')).toHaveCount(0);

  await box.getByTestId('stack-delete').click();
  await box.getByTestId('stack-delete-confirm').click();
  await expect(box).toHaveCount(0);
  await expect(page.getByTestId('toast').filter({ hasText: `stack ${stack} deleted` })).toHaveCount(1);
  // nothing ticks a stack that is gone, so no error follows the notice
  await page.waitForTimeout(800);
  await expect(page.locator('[data-testid="toast"].err')).toHaveCount(0);
});
