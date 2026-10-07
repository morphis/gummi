import { expect, test } from '../fixtures/test';

// A session's own commands work on every agent — the e2e agent has no
// compaction of its own — and gummi answers them: "/" lists them under a
// heading of their own, /cost and /help answer on the thread without a
// turn, /compact replaces the conversation with the agent's summary, and
// /clear starts it afresh.
test('a session takes /help, /cost, /compact and /clear on any agent', async ({ pairedPage: page, server, api }, info) => {
  test.setTimeout(120_000);
  const made = await api('POST', '/api/cards', { kind: 'freeform', title: 'Session commands' });
  const id = String(made.json?.id);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  if (info.project.name === 'phone') await page.getByTestId('tab-thread').click();
  const says = page.getByTestId('composer-says');
  await expect(says).not.toContainText('stop this turn', { timeout: 30_000 });

  const input = page.getByTestId('composer-input');
  const send = async (line: string) => {
    await input.click();
    await input.fill(line);
    await expect(says).not.toHaveText('sends your message', { timeout: 5_000 });
    await page.keyboard.press('Enter');
    await expect(input).toHaveValue('');
    await expect(says).not.toContainText('stop this turn', { timeout: 30_000 });
  };

  // "/" offers the session's commands under their own heading
  await input.click();
  await input.fill('/');
  const menu = page.getByTestId('composer-complete');
  await expect(menu).toContainText('Session');
  for (const c of ['/compact', '/clear', '/retry', '/context', '/cost', '/help']) await expect(menu).toContainText(c);
  await input.fill('/cost');
  await expect(says).toContainText('sends nothing');
  await page.screenshot({ path: `screens/session-commands-menu-${info.project.name}.png` });

  // instant commands answer on the thread and cost no turn
  const turnsBefore = (await api('GET', `/api/cards/${id}/live`)).json.freeform?.turns?.filter((t: { author: string }) => t.author === 'you').length ?? 0;
  await send('/cost');
  await expect(page.getByTestId('thread')).toContainText('This backend has spent');
  await send('/help');
  await expect(page.getByTestId('thread')).toContainText('Commands this session takes');
  const live = (await api('GET', `/api/cards/${id}/live`)).json;
  expect(live.freeform?.turns?.filter((t: { author: string }) => t.author === 'you').length ?? 0).toBe(turnsBefore);

  // /compact: the agent writes a summary and the conversation becomes it
  await send('/compact');
  await expect(page.getByTestId('thread')).toContainText('Compacted the conversation', { timeout: 30_000 });
  await page.screenshot({ path: `screens/session-commands-compact-${info.project.name}.png` });

  // /clear: the conversation starts afresh, the branch untouched
  await send('/clear');
  await expect(page.getByTestId('thread')).toContainText('Cleared the conversation', { timeout: 10_000 });
  await expect(page.getByTestId('thread')).not.toContainText('Compacted the conversation');
});
