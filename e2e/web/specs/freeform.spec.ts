import { expect, test } from '../fixtures/test';
import fs from 'node:fs';
import path from 'node:path';

// A freeform card is a conversation: one enter sends a line as a turn, the
// agent's reply lands in the thread, and when the turn ends the card stops
// offering to stop it — without a reload.
test('a freeform turn is sent by one enter and ends on the page when it ends', async ({ pairedPage: page, server, api }, info) => {
  test.setTimeout(90_000);
  const made = await api('POST', '/api/cards', { kind: 'freeform', title: 'Poke at the rounding' });
  const id = String(made.json?.id);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  if (info.project.name === 'phone') await page.getByTestId('tab-thread').click();
  // the card's opening turn runs and ends
  await expect(page.getByTestId('composer-says')).not.toContainText('stop this turn', { timeout: 30_000 });

  const posts: string[] = [];
  page.on('request', (r) => { if (r.method() === 'POST' && /\/api\/cards\/[^/]+\/(send|answer)$/.test(r.url())) posts.push(r.url().split('/').pop()!) });
  const input = page.getByTestId('composer-input');
  await input.click();
  await input.fill('make the week view sum before rounding');
  await page.keyboard.press('Enter');
  // one enter was enough: the line went as a turn, not as an answer
  await expect(input).toHaveValue('');
  await expect.poll(() => posts).toEqual(['send']);

  // the reply arrives, and the card is no longer working
  await expect(page.getByTestId('thread')).toContainText('Done: noted it in NOTES.md.', { timeout: 30_000 });
  await expect(page.getByTestId('composer-says')).not.toContainText('stop this turn', { timeout: 10_000 });
  await expect(page.getByTestId('decision-question').filter({ hasText: 'working on a turn' })).toHaveCount(0);
  // what the agent thought is there, folded behind its summary, and the
  // call it made settled with an outcome rather than hanging unresolved
  const activity = page.getByTestId('activity').last();
  await expect(activity).toContainText('thought');
  await expect(activity).not.toHaveAttribute('open', '');
  await activity.locator('summary').click();
  await expect(activity).toContainText('The ask belongs in NOTES.md');
  const live = (await api('GET', `/api/cards/${id}/live`)).json;
  const turns = JSON.stringify(live.freeform?.turns ?? []);
  expect(turns).toContain('"author":"thinking"');
  expect(turns).toContain('"status":"ok"');
  // the agent's checklist is pinned under the conversation, not in its turns
  const tasks = page.getByTestId('tasks');
  await expect(tasks).toContainText('tasks 1/2');
  await expect(tasks).toContainText('Say what was done');
  expect(live.freeform?.tasks?.length).toBe(2);
  expect(turns).not.toContain('Note the fix');
  const card = (await api('GET', `/api/cards/${id}`)).json;
  expect(JSON.stringify(card.decision ?? {})).not.toContain('working on a turn');
});

// A freeform card walks no stages, so once it lands it has none to have
// passed: the head and the rail must not tick a plan, an implement and a
// verify it never had.
test('a landed freeform card claims no stages', async ({ pairedPage: page, server, api }, info) => {
  test.setTimeout(90_000);
  const id = String((await api('POST', '/api/cards', { kind: 'freeform', title: 'Poke at the padding' })).json?.id);
  // an idle session pins no decision: its landing is its menu's merge
  await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.actions?.some((a: any) => a.id === 'merge'), { timeout: 30_000 }).toBe(true);
  expect((await api('GET', `/api/cards/${id}`)).json.decision).toBeUndefined();
  let r = await api('POST', `/api/cards/${id}/actions/merge`, { message: 'chore: pad the padding' });
  for (let i = 0; i < 3 && r.json?.confirm; i++) {
    r = await api('POST', `/api/cards/${id}/actions/merge`, { message: 'chore: pad the padding', confirm: r.json.confirm });
  }
  await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.stage, { timeout: 30_000 }).toBe('done');
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await expect(page.getByTestId('card-stages')).toContainText('session');
  await expect(page.getByTestId('card-stages')).not.toContainText('verify');
  await expect(page.getByTestId('stage-verify')).toHaveCount(0);
  if (info.project.name === 'phone') await page.getByTestId('card-back').click();
  await expect(page.getByTestId(`rail-row-${id}`)).toContainText('session');
});

// A line sent while the agent is mid-turn waits under the conversation
// instead of being refused; it can be taken back to edit, and what is left
// goes to the agent once the turn ends.
test('a line sent mid-turn is queued and can be taken back', async ({ pairedPage: page, server, api }, info) => {
  test.setTimeout(90_000);
  const made = await api('POST', '/api/cards', { kind: 'freeform', title: '[slow] Queue behind a turn' });
  const id = String(made.json?.id);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  if (info.project.name === 'phone') await page.getByTestId('tab-thread').click();
  await expect(page.getByTestId('live-freeform')).toBeVisible({ timeout: 30_000 });

  const input = page.getByTestId('composer-input');
  for (const line of ['first queued line', 'second queued line']) {
    await input.click();
    await input.fill(line);
    await page.keyboard.press('Enter');
    await expect(input).toHaveValue('');
  }
  const queued = page.getByTestId('queued');
  await expect(queued).toContainText('first queued line');
  await expect(queued).toContainText('second queued line');
  // edit takes the line back into the composer and out of the queue
  await page.getByTestId('queued-edit').last().click();
  await expect(input).toHaveValue('second queued line');
  await expect(queued).not.toContainText('second queued line');

  // the rest goes once the turn in flight ends
  await expect(queued).toHaveCount(0, { timeout: 45_000 });
  await expect(page.getByTestId('thread')).toContainText('first queued line', { timeout: 30_000 });
});

// Rewind takes the conversation back to before one of the person's
// messages and puts it in the composer to edit; the branch is not rewound,
// and the session says so.
test('a freeform conversation can be rewound to before a message', async ({ pairedPage: page, server, api }, info) => {
  test.setTimeout(90_000);
  const made = await api('POST', '/api/cards', { kind: 'freeform', title: 'Poke at the rewind' });
  const id = String(made.json?.id);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  if (info.project.name === 'phone') await page.getByTestId('tab-thread').click();
  await expect(page.getByTestId('composer-says')).not.toContainText('stop this turn', { timeout: 30_000 });

  const input = page.getByTestId('composer-input');
  await input.click();
  await input.fill('round the totals instead');
  await page.keyboard.press('Enter');
  // the turn has run and ended: the reply to it is in, and nothing is busy
  await expect.poll(async () => {
    const f = (await api('GET', `/api/cards/${id}/live`)).json?.freeform;
    const turns = f?.turns ?? [];
    const mine = turns.findIndex((t: { text?: string }) => t.text === 'round the totals instead');
    return !f?.busy && mine >= 0 && turns.slice(mine + 1).some((t: { author: string }) => t.author !== 'you' && t.author !== 'tool');
  }, { timeout: 30_000 }).toBe(true);

  await page.getByTestId('turn-rewind').last().click();
  await expect(input).toHaveValue('round the totals instead');
  await expect(page.getByTestId('thread')).toContainText('The branch was not rewound');
  const live = (await api('GET', `/api/cards/${id}/live`)).json;
  const yours = (live.freeform?.turns ?? []).filter((t: { author: string }) => t.author === 'you').map((t: { text: string }) => t.text);
  expect(yours).not.toContain('round the totals instead');
});

// A half-typed "/word" on a freeform card offers the repository's own
// commands that it could become, and tab takes the highlighted one.
test('a project command is offered while its name is typed', async ({ pairedPage: page, server, api, workspace }, info) => {
  test.setTimeout(90_000);
  const id = String((await api('POST', '/api/cards', { kind: 'freeform', title: 'Offer the commands' })).json?.id);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  if (info.project.name === 'phone') await page.getByTestId('tab-thread').click();
  await expect(page.getByTestId('composer-says')).not.toContainText('stop this turn', { timeout: 30_000 });
  const dir = path.join(workspace.repo, '.gummi', 'worktrees', id, '.claude', 'commands');
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'review.md'), '---\ndescription: review the diff\n---\nReview $ARGUMENTS.\n');
  fs.writeFileSync(path.join(dir, 'release.md'), 'Cut a release.\n');

  const input = page.getByTestId('composer-input');
  const offer = page.getByTestId('composer-complete');
  await input.click();
  await input.fill('/re');
  await expect(offer).toBeVisible();
  // the card's own words complete beside the repository's command files
  await expect(offer.getByRole('option')).toHaveText([
    /\/release\s*Cut a release\./,
    /\/review\s*review the diff/,
    /\/rebase\s*rebase branch onto main \(conflicts go to an agent\)/,
  ]);
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('Tab');
  await expect(input).toHaveValue('/review ');
  await expect(input).toBeFocused();
  await expect(page.getByTestId('composer-says')).toContainText('runs the project\'s /review');
  await expect(offer).toBeHidden();
  // a word naming nothing offers nothing
  await input.fill('/nosuch');
  await expect(page.getByTestId('composer-says')).toContainText('opens the card\'s menu');
  await expect(offer).toBeHidden();
  // and on a session a menu word is handed over the same way: the menu
  // opens and the line leaves the field, never looping back
  await page.keyboard.press('Enter');
  await expect(page.getByTestId('card-actions-menu')).toBeVisible();
  await expect(input).toHaveValue('');
  await page.keyboard.press('Escape');
  await expect(page.getByTestId('card-actions-menu')).toBeHidden();
  await expect(input).toHaveValue('');

  // the completed command sends as a turn: the session hears the command
  await input.fill('/review');
  await expect(offer).toBeVisible();
  await page.keyboard.press('Tab');
  await expect(input).toHaveValue('/review ');
  await input.fill('/review the parser');
  await expect(page.getByTestId('composer-says')).toContainText('runs the project\'s /review');
  await page.keyboard.press('Enter');
  await expect(input).toHaveValue('');
  await expect(page.getByTestId('thread')).toContainText('/review the parser', { timeout: 30_000 });
});

// A session's work stays uncommitted until the person commits it — or
// lands, which commits what is left as a final checkpoint first. The
// menu says so on both entries, and a commit sent with no message is
// refused in words rather than asked again as if nothing was sent.
test('commit and land say what each commits, and an empty commit message is refused', async ({ pairedPage: page, server, api, workspace }, info) => {
  test.setTimeout(90_000);
  const id = String((await api('POST', '/api/cards', { kind: 'freeform', title: 'Poke at the margins' })).json?.id);
  await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.actions?.some((a: any) => a.id === 'merge'), { timeout: 30_000 }).toBe(true);
  fs.writeFileSync(path.join(workspace.worktree(id), 'MARGINS.md'), 'wider\n');
  await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.actions?.some((a: any) => a.id === 'commit'), { timeout: 30_000 }).toBe(true);
  const actions = (await api('GET', `/api/cards/${id}`)).json.actions as any[];
  expect(actions.find((a) => a.id === 'commit').detail).toContain('final checkpoint');
  expect(actions.find((a) => a.id === 'commit').detail).not.toContain('never commits');
  expect(actions.find((a) => a.id === 'merge').detail).toContain('uncommitted is committed first');

  const refused = await api('POST', `/api/cards/${id}/actions/commit`, { message: '' });
  expect(refused.status).toBe(400);
  expect(String(refused.json?.error)).toContain('a commit needs a message');

  if (info.project.name === 'phone') return;
  await page.goto(`${server.url}/#${id}`);
  // beside the open panel the head's Commit… may fold into the card's
  // menu, where it is always an entry
  await page.getByTestId('card-actions').click();
  await page.getByTestId('action-commit').click();
  await page.getByTestId('action-confirm').click();
  await expect(page.getByTestId('action-error')).toContainText('needs a message');
});
