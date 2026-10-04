import { expect, test } from '../fixtures/test';
import { shot } from '../fixtures/shots';

// Pairing through the page: the form a browser without a device cookie
// sees, a wrong code (and how many tries it leaves), then the right one.

test('a browser pairs with the printed code and lands on the board', async ({ page, server }, info) => {
  await page.goto(server.url);
  await expect(page.getByTestId('pair-form')).toBeVisible();
  await expect(page.getByTestId('app')).toBeHidden();
  await shot(page, info, 'pair');
  const code = await server.freshCode();
  const wrong = code === '000000' ? '111111' : '000000';
  await page.getByLabel(/name/i).fill('Tester');
  await page.getByLabel(/code/i).fill(wrong);
  await page.getByRole('button', { name: /^pair$/i }).click();
  await expect(page.getByTestId('pair-error')).toContainText(/tries? left/);
  // the count is said once, not by the server and again by the page
  expect((await page.getByTestId('pair-error').textContent())!.match(/left/g)).toHaveLength(1);
  await page.getByLabel(/code/i).fill(code);
  await page.getByRole('button', { name: /^pair$/i }).click();
  await expect(page.getByTestId('app')).toBeVisible();
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  await expect(page.getByTestId('pair')).toBeHidden();
});

test('the connection pill says when the board is gone and when it is back', async ({ pairedPage: page, server }, info) => {
  test.skip(info.project.name !== 'desktop', 'one viewport is enough');
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  await server.stop();
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'reconnecting');
  await expect(page.getByTestId('composer-says')).toContainText('wait until the board reconnects');
  await server.restart();
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live', { timeout: 30_000 });
});
