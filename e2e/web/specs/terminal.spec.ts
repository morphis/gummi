import * as fs from 'node:fs';
import * as path from 'node:path';
import { expect, test, type BoundApi } from '../fixtures/test';
import type { Page } from '@playwright/test';

// `gummi web --terminal` gives a card a Terminal tab: a shell in its
// worktree, drawn by a vendored xterm.js. The page's CSP allows no inline
// style, which is how xterm.js sizes its rows and colours its cells, so
// the vendored copy is patched (scripts/vendor-xterm.sh); these tests are
// what says the patched copy still draws, and breaks no rule doing it.

const desktopOnly = () => test.skip(test.info().project.name !== 'desktop', 'the shell and its socket are the same on every viewport');

async function sessionCard(api: BoundApi): Promise<{ id: string; dir: string }> {
  const id = String((await api('POST', '/api/cards', { kind: 'freeform', title: 'Poke around' })).json?.id);
  await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.files?.dir, { timeout: 30_000 }).toBeTruthy();
  return { id, dir: (await api('GET', `/api/cards/${id}`)).json.files.dir };
}

const rows = (page: Page) => page.locator('.xterm-rows');

test.describe('a board served with --terminal', () => {
  test.use({ serverArgs: ['--terminal'], workspaceEnv: { SHELL: '/bin/sh' } });

  test('the Terminal tab is a shell in the card’s worktree', async ({ pairedPage: page, server, api }) => {
    desktopOnly();
    test.setTimeout(90_000);
    const refused: string[] = [];
    page.on('console', (m) => { if (/Content Security Policy|Refused to/i.test(m.text())) refused.push(m.text()); });

    const { id, dir } = await sessionCard(api);
    fs.writeFileSync(path.join(dir, 'marker-quokka.txt'), 'here\n');

    await page.goto(`${server.url}/#${id}`);
    await expect(page.getByTestId('card-id')).toHaveText(id);
    await page.getByTestId('tab-terminal').click();
    const term = page.getByTestId('terminal');
    await expect(term).toHaveAttribute('data-state', 'open');
    // the key bar is a touch screen's
    await expect(page.getByTestId('terminal-keys')).toHaveCount(0);

    // it opens with the keyboard, and what is typed runs in the worktree
    await page.keyboard.type('ls marker-*; echo sum-$((6*7))\n');
    await expect(rows(page)).toContainText('marker-quokka.txt');
    await expect(rows(page)).toContainText('sum-42');

    // a cell with a colour of its own is coloured, and the rows are laid
    // out by the generated rules: both are what the CSP would have dropped
    await page.keyboard.type("printf '\\033[38;2;1;2;3mtinted\\033[0m\\n'\n");
    const tinted = rows(page).locator('span', { hasText: /^tinted$/ }).first();
    await expect(tinted).toHaveCSS('color', 'rgb(1, 2, 3)');
    expect(await page.evaluate(() => document.adoptedStyleSheets.length)).toBeGreaterThanOrEqual(3);
    expect(await rows(page).evaluate((el) => getComputedStyle(el.firstElementChild as Element).height)).not.toBe('auto');

    // the pane is redrawn when the card changes; the keyboard stays put
    await page.evaluate(async () => { (await import('/assets/store.js' + new URL((document.querySelector('script[type=module]') as HTMLScriptElement).src).search)).set({ cardRev: Date.now() }); });
    await page.keyboard.type('echo kept-$((1+1))\n');
    await expect(rows(page)).toContainText('kept-2');

    // the shell is the card's: leaving the tab and coming back finds it
    await page.getByTestId('tab-diff').click();
    await expect(term).toHaveCount(0);
    await page.getByTestId('tab-terminal').click();
    await expect(term).toHaveAttribute('data-state', 'open');
    await expect(rows(page)).toContainText('sum-42');

    // exit ends it, and says so
    await page.getByTestId('terminal-screen').click();
    await page.keyboard.type('exit\n');
    await expect(page.getByTestId('terminal-note')).toContainText('The shell exited');
    await page.getByTestId('terminal-again').click();
    await expect(term).toHaveAttribute('data-state', 'open');

    expect(refused).toEqual([]);
    expect(server.log).toContain(`opened a terminal in ${id}`);
  });
});

// A phone's keyboard has no esc, tab, ctrl or arrows: the bar under the
// shell has them. bash, because these are its line editor's keys.
test.describe('a phone at a board served with --terminal', () => {
  test.use({ serverArgs: ['--terminal'], workspaceEnv: { SHELL: '/bin/bash' } });

  test('the key bar types what the phone’s keyboard cannot', async ({ pairedPage: page, server, api }) => {
    test.skip(test.info().project.name !== 'phone', 'the key bar is a touch screen’s');
    test.setTimeout(90_000);
    const { id, dir } = await sessionCard(api);
    fs.writeFileSync(path.join(dir, 'marker-quokka.txt'), 'here\n');

    await page.goto(`${server.url}/#${id}`);
    await page.getByTestId('tab-terminal').tap();
    const term = page.getByTestId('terminal');
    await expect(term).toHaveAttribute('data-state', 'open');
    const key = (name: string) => page.getByTestId(`terminal-key-${name}`);
    const typing = () => page.evaluate(() => document.activeElement?.classList.contains('xterm-helper-textarea'));

    // the shell and the bar fit the screen, and the shell is wide enough to use
    const view = page.viewportSize()!;
    const bar = (await page.getByTestId('terminal-keys').boundingBox())!;
    expect(bar.y + bar.height).toBeLessThanOrEqual(view.height);
    expect(bar.width).toBeLessThanOrEqual(view.width);
    const cols = await page.evaluate(() => document.querySelector('.xterm-rows')!.firstElementChild!.getBoundingClientRect().width);
    expect(cols).toBeGreaterThan(300);

    // a tap on the shell brings the keyboard; a key on the bar keeps it
    await page.getByTestId('terminal-screen').tap();
    expect(await typing()).toBe(true);
    await page.keyboard.type('echo sum-$((6*7))\n');
    await expect(rows(page)).toContainText('sum-42');

    // tab completes
    await page.keyboard.type('cat marker-q');
    await key('tab').tap();
    await expect(rows(page)).toContainText('cat marker-quokka.txt');
    expect(await typing()).toBe(true);
    await page.keyboard.press('Enter');
    await expect(rows(page)).toContainText('here');

    // left moves the cursor in the line
    await page.keyboard.type('echo left-ac');
    await key('left').tap();
    await page.keyboard.type('b\n');
    await expect(rows(page)).toContainText('left-abc');

    // up recalls the line before
    await key('up').tap();
    await page.keyboard.type('-again\n');
    await expect(rows(page)).toContainText('left-abc-again');

    // ctrl holds for one key: ctrl c interrupts, and is then released
    await page.keyboard.type('sleep 300\n');
    await key('ctrl').tap();
    await expect(key('ctrl')).toHaveAttribute('aria-pressed', 'true');
    await page.keyboard.type('c');
    await expect(key('ctrl')).toHaveAttribute('aria-pressed', 'false');
    await page.keyboard.type('echo free-$((2+3))\n');
    await expect(rows(page)).toContainText('free-5', { timeout: 10_000 });

    // alt sends esc first: alt b is the line editor's "back a word"
    await page.keyboard.type('echo alt-yz');
    await key('alt').tap();
    await page.keyboard.type('b');
    await page.keyboard.type('x\n');
    await expect(rows(page)).toContainText('alt-xyz');

    // the far keys are reached by sliding the row
    await key('tilde').scrollIntoViewIfNeeded();
    await page.keyboard.type('echo pipe');
    await key('pipe').tap();
    await page.keyboard.type('tr a-z A-Z\n');
    await expect(rows(page)).toContainText('PIPE');
    await page.screenshot({ path: test.info().outputPath('terminal-phone.png') });
  });
});

test('a board served without --terminal has no Terminal tab', async ({ pairedPage: page, server, api }) => {
  desktopOnly();
  const { id } = await sessionCard(api);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await expect(page.getByTestId('tab-diff')).toBeVisible();
  await expect(page.getByTestId('tab-terminal')).toHaveCount(0);
});
