import { spawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { gummiBin } from './paths';
import { parseEvents, type Workspace } from './workspace';

/**
 * Seeding goals through the real CLI. The scripted agent agrees a goal's
 * plan without touching the doc `gummi goal --plan-file` started it from
 * (scripts/web-e2e-agent.py, plan_goal), so the doc below is what the plan
 * gate judges: two done-when items, each checked by a command that passes
 * on the tiny module, and one card serving each.
 */
export function goalDoc(cards: string[]): string {
  const items = [
    { id: 'DW-1', says: 'the module builds', check: 'go build ./...' },
    { id: 'DW-2', says: 'the tests pass', check: 'go test -count=1 ./...' },
  ];
  return [
    '# goal', '',
    '## Objective', '', 'Greet in two languages.', '',
    '## Done when', '', '```gummi-done-when',
    ...items.flatMap((d) => [`- id: ${d.id}`, `  says: ${d.says}`, `  check: ${d.check}`]),
    '```', '',
    '## Limits', '', 'None.', '',
    '## Budget', '', 'Fine.', '', '```gummi-goal', 'lanes: 2', '```', '',
    '## Cards', '', '```gummi-cards',
    ...cards.flatMap((t, i) => [`- title: ${JSON.stringify(t)}`, `  serves: [${items[i % items.length].id}]`]),
    '```', '',
    '## Notes', '', '',
    '## Try it', '', 'Run `go run ./cmd/tiny`.', '',
    '## Review', '', '',
    '## Verification plan', '', 'The checks.', '',
    '## Report', '', '',
  ].join('\n');
}

export interface GoalOpts {
  /** The goal's whole budget in dollars (default 30). */
  envelope?: number;
  /** Card titles for the doc's gummi-cards block. */
  cards?: string[];
}

/** A goal parked at its plan gate: `gummi goal --plan-file … --until plan`. */
export async function seedGoalAtPlan(ws: Workspace, objective: string, opts: GoalOpts = {}): Promise<string> {
  const doc = path.join(ws.root, 'tmp', `goal-${Date.now()}.md`);
  await fs.promises.writeFile(doc, goalDoc(opts.cards ?? ['Add a hola helper', 'Add a bonjour helper']));
  const r = await ws.gummi(['goal', '--plan-file', doc, '--gate-approval', 'attended', '--until', 'plan',
    '--envelope', String(opts.envelope ?? 30), objective]);
  const events = parseEvents(r.stdout);
  const created = events.find((e) => e.event === 'created');
  if (!created || !events.some((e) => e.event === 'stopped')) {
    throw new Error(`gummi goal did not stop at its plan gate (exit ${r.code})\n${r.stdout}\n${r.stderr}`);
  }
  return String(created.id);
}

/**
 * A goal at implement that nothing is driving: its plan approved by
 * `gummi resume --approve`, which is stopped the moment the goal crosses
 * into implement. A board that opens it afterwards conducts it itself (and
 * only a board that is not being driven elsewhere may act on it). Give its
 * cards the [slow] keyword and GUMMI_E2E_SLOW_SECONDS to keep it there.
 */
export async function seedGoalRunning(ws: Workspace, objective: string, opts: GoalOpts = {}): Promise<string> {
  const id = await seedGoalAtPlan(ws, objective, opts);
  const child = spawn(gummiBin, ['resume', id, '--approve'], { cwd: ws.repo, env: ws.env, stdio: ['ignore', 'ignore', 'ignore'] });
  const exited = new Promise<void>((resolve) => child.on('close', () => resolve()));
  try {
    const deadline = Date.now() + 60_000;
    for (;;) {
      const st = await ws.status(id);
      if (st.stage === 'implement') break;
      if (Date.now() > deadline) throw new Error(`${id} did not reach implement: ${JSON.stringify(st)}`);
      await new Promise((r) => setTimeout(r, 200));
    }
    // let it mint and start the cards before the driver goes
    await new Promise((r) => setTimeout(r, 800));
  } finally {
    child.kill('SIGTERM');
    await exited;
  }
  return id;
}
