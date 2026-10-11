import { test as base, expect, type BrowserContext, type Page } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { collectCoverage, type ScriptCoverage } from './coverage';
import { api, pair, type ApiResponse, GummiServer } from './server';
import { Workspace } from './workspace';

export { expect };
export { GummiServer, Workspace };
export { api, pair, seedFreeform, freePort } from './server';

/**
 * What to put on the board before the server starts. An object rather than
 * a bare function because Playwright reads a function passed to test.use()
 * as a fixture implementation, not as the option's value.
 */
export interface Seed {
  run(ws: Workspace): Promise<void>;
}

/** `api` bound to the paired page's cookie and the server's origin. */
export type BoundApi = <T = any>(
  method: string,
  pathname: string,
  body?: unknown,
  opts?: { origin?: string | null; headers?: Record<string, string> },
) => Promise<ApiResponse<T>>;

interface Fixtures {
  _coverage: void;
  /**
   * Option: runs against the fresh workspace BEFORE the server starts, so a
   * test's cards exist when the board first loads:
   *
   *   test.use({ seed: { run: async (ws) => { await ws.seedDesignGate('Add a wave helper'); } } });
   */
  seed: Seed | null;
  /** Option: extra environment for every gummi process (agent pacing etc.). */
  workspaceEnv: NodeJS.ProcessEnv;
  /** Option: extra `gummi web` flags. */
  serverArgs: string[];
  /** Option: the name pairedPage pairs as. */
  person: string;

  /** A fresh, seeded workspace; removed after the test. */
  workspace: Workspace;
  /** `gummi web` on a free loopback port for that workspace; stopped after. */
  server: GummiServer;
  /** A page whose context is paired (cookie set) and has the board open. */
  pairedPage: Page;
  /** JSON API calls as the paired page's device, same-origin by default. */
  api: BoundApi;
}

export const test = base.extend<Fixtures>({
  seed: [null, { option: true }],
  workspaceEnv: [{}, { option: true }],
  serverArgs: [[], { option: true }],
  person: ['Tester', { option: true }],

  // GUMMI_E2E_COVERAGE=<dir> records which bytes of the page's own
  // scripts ran, for scripts/web-cover.mjs to read back. It follows every
  // page the test opens — the built-in one and those of contexts the test
  // makes itself — and starts before anything on the page loads.
  _coverage: [
    async ({ context, browser }, use, testInfo) => {
      const dir = process.env.GUMMI_E2E_COVERAGE;
      if (!dir) return use();
      // A page's coverage cannot be read once it is closed, and specs do
      // close pages and contexts, so each one is read out just before.
      const results: Array<Promise<ScriptCoverage[]>> = [];
      const pageReads: Array<() => void> = [];
      const follow = (c: BrowserContext) => {
        c.on('page', (p) => {
          const ready = collectCoverage(p);
          let read: Promise<ScriptCoverage[]> | null = null;
          const take = () => {
            if (!read) {
              read = ready.then((stop) => stop()).catch(() => []);
              results.push(read);
            }
            return read;
          };
          pageReads.push(() => void take());
          const close = p.close.bind(p);
          p.close = async (o) => {
            await take();
            return close(o);
          };
        });
        const close = c.close.bind(c);
        c.close = async (o) => {
          for (const take of pageReads) take();
          await Promise.all(results);
          return close(o);
        };
      };
      follow(context);
      const made = browser.newContext.bind(browser);
      browser.newContext = async (o) => {
        const c = await made(o);
        follow(c);
        return c;
      };
      await use();
      browser.newContext = made;
      for (const take of pageReads) take();
      const all = (await Promise.all(results)).flat();
      fs.mkdirSync(dir, { recursive: true });
      fs.writeFileSync(path.join(dir, `${testInfo.project.name}-${testInfo.testId}.json`), JSON.stringify(all));
    },
    { auto: true },
  ],

  workspace: async ({ seed, workspaceEnv }, use, testInfo) => {
    const ws = await Workspace.create({ env: workspaceEnv, name: testInfo.project.name });
    if (seed) await seed.run(ws);
    await use(ws);
    if (testInfo.status !== testInfo.expectedStatus) {
      for (const name of ['agent.log', 'gh.log']) {
        const p = `${ws.logs}/${name}`;
        if (fs.existsSync(p)) await testInfo.attach(name, { path: p, contentType: 'text/plain' });
      }
    }
    await ws.dispose();
  },

  server: async ({ workspace, serverArgs }, use, testInfo) => {
    const server = await GummiServer.start(workspace, { args: serverArgs });
    await use(server);
    await server.stop();
    if (testInfo.status !== testInfo.expectedStatus && fs.existsSync(server.logFile)) {
      await testInfo.attach('server.log', { path: server.logFile, contentType: 'text/plain' });
    }
  },

  pairedPage: async ({ page, server, person }, use) => {
    await pair(page, server, person);
    await use(page);
  },

  api: async ({ pairedPage, server }, use) => {
    const request = pairedPage.context().request;
    await use((method, pathname, body, opts) => api(request, server, method, pathname, body, opts));
  },
});

/**
 * Open card `id` the way a person would on a phone: a phone's board opens
 * on its cards list (and comes back to it on reload), so the card is
 * tapped there. Anywhere wider the board already shows the card it
 * picked, and this does nothing.
 */
export async function showCard(page: Page, id: string): Promise<void> {
  if ((page.viewportSize()?.width ?? 1440) > 760) return;
  // the board is up once it is live; a deep link has already opened the
  // card by then, and the cards list is only showing when it has not
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  if ((await page.getByTestId('app').getAttribute('data-view')) === 'cards') await page.getByTestId(`rail-row-${id}`).click();
  await expect(page.getByTestId('card-id')).toHaveText(id);
}

// showTab brings one of a card's documents on screen, whatever shows now.
// Spec, Memory, Run and Terminal each have an icon (a tab, on a phone);
// the diff, the log and the pull request share the Changes one, behind the
// switch in its header. An icon pressed while its surface is open closes
// it, so this presses only what is not already showing.
export async function showTab(page: Page, name: string): Promise<void> {
  const changes = ['diff', 'log', 'pr'].includes(name);
  const icon = page.getByTestId(`tab-${changes ? 'diff' : name}`);
  if ((await icon.getAttribute('aria-selected')) !== 'true') await icon.click();
  if (!changes) return;
  const leaf = page.getByTestId(name === 'diff' ? 'changes-diff' : `tab-${name}`);
  if ((await leaf.getAttribute('aria-selected')) !== 'true') await leaf.click();
}
