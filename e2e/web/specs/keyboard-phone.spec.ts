import { expect, test } from '../fixtures/test';
import { shot } from '../fixtures/shots';

// A phone's keyboard takes about half the screen. While a line is being
// written the page keeps what matters above it — the conversation, the
// pinned decision's question and the composer with its Send — and moves
// the nav and the top bar out of the way. A headless browser has no
// keyboard, so the test does what one does to the page: the composer
// takes focus and the visible viewport shrinks.
test.describe('typing on a phone', () => {
  let id: string;
  test.use({ seed: { run: async (ws) => { id = await ws.seedDesignGate('Add a wave helper'); } } });

  test('the composer and Send stay on screen above the keyboard', async ({ pairedPage: page, server }, info) => {
    test.skip(info.project.name !== 'phone', 'the typing layout is the phone’s');
    await page.goto(`${server.url}/#${id}`);
    await expect(page.getByTestId('card-id')).toHaveText(id);
    await expect(page.getByTestId('decision')).toBeVisible();
    const full = page.viewportSize()!;

    await page.getByTestId('composer-input').click();
    await page.getByTestId('composer-input').fill('What is the plan for the tests?');
    // the keyboard arrives
    const kb = { width: full.width, height: Math.round(full.height * 0.5) };
    await page.setViewportSize(kb);
    await expect(page.locator('html')).toHaveClass(/\bkb\b/);
    await expect(page.getByTestId('panel-tabs')).toBeHidden();
    await expect(page.getByTestId('topbar')).toBeHidden();

    // everything the person needs is inside what the keyboard leaves
    for (const tid of ['composer-input', 'composer-send', 'decision-question', 'card-id']) {
      const box = await page.getByTestId(tid).boundingBox();
      expect(box, `${tid} is on screen`).not.toBeNull();
      expect(box!.y, `${tid} starts on screen`).toBeGreaterThanOrEqual(0);
      expect(box!.y + box!.height, `${tid} ends above the keyboard`).toBeLessThanOrEqual(kb.height + 0.5);
    }
    // and the conversation still has room to be read
    const th = await page.getByTestId('thread').boundingBox();
    expect(th!.height).toBeGreaterThan(80);
    // nothing scrolls the page itself sideways or down
    expect(await page.evaluate(() => [document.scrollingElement!.scrollTop, document.scrollingElement!.scrollLeft])).toEqual([0, 0]);
    await shot(page, info, 'typing-keyboard');

    // the keyboard goes away: the tabs and the top bar are back
    await page.getByTestId('composer-input').blur();
    await page.setViewportSize(full);
    await expect(page.locator('html')).not.toHaveClass(/\bkb\b/);
    await expect(page.getByTestId('panel-tabs')).toBeVisible();
    await expect(page.getByTestId('topbar')).toBeVisible();
  });
});
