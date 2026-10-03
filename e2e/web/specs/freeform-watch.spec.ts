import { expect, test } from '../fixtures/test';

// A freeform turn that starts a Monitor watch and ends with it still open:
// the card reads as watching on the rail once the turn is over, the thread
// lists the watch by its command, and the turn's activity row says watching
// rather than hiding the watch in its summary — all without a reload.
test('an open watch shows on the rail and in the thread once its turn ends', async ({ pairedPage: page, server, api }, info) => {
  test.setTimeout(90_000);
  const made = await api('POST', '/api/cards', { kind: 'freeform', title: 'Watch the build' });
  const id = String(made.json?.id);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  if (info.project.name === 'phone') await page.getByTestId('tab-thread').click();
  await expect(page.getByTestId('composer-says')).not.toContainText('stop this turn', { timeout: 30_000 });

  const input = page.getByTestId('composer-input');
  await input.click();
  await input.fill('[watch] tell me when the build breaks');
  await page.keyboard.press('Enter');
  await expect(input).toHaveValue('');

  // the turn is over and the watch is still open: the card says so
  await expect(page.getByTestId('composer-says')).not.toContainText('stop this turn', { timeout: 30_000 });
  await expect(page.getByTestId('watches')).toContainText('tail -f build.log');
  await expect(page.getByTestId('activity').last()).toContainText('watching');
});
