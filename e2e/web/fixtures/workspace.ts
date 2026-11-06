import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { agentScript, fakeGh, fakeGhData, fakeSsh, gummiBin } from './paths';

/**
 * A throwaway gummi workspace: a git repository holding a tiny Go module,
 * with a .gummi of its own configured so every role runs on the
 * scripted agent (scripts/web-e2e-agent.py) and every `gh` call lands on
 * fixtures/fake-gh.
 *
 * Layout under `root` (one temp dir, removed by dispose()):
 *
 *   repo/    the git repository and its .gummi workspace (cwd of every gummi)
 *   tmp/     TMPDIR for gummi and everything it runs
 *   gh/      this workspace's copy of fixtures/gh — fake-gh's answers
 *   bin/gh   a symlink to fake-gh, first on PATH (for anything that runs plain `gh`)
 *   logs/    agent.log (every protocol frame), gh.log (every gh argv),
 *            server logs
 *   config/  XDG_CONFIG_HOME, so the user's own gummi config never leaks in
 *
 * Seeding helpers drive a board of their own through the web API, as the
 * page does (POST /api/cards, an answer, the commit and merge actions), so a
 * seeded card is in exactly the state the product puts it in. That board is
 * started on the first seed and stopped by closeSeeder(), which the
 * fixtures call before the test's own server starts: the board's instance
 * lock admits one, so a helper is never called while that server runs.
 */
export class Workspace {
  readonly root: string;
  readonly repo: string;
  readonly logs: string;
  readonly ghData: string;
  readonly env: NodeJS.ProcessEnv;
  private seeder: Promise<{ server: import('./server').GummiServer; call: Call }> | undefined;
  private seederConfig = '';

  private constructor(root: string, extraEnv: NodeJS.ProcessEnv) {
    this.root = root;
    this.repo = path.join(root, 'repo');
    this.logs = path.join(root, 'logs');
    this.ghData = path.join(root, 'gh');
    const bin = path.join(root, 'bin');
    this.env = {
      ...process.env,
      PATH: `${bin}${path.delimiter}${process.env.PATH ?? ''}`,
      TMPDIR: path.join(root, 'tmp'),
      XDG_CONFIG_HOME: path.join(root, 'config'),
      GUMMI_AGENT: 'headless',
      GUMMI_AGENT_CMD: agentScript,
      GUMMI_ENVELOPE: '20',
      GUMMI_NOTIFY: 'off',
      GUMMI_GH_CMD: fakeGh,
      FAKE_GH_DATA: path.join(root, 'gh'),
      FAKE_GH_LOG: path.join(root, 'logs', 'gh.log'),
      // publishing: git's ssh is fake-ssh, serving the bare repositories
      // under remote/ as github.com (see addGitHubRemote)
      FAKE_GH_REMOTE: path.join(root, 'remote'),
      GIT_SSH_COMMAND: fakeSsh,
      GUMMI_E2E_FAST: '1',
      GUMMI_E2E_AGENT_LOG: path.join(root, 'logs', 'agent.log'),
      // a stray web address in the caller's shell must not leak into a test
      GUMMI_WEB_ADDR: '',
      // test-only: let `gummi web` accept and dial push endpoints on
      // loopback/private addresses, for a stand-in push service (a real
      // board refuses them: a paired device could otherwise aim the host's
      // POSTs at anything on its network)
      GUMMI_WEB_PUSH_ALLOW_PRIVATE: '1',
      ...extraEnv,
    };
  }

  /**
   * Create a fresh workspace. `env` adds or overrides environment for every
   * gummi process this workspace starts (e.g. GUMMI_E2E_SLOW_SECONDS).
   */
  static async create(opts: { env?: NodeJS.ProcessEnv; name?: string } = {}): Promise<Workspace> {
    const root = await fs.promises.mkdtemp(path.join(os.tmpdir(), `gummi-e2e-${opts.name ?? 'ws'}-`));
    const ws = new Workspace(root, opts.env ?? {});
    await ws.scaffold();
    return ws;
  }

  private async scaffold(): Promise<void> {
    for (const dir of ['repo/cmd/tiny', 'tmp', 'bin', 'logs', 'config']) {
      await fs.promises.mkdir(path.join(this.root, dir), { recursive: true });
    }
    await fs.promises.cp(fakeGhData, this.ghData, { recursive: true });
    await fs.promises.symlink(fakeGh, path.join(this.root, 'bin', 'gh'));

    const files: Record<string, string> = {
      'go.mod': 'module example.com/tiny\n\ngo 1.22\n',
      // A library package at the root and the command under cmd/: with a
      // single main package `go build ./...` would drop a binary into the
      // worktree, and gummi rightly blocks a branch that ships one.
      'greet.go': 'package tiny\n\n// Greet says hello.\nfunc Greet(name string) string { return "hello, " + name }\n',
      'greet_test.go':
        'package tiny\n\nimport "testing"\n\nfunc TestGreet(t *testing.T) {\n' +
        '\tif got := Greet("x"); got != "hello, x" {\n\t\tt.Fatalf("got %q", got)\n\t}\n}\n',
      'cmd/tiny/main.go':
        'package main\n\nimport (\n\t"fmt"\n\n\t"example.com/tiny"\n)\n\nfunc main() { fmt.Println(tiny.Greet("world")) }\n',
      '.gitignore': '.gummi/\n',
      'README.md': '# tiny\n\nA module small enough that its checks run in a second.\n',
    };
    for (const [name, body] of Object.entries(files)) {
      await fs.promises.writeFile(path.join(this.repo, name), body);
    }
    await this.git('init', '-q', '-b', 'main');
    await this.git('config', 'user.name', 'E2E Tester');
    await this.git('config', 'user.email', 'e2e@example.invalid');
    await this.git('config', 'commit.gpgsign', 'false');
    await this.git('add', '-A');
    await this.git('commit', '-q', '-m', 'init: a tiny module');

    // the board writes the rest of .gummi the first time it opens
    await fs.promises.mkdir(path.join(this.repo, '.gummi'), { recursive: true });
    await this.writeGummiFile(
      'config.yaml',
      [
        '# gummi e2e workspace',
        'permissions: allow-all',
        'sandbox: off',
        'workflows: on',
        '',
      ].join('\n'),
    );
    await this.writeGummiFile(
      'profiles.yaml',
      [
        'default: e2e',
        'profiles:',
        '  e2e: # every role on the scripted agent (GUMMI_AGENT_CMD)',
        '    architect: { backend: headless, model: e2e-architect }',
        '    implementer: { backend: headless, model: e2e-implementer }',
        '    reviewer: { backend: headless, model: e2e-reviewer }',
        '    scribe: { backend: headless, model: e2e-scribe }',
        '  e2e-alt: # a second profile, for the profile picker',
        '    architect: { backend: headless, model: e2e-alt-architect }',
        '    implementer: { backend: headless, model: e2e-alt-implementer }',
        '    reviewer: { backend: headless, model: e2e-alt-reviewer }',
        '    scribe: { backend: headless, model: e2e-alt-scribe }',
        '',
      ].join('\n'),
    );
  }

  /** Write a file under .gummi/ (config.yaml, profiles.yaml, …). */
  async writeGummiFile(name: string, body: string): Promise<void> {
    await fs.promises.writeFile(path.join(this.repo, '.gummi', name), body);
  }

  /** Remove the whole workspace. Kept when GUMMI_E2E_KEEP=1, for poking at. */
  async dispose(): Promise<void> {
    await this.closeSeeder();
    if (process.env.GUMMI_E2E_KEEP === '1') {
      console.log(`[e2e] kept workspace ${this.root}`);
      return;
    }
    await fs.promises.rm(this.root, { recursive: true, force: true });
  }

  // ------------------------------------------------------------------
  // running things
  // ------------------------------------------------------------------

  /** Run a command in the repository with the workspace's environment. */
  exec(cmd: string, args: string[], opts: { timeoutMs?: number; input?: string; cwd?: string } = {}): Promise<Exec> {
    return run(cmd, args, { cwd: opts.cwd ?? this.repo, env: this.env, timeoutMs: opts.timeoutMs, input: opts.input });
  }

  /** Run `gummi <args>`; never throws on a non-zero exit (see gummiOK). */
  gummi(args: string[], opts: { timeoutMs?: number; input?: string } = {}): Promise<Exec> {
    return this.exec(gummiBin, args, { timeoutMs: opts.timeoutMs ?? 120_000, input: opts.input });
  }

  /** Run `gummi <args>` and throw, with its output, unless it exits 0. */
  async gummiOK(args: string[], opts: { timeoutMs?: number; input?: string } = {}): Promise<Exec> {
    const r = await this.gummi(args, opts);
    if (r.code !== 0) throw new Error(`gummi ${args.join(' ')} exited ${r.code}\n${r.stdout}\n${r.stderr}`);
    return r;
  }

  /** Run git in the repository (or in `cwd`), throwing on failure. */
  async git(...args: string[]): Promise<string> {
    const r = await this.exec('git', args);
    if (r.code !== 0) throw new Error(`git ${args.join(' ')} exited ${r.code}\n${r.stderr}`);
    return r.stdout;
  }

  /** The card as the web API has it (GET /api/cards/{id}), on the seeding board. */
  async card(id: string): Promise<SeedCard> {
    return (await this.call('GET', `/api/cards/${id}`)) as SeedCard;
  }

  /** The card's worktree under .gummi/worktrees. */
  worktree(id: string): string {
    return path.join(this.repo, '.gummi', 'worktrees', id);
  }

  /** The agent protocol log: one line per frame, `IN|OUT <pid> <json>`. */
  agentLog(): string {
    return readIfExists(path.join(this.logs, 'agent.log'));
  }

  /** Every argv fake-gh was called with, oldest first. */
  ghCalls(): string[][] {
    return readIfExists(path.join(this.logs, 'gh.log'))
      .split('\n')
      .filter(Boolean)
      .map((l) => JSON.parse(l) as string[]);
  }

  /** Replace one of fake-gh's canned answers for this workspace (see fake-gh). */
  async setGh(file: string, data: unknown): Promise<void> {
    await fs.promises.writeFile(path.join(this.ghData, file), JSON.stringify(data, null, 2));
  }

  /**
   * Make fake-gh slow to answer `gh <cmd> <sub>`: the call waits until the
   * returned function is called (see fake-gh).
   */
  async holdGh(cmd: string, sub: string): Promise<() => Promise<void>> {
    const file = path.join(this.ghData, `hold-${cmd}-${sub}`);
    await fs.promises.writeFile(file, '');
    return () => fs.promises.rm(file, { force: true });
  }

  // ------------------------------------------------------------------
  // seeding — each returns the card id and leaves the card parked
  // ------------------------------------------------------------------

  /**
   * A session minted from title and driven until its first turn is
   * answered: the scripted agent's turn edits a file, so its diff is not
   * empty. Returns the session's id.
   */
  async seedSession(title: string, opts: RunOpts = {}): Promise<string> {
    const [id] = await this.mint([{ title, ...cardFields(opts) }]);
    return id;
  }

  /** A session whose work is committed on its branch — ready to land. */
  async seedReady(title: string, opts: RunOpts = {}): Promise<string> {
    const id = await this.seedSession(title, opts);
    await this.act(id, 'commit', { message: commitSubject(title) });
    return id;
  }

  /**
   * A session whose agent is waiting on an ask_user question with two
   * options ("Left", "Right"). The [ask] keyword rides a line of its own
   * under the title, so the card keeps the title it was given.
   */
  async seedAsk(title: string, opts: RunOpts = {}): Promise<{ id: string; question: string; options: string[]; decision: string }> {
    const [id] = await this.mint([{ title, ...(title.includes('[ask]') ? {} : { description: '[ask]' }), ...cardFields(opts) }]);
    const deadline = Date.now() + 30_000;
    for (;;) {
      const d = (await this.card(id)).decision;
      if (d?.kind === 'ask') {
        return { id, question: d.question, options: d.options.filter((o) => /^\d+$/.test(o.id)).map((o) => o.label), decision: d.ref };
      }
      if (Date.now() > deadline) throw new Error(`${id} never asked its question: ${JSON.stringify(d)}`);
      await sleep(100);
    }
  }

  /** A ready session landed on main with the merge action (stage done). */
  async seedLanded(title: string, message = commitSubject(title), opts: RunOpts = {}): Promise<string> {
    const id = await this.seedReady(title, opts);
    await this.land(id, message, opts);
    return id;
  }

  /** Land id with the merge action and wait until it is done. */
  async land(id: string, message: string, opts: { keepBranch?: boolean } = {}): Promise<void> {
    await this.act(id, 'merge', { message, keepBranch: !!opts.keepBranch });
    await this.until(id, (c) => c.stage === 'done', 'landed');
  }

  /**
   * Sessions (freeform cards), one per title, each with its title as its
   * opening turn and that turn answered. `extra` is sent with each card
   * (e.g. `{ repo: 'lxd' }`).
   */
  async seedSessions(titles: string[], extra: Record<string, unknown> = {}): Promise<string[]> {
    return this.mint(titles.map((title) => ({ title, ...extra })));
  }

  /** Answer the card's open ask with the option labelled `label`. */
  async answer(id: string, label: string): Promise<void> {
    const d = (await this.card(id)).decision;
    const o = d?.options.find((x) => x.label === label);
    if (!d || !o) throw new Error(`${id} has no option "${label}" to answer: ${JSON.stringify(d)}`);
    await this.call('POST', `/api/cards/${id}/answer`, { ref: d.ref, option: o.id, against: d.against.token });
    await this.until(id, (c) => c.status !== 'running' && c.decision?.ref !== d.ref, 'settled after its answer');
  }

  /** A stack named name holding cards, bottom first. */
  async stack(name: string, cards: string[]): Promise<void> {
    await this.call('POST', '/api/stacks', { name, card: cards[0], cards: cards.slice(1) });
  }

  /** A schedule, as the schedules view defines one (POST /api/schedules). */
  async addSchedule(def: Record<string, unknown>): Promise<void> {
    await this.call('POST', '/api/schedules', def);
  }

  /** Stop the seeding board, if one is up. */
  async closeSeeder(): Promise<void> {
    const s = this.seeder;
    this.seeder = undefined;
    if (s) await (await s).server.stop();
  }

  /** Call the web API on the seeding board; throws on an error status. */
  async call(method: string, pathname: string, body?: unknown): Promise<any> {
    const r = await (await this.board()).call(method, pathname, body);
    if (r.status >= 300) throw new Error(`${method} ${pathname} → ${r.status}: ${r.text}`);
    return r.json;
  }

  /**
   * The seeding board: one started for it on a free port with no
   * pairing, kept until closeSeeder().
   */
  private async board(): Promise<{ server: import('./server').GummiServer; call: Call }> {
    // a board reads config.yaml when it opens: a seed that changed it
    // (a repos: table) gets a board that has read the change
    const config = configStamp(this.repo);
    if (this.seeder && config !== this.seederConfig) await this.closeSeeder();
    this.seederConfig = config;
    this.seeder ??= (async () => {
      const { GummiServer } = await import('./server');
      const server = await GummiServer.start(this, { args: ['--no-pairing'] });
      const call: Call = async (m, p, b) => {
        const res = await fetch(server.url + p, {
          method: m,
          headers: { Origin: server.url, 'Content-Type': 'application/json' },
          body: b === undefined ? undefined : JSON.stringify(b),
        });
        const text = await res.text();
        let json: any;
        try {
          json = text ? JSON.parse(text) : undefined;
        } catch {
          json = undefined;
        }
        return { status: res.status, json, text };
      };
      return { server, call };
    })();
    return this.seeder;
  }

  /**
   * Run a card action, saying yes to every confirmation it stops on, as a
   * person pressing through the page's dialogs would.
   */
  async act(id: string, action: string, body: Record<string, unknown> = {}): Promise<void> {
    const { call } = await this.board();
    const confirms: string[] = [];
    for (let i = 0; i < 5; i++) {
      const r = await call('POST', `/api/cards/${id}/actions/${action}`, { ...body, ...(confirms.length ? { confirm: confirms.join(',') } : {}) });
      if (r.status === 202 && r.json?.error === 'confirm' && r.json.confirm) {
        confirms.push(r.json.confirm);
        continue;
      }
      if (r.status >= 300) throw new Error(`${action} on ${id} → ${r.status}: ${r.text}`);
      return;
    }
    throw new Error(`${action} on ${id} kept asking to confirm`);
  }

  /** Link a card to fake-gh's PR (7 by default) with the card's "link PR…". */
  async linkPR(id: string, number = 7): Promise<void> {
    await this.act(id, 'prlink', { message: String(number) });
    await this.until(id, (c) => !!c.pr || !!c.pullRequest || JSON.stringify(c).includes(`#${number}`), 'linked');
  }

  /** Mint cards through POST /api/cards and wait for every opening turn to settle. */
  private async mint(cards: Array<Record<string, unknown>>): Promise<string[]> {
    const ids: string[] = [];
    for (const card of cards) {
      const c = await this.call('POST', '/api/cards', { kind: 'freeform', ...card });
      ids.push(String(c?.id ?? c?.card?.id));
    }
    for (const id of ids) await this.until(id, (c) => c.status !== 'running', 'done with its opening turn', 60_000);
    return ids;
  }

  /** Wait until the card reads as ok says. */
  private async until(id: string, ok: (c: SeedCard) => boolean, what: string, ms = 30_000): Promise<SeedCard> {
    const deadline = Date.now() + ms;
    for (;;) {
      const c = await this.card(id);
      if (ok(c)) return c;
      if (Date.now() > deadline) throw new Error(`${id} never ${what}: ${JSON.stringify({ stage: c.stage, status: c.status, decision: c.decision })}`);
      await sleep(100);
    }
  }

  /**
   * Give the repository a github.com remote that works: `origin` is
   * git@github.com:e2e/tiny.git, and fake-ssh serves it from a bare
   * repository under remote/ that starts with main pushed. With it (and
   * fake-gh signed in) the board offers its publish acts.
   */
  async addGitHubRemote(repo = 'e2e/tiny'): Promise<void> {
    const bare = this.remotePath(repo);
    await fs.promises.mkdir(path.dirname(bare), { recursive: true });
    await this.git('init', '-q', '--bare', '-b', 'main', bare);
    await this.git('remote', 'add', 'origin', `git@github.com:${repo}.git`);
    await this.git('push', '-q', 'origin', 'main');
  }

  /** The bare repository standing in for github.com/<repo>. */
  remotePath(repo = 'e2e/tiny'): string {
    return path.join(this.root, 'remote', `${repo}.git`);
  }
}

// ----------------------------------------------------------------------
// types and helpers
// ----------------------------------------------------------------------

export interface Exec {
  code: number | null;
  stdout: string;
  stderr: string;
}

type Call = (method: string, pathname: string, body?: unknown) => Promise<{ status: number; json: any; text: string }>;

/** What a seeding helper forwards with the card it mints. */
export interface RunOpts {
  envelope?: number;
  repo?: string;
  profile?: string;
  base?: string;
  keepBranch?: boolean;
}

/** The fields of GET /api/cards/{id} the helpers and the harness read (it carries more). */
export interface SeedCard {
  id: string;
  kind: string;
  title: string;
  stage: string;
  status: string;
  branch?: string;
  decision?: { ref: string; kind: string; question: string; options: Array<{ id: string; label: string }>; against: { token: string } };
  [k: string]: unknown;
}

function cardFields(o: RunOpts): Record<string, unknown> {
  const f: Record<string, unknown> = {};
  if (o.envelope !== undefined) f.envelope = o.envelope;
  if (o.profile) f.profile = o.profile;
  if (o.base) f.base = o.base;
  if (o.repo) f.repo = o.repo;
  return f;
}

/** config.yaml's size and change time, "" while there is none. */
function configStamp(repo: string): string {
  try {
    const st = fs.statSync(path.join(repo, '.gummi', 'config.yaml'));
    return `${st.size}:${st.mtimeMs}`;
  } catch {
    return '';
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

/** A Conventional Commits subject a landing or commit accepts, from a title. */
export function commitSubject(title: string): string {
  const words = title.toLowerCase().replace(/\[[^\]]*\]/g, ' ').replace(/[^a-z0-9 ]+/g, ' ').replace(/\s+/g, ' ').trim();
  return `feat: ${words || 'work'}`;
}

function readIfExists(p: string): string {
  try {
    return fs.readFileSync(p, 'utf8');
  } catch {
    return '';
  }
}

export function run(
  cmd: string,
  args: string[],
  opts: { cwd: string; env: NodeJS.ProcessEnv; timeoutMs?: number; input?: string },
): Promise<Exec> {
  return new Promise((resolve, reject) => {
    const child = spawn(cmd, args, { cwd: opts.cwd, env: opts.env, stdio: ['pipe', 'pipe', 'pipe'] });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (d) => (stdout += d));
    child.stderr.on('data', (d) => (stderr += d));
    const timer = setTimeout(() => {
      child.kill('SIGKILL');
      reject(new Error(`${cmd} ${args.join(' ')} timed out after ${opts.timeoutMs}ms\n${stdout}\n${stderr}`));
    }, opts.timeoutMs ?? 60_000);
    child.on('error', (err) => {
      clearTimeout(timer);
      reject(err);
    });
    child.on('close', (code) => {
      clearTimeout(timer);
      resolve({ code, stdout, stderr });
    });
    // a child that exits before reading its stdin makes the write fail
    // with EPIPE; that is not the command's failure, its exit code is
    child.stdin.on('error', () => {});
    child.stdin.end(opts.input ?? '');
  });
}
