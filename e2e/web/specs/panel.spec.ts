import { expect, test } from '../fixtures/test';
import { diff, mockCard } from '../fixtures/contract';
import type { Page } from '@playwright/test';

// The right-hand panel's own behaviour against contract-shaped answers
// (fixtures/contract.ts): what a redraw keeps, the comments the diff can
// still not place, editing a comment, "show in diff", a finished card's
// read-only documents, a sticky bar, the tab a person chose, and the
// panel under a new session's draft. The card head and board come from a
// real `gummi web`.

let id: string;
let other: string;
test.use({ seed: { run: async (ws) => { id = await ws.seedDesignGate('Add a wave helper'); [other] = await ws.seedBacklog(['Add a shrug helper']); } } });

test.beforeEach(({}, info) => {
  test.skip(info.project.name === 'phone', 'the panel sits beside the thread here; the phone moves the same tabs into the card screen');
});

// bump is what a "card" event for the open card does to the panel: every
// tab is read again and the open one redrawn (selection.js refresh).
async function bump(page: Page) {
  await page.evaluate(async () => {
    const url = performance.getEntriesByType('resource').map((e) => e.name).find((n) => /\/assets\/store\.js/.test(n));
    const { set, state } = await import(url!);
    set({ cardRev: (state.cardRev || 0) + 1 });
  });
}

test('a half-written note and comment outlive the card changing', async ({ pairedPage: page }) => {
  await mockCard(page, id);
  await page.goto(page.url().replace(/#.*$/, '') + `#${id}/spec`);
  await page.reload();
  const doc = page.getByTestId('spec-doc');
  await doc.getByTestId('spec-comment-0').click();
  await page.getByTestId('spec-note-input').fill('half a thought');
  await expect(page.getByTestId('spec-note-hint')).toContainText('line breaks become spaces');
  await bump(page);
  await expect(page.getByTestId('spec-note-input')).toHaveValue('half a thought');
  await expect(page.getByTestId('spec-note-input')).toBeFocused();

  await page.getByTestId('tab-diff').click();
  await page.getByTestId('diff-line-10').locator('.n').click();
  await page.getByTestId('annotation-input').fill('the brace');
  await bump(page);
  await expect(page.getByTestId('annotation-input')).toHaveValue('the brace');
  await expect(page.getByTestId('annotation-input')).toBeFocused();
  // and the spec's draft is still there when its tab comes back
  await page.getByTestId('tab-spec').click();
  await expect(page.getByTestId('spec-note-input')).toHaveValue('half a thought');
});

test('a comment on a file the diff does not show is drawn and can be resolved', async ({ pairedPage: page }) => {
  const m = await mockCard(page, id);
  m.annotations.push({ id: 2, file: 'fd001.go', idx: -1, excerpt: '+func Fd001()', comment: '@octo: name it?', by: 'octo', source: 'pr', resolved: false });
  m.annotations.push({ id: 3, file: '/dev/null', idx: 6, excerpt: '-// nothing waves', comment: 'why drop this?', by: 'Yuki', source: 'gummi', resolved: false });
  let resolved = 0;
  await page.route(`**/api/cards/${id}/diff/annotations/2/resolve`, async (r) => {
    resolved++;
    m.annotations[1].resolved = true;
    await r.fulfill({ contentType: 'application/json', body: JSON.stringify(diff(m.diffRev, m.annotations)) });
  });
  await page.reload();
  await page.getByTestId('tab-diff').click();
  const box = page.getByTestId('diff-other');
  await expect(box).toContainText('Comments on files not in this diff');
  await expect(box.getByTestId('annotation-2')).toContainText('name it?');
  await expect(box.getByTestId('annotation-where-2')).toHaveText('on fd001.go');
  // a pulled thread is the reviewer's: no edit
  await expect(box.getByTestId('annotation-edit-2')).toHaveCount(0);
  // the comment on a line of the deleted side is drawn in its file, and
  // that file wears the dot
  await expect(page.getByTestId('diff-filebox-0').getByTestId('annotation-3')).toBeVisible();
  await expect(page.getByTestId('diff-file-0').locator('.dot')).toHaveCount(1);
  await expect(page.getByTestId('diff-file-1').locator('.dot')).toHaveCount(0);
  await box.getByTestId('annotation-resolve-2').click();
  await expect(box.getByTestId('annotation-resolve-2')).toHaveText('Reopen');
  expect(resolved).toBe(1);

  // with no code at all, the loose comment is still there to resolve
  await page.route(`**/api/cards/${id}/diff`, (r) => r.fulfill({ contentType: 'application/json', body: JSON.stringify({ ...diff(m.diffRev, m.annotations), files: [] }) }));
  await page.reload();
  await page.getByTestId('tab-diff').click();
  await expect(page.getByTestId('diff-none')).toBeVisible();
  await expect(page.getByTestId('diff-other').getByTestId('annotation-2')).toBeVisible();
});

test('a comment can be edited in place', async ({ pairedPage: page }) => {
  const m = await mockCard(page, id);
  const edits: any[] = [];
  await page.route(`**/api/cards/${id}/diff/annotations/1`, async (r) => {
    if (r.request().method() !== 'PATCH') return r.fallback();
    const body = r.request().postDataJSON();
    edits.push(body);
    m.annotations[0].comment = body.comment;
    await r.fulfill({ contentType: 'application/json', body: JSON.stringify(diff(m.diffRev, m.annotations)) });
  });
  await page.reload();
  await page.getByTestId('tab-diff').click();
  await page.getByTestId('annotation-edit-1').click();
  const input = page.getByTestId('annotation-edit-input-1');
  await expect(input).toBeFocused();
  await expect(input).toHaveValue('Name it WaveAt?');
  await input.fill('Name it WaveTo?');
  await page.getByTestId('annotation-edit-save-1').click();
  await expect(page.getByTestId('annotation-1')).toContainText('Name it WaveTo?');
  expect(edits).toEqual([{ comment: 'Name it WaveTo?' }]);
});

test('show in diff opens the diff at the thread’s line', async ({ pairedPage: page }) => {
  await mockCard(page, id);
  await page.reload();
  await page.getByTestId('tab-pr').click();
  await expect(page.getByTestId('pr-open')).toHaveText('1 open thread');
  await page.getByTestId('pr-thread-show-0').click();
  await expect(page.getByTestId('tab-diff')).toHaveAttribute('aria-selected', 'true');
  // the thread is on wave.go:3, the new side of raw line 8
  await expect(page.getByTestId('diff-line-8')).toHaveClass(/flash/);
  await expect(page.getByTestId('diff-line-8')).toBeInViewport();
});

test('a landed card takes no more review input', async ({ pairedPage: page }) => {
  await mockCard(page, id, { decision: null, patch: (c) => { c.stage = 'done'; c.landed = true; delete c.decision; } });
  await page.goto(page.url().replace(/#.*$/, '') + `#${id}/spec`);
  await page.reload();
  await expect(page.getByTestId('spec-closed')).toContainText('has landed');
  await expect(page.getByTestId('spec-comment-0')).toHaveCount(0);
  await expect(page.getByTestId('spec-note-resolve')).toHaveCount(0);
  await expect(page.getByTestId('spec-request-changes')).toHaveCount(0);
  await page.getByTestId('tab-diff').click();
  await expect(page.getByTestId('diff-pending')).toContainText('still open when it closed');
  await expect(page.getByTestId('diff-request-changes')).toHaveCount(0);
  await expect(page.getByTestId('diff-line-10').locator('[role="button"]')).toHaveCount(0);
  await expect(page.getByTestId('annotation-edit-1')).toHaveCount(0);
  await page.getByTestId('tab-pr').click();
  await expect(page.getByTestId('pr-push')).toContainText('nothing left that needs pushing');
  await expect(page.getByTestId('pr-push-cmd')).toHaveCount(0);
});

test('the open comments’ bar stays in view down a long diff', async ({ pairedPage: page }) => {
  const m = await mockCard(page, id);
  // a long file under the commented one, so the pane scrolls
  await page.route(`**/api/cards/${id}/diff`, (r) => {
    const d: any = diff(m.diffRev, m.annotations);
    d.files.push({ path: 'long.go', add: 300, del: 0, hunks: [{ header: '@@ -0,0 +1,300 @@', lines: Array.from({ length: 300 }, (_, i) => ({ t: '+', new: i + 1, text: `// line ${i + 1}`, idx: 100 + i })) }] });
    return r.fulfill({ contentType: 'application/json', body: JSON.stringify(d) });
  });
  await page.reload();
  await page.getByTestId('tab-diff').click();
  await expect(page.getByTestId('diff-line-399')).toBeAttached();
  await page.locator('#pane').evaluate((el) => { el.scrollTop = el.scrollHeight; });
  await expect(page.getByTestId('diff-request-changes')).toBeInViewport();
});

test('the tab a person picked on a card is where its next visit opens', async ({ pairedPage: page }) => {
  await mockCard(page, id);
  await page.reload();
  // the verify-failed decision is about the diff: the panel follows it,
  // and says so in the address
  await expect(page.getByTestId('tab-diff')).toHaveAttribute('aria-selected', 'true');
  await expect(page).toHaveURL(new RegExp(`#${id}/diff$`));
  await page.getByTestId('tab-log').click();
  await page.getByTestId(`rail-row-${other}`).click();
  await expect(page.getByTestId('card-id')).toHaveText(other);
  await page.getByTestId(`rail-row-${id}`).click();
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await expect(page.getByTestId('tab-log')).toHaveAttribute('aria-selected', 'true');
  await expect(page).toHaveURL(new RegExp(`#${id}/log$`));
});

test('a new session’s draft has no documents, and Cancel goes back', async ({ pairedPage: page }) => {
  await page.goto(page.url().replace(/#.*$/, '') + `#${other}`);
  await expect(page.getByTestId('card-id')).toHaveText(other);
  await page.getByTestId('rail-new-session').click();
  await expect(page.getByTestId('draft-hero')).toBeVisible();
  await expect(page.getByTestId('tab-diff')).toHaveCount(0);
  await expect(page.getByTestId('panel-draft')).toBeVisible();
  expect(new URL(page.url()).hash).toBe('');
  await page.getByTestId('draft-cancel').click();
  await expect(page.getByTestId('card-id')).toHaveText(other);
});
