import { expect, test, type GummiServer } from '../fixtures/test';
import type { Page } from '@playwright/test';
import { shot } from '../fixtures/shots';

// A card's menu against a real `gummi web`: every entry collects what it
// needs first (a number, cards, a message, a yes), prefilled with what the
// board suggests, and runs through the code the TUI's key runs.

async function open(page: Page, server: GummiServer, id: string, phone: boolean) {
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  if (phone) await page.getByTestId('tab-thread').click();
}

async function menu(page: Page, action: string) {
  await page.getByTestId('card-actions').click();
  await expect(page.getByTestId('card-actions-menu')).toBeVisible();
  await page.getByTestId(`action-${action}`).click();
}

const phone = (info: { project: { name: string } }) => info.project.name === 'phone';

test.describe('a backlog', () => {
  let ids: string[];
  test.use({ seed: { run: async (ws) => { ids = await ws.seedBacklog(['Add a shrug helper', 'Add a nod helper', 'Add a bow helper']); } } });

  test('the budget is raised from the number it is now', async ({ pairedPage: page, server, api }, info) => {
    await open(page, server, ids[0], phone(info));
    await menu(page, 'envelope');
    const input = page.getByTestId('action-input');
    await expect(input).toHaveValue('20');
    await input.fill('25');
    await shot(page, info, 'action-budget');
    await page.getByTestId('action-confirm').click();
    await expect(page.getByTestId('action-dialog')).toHaveCount(0);
    await expect(page.getByTestId('card-spend')).toContainText('/ $25.00');
    expect((await api('GET', `/api/cards/${ids[0]}`)).json.envelope).toBe(2500);
  });

  test('a cycle is refused in the dialog, in the board’s words', async ({ pairedPage: page, server, api }, info) => {
    const [a, b] = ids;
    expect((await api('POST', `/api/cards/${b}/actions/deps`, { cards: [a] })).status).toBe(200);
    await open(page, server, a, phone(info));
    await menu(page, 'deps');
    await page.getByTestId(`action-card-${b}`).check();
    await page.getByTestId('action-confirm').click();
    await expect(page.getByTestId('action-error')).toBeVisible();
    await expect(page.getByTestId('action-dialog')).toBeVisible();
    // said once, in the dialog: the board's broadcast of the same refusal
    // does not stand over it as a toast as well
    // (the dialog capitalises the board's line: compare past its first letter)
    const said = (await page.getByTestId('action-error').textContent())!.trim().slice(1, 30);
    await page.waitForTimeout(1000);
    expect(await page.getByTestId('toast').filter({ hasText: said }).count()).toBe(0);
    await shot(page, info, 'action-refused');
  });

  test('a budget that is not a dollar amount is said in the dialog, not sent', async ({ pairedPage: page, server }, info) => {
    await open(page, server, ids[0], phone(info));
    const sent: string[] = [];
    page.on('request', (r) => { if (r.method() === 'POST' && /\/actions\/envelope$/.test(r.url())) sent.push(r.postData() || '') });
    await menu(page, 'envelope');
    await expect(page.getByTestId('action-confirm')).toHaveText('Set budget');
    for (const v of ['1.505', '1e20', '-3']) {
      await page.getByTestId('action-input').fill(v);
      await page.getByTestId('action-confirm').click();
      await expect(page.getByTestId('action-error')).toContainText(/cent|dollar amount|negative/);
      await expect(page.getByTestId('action-dialog')).toBeVisible();
    }
    expect(sent).toEqual([]);
  });

  test('handing a card to autopilot asks first, in the overlay\'s words', async ({ pairedPage: page, server, api }, info) => {
    const id = ids[2];
    await open(page, server, id, phone(info));
    await menu(page, 'gate');
    // it loosens control: sent bare, the server asks back what autopilot
    // will do with this card, and nothing changes until that is a yes
    await expect(page.getByTestId('action-question')).toContainText(`Hand ${id} to autopilot?`);
    await expect(page.getByTestId('action-question')).toContainText('never lands on');
    expect(((await api('GET', '/api/board')).json.rows.find((r: any) => r.id === id) || {}).autopilot).toBeFalsy();
    await shot(page, info, 'action-autopilot');
    await page.getByTestId('action-confirm').click();
    await expect(page.getByTestId('action-dialog')).toHaveCount(0);
    await expect.poll(async () => ((await api('GET', '/api/board')).json.rows.find((r: any) => r.id === id) || {}).autopilot).toBeTruthy();
  });

  test('delete asks first, then the card is gone', async ({ pairedPage: page, server, api }, info) => {
    const id = ids[1];
    await open(page, server, id, phone(info));
    const sent: any[] = [];
    page.on('request', (r) => { if (r.url().includes('/actions/delete') && r.method() === 'POST') sent.push(r.postDataJSON() || {}); });
    await menu(page, 'delete');
    // the question is the server's own, whole: sent bare, asked back
    await expect(page.getByTestId('action-question')).toContainText(`Delete ${id}?`);
    await expect(page.getByTestId('action-question')).toContainText('removes worktree, branch, and record');
    expect(sent).toHaveLength(1);
    expect(sent[0].confirm).toBeUndefined();
    await shot(page, info, 'action-delete');
    await page.getByTestId('action-cancel').click();
    expect((await api('GET', `/api/cards/${id}`)).status).toBe(200);
    // a bare yes is not one
    expect((await api('POST', `/api/cards/${id}/actions/delete`, { confirm: true })).status).toBe(400);
    await menu(page, 'delete');
    await page.getByTestId('action-confirm').click();
    // the yes is the token the question came with
    await expect.poll(() => sent.length).toBe(3);
    expect(typeof sent[2].confirm).toBe('string');
    expect(sent[2].confirm.length).toBeGreaterThan(8);
    await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).status).toBe(404);
    // where it was open, it is said to be gone; the page does not move
    // itself onto another card (on a phone it is this screen's own delete,
    // so the page is back on the cards already)
    await expect(page.getByTestId('card-title')).toHaveText(`${id} · deleted`);
    await expect(page.getByTestId('card-id')).toHaveCount(0);
    await expect(page.getByTestId(`rail-row-${id}`)).toHaveCount(0);
  });
});

test.describe('a card in its design stage', () => {
  let ids: string[];
  test.use({ seed: { run: async (ws) => { ids = await ws.seedBacklog(['Add a shrug helper', 'Add a nod helper', 'Add a bow helper']); } } });

  test('dependencies are ticked from the board, and cleared the same way', async ({ pairedPage: page, server, api }, info) => {
    const [a, b, c] = ids;
    // into its design stage, where what it waits on starts to matter
    const card = (await api('GET', `/api/cards/${c}`)).json;
    expect((await api('POST', `/api/cards/${c}/answer`, { ref: card.decision.ref, option: 'advance', against: card.decision.against.token })).status).toBe(200);
    await open(page, server, c, phone(info));
    await expect(page.getByTestId('stage-plan')).toHaveAttribute('aria-current', 'step');
    await menu(page, 'deps');
    await page.getByTestId(`action-card-${a}`).check();
    await page.getByTestId(`action-card-${b}`).check();
    await shot(page, info, 'action-deps');
    await page.getByTestId('action-confirm').click();
    await expect(page.getByTestId('action-dialog')).toHaveCount(0);
    // it now waits on both before it may implement
    await expect(page.getByTestId('card-head')).toContainText(`waits on ${a}, ${b}`);
    // the picker opens with what is set ticked
    await menu(page, 'deps');
    await expect(page.getByTestId(`action-card-${a}`)).toBeChecked();
    await page.getByTestId(`action-card-${a}`).uncheck();
    await page.getByTestId('action-confirm').click();
    await expect.poll(async () => (await api('GET', `/api/cards/${c}`)).json.waits).toEqual([b]);
    await expect(page.getByTestId('card-head')).not.toContainText(a);
  });
});

test.describe('a verified card', () => {
  let id: string;
  test.use({ seed: { run: async (ws) => { id = await ws.seedVerified('Add a farewell helper'); } } });

  test('lands with the message drafted when verify passed', async ({ pairedPage: page, server, workspace }, info) => {
    await open(page, server, id, phone(info));
    await menu(page, 'merge');
    const msg = page.getByTestId('action-input');
    await expect(msg).toHaveValue(/^feat: land /);
    await expect(page.getByTestId('action-dialog')).toContainText('Drafted when verify passed');
    await shot(page, info, 'action-land');
    // a squash in place is not a landing, and its hint does not say so
    await page.getByTestId('action-cancel').click();
    await menu(page, 'squash');
    await expect(page.getByTestId('action-input')).toHaveValue(/^feat: land /);
    await expect(page.getByTestId('action-hint')).toContainText('the one commit the branch becomes');
    await expect(page.getByTestId('action-hint')).not.toContainText(/lands|Nothing was drafted/);
    // sent with no message, the squash waits on a drafting pass: the
    // dialog says it is drafting until the draft comes back into the box
    await page.getByTestId('action-input').fill('');
    let release!: () => void;
    const held = new Promise<void>((r) => { release = r; });
    await page.route(`**/api/cards/${id}/actions/squash`, async (route) => { await held; await route.continue(); });
    await page.getByTestId('action-confirm').click();
    await expect(page.getByTestId('action-confirm')).toHaveText('Drafting…');
    await expect(page.getByTestId('action-confirm')).toBeDisabled();
    await expect(page.getByTestId('action-hint')).toContainText('gummi is drafting the message');
    await expect(page.getByTestId('action-input')).not.toBeEditable();
    await shot(page, info, 'action-drafting');
    release();
    await expect(page.getByTestId('action-input')).toHaveValue(/^feat: land /);
    // what came back is the draft stored for the branch, and the hint
    // says that — not that gummi drafted it just now
    await expect(page.getByTestId('action-hint')).toContainText('Drafted for this branch');
    await expect(page.getByTestId('action-confirm')).toHaveText('Squash');
    await expect(page.getByTestId('action-input')).toBeEditable();
    await page.unroute(`**/api/cards/${id}/actions/squash`);
    await page.getByTestId('action-cancel').click();
    await menu(page, 'merge');
    await page.getByTestId('action-confirm').click();
    await expect(page.getByTestId('action-dialog')).toHaveCount(0, { timeout: 30_000 });
    if (phone(info)) await page.getByTestId('card-back').click();
    await expect(page.getByTestId('rail-group-done').getByTestId(`rail-row-${id}`)).toBeVisible();
    // the group holds every closed card, landed or handed off: it is not
    // headed as if each one landed
    await expect(page.getByTestId('rail-group-done')).toContainText('Done');
    await expect(page.getByTestId('rail-group-done')).not.toContainText('Landed');
    expect(await workspace.git('log', '-1', '--format=%s', 'main')).toMatch(/^feat: land /);
    await shot(page, info, 'landed');
  });
});

test.describe('a verified card’s menu', () => {
  let id: string;
  test.use({ seed: { run: async (ws) => { id = await ws.seedVerified('Add a farewell helper'); } } });

  // "next stage" out of verify lands the branch: the menu says so in the
  // decision's own words, and presses it as the line it crosses
  test('next stage at verify reads as the landing it is', async ({ pairedPage: page, server }, info) => {
    await open(page, server, id, phone(info));
    await page.getByTestId('card-actions').click();
    const entry = page.getByTestId('action-advance');
    await expect(entry).toContainText('land on main');
    await expect(entry).toHaveClass(/\bdanger\b/);
    await entry.click();
    const dlg = page.getByTestId('action-dialog');
    await expect(dlg).toBeVisible({ timeout: 30_000 });
    await expect(dlg).toContainText('Land on main');
    await expect(page.getByTestId('action-confirm')).toHaveClass(/\bdanger\b/);
    await expect(page.getByTestId('action-hint')).toContainText('Drafted when verify passed');
    await shot(page, info, 'action-next-lands');
    await page.getByTestId('action-cancel').click();
  });
});

test.describe('a running card', () => {
  test.use({ workspaceEnv: { GUMMI_E2E_SLOW_SECONDS: '30' } });

  test('pauses from the head’s button', async ({ pairedPage: page, server, api }, info) => {
    const c = (await api('POST', '/api/cards', { kind: 'feature', title: '[slow] Add a lazy helper' })).json;
    let card = (await api('POST', `/api/cards/${c.id}/answer`, { ref: c.decision.ref, option: 'advance', against: c.decision.against.token })).json;
    card = (await api('POST', `/api/cards/${c.id}/answer`, { ref: card.decision.ref, option: 'run', against: card.decision.against.token })).json;
    await open(page, server, c.id, phone(info));
    const pause = page.getByTestId('action-btn-pause');
    await expect(pause).toBeVisible();
    await expect(page.getByTestId('live')).toBeVisible();
    await shot(page, info, 'running');
    await pause.click();
    await expect.poll(async () => (await api('GET', `/api/cards/${c.id}`)).json.status).toBe('paused');
    if (phone(info)) await page.getByTestId('card-back').click();
    await expect(page.getByTestId(`rail-row-${c.id}`)).toHaveAttribute('data-status', 'paused');
  });
});
