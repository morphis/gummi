import { expect, showCard, showTab, test } from '../fixtures/test';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { shot } from '../fixtures/shots';

// Signing a branch from the Log tab: a card whose commits were made
// before signing was switched on, in a repository that now signs. The
// page offers to sign them, and applying makes each again with its
// message and content as they were. The signer is a stub that answers
// git as gpg does; whose key signs is not this tab's business.

let id: string;
let tree: string;
let subjects: string;
const git = (cwd: string, ...args: string[]) => execFileSync('git', ['-C', cwd, ...args], { encoding: 'utf8' }).trim();

test.use({
  seed: {
    run: async (ws) => {
      id = await ws.seedVerified('Add a log helper');
      const wt = ws.worktree(id);
      fs.writeFileSync(path.join(wt, 'one.txt'), 'one\n');
      git(wt, 'add', '-A');
      git(wt, 'commit', '-q', '-m', 'wip: one');
      tree = git(wt, 'rev-parse', 'HEAD^{tree}');
      subjects = git(wt, 'log', '--format=%s', '-3');
      const stub = path.join(ws.root, 'sign-stub');
      fs.writeFileSync(stub, "#!/bin/sh\ncat >/dev/null\necho '[GNUPG:] SIG_CREATED ' >&2\nprintf -- '-----BEGIN PGP SIGNATURE-----\\n\\nstub\\n-----END PGP SIGNATURE-----\\n'\n", { mode: 0o700 });
      git(wt, 'config', 'gpg.program', stub);
      git(wt, 'config', 'commit.gpgsign', 'true');
    },
  },
});

test('the log offers to sign unsigned commits, and signs them without changing content', async ({ pairedPage: page, workspace }, info) => {
  await showCard(page, id);
  await showTab(page, 'log');
  const rows = page.locator('[data-testid^="log-commit-"]');
  await expect(rows.last()).toContainText('wip: one');
  const n = await rows.count();
  await expect(page.getByTestId('log-apply')).toBeDisabled();

  await page.getByTestId('log-sign').click();
  await expect(rows.locator('.tag.tosign')).toHaveCount(n);
  await expect(page.getByTestId('log-plan-line')).toContainText(`${n} commits made again, signed`);
  await shot(page, info, 'log-sign');
  await page.getByTestId('log-apply').click();

  // every commit now carries a signature, and there is nothing left to offer
  await expect(rows.locator('.tag', { hasText: 'signed' })).toHaveCount(n);
  await expect(page.getByTestId('log-sign')).toHaveCount(0);
  const wt = workspace.worktree(id);
  expect(git(wt, 'rev-parse', 'HEAD^{tree}')).toBe(tree);
  expect(git(wt, 'log', '--format=%s', '-3')).toBe(subjects);
  expect(git(wt, 'cat-file', 'commit', 'HEAD')).toContain('gpgsig -----BEGIN PGP SIGNATURE-----');
  expect(git(wt, 'status', '--porcelain')).toBe('');
});
