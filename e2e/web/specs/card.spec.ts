import { expect, test } from '../fixtures/test';
import { decision, mockCard } from '../fixtures/contract';
import { shot } from '../fixtures/shots';

// The card page's drawing against contract-shaped answers (fixtures/
// contract.ts), for states the harness cannot put a card in on demand: a
// thread holding every item kind at once (markup that must stay text, a
// stretch, a live stream), a spec with a person's note and gummi's own
// prompt, a diff with a comment already on it, a linked PR's threads and a
// run with rework in its stats. The card head and the board come from a
// real `gummi web`. Answering decisions, the composer, the card's menu and
// the new-card form run against the real server in decisions.spec.ts,
// actions.spec.ts and newcard.spec.ts.

let id: string;
test.use({ seed: { run: async (ws) => { id = await ws.seedDesignGate('Add a wave helper'); await ws.seedBacklog(['Add a shrug helper']); } } });

test('the thread folds past stages and draws every item kind', async ({ pairedPage: page }, info) => {
  await mockCard(page, id);
  await page.reload();
  const items = page.getByTestId('thread-items');
  await expect(page.getByTestId('stage-group-verify')).toHaveAttribute('open', '');
  await expect(page.getByTestId('stage-group-plan')).not.toHaveAttribute('open', '');
  await expect(page.getByTestId('stage-group-plan')).toContainText('approved');
  await expect(items.getByTestId('verify')).toContainText('1 failed');
  // the verify pass wears an avatar like every other role's
  await expect(items.getByTestId('verify-avatar')).toHaveText('VE');
  await expect(items.getByTestId('check-clean')).toContainText('TestCleanKeepsAdopted');
  await expect(page.getByTestId('live')).toContainText('reading the failure');
  // the safe renderer: markup in a message is text, links are http(s) only
  await page.getByTestId('stage-group-plan').locator(':scope > summary').click();
  await expect(items.locator('script')).toHaveCount(0);
  await expect(items).toContainText('<script>alert(1)</script>');
  await expect(items.locator('a[href="https://example.com/design"]')).toHaveAttribute('rel', /noopener/);
  // a past stage in the head's strip opens its segment
  await page.getByTestId('stage-group-plan').locator(':scope > summary').click();
  if (info.project.name !== 'phone') {
    await page.getByTestId('stage-implement').click();
    await expect(page.getByTestId('stage-group-implement')).toHaveAttribute('open', '');
  }
  await shot(page, info, 'thread');
});

test('the spec shows sections, notes, checks and a comment box', async ({ pairedPage: page }, info) => {
  const m = await mockCard(page, id);
  await page.goto(page.url().replace(/#.*$/, '') + `#${id}/spec`);
  await page.reload();
  if (info.project.name === 'phone') await page.getByTestId('tab-spec').click();
  const doc = page.getByTestId('spec-doc');
  await expect(doc.getByTestId('spec-section-1')).toContainText('Chosen approach');
  await expect(doc.getByTestId('spec-note')).toContainText('Does clean share');
  await expect(doc).not.toContainText('a prompt the page must not show');
  await expect(doc.getByTestId('spec-check-test')).toContainText('✕');
  await doc.getByTestId('spec-comment-0').click();
  await expect(doc.getByTestId('spec-note-input')).toBeFocused();
  await expect(page.getByTestId('spec-pending')).toHaveText('1 comment is still open here.');
  await page.getByTestId('spec-request-changes').click();
  await expect(page.getByTestId('toast').filter({ hasText: 'with 1 review comment' })).toBeVisible();
  // the comments stay open until the agent answers them: the button stays
  // down so the same ones are not sent twice
  await expect(page.getByTestId('spec-request-changes')).toBeDisabled();
  await expect(page.getByTestId('spec-request-changes')).toHaveText('Sent');
  expect(m.changes).toEqual(['spec']);
  await shot(page, info, 'spec');
});

test('the diff draws files, comments inline, keeps viewed ticks and adds a comment', async ({ pairedPage: page }, info) => {
  const m = await mockCard(page, id);
  await page.reload();
  if (info.project.name === 'phone') await page.getByTestId('tab-diff').click();
  // a verify-failed decision is about the diff: the panel follows it
  await expect(page.getByTestId('tab-diff')).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByTestId('diff-line-8')).toContainText('func Wave');
  await expect(page.getByTestId('diff-line-9')).toContainText('<b>not bold</b>');
  await expect(page.getByTestId('annotation-1')).toContainText('Name it WaveAt?');
  await expect(page.getByTestId('diff-pending')).toContainText('1 comment');
  await page.getByTestId('diff-line-10').locator('.n').click();
  await page.getByTestId('annotation-input').fill('Close the brace on its own line');
  await page.getByTestId('annotation-save').click();
  await expect(page.getByTestId('annotation-2')).toContainText('Close the brace');
  await expect(page.getByTestId('diff-pending')).toContainText('2 comments');
  await page.getByTestId('diff-viewed-1').check();
  await expect(page.getByTestId('diff-filebox-1')).toHaveClass(/viewed/);
  await page.reload();
  if (info.project.name === 'phone') await page.getByTestId('tab-diff').click();
  await expect(page.getByTestId('diff-viewed-1')).toBeChecked();
  expect(m.annotations).toHaveLength(2);
  // at verify the diff's comments send the card back to implement, and
  // the page asks the board's own question before it does
  await page.getByTestId('diff-request-changes').click();
  await expect(page.getByTestId('changes-question')).toContainText('back to implement');
  await page.getByTestId('changes-go').click();
  await expect(page.getByTestId('toast').filter({ hasText: 'sent back to implement' })).toBeVisible();
  await expect(page.getByTestId('diff-request-changes')).toBeDisabled();
  await expect(page.getByTestId('diff-request-changes')).toHaveText('Sent');
  expect(m.changes).toEqual(['diff', 'diff:c0ffee']);
  await shot(page, info, 'diff');
});

test('the decision points only at what the card has', async ({ pairedPage: page }, info) => {
  // a research card has no branch diff: its decision sends the reader to
  // the document, whatever tab the decision is about
  await mockCard(page, id, { kind: 'research' });
  await page.reload();
  if (info.project.name === 'phone') await page.getByTestId('tab-thread').click();
  await expect(page.getByTestId('decision-jump')).toHaveText('read the document');
  await page.unrouteAll({ behavior: 'ignoreErrors' });

  // comments on a diff go with an answer only when one carries them
  const { options } = decision;
  await mockCard(page, id, { decision: { ...decision, options: options.map((o) => ({ ...o, carriesComments: false })) } });
  await page.reload();
  await page.getByTestId('tab-diff').click();
  await expect(page.getByTestId('diff-pending')).toHaveText('1 comment on this diff is still open.');
  await expect(page.getByTestId('decision-carry')).toHaveCount(0);
});

test('the PR and stats tabs draw their reads', async ({ pairedPage: page }, info) => {
  await mockCard(page, id);
  await page.reload();
  await page.getByTestId('tab-pr').click();
  await expect(page.getByTestId('pr-state')).toContainText('open');
  await expect(page.getByTestId('pr-thread-0')).toContainText('Should Wave trim');
  await expect(page.getByTestId('pr-push-cmd')).toHaveText('git push origin feat/add-a-wave-helper');
  await shot(page, info, 'pr');
  await page.getByTestId('tab-stats').click();
  await expect(page.getByTestId('stats-spent')).toContainText('8.5');
  await expect(page.getByTestId('stats-table').locator('tr.rework')).toHaveCount(1);
  await shot(page, info, 'stats');
});
