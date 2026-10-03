import { expect, test } from '../fixtures/test';
import { decision, mockCard, stats } from '../fixtures/contract';
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
  await expect(page.getByTestId('stats-spent')).toContainText('11.0');
  await expect(page.getByTestId('stats-table').locator('tr.rework')).toHaveCount(1);
  // where it went: the stage/role/model bars, the estimated mark among them
  await expect(page.getByTestId('stats-bars')).toContainText('implement');
  await expect(page.getByTestId('stats-bars')).toContainText('6.5');
  await expect(page.getByTestId('stats-bars')).toContainText('estimated');
  // the redo block names the pass, and flags the one that cost more than
  // the first pass of the same work
  await expect(page.getByTestId('stats-redo')).toContainText('corrected');
  await expect(page.getByTestId('stats-redo')).toContainText('cost more than the first');
  // the clock: a bar over the segments the card has, no idle one
  await expect(page.getByTestId('stats-clock')).toContainText('agent working');
  await expect(page.getByTestId('stats-clock')).toContainText('waiting on you');
  await expect(page.getByTestId('stats-clock')).toContainText('elapsed');
  await expect(page.getByTestId('stats-clock')).toContainText('to first gate');
  await expect(page.getByTestId('stats-clock')).toContainText('to verified');
  await expect(page.getByTestId('stats-clock')).not.toContainText('nothing running');
  // its hands: the tool table, what was handed to a skill and a subagent,
  // and gummi's checks with the excused marking
  await expect(page.getByTestId('stats-hands')).toContainText('turns 9');
  await expect(page.getByTestId('stats-hands')).toContainText('12 calls, 1 failed');
  await expect(page.getByTestId('stats-tools')).toContainText('run');
  await expect(page.getByTestId('stats-hands')).toContainText('gummi-go-verify');
  await expect(page.getByTestId('stats-hands')).toContainText('1 spawned');
  await expect(page.getByTestId('stats-hands')).toContainText('find the fold');
  await expect(page.getByTestId('stats-checks')).toContainText('clean');
  await expect(page.getByTestId('stats-checks')).toContainText('pre-existing, excused');
  // its judgment: gates and asks split by who answered, and the park
  await expect(page.getByTestId('stats-judgment')).toContainText('2 gates');
  await expect(page.getByTestId('stats-judgment')).toContainText('1 asks');
  await expect(page.getByTestId('stats-judgment')).toContainText('1 of 3 checks failed');
  // the envelope line carries the utilization
  await expect(page.getByTestId('stats-envelope')).toContainText('granted 40 · spent 11.0 · 28% used');
  // the passes table's honesty marks: per-pass tokens with the components
  // beside the total, and the context occupancy where the pass reported one
  await expect(page.getByTestId('stats-table')).toContainText('81k');
  await expect(page.getByTestId('stats-table')).toContainText('31k cached');
  await expect(page.getByTestId('stats-table')).toContainText('2.1k out');
  await expect(page.getByTestId('stats-table').locator('.ctxm')).toHaveCount(1);
  await shot(page, info, 'stats');
});

// The honesty sentence on an absent tools array: a backend that reports no
// tool calls reads as words — never as a zeroed tools line.
test('the stats tab says none recorded where a backend reports no tool calls', async ({ pairedPage: page }, info) => {
  await mockCard(page, id);
  // JSON.stringify drops undefined keys, so tools gone from the spread
  // answers the wire's absent array, not an empty one the card made none of
  await page.route(`**/api/cards/${id}/stats`, (r) => r.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ ...stats, hands: { ...stats.hands, tools: undefined } }),
  }));
  await page.reload();
  await page.getByTestId('tab-stats').click();
  await expect(page.getByTestId('stats-hands')).toContainText('turns 9');
  await expect(page.getByTestId('stats-no-tools')).toContainText('none recorded');
  await expect(page.getByTestId('stats-hands')).not.toContainText('0 calls');
  await expect(page.getByTestId('stats-tools')).toHaveCount(0);
  await shot(page, info, 'stats-no-tools');
});
