import { expect, showTab, test } from '../fixtures/test';
import fs from 'node:fs';
import path from 'node:path';

// A freeform card's project memory reads on the page the way a workflow
// card's spec does: the workspace's global memory and the card's own
// memory and dead-ends, as the files stand — the documents its session
// reads at spawn, the card's own two filled as it works. Each document
// folds to a header row: the card's own is open by default, the other
// two open on the desktop and collapsed on the phone, and a reader's
// open/closed choice persists in the browser.
test('a freeform card shows its memory documents', async ({ pairedPage: page, server, api, workspace }, info) => {
  test.setTimeout(90_000);
  const phone = info.project.name === 'phone';
  const id = String((await api('POST', '/api/cards', { kind: 'freeform', title: 'Memory on the page' })).json?.id);
  const mem = path.join(workspace.repo, '.gummi', 'memory');
  fs.mkdirSync(path.join(mem, id), { recursive: true });
  fs.writeFileSync(path.join(mem, 'global.md'), "The repo's checks are `make ci`.\n");
  fs.writeFileSync(path.join(mem, id, 'memory.md'), '## Plan\n- split the parser\n');
  fs.writeFileSync(path.join(mem, id, 'dead-ends.md'), 'attempt 1 failed\n');

  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await showTab(page, 'memory');
  await expect(page.getByTestId('memory')).toContainText('.gummi/memory', { timeout: 30_000 });

  const doc = (which) => page.getByTestId(`memory-${which}`);
  const body = (which) => doc(which).locator('.md').first();

  // each document renders its workspace-relative path under its fold's
  // header, whatever the fold state
  await expect(doc('global')).toContainText('.gummi/memory/global.md');
  await expect(doc('memory')).toContainText(`.gummi/memory/${id}/memory.md`);
  await expect(doc('dead-ends')).toContainText(`.gummi/memory/${id}/dead-ends.md`);

  // defaults: the card's own memory open everywhere, the other two
  // open on the desktop and collapsed on the phone
  await expect(doc('memory')).toHaveAttribute('open', /.*/);
  for (const which of ['global', 'dead-ends']) {
    if (phone) await expect(doc(which)).not.toHaveAttribute('open', /.*/);
    else await expect(doc(which)).toHaveAttribute('open', /.*/);
  }

  // content sits intact behind each fold, and a collapsed one expands
  await expect(body('memory')).toContainText('split the parser');
  for (const which of ['global', 'dead-ends']) {
    if (phone) await doc(which).locator('summary').click();
    await expect(body(which)).toBeVisible();
  }
  await expect(body('global')).toContainText('make ci');
  await expect(body('dead-ends')).toContainText('attempt 1 failed');

  // the choice persists across visits: collapse global, reload, and it
  // is the one document closed
  await doc('global').locator('summary').click();
  await page.reload();
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await showTab(page, 'memory');
  await expect(page.getByTestId('memory')).toContainText('.gummi/memory', { timeout: 30_000 });
  await expect(doc('global')).not.toHaveAttribute('open', /.*/);
  await expect(doc('memory')).toHaveAttribute('open', /.*/);
  await expect(doc('dead-ends')).toHaveAttribute('open', /.*/);
});

// Nothing written yet reads as a placeholder, not as missing; and a
// workflow card — whose document is its spec — shows no memory tab at
// all, the mirror of a session's missing spec tab.
test('a workflow card shows no memory tab', async ({ pairedPage: page, server, api }, info) => {
  test.setTimeout(90_000);
  const made = await api('POST', '/api/cards', { kind: 'feature', title: 'Row cache' });
  const id = String(made.json?.id);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await expect(page.getByTestId('tab-memory')).toHaveCount(0);
  const mem = (await api('GET', `/api/cards/${id}/memory`)).json;
  expect(mem.none).toBe(true);
});
// The Memory tab is a tab like the others: it survives moving to another
// session (the next card's head still in flight says nothing about which
// tabs it has), and a reload or a link naming it opens it.
test('the memory tab sticks across sessions and reloads', async ({ pairedPage: page, server, api }, info) => {
  test.setTimeout(90_000);
  const a = String((await api('POST', '/api/cards', { kind: 'freeform', title: 'First session' })).json?.id);
  const b = String((await api('POST', '/api/cards', { kind: 'freeform', title: 'Second session' })).json?.id);
  await page.goto(`${server.url}/#${a}`);
  await expect(page.getByTestId('card-id')).toHaveText(a);
  await showTab(page, 'memory');
  await expect(page).toHaveURL(new RegExp(`#${a}/memory$`));
  if (info.project.name === 'phone') {
    await page.goto(`${server.url}/#${b}/memory`);
  } else {
    await page.getByTestId(`rail-row-${b}`).click();
  }
  await expect(page.getByTestId('card-id')).toHaveText(b);
  await expect(page.getByTestId('tab-memory')).toHaveAttribute('aria-selected', 'true');
  await expect(page).toHaveURL(new RegExp(`#${b}/memory$`));
  await page.reload();
  await expect(page.getByTestId('card-id')).toHaveText(b);
  await expect(page.getByTestId('tab-memory')).toHaveAttribute('aria-selected', 'true');
  await expect(page).toHaveURL(new RegExp(`#${b}/memory$`));
});
