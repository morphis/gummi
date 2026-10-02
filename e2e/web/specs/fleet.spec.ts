import { expect, test } from '../fixtures/test';
import type { Page } from '@playwright/test';
import { shot } from '../fixtures/shots';

// Fleet stats: the board's run over a window, fleetrun's fold. Seeded with
// a landed card (every stage ran, gates crossed, a landing mark), one
// parked at its design gate, and one whose architect asked a question (an
// open wait on you).

let ids: { landed: string; gate: string; ask: string };

test.describe.configure({ timeout: 180_000 });
test.use({
  seed: {
    run: async (ws) => {
      const landed = await ws.seedLanded('Add a wave helper');
      const gate = await ws.seedDesignGate('Add a nod helper');
      const ask = (await ws.seedAsk('Add a bow helper')).id;
      ids = { landed, gate, ask };
    },
  },
});

async function openFleet(page: Page, phone: boolean) {
  if (phone) await page.getByTestId('card-back').click();
  await page.getByTestId('rail-fleet').click();
  await expect(page.getByTestId('view-fleet')).toBeVisible();
}

test('the headline, the clock, the breakdowns and the timeline, to scale', async ({ pairedPage: page, api }, info) => {
  const phone = info.project.name === 'phone';
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  await openFleet(page, phone);
  await expect(page.getByTestId('fleet-window-7d')).toHaveAttribute('aria-pressed', 'true');

  const rep = (await api('GET', `/api/fleet?from=${encodeURIComponent(new Date(Date.now() - 7 * 864e5).toISOString())}`)).json;
  expect(rep.credits).toBeGreaterThan(0);
  await expect(page.getByTestId('fleet-spent')).toContainText(rep.credits.toFixed(1));
  await expect(page.getByTestId('fleet-alltime')).toContainText(`${rep.allTimeCards} card`);
  await expect(page.getByTestId('fleet-rework')).toContainText('corrected');
  await expect(page.getByTestId('fleet-clock-agent')).toBeVisible();
  await expect(page.getByTestId('fleet-clock-you')).toBeVisible();

  // by stage, in the stage order; by model, biggest first
  const stages = page.getByTestId('fleet-stage-bars').locator('th');
  await expect(stages.first()).toHaveText('plan');
  await expect(page.getByTestId('fleet-stage-bars')).toContainText('implement');
  await expect(page.getByTestId('fleet-model-bars')).toContainText('e2e-architect');

  // one lane per card; the landed one shows its landing and gates, the
  // asking one its open wait on you
  const landed = page.getByTestId(`fleet-lane-${ids.landed}`);
  const gate = page.getByTestId(`fleet-lane-${ids.ask}`);
  await expect(landed).toBeVisible();
  await expect(page.getByTestId(`fleet-lane-${ids.gate}`)).toBeVisible();
  await expect(gate).toBeVisible();
  await expect(landed.locator('.blk')).not.toHaveCount(0);
  await expect(landed.locator('.land')).toHaveCount(1);
  await expect(landed.locator('.gate')).not.toHaveCount(0);
  await expect(gate.locator('.wait.open')).toHaveCount(1);
  await expect(gate).toContainText('on you since');
  await expect(page.getByTestId('fleet-legend')).toContainText('waiting on you');
  // spans sit inside their track
  const track = await landed.locator('.track').boundingBox();
  const blk = await landed.locator('.blk').first().boundingBox();
  expect(blk!.x).toBeGreaterThanOrEqual(track!.x - 1);
  expect(blk!.x + blk!.width).toBeLessThanOrEqual(track!.x + track!.width + 3); // a 2px minimum at the edge

  if (!phone) {
    // every mark carries its numbers on hover: value first, then the name
    await page.getByTestId('fleet-stage-bars').locator('tr').first().hover();
    await expect(page.getByTestId('fleet-tip')).toBeVisible();
    await expect(page.getByTestId('fleet-tip')).toContainText(/cr.*plan$/);
    await landed.locator('.land').hover();
    await expect(page.getByTestId('fleet-tip')).toContainText('landed');
  } else {
    // the timeline scrolls sideways on a phone instead of squeezing
    const sc = page.getByTestId('fleet-timeline-scroll');
    const [sw, cw] = await sc.evaluate((el) => [el.scrollWidth, el.clientWidth]);
    expect(sw).toBeGreaterThan(cw);
  }
  await shot(page, info, 'fleet');
  await page.getByTestId('fleet-timeline').scrollIntoViewIfNeeded();
  await shot(page, info, 'fleet-timeline');

  // the window picker refetches: all time asks for from=all
  const all = page.waitForRequest((r) => r.url().includes('/api/fleet?from=all'));
  await page.getByTestId('fleet-window-all').click();
  await all;
  await expect(page.getByTestId('fleet-span')).toContainText('whole history');
  await expect(page.getByTestId(`fleet-lane-${ids.landed}`)).toBeVisible();
  await page.getByTestId('fleet-window-24h').click();
  await expect(page.getByTestId('fleet-window-24h')).toHaveAttribute('aria-pressed', 'true');
  await expect(page.getByTestId(`fleet-lane-${ids.gate}`)).toBeVisible();

  // a lane's id opens the card
  await page.getByTestId(`fleet-lane-${ids.gate}`).locator('.id').click();
  await expect(page.getByTestId('view-fleet')).toHaveCount(0);
  await expect(page.getByTestId('card-id')).toHaveText(ids.gate);
  expect(errors).toEqual([]);
});

test('reads in the dark theme too', async ({ pairedPage: page }, info) => {
  test.skip(info.project.name !== 'desktop', 'one theme screenshot is enough');
  await page.getByTestId('btn-theme').click();
  await openFleet(page, false);
  await expect(page.getByTestId(`fleet-lane-${ids.landed}`)).toBeVisible();
  await shot(page, info, 'fleet-dark');
});
