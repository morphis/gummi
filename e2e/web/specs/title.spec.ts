import { expect, test } from '../fixtures/test';

// The browser tab's title. A tab strip shows only a title's first few
// characters, so a named instance has to put its name ahead of the word
// every board shares.

test.use({ seed: { run: async (ws) => { await ws.seedDesignGate('Add a wave helper'); } } });

test('a named instance leads the tab title', async ({ pairedPage: page, api }) => {
  await expect(page).toHaveTitle('(1) gummi · repo');
  await api('PUT', '/api/settings', { name: 'east' });
  await page.reload();
  await expect(page.getByTestId('ws-name')).toHaveText('east');
  await expect(page).toHaveTitle('(1) east · gummi · repo');
});
