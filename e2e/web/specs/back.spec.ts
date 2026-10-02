import { expect, test } from '../fixtures/test';

// The browser's back button — a phone's back gesture — steps back inside
// the board before it leaves it: a menu, a dialog or the palette closes
// first, and on a phone the thread and documents return to the cards.
// Only from the page's own root does back leave the site.
test.describe('back', () => {
  let a: string, b: string;
  test.use({ seed: { run: async (ws) => {
    a = await ws.seedDesignGate('Add a wave helper');
    b = await ws.seedDesignGate('Add a nod helper');
  } } });

  test('on a phone it walks back to the cards, closing what is open first', async ({ pairedPage, server }, info) => {
    test.skip(info.project.name !== 'phone', 'the phone’s views');
    // a fresh tab opened on the board, so its history starts at the board
    const page = await pairedPage.context().newPage();
    await page.goto(`${server.url}/#${a}`);
    await expect(page.getByTestId('card-id')).toHaveText(a);
    await expect(page.getByTestId('tab-thread')).toHaveAttribute('aria-selected', 'true');

    // the documents are not a step of their own: back from them is the cards
    await page.getByTestId('tab-diff').click();
    await page.goBack();
    await expect(page.getByTestId('rail')).toBeVisible();
    expect(page.url()).toContain(server.url);

    // a card opened from the cards: back returns to them
    await page.getByTestId(`rail-row-${b}`).click();
    await expect(page.getByTestId('tab-thread')).toHaveAttribute('aria-selected', 'true');
    await expect(page.getByTestId('card-id')).toHaveText(b);
    await page.goBack();
    await expect(page.getByTestId('rail')).toBeVisible();
    // the card stays the one that was open, whatever the address bar held before
    await page.waitForTimeout(600);
    expect(page.url()).toContain(`#${b}`);

    // a menu, then a dialog opened from it: back closes the dialog, and the
    // cards are still there
    await page.getByTestId('rail-more').click();
    await expect(page.getByTestId('rail-more-menu')).toBeVisible();
    await page.goBack();
    await expect(page.getByTestId('rail-more-menu')).toHaveCount(0);
    await page.getByTestId('rail-more').click();
    await page.getByTestId('menu-doctor').click();
    await expect(page.getByTestId('view-doctor')).toBeVisible();
    await page.goBack();
    await expect(page.getByTestId('view-doctor')).toHaveCount(0);
    await expect(page.getByTestId('rail')).toBeVisible();

    // from the cards there is nothing of the board's left to step back
    // through: back would leave the site
    await page.waitForTimeout(300);
    expect(await page.evaluate(() => history.state?.gummiLayer ?? null)).toBeNull();
    expect(await page.goBack()).toBeNull();
    await page.close();
  });

  test('on a desktop it closes the palette and a dialog, and never reverts the card', async ({ pairedPage, server }, info) => {
    test.skip(info.project.name === 'phone', 'the desktop’s overlays');
    const page = await pairedPage.context().newPage();
    await page.goto(`${server.url}/#${a}`);
    await expect(page.getByTestId('card-id')).toHaveText(a);

    // the palette closes on back, and the card is unchanged
    await page.getByTestId('btn-palette').click();
    await expect(page.getByTestId('palette')).toBeVisible();
    await page.goBack();
    await expect(page.getByTestId('palette')).toHaveCount(0);
    await expect(page.getByTestId('card-id')).toHaveText(a);
    expect(page.url()).toContain(`#${a}`);

    // a card picked in the palette stays picked once the palette's own
    // entry has gone
    await page.getByTestId('btn-palette').click();
    await page.getByTestId(`palette-row-${b}`).click();
    await expect(page.getByTestId('card-id')).toHaveText(b);
    await page.waitForTimeout(600);
    await expect(page.getByTestId('card-id')).toHaveText(b);
    expect(page.url()).toContain(`#${b}`);

    // a dialog closed by its × leaves nothing behind: the next back leaves
    await page.getByTestId('rail-more').click();
    await page.getByTestId('menu-doctor').click();
    await expect(page.getByTestId('view-doctor')).toBeVisible();
    await page.getByTestId('view-doctor').getByTestId('modal-close').click();
    await expect(page.getByTestId('view-doctor')).toHaveCount(0);
    await page.waitForTimeout(300);
    expect(await page.evaluate(() => history.state?.gummiLayer ?? null)).toBeNull();
    expect(await page.goBack()).toBeNull();
    await page.close();
  });
});
