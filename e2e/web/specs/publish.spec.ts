import fs from 'node:fs';
import path from 'node:path';
import type { Page } from '@playwright/test';
import { expect, showTab, test, type GummiServer, type Workspace } from '../fixtures/test';
import { shot } from '../fixtures/shots';

// Publishing a card from the page (DESIGN §22): a person pushes the card's
// branch and opens, updates, readies and drafts its pull request. GitHub is
// two stand-ins: fake-gh keeps the pull requests, and fake-ssh serves a
// bare repository as git@github.com:e2e/tiny.git, so every push here is a
// real `git push` to a real github.com URL.

const phone = (info: { project: { name: string } }) => info.project.name === 'phone';

async function openPR(page: Page, server: GummiServer, id: string, isPhone: boolean) {
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  if (isPhone) await page.getByTestId('tab-thread').click();
  await showTab(page, 'pr');
}

async function branchOf(ws: Workspace, id: string): Promise<string> {
  return (await ws.exec('git', ['rev-parse', '--abbrev-ref', 'HEAD'], { cwd: ws.worktree(id) })).stdout.trim();
}

async function tipOf(ws: Workspace, id: string): Promise<string> {
  return (await ws.exec('git', ['rev-parse', 'HEAD'], { cwd: ws.worktree(id) })).stdout.trim();
}

/** The branch's tip on the stand-in github.com, "" when it is not there. */
async function remoteTip(ws: Workspace, branch: string): Promise<string> {
  const r = await ws.exec('git', ['--git-dir', ws.remotePath(), 'rev-parse', '--verify', '-q', `refs/heads/${branch}`]);
  return r.code === 0 ? r.stdout.trim() : '';
}

/** One more commit on the card's branch, as a person fixing something by hand makes. */
async function commitOn(ws: Workspace, id: string, subject: string): Promise<void> {
  const cwd = ws.worktree(id);
  await fs.promises.appendFile(path.join(cwd, 'README.md'), `\n${subject}.\n`);
  for (const args of [['add', '-A'], ['commit', '-q', '-m', subject]]) {
    const r = await ws.exec('git', args, { cwd });
    if (r.code !== 0) throw new Error(`git ${args.join(' ')}: ${r.stderr}`);
  }
}

test.describe('a verified card on a repository with a github.com remote', () => {
  let id: string;
  test.use({ seed: { run: async (ws) => { await ws.addGitHubRemote(); id = await ws.seedVerified('Add a wave helper'); } } });

  test('is opened as a pull request, updated and readied from the page', async ({ pairedPage: page, server, api, workspace }, info) => {
    test.setTimeout(120_000);
    const branch = await branchOf(workspace, id);
    const tip = await tipOf(workspace, id);
    await openPR(page, server, id, phone(info));

    // nothing is on GitHub yet: the tab offers to open one, and so does
    // the card's head where there is room for it
    await expect(page.getByTestId('pr-publish-create')).toBeVisible({ timeout: 20_000 });
    // (beside an open surface a laptop's head folds it into the card's menu)
    if (info.project.name === 'desktop') await expect(page.getByTestId('card-publish')).toBeVisible();
    await shot(page, info, 'publish-1-offer');

    // the confirm: one sentence, the words the PR opens with, and the
    // resolved facts behind a fold
    await page.getByTestId('pr-publish-create').click();
    const dialog = page.getByTestId('publish-dialog');
    await expect(dialog).toBeVisible({ timeout: 20_000 });
    await expect(page.getByTestId('publish-summary')).not.toBeEmpty();
    await expect(page.getByTestId('publish-title')).not.toHaveValue('');
    // a verified tip may open ready: the draft box is the person's
    await expect(page.getByTestId('publish-draft')).toBeEnabled();
    await shot(page, info, 'publish-2-create');
    await page.getByTestId('publish-details').locator('summary').click();
    await expect(page.getByTestId('publish-commands')).toContainText('git push');
    await expect(page.getByTestId('publish-commands')).toContainText('gh pr create');
    await expect(page.getByTestId('publish-details')).toContainText('git@github.com:e2e/tiny.git');
    await shot(page, info, 'publish-3-create-details');

    await page.getByTestId('publish-title').fill('Add a wave helper');
    await page.getByTestId('publish-confirm').click();
    await expect(dialog).toHaveCount(0, { timeout: 30_000 });
    // the branch is on the remote at the tip that was shown, the PR
    // exists, and the card is linked to it
    expect(await remoteTip(workspace, branch)).toBe(tip);
    const created = workspace.ghCalls().find((a) => a[0] === 'pr' && a[1] === 'create');
    expect(created).toBeTruthy();
    expect(created).toEqual(expect.arrayContaining(['--repo', 'e2e/tiny', '--head', `e2e:${branch}`, '--base', 'main', '--title=Add a wave helper']));
    expect(created).not.toContain('--draft');
    await expect.poll(async () => (await api('GET', `/api/cards/${id}`)).json.pr).toBeTruthy();
    await expect(page.getByTestId('pr-state')).toBeVisible({ timeout: 15_000 });
    await expect(page.getByTestId('pr-publish')).toContainText('ready for review');
    await shot(page, info, 'publish-4-opened');

    // a commit made since is not on GitHub, and it is not verified: the
    // one push on offer returns the ready PR to draft first
    await commitOn(workspace, id, 'Fix a typo by hand');
    await page.getByTestId('pr-refresh').click();
    await expect(page.getByTestId('pr-publish')).toContainText('1 commit not on GitHub', { timeout: 15_000 });
    await shot(page, info, 'publish-5-unpushed');
    await page.getByTestId('pr-publish').getByRole('button').first().click();
    await expect(dialog).toBeVisible({ timeout: 20_000 });
    await expect(page.getByTestId('publish-confirm')).toHaveText('Push and return to draft');
    await page.getByTestId('publish-details').locator('summary').click();
    await shot(page, info, 'publish-6-update');
    await page.getByTestId('publish-confirm').click();
    await expect(dialog).toHaveCount(0, { timeout: 30_000 });
    expect(await remoteTip(workspace, branch)).toBe(await tipOf(workspace, id));
    expect(workspace.ghCalls().some((a) => a[0] === 'pr' && a[1] === 'ready' && a.includes('--undo'))).toBe(true);
    await expect(page.getByTestId('pr-publish')).toContainText('draft', { timeout: 15_000 });
    await expect(page.getByTestId('pr-publish')).not.toContainText('not on GitHub');

    // the floor: a tip nobody verified is not marked ready from here, and
    // the strip says why instead of offering it
    await expect(page.getByTestId('pr-publish-ready')).toHaveCount(0);
    await expect(page.getByTestId('pr-publish-why')).toContainText('is not verified');
    const facts = (await api('GET', `/api/cards/${id}/publish?act=ready`)).json;
    expect(facts.error?.code).toBe('not-verified');
    expect(facts.error?.fix).toBeTruthy();
    await shot(page, info, 'publish-7-updated');

    // the card's thread says what was published, and who did it
    const thread = JSON.stringify((await api('GET', `/api/cards/${id}/thread`)).json.items);
    expect(thread).toContain('Tester');
    expect(thread).toMatch(/#12/);
    if (phone(info)) await page.getByTestId('tab-thread').click();
    await shot(page, info, 'publish-8-thread');
  });

  test('is opened as a draft and marked ready when the person says so', async ({ pairedPage: page, server, workspace }, info) => {
    await openPR(page, server, id, phone(info));
    await page.getByTestId('pr-publish-create').click({ timeout: 20_000 });
    const dialog = page.getByTestId('publish-dialog');
    await page.getByTestId('publish-draft').check({ timeout: 20_000 });
    await page.getByTestId('publish-body').fill('Adds `Wave`, with its test.');
    await page.getByTestId('publish-confirm').click();
    await expect(dialog).toHaveCount(0, { timeout: 30_000 });
    const created = workspace.ghCalls().find((a) => a[0] === 'pr' && a[1] === 'create');
    expect(created).toContain('--draft');
    expect(JSON.parse(fs.readFileSync(path.join(workspace.ghData, 'pr-12.json'), 'utf8')).body).toBe('Adds `Wave`, with its test.');
    // a draft at a verified tip: ready is the act on offer
    await expect(page.getByTestId('pr-publish')).toContainText('draft', { timeout: 15_000 });
    await shot(page, info, 'publish-draft-1-opened');
    await page.getByTestId('pr-publish-ready').click();
    await expect(page.getByTestId('publish-summary')).toBeVisible({ timeout: 20_000 });
    await page.getByTestId('publish-details').locator('summary').click();
    await expect(page.getByTestId('publish-commands')).toHaveText('gh pr ready 12 --repo e2e/tiny');
    await shot(page, info, 'publish-draft-2-ready');
    await page.getByTestId('publish-confirm').click();
    await expect(dialog).toHaveCount(0, { timeout: 30_000 });
    await expect(page.getByTestId('pr-publish')).toContainText('ready for review', { timeout: 15_000 });
    await expect(page.getByTestId('pr-publish-draft')).toBeVisible();
    await shot(page, info, 'publish-draft-3-readied');
  });
});

test.describe('a publish GitHub is slow to answer', () => {
  let id: string;
  test.use({ seed: { run: async (ws) => { await ws.addGitHubRemote(); id = await ws.seedVerified('Add a wave helper'); } } });

  test('says what it is waiting on, step by step', async ({ pairedPage: page, server, workspace }, info) => {
    test.setTimeout(120_000);
    await openPR(page, server, id, phone(info));
    await expect(page.getByTestId('pr-publish-create')).toBeVisible({ timeout: 20_000 });

    // the dialog is up before its facts are, saying what it reads
    let answer = await workspace.holdGh('repo', 'view');
    await page.getByTestId('pr-publish-create').click();
    const dialog = page.getByTestId('publish-dialog');
    await expect(page.getByTestId('publish-reading')).toBeVisible();
    await expect(page.getByTestId('publish-confirm')).toHaveCount(0);
    await shot(page, info, 'publish-wait-1-reading');
    await answer();
    await expect(page.getByTestId('publish-summary')).not.toBeEmpty({ timeout: 20_000 });
    await expect(page.getByTestId('publish-steps')).toBeHidden();

    // confirmed: the plan's steps are a checklist the board marks as it
    // goes, and nothing in the dialog can start the act a second time
    answer = await workspace.holdGh('pr', 'create');
    await page.getByTestId('publish-confirm').click();
    await expect(page.getByTestId('publish-steps')).toBeVisible();
    await expect(page.getByTestId('publish-step-create')).toHaveAttribute('data-state', 'run', { timeout: 30_000 });
    await expect(page.getByTestId('publish-step-check')).toHaveAttribute('data-state', 'done');
    await expect(page.getByTestId('publish-step-push')).toHaveAttribute('data-state', 'done');
    await expect(page.getByTestId('publish-confirm')).toBeDisabled();
    await expect(page.getByTestId('publish-confirm')).toHaveAttribute('aria-busy', 'true');
    await expect(page.getByTestId('publish-cancel')).toBeDisabled();
    await expect(page.getByTestId('publish-title')).toBeDisabled();
    // the checklist is the act's wait: no second notice stands over it
    await expect(page.getByTestId('work-toast')).toHaveCount(0);
    // a step that has run a while says for how long
    await expect(page.getByTestId('publish-step-create')).toContainText(/· \d+s/, { timeout: 10_000 });
    await shot(page, info, 'publish-wait-2-steps');
    await answer();
    await expect(dialog).toHaveCount(0, { timeout: 30_000 });
    await expect(page.getByTestId('pr-state')).toBeVisible({ timeout: 15_000 });
  });
});

test.describe('a repository whose push runs a hook', () => {
  let id: string;
  test.use({
    seed: {
      run: async (ws) => {
        await ws.addGitHubRemote();
        id = await ws.seedVerified('Add a wave helper');
        const hook = path.join(ws.repo, '.git', 'hooks', 'pre-push');
        await fs.promises.writeFile(hook, '#!/bin/sh\nexit 0\n', { mode: 0o755 });
      },
    },
  });

  test('asks for the hook to be read before the push', async ({ pairedPage: page, server, workspace }, info) => {
    const branch = await branchOf(workspace, id);
    await openPR(page, server, id, phone(info));
    await page.getByTestId('pr-publish-create').click({ timeout: 20_000 });
    await expect(page.getByTestId('publish-hook')).toBeVisible({ timeout: 20_000 });
    await page.getByTestId('publish-confirm').click();
    await expect(page.getByTestId('publish-error')).toContainText('pre-push hook');
    expect(await remoteTip(workspace, branch)).toBe('');
    await shot(page, info, 'publish-hook');
    await page.getByTestId('publish-hook').check();
    await page.getByTestId('publish-confirm').click();
    await expect(page.getByTestId('publish-dialog')).toHaveCount(0, { timeout: 30_000 });
    expect(await remoteTip(workspace, branch)).not.toBe('');
  });
});

test.describe('a card that may not be published as it stands', () => {
  let id: string;
  test.use({ seed: { run: async (ws) => { await ws.addGitHubRemote(); id = await ws.seedVerified('Add a wave helper'); } } });

  test('is refused in the dialog, in words, with nothing to confirm', async ({ pairedPage: page, server, workspace }, info) => {
    const branch = await branchOf(workspace, id);
    // somebody else's commits already sit under the branch's name
    const foreign = (await workspace.git('commit-tree', 'main^{tree}', '-p', 'main', '-m', 'somebody else')).trim();
    await workspace.git('push', '-q', 'origin', `${foreign}:refs/heads/${branch}`);
    await openPR(page, server, id, phone(info));
    await page.getByTestId('pr-publish-create').click({ timeout: 20_000 });
    await expect(page.getByTestId('publish-refusal')).toContainText('never overwrites', { timeout: 20_000 });
    await expect(page.getByTestId('publish-confirm')).toHaveCount(0);
    await shot(page, info, 'publish-refused-name-taken');
    await page.getByTestId('publish-cancel').click();
    expect(await remoteTip(workspace, branch)).toBe(foreign);

    // uncommitted work is never published around
    await fs.promises.appendFile(path.join(workspace.worktree(id), 'README.md'), '\nhalf a thought\n');
    await page.getByTestId('pr-publish-create').click();
    await expect(page.getByTestId('publish-refusal')).toBeVisible({ timeout: 20_000 });
    await shot(page, info, 'publish-refused-dirty');
  });
});

// A session lands on a person's read of its diff, so that is its floor here
// too: a comment still open on the diff holds its pull request at draft.
test.describe('a session with a comment still open on its diff', () => {
  test.use({ seed: { run: async (ws) => { await ws.addGitHubRemote(); } } });

  test('opens its pull request as a draft the person cannot untick', async ({ pairedPage: page, server, api, workspace }, info) => {
    test.setTimeout(120_000);
    const made = await api('POST', '/api/cards', { kind: 'freeform', description: 'Poke at the rounding', backend: 'headless', model: 'e2e-implementer' });
    const id = String(made.json?.id);
    await page.goto(`${server.url}/#${id}`);
    await expect(page.getByTestId('card-id')).toHaveText(id);
    await expect(page.getByTestId('composer-says')).not.toContainText('stop this turn', { timeout: 30_000 });
    // the opening turn leaves its edit uncommitted: commit it as the person would
    await workspace.exec('sh', ['-c', 'git add -A && git commit -q -m "Note the rounding"'], { cwd: workspace.worktree(id) });
    const diff = (await api('GET', `/api/cards/${id}/diff`)).json;
    const line = diff.files.flatMap((f: any) => f.hunks.flatMap((hk: any) => hk.lines)).find((l: any) => l.t === '+');
    const ann = await api('POST', `/api/cards/${id}/diff/annotations`, { idx: line.idx, comment: 'Say which rounding.' });
    expect(ann.status, ann.text).toBe(200);

    await openPR(page, server, id, phone(info));
    await page.getByTestId('pr-publish-create').click({ timeout: 20_000 });
    await expect(page.getByTestId('publish-summary')).toContainText('open a draft PR', { timeout: 20_000 });
    await expect(page.getByTestId('publish-draft')).toBeChecked();
    await expect(page.getByTestId('publish-draft')).toBeDisabled();
    await expect(page.getByTestId('publish-draft-why')).toContainText('unresolved');
    await shot(page, info, 'publish-session-locked-draft');
    await page.getByTestId('publish-confirm').click();
    await expect(page.getByTestId('publish-dialog')).toHaveCount(0, { timeout: 30_000 });
    const created = workspace.ghCalls().find((a) => a[0] === 'pr' && a[1] === 'create');
    expect(created).toContain('--draft');
    expect(await remoteTip(workspace, await branchOf(workspace, id))).toBe(await tipOf(workspace, id));
    // and the strip says why it cannot be marked ready, instead of offering it
    await expect(page.getByTestId('pr-publish-why')).toContainText('unresolved', { timeout: 15_000 });
    await expect(page.getByTestId('pr-publish-ready')).toHaveCount(0);
  });
});

test.describe('a machine where gh is not signed in', () => {
  let id: string;
  test.use({
    seed: {
      run: async (ws) => {
        await ws.addGitHubRemote();
        id = await ws.seedVerified('Add a wave helper');
        await fs.promises.writeFile(path.join(ws.ghData, 'signed-out'), '');
      },
    },
  });

  test('offers no publishing at all', async ({ pairedPage: page, server }, info) => {
    await openPR(page, server, id, phone(info));
    await expect(page.getByTestId('pr-none')).toBeVisible();
    await expect(page.getByTestId('pr-push')).toBeVisible();
    await expect(page.getByTestId('pr-publish-create')).toHaveCount(0);
    await expect(page.getByTestId('card-publish')).toHaveCount(0);
    await shot(page, info, 'publish-not-set-up');
  });
});

test.describe('a card on a fork', () => {
  let id: string;
  test.use({
    seed: {
      run: async (ws) => {
        await ws.addGitHubRemote();
        id = await ws.seedVerified('Add a wave helper');
        await ws.setGh('repo-view.json', {
          nameWithOwner: 'e2e/tiny', viewerPermission: 'WRITE', isFork: true, parent: { name: 'tiny', owner: { login: 'upstream' } },
        });
      },
    },
  });

  test('asks where the pull request opens, and remembers the answer', async ({ pairedPage: page, server, workspace }, info) => {
    await openPR(page, server, id, phone(info));
    await page.getByTestId('pr-publish-create').click({ timeout: 20_000 });
    // a fork's PR can open in the fork or its parent: nothing is guessed
    await expect(page.getByTestId('publish-choose')).toContainText('e2e/tiny is a fork', { timeout: 20_000 });
    await shot(page, info, 'publish-fork-1-choose');
    await page.getByRole('button', { name: 'upstream/tiny' }).click();
    const base = page.getByTestId('publish-base');
    await expect(base).toHaveValue('upstream/tiny', { timeout: 20_000 });
    await expect(page.getByTestId('publish-summary')).toContainText('upstream/tiny');

    // the other one is a change of mind away, and what was typed stays
    await page.getByTestId('publish-title').fill('Add a wave helper, typed');
    await base.selectOption('e2e/tiny');
    await expect(page.getByTestId('publish-summary')).toContainText('into e2e/tiny', { timeout: 20_000 });
    await expect(page.getByTestId('publish-title')).toHaveValue('Add a wave helper, typed');
    await shot(page, info, 'publish-fork-2-chosen');
    await page.getByTestId('publish-confirm').click();
    await expect(page.getByTestId('publish-dialog')).toHaveCount(0, { timeout: 30_000 });
    const created = workspace.ghCalls().find((a) => a[0] === 'pr' && a[1] === 'create');
    expect(created).toEqual(expect.arrayContaining(['--repo', 'e2e/tiny']));
    // remembered where gh keeps it, so the next card is not asked
    const kept = await workspace.exec('git', ['config', '--get', 'remote.origin.gh-resolved'], { cwd: workspace.worktree(id) });
    expect(kept.stdout.trim()).toBe('base');
  });
});
