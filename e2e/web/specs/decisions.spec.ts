import { expect, test, pair, type GummiServer } from '../fixtures/test';
import type { Page } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { shot } from '../fixtures/shots';

// Answering a card's pinned decision against a real `gummi web` and the
// scripted agent: the design gate, reworking a plan with a note, sending a
// failed verify back with a diff comment, an agent's question (an option,
// and "chat about this"), the composer's enter line, two people answering
// at once, and an answer given against a card that has since moved. The
// phone answers from the docked decision bar.

const isPhone = (info: { project: { name: string } }) => info.project.name === 'phone';

async function open(page: Page, server: GummiServer, id: string) {
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
}

// answerOption answers the pinned decision with one option: on a phone
// from the docked bar (the documents view shows it), elsewhere from the
// pinned block. One press gives it: what asks first (a landing's message,
// a yes) is the server's to ask.
async function answerOption(page: Page, phone: boolean, option: string) {
  if (phone) {
    await page.getByTestId('tab-diff').click();
    await page.getByTestId('mdec-toggle').click();
    await page.getByTestId(`mdec-option-${option}`).click();
    return;
  }
  await page.getByTestId(`decision-option-${option}`).click();
}

async function thread(page: Page, phone: boolean) {
  if (phone) await page.getByTestId('tab-thread').click();
  return page.getByTestId('thread-items');
}

test.describe('a design gate', () => {
  let id: string;
  test.use({ seed: { run: async (ws) => { id = await ws.seedDesignGate('Add a wave helper'); } } });

  test('approving it moves the card to implement and the thread names who', async ({ pairedPage: page, server }, info) => {
    await open(page, server, id);
    await expect(page.getByTestId('decision')).toHaveAttribute('data-kind', 'gate');
    await expect(page.getByTestId('decision-against')).toContainText('spec ');
    await shot(page, info, 'gate-open');
    if (isPhone(info)) {
      await answerOption(page, true, 'advance');
    } else {
      // the keyboard: a digit picks, enter gives it
      // keys go to the page, not to whatever control the pointer last
      // touched: drop focus rather than click into the thread, whose
      // middle may be a control (enter on one activates it, by design)
      await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
      await page.keyboard.press('3');
      await expect(page.getByTestId('decision-option-pause')).toHaveAttribute('aria-pressed', 'true');
      await page.keyboard.press('ArrowUp');
      await page.keyboard.press('1');
      await expect(page.getByTestId('composer-says')).toHaveText('approve');
      await page.keyboard.press('Enter');
    }
    await expect(page.getByTestId('stage-implement')).toHaveAttribute('aria-current', 'step');
    const items = await thread(page, isPhone(info));
    await expect(items.getByTestId('receipt').last()).toContainText('Tester');
    await shot(page, info, 'gate-approved');
  });

  // One press answers. A second press a moment later, meant for the
  // answer just given, meets the stop that answer led to: it is chosen,
  // not answered unread.
  test('one press approves, and a second press does not answer the next stop', async ({ pairedPage: page, server, api }, info) => {
    test.skip(isPhone(info), 'the phone answers from the docked bar');
    await open(page, server, id);
    await page.getByTestId('decision-option-advance').click();
    await expect(page.getByTestId('stage-implement')).toHaveAttribute('aria-current', 'step');
    const next = (await api('GET', `/api/cards/${id}`)).json.decision;
    await expect(page.getByTestId('decision')).toHaveAttribute('data-ref', next.ref);
    await page.getByTestId('decision').locator('.opt').first().click();
    await expect(page.getByTestId('decision-fresh')).toContainText('new decision');
    await page.waitForTimeout(300);
    expect((await api('GET', `/api/cards/${id}`)).json.decision?.ref).toBe(next.ref);
  });

  test('reworking the plan carries the note to the architect', async ({ pairedPage: page, server, workspace }, info) => {
    await open(page, server, id);
    if (isPhone(info)) await page.getByTestId('tab-thread').click();
    await page.getByTestId('composer-input').fill('Cover an empty name too');
    await expect(page.getByTestId('decision-option-run')).toHaveAttribute('aria-pressed', 'true');
    await expect(page.getByTestId('composer-says')).toHaveText('start the architect with your words');
    await page.getByTestId('composer-send').click();
    await expect(page.getByTestId('composer-input')).toHaveValue('');
    // a line at a gate is read first, and the reading is put to you
    const chip = page.getByTestId('decision');
    await expect(chip).toHaveAttribute('data-kind', 'confirm');
    await expect(page.getByTestId('decision-option-keep')).toBeVisible();
    await shot(page, info, 'gate-chip');
    // going on runs the architect again, which spends: as the terminal's
    // chip wants y rather than enter, the page asks before it goes
    await expect(page.getByTestId('decision-option-go')).toHaveClass(/\bdanger\b/);
    await answerOption(page, false, 'go');
    await expect(page.getByTestId('decision-confirm')).toBeVisible();
    // the question is the server's, and it says what the go does
    await expect(page.getByTestId('decision-confirm-question')).toContainText('spends credits');
    const yes = page.waitForRequest((r) => r.url().endsWith('/answer') && typeof r.postDataJSON()?.confirm === 'string');
    await page.getByTestId('decision-confirm-yes').click();
    expect((await yes).postDataJSON().confirm).toMatch(/^c[0-9a-f]+$/);
    // the architect heard it, and the plan comes back to the gate
    await expect.poll(() => workspace.agentLog().includes('Cover an empty name too')).toBe(true);
    await expect(page.getByTestId('decision')).toHaveAttribute('data-kind', 'gate', { timeout: 30_000 });
    await shot(page, info, 'gate-rework');
  });

  test('a refused approval is said once, in the decision, and nothing covers the composer', async ({ pairedPage: page, server, workspace }, info) => {
    // a plan whose Chosen approach was emptied keeps the gate shut
    const dir = path.join(workspace.repo, '.gummi', 'specs');
    const file = path.join(dir, fs.readdirSync(dir).find((f) => f.startsWith(id))!);
    const body = fs.readFileSync(file, 'utf8');
    fs.writeFileSync(file, body.replace(/(## Chosen approach\n)[\s\S]*?(\n## )/, '$1$2'));
    expect(fs.readFileSync(file, 'utf8')).not.toBe(body);
    await open(page, server, id);
    if (isPhone(info)) await page.getByTestId('tab-thread').click();
    await answerOption(page, false, 'advance');
    const note = page.getByTestId('decision-error');
    await expect(note).toContainText('gate stays shut');
    // the board says the same line to every viewer; this one has it in the
    // note already, so no toast repeats it over the page
    await page.waitForTimeout(1500);
    // (counted now, not polled: a toast goes by itself after a few seconds)
    expect(await page.getByTestId('toast').filter({ hasText: 'gate stays shut' }).count()).toBe(0);
    // and a toast, when there is one, stands clear of the composer
    await page.evaluate(async () => { (await import('/assets/toast.js')).toast('A notice\nof two lines', { ms: 8000 }); });
    const t = await page.getByTestId('toast').last().boundingBox();
    const c = await page.getByTestId('composer').boundingBox();
    expect(t && c && (t.y + t.height <= c.y || t.y >= c.y + c.height), 'the toast does not overlap the composer').toBe(true);
    await shot(page, info, 'gate-refused');
  });

  test('the enter line says what a line would do as it is typed', async ({ pairedPage: page, server }, info) => {
    await open(page, server, id);
    if (isPhone(info)) await page.getByTestId('tab-thread').click();
    const says = page.getByTestId('composer-says');
    await expect(says).toHaveText('approve');
    await page.getByTestId('composer-input').fill('the plan misses the empty name');
    await expect(says).toHaveText('start the architect with your words');
    // a command that is in the card's menu, not one of the answers
    await page.getByTestId('composer-input').fill('/rebase');
    await expect(says).toContainText('menu');
    await expect(page.getByTestId('composer-send')).toHaveText('Send');
    await page.getByTestId('composer-send').click();
    await expect(page.getByTestId('card-actions-menu')).toBeVisible();
    await expect(page.getByTestId('card-actions-menu').locator('button').first()).toHaveAttribute('data-testid', 'action-rebase');
    await shot(page, info, 'composer-menu');
  });

  test('two people answer at once: one wins, the other is told who', async ({ pairedPage: page, server, browser }, info) => {
    const u = info.project.use as any;
    const other = await browser.newContext({ viewport: u.viewport, isMobile: u.isMobile, hasTouch: u.hasTouch, deviceScaleFactor: u.deviceScaleFactor, userAgent: u.userAgent });
    const page2 = await other.newPage();
    try {
      await pair(page2, server, 'Yuki');
      await open(page, server, id);
      await open(page2, server, id);
      const phone = isPhone(info);
      const go = async (p: Page) => {
        if (phone) {
          await p.getByTestId('tab-diff').click();
          await p.getByTestId('mdec-toggle').click();
          return p.getByTestId('mdec-option-advance');
        }
        return p.getByTestId('decision-option-advance');
      };
      const [a, b] = [await go(page), await go(page2)];
      // both give it at once
      await Promise.all([a.click(), b.click()]);
      // the loser is told: who answered, when its answer reached the board
      // second; or that the card moved, when the winner's answer reached
      // its page before its own press did
      const told = (p: Page) => p.getByTestId(phone ? 'mdec-note' : 'decision-answered').or(p.getByTestId('decision-moved'));
      await expect.poll(async () => (await told(page).count()) + (await told(page2).count())).toBe(1);
      const [loser, winner] = (await told(page).count()) ? [page, 'Yuki'] : [page2, 'Tester'];
      await expect(told(loser)).toContainText(new RegExp(`Answered by ${winner}|moved`));
      await shot(loser, info, 'answered-first');
    } finally {
      await other.close();
    }
  });
});

test.describe('a failed verify', () => {
  let id: string;
  test.use({ seed: { run: async (ws) => { id = await ws.seedVerifyFailed('Add a regressing helper'); } } });

  test('a diff comment goes back with the failure', async ({ pairedPage: page, server, api }, info) => {
    const phone = isPhone(info);
    await open(page, server, id);
    if (phone) await page.getByTestId('tab-diff').click();
    await expect(page.getByTestId('tab-diff')).toHaveAttribute('aria-selected', 'true');
    await page.locator('[data-testid^="diff-line-"]').nth(3).locator('.n').click();
    await page.getByTestId('annotation-input').fill('Return early on an empty name');
    await page.getByTestId('annotation-save').click();
    await expect(page.getByTestId('diff-pending')).toContainText('1 comment');
    if (phone) {
      await page.getByTestId('mdec-toggle').click();
      await expect(page.getByTestId('mdec-option-bounce')).toContainText('+ 1 diff comment');
      await shot(page, info, 'verify-carry');
      // the tap sends it back, and the comment goes with it
      await page.getByTestId('mdec-option-bounce').click();
    } else {
      await expect(page.getByTestId('decision-option-bounce')).toContainText('+ 1 diff comment');
      await shot(page, info, 'verify-carry');
      await answerOption(page, false, 'bounce');
    }
    // the card is back in implement, crossed by the person who answered
    await expect(page.getByTestId('stage-implement')).toHaveAttribute('aria-current', 'step');
    await expect.poll(async () => (await api('GET', `/api/cards/${id}/thread`)).json.items
      .some((it: any) => it.t === 'receipt' && /Tester/.test(`${it.receipt?.by} ${it.receipt?.text}`))).toBe(true);
    await shot(page, info, 'verify-sent-back');
    // and the comment goes to the implementer with its next run — which the
    // card, on autopilot, starts itself once it is sent back
    await expect.poll(() => server.ws.agentLog().includes('Return early on an empty name'), { timeout: 20_000 }).toBe(true);
  });

  test('an answer given against a card that moved is refused and re-read', async ({ pairedPage: page, server, workspace }, info) => {
    await open(page, server, id);
    const against = await page.getByTestId('decision-against').textContent();
    // someone commits to the branch under the page
    const wt = workspace.worktree(id);
    await workspace.exec('sh', ['-c', 'echo "// late" >> README.md && git add README.md && git commit -qm late'], { cwd: wt });
    await answerOption(page, isPhone(info), 'pause');
    const note = page.getByTestId(isPhone(info) ? 'mdec-note' : 'decision-moved');
    await expect(note).toContainText('moved since you read it');
    await shot(page, info, 'moved');
    if (!isPhone(info)) {
      await expect(page.getByTestId('decision-against')).not.toHaveText(against || '');
      // read again, the same answer goes through
      await answerOption(page, false, 'pause');
      await expect(page.getByTestId('rail-row-' + id)).toHaveAttribute('data-status', /paused|idle|needs/);
      await expect(page.getByTestId('decision-moved')).toHaveCount(0);
    }
  });
});

test.describe('an agent’s question', () => {
  // A question's options live only in the process that asked it, so the
  // card is started from this board rather than seeded by `gummi run`.
  async function ask(api: any, title = '[ask] Add a choosy helper'): Promise<string> {
    const c = (await api('POST', '/api/cards', { kind: 'feature', title })).json;
    let card = (await api('POST', `/api/cards/${c.id}/answer`, { ref: c.decision.ref, option: 'advance', against: c.decision.against.token })).json;
    card = (await api('POST', `/api/cards/${c.id}/answer`, { ref: card.decision.ref, option: 'run', against: card.decision.against.token })).json;
    await expect.poll(async () => (await api('GET', `/api/cards/${c.id}`)).json.decision?.kind).toBe('ask');
    return c.id;
  }

  test('an option answers it', async ({ pairedPage: page, server, api }, info) => {
    const id = await ask(api);
    await open(page, server, id);
    await expect(page.getByTestId('decision-question')).toContainText('Where should');
    await expect(page.getByTestId('decision-option-chat')).toBeVisible();
    await shot(page, info, 'ask');
    await answerOption(page, isPhone(info), '1');
    const items = await thread(page, isPhone(info));
    await expect(items).toContainText('Extend the existing file');
    await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.decision?.kind).not.toBe('ask');
  });

  // A question long enough to overflow the decision's cap on every
  // viewport: the question yields first (scrolling inside its own region,
  // down to a floor of about two lines) so the answers list never gets
  // pushed past the box's bottom edge.
  test('a long question keeps its answers in reach', async ({ pairedPage: page, server, api }, info) => {
    const id = await ask(api, '[ask-long] Add a wordy helper');
    await open(page, server, id);
    if (isPhone(info)) await page.getByTestId('tab-thread').click();
    const decision = page.getByTestId('decision');
    await expect(decision).toHaveAttribute('data-kind', 'ask');
    const q = page.getByTestId('decision-question');

    // (a) the answers list's rect lies inside the decision box's rect, and
    // is at least as tall as its first two answers; (b) the box itself
    // never scrolls
    const geo = await decision.evaluate((box) => {
      const opts = box.querySelector('.opts')!;
      const first = opts.querySelector('.opt')!.getBoundingClientRect();
      const b = box.getBoundingClientRect();
      const o = opts.getBoundingClientRect();
      return { top: o.top - b.top, bottom: b.bottom - o.bottom, height: o.height, floor: first.height * 2, scrollTop: box.scrollTop };
    });
    expect(geo.top, 'the answers list starts inside the box').toBeGreaterThanOrEqual(-0.5);
    expect(geo.bottom, 'the answers list ends inside the box').toBeGreaterThanOrEqual(-0.5);
    expect(geo.height, 'the answers list keeps room for two answers').toBeGreaterThanOrEqual(geo.floor - 0.5);
    expect(geo.scrollTop, 'the box itself is never scrolled').toBe(0);

    // (c) every answer, scrolled into view within the list, is the element
    // at its own centre point (reachable and tappable)
    const options = decision.locator('.opt');
    const count = await options.count();
    for (let i = 0; i < count; i++) {
      const opt = options.nth(i);
      await opt.scrollIntoViewIfNeeded();
      const hit = await opt.evaluate((el) => {
        const r = el.getBoundingClientRect();
        const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
        return !!at && (at === el || el.contains(at));
      });
      expect(hit, `option ${i} is reachable and tappable`).toBe(true);
    }

    // (d) the question region is scrollable, and scrolled to its end its
    // last line is inside the box
    expect(await q.evaluate((el) => el.scrollHeight > el.clientHeight), 'the question scrolls').toBe(true);
    const qEnd = await q.evaluate((el) => { el.scrollTop = el.scrollHeight; return el.getBoundingClientRect().bottom; });
    const boxBox = (await decision.boundingBox())!;
    expect(qEnd, 'the question, scrolled to its end, is inside the box').toBeLessThanOrEqual(boxBox.y + boxBox.height + 0.5);
    await shot(page, info, 'ask-long');

    // the question's scroll position survives a redraw of the decision (a
    // highlight move), as the answers list's already does
    await q.evaluate((el) => { el.scrollTop = 10 });
    if (isPhone(info)) {
      // tapping the chat answer only highlights it: it needs words before
      // it answers, so this is a redraw with no answer given
      await page.getByTestId('decision-option-chat').click();
    } else {
      // a digit only moves the highlight; enter is what answers
      await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
      await page.keyboard.press('1');
    }
    await expect.poll(() => q.evaluate((el) => el.scrollTop)).toBe(10);
    await expect(decision).toHaveAttribute('data-kind', 'ask');

    // (e) tapping the second answer answers the question (card leaves ask)
    await page.getByTestId('decision-option-2').click();
    await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.decision?.kind).not.toBe('ask');
  });

  test('chat about this answers in your words', async ({ pairedPage: page, server, api }, info) => {
    const id = await ask(api);
    await open(page, server, id);
    if (isPhone(info)) await page.getByTestId('tab-thread').click();
    // the chat row with nothing typed asks for the words
    await page.getByTestId('decision-option-chat').click();
    await expect(page.getByTestId('decision-needs')).toContainText('Type your answer');
    await page.getByTestId('composer-input').fill('Put it beside Greet, in greet.go');
    await expect(page.getByTestId('composer-says')).toHaveText('Chat about this');
    await page.keyboard.press('Enter');
    await expect(page.getByTestId('thread-items')).toContainText('Put it beside Greet, in greet.go');
    await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.decision?.kind).not.toBe('ask');
  });

  // A reply being written to the question while another device answers
  // it: the words stay, the page says the card moved, and the first enter
  // does not send them on to whatever the next decision's words go to (a
  // rework of the plan) without saying so.
  test('words typed for a question someone else answered are not re-aimed unannounced', async ({ pairedPage: page, server, api }, info) => {
    test.skip(isPhone(info), 'enter answers from the composer on a keyboard');
    const id = await ask(api);
    await open(page, server, id);
    await expect(page.getByTestId('decision')).toHaveAttribute('data-kind', 'ask');
    await page.getByTestId('composer-input').fill('Put it beside Greet, in greet.go');
    await expect(page.getByTestId('composer-says')).toHaveText('Chat about this');
    // the other device picks an option
    const d = (await api('GET', `/api/cards/${id}`)).json.decision;
    await api('POST', `/api/cards/${id}/answer`, { ref: d.ref, option: '0', against: d.against.token });
    await expect(page.getByTestId('decision')).not.toHaveAttribute('data-kind', 'ask', { timeout: 30_000 });
    await expect(page.getByTestId('decision-moved')).toContainText('while you were writing');
    await expect(page.getByTestId('composer-input')).toHaveValue('Put it beside Greet, in greet.go');
    const before = (await api('GET', `/api/cards/${id}`)).json.decision?.ref;
    await page.getByTestId('composer-input').press('Enter');
    await expect(page.getByTestId('decision-moved')).toContainText('enter again sends your words');
    await page.waitForTimeout(300);
    expect((await api('GET', `/api/cards/${id}`)).json.decision?.ref).toBe(before);
    await expect(page.getByTestId('composer-input')).toHaveValue('Put it beside Greet, in greet.go');
  });

  // Enter pressed twice on "start the architect" meets the question the
  // architect asks within a moment of the first: the second press must not
  // give that question its first answer before anyone has read it.
  test('a second enter does not answer a question that has just arrived', async ({ pairedPage: page, server, api }, info) => {
    test.skip(isPhone(info), 'enter answers from the composer on a keyboard');
    const c = (await api('POST', '/api/cards', { kind: 'feature', title: '[ask] Add a hasty helper' })).json;
    const card = (await api('POST', `/api/cards/${c.id}/answer`, { ref: c.decision.ref, option: 'advance', against: c.decision.against.token })).json;
    await open(page, server, c.id);
    await expect(page.getByTestId('decision')).toHaveAttribute('data-ref', card.decision.ref);
    await page.getByTestId('composer-input').focus();
    await page.keyboard.press('1');
    await page.keyboard.press('Enter');
    await expect(page.getByTestId('decision')).toHaveAttribute('data-kind', 'ask');
    await page.keyboard.press('Enter');
    await expect(page.getByTestId('decision-fresh')).toContainText('new decision');
    await page.waitForTimeout(300);
    expect((await api('GET', `/api/cards/${c.id}`)).json.decision?.kind).toBe('ask');
    // read, it answers as ever
    await page.waitForTimeout(800);
    await page.keyboard.press('Enter');
    await expect.poll(async () => (await api('GET', `/api/cards/${c.id}`)).json.decision?.kind).not.toBe('ask');
  });
});

// A backend's own MCP client bounds a tool call (a minute, on several of
// them), and a question is a tool call that waits on a person. When the
// bound passes the agent is told its call failed, says so and ends its
// turn, with the question still on the page. A turn that ended on an open
// question has not finished the stage: the card waits where it is, and
// the answer reaches the architect as its next turn.
test.describe('a question the agent stopped waiting on', () => {
  async function ask(api: any): Promise<string> {
    const c = (await api('POST', '/api/cards', { kind: 'feature', title: '[ask-gives-up] Add a patient helper' })).json;
    let card = (await api('POST', `/api/cards/${c.id}/answer`, { ref: c.decision.ref, option: 'advance', against: c.decision.against.token })).json;
    card = (await api('POST', `/api/cards/${c.id}/answer`, { ref: card.decision.ref, option: 'run', against: card.decision.against.token })).json;
    await expect.poll(async () => (await api('GET', `/api/cards/${c.id}`)).json.decision?.kind).toBe('ask');
    return c.id;
  }

  test('stays open, and its answer still reaches the architect', async ({ pairedPage: page, server, api }, info) => {
    const id = await ask(api);
    await open(page, server, id);
    await expect(page.getByTestId('decision-question')).toContainText('Where should');
    // the agent gives up on its call and ends its turn while the reader
    // is still looking at the question
    // (a running stage's turns are drawn under the settled items, so
    // this reads the whole thread)
    if (isPhone(info)) await page.getByTestId('tab-thread').click();
    await expect(page.getByTestId('thread')).toContainText('The ask timed out');
    // long enough for a critique to have started, had the stage been
    // taken for finished
    await page.waitForTimeout(3000);
    const card = (await api('GET', `/api/cards/${id}`)).json;
    expect(card.decision?.kind).toBe('ask');
    expect(card.status).toBe('needs');
    await expect(page.getByTestId('thread')).not.toContainText('critique');
    await expect(page.getByTestId('thread')).not.toContainText('superseded');
    await shot(page, info, 'ask-outlived-its-call');
    if (!isPhone(info)) {
      await expect(page.getByTestId('decision')).toHaveAttribute('data-kind', 'ask');
      await expect(page.getByTestId('decision-question')).toContainText('Where should');
    }

    await answerOption(page, isPhone(info), '1');
    await expect(await thread(page, isPhone(info))).toContainText('Extend the existing file');
    // the architect finishes the plan with the answer, and only then is
    // the plan critiqued and its gate raised
    await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.decision?.kind, { timeout: 30_000 }).toBe('gate');
    const spec = (await api('GET', `/api/cards/${id}/spec`)).json;
    expect(JSON.stringify(spec)).toContain('Decided with the user: Extend the existing file');
  });
});

// A question outlives more than its call: the backend behind it can die
// while the person reads, or the host can restart under it. Either way the
// question stays on the page, and answering it is what brings the card
// back — a backend is started for the answer, which reaches the architect
// as a turn. Both used to refuse every answer (the web has no attach step)
// while writing each try to the log, so the question vanished at the next
// restart and the spec recorded choices nobody had delivered.
test.describe('a question whose backend went away', () => {
  async function ask(api: any, title: string): Promise<string> {
    const c = (await api('POST', '/api/cards', { kind: 'feature', title })).json;
    let card = (await api('POST', `/api/cards/${c.id}/answer`, { ref: c.decision.ref, option: 'advance', against: c.decision.against.token })).json;
    card = (await api('POST', `/api/cards/${c.id}/answer`, { ref: card.decision.ref, option: 'run', against: card.decision.against.token })).json;
    await expect.poll(async () => (await api('GET', `/api/cards/${c.id}`)).json.decision?.kind).toBe('ask');
    return c.id;
  }

  test('a backend that died behind it is replaced by the answer', async ({ pairedPage: page, server, api }, info) => {
    const id = await ask(api, '[ask-dies] Add a fragile helper');
    await open(page, server, id);
    await expect(page.getByTestId('decision-question')).toContainText('Where should');
    // the process dies while the reader is looking at the question
    await expect(page.getByTestId('rail-row-' + id)).toContainText('failed', { timeout: 15_000 });
    expect((await api('GET', `/api/cards/${id}`)).json.decision?.kind).toBe('ask');
    await shot(page, info, 'ask-backend-died');

    await answerOption(page, isPhone(info), '1');
    // the answer opens the new backend's session, a running one: its turns
    // are drawn under the settled items, so this reads the whole thread
    if (isPhone(info)) await page.getByTestId('tab-thread').click();
    await expect(page.getByTestId('thread')).toContainText('Extend the existing file');
    await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.decision?.kind, { timeout: 30_000 }).toBe('gate');
    const spec = JSON.stringify((await api('GET', `/api/cards/${id}/spec`)).json);
    expect(spec).toContain('Decided with the user: Extend the existing file');
    expect(spec).not.toContain('A new file (recommended)');
  });

  test('a restart keeps it, and its answer carries the card on', async ({ pairedPage: page, server, api }, info) => {
    const id = await ask(api, '[ask] Add a durable helper');
    await server.restart();
    await open(page, server, id);
    // the options died with the process that asked; the words are left
    await expect(page.getByTestId('decision-question')).toContainText('Where should');
    await expect(page.getByTestId('decision-option-chat')).toBeVisible();
    await shot(page, info, 'ask-restored');
    if (isPhone(info)) await page.getByTestId('tab-thread').click();
    await page.getByTestId('decision-option-chat').click();
    await page.getByTestId('composer-input').fill('Put it beside Greet, in greet.go');
    await page.keyboard.press('Enter');
    await expect(page.getByTestId('thread')).toContainText('Put it beside Greet, in greet.go');
    await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.decision?.kind, { timeout: 30_000 }).toBe('gate');
    const spec = JSON.stringify((await api('GET', `/api/cards/${id}/spec`)).json);
    expect(spec).toContain('Decided with the user: Put it beside Greet, in greet.go');
  });
});

// A model that polls or sleeps while it waits on its question goes on
// spending, and the card can run out of budget behind the question. The
// question comes first; answering it settles the question and leaves the
// card on its budget stop, which the board used to forget until the next
// restart (offering a plain run in its place). Topping up runs the stage
// again, and that run opens with the answer.
test('a question answered behind a budget stop reaches the run the top-up starts', async ({ pairedPage: page, server, api }, info) => {
  const c = (await api('POST', '/api/cards', { kind: 'feature', title: '[ask-spends] Add a thrifty helper' })).json;
  let card = (await api('POST', `/api/cards/${c.id}/answer`, { ref: c.decision.ref, option: 'advance', against: c.decision.against.token })).json;
  card = (await api('POST', `/api/cards/${c.id}/answer`, { ref: card.decision.ref, option: 'run', against: card.decision.against.token })).json;
  await expect.poll(async () => (await api('GET', `/api/cards/${c.id}`)).json.needs?.kind).toBe('budget');
  await open(page, server, c.id);
  await expect(page.getByTestId('decision-question')).toContainText('Where should');
  await shot(page, info, 'ask-over-budget');

  await answerOption(page, isPhone(info), '1');
  await expect.poll(async () => (await api('GET', `/api/cards/${c.id}`)).json.decision?.kind).toBe('budget');
  expect((await api('GET', `/api/cards/${c.id}`)).json.status).not.toBe('running');
  if (!isPhone(info)) await expect(page.getByTestId('decision')).toHaveAttribute('data-kind', 'budget');
  await shot(page, info, 'budget-after-answer');

  const stop = (await api('GET', `/api/cards/${c.id}`)).json.decision;
  let r = await api('POST', `/api/cards/${c.id}/answer`, { ref: stop.ref, option: 'topup', against: stop.against.token });
  if (r.status === 409 && r.json.confirm) {
    r = await api('POST', `/api/cards/${c.id}/answer`, { ref: stop.ref, option: 'topup', against: stop.against.token, confirm: r.json.confirm });
  }
  expect(r.status).toBe(200);
  await expect.poll(async () => (await api('GET', `/api/cards/${c.id}`)).json.decision?.kind, { timeout: 30_000 }).toBe('gate');
  const spec = JSON.stringify((await api('GET', `/api/cards/${c.id}/spec`)).json);
  expect(spec).toContain('Decided with the user: Extend the existing file');
});


test.describe('a verified card', () => {
  let id: string;
  test.use({ seed: { run: async (ws) => { id = await ws.seedVerified('Add a parting helper'); } } });

  // A confirm-gated answer stops on the board's own question (the TUI's
  // y/n). The question and its buttons are drawn beside the decision, not
  // inside its capped box: inside it they were clipped under the composer
  // on a phone, and nobody could hand a card off by touch.
  test('handing it off asks first, and the question can be answered by touch', async ({ pairedPage: page, server, api }, info) => {
    await open(page, server, id);
    if (isPhone(info)) await page.getByTestId('tab-thread').click();
    const opt = page.getByTestId('decision-option-handoff');
    await expect(opt).toBeVisible();
    await opt.click();
    const confirm = page.getByTestId('decision-confirm');
    await expect(confirm).toBeVisible();
    // the board's question, with its lines kept as the board wrote them
    await expect(page.getByTestId('decision-confirm-question')).toContainText(`Hand off ${id}`);
    await expect(page.getByTestId('decision-confirm-question')).toHaveCSS('white-space', 'pre-line');
    await expect(confirm).toBeFocused();
    await shot(page, info, 'handoff-confirm');
    // both buttons are on screen and nothing stands over them
    for (const tid of ['decision-confirm-no', 'decision-confirm-yes']) {
      const b = page.getByTestId(tid);
      await expect(b).toBeInViewport({ ratio: 1 });
      const hit = await b.evaluate((el) => {
        const r = el.getBoundingClientRect();
        const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
        return !!at && (at === el || el.contains(at));
      });
      expect(hit, `${tid} is the element under its own centre`).toBe(true);
    }
    if (isPhone(info)) await page.getByTestId('decision-confirm-yes').tap();
    else await page.getByTestId('decision-confirm-yes').click();
    await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.stage).toBe('done');
    await expect(page.getByTestId('decision-confirm')).toHaveCount(0);
  });
});

// A question that takes several answers, on a phone: tapping answers in
// the docked bar only ticks them, and a phone has no enter, so the bar
// must carry its own way to send them.
test('a phone sends a multi-pick answer from the docked bar', async ({ pairedPage: page, server, api }, info) => {
  test.skip(!isPhone(info), 'the docked bar is the phone’s');
  const c = (await api('POST', '/api/cards', { kind: 'feature', title: '[ask-multi] Add a plural helper' })).json;
  let card = (await api('POST', `/api/cards/${c.id}/answer`, { ref: c.decision.ref, option: 'advance', against: c.decision.against.token })).json;
  card = (await api('POST', `/api/cards/${c.id}/answer`, { ref: card.decision.ref, option: 'run', against: card.decision.against.token })).json;
  await expect.poll(async () => (await api('GET', `/api/cards/${c.id}`)).json.decision?.multi).toBe(true);
  await open(page, server, c.id);
  await page.getByTestId('tab-diff').click();
  await page.getByTestId('mdec-toggle').click();
  await expect(page.getByTestId('mdec-send')).toBeDisabled();
  await page.getByTestId('mdec-option-0').click();
  await page.getByTestId('mdec-option-1').click();
  await expect(page.getByTestId('mdec-send')).toContainText('2 choices');
  await shot(page, info, 'mdec-multi');
  await page.getByTestId('mdec-send').click();
  await expect.poll(async () => (await api('GET', `/api/cards/${c.id}`)).json.decision?.kind, { timeout: 30_000 }).not.toBe('ask');
});
