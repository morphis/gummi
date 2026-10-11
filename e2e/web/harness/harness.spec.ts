import { expect, GummiServer, test } from '../fixtures/test';

// These prove the harness, not the web UI: the scripted agent, the seeding
// helpers and fake gh, all through the real CLI. No browser, no server
// (except the last test, which waits for `gummi web` to exist).

test.describe.configure({ mode: 'serial' });

test('a default card plans, implements and verifies with real checks', async ({ workspace: ws }) => {
  const id = await ws.seedDesignGate('Add a wave helper');
  expect(id).toMatch(/^FD-\d+$/);

  let st = await ws.status(id);
  expect(st.stage).toBe('plan');
  const spec = await ws.gummiOK(['spec', id]);
  for (const section of ['## Problem', '## Chosen approach', '## Implementation notes', '## Verification plan']) {
    expect(spec.stdout).toContain(section);
  }
  expect(spec.stdout).toContain('```gummi-checks');
  expect(spec.stdout).toContain('go build ./...');

  const r = await ws.approve(id);
  expect(r.events.map((e) => e.event)).toContain('verified');
  st = await ws.status(id);
  expect(st.stage).toBe('verify');
  expect(st.verified).toBe(true);

  // the implementer's change is real and committed on the card's branch
  const files = await ws.git('-C', ws.worktree(id), 'show', '--name-only', '--format=', 'HEAD~1', 'HEAD');
  expect(files).toMatch(/fd\d+\.go/);
  const diff = await ws.gummiOK(['diff', id]);
  expect(diff.stdout).toContain(`func ${id.replace('-', '').replace('FD', 'Fd')}()`);
});

test('[fail-check] fails a real check at verify', async ({ workspace: ws }) => {
  const id = await ws.seedVerifyFailed('Add a regressing helper');
  const st = await ws.status(id);
  expect(st.stage).toBe('verify');
  expect(st.verified).toBe(false);
  expect(st.escalation?.kind).toBe('verify');
  expect(ws.agentLog()).toContain('test: FAIL (exit');
});

test('[fail-verify] fails verify with every check passing', async ({ workspace: ws }) => {
  const id = await ws.seedVerifyFailed('Add a wrong helper', { how: 'verdict' });
  const st = await ws.status(id);
  expect(st.escalation?.kind).toBe('verify');
  expect(ws.agentLog()).not.toContain('FAIL (exit');
});

test('[ask] stops on an open question, and the answer lands in the spec', async ({ workspace: ws }) => {
  const ask = await ws.seedAsk('Add a choosy helper');
  expect(ask.options).toEqual(['A new file (recommended)', 'Extend the existing file']);
  let st = await ws.status(ask.id);
  expect(st.stage).toBe('plan');
  expect(st.escalation?.kind).toBe('ask');

  const r = await ws.answer(ask.id, 'Extend the existing file');
  expect(r.events.at(-1)?.event).toBe('stopped');
  // the answer closed the question; the run then stopped at the design
  // gate --until names, which is a decision of its own, waiting on you
  st = await ws.status(ask.id);
  expect(st.escalation?.kind).toBe('gate');
  expect(st.escalation?.reason).toContain('where the run was asked to stop');
  const spec = await ws.gummiOK(['spec', ask.id]);
  expect(spec.stdout).toContain('Decided with the user: Extend the existing file');
});

test('a landed card is done and on main', async ({ workspace: ws }) => {
  const id = await ws.seedLanded('Add a nod helper', 'feat: add a nod helper');
  const st = await ws.status(id);
  expect(st.stage).toBe('done');
  expect(await ws.git('log', '-1', '--format=%s', 'main')).toContain('feat: add a nod helper');
});

test('backlog, bug and research cards', async ({ workspace: ws }) => {
  const backlog = await ws.seedBacklog(['Add a wave helper', 'Add a shrug helper']);
  expect(backlog).toHaveLength(2);
  const bug = await ws.seedBug('Greet panics on an empty name', { severity: 'high' });
  for (const id of [...backlog, bug]) expect((await ws.status(id)).stage).toBe('todo');

  const rs = await ws.seedResearch('How does the module greet');
  expect(rs).toMatch(/^RS-\d+$/);
  const st = await ws.status(rs);
  expect(st.kind).toBe('research');
  expect(st.stage).toBe('plan');
  const doc = await ws.gummiOK(['spec', rs]);
  expect(doc.stdout).toContain('## Direction');
  expect(doc.stdout).toContain('Survey `greet.go`');
});

test('fake gh answers pr link/status/comments and bug import', async ({ workspace: ws }) => {
  const id = await ws.seedVerified('Add a farewell helper');
  await ws.linkPR(id, 7);
  const status = await ws.gummiOK(['pr', 'status', id]);
  expect(status.stdout).toContain('e2e/tiny#7');
  expect(status.stdout).toContain('Comments: 1');

  const comments = await ws.gummiOK(['pr', 'comments', id, '--json']);
  const threads = JSON.parse(comments.stdout);
  expect(threads.threads).toHaveLength(1); // the resolved one is dropped
  expect(threads.threads[0].root_body_first_line).toBe('Should this helper take a name?');

  const bugs = await ws.importBugs({ comments: true });
  expect(bugs).toHaveLength(2); // #103 is closed

  const calls = ws.ghCalls().map((c) => c.slice(0, 2).join(' '));
  expect(calls).toEqual(expect.arrayContaining(['pr view', 'api repos/e2e/tiny', 'api graphql', 'issue list', 'issue view']));

  // a test can swap an answer for its own workspace
  await ws.setGh('repo.json', { allow_squash_merge: false });
  await ws.gummiOK(['pr', 'unlink', id]);
  const relink = await ws.gummiOK(['pr', 'link', id, '7']);
  expect(relink.stderr).toContain('does not allow squash-merge');
});

test.describe('slow', () => {
  test.use({ workspaceEnv: { GUMMI_E2E_SLOW_SECONDS: '1' } });

  test('[slow] streams its reply in deltas', async ({ workspace: ws }) => {
    const started = Date.now();
    await ws.seedDesignGate('[slow] Add a lazy helper');
    expect(Date.now() - started).toBeGreaterThan(1_500); // plan + critique, ~1s each
    const deltas = ws.agentLog().split('\n').filter((l) => l.startsWith('OUT') && l.includes('"type": "text"'));
    expect(deltas.length).toBeGreaterThan(10);
  });
});

test('gummi web serves, pairs and refuses a second instance', async ({ workspace: ws }) => {
  const help = await ws.gummi(['web', '--help']);
  test.skip(help.code !== 0 || !/--addr/.test(help.stdout + help.stderr), '`gummi web` does not exist in this build yet');

  const server = await GummiServer.start(ws);
  try {
    expect(server.code).toMatch(/^\d{6}$/);
    const session = await (await fetch(`${server.url}/api/session`)).json();
    expect(session.authed).toBe(false);

    const res = await fetch(`${server.url}/api/pair`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Origin: server.url },
      body: JSON.stringify({ code: await server.freshCode(), name: 'Harness' }),
    });
    expect(res.status).toBe(200);
    expect(res.headers.get('set-cookie')).toBeTruthy();

    const second = await server.startSecond();
    expect(second.code).not.toBe(0);
    expect(second.stdout + second.stderr).toContain('served by gummi web');
  } finally {
    await server.stop();
  }
});
