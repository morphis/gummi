import fs from 'node:fs';
import path from 'node:path';
import type { Page } from '@playwright/test';
import { expect, test } from '../fixtures/test';
import { shot } from '../fixtures/shots';

// Agent Plugins: a scan's results stay with their tab while the user moves
// between agents and skills, the search narrows them as typed, and what the
// library already holds comes back checked, disabled and tagged imported.

test.use({
  seed: {
    run: async (ws) => {
      const files: Record<string, string> = {
        '.github/skills/review-rules/SKILL.md': '---\nname: review-rules\ndescription: How we review\n---\n\n# Review\n',
        '.github/skills/release-notes/SKILL.md': '---\nname: release-notes\ndescription: Notes\n---\n\n# Notes\n',
        '.github/agents/triager.agent.md': '---\nname: triager\ndescription: Triage bugs\n---\n\n# Triager\n',
      };
      for (const [name, body] of Object.entries(files)) {
        await fs.promises.mkdir(path.dirname(path.join(ws.repo, name)), { recursive: true });
        await fs.promises.writeFile(path.join(ws.repo, name), body);
      }
    },
  },
});

async function openPlugins(page: Page, phone: boolean) {
  if (phone && (await page.getByTestId('card-back').isVisible())) await page.getByTestId('card-back').click();
  await page.getByTestId('rail-more').click();
  await page.getByTestId('menu-agentplugins').click();
  await expect(page.getByTestId('view-agentplugins')).toBeVisible();
}

test('scans survive tab switches; imported entries are locked', async ({ pairedPage: page }, info) => {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  await openPlugins(page, info.project.name === 'phone');

  const skill = (n: string) => page.getByTestId(`plugins-candidate-skill-default-${n}`);
  const agent = page.getByTestId('plugins-candidate-agent-default-triager');

  // nothing scanned yet: no results list and no search
  await expect(page.getByTestId('plugins-candidates')).toHaveCount(0);

  // scan agents, then skills
  await page.getByTestId('plugins-discover').click();
  await expect(agent).toBeVisible();
  await page.getByTestId('plugins-tab-skills').click();
  await expect(page.getByTestId('plugins-candidates')).toHaveCount(0);
  await page.getByTestId('plugins-discover').click();
  await expect(skill('review-rules')).toBeVisible();

  // a selection and a search on skills
  await skill('release-notes').locator('input').check();
  const search = page.getByTestId('plugins-candidate-filter');
  await search.pressSequentially('review');
  await expect(search).toBeFocused();
  await expect(skill('release-notes')).toHaveCount(0);
  await expect(skill('review-rules')).toBeVisible();
  await shot(page, info, 'plugins-skills-filtered');

  // back and forth: each tab keeps its own results, search and selection
  await page.getByTestId('plugins-tab-agents').click();
  await expect(agent).toBeVisible();
  await page.getByTestId('plugins-tab-skills').click();
  await expect(search).toHaveValue('review');
  await search.fill('');
  await expect(skill('release-notes').locator('input')).toBeChecked();

  // import it: the scan stays, and the entry is now locked as imported
  await page.getByTestId('plugins-import-selected').click();
  await expect(page.getByTestId('plugins-items')).toContainText('release-notes');
  await expect(skill('release-notes')).toContainText('imported');
  await expect(skill('release-notes').locator('input')).toBeChecked();
  await expect(skill('release-notes').locator('input')).toBeDisabled();
  await expect(skill('review-rules').locator('input')).not.toBeChecked();
  await expect(page.getByTestId('plugins-import-selected')).toBeDisabled();
  await shot(page, info, 'plugins-skills-imported');

  // and it is still there after another round trip through agents
  await page.getByTestId('plugins-tab-agents').click();
  await page.getByTestId('plugins-tab-skills').click();
  await expect(skill('release-notes').locator('input')).toBeDisabled();

  // an item's actions sit at the right edge of its row, on both tabs
  await page.getByTestId('plugins-tab-agents').click();
  await agent.locator('input').check();
  await page.getByTestId('plugins-import-selected').click();
  await expect(page.getByTestId('plugins-items')).toContainText('triager');
  for (const tab of ['agents', 'skills']) {
    await page.getByTestId(`plugins-tab-${tab}`).click();
    const row = page.getByTestId('plugins-items').locator('.pitem').first();
    const r = (await row.boundingBox())!;
    const a = (await row.locator('.pitemactions').boundingBox())!;
    expect(r.x + r.width - (a.x + a.width), `${tab}: actions hug the right edge`).toBeLessThan(16);
    await shot(page, info, `plugins-${tab}-items`);
  }
  expect(errors).toEqual([]);
});
