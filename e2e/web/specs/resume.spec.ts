import { expect, test } from '../fixtures/test';
import type { Page } from '@playwright/test';
import { shot } from '../fixtures/shots';

// The quit-resume question against a real `gummi web`: closing the host
// with an autopilot card running stops it and marks where, and the next
// host asks — from a banner above the board — whether to pick it back up.
// Nothing restarts until someone answers.

test.use({ workspaceEnv: { GUMMI_E2E_SLOW_SECONDS: '60' } });

async function runningOnAutopilot(api: any, title: string): Promise<string> {
  const c = (await api('POST', '/api/cards', { kind: 'feature', title, autopilot: true })).json;
  await expect.poll(async () => (await api('GET', `/api/cards/${c.id}`)).json.status).toBe('running');
  return c.id;
}

async function reopen(page: Page, server: any) {
  await server.restart();
  await page.reload();
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
}

test('not now leaves the stopped cards parked', async ({ pairedPage: page, server, api }, info) => {
  const id = await runningOnAutopilot(api, '[slow] Add a lazy helper');
  await reopen(page, server);
  const banner = page.getByTestId('resume-banner');
  await expect(banner).toBeVisible();
  await expect(page.getByTestId('resume-text')).toContainText(id);
  expect((await api('GET', '/api/board')).json.resume.cards.map((c: any) => c.id)).toEqual([id]);
  await shot(page, info, 'resume-offer');
  await page.getByTestId('resume-none').click();
  await expect(banner).toHaveCount(0);
  expect((await api('GET', '/api/board')).json.resume).toBeUndefined();
  expect((await api('GET', `/api/cards/${id}`)).json.status).not.toBe('running');
});

// The banner says how long ago the board quit, counted from the quit
// itself, and keeps saying it while it stands rather than freezing on
// the age it had when the page drew it.
test('how long ago the board quit stays current', async ({ pairedPage: page, server, api }) => {
  const id = await runningOnAutopilot(api, '[slow] Add a lazy helper');
  await server.restart();
  await page.clock.install();
  await page.reload();
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  const offer = (await api('GET', '/api/board')).json.resume;
  expect(offer.cards.map((c: any) => c.id)).toEqual([id]);
  expect(Date.parse(offer.at)).toBeLessThanOrEqual(Date.now());
  const ago = page.getByTestId('resume-ago');
  await expect(ago).toHaveText(' (moments ago)');
  await page.clock.fastForward('05:00');
  await expect(ago).toHaveText(' (5m ago)');
  await page.getByTestId('resume-none').click();
});

test('resume picks the chosen cards back up', async ({ pairedPage: page, server, api }, info) => {
  const a = await runningOnAutopilot(api, '[slow] Add a lazy helper');
  const b = await runningOnAutopilot(api, '[slow] Add a sleepy helper');
  await reopen(page, server);
  await expect(page.getByTestId('resume-banner')).toBeVisible();
  await page.getByTestId('resume-choose').click();
  await page.getByTestId(`resume-card-${b}`).uncheck();
  // a board event redraws the banner: what was unticked stays unticked
  const other = (await api('POST', '/api/cards', { kind: 'feature', title: 'Add a tick helper' })).json.id;
  await expect(page.getByTestId(`rail-row-${other}`)).toHaveCount(1);
  await expect(page.getByTestId(`resume-card-${b}`)).not.toBeChecked();
  await expect(page.getByTestId(`resume-card-${a}`)).toBeChecked();
  await shot(page, info, 'resume-choose');
  await page.getByTestId('resume-picked').click();
  await expect(page.getByTestId('resume-banner')).toHaveCount(0);
  await expect.poll(async () => (await api('GET', `/api/cards/${a}`)).json.status).toBe('running');
  expect((await api('GET', `/api/cards/${b}`)).json.status).not.toBe('running');
});

// A card started by hand is stopped by the quit just the same; the banner
// names it for what it was doing, not as autopilot's.
test('a card started by hand is not called autopilot’s', async ({ pairedPage: page, server, api }) => {
  const c = (await api('POST', '/api/cards', { kind: 'feature', title: '[slow] Add a handmade helper' })).json;
  const card = (await api('POST', `/api/cards/${c.id}/answer`, { ref: c.decision.ref, option: 'advance', against: c.decision.against.token })).json;
  await api('POST', `/api/cards/${c.id}/answer`, { ref: card.decision.ref, option: 'run', against: card.decision.against.token });
  await expect.poll(async () => (await api('GET', `/api/cards/${c.id}`)).json.status).toBe('running');
  await reopen(page, server);
  await expect(page.getByTestId('resume-text')).toContainText(c.id);
  await expect(page.getByTestId('resume-text')).toContainText('mid-stage');
  await expect(page.getByTestId('resume-text')).not.toContainText('autopilot');
  await page.getByTestId('resume-none').click();
});
