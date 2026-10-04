import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import { expect, test } from '../fixtures/test';
import type { Page } from '@playwright/test';
import { shot } from '../fixtures/shots';

// "Notifications on this device" against a real `gummi web`. Headless
// Chromium has neither notifications nor a push service, so where the
// browser's own permission and PushManager would be, an init script stands
// in for them (and only for them): the page's calls to the server — the
// key, the subscription, the unsubscribe — are real. The service worker is
// checked registered and in control of the page, and its push and click
// handlers are run against stand-ins for the worker's globals.

const pushFile = (repo: string) => path.join(repo, '.gummi', 'state', 'web', 'push.json');

async function openPush(page: Page) {
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  await page.getByTestId('rail-more').click();
  await page.getByTestId('menu-push').click();
  await expect(page.getByTestId('push-dialog')).toBeVisible();
}

test('a browser that is not allowed to notify says so', async ({ pairedPage: page }, info) => {
  // headless Chromium has no notifications at all: Notification.permission
  // reads "denied" whatever is granted, which is what a person who blocked
  // the site sees
  expect(await page.evaluate(() => Notification.permission)).toBe('denied');
  await openPush(page);
  await expect(page.getByTestId('push-state')).toHaveAttribute('data-state', 'denied');
  await expect(page.getByTestId('push-body')).toContainText('blocked for this site');
  await expect(page.getByTestId('push-on')).toHaveCount(0);
  await shot(page, info, 'push-denied');
});

test.describe('with a stand-in push service', () => {
  // a real P-256 key and auth secret: the server checks both before it
  // keeps a subscription
  const ecdh = crypto.createECDH('prime256v1');
  ecdh.generateKeys();
  const keys = { p256dh: ecdh.getPublicKey().toString('base64url'), auth: crypto.randomBytes(16).toString('base64url') };
  const endpoint = `https://push.example.invalid/e2e/${crypto.randomBytes(6).toString('hex')}`;

  test.beforeEach(async ({ context }) => {
    await context.grantPermissions(['notifications']);
    await context.addInitScript(({ endpoint, keys }) => {
      if (!('PushManager' in self)) return;
      // the browser's permission, which headless Chromium cannot give
      let perm: NotificationPermission = 'default';
      Object.defineProperty(Notification, 'permission', { get: () => perm, configurable: true });
      (Notification as any).requestPermission = async () => { perm = 'granted'; return perm; };
      let current: any = null;
      const sub = () => ({
        endpoint,
        expirationTime: null,
        options: { userVisibleOnly: true },
        toJSON: () => ({ endpoint, expirationTime: null, keys }),
        unsubscribe: async () => { current = null; return true; },
      });
      (PushManager.prototype as any).subscribe = async function (opts: any) {
        if (!opts || !opts.applicationServerKey) throw new Error('no applicationServerKey');
        (self as any).__pushKeyBytes = new Uint8Array(opts.applicationServerKey).length;
        current = sub();
        return current;
      };
      (PushManager.prototype as any).getSubscription = async () => current;
    }, { endpoint, keys });
  });

  test('turning it on subscribes this device, and off unsubscribes it', async ({ pairedPage: page, workspace }, info) => {
    await page.reload();
    await openPush(page);
    await expect(page.getByTestId('push-state')).toHaveAttribute('data-state', 'off');
    const subscribed = page.waitForRequest((r) => r.url().endsWith('/api/push/subscribe') && r.method() === 'POST');
    await page.getByTestId('push-on').click();
    const req = await subscribed;
    expect(req.postDataJSON()).toMatchObject({ endpoint, keys });
    await expect(page.getByTestId('push-state')).toHaveAttribute('data-state', 'on');
    // the server's VAPID key, decoded: an uncompressed P-256 point
    expect(await page.evaluate(() => (self as any).__pushKeyBytes)).toBe(65);
    const kept = JSON.parse(fs.readFileSync(pushFile(workspace.repo), 'utf8'));
    expect(kept.subscriptions.map((s: any) => s.endpoint)).toContain(endpoint);
    await shot(page, info, 'push-on');

    const dropped = page.waitForRequest((r) => r.url().endsWith('/api/push/subscribe') && r.method() === 'DELETE');
    await page.getByTestId('push-off').click();
    await dropped;
    await expect(page.getByTestId('push-state')).toHaveAttribute('data-state', 'off');
    await expect.poll(() => JSON.parse(fs.readFileSync(pushFile(workspace.repo), 'utf8')).subscriptions.length).toBe(0);
  });

  test('the service worker is registered at the root and controls the page', async ({ pairedPage: page, server }) => {
    await page.reload();
    const reg = await page.evaluate(async () => {
      const r = await navigator.serviceWorker.ready;
      return { scope: r.scope, script: r.active?.scriptURL };
    });
    expect(reg).toEqual({ scope: `${server.url}/`, script: `${server.url}/sw.js` });
    // ready resolves once a worker is active, which can be before it has
    // claimed this page (its activate handler calls clients.claim()): wait
    // for the claim's controllerchange rather than reading controller once,
    // and poll on top, since a loaded machine can take seconds to get there
    await page.evaluate(() => navigator.serviceWorker.controller
      ? null
      : new Promise<void>((done) => {
        navigator.serviceWorker.addEventListener('controllerchange', () => done(), { once: true });
        setTimeout(done, 10_000);
      }));
    await expect.poll(() => page.evaluate(() => !!navigator.serviceWorker.controller), { timeout: 15_000 }).toBe(true);
  });
});

// sw.js's handlers, run against stand-ins for the worker's globals: what
// a push shows, and what tapping it opens.
function worker(origin: string, wins: string[] = []) {
  const src = fs.readFileSync(path.join(__dirname, '..', '..', '..', 'internal', 'web', 'assets', 'sw.js'), 'utf8');
  const handlers: Record<string, (e: any) => void> = {};
  const calls: any[] = [];
  const self = {
    location: new URL(`${origin}/sw.js`),
    addEventListener: (k: string, fn: (e: any) => void) => { handlers[k] = fn; },
    skipWaiting: () => {},
    clients: {
      claim: async () => {},
      matchAll: async () => wins.map((u) => ({
        url: u,
        focus: async () => { calls.push(['focus', u]); },
        postMessage: (m: any) => calls.push(['post', u, m]),
      })),
      openWindow: async (u: string) => { calls.push(['open', u]); },
    },
    registration: { showNotification: async (title: string, opts: any) => { calls.push(['show', title, opts]); } },
  };
  vm.runInNewContext(src, { self, URL });
  const fire = async (kind: string, e: any) => {
    let done: Promise<unknown> = Promise.resolve();
    handlers[kind]({ ...e, waitUntil: (p: Promise<unknown>) => { done = p; } });
    await done;
    return calls;
  };
  return { fire };
}

test('a push becomes a notification that links to its card', async ({ server }) => {
  const msg = { title: 'FD-001 needs you', body: 'plan finished — review & advance', url: '/#FD-001', tag: 'FD-001' };
  const calls = await worker(server.url).fire('push', { data: { json: () => msg, text: () => JSON.stringify(msg) } });
  expect(calls).toEqual([['show', 'FD-001 needs you', expect.objectContaining({ body: msg.body, tag: 'FD-001', data: { url: '/#FD-001' } })]]);
  // a link off the board is never followed
  const odd = await worker(server.url).fire('push', { data: { json: () => ({ title: 'x', url: 'https://evil.example/' }), text: () => '' } });
  expect(odd[0][2].data).toEqual({ url: '/' });
  const sneaky = await worker(server.url).fire('push', { data: { json: () => ({ title: 'x', url: '//evil.example/' }), text: () => '' } });
  expect(sneaky[0][2].data).toEqual({ url: '/' });
});

test('tapping a notification focuses the board and opens the card', async ({ pairedPage: page, server, api }, info) => {
  const id = (await api('POST', '/api/cards', { kind: 'feature', title: 'Add a wink helper' })).json.id;
  // the worker's half: focus the board's window and tell it where to go,
  // or open one when there is none
  const tap = { notification: { data: { url: `/#${id}` }, close: () => {} } };
  expect(await worker(server.url, [`${server.url}/#FD-999`, 'https://elsewhere.example/']).fire('notificationclick', tap)).toEqual([
    ['focus', `${server.url}/#FD-999`], ['post', `${server.url}/#FD-999`, { type: 'gummi:open', url: `/#${id}` }],
  ]);
  expect(await worker(server.url, ['https://elsewhere.example/']).fire('notificationclick', tap)).toEqual([['open', `${server.url}/#${id}`]]);

  // the page's half: told by its worker, it opens the card
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  await page.evaluate((url) => navigator.serviceWorker.dispatchEvent(new MessageEvent('message', { data: { type: 'gummi:open', url } })), `/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
});
