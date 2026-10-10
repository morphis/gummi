import path from 'node:path';
import type { Page } from '@playwright/test';
import { expect, test } from '../fixtures/test';
import { shot } from '../fixtures/shots';

// The repositories view against a real `gummi web`: the workspace's
// repository with its branches sorted by who holds them, a fetch that
// finds the remote ahead, the fast-forward that takes it, and deleting a
// branch no card holds — at once when the base has everything on it, by
// typing its name when it does not. gummi never pushes: the base's
// unpushed commit shows the line to run.

test.use({
  seed: {
    run: async (ws) => {
      await ws.seedBacklog(['Add a shrug helper']);
      const sh = (script: string, cwd = ws.repo) => ws.exec('sh', ['-ec', script], { cwd });
      const bare = path.join(ws.root, 'tmp', 'origin.git');
      const other = path.join(ws.root, 'tmp', 'other');
      await sh(`mkdir -p "${path.dirname(bare)}" && git clone -q --bare . "${bare}" && git remote add origin "${bare}" && git fetch -q origin && git branch -q --set-upstream-to=origin/main main`);
      // the remote moves on after the clone
      await sh(`git clone -q "${bare}" "${other}" && cd "${other}" && echo remote > REMOTE.md && git add . && git -c user.name=E2E -c user.email=e2e@example.invalid commit -q -m "docs: remote work" && git push -q origin main`);
      // two branches nobody holds: one the base has, one it does not
      await sh('git branch -q spike/old && git checkout -q -b wip/simon && echo wip > WIP.md && git add . && git -c user.name=E2E -c user.email=e2e@example.invalid commit -q -m "wip: half an idea" && git checkout -q main');
    },
  },
});

async function openRepos(page: Page) {
  await page.getByTestId('rail-more').click();
  await page.getByTestId('menu-repos').click();
  await expect(page.getByTestId('view-repos')).toBeVisible();
}

test('a repository is fetched, fast-forwarded and its stray branches deleted', async ({ pairedPage: page, workspace }, info) => {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  await openRepos(page);
  const repo = page.getByTestId('repo-default');
  await expect(repo).toBeVisible();
  await expect(page.getByTestId('repos-count')).toContainText('1 repository');
  await expect(repo.getByTestId('repo-default-base').getByTestId('branch-main')).toBeVisible();
  const unowned = repo.getByTestId('repo-default-unowned');
  await expect(unowned.getByTestId('branch-spike/old')).toContainText('merged');
  await expect(unowned.getByTestId('branch-wip/simon')).toContainText('+1');
  // nothing fetched since the remote moved: no fast-forward on offer yet
  await expect(repo.getByTestId('repo-ff-default')).toHaveCount(0);

  await repo.getByTestId('repo-fetch-default').click();
  const ff = repo.getByTestId('repo-ff-default');
  await expect(ff).toContainText('Fast-forward main ↓1');
  await shot(page, info, 'repos');
  await ff.click();
  await expect(ff).toHaveCount(0);
  const head = (await workspace.exec('git', ['log', '-1', '--format=%s', 'main'], { cwd: workspace.repo })).stdout.trim();
  expect(head).toBe('docs: remote work');

  // the base has everything on spike/old: one question, then it is gone
  await unowned.getByTestId('branch-menu-spike/old').click();
  await page.getByTestId('branch-delete-spike/old').click();
  await page.getByTestId('branch-delete-confirm-yes').click();
  await expect(unowned.getByTestId('branch-spike/old')).toHaveCount(0);

  // wip/simon holds a commit the base lacks: its name is the yes
  await unowned.getByTestId('branch-menu-wip/simon').click();
  await page.getByTestId('branch-delete-wip/simon').click();
  const force = page.getByTestId('branch-delete-force');
  await expect(force).toBeDisabled();
  await page.getByTestId('branch-delete-name').fill('wip/simon');
  await force.click();
  await expect(repo.getByTestId('repo-default-unowned')).toHaveCount(0);
  const left = (await workspace.exec('git', ['branch', '--format=%(refname:short)'], { cwd: workspace.repo })).stdout;
  expect(left).not.toContain('wip/simon');
  expect(left).not.toContain('spike/old');
  expect(errors).toEqual([]);
});

test('a base ahead of its remote shows the push to run, and nothing runs it', async ({ pairedPage: page, workspace }) => {
  await workspace.exec('sh', ['-ec', 'git fetch -q origin && git merge -q --ff-only origin/main && echo local > LOCAL.md && git add . && git -c user.name=E2E -c user.email=e2e@example.invalid commit -q -m "docs: local work"'], { cwd: workspace.repo });
  await openRepos(page);
  const main = page.getByTestId('repo-default').getByTestId('branch-main');
  await expect(main.getByTestId('branch-push-line')).toHaveText('git push origin main');
  const remote = (await workspace.exec('git', ['ls-remote', 'origin', 'refs/heads/main'], { cwd: workspace.repo })).stdout;
  const local = (await workspace.exec('git', ['rev-parse', 'main'], { cwd: workspace.repo })).stdout.trim();
  expect(remote).not.toContain(local);
});

test('remotes are added, renamed, repointed and removed, and a branch is told what to track', async ({ pairedPage: page, workspace }, info) => {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  const bare = path.join(workspace.root, 'tmp', 'origin.git');
  const git = async (...args: string[]) => (await workspace.exec('git', args, { cwd: workspace.repo })).stdout.trim();
  await openRepos(page);
  const repo = page.getByTestId('repo-default');
  const remotes = repo.getByTestId('repo-default-remotes');
  await expect(remotes.getByTestId('remote-origin')).toContainText('1 branch tracks it');
  await expect(remotes.getByTestId('remote-url-origin')).toHaveText(bare);

  // a second remote: recorded, and nothing fetched until asked
  await remotes.getByTestId('remote-add-default').click();
  const add = page.getByTestId('remote-add-yes');
  await expect(add).toBeDisabled();
  await page.getByTestId('remote-add-remote').fill('fork');
  await page.getByTestId('remote-add-url').fill(bare);
  await add.click();
  const fork = remotes.getByTestId('remote-fork');
  await expect(fork).toContainText('no branch tracks it');
  expect(await git('for-each-ref', 'refs/remotes/fork')).toBe('');
  await fork.getByTestId('remote-menu-fork').click();
  await page.getByTestId('remote-fetch-fork').click();
  await expect.poll(() => git('for-each-ref', '--format=%(refname:short)', 'refs/remotes/fork')).toContain('fork/main');

  // wip/simon tracks nothing; point it at the fork's main
  const wip = repo.getByTestId('branch-wip/simon');
  const at = await git('rev-parse', 'wip/simon');
  await expect(wip.getByTestId('branch-track-wip/simon')).toHaveText('local only');
  await wip.getByTestId('branch-menu-wip/simon').click();
  await page.getByTestId('branch-upstream-wip/simon').click();
  const save = page.getByTestId('branch-upstream-yes');
  await expect(save).toBeDisabled();
  await page.getByTestId('branch-upstream-pick').selectOption('fork/main');
  await save.click();
  // one commit of its own, and one the remote made since
  await expect(wip.getByTestId('branch-track-wip/simon')).toHaveText('↑1 ↓1');
  await expect(wip.getByTestId('branch-track-wip/simon')).toHaveAttribute('title', /against fork\/main/);
  await expect(fork).toContainText('1 branch tracks it');
  expect(await git('rev-parse', 'wip/simon')).toBe(at);
  await shot(page, info, 'repos-remotes');

  // renamed, the branch follows it
  await fork.getByTestId('remote-menu-fork').click();
  await page.getByTestId('remote-rename-fork').click();
  await page.getByTestId('remote-rename-newName').fill('mirror');
  await page.getByTestId('remote-rename-yes').click();
  const mirror = remotes.getByTestId('remote-mirror');
  await expect(mirror).toBeVisible();
  await expect(wip.getByTestId('branch-track-wip/simon')).toHaveAttribute('title', /against mirror\/main/);

  // a URL with a credential is stored whole and shown without it
  await mirror.getByTestId('remote-menu-mirror').click();
  await page.getByTestId('remote-seturl-mirror').click();
  await page.getByTestId('remote-seturl-url').fill('https://simon:hunter2@example.invalid/simon/demo.git');
  await page.getByTestId('remote-seturl-yes').click();
  await expect(remotes.getByTestId('remote-url-mirror')).toHaveText('https://•••@example.invalid/simon/demo.git');
  await expect(page.locator('body')).not.toContainText('hunter2');
  expect(await git('remote', 'get-url', 'mirror')).toBe('https://simon:hunter2@example.invalid/simon/demo.git');
  // the masked text is not offered back for editing
  await mirror.getByTestId('remote-menu-mirror').click();
  await page.getByTestId('remote-seturl-mirror').click();
  await expect(page.getByTestId('remote-seturl-url')).toHaveValue('');
  await page.getByTestId('remote-seturl-no').click();

  // removed, the branch that tracked it tracks nothing and has not moved
  await mirror.getByTestId('remote-menu-mirror').click();
  await page.getByTestId('remote-remove-mirror').click();
  await expect(page.getByTestId('remote-remove-confirm')).toContainText('wip/simon tracks it');
  await page.getByTestId('remote-remove-confirm-yes').click();
  await expect(mirror).toHaveCount(0);
  await expect(wip.getByTestId('branch-track-wip/simon')).toHaveText('local only');
  expect(await git('remote')).toBe('origin');
  expect(await git('rev-parse', 'wip/simon')).toBe(at);
  expect(errors).toEqual([]);
});
