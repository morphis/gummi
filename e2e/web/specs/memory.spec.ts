import { expect, test } from '../fixtures/test';
import fs from 'node:fs';
import path from 'node:path';

// A freeform card's project memory reads on the page the way a workflow
// card's spec does: the workspace's global memory and the card's own
// plan and dead-ends, as the files stand — the documents its session
// reads at spawn, the card's own two filled as it works.
test('a freeform card shows its memory documents', async ({ pairedPage: page, server, api, workspace }, info) => {
  test.setTimeout(90_000);
  const id = String((await api('POST', '/api/cards', { kind: 'freeform', title: 'Memory on the page' })).json?.id);
  const mem = path.join(workspace.repo, '.gummi', 'memory');
  fs.mkdirSync(path.join(mem, id), { recursive: true });
  fs.writeFileSync(path.join(mem, 'global.md'), "The repo's checks are `make ci`.\n");
  fs.writeFileSync(path.join(mem, id, 'plan.md'), '## Plan\n- split the parser\n');
  fs.writeFileSync(path.join(mem, id, 'dead-ends.md'), 'attempt 1 failed\n');

  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await page.getByTestId('tab-memory').click();
  await expect(page.getByTestId('memory')).toContainText('.gummi/memory', { timeout: 30_000 });
  await expect(page.getByTestId('memory-global')).toContainText('make ci');
  await expect(page.getByTestId('memory-plan')).toContainText('split the parser');
  await expect(page.getByTestId('memory-dead-ends')).toContainText('attempt 1 failed');
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
