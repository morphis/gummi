import { expect, test } from '../fixtures/test';
import { shot } from '../fixtures/shots';

// A session (DESIGN §19.8) is started from a draft, not a form: New session
// empties the conversation column, the repository, base and budget sit in
// the composer, the model is picked beside Send, and the first message sent
// is what creates the session and its first turn.
test('a session starts from its first message, on the model picked beside Send', async ({ pairedPage: page, api }, info) => {
  test.setTimeout(90_000);
  const phone = info.project.name === 'phone';
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  if (phone) await page.getByTestId('card-back').click();
  await page.getByTestId('rail-new-session').click();

  await expect(page.getByTestId('draft-hero')).toBeVisible();
  await expect(page.getByTestId('card-title')).toHaveText('New session');
  // nothing is minted until the first message is sent
  expect((await api('GET', '/api/board')).json.rows).toHaveLength(0);
  // the default is the default profile's implementer
  await expect(page.getByTestId('model-picker-btn')).toContainText('e2e-implementer');

  await page.getByTestId('model-picker-btn').click();
  await expect(page.getByTestId('model-picker')).toBeVisible();
  await page.getByTestId('model-search').fill('alt-impl');
  await shot(page, info, 'session-model-picker');
  await page.getByTestId('model-headless-e2e-alt-implementer').click();
  await expect(page.getByTestId('model-picker')).toHaveCount(0);
  await expect(page.getByTestId('model-picker-btn')).toContainText('e2e-alt-implementer');

  await page.getByTestId('draft-budget').click();
  await page.getByTestId('draft-budget-500').click();
  await expect(page.getByTestId('draft-budget')).toContainText('500 cr');

  const input = page.getByTestId('composer-input');
  await input.fill('Tidy the readme.\n\nKeep it to one paragraph.');
  await expect(page.getByTestId('composer-says')).toContainText('starts the session');
  await shot(page, info, 'session-draft');
  await input.press('Enter');

  await expect(page.getByTestId('card-id')).toHaveText(/^FF-/, { timeout: 20_000 });
  const id = (await page.getByTestId('card-id').textContent())!;
  await expect(page.getByTestId('card-title')).toHaveText('Tidy the readme.');
  // an open session has no stages at all, so it shows no badge for them
  await expect(page.getByTestId('card-stages')).toHaveCount(0);
  const card = (await api('GET', `/api/cards/${id}`)).json;
  expect(card.session).toEqual({ backend: 'headless', model: 'e2e-alt-implementer' });
  expect(card.envelope).toBe(500);
  // the whole first message is the first turn, not the title it was cut to
  await expect(page.getByTestId('thread')).toContainText('Keep it to one paragraph.', { timeout: 30_000 });

  // a second draft shows nothing of the card it was opened from, before
  // its first message is sent
  if (phone) return;
  // a session has no spec, so it has no Spec tab
  await expect(page.getByTestId('tab-diff')).toBeVisible();
  await expect(page.getByTestId('tab-spec')).toHaveCount(0);
  await page.getByTestId('tab-stats').click();
  await expect(page.locator('#pane')).not.toBeEmpty();
  await page.getByTestId('rail-new-session').click();
  await expect(page.getByTestId('draft-hero')).toBeVisible();
  await expect(page.locator('#pane')).toBeEmpty();
});

// The model picker's search input takes focus as it opens, which on a phone
// raises the keyboard — and a headless browser stands in for that by
// shrinking the viewport, the same way typing-on-a-phone does. That shrink
// is a window resize, which the popover also closes on when the page moves
// out from under it; it must tell its own keyboard apart from that.
test('the model picker survives the keyboard it raises, on a phone', async ({ pairedPage: page }, info) => {
  test.skip(info.project.name !== 'phone', 'the keyboard is a phone’s');
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  await page.getByTestId('card-back').click();
  await page.getByTestId('rail-new-session').click();
  await expect(page.getByTestId('draft-hero')).toBeVisible();

  const full = page.viewportSize()!;
  await page.getByTestId('model-picker-btn').click();
  await expect(page.getByTestId('model-picker')).toBeVisible();
  await page.setViewportSize({ width: full.width, height: Math.round(full.height * 0.5) });
  await expect(page.getByTestId('model-picker')).toBeVisible();
  await page.getByTestId('model-search').fill('alt-impl');
  await expect(page.getByTestId('model-headless-e2e-alt-implementer')).toBeVisible();

  await page.setViewportSize(full);
});

// A session's model is switched from the same picker mid-conversation: the
// switch is the card's model action, the thread says so, and the next turn
// runs on the new model. A card in the workflow has no picker at all.
test('a session switches its model mid-conversation, and a stage card has no picker', async ({ pairedPage: page, server, api }) => {
  test.setTimeout(90_000);
  const made = await api('POST', '/api/cards', { kind: 'freeform', description: 'Poke at the rounding', backend: 'headless', model: 'e2e-implementer' });
  const id = String(made.json?.id);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  // the opening turn runs and ends before the model can change under it
  await expect(page.getByTestId('composer-says')).not.toContainText('stop this turn', { timeout: 30_000 });
  await expect(page.getByTestId('composer-input')).toHaveAttribute('placeholder', /Message headless/);

  await page.getByTestId('model-picker-btn').click();
  await page.getByTestId('model-headless-e2e-alt-implementer').click();
  await expect(page.getByTestId('model-picker-btn')).toContainText('e2e-alt-implementer');
  await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.session?.model).toBe('e2e-alt-implementer');

  const input = page.getByTestId('composer-input');
  await input.fill('now sum before rounding');
  await input.press('Enter');
  await expect(page.getByTestId('thread')).toContainText('Done: noted it in NOTES.md.', { timeout: 30_000 });

  const feature = String((await api('POST', '/api/cards', { kind: 'feature', title: 'Configurable retries' })).json?.id);
  await page.goto(`${server.url}/#${feature}`);
  await expect(page.getByTestId('card-id')).toHaveText(feature);
  await expect(page.getByTestId('model-picker-btn')).toHaveCount(0);
});

// Writing a spec moves a session into the workflow from its own head: the
// dialog asks for a title, a profile and a budget, the session closes with
// its branch kept, and the page lands on the new feature in its plan stage.
test('a session is continued as a spec from its head', async ({ pairedPage: page, server, api }, info) => {
  test.setTimeout(120_000);
  const made = await api('POST', '/api/cards', { kind: 'freeform', description: 'Poke at the rounding', backend: 'headless', model: 'e2e-implementer' });
  const id = String(made.json?.id);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await expect(page.getByTestId('composer-says')).not.toContainText('stop this turn', { timeout: 30_000 });

  if (info.project.name === 'phone') {
    // no room in a phone's head: the card's menu carries it
    await page.getByTestId('card-actions').click();
    await page.getByTestId('action-writespec').click();
  } else {
    await page.getByTestId('write-spec').click();
  }
  await expect(page.getByTestId('write-spec-dialog')).toBeVisible();
  await page.getByTestId('spec-title').fill('Sum before rounding');
  await page.getByTestId('spec-profile').selectOption('e2e-alt');
  await page.getByTestId('spec-budget').fill('600');
  await shot(page, info, 'session-write-spec');
  await page.getByTestId('spec-start').click();

  await expect(page.getByTestId('card-id')).toHaveText(/^FD-/, { timeout: 30_000 });
  await expect(page.getByTestId('card-title')).toHaveText('Sum before rounding');
  await expect(page.getByTestId('stage-plan')).toHaveAttribute('aria-current', 'step', { timeout: 30_000 });
  const spec = (await page.getByTestId('card-id').textContent())!;
  const card = (await api('GET', `/api/cards/${spec}`)).json;
  expect(card.profile).toBe('e2e-alt');
  expect(card.envelope).toBe(600);
  expect(card.session).toBeUndefined();
  const session = (await api('GET', `/api/cards/${id}`)).json;
  expect(session.stage).toBe('done');
  expect(session.branch).not.toBe(card.branch);
});
