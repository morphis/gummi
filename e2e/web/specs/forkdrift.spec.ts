import { expect, test, type GummiServer } from '../fixtures/test';
import type { Page } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { shot } from '../fixtures/shots';

// A card whose base was rewritten under it (an amend on main after the
// card was cut): every stage session is refused on the drift, so the page
// has to offer the one answer that clears it. It used to offer "try again"
// and "change profile" — both refused the same way — and a sentence
// pointing at a board key the page does not have.

const isPhone = (info: { project: { name: string } }) => info.project.name === 'phone';

async function open(page: Page, server: GummiServer, id: string) {
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
}

async function option(page: Page, phone: boolean, id: string) {
  if (phone) {
    await page.getByTestId('tab-diff').click();
    const toggle = page.getByTestId('mdec-toggle');
    if ((await toggle.getAttribute('aria-expanded')) !== 'true') await toggle.click();
    return page.getByTestId(`mdec-option-${id}`);
  }
  return page.getByTestId(`decision-option-${id}`);
}

test.describe('a card whose base was rewritten under it', () => {
  let id: string;
  test.use({
    seed: {
      run: async (ws) => {
        id = await ws.seedVerifyFailed('Add a shout helper');
        // amend main's tip: the commit the card forked from is gone from it
        fs.writeFileSync(path.join(ws.repo, 'AMENDED.md'), 'amended\n');
        await ws.git('add', 'AMENDED.md');
        await ws.git('commit', '-q', '--amend', '--no-edit');
      },
    },
  });

  test('is answered from its own page', async ({ pairedPage: page, server, api, workspace }, info) => {
    const phone = isPhone(info);
    await open(page, server, id);

    // the stop it was already at leads with the rebase
    const card = (await api('GET', `/api/cards/${id}`)).json;
    expect(card.decision.options[0].id, JSON.stringify(card.decision.options)).toBe('rebase');
    await expect(await option(page, phone, 'rebase')).toContainText('rebase onto main');

    // sending it back runs implement, which is refused on the drift
    await (await option(page, phone, 'bounce')).click();
    await expect.poll(async () => {
      const c = (await api('GET', `/api/cards/${id}`)).json;
      return c.decision?.options?.map((o: any) => o.id).join(',');
    }, { timeout: 30_000 }).toBe('rebase,settle');
    const failed = (await api('GET', `/api/cards/${id}`)).json;
    expect(failed.decision.question).toContain('implement cannot run: main no longer carries the commit this card forked from');
    // the decision box stays short enough that its answers are in view
    await expect(await option(page, phone, 'rebase')).toBeInViewport();
    await shot(page, info, 'drift-failure');

    // the rebase clears it, and the retry that now works comes back (a
    // press within the page's settle window of a new decision is held)
    await page.waitForTimeout(1000);
    await (await option(page, phone, 'rebase')).click();
    await expect.poll(async () => {
      const c = (await api('GET', `/api/cards/${id}`)).json;
      return c.decision?.options?.[0]?.id;
    }, { timeout: 30_000 }).toBe('run');
    const cleared = (await api('GET', `/api/cards/${id}`)).json;
    expect(cleared.decision.question).toContain('rebased onto main');
    expect(cleared.decision.question).not.toContain('fork drift —');
    // only the card's own commits sit on main's new tip
    const log = await workspace.git('log', '--format=%s', `main..${cleared.branch ?? `gummi/${id}`}`);
    expect(log).not.toContain('init: a tiny module');
    // the thread keeps the rebase, naming who asked for it
    const receipts = async () => ((await api('GET', `/api/cards/${id}/thread`)).json.items as any[])
      .filter(it => it.receipt?.kind === 'rebase').map(it => it.receipt.text)
    await expect.poll(receipts).toEqual(['Tester rebased it onto main']);
    await shot(page, info, 'drift-cleared');

    // and the stage runs again
    await page.waitForTimeout(1000);
    await (await option(page, phone, 'run')).click();
    // past the refusal to a finished run: its critique has spoken (the seed's
    // check still fails, which is the scripted agent's, not the drift's)
    await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.decision?.question ?? '', { timeout: 30_000 })
      .toContain('critique');
    const after = (await api('GET', `/api/cards/${id}`)).json;
    expect(after.stage).toBe('implement');
    expect(after.decision.question).not.toContain('fork drift');
  });
});

// The same card, but its own work really conflicts with the rewritten base,
// and it carries uncommitted work the drift kept it from checkpointing: the
// board's rebase stops, and the page offers the agent — which replays the
// card's commits, sorts the conflict, and hands the uncommitted work back.
test.describe('a rewritten base the card really conflicts with', () => {
  let id: string;
  let tree: string;
  test.use({
    seed: {
      run: async (ws) => {
        id = await ws.seedVerifyFailed('Add a shout helper');
        const file = `${id.toLowerCase().replace('-', '')}.go`;
        tree = path.join(ws.repo, '.gummi', 'worktrees', id);
        // what the drift kept from being checkpointed
        fs.writeFileSync(path.join(tree, 'NOTES.md'), 'unfinished\n');
        fs.appendFileSync(path.join(tree, 'README.md'), '\nuncommitted line\n');
        // the base rewritten under the card, adding the card's own file
        fs.writeFileSync(path.join(ws.repo, file), 'package tiny\n\n// from the rewritten base\n');
        await ws.git('add', file);
        await ws.git('commit', '-q', '--amend', '--no-edit');
      },
    },
  });

  test('is handed to the agent from the page', async ({ pairedPage: page, server, api, workspace }, info) => {
    const phone = isPhone(info);
    await open(page, server, id);
    const opts = async () => (await api('GET', `/api/cards/${id}`)).json.decision?.options?.map((o: any) => o.id).join(',');
    await expect.poll(opts).toMatch(/^rebase,/);
    // the seed ran on autopilot, which answers the hand-off itself; an
    // attended card waits for a yes before an agent session spends
    const against = (await api('GET', `/api/cards/${id}`)).json.decision.against.token;
    let mode = await api('POST', `/api/cards/${id}/actions/gate`, { mode: 'attended', against });
    if (mode.status === 409 && mode.json?.confirm) mode = await api('POST', `/api/cards/${id}/actions/gate`, { mode: 'attended', against, confirm: mode.json.confirm });
    expect(mode.status, mode.text).toBe(200);
    await page.waitForTimeout(1000);
    await (await option(page, phone, 'rebase')).click();

    const confirm = page.getByTestId(phone ? 'mdec-confirm' : 'decision-confirm');
    await expect(confirm).toBeVisible();
    await expect(confirm).toContainText('let the agent resolve them');
    await expect(confirm).toContainText('carrying the uncommitted work across');
    await shot(page, info, 'drift-agent-offer');
    await page.getByTestId(phone ? 'mdec-confirm-yes' : 'decision-confirm-yes').click();

    // the agent's rebase settles onto main's new tip; at verify the
    // resolution is unreviewed agent work, so verify runs again (and this
    // seed's check fails again — the scripted card's, not the drift's)
    const branch = (await api('GET', `/api/cards/${id}`)).json.branch;
    await expect.poll(async () => {
      try { await workspace.git('merge-base', '--is-ancestor', 'main', branch); return true; } catch { return false; }
    }, { timeout: 30_000 }).toBe(true);
    await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.decision?.question ?? '', { timeout: 30_000 })
      .toContain('verification stopped here');
    const after = (await api('GET', `/api/cards/${id}`)).json;
    expect(after.decision.options[0].id).not.toBe('rebase');
    // the thread keeps the rebase the agent finished
    expect(((await api('GET', `/api/cards/${id}/thread`)).json.items as any[])
      .filter(it => it.receipt?.kind === 'rebase').map(it => it.receipt.text)).toEqual(['the agent rebased it onto main']);
    // the card's own file on top of the base's, the conflict taking the card's side
    const file = `${id.toLowerCase().replace('-', '')}.go`;
    expect(await workspace.git('show', `${branch}:${file}`)).not.toContain('from the rewritten base');
    expect(fs.readFileSync(path.join(tree, 'NOTES.md'), 'utf8')).toBe('unfinished\n');
    expect(fs.readFileSync(path.join(tree, 'README.md'), 'utf8')).toContain('uncommitted line');
    await shot(page, info, 'drift-agent-done');
  });
});
