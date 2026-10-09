import { expect, test } from '../fixtures/test';

let id = '';
test.use({ seed: { run: async (ws) => { id = await ws.seedVerified('Add a parting helper'); } } });

// /close is hand-off typed: the board asks its own yes/no first, and the
// yes sends the same line again with the question's token.
test('/close from the composer hands the card off after a yes', async ({ pairedPage: page, server, api }, info) => {
  test.setTimeout(120_000);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  if (info.project.name === 'phone') await page.getByTestId('tab-thread').click();

  const input = page.getByTestId('composer-input');
  await input.click();
  await input.fill('/close');
  await page.keyboard.press('Enter');

  const confirm = page.getByTestId('decision-confirm');
  await expect(confirm).toBeVisible();
  await expect(confirm).toContainText(`Hand off ${id}`);
  expect((await api('GET', `/api/cards/${id}`)).json.stage).not.toBe('done');

  await page.getByTestId('decision-confirm-yes').click();
  await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.stage).toBe('done');
  await expect(input).toHaveValue('');
});
