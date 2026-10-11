import AxeBuilder from '@axe-core/playwright';
import type { Page } from '@playwright/test';
import { expect, showTab, test } from '../fixtures/test';
import { seedBoard, seedLive, type Board } from '../fixtures/board';

// Accessibility: axe on the page's main surfaces — the board with a card
// open, each document tab, each view, the palette and the keys sheet — in
// the light and the dark theme. A serious or critical violation fails the
// test, listed by rule and the elements it found.

let board: Board;

test.use({
  workspaceEnv: { GUMMI_E2E_SLOW_SECONDS: '900' },
  seed: { run: async (ws) => { board = await seedBoard(ws); } },
});

async function audit(page: Page, where: string, found: string[]) {
  const r = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
    .analyze();
  for (const v of r.violations) {
    if (v.impact !== 'serious' && v.impact !== 'critical') continue;
    if (process.env.AXE_DUMP && v.id === 'color-contrast') for (const n of v.nodes) {
      const d: any = n.any[0]?.data || {};
      console.log(`CC ${where.split('/')[0]} ${d.fgColor} on ${d.bgColor} ${d.contrastRatio} :: ${n.target.join(' ')} :: ${n.html.slice(0, 90)}`);
    }
    const targets = v.nodes.slice(0, 4).map((n) => n.target.join(' ')).join(' | ');
    found.push(`${where}: ${v.id} (${v.impact}, ${v.nodes.length}) ${targets}`);
  }
}

async function open(page: Page, id: string, tab?: string) {
  await page.evaluate(([i, t]) => { location.hash = t ? `${i}/${t}` : i; }, [id, tab ?? '']);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await expect(page.getByTestId('thread-loading')).toHaveCount(0);
  await expect(page.getByTestId('panel-loading')).toHaveCount(0);
}

test('the main surfaces pass axe in both themes', async ({ pairedPage: page, server }, info) => {
  test.skip(info.project.name !== 'desktop', 'one viewport is enough for the audit');
  test.setTimeout(300_000);
  await seedLive(page.context().request, server, board);
  await page.reload();
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  const found: string[] = [];
  const b = board;

  for (const theme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' });
    const at = (s: string) => `${theme}/${s}`;

    await open(page, b.failed);
    await audit(page, at('board, failed verify'), found);
    for (const tab of ['spec', 'diff', 'pr', 'stats']) {
      await showTab(page, tab);
      await expect(page.getByTestId('panel-pane')).toHaveAttribute('data-tab', tab);
      await expect(page.getByTestId('panel-loading')).toHaveCount(0);
      await audit(page, at(`tab ${tab}`), found);
    }
    for (const id of [b.gate, b.ask, b.running!, b.freeform!, b.goal, b.landed]) {
      await open(page, id);
      await audit(page, at(`card ${id}`), found);
    }

    await page.getByTestId('btn-palette').click();
    await audit(page, at('palette'), found);
    await page.keyboard.press('Escape');
    await page.getByTestId('btn-keys').click();
    await audit(page, at('keys'), found);
    await page.keyboard.press('Escape');
    await page.getByTestId('card-actions').click();
    await audit(page, at('card menu'), found);
    await page.keyboard.press('Escape');

    const views: Array<[string, () => Promise<void>]> = [
      ['newcard', async () => { await page.getByTestId('rail-new-menu').click(); await page.getByTestId('rail-new').click(); }],
      ['fleet', () => page.getByTestId('rail-fleet').click()],
    ];
    for (const name of ['goals', 'stacks', 'repos', 'ingest', 'bugs', 'doctor']) {
      views.push([name, async () => { await page.getByTestId('rail-more').click(); await page.getByTestId(`menu-${name}`).click(); }]);
    }
    for (const [name, go] of views) {
      await go();
      const v = page.getByTestId(`view-${name}`);
      await expect(v).toBeVisible();
      await expect(v.locator('.spinner')).toHaveCount(0, { timeout: 15_000 });
      await audit(page, at(`view ${name}`), found);
      await page.keyboard.press('Escape');
      await expect(v).toHaveCount(0);
    }
    await page.getByTestId('rail-more').click();
    await page.getByTestId('menu-goals').click();
    await page.getByTestId(`goal-row-${b.goal}`).click();
    await expect(page.getByTestId('view-goal')).toBeVisible();
    await audit(page, at('view goal'), found);
    await page.keyboard.press('Escape');
  }

  console.log(`axe: ${found.length} serious or critical\n${found.join('\n')}`);
  expect(found).toEqual([]);
});
