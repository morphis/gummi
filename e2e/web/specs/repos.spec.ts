import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { expect, test } from '../fixtures/test';
import type { Page } from '@playwright/test';

// The rail on a workspace that spans two repositories: each card names its
// repo as a chip, and the repo chips narrow the rail. The default repo's card
// is seeded first, while the workspace still has no repos: table.

test.use({
  seed: {
    run: async (ws) => {
      await ws.seedBacklog(['Add a shrug helper']);
      const lxd = path.join(ws.repo, 'git', 'lxd');
      await fs.promises.mkdir(lxd, { recursive: true });
      const git = (...args: string[]) => execFileSync('git', ['-C', lxd, '-c', 'user.name=E2E Tester', '-c', 'user.email=e2e@example.invalid', '-c', 'commit.gpgsign=false', ...args], { stdio: 'ignore' });
      git('init', '-q', '-b', 'main');
      git('commit', '-q', '--allow-empty', '-m', 'init: lxd');
      await fs.promises.appendFile(path.join(ws.repo, '.gummi', 'config.yaml'), 'repos:\n  lxd: git/lxd\n');
      const doc = path.join(ws.root, 'tmp', 'lxd-backlog.md');
      await fs.promises.writeFile(doc, '# Backlog\n\n## Rate limit the login\n\nDo rate limit the login.\n');
      await ws.gummiOK(['ingest', '--yes', '--repo', 'lxd', doc]);
    },
  },
});

// A narrow desktop starts with the rail compact, which hides the chip rows
// and each row's repo chip; the full rail is what these tests are about.
async function fullRail(page: Page, project: string): Promise<void> {
  if (project === 'phone') {
    // a phone opens on its cards list, the full rail; from a card, back
    if ((await page.getByTestId('app').getAttribute('data-view')) !== 'cards') await page.getByTestId('card-back').click();
    return;
  }
  if ((await page.getByTestId('rail-toggle').getAttribute('aria-pressed')) === 'false') {
    await page.getByTestId('rail-toggle').click();
  }
}

function watchErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  page.on('console', (m) => {
    if (m.type() === 'error' && !/501|Not Implemented|404/.test(m.text())) errors.push(m.text());
  });
  return errors;
}

test('a multi-repo board tags each card with its repo and offers repo chips', async ({ pairedPage: page }, info) => {
  const errors = watchErrors(page);
  await page.reload();
  await fullRail(page, info.project.name);
  await expect(page.getByTestId('rail-repos')).toBeVisible();
  for (const name of ['all', 'default', 'lxd']) {
    await expect(page.getByTestId(`rail-repo-${name}`)).toBeVisible();
  }
  await expect(page.getByTestId('rail-repo-all')).toHaveAttribute('aria-pressed', 'true');
  const lxdChip = page.locator('[data-testid^="rail-row-repo-"]');
  await expect(lxdChip).toHaveCount(1);
  await expect(lxdChip).toHaveText('lxd');
  // the default repo's card carries no chip: the default is implicit
  const plain = page.locator('.row:not(:has([data-testid^="rail-row-repo-"]))');
  await expect(plain).toHaveCount(1);
  await expect(plain).toContainText('Add a shrug helper');
  expect(errors).toEqual([]);
});

test('a repo chip narrows the rail to that repo, and all restores it', async ({ pairedPage: page }, info) => {
  await page.reload();
  await fullRail(page, info.project.name);
  const lxdRow = page.locator('.row', { hasText: 'Rate limit the login' });
  const defaultRow = page.locator('.row', { hasText: 'Add a shrug helper' });
  await expect(lxdRow).toBeVisible();
  await expect(defaultRow).toBeVisible();

  await page.getByTestId('rail-repo-lxd').click();
  await expect(page.getByTestId('rail-repo-lxd')).toHaveAttribute('aria-pressed', 'true');
  await expect(lxdRow).toBeVisible();
  await expect(defaultRow).toHaveCount(0);

  await page.getByTestId('rail-repo-default').click();
  await expect(defaultRow).toBeVisible();
  await expect(lxdRow).toHaveCount(0);

  await page.getByTestId('rail-repo-all').click();
  await expect(lxdRow).toBeVisible();
  await expect(defaultRow).toBeVisible();
});
