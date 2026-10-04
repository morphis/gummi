import type { Browser, BrowserContext, Page, TestInfo } from '@playwright/test';
import { api, expect, test, type GummiServer } from '../fixtures/test';
import { shot } from '../fixtures/shots';

// Letting a device in (DESIGN §20.3). Once a browser has the board, one
// paired with a code `gummi web pair` minted — which anything running as
// the operator can mint — only waits: the page says so, every route but
// the session refuses it, and a page already at the board shows the
// request until somebody approves or rejects it.

async function secondBrowser(browser: Browser, info: TestInfo): Promise<BrowserContext> {
  const u = info.project.use as any;
  return browser.newContext({ viewport: u.viewport, isMobile: u.isMobile, hasTouch: u.hasTouch, deviceScaleFactor: u.deviceScaleFactor, userAgent: u.userAgent });
}

// pairWaiting pairs page2 through the form with `gummi web pair`'s code and
// returns the device id it waits as.
async function pairWaiting(page2: Page, server: GummiServer, name: string): Promise<string> {
  const code = await server.freshCode();
  await page2.goto(server.url);
  await page2.getByTestId('pair-name').fill(name);
  await page2.getByTestId('pair-code').fill(code);
  await page2.getByTestId('pair-submit').click();
  await expect(page2.getByTestId('pending')).toBeVisible();
  await expect(page2.getByTestId('app')).toBeHidden();
  const session = await (await page2.context().request.get(`${server.url}/api/session`)).json();
  expect(session.approval).toBe('pending');
  return session.deviceId;
}

test('a second browser waits until a paired page approves it', async ({ pairedPage: page, server, browser }, info) => {
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  const other = await secondBrowser(browser, info);
  const page2 = await other.newPage();
  try {
    const id = await pairWaiting(page2, server, 'Ana');
    await expect(page2.getByTestId('pending-text')).toContainText('Ana');
    await expect(page2.getByTestId('pending-expires')).toHaveText(/^\d+:\d\d$/);
    await shot(page2, info, 'approval-waiting');

    // waiting reaches nothing: not the board, not a write
    const board = await api(page2.context().request, server, 'GET', '/api/board');
    expect(board.status).toBe(403);
    expect(board.json.error).toBe('waiting for approval on a paired device');
    expect(board.json.approval).toBe('pending');
    const write = await api(page2.context().request, server, 'POST', '/api/cards', { kind: 'freeform', title: 'sneak' });
    expect(write.status).toBe(403);
    expect(write.json.approval).toBe('pending');

    // the page at the board shows the request, with what it needs to decide
    const req = page.getByTestId(`approval-${id}`);
    await expect(page.getByTestId('approvals')).toBeVisible();
    await expect(req.getByTestId('approval-person')).toHaveText('Ana');
    await expect(req.getByTestId('approval-via')).toContainText('minted on the machine hosting the board');
    await expect(req.getByTestId('approval-source')).toHaveText('127.0.0.1');
    await expect(req.getByTestId('approval-ua')).toContainText('Mozilla');
    await expect(req.getByTestId('approval-time')).toContainText('lapses in');
    await shot(page, info, 'approval-banner');

    await req.getByTestId('approval-approve').click();
    await expect(page.getByTestId('approvals-slot')).toBeHidden();
    // the waiting page is let in at once, without a reload by hand
    await expect(page2.getByTestId('app')).toBeVisible();
    await expect(page2.getByTestId('conn')).toHaveAttribute('data-state', 'live');
    expect((await api(page2.context().request, server, 'GET', '/api/board')).status).toBe(200);
    expect(server.log).toContain('approved Ana');
  } finally {
    await other.close();
  }
});

test('a rejected browser is back at the pairing form, and told why', async ({ pairedPage: page, server, browser }, info) => {
  test.skip(info.project.name === 'laptop', 'desktop and phone cover it');
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  const other = await secondBrowser(browser, info);
  const page2 = await other.newPage();
  try {
    const id = await pairWaiting(page2, server, 'Mallory');
    await page.getByTestId(`approval-${id}`).getByTestId('approval-reject').click();
    await expect(page.getByTestId('approvals-slot')).toBeHidden();
    await expect(page2.getByTestId('pair-refused')).toContainText('rejected');
    await expect(page2.getByTestId('pair-form')).toBeVisible();
    await expect(page2.getByTestId('app')).toBeHidden();
    expect((await api(page2.context().request, server, 'GET', '/api/board')).status).toBe(401);
    expect(server.log).toContain('rejected Mallory');
  } finally {
    await other.close();
  }
});

// A request can lapse while a dialog is up: the banner over the dialog is
// still live (not made inert with the page behind the dialog), and it is
// said aloud once when it arrives.
test('a request is answerable while a dialog is open, and announced', async ({ pairedPage: page, server, browser }, info) => {
  test.skip(info.project.name !== 'desktop', 'one viewport is enough');
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  await page.getByTestId('rail-fleet').click();
  await expect(page.getByTestId('view-fleet')).toBeVisible();
  const other = await secondBrowser(browser, info);
  const page2 = await other.newPage();
  try {
    const id = await pairWaiting(page2, server, 'Ana');
    await expect(page.getByTestId('approvals-live')).toContainText('Ana');
    await page.getByTestId(`approval-${id}`).getByTestId('approval-approve').click();
    await expect(page.getByTestId('approvals-slot')).toBeHidden();
    await expect(page2.getByTestId('app')).toBeVisible();
    // the dialog is still the one open, and still holds the page
    await expect(page.getByTestId('view-fleet')).toBeVisible();
  } finally {
    await other.close();
  }
});
