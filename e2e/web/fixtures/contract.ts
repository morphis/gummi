import type { Page, Route } from '@playwright/test';

/**
 * Contract-shaped answers (internal/webapi) for the routes a card page reads,
 * so the page's drawing can be tested before — and independently of — the
 * server filling each route. `mockCard(page, id)` answers thread, live,
 * spec, diff, pr, stats, composer, answer and annotations for one card and
 * pins a verify-failed decision on its head (the real head is fetched and
 * extended). Returns a handle to steer the next answer's outcome.
 */
export interface MockHandle {
  answers: any[];
  annotations: any[];
  nextAnswer: { status: number; body: any } | null;
  diffRev: string;
  changes: string[];
}

const t0 = '2026-09-27T09:12:00Z';
const at = (m: number) => new Date(Date.parse(t0) + m * 60_000).toISOString();

export const decision = {
  ref: 'dec-7',
  kind: 'verify',
  question: 'Verify failed on 1 of 3 checks. How should this card continue?',
  anchor: 'diff',
  against: { token: 'dec-7@a41c9e2', label: 'verify at 10:03 · branch a41c9e2' },
  options: [
    { id: 'sendback', label: 'Send back to implement with the failure', detail: 'implementer', words: false, carriesComments: true },
    { id: 'note', label: 'Send back to implement with your note', words: true, relabel: 'Send back to implement with this note', carriesComments: true },
    { id: 'excuse', label: 'Excuse the failing check', detail: 'needs a reason', words: true, relabel: 'Excuse the check for this reason' },
    { id: 'park', label: 'Park the card', detail: 'keeps the branch', words: false },
  ],
};

export function thread() {
  return {
    lastSeq: 40,
    items: [
      { key: 's1', seq: 12, t: 'stage', time: at(0), stage: 'plan', role: 'architect', model: 'e2e-architect', verdict: 'approved', credits: 4.5 },
      { key: 'm1', seq: 3, t: 'message', time: at(1), stage: 'plan', role: 'architect', model: 'e2e-architect', text: 'The repro holds: a card whose `DocPath` is empty reaches **RemoveAll** on the workspace.\n\n- refuse an empty path in `domain`\n- let callers skip the remove\n\nSee [the design](https://example.com/design) and <script>alert(1)</script>.' },
      { key: 't1', seq: 4, t: 'tools', time: at(2), stage: 'plan', tools: [{ tool: 'read', label: 'internal/domain/delete.go', status: 'ok', ms: 12 }, { tool: 'grep', label: 'RemoveAll(', status: 'ok', ms: 40 }, { tool: 'run', label: 'go test ./internal/domain', status: 'fail', ms: 2100 }] },
      { key: 'r1', seq: 5, t: 'receipt', time: at(19), stage: 'plan', receipt: { ok: true, text: 'Design approved', by: 'Yuki' } },
      { key: 's2', seq: 20, t: 'stage', time: at(20), stage: 'implement', role: 'implementer', model: 'e2e-implementer', verdict: 'done', credits: 3.1 },
      { key: 'y1', seq: 21, t: 'you', time: at(22), stage: 'implement', by: 'Tester', via: 'steered', text: 'Keep the adopted branch alive in `clean`.' },
      { key: 'm2', seq: 22, t: 'message', time: at(30), stage: 'implement', role: 'implementer', model: 'e2e-implementer', text: 'Added `ErrNoDocument` and a regression test.' },
      { key: 'x1', seq: 23, t: 'stretch', time: at(31), label: 'autopilot', tally: '1 gate crossed' },
      { key: 's3', seq: 30, t: 'stage', time: at(40), stage: 'verify', role: 'verify' },
      { key: 'v1', seq: 31, t: 'verify', time: at(41), stage: 'verify', checks: [
        { name: 'build', cmd: 'go build ./...', ok: true, ms: 4100 },
        { name: 'repro', cmd: 'go test ./cmd/gummi -run BG104', ok: true, ms: 2800 },
        { name: 'clean', cmd: 'go test ./cmd/gummi -run TestCleanKeepsAdopted', ok: false, ms: 1900, output: '--- FAIL: TestCleanKeepsAdopted (0.21s)\n    clean_test.go:142: adopted branch was deleted' },
      ] },
      { key: 'd1', seq: 40, t: 'decision', time: at(42), stage: 'verify', decision: { kind: 'verify', question: decision.question } },
    ],
  };
}

export const spec = {
  path: 'docs/features/FD-001.md', rev: 'a41c9e2b', title: 'Add a wave helper',
  markdown: '# Add a wave helper\n\n## Problem\n\nGreet only says hello. We want a `Wave` too.\n\n%% @gummi: a prompt the page must not show\n\n## Chosen approach\n\nAdd `Wave(name)` beside `Greet`.\n\n1. write it\n2. test it\n\n## Verification plan\n\n```gummi-checks\nbuild: go build ./...\ntest: go test ./...\n```\n',
  sections: [{ name: 'Problem', line: 3 }, { name: 'Chosen approach', line: 9 }, { name: 'Verification plan', line: 16 }],
  notes: [{ line: 5, author: 'user', by: 'Yuki', date: '09:29', text: 'Does clean share the same path?', resolved: false }],
  checks: [{ name: 'build', cmd: 'go build ./...', last: { ok: true, at: at(41) } }, { name: 'test', cmd: 'go test ./...', last: { ok: false, at: at(41) } }],
  openComments: 1,
};

export function diff(rev: string, annotations: any[]) {
  return {
    base: 'main', rev, pendingComments: annotations.filter((a) => !a.resolved).length, annotations,
    files: [
      { path: 'wave.go', add: 4, del: 1, hunks: [{ header: '@@ -1,3 +1,6 @@ package tiny', lines: [
        { t: ' ', old: 1, new: 1, text: 'package tiny', idx: 5 },
        { t: '-', old: 2, text: '// nothing waves', idx: 6 },
        { t: '+', new: 2, text: '// Wave waves at name.', idx: 7 },
        { t: '+', new: 3, text: 'func Wave(name string) string {', idx: 8 },
        { t: '+', new: 4, text: '\treturn "wave, " + name // <b>not bold</b>', idx: 9 },
        { t: '+', new: 5, text: '}', idx: 10 },
      ] }] },
      { path: 'wave_test.go', add: 3, del: 0, hunks: [{ header: '@@ -0,0 +1,3 @@', lines: [
        { t: '+', new: 1, text: 'package tiny', idx: 14 },
        { t: '+', new: 2, text: '', idx: 15 },
        { t: '+', new: 3, text: 'func TestWave(t *testing.T) {}', idx: 16 },
      ] }] },
    ],
  };
}

export const pr = {
  linked: true, ref: '#7', url: 'https://github.com/example/tiny/pull/7', state: 'OPEN',
  threads: [{ path: 'wave.go', line: 3, resolved: false, notes: [{ author: 'reviewer-a', body: 'Should `Wave` trim the name?', at: at(50) }] }],
  pushCommand: 'git push origin feat/add-a-wave-helper',
};

export const stats = {
  id: 'FD-001', title: 'Add a wave helper', kind: 'feature', stage: 'verify',
  money: {
    credits: 11.0, estimated: 0.4, firstPass: 7.6, rework: 3.4, corrected: 3.4, reproved: 0, elsewhere: 0,
    byStage: [{ name: 'plan', credits: 4.5 }, { name: 'implement', credits: 6.5 }],
    byRole: [{ name: 'architect', credits: 4.5 }, { name: 'implementer', credits: 6.5 }],
    byModel: [{ name: 'e2e-implementer', credits: 6.5 }, { name: 'e2e-architect', credits: 3.6 }, { name: 'e2e-reviewer', credits: 0.9 }],
  },
  clock: { agentMs: 1_500_000, onYouMs: 2_460_000, idleMs: 0, elapsedMs: 4_000_000, toFirstGateMs: 600_000, toVerifiedMs: 3_600_000 },
  hands: {
    turns: 9, toolCalls: 12, toolFails: 1,
    tools: [
      { name: 'read', calls: 6, totalMs: 480 },
      { name: 'run', calls: 6, fails: 1, detail: 'go test ./internal/tiny', totalMs: 4_900 },
    ],
    skills: [{ name: 'skill', calls: 1, detail: 'gummi-go-verify' }],
    subagents: [{ name: 'task', calls: 1, detail: 'find the fold' }],
    checks: [
      { name: 'build', runs: 2, fails: 0 },
      { name: 'clean', runs: 1, fails: 1, excused: true },
    ],
  },
  judgment: {
    gates: { total: 2, byYou: 1, byMachine: 1 },
    asks: { total: 1, byYou: 0, byMachine: 1 },
    parks: [{ reason: 'verify failed', detail: '1 of 3 checks failed', at: at(42) }],
  },
  envelope: { credits: 40, left: 29.0 },
  sessions: [
    { stage: 'plan', role: 'architect', model: 'e2e-architect', started: at(0), ended: at(11), turns: 3, tools: 4, toolFails: 0, credits: 3.6, tokens: { input: 48_000, cached: 31_000, output: 2_100 }, contextPeak: 41_000, contextLimit: 200_000 },
    { stage: 'plan', role: 'reviewer', flavor: 'critique', model: 'e2e-reviewer', started: at(11), ended: at(14), turns: 1, tools: 0, toolFails: 0, credits: 0.9 },
    { stage: 'implement', role: 'implementer', model: 'e2e-implementer', started: at(20), ended: at(34), turns: 4, tools: 6, toolFails: 1, credits: 3.1, tokens: { input: 9_000, cached: 1_000, output: 500 } },
    { stage: 'implement', role: 'implementer', model: 'e2e-implementer', started: at(35), ended: at(39), turns: 1, tools: 2, toolFails: 0, credits: 3.4, redo: true, redoReason: 'corrected' },
  ],
};

// A session's stats (the card is freeform, stage open): spend against its
// envelope and the model table, with the stage/role buckets the session
// view draws as bars. Sessions have no passes, so there is no hands or
// judgment to carry.
export const sessionStats = {
  id: 'FF-001', title: 'Poke at the rounding', kind: 'freeform', stage: 'open',
  money: {
    credits: 3.2, estimated: 0, firstPass: 3.2, rework: 0, corrected: 0, reproved: 0, elsewhere: 0,
    byStage: [{ name: 'open', credits: 3.2 }],
    byRole: [{ name: 'session', credits: 3.2 }],
    byModel: [{ name: 'e2e-implementer', credits: 3.2 }],
  },
  clock: { agentMs: 90_000, onYouMs: 0, idleMs: 0, elapsedMs: 120_000 },
  envelope: { credits: 500, left: 496.8 },
  sessions: [],
};

export async function mockCard(page: Page, id: string, opts: { kind?: string; decision?: any } = {}): Promise<MockHandle> {
  const m: MockHandle = { answers: [], annotations: [{ id: 1, file: 'wave.go', idx: 8, excerpt: 'func Wave', comment: 'Name it WaveAt?', by: 'Yuki', source: 'gummi', resolved: false }], nextAnswer: null, diffRev: 'a41c9e2', changes: [] };
  // A request the page gave up on (a reload, or a card change the board
  // pushed while the card's own fetch was in flight) is already settled by
  // the time its handler answers; answering it again is not the test's
  // failure, so a settled route is let go.
  const json = async (route: Route, body: any, status = 200) => {
    try {
      await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
    } catch (err) {
      if (!/already handled|Target .*closed|has been closed/.test(String(err))) throw err;
    }
  };
  const base = `**/api/cards/${id}`;
  await page.route(base, async (route) => {
    let card: any;
    try {
      card = await (await route.fetch()).json();
    } catch (err) {
      if (/already handled|Target .*closed|has been closed|Request context disposed/.test(String(err))) return;
      throw err;
    }
    card.stage = 'verify';
    if (opts.kind) card.kind = opts.kind;
    card.decision = opts.decision ?? decision;
    card.decisionsMore = 0;
    card.composer = { says: 'answers the pinned decision', route: 'answer' };
    await json(route, card);
  });
  await page.route(`${base}/thread*`, (r) => json(r, thread()));
  await page.route(`${base}/live`, (r) => json(r, { busy: true, verb: 'reading the failure', spent: 0.4, streaming: 'Looking at `clean_test.go`…', tool: { tool: 'read', label: 'clean_test.go', status: 'running' } }));
  await page.route(`${base}/spec`, (r) => json(r, spec));
  await page.route(`${base}/diff`, (r) => json(r, diff(m.diffRev, m.annotations)));
  await page.route(`${base}/diff/annotations`, async (r) => {
    const body = r.request().postDataJSON();
    m.annotations.push({ id: m.annotations.length + 1, file: 'wave.go', idx: body.idx, excerpt: '', comment: body.comment, by: 'Tester', source: 'gummi', resolved: false });
    await json(r, diff(m.diffRev, m.annotations));
  });
  for (const what of ['spec', 'diff']) {
    await page.route(`${base}/${what}/changes`, async (r) => {
      const confirm = r.request().postDataJSON()?.confirm;
      m.changes.push(confirm ? `${what}:${confirm}` : what);
      if (what === 'spec') {
        await json(r, { ok: true, text: `${id}: re-running verify with 1 review comment(s)` });
      } else if (!confirm) {
        // the card is at verify: the diff's comments are implement's, so
        // the board asks before it sends the card back (a 202 question)
        await json(r, { error: 'confirm', needs: 'confirm', text: `send ${id} back to implement? the diff comments are implement's to answer — verify runs again after it`, confirm: 'c0ffee' }, 202);
      } else {
        await json(r, { ok: true, text: `${id}: sent back to implement with the diff comments` });
      }
    });
  }
  await page.route(`${base}/pr`, (r) => json(r, pr));
  await page.route(`${base}/stats`, (r) => json(r, stats));
  await page.route(`${base}/answer`, async (r) => {
    m.answers.push(r.request().postDataJSON());
    const next = m.nextAnswer;
    if (next) await json(r, next.body, next.status);
    else await r.fallback();
  });
  return m;
}
