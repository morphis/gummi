import type { Page } from '@playwright/test';
import { expect, test, type GummiServer } from '../fixtures/test';
import { seedGoalAtPlan } from '../fixtures/goals';
import { shot } from '../fixtures/shots';

// Two answers the terminal gives in a dialog of its own, given from the
// page: a goal's plan gate, approved from the goal's card, and a pull
// request linked to a verified card (fake gh answers for #7).

async function open(page: Page, server: GummiServer, id: string, phone: boolean) {
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  if (phone) await page.getByTestId('tab-thread').click();
}

const phone = (info: { project: { name: string } }) => info.project.name === 'phone';

test.describe('a goal at its plan gate', () => {
  let goal: string;
  test.use({ seed: { run: async (ws) => { goal = await seedGoalAtPlan(ws, 'Greet in two languages'); } } });

  test('is approved from its card, as the terminal approves it', async ({ pairedPage: page, server, api }, info) => {
    await open(page, server, goal, phone(info));
    const d = page.getByTestId('decision');
    await expect(d).toHaveAttribute('data-kind', 'gate');
    await expect(page.getByTestId('decision-option-advance')).toContainText('approve');
    // the head leads to the goal's own page
    await expect(page.getByTestId('card-goal')).toHaveText('goal page');
    await shot(page, info, 'goal-card-gate');
    // approve is the highlighted answer, and one press gives it
    await expect(page.getByTestId('decision-option-advance')).toHaveClass(/\bhi\b/);
    await page.getByTestId('decision-option-advance').click();
    // past its plan gate the lead takes it from here (the scripted one is
    // quick), as far as it may go without a person: verify, ready for
    // you. Landing on main is a person's decision — approving the plan
    // must never carry the goal through its landing.
    await expect.poll(async () => (await api('GET', `/api/cards/${goal}`)).json.stage, { timeout: 30_000 }).toBe('verify');
    await page.waitForTimeout(1500);
    const parked = (await api('GET', `/api/cards/${goal}`)).json;
    expect(parked.stage).toBe('verify');
    expect(parked.decision?.kind).toMatch(/^(gate|verify)$/);
    expect(JSON.stringify((await api('GET', `/api/cards/${goal}/thread`)).json.items)).toContain('Tester');
    // landing it is a second, deliberate answer, and it is recorded as the
    // person who gave it
    await expect(page.getByTestId('decision-option-advance')).toContainText(/land/i);
    await page.getByTestId('decision-option-advance').click();
    // the landing shows its drafted message, and lands on its own button
    await expect(page.getByTestId('landing-dialog')).toBeVisible({ timeout: 60_000 });
    await expect(page.getByTestId('landing-message')).not.toHaveValue('');
    await page.getByTestId('landing-confirm').click();
    await expect.poll(async () => (await api('GET', `/api/cards/${goal}`)).json.stage, { timeout: 60_000 }).toBe('done');
    const thread = (await api('GET', `/api/cards/${goal}/thread`)).json;
    const landing = (thread.items as any[]).filter((it) => it.t === 'receipt' && it.receipt?.kind === 'gate').pop();
    expect(JSON.stringify(landing ?? thread.items)).toContain('Tester');
  });
});

test.describe('a verified card', () => {
  let id: string;
  test.use({ seed: { run: async (ws) => { id = await ws.seedVerified('Add a wave helper'); } } });

  test('links a pull request by its number', async ({ pairedPage: page, server, api, workspace }, info) => {
    await open(page, server, id, phone(info));
    await page.getByTestId('tab-pr').click();
    await expect(page.getByTestId('pr-none')).toBeVisible();
    // the PR tab offers the link the card's menu has
    await page.getByTestId('pr-link').click();
    const dialog = page.getByTestId('action-dialog');
    await expect(dialog).toBeVisible();
    await expect(page.getByTestId('action-question')).toContainText('URL or number');
    await page.getByTestId('action-input').fill('7');
    await shot(page, info, 'pr-link');
    // the lookup is GitHub's to answer: while it is out a notice says what
    // the board is waiting on
    const answer = await workspace.holdGh('pr', 'view');
    await page.getByTestId('action-confirm').click();
    await expect(page.getByTestId('work-toast')).toContainText(`${id}: reading pull request 7 from GitHub`, { timeout: 15_000 });
    await shot(page, info, 'pr-link-reading');
    await answer();
    await expect(dialog).toHaveCount(0, { timeout: 30_000 });
    await expect(page.getByTestId('work-toast')).toHaveCount(0, { timeout: 15_000 });
    await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.pr).toBeTruthy();
    expect(workspace.ghCalls().some((a) => a.includes('pr') && a.includes('7'))).toBe(true);
    await expect(page.getByTestId('pr-state')).toBeVisible({ timeout: 15_000 });
    // linked once is linked: the menu no longer offers it
    const card = (await api('GET', `/api/cards/${id}`)).json;
    expect(card.actions.map((a: any) => a.id)).not.toContain('prlink');
  });
});
