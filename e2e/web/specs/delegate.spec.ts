import { expect, test } from '../fixtures/test';

// A session's delegation (DESIGN §19.10): off until the person sets a
// budget; then the session may create workflow cards, each put to the
// person first. The card runs the whole graph on autopilot from the
// session's branch, stops verified, and lands back on that branch only
// when the session calls card_land.
test('a session with a delegation budget hands work to a card that lands back on it', async ({ pairedPage: page, server, api }, info) => {
  test.setTimeout(300_000);
  test.skip(info.project.name === 'phone', 'the phone answers decisions from its own dock; decisions.spec covers that path');
  const id = String((await api('POST', '/api/cards', { kind: 'freeform', title: 'Tidy the greetings' })).json?.id);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  const says = page.getByTestId('composer-says');
  await expect(says).not.toContainText('stop this turn', { timeout: 30_000 });
  const thread = page.getByTestId('thread');
  const input = page.getByTestId('composer-input');
  const send = async (text: string) => {
    await expect(says).not.toContainText('stop this turn', { timeout: 30_000 });
    await input.click();
    await input.fill(text);
    await page.keyboard.press('Enter');
    await expect(input).toHaveValue('');
  };

  // off by default: nothing on the card says it may delegate
  expect((await api('GET', `/api/cards/${id}`)).json.delegation).toBeUndefined();
  // the head may have folded the button away: the card's menu offers it too
  await page.getByTestId('card-actions').click();
  await page.getByTestId('action-delegate').click();
  await page.getByTestId('delegate-budget').fill('10');
  await page.getByTestId('delegate-save').click();
  await expect(page.getByTestId('delegate')).toContainText('Delegating · $10.00 left');
  expect((await api('GET', `/api/cards/${id}`)).json.delegation).toEqual({ budget: 1000, confirmAll: false, left: 1000 });

  // the session asks for a card; the person is asked before it exists
  await send('[delegate]');
  await expect(page.getByTestId('decision-question')).toContainText('create a feature card with 300 credits', { timeout: 30_000 });
  await page.getByTestId('decision-option-1').click();
  await expect(thread).toContainText(/card_create: created FD-\d+/, { timeout: 30_000 });
  const child = (await thread.textContent())!.match(/card_create: created (FD-\d+)/)![1];
  await expect(page.getByTestId('delegate')).toContainText('Delegating · $7.00 left');

  // the card forks from the session's branch and runs to a verified branch
  const card = (await api('GET', `/api/cards/${child}`)).json;
  expect(card.base).toBe((await api('GET', `/api/cards/${id}`)).json.branch);
  await expect.poll(async () => (await api('GET', `/api/cards/${child}`)).json.stage, { timeout: 200_000, intervals: [2_000] }).toBe('verify');
  let ready = false;
  for (let i = 1; i <= 20 && !ready; i++) {
    await send('[cards]');
    await expect.poll(async () => ((await thread.textContent()) ?? '').split('card_list:').length - 1, { timeout: 30_000 }).toBe(i);
    const last = ((await thread.textContent()) ?? '').split('card_list:').pop()!;
    ready = last.includes(`${child}`) && last.includes('ready to land');
    if (!ready) await page.waitForTimeout(3_000);
  }
  expect(ready).toBe(true);

  // the session commits its own work and lands the card on its branch
  await send(`[land ${child}]`);
  await expect(thread).toContainText(`card_land: landed ${child} on this branch`, { timeout: 60_000 });
  await expect.poll(async () => (await api('GET', `/api/cards/${child}`)).json.stage, { timeout: 10_000 }).toBe('done');
  // the squash is on the session's branch, beside its own commit
  const log = JSON.stringify((await api('GET', `/api/cards/${id}/log`)).json.commits);
  expect(log).toContain('feat: land the card');
  expect(log).toContain("the session's own notes");
});
