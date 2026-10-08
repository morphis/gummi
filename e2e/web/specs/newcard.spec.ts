import { expect, test } from '../fixtures/test';
import type { Page } from '@playwright/test';
import { shot } from '../fixtures/shots';

// The new-card form against a real `gummi web`: the choices come from
// GET /api/form, the card from POST /api/cards (the TUI's own form,
// filled in and submitted), and a refusal is shown beside its field in the
// form's own words.

let gate: string;
let backlog: string[];
test.use({ seed: { run: async (ws) => { gate = await ws.seedDesignGate('Add a wave helper'); backlog = await ws.seedBacklog(['Add a shrug helper']); } } });

async function openForm(page: Page) {
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  await page.getByTestId('rail-new').click();
  await expect(page.getByTestId('view-newcard')).toBeVisible();
  await expect(page.getByTestId('newcard-title')).toBeVisible();
}

const phone = (info: { project: { name: string } }) => info.project.name === 'phone';

test('a feature that waits on one card and stacks on another', async ({ pairedPage: page, api }, info) => {
  await openForm(page);
  // nothing typed: the form's own refusal, beside the title
  await page.getByTestId('newcard-create').click();
  await expect(page.getByTestId('newcard-error-title')).toHaveText('A card needs a title');
  await page.getByTestId('newcard-title').fill('Add a farewell helper');
  await page.getByTestId('newcard-desc').fill('Greet has no goodbye. Add Farewell(name) beside it, with a test.');
  await expect(page.getByTestId('newcard-envelope')).toHaveValue('20');
  await page.getByTestId('newcard-envelope').fill('3');
  await page.getByTestId('newcard-profile').selectOption('e2e-alt');
  await page.getByTestId(`newcard-after-${backlog[0]}`).check();
  await page.getByTestId('newcard-stack').selectOption(gate);
  await shot(page, info, 'newcard-feature');
  await page.getByTestId('newcard-create').click();
  await expect(page.getByTestId('view-newcard')).toHaveCount(0);
  await expect(page.getByTestId('card-title')).toHaveText('Add a farewell helper');
  const id = (await page.getByTestId('card-id').textContent())!;
  const card = (await api('GET', `/api/cards/${id}`)).json;
  expect(card).toMatchObject({ kind: 'feature', stage: 'todo', envelope: 300, profile: 'e2e-alt' });
  expect(card.stack).toMatchObject({ pos: 1, of: 2 });
  expect(await api('GET', '/api/form').then((r) => r.json.dependable.map((c: any) => c.id))).toContain(id);
  if (phone(info)) await page.getByTestId('card-back').click();
  await expect(page.getByTestId(`rail-row-${id}`)).toContainText('stack 2 of 2');
  const deps = await server_deps(api, id);
  expect(deps).toEqual(backlog[0]);
});

// server_deps reads a card's dependencies back through its menu's default.
async function server_deps(api: any, id: string): Promise<string> {
  const card = (await api('GET', `/api/cards/${id}`)).json;
  return card.actions.find((a: any) => a.id === 'deps')?.default ?? '';
}

test('a bug with its severity and report', async ({ pairedPage: page, api }, info) => {
  await openForm(page);
  await page.getByTestId('newcard-kind-bug').click();
  await expect(page.getByTestId('newcard-about')).toContainText('defect');
  await page.getByTestId('newcard-title').fill('Greet panics on an empty name');
  await page.getByTestId('newcard-desc').fill('Greet("") panics in the command.');
  await page.getByTestId('newcard-severity').selectOption('high');
  await page.getByTestId('newcard-repro').fill('go run ./cmd/tiny ""');
  await page.getByTestId('newcard-expected').fill('hello, ');
  await page.getByTestId('newcard-actual').fill('a panic');
  await shot(page, info, 'newcard-bug');
  await page.getByTestId('newcard-create').click();
  await expect(page.getByTestId('card-title')).toHaveText('Greet panics on an empty name');
  const id = (await page.getByTestId('card-id').textContent())!;
  expect(id).toMatch(/^BG-/);
  expect((await api('GET', `/api/cards/${id}`)).json.severity).toBe('high');
  expect((await api('GET', `/api/board`)).json.rows.find((r: any) => r.id === id).severity).toBe('high');
});

test('a freeform card opens at once', async ({ pairedPage: page }, info) => {
  await openForm(page);
  await page.getByTestId('newcard-kind-freeform').click();
  await expect(page.getByTestId('newcard-autopilot')).toHaveCount(0);
  await expect(page.getByTestId('newcard-adopt')).toHaveCount(0);
  await page.getByTestId('newcard-title').fill('Tidy the readme');
  await page.getByTestId('newcard-create').click();
  await expect(page.getByTestId('card-title')).toHaveText('Tidy the readme');
  await expect(page.getByTestId('card-id')).toHaveText(/^FF-/);
  // an open session has no stages at all, so it shows no badge for them
  await expect(page.getByTestId('card-stages')).toHaveCount(0);
  if (phone(info)) await page.getByTestId('tab-thread').click();
  await page.getByTestId('composer-input').fill('Add a line about the command');
  await expect(page.getByTestId('composer-says')).toContainText('a turn for this card');
  await shot(page, info, 'newcard-freeform');
});

test('a research card, and a diagnosis', async ({ pairedPage: page, api }, info) => {
  await openForm(page);
  await page.getByTestId('newcard-kind-research').click();
  await expect(page.getByTestId('newcard-stack')).toHaveCount(0);
  await page.getByTestId('newcard-title').fill('How does the module greet');
  await page.getByTestId('newcard-desc').fill('Which calls reach Greet, and with what?');
  // a budget the form refuses, in its own words, beside the budget
  await page.getByTestId('newcard-envelope').fill('-5');
  await page.getByTestId('newcard-create').click();
  await expect(page.getByTestId('newcard-error-envelope')).toContainText('negative');
  await page.getByTestId('newcard-envelope').fill('1.5');
  await page.getByTestId('newcard-create').click();
  await expect(page.getByTestId('card-id')).toHaveText(/^RS-/);
  const rs = (await page.getByTestId('card-id').textContent())!;
  expect((await api('GET', `/api/cards/${rs}`)).json).toMatchObject({ kind: 'research', envelope: 150 });
  // the created card opened; back to the cards for the next form
  if (phone(info)) await page.getByTestId('card-back').click();

  await openForm(page);
  await page.getByTestId('newcard-kind-research-diagnosis').click();
  await page.getByTestId('newcard-title').fill('Why does the build print twice');
  await page.getByTestId('newcard-create').click();
  await expect(page.getByTestId('card-title')).toHaveText('Why does the build print twice');
});

test('create & autopilot hands the new card to autopilot', async ({ pairedPage: page, api }, info) => {
  await openForm(page);
  await page.getByTestId('newcard-title').fill('Add a wink helper');
  await page.getByTestId('newcard-autopilot').click();
  await expect(page.getByTestId('card-title')).toHaveText('Add a wink helper');
  const id = (await page.getByTestId('card-id').textContent())!;
  await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.autopilot).toBe(true);
});

// A title in any script names a card: Latin letters with marks fold into
// the branch name, and a script that does not fold still gets one.
test('a title in any script makes a card', async ({ pairedPage: page, api }, info) => {
  for (const [title, branch] of [['Привет мир', /\/card-[0-9a-f]{8}$/], ['Café résumé', /\/cafe-resume$/]] as const) {
    await openForm(page);
    await page.getByTestId('newcard-title').fill(title);
    await page.getByTestId('newcard-desc').fill('Say hello in the reader’s own words.');
    await page.getByTestId('newcard-create').click();
    await expect(page.getByTestId('card-title')).toHaveText(title);
    const id = (await page.getByTestId('card-id').innerText()).trim();
    expect((await api('GET', `/api/cards/${id}`)).json.branch).toMatch(branch);
    // the created card opened; back to the cards for the next one
    if (phone(info)) await page.getByTestId('card-back').click();
  }
});

// Adopting: a branch that would be refused is listed only to say why — the
// one the card would land on, one another card holds — a refusal the list
// cannot foresee sits under the adopt field (said once, not again as a
// toast over the form), and a stacked card's base steps aside.
test('adopt refusals sit under their field, and a stack sets the base', async ({ pairedPage: page, api }, info) => {
  test.skip(phone(info), 'the form’s wiring is the same at every width');
  await openForm(page);
  const head = (await api('GET', '/api/board')).json.head;
  const adopt = page.getByTestId('newcard-adopt');
  await expect(adopt.locator(`option[value="${head}"]`)).toHaveAttribute('disabled', '');
  const branch = (await api('GET', `/api/cards/${gate}`)).json.branch;
  const held = adopt.locator(`option[value="${branch}"]`);
  await expect(held).toHaveAttribute('disabled', '');
  await expect(held).toHaveText(`${branch} — ${gate} has it`);

  await page.getByTestId('newcard-stack').selectOption(gate);
  await expect(page.getByTestId('newcard-base')).toBeDisabled();
  await expect(page.getByTestId('newcard-base-hint')).toContainText(gate);
  await page.getByTestId('newcard-stack').selectOption('');
  await expect(page.getByTestId('newcard-base')).toBeEnabled();

  // forked from the held branch, the trunk carries nothing of its own to
  // adopt: the list measured against the default base, so the create says it
  await page.getByTestId('newcard-title').fill('Take over the trunk');
  await page.getByTestId('newcard-base').selectOption(branch);
  await expect(held).toHaveAttribute('disabled', '');
  await adopt.selectOption(head);
  await page.getByTestId('newcard-create').click();
  await expect(page.getByTestId('newcard-error-adopt')).toContainText('nothing to adopt');
  await expect(page.getByTestId('newcard-error-base')).toBeHidden();
  await page.waitForTimeout(800);
  await expect(page.getByTestId('toast').filter({ hasText: 'nothing to adopt' })).toHaveCount(0);
});
