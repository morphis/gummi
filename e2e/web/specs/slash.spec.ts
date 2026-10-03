import { expect, test } from '../fixtures/test';

let id = '';
test.use({ seed: { run: async (ws) => { id = await ws.seedDesignGate('Add a wave helper'); } } });

// A slash line in the composer is a command, not prose: the card's own
// vocabulary completes while the word is typed, and enter runs it — the
// verb itself when the card answers it, the menu (focused on the match,
// one enter from firing) when it is menu-only.
test('a slash command completes and runs', async ({ pairedPage: page, server, api }, info) => {
  test.setTimeout(120_000);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  if (info.project.name === 'phone') await page.getByTestId('tab-thread').click();

  const input = page.getByTestId('composer-input');
  const offer = page.getByTestId('composer-complete');

  // the bare slash offers the card's own words; a partly typed word narrows
  await input.click();
  await input.fill('/');
  await expect(offer).toBeVisible();
  await expect(offer.getByRole('option').first()).toContainText('/');
  await input.fill('/ver');
  await expect(offer.getByRole('option')).toHaveText([/\/verify\s*run verify checks/]);
  // a slash draft never reads as the pinned decision's answer — not while
  // the server is still classifying it, and not after
  await expect(page.getByTestId('composer-says')).not.toContainText('start the architect');
  await expect(page.getByTestId('composer-send')).toHaveText('Send');
  // tab takes it, ready for arguments, and the says line tells what enter
  // will do with it
  await page.keyboard.press('Tab');
  await expect(input).toHaveValue('/verify ');
  await expect(input).toBeFocused();
  await expect(offer).toBeHidden();
  await expect(page.getByTestId('composer-says')).toContainText('menu');

  // a menu-only command hands the line to the menu: the composer lets it
  // go, the menu opens on the entry the line named, and enter runs it
  const posts: string[] = [];
  page.on('request', (r) => { if (r.method() === 'POST' && /\/actions\/verify$/.test(r.url())) posts.push(r.url()) });
  await page.keyboard.press('Enter');
  const menu = page.getByTestId('card-actions-menu');
  await expect(menu).toBeVisible();
  await expect(input).toHaveValue('');
  await expect(menu.locator('button').first()).toContainText('verify');
  await page.keyboard.press('Enter');
  await expect.poll(() => posts).toHaveLength(1);
  await expect(menu).toBeHidden();
  // the action ran gummi's own path, which asks before it runs the
  // checks — the question the TUI's v key asks too
  await expect(page.getByTestId('action-dialog')).toBeVisible();
  await page.getByTestId('action-cancel').click();
  await expect(page.getByTestId('action-dialog')).toBeHidden();

  // a verb the card answers runs from the composer alone: approving the
  // design gate crosses into implement
  await input.click();
  await input.fill('/approve');
  await expect(page.getByTestId('composer-says')).toContainText('approve');
  await page.keyboard.press('Enter');
  await expect(input).toHaveValue('');
  await expect(page.getByTestId('stage-implement')).toHaveAttribute('aria-current', 'step', { timeout: 30_000 });
});

// A word naming nothing still opens the menu — and never strands the line
// in the composer.
test('a slash word naming nothing hands the line to the menu', async ({ pairedPage: page, server }, info) => {
  test.setTimeout(120_000);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  if (info.project.name === 'phone') await page.getByTestId('tab-thread').click();

  const input = page.getByTestId('composer-input');
  await input.click();
  await input.fill('/nosuch');
  await expect(page.getByTestId('composer-complete')).toBeHidden();
  await page.keyboard.press('Enter');
  await expect(page.getByTestId('card-actions-menu')).toBeVisible();
  await expect(input).toHaveValue('');
});
