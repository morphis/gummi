import { expect, test } from '../fixtures/test';
import { agentScript } from '../fixtures/paths';
import { execFile } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { promisify } from 'node:util';

const run = promisify(execFile);

// A landing pressed from the pinned answer block with no pre-drafted
// message used to hold its answer request behind the live scribe pass
// that drafts one — the answer block dimmed and disabled with no dialog,
// no note and no way through for the whole pass (a model call, a minute
// or two on a real backend), so the press read as broken. The dialog must
// be on screen where the person is looking within seconds of the press,
// the way the card menu's own merge entry already behaves: it opens with
// the nothing-drafted hint, says the drafting is going on while the pass
// runs behind the held answer request, and the drafted message fills it
// once the pass ends.
//
// The agent wrapper below holds the scribe's landing-draft turn for
// DRAFT_DELAY seconds, making the pass observable; every other frame is
// forwarded untouched.
const wrapperPath = path.join(os.tmpdir(), 'gummi-e2e-slow-draft-agent.py');
fs.writeFileSync(
  wrapperPath,
  `#!/usr/bin/env python3
import os, subprocess, sys, threading, time
delay = float(os.environ.get("GUMMI_E2E_DRAFT_DELAY") or "8")
p = subprocess.Popen([sys.executable, os.environ["GUMMI_E2E_AGENT_SCRIPT"]],
                     stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, bufsize=1)
def pump():
    for l in p.stdout:
        sys.stdout.write(l)
        sys.stdout.flush()
threading.Thread(target=pump, daemon=True).start()
for line in sys.stdin:
    if '"send"' in line and "squash-merge landing commit" in line:
        time.sleep(delay)
    try:
        p.stdin.write(line)
        p.stdin.flush()
    except BrokenPipeError:
        break
try:
    p.stdin.close()
except BrokenPipeError:
    pass
p.wait()
`,
  { mode: 0o755 },
);
fs.chmodSync(wrapperPath, 0o755);

async function open(page: import('@playwright/test').Page, url: string, id: string) {
  await page.goto(`${url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
}

test.describe('a landing pressed from the answer block with no stored draft', () => {
  let id: string;
  test.use({
    workspaceEnv: {
      GUMMI_AGENT_CMD: wrapperPath,
      GUMMI_E2E_AGENT_SCRIPT: agentScript,
      GUMMI_E2E_DRAFT_DELAY: '8',
    },
    seed: {
      run: async (ws) => {
        id = await ws.seedVerified('Add a farewell helper');
        // the state a real backend's failed (or stale) pre-draft leaves:
        // no stored landing message for the branch as it stands
        await run('python3', ['-c', [
          'import sqlite3',
          `c = sqlite3.connect(${JSON.stringify(ws.repo + '/.gummi/state/gummi.db')})`,
          `c.execute("UPDATE features SET commit_draft='', commit_draft_sha='' WHERE id=?", (${JSON.stringify(id)},))`,
          'c.commit()',
          'c.close()',
        ].join('; ')]);
      },
    },
  });

  test('the landing dialog comes up on the press, and the drafted message fills it', async ({ pairedPage: page, server }) => {
    await open(page, server.url, id);
    await expect(page.getByTestId('decision-option-advance')).toContainText(/land/i);

    await page.getByTestId('decision-option-advance').click();

    // the dialog is up where the person is looking, while the draft pass
    // still runs behind the held answer request — and it says so: the
    // box is not for typing, the hint is busy, the button is waiting
    await expect(page.getByTestId('landing-dialog')).toBeVisible({ timeout: 3_000 });
    await expect(page.getByTestId('landing-confirm')).toHaveText('Drafting…');
    await expect(page.getByTestId('landing-message')).not.toBeEditable();
    await expect(page.getByTestId('landing-hint')).toContainText(/drafting/i);
    // the pass ends and its draft fills the box to read before landing
    await expect(page.getByTestId('landing-message')).toHaveValue(/feat: land/, { timeout: 20_000 });
    await expect(page.getByTestId('landing-hint')).toContainText(/drafted by gummi/i);
    await expect(page.getByTestId('landing-confirm')).toHaveText(/land/i);
    await expect(page.getByTestId('landing-confirm')).toBeEnabled();
  });
});
