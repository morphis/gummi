import type { Page } from '@playwright/test';
import { expect, test } from '../fixtures/test';
import { shot } from '../fixtures/shots';

// The schedules view against a real `gummi web`: define a heartbeat
// against a session, see it listed off, enable it through the confirm,
// fire it now, and delete it — with a mint schedule seeded from the CLI
// so both kinds are on screen. A definition that cannot fire (a mint
// naming a repository the board does not configure) is refused with the
// sentence the board gave.

test.use({
  seed: {
    run: async (ws) => {
      // a mint schedule, defined the way the CLI does it, stored off
      await ws.gummiOK(['schedule', 'add', '--name', 'nightly triage', '--every', '1h', '--prompt', 'triage new issues', '--envelope', '50']);
    },
  },
});

async function openSchedules(page: Page) {
  await page.getByTestId('rail-more').click();
  await page.getByTestId('menu-schedules').click();
  await expect(page.getByTestId('view-schedules')).toBeVisible();
}

test('a schedule is defined, enabled through its confirm, fired and deleted', async ({ pairedPage: page, api, workspace }, info) => {
  test.setTimeout(120_000);
  await openSchedules(page);

  // the seeded definition lists, off
  const seeded = page.getByTestId('schedule-nightly-triage');
  await expect(seeded).toBeVisible();
  await expect(seeded).toHaveAttribute('data-enabled', 'false');
  await expect(seeded.getByTestId('schedule-nightly-triage-state')).toHaveText('off');
  await expect(page.getByTestId('schedules-count')).toHaveText('1 schedule');

  // the session a heartbeat will target: minted through the board like
  // any freeform card
  const made = await api('POST', '/api/cards', { kind: 'freeform', title: 'Tidy the README' });
  expect(made.ok, made.text).toBe(true);
  const ff = (made.json?.id ?? made.json?.card?.id) as string;
  expect(ff).toMatch(/^FF-/);

  // the add form: a heartbeat against that session
  await page.getByTestId('schedules-new').click();
  const form = page.getByTestId('schedule-form');
  await expect(form).toBeVisible();
  await page.getByTestId('schedule-form-name').fill('keep tidy');
  await page.getByTestId('schedule-form-kind-heartbeat').click();
  await page.getByTestId('schedule-form-target').fill(ff);
  await page.getByTestId('schedule-form-every').fill('15m');
  await page.getByTestId('schedule-form-prompt').fill('check the README, keep going');
  await page.getByTestId('schedule-form-submit').click();
  const row = page.getByTestId('schedule-keep-tidy');
  await expect(row).toBeVisible();
  await expect(row).toHaveAttribute('data-enabled', 'false');
  await expect(page.getByTestId('schedules-count')).toHaveText('2 schedules');

  // enabling asks, because it is the switch that starts spending
  await row.getByTestId('schedule-keep-tidy-toggle').click();
  await expect(page.getByTestId('schedule-keep-tidy-confirm')).toBeVisible();
  await page.getByTestId('schedule-keep-tidy-confirm-yes').click();
  await expect(page.getByTestId('schedule-keep-tidy-state')).toHaveText('on');
  await expect(page.getByTestId('schedule-keep-tidy-next')).toBeVisible();

  // turning it off again does not ask: it is the safe direction
  await row.getByTestId('schedule-keep-tidy-toggle').click();
  await expect(page.getByTestId('schedule-keep-tidy-state')).toHaveText('off');
  await expect(page.getByTestId('schedule-keep-tidy-confirm')).toHaveCount(0);
  // and the "enabled — next fire" line goes with it
  await expect(page.getByTestId('schedule-keep-tidy-result')).toHaveCount(0);

  // a run-now answers with the outcome: the turn went to the session
  await row.getByTestId('schedule-keep-tidy-run').click();
  await expect(page.getByTestId('schedule-keep-tidy-confirm')).toBeVisible();
  await page.getByTestId('schedule-keep-tidy-confirm-yes').click();
  await expect(page.getByTestId('schedule-keep-tidy-result')).toContainText('sent its turn');
  // the status reads in words, never the server's slug
  await expect(page.getByTestId('schedule-keep-tidy-status')).toHaveText('fired');

  // delete removes the row; the session it heartbeat stays on the board
  await row.getByTestId('schedule-keep-tidy-rm').click();
  await expect(page.getByTestId('schedule-keep-tidy-confirm')).toBeVisible();
  await page.getByTestId('schedule-keep-tidy-confirm-yes').click();
  await expect(page.getByTestId('schedule-keep-tidy')).toHaveCount(0);
  await expect(seeded).toBeVisible();

  // a mint naming a repository the board does not configure is refused
  await page.getByTestId('schedules-new').click();
  await page.getByTestId('schedule-form-name').fill('bad repo');
  await page.getByTestId('schedule-form-repo').fill('nope');
  await page.getByTestId('schedule-form-prompt').fill('triage new issues');
  await page.getByTestId('schedule-form-submit').click();
  await expect(page.getByTestId('schedule-form-error')).toContainText(/not configured/i);
  await expect(page.getByTestId('schedule-bad-repo')).toHaveCount(0);

  // a heartbeat on a session that is not on the board is refused at the
  // form, not turned off by its first fire
  await page.getByTestId('schedule-form-name').fill('ghost');
  await page.getByTestId('schedule-form-kind-heartbeat').click();
  await page.getByTestId('schedule-form-target').fill('FF-999');
  await page.getByTestId('schedule-form-every').fill('15m');
  await page.getByTestId('schedule-form-submit').click();
  await expect(page.getByTestId('schedule-form-error')).toContainText('FF-999 is not a card on this board');
  await expect(page.getByTestId('schedule-ghost')).toHaveCount(0);

  await shot(page, info, 'schedules');
});

// The form answers while it is filled: the cadence is previewed against
// the board (the page parses no cron of its own), the agent and model
// are picked from the session picker's catalog rather than typed blind,
// the timezone is a shortlist plus free text, and the same form edits an
// existing row — which stays off across the save.
test('the schedule form previews its cadence, picks a pair from the catalog, and edits', async ({ pairedPage: page, api }, info) => {
  test.setTimeout(120_000);
  const mobile = info.project.name === 'phone';
  await openSchedules(page, mobile);

  await page.getByTestId('schedules-new').click();
  const form = page.getByTestId('schedule-form');
  await expect(form).toBeVisible();

  // the catalog fills the agent picker; headless is installed here (the
  // workspace runs the scripted agent), so its row carries no refusal
  const backend = page.getByTestId('schedule-form-backend');
  await expect(backend).toContainText('headless');

  // the cadence preview: the default preset compiles and shows when the
  // schedule would fire, before anything is stored
  await expect(page.getByTestId('schedule-form-preview-cron')).toContainText('0 * * * *');
  await expect(page.getByTestId('schedule-form-preview-fires')).toBeVisible();

  // an expression that can never fire is refused in the form, not at
  // the first fire
  await page.getByTestId('schedule-form-cron').fill('0 0 30 2 *');
  await expect(page.getByTestId('schedule-form-preview-error')).toContainText(/never/i);

  // a nonsense timezone is refused; a real one previews in its own wall
  // clock
  await page.getByTestId('schedule-form-cron').fill('');
  await page.getByTestId('schedule-form-tz').fill('Mars/Olympus');
  await expect(page.getByTestId('schedule-form-preview-error')).toContainText(/timezone/i);
  await page.getByTestId('schedule-form-tz').fill('Europe/Berlin');
  await expect(page.getByTestId('schedule-form-preview-fires')).toBeVisible();

  // picking the pair from the catalog: headless' models are the ids this
  // workspace's profiles run
  await backend.selectOption('headless');
  await page.getByTestId('schedule-form-model').fill('e2e-implementer');
  await expect(page.getByTestId('schedule-form-envelope-hint')).toContainText(/envelope/i);

  await page.getByTestId('schedule-form-name').fill('previewed mint');
  await page.getByTestId('schedule-form-prompt').fill('triage new issues');
  await page.getByTestId('schedule-form-submit').click();
  const row = page.getByTestId('schedule-previewed-mint');
  await expect(row).toBeVisible();
  await expect(row).toHaveAttribute('data-enabled', 'false');

  // the same form, opened on the row, prefilled; the saved edit leaves
  // the row off — a cadence the person has not re-approved must not fire
  await row.getByTestId('schedule-previewed-mint-edit').click();
  await expect(form).toBeVisible();
  await expect(page.getByTestId('schedule-form-name')).toHaveValue('previewed mint');
  await expect(page.getByTestId('schedule-form-cron')).toHaveValue('0 * * * *');
  await expect(page.getByTestId('schedule-form-kind-fixed')).toContainText('mint');
  await page.getByTestId('schedule-form-cron').fill('30 5 * * *');
  await page.getByTestId('schedule-form-submit').click();
  await expect(row).toHaveAttribute('data-enabled', 'false');
  await expect(row.locator('.sch-cron')).toHaveText('30 5 * * *');

  await shot(page, info, 'schedules-form');
});

// A session's own menu carries the heartbeat: it opens the Schedules view
// with the form already aimed at that card, and saving it switches it on
// (the Schedules view's own add leaves a definition off).
test('a session\'s menu opens a heartbeat aimed at it, and saving turns it on', async ({ pairedPage: page, server, api }, info) => {
  test.setTimeout(120_000);
  const made = await api('POST', '/api/cards', { kind: 'freeform', description: 'Poke at the rounding', backend: 'headless', model: 'e2e-implementer' });
  const id = String(made.json?.id);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await expect(page.getByTestId('composer-says')).not.toContainText('stop this turn', { timeout: 30_000 });

  await page.getByTestId('card-actions').click();
  await page.getByTestId('action-heartbeat').click();
  await expect(page.getByTestId('view-schedules')).toBeVisible();

  // the form is open, a heartbeat, aimed at this card, named after it
  await expect(page.getByTestId('schedule-form')).toBeVisible();
  await expect(page.getByTestId('schedule-form-target')).toHaveValue(id);
  await expect(page.getByTestId('schedule-form-name')).toHaveValue(`${id.toLowerCase()} heartbeat`);
  await expect(page.getByTestId('schedule-form-every')).toHaveValue('1h');
  await page.getByTestId('schedule-form-prompt').fill('check the rounding, keep going');
  await page.getByTestId('schedule-form-submit').click();

  const row = page.getByTestId(`schedule-${id.toLowerCase()}-heartbeat`);
  await expect(row).toBeVisible();
  await expect(row).toHaveAttribute('data-enabled', 'true');
  await expect(page.getByTestId(`schedule-${id.toLowerCase()}-heartbeat-state`)).toHaveText('on');
  await shot(page, info, 'schedules-card-heartbeat');
});
