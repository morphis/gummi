import { expect, test } from '../fixtures/test';

// An objective keeps a session going on its own (DESIGN §19.11). The e2e
// agent never answers an audit with a verdict, which counts as STUCK, so
// the objective settles stuck after three turns gummi sent itself: the
// strip says so, the rail marks the row, and the strip's clear removes it.
test('a session runs an objective set from the composer, and the strip clears it', async ({ pairedPage: page, server, api }, info) => {
  test.setTimeout(120_000);
  const made = await api('POST', '/api/cards', { kind: 'freeform', title: 'Objective' });
  const id = String(made.json?.id);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  if (info.project.name === 'phone') await page.getByTestId('tab-thread').click();
  const says = page.getByTestId('composer-says');
  await expect(says).not.toContainText('stop this turn', { timeout: 30_000 });

  const input = page.getByTestId('composer-input');
  await input.click();
  await input.fill('/obj');
  await expect(page.getByTestId('composer-complete')).toContainText('/objective');
  await input.fill('/objective the parser is tidy');
  await page.keyboard.press('Enter');
  await expect(input).toHaveValue('');

  const strip = page.getByTestId('objective');
  await expect(strip).toContainText('the parser is tidy');
  await expect(page.getByTestId('objective-state')).toContainText('stuck', { timeout: 60_000 });
  await expect(page.getByTestId('objective-note')).toContainText('no verdict');
  await expect(page.getByTestId('thread')).toContainText('Keep working toward the objective');
  if (info.project.name !== 'phone') await expect(page.getByTestId(`rail-row-objective-${id}`)).toContainText('stuck');
  await page.screenshot({ path: `screens/objective-${info.project.name}.png` });

  const live = (await api('GET', `/api/cards/${id}/live`)).json;
  expect(live.freeform?.objective).toMatchObject({ state: 'stuck', turns: 2, cap: 20 });
  expect(live.freeform?.turns?.some((t: { by?: string }) => t.by === 'gummi')).toBe(true);

  await page.getByTestId('objective-clear').click();
  await expect(strip).toHaveCount(0, { timeout: 10_000 });
});
