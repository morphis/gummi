import { expect, showCard, showTab, test } from '../fixtures/test';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { shot } from '../fixtures/shots';

// The Log tab against a real `gummi web`: a verified card whose branch
// carries the checkpoints the scripted agent made plus two commits added
// here, read as a list, then squashed and reworded through the page. The
// content of the branch must be exactly what it was.

let id: string;
let tree: string;
const git = (cwd: string, ...args: string[]) => execFileSync('git', ['-C', cwd, ...args], { encoding: 'utf8' }).trim();

test.use({
  seed: {
    run: async (ws) => {
      id = await ws.seedVerified('Add a log helper');
      const wt = ws.worktree(id);
      for (const n of ['one', 'two']) {
        fs.writeFileSync(path.join(wt, `${n}.txt`), `${n}\n`);
        git(wt, 'add', '-A');
        git(wt, 'commit', '-q', '-m', `wip: ${n}`);
      }
      tree = git(wt, 'rev-parse', 'HEAD^{tree}');
    },
  },
});

test('the log lists the branch, and squash and reword rewrite it without changing content', async ({ pairedPage: page, workspace }, info) => {
  await showCard(page, id);
  await showTab(page, 'log');
  const rows = page.locator('[data-testid^="log-commit-"]');
  await expect(page.getByTestId('log-head')).toContainText('commits ahead of');
  const before = await rows.count();
  expect(before).toBeGreaterThanOrEqual(3);
  await expect(rows.last()).toContainText('wip: two');
  await shot(page, info, 'log');

  // a commit's changes open in place
  await page.getByTestId(`log-show-${before - 1}`).click();
  await expect(page.getByTestId('log-patch')).toContainText('two.txt');

  // squash the last into the one before, and reword the result
  await page.getByTestId(`log-squash-${before - 1}`).click();
  await expect(page.getByTestId('log-plan-line')).toContainText(`${before} commits → ${before - 1} commits · content unchanged`);
  await page.getByTestId(`log-reword-${before - 2}`).click();
  await page.getByTestId(`log-editor-${before - 2}`).fill('feat: add the wip files');
  await page.getByTestId(`log-editor-save-${before - 2}`).click();
  await expect(page.getByTestId('log-plan-line')).toContainText('content unchanged');
  await shot(page, info, 'log-plan');
  await page.getByTestId('log-apply').click();

  await expect(rows).toHaveCount(before - 1);
  await expect(rows.last()).toContainText('feat: add the wip files');
  const wt = workspace.worktree(id);
  expect(git(wt, 'rev-parse', 'HEAD^{tree}')).toBe(tree);
  expect(git(wt, 'log', '-1', '--format=%s')).toBe('feat: add the wip files');
  expect(git(wt, 'status', '--porcelain')).toBe('');

  // the verify gate is pinned to the branch head, which moved: the page
  // re-reads the card and offers the decision against the new head
  if (info.project.name !== 'phone') {
    await expect(page.getByText(`at ${git(wt, 'rev-parse', '--short=7', 'HEAD')}`).first()).toBeVisible();
  }
});
