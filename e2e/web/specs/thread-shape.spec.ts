import { expect, test } from '../fixtures/test';

// A session reads as a conversation: the person's lines are bubbles of
// their own, the agent's replies say what ran them, each step it took is a
// row that opens to its output, the files it changed sit above the
// composer, and a file can be named by "@".
test('a session thread has bubbles, reply footers, step rows and changed files', async ({ pairedPage: page, server, api }, info) => {
  test.setTimeout(90_000);
  const made = await api('POST', '/api/cards', { kind: 'freeform', title: 'Shape of the thread' });
  const id = String(made.json?.id);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  if (info.project.name === 'phone') await page.getByTestId('tab-thread').click();
  await expect(page.getByTestId('composer-says')).not.toContainText('stop this turn', { timeout: 30_000 });

  const input = page.getByTestId('composer-input');
  await expect(input).toHaveAttribute('placeholder', /@ for files/);
  await input.click();
  await input.fill('first line\nsecond line');
  await page.keyboard.press('Enter');
  await expect(page.getByTestId('thread')).toContainText('Done: noted it in NOTES.md.', { timeout: 30_000 });
  await expect(page.getByTestId('composer-says')).not.toContainText('stop this turn', { timeout: 10_000 });

  // the person's line is a bubble on the right, its line breaks kept
  const bubble = page.getByTestId('bubble').filter({ hasText: 'first line' }).last();
  await expect(bubble).toBeVisible();
  const lines = await bubble.locator('.bubble .body').evaluate((el) => (el as HTMLElement).innerText);
  expect(lines).toMatch(/first line\n+second line/);
  const thread = await page.getByTestId('thread').boundingBox();
  const box = await bubble.locator('.bubble').boundingBox();
  expect(box!.x + box!.width).toBeGreaterThan(thread!.x + thread!.width * 0.7);

  // the reply has a footer: when it was written, how long it took, a copy
  const reply = page.getByTestId('reply').filter({ hasText: 'Done: noted it in NOTES.md.' }).last();
  const foot = reply.getByTestId('reply-foot');
  await expect(foot).toBeVisible();
  await expect(foot.getByTestId('reply-copy')).toHaveCount(1);

  // each step is a row; one with output opens to show it, with a copy
  const activity = page.getByTestId('activity').last();
  await expect(activity).toHaveAttribute('open', '');
  const edit = activity.getByTestId('tool-row').filter({ hasText: 'NOTES.md' }).last();
  await expect(edit).toContainText('Edited');
  if (await edit.locator('summary').count()) {
    await edit.locator('summary').click();
    await expect(edit.getByTestId('tool-output')).toBeVisible();
    await expect(edit.getByTestId('tool-copy')).toBeVisible();
  }

  // "@" offers the worktree's files, and picking one replaces only the word
  await input.click();
  await input.fill('look at @NOT');
  const offered = page.getByTestId('composer-complete');
  await expect(offered).toContainText('@NOTES.md', { timeout: 5_000 });
  await page.keyboard.press('Tab');
  await expect(input).toHaveValue('look at @NOTES.md ');
  await input.fill('');

  await page.screenshot({ path: `screens/thread-shape-${info.project.name}.png`, fullPage: false });

  // scrolled up off the newest line, the thread offers the way back down
  const sc = page.getByTestId('thread');
  await sc.evaluate((el) => { el.querySelector('.thread-inner')!.append(Object.assign(document.createElement('div'), { style: 'height:3000px' })); el.scrollTop = 0; });
  const down = page.getByTestId('to-bottom');
  await expect(down).toBeVisible();
  await down.click();
  await expect(down).toBeHidden({ timeout: 5_000 });
});
