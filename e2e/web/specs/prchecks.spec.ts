import fs from 'node:fs';
import path from 'node:path';
import { expect, test } from '../fixtures/test';
import { shot } from '../fixtures/shots';

// A linked pull request's checks (DESIGN §22.11) against a real `gummi web`
// and fake gh: the PR tab draws them failing first, and its send button
// reads the failed job's log and hands it to the card's session as a turn.

const sha = '0000000000000000000000000000000000000007';
const job = 'https://github.com/e2e/tiny/actions/runs/31/job/4242';
const pr = (rollup: unknown[]) => ({
  number: 7,
  url: 'https://github.com/e2e/tiny/pull/7',
  state: 'OPEN',
  headRefOid: sha,
  headRefName: 'ff/checks',
  isCrossRepository: false,
  headRepositoryOwner: { login: 'e2e' },
  title: 'Checks',
  comments: [],
  statusCheckRollup: rollup,
});
const passed = { name: 'lint', workflowName: 'ci', status: 'COMPLETED', conclusion: 'SUCCESS', detailsUrl: 'https://github.com/e2e/tiny/actions/runs/31/job/4241' };
const failed = { name: 'test', workflowName: 'ci', status: 'COMPLETED', conclusion: 'FAILURE', detailsUrl: job };

test('the PR tab sends a failing check and its log to the session', async ({ pairedPage: page, server, api, workspace: ws }, info) => {
  test.setTimeout(120_000);
  await ws.setGh('pr-7.json', pr([passed, failed]));
  await fs.promises.writeFile(path.join(ws.ghData, 'job-4242.log'),
    'test\tRun make test\t2026-09-01T10:00:00.0000000Z --- FAIL: TestWaveTrims (0.00s)\n' +
    'test\tRun make test\t2026-09-01T10:00:01.0000000Z FAIL github.com/e2e/tiny\n');

  const made = await api('POST', '/api/cards', { kind: 'freeform', title: 'Checks' });
  const id = String(made.json?.id);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  if (info.project.name === 'phone') await page.getByTestId('tab-thread').click();
  const says = page.getByTestId('composer-says');
  await expect(says).not.toContainText('stop this turn', { timeout: 30_000 });
  await ws.linkPR(id);
  await page.reload();
  await expect(page.getByTestId('card-id')).toHaveText(id);

  await page.getByTestId('tab-pr').click();
  await expect(page.getByTestId('pr-state')).toContainText('open');
  await expect(page.getByTestId('pr-checks-summary')).toHaveText('1 failing');
  await expect(page.getByTestId('pr-checks')).toContainText('of 2');
  // failing first, whatever order GitHub listed them in
  await expect(page.getByTestId('pr-check-0')).toContainText('failing');
  await expect(page.getByTestId('pr-check-0')).toContainText('ci / test');
  await expect(page.getByTestId('pr-check-1')).toContainText('passed');
  // the link was made from the CLI, so the send arrives with the board's next read
  await expect(page.getByTestId('pr-checks-send')).toBeVisible({ timeout: 15_000 });
  await shot(page, info, 'pr-checks');

  await page.getByTestId('pr-checks-send').click();
  await expect(page.getByTestId('toasts')).toContainText('sent 1 failing check to its session', { timeout: 30_000 });
  // gummi read the failed job's log itself, with the person's gh
  expect(ws.ghCalls().some((c) => c[0] === 'run' && c.includes('--log-failed') && c.includes('4242'))).toBe(true);
  // and the session got it as a turn: the check, its link and where it stopped
  if (info.project.name === 'phone') await page.getByTestId('tab-thread').click();
  const thread = page.getByTestId('thread');
  await expect(thread).toContainText('e2e/tiny#7) has 1 failing check', { timeout: 30_000 });
  await expect(thread).toContainText('FAIL: TestWaveTrims');
});

test('a pull request with nothing failing offers no send', async ({ pairedPage: page, server, api, workspace: ws }, info) => {
  await ws.setGh('pr-7.json', pr([passed]));
  const made = await api('POST', '/api/cards', { kind: 'freeform', title: 'Checks' });
  const id = String(made.json?.id);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  if (info.project.name === 'phone') await page.getByTestId('tab-thread').click();
  await expect(page.getByTestId('composer-says')).not.toContainText('stop this turn', { timeout: 30_000 });
  await ws.linkPR(id);
  await page.reload();
  await page.getByTestId('tab-pr').click();
  await expect(page.getByTestId('pr-checks-summary')).toHaveText('none failing');
  await expect(page.getByTestId('pr-checks-send')).toHaveCount(0);
});
