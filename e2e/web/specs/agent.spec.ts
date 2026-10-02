import { expect, test } from '../fixtures/test';
import type { Page } from '@playwright/test';
import { shot } from '../fixtures/shots';

// The board agent: open the board-level session under a chosen profile,
// talk to it (the scripted agent acknowledges each line), stop a turn in
// flight, switch profile through the page's own confirm, and /clear.

async function openAgent(page: Page, phone: boolean) {
  if (phone) await page.getByTestId('card-back').click();
  await page.getByTestId('rail-agent').click();
  await expect(page.getByTestId('view-agent')).toBeVisible();
}

test('open, send, reply, interrupt, switch profile, clear', async ({ pairedPage: page, api }, info) => {
  const phone = info.project.name === 'phone';
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  await openAgent(page, phone);

  // the opener offers the profiles profiles.yaml declares
  await expect(page.getByTestId('agent-opener')).toBeVisible();
  await expect(page.getByTestId('agent-open-profile').locator('option')).toHaveText(['e2e', 'e2e-alt']);
  await page.getByTestId('agent-open-profile').selectOption('e2e');
  await shot(page, info, 'agent-opener');
  await page.getByTestId('agent-open').click();
  await expect(page.getByTestId('agent-input')).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId('agent-profile')).toHaveValue('e2e');

  // a line and its reply
  const input = page.getByTestId('agent-input');
  await input.fill('What needs me first?');
  await input.press('Enter');
  const transcript = page.getByTestId('agent-transcript');
  await expect(transcript.locator('[data-type="you"]').last()).toContainText('What needs me first?');
  await expect(transcript.locator('[data-type="message"]').last()).toContainText('Noted: What needs me first?', { timeout: 30_000 });
  await expect(page.getByTestId('agent-busy')).toHaveCount(0);
  await shot(page, info, 'agent-reply');

  // a slow turn shows the busy line and stops on interrupt
  await input.fill('[slow] walk me through every card on the board');
  await input.press('Enter');
  await expect(page.getByTestId('agent-busy')).toBeVisible({ timeout: 15_000 });
  await expect(page.getByTestId('agent-interrupt')).toBeVisible();
  await shot(page, info, 'agent-busy');
  await page.getByTestId('agent-interrupt').click();
  await expect(page.getByTestId('agent-busy')).toHaveCount(0, { timeout: 15_000 });
  await expect(page.getByTestId('agent-interrupt')).toBeHidden();
  const after = await api('GET', '/api/agent');
  expect(after.json.live?.busy ?? false).toBe(false);

  // switching profile would end the conversation: the page asks first
  await page.getByTestId('agent-profile').selectOption('e2e-alt');
  const confirm = page.getByTestId('agent-confirm');
  await expect(confirm).toContainText('e2e-alt');
  await confirm.getByTestId('agent-confirm-no').click();
  await expect(confirm).toHaveCount(0);
  await expect(page.getByTestId('agent-profile')).toHaveValue('e2e');
  await page.getByTestId('agent-profile').selectOption('e2e-alt');
  await page.getByTestId('agent-confirm-yes').click();
  await expect(page.getByTestId('agent-profile')).toHaveValue('e2e-alt', { timeout: 30_000 });
  await expect(transcript.locator('[data-type="you"]')).toHaveCount(0);

  // a fresh conversation, then /clear empties it
  await input.fill('hello again');
  await input.press('Enter');
  await expect(transcript.locator('[data-type="message"]').last()).toContainText('Noted: hello again', { timeout: 30_000 });
  await input.fill('/clear');
  await input.press('Enter');
  await expect(transcript.locator('[data-type="you"]')).toHaveCount(0, { timeout: 30_000 });
  expect(errors).toEqual([]);
});
