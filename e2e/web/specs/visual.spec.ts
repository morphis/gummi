import type { Page, TestInfo } from '@playwright/test';
import { expect, test, type GummiServer } from '../fixtures/test';
import { seedBoard, seedLive, type Board } from '../fixtures/board';
import { shot } from '../fixtures/shots';

// Visual QA: a board with a card in every state, and every surface of the
// page photographed in the light and the dark theme (screens/, per
// viewport project). The screenshots are for a person to read; the test
// itself only fails on what a machine can see — text a template leaked
// ("undefined", "null", "NaN"), a page that scrolls sideways, a surface
// that did not open.

let board: Board;

test.use({
  workspaceEnv: { GUMMI_E2E_SLOW_SECONDS: '900' },
  seed: { run: async (ws) => { board = await seedBoard(ws); } },
});

const LEAKS = /\b(undefined|null|NaN|\[object Object\])\b/;

class Look {
  problems: string[] = [];
  theme: 'light' | 'dark' = 'light';
  constructor(readonly page: Page, readonly info: TestInfo, readonly server: GummiServer) {}

  get phone() { return this.info.project.name === 'phone'; }

  async theme_(t: 'light' | 'dark') {
    this.theme = t;
    await this.page.emulateMedia({ colorScheme: t });
  }

  async check(where: string) {
    const { text, wide } = await this.page.evaluate(() => ({
      text: document.body.innerText,
      wide: document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
    }));
    for (const line of text.split('\n')) {
      if (LEAKS.test(line)) this.problems.push(`${where}: "${line.trim().slice(0, 120)}"`);
    }
    if (wide) this.problems.push(`${where}: the page scrolls sideways`);
  }

  async snap(name: string) {
    await this.page.waitForTimeout(200);
    await this.check(`${this.theme}/${name}`);
    await shot(this.page, this.info, `${this.theme}-${name}`);
  }

  async settle() {
    await expect(this.page.getByTestId('thread-loading')).toHaveCount(0);
    await expect(this.page.getByTestId('panel-loading')).toHaveCount(0);
  }

  async open(id: string, tab?: string) {
    await this.page.evaluate(([i, t]) => { location.hash = t ? `${i}/${t}` : i; }, [id, tab ?? '']);
    await expect(this.page.getByTestId('card-id')).toHaveText(id);
    // back on the cards with the hash unchanged, the card is opened as a
    // person would: from its row
    if (this.phone && !(await this.page.getByTestId('card-back').isVisible())) await this.page.getByTestId(`rail-row-${id}`).click();
    if (this.phone) await this.page.getByTestId('tab-thread').click();
    await this.settle();
  }

  async tab(tab: string) {
    await this.page.getByTestId(`tab-${tab}`).click();
    await expect(this.page.getByTestId('panel-pane')).toHaveAttribute('data-tab', tab);
    await this.settle();
  }

  async rail() {
    // back to the cards, unless they are already the screen
    const back = this.page.getByTestId('card-back');
    if (this.phone && (await back.isVisible())) await back.click();
  }

  async view(name: string, open: () => Promise<void>) {
    await open();
    await expect(this.page.getByTestId(`view-${name}`)).toBeVisible();
    await expect(this.page.getByTestId(`view-${name}`).locator('.spinner')).toHaveCount(0, { timeout: 15_000 });
    await this.snap(`view-${name}`);
    await this.page.keyboard.press('Escape');
    await expect(this.page.getByTestId(`view-${name}`)).toHaveCount(0);
  }

  async more(name: string) {
    await this.rail();
    await this.page.getByTestId('rail-more').click();
    await this.page.getByTestId(`menu-${name}`).click();
  }
}

test('every surface, in both themes', async ({ pairedPage: page, server, api }, info) => {
  test.setTimeout(600_000);
  await seedLive(page.context().request, server, board);
  await page.reload();
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  const L = new Look(page, info, server);
  const b = board;

  for (const theme of ['light', 'dark'] as const) {
    await L.theme_(theme);

    // the board, with a design gate open on its spec
    await L.open(b.gate);
    await L.rail();
    await L.snap('rail');
    await L.open(b.gate);
    await L.snap('gate-thread');
    await L.tab('spec');
    await L.snap('gate-spec');

    // a failed verify: the decision, the diff with a pending comment, the checks
    await L.open(b.failed);
    await L.snap('failed-thread');
    await L.tab('diff');
    await L.snap('failed-diff');
    await L.tab('spec');
    await L.snap('failed-spec');
    await L.tab('pr');
    await L.snap('failed-pr');
    await L.tab('stats');
    await L.snap('failed-stats');

    // an agent's question
    await L.open(b.ask);
    await L.snap('ask-thread');

    // a running card, streaming
    await L.open(b.running!);
    await expect(page.getByTestId('live')).toBeVisible();
    await L.snap('running-thread');

    // landed, backlog with a dependency, a bug, freeform, research, a goal, a stack
    await L.open(b.landed);
    await L.snap('landed-thread');
    await L.tab('stats');
    await L.snap('landed-stats');
    await L.open(b.backlog[1]);
    await L.snap('backlog-thread');
    await L.open(b.bug);
    await L.snap('bug-thread');
    await L.open(b.freeform!);
    await L.snap('freeform-thread');
    await L.open(b.research);
    await L.snap('research-thread');
    await L.tab('spec');
    await L.snap('research-spec');
    await L.open(b.goal);
    await L.snap('goal-thread');
    await L.open(b.stacked[0]);
    await L.snap('stacked-thread');

    // the card's menu and one of its dialogs
    await L.open(b.backlog[0]);
    await page.getByTestId('card-actions').click();
    await expect(page.getByTestId('card-actions-menu')).toBeVisible();
    await L.snap('card-menu');
    await page.getByTestId('action-envelope').click();
    await expect(page.getByTestId('action-dialog')).toBeVisible();
    await L.snap('action-dialog');
    await page.getByTestId('action-cancel').click();
    await page.getByTestId('card-actions').click();
    await page.getByTestId('action-delete').click();
    await L.snap('confirm-dialog');
    await page.getByTestId('action-cancel').click();

    // the rail's More menu, the palette, the keys, a toast
    await L.rail();
    await page.getByTestId('rail-more').click();
    await expect(page.getByTestId('rail-more-menu')).toBeVisible();
    await L.snap('more-menu');
    await page.keyboard.press('Escape');
    if (!L.phone) {
      await page.getByTestId('btn-palette').click();
      await expect(page.getByTestId('palette')).toBeVisible();
      await L.snap('palette');
      await page.getByTestId('palette-input').fill('help');
      await L.snap('palette-query');
      await page.keyboard.press('Escape');
      await page.getByTestId('btn-keys').click();
      await expect(page.getByTestId('keys-help')).toBeVisible();
      await L.snap('keys-help');
      await page.keyboard.press('Escape');
    }
    await page.evaluate(async () => {
      const { toast } = await import('/assets/toast.js');
      toast('Answered: approve');
      toast('BG-104: rebasing onto main failed — the branch has conflicts', { err: true, ms: 8000 });
    });
    await L.snap('toasts');

    // the board's other surfaces
    await L.view('newcard', async () => { await L.rail(); await page.getByTestId('rail-new').click(); });
    await L.view('goals', () => L.more('goals'));
    await L.more('goals');
    await page.getByTestId(`goal-row-${b.goal}`).click();
    await expect(page.getByTestId('view-goal')).toBeVisible();
    await L.snap('view-goal');
    await page.keyboard.press('Escape');
    await L.view('stacks', () => L.more('stacks'));
    await L.view('ingest', () => L.more('ingest'));
    await L.view('bugs', () => L.more('bugs'));
    await L.view('doctor', () => L.more('doctor'));
    await L.view('fleet', async () => { await L.rail(); await page.getByTestId('rail-fleet').click(); });
  }

  // a decision pinned at the smaller desktop heights leaves room to read
  if (info.project.name === 'desktop') {
    await L.theme_('light');
    for (const [w, hgt] of [[1280, 720], [1024, 768], [1440, 900]]) {
      await page.setViewportSize({ width: w, height: hgt });
      for (const id of [b.failed, b.ask, b.goal]) {
        await L.open(id);
        const thread = await page.getByTestId('thread').boundingBox();
        expect(thread!.height, `${id} at ${w}×${hgt}: the thread keeps room`).toBeGreaterThan(hgt * 0.3);
        await L.snap(`dock-${id}-${w}x${hgt}`);
      }
    }
    await page.setViewportSize({ width: 1440, height: 900 });
  }

  // the connection drops: the pill says so and answers pause
  await L.open(b.gate);
  await server.stop();
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'reconnecting');
  for (const theme of ['light', 'dark'] as const) {
    await L.theme_(theme);
    await L.snap('reconnecting');
  }

  expect(L.problems).toEqual([]);
});
