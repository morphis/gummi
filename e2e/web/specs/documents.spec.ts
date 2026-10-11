import { expect, showCard, showTab, test } from '../fixtures/test';
import { shot } from '../fixtures/shots';

// The card page against a real `gummi web` and a card the scripted agent
// walked to a failed verify, with a linked pull request (fake gh): the
// thread, spec, diff, PR and stats tabs as the server serves them.

let id: string;
test.use({ seed: { run: async (ws) => { id = await ws.seedVerifyFailed('Add a regressing helper'); await ws.linkPR(id); } } });

test('the thread shows the stages the card walked and its verify', async ({ pairedPage: page }, info) => {
  await showCard(page, id);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await expect(page.getByTestId('stage-group-verify').last()).toBeVisible();
  await expect(page.getByTestId('stage-group-plan').first()).not.toHaveAttribute('open', '');
  await expect(page.getByTestId('thread-items').getByTestId('verify').last()).toContainText('failed');
  await shot(page, info, 'real-thread');
});

test('the spec, diff, PR and stats tabs read the real card', async ({ pairedPage: page }, info) => {
  await showCard(page, id);
  await showTab(page, 'spec');
  await expect(page.getByTestId('spec-toc')).toContainText('Chosen approach');
  await expect(page.getByTestId('spec-checks')).toContainText('go build');
  await shot(page, info, 'real-spec');

  await showTab(page, 'diff');
  await expect(page.getByTestId('diff-files')).toContainText(`${id.replace('-', '').toLowerCase()}.go`);
  const line = page.locator('[data-testid^="diff-line-"]').nth(3);
  await line.locator('.n').click();
  await page.getByTestId('annotation-input').fill('Name the helper after what it does');
  await page.getByTestId('annotation-save').click();
  await expect(page.locator('[data-testid^="annotation-"]').filter({ hasText: 'Name the helper' }).first()).toBeVisible();
  await expect(page.getByTestId('diff-pending')).toContainText('comment');
  await shot(page, info, 'real-diff');

  await showTab(page, 'pr');
  await expect(page.getByTestId('pr-state')).toContainText('open');
  await expect(page.getByTestId('pr-push-cmd')).toContainText('git push');
  await showTab(page, 'stats');
  await expect(page.getByTestId('stats-table')).toContainText('implement');
  await shot(page, info, 'real-stats');
});
