import { expect, showTab, test } from '../fixtures/test';
import { shot } from '../fixtures/shots';

// A research card never gets a branch: its work is the document. Its
// diff and PR tabs must say so, rather than promise a worktree "when you
// approve the research document" or hand the reader a push command for a
// branch that does not exist.

let id: string;
test.use({ seed: { run: async (ws) => { id = await ws.seedResearch('How are greetings used'); } } });

test('a research card offers no branch to diff or push', async ({ pairedPage: page, server }, info) => {
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await showTab(page, 'diff');
  await expect(page.getByTestId('panel-pane')).toContainText('carries no branch');
  await expect(page.getByTestId('panel-pane')).not.toContainText('when you approve');
  await showTab(page, 'pr');
  await expect(page.getByTestId('pr-none')).toBeVisible();
  await expect(page.getByTestId('pr-push-cmd')).toHaveCount(0);
  await shot(page, info, 'research-pr');
});
