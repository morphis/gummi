# AGENTS.md

Guidance for AI agents working on **gummi**. Read this first, then
`docs/DESIGN.md` before making non-trivial changes.

## What gummi is

gummi is a **meta-harness for coding agents**: a single binary that
runs a fleet of coding agent sessions, each on its own git worktree and
branch, and lets a session run a **workflow** — a recipe of
fresh-context steps working in its own tree — when the work is worth
one. It orchestrates other agents — it is
not itself a coding agent. A person drives it from the keyboard in the
TUI or from a browser through its web face, `gummi web`; there is no
command-line driver. An agent working *inside* a card reaches gummi's
tools only through that card's own session MCP endpoint (§16) — there is
no board-level agent.

- **Language:** Go 1.26, single module `github.com/morphis/gummi`.
- **Binary:** `cmd/gummi` → `bin/gummi`. Run with no args inside a git repo.
- **TUI stack:** Bubbletea v2 / Lipgloss v2 / Bubbles v2 (Charm).
- **Storage:** SQLite (`modernc.org/sqlite`, pure-Go, no cgo).
- **Runtime workspace:** a `.gummi/` dir created lazily in the target
  repo (state DB, `config.yaml`, `profiles.yaml`, `worktrees/`, `specs/`,
  `workflows/`, `runs/`, `homes/`). Gitignored.

The mental model (DESIGN §24 and §25, decisions 27 and 28): every unit
of work is a **session** — a freeform card (`FF`, `KindFreeform`)
holding `StageOpen` until it is closed at `StageDone`, a coding agent on
a branch and worktree of its own. There is no stage graph and no gate
the session must cross. An **agent** is a session with a home of its
own instead of a repository (`Feature.Home`).

```
session ──talk──▶ spec n ──workflow_run──▶ proposal ──you allow──▶ run ──▶ report
   ▲                          (steps work in the session's own tree)        │
   └──── the session reads the report and the diff, commits what it keeps ──┘
   lands on a person's read of its diff
```

- A session may keep **specs, one per piece of work**
  (`engine/sessionspec.go`: `spec_start`, `spec_list`, `spec_view`,
  `spec_replace_section`, `spec_annotate`, each taking the spec's number),
  stored under `.gummi/specs/`, never on the branch; every write is a
  numbered revision of that spec in the store. A spec's state (`draft`,
  `running`, `met`, `partial`) comes from its runs.
- A session may run a **workflow** (`engine/workflow.go`): a **recipe**
  of steps kept as YAML (`.gummi/workflows/<name>.yaml`, else the
  bundled `deliver`, `investigate` and `fix-until-green`, or handed over
  inline), run against one of its specs. `workflow_run` proposes the
  run; the person allows it (or always allows the session's runs). A
  run estimates nothing: it spends from the session's envelope until
  that is spent, then pauses for a top-up. Each step is a fresh-context agent of a role (`scout`,
  `planner`, `contractor`, `implementer`, `tester`, `scribe`,
  `reviewer`) on a hidden child card whose working directory is the
  session's own checkout — one after another, side by side in a
  `parallel`, or round a `repeat` until its condition holds. Nothing is
  branched, landed or committed by gummi: it snapshots the tree at every
  step boundary, so each step has a diff of its own and a person can
  **Revert run**. At the end gummi runs the frozen spec's done-when
  checks and records each item's result, writes the report into the
  spec and sends it to the session; the spec reads `met` when every
  checked item passed and none is unjudged. The session (or the person)
  reads the diff and commits.
- A **profile** maps roles to concrete models. A step runs on its
  role's slot (`scout`, `planner`, `contractor`, `implementer`, `tester`,
  `scribe`, `reviewer`) or one its recipe names (`slot:`), falling back
  to `architect` for design work and then `implementer`; the session
  itself runs on `implementer`. A person edits profiles from the board
  (`P` in the TUI, Profiles on the web page).

Cards of the retired workflow kinds (feature, bug, research, goal) may
still sit in an old database: they are read-only, and gummi refuses to
run them.

Read `README.md` for the user-facing overview; the full key table is `?`
in the TUI.

## Package map (`internal/`)

Each package has a real doc comment at the top of its lead file — read it
before touching the package. Dependency flow is roughly
domain → state/spec/workflowrecipe/workflowpolicy → engine → ui, with
agent/worktree/verify as leaf services.

| package | responsibility |
|---|---|
| `domain` | Core types: a session's attached repositories (`attachment.go`) and a run's snapshot of several trees (`treeset.go`), cards (sessions and agents, plus the read-only rows of the retired workflow kinds), stages, a session's specs, workflow recipes, runs, their steps and decisions (`workflow.go`), a session's hidden children — a step's card, and an old crew's (`child.go`: `ChildLink`, `StepLink`, `IsChild`) — and done-when items. No I/O. |
| `spec` | The markdown artifacts and their blocks: a session's spec template (`session.go`), the `gummi-done-when` block (`donewhen.go`) and `gummi-checks`, and the `%%` marker grammar for anchored comments. |
| `state` | SQLite store: cards, sessions, a session's specs and their revisions (`sessionspecs.go`), workflow runs and their steps (`workflow.go`), whether a session always allows its runs and an old database's crews, read only (`legacycrew.go`, `workflowmigrate.go`), diff annotations, card events, sequences, the sessions an agent dispatched (`dispatch.go`), the repositories a session attached (`attachments.go`), workspace. |
| `workflowrecipe` | Pure rules for a workflow recipe (DESIGN §25.4): `Parse` the YAML, `Check` it (ids, roles, slots, templates, `repeat` and `parallel` shapes; a warning for two writers side by side, and for a recipe or step still naming an `envelope` or a `tier`, which are ignored), `Fill` a step's templates, `Load`/`List` the workspace's recipes and the bundled ones (`bundled/`), read the lines a step's output may carry (a planner's `gummi-workflow` block, `FAILED:`, `MET:`/`UNMET:`, `DECISION:`, `OPEN:`), and capture an artifact a step declared but did not write from its output (`CaptureArtifact`). |
| `workflowpolicy` | Pure conductor of a running run (DESIGN §25.7): `Decide` takes a snapshot and returns the actions — start ready steps, interrupt, judge a `repeat`'s `until`, ask at its max or when its `until` could not be judged, pause on a spent pool, end. Nothing is estimated. `Engine.WorkflowTick` builds the snapshot and executes them. |
| `engine` | The orchestrator. Runs sessions on agent backends, routes turns, streams activity, and conducts workflow runs. Start here to trace behavior. A run is `workflow.go` (lifecycle, the session's `workflow_*` tools, the person's answers, pause/resume/stop/steer, `WorkflowTick`, a run's end and report), `workflowproposal.go` (what a proposal shows), `workflowstep.go` (a step's card, brief, snapshots and the person's messages to it), `workflowtree.go` (the run's directory, snapshots and diffs, end checks, Revert run) and `workflowfaces.go` (what the faces read); `pool.go` is the session's pool and the helpers they share; the session's specs are `sessionspec.go`. `attach.go` is a session's attached repositories (`AttachRepo`, `DetachRepo`, `RepoViews`, DESIGN §27) and `repotools.go` its `repo_list`, `repo_attach` and `repo_detach`; `workflowcmd.go` is a `run:` command step and a run's quiet end, `workflowdispatch.go` a `dispatch` step and an agent's held envelopes, `freeze.go` the recipes a proposal copies in, and `agenttools.go` an agent's `schedule_propose`, `session_status` and `session_send` (DESIGN §26). |
| `agent` | Adapter layer over concrete agents. Interfaces hide the backend: `copilot` (default), `claude`, `opencode` — the three supported agents — plus `headless` (the line protocol the demo and web e2e drive) and `fake.go` for tests. |
| `cardmint` | The one recipe for minting a card: a session, or an agent in a home of its own. Refuses the retired kinds. |
| `worktree` | Per-card git worktrees under `.gummi/worktrees/`: create, rebase-on-main, dirty/landed detection, squash landing, cleanup. An agent's home is `home.go`. A repository attached to a session is the card's `AttachedView` (`Feature.TreeRepo`), its tree named `<ID>@<repo>` by `featurePaths`. A branch gummi did **not** cut — a legacy adopted card's — keeps its custody rules here (`Feature.Adopted`, `Manager.Attach`, `ErrAdoptedBranch`, `branchmove.go`). `tree.go` is a person working a card's tree by hand — stage, hunk and line staging, discard, commit, the branch's own stashes (DESIGN §22.13); `ui/webtree.go` refuses every write while an agent is mid-turn. `snapshot.go` is a workflow run's view of the tree: `Snapshot` (a tree hash through a temporary index, touching nothing), `DiffTrees` and `RevertTo`. |
| `verify` | Runs check commands in a tree and reports pass/fail: a workflow run's done-when checks, at its end, and a `repeat`'s `until: checks`. A passing `go test -run` that ran no tests says so (`RanNoTests`), and its item is unjudged, never a pass. |
| `stack` | Pure policy for a **stack** — a chain of cards whose branches fork from one another. Answers what each card forks from, which are sitting on commits that have moved, and whether one may land yet. No git, no store, no clock. Read by `Engine.StackTick`, the worktree base seam and the board alike. |
| `schedule` | Pure policy for **schedules and heartbeats** — freeform sessions that come back on a clock. Cron parse, compile, `Due`, `Next` and `Advance` (missed fires coalesce). No clock, no IO, no store. Read by the store's write boundary (`Check`), `Engine.ScheduleTick` and the faces. |
| `cardrun` | Pure read model: one card's record → how it ran (its passes, what each cost, how much was rework, how long it waited). Shared by the card's run tab and the week view. |
| `branchlog` | Pure read model of a card's own commits (`Manager.Log` → rows: checkpoint, pushed, attribution) and the one rule for whether they may be rewritten (`Refusal`). Its `Env` runs the reads and `Manager.Rewrite`. Read by the TUI's log tab and the web page's (DESIGN §21). |
| `repoview` | Pure read model of one repository's local branches: who holds each (the base, a card, a legacy adopted card or goal, nobody) and the one rule for which may be deleted from the repositories view — only a branch no card holds, nothing has checked out and no card forks from. The git reads and the three writes (fetch, fast-forward the base, delete a branch) are `worktree/repoview.go`, and a repository's remotes and a branch's upstream — git-config writes only — are `worktree/remotes.go`; how a remote closes cards (PR or push of the base, DESIGN §22.12) and that base push are `worktree/closepush.go`; the web face reads all of it through `ui/webrepos.go`. |
| `fleetrun` | Pure fold at the workspace scale: every card's run → the stats tab's report (window and all-time money, the clock, peak concurrency, and the timeline lanes). Charges a pass to the window it started in; reuses `cardrun` per card, so the tab cannot disagree with the cards it is made of. |
| `publish` | A **person** publishing a card (DESIGN §22): pure rules (`Refusal`, `Floor`, `PlanFor`) and the `Env` that reads the facts a person confirms (`Resolve`, `Fingerprint`) and runs the push and the `gh` calls (`Do`). Plans and publishes a session's attached repositories too (`span.go`: one push and PR each, bodies naming the others). Imported only by the person-facing faces (`ui/publish.go`); `TestOnlyPersonFacingCodeImportsPublish` fails if the engine, MCP shim or a loop does. Where a branch pushes is `worktree.Manager.PushTarget` (`pushtarget.go`), read by the pushed mark and the printed push line too. |
| `credentials` | The GitHub token and SSH key a person may store from the web page's settings (DESIGN §22.2), 0600 under `.gummi/state/credentials/`. `WithToken` sets `GH_TOKEN` on a `gh` command gummi runs; `WithAgent` answers as a sign-only ssh-agent for the life of one git command. Behind a settings switch the key also signs every commit (`sign.go`): gummi names itself git's signing program through `GIT_CONFIG_*` in its own environment, which every child inherits, and `main` answers that call before cobra sees it. Never export either secret to an agent backend. |
| `skillpack` | The workspace skill pack (DESIGN §23): `.gummi/skills`, its curator's writes, a card's staged changes and their delivery to claude, copilot and opencode. Also gummi's own skills (`builtin/`: `workflows`, `workflow-recipes`), shipped in the binary and delivered beside the pack (`Builtin`); change them with the workflow code they teach, and `TestEveryRecipeTheSkillsShowCanRun` holds their examples to the grammar. |
| `diffannot` | Anchors line comments to diff content (survives minor rebases). |
| `config` | Loads `.gummi/config.yaml` (permission mode only, since M5). |
| `notify` | Terminal bell / desktop notification on needs-attention. |
| `atomicfile` | Crash-safe file writes for pre-approval drafts (no git backstop). |
| `rmtree` | `RemoveAll` that also takes read-only trees (a Go module cache under an agent home) without following links. Use it for any per-card directory an agent may have built in. |
| `childproc` | How gummi starts a process it supervises: `Group` (own process group, killed whole on cancel) and `Start` (on Linux, dies with gummi even when gummi is killed outright). Use both for any agent backend, probe or watch you spawn. |
| `term` | A person's own shell on a pty in a card's worktree, for the web face's Terminal tab (`gummi web --terminal`): one session per card, bounded scrollback replayed on reattach, a cap and an idle sweep. Not an agent surface and not sandboxed; `web/routes_term.go` decides who may open one (DESIGN §20.5). |
| `gittest` | Blank-imported by every test package that builds a git repository in a `t.TempDir`: switches off git's detached auto-maintenance, which otherwise outlives the command a test ran and fails the TempDir cleanup. A bare remote a test pushes to sets `maintenance.auto false` itself — git does not pass the switch to the receiving end. |
| `ui` | The Bubbletea TUI: board, card thread, diff/spec views, inbox, dialogs. `workflowloop.go` ticks every session's live run; `runblock.go` and `runkeys.go` draw a run's block in the thread, its live strip above the composer, and what a person does to it (alt+w jumps to it), `stepview.go` a step opened from it; `webrun.go` and `websessionspec.go` are what the web page's run block, step panel and Specs tab are told. |
| `sandbox` | Resolves effective confinement (`enforce`/`warn`/`off`) from config, profile, and backend capabilities; shared by engine refusal and `doctor`. |
| `engine/freeformsession.go` | A session's whole working life — its worktree, its per-card lock and its envelope. It never commits for the agent: every commit on its branch is one the agent made on purpose. Built from `consultsession.go`'s lifecycle; read the two together. `engine/sessionmodel.go` holds the agent and model a session picks for itself, starting a backend the board did not launch, and switching mid-conversation, and `ui/websession.go` holds the picker's catalog (DESIGN §19.8). |
| `web` | `gummi web`: serves the board to a browser as a page of its own (DESIGN §20). Holds no board: it asks the TUI's model, run headless behind `ui.Bridge`, for every read and write. Pairing and device tokens (`pair.go`), same-origin and auth (`auth.go`), the SSE hub (`hub.go`), one `routes_*.go` per area (a session's runs, steps and specs are `routes_runs.go`), and the page itself under `assets/` (plain ES modules, no build step, strict CSP — no inline script or style; a run's block, its live strip above the composer, the run-above pill and the step panel are `workflow.js`). |
| `web/push` | Web Push from the host with the standard library alone: VAPID identity, RFC 8291 encryption, the per-device subscription store, and the fan-out notifier the board's attention hook posts to. |
| `webapi` | The JSON contract between `internal/web` and its page: types only, golden-tested shapes. Change a shape here first. |
| `threadfold` | Pure fold of a card's event log into what a thread draws — its sessions, answered decisions, and the structured `Items` the web page renders — in the words the TUI thread uses. A session's workflow runs fold into one item each (`runs.go`: `Options.Runs`, `FoldRun`, keyed `run:<n>` with a content-hash `Rev`), an open one carrying its live strip (`runstrip.go`: `RunStrip`). Both faces read it. |
| `decisions` | Pure rules for a card's open decisions: which one a card shows (`Rank`), its attention lane, how an `ask_user` question is offered as options, a run's decision too (`RunOptions`), and what an answer says. Both faces read it. |
| `mcp` | Backs the hidden `gummi __mcp` shim: bridges an agent backend's MCP stdio calls to a live session's tools (`--feature <id>`). |

`cmd/gummi` holds `main.go` and the few commands there are: the board (no
arguments), `web` (with `pair`, `devices` and `unpair`), `doctor`, `version`
and the hidden `__mcp` shim. Everything else a person does is in the TUI or
on the web page; do not add a command-line verb for it.

**Flags are declared once.** Cobra owns routing, help, completion *and*
parsing: every flag is declared in `cobra.go`'s `bind*Flags` functions, and a
command body reads what cobra parsed through `cliFlags`. Do not add a
`flag.NewFlagSet` to a command — `TestNoCommandParsesItsOwnFlags` fails if you
do. Tests drive commands through the real tree via `runCLI` rather than
calling `runXxx` directly.

`internal/deps.go` (build tag `pin`) blank-imports the pinned Charm stack
— that's why `make build` runs `go build -tags pin ./...` too.

## Build, test, lint

Use the Makefile targets — don't hand-roll `go` invocations:

```sh
make build          # go build -o bin/gummi ./cmd/gummi  (+ -tags pin ./...)
make test           # go test ./...
make lint           # go vet + golangci-lint (v2 config in .golangci.yml)
make ci             # build + test + lint — run this before considering work done
make golden-update  # regenerate UI snapshot goldens (see below)
make triage-eval    # score triage's prompt on real claude (spends money; never in ci)
```

Scoped iteration while developing:

```sh
go test ./internal/engine/...          # one package
go test ./internal/ui/ -run TestBoard  # one test
go test -tags pin ./...                # match the build tag when compiling everything
```

## Testing conventions

- **86+ tests, no network, no real agents.** Tests use the in-process
  `internal/agent/fake.go` — never spawn `copilot`
  or hit an API in a test. Follow that pattern for new engine/UI tests.
- **UI golden files.** Several `internal/ui/...` packages snapshot
  rendered output into `testdata/` via `x/exp/golden`. After an
  intentional UI change, run `make golden-update`, then **inspect the
  diff** before committing — goldens are the review surface.
- **SQLite store** is pure-Go; tests spin up throwaway DBs. Injectables
  like `CreatedAt at time.Time` exist so tests stay deterministic — use them.
- `_test.go` files are exempted from `gosec`/`errcheck` in the linter
  config; don't paper over real issues by moving code into tests.

## Running / trying it live

```sh
make demo   # throwaway repo with gummi initialized — safe sandbox to poke the TUI
make e2e    # scripted TUI drive asserting the full lifecycle (needs tmux)
```

Running the real TUI (`bin/gummi`) needs a git repo and, for the default
backend, an authenticated GitHub Copilot CLI. To drive agents without
Copilot auth, set `GUMMI_AGENT=headless` with `GUMMI_AGENT_CMD` pointed at
a BYOK/local endpoint's adapter (`GUMMI_HEADLESS_CREDITS_PER_1K` prices its
spend into credits). With no usable agent, creating sessions, their
worktrees and specs still work — the board just stays static. Key env vars are tabled in
`docs/CONFIGURATION.md`.

## Conventions & guardrails

- **gummi offers the process; it no longer enforces it** (decision 27,
  DESIGN §24). There is no stage graph, no stage gate and no
  configuration that adds one back. A session lands on a **person's read
  of its diff**: `domain.Feature.MayLand` refuses only a card with no
  landing of its own, unresolved diff comments hold a landing through
  the open-comment check every landing crosses, an open workflow run
  holds its session's (`Engine.RunHolds`), and a stacked card waits for the cards
  below it. Read `MayLand` and `engine/handoff.go` before touching any landing
  path, and do not add a gate there.
- **A run answers to three rules, and they do not bend** (decision 28,
  DESIGN §25.12):
  - **a person allows the run and every revision of its plan** — the
    proposal and a planner's revised plan are the person's decisions —
    the proposal answered, or the session's runs always allowed by a
    person; no agent allows its own run;
  - **money is a ceiling no agent raises** — one pool, the session's
    envelope, and nothing estimated against it; a run is never stopped
    over money but paused for a person's top-up when the pool is spent
    (DESIGN §24.7a);
  - **a question the run cannot settle reaches the person** — a step's
    `ask_user`, a `repeat` at its max, a `repeat` that cannot judge its
    `until`.

  Review, checks and design are what a recipe chooses, never a floor
  gummi imposes; a run's review and its end checks are evidence for the
  person's read of the diff, never a gate on it. gummi never commits for
  a run: it leaves its changes in the session's tree.
- **The retired kinds are read-only.** A feature, bug, research or goal
  card left in an old database is shown but never run, minted or copied
  again; gummi refuses with a sentence naming it as retired. Do not add
  a path that drives one.
- **An agent** is a session minted into a home of its own
  (`Feature.Home`, DESIGN §19.3d): in no repository, a main-checkout
  card whose checkout is `.gummi/homes/<card>` (`worktree/home.go`),
  budgeted like any card and never landed.
- **A person reads a card as its title and its type, not its id.** The
  id (`FF-178`) stays the stored key, the worktree's name and the machine
  contract (NDJSON, `--json`, the web API). Where a sentence names a
  card, say `f.Name()` (its title, quoted and cut short; `Shell.cardName`
  from an id alone, `cardName` on the page) and `f.TypeName()` (Session,
  Agent, Feature …). The number, `id.Ref()` (`#178`), is the handle for
  typing a card: it belongs on the card's own head, not on a board row or
  in a notice. Where one is typed, read it through
  `Store.ResolveFeature`, which takes `178`, `#178` or the id. Never put a `Ref` in a commit message or
  anything else bound for GitHub — `#178` is an issue there — or in a
  printed shell command, where `#` starts a comment.
- **The spec is the context carrier between agents**, not chat
  transcripts. A workflow run is judged against the spec revision it
  froze; a step is sent its filled prompt — that spec, the artifacts and
  the outputs of the steps it names — never another agent's transcript.
- **gummi's job ends at a branch a person can land — and a person may publish it.**
  It does not release, merge on GitHub, retarget a PR unasked or resolve
  a thread. Pushing a card's own branch and opening, updating or readying
  its PR are acts a *person* starts (TUI or web),
  through `gh` and the credentials already on the machine, or the GitHub
  token and SSH key a person stored in the web page's settings
  (`internal/credentials`: lent to gummi's own `gh` and push commands,
  never put in an agent's environment). It is optional, detected rather
  than switched on, and absent where `gh`/push access is not set up. No
  session, workflow run, schedule, or MCP tool may reach the
  publish code (decision 25, DESIGN §22).
  Stacks (§18) still replay branches locally and print the `git push
  --force-with-lease` they need; publishing a stacked card is refused
  until §18.5's deferral is done.
- **An adopted branch is held, never owned.** No new card adopts a
  branch any more, but a legacy card minted onto a branch gummi did not
  cut (DESIGN §10 D22) keeps its custody rules: gummi never deletes that
  branch (`clean` keeps it), never rebases or force-pushes it.
  `Feature.Adopted` is the question every such path asks, and
  `TestAnAdoptedBranchIsNeverDeleted` asserts the deletion half.
- **A stack is topology, never scheduling.** A stack position says "my
  branch forks from that card's branch" and must never gate a card from
  running; only landing is ordered, because git orders it. See DESIGN
  §18.1; `internal/stack` enforces it and `TestAStackNeverBlocksWork`
  asserts it.
- **Formatting:** `gofumpt` + `goimports` (enforced by golangci-lint v2).
- **Errors on cleanup paths** (`Close`, `os.Remove` in defer) are
  intentionally unchecked per the linter's `exclude-functions` — match
  that; don't add noise elsewhere.
- Commit style follows the existing log: `type(scope): summary`
  (e.g. `feat(engine,ui): ...`, `fix(ui): ...`).
- **Never commit or reference plans.** Do not add planning documents,
  scratch plans, or TODO/plan files to the repo, and do not reference a
  plan (yours or the user's) in code comments or commit messages. Commit
  messages describe the change itself, not the process that produced it.
- **Never mention the model or agent in commit messages.** No model
  names, no "generated by", no co-authored-by/attribution trailers, no
  reference to an AI or agent having made the change. Write commits as a
  human author would. (This is about *authorship metadata* — gummi's
  product domain legitimately talks about agents and roles; that
  vocabulary belongs in code and messages when it describes the feature.)

## Where to look first

- What a session does → `internal/engine/freeformsession.go`, then
  `engine.go`; its specs → `internal/engine/sessionspec.go`,
  `internal/state/sessionspecs.go` and `internal/spec/session.go`.
- What a workflow run does next, or why a recipe was refused →
  `internal/workflowpolicy` and `internal/workflowrecipe` for the rules,
  then `internal/engine/workflow.go` (`WorkflowTick`) and its siblings
  (`workflowstep.go`, `workflowtree.go`, `workflowproposal.go`, `pool.go`).
  How a run is drawn is `internal/threadfold/runs.go`; the faces are
  `internal/ui/runblock.go`, `runkeys.go` and `stepview.go` in the
  terminal, `internal/ui/webrun.go`, `internal/web/routes_runs.go` and
  `internal/web/assets/workflow.js` on the page, and
  `internal/ui/workflowloop.go` ticks it.
- "why did this card's branch move" / stacks → `internal/stack` for the
  rules, `internal/engine/stack.go` for the tick, `worktree.Manager.baseRev`
  for what a card forks from (and read its comment before touching any
  `"HEAD"` in that package — the token means two different things there).
- A TUI bug → `internal/ui` (`board.go`, `diffview.go`, `inbox.go`).
- A web face bug → `internal/web` for the server and page, `internal/ui/web*.go`
  and `internal/ui/bridge.go` for what the page is told and how its writes
  reach the board. The web face never decides anything itself: if a web
  answer differs from the TUI's, the fix belongs in the shared code
  (`threadfold`, `decisions`, the Shell), and `TestWebDecisionsMatchThePicker`
  is the test that should have caught it. Browser tests live in `e2e/web`
  (`scripts/web-e2e.sh`, Playwright).
- "what did this card cost / how did it run" → `internal/cardrun`, then its
  readers (`internal/ui/statsview.go`, `internal/ui/week.go`). The derivation lives in one place on purpose: two
  surfaces that disagree about what a card cost are worse than one.
- "how is the whole board running / the timeline" → `internal/fleetrun`,
  then its one reader (`internal/ui/wsstats.go`). Same seam, one scale up:
  the fold reuses `cardrun` per card and states its own attribution rules
  (a pass is charged to the window it started in; the window clock counts
  an open session to the right edge).
- "why won't gummi delete or rebase this branch" → a legacy adopted
  card: `domain.Feature.Adopted`, then `internal/worktree/manager.go`
  (`Attach`, `ErrAdoptedBranch`) and `branchmove.go`.
- "push this card / open its PR" → `internal/publish` for the rules and
  the acts, `internal/worktree/pushtarget.go` for where a branch goes,
  then the two faces: `internal/ui/publish.go`
  (TUI dialog and what the web page is told) and
  `internal/web/assets/publish.js`. A face never decides: a difference
  between two of them is a bug in the shared code.
- "hand this card's failing PR checks to its session" → `internal/pr/checks.go`
  for the read and the message, `internal/ui/prchecks.go` for who gets it
  (DESIGN §22.11). A snapshot a person sends: never stored, never a gate.
- Agent/model wiring → `internal/agent` + `internal/engine/profiles.go`.
- "why may (or may not) this card land" → `domain.Feature.MayLand`, then
  `internal/engine/handoff.go` and `internal/ui` for the checks a landing
  crosses (an open workflow run, a stack below it, unresolved diff
  comments), and
  `docs/DESIGN.md` §19.1 and §24.8 for why the floor is a person's read
  of the diff.
- Anything architectural or a "why is it this way" question →
  `docs/DESIGN.md` (its **Decisions** list in §10 is binding; §24 and
  decision 27 supersede the old stage-workflow sections, and §25 and
  decision 28 supersede §24's crews; the superseded sections are kept as
  history).
