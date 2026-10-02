import { expect, test } from '../fixtures/test';
import { shot } from '../fixtures/shots';

// The running turn is drawn apart from the settled thread, and it must
// read like it: its prompt, tool calls, message and status line spaced as
// the settled items are, and the status line's spinner in the avatar
// column, on the first line of the words beside it.

test.use({ workspaceEnv: { GUMMI_E2E_SLOW_SECONDS: '30' } });

test('the live turn is spaced and aligned as the thread is', async ({ pairedPage: page, server, api }, info) => {
  const c = (await api('POST', '/api/cards', { kind: 'feature', title: '[slow] Add a lively helper' })).json;
  const card = (await api('POST', `/api/cards/${c.id}/answer`, { ref: c.decision.ref, option: 'advance', against: c.decision.against.token })).json;
  await page.goto(`${server.url}/#${c.id}`);
  await expect(page.getByTestId('card-id')).toHaveText(c.id);
  if (info.project.name === 'phone') await page.getByTestId('tab-thread').click();
  await api('POST', `/api/cards/${c.id}/answer`, { ref: card.decision.ref, option: 'run', against: card.decision.against.token });
  await expect(page.getByTestId('live-streaming')).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId('live')).toBeVisible();

  const geo = await page.evaluate(() => {
    const live = document.querySelector('#thread-live')!;
    const kids = [...live.children].map((e) => e.getBoundingClientRect());
    const gaps = kids.slice(1).map((r, i) => Math.round(r.top - kids[i].bottom));
    const av = document.querySelector('[data-testid="live-streaming"] .av')!.getBoundingClientRect();
    const line = document.querySelector('[data-testid="live"]')!;
    const sp = line.querySelector('.spinner') as HTMLElement;
    const words = line.querySelector('.spinner + span')!.getBoundingClientRect();
    // the spinner's own box, not its rotating bounds
    const spLeft = sp.offsetLeft - (line as HTMLElement).offsetLeft;
    const spTop = sp.offsetTop - (line as HTMLElement).offsetTop;
    return {
      gaps,
      spinnerCentre: line.getBoundingClientRect().left + spLeft + sp.offsetWidth / 2,
      avCentre: av.left + av.width / 2,
      spinnerTop: spTop,
      lineHeight: parseFloat(getComputedStyle(line).lineHeight),
      wordsLeft: words.left,
      bodyLeft: document.querySelector('[data-testid="live-streaming"] .body')!.getBoundingClientRect().left,
    };
  });
  for (const g of geo.gaps) expect(g).toBeGreaterThanOrEqual(10);
  expect(Math.abs(geo.spinnerCentre - geo.avCentre)).toBeLessThanOrEqual(1.5);
  expect(geo.spinnerTop).toBeLessThan(geo.lineHeight);
  expect(Math.abs(geo.wordsLeft - geo.bodyLeft)).toBeLessThanOrEqual(1.5);
  await shot(page, info, 'thread-live');
});
