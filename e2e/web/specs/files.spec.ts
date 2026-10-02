import * as fs from 'node:fs';
import * as path from 'node:path';
import { expect, test } from '../fixtures/test';

// An agent names a file it wrote by its absolute path, as in "open
// <worktree>/mockup.html directly in a browser". In a card's thread that
// path links to the server's copy of the file, which opens in a tab of its
// own: sandboxed, so the agent's page runs its script and loads its
// stylesheet beside it, but is a page of no origin that cannot act on the
// board as the person looking at it.

test.beforeEach(({}, info) => {
  test.skip(info.project.name !== 'desktop', 'the renderer and the route are the same on every viewport');
});

test('a worktree path in a message opens the file, sandboxed', async ({ pairedPage: page, server, api }) => {
  test.setTimeout(90_000);
  const id = String((await api('POST', '/api/cards', { kind: 'freeform', title: 'Mock up the cards' })).json?.id);
  await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.files?.url, { timeout: 30_000 }).toBeTruthy();
  const files = (await api('GET', `/api/cards/${id}`)).json.files as { dir: string; url: string };

  fs.mkdirSync(path.join(files.dir, 'mock up'), { recursive: true });
  fs.writeFileSync(path.join(files.dir, 'mock up', 'cards.css'), 'h1 { color: rgb(1, 2, 3); }\n');
  fs.writeFileSync(path.join(files.dir, 'mock up', 'cards.js'), [
    "document.getElementById('ran').textContent = 'script ran';",
    "let cookie = 'readable';",
    "try { void document.cookie } catch { cookie = 'blocked' }",
    "document.getElementById('cookie').textContent = cookie;",
    "fetch('/api/cards').then((r) => r.status, () => 'refused').then((s) => { document.getElementById('api').textContent = String(s) });",
  ].join('\n'));
  fs.writeFileSync(path.join(files.dir, 'mock up', 'cards.html'), [
    '<!doctype html><link rel="stylesheet" href="cards.css">',
    '<h1>Multi-repo cards</h1><p id="ran"></p><p id="cookie"></p><p id="api"></p>',
    '<script src="cards.js"></script>',
  ].join('\n'));

  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  const full = `${files.dir}/mock up/cards.html`;
  const hrefs = await page.evaluate(async ({ files, full }) => {
    const { markdown } = await import('/assets/markdown.js');
    const el = markdown([
      `See ${files.dir}/notes.md:12.`,
      `Open \`${full}\` directly in a browser, or [the mockup](${files.dir}/mock%20up/cards.html).`,
      'Not mine: /etc/passwd, and `/etc/hosts`.',
    ].join('\n\n'), { files });
    el.id = 'files-probe';
    document.getElementById('files-probe')?.remove();
    document.body.append(el);
    return [...el.querySelectorAll('a')].map((a) => a.getAttribute('href'));
  }, { files, full });
  // plain text, a code span and a link each link; the line number and the
  // sentence's full stop are not part of the path; a path outside is text.
  // Plain text ends a path at a space, so only the other two may hold one.
  expect(hrefs).toEqual([
    `${files.url}notes.md`,
    `${files.url}mock%20up/cards.html`,
    `${files.url}mock%20up/cards.html`,
  ]);

  const link = page.locator('#files-probe a', { hasText: full });
  await expect(link).toHaveAttribute('target', '_blank');
  const [tab] = await Promise.all([page.waitForEvent('popup'), link.click()]);
  await expect(tab.locator('h1')).toHaveText('Multi-repo cards');
  await expect(tab.locator('h1')).toHaveCSS('color', 'rgb(1, 2, 3)');
  await expect(tab.locator('#ran')).toHaveText('script ran');
  // no origin: no cookie to read, and the board's API does not answer it
  await expect(tab.locator('#cookie')).toHaveText('blocked');
  await expect(tab.locator('#api')).not.toHaveText('');
  await expect(tab.locator('#api')).not.toHaveText('200');
});
