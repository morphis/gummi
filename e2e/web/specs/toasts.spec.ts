import { expect, test } from '../fixtures/test';
import { shot } from '../fixtures/shots';

// A notice stands just above what is docked at the bottom. The decision
// under it can grow while the notice is up (resolving the last diff
// comment gives a verified card its landing answers back): the notice
// must move up with it rather than stand over the answers.

let id: string;
test.use({ seed: { run: async (ws) => { id = await ws.seedVerified('Add a noticed helper'); } } });

test('a notice follows the decision up when it grows', async ({ pairedPage: page }, info) => {
  test.skip(info.project.name === 'phone', 'the phone docks a folded decision bar');
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await page.getByTestId('tab-diff').click();
  await page.locator('[data-testid^="diff-line-"]').nth(3).locator('.n').click();
  await page.getByTestId('annotation-input').fill('Name it after what it does');
  await page.getByTestId('annotation-save').click();
  await expect(page.getByTestId('decision-option-diff')).toBeVisible();
  await expect(page.getByTestId('toast')).toHaveCount(0, { timeout: 10_000 });

  const dockTop = async () => (await page.locator('.dock').boundingBox())!.y;
  const before = await dockTop();
  await page.locator('[data-testid^="annotation-resolve-"]').first().click();
  await expect(page.getByTestId('decision-option-advance')).toBeVisible();
  const toast = page.getByTestId('toast').filter({ hasText: 'resolved' });
  await expect(toast).toBeVisible();
  // the dock grows with the answers, unless the decision already stood at
  // its cap (a short window): either way the notice stays above it
  expect(await dockTop()).toBeLessThanOrEqual(before);
  await expect.poll(async () => (await toast.boundingBox())!.y + (await toast.boundingBox())!.height).toBeLessThanOrEqual(await dockTop());
  await shot(page, info, 'toast-above-decision');

  // and a plain notice never takes a press meant for what is under it
  expect(await toast.evaluate((e) => getComputedStyle(e).pointerEvents)).toBe('none');
});
