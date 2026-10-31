# gummi — Design Document

> A meta-harness for coding agents. Drive a fleet of agents through a
> spec-driven workflow across git worktrees — from one beautiful TUI, or
> headlessly from your own agents and CI.

**Status:** living design record — first written 2026-07-03, amended since.
Sections marked *superseded in part* keep the record of what a change
replaced; §3 is always the current workflow.

---

## 1. The problem

Working on multiple independent features in one codebase with coding agents
today means juggling terminals, tmux panes, worktrees, and your own memory of
"which agent is doing what and what does it need from me." Quality suffers
because there's no enforced process — agents jump straight to code without a
spec, reviews are ad-hoc, and every step burns premium-model tokens whether it
needs frontier intelligence or not.

gummi solves three problems at once:

1. **Orchestration** — many features in flight, each isolated in a worktree,
   each at a known stage of a structured workflow.
2. **Quality** — a spec-driven state machine (inspired by
   [schipper.ai's parallel coding agents workflow](https://schipper.ai/posts/parallel-coding-agents/))
   with gates: no implementation without an approved spec, no merge without
   review + verification.
3. **Cost** — per-stage model routing via profiles. Frontier models for design
   and review, cheap/local models for mechanical steps.

The parallelism model is **attention-based, not throughput-based**: you are
the scarce resource. Autonomous runs start the moment you ask for them, and
nothing caps or queues them: how many run in parallel is your decision, and
gummi does not second-guess it. A card's gate-approval mode decides how its
gates are crossed and who answers its questions, never when it runs.
gummi's job is to make the "waiting on you" queue visible and make
context-switching between features cheap.

## 2. Core concepts (domain model)

| Concept | Description |
|---|---|
| **Feature** | One unit of work. Has an ID (`FD-042`), a spec file, a worktree + branch, a workflow state, and a profile. The kanban card. |
| **Spec (FD)** | Markdown feature design doc, lives *in the repo* (`.gummi/specs/FD-042-dark-mode.md`). Problem, out of scope, considered solutions, chosen approach, implementation notes, verification plan. The durable artifact agents read and write. |
| **Workflow** | The single, fixed state machine every card moves through: `todo → plan → implement → verify → done`. Never configurable, and one graph for every kind — the only movement that is not forward is the rerun edges (a wrong plan, a failed verify). |
| **Stage** | One node in the workflow. Declares: agent action, completion gate, the critique pass it ends with, and which *role* performs it. Its contract varies by the card's kind; the graph does not. |
| **Role** | A named agent capability slot: `architect`, `implementer`, `reviewer`, `scribe`. Workflows reference roles, never concrete models. |
| **Profile** | Maps roles → concrete agent configs (adapter, model, provider/BYOK env, permission level). Selected per feature. `premium`, `thrifty`, `local-heavy`, ... |
| **Session** | One live agent conversation bound to a feature + stage. Can be attached (focused in TUI), running in background, or paused. |
| **Worktree** | Git worktree per card: `.gummi/worktrees/FD-042/`, branch `feat/dark-mode` (§18.6). Created on the card's first stage run and kept for its whole life; removed after merge. A research card never gets one. |

### Why roles indirect between workflow and profile

The workflow says *"the review stage is performed by `reviewer`, autonomously"*.
The profile says *"`reviewer` = copilot with Claude Opus"* or *"`reviewer` =
copilot BYOK → local llama.cpp with Qwen-Coder"*. Same process, different
spend. This is the single most important design decision for the cost goal:
**the process is fixed; spend is chosen per feature.**

## 3. The workflow

> **Superseded by §24 (decision 27).** The workflow is retired. No card
> is minted into it any more and nothing runs one: the only card anyone
> makes is a session (§19, §24), which holds `StageOpen` until it is
> closed. The graph, its stage contracts, its critique passes, its verify
> gate, autopilot and the rerun edges are gone from the code
> (`internal/workflow` is deleted). A crew's plan checks, per-node review
> and rework, and its done-when checks (§24.5–§24.8) are what remains of
> the floor, and they run only where a session calls a crew. A workflow
> card left in an old database is read-only: shown, never run. What
> follows is kept as the record of what the crew replaced.

There is exactly one workflow, compiled into gummi — never configurable,
and now literally one: the three graphs this document originally
described (feature, bug, research) were the same four ideas wearing three
sets of names, and they have been merged. There are no routes, no skip
flags and no quick preset. The only movement that is not forward is the
**rerun edges** (implement → plan when the plan was wrong, verify →
implement when the checks fail).

```
            ┌──────────┐    ┌──────────┐    ┌──────────┐
  todo ───▶ │   Plan   │──▶ │Implement │──▶ │  Verify  │──▶ gate: checks green
            │(the design    │(autonomous,   │(autonomous:
            │ stage)   │    │ pausable)│    │ build/test/lint
            └──────────┘    └──────────┘    │ + live check)
                 ▲   │            ▲   │     └──────────┘
                 └───┘            └───┘        (rerun edges)
        ──▶ gate: you accept ──▶ Done: landed on main as one squash
                                 commit (you approve the message;
                                 gummi offers worktree cleanup)
```

The **kind** (FD/BG/RS) no longer selects a graph. It selects each
stage's *contract*: which hints its agent gets, which artifact template
it writes, and which sections its gate demands. A bug's design stage
reproduces and diagnoses; a research card's design stage shapes the
question; a feature's explores, converges and plans. Same stage, same
gate, same critique.

Every stage ends with a **critique pass** before its gate: a
fresh-context reviewer that tries to refute what the stage produced.
Review used to be a stage of its own; it is a pass now, because a step
that iterates its own work in place needs no edge in the graph.

Stage semantics:

- **Plan** *(interactive, role: architect)* — the design stage, and the
  one that absorbed brainstorm, spec and plan (triage and diagnose for a
  bug; investigate and shape for research). You talk to the agent in the
  card's own thread (§6), and it works three phases in one prompt:
  explore the problem and the candidate approaches, converge with you on
  exactly one, then derive the numbered, tracer-bullet-ordered
  implementation plan — one line per step so critique markers can anchor.
  Unresolved questions are flagged with `%%` markers (schipper
  convention) that gummi surfaces as a checklist. Before the gate is
  raised, a **plan critique** runs: a fresh-context reviewer session
  (same cross-model property as the work stage's critique) that tries to
  refute the plan — security, correctness, completeness — before any
  implementation tokens are spent. Findings land as `%% @reviewer:`
  threads anchored to the plan lines they indict, and missing checks are
  appended to the spec's Verification plan so Verify proves them later.
  The critique ends with the verdict grammar: *pass* raises your approval
  gate; *changes* bounces to an automatic replan round (capped at 2, then
  it escalates to you with the findings in the checklist). The loop is
  invisible to the state machine — the card never leaves Plan.
  Gate: your approval. Crossing it promotes the spec to its workspace
  home (`.gummi/specs/`), discovers the repo's checks, and starts the
  implementer. A stage that wrote nothing into the sections its gate
  names does not cross (§4).
- **Implement** *(autonomous, role: implementer)* — agent works in the
  worktree with the spec + plan as context. Streams progress into the feature
  card. Pauses when it needs input (or on permission requests in `guarded`
  mode) → feature jumps into your "needs attention" queue. gummi owns the
  branch's commits: the agent commits as it goes, and gummi
  checkpoint-commits whatever a stage leaves uncommitted (turn end, budget
  exhaustion), so work on the branch is never stranded in the working tree.
  Checkpoint granularity never reaches main — the branch lands as one
  squash commit. On the PR route the same one-commit shape is produced
  by `gummi squash` on the branch, because the branch's own commits
  interleaved with checkpoint commits are fit for squashing and nothing
  else.
- **The work stage's critique** *(autonomous, role: reviewer)* — not a
  stage: the pass Implement ends with, exactly as Plan ends with one.
  **Fresh session, no shared context with the implementer**, ideally a
  *different model* (cross-model review catches more). Findings written
  into the spec's review section; serious findings re-run Implement in
  place rather than moving the card, which is what makes it a pass. A
  fresh critique triggers **automatically** after fixes, capped (default
  2–3 rounds, bounded by the protected budget floor); past the cap it
  escalates to you instead of looping. The kickoff hands the session what
  it would
  otherwise spend its own turns assembling: the branch diff with its base
  SHA named (or, past a size cap, the file-by-file stat and the command
  for the rest — the preamble is re-read on every turn, so a large diff
  costs more inline than it saves), and the results of the repo's
  gummi-checks, run gummi-side first. Those results are **review's own
  snapshot of a branch that is not final**: nothing is recorded from
  them, and Verify still takes its own run. They answer different
  questions at different times.
  The plan also ships a **file manifest** — a `gummi-files` fenced block
  naming the files the work will change, one line each on what for. The
  implement kickoff carries it verbatim, so the stage opens the right
  files instead of rediscovering them: gummi's implementer was measured
  making its first edit at turn 32 of 97, *later* than a bare agent given
  no spec at all (turn 20-31 of 62-72) — it explored more than an agent
  working blind, because the plan's file-level knowledge was prose it
  re-derived from the repo. The manifest is stated as a **starting point,
  never a closed set**: a stale list that reads as exhaustive is worse
  than none, so the implementer is told to change whatever else the work
  needs (noting the addition in Progress, so the next round inherits the
  correction) and to leave a listed file alone rather than invent work
  for it. Written by the design stage, which drafts the Implementation
  notes for every kind.
- **Verify** *(autonomous, role: implementer or scribe)* — two parts:
  the repo's check commands (build/test/lint) always run, and the spec's
  verification plan adds feature-specific live checks the agent
  executes. The commands live in the spec itself — a `gummi-checks`
  block in the Verification plan, auto-discovered by a one-shot scribe
  pass when approval creates the worktree, then human-gated and edited
  like any other spec content (the implementer updates it when a change
  alters how the repo builds/tests). Discovery **merges rather than
  defers**: a block it did not write — an architect filling one in from
  the packages it happened to be editing — is widened with the repo's own
  commands rather than taken as the final word, because a test command
  scoped to the change's own directory cannot fail on what the change
  broke elsewhere. What is already in the block is never rewritten or
  dropped, and a block discovery itself wrote (it stamps one) is left
  alone, so a command someone removed on purpose stays removed. Because it is a strict-YAML island
  inside a section three roles rewrite as prose, gummi **parses it
  forgivingly and renders it canonically**: `%%` markers that land inside
  the fence are dropped, `[env: …]`/`[CI-only]` tags glued to an entry are
  stripped (they are a plan defect the reviewer still flags, but they
  never become part of the command), tab indentation and a flush-left
  `cmd:` are re-indented, and a plain value holding a colon-space is
  quoted. A block that still will not parse is reported wherever it is
  read — never swallowed into "this card has no checks". Nor is **no
  block at all**: discovery is best-effort (a scribe the backend refuses
  finds nothing), but a card it leaves without one is said so — a note
  on the card's thread and a notice, discovery retried when verify
  starts, a verify that still has nothing to run drawn as "no checks"
  rather than "all passed", and a pass on such a card worded as the
  reviewer's own. A failing scribe is said once per card, naming its
  model, because the same failure also skips the landing draft. The approval baseline records the **commit it was
  measured on**, and a check it excuses as already failing is excused
  on that commit only: when the card's base moves (a rebase, a
  restack), the excused checks are measured again on the new base
  before verify writes anything off. Results recorded in the spec.
  Deterministic floor, adaptive ceiling.
- **Done** — you decide the feature is done. A verified card has **three
  endings**, and the answer set at the verify gate offers all three
  rather than assuming the first:
  - **Land on main** (`g`, or `m`, at verify — never before it; `gummi merge`) —
    advancing out of Verify squash-merges the branch into main as a
    single commit whose message gummi drafts from the spec and the
    branch; you review, edit, and approve it before anything lands.
    **The draft is composed at the verify gate, not at the keypress.**
    The pass is the same zero-tool scribe turn it always was, but the
    moment it costs nobody anything is the one where the branch has just
    become final and the card has parked: the reader is elsewhere, and a
    measured ~60s of it used to be spent with them watching an empty box
    — once per card, serially, in the close-out ritual. Drafting there
    also hands the scribe the one input the merge dialog could never
    have: what verify just reported. The stored draft is stamped with
    the branch tip it describes, so a branch that moved since (a rebase,
    a post-verify fix, the merge flow's own final checkpoint) is drafted
    live exactly as before — staleness is a question about the tree, and
    the tree answers it. Redraft (`ctrl+r`) always composes anew. What
    does not change: it is a draft, the human still reviews and approves
    it, and untouched text still takes a second `ctrl+s` to land.
  - **Land through a PR** — a PR merge is a first-class landing route
    alongside gummi's own squash merge, and works under any of GitHub's
    three merge methods — squash merge, merge commit, or rebase merge.
    The trigger is your `git pull` on main: no new verb, no `pr merge`
    shim — the existing verify→done gate carries the flow, and the
    fork-point invariant continues to hold because a fast-forward pull
    keeps the recorded fork point an ancestor of main. A branch that
    lands this way (or any other manual merge) skips straight to Done.
    `link PR…` rises out of the action inventory's fold at a clean
    verify, which is the one moment linking is the move.
  - **Hand off** (`h`, `/close`; `gummi handoff`) — the card closes and
    the branch stays exactly where it is: yours to push, PR by hand,
    cherry-pick, or keep. gummi commits a final checkpoint so nothing
    loose is left on the branch (never on a freeform card, whose loose
    work stays in its worktree, §19.3), stamps `handed_off_at`, and crosses the
    same gate with the landing waived and **every other floor intact** —
    open `%%` threads, open diff annotations, the omission gate and the
    document floor all still hold it. Landing it after all stays
    available for as long as the branch exists, and retracts the stamp.
    A hand-off keeps the branch only where it is the only copy of its
    work: one a remote already holds goes with the card, unless the person
    keeps it at the confirm.

  Because of the last two, **Done means the card is closed, not that
  anything merged.** The board badges which ending a card took (`landed`
  / `handed off` / `dropped`), and `status --json` names it in one
  `ending` field beside `branch_state` — "how did it close" and "is the
  work on the trunk" being two different questions. **A closed card takes
  its worktree and branch with it.** A branch is a piece of work at a point
  in time and a closed card has no further use for one, so keeping it is a
  choice made at the closing (the landing dialog's field, the hand-off
  confirm's option, `--keep-branch`), not the default. One rule decides
  whether a closed card's branch may go, `worktree.Manager.Spent`: it
  landed, a landing was recorded for it (which survives the base being
  reset or rebased past it), a remote has it, or it has no commits of its
  own. A branch that is the only copy of its work stays. What a closing
  leaves standing — uncommitted work, a stacked card, a goal's own cards —
  cleans up per card with `c` or `gummi clean`, or for the whole board in
  the close-out sweep and `gummi clean --all`. The card's artifact stays where it is at
  its workspace home: "spec archival" was described here for a long time
  and never built, and the artifact of a finished card is the thing a
  follow-up reads, so there is nothing archival to do to it.

  One consequence is named at the hand-off confirm rather than blocked:
  a dependency is met at `Stage == done`, so handing a card off frees its
  dependents to start from a base branch that does not carry its work.
  The confirm names them; the choice is the user's.

- **After done.** A finished card is not a silent one. Its page carries a
  **closing block**: the ending and its date, the commit it became, what
  became of its branch and worktree, what it cost and how much of that
  was rework — every one of those facts already stored, and none of them
  previously on any screen in the TUI. Beside it are the answers that
  remain: clean up, land a handed-off card after all, adopt a dropped
  one, and **open a bug from this**, which mints a fresh BG card carrying
  the parent's artifact, branch and thread (`FoundBy`) instead of making
  someone retype them. Done stays terminal — the follow-up is new work
  with its own spec, never a rewind.

- **A drop is a proposal, not a verdict.** A card its goal gave up on
  closes as `dropped` in its own right (it used to borrow the hand-off
  stamp to clear the landing floor, and reported itself handed off ever
  after). The reason the goal recorded is written to the card's own
  thread as well as the goal's log, and `adopt` takes the card back onto
  the open board at the stage the drop closed it from — read from the
  closing transition, so the rewind target is a recorded fact. There is
  deliberately no "acknowledge" verb: a recent drop sits in its own board
  group for a day and then folds with everything else, because agreeing
  with a drop is what doing nothing means.

- **Settled, and the archive.** A card is **settled** when it has nothing
  left to ask — it reached done, by any route. Settled cards older than a
  day fold into one board line (`f` opens it), out of the jump numbers
  and out of `alt+j`/`alt+k`. This is a DISPLAY grouping read from stored
  facts, never a state: nothing here touches the graph. The one question
  a finished card can still raise — a worktree still on disk — moves to
  that header rather than keeping a row per card in the live list, since
  there are always several (you land seven cards and clean none until
  Friday) and a live list holding all of them never empties.

- **The close-out pass** (`C`) is the end of a session as one ritual: it
  walks the cards whose branches are ready, **one confirm each** — the
  drafted commit message is still the review, and `skip` is bound as
  cheaply as `land` — then ends on the cleanup **sweep**, which collects
  every refusal cleanup already made (not landed, uncommitted rework, a
  branch kept on purpose) into a plan that can be read instead of one
  error at a time on a card someone had to go find. The masthead names
  what a pass would find, so hygiene is noticed rather than remembered.
  `W` reports what the last seven days produced, grouped by ending —
  the goal hand-over's shape at the scale of a week.

Every stage transition is recorded (who/what/when) in the feature's history —
the audit trail is part of the quality story.

## 4. Architecture

```
┌─────────────────────────────────────────────────────────┐
│  TUI (Bubble Tea)                                        │
│  kanban │ card thread │ activity/queue                   │
└───────────────▲─────────────────────────────────────────┘
                │ (Elm msgs; engine events via channel)
┌───────────────┴─────────────────────────────────────────┐
│  Orchestrator (engine)                                   │
│  workflow state machine · scheduler (no caps)         ·  │
│  event bus · persistence                                 │
└──┬──────────────┬───────────────┬───────────────────────┘
   │              │               │
┌──▼───────┐  ┌───▼────────┐  ┌───▼───────────┐
│ Worktree │  │ Spec store │  │ Agent runtime │
│ manager  │  │ (.gummi/   │  │  (adapters)   │
│ (git CLI,│  │  specs/)   │  └──┬────────┬───┘
│  argv)   │  └────────────┘     │        │
└──────────┘            ┌────────▼──┐  ┌──▼─────────┐
                        │ copilot   │  │ opencode / │
                        │ (Copilot  │  │ generic    │
                        │ SDK, JSON-│  │ adapter    │
                        │ RPC srv)  │  │ (later)    │
                        └───────────┘  └────────────┘
```

### 4.1 Agent abstraction layer

The pluggability requirement (copilot-cli today, opencode etc. later) lives
behind one interface:

```go
type Agent interface {
    // Start a session in a working directory with a role config.
    NewSession(ctx context.Context, opts SessionOpts) (Session, error)
    Capabilities() Capabilities // models? BYOK? server mode? resume?
}

type Session interface {
    Send(ctx context.Context, msg string) error   // user/orchestrator turn
    Events() <-chan Event                          // stream: text deltas, tool calls,
                                                   // permission requests, done, error
    Interrupt() error
    Close() error
}

type SessionOpts struct {
    WorkDir     string            // the feature's worktree
    SystemHints []string          // stage instructions, spec path, dev-guide
    Model       string            // e.g. "claude-opus", "gpt-5-codex"
    Env         map[string]string // BYOK: COPILOT_PROVIDER_* per process
    Permissions PermissionPolicy  // auto-approve reads? writes? shell?
}
```

**Copilot adapter (first-class, v1):** built on the official
[Copilot SDK for Go](https://github.com/github/copilot-sdk/blob/main/go/README.md),
which manages a `copilot` CLI process in server mode and speaks JSON-RPC to
it. This gives us sessions, streaming events, tool-call visibility, and
permission callbacks *natively* — no PTY scraping, no parsing TUI output.
Each session gets its own env, so BYOK routing
(`COPILOT_PROVIDER_TYPE/BASE_URL/API_KEY`, `COPILOT_MODEL` —
[docs](https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/use-byok-models))
is per-role, exactly what profiles need.

**Interactive mode is gummi-native, not embedded copilot TUI.** Because
the SDK exposes full duplex sessions, the design stage renders
inside the card's own thread (glamour for markdown, streaming responses,
tool-call collapsibles, §6) rather than a pane of their own — one
integration surface for both interactive and autonomous stages, and the
UI stays coherent and beautiful.

*Escape hatch:* a "raw attach" action that suspends the TUI and hands the
terminal to a real `copilot` session in the worktree (`tea.ExecProcess`),
for when you want the native experience. Cheap to build, zero risk.

**opencode adapter:** one `opencode serve` process per gummi session,
started with the session's own config (the worktree permission cage, the
session's MCP endpoint), killed with it. Every action is
an HTTP call against that server — a turn is one message POST that blocks
until the turn resolves while the server's event bus streams the same
mapped activity a CLI process used to; an interrupt is a server-side
abort (partial work aborted, not lost); the model catalog, compaction and
guarded approvals all ride the same client. Also shipped: a **generic
headless adapter** (spawn `<cmd> -p "<prompt>"`, capture output) as the
lowest common denominator for one-shot autonomous stages with any CLI agent.

Shipped alongside the above: **claude** (Claude Code CLI, streaming
stream-json) and **opencode**. These three are the supported agents
(decision 27); headless stays only as the line protocol gummi's own demo
and browser tests drive.

### 4.1a What a session discovers: skills, plugins, extensions

**gummi does not curate what a backend discovers, and adds nothing to
it.** Every backend CLI carries its own discovery — skill directories,
plugins, extensions, user config — and a card's session runs with all of
it, at user scope and at repo scope, exactly as the operator's own run of
that CLI would. A card on claude meets the repository's `.claude/skills`
and the operator's `~/.claude` skills and plugins; a card on opencode
its own and `~/.config/opencode`'s. A skill a CLI does not discover is a skill
that CLI does not have — gummi does not paper over the difference with a
convention of its own. This is what other multi-agent harnesses do
(paseo runs claude with every setting source; openchamber mirrors opencode's discovery rather than
narrowing it), and it is the only posture under which "the same agent the
operator already tuned" is true.

There used to be a third source, `skills.forward`: workspace-root skills
gummi pointed each backend at, since a worktree is a sibling of the
repository and nothing beside `.gummi` is in any backend's project scope.
It is gone — four backends of seven could take it, under four different
mechanisms — and a config that still names `skills:` is refused at load,
by name, rather than loading and quietly losing them.

*Amended for the skill pack (§23.2).* gummi now adds one thing: the
workspace's own skill pack, `.gummi/skills`, which its curator writes.
It reaches every session on claude, copilot and opencode, scoped per
repository, at the revision the session started with. It is added
beside what the backend discovers, never over it: no skill, plugin or
MCP server the operator installed is switched off, and no pack skill
takes an operator skill's name (§23.2). The rest of this section
stands: the operator's discovery is untouched, and `skills:` is still
refused, because the pack is not user-configured forwarding.

**Where gummi still touches discovery, and why:**

- claude's tool roster is never narrowed for a session that writes: it is
  shown the CLI's whole built-in surface (web, subagents, skills), and its
  allowlist pre-approves the web and subagent tools so they work headless.
  Only a read-only research session gets `--tools`, and that roster always
  names `Skill`, which `--tools` gates and every discovered skill is invoked
  through.
- claude's card sessions add gummi's MCP server to the operator's own
  (`~/.claude.json`, a project `.mcp.json`, an installed plugin's) rather
  than replacing them, decided 2026-10-16: a plugin that ships a server
  is a plugin that works. The CLI marks a server that fails to start;
  it does not fail the session. Only a read-only research session takes
  `--strict-mcp-config`, because its roster is its cage and an
  operator's server is a tool the roster does not name.
- `gummi`'s own skill is not filtered. A user-scope `gummi skill install`
  is discovered by the CLIs that read that scope; the stage hints, not
  discovery, are what tell a card never to start a second gummi (§16).

### 4.2 Orchestrator

- **State machine** per card — the workflow is compiled in, not
  configured; the only non-forward movement is the rerun edges.
  Transitions fire actions (start session, run checks, request human gate)
  and emit events.
- **Autonomous runs start when asked, uncapped** (§1): every run (plan,
  implement, review, verify, rebase) starts as soon as it is requested. The
  engine keeps no attention pool, cap, queue or slot. A session waiting on a
  person's `ask_user` answer stays running, and a card's gate mode never
  delays another card's run. Parallel token burn is the operator's call:
  cards drive in disjoint worktrees under per-card locks (§8.2 Decision 12),
  so nothing in the engine needs the serialization.
  Interactive stages only run when you attach.
- **Needs-attention queue**: gates, agent questions, budget exhaustion, and
  failures — plus permission requests when running in `guarded` mode — land
  in one inbox, newest-first, with desktop-bell/notification hooks. A
  `hooks:` list in config.yaml runs user scripts on the same events —
  the event vocabulary is closed, the contract advisory (a hook can
  never block or fail a run), and the reporting rides the store's own
  committed rows, so the TUI and the headless driver fire hooks from one
  seam rather than each growing call sites (internal/hooks).
- **Persistence**: feature state + session transcripts in
  `.gummi/state/` (SQLite via `modernc.org/sqlite`, no cgo). Specs live in
  git; state is machinery. gummi must be fully restartable: on launch it
  re-reads state, re-attaches or restarts sessions (Copilot SDK supports
  session resume).

### 4.3 Worktree manager

- `gummi` runs from the main checkout; each feature gets
  `git worktree add .gummi/worktrees/FD-042 -b feat/<slug> <base>`.
  Worktrees are nested by design — `gummi init` writes the ignore rules
  (`.gummi/worktrees/`, `.gummi/scratch/`, `.gummi/state/`) so the repo
  stays clean.
- **No agent stage runs in the main checkout.** A stage that has no
  branch worktree yet — every interactive design stage, and every
  research stage — gets a *scratch tree* instead:
  `git worktree add --detach .gummi/scratch/FD-042 HEAD`, one per card,
  reused across its pre-worktree stages. It is deliberately branchless,
  so nothing done in it can become the card's work, and it is discarded
  when the real worktree is cut. Every backend already cages its file
  tools to the session's working directory, so this is what makes "the
  design chat does not write to your repo" a boundary rather than a
  sentence in a prompt.
- Handles: creation at spec-approval (drafts live in `.gummi/state/drafts/`
  until then), rebase-on-main helper, dirty-state detection, landed-branch
  detection with worktree cleanup, and the on-disk size behind the
  close-out sweep's figures (`DiskSize` — a checkout walk, so it is called
  from surfaces someone opened on purpose and never on a render path).
- Merge-conflict triage is itself a good `scribe`-role autonomous task later.

### 4.4 Permissions & sandboxing

**Default: allow everything.** gummi assumes it runs inside a sandbox
(container, devcontainer, VM) — that boundary is the safety mechanism, not
per-tool-call approval prompts. Copilot sessions launch with the equivalent
of `--allow-all-tools` (SDK: an auto-approving permission handler), so
autonomous stages never stall on "may I run this command?" and the
needs-attention queue carries only things worth your attention: gates,
agent questions, budget exhaustion, failures.

Escape hatches, because the assumption won't always hold:

- Global config `permissions: allow-all | guarded` (`.gummi/config.yaml`);
  `guarded` restores interactive approval via the queue, for running gummi
  on a bare host.
- Per-role deny-list overrides in profiles (Copilot supports granular
  `--deny-tool` rules) — e.g. lock the `reviewer` to read-only so a review
  can never "helpfully" edit code, deny `git push`/network for local-model
  roles.
- Even in allow-all, every tool call still streams into the activity feed
  and session transcript — full audit trail, and git worktrees make any
  agent change revertible.

Instead of probing for a container or warning once, gummi ships the
sandbox assumption as layered, always-on guards. The `permissions:
allow-all | guarded` mode (above) is the first layer; per-profile
`sandbox: enforce | warn | off` is the second. Running gummi on a bare
host with allow-all is possible and surfaced, not silently degraded — the
escape hatches below stay the honest path.

**What each layer actually guarantees**, because the names promise more
than they keep and an operator routing a role deserves the real shape:

- `sandbox: enforce` refuses to start a run whose profile names a backend
  that cannot reach gummi's tools. `warn` (the default) and `off` both let
  such a run start; they are two names for the same permissive decision,
  kept apart only because they used to differ on the tripwire. **None of
  the three confines a write.** The mode is about tool coverage, not
  containment.
- **File-tool confinement is the backend's**, and it comes in two tiers.
  claude and opencode pin their file-writing tools to the session's
  working directory, so a write naming somewhere else is refused by the
  backend. copilot and headless are merely *started* there — their
  tools may name any path, and nothing notices. `gummi doctor`
  reports the tier per role per profile as `write-cage:<profile>`, so the
  choice is visible before a role is routed rather than after a run goes
  wrong.
- **No backend confines shell commands, on either tier.** A bash policy is
  command-string based rather than path based, so a real shell cage needs
  process-level confinement, which gummi does not do (scoped out of
  FD-014, and still out). This is the gap that let a weak model on the
  reviewer role write a feature's files into the operator's main checkout.
- **Nothing backstops what gets past those two.** A main-checkout
  tripwire used to: it snapshotted main's dirty set around every turn and
  killed the run on a clean→dirty transition. It was removed because that
  snapshot cannot tell the agent's writes from the operator's own — a
  human editing their own checkout while a card ran was enough to park
  the card — and because it only ever detected, never prevented: the
  write had already happened by the time it fired. A role routed at the
  weaker write-cage tier is trusted, not contained.

Since §4.3, no stage runs in the main checkout at all — the pre-worktree
stages have their own scratch tree — so worktree discipline no longer
rests on a prompt asking for it. What still rests on the model is
everything the shell can reach.

That includes the web face (§20). An unconfined agent running as the
operator can run `gummi web pair` — or read the admin token it uses — and
redeem the code it gets. Once any browser is paired, that no longer gets
it the board: the device only waits until a person on a paired browser
approves it (§20.3), and there is deliberately no command that approves
one, since an agent could run it as easily. What the agent can still do
is what any process with the operator's permissions can: drive the
operator's own browser profile or read its cookie store, race the
operator to be the very first device, edit `devices.json` or kill and
restart the server so a restart trusts what it wrote, or read wherever
the operator sends the server's output. Those are the operator's own
account, not a surface gummi can guard; the container boundary above is
what prevents them.

#### Config layering

Settings are merged from two files: a user-level config at
`$XDG_CONFIG_HOME/gummi/config.yaml` (falling back to
`~/.config/gummi/config.yaml`), and the workspace config at
`.gummi/config.yaml`. `config.LoadLayered` loads both and applies explicit
per-field rules:

- `permissions` and `sandbox`: workspace wins when set, otherwise user,
  otherwise the built-in default.
- `env`: keys are merged; the workspace entry wins on a name collision.
- `instructions`: a list of absolute paths to extra instruction files,
  concatenated user-first then workspace; the engine appends each file's
  content to the environment card. Every path must be absolute — a relative
  or empty entry is rejected at load time so a path cannot silently walk out
  of the workspace.
- `repo` and `repos`: workspace-only. Setting either in the user-level file
  is a load error.

`UserConfigPath` returning an error (no XDG dir and no home directory) is
treated as "no user config" everywhere: a warning is surfaced, but gummi
continues with workspace-only settings. A `LoadLayered` error is handled at
each call site exactly like the previous `config.Load` error: it aborts
environment probing in the engine and aborts startup in `resolveAllRoots`;
the engine-build path prints a warning and falls back to defaults; doctor
reports it as a failing `config:load` check and continues the rest of the
report. `gummi doctor` prints one `config:*` line per field, naming the file
that supplied the winning value (`user: ...`, `workspace: ...`, or
`default`), and one `config:instructions.<path>` line per instruction path
reporting whether the path exists.

### 4.5 Client tools & the ask protocol

Beyond reading and writing the spec, agents need a first-class way to
*ask the user a bounded question* — "per-device or synced?" — without
that decision getting lost in prose. gummi exposes gummi-owned **client
tools**: tool declarations passed on `SessionOpts.Tools`, whose handlers
run inside gummi, not the model's sandbox.

The one tool today is **`ask_user`** (`{question, options[], multi_select,
spec_anchor}`). There is no flag for whether the person may answer in
their own words — see §6.3's "an agent's question always offers to talk
it over". When the model calls it, the adapter
surfaces an `EventClientToolCall` and *blocks that call* until the
orchestrator answers — a blocked call spends no tokens, so waiting on a
human is free. gummi renders the question as the card's open decision
(§6.3) — an inline option picker pinned above the composer in the
card's thread, and, when the card isn't the one on screen, a
needs-attention item that opens straight to it. The chosen answer is
fed back as the tool's result, so the model's turn resumes in-context —
cheaper than a fresh chat round-trip.
If the ask carries a `spec_anchor` (a unique snippet of a spec line),
gummi writes the answer into the spec as a resolved `%%` marker, so
decisions become durable spec content with no model effort.

Two design rules keep this from fighting the "workflow compiled in,
model does the thinking" stance: the tool owns *mechanics* (surfacing,
capture, anchoring) while the model owns *content*; and `ask_user` is
offered **only on interactive stages**, where the picker exists to
answer it — an autonomous stage that needs a decision still stops and
raises a gate rather than blocking a slot on an unanswerable question.

**Adapter coverage.** The Copilot SDK provides client tools natively
(in-process `Tool.Handler`), so the handler blocks the turn directly.
The generic headless adapter carries them over its JSON protocol (an
`ask` frame out, a `resolve` frame back). Backends without a tool
channel (opencode) use the **prompt-convention fallback**: the stage
hint asks the model to emit a fenced ` ```gummi-ask``` ` JSON block,
which gummi parses into the same picker and answers as the next turn.
One `Ask` type, one picker, one answer path — the capability differences
live entirely in the adapters.

## 5. Profiles & cost strategy

`.gummi/profiles.yaml`:

```yaml
profiles:
  premium:            # ship-critical features
    architect:   { adapter: copilot, model: claude-opus-4.8 }
    implementer: { adapter: copilot, model: claude-sonnet-5 }
    reviewer:    { adapter: copilot, model: gpt-5-codex }   # cross-model review
    scribe:      { adapter: copilot, model: gpt-5-mini }

  thrifty:            # everyday features — minimize premium requests
    architect:   { adapter: copilot, model: claude-sonnet-5 }
    implementer: { adapter: copilot, model: gpt-5-mini }
    reviewer:    { adapter: copilot, model: claude-sonnet-5 }
    scribe:      &local
      adapter: copilot
      byok:
        type: openai            # llama.cpp server, OpenAI-compatible
        base_url: http://127.0.0.1:8080/v1
        model: qwen2.5-coder-32b
    ...

  local-heavy:        # experiments, private code — near-zero cloud spend
    architect:   { adapter: copilot, model: claude-sonnet-5 }  # design still needs a big brain
    implementer: *local
    reviewer:    *local
    scribe:      *local
```

Cost levers beyond profiles:

- **Premium-request awareness**: Copilot bills premium requests with
  per-model multipliers. gummi tracks requests per feature/stage (the SDK
  surfaces model + turn events) and shows a running cost column on the
  kanban board. Budget warnings per feature ("FD-042 has burned 38 premium
  requests").
- **Cheap-by-default mechanical work**: commit messages, spec formatting,
  transition summaries, changelog entries → always `scribe`.
- **Context discipline**: fresh sessions per stage (spec is the context
  carrier, not the transcript) keeps token windows small — this is the
  spec-driven approach paying for itself.

### 5.1 Budgets & spend plans

Copilot CLI supports hard session cost limits
(`--max-ai-credits=N`, 1 credit = $0.01, soft-stop —
[docs](https://docs.github.com/en/copilot/how-tos/copilot-cli/use-copilot-cli/set-session-limit)).
gummi builds a three-layer budget system on top:

**Layer 1 — enforcement (backstop).** Every session gets a hard cap derived
from its stage budget, passed as `--max-ai-credits` (SDK session config).
Because the stop is soft (the in-flight response completes), gummi sets the
enforced cap ~10% below the stage budget to absorb overrun. When a session
hits its cap, the orchestrator catches the stop event, records a
`budget-exhausted` checkpoint, and moves the feature into the
needs-attention queue — never a silent death.

**Layer 2 — model awareness (advisory).** The CLI does not tell the model
its budget, so gummi does, twice — and in neither does it ask the model to
judge its own spend. A model cannot see what it has spent; asked to
"estimate whether the task fits", it guessed, and guessed badly (stopping
early with most of the budget left, or ploughing on into the cap). The
metering is gummi's, so the judgment is too:

- *At session start*, in the stage system hints:
  > This stage has a budget of $X. gummi meters what you spend and will
  > tell you here as you pass 50%, 80% and 95% of it. You cannot see your
  > own spend, so do not estimate it, ration your work against it, or stop
  > early on a guess about it. Work efficiently: prefer targeted reads over
  > broad exploration, batch related edits, and avoid speculative
  > refactors. When gummi tells you to checkpoint, write what's done,
  > what's left and where to resume into the spec's progress section,
  > then stop.
- *Mid-session*, the orchestrator meters actual spend from SDK usage events
  and injects budget updates at thresholds (50%, 80%, 95%), each naming
  the metered figure: `[budget] 80% consumed, ~$0.12 left — write what you
  have into the artifact NOW…`. The 95% message explicitly demands a
  checkpoint. This converts the hard stop from a cliff into a landing.

Goal cards are the exception: a goal's lead still reasons about budget
(its `reserve_set`, the envelope it asks for a card), because sizing work
against a ceiling is the lead's job (§17.3).

**Layer 3 — the budget envelope.** Each work item carries one credit
envelope, shown on the kanban card. Every stage — interactive or
autonomous — draws from the same pool; an autonomous stage's session cap
is simply what's left of the envelope (floored at one agent turn, since
enforcement runs between turns and a smaller cap cannot be held).

There are deliberately **no per-stage allocations**. An earlier design
split the envelope into stage shares with rollover, a protected
review/verify floor, and an orchestrator-held reserve; it was dropped.
The stage-level precision was fictional (turn-granular enforcement,
uncapped interactive stages, and provider price differences all blow
through fractional caps), and it produced the worst budget UX gummi had:
a stage gating "exhausted" while the card showed plenty of envelope
left. The quality guarantee the floor purported to give is already owned
by the workflow — review and verify can never be skipped, so a feature
whose envelope runs dry before review simply parks at the top-up gate;
it cannot land unreviewed.

Rules that make the envelope real rather than decorative:

- **One pool, one gate**: the item runs until the envelope is spent, then
  moves to the needs-attention queue. When a stage runs dry, the gate
  offers: *top up* (raise the envelope), *downshift* (re-route the
  role to a cheaper/BYOK model from the profile's fallback chain and
  resume from checkpoint), *split* (agent proposes cutting scope into a
  follow-up FD), or *park*.
- **Top-ups leave real headroom**: a raise is sized to the larger of
  spend × 1.25 (re-deriving the envelope from what the work actually
  costs) and spend + two agent turns, so a resumed stage never re-gates
  on the next turn.
- **Plan-time proposal**: the envelope is proposed from the historical
  median spend of completed cards, padded and floored (`MinEnvelope`) so
  a thin history doesn't gate instantly. There is no agent estimate in
  it: an earlier scribe pass guessed each card's cost and was blended
  in, and its guesses were the least reliable input the proposal had.

**Dollars, not credits.** Every figure a person reads or types — the board,
the web page, the CLI's `--envelope` and `GUMMI_ENVELOPE`, `status` — is in
US dollars, and so is every figure an agent is told. Storage, the
`--json` fields and the web contract stay in credits (1 credit = $0.01,
so a credit is a cent and a dollar figure finer than a cent is refused
rather than rounded); the conversion happens only at the edges, through
`domain.FormatDollars` and `domain.ParseDollars` (and their twins in the
web page's `dom.js`).

**BYOK/local spend.** Credits only meter GitHub-hosted usage; local llama.cpp
is credit-free but not cost-free (time, watts). The meter therefore records a
unified `spend` per session: `credits` for Copilot-hosted, `tokens` for BYOK
(from the provider's usage fields), each convertible to display-dollars via
per-provider rates in `profiles.yaml`. Enforcement for BYOK sessions is
gummi-side (orchestrator interrupts at the token cap) since
`--max-ai-credits` won't fire for free-credit sessions.

## 6. TUI design

**The visual bar is [Crush](https://github.com/charmbracelet/crush).** Not
"nice for a TUI" — Crush-grade. We adopt Crush's exact stack and its
architecture (studied from the repo, which documents it):
`charm.land/bubbletea/v2` (runtime), `charm.land/lipgloss/v2` (style),
`charm.land/bubbles/v2` (textarea, viewport, spinner),
`charm.land/glamour/v2` (markdown), **ultraviolet** (screen-buffer
compositor), **charmtone** (palette), **colorprofile** (graceful color
degradation), `x/ansi` (safe ANSI string ops). Section 6.2 details the
design system.

```
┌ gummi ▸ myrepo ───────────────────────────────── ⬤ 1 active · ⏸ 2 · ✉ 2 need you ┐
│                       │                                                          │
│  TODO                 │   FD-042 · Dark mode toggle              [thrifty]       │
│   ○ FD-051 rate limits│   ────────────────────────────────────────────────       │
│                       │   Stage: Implement (autonomous)        ⣾ running         │
│  IN PROGRESS          │   Branch: feat/dark-mode           +412 −38 · 9 files    │
│  ▸● FD-042 dark mode ⣾│                                                          │
│   ● FD-047 csv export⏸│   ┌ activity ────────────────────────────────────┐       │
│   ✉ FD-049 auth fix  ?│   │ ✓ edited internal/theme/palette.go           │       │
│                       │   │ ✓ ran go test ./... (pass)                   │       │
│  REVIEW / VERIFY      │   │ ⚠ budget 80% consumed — ~12 credits left     │       │
│   ◐ FD-044 search     │   └──────────────────────────────────────────────┘       │
│                       │                                                          │
│  DONE                 │   [enter] attach · [s]pec · [d]iff · [p]ause · [g]ate    │
│   ✔ FD-039 onboarding │                                                          │
└───────────────────────┴──────────────────────────────────────────────────────────┘
```

- **Left column**: kanban list grouped by workflow super-states (todo / in
  progress / review-verify / done). Each card: ID, title, stage glyph,
  activity spinner, profile tag, cost tick, "needs you" badge (`✉`).
- **Right pane** swaps by mode:
  - *Dashboard* — selected feature's stage, diffstat, live activity feed,
    pending gate/permission prompts answerable inline.
  - *Chat/attach* — full-screen-ish interactive session for the design
    stage; `esc` detaches, session keeps state.
  - *Spec view* — glamour-rendered FD with `%%` open questions extracted to
    a checklist; gate approval lives here. `tab` switches into **annotate
    mode** (see 6.1).
  - *Diff view* — worktree diff pager before gates, with the same
    annotation mechanics as the spec view.
- **Global**: `n` new card — one dialog for a feature, a bug or a research
  card (the kind is a row; `B`/`R`/`G` open it preset). It asks where
  (the repository, every configured name side by side with its GitHub
  origin read from git), what kind, and what (free text whose first line
  is the title); it reads back what will be created and, on one collapsed
  line, how it will run (envelope, profile, dependencies). A GitHub issue
  reference on the first line is an *offer*: `alt+g` imports it into the
  box, nothing fetches on its own, and enter only ever creates. `tab`
  cycle gummi's own tabs (§6 below), `1..9` jump to feature, `?` help.

**One board, tabbed.** The split layout the diagram above shows (a kanban
column beside the dashboard, with `→`/`←` moving the arrow keys between
them) is retired, and so is the *Chat/attach* mode it lists: an
interactive stage no longer has an attach/detach state of its own to
switch a pane into (§6.3, §10 decision 5) — opening the card is opening
its thread, and there is no pane to return to. The board is the
**backlog**: no column, the full width is the same super-state-grouped
list, and `enter` opens the selected card on a page of its own (`esc`
back in one press, `alt+j`/`alt+k` to the previous/next card without
leaving it). Card titles, badges and the card's own detail get
the whole terminal, and there is only ever one list on screen at a time
— so the arrow keys never have to be aimed and the focus band never has
to disambiguate which pane owns them. Every card verb (`g`, `v`, `m`,
`d`, …) answers at either level (the list or the page) because both
route through the one guarded `boardVerb`; only movement, `enter` and
`esc` differ, and each level's binding table says which (`keymap.go`).

**The session rail.** With a card page or a new session's draft open, a
terminal wide enough for both (the page keeps at least 80 columns, so
110 and up) draws the board's rows again in a 30-column rail on the
left — the web page's rail, in the terminal (`ui/rail.go`,
`layout.WithRail`). It is the backlog drawn small, not a second list:
the same order, groups and marks (`backlogEntries`), two lines a card —
the title, then what it is doing and what it has spent — and no cursor
or keys of its own. The board's selection is its band, so `alt+j`/`alt+k`
walk it, and the arrow keys stay aimed at the page. Beside the full-width
backlog it would be the same list twice, so it is drawn only beside a
page; below the width it steps aside and the page takes the terminal.

The board sits behind a one-row tab bar shared with the status bar:
`gummi │ board │ stats │ inbox │`. `tab` cycles all three;
`alt+1`/`alt+2`/`alt+3` jump straight to one — alt-prefixed
deliberately (the same reasoning as the thread's `alt+o` outputs
toggle: a plain `ctrl`/bare key a terminal multiplexer might already
claim). Both are answered at the top
of `handleKey`, above whatever surface holds the keyboard, so a tab is
always one keystroke away from inside a card's thread, its spec view or
its diff view. The stats tab is the board's ledger: the same run
derivation the card's run tab reads (`cardrun`), folded over every card
by a pure workspace fold (`fleetrun`) — window and all-time money, the
clock, and a per-card timeline of sessions, waits and marks. It is a
tab rather than a board surface for the reason the board is the
backlog: one list on screen at a time.
**`?` and `alt+/`.** `?` is the convenient help key, but it is ordinary
punctuation, so it must yield wherever the user types prose: the thread's
composer, the bug-import filter, and the board's own composer. Those are
exactly the surfaces whose key rules are least guessable, so leaving them
without a route to their own key table was the worst place to leave one. `alt+/`
is the help key that is always gummi's — alt-prefixed for the same reason
`alt+N` is, and tier-1 with them: it is answered above whatever surface
holds the keyboard, so the route to a surface's own key table is never
the thing a surface can swallow.

The board's own overlaying surfaces (spec, diff, ingest review, bug
import, dependency picker) are scoped to the board tab: each belongs to a
card, and a card belongs to the board. Leaving the tab hides them and
returning restores them — never discards, since a card's thread holds an
unsent composer draft the same way. The inbox tab promotes the
needs-attention queue out of its modal overlay onto a tab of its own.

**The needs-you queue is a query, not a second list.** It is read from
the open decision rows (§6.3) rather than kept as a queue of its own, so
a stop raised by a headless run in another process is in it, and a
restart no longer has to guess the queue back from whatever sessions
happen to be restorable. One row per card, oldest first, carrying the
question in the words it was recorded with. The rows navigate and do not
act — you decide in the thread, where the evidence is — with one
exception that earns itself: `u` tops up an exhausted envelope, and it
is the only way to un-strand a card parked for lack of one. The session
inference behind it survives as the fallback for cards whose stop
predates the record, and for a failed session, which is the one stop
nobody decided anything about.

**The card page is a thread.** Opening a card does not show a detail
pane describing it; it shows one conversation running the card's whole
length — identity and a stage strip, the pinned spec line, one folded
line per finished stage, the live stage, and the input. Every stage
underneath is still a fresh agent with a fresh context window, and that
is exactly why the thread names every reset: a single continuous surface
implies a single memory, and the implication would be false. The spec
stays the context carrier between stages; the thread is a log, never a
prompt.

Its history is the card's own event log rather than the session
transcript, which holds only the live stage and is rewritten wholesale on
every save. Guidance lives in one place, and that place is **the open
decision** (§6.3): when the card is waiting on a human, the thread asks,
inline, with the legal answers beside the question; when it is not, the
foot of the page is a bare composer and nothing else.

**What a card decided on its own is history, not status.** A period the
card ran itself is drawn where it happened — an opening rule where
autopilot took over, its own crossings and answers as ordinary lines
among the folded receipts they fall between, and a closing rule naming
how it ended: it parked, it finished, or you took it back. The tally of
what it decided sits under the closing rule, and the whole stretch
scrolls up as the conversation grows, because it is a record of a period
that is over.

This replaces a rollup pinned to the end of the body. That block was
rebuilt from the card's whole event log on every frame and appended after
the live stage, so every new line — the agent's output, and your own
turns after you took the card back — was inserted above it and it stayed
permanently the newest thing on the page. It was a status banner filed as
history, and it outlived by hours the away period it described. The rule
it broke is §6.3's own: a decision collapses into the body's history at
the point in time where it happened.

Only two autopilot facts stay pinned, and both are about *now*: the mode
on the masthead, and — while a decision is open that autopilot is
answering — whose that decision is. Both are read from live state every
frame, so neither can outlive its truth. Nothing about the past is
pinned, and nothing pinned has to be cleaned up afterwards.

This replaces the earlier rule, which was that guidance lived in a
persistent `next` card at the bottom of the page. That rule put a tray of
ranked suggestions on screen whether or not anything needed deciding, and
it answered "what could I do" at a moment when the honest answer was
usually "nothing — watch". The decision answers "what does this card need
from me", and it is absent exactly when the card needs nothing.

**`esc` leaves, in one press.** It used to blur the composer first, on
the reasoning that the card's single-letter accelerators deserved a
keyboard state of their own. They no longer have a surface: the action
list that state drove became an overlay, so blurring landed the user in a
level with nothing on screen but a swapped status bar — `esc` looked like
it *opened* a mode rather than backing out of one, and `j`/`k`/`enter`
there moved and fired an action cursor nothing was drawing. The one thing
the level uniquely reached was `J`/`K`, which is `alt+j`/`alt+k` from the
line now. `esc` cancels whatever is visibly pending — a confirm chip, an
armed free-form answer — and otherwise returns to the backlog with the
draft intact. The accelerators are reached by word, by the `↑` inventory
(which the placeholder advertises, and which carries the keyless actions
no letter ever reached), or by the `/` menu. The blurred level still
exists for a card another process drives, because that card withholds the
composer and there is nothing for `esc` to leave from.

Typing into it is first-class: a line whose first word is one of a closed
vocabulary is a command, and every other line is a message to the agent.
Nothing is fuzzy-matched, so the classification is deterministic — and
because `verify` and `changes` are also ordinary English first words, a
verb that spends money or changes state confirms in place rather than
firing. The mitigation belongs at the point of action, not in the parser.

**Who writes the spec.** An agent does. A line typed into the thread is a
turn to the agent and nothing else — gummi never copies the user's prose
into the artifact. The agent receiving that turn may judge the point worth
keeping and write it into the Implementation notes itself, in its own
words and under its own role, exactly as it does with everything else it
learns; a fresh agent after a bounce then reads it because it *is* the
spec. The one thing gummi writes on the user's behalf is an answer to a
question the agent anchored (`spec_anchor`, §4.5): there the agent has
already declared where the answer belongs, so writing it is executing the
agent's instruction rather than editorialising over it.

That gives corrections three levels, each saying something different
about how much the user meant it: **a turn** (heard now, may not outlive
the session); **a note the agent chose to keep** (it judged the point
durable and wrote it); **a `%%` annotation** (§6.1 — attributed, anchored
to a line, and it blocks the gate until resolved). Persistence at the
middle level is the agent's judgment rather than a mechanism, which is
the price of a document with one author; the annotation editor is the
instrument for anything that must not depend on that judgment.

### 6.1 Annotation editor (line-level review, like a PR)

Gates shouldn't force feedback through chat ("the third paragraph is wrong,
the one about caching…"). Specs and diffs get a **review-style annotator**:
move a line cursor, mark a line or range, attach a comment — exactly the
GitHub PR review interaction, in the TUI.

**Interaction.** Spec and diff are each a single view over the *source*
(raw markdown / unified diff) with line numbers and a cursor. There is no
read/annotate mode split: they used to have one, because the read mode
was a glamour render and glamour re-wraps text — so a cursor on a
rendered row could never say which source line it was on, and comments
are addressed by source line. The two modes were therefore two different
documents, and the key that toggled between them was one of five things
`tab` meant.

The source is styled *in place* instead (`internal/ui/mdsource.go`):
headings, fenced blocks, inline code and `**bold**` get their own color
without a character moving, so one view is both readable and addressable.
What that deliberately gives up is re-wrapped prose and laid-out tables —
both move text between lines, and here line numbers are load-bearing. The
live dependency status and the open-thread checklist that read mode
carried are now a fixed header above the body, so they are true of the
surface rather than of a mode.

| key | action |
|---|---|
| `j/k` / `↓↑` | move line cursor |
| `v` | start/extend range selection |
| `c` | comment on line/range (inline textarea popover) |
| `e` | open the spec in `$EDITOR` at the cursor line (`tea.ExecProcess`) — the heavy-edit escape hatch |
| `n` / `p` | jump next/previous annotation |
| `x` | toggle resolved |
| `A` / `R` | approve gate / request changes (submits all pending annotations) |

Annotated lines get a gutter marker (`▍`) and the comment renders as an
indented, tinted block under the line; a counter (`✎ 4 · 1 open`) shows on
the feature card and gate prompt.

**Anchoring & storage — two backends, one UI:**

- *Specs*: annotations are written **into the document** using the existing
  `%%` convention, attributed and stamped:

  ```markdown
  The toggle persists via localStorage.
  %% @user(2026-07-03): should this be per-device or synced to the account?
  %% @architect: resolved — per-device; account sync deferred to FD-051.
  ```

  This makes user annotations, agent questions, and their resolutions one
  system: durable, versioned in git with the spec, zero anchor drift (the
  comment travels with the text it annotates), and visible to any agent
  that reads the file — no side-channel to explain in prompts.

- *Diffs*: can't write into a diff, so annotations live in
  `.gummi/state` as `{file, line, content-hash of ±2 surrounding lines,
  comment}` — content anchoring keeps them attached across minor rebases;
  orphaned anchors degrade to file-level comments rather than vanishing.

**Feedback loop.** Submitting "request changes" at a gate compiles open
annotations into a structured turn for the responsible role — spec comments
go back to the `architect`, diff comments to the `implementer` (or spawn
the fix-up session after review). The agent must address each annotation
and mark it resolved (`%% @role: resolved — …` for specs, a resolve event
for diffs); gummi shows the open-count burn down and re-gates when it hits
zero. Unresolved annotations block the gate — that's the quality mechanism,
not a convention.

**Who a comment belongs to.** "The responsible role" is decided per
comment, not by where the card happens to be. A spec comment belongs to the
stage whose writer writes the section it sits in — Problem, the approaches
and Implementation notes are plan's; Progress (a bug's Fix, a survey's
Findings) is implement's — and a diff comment to implement. A comment in a
section no stage owns (the preamble, Review, the Verification plan verify
re-authors) is the current stage's. `engine.SectionOwner` holds the table.
Ownership decides three things, and all three read the same rule
(`internal/engine/commentroute.go`):

- **Where request changes goes.** To the earliest stage that owns an open
  comment. A design note left at verify sends the card back to plan over
  the graph's own rerun edges (verify → implement → plan, both recorded);
  it is never handed to the verify agent. A send-back always asks first,
  naming the stage, the sections that pulled it back and what runs again
  after it — the terminal's confirm and the web's `confirm` question say
  the same words.
- **What a writer is given.** Its own comments and nobody's. A comment an
  earlier stage owns waits for the send-back rather than being answered
  in the wrong stage.
- **What holds a gate.** Every open comment except a later stage's. A card
  sent back to plan is not held at the design gate by a Progress note or a
  diff comment the architect cannot answer; they hold implement's gate
  instead, and nothing reaches done with a comment open.

### 6.2 Visual design system (Crush-grade)

Beauty is a feature requirement, not polish. These are the concrete
techniques that make Crush look the way it does, and how gummi uses them.

**Rendering architecture (hybrid, à la Crush).** The top-level model owns
an ultraviolet `ScreenBuffer` and computes a rectangle layout each resize
(`layout.kanban`, `layout.main`, `layout.status`, overlay region).
Sub-components (kanban list, card thread, spec view) render to strings
and are painted into their rects; dialogs live on an **overlay stack** (gate
prompts, comment popovers, new-feature form) composited over a dimmed
backdrop. Craft rules imported from Crush's own UI guidelines: never do IO
or expensive work in `Update` (always `tea.Cmd`), never manipulate ANSI
strings at byte level (`x/ansi`: `Cut`, `StringWidth`, `Truncate`), don't
nest models — keep state in the top-level model.

**Theme system: semantic tokens, one source of truth.** No raw colors in
components, ever. A theme is a small set of semantic slots — `primary`,
`secondary`, `accent`, four foreground tiers (base → most-subtle), four
background visibility tiers, `separator`, and status colors
(`error/warning/success/destructive`) — from which all component styles are
derived once by a builder (Crush's `quickStyle` pattern). Default theme:
built on **charmtone** (Crush's default is charmtone "Pantera"); gummi's
identity comes from the accent slots — gummy-bear berry/lime/lemon hues
mapped to workflow stages, so a card's stage is readable by color alone.
Feature cards are "gummies": small, colorful, chewable units of work. The
fun stays subtle; the base stays restrained. **colorprofile** degrades
everything gracefully on non-truecolor terminals.

**Signature elements** (the details that read as premium):

- **Animated gradient shimmer** (Crush's `anim` package pattern:
  per-grapheme `ForegroundGrad`) on exactly one thing — the actively
  working agent's status line. Motion marks *the* live agent; everything
  else is still.
- **Gradient wordmark** with custom letterforms on the splash/empty state
  (Crush's `logo` package approach) — the first-run screen should make
  someone screenshot it.
- **Status bar as pills**: mode, active/paused/needs-you counts, spend
  meter, contextual key hints — quiet, single-line, always accurate.
- **Syntax-highlighted diffs**: Crush ships a `diffview` component
  (chroma-based highlighting) — evaluate reusing it directly for our
  diff view instead of building one.
- **Restraint**: generous padding, subtle separators over heavy borders,
  one accent per surface, spinners only where something is actually
  happening.

**Selection and focus.** Two questions have to be answerable without
moving: *what is selected* and *which region do the arrow keys drive*.

- *Selection is a band, not a marker.* A selected row wears a full-width
  background bar (`theme.Band`) with the `▸` on it. A one-glyph marker is
  too small to track while paging a list, and it leaves the row's text
  looking exactly like every other row's. A band costs contrast, so a
  banded row collapses the four-tier text ramp to two (`BandText`,
  `BandTextDim`): against the band `FgMuted` lands near 2:1 and `FgFaint`
  near 1.2:1, which would erase the metadata on precisely the row the eye
  was sent to.
- *Focus is the band's strength.* Surfaces keep their selection when
  focus leaves them — moving from the kanban into a card's action list
  leaves the card selected — so presence of a band can't mean focus. The
  accent-tinted band marks the region that owns the arrow keys; the quiet
  grey one a region that is only remembering where its cursor was. The
  focused region's section headers take the accent too
  (`PaneTitleActive`), and the status bar names what the arrows and enter
  do there, so the answer is available by color and in words.
- *Focus on a control is a fill, not a hue.* A focused button is filled —
  the accent for an ordinary one, the destructive color for a danger one.
  Hue alone cannot carry focus on a control that is already colored:
  swapping `Destructive` for `Error` says nothing on a dark palette and
  literally nothing on the light one, where the two slots are the same
  color.

**Quality enforcement.** Crush golden-tests its UI (`x/exp/golden`); gummi
does the same from M0 — every component gets golden-file snapshot tests at
several widths, so visual regressions fail CI, not eyes. Demo GIFs via
`vhs`, scripted and reproducible.

### 6.3 The decision — one control for every human checkpoint

> **Superseded in part by §24 (decision 27).** The design gates, verify
> failures and autopilot this section names are retired with the
> workflow. The one control and the durable decision record (decision
> 18) stand: a session's `ask_user` question, an exhausted envelope, a
> rebase conflict, and a crew's plan, widening and node questions the
> planner passes on (§24.3, §24.7) are its decisions now.

A card stops for a human in several unrelated-looking ways: a design gate
opens, an agent calls `ask_user`, verify fails, a rebase conflicts, a
stage exhausts its envelope, nothing is running and something must be
started. These were separate mechanisms with separate surfaces — the
inbox's `attnKind` queue, the live session's in-memory `PendingAsk`, the
`next` block's ranked suggestions, and the headless driver's own wire
vocabulary. They are one thing: **the card is blocked on a person, and
here are the legal answers.**

They therefore become one thing in the log. A `decision_open` event
records that the card is waiting and what it is waiting for; the existing
`gate` and `ask` events become its *answers*, gaining an id that
correlates them and the id of the option chosen. Nothing new is invented
for the closed case — those two records already exist and are already
durable; what has never existed is a record of a decision that is still
open, which is why an unanswered `ask_user` evaporates when the process
exits and why the inbox has to be reconstructed by inference at startup.
A stage that could not run (its session errored or never started) opens
one of kind `failure`; like a budget stop it has no answer event, and
what closes it is the stage running again. Its answers are a retry,
another profile when the board has one, and "stop here", which takes the
card out of the needs-you queue and leaves the failure on record; the
same failure raised again with nothing run in between is the one stop
and writes nothing new.

Rules that make the control safe:

- **Options are never stored.** A `decision_open` carries the question,
  its kind and its anchor; the answers are regenerated at render time
  from `nextsteps.go` for a workflow decision, or from the live `Ask` for
  an agent question. Freezing them would create a second source of truth
  free to drift from the card's actual state — the same reason a folded
  receipt reads credits from `stage_spend` rather than from its own
  payload.
- **It is pinned while open, inline once answered.** An open decision
  holds its own region directly above the composer, so it cannot be
  scrolled away from and cannot be squeezed out on a short terminal —
  the thread's body is the first region to yield, and at 36×9 it yields
  entirely. The moment it is answered it collapses into the body's
  history at the point in time where it happened.

  The page yields in a fixed order, and the order is the argument. The
  blank rows that separate its regions go first — the row above the
  crumb, the row between the card's title and its stage strip, the one
  under the composer that stops it reading as part of the status bar —
  because on a page you are operating rather than reading, an option you
  can see is worth more than air. Then the crumb, since `esc` answers
  the way out whether or not a row says so. Then the strip and the spec
  line. What never yields is the card's own identity, the question, the
  highlighted answer and the line to type on: one row is reserved for
  the head before the decision takes the rest, because you can answer a
  question on a card you cannot name and should not have to. Unfocused
  options window around the cursor rather than the block being trimmed
  from the bottom, so the answer the cursor is on is never the row that
  went missing.
- **Answering is never ambiguous.** `enter` sends the composer's line
  when there is one, and answers the highlighted option when there is
  not. Typing moves the highlight onto the option that consumes words and
  relabels it to say what it will do with them, so the screen always
  states what `enter` is about to do before it does it. A line whose
  first word is a verb is a command the parser owns (§6) and never aims:
  the confirm chip keeps verb-words, the decision keeps prose, and the
  chip's own "no — send it as a message" hands the line back untouched
  rather than to the highlighted option. One classification, made the
  same way whether or not anything is open.
- **An agent's question always offers to talk it over.** Every
  `ask_user` picker carries a "Chat about this" row after the model's own
  options, and `o` arms the composer as the answer from any question at
  all. The tool used to take an `allow_free_form` flag, and the row
  appeared only when the model set it — but the answer path never
  honoured that flag: a prose line at an open question has always been
  delivered as its answer, because the ask blocks the agent's turn from
  inside a client tool and a second turn is refused. So the flag withheld
  nothing but the *knowledge* that talking was allowed, from exactly the
  questions whose options were too narrow to say what the reader meant.
  It is gone. The model owns the question and the options; whether a
  person may answer in a sentence is gummi's, and the answer is always
  yes. This is the free-form channel *of the offered decision*, not the
  "other option" the rule below refuses: the words become the ask's own
  answer, never an action nobody offered.
- **More than one can be open, and each surface names one.** A card can
  genuinely be waiting on two things at once — a verify gate raised
  beside an exhausted envelope — so `Store.OpenDecisions` reports a list
  rather than a value. Enforcing a single open row was considered and
  rejected: it would mean a closing write on every path that resolves
  one, in every process that can resolve one, and a missed write would
  leave a card reading "needs you" forever. The two surfaces rank
  instead, and rank identically — the thread's pinned control and the
  needs-you queue both name the decision that stops you first (an agent's
  question, then the envelope, then a failed verify, then a design gate)
  — so they cannot disagree about what a card is waiting on. The headless
  driver resolves the same list by recency rather than by rank (§14.1),
  because a caller answering over a wire has no screen to have read a
  ranking off; the two orders agree on every stop raised today, and if a
  future kind makes them diverge the driver's own rule is the one to
  revisit.
- **A decision closes without anyone writing that it did.** The
  append-only log has no delete, so "still open" is a question asked of
  the log rather than a flag maintained in it: a decision is open while
  no later `gate`/`ask` event carries its id *and* the card still sits at
  the stage that raised it. That second clause is what makes closure
  self-healing — a gate crossed by `g`, by `gummi run`, or by a re-run
  all move the card, and moving the card abandons what the
  stage before it was waiting on, whether or not the crossing remembered
  to say so. The one stop that resolves without moving is an exhausted
  envelope, which is answered by the stage simply running again on a
  raised one; a plain re-run of that same stage closes it, and the
  borrowed-stage runs (a plan critique, a rebase resolution) deliberately
  do not count, since neither raises anybody's envelope.
- **There is no *other* option.** Every answer offered comes from the
  legal set — `workflow.Next` and the stage's own guidance. Prose is
  always accepted and always safe: it becomes a turn, never an action
  nobody offered. This is what makes the thread a guide through the
  workflow rather than a prompt that can be talked out of it.
- **The options are deterministic; the narration is not.** A model may
  describe the situation and may route a typed sentence to a stage. It
  may never add, remove, or reorder an option. Prose is always accepted
  and always safe: it becomes a turn or a routed re-entry, never an
  action nobody offered.

  That last sentence is the safety property stated a row above, and it
  is what the split protects. The option rows are `stageActions(in)` — a
  pure function of card state, table-tested, free, and still regenerated
  every render, so "options are never stored" holds unchanged. Above
  them sits a short paragraph that a model may write, and a typed line
  that a model may classify; neither can reach the rows.

  Three things keep that true rather than merely intended:

  - **The narration yields first.** It is the first region of the
    decision's own block to give up its rows on a short terminal, ahead
    of the unfocused options and well ahead of the highlighted answer
    (`fitNarration`). A page too short for both simply has no
    paragraph — the description is what is optional, never the choice.
  - **Every claim carries a resolvable citation.** A generated sentence
    may only assert what it can anchor to a real check, a real event, a
    real hunk, or a real artifact section, and the anchor is resolved
    against the card before the sentence is admitted. An anchor that
    resolves to nothing discards its claim. Hallucinated evidence is
    therefore refused by the code that admits evidence rather than
    prevented by prompting.
  - **Routing walks declared edges only.** A classified sentence is
    routed by `internal/reentry` — compiled in and pure, like
    `gatepolicy` — and every move it can produce is an edge
    `internal/workflow` already declares. An intent that reaches no edge
    becomes a turn. A rewind additionally refuses to happen at all
    unless the miss is first written into the artifact, where an
    unresolved `%%` marker then holds the gate shut until somebody
    answers it.

  The router reads every prose line typed at a stop, not only one aimed
  at "send it back". Its answers are still drawn from the offered set
  and the graph's rerun edges; what moves or spends is shown before it
  happens and waits for the reader, and what spends waits for `y`. A
  line typed at a running or live stage is steering, and a line that
  continues a conversation stays in it.

  **The read is on screen while it runs, and no way out of it costs the
  reader the line.** Placing a sentence is a model call — seconds, not
  milliseconds — and a screen that does not account for them is a screen
  that has stopped: same picker, same line, nothing moving, and the
  reader's own `enter` the last thing that visibly happened. So the pass
  is state, and it is reported where the card reports everything else
  that is happening: in the conversation, beside a stage's own
  "thinking…", and on the card's busy marker, so the board row, the
  dashboard and the status bar animate for it too. It is deliberately not
  reported *as a control*. The picker goes with it — `enter` was the
  answer, and rows that are no longer waiting on anybody must not go on
  standing there as though they were — so the two keys that still mean
  something are the whole table, and the bar says so: `enter` has nothing
  left to commit, and `esc` stops the read. Stopping is only stopping —
  nothing has been proposed yet, so nothing is sent, the line stays in
  the composer and the answers come back.
  The chip's own `esc` is the other one and still sends, because
  declining a proposed act still owes the line a destination; getting it
  there opens a consult session, which is another model call, and that
  one is on screen too, carrying the line, until the session has it. The
  rule holds one step further in than the router itself: abandoning the
  new card a `separate_card` reading opens puts the line back in the
  composer it was typed in. A route that reads prose may never be a route
  that loses it.

  What is still refused is a model-backed *conductor*: something that
  decides what a card may do next, or that a user could argue out of the
  workflow. Describing a stop and placing a sentence are not that. The
  spend is bounded to match — one cheap scribe turn at a stop, cached
  against the card's newest event id and its blocking counts, metered to
  the card's own stage, and never bought at all for a card another
  process is driving.
- **An open ask survives the process that asked it, or dies honestly.**
  Durability without an answer path would be worse than the evaporation
  it replaces — after a restart the blocked tool call, its id and its
  RPC are all gone, so a restored open ask needs a route back or a
  marked exit, and it gets both. **Re-armed on restore:** when the engine
  rehydrates a card's session for the stage an open ask decision was
  raised in, the question is re-armed as that session's pending ask from
  the durable record — with no options, because the recorded ones died
  with the process and are never stored (§ above), so the answer is
  prose, which every question always allows. The answer rides a fresh turn
  (the convention path), not a tool resolution — the blocked call is
  gone — and still carries the same decision id, so the record closes on
  the answer event like any other. **Abandoned when the stage moves on:**
  a decision whose stage is no longer the card's stage — the gate was
  crossed over it, the card was bounced, the stage re-ran without the
  answer — is dead, and `Store.OpenDecisions` reports it as nothing:
  only decisions whose stage still matches the card's current stage are
  open, so a question nobody answered reads as what it is once the card
  has moved past it rather than as a question waiting forever.

Because the same event is what the headless driver raises at its own
checkpoints, the two loops share the decision rather than each modelling
it: both write it through `Store.OpenDecision`, with the same id shape
and the same kinds, so a reader of the log cannot tell which loop stopped
the card. What they share for *when* to stop is `internal/gatepolicy`,
whose `RaiseGate` outcome decides the checkpoint after a critique or
verify pass. It is not the only place a decision is opened: each loop
also opens one at the stops that are its own — the driver at an
`--until` stop, at the landing gate a verified card waits on, at a
blocked gate and after a decompose pass (`Driver.openDecision`); the
board where it raises a card to the inbox (`Shell.logDecision`).

## 7. What gummi is not (scope guards)

- Not a CI system — Verify runs local checks; real CI stays in your PR flow.
- Not a general agent framework — it orchestrates *existing* coding agents.
- Not a cloud service — single-binary local tool, state in your repo.
- Not tmux — gummi owns its sessions; raw attach is the escape hatch.
- Not a merge pipeline — the one integration gummi does onto local main
  is the landing you accept: a feature's squash merge, or a goal's merge
  commit over its cards' commits. The one other merge it makes on its own
  is inside a goal (§17): a goal card lands on the goal branch, and the
  goal branch catches up with main — both gummi-owned, local branches
  that reach main only through that same accepted landing. A **stack**
  (§18) adds a third gummi-owned arrangement of the same kind: a card's
  branch may fork from another card's branch, and gummi replays the cards
  above one that changed. Those replays are local `rebase --onto` on
  branches gummi cut, and the stack still reaches main one accepted
  landing at a time, bottom first. The web face's repositories view
  adds the one write to a base that is not a landing: it fetches
  (`git fetch --all --prune`, a read of the remotes) and, when asked,
  fast-forwards a repository's base to its upstream — only ever a
  fast-forward, refused over a dirty main checkout or a base with
  commits of its own, so it is the `git pull --ff-only` a person would
  have run and never a merge gummi chose. It also deletes a local branch
  no card holds; a card's branch goes only through the card's own
  clean-up, and an adopted one not at all. The same view edits a
  repository's own git config and nothing past it: which remotes it
  knows (add, rename, point elsewhere, remove) and which remote branch
  a local one tracks. None of that contacts a remote or moves a commit;
  a URL naming a transport helper (`ext::…`) is refused, and a
  credential in a URL is stored as given and never sent to the page.
  Releasing stays in your hands. A card
  may name and read the PR it lands through — linking it and pulling its
  review threads in as diff annotations — and, on a person's explicit
  act (decision 25, §22), **publish**: push its own branch and open,
  update or ready its PR through `gh` and the credentials you already
  have. That is the whole of what gummi writes to GitHub: no merge, no
  base retarget it was not asked for, no thread resolution, no CI
  gating, no release, and nothing an agent or a run can start by itself.
  **Adoption** (decision 22) widens which branches gummi may work on
  without widening what it may do to GitHub: a card can be minted onto a
  branch gummi did not cut, and for a pull request gummi will fetch that
  PR's head into a local branch — a read of a remote ref and a write to
  your own repository, nothing more. It never resolves a thread, and —
  new with adoption — never deletes or rewrites a branch it took custody
  of; publishing an adopted card adds commits and nothing else. A fork's PR can be adopted and reworked; the result is a
  local branch you own, and gummi says so rather than implying the work
  can travel back to a fork it cannot write to.
- Not a process editor — one workflow, compiled in. If the workflow needs
  changing, that's a gummi release, not a config file. A **freeform card**
  (§19) is not an exception to that: it does not configure the workflow
  down, it sits outside it, and it pays for that by having no verified
  branch to point at — which is why the two landing floors are stated in
  one predicate rather than left implicit.
- Not a second driver — a card's own stage session acts on gummi through
  its per-card tool contract (§16); nothing reaches gummi by invoking
  another `gummi` process against a board that is running.
- Not a second board — the web face (§20) is another view of the one
  process driving the cards, never a process of its own beside it, and
  a board has one interactive host at a time.

## 8. Prior art & differentiation

- **claude-squad** (Go/charm): manages multiple agent instances in tmux +
  worktrees — closest structurally, but no workflow/spec layer, no roles or
  cost routing.
- **vibe-kanban / crystal / conductor**: kanban-for-agents tools; mostly
  web-UI, mostly single-agent-vendor, no per-stage model routing.
- **schipper.ai FD workflow**: the process gummi automates — it exists today
  as slash-commands + human tmux discipline; gummi turns it into an engine.

gummi's wedge = **structured workflow × role/profile cost routing × TUI
attention management**, in one binary.

## 9. Roadmap

**M0 — walking skeleton (1–2 weeks of evenings)**
Go module on the charm.land v2 stack, `.gummi/` init, feature CRUD +
kanban TUI (static), worktree create/remove, state persistence. The design
system lands here, not in polish: theme builder + semantic tokens,
ultraviolet layout shell, status bar, gradient wordmark splash, golden-file
snapshot tests wired into CI. No agents yet. *Proves: data model, and that
the empty shell already looks Crush-grade.*

**M1 — one feature, end to end**
Copilot adapter via Go SDK: interactive chat pane (the design stage) +
autonomous implement with streamed activity. The fixed workflow, single
active session. *Proves: the core loop feels good.*

**M2 — the fleet**
Scheduler, pause/resume, needs-attention queue,
multiple concurrent features, session resume across gummi restarts,
review stage (fresh-context autonomous, capped auto re-review loop) +
verify stage (config checks + spec plan). Skip flags at feature creation.
Spec annotation editor (`%%`-based, line-addressed + gate feedback loop).

**M3 — profiles & cost**
`profiles.yaml`, per-role model/BYOK env injection, llama.cpp smoke test,
spend metering + kanban cost column, cross-model review default. Budget
layers 1+2: `--max-ai-credits` caps per session, budget-aware system hints,
threshold nudges, budget-exhausted checkpointing. Budget envelopes with
exhaustion gates (layer 3).

**M4 — quality automation**
`%%` question extraction, spec templates, diff viewer with line annotations
(content-hash anchored), rebase-on-main & merge-conflict helper,
landed-branch detection + cleanup, notification hooks (bell/desktop),
plan-time budget estimation.

**M5 — second adapter & polish**
opencode adapter, generic headless adapter, additional themes
(light + alternates on the token system), raw-attach escape hatch.
(Demo GIFs and a docs site were dropped from scope.)

## 10. Decisions & open questions

Decided in the design interview (2026-07-03):

1. **Worktrees are nested** under `.gummi/worktrees/` — fully
   self-contained; `gummi init` writes the needed ignore rules.
2. **Feature IDs**: `FD-NNN` monotonic counter in `.gummi/seq`,
   retry-on-conflict. The id is the stored key and the name of a card's
   worktree; it is not what a person reads. Both faces name a card by
   its title (`domain.Feature.Name` in a sentence) beside the word for
   what it is (Feature, Bug, Research, Goal, Session, Agent —
   `domain.Feature.TypeName`): a person knows a card by what it is about.
   Its number alone (`#178`, `domain.FeatureID.Ref`) is the handle for
   typing one — shown on the card's own head and in the tables that key
   on it, not on a board row or in a notice — and every place a card is
   typed takes the number as well as the id
   (`state.Store.ResolveFeature`). The counter is shared across kinds, so
   a number names one card; an external ref that is itself a number
   (`--ref 42`) is read first, so a driver keeps reaching its own card. Text bound for a
   forge — a commit message, a pull request — keeps the full id: there
   `#178` is somebody else's issue.
3. **One strict workflow, never configurable.** No workflow YAML, ever —
   and, since the merge (§3), literally one graph for every kind:
   `todo → plan → implement → verify → done`. There are no skip flags and
   no quick route; the flexibility they offered was three graphs' worth of
   duplication for two slots each graph half-ignored. The only movement
   that is not forward is the *rerun edges* (implement → plan, verify →
   implement). The quality floor is non-negotiable.

   *Amended for freeform cards (§19).* There is still exactly one graph
   and still nothing that skips a stage of it — but there is now one kind
   of card that is **not in it**: a freeform card (`FF`) holds
   `StageOpen`, which has no edge in the table in either direction, and
   lands on a human's read of its diff rather than on a verified branch.
   The floor did not soften for anything that walks the graph; a second
   floor was named beside it, and both live in `Feature.MayLand`. What
   this decision still forbids absolutely: a card in the workflow
   reaching `done` without its critique and its verify, and any
   configuration that would let one.

   *Reversed by decision 27 (§24).* Workflow cards are retired: the
   floor moved into the crew, and whether to call one is the session's
   choice.
4. **Critique before every gate**: each stage ends with a fresh-context
   pass that tries to refute what it produced, and a *changes* verdict
   re-runs that stage in place — capped (default 2–3 rounds, and bounded
   by the card's budget envelope); past the cap it escalates to the human
   instead of looping. Review used to be a stage of its own; it is this
   pass on the work stage. At design altitude the same pattern is the
   **plan critique**: critique→replan, capped at 2 rounds, then the human
   gate — catching design-level security/correctness flaws before
   implementation tokens are spent.

   *Reversed by decision 27 (§24)*: a crew reviews every node it
   writes; nothing else is critiqued unasked.
5. **The design stage is gummi-native chat** over SDK sessions; raw
   copilot attach is an escape hatch only. **The surface is the card
   thread** (§6): opening a card at its design stage *is* opening the
   conversation, with no separate pane to attach to and detach from, so a
   card has one conversation in one place for its whole length. The chat
   pane that used to host these stages is retired into the thread, which
   inherits what only it could do — unbounded scrollback, raw tool-output
   expansion, the failure tail, and the option picker — since those are
   requirements of reading a run, not of the pane that happened to hold
   them. Watching a run another process drives stays a read-only view
   (decision 13).
6. **The endgame is a squash commit on main** (or, where a person chooses it per landing, a merge commit that keeps the branch's commits). *Amended for stacks
   (§18):* the branch a card forks **from** is now the card's own — a
   chosen local branch, or the branch of the card below it in a stack —
   while what it lands **onto** is unchanged, and a stacked card refuses
   to land until every card below it has. When you accept a
   verified feature, gummi lands its branch on local main as one squash
   commit with a message you approve — no PR or push *automation*: nothing
   publishes a branch unless a person asks (*amended, decision 25, §22:*
   push and PR are acts a person can take from gummi, never steps it
   takes). gummi detects when a branch landed outside this
   flow and offers worktree cleanup either way.
   *Amended for merge landings:* a landing may instead be a `--no-ff`
   merge commit carrying the approved message, which keeps the branch's
   own commits on main. The person chooses per landing, in the landing
   dialog (or `--no-squash` headless), and squash stays the default. A
   merge landing needs no message: it commits with git's own merge message,
   and no drafting pass runs for it. A merge that cannot be made is undone
   and reported, never retried as a squash. A merge landing takes the branch's commits as they are: tidying them is
   the log tab's or `gummi rewrite`'s job, done before landing. A card
   that belongs to a goal, and a goal itself, land as a squash only, since
   a goal's history is one commit per card under the goal's merge commit.
7. **Verify = discovered checks + spec plan**: the repo's build/test/lint
   commands always run, from the spec's `gummi-checks` block —
   auto-discovered into the Verification plan at approval and
   human-gated with the rest of the spec; the verification plan adds
   feature-specific live checks the agent executes.
8. **First-class providers**: GitHub Copilot Pro/Pro+ (premium-request
   pool) and OpenAI-compatible BYOK endpoints (llama.cpp, vLLM, hosted).
   Profiles are designed around exactly these two paths.
9. **No cap on autonomous runs, and none is configurable** — every run starts
   when it is asked for, and nothing queues one behind another. How many run
   at once is the operator's decision. *Amended 2026-10-03 (FD-054):* the
   attended and autopilot caps, their queues and the settings that sized them
   were removed.
10. **One repo per gummi instance** — *reversed 2026-08-19* (FD-070..073).
    One workspace can now manage several repositories, and the repository
    is a per-card choice. `.gummi/config.yaml` takes **at most one** of two
    keys, never both (setting both is a config error, since each defines
    the managed set):
    - `repo: <path>` — the single managed repository, when `.gummi` does
      not sit at its root. It may be the workspace root or any
      subdirectory of it, so `.gummi` at `/project` can drive
      `/project/git/lxd`. Omitting it entirely — the ordinary case — makes
      the workspace root itself the sole repository.
    - `repos: {name: path}` — several selectable repositories. Such a
      workspace has **no default repository at all**: the root is a mere
      parent of checkouts, so every card names one. The creation dialogs
      refuse to create a card until you pick (no pre-selected repo, no
      "default" option), `run`/`bugs new`/`ingest` take `--repo <name>`
      and reject an omitted one before minting an id, and the board's `o`
      key retargets a card that has not cut a worktree yet. A `repos:`
      map with a *single* entry is not a choice, so the dialogs select
      it and skip the tab stop — but they still render the row, because
      naming the repository is the only way the dialog says where the
      card lands. Silence there reads as `repos:` having been ignored.
    - `discover: [glob, …]` — *added 2026-10-06*. Every git
      checkout the globs match joins the `repos:`-style set under its
      **folder name**; it combines with `repos:` (a pinned name wins) and
      is refused beside `repo:`. With none of the three keys and a
      workspace root that is not itself a checkout, gummi scans
      `["*", "*/*"]` — the layout that used to be an error. Only a real
      `.git` directory counts: a `.git` file is a linked worktree (every
      card's own) or a submodule. A card stores its repo by name, so a
      name must never move: two checkouts sharing a folder name get
      **neither**, and `doctor` and the resolution error say which to pin.
      A pool with no default rescans on a name it does not know (at most
      every 2s), so a fresh clone needs no restart; the default repository
      is fixed at launch. A discovered set is also rescanned when a dialog
      that offers repositories opens, and the web card's repository action
      rescans before it is offered. Those reads run off the board's loop
      and refresh each repository's branches with the names, so the dialog
      offers a clone's own branches, and its checked-out branch as the
      default base. Reads are ordered: a snapshot older than the installed
      one is dropped, so a slow read cannot overwrite a fresher one.

    `worktree.Pool` caches one `Manager` per repo root and resolves a card
    through `ManagerFor`; worktrees still live under the *workspace* root,
    so a multi-repo board keeps one `.gummi`. Dependency edges cross repos
    freely — `feature_deps` references `features(id)` with no repo
    awareness.

    The web face lists the set (`GET /api/repos`, the Repositories
    view): each repository with its local branches sorted by who holds
    them — the base, a card's, an adopted one, a goal's, or nobody's
    (`internal/repoview`) — where it stands against its upstream, and
    the fetch, fast-forward and branch clean-up §7 allows. Each
    repository's remotes are listed and edited there too, and a branch's
    upstream chosen. The set itself is still edited in `config.yaml`.

    A **goal** is the one card that is not a per-card choice: it is in no
    repository, its cards each name one, and it keeps a branch in every
    repository they are in (§17.2a, decision 20). The creation surfaces
    therefore never ask a goal which repo it is in, and `gummi goal` has no
    `--repo`.
11. **Spec drafts** live in `.gummi/state/drafts/` while a card is at its
    design stage and has never run one; at the design gate the worktree +
    branch are ensured and the spec is promoted to `.gummi/specs/FD-NNN-slug.md` in
    the main checkout — its workspace home for the rest of the feature's
    life. The artifact is gummi workspace content: it never enters the
    worktree and is never committed, so the feature branch (and the
    squash commit that lands it) carries only product changes. When the
    card lands through a PR under a non-squash merge method instead,
    `gummi squash` is the explicit escape hatch that collapses the
    branch to one presentable commit before it is pushed, preserving
    the same promise across whatever merge method the repo uses.
 12. **One process per card, not per workspace.** Headless
     run/resume/verify/merge/clean each hold an exclusive per-card lock
     (`.gummi/state/locks/<id>.lock`), so independent cards drive
     concurrently while two drives of the same card are mutually excluded.
     The board holds that same lock for every card *it* drives — a session
     takes it before the backend spawns and lets it go when the session
     stops, and the board's own git verbs take it for their duration — so
     the exclusion runs both ways rather than only between headless
     commands. Holds inside one process are refcounted
     (`state.CardLocks`), because a flock is per open file description:
     without that, a merge on a card the board is already driving would
     refuse itself. The TUI additionally holds the whole-workspace lock for
     its own lifetime, so a second board refuses to open. Shared resources
     (worktree creation, merge) keep git's own serialization on the repo's
     `.git` lock, and the SQLite store already serializes writers via WAL +
     `busy_timeout`.
13. **A parallel process can watch what it may not drive.** The live
    agent stream — transcript deltas, tool calls, state changes — is
    otherwise in-process only: the backend CLI is a child of whichever
    gummi spawned it, and the store's session snapshot lands once per
    turn, so it records what happened, never what is happening. Each
    session therefore mirrors its stream to `.gummi/state/live/<id>.jsonl`
    (one JSON object per line, `internal/livelog`), which a second
    process tails: `gummi watch <id>` renders it, `--json` hands it to a
    calling agent, and the board opens it as a read-only pane for any
    card another process is driving. The file is a *view*, not a log of
    record — each new session truncates it (a follower reports the
    truncation as a takeover) and the store stays authoritative. Writes
    are best-effort and never block the run: a full queue drops records
    and says so on the stream rather than stalling the agent. A card
    driven elsewhere is badged on the board, and every verb that would
    write to it is withheld from the action list, the key handler, and
    the help overlay alike — watching is the only thing this board can
    honestly offer.
14. *Retired with research cards by decision 27 (§24): research is a
    session's to do, or a crew of scouts and a scribe.*
    **Third kind, `RS-NNN`** — own compiled-in graph and artifact under
    `.gummi/research/`, no branch or worktree, reusing Review/Verify
    verbatim as the quality floor.
15. **Decomposition is wired into the RS `verify → done` gate** —
    re-runnable from `done`, back-annotates minted FD ids into `## Slices`,
    never wedges the crossing, reserves envelope for its own architect
    pass.
16. **`investigate` as a borrowed pass on an existing FD/BG** is deferred
    to a follow-up.
17. *Autopilot is retired with the workflow by decision 27 (§24). The
    rule survives inside a crew: the planner may redo, rework, split and
    drop nodes, and any widening of scope, budget or done-when goes to
    the person (§24.7).*
    **Autopilot may redo its own work. It may never widen its own
    reach.** A card can be pointed at a stop — *off*, *gates*, or *full*
    — and run itself from wherever it sits. Under *full* it may cross its
    own design gates, answer its own consequential questions, bounce a
    failed verify, and resolve a rebase conflict: every one of those is
    the same work, done again. It may not do anything that enlarges what
    it is allowed to touch. A tool asking to act outside the sandbox
    always parks — the one refusal. Research parks at `decompose`,
    because decomposition mints new cards and creating work is not
    redoing it. Landing on main stays a keypress. Lanes are a number the
    operator sets, never one autopilot raises. Nothing resumes itself
    after a quit without being asked. This is what makes autopilot
    compatible with a quality floor that is never softened: it changes
    *who approves*, never *what must happen* — no implementation without
    an approved spec, no merge without review and verify, at every stop.

    *Amended for the skill curator (§23, D26).* The curator is not
    autopilot and crosses no gate, but it does change what every later
    pass is told, critique and verify included, without a person
    approving it first. That is allowed because it changes neither rule
    above: every pass still runs, and a skill can make a pass worse at
    its work but cannot remove it, answer its decision or land its card.
    The check is after the fact: snapshots, a digest and revert.

    A decision autopilot has taken renders open, with its options,
    marked as autopilot's — and collapses when the answer event lands,
    never on a timer. A countdown was proposed and cut: it would make the
    same decision resolve differently depending on whether a human
    happened to be looking at that card, and it would diverge the TUI
    from the driver, which answers immediately and has nobody to count
    down for. What the mark says is that an answer is already on its way,
    which is true for as long as the command carrying it is in flight;
    `esc` keeps its two meanings rather than gaining a third gated on a
    window that no longer exists.

    Stated mechanically against §6.3: autopilot may answer decisions of
    kind `gate`, `verify`, `conflict`, `ask` and `idle`, and may not
    answer one of kind `budget` — topping up an exhausted envelope is
    enlarging what the card may spend, which is the definition of
    widening its reach, and `u` never silently restarts a run. A sandbox
    refusal is not a decision at all: it is a typed error raised before a
    session starts, with no options and no answer from anyone, and it
    parks. "One refusal" is therefore two statements — the one decision
    autopilot must hand back, and the one condition that was never
    negotiable.
18. **Every human checkpoint is a decision, and a decision is an event.**
    A design gate, an `ask_user`, a failed verify, a rebase conflict, an
    exhausted envelope and an idle card with nothing running are one
    thing — the card is blocked on a person — and they get one control
    (§6.3) and one durable record. The `gate` and `ask` events already in
    `card_events` become the *answer* half of that record, correlated by
    id to a new `decision_open`; the options themselves are never stored,
    only regenerated. This is additive: the existing kinds keep their
    meaning, so history written before the change still reads, which
    matters because card events are never pruned. The rule it enforces is
    that **nothing may block a card without leaving a row** — the reason
    an unanswered question could previously vanish with the process, and
    the reason the needs-you queue had to be re-derived by inference at
    every startup.

    The answer half gains one more field, and it fixes a live bug rather
    than serving the new record. Who answered was inferred from the
    card's *stored* gate-approval mode, while the headless driver
    auto-answers off a flag of its own — so `resume --autonomous` on a
    card stored at `gates` filed the machine's own answers as a person's,
    and the record of what autopilot decided, which is a filter over
    exactly that field, dropped them. The answerer declares itself on the event instead of
    the event guessing from state that was never about the answerer. That
    correction is the honest headline of this work: the durable open row
    is the larger idea, but this is the part that was wrong before.
19. **`enter` means send.** In the card thread `enter` sends the
    composer's line when there is one and answers the open decision's
    highlighted option when there is not; it never runs an action the
    screen is not offering. The state that used to have no visible
    control — a card with nothing running — becomes a decision like any
    other, so a bare composer means exactly one thing: an agent is
    working and there is nothing to decide.

20. **A goal is a card whose work is other cards** (§17), decided in the
    goal interview (2026-09-13). It walks the one graph: the plan is the
    one conversation a person has with it, its implement stage is
    *conducted* rather than written, and its verify checks the combined
    branch. Five existing rules bend for it, each deliberately and each
    only inside a goal:
    - **gummi merges onto a branch that is not main, on its own**: a
      goal card's squash landing goes onto the goal branch, and the goal
      branch merges main in to keep up. Main still moves only on a
      person's landing (§7).
    - **Autopilot creates work inside a goal**: decision 17 has autopilot
      park before a research document becomes cards, because creating
      work is not redoing it. Inside a goal the approved plan is that
      approval — the lead may mint cards, but only cards that serve an
      agreed done-when item, only inside the goal, and only within its
      budget.
    - **A dependency is met without landing on main**: inside a goal,
      "done" for a card means landed on the goal branch.
    - **Something other than a person answers a card's questions**: the
      goal's lead does, and every call a user would notice is recorded as
      a decision for review and shown at the hand-over.
    - **A landing on main is a merge commit** over the cards' commits, not
      one squash, so `git log --first-parent` reads one line per goal and
      any one card can be reverted.
    - **A card's repository is not the goal's** (2026-09-18, §17.2a): an
      outcome may need changes in several repositories, so a goal is in
      none of them. Its cards name their own, it has a branch in each, and
      it lands once per repository — the one place gummi cannot promise
      atomicity, because git has no merge that spans repositories, and so
      the one place it says plainly which repositories have the goal and
      which do not.

    What does not bend: no implementation without an approved plan (each
    card's own plan crosses its own gate, and the lead reads it first), no
    landing without review and verify (each card's, and the goal's own on
    the combined branch), the budget is a ceiling no agent raises, and a
    sandbox refusal is a refusal — the lead plans around it, never widens
    a card's reach.

    *Retired by decision 27 (§24)*: goals are gone. No goal is minted or
    run; a legacy goal row is read-only and never lands. Outcome-sized
    work is a session with a spec and its crews.
21. *Retired with the workflow by decision 27 (§24): there is no plan
    stage or verify gate to hold a card to its promises; a crew is held
    to its done-when checks instead.*
    **A plan's promises are part of the quality floor**, decided after
    the lxd drives (2026-09-15). Until then the floor asked whether the
    stages *ran* — a plan exists, a critique read the diff, the checks
    exited zero — and never whether what the plan *promised* is in the
    branch. Two drives of one feature showed both halves of the gap: a
    plan pinned a golden the implementation silently dropped, and a card
    shipped a branch contradicting its own description's must-keep
    clause because a critique called the contradiction "non-blocking".
    Three critique rounds and a verify passed each time. So a plan now
    writes two checkable commitments, and the verify→done gate holds the
    card to them:
    - an **invariant** (`invariant: …` in `Plan claims`, numbered INV-n)
      is a must-keep clause, usually lifted from the card's own
      description. Verify answers each by id; an unanswered or failed
      invariant blocks the gate. A critique may argue about how to keep
      an invariant; it may not reclassify breaking one as documented.
    - a **golden** quotes its input, and that input must appear
      somewhere on the branch. A golden the work outgrew is struck from
      the plan, not left standing.

    Two rules bound it, both so the floor can never become the reason a
    card cannot finish: it holds a card only to promises it can *read*
    (a golden quoting nothing is prose), and a check that cannot run —
    an unreadable artifact, a search that timed out — is no opinion
    rather than a block. Coverage is recorded, not gated: verify
    declares each changed file no check exercised (`UNPROVEN: <path> —
    <why>`), and gummi carries those onto `status --json` and the `done`
    event, because a pass that never compiled three of the branch's
    files is a pass about the other files and the caller cannot
    otherwise tell.
22. *Amended by decision 27 (§24): no new card adopts a branch
    (`--adopt` and `--pr` are gone), and nothing walks the graph. A
    legacy adopted card keeps the custody rules below: gummi never
    deletes, rebases or force-pushes its branch (`Feature.Adopted`,
    `worktree.ErrAdoptedBranch`).*
    **gummi may take custody of a branch it did not cut**, decided
    2026-09-22. Until then every branch on the board was one gummi
    created from a base it chose, and work that arrived any other way —
    a colleague's half-finished branch, a PR sitting under review — could
    be reached only by starting a fresh card and hunting the branch down
    by hand. A card may now be minted **onto** an existing branch
    (`--adopt <branch>`, or `--pr <url|number>` for the branch behind a
    pull request): gummi attaches a worktree to that ref instead of
    cutting a new one, and the card's stages read and extend the work
    already there. Six rules bound it, and together they are what keep
    *custody* from becoming *ownership*:
    - **It works on the branch itself, not a copy.** A copy would be the
      safer-looking choice and the wrong one: the branch a reviewer is
      reading is the branch the rework has to improve, and a second
      branch beside it is a reconciliation problem handed back to the
      person who asked for help.
    - **It never deletes one.** `clean` removes a landed card's worktree
      and its branch; for an adopted card it removes the worktree and
      stops. The branch was never gummi's to destroy.
    - **It never rewrites one unasked.** An inherited branch is usually
      old, and gummi says how far behind its base it is rather than
      catching it up: a rebase here rewrites commits that may already be
      pushed and read by other people. Catching up stays the manual
      rebase it always was, with a warning naming the risk.
    - **It still walks the whole graph.** `todo → plan → implement →
      verify`, unchanged and unskipped. The plan stage reconstructs the
      spec by reading the diff it inherited — which is what keeps
      decision 3's floor true for code gummi did not write, rather than
      carving the first hole in it.
    - **The ending is the hand-off, not the squash.** An adopted card
      finishes at a verified branch that is handed back (§14's `handoff`);
      landing it is still available and still a deliberate act. Squashing
      somebody else's commits into one of yours is not a default.
    - **What was already broken is not the card's fault.** The approval
      baseline (decision 7) runs the checks on the worktree before the
      card changes anything, which for an adopted card is precisely the
      state it inherited — so verify holds it to what the rework broke
      and no more, and the hand-off says plainly what was red on arrival.

23. **The board has a second face, and neither face owns logic**, decided
    2026-09-27. `gummi web` draws the board as HTML for a browser rather
    than streaming the TUI through a pty (§20). What made the earlier
    refusal right — two implementations drifting apart — is answered by
    running the TUI's own conductor headless in the web host, so there is
    one implementation of what happens to a card; by moving the read side
    into packages both faces call; by one answer path; and by a golden
    test that fails when the two faces offer different answers to the
    same decision. One board has one interactive host at a time, held by the
    instance lock the TUI already takes; the headless verbs are not hosts
    and do not take it.
24. **A card's history may be rewritten, but never its content**,
    decided 2026-09-30 (§21). A card's own commits can be reworded, and
    contiguous runs of them squashed into one, from the log tab of
    either face or `gummi rewrite`. Reordering and dropping are not
    offered, so the branch's tree is unchanged by construction and the
    verify that ran on it still stands. It is a human's act, like
    `squash`: refused while an agent
    holds the card, on an adopted branch (D22) and on a landed one, and
    on pushed commits until the person acknowledges the force push gummi
    prints and never runs.
25. **A person may publish a card from inside gummi**, decided
    2026-10-09 (§22). Until now every write to GitHub was a command gummi
    printed and a person ran, which kept the board honest about what it
    does and left the last step of every card outside it. A card's own
    branch can now be pushed, and its pull request opened, updated and
    marked ready, from either face or `gummi push` / `gummi pr`. Six
    rules bound it:
    - **A person starts it, every time** (amended by decision 31: or has
      allowed one session to, once). No stage, autopilot step,
      goal, schedule, MCP tool or `run`/`resume` reaches the publish
      code, and a test fails if one imports it. An agent may draft the
      words; it never sends them.
    - **It is optional, and gummi finds out rather than being told.**
      Publishing exists only where `gh` is signed in and git has a push
      credential for the remote. gummi detects that and acts on it:
      with it, the acts are offered; without it, they are simply
      absent, and nothing else about a card changes. There is no switch
      to turn on.
    - **The credential is yours, and gummi holds one only if you hand
      it over.** `gh` and git authenticate as they already do. A person
      may instead store a GitHub token and an SSH key in the web page's
      settings, for a host where setting those up means a shell they do
      not have open. gummi then lends each to the one command that
      needs it — the token as `GH_TOKEN` on its own `gh` calls, the key
      through an ssh-agent it answers as while a push runs — and puts
      neither in an agent's environment (§22.2). *(Amended: this rule
      first read "gummi reads no token and stores no key".)*
    - **The quality floor still stands at the one step it protects.**
      Marking a PR ready needs `MayLandAt(head)`; a push and a draft PR
      do not, because neither can merge anything.
    - **Only the card's own branch, only forward.** Never a repository
      the person cannot write to, never a branch gummi does not hold,
      never over commits the branch itself never had, and never a force
      push without a lease pinned to the tip the person was shown.
    - **Never a path around landing.** A card with a PR lands through it
      or locally, never both; creating one is the act that picks.
    This reverses the "never pushes, never opens a PR" of decisions 6
    and 22, §7, §18.5, §20.5 and §21.2, each amended in place.
26. **gummi learns, and no person approves the lesson first**, decided
    2026-10-15 (§23). Sessions record what they learned; a curator
    distils it into the workspace's skill pack, `.gummi/skills`; gummi
    lints each write, counts each skill's use, archives what goes
    unused, and delivers the pack to every session on claude, copilot
    and opencode, beside the operator's own skills and never over them. Four rules bound it:
    - **The curator is a background job, not a card.** Its backend,
      model, schedule and budget are config; its spend is its own ledger,
      shown on the stats tab. It acts on no card (§16).
    - **It writes through tools, only into the pack.** gummi does the
      write, the lint and the commit. Never global memory, never a
      person's or a pinned skill.
    - **Sessions stage, the curator publishes** (amended 2026-10-16).
      A card's session may stage a change to the pack that only its own
      sessions read; the curator reviews it before any other card sees
      it, and is the only writer of `published`.
    - **Sessions read a published revision**, pinned at session start,
      scoped per repository. A revert moves it.
    - **The check is after, not before.** Snapshots, a digest and
      revert, as in Hermes. Every pass, critique and verify included,
      gets the pack: a skill can make a pass worse, never remove it.
    This amends §4.1a, §16, §19.3c, §19.7, §19.9 and decision 17 in
    place.
27. **gummi supports three agents: copilot, claude and opencode**,
    decided 2026-10-21. codex, pi and antigravity are gone, adapters and
    all, so every feature is built and tested against the agents people
    run rather than spread thin across six. headless stays as the line
    protocol the demo and the browser tests drive; it is not offered as
    an agent to run cards on.

27. **Work starts as a session, and a crew is the session's to call**,
    decided 2026-10-21 (§24). Every card becomes a session; a session may
    grow a spec and call on a crew: a planner whose plan of nodes a
    person approves, and specialist agents that carry it out, each
    checked and reviewed, onto a branch of the crew's own. gummi offers
    the process and no longer enforces it: the session agent calls a
    crew like a tool, or a person asks for one. Workflow cards are
    retired, with no crew template that reproduces them (*implemented*,
    §24.12): nothing mints or runs one, and a legacy row is read-only.
    This decision **reverses decision 3** (the graph and its floor),
    **reverses decision 4** (critique before every gate becomes review
    after every crew node), **retires decision 20** (goals) and with it
    autopilot (decision 17), research (14–16), plan promises (21),
    delegation (§19.10), new branch adoption (22), spec ingestion and
    card dependencies (§11), and ends the verified-branch floor in
    `Feature.MayLand`, leaving the one §19.1 named beside it: a person's
    read of the diff.

    What does not bend, inside a crew: the plan passes
    `crewplan.Problems` before a person sees it; every writing node is
    checked and reviewed; two nodes never write the same path at once,
    and what a node owns is enforced when it lands; the person approves
    the plan, or has pre-authorised crews up to an amount, and every
    widening always (decision 17's "may redo, may never widen"); no agent
    approves its own crew; a node's question
    reaches the person whenever the frozen spec does not answer it; and
    the session's envelope is a ceiling no agent raises — a crew that
    reaches it pauses for the person's top-up (§24.7a). After a crew there is no
    automatic review: the card says how many commits it is past its last
    verification, and a person decides.

28. **A crew becomes a workflow: a recipe of steps run in the
    session's own tree**, decided 2026-10-22 (§25). A workflow is a
    recipe file of steps, with `parallel` and `repeat` blocks, kept in
    `.gummi/workflows/` beside the bundled `deliver`, `investigate` and
    `fix-until-green`. Each step is a fresh-context agent working in the
    session's worktree. There is no branch per step and nothing to land.
    A run leaves its changes uncommitted, snapshotted at every step
    boundary, and the session commits what it keeps. A session holds a
    list of specs and runs one workflow at a time. A run is one block in
    the thread, and a person may open any step and talk to its agent.
    This **amends decision 27**. Inside a run, three rules stay fixed:
    a person allows the run, money is a ceiling no agent raises, and a
    question the run cannot settle reaches the person. Review, checks
    and design become steps a recipe chooses. The crew branch, node
    landing and the owns check (§24.3–24.6) are retired. Built
    2026-10-22; §25.14 records where the build departed from §25.

29. **An agent may watch on a schedule and dispatch sessions**, decided
    2026-10-23 (§26), built (§26.8). A schedule may start a run on an agent,
    and a recipe may hold a `run:` step — a command, no model, whose
    declared `quietExit` ends the run with nothing but the schedule's
    `no-change` — and a `dispatch` step that mints a session in a
    repository the recipe names literally, with an envelope carved from
    the agent's. An agent may propose such a schedule; the person's one
    allowance covers the recipe and every recipe its dispatches name,
    and enables it. This **amends §19.3d** (an agent may run a workflow
    and dispatch sessions), **§19.9** (an agent may propose a schedule,
    never enable one) and **§25.13** (a command step is no longer
    deferred). Decision 28's three rules are unchanged.

30. **A session may span repositories**, decided 2026-10-23 (§27), built
    (§27.11). A session keeps its primary repository and may attach
    others from the workspace's pool: the card's branch name is cut in
    each, in a worktree of its own beside the primary one. The agent
    attaches freely (`repo_attach`), as may the person, and nothing
    lands without a person; each repository lands on its own, and the
    card closes when every repository with commits has. This **amends
    §19** (a session's repository), **§22** (a PR per repository),
    **§25.6** (snapshots span the trees), **§26.4** (`repos`) and
    **§18** (stacks refuse attachments). Decision 28's three rules are
    unchanged.

31. **A session may publish its own branch and watch its checks, on a
    person's standing allowance**, decided 2026-10-25 (§22.14). A person
    switches "agent may publish" on for a session once, the way "always
    allow runs" is switched on; thereafter the session's `git_push`,
    `pr_create`, `pr_update`, `pr_ready` and `pr_checks` push its branch,
    open and update its PR, mark it ready and read its checks with no
    confirm of their own. This **amends decision 25** (publishing is no
    longer a person's act every time: the allowance stands in for the
    per-act confirm), **§22.7** and **§22.11**. Merge, retarget,
    force-push, resolving a thread and landing stay a person's, and no
    tool offers them. Decision 28's three rules are unchanged.
32. **A run may publish its session's PR and wait on it, on the same
    allowance**, decided 2026-10-28 (§28). A recipe may carry `pr:` steps
    (push, open, update, mark ready) and one `watch:` step that waits on
    the session's PR without spending a model turn: gummi polls it,
    sorts what changed into a closed set of events, and runs the
    recipe's handler for each — inline steps, a named recipe, or a
    `goto` back to an earlier step — pushing after each pass. Such a run
    may be proposed only for a session whose "agent may publish" is on
    (decision 31), and its proposal names every PR act and the
    resolved trust filter, so allowing the run allows exactly those.
    This **amends decision 25** (a run reaches the publish code, through
    `internal/agentpublish` alone), **§22.14** (gummi now polls, for a
    run), **§25.7** (a run waiting only in a watch resumes its polling
    by itself after a restart; its next handler waits for a person's
    Resume) and **§25.13** (the waiting step is decided). Merge,
    retarget, force-push, resolving a thread and landing stay a
    person's; a watch ends on a merge a person made, or on green and
    approved, as its recipe says. Decision 28's three rules are
    unchanged: a handler at the watch's max, a repeated event, an
    unreadable trust filter and a long silence all reach the person.

Still open:

1. **Copilot SDK maturity** — verify the Go SDK exposes interrupt,
   permission callbacks, and per-session env cleanly; if per-session env
   isn't supported, run one CLI server *per role config* (still fine).
   Also verify the SDK surfaces per-turn usage/credit events and a
   `max-ai-credits` equivalent in session config (the flag is public
   preview); if usage events are missing, fall back to polling
   `/usage`-style session stats or estimating from token counts.
2. **Metering fidelity** — how precisely credits/premium requests can be
   attributed per stage from SDK events; may need an estimation fallback
   for the kanban cost column.
3. **A steer typed mid-turn goes straight through.** The design drew it
   as held — a line sent while an agent is working showed as *queued for
   the end of the turn* — and it is not built that way: the turn is
   dispatched immediately, and only a budget nudge is ever held back for
   the next one. Neither is obviously right. Delivering at once is what a
   chat does and lets a correction land before more work is done on the
   wrong thing; holding until the turn ends keeps the agent from being
   interrupted mid-thought and matches what the picture promised. Worth
   settling deliberately rather than by whichever the backend happens to
   tolerate.

## 11. Spec ingestion — decomposing an existing spec into features

> **Superseded by §24 (decision 27).** Ingestion is retired: there are no
> feature cards to decompose a spec into, and the `ingest` verb is gone.
> Work that starts from a document is a session that reads it, keeps a
> spec of its own (§24.2) and, where the work divides, calls a crew whose
> planner splits it into nodes (§24.4). The section is kept as history.

gummi's unit is a Feature (FD): one PR-sized branch that flows through the
fixed workflow. Today features are *born blank* — the creation form takes a
single title line, mints an ID, and stamps the empty spec template; the design
stage then authors the FD from scratch. But work often *starts* from a
document that already exists — a PRD, a design doc, a meeting write-up that
describes many features at once. Ingestion inverts the birth path: gummi reads
a source spec and **decomposes it into N pre-seeded FDs**, so the design stage
*refines* an already-populated draft instead of starting cold.

### 11.1 How ingestion fits the stances

Three existing rules shape the design, and they all point the same way:

- **Tool owns mechanics, model owns content** (§4.5). gummi owns reading the
  source file, the decomposition *schema*, minting IDs/slugs, writing drafts,
  and the review surface. The model owns where the feature boundaries fall, the
  slice of source text per feature, the dependencies, and the coverage map. So
  the decomposition is delivered through a **structured client tool**
  (`propose_features`, the same plumbing as `ask_user` / `submit_verdict`), not
  free prose gummi regexes out.
- **One-shot agent passes already exist.** The scribe's landing draft is the
  template: a transient session that is not tracked on the board, sends one
  prompt, collects a structured result, and closes. Ingestion is the same shape
  but **architect-role** — decomposition is design judgment (boundaries,
  dependencies, coverage), not scribe mechanics.
- **High-leverage, error-prone work takes a human gate.** Auto-minting a dozen
  FDs you then have to delete fights the attention-based model. Ingestion's
  output is therefore a **proposal you review, edit, and approve** — reusing the
  annotate/gate interaction (§6.1) — and only then materializes onto the board.

### 11.2 What a proposal carries

The architect pass emits, per candidate FD, a structured record:

- **title + one-liner** — feed the slug and the feature's one-liner directly.
- **source refs** — which sections / line ranges of the source it came from;
  provenance and traceability back to the original document.
- **seeded draft** — the extracted *problem*, *constraints*, and *acceptance
  criteria*, mapped into the real template sections. The *considered/chosen
  approach* sections stay open `%%` prompts — ingestion seeds the **what**, not
  the **how**; converging on an approach is still the design stage's job.
- **open questions** — anything the decomposition is unsure about is emitted as
  a `%%` marker on its anchor, so **decomposition uncertainty becomes the FD's
  open-questions checklist for free**, with no new machinery.
- **depends_on** — other proposals this one needs, recorded as a first-class
  edge in the dependency store (§11.4a), not prose.

Plus one document-level **coverage map**: every source requirement mapped to an
FD or explicitly marked out-of-scope, with an **unmapped** list surfaced loudly
so the human can see nothing fell through the cracks.

The **source document itself** is copied into the workspace
(`.gummi/ingest/<name>.md`) for provenance, so each draft references it by path
and any downstream agent can read the full context on demand — the draft carries
only the slice, not the whole document.

### 11.3 Decomposition granularity

The target is **PR-sized vertical slices**: each FD is one independently
reviewable and verifiable branch, which is exactly gummi's deliverable. Too
fine and you drown in per-FD workflow overhead; too coarse and each "feature" is
a mini-project the design stage has to re-split. A typical PRD lands as a handful
to ~15 FDs. The granularity rule lives in the ingest system hint, and the
coverage map is where the agent justifies its cut.

### 11.4 The pipeline

```
  source.md ──▶ [A] architect pass ──▶ IngestResult ──▶ [B] review gate ──▶ [C] materialize
                (propose_features)      (proposals +      (edit/merge/split/    (mint + seed
                                         coverage)         drop/approve)         drafts → todo)
```

- **A · extraction primitive** *(engine)* — `Engine.Ingest(source, profile)`,
  modeled on the one-shot scribe passes: copy the source into `.gummi/ingest/`, open a transient
  architect session with the source path + granularity + coverage rules in the
  system hint, register the `propose_features` client tool whose handler
  captures the structured `IngestResult`, and close. Creates nothing on the
  board.
- **B · review gate** — the cheap human judgment gummi can't do itself, on two
  surfaces. In the **TUI**, an ingest-review pane reusing the annotate
  interaction (§6.1): the proposals as a line-cursor list with a detail panel
  (one-liner, refs, dependencies, seed highlights) and a coverage panel that
  flags the unmapped list loudly. Keys `r` rename · `o` edit one-liner · `x`
  drop/undrop · `m` merge into the proposal above · `A` approve (confirms, and
  surfaces any unmapped count before minting). *Split* is intentionally not a
  gate operation — a too-coarse slice is better re-split by the design stage
  once it is a real feature, so the gate only coarsens (drop/merge) and edits. On the
  **CLI**, `gummi ingest <path>` prints the proposals + coverage and gates on a
  y/N confirmation (or `--yes`).
- **C · materialization** *(engine)* — each approved proposal runs the existing
  create path (mint number → ID → slug → create in todo, with the proposed skip
  flags), but writes a **seeded** draft instead of the blank template:
  provenance header, the mapped sections, and the `%%` questions. Any
  `depends_on` proposals are written as first-class edges (§11.4a) once both
  sides have minted IDs. Features land in todo, pre-loaded, ready for the
  normal workflow.

### 11.4a First-class dependencies

> **Retired by decision 27 (§24).** Card dependencies, the `deps` verb
> and the dependency gate are gone with the workflow cards they ordered.
> Ordering inside a piece of work is a crew node's `needs` (§24.4);
> ordering of landings between branches is a stack (§18), which never
> gates work. What follows is history.

Shipped (FD-058–FD-062), superseding the prose-based `depends_on` this
section originally described. A `feature_deps` store (`internal/state`) holds
direct dependency edges between cards, independent of ingestion — added by
`gummi deps add <dependent> <depends-on>` (`rm`/`list` remove or read them
back), by the TUI's `p`-key picker (self- and cycle-checked), or populated
automatically from an ingested spec's `depends_on` proposals.

A dependency counts as **met** only at `StageDone` — verified and landed —
so anything short of that is unmet. The gate lives at the single chokepoint
every forward path into a card's coding stage (`Implement`/`Fix`) already
resolves through, `Engine.Advance`: a `StatusBlockedDependency` result sits
alongside `StatusBlockedQuestions`/`StatusBlockedDiff`, naming each
outstanding dependency and its current stage (`BlockingDeps`) rather than
just a count. `GateBlockers` — the read-only pre-check the headless
`--gate-approval=attended` path uses — reports the same blockage before
offering to approve a coding gate. Both TUI and headless drivers route
through `Advance`, so the gate cannot be bypassed by a future driver; no
transitive closure is walked (only direct dependencies block), and the design
stage is never gated — only entry to the coding stage is. The board and spec view resolve and render each
dependency's live status rather than static prose.

Each piece is independently testable: domain types + the seeded template (golden
tests, no agent), `Engine.Ingest` + the tool (unit-tested against the `Fake`
agent, both the client-tool and fenced-convention paths), materialization
(headless engine/state), the `gummi ingest` CLI, and the TUI review pane (driven
through simulated key presses against a `Fake` architect).

A dependency is **scheduling**, and is deliberately not the same fact as
a stack position, which is **topology** (§18). A dependency says "do not
start coding until that card is done" and is met only at `StageDone`; a
stack position says "my branch forks from that card's branch" and never
gates anything. Conflating them was this feature's first design and it
was wrong twice over: a card with two same-repo dependencies has no single
unambiguous base, and treating a position as a dependency would hold
every card above the bottom out of its coding stage until the one below
had landed — the serialized waiting a stack exists to remove. The two
coexist on one card, and `worktree.ResolveCollapseBase`'s original
"exactly one dependency names the parent branch" guess is superseded for
a stacked card (it remains the fallback for an unstacked one, so
`gummi squash` is unchanged).

### 11.5 Deferred

- **Persisted coverage report** — keeping the source→FD map queryable after
  ingestion, for traceability audits against the original document.

## 12. Bugs — the second kind

> **Superseded by §24 (decision 27).** Bug cards are retired, and so is
> the `bugs` import verb. A bug is a session; a session's spec template
> may carry a Reproduction section, and a crew may open with a scout
> that reproduces it. A legacy `BG` card is read-only. The rest of this
> section is history.

> **Superseded in part.** This section was written when a bug ran its own
> graph. It does not any more: the three workflows merged into one (§3),
> and a bug walks `todo → plan → implement → verify → done` like every
> other card. What survives is everything below about the *kind* — the
> discriminator, the report, ingestion — and the design-stage contract,
> which is now phase 1 (triage) and phase 2 (diagnose) of one prompt
> rather than two stages. The graph in §12.2 is kept as the record of
> what it replaced.

Features are design-driven: explore an approach, converge on it, plan it, build
it. Bugs are diagnosis-driven: something already works wrong, and the work is to
reproduce it, find why, and fix it without regressing. gummi models a bug as a
second **kind** of work item that shares everything structural with a feature —
the store, the engine, the worktree, the board, the never-skippable critique →
Verify quality floor — and now the workflow too; what differs is its stages'
contracts and the bug report it carries instead of a spec.

### 12.1 The kind discriminator

A work item has a `Kind` (`feature` | `bug`). Bugs get `BG-NNN` IDs from the
same monotonic counter features draw from (shared, so numbers never collide),
and everything derived from the ID — branch, worktree, artifact path — follows.
Only three things branch on kind: which workflow governs transitions, which
template seeds the artifact, and a board badge. The empty kind reads as a
feature, so items predating bugs need no backfill. This is deliberately *not* a
separate `Bug` entity: the engine already orchestrates *(item, stage)* sessions
generically, so a parallel type would duplicate the store, engine, and board for
no gain.

### 12.2 The bug's stage contracts

A bug runs the one graph (§3). What was a second workflow is now what its
stages are *told*:

```
  todo ──▶ Plan ──▶ Implement ──▶ Verify ──▶ Done
           (triage,   (the fix,     (repro is gone,
            then       + regression  regression test
            diagnose)  test)         covers it)
```

- **Plan, phase 1 — triage** — confirm and reproduce the bug; pin down repro
  steps, expected vs actual, environment, severity.
- **Plan, phase 2 — diagnose** — converge on the root cause and record it in
  the report. The gate demands the Root cause section: a design stage that
  wrote nothing there does not cross.
- **Implement** — the smallest change that resolves the bug **and** a
  regression test. Its critique and the verify rerun edge both land here.
- **Verify** — Verify gains a sharp bug meaning: the deterministic repo checks
  still run, and on top the reproduction must no longer reproduce and a
  regression test must cover the fix.

*(What this replaced: `todo → Triage → Diagnose → Fix → Review → Verify →
Done`, with Triage and Diagnose skippable.)*

### 12.3 The bug report

A bug's durable artifact is `.gummi/bugs/BG-NNN-slug.md` (the analog of the
spec): Summary · Reproduction · Expected vs actual · Environment · Root cause ·
Fix · Review · Verification. Symptoms are seeded from the source; Root cause and
Fix stay open `%%` prompts — converging on *why* and *how* is the design
stage's and the fix's work, exactly as the spec's chosen approach is.

### 12.4 Ingestion — sources, not decomposition

Bug ingestion reuses spec ingestion's pipeline shape — source → proposals →
human gate → materialize — but the source yields *discrete* bugs rather than one
document to decompose, so there is no architect pass and no coverage map. A
`BugSource` is the seam:

- **GitHub** — `gh issue list` against a target repo (default: the repo's origin
  remote; overridable to any `owner/repo`), filtered by label/state. The import
  is **deterministic and agent-free**: one issue → one proposal, body verbatim
  into Summary, labels mapped to severity. Per-bug reproduction and root-cause
  enrichment is the design stage's job, not the source's — "tool owns
  mechanics, model owns content" (§11.1), applied to bugs. This spends no tokens
  on issues you drop at the gate.
- **Manual** — a single hand-entered bug, from the TUI new-bug form or
  `gummi bugs new`.
- **Future** (Sentry, Linear, …) implement the same interface.

Each bug persists its **external ref** (e.g. the issue URL), so re-ingesting a
repo skips bugs already on the board rather than minting duplicates — the one
piece of machinery doc-ingest didn't need, and what makes GitHub polling safe.

The gate lives on two surfaces, mirroring §11.4, and both pick a **single**
issue out of one fetch rather than gating a batch — an entire repo's issues
should never land in todo from one keystroke. In the TUI the gate *is* the
new session's draft (§19.8; it was the new-card dialog): a pasted issue reference is imported into it with `alt+g`
(the title and body land in the box, editable, with the labels reported
beside the reference and the severity read from them), and the issue
picker — `alt+g` with no reference, or `G` from the board — lists the
chosen repository's issues with a live substring filter, greys the ones
already on the board with their card id, and on `enter` fills the dialog
rather than minting; Create from a dialog filled that way returns to the
picker with the row greyed, so importing several is one `enter` each. gh
runs in the chosen repository's checkout with its origin's owner/repo, so
a `repos:` workspace whose root is no checkout still lists the right
issues. The CLI (`gummi bugs ingest`, gated y/N or `--yes`) keeps its batch
import for scripted use, plus an additive `--issue N` that resolves N against
the same fetched batch and materializes just that one bug.

### 12.5 Deferred

- **Severity-ordered backlog** — using the seeded severity to rank the todo
  column, once there are enough bugs for ordering to matter.
- **Agent triage-at-ingest** — an optional pass that dedupes/clusters near-
  duplicate issues before the gate, if verbatim import proves too noisy.

## 13. Research — the third kind

> **Superseded by §24 (decision 27).** Research cards, their scratch
> tree, their deterministic verify and the decompose gate are retired. A
> question is a session's to answer, or a crew of scouts and a scribe's;
> new work a report proposes is started by a person as new sessions. A
> legacy `RS` card is read-only and never runs. The rest of this section
> is history.

> **Superseded in part.** Like §12, this section was written when research
> ran its own graph. It walks the one graph now (§3). The order also
> inverted: it used to survey first and converge second, and now the
> design stage converges on the question and the direction while the work
> stage gathers the evidence. Everything else here — the kind, the
> document, the deterministic verify, the decompose gate — is unchanged.

Features are design-driven and bugs are diagnosis-driven; research is
investigation-driven: the ask is open enough that neither a spec nor a bug
report fits, and the work is to ground an answer before anything gets built.
gummi models research as a third **kind** of work item that shares everything
structural with a feature or a bug — the store, the engine, the worktree
machinery, the board, the never-skippable critique → Verify quality floor, and
now the workflow itself — and carries a research document instead of a spec or
a bug report.

### 13.1 The third kind

A work item's `Kind` gains a third value: `research`. `RS-NNN` IDs draw from
the same monotonic counter features and bugs share, so numbers never
collide. Unlike a feature or a bug, an `RS` card has **no branch and no
worktree** — its artifact resolves to `.gummi/research/RS-NNN-slug.md` in the
main checkout, and its work stage runs against the repo from the card's
scratch tree (§4.3). Only three things branch on kind, same as bugs: the
contract each stage is given, which template seeds the artifact, and a
board badge. The empty kind still reads as a
feature, so nothing predating research needs a backfill.

### 13.2 The research card's stage contracts

A research card runs the one graph (§3), read-only and worktree-less:

```
  todo ──▶ Plan ──▶ Implement ──▶ Verify ──▶ Done
           (shape the (investigate: (deterministic
            question)  read-only,    citation +
                       cited)        coverage check)
```

- **Plan** *(interactive, architect)* — converge the brief into a research
  question, a direction to pursue, and the constraints the investigation is
  bound by. The gate is the human's.
- **Implement** *(autonomous, architect, read-only)* — ground the brief
  against the repo (and any cited external sources) and write up findings
  with `path:line` citations. Branchless: it runs in the card's scratch tree
  (§4.3), never on a branch, and every writing tool is stripped, so the
  read-only property is structural rather than a promise. Rerun/bounce edges
  land here, as they do for every other kind.
- **Verify is reused verbatim** — same stage, same round cap, same
  escalation, same board columns, sharpened only in *what content it
  enforces* (§13.4).

Roles reuse `architect` and `reviewer`; there is no `profiles.yaml`
migration for research.

*(What this replaced: `todo → Investigate → Shape → Review → Verify → Done`,
which surveyed before it converged.)*

### 13.3 The research document

The template's sections and the decompose gate's `propose_features`-shaped
input (§13.5) are designed together, so turning an approved document into
FDs is nearly free — the template *is* the ingest contract:

| section | seeded by | consumed by |
|---|---|---|
| `## Brief` | the ask, in the requester's own words | Plan (the question to shape), Implement (the question to ground) |
| `## Questions` | the open questions the research must answer | Implement (grounding target), §13.4's coverage check |
| `## Findings` | Implement — prose with inline `path:line`/`path:start-end` citations | §13.4's citation check, the reviewer |
| `## Constraints` | the constraints the investigation is bound by | Plan (direction must fit them) |
| `## Options` | Plan — candidate directions with tradeoffs | Plan's own convergence |
| `## Direction` | Plan — the recommended direction and why it wins | the reviewer, and the reader of `done` |
| `## Slices` | Plan — one row per proposed follow-on (title/one-liner/depends-on/requirements/id) | §13.5's decompose gate (`propose_features`-shaped rows), back-annotated with minted FD ids |
| `## Out of scope` | Plan — what the research deliberately won't cover | §13.4's coverage check (an explicit out-of-scope line settles a question without a slice) |
| `## Open risks` | Plan — risks and what would de-risk each | the reviewer |
| `## Review` | reviewer findings | the researcher, resolving each one |

The durable artifact lives at `.gummi/research/RS-NNN-slug.md` (§13.1).

### 13.4 Deterministic verify

Research's Verify is agent-free and spends no tokens — "tool owns
mechanics, model owns content" (§11.1), applied to grounding. Three checks,
all deterministic:

- **No open user `%%` threads** — the existing gate rule, reused verbatim.
- **Citations resolve** — for every `path:line`/`path:start-end` reference in
  `## Findings`, the file exists, the line is in range, and the quoted
  snippet (when the doc quotes one) still matches the file.
- **Coverage reconciles** — every `## Questions` bullet is quoted in some
  slice row's `requirements` (the fenced yaml under `## Slices`) or given an
  explicit `## Out of scope` line `- <the bullet>: <why not>`; anything
  left unmapped is surfaced loudly rather than silently dropped. The match
  is the bullet's text, not its typography — case, spacing, a leading
  question number, emphasis and trailing punctuation are ignored — and
  nothing looser: a paraphrase, or a Findings paragraph that answers the
  question, does not map it.

The floor is not a rule the document meets by luck. Every research stage
is told it in the same sentence the refusal prints
(`verifydoc.CoverageRule`), and it runs **as part of verify**, not only at
the done edge: its report opens the verify kickoff, so the verifier can
repair what it names, and it runs again as the stage ends, where a failing
floor fails the verify with the report on the thread — so a verify cannot
pass a document that "mark done" then refuses. A refusal at the done edge
names the unmapped questions and the rule.

A safety note: research's autonomous work stage runs branchless, in the
card's scratch tree (§4.3) rather than the main checkout, under the
reviewer's per-role read-only deny policy (§4.4). The read-only tool
stripping is the research guarantee; the scratch tree is what keeps it from
resting on tool coverage alone.

### 13.5 The decompose gate

Decompose is to the RS `done` gate what worktree creation and draft
promotion are to the feature spec gate — a side effect of crossing an edge:

```
verify (checks pass) ──▶ [decompose → proposal gate → materialize FDs + deps] ──▶ done
```

- **Crossing is decoupled** — the document is approved on its own merit; a
  failed or dropped decomposition never un-approves it. Research lands
  `done` either way, and the decompose pass is re-runnable from `done`.
- **Metering** — the pass reserves envelope like `TurnReserve` and reports
  `exhausted` before starting a doomed session, so a card without headroom
  fails loudly instead of partially materializing.
- **Back-annotation** — the minted FD ids are written back into `## Slices`
  rows, so the RS↔FD traceability link is bidirectional without a new store
  type.
- **Headless surface** — the gate exits `question` with proposals and a
  coverage map on the NDJSON event; `resume --approve` mints all of them,
  `resume --request-changes "<note>"` re-runs the pass with the note
  attached, and per-proposal edit/drop/merge stays a TUI-only affordance
  (the existing ingest-review pane, §11.4).

### 13.6 Diagnosis — the second research mode

An open ask and a broken behaviour both need grounding before anything is
built, but they are not the same document. Research is question-driven:
Brief → Questions → Findings → Options → Direction. A fault is
symptom-driven, and the three things it has to record have no slot in
that shape — how the fault is recognised, what was **ruled out**, and the
fact that a symptom may have **more than one** cause.

A bug card does not cover it either. A bug presumes one defect, commits
to fixing it in the same card, and has exactly one `## Root cause`
section. The case this mode exists for is the one where you do not yet
know what is wrong, or how many things are wrong: one symptom, two
independent causes, two fixes.

**It is a mode, not a fifth kind.** Everything structural about a
diagnosis is a research card already — the RS id, no branch, no worktree,
the scratch tree (§4.3), the read-only build stage, the agent-free
document verify (§13.4), the decompose gate (§13.5). A kind would buy an
id prefix and cost a `== KindResearch` branch at every one of those
sites. What actually differs is the *contract*, which is what the kind
already selects — so `domain.ResearchMode` selects it one level down. The
empty mode is the survey, so no RS card written before diagnosis existed
needs a backfill.

```
  todo ──▶ Plan ──────▶ Implement ────────▶ Verify ──────▶ Done
           (scope the   (investigate:       (citations +   (decompose
            fault)       read-only, cited)   coverage)      → fix cards)
```

| section | written by | consumed by |
|---|---|---|
| `## Symptom` | the ask, in the requester's own words | Plan, Implement |
| `## Reproduction` | Plan — how the fault is recognised; "it does not reproduce, here is the log" is a valid answer | the design gate, Implement |
| `## Constraints` | Plan — time, scope, what must not be touched | the design gate |
| `## Evidence` | Implement — prose with inline `path:line` citations | §13.4's citation check |
| `## Ruled out` | Implement — each candidate cause eliminated, and by what | the fix cards, who would otherwise re-walk them |
| `## Causes` | Implement — one bullet per cause, saying why it produces the symptom | §13.4's coverage check |
| `## Slices` | Implement — one row per cause, `kind:` defaulting to `bug` | §13.5's decompose gate |
| `## Out of scope` | Implement — a cause deliberately not fixed, with the reason | §13.4's coverage check |

Three things follow from that table:

- **The readers are shared, the headings are not.** `spec.Layout` is the
  one place that maps mode → (evidence section, coverage section, default
  slice kind); `verifydoc` and the gates take a layout rather than knowing
  either document. A survey cites under Findings and reconciles Questions;
  a diagnosis cites under Evidence and reconciles Causes. The checks
  themselves are byte-identical, which is the evidence that this is a
  layout and not a second implementation.
- **Coverage reconciles causes.** Every `## Causes` bullet must be
  answered by a slice's `requirements` or an explicit `## Out of scope`
  line — so a diagnosis cannot find a cause and then silently drop it.
- **Slices mint fixes.** `## Slices` rows carry a `kind:`, defaulting to
  `bug` for a diagnosis and `feature` for a survey, and `Materialize`
  mints per proposal. A diagnosis's follow-on work arrives as BG cards
  carrying a bug report; their `## Root cause` stays the `%%` prompt,
  because the fix card's own design stage is what establishes the cause
  against the code it is about to change.

**Causes is not required at the done gate**, the same way `## Slices` is
not required of a survey (§13.5): "I could not establish the cause, and
here is what I ruled out" is an honest terminal for an investigation, and
a gate that forbade it would be a gate that rewards inventing a cause.
`## Evidence` is required at every edge past design, because a diagnosis
with no evidence has nothing to have shown anything with.

**Where it earns the most is inside a goal.** When a goal card fails
verify twice, or a done-when check fails for a reason nobody can name,
the lead's only moves were to bounce with a note or park. A diagnosis is
the lead's bounded way to convert an unexplained failure into scoped
work: `card_create` accepts `kind: diagnosis`, it runs read-only for a
small envelope, and it terminates in rows rather than a diff.

### 13.7 Deferred

- **`investigate` as a borrowed pass on an existing FD/BG** — grounding a
  single feature or bug before planning it, the way plan critique borrows
  the plan stage.
- **A dedicated `researcher` role** and its own profile key.
- **RS→FD provenance as a first-class edge** in the dependency store — the
  back-annotated `## Slices` rows plus the seeded FD's header line are
  enough for now.
- **Non-repo research** (web, external docs).
- **A new TUI review pane for proposals** — the existing ingest-review pane
  is reused meanwhile.
- **A mode-aware artifact noun** — a diagnosis's document is still called
  "the research document" by the ~25 surfaces that name it, because
  `Kind.ArtifactNoun` is keyed on the kind and every one of them must
  agree (§13.1). Splitting it is a change to the view structs that carry
  `kind`, not a one-line rename.
- **A diagnosis that can prove a cause with a failing test** — the mode is
  read-only by construction, so it proposes fixes rather than
  demonstrating them. Promoting it to a branch-carrying card is the
  answer if diagnoses start getting bounced for unproven causes, or if
  the fix cards keep re-deriving the reproduction from scratch.

## 14. Non-interactive driver & skill distribution

> **Superseded in part by §24 (decision 27).** The headless driver was
> rebuilt on sessions: `gummi run "<prompt>"` mints a session,
> pre-authorises its crew up to `--envelope`, sends the prompt and loops
> until the session is idle with no crew open (`done`) or something needs
> a person (`needs_you`); `gummi resume <id> [--answer TEXT | "<message>"]`
> answers and drives on. The stage-shaped flags (`--gate-approval`,
> `--until`, `--bounce`, `--approve`) and the dependency gate below are
> gone. `docs/HEADLESS.md` is the current reference; §14.3 and §14.4 still
> hold. What follows about stages and gates is history.

The TUI assumes a human at the keyboard. A second driver runs the **same
engine and the same quality floor** unattended, so a calling agent can ship a
feature end to end — and gummi ships the skill that teaches that agent how.
Nothing here softens the invariant workflow: the driver changes *who approves
a gate*, not *whether* the floor runs. Anything a human would resolve becomes a
durable, resumable escalation with a non-zero exit — deterministic failure over
silent degradation.

### 14.1 The driver

One `gummi run` process drives exactly one card (a free-form description)
through the one workflow, holds its per-card lock, streams
milestone + decision NDJSON, and **stops at a verified branch — it never
merges**. The engine's full restartability (SQLite state, spec on the branch,
session resume) makes `resume` free: each invocation runs forward until the
caller must decide, then exits.

- **Gate control** — `--gate-approval=attended` (default) checkpoints the
  design gate for `resume --approve`/`--request-changes`; `=autopilot` crosses
  it unattended. Blockers (open `%%`/diff threads, and a stage that drafted
  nothing) are honored either way; Verify is never a caller gate — it is the
  floor's stop-at-verified.
- **Verify-fail bounce** — a `verify FAILED` (or a review cap-hit) escalation
  is un-parked with `resume --bounce [--note <why>]`, which rewinds the
  card to implement and drives the critique→verify tail again — the CLI counterpart of the TUI's `b` key (§10 review floor's rerun
  edge). The `--note` becomes an addendum to the reborn implement kickoff,
  alongside any open `%%` diff/spec annotations the engine already folds in.
  The same flag takes the graph's other rerun edge: a card still at
  implement — the plan itself turned out wrong — rewinds to plan, and the
  note rides the replan kickoff.
- **Dependency gate** — a card cannot enter its coding stage while a direct
  dependency (§11.4a) is unmet; `Advance` returns `StatusBlockedDependency`,
  which the driver reports as the same `blocked` event as an open `%%`/diff
  thread, naming the outstanding card(s) in `blocking_deps`.
- **Landing and cleanup are separate verbs, not part of `run`/`resume`.**
  `run`/`resume` never merge; `gummi merge <id> -m <message>` is the headless
  counterpart of the TUI's `m`, requiring the card at a verified branch and a
  Conventional Commits message with no diff dump or agent attribution before
  it will touch git. `gummi handoff <id>` is the counterpart of `h` — it ends
  a verified card WITHOUT landing it (final checkpoint, `handed_off` stamp,
  the same gate floor), emits a `handed off` event naming the branch the
  caller now owns, and is the only way to close a card from a script without
  either merging it or destroying its work. `gummi clean <id>` is the
  counterpart of `c`, removing a landed card's worktree and branch. `gummi commit <id> -m <message>` commits
  a card's own uncommitted worktree changes onto its own branch, with no
  PR-linked or stage precondition, so a dirty card can be readied for
  `squash` (or, unlinked, `merge`) without raw git. All three hold the same
  per-card lock as `run`/`resume` (Decision 12) and stream the same typed
  NDJSON/exit contract.
- **PR landing** — a linked card refuses `gummi merge`; `gummi pr link/unlink/status/comments`
  name and read the PR, `gummi squash <id> -m <message|->` collapses the
  branch to one presentable commit before the user's `git push`, and
  `pr comments --ingest` writes review threads as diff annotations so
  `resume --bounce` rewinds review-round work exactly as it does for
  gummi's own reviewer findings.
- **Envelope required** — a headless run refuses to start without one
  (`--envelope $N` or `GUMMI_ENVELOPE`, in dollars); exhaustion fails loud (no
  auto-topup). The board's creation dialogs prefill `ui.DefaultEnvelopeCredits`
  (2000 credits, $20) when
  `GUMMI_ENVELOPE` is unset — a number in a field someone is looking at and can
  edit, which is a different thing from a number an unattended run assumes.
- **Design questions are delegated** — an interactive stage's `ask_user`
  becomes a `question` checkpoint (answerable by option or free-form), unless
  `--autonomous` auto-takes the recommended answer.
- **Liveness** — a per-stage inactivity timeout (`--stage-timeout`) escalates a
  hung stage rather than blocking forever.
- **Steering seeds** — `--acceptance <file|->` seeds the spec's verification
  plan; `--ref <id>` persists an external correlation id (`status`/`resume`
  resolve it); `--until <stage>` stops cleanly at a design boundary
  (event `stopped`, exit 0) for a human review before implementation spends
  tokens. `--until` is per-invocation only — it is never persisted, so it
  must be re-passed on every `resume` to keep stopping at later boundaries.
- **Read commands** — `status`/`spec`/`diff` are agent-free and take **no
  lock** (SQLite WAL + read-only git), so they observe a live run safely.
  `status --json` distinguishes two terminal signals a poller must not
  conflate: `verified` — the verify gate passed and the branch is **ready to
  land** (stamped when the floor reaches the stop-at-verified gate, and false
  while verify is still in flight, so an already-ahead branch mid-run never
  false-positives) — and `done` — the card is **closed**. `done` does not
  mean "merged": two of the three endings reach it without gummi merging
  anything, so a poller that wants "is this on the trunk" reads
  `branch_state == "landed"`, and `handed_off` tells it the card was closed
  with its branch deliberately kept. A headless run ends at
  `verified:true`/`done:false`; only a land or a hand-off flips `done`. It also carries **why** a card stopped, so an unattended driver
  never has to parse the event stream to find out: `escalation` is the
  newest open decision (its kind, the question verbatim, and the stage it
  was raised in) or absent when nothing is waiting; `rounds` is the
  per-loop bounce counters; and `stage_spend` is the per-stage cost
  breakdown, largest first. A review bounce buys an entire second implement
  pass, so `rounds` beside `stage_spend` is what makes "is this reviewer
  worth its bounces" answerable at all. Note `stage_spend` accumulates
  across rounds (its key is feature/stage/model/role), so it is per stage,
  not per round — `rounds` is what says how many passes a figure covers.

### 14.2 The exit contract

Each `run`/`resume` ends on a typed `Status` with a stable exit code, so a
caller branches on the result without parsing stdout:

| exit | status | meaning |
|---|---|---|
| `0` | `verified` | verified branch ready — report it, stop |
| `0` | `stopped` | `--until` reached its clean stop — `resume --approve` to continue |
| `1` | `error` | setup/agent failure — nothing partial landed |
| `2` | `question` | delegated `ask_user`, or a caller design gate awaiting a decision |
| `3` | `blocked` | open `%%`/diff threads block a gate |
| `4` | `escalation` | a rerun/critique cap was hit, or a stage returned no clear verdict |
| `5` | `exhausted` | the credit envelope ran dry |
| `6` | `timeout` | a stage went quiet past the inactivity budget |

The exit/event `verified` above names the run outcome — *a verified branch
is ready* — not a merge, and not the card's own `done`. It used to be
spelled `done` too, which left one word meaning "a branch is ready" in the
exit table and "the card is closed" in the card model; the collision was
documented rather than removed, and documenting a trap is not removing it.
A run reaches `verified`; only a card is ever `done`. Inside a goal, a
child card reaching its verified branch emits `card_verified` — a
notification, not a terminal event.

Long autonomous stretches (implement → critique → verify) carry no caller
decisions under `auto`, so one `resume` streams that whole tail and returns
only at `done` or an escalation.

### 14.3 Skill distribution

`gummi skill install` generates a `SKILL.md` (YAML frontmatter + markdown body)
and writes it where each supported agent reads it: Claude, Copilot, and
opencode share one convention. **Content converges on
one primitive** — there is no per-agent instruction format, only where it
lands differs: a **project-scope** install writes `.claude/skills/gummi/` for
Claude, Copilot, and opencode (Copilot and opencode both read the Claude-style
skill layout) — one file, not three.
`--scope user` diverges per agent (Claude + opencode share
`$CLAUDE_CONFIG_DIR`/`~/.claude`; Copilot uses `~/.copilot`); `--agent` overrides detection; scope is detect-and-ask with
project recommended.

Two properties keep the doc honest:

- **Grammar can't drift.** The body's command grammar is generated from the
  real `run`/`resume`/`status`/`doctor` flag sets and its exit table from
  `driver.Status`, and a golden test asserts every shipped flag appears — the
  doc is locked to the binary in CI.
- **Whole-file overwrite protection.** The frontmatter is version-stamped with
  a content hash of the body; `install`/`list`/`doctor` compare it to detect a
  stale or hand-edited file and refuse to overwrite without `--force` — safe by
  default, never silently stale.

### 14.4 Guided setup (`gummi doctor`)

`gummi doctor [--json] [--deep]` emits a structured readiness checklist —
repo, workspace, backend, profile, auth, envelope, lock — that the skill's
first-run flow consumes. It **reports; it never repairs**. Auth is probed
offline by default: a BYOK key is confirmed by environment-variable **name**
(never its value), and an interactive-login backend degrades to `unknown`
with the exact command handed to the human to run. `--deep` adds a live,
TTL-cached probe per configured role that sends one real backend turn to
confirm the role's model is actually reachable, catching a misconfigured
model id that the offline checks can't see. The profile check steers toward
a cost-tiered profile and carries the nesting-cost warning (§5). `ready` is
false iff any check fails; warnings and unknowns are advisory (an unset
envelope warns — a run can still take `--envelope` — rather than blocking).

## 15. References

- Spec-driven parallel agents workflow: <https://schipper.ai/posts/parallel-coding-agents/>
- Copilot SDK (Go): <https://github.com/github/copilot-sdk>
- Copilot SDK GA announcement: <https://github.blog/changelog/2026-06-02-copilot-sdk-is-now-generally-available/>
- Copilot CLI BYOK/local models: <https://github.blog/changelog/2026-04-07-copilot-cli-now-supports-byok-and-local-models/>
- BYOK how-to (env vars): <https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/use-byok-models>
- Charm libraries: <https://charm.land/>
- Crush (the visual bar; UI architecture studied from
  `internal/ui/AGENTS.md`): <https://github.com/charmbracelet/crush>

## 16. How an agent meets gummi: inside one card, or outside the board

An agent meets gummi one of two ways, and the two never overlap:

- **Inside a card.** The agent IS a card's stage session — an implementer,
  reviewer, scribe, a consult beside a stage, a freeform card's session.
  It reaches gummi's own tools over the per-card MCP endpoint (`gummi
  __mcp --feature <id>`), and the endpoint is the boundary of what
  it may act on: the one card it is bound to, the tools its stage hands
  it. It never reaches gummi by invoking another `gummi` process: the
  engine holds its card's per-card lock for the session's life, so a
  shelled-out `run`/`resume` would contend for a lock already held and
  lose.
- **Outside the board.** An agent, a script, or CI drives a *fresh* gummi
  through the headless CLI driver (`gummi run`/`resume`, §14) — its own
  process, its own lock, NDJSON events and typed exit statuses to branch
  on.

There is no third way, and there used to be one worth naming: an agent
hosted inside the TUI, bound to the whole workspace rather than to any
card, acting on every card through a board-level tool contract
(`board_list`, `card_status`, `card_run`, `card_new`, …). It is gone, on
purpose. Every one of its seven tools was either a re-wording of what
the board's own driving loop already does (run a card, mint a card) or a
read the outside path already serves (`gummi status`, `spec`, `diff` —
lock-free, safe for an agent in another process to call). Keeping it
meant keeping a second surface for the same verbs, a second lock story
and a second place a decision could disagree with the board's own — for
an agent whose only unique capability was acting on cards no person had
asked it to act on. Where you want an agent to drive gummi at all, the
headless driver is the way; where you want an agent to *work*, it is a
card. An unattended goal's lead (§17) is the sanctioned form of "an agent
acting on the whole board": it is inside gummi's own driving loop,
constrained to the goal doc, and every turn it costs is recorded
against the goal. The skill curator (§23.4) is the second: it acts on
no card, writes only the workspace's skill pack through two tools, and
charges its own ledger.

**The knowledge-delivery contract.** Knowledge about *gummi* — the
workflow, the gates, the lock, this taxonomy — travels with the agent's
session (the skill/system context an inside or outside agent is given),
identical regardless of which repo it's pointed at. Knowledge about *a
repo* — its build commands, style, test and review conventions — lives in
that repo's own instructions (its AGENTS.md/CLAUDE.md/equivalent) and
never in gummi's.

**Precedence.** The repo's own instructions govern craft; gummi governs
process (the stage, the gates, never merging, never spawning a second
driver). Where a repo's instructions ask for something the workflow
forbids — "always merge your own branch," "skip review for small
changes" — the workflow wins, and the agent says so rather than silently
complying or silently ignoring the repo.

## 17. Goals — the fourth kind

> **Superseded by §24 (decision 27).** Goals are retired: no goal is
> minted or run, and the goal engine, its ledger, substrates,
> experiments, the notebook and programmes are deleted. A legacy `GL`
> row is read-only and never lands. An outcome is a session with a spec;
> its crews do the work, spending from the session's envelope (§24.7a). This section is kept as the record of what crews
> borrowed from it — the done-when items, the held-budget rule, a planner
> that answers its workers' questions.

A goal (`GL-NNN`) is a card whose work is other cards. You describe an
outcome and a budget, agree what "done" means, and walk away; gummi works
out which features, bugs and research that takes, runs them on one shared
branch, and comes back when there is a working, verified result to judge.
Decision 20 records the rules it bends.

### 17.1 The shape

```
todo ──▶ plan ──────────▶ implement ─────────▶ verify ─────────▶ done
         agree the goal   the lead runs the    check the         you land it,
         with the         goal's cards on      combined branch   send notes back,
         architect        the goal branch      against done-when or hand it off
         ▲ you            (silent)             (silent)          ▲ you
```

- **Plan** is the architect's conversation. The goal doc
  (`.gummi/goals/GL-NNN-slug.md`) carries the objective, a
  `gummi-done-when` block (each item `says` a statement and has exactly
  one of a `check:` command, an `experiment:` (§17.8) or `judge: true`), limits, a budget section with a rough
  cost per item and a `gummi-goal` block naming the lanes, and a
  `gummi-cards` block — one row per card, each serving at least one item;
  a row with an existing card's `id` attaches it. A row names the `repo:`
  its card is minted into and a commanded item names the `repo:` its check
  runs in (§17.2a). The gate (`goalPlanProblems`) refuses an item nothing
  can check, an item no card serves, an attachment that belongs elsewhere,
  a repository the workspace does not manage, and a card list the budget
  cannot fund. Crossing it (`startGoal`) mints and attaches the cards, puts
  every done-when command into the doc's `gummi-checks` (never baselined),
  and hands the goal to autopilot.
- **Implement** has no stage session. `Engine.GoalTick` builds a snapshot,
  asks the pure `goalpolicy.Decide`, and executes: land one verified card,
  raise or drop cards, run a lead turn, start ready cards up to the lanes,
  wrap up, or finish by starting the goal's review. The driving loop — the
  board, or the headless driver's goroutine per card — starts the cards a
  tick names through its ordinary autopilot path. A run of the goal's
  implement stage (a review's changes, a failed verify, your send-back) is
  recorded as rework the lead reads, never a session.
- **Verify** runs the goal's checks, each in the goal tree of the
  repository its done-when item names, and the goal's
  verify contract: judge the judged items, record each item met or not, and
  write and run the try-it guide. A goal whose verify does not pass goes
  back to its cards for its rework rounds, then stops ready for you,
  partial.
- **Done** is a merge commit on main — one per repository the goal touches
  (`LandGoal`: a last catch-up per repo, the checks again if any of them
  brought something in, then `GoalTree.Merge`, home repo first) — a
  hand-off, or an abandonment.

### 17.2 The goal branch

Nothing in `internal/worktree` names a trunk: every "main" is `HEAD` of
`Manager.repo`. So a goal card resolves (`Pool.ManagerFor`) to a manager
whose repo is the goal's worktree. With no other change its branch forks
from the goal branch, `SquashMerge` lands it there as one commit,
`Landed`/`BranchAhead`/`Diff` read against the goal branch, and the
dependency gate's "met at done" means "landed on the goal branch". The
goal catches up with main by *merging* main in (`GoalTree.CatchUp`), never
by rebasing, so no running card's recorded fork point drifts; a conflict
gets an implementer pass in the goal worktree, and gummi concludes the
merge only when nothing is left unmerged. Attaching or detaching a started
card moves its own commits with `rebase --onto` (`RebaseOnto`). Once a goal
has ended and its trees are gone, its cards fall back to their own
repository's manager.

### 17.2a A goal is not in a repository

An outcome is not a checkout. A goal's cards name their own repositories
(`gummi-cards`' `repo:`, or an attached card's own), and the goal has a
**goal tree** — a worktree on a branch of its name — in each of them. The
paragraph above then holds once per repository instead of once per goal,
and a single-repo goal is the case where that is the same sentence.

- **Nobody is asked.** The creation dialogs skip the repo row for a goal
  (`cardForm.asksRepo`) and `gummi goal` has no `--repo`. A goal is minted
  into a *provisional* home — the workspace default, or the first
  configured repo where there is none — because its own branch has to be
  cut somewhere while its plan is still being agreed.
- **The plan settles the home.** Crossing the plan gate (`settleGoalHome`)
  moves the goal card to the repository most of its cards are in, ties
  going to the first row. Nothing has landed on the goal branch yet — the
  doc lives in `.gummi/goals/`, never in the tree — so re-homing is
  dropping an empty branch and cutting it again elsewhere. `SetRepo`
  refuses a goal, for the same reason the dialog does not ask.
- **Where the trees are.** The home tree is the goal card's own worktree,
  `.gummi/worktrees/GL-NNN`; every other repository's is
  `.gummi/worktrees/GL-NNN@<repo>`, its sibling. `Pool.EnsureGoalTree` cuts
  one whenever a card reaches a repository the goal has not touched yet
  (the plan gate, an attachment, the lead's `card_create`), and
  `managerForGoalCard` resolves a card to the tree of ITS repository.
- **Checks name their repository.** A done-when `check:` is a command and a
  command needs a directory, so the item carries `repo:` (default: the
  home) and `runGoalChecks` runs each group where it belongs. The trees are
  siblings on disk, so a check that genuinely needs two repositories at
  once reaches the other by relative path.
- **Landing is N landings.** Git has no merge that spans repositories.
  `LandGoal` catches each tree up, then merges each — home first, skipping
  any whose branch is already in its main, so a retry is safe — and a
  failure part way through returns `ErrGoalPartlyLanded` naming what landed
  and what did not. The hand-over's `repos` says the same. The alternative
  was pretending a goal lands once, which would be a lie about the one
  thing a reader needs to know at that moment.

A **stack** is still one repository (§18.1): a branch can only fork from a
branch in its own. What spans repositories is the goal.

A dropped card leaves the goal at once. One you attached goes back to the
board with its commits moved onto main. One the goal created has no other
reason to exist, so it is closed where it stands (`CloseGoalDropped`):
straight to done, stamped handed off with its branch kept, its open
decisions abandoned — never walked through stages it did not pass.

### 17.3 The budget

The goal envelope is a hard ceiling (`goalpolicy.Ledger`):

```
available = envelope − goal's own spend − Σ held by cards − reserve
held      = a live card's max(envelope, spend); a landed or dropped card's spend
```

Minting leaves part of the pool ungiven, for lead turns and raises: four
lead turns per card, never less than a tenth of the pool nor more than
three tenths — cards hold their whole envelopes however little they have
spent, so a lead given a flat tenth runs dry half-way through. A
card is raised only from `available`; lead turns are capped by it; the
reserve (the lead's `reserve_set` estimate, else 15% of the budget, at
least 100) belongs to the goal's own review and verify.

`available < 0` is answered in three steps, because running out of money
is not a reason to choose which work to abandon (§17.4a). What a card
that has verified did not spend comes back the moment it lands, so that
is counted first — a goal short of credits it is already holding is not
short. Then a waiting card is shrunk to what the goal can still fund.
Only if it is still short does it stop: `NeedBudget`, naming the card
whose allocation it cannot cover, keeping every branch, every commit and
every envelope, with nothing new started until a person answers. It wraps
up instead only when there is no work to keep — every live card unstarted
— since a card funded below the floor buys a guaranteed exhaustion rather
than a chance at the work.

It used to wrap up in every case, which dropped the unfinished cards
without the raise ever having been offered, and did so or did not
depending on nothing but how many of them happened to be running at that
tick: only a waiting card can be shrunk, so the same goal, sixteen
credits short, carried on with two cards waiting and abandoned all three
— 470 credits of finished work among them — with two of them running.

Only a person raises the ceiling, and a raise lifts a wrap-up the budget
forced.

**The second ledger.** Credits buy agent turns. A goal proved by an
experiment (§17.8) spends something credits cannot buy — time on
infrastructure there is one of — so it has a second budget, agreed in the
same block of the same doc: `runs:` and/or `minutes:`. The gate refuses an
experiment item without one.

```
spent      = the runs on disk that took the substrate, and how long they held it
held back  = ReserveRuns (2) runs, and that many typical runs' minutes
CanProve   = something is left under the ceilings          (may spend what is held back)
CanExplore = something is left above what is held back
```

It is deliberately **not** converted into credits. An exchange rate between
a cluster's minutes and a model's tokens would be the fiction §5.1's
per-stage shares were, and it would let a goal buy verification by starving
its agents, or agents by starving its proof. Spend is derived from the run
records, never stored: a run another gummi made, or one that finished while
nobody was looking, counts the moment it is read. What is held back belongs
to the runs the goal needs **in order to be judged** — the proof of its
final heads, and that proof again after a rework round — so that nothing a
goal does to find out early can spend the run its hand-over needs. When it
cannot afford that run it stops and asks (`NeedSubstrate`, `NeedBudget`'s
twin): which proof to go without is no more the goal's decision than which
work to abandon. `gummi resume GL-NNN --runs N --minutes M` raises it, and
like the envelope it only ever goes up.

### 17.4 The lead

A lead turn is a short synchronous session on `agent.RoleLead` (falling
back to the architect's backend and model), in the goal worktree, acting
only through goal tools — client tools natively, MCP through a per-turn
endpoint. The conductor wakes it for a kickoff, your notes, rework, stuck
and exhausted cards, and findings to settle; the hooks `GoalAnswer` and
`GoalPlanCheck` put it between a goal card and a person for questions and
plan checks, falling back to what a plain autopilot card would do. Every
tool writes the goal log (`card_events` of kind `goal`), and every turn is
booked to the goal card. Three failed turns in a row wrap the goal up.

What a done-when item says is the owner's and no tool changes it. Its
check's command is only the means of proving it, and an agreed command can
be unable to — a wrapper that reports its own exit status instead of the
program's. The lead may repair the command (`done_when_check_fix`), held to
what makes a check a check: it must fail in a throwaway checkout of main,
and pass on the goal branch once every card serving the item has settled.
The repair is a decision for review with the agreed command as its
alternative, and a check run that passes after an item was marked not met
settles the item.

### 17.4a What a goal waits for

Three things stop a goal without ending anything, and they share one rule:
*which work to abandon is not the goal's decision, and waiting is not
abandoning.* The budget (`NeedBudget`), the agent backend (`Stall`), and —
the same shape one noun over — the **environment**.

A verify may end `VERDICT: blocked`: this machine cannot run the
verification plan. `gatepolicy` has always kept that apart from a failed
verify so that it "never burns a corrective round against a problem no
retry fixes"; a goal used to lose the distinction one layer up, reading the
park as `Stuck` — two lead turns, then the card's branch — and sending its
own blocked verify back to its cards as rework. Now:

- The park carries `state.ParkReasonBlocked`, the one value a goal reads
  (`goalCardState`) to tell a card to wait for from a card to give up on.
  Only the verifier's **own** word earns it (`verdict.BlockedByEnvironment`,
  `gatepolicy.ReasonNoEnvironment`): a pass gummi floored to blocked — a
  failed check, a promise not on the branch — is about the work and stays
  `verify-blocked`, handled as a stuck card.
- `goalpolicy.Blocked` holds no lane, goes to the lead **once** (it may
  belong under `[CI-only]`), and is never dropped for it. When nothing else
  can move, `Decide` returns `Stall` naming the card; the hand-over reads
  *waiting on an environment*, and the items it serves say the same rather
  than *not met*.
- A retry is a verify session, so there is no timer. `Card.Retry` is set
  when a person has been at the goal since the card parked — a note, or a
  resume of a goal that stalled on it (`Engine.GoalResumed`) — and then the
  card starts again with a note saying why. A resume at verify re-runs a
  verdict that was the environment's (`resumeFinishedVerify` honours only
  verdicts a card *earned*).
- The goal's own blocked verify stops at verify, whole, with no rework
  round spent; in the board it is the goal's one stop that reaches you.


**The quiet backstop.** A goal that produces nothing at all — no
landing, no lead turn, no card event, no run heartbeat — for longer than
`goalpolicy.MaxQuiet` (30 minutes) stops and says so, because a goal in
that state is finished, stalled or waiting for a person and is never
simply quiet. It deliberately does not require that nothing is running:
a card the conductor reads as running is the one thing no other rule
touches, and a card that is genuinely working produces events.

The 30 minutes is a floor, not the figure. It is longer than the default
stage timeout on purpose, so a stage that has genuinely stopped is cut
off by its driver — which is an event — before the backstop is reached.
That margin is what matters, so `QuietCeilingFor` keeps it rather than
the number: a caller that gives its stages longer than 30 minutes to be
silent in (`--stage-timeout 45m`) raises the ceiling with it. Held at
the constant, such a run had its goals stopped inside a window its
operator had explicitly allowed.

### 17.5 Silence and the hand-over

A goal card's stops (escalations, failures, exhausted envelopes) are
recorded where the conductor reads them and wake the goal; they never reach
the inbox. The goal's own verify is the one stop that does: *ready for you*,
whole or partial. The hand-over (`GoalReport`) is one structure every
surface renders — the goal page, `status --json`, the headless `done`
event, and the goal doc's Report section: each done-when item met or not
met with its evidence, the cards with their landed commits and diff stats,
the decisions for review, declined findings, what was found along the way,
the try-it guide, and the budget tree.

### 17.6 Watching a goal's cards

Silence is not opacity. A goal's cards are ordinary cards with ordinary
threads, and the page a person opens to look in on a running goal is a
way into them: the goal page's card list carries a cursor, `enter` opens
the selected card's page with its live stage block streaming, and `esc`
comes back to the page rather than to the board underneath it. `P` opens
that page from the goal row or from any of its cards. On the board the
cards are still there, folded under their goal behind `f`.

What that page opens is a **watch, not a chat**
(`domain.Feature.Conducted`, `ui.featureRow.watchOnly`). A card inside a
running goal already has a driver: the lead starts it, answers its
questions, raises its envelope and lands it. So the board withholds from
such a card exactly what it withholds from one another gummi process is
driving (`ui.foreignSafeActions`, §14's one-driver rule seen from the
board) — every verb that would write to it, the autopilot key, the
decision block, and the composer — and says which lead owns the run.
Only the driver differs: a pid there, a lead here, and in both cases not
this board. A line typed into a conducted card is refused rather than
delivered, because the one thing it could honestly be — a note for the
conductor — has a home already: the goal's own thread.

A card the goal **dropped** wears a goal id and is nobody's to conduct: it
is closed where it stands and keeps the full inventory, which is what
makes `adopt` reachable on it.

A card the goal **landed** is not released that way, and the card alone
cannot say so — `domain.Feature.Conducted` sees a card at done and lets go
of it. The board supplies the half it is missing (`ui.featureRow.conducted`,
from the row load's reading of the goal's own stage): a landed card stays
the goal's until the **goal** reaches done. Its branch sits inside the
running goal's worktree, so every verb over it — clean-up above all —
would reach into a checkout the conductor is still working beside. When
the goal ends, its cards come back with it.

That is also why a goal's cleanup takes its cards' worktrees and merged
branches with it (`Engine.CleanGoalCards`), in the moment before it
removes its trees: a card of a goal resolves to a manager rooted at that
tree, so afterwards its branch is measured against a trunk that never took
its commits and reads as unlanded for good. A card carrying work no merge
has — dropped, handed off — is left standing, branch and all.

**Delete** (`D`) is the same fact with nothing kept back. Cleanup is
offered work that landed and refuses the rest; delete was asked to destroy
the goal, so it takes every card with it — checkout, branch and record,
landed or not — and then the goal's own tree and branch in *every*
repository it spans, not only its home (`ui.Shell.deleteFeature`,
`worktree.Pool.DeleteGoalTrees`). Cards kept back would be rows pointing
at a goal that no longer exists, holding branches no surface would ever
offer to remove again, in checkouts already deleted underneath them. The
one thing that stops it is an edge it does not own: a card **outside** the
goal depending on one inside refuses the whole delete, before anything is
touched, and names the pair to break.

Every sentence the board writes about such a card names the **goal
branch** it forks from and lands on, never the repository's trunk
(`ui.Shell.baseBranch`): the trunk is where the goal lands, later, once,
under its own message.


### 17.7 Substrates

Some work is only provable on infrastructure: a cluster of machines, a
device farm, a staging account. Such a **substrate** differs from an env
prerequisite (§4.4's `env:`) in every way that matters to scheduling: it is
scarce, slow, stateful and shared. It can be absent or *broken*; it can be
brought up and put back; it expires; and two jobs on it at once are not
slow but **wrong**, in a way that reads as the code's fault.

`substrates:` in the config names them, with the commands that probe,
provision and reset each. They are operator config from outside every
worktree, as env probes are, and for a stronger reason: the commands that
decide whether work is proven must not be editable by the work.
`internal/substrate` answers three questions and knows nothing of goals,
cards or credits:

- **What state is it in** (`Status`): ready, held, absent, broken or
  expired. Expiry outranks a passing probe — machines past their TTL may be
  reclaimed under a job at any moment — and a held substrate is reported
  held *without* being probed, since a health check fired into someone
  else's experiment is its own kind of interference.
- **Who may use it now** (`Acquire`): one holder per substrate, workspace
  wide — another goal, a board card and a person at the TUI contend for the
  same machines. The lease is an advisory lock on an open file, the shape of
  the `.gummi` driver lock: the process that dies holding one lets go of it,
  so a crash can leave a substrate dirty but never unreachable.
- **Can it be made ready** (`Lease.EnsureReady`): a reset, then a
  provision, each at most once and each reported, so that whoever is keeping
  count of what bringing it up cost can charge it. The reset is tried even
  when the probe says absent — an exit status cannot tell "not there" from
  "there and dirty", and the step it might save is the slowest thing gummi
  ever waits for.

A verification plan cites a substrate as `[env: <name>]`, and a verify
kickoff reports its state beside the env probes; one that is held reads as
errored, never absent, because only a clean absence licenses skipping a
live step. A goal's done-when item says the same thing about its **check**
with `substrate:`, and for the same reason one job at a time is the rule:
`runGoalChecks` takes that substrate's lease for the length of the checks
that cite it, so a command in a checkout cannot drive the machines an
experiment run believes it has to itself. A substrate somebody else holds,
or one that is not ready, makes those checks **not run** rather than
failed — decision 21's rule, that a check which cannot run is no opinion
rather than a block. It is a property of the means, not of the statement:
an item proved by an `experiment:` takes its substrate from the
experiment's own definition and may not name one. A goal card blocked at verify (§17.4a) is retried when every
substrate its plan cites probes ready — read at most every two minutes, and
at most twice between one visit from a person and the next, because a card
that keeps blocking against a passing probe is waiting for something the
probe cannot see.

### 17.8 Experiments — the third means of proof

A done-when item is shown to hold by a `check:` (a command in a checkout),
by `judge: true` (read against the diff), or by an **`experiment:`** — an
orchestrated run on a substrate, for a statement that is only observable on
live infrastructure. It is a different noun from a check and is not a long
check: `verify.MaxCheckTimeout` stays thirty minutes because that is right
for a command. What a check cannot carry is not the duration but the
*answer*. "It failed" has two meanings on a substrate — the code is wrong,
or the environment was not ready — they exit the same, and a budgeted
unattended run that cannot tell them apart spends itself chasing the
environment.

So a run (`internal/experiment`) has four outcomes:

| outcome | when | means |
|---|---|---|
| `pass` | every phase passed | the item holds for these heads |
| `fail` | a phase failed, **and failed again in the same phase on a reset substrate** | a verdict on the work |
| `inconclusive` | the substrate could not be made ready; the rig failed its own `control`; a phase exited 75; the runner died; or a failure did not reproduce (`flaky`) | no opinion — decision 21's rule, "a check that cannot run is no opinion rather than a block" |
| `not-run` | the substrate was held, or would expire before the run could end and cannot be renewed | — |

Three generic things make the split: **phase attribution** (bringing the
substrate up and proving the rig against its own reference are the
environment's), an **explicit signal** (exit 75, `EX_TEMPFAIL`, from any
phase), and **reproduce-before-believing**. None of them knows what is being
tested.

- **And evidence can go stale in a way heads cannot show.** A run that
  failed because the substrate was steadily wrong reproduces faithfully —
  reproduce-before-believing catches flakiness, not a rig that is
  consistently broken — so it is recorded as a verdict on the work.
  Repairing the substrate does not move the heads, so the goal would
  never take that run again and would hand over reporting an item unmet
  on evidence nobody believes. Only an owner can know this, so only an
  owner says it: `gummi resume GL-NNN --retake <experiment>` (or `*`)
  marks the conclusive runs stale. They stay where they are — the
  directory is the record, and the substrate time was really spent — and
  stop being evidence about anything, so the goal takes them again.
- **Evidence is about heads.** A run records the commit of every input —
  the goal's trees, and the trunk of any input repository the goal never
  touched — and is evidence only while each is still at that commit
  (`Result.About`). A landing makes it stale; so does the catch-up before a
  goal lands, which therefore sends the goal back to its conductor to make
  the run on what would actually land, rather than landing on proof of
  something else.
- **Assertions.** A run reports `{"id","ok","detail"}` lines, and an item
  may be about some of them (`assertions:`). That is what lets the parts of
  a matrix that can hold early be *seen* to hold early, and it is judged per
  assertion: an item whose assertions held is met by a run that failed
  elsewhere.
- **And so is the run.** A goal is usually about part of a matrix — §17.12
  recommends cutting a programme into a sequence of goals, and then every
  goal but the last is. Judging such a run on everything the experiment
  asserts makes its experiment unpassable by construction, and the goal
  pays for that three times: a reproduce attempt on every run, a lead turn
  for a "failure" whose every item is met, and no negative control at all,
  since that one is triggered by heads that pass. So a run is judged on the
  assertions the goal's items actually cite (`Job.Wanted`): when every one
  of them held, the run passed, `Scoped` records that it was a narrower
  pass than the whole matrix, and the reason names what did not hold and
  that it was outside what the goal agreed to do. An item that names no
  assertions is about the whole run, and one such item makes the run whole
  again.
- **The record is the directory.** `.gummi/evidence/<goal>/<run>/` holds the
  job (with its own copy of the definitions — what a run did is decided when
  it starts), the result as it is written, a log per phase, and whatever
  `collect` gathered. Nothing about a run lives only in a process.
- **The runner holds the lease.** A run is made by a detached
  `gummi __experiment` process, not by the engine. It outlasts the gummi
  that asked for it, and the substrate is held exactly as long as something
  is really using it: a runner that dies lets go (and its run reads
  inconclusive, never failed), and a gummi that dies does not take a running
  experiment's exclusivity down with it. On Linux a phase's command is
  asked to die with its runner (a parent-death signal), but that signal
  reaches the shell and nothing it started, so the run record also names the
  phase's **process group** — and the moment a run is read back and found to
  have lost its runner, that group is killed. Reading the run and killing
  what it left behind are one act because the process that would otherwise
  do the killing is the one that died. A deploy therefore cannot outlive the
  lease that covered it and go on working a substrate the next holder
  believes it has to itself.
- **The judge is not the work's to edit — and that is all it is.**
  Experiments are operator config from outside every worktree, as env
  probes are. That settles the *definitions*; it does not settle the
  scripts they name, and a command that lives inside a managed repository
  is a judge a card can rewrite on its own branch (`gummi doctor`'s
  `judge` check says so). Nor does any of it hide the rig: no backend
  cages an agent's shell, so the work can *read* the experiment's
  commands, the substrate's state and the evidence directory. That has
  been useful — a lead once diagnosed a rig that was lying to it by
  reading the rig — but it must not be mistaken for a guarantee. The rule
  is that the work cannot change what counts as passing, never that it
  cannot see it. A goal may well change the
  harness it is tested on — that is part of the work — but the definition of
  passing is not on any branch it can reach.

In a goal (`goalpolicy.proveFirst`): settled work is **proven before it is
judged**. With every card landed or dropped, an experiment item with no
conclusive run about the current heads gets one (`Run`), and the goal waits
for it — also while someone else holds the substrate. A run that *failed*
goes to the lead once, while turning it into a card still costs only a card;
after that the goal is judged on it. Runs that judge nothing three times
running, or one that fails the rig's control, **stall** the goal: evidence
that cannot be believed is not something more turns can fix, and each
further run would be substrate time spent learning the same thing. The first
run of an experiment for a goal proves the rig (`control`) first; later ones
do not pay for it again.

Two **controls** make a pass worth handing over, and they are the rule a
repaired check is already held to — *it must fail on main* — applied to a
run. The **positive control** (`control:`) proves the rig against its own
reference on a freshly reset substrate; a rig that fails it judges nothing.
The **negative control** is the same experiment on the trunk, made once per
goal when its heads first pass: an experiment that has only ever been seen
to pass has not been seen to be able to fail. It never blocks. What it says
goes into each item's evidence — *also holding on the trunk, so it is not
this goal's work that made it true*, or *not holding on the trunk* — because
a pass is worth exactly what the same run says about the trunk, and for an
item about part of a matrix the first of those is often the honest answer.

The goal's verify makes no run. It reads what the runs say about the branch
as it is, as results beside its commands' — so the verdict floor, the
hand-over and `status --json` treat an item proved by an experiment exactly
as one proved by a command — and the hand-over says, per experiment, how far
the rig could be believed: runs made, how many judged nothing, how many were
failures that did not reproduce, and the substrate minutes they held. A pass
from a rig that wavered reads differently from one that never did.

### 17.9 Finding out early

If only the goal's verify touches the substrate, dozens of cards land on the
strength of unit tests and the first real signal arrives at the end —
precisely what a budgeted unattended run cannot absorb. If every card
verifies live, the substrate is the bottleneck and the lanes mean nothing.
The middle is what a merge queue does, and a goal branch already has the
property that makes it cheap: **every card lands as one commit**.

- **Every card**: its local checks, as always. They falsify compilation.
- **The goal branch, continuously** (`goalpolicy.integrate`): an
  *integration run* whenever cards have landed on heads no conclusive run is
  about and the substrate is idle — one run amortised over every landing
  since the last. An idle scarce resource is pure waste, so that is the
  default cadence; `integrate_every: N` batches when runs are dear. It is
  bought only from what is left above the runs held back for being judged
  (§17.3), and a rig that has stopped being believable is not asked again.
- **A card that asks** (`live: true`): the few cards whose whole point is
  live behaviour prove themselves *before* they land, on the goal's heads
  with their own branch in place of the goal's. A failed proof goes to the
  lead once, as open findings do. One that cannot be afforded, or that the
  rig would not judge, lands anyway with the log saying it landed unproven —
  the goal's own proof still stands between it and the hand-over.
- **The frontier.** A run reports per assertion, so a goal knows everything
  that has *ever* held on its heads (`GoalExperiment.Green`), and that set
  should only grow. Something that never held failing is expected and costs
  nothing. Something that **held and no longer does** is a landing's doing —
  the strongest statement about the code a substrate makes — and the goal
  bisects the landings between the two runs (`goalpolicy.Bisect`, log₂ n
  runs, each about the heads as they were just after one landing), then
  tells the lead which card. Verdicts that contradict each other — held
  after a landing it had failed before — blame nobody: that is a rig to
  distrust, and the lead is told so instead.

Every run deploys from **detached snapshots** of the exact commits it
records, under its run directory, never from a goal tree: a goal tree is
what cards land on, and a landing under a running deploy would make the run
evidence about a commit it only half deployed. The snapshots go when the run
is over; the evidence stays.

One run at a time per goal, and only on a substrate nobody holds. Lanes keep
their meaning — they bound the *agent* work that runs at once, and all of it
still runs in parallel — while live proof is a second, serial queue. After
mutual exclusion that is the honest concurrency figure for a programme like
this: as many cards thinking as the lanes allow, and one experiment.

### 17.10 The notebook — what a goal knows that no card owns

The durable context carrier is a markdown spec on the card's branch. That is
right for a card and wrong for a programme. The design a goal implements,
the allocation scheme its parts must agree on, what was learned about how
the system under it really behaves: every card needs these and none owns
them. Re-deriving them per card is expensive, and worse — two cards that
derive a scheme differently produce parts that do not fit, and each one's
own review and verify pass.

A goal is in no repository (§17.2a), so its knowledge is in none: the
notebook (`internal/notebook`) is a directory beside the goal doc,
`.gummi/goals/GL-NNN/`, never on a branch an upstream would receive. Its
three parts are told apart by **who may write them**:

| part | writer | rule |
|---|---|---|
| `reference/` | the owner (`gummi goal --reference`, or by hand before the gate) | hashed at the plan gate; a document that changes or goes missing afterwards is reported as such wherever it is listed |
| registry (`REGISTRY.md`) | the lead, `notebook_set` | decided constants — names, numbers, schemes, tables. One writer, so two cards cannot disagree about one: neither may decide it. Every entry is a decision for review |
| findings (`FINDINGS.md`) | the lead, `notebook_finding` | what turned out to be true, with its evidence. Append-only: never edited, only superseded, so a spec that cites `F-7` cites something that will always say what it said |

Every session inside the goal — each card's stages, the goal's own, the
lead's turns — is shown the **index**, a line per entry, and reads a body
from disk when its work touches one. The token window stays small however
much a goal comes to know. Cards cite what they stand on by key or
F-number, which makes the dependency mechanical: the moment the lead
replaces a constant or supersedes a finding, the tool's own result names the
unfinished cards whose specs cite it, while the lead is still in the turn
that can send them a note. A card that discovers something writes a
`FINDING:` line in its spec, and a verified card with one goes to the lead
once before it lands — only the lead can put it where the cards after it
will see it.

What the lead may *not* settle is a finding that contradicts a done-when
item or the reference: that changes what "done" means, and is the owner's
(§17.11).

### 17.11 What a plan does not know, and what is not the goal's to decide

The plan gate demands that every done-when item be checkable, served by a
card, and funded. Of work that begins with discovery that demands a list
nobody can honestly write — and gets an invented one, which the budget is
then committed to.

**A deliberate unknown.** A `gummi-cards` row of kind `tbd` is not a card
yet: it has a title, the items it serves, the rows it waits for
(`depends_on`, required — an unknown no research can resolve is not a plan)
and an envelope (required — it is what bounds the unknown). It serves its
items, so "every item is served" stays true without anyone pretending. Its
envelope is a **tranche** the ledger holds exactly as it holds a waiting
card's. When everything it waited for has landed or been dropped the lead is
woken once, creates the cards the findings call for with `card_create` and
`from: "<title>"` — funded from the tranche, not from what is left to give,
and refused beyond it or for items it was not held for — and the tranche is
closed after that turn, returning what it did not use. A goal does not
finish past an open tranche. The critique's `covered` lens asks whether each
unknown is one its research can actually resolve.

**The question that is the owner's.** Autopilot may create work inside a
goal *within* the agreed items (decision 20). That is not enough when a
finding changes what an item should say — the release has no such option;
the design's assumption is wrong. "What a done-when item says is the owner's
and no tool changes it" must not bend: it is the only thing between a
budgeted unattended run and a goal that redefines success until it succeeds.
And `done_when_not_met` is the wrong tool too — it decides, for the owner,
that the goal should fail the item.

So the lead asks (`owner_ask`: the item, what was found, and the amendment
it would propose), and the answer is `NeedBudget`'s shape once more:

- the cards that serve **only** that item are *frozen* — never started,
  landed, raised or shown to the lead — and keep everything they have;
- everything else runs on;
- the board is told at once (`GoalTickResult.NeedsOwner` — the one thing a
  goal otherwise silent until it is ready says first);
- when nothing else can move the goal **stops** (`NeedOwner`; headless:
  `owner_question`, the question status). It does not finish past an open
  question either: a goal that goes on to be judged with the question open
  has answered it by default.

Anything the owner says answers it — accept the proposal, amend the item in
the goal doc themselves, or say it stands — and the lead reads the note on
its next turn. One question at a time.

**A goal that changes its own rig.** A programme's test harness is often
part of the programme. The judge still cannot be edited by the work, and
needs no guard to make it so: an experiment's commands are operator config
and run in the **workspace root** — the trunk's harness — while they deploy
from snapshots of the *goal's* heads (`GUMMI_TREE_<REPO>`). A goal's changes
to the harness are product like any other, reach the substrate as something
deployed, and only become the judge once a person has landed them.

### 17.12 Programmes — a sequence of goals

Goals do not nest, and a programme does not need them to. The plan gate is
the only place a person agrees what done means, and for work that begins
with discovery that cannot be known once, up front — so the honest structure
puts a gate wherever knowledge changes hands. One goal makes the rig
trustworthy; the next finds out what is true; the next builds on it. Each
has its own budget, so a failure costs a phase rather than the programme,
and its own hand-over, which takes a person minutes. A test rig in
particular should be trusted before it judges anything, and it is trusted
when a person has landed it (§17.11).

What that needs is small, and additive:

- **`after: GL-NNN`** in the `gummi-goal` block (or `gummi goal --after`).
  The goal may be *planned* while the one before it runs — that is most of
  the point — and its gate refuses to cross until that one has ended, and
  refuses a predecessor that was handed off rather than landed, because its
  work is not on the trunk this goal's cards would fork from.
- **What was known comes with it** (`Engine.ContinueGoal`): the
  predecessor's reference, its registry, the findings of it that still hold
  (each saying which goal it came from), and its hand-over as a reference
  document — the plan conversation reads what was handed over, not a summary
  of it. What the new goal already decided wins. Substrates and their leases
  are workspace-wide already, so nothing about them needs carrying.
- **`land_order:`** lists a goal's repositories in the order they land. Git
  has no merge that spans repositories, so a landing can stop part way; with
  an agreed order it stops with the dependency in and the dependent out
  rather than the other way round. The hand-over numbers the repositories in
  that order and lists each one's **series** — what the goal branch brings to
  its trunk, oldest first, one commit per landed card, read off the branch —
  because for a repository whose trunk is someone else's the deliverable is
  not a merge here but a series a person upstreams, in that order. gummi
  still never pushes. Turning a goal branch into a §18 stack of per-card
  branches for that purpose is deferred.

## 18. Stacks — slicing one piece of work into several landings

> *Amended by §27 (decision 30): a card with attached repositories cannot join a stack, and a stacked card cannot attach one. A stack chains the branches of one repository.*

A **stack** is an ordered chain of cards in one repository whose branches
fork from one another: the card at the bottom forks from its own base, and
every card above it forks from the branch of the card below. It exists for
the shape of work gummi could not hold before — slice a feature into
several separately reviewable branches, work them all at once, land them
one at a time, take review feedback on the ones below, and never rebase by
hand.

### 18.1 Topology, not scheduling

The one thing to get right, because getting it wrong defeats the feature:

| | dependency (§11.4a) | stack position |
|---|---|---|
| means | "don't start coding until A is done" | "my branch forks from A's branch" |
| shape | a DAG, any number of parents, crosses repos | a line, exactly one predecessor, one repo |
| blocks work? | **yes** — met only at `StageDone` | **never** |
| stored as | `feature_deps` | `features.stack_id` / `stack_pos` |

A card with three same-repo dependencies still has exactly one base,
because the base comes from its declared position and is never inferred
from an edge count. And a position must not gate: a dependency is met only
at done, so a position that implied one would hold every card above the
bottom out of its coding stage until the one below had *landed* — the
serialized waiting the whole feature removes. `internal/stack` is where
this is enforced, and `TestAStackNeverBlocksWork` asserts it structurally.

The only ordering a stack imposes is on **landing**, and git imposes it,
not gummi: a card's branch contains the commits of every card beneath it,
so landing it early would land their work under its message and without
their review. Both land paths refuse it (`ui/merge.go`, `driver.Merge`),
beside the refusal a PR-linked card already gets.

### 18.2 The base a card forks from

Before stacks, `worktree.BaseBranch` was **decorative** — every "main" in
the package was `HEAD` of the manager's repo, which is what made the goal
branch free (§17.2). A per-card base needed that made explicit, so
`Manager.baseRev` is now the single chokepoint for "the revision this card
forks from and lands on", and every `runGit(ctx, m.repo, …, "HEAD")` that
meant *the trunk* goes through it. A `"HEAD"` against a **worktree** path
still means the card's own branch tip — a different fact wearing the same
token, and the distinction is the whole review surface of the change.

Resolution is a callback (`worktree.BaseLookup`, installed at launch from
`Engine.StackBaseFor`) for the reason `forkStore` is one: a stacked card's
base takes the store, and the package must not import it. A nil lookup
leaves every card on the checkout's HEAD, so an unstacked card with no
chosen base behaves exactly as it always did — asserted directly, because
that equivalence is what makes the sweep reviewable.

`Feature.Base` (empty = the checkout's HEAD) is the card's own choice,
offered by the creation dialog's `forks from` row and `--base` on every
card-creating command. A stacked card above the bottom ignores it.

### 18.3 The replay

`BaseFor` skips **landed** members, and that one rule is the whole landing
cascade: when the bottom card lands, the card above it stops forking from a
branch whose commits are in the base anyway and forks from the base
directly, so the replay that follows is a fast-forward rather than a
re-application of work already there.

`Engine.StackTick` mirrors `GoalTick` — read, ask the pure policy,
execute — and the board ticks it without deciding anything itself
(`queueStackTick`, drained in `Update` beside `drainGoalTicks`, plus a
backstop poll; the TUI and `gummi web` alike, since both run the same
board). The headless driver does not tick stacks: a card `gummi run`
moved leaves the cards above it stale until a board is next open on the
workspace, or until `gummi stack restack` walks the stack (`Engine.Restack`,
StackTick to a fixed point) and prints the pushes. `stack.Decide` returns **at most one** restack per
tick on purpose: a replay rewrites what every card above it forks from, so
their staleness is not knowable until it has happened.

The primitive is `Manager.RebaseOnto` — `rebase --onto <base> <oldFork>`
inside the card's worktree, then re-anchor the fork point, with the fork
point read *before* anything moves. `--onto` replaying only the card's own
commits is the property the feature rests on. It targeted `MainHead` while
goals were its only caller (correct there, since a goal card's manager is
rooted at the goal worktree) and now targets `BaseHead`, which is the same
value for a goal card and the card below for a stacked one.

Staleness is `RebasedOnBase` inverted. A replay never races a session: the
policy declines to move a card with a live one, and the executor takes the
card's own lock to close the gap. A conflict stops the walk exactly there —
`RebaseOnto` aborts before returning, so the branch is untouched, the cards
below are already correct, and the TUI offers the agent through the
hand-off `internal/ui/rebase.go` already had.

### 18.4 Setting one up

A stack is **created by the act of stacking**, not by a dialog: `T` on a
card opens a new session's draft with its `from` setting already on that
card, and creating that second card is what brings the stack into being,
named after the card at the bottom. The common case costs one key, and a
stack of one card — which nobody wants — never exists.

The setting is **answerable on its own**, not only by `T`. A branch to fork
from and a card to stack on answer one question — what this branch forks
from — so they are one `from` chip in the draft's strip (§19.8), changed
with `/from` or by stepping through it with `←`/`→`: the
repository's branches, then the cards on the board that have a branch to
fork from, most recently touched first, so the card the reader just left is
close by. Someone who opened a draft with `n` and only then thought of the
card below is not told to leave and start again; `T` remains the one-key
path, and all it does is preset the chip. What keeps it from collapsing
into a dependency picker is what it names: one branch, out of a short,
repo-local list. A stack position is topology and a dependency is
scheduling (§18.1).

The row's candidates are the cards that could actually carry the fork:
no research card (no branch), no goal and no card inside one (a goal's
cards share the goal's branch, §18.5), and only the chosen repository's,
since a stack is one repository. A card already **done** stays on offer —
done is a verified branch, not a landed one, so putting the next slice on
top of one waiting for review is the case stacks exist for. Because the
repository can move after a card has been chosen, the draft re-checks at
send and refuses; the store refuses it too (`ErrStackRepoMismatch`), but
only once the card has been created, as a warning about a stack that did
not happen.

`T` and not `S`: `S` is the severity sort, and one key wearing two meanings
on one surface is the defect this keymap's own comments keep recording.

### 18.5 Deferred

- **Pushing and retargeting a stack.** A single card can now publish
  (§22); a stacked card cannot yet. Doing it right needs the parent's PR
  open or merged, pushes ordered bottom first, and a base retarget on
  every replay, because a parent's squash landing leaves the child's PR
  pointing at a branch that is gone. Until then gummi still prints the
  `git push --force-with-lease` a replayed branch needs and refuses to
  publish a stacked card (§22.8).
- **Stacks inside a goal.** A goal's cards already share one branch; the
  two arrangements answer different questions and are kept apart.
- **Renaming existing branches.** A card minted under the original
  `gummi/<ID>-<slug>` scheme keeps it for life (`Feature.BranchScheme`),
  because its branch already exists in checkouts gummi cannot see.

### 18.6 The branch spelling

A new card's branch is the kind of work and the label: `bug/flaky-login`,
`feat/dark-mode`, `goal/auth-rework`. `Feature.BranchName` is the one
place it is constructed and nothing anywhere parses one back, so the
spelling was free to change — worktrees are found by path, branches by
this derived name, and drifted cards by walking store rows.

The card id is deliberately absent, and that costs something worth naming.
It was doing two jobs in the old spelling: identifying the card, which the
slug already does in the words a reviewer reads, and guaranteeing
uniqueness, which it did for free. Uniqueness is therefore **checked
rather than structural** now — `Store.BranchTaken`, asked at every mint
(`cardmint.Mint`, `Materialize`, `MaterializeBugs`) before a sequence
number is spent, with the batch paths also checking their own proposals
against each other. `worktree.Create`'s already-exists refusal stays as
the backstop for a ref that arrived by some other route, and names both
possible causes.

Two cards may still share a label across kinds (the prefix separates
them) and across repositories (a branch name collides only within one
checkout). The worktree path is still keyed by id
(`.gummi/worktrees/FD-042`), which is what keeps two cards' checkouts
apart regardless of their labels. Research cards are exempt from the whole
question: they never cut a branch.

## 19. Freeform cards — a coding agent that happens to be a card

> *Amended by §27 (decision 30): a session's repository (`Feature.Repo`) is its primary one; it may have attached repositories beside it, each with the same branch name in a worktree of its own.*

Every other kind is work gummi conducts through a workflow. A **freeform
card** (`FF-NNN`, `KindFreeform`) is the one that is not conducted at all:
you talk to an agent in the card's thread, it works in the card's worktree,
and when its diff is what you want, it lands on main. There is no design
stage, no gate, no critique pass, no verify, and no verdict.

It exists because the ceremony a feature earns is not worth paying for
every change a person wants made, and the alternative people actually
reach for is a bare coding agent in a terminal — which gives up the id,
the branch, the worktree boundary, the budget, the diff review surface and
the board along with the ceremony. A freeform card gives up only the
ceremony.

### 19.1 The two landing floors

> **Superseded in part by §24 (decision 27).** There is one floor now:
> with the workflow retired, every card that lands is a session and lands
> on a person's read of its diff. `MayLand` no longer holds a verified
> branch; an open crew, a stack below and unresolved diff comments hold
> a landing (§24.10). What follows describes the two floors as they
> stood.

This is the part that changes a promise, so it is stated plainly rather
than buried in the mechanism. Decision 3 said the workflow is invariant
and nothing lands without a critique and a verify. That remains true of
**every card in the workflow**, and `domain.Feature.MayLand` is now the
one place the whole rule lives:

- A card in the workflow lands on a **verified branch** — it walked the
  graph, its critique passed, its checks ran. Unchanged, and no
  configuration softens it. "Verified" is about a revision, not a card:
  the stamp records the branch tip the pass ran on, and a landing reads
  the tip it would squash (after its own final checkpoint) against it
  (`domain.Feature.MayLandAt`). A commit made after the pass — by hand,
  by a later turn, by that checkpoint — is refused until a verify runs
  on the new tip; the verify stop offers that re-verify in place of the
  landing. A stamp written before the revision was recorded reads the
  same way. A goal is not asked: it re-runs its checks itself when its
  landing's catch-up moves one of its branches.
- A freeform card lands on **a human's read of its diff**. That is the
  whole of what freeform means.

Lifting the precondition out of the two headless landing verbs into one
predicate is deliberate: two floors are a fact about the product, and a
fact like that belongs in one function with both clauses visible, not as a
per-kind `if` copied into every surface that lands a branch.

The freeform card's own floor is its **unresolved diff annotations**, and
it is enforced by exactly the check every other landing crosses
(`Engine.GateBlockers`). So the rule reads: *you land when you have
nothing left to say.* That is not a gate — it is the reader's own comments
holding their own card.

### 19.2 Off the graph, not around it

A freeform card holds `domain.StageOpen` from mint until it closes, and
`StageOpen` has **no edge in the transition table**, in either direction.
It is deliberately absent from `domain.Stages` (that list is the graph, in
order) while `Stage.Valid` still admits it, because "may a card hold this"
and "may a transition name this" are two different questions —
`Stage.InGraph` answers the second.

That single choice is the whole cost to the state machine, because every
graph reader already does the right thing with an edgeless stage:
`CanTransition` refuses every move, `Next` returns nothing (so there is no
advance action and no next-stage chip), and `Terminal` reports true (so
the headless driver reports it unresumable). `workflow.InitialFor` is the
one kind-aware thing about it — where such a card *starts* — and the table
itself stays kind-blind, which is what keeps a feature from ever reaching
a stage with no gate.

`Validate` makes the two halves imply each other: a freeform card is at
`open` or `done`, and nothing else may be at `open` at all. A card in the
workflow parked there could never be moved by anything.

Its ending is therefore not a crossing. `Store.CloseFreeform` writes
`done` without asking the workflow, and refuses every other kind — the
refusal beside the bypass is what keeps a store method that can write
"done" from becoming a way to land a feature without verifying it. The
card's history still records the transition; no gate event is appended,
because nothing gated it.

### 19.3 The session

`engine.FreeformSession` is assembled out of the two non-stage sessions
that already existed: `ConsultSession`'s lifecycle (one per card,
idempotent to open, a backend that idles out after 20 minutes and respawns
carrying its own transcript) and a read-only session's absences (no attention
slot and no scheduling: a human-paced conversation is never rationed against
autonomous stages — no gate, no verdict, no advance).

What it has that neither of them does is that it **writes**:

- the card's **worktree** as its cwd, ensured through the same `locate`
  every stage goes through, so the branch is cut the same way and a
  rewrite of main under it is refused the same way;
- the card's **per-card lock**, held for the whole conversation rather
  than one turn, because between two turns the tree holds uncommitted work
  and the branch holds commits nothing has reviewed;
- the card's **envelope** as a real cap, recomputed on every respawn. It
  is the one floor a freeform card keeps, and running into it is a stop,
  not an ending: raising the envelope respawns and the conversation
  carries on;
- **no commit of its own**. gummi never checkpoints a freeform card —
  not when a turn ends, is interrupted or runs out of envelope, nor when
  the session closes. Every commit on its branch is intentional: the agent
  makes it when asked or when the work is worth keeping, the person makes
  it from the web page's **Commit** action (offered while the worktree is
  dirty, with their own message), and what it
  leaves loose stays in the worktree (which `Remove` refuses to delete
  while dirty, and the diff surface reads against the working tree).

It has **no artifact**: `Kind.ArtifactNoun` and `ArtifactPath` are empty
for it, nothing is seeded at mint, and the thread is the record.

While the session's agent waits on a question (`ask_user`), a prose line in
the composer is that question's answer (§6.3), not a turn: the agent is
blocked inside the call, so a turn could only be refused.

### 19.3a The conversation is the context, so the conversation is persisted

The conversation is also what a freeform card's landing message is drafted
from: with no spec to digest, the scribe gets the card's title and the
person's own turns beside the branch's commits and diffstat, and is told a
person's read of the diff — not a verify — stands behind the landing.

Because the thread is the record, losing it loses the card. A freeform
card's session therefore has a durable row like a stage session's — its
transcript, its spend, its activity, and the backend conversation id it was
keeping — written as the conversation grows (each user turn, each completed
reply, each turn's end) rather than only at shutdown.

`Engine.Restore` rebuilds it as a `FreeformSession`, not as a stage run:
the row's stage is `open`, which `roleForStage` answers nothing for, so
without an explicit branch every restart would silently drop exactly the
thing a person came back for. What comes back is the session and its
transcript, and **nothing else**: no backend is started and no card lock is
taken, because a board that opens on eight freeform cards must not spawn
eight agents and block eight cards for work nobody has touched yet. The
next turn is what spawns one — `ensureBackend` treats a restored session
with no agent exactly as it treats one whose backend idled out.

**What the model gets back is a separate question from what the reader
gets back**, and the answer has two layers because backends differ:

- A backend that can continue its own conversation is asked to, with
  `SessionOpts.ResumeID` (the id the row carried) and a stable per-card
  `ResumePath`. Full fidelity, nothing replayed, nothing paid for twice.
- One that cannot — no `Resume` capability (headless/BYOK), or no recorded
  conversation — is handed the conversation as text instead
  (`freeformReplayHint`), framed as its own earlier work rather than as a
  report of someone else's. Without this the transcript on screen would be
  a record the model does not share, and the first turn after a restart
  would answer as though nothing had been said.

  The replay carries **what was said and nothing else** — the person's
  turns and the agent's replies. Tool lines stay out: a restored row cannot
  tell the backend's own calls from gummi's activity notes, and replayed
  indiscriminately they came out as "you ran: worktree committed", which
  the session did not do and which contradicts the contract telling it
  gummi commits for it. Nothing is lost — what the tools did is in the
  worktree and the diff. It is bounded by a character budget that keeps the
  newest turns, and says so when it drops the rest.

The card's **events are still not mirrored** into the card-event log. That
log is what a thread renders for a stage that is no longer live, and a
freeform card's thread renders its session — mirroring would draw the same
conversation twice, once from each. The cost is that `cardrun` has no
passes to fold for such a card, which is why its stats tab reports the
money and the envelope and not a pass list (§19.6).

**The card lock spans a backend, not the conversation.** A backend is what
drives the card, and while one exists the worktree may hold work that is
not committed yet — which is what a `gummi merge` or a second board must be
excluded from. Between turns there is no backend and the tree is committed,
so there is nothing left to exclude, and a board left open overnight stops
blocking every CLI landing of every freeform card on it.

Its tool surface is `resolve_annotation`, `ask_user`, `memory_read` and
`memory_write` (§19.3c), and, on a backend with no watch tool of its own,
`watch`/`unwatch` — and no spec tools, since there is no document for
them to reach. The review loop needs
`resolve_annotation`; `ask_user` is how any coding-agent session stops for a
decision instead of guessing. `watch` is gummi's stand-in for Claude Code's
Monitor (`agent.Capabilities.NativeWatch`): gummi runs the command in the
worktree and hands its output and exit back to the agent as turns of their
own, batched and spaced because each one is billed. A watch belongs to the
session, not the backend, so it outlives an idle close and its next output
respawns one; it does not survive the board closing.

**A rewind takes back the conversation, never the branch.** Rewinding to
before one of the person's messages drops it and everything after from
the transcript, hands the message back to the composer, and forgets the
backend's own conversation id so the next turn replays what is left
rather than resuming what was cut. The commits and the worktree stay as
they are, and a note in the conversation — replayed to the agent — says
so. Taking the branch back too would be gummi rewriting history the agent
made on purpose, a step this card's other floor (every commit is
intentional) leaves to the person and git. Not mid-turn, and not over an
open question.

### 19.3b Where a session works: the main checkout

The worktree is the default, not the requirement. A freeform card may be
minted **into the main checkout** (`Feature.MainCheckout`) — the `n`
dialog's "runs in" row, the web draft's toggle, `card_new`'s
`main_checkout` — for a session that runs directly in the repository's
managed checkout: no branch is cut, no worktree is created, and its turns
write beside everything else the person has there. It is the person
opting out of the isolation, deliberately and at mint, not a card that
lost its tree.

What such a card does not have, it does not pretend to have. There is no
branch to land, so the landing floor refuses (`landingRefusalIn`) and the
headless `merge` verb refuses with it; the person commits what they keep,
in their own checkout, on their own branch. `CommitFreeform` refuses too —
sweeping the checkout's loose work into a commit is not this card's to
do. Hand-off is the ending that works: it closes the card and keeps the
loose work exactly where it is, committing nothing. What replaces the
diff is the same surface read against the checkout itself: the diff
family anchors at the checkout's `HEAD` (`Manager.diffBase`), so the
loose tracked work is what it describes, and a commit the person makes
mid-session — or a squash-merge another card lands while the session
runs — never rides into it. `Validate` holds the invariants (freeform
only, and no base, no branch, no stack position), and `Manager.Ensure`
refuses to cut the tree the card was minted to do without, whatever
caller reaches it.

### 19.3c Project memory — what a session starts from, and what it fills

A freeform card's durable context is three markdown files under
`.gummi/memory/`, never committed, read by its session at every spawn —
the tier beneath the branch and beside the conversation, and the one
thing a respawned backend starts from beyond whatever replay or resume
§19.3a can hand it. Each spawn inlines what memory holds into the system
hints, capped per file, and the session carries two tools, `memory_read`
and `memory_write`, to read the lot and fill the card's own. The inline
is the read: an empty memory is said in a line rather than left out, and
the hint tells the session not to open with a `memory_read` — one shown
nothing cannot tell empty from unshown, and spends a tool call to learn it.

- **Global memory** (`global.md`) is shared by every freeform session in
  the workspace — the durable facts they should all start with. It is
  **read-only to a session**: `memory_write` refuses it in either form.
  The person fills and restructures it by hand, and a later, separate
  distillation pass will own what goes into it; a session that learned
  something the others should start with records it in its own session
  memory and says so in the thread. The reason is ownership, not caution:
  the tier a workspace curates must not be writable underneath whoever
  curates it.
- **Session memory** (`<card>/memory.md` and `<card>/dead-ends.md`) is the
  card's own — the working plan, kept current, and the dead ends,
  recorded so no later turn pays for a failure twice. A replace goes
  through `atomicfile`, an append through a single `O_APPEND` write, and
  a card's writes serialize on the session — an MCP backend can issue
  two at once. Cards whose memory still sits in the file's former name,
  `plan.md`, are migrated on first touch by a no-replace rename
  (`state.Workspace.LegacyPlanRename`), so existing sessions start from
  what they learned.

None of it is required: no gate reads it, no section is demanded, and no
stage session is ever offered the tools — a stage's durable context
carrier is the spec, and memory is deliberately not that. The files sit
inside the workspace's git exclusions, and the web face displays them
read-only (§20). What distills from session memory into global is
deferred with §19.7's list. What distills into skills is the curator's
(§23): it reads every card's session memory and dead-ends, and writes
the workspace's skill pack, never these files.

### 19.3d Agents — a session with a home of its own

> **Amended by §26 (decision 29).** An agent may run a workflow in its
> home, and a run's `dispatch` step may mint a session in a repository
> the allowed recipe names. "Delegation is refused" and the deferred
> "letting an agent file cards" below are history where §26 applies.

A session may also be minted into **a home of its own** (`Feature.Home`):
the third "runs in" answer, after a worktree and the main checkout. Such a
card is an **agent**. It is in no repository of the person's. Its working
directory is `.gummi/homes/<card>`, which outlives its conversations, and
it is told to keep there what makes it better at its work the next time:
notes, scripts, and skills in the form its own CLI discovers from its
working directory. gummi adds nothing to that discovery (§4.1a) and
manages none of it: there is no skill list, no memory view and no review
of what the agent wrote for itself. A person reads the home in their own
shell — the web face's Terminal tab opens in it.

**The home is a repository, and the agent is its main-checkout card.**
Every seam already knows a card with a checkout and no branch (§19.3b),
so `Pool.ManagerFor` makes the home a git repository on first use (one
empty commit, under an identity of gummi's own) and roots the agent's
manager there. `Validate` demands `MainCheckout` with `Home` and refuses
a repository name. What follows is what a main-checkout card already
does without: no branch, no landing, no `CommitFreeform`, no stack, no
"write a spec" — a feature's branch cannot be cut in a home. Delegation
(§19.10) is refused too: a delegated card forks from its parent's branch
and an agent has none. Work that belongs in a repository is the person's
to start as a card.

**A home is only ever a directory gummi made, and git stays inside it.**
The home sits under the workspace, which is usually a repository itself,
so two things are held on every resolve (`worktree/home.go`). A link, or
a `.git` file naming a repository elsewhere, standing where the home
should be is refused: following it would run the "in no repository"
agent in whatever it points at. And the process, with every child it
starts, carries `GIT_CEILING_DIRECTORIES` naming `.gummi/homes`, so a
home whose `.git` is gone finds no repository rather than the workspace's
— an agent's own `git reset` cannot land on the person's work. A home
missing its `.git` or its first commit is finished on the next resolve,
not trusted. Looking does not make one: the terminal and the file route
answer only for a home a turn has made, and deleting an agent that never
ran leaves nothing behind.

**It is budgeted like any card.** One envelope, the card's own; a
heartbeat (§19.9) or an objective (§19.11) spends it and stops when it is
spent, and raising it is the person's. Nothing about an agent's budget is
recurring.

**The home is never cleaned up.** Handing the agent off closes the card;
`Pool.Remove` leaves the home where it is, because what an agent built is
not a cleanup's to discard. Removing a home is `rm -r` by a person.
Because a home can outlive the record that numbered it, a mint that is
handed a number where a home still stands is refused: an agent never
wakes among another's files without word of it.

**The faces.** The TUI's "runs in" row has the third cell; the web rail
has a New agent button that opens the session draft as an agent's, lists
agents in a group of their own above the status groups (which reorder
with every turn), and gives an agent's page two tabs, Stats and Terminal.
The card keeps a session's `FF-` id; the page tags it `AG`.

Deferred: a files explorer for the home; letting an agent file cards in
the workspace's repositories; confining an agent's writes to its home by
sandbox rather than by instruction; and a rule for when a long-lived
agent's conversation restarts.

### 19.4 The review loop

A freeform card is the **simplest consumer** of the diff-review machinery
that already exists, not a second copy of it: the annotation editor
(`c`/`R`/`x`), content-anchored comments (`internal/diffannot`),
`CompileDiffComments`, `resolve_annotation`, and `diffReviewHints` for a
backend that was not alive when the comments were written.

What collapses is the delivery routing. For a stage, comments have to pick
between a running writer, a critique or rebase pass that must not receive
them, a finished stage to re-run, and a queued one. A freeform card has
one session and it is always the writer, so they go to it as the next turn
— and the loop is **uncapped**, because the corrective-round cap exists to
stop an *unattended* loop from spinning, and here the person is the loop.

### 19.5 What a freeform card may not do

> **Superseded in part by §24 (decision 27).** Adoption and goals are
> retired for every card, so the first two refusals below no longer have
> anything to refuse. The third is reversed: `run`/`resume` now drive
> exactly a session and its crew (§14's note, `docs/HEADLESS.md`). The
> main-checkout stack refusal stands.

- **Adopt a branch.** An adopted card walks the whole graph, "because the
  alternative is the first hole in the quality floor" (decision 22). A
  freeform card walks nothing, so adopting with one would be that hole.
  Mint a feature onto the branch instead.
- **Belong to a goal.** A goal's cards land on its branch one commit each
  and its lead reasons about their stages; a card with no stage is
  invisible to it.
- **Be driven.** `run`/`resume` drive stages. The refusal is explicit
  rather than inherited from `Terminal`, which would otherwise report a
  card that is very much open as done.
- **Stack, when it runs in the main checkout.** A stack position says a
  branch forks from a branch (§18.1), and a main-checkout session holds
  no branch of its own — `Validate` refuses the combination, and the
  creation surfaces never offer the rows together.

### 19.6 The board

The TUI treats a freeform card as a card and withholds the workflow:

- **Minting** is the `n` dialog's last kind row, or `/freeform` — and it
  **starts on create**, whichever button was pressed. There is no gate for
  autopilot to cross and no backlog to wait in, and the description is the
  first turn (`FreeformSession.Kickoff`), so a card that sat waiting for
  the reader to retype it would be waiting for nothing. Its page opens
  with it.
- **The masthead** names the card's branch where every other card's stage
  strip goes. Five stages faint with none of them lit would say the card
  is somewhere in the workflow, which is the one thing that is not true of
  it; the branch is what a reader of that row will actually check out.
  There is no autopilot field either — nothing for a gate-approval mode to
  govern.
- **The thread** is the conversation, drawn in the slot the consult
  exchange uses (exactly one of the two ever renders). With no session on
  screen it still says something: how to start it, or — after a restart —
  where its work is and why the conversation is not here, since a freeform
  session is not persisted.
- **The tabs** are thread, diff and stats. There is no artifact tab,
  because there is no artifact; the chord still answers, saying so.
- **Every line typed is a turn** to the session that can act on the card.
  There is no read-only consult fallback here — the composer's placeholder
  names what this card has (a branch, a diff, a landing) instead of the
  approve/send-back vocabulary it does not.
- **The answers** are the two ends of its review loop and the ending that
  keeps the branch: read the diff, land it, hand off. Everything
  workflow-shaped is withheld from the inventory, the key list and the
  help overlay together — `g`, `s`, `b`, `v`, `A` — so "not shown" and
  "not available" cannot diverge.
- **Landing closes it**, the same way a verified card's landing does, and
  ending the card closes its session: the backend stops and the card lock
  is dropped. A landing squashes whatever is still loose into the commit
  whose message the person approves; a hand-off commits nothing, and the
  loose work stays in the worktree it keeps.

### 19.7 Deferred

A `gummi ff` CLI verb — a freeform card drives like its conversation does
until then. A pass list on its stats tab, which
needs its turns in the card-event log without the thread drawing them
twice (§19.3a). The third ending, discard, which
would delete the branch and worktree the way `clean` does for a landed
card; today a freeform card's branch is cleaned up after it lands, or kept
on purpose after a hand-off, exactly like any other card's. The PR
landing route is withheld rather than wired: what "gummi follows the PR
and waits" means for a card with no gate to wait at is a question nobody
has answered. And project memory (§19.3c) waits for its distillation
pass — the writer that folds what sessions record in their own memory up
into the global tier they all read. Half of that is now designed: the
curator (§23) folds session memory into skills. Global memory stays
deferred until it has a history to revert from (§23.7).

Two things worth building next for their own sake: recording **which
floor a landing crossed**, so "how much of this week landed without
checks" is a number on a screen rather than a feeling; and a
**since-I-last-looked** toggle in the diff tab, since by a freeform
card's fourth cycle most of the diff against base is already read.

### 19.8 Sessions: the model is the session's own

> **Superseded in part by §24 (decision 27).** A session keeps its spec
> rather than handing itself off into a feature card: the "write a spec"
> ending below is retired, and `spec_start` (§24.2) replaces it. A
> session handed off that way before the change still names the card its
> work continued as (`Feature.ContinuedAs`), which lands it. The rest —
> the model as the session's own — holds.

The web face calls a freeform card a **session**, and treats it as the
default kind of work rather than the last row of the new-card form: one
agent, running as itself, in its own worktree, reached from any browser.
The workflow stays exactly what it was for every card in it; a session is
how work starts when nobody has asked for a design yet, and **writing a
spec** is how it enters the graph when somebody does.

**A session picks its own agent and model; a stage never does.** The card
carries `SessionBackend` and `SessionModel`. Both empty is the profile's
implementer, which is what every freeform card ran on before, so no row
needs a migration value. `Feature.Validate` refuses either on a card in
the workflow. "Stages take their agents from the profile" is therefore a
fact the data enforces, not a convention every reader has to remember.

**Any installed agent, not only the ones the profiles name.** A board
starts the backends its profiles route to (§5), so a person picking opencode
on a claude-only workspace would otherwise get the default agent. That
would be a model nobody chose. `engine.Config.StartAgent` starts a backend
the first time a session asks for it, and the engine closes the ones it
started. `sessionAgent` never falls back to the default for a name it
does not know, which is the one way it differs from `agentFor`.

**The list is the agent's own, asked live.** Where a backend can
enumerate what it provides — copilot through its SDK's model list,
opencode through its server's providers endpoint — the picker offers
that catalog, merged
with the ids the workspace's profiles run on it and the pairs sessions on
the board use, and accepts any other id typed in. There is still no
registry gummi keeps: a baked-in list would go stale
the week a provider ships something, and the agent's answer about itself
never does. A backend that cannot enumerate is not faked — its picker
keeps the profile ids and the typed entry — and no backend is ever
*started* just to be asked: the catalog comes from an adapter the board
already runs, or, for opencode, from a transient serve spawned for the
ask and killed as soon as it answers — the engine caches, so that is
one transient serve per cache miss. The ask is cached briefly, since a
picker repeats it, and both
faces read the one merged view (`SessionModelChoices`), so the terminal
and the page cannot disagree about what a session can run on.
`CheckSessionModel` still refuses a pair before it is stored: an unknown
agent, an agent that needs a model and got none, and the ids the claude
CLI would refuse at session start. Refusing it then, rather than at the
next spawn, means a person does not type a message into a session that
cannot answer it.

**Switching mid-session keeps the conversation, not the backend's.**
`SwitchSessionModel` refuses a turn in flight, stores the new pair on the
card and stops the backend between turns, when the worktree is committed.
It also drops the conversation id the old backend kept, so the next turn
respawns on the new model with the conversation replayed
(`freeformReplayHint`) instead of resumed. A conversation id belongs to
the backend and model that kept it; asking another to resume it is either
refused or quietly wrong. The thread records the switch as a system line.

**The draft is the new-session form.** On the web, New session opens no
dialog. The conversation column becomes an empty session. Repository, base
and budget sit in the composer, and the model picker sits beside Send,
where it stays for the life of the session. The first message creates
the card (`POST /api/cards`, kind freeform, no title needed; the title is
its first line) and is its first turn verbatim (`KickoffWith`). The card
itself keeps only that first line, so before this a multi-line opening
was cut to its title.

The terminal does the same (`ui/sessiondraft.go`). `n`, the command
menu's New session and `T` open no dialog. The board tab's pane becomes
an empty session, with the thread's own composer at its foot and one
strip of settings above it, already holding the defaults: repository,
where it works (its own worktree, the main checkout, an agent's home),
what it forks from (a branch, or a card to stack on: one setting), model,
budget and profile. shift+tab moves into the strip. A setting can also
be typed as a slash command (`/repo`, `/from`, `/main`, `/budget 5`, …).
Enter on a message creates the session and starts it. esc puts a draft
with words in it away for the next `n`. A workspace with one named
repository has nothing to choose, so the strip names it without making
it a setting. The draft has no rules of its own: its model is
`cardForm`, which draws nothing and reads no keys, and the web's create
drives the same model, so both faces validate and create through one
path. The new-card dialog that came before the draft is gone.

**Writing a spec continues the work on a branch of the spec's own.** The
session is handed off: its branch kept, and the one floor it has (open
diff comments) checked first. It is refused while the worktree holds
uncommitted work (`Engine.WritespecRefusal`): the spec's branch is cut
from the session's last commit, and nothing here commits for the session,
so that work would be left behind on a branch the spec does not continue.
The person commits it first — the page's **Commit**, `gummi commit`, or
by asking the agent — or discards it. A feature is minted
with the person's turns as its brief, and its branch is cut from the
session's tip (`Manager.CreateFrom`). Its fork point is the merge base
with the card's base, which for such a branch is the session's own fork,
so the feature's diff is the whole of the work. It then moves into plan,
whose architect reads that brief and that branch. It walks the whole graph
from there, like an adopted card (D22).

It does not *adopt* the session's branch. Adoption would make one branch
two cards' ("one branch, one card", `BranchTaken`), and deleting the
closed session force-deletes a branch it cut, which would take the spec's
work with it. A branch of its own is gummi's to rebase, land and clean
like any feature's. The session's branch is left where a hand-off always
leaves one.

The session records the spec it went on as (`Feature.ContinuedAs`), and
that closes the one door a hand-off otherwise leaves open: it may not be
landed after all (`Feature.MayLandAfterAll`). Its work lands through the
spec, on a verified branch; landing the session too would put the same
commits on main a second way, past the floor the person just sent them
to. Its closing block says where the work went and offers no landing.

The brief the dialog opens on is written by the session itself, on a
fresh session of its backend with none of gummi's tools and — wherever
the backend can enforce it, as for a consult — read-only: it runs in the
card's worktree, and a backend's own writing tools must not be in reach
of a turn whose only product is text. That turn is uncached, so a brief
already on the record with nothing said since is handed back rather than
written again. It is asked for labelled sections, not headings, and any
heading a seeded body carries is nested beneath the section it is set in
(`spec.nestHeadings`), so the brief stays inside Problem rather than
opening sections of its own beside the ones the plan gate reads.

**An idle session pins nothing.** A card in the workflow that has
stopped is waiting on a decision, so its thread pins one. A session that
has stopped is waiting for its next turn. Pinning "choose what happens
next" there made every line typed into it an answer to a menu.
`openDecision` therefore pins nothing for an open, idle session.
A turn in flight still pins its stop, and a failure or an empty budget
still asks. The endings (land, hand off, write a spec) are in the
session's menu, and on the web its head carries Land and Write a spec.

**A closed session keeps its conversation.** Its row outlives the card,
and its thread is the record of what was said about the work on its
branch. `FreeformHistory` serves it after `Close` and after a restart
(`Restore` keeps a closed card's row as history rather than skipping it as
stale). A session continued as a spec says so as its last line.

**The picker hides what an agent would refuse.** Each agent in the
catalog carries a pattern for typed ids: claude refuses the foreign
prefixes and dotted versions, and opencode needs `provider/model`.
The page offers a typed id only on agents it fits. The pattern is a
courtesy; `CheckSessionModel` is still the check.

**The terminal picks through the card's menu.** The TUI's freeform card
carries the same "model" row its web menu does: one picker for the agent,
then one for the model — the catalog rows and the workspace's ids, the
running pair marked, and a last row that runs the id nobody offered,
typed straight into the filter. Both tiers are the shared command menu,
and the pick checks and switches through the shared Shell half
(`checkSessionPick`, `switchSessionModel`), so a refusal surfaces as a
notice in the same words the page's toast uses. The catalog ask runs off
the loop, like every read that opens a dialog.

Deferred: the model pick at CREATION for a terminal mint (the form has
no session rows; the web's draft does — a card made in the terminal starts
on its profile's implementer and switches from its page); a terminal and a files
tab, since a shell in the browser is a new surface under §20.5, not a
restyle. The permission dock is no longer deferred on the adapter: a
backend that emits `EventPermission` and takes the ruling by request id
now exists (opencode's server), so the dock's work is its own card.

### 19.9 Schedules and heartbeats

> **Amended by §26 (decision 29).** A third kind, `run`, starts a run on
> an agent instead of sending a turn, and an agent may *propose* a
> schedule (`schedule_propose`). Only the person's enable turns it on;
> "an agent cannot schedule itself" below still holds for enabling,
> editing and raising one.

Freeform sessions only move when a person types. Two time-driven
primitives, sharing one cron engine, put them on a clock:

- A **schedule** mints a NEW freeform card on a cadence and kicks it off
  with a stored prompt — a nightly "triage new issues".
- A **heartbeat** sends a recurring turn into ONE existing session —
  "check CI, keep going" every hour.

Both are store rows (`schedules`), and the board is the clock. The Shell
arms a 15-second poll beside the stack poll; each poll calls
`Engine.ScheduleTick(ctx, now)`, which decides from the pure cron policy
(`internal/schedule` — no clock, no IO of its own) and claims each due
instant in the store by compare-and-set before acting on it. One due
instant produces at most one fire even with a CLI write landing at the
same moment, and nothing fires while no board (TUI or `gummi web`) is
running: missed fires coalesce into one catch-up fire, because
`Advance` returns the next run after *now*, never after the missed
marks.

The cadence is a 5-field cron expression, and **cron is canonical**:
presets (`5m`, `1h`, `@daily`) compile to cron at the boundary and the
compiled form is what is stored, so what a face shows and what fires
agree by construction. Expressions no month can match (`0 0 30 2 *`)
are refused at the store's write boundary — the one place every face
must pass through — so a schedule can never silently never-fire. DST
falls out of a minute-grained walk in the row's zone: a wall time that
does not exist (spring forward) is skipped; one that repeats (fall back)
keeps its cadence — every matched minute of the repeated hour fires at
both of its occurrences — while the repeat of the minute a fire came due
in is skipped, so a schedule set for the hour fires it once, at its
first pass.

**Off by default, always.** A definition is inserted disabled; any edit
to it forces it off again (and clears a pending run-now, so re-enabling
never fires off-cadence); enabling is explicit, computes the first fire
from now, and asks at the TUI and the web page — it is the switch that
starts spending. `run-now` from the CLI is refused on a disabled row;
the one fire a disabled row may take is `Engine.RunSchedule`, a person
at the board forcing it once, which leaves the row disabled with no
cadence.

**The envelope is the brake.** A minted card always carries the
schedule's non-zero envelope; a heartbeat spends its target's, and an
exhausted target pauses the schedule (records `paused-exhausted`,
disables the row, notifies) rather than raising anything. A fire against
a busy target is skipped, not queued (`skipped-busy`, recorded but never
notified) — a turn delivered late would land in a conversation that has
since moved on. Attention slots no longer exist to ration against.

**A failed kickoff never duplicates a card.** A mint whose kickoff fails
records the card as the row's `orphan_card`; the next fire retries that
card's kickoff instead of minting a second beside it — until the card is
closed, deleted (a missing orphan counts as closed), or the refusal
persists, in which case the row keeps naming the one card rather than
piling up. A failure *before* the mint names no orphan, so a good fire's
card never receives a second opening turn. Repeated mints of the same
prompt collide on branch names with nobody present to retitle; like a
goal's cards, a scheduled mint takes the next free variant silently.

A schedule only ever mints freeform cards, never crosses a gate, and
never acts board-wide; an agent cannot schedule itself (no MCP tool —
schedules are an operator surface, reachable from the TUI's `L` view,
`gummi schedule list|add|enable|disable|run-now|rm`, and the web page's
routes, all of which sit behind the same device-token and same-origin
guards as every other mutating route). What a scheduled session
produces waits for a person to land, like any freeform card's work.

*One exception, stated on purpose (§23.6):* the skill curator fires on
the same cron engine, and what it writes reaches sessions without a
person landing it. It is not a schedule row and mints no card. It is
budgeted by its own config, and a person checks it after the fact,
through its digest and revert.

Deferred: a fresh-process `gummi schedule tick` driven by system cron or
launchd — there is no headless freeform driver for it to run yet
(`run`/`resume` refuse freeform, §19.5; `gummi ff` is deferred, §19.7).
Per-fire history beyond the row's one last outcome is a later need.

### 19.10 Delegation: a session that creates workflow cards

> **Superseded by §24 (decision 27).** Delegation is retired and replaced
> by crews: a session that wants pieces of its work done by other agents
> calls `crew_start`, and the crew's nodes fork from and land back on a
> branch of the crew's own (§24.3). There are no workflow cards left to
> delegate to. What follows is history.

A person may let a session hand pieces of its work to workflow cards
(`domain.Delegation`, `engine/delegate.go`). The cards are a goal's cards
without the goal (§17): each forks from the session's branch and lands
back on it as one squash commit (`worktree.Pool.ManagerFor` routes on
`ParentID`), and what each holds of the delegation is the goal ledger's
rule — an unfinished card holds the larger of its envelope and its spend,
a landed one only its spend (`domain.DelegateHeld`, §17.3).

- **Opt-in, and asked.** A session is offered `card_create`, `card_list`
  and `card_land`, and told about them, only once its person has set a
  delegation budget. Tools are fixed when a backend starts, so setting or
  withdrawing the budget stops an idle backend and the next turn resumes
  the conversation with the new tool set. Every `card_create` is put to
  the person first — this one, this one and all that follow, or no — and
  the budget caps the session whichever they answer. Without both guards
  a session that can file cards drifts into filing one for every change.
- **The workflow floor is the card's own.** A created card runs on
  autopilot through plan, implement and verify with its critiques, and
  stops at a verified branch. Nothing about its parent softens that.
- **It lands when the session says so.** The freeform worktree is the
  agent's live checkout, and a squash onto it refuses tracked changes, so
  a verified card waits until the session commits its own work and calls
  `card_land`. A conflict comes back to the session.
- **The parent waits for its cards.** A freeform card may not land while
  a card it created is still open (neither landed nor closed): that card's branch
  forks from a branch that would be gone. Its work reaches main through
  the person's read of the freeform card's diff (§19.1), the same floor
  everything else on that branch crosses.
- **Not in a goal.** A delegated card never also belongs to a goal (two
  ledgers would hold the same credits), and only feature and bug cards
  are delegated: research has no branch to land.

### 19.11 Objectives: a session that keeps going until it is done

A heartbeat (§19.9) brings a session back on a clock. An **objective**
brings it back when its turn ends: a person sets one on a session, and
after each turn an auditor decides whether the work is done. If not,
gummi sends the next turn itself, and it keeps doing so until the work is
met, stuck or out of money. The person reads the result once, at the end,
instead of typing "keep going" after every turn.

**The name.** A goal (§17) already means a card whose work is other
cards. An objective is the single-session counterpart: what a goal doc's
Objective section would be with no goal around it, no lanes and no cards.
It belongs to one freeform card and stays on that card's branch.

**Freeform only.** A workflow card already loops: autopilot drives it to
its next gate, and each stage's critique is its auditor. Setting an
objective on one is refused, by the engine and again by the store's
write (`Store.SetObjective` writes only a freeform card's row).

**The loop.** When a freeform turn ends and its objective is active:

1. **The person goes first.** Lines queued while the turn ran are sent
   (`drainQueue`), and the loop waits for the turn they start. An open
   `ask_user` question holds the loop until it is answered, which makes
   asking the agent's way of saying it is blocked. The loop never answers
   for the person.
2. **The audit.** A fresh, tool-less session of the profile's `auditor`
   role (undeclared, it is the scribe's, and failing that the session's
   own model) is given the objective, the agent's last reply, and what
   gummi can see for itself: commits since the objective was set, the
   diffstat, and whether the tree is dirty. It is not given the
   transcript. It answers with a `VERDICT:` line — `CONTINUE`, `MET` or
   `STUCK` and a one-line note (`domain.ParseAudit`). The audit's cost is charged to the card, and an
   answer that does not parse counts as `STUCK`.
3. **The next turn.** On `CONTINUE`, gummi sends a turn under an actor of
   its own (`WithActor`), the way a heartbeat does: the objective
   restated with the auditor's note. The thread draws that turn as
   gummi's, never as the person's.
4. **The end.** The objective settles once, in one of these states, with
   one notification. The per-turn attention pings are muted while it runs.

   | state | when |
   |---|---|
   | `met` | the auditor says `MET` and the check, if any, passes |
   | `stuck` | three `STUCK` verdicts in a row |
   | `exhausted` | the card's envelope ran out |
   | `capped` | it reached its continuation limit (default 20) |
   | `failed` | the backend errored, or the session was closed |

   `paused` is the one other state. It is the person's own, from pause or
   stop, and resume leaves it.

**`met` can mean a command passed.** An objective may carry a `check:`
command, the shape of a `gummi-done-when` item (§17.1). `MET` then counts
only once that command exits 0 in the worktree, and a failing check is a
`CONTINUE` whose note is the check's tail. The auditor is a cheap model
reading one reply. Where the work has a test that proves it, the test is
the judge and the auditor is not.

**The envelope is the brake.** An objective has no token budget of its
own to keep in step with the card's. It spends the card's envelope, and it
stops at `exhausted` the way a heartbeat pauses (§19.9). Raising the
envelope does not resume it by itself, because spending more is a choice
the person makes. The continuation cap catches a loop that is cheap and
going nowhere, which the envelope would let run for a long time.

**Only a person sets one.** There is no MCP tool for an objective, so an
agent cannot set one for itself. It is the same operator surface as
schedules: `/objective [--check '<cmd>'] <text>` in the composer — a
session command of its own, so both faces' composers take it and offer
it — and `/objective pause|resume|stop|clear`. The card's menu has an
**objective…** row on both faces: the TUI puts `/objective ` in the
composer, and the web page asks for the text in a dialog. The web page's
strip sends pause, resume, stop and clear as that row's text.
Stop interrupts the turn in flight and leaves the objective `paused`.
Setting a new objective replaces the old one, and a session has at most
one. An objective is optional: a session without one is a freeform card
as before, and **clear** removes a paused or settled objective, which
puts the session back to moving only when its person types.

**`met` lands nothing.** The freeform floor (§19.1) does not change. The
card shows as needing the person, with the auditor's last note, and the
person reads the diff and lands it, hands it off or writes a spec.

**It survives a restart.** The objective is a store row next to the
conversation's (§19.3a): its text, check, state, continuation count, stuck
streak and last note. A board that comes back finds an `active` objective
on an idle session and audits once before it sends anything, since the
turn that ended before the restart was never audited.

**What each face shows.** A strip above the composer gives the state, the
last note, the number of turns and what the objective has spent, with
pause and resume. "Auditing…" replaces the working indicator between
turns. The board row carries a mark coloured by state. Both faces read
one view of the row, as they do for every other thread (`threadfold`).

Deferred: "run as objective" on a schedule (§19.9), so that a minted
card starts with an objective instead of a single prompt; and an auditor
that sees more than the last reply, if the last reply turns out to be too
little to judge from.

## 20. The web face — the board in a browser

`gummi web` serves the board to a browser as a page of its own: cards on
the left, the open card's conversation in the middle, and on the right a
column of icons that opens one surface at a time beside it — the spec,
the changes (diff, commits and pull request behind one switch), the run
and, for a session, its memory. No surface is open until one is asked
for: the conversation is what a card is opened to read, and in it what
an agent did between two messages folds to one line once it is done. A
phone answers a decision by its first answer, one button in the thread
and on the bar that follows the card into its documents, with the other
answers behind it. It exists for two situations the
terminal serves badly. The board runs on a machine you are not sitting
at, all day, and a card wants a gate crossed from a phone. And a person
answering a gate wants to read the thing the gate is about *while*
answering it, which a terminal that mounts the spec or the diff over the
thread (§6, `cardtabs.go`) cannot offer.

### 20.1 A second face, not a second model

An earlier attempt hosted the TUI in a pty and streamed it to xterm.js,
on the argument that an HTML board would be "a second implementation of
the card page, permanently one release behind the TUI". The risk is real
and the pty paid for avoiding it: one screen shared by every viewer and
sized to the smallest, nothing linkable or selectable, no reading beside
answering, and no alt chords on a phone keyboard. The web face takes the
other side of that trade and contains the risk with four rules instead.

- **It renders projections and derives nothing.** Every route marshals a
  value the TUI also renders: the board rows, the folded thread, the
  open decisions and their answers, the spec with its checks, the diff
  with its annotations, the card's run (`cardrun`) and the fleet's
  (`fleetrun`). When the page needs a value the TUI does not compute,
  the value moves into the shared read model first.
- **It runs the board's own conductor.** What happens after a stage
  ends — the critique and verify loops, autopilot crossing gates, the
  goal and stack ticks, landing, the needs-you queue, which answers a
  decision offers — lives in the TUI's model, not in the engine. The web
  host does not reimplement it: it runs that same model without a
  screen, and the page acts on it through the same messages a keypress
  sends. Read-side pieces the page needs in structured form (the thread
  fold, the board rows, a decision's answers) move into UI-free packages
  as the page needs them; the conductor itself moves out when there is a
  reason beyond the web face, and not before.
- **There is one answer path.** A decision is answered with a decision
  id, an option id and optional words, and the server runs the function
  the TUI's `enter` runs. Options are regenerated on every read and never
  stored (decision 18). A composer line is classified and routed on the
  server, so a line typed in a browser is a steer, a consult or a command
  exactly when the same line typed in the terminal would be.
- **A line is never an answer it did not ask for, and a landing is read
  before it lands.** A line typed at a stop whose answers take no words
  is sent as a line and read, as the TUI's `enter` reads it — never given
  as the highlighted answer. A landing answer carries the message the
  person approved; one without is handed back the drafted message to read
  (the TUI's landing dialog, and its second `ctrl+s` on an unreviewed
  draft). A chip whose "go" spends credits asks first, as `y` does. A
  confirmation is never the page's to give on its own: the page sends a
  destructive action bare, the server answers a `confirm` question with
  the question its own dialog asks — every word, line breaks kept, the
  commands verify would run listed one to a line — and a token bound to
  that dialog, that card and that text. The yes is that token sent back.
  It answers that one question, once: a question that reads differently
  by the time the yes arrives (a goal that grew a card) is asked again,
  a second question in the same flow is asked on its own, and a question
  about another card a flow reached on the way is never the request's.
  Handing a card to autopilot from its menu loosens control, so it asks
  the same way, in the TUI overlay's words (what autopilot runs, within
  what budget, and that it never lands); stopping autopilot goes at once,
  and "Create & autopilot" was asked on the new-card form itself.
- **A question is not an error.** Where a flow stops to ask — an input
  it needs (`needs`, a landing message to read among them), a
  confirmation (`confirm`), a line that reads as a new card (`newcard`) —
  the server answers `202 Accepted`: understood, nothing done, answer
  this. The body is the same `webapi.Error` shape a refusal carries, and
  the page asks it and sends the request again. Only a refusal is a 4xx:
  the card moved, someone answered first, the agent is busy, the answer
  is not offered, the request is malformed. A question is ordinary
  control flow on every landing, delete and hand-off, and a 409 had every
  browser log each one as a failed load, which no page code can silence.
- **An answer names what it was given against.** Every open decision
  carries the revision it was raised on — the spec commit for a design
  gate, the verify run and branch head for a failure — and the page shows
  it under the question. An answer sends it back and is refused with
  `409` if the card has moved, or if another viewer answered first — and
  so does a composer line or a menu action sent at that stop, so nothing
  meant for one stop runs at another. While a card pins a decision, a
  line or an action that carries no revision at all is refused the same
  way, except the menu entries that answer nothing the decision asks
  (`webapi.DecisionIndependentActions`: dependencies, budget, profile,
  repository, pull-request link, duplicate, delete, clean). A second
  person whose answer lost the race is told who won and what they chose,
  even when the winning answer left the card on its stage ("stop here"
  parks the run and keeps the gate). A running card's diff announces a
  new commit rather than replacing what the reader is looking at.
- **Every landing reads one floor.** The menu's land and "next stage" at
  verify pass the floor the verify stop's own landing answer does
  (`domain.Feature.MayLand`, or the stop's "land anyway" — a person's
  overrule of a verify pass that finished and failed). A card that has
  not finished a verify pass does not land from any door. Nor does the
  menu offer a door that would refuse: on both faces it lists merge and
  "next stage" out of verify only where that floor lets a landing
  through and no unlanded card sits below it in its stack, lists no
  local merge on a card linked to a pull request, and no "next stage"
  into implement while an unmet dependency holds the design gate — the
  decision leads with what the card waits on instead.
- **Drift fails a test.** The read models are golden-tested from the
  same fixtures as the TUI's goldens, and a test asserts that the answers
  served for each fixture's open decision are the answers the TUI draws.
- **What a page shows follows the store, whoever wrote it.** The board's
  rows are a snapshot, so the host re-reads them whenever the store moved
  after they were read — another process's commit (`data_version`) or
  this process's own writes (its connection's change count): a verified
  stamp, a scribe's spend, a goal's done-when results. Row loads are
  numbered so an older one never lands over a newer one, and a reload
  pushes only the cards whose rows moved. What no row holds — a busy
  word, a session's state, a re-entry chip — is compared after every
  message and request and pushed when it moves, whether or not the
  request that started it is still waiting. The cards a page has open are
  also watched for a new branch head or artifact revision, which a commit
  made in the worktree elsewhere, or a spec gummi rewrote, never tells the
  store. Card spend is the store's figure; a live session's own running
  total is shown only while it is ahead, and never added to it. A
  consult's questions and answers are written to the card's log (kind
  `consult`, in no stage's segment), so the web thread draws them where
  they were asked and both faces still have them after a restart.

### 20.2 One process hosts the board, and one board has one host

Live sessions, open asks and per-card locks belong to the process that
drives the cards, so the web server cannot be a separate process reading
the store beside a TUI: it would see no live stream and could answer no
live ask. `gummi web` is therefore a **board host without a terminal**: it
builds the board exactly as the TUI builds it — store, engine, worktree
pool, card locks, hooks — runs the TUI's model with no renderer attached,
and serves the page from that process. The TUI does not
serve the page. This keeps the web face off the list of second drivers
(§16): it acts on the running board the way the TUI's own keys do.

The TUI and the web face are both first-class, and **one board has one
interactive host at a time**. Both take the lock the TUI has always
taken, `.gummi/state/instance.lock`, and the holder now writes who it is
beside it (TUI or web, pid, host, URL, start time), so the second to start
exits naming the holder and how to reach it instead of a bare lock error.

The headless verbs do not take the board lock. `run`, `resume`,
`status` and the rest keep working card by card as they do beside a TUI
today, and a card the host is driving stays protected by its card lock
(decision 11).

### 20.3 Who is at the board

The web face is for one person on several devices, or two or three
people who trust each other. A device is paired with a six-digit code
printed in the terminal running the server and redeemed once for a
long-lived device token (an `HttpOnly; SameSite=Strict` cookie, stored by
gummi only as a hash). Pairing asks for a **name**, and devices paired
under one name are one person. Every paired person may do everything a
person at the TUI may do: answer, cross gates, land, raise envelopes.
Receipts, comments and spec notes carry the name, and the page shows who
else is viewing. There are no accounts; a board that needs roles has
outgrown what a local tool should decide.

Every write route checks the request's `Origin` — scheme, host and port —
against the server on top of the cookie, since a JSON API has many doors
where the pty face had one websocket upgrade; and every request, on every
route, must name one of the server's own hosts, so a page elsewhere cannot
reach a loopback board through a name it points at 127.0.0.1 (a DNS
rebinding, whose requests carry matching `Host` and `Origin`). A browser
asking for a code never replaces one that is live — a code minted for a
named person stays theirs. Wrong guesses are counted per address (an
IPv6 /64 as one) and across every caller: one code's worth from one
address locks that address out for a minute, and ten from anywhere in a
quarter-hour lock out every code a browser asks for, for a quarter-hour;
each lockout doubles the one before it (up to a day), so a guesser
spread over many addresses gets tens of guesses a day, not a thousand.
No lockout holds a code the operator minted — the one printed at start,
or `gummi web pair`'s — which only its own three wrong guesses end, so a
guesser can keep the pairing form busy but never keep the operator from
pairing. Until a browser is paired, the session route says only whether
it is and whether a code is live.

**A new device is let in by one already at the board.** The first
device has nobody to ask and is let in at once, whatever code it used, as
is one paired with the code printed when `gummi web` started — that code
goes to the server's terminal and nowhere else, never to a file under
`.gummi`. Any other pairing — a code from `gummi web pair`, or one a
browser asked for (printed in that same terminal, which an agent that
started the server or can read its pane sees too) — only **waits**: the
browser shows a waiting screen, `GET /api/session` tells it where it
stands, and every other route answers 403 "waiting for approval on a
paired device"; it gets no push and no board event, only a `pairing`
event about itself. Every page at the board shows the request — name,
browser and User-Agent, source address, which code it used, when — with
Approve and Reject; the answer reaches every page and the waiting one at
once over the event stream, is logged and pushed, and is recorded in
`devices.json` with who gave it. A request nobody answers lapses after
ten minutes; three may wait at once and five may be made in ten minutes,
so asking cannot bury the request that matters. `gummi web devices`
shows waiting devices and `gummi web unpair` withdraws one, but no
command approves: a command is exactly what an agent running as the
operator can run, so approval is only from a paired browser. The
running server also honours a device only as it knows it — a row written
into `devices.json` behind its back is ignored and logged, and a waiting
device edited to "approved" there still waits — though what the file
takes away (an unpair) counts at once, and a restart trusts the file
afresh.

Every pairing is announced: a line in the server's terminal naming the
address and where the code came from, a notice on every open page
("new device paired: …", or the request itself for a device that waits),
and a push to every subscribed device, with the `gummi web unpair` that
undoes it; a code minted by `gummi web pair` is announced when it is
minted too, and minting is rate limited like a browser asking. A
device's token is bound to the host and port it paired on and refused
through any other — a browser sends its cookies to every port of a host,
so another server on this machine receives the token, and that must not
make it a key to this board. (It does not stop that server from seeing
the token; only running nothing untrusted on the board's host does.)
`gummi web devices` shows where each device paired and how. Unpairing a
device — from the page or from `gummi web unpair` — drops its push
subscription and closes its open event streams within a second. Landing
and gate-crossing are available on the web because a person is
answering: the workflow refuses them from agents (§16), and the web face
is a human at the board.

### 20.4 Running unattended

An always-on host is the expected deployment. Autopilot keeps moving
cards while nobody is watching and stops only where it always stops: a
gate, a question, an exhausted envelope. There is no board-wide daily
ceiling; each card's envelope is the brake, and today's spend stays
visible in the header. When a card stops for a person, every paired
device that allowed it gets a **Web Push** notification, sent by the host
itself with its own VAPID key — no third-party relay. Tapping it opens
that card with the decision pinned and the tab it is about already
showing. Answers are never given from a notification: an answer should
be read before it is given.

Web Push and the installed page need a secure origin. The host serves
HTTPS when given a certificate (`--tls-cert`, `--tls-key`) and plain HTTP
otherwise, on whatever `--addr` names — loopback by default, but a paired
board may listen on any address. Plain HTTP is a secure origin only on
loopback (`localhost`), so off loopback it serves a board that works but
cannot install or receive a push — and the pairing code and every
device's token cross the network in clear, which the server says loudly
when it starts that way. What is restricted to loopback is
`--no-pairing`, which serves the board to anyone who can reach it: it is
refused on any other listener, the tailnet included, and beside any
`--allow-host` that is not loopback, since that names a proxy in front of
the board through which the unpaired board would be open.

A phone reaches an always-on host over the tailnet, and the host joins it
itself: `--tailscale` embeds a Tailscale node (`tsnet`) with its own name
on the tailnet, beside the loopback listener rather than instead of it. It
needs no `tailscaled` on the host, no port forwarding and no public
listener, and its identity lives in the workspace's web state, so it
keeps its name and login across restarts. `--ts-tls` serves HTTPS on 443
with the tailnet's own certificate, which is the secure origin Web Push
asks for. A device token is bound to the host and port it paired on, so
the same browser opened through the tailnet name after pairing on
loopback pairs again there as a second device — and, pairing with a code
other than the start-up one while the first has the board, waits for the
first to approve it. The node joins only after the host holds the instance lock
(§20.2): a second host is refused before it can bring up a second node on
the same identity, and the lock's record names the tailnet address once
the node is up. A tailnet is a network boundary, not an identity check on
the browser in someone's hand, so pairing applies there as on loopback,
and `--no-pairing` stays loopback-only. `tailscale serve` in front of a
loopback host remains an alternative for a machine that already runs
`tailscaled`, given the tailnet name with `--allow-host`.

### 20.5 Scope guards

The web face is bound by §7 rather than excused from it. It is not a
cloud service: one binary, your machine, state in your repo, listening on
loopback unless told otherwise. It writes to GitHub only on a
person's explicit act and only what §22 allows: the PR tab reads, and
its publish acts run only on a confirm that carries the fingerprint of
the facts the page showed (§22.9). It never runs a
workflow the TUI would not run, and it offers no agent a surface the
workflow (§16) withholds.

That last sentence has a limit, and it is stated here rather than
hidden. `gummi web pair` is the operator at the machine: it reads the
admin token from `.gummi/state/web/server.json` (mode 0600) and asks the
server over loopback for a code. An agent running unconfined as the same
user — which is what a stage session is on a bare host (§4.4) — can read
that file, or simply run `gummi web pair`, and redeem the code. No file
mode, nonce or second token stops that: anything the operator's CLI can
read, a process with the operator's permissions can read too. So a code
is not what lets a device in. Once a browser is paired, the device that
redeems such a code only waits until a person approves it on a paired
browser (§20.3), and nothing but a paired browser can approve — not a
command, not an edit to `devices.json` while the server runs. That stops
an agent pairing itself **silently**: the attempt is a request on every
open page and a push to every subscribed device, and it goes nowhere
unless a person says yes.

What it does not stop is an agent acting as the operator in the places
the operator already is. One that can drive the operator's browser
profile, or read its cookie store, is a paired browser. One that pairs
before any browser has — the first device is let in unasked — or that
unpairs every device so the next pairing is first again, gets the board
(visibly: the operator's own browser is then the one left waiting, or
logged out). One that edits `devices.json` and then kills and restarts
`gummi web` gets what it wrote, since a restart trusts the file. One that
can read wherever the operator sends the server's output reads the
start-up code. The boundary that keeps an agent out of those is the one
§4.4 relies on — a container or sandbox in which the agent cannot reach
the operator's browser, the workspace's `.gummi/state` or the host's
loopback.

**The terminal is the one thing here that is more than the board.**
`gummi web --terminal` adds a Terminal tab to every open session and to
any other card with a worktree: the operator's own shell, started in that
worktree on a pty and drawn in the page (`internal/term`,
`internal/web/routes_term.go`). A session with no tree on disk yet has
it made when the shell opens, exactly as its next turn would make it
(`Engine.WorkDir`); a main-checkout session's shell opens in the checkout
itself. It is not the
pty face §20.1 set aside — the board stays a page, and the shell is one
more pane beside a card's diff — and it is not an agent's surface: no
session can reach it through §16. But it is not bounded by the workflow
either. Everything else a paired device can do is something the TUI would
do; a shell is anything the person running the server can do, outside any
agent sandbox (§4.4). So it is off unless asked for, refused together
with `--no-pairing`, and held to more than a read: the WebSocket upgrade
is a GET, which the same-origin check passes as one, so the route checks
the origin itself, and a socket whose device is unpaired is closed.
Opening a shell is a line in the server's log and a notice on every other
open page. With `--terminal`, the limit stated above costs more: an agent
that becomes a paired browser by any of those routes has a shell as the
operator, not only the board. On a bare host it already had one; behind a
sandbox, `--terminal` is a door out of it for whatever reaches the page.

The shell belongs to the card, not to the socket. It keeps running when
the page goes away and the next socket is replayed the tail of its output
(256 KB), so a phone that slept comes back to the same shell. It ends
when the person exits it or ends it from the tab (which kills everything
in its session, for a shell that no longer answers), when its worktree is
removed, after an hour with no page attached, and with the server; a
board runs four at most. It
does not take the card's lock: typing in a worktree an agent is working
in is what a person with SSH could always do, and verify still judges
what ends up on the branch.

xterm.js is vendored, with no build step, by `scripts/vendor-xterm.sh`.
The page's CSP is unchanged by it: xterm.js writes generated `<style>`
elements and per-cell `style` attributes, both of which `style-src
'self'` drops, so the script rewrites those few call sites to go through
the CSSOM (a constructed stylesheet, `style.cssText`) and fails if a new
release has moved them.

### 20.6 Attachments

A person on the page can attach an image where the terminal cannot offer
one: the new-card form, a spec comment, and the card's composer. The two
kinds of place split on how long the image has to last and what "shows
it to the agent" means there, and each gets a different mechanism rather
than one stretched to cover both.

A **thread turn**'s images are this turn's alone — a steer, a consult
question, a freeform turn — so they ride with it, natively, exactly once:
`agent.Turn` carries them alongside the text, `agent.Capabilities.Images`
says which backends can take them at all (claude, copilot and
opencode; never the line-protocol headless backend), and a turn that
carries images to a session that cannot is refused with
`agent.ErrImagesUnsupported` before anything is sent or recorded —
refusal, not drop, the same rule the rest of this section already lives
by for every other kind of write. copilot is the one backend that can
also know a *model's* support (its SDK's model list), so it refuses a
text-only model the same way. The web contract mirrors this exactly:
`Composer.Images` is true only when the card's live session can take
images right now, and the page's attach control shows only then — an
attach button the backend would refuse is worse than none.

A **card description or a spec note** has to outlive the request that
uploaded it and be there for every later stage, so it is stored, not
streamed: `internal/attachment` is a content-addressed store under the
workspace's gitignored `.gummi/attachments` (sha256 id, sniffed media
type, ≤ 5 MB, at most 8 per write), and the description or note names it
by a markdown image link whose target is the store's own workspace-
relative path — `![name](.gummi/attachments/<id>.<ext>)`. That link is
the whole of the mechanism: the spec is the durable carrier this section
already trusts stages, reviewers and verify to read, so a later stage's
opening turn scans it for that link grammar and delivers the images it
names the same way a thread turn's own images ride — natively when the
backend can take them, and as a line naming the file's absolute path
when it cannot, so a read-tool can still open it. Never an error at
kickoff: the spec already committed to naming the file, and a missing
one is named as missing rather than failing the stage. `markdown.js`
renders exactly that link grammar as an `<img>` pointing at the serve
endpoint, and no other image syntax — an agent's own words, or a spec
line, naming an arbitrary third-party URL must never make the browser
fetch it.

Neither surface ever writes into a card's worktree: the store lives only
under the workspace directory, never touched by a checkout, a commit or
`clean`.

### 20.7 Deferred

A daily ceiling for the whole board, if unattended nights prove expensive.
Holding a line typed mid-turn until the turn ends (open question 3): the
web face does whatever the TUI does, and will change when it does.
Garbage-collecting the attachment store (§20.6): nothing removes a
stored image today, including `clean`.

## 21. The log — a card's own commits, and rewriting them

A card's branch was the one part of its record neither face could show
as it is. The diff shows what the branch changes in total; `gummi
squash` collapses it to one commit; nothing let a person read the
commits themselves, or tidy them short of collapsing all of them. The
log tab — `alt+l` in the terminal, Log beside Diff on the web page,
`gummi log` headlessly — reads them, and rewrites them in the only ways
that leave the card's quality floor where it was.

### 21.1 Content never changes

A rewrite is a plan: the branch as it should read afterwards, oldest
first, as contiguous runs of its current commits, each becoming one
commit with the message given (a lone commit given no message is kept).
Every commit belongs to exactly one run and the runs keep their order,
so a reword and a squash are expressible and a reorder and a drop are
not. That is the whole of the rule, and it is chosen for what it
preserves: the final tree is the tree verify ran on, the diff the
comments anchor to is the same diff (§6.1), and nothing about the
card's standing moves. A drop would change content and would have to
send the card back through verify; a reorder can change every
intermediate tree and can conflict. Neither is worth building until
someone needs one.

`Manager.Rewrite` builds each result commit with `commit-tree` from the
tree of the last commit of its run, on top of the result commit before
it, then moves the branch with `update-ref` against the old tip. So
there is no rebase to stop half way, no hook to run, no checkout, and a
commit made in between is not overwritten; commits before the first
changed run keep their SHAs. It still asserts the tree afterwards, as
`Collapse` does. Author and author date come from the first commit of
each run.

Where git signs commits (`commit.gpgsign`, whether from the switch of
§22.2 or the person's own setup) the commits a rewrite writes are
signed, and the log marks each commit that carries a signature — the
header is read, not verified. A plan may also ask to **sign**: every
commit from the first unsigned one up is then made again with its
message, author and tree as they were, so a branch begun before signing
was switched on ends signed without a rebase. A signed commit with
nothing changing beneath it is kept, so asking twice is a no-op; where
git signs nothing the plan is refused (`ErrNotSigning`) and no surface
offers it (`branchlog.Log.Signable`). It is a rewrite like any other:
new SHAs, the pushed acknowledgement, and the committer becomes whoever
runs gummi.

The plan carries the tip it was made against, and a branch that has
moved since refuses it (`ErrPlanMismatch`) rather than being rewritten
from a view that is no longer true. Every surface dry-runs the plan
first (`PlanRewrite`: what the branch would read like, how many commits
change, whether any was pushed) and applies it only on a second act.

### 21.2 Who may, and when

The rule is `branchlog.Refusal`, one function all three surfaces call:

| the card | its log |
|---|---|
| research, todo, goal | no branch of its own to show |
| adopted (D22) | read-only: gummi does not record where its own commits on somebody else's branch begin |
| landed | read-only: the commits are already on the base |
| an agent holds it | read-only until the session stops — a rewrite would pull commits out from under the next checkpoint |
| otherwise | rewritable |

Under that, `Rewrite` refuses a dirty worktree and a rebase in flight
exactly as `Collapse` does, and the whole thing runs under the card's
lock, so a headless drive of the same card excludes it. A message
carrying agent attribution is refused (`MatchesAttribution`, the same
match the landing message is scrubbed for), and a commit that already
carries one is flagged in the log.

Commits the branch's upstream already has are marked **pushed**. A plan
that replaces one is refused until the person acknowledges it, and a
rewrite that did ends with the `git push --force-with-lease` it needs
(`engine.PushCommandFor`, the line a replayed stack card prints). The
rewrite itself never pushes; publishing it is the separate act §22.5
describes, with a lease pinned to the tip that was shown. A card with an
open PR is refused a rewrite until the person acknowledges that the PR
will need the force push.

Rewriting is refused while an agent holds the card, for the reason
`squash` is: what a card's history says is the person's to decide. An outside
agent can call `gummi rewrite`, and the skill tells it to ask first.

### 21.3 One read model

`branchlog` folds `Manager.Log` into rows — subject, body, author,
numstat, whether gummi made the commit itself as a checkpoint, whether
it was pushed, what attribution it carries — and answers whether the
card may be rewritten. The TUI's tab, the web page's tab (`webapi.Log`,
`GET /api/cards/{id}/log`, `…/log/{sha}` for one commit's patch,
`POST …/log/plan` and `…/log/rewrite`) and `gummi log --json` all read
it, per §20.1: neither face decides who may rewrite.

### 21.4 Deferred

- **Reorder and drop**, for the reasons in §21.1. Drop would need to
  re-enter verify; reorder would need the conflict hand-off
  `internal/ui/rebase.go` already offers.
- **Splitting a commit**, which is interactive by nature.
- **Comments on a commit** rather than on the branch's diff. Diff
  comments survive a rewrite because they anchor to the diff, not to a
  commit; comments on a commit would not.
- **Adopted branches.** Recording the tip at adoption would let the
  commits gummi added be rewritten while the inherited ones stay
  locked.

## 22. Publishing — pushing a card's branch and opening its PR

> *Amended by §27.7 (decision 30): a session that spans repositories publishes one branch and one PR per repository, each PR's body naming the others.*

gummi's job used to end at a verified branch, and the last step — push
it, open the PR, keep the PR current — was a command it printed. That
step is where a person's attention most often leaves the board, and it
is also where the card's state (is this the PR it lands through? is it
ready? did the branch move?) is easiest to lose. This section brings it
inside, under decision 25, without bringing a credential, an agent or a
new route around the landing floors with it.

### 22.1 What it is

Five acts on one card, each a human's, each the same code on every
face that offers it (the CLI offers all five; the TUI and the web page
leave out `update`, whose push is their "push" and whose edit of the
words is GitHub's own page):

| act | what it does | verb |
|---|---|---|
| push | publishes the card's branch to its remote | `gummi push <id>` |
| open | pushes if needed, then `gh pr create`, then links the PR | `gummi pr create <id>` |
| update | `gh pr edit` of title/body, and a push for new commits | `gummi pr update <id>` |
| ready | `gh pr ready` of a draft | `gummi pr ready <id>` |
| draft | `gh pr ready --undo` | `gummi pr draft <id>` |

It is not merge, not review, not retarget, and not CI: gummi reads a
PR's checks (§22.11) and never runs, re-runs or waits on one.

Every act is two steps, on every face. First the **facts** are resolved
and shown: the branch and its tip, the remote and the URL git will
really push to, how the push will go (new, fast-forward, a pinned
lease), the head and base repositories, the `gh` binary, a pre-push hook
if one would run, the linked PR as GitHub has it, and the exact commands.
Then the person confirms, and the confirm carries the facts'
**fingerprint**. The act resolves the facts again under the card's lock
and runs only if they digest the same (`facts-changed` otherwise), so
what runs is what was read: a branch that moved, a remote or `insteadOf`
rule edited, a hook dropped into `.git/hooks`, or a PR that changed
state in between each refuse rather than publish unseen.

### 22.2 Optional: detected, and the credential may be lent

Publishing is a capability of the person's machine, not a switch of
gummi's. A person who wants it installs `gh` and either sets the
credentials up on the machine — `gh auth login`, and a push credential
for the remote (an SSH key, an agent, a credential helper — whatever git
already uses there) — or stores them in the web page's settings (below).
There is no `publish` block and nothing to turn on: gummi looks, and acts
on what it finds.

Detection is two reads:

1. Once, when a board starts (`publish.Detect`): `gh` is on the path
   (`GUMMI_GH_CMD` honoured) and `gh auth status` is signed in to
   github.com. Until that passes the publish acts are not drawn at all.
2. Each time a person asks for an act (`publish.Resolve`): the push
   remote names a github.com repository, `gh repo view --json
   viewerPermission` says the signed-in user may write to it, and the
   URL git would really push to is that repository's (an `insteadOf`
   rule that sends it elsewhere is refused, not shown).

Whether the push credential itself works is found out by the push: a
failure there is typed (`auth-failed`, `credential-needs-interaction`)
and nothing after it runs. A missing capability is never an error on
the board, never a warning on a card, and never a reason a card cannot
land the way it always could (§7); where a person asks for an act by
name (`gummi push`) the refusal says what is missing and what would fix
it.

gummi does not read `GH_TOKEN` itself and does not open `~/.ssh`. A
passphrase-protected key works only through an agent that already holds
it; gummi never prompts for or stores a passphrase.

**Stored credentials.** A board served to a browser is often on a host
its person has no shell open on, so the web page's settings take two
secrets (`PUT /api/settings/credentials`, `internal/credentials`): a
GitHub token and an SSH private key. Both are optional and independent,
and a workspace with neither behaves exactly as above.

- They are kept under the workspace's state directory
  (`.gummi/state/credentials/`, 0600 in a 0700 directory), beside the
  paired-device tokens and for the same reason. They are write-only from
  the page: an answer names a token by its last four characters and a
  key by its type, fingerprint and public half, never more. A key with a
  passphrase is refused at the door.
- A key may be **generated on the host** instead of pasted (ed25519).
  Its private half then never crosses the network at all; the page shows
  the public line to add to GitHub. This is the way to prefer wherever
  the page is not served over TLS.
- **The token** is set as `GH_TOKEN` on the `gh` commands gummi itself
  runs — publishing, PR linking and review threads, issue import — in
  place of any token the environment carried. Detection runs again when
  it changes, since it is what makes `gh` signed in.
- **The key** is never handed to a command as a file or a variable.
  For a git command that reaches the remote (`push`, `ls-remote`) and
  only for as long as it runs, gummi answers as an ssh-agent on a
  socket in a private temporary directory and points `SSH_AUTH_SOCK` at
  it. That agent lists and signs with the one key and refuses to be
  added to, emptied or locked.
- **The key may sign commits**, behind a switch beside it that is off
  until a person turns it on (`signCommits`; refused with no key held,
  and forgotten with the key). git signs through a program it runs as
  `ssh-keygen -Y sign`, and gummi names *itself* as that program
  (`gpg.format=ssh`, `gpg.ssh.program`, `user.signingkey`,
  `commit.gpgsign`), so the key is still read by gummi alone. That
  configuration is written to no repository: it rides git's own
  `GIT_CONFIG_COUNT` variables in gummi's environment, after any the
  environment already carried, and so reaches every process gummi
  starts — its own checkpoints, landings, rebases and rewrites, the
  commits a card's agent makes, and a person's terminal tab. Nothing
  outside gummi is touched: a commit made in the main checkout from a
  shell of your own is signed as the machine is set up to, or not at
  all. The signer answers in git's namespace only, so it puts the
  key's name to git objects and to nothing else. A session already
  running keeps the environment it started with: switched on, it signs
  from its next start; switched off, the signer refuses it — off means
  off — and its commits fail, saying so, until it starts again. For GitHub to mark the commits verified the
  public half must be added there as a *signing* key, and the git
  identity must be an email verified on that account.
- Neither is put in an agent backend's environment. With signing on, what
  that environment carries is the instruction to call gummi and the path
  of the key's public half — which is beside the key — and with it the
  means to make a signed commit, which is the point of the switch.
  Otherwise nothing an agent is given names where they are. That is not confinement: they
  are files of the account gummi runs as, so an unconfined agent can
  read them exactly as it can read `~/.ssh` (§22.7).
- What is stored is part of the facts' fingerprint (§22.1): a credential
  swapped between the confirm and the act refuses the act.
- The token does not authenticate a push. A remote pushed to over HTTPS
  still needs a credential helper on the machine; the stored key serves
  an SSH remote, whose host key must already be known there.
- **The git identity** sits beside them in settings and is not a secret:
  `PUT /api/settings/identity` writes `user.name` and `user.email` to the
  local configuration of each repository the workspace manages, which
  every card's worktree shares. It is who an agent's commits and a
  landing are written as. gummi never writes the global configuration;
  clearing both removes the repository's own setting and leaves the
  machine's.
- **Co-authors** sit there too, and in the TUI's settings dialog:
  `PUT /api/settings/coauthors` writes the workspace config's `coauthors` list, each entry `Name <email>`. Every
  commit made in the workspace from then on ends with a
  `Co-authored-by:` trailer per entry — gummi appends them to the
  commits it makes, and a `commit-msg` hook adds them to every other
  commit made under it (`worktree/coauthorhook.go`). git has no setting
  that adds a trailer, and asking a session to add one sets gummi against
  whatever the repository's instructions say about trailers; so the hook
  lives under `.gummi/state/hooks` and is named to git as
  `core.hooksPath` in gummi's own environment (`internal/gitenv`, the
  seam signing uses), which every child inherits. Nothing is written to
  a repository. Naming a hooks directory hides the repository's own, so
  every hook there runs the repository's of the same name first. It reads
  the list at commit time, names co-authors only in checkouts under the
  workspace root — a repository a test builds elsewhere is not the
  workspace's — and `--no-verify` skips it. Empty, the default, means no
  trailer and no hooks directory named. The attribution
  scan (`worktree.MatchesAttribution`) lets exactly the configured lines
  through: a person named them, where any other `Co-authored-by:` is an
  agent signing its own work and is refused as before.
- A paired browser may store or replace them, which makes pairing
  (§20.3) the strength of this too. Off loopback without TLS the key
  crosses the network in the clear; serve the page over HTTPS
  (`--tls-cert`, `--ts-tls`) before pasting one there.

### 22.3 Who may publish

One predicate, `publish.Refusal`, called by every face — deliberately not
`branchlog.Refusal`, which answers whether history may be *rewritten* and
refuses adopted branches that may still be pushed:

| the card | publish |
|---|---|
| research, todo, goal, a main-checkout card | refused: no branch of its own to publish |
| a card inside a goal | refused: it lands on the goal's branch, and publishing it would go around the goal |
| nothing ahead of its base, or a branch set to push onto its base or under another name than its own | refused: nothing to publish, or not this card's branch |
| a card in a stack | refused for now (§22.8) |
| landed | refused: the commits are already on the base |
| an agent holds it, or the tree is dirty, or a rebase is in flight | refused until it is not; the session that holds it is not refused for holding it when a person has allowed it to publish (§22.14), a workflow run in its tree still is |
| a freeform card that was continued as another | refused: the work would go out twice |
| adopted (D22) | push and open allowed; adds commits, never rewrites; refused when the branch came from a fork, which gummi re-asks `gh` about at the moment of publishing rather than trusting a flag saved at adoption |
| otherwise | publishable |

### 22.4 One seam, typed failures

Publishing is `worktree.Manager` plus a `publish` package in the shape
`branchlog` already has: pure rules (`Refusal`, `Floor`, `PlanFor`) and
an `Env` that runs the git and `gh` reads and writes (`Resolve`, `Do`).
The faces call it; none decides. It runs under the card's lock, with a
timeout, with prompts disabled (`GIT_TERMINAL_PROMPT=0`,
`GH_PROMPT_DISABLED=1`, stdin closed), with `GH_REPO`, `GH_HOST`,
`GIT_ASKPASS` and `SSH_ASKPASS` dropped from its environment, with what
the person stored lent to the command that needs it (§22.2), and with
the resolved `gh` path shown to the person, because `GUMMI_GH_CMD` can replace the binary
that carries the credential.

Where a push goes is one function, `worktree.Manager.PushTarget`
(`branch.<n>.pushRemote`, `remote.pushDefault`, the tracked upstream,
else `origin` under the branch's own name). The log's **pushed** mark,
the printed push command and publishing all read it, so they cannot
disagree about where a branch lives.

Where a PR opens is one function too, `resolveBase`. A linked PR has
settled it, and a repository that is no fork has one answer. A fork has
two — itself and its parent — and gummi does not guess between them: the
answer is the one the person gives in the confirm (`--repo`, the TUI's
`ctrl+t`, the page's **Opens in**), else the one recorded for the
repository, else none, and a create with none is refused
(`base-unchosen`) with both named. The record is `gh`'s own,
`remote.<name>.gh-resolved` (what `gh repo set-default` writes), so the
question is asked once per repository and `gh` run by hand agrees with
the answer; it is written only once a PR has opened there. Which remotes
the repository has says nothing: a rule that read them moved a fork's
PRs to its parent, where the token could not open one, the day somebody
added `upstream` to fetch from. An open PR for the head is looked for in
both repositories whichever is chosen.

Failures are typed (`publish.Code`), the same word on every face and in
the CLI's output: not signed in, no write access, non-fast-forward, a
stale lease, a protected branch, a hook refusing, a PR already open for
the head, the branch moved, the facts changed, a token GitHub will not
let open a PR in that repository (`token-refused`, naming the repository
and the other target).

### 22.5 What a push does

- The branch is pushed as the tip the person was shown, by SHA, to a
  fully qualified ref (`<sha>:refs/heads/<branch>`). If the branch moved
  between the confirm and the push the act refuses (`branch-moved`); a
  commit made since is never published unseen.
- A branch that tracks nothing yet gets the push target recorded as its
  upstream afterwards, so the log's **pushed** mark (§21) and git's own
  tools see where it lives. An existing upstream (a triangular
  workflow's `upstream/main`) is never overwritten.
- A fast-forward is a plain push. A branch rewritten after it was
  pushed goes out with `--force-with-lease=<ref>:<sha>`, the sha being
  the remote tip the person was shown — and only when that tip is one
  this very branch once had (its reflog holds it). Having fetched a tip
  earns nothing: a remote branch holding commits this branch never had
  is never overwritten (`name-taken`), and an adopted branch is never
  forced.
- A remote with several push URLs, or one that fetches from one
  repository and pushes to another, is refused: git would push
  somewhere the person was not shown.
- The hooks a push runs (`pre-push`, `reference-transaction`) run with
  the person's credential, so the confirm names them and their content
  is part of the fingerprint. The TUI and the web page also ask for
  them to be acknowledged; the CLI prints them above its prompt.
- Arguments are never positional where they could be an option: titles
  as `--title=<v>`, bodies on stdin, the branch name checked with
  `git check-ref-format`, and `--repo` and `--head` always explicit.
- Opening links. The PR created is recorded through `SetPullRequest`
  before the act reports done; if the link fails the act says the PR
  exists and how to link it, rather than reporting a failure. After
  every push the link's head SHA follows what was pushed.
- The act is recorded in the card's thread (`state.EventPublish`): who
  pushed what where, and what happened to the PR. An act that fails
  part way (pushed, then `gh` refused) says what already happened and
  records that part.

### 22.6 The words

The PR's title and body start from the card — its title and the
branch's commit subjects — as a template, shown on every face before
the confirm and never sent unread (the web page edits both; the TUI
edits the title; the CLI takes `--title` and `--body-file`). Nothing
imported (issue and review-comment bodies) is placed in
it. An agent-drafted description is deferred (§22.10).

### 22.7 What this does not claim

It does not claim an agent cannot push. A card's backend inherits the
person's environment and its shell is unconfined (§4.4), so an agent that
runs `git push` or `gh pr create` itself can: that was true before this
section and is not made better or worse by it. The claim is narrower and
checkable: *gummi's own code* never publishes except through the acts in
§22.1, and never without a person's confirm of the resolved facts — or,
for a session, the person's standing allowance (§22.14).

- The `publish` package is imported by the person-facing faces and by one
  agent-facing adapter, `internal/agentpublish`, which acts only under a
  person's per-session allowance;
  `TestOnlyPersonFacingCodeImportsPublish` names the engine, the driver,
  the MCP shim, goals, schedules and the rest that must not, and
  `TestAgentAdapterIsTheOnlyEngineSidePublishImporter` names the one
  adapter and the one package (the engine) that may import it.
- The CLI verbs refuse inside a session gummi spawned (`agent-session`:
  every backend gummi starts carries a marker in its environment), ask
  at the terminal otherwise, and without a terminal run only with
  `--yes=<fingerprint>` of facts already printed. They are left out of
  the `gummi skill` bundle an agent is handed.
- There is no `/push` in the composer, no publish option in an agent's
  question, no publish step in autopilot, goals or schedules, and no
  action on a push notification.

Scrubbing `SSH_AUTH_SOCK` and `GH_TOKEN` from an agent's environment is
a separate, opt-in confinement decision and is not made here.

### 22.8 Floors, stacks, and rewrite

- **Ready is floored; push and draft are not** (`publish.Floor`). A PR
  may be offered as ready only where the branch could land here: a
  workflow card verified at exactly this tip with no open spec thread
  or diff comment holding its gate, a freeform card with no unresolved
  comment on its diff (its floor is the person's read, §19.1).
  Below the floor a new PR opens as a draft and the choice is not the
  person's to untick; `ready` is refused, and also refused when GitHub's
  head is not the verified tip.
- **A ready PR never quietly gains unverified commits.** A push of an
  unverified tip to a ready PR is offered only as "push and return to
  draft": the PR goes back to draft first, then the commits arrive. A
  push that would add to an open PR the card does not link is refused
  until it is linked, since no floor could be asked of it.
- **A card is published or landed locally, never both.** Linking a PR is
  what already refuses local landing; opening one is the act that picks.
  Unlinking is how a person changes their mind, and a PR that GitHub
  reports merged or closed refuses every act with exactly that advice.
- **Stacks are refused for now.** Their PRs need the parent's PR open or
  merged, bottom-first order and a retarget on every replay (§18.5).

### 22.9 Faces

One seam, three faces:

1. **CLI**: `gummi push` and `gummi pr create|update|ready|draft` print
   the facts and the commands, then ask (§22.7).
2. **TUI**: "open pull request…", "push to GitHub…", "mark PR ready…"
   and "PR back to draft…" in the card's actions menu, shown only where
   detection passed and `Refusal` lets the card through. The confirm
   overlay leads with one sentence of what will happen; `tab` unfolds
   the facts and commands.
3. **Web**: the same entries in the card's menu, an **Open PR…** button
   in the head, and a strip in the PR tab offering the act that fits the
   card now. The page decides nothing: it shows the facts
   `GET /api/cards/{id}/publish?act=` resolved and posts the confirm with
   their fingerprint. A paired browser is therefore not a push
   credential by itself — a request that names no facts, or stale ones,
   runs nothing — but it is as strong as pairing is (§20.3), no
   stronger.

**No face waits in silence.** Reading the facts and running the act are
both GitHub's to answer, and each call may take up to `publish.Timeout`.
The plan carries its steps in a person's words (`Plan.Steps`: the
re-read of the facts, the push, each `gh` call) and `Do` reports each
one to `Env.Progress` as it starts and as it ends, so the faces draw the
same progress from the same list: the CLI prints each step to stderr as
it starts; the TUI's status bar spins on the card with the step and its
place in the plan; the web dialog is up before its facts are, and once
confirmed turns the steps into a checklist with the buttons held off,
the failed step marked where it stopped. The board says what it is
waiting on as `work` changes (`webapi.ChangeWork`), which also carry the
PR reads of §22.11, the thread pull and a link's lookup, so a page shows
a wait whichever
face started it. They are a display: nothing is stored and nothing is
decided from them.

### 22.10 Deferred

- **Stacked cards** (§18.5).
- **An agent-drafted PR description**, through the same bounded scribe
  pass the landing message uses.
- **Publish state on the board itself** — commits not yet on GitHub
  beside a row's PR badge, and a publish choice in the done gate. Both
  need a per-card remote read the board does not make today.
- **`gummi doctor` checks** for `gh` and push access.
- **GitHub Enterprise**, which `PullRequestRef.Validate`'s `github.com`
  requirement excludes.
- **Merging, retargeting on request, resolving threads** — still not
  gummi's.
- **Branch switching.** A card's worktree is its branch. Staging,
  committing by hand and stash were deferred here too, for fear a
  hand-staged index would fight the agent's checkpoints; §22.13 brings
  them in, offered only between an agent's turns.
- **Fetching and conflict surfacing** beyond what `ForkDriftError` and
  the rebase dialog already do, until publishing shows the need.

### 22.11 Failing checks — read by gummi, sent by a person

A PR's review threads reach a card as diff comments. Its checks had no
way in at all, and a session cannot be told to look for itself: the
token a person stored is never in an agent's environment (§22.2), so
`gh run view` inside a session works on one machine and not the next.
So the read is gummi's, with the same `gh` and the same lent token as
every other read here, and it is the same on a freeform card and a
workflow card.

- **The read** (`internal/pr/checks.go`). `FetchChecks` asks `gh pr view
  --json headRefOid,statusCheckRollup` and sorts each entry into `gh`'s
  own buckets (pass, fail, pending, skipping, cancel); only what GitHub
  itself called failed is failing. `FetchFailedLogs` adds, for a failing
  GitHub Actions job, the end of what its failed steps printed (`gh run
  view --job --log-failed`), bounded per line, per job and in the number
  of jobs. Another CI's status carries its name, description and link.
- **Shown** in the web page's PR tab and in `gummi pr status`, without
  logs.
- **Sent** by one card action, "send failing checks" (`prchecks`), which
  only a person starts. A freeform card's session gets them as its next
  turn. A workflow card's writer is its implement stage: a running one
  gets a live turn, an idle one is run again with the checks in its
  kickoff, and a card at verify goes back over its rerun edge first —
  the route "request changes" takes. A critique, a rebase or a verify
  in flight is not interrupted.
- **A snapshot, not state.** Nothing is stored, no gate is held and no
  floor reads it: a check's answer belongs to a commit and is stale the
  moment the branch is pushed again. The message names the commit the
  checks ran on, and says so when the card's branch is no longer there.
- **A log is somebody else's text.** It is stripped of escape sequences
  and control characters, bounded, and handed over as quoted evidence —
  the same class of input as a review comment's body.
- **Offered to a session a person allowed to publish** as `pr_checks`
  (§22.14): the same read, the same message (`pr.FailingReport`, with
  the push line for a session that pushes its own fix), the same
  quoting of logs. There is still no autopilot, goal or schedule step
  for it. A tool for "more of this job's log" is the follow-up if the
  bounded end proves too short.
- **Not followed.** gummi does not poll a PR's checks or raise attention
  when they turn red; that needs the per-card remote read §22.10 defers.
  A session watches by calling `pr_checks` (§22.14).

### 22.12 Closing a card by push

Some remotes take a push to the base directly — a team forge with no
review step, a mirror, a personal repository — and a PR there is a
detour. A remote may therefore be set to **close cards by push**: the
card lands locally exactly as it always has (§7, the floors unmoved:
`domain.Feature.MayLand`), and then the base is pushed to that remote.

- The choice is a remote's, kept in the repository's own config as
  `remote.<name>.gummi-close = push` beside `gh-resolved`, so it travels
  with the clone and `git config` agrees with the board. Unset is `pr`,
  today's behaviour. At most one remote of a repository closes by push:
  setting one sets the others back (`worktree.Manager.SetCloseMode`).
- The base push is a person's act with the shape of §22.1: the facts
  (`ResolveBasePush`: the base and its tip, the remote's tip read with
  `ls-remote`, the push URL, a pre-push hook and its content, the card
  branch to delete there) are shown with the commands, and the confirm
  carries their fingerprint; `PushBase` reads them again and runs only
  if they digest the same. It pushes the tip shown, by SHA, as a
  fast-forward or not at all — a remote whose base moved past ours is
  refused, never forced — and then deletes the landed card's branch on
  that remote with a lease on the tip shown. A remote with several push
  URLs is refused. The stored SSH key is lent to it as to a publish push.
- The landing dialogs offer it as their second half ("then push main to
  internal", on by default where the repository closes by push;
  `Action.PushTo`, `Option.PushTo`), and the repositories view offers it
  on its own — which is also how a landing whose push failed is retried.
- It is recorded in the card's thread (`EventPublish`, act `pushbase`).
- It is not a stage, not autopilot's, not a goal's, and no MCP tool or
  `run`/`resume` reaches it, exactly as for §22.7. It needs no `gh`: it
  is git's push to whatever the remote is.

### 22.13 The working tree by hand

The web page's Changes surface has a **Working tree** tab
(`worktree/tree.go`, `ui/webtree.go`, `assets/tree.js`): what is staged
and what is not, staging a file, a hunk or some of a hunk's lines,
discarding, committing or amending, the stashes made on the card's
branch, a fetch, and the push of §22.1.

- **Only between turns.** Every write is refused while an agent is
  working in the tree (`WebDocs.busy`) or another gummi drives the card,
  and runs under the card's lock; the tab says why it is read-only. This
  is what answers §22.10's worry: the index is the person's only while no
  agent is using it.
- **Nothing unseen.** A hunk write names the digest of the diff it was
  chosen from and is refused (`moved`) when the diff has changed; a path
  write names only paths the tree lists. A line pick builds the patch git
  would from `add -p`: an unpicked addition is dropped, an unpicked
  removal kept as context.
- **A person's commit.** It runs git's own `commit` in the worktree, so
  the co-author hook and commit signing of §22.2 apply as they do to
  every commit gummi's environment makes. An attribution line is refused
  as it is in a rewrite. Amending is refused for a commit already on the
  base or already on a remote. The commit is recorded in the card's
  thread (act `commit`), which is how the agent's next turn and the
  person reading later see it was made by hand.
- **The stash stack is the repository's**, shared by every worktree: the
  tab lists, applies and drops only entries made on the card's own
  branch, found by commit rather than by position.
- **A fetch names its remote.** Fetch takes the remote the branch pushes
  to unless the person picks another of the repository's remotes, or all
  of them (`git fetch --all --prune`); a name the repository lacks is
  refused. A fetch writes only `refs/remotes`, so it is not held back by
  a running agent either.
- The TUI does not offer the tab yet; its diff view and the card's
  terminal remain how a person works the tree there.

### 22.14 A session publishes on a person's standing allowance

Decision 31. Decision 25 made every publish a person's act. That left a
session able to write a branch and unable to finish the job it was given:
open the PR, see a check go red, fix it and push again. A person who
wants that can now say so once.

- **The allowance.** A per-session flag, off by default
  (`features.agent_publish`, `Store.SetAgentPublish`,
  `Engine.AgentMayPublish` / `SetAgentMayPublish`). The PR tab's
  "Agent may publish" switch (`PUT /api/cards/{id}/publish/allowance`)
  and the card menu's "let the session publish…" set it; a person is
  named in the thread when it changes. It is given for one repository:
  the GitHub repository the branch pushes to when the person gives it
  (`features.agent_publish_repo`, `agentpublish.Target`), named in that
  thread line; a push anywhere else is refused (`target-changed`). It is
  refused for an agent (a home), a main-checkout card, a workflow step's
  card and a closed card (`engine.MayHavePublishAllowance`, which both
  faces ask), and giving it is refused while a turn is in flight, because
  the session's tools are fixed when its backend starts and a change
  restarts it with the conversation replayed. Taking it back is never
  refused: every call reads the store, so the next one fails. No tool,
  run or schedule sets it. A session may *ask*: `publish_request` (offered
  while the allowance is off) puts an ask in the thread naming the repository
  and returns at once; only the person's answer of its first option calls
  `SetAgentMayPublish`, any other answer leaves it off, and an allow while
  the session is mid-turn is refused until the turn ends.
- **The tools** (`engine/prtools.go`), listed only while the allowance
  is on and refused with `publish-not-allowed` if called anyway (each
  call reads the store afresh): `git_push`, `pr_create`, `pr_update`,
  `pr_ready`, `pr_checks`. Each resolves and acts in one call with no
  confirm. There is no merge, force-push, retarget, review-resolve,
  landing or back-to-draft tool.
- **One adapter** (`internal/agentpublish`): the same `Resolve`,
  `PlanFor` and `Do` a person's confirm runs, under the card lock, with
  the credential lent to the one command that needs it (§22.2) and never
  in the session's environment. The same refusals apply — a dirty tree,
  a rebase in flight, a stacked or landed card, the floor under ready.
  The one input that changes is "an agent holds it", because the caller
  is that agent; a workflow run working in the same tree still refuses
  (`agent-busy`). A failure reaches the session as `error: <code>: text
  — fix`, the code being `publish.Code`'s or the adapter's own
  (`steering-config`, `target-changed`, `force-refused`). A push is
  never forced: a remote that has moved is refused, not overwritten, and
  so is a remote holding a tip the branch was rewritten away from, which
  a person's confirm would push with a lease (`force-refused`). A push to
  a ready PR whose tip may not land returns the PR to draft first, as a
  person's would, and says so in its answer. Each act is a line in the
  thread, by the agent. The engine does not import `publish`;
  `internal/agentpublish` is the one agent-facing importer.
- **What the agent planted is refused, not shown.** A person's confirm
  shows the hooks and the steering configuration and its fingerprint
  pins them; nobody is shown anything here. So before the first command
  that carries the credential (`gh`, `ls-remote`), and again after the
  act's last network read and just before the push (`publish.Env.Recheck`),
  the adapter refuses, reading only local files and configuration:
  - a push hook (`pre-push`, `reference-transaction`) git would run that
    is not gummi's own. gummi's own is a file in the workspace's hooks
    directory (`Workspace.HooksDir`, lent by the engine, never read from
    the environment) holding exactly gummi's script
    (`worktree.IsOwnHook`); a link out of it, other content in it, or a
    hook anywhere else is refused (`hook-rejected`). gummi's script runs
    the repository's own hook of the same name, found as the script
    finds it (the last `core.hooksPath` of any scope but the
    environment's, else the common git directory's `hooks`), so an
    executable one of those is refused too;
  - a setting in the repository's own configuration, its local and
    worktree scopes, that runs a program or carries authentication:
    `core.sshCommand`, `core.askpass`, `core.hooksPath`, `core.gitProxy`,
    `core.fsmonitor`, `credential.*`, `url.*`, `http.*`, `include*`,
    `gpg.*`, `push.*`, `submodule.*`, `protocol.*`, and a remote's
    `pushurl`, `receivepack`, `uploadpack`, `proxy` or `vcs`
    (`steering-config`);
  - a push target other than the allowance's repository, however the
    branch's push remote, `remote.pushDefault` or a remote's URL was
    changed (`target-changed`).
- **What those checks do not close.** The agent's shell is not confined
  (`internal/sandbox`): it runs as the person. The checks keep gummi's own commands from
  running what the agent planted *in the repository*; they are not a
  sandbox. They leave open:
  - the person's home: the global and system git configuration, which
    are the person's own and legitimately carry credential helpers and
    URL rewrites, are not read for steering keys, and `~/.ssh/config`
    (a `ProxyCommand` runs with the lent key's agent socket), `gh`'s
    configuration and the `PATH` gummi itself was started with are not
    read at all. A shell that can write those can as well read the
    stored token and key directly (`internal/credentials`);
  - a write in the moment between the last check and git reading its
    configuration and hooks for the push, which no check outside git
    closes;
  - what the receiving side runs, and what a check run on GitHub does
    with the pushed commits.
  A person who needs more gives the allowance only to a session whose
  backend is confined, or publishes by hand.
- **Watching without a watcher.** *Amended by decision 32 (§28): a
  workflow run's `watch:` step is a poller gummi runs for that run; a
  session outside a run still watches by itself, as below.*
  gummi adds no poller, no webhook and
  no attention event on a red check. The loop is the session's — push,
  `pr_checks`, on a failure read the logs and the commit they ran on,
  fix, commit, `git_push`, `pr_checks` again — and the bundled
  `publishing-prs` skill teaches it. To come back later it asks the
  person for a heartbeat (§26); a session cannot propose a schedule
  itself today. The loop is bounded by the session's envelope like any
  turn.
- **What does not bend.** gummi never merges, retargets, force-pushes or
  lands, and never resolves a review thread; a person does. The
  allowance replaces the per-act confirm, not the floor.

## 23. Self-improvement — the skill pack and its curator

Every session gummi runs learns something the next one will pay for
again: a flag that does not exist, a test that only fails in a card's
environment, the one command that rebuilds the goldens. Freeform
sessions already write that down (§19.3c), and nothing reads it beyond
the card that wrote it. This section closes the loop, in the shape
Hermes uses for its own agent: sessions record and stage, one curator
reviews and distils, and gummi delivers, lints and counts. There is no
person in the loop before a skill reaches a session: it is checked
after the fact, with snapshots and revert (§23.6).

```
sessions ──record──▶ session memory, dead-ends, card events
    │                         │
    └──stage──▶ the card's own overlay (only its sessions read it)
                              │
                         curator (scheduled, budgeted, §23.4)
                              │  staged_decide / skill_write / skill_archive
                              ▼
          gummi: lint ─▶ commit in .gummi/skills ─▶ advance `published`
                              │
          every session start: materialize `published`, scoped per repo
                              ▼
                       the backend discovers it
```

### 23.1 The pack

The pack is `.gummi/skills`, a git repository of its own beside global
memory, made by gummi on first use under an identity of gummi's own,
like an agent home (§19.3d). Each skill is a directory holding a
`SKILL.md` in the agentskills form every backend that takes skills
already reads: frontmatter `name` and `description`, a markdown body,
optional `references/`. gummi adds three frontmatter keys:

- `scope` — `general`, `default` (the workspace's own repository, the
  one a card naming none works in), or the name of one of the
  workspace's repositories (§10 D10). A repository skill reaches only
  sessions working in that repository; a general one reaches every
  session.
- `source` — `curator` or `person`. A skill a person wrote or edited is
  `person`; the curator never edits, merges or archives one, and the
  usage timers never move it.
- `pinned` — a person's way to hold a curator skill where it is.

A person may commit to the pack by hand; it is their repository. The
curator never touches the working tree at all (§23.4).

**`published` is what sessions read.** The pack's branch is where
commits land; the ref `published` is what delivery reads. gummi
advances it after each write that passes lint, and a revert moves it
(§23.6). A write to the pack's working tree that is not a commit on
`published` — a session's stray shell write, a half-edited file —
reaches nothing, because delivery never reads the working tree.

### 23.2 Delivery: into every session

This reverses §4.1a's "adds nothing" for the pack, and only the pack.
The `skills:` config key stays refused at load: there is still no
user-configured forwarding. At each session start gummi materializes
`published` (`git archive` into `.gummi/state/skillpacks/<rev>`, kept
while any conversation reads it), filters it by `scope` against the
session's repository, and hands the filtered set to the backend through
the mechanism each already has:

- **claude**: a generated plugin, `--plugin-dir`, its skills named
  `gummi-skills:<name>`.
- **copilot**: the SDK's skill directories, holding the pack's skills
  renamed `gummi-<name>`.
- **opencode**: skill paths in `OPENCODE_CONFIG_CONTENT`, the inline
  config opencode loads after every file (global, gummi's session file,
  the repository's own `opencode.json(c)`), to the same renamed copy. A
  later `skills` key replaces an earlier one, so the inline one carries
  every path the files it outranks name (`.jsonc` comments and all),
  then the pack's.

**Beside the operator's skills, never over them** (decided 2026-10-16).
Each backend keeps its own discovery, the operator's installed plugins
included (§4.1a), and gummi only adds. copilot and opencode let a skill
directory they are handed win over the operator's skill of the same
name (copilot's SDK says so; opencode was checked with `opencode debug
skill`), so those two get the pack renamed, directory and frontmatter
alike, in a copy beside the materialized revision
(`skillpack.Prefixed`). claude needs no copy: the plugin is the
namespace. A pack skill's name is held to 58 characters so the prefixed
one stays within the agentskills limit. Usage counts only the pack's own
names, `gummi-skills:<name>` and `gummi-<name>`; a bare name is an
operator's or a repository's skill and is not counted.

The pack reaches these three backends and no others, decided
2026-10-16 — and since decision 27 they are the only agents gummi
runs. The headless adapter runs cards without the pack, and `doctor`
says once, not each turn, that a routed backend runs
without the pack. That narrowing is why the old `skills.forward` was
dropped (§4.1a); it is accepted here because the pack is gummi's own
learning, not a convenience over the operator's discovery.

*Amended for gummi's own skills (§25.14).* Beside the pack, delivery
hands every session two skills gummi ships in its binary, `workflows`
and `workflow-recipes` (`internal/skillpack/builtin/`). They teach
gummi itself — when a workflow run pays, how to write the checks it is
judged by, how to write a recipe — so they move with the code they
describe, and no curator, session or person edits them in a workspace.
They are written once per content under `.gummi/state/skillbuiltin/`
(`skillpack.Builtin`), outside the pack's cache, which prunes, and reach
the same three backends under the pack's names (`gummi-skills:<name>`,
`gummi-<name>`). A pack skill may not take one of their names, and their
use is not counted as the pack's.

Every pass gets the pack: plan, implement, critique, verify, and
freeform sessions. A conversation pins the revision it started with, so
a curator run mid-conversation never changes what it sees. The pin
outlives one backend process: a resumed stage session, a reopened
freeform card and a freeform card switched to another backend keep it,
and only a conversation that starts afresh takes the newest
`published`. The pins are `.gummi/state/skillpacks/pins.json`; the
hourly sweep drops those of finished cards and removes every
materialized revision no pin reads.

### 23.3 What gummi does without a model

Everything that can be decided without a model is Go in the engine,
costs nothing and runs whether or not the curator is configured:

- **Lint at write time.** A write that breaks the structure (no
  `name` or `description`, a name that collides, a body or `references/`
  over the size bound, a `scope` naming no repository) is refused and
  the refusal goes back to the curator as the tool's answer. The shape
  checks are advisory and travel with the answer too: a body that reads
  as an incident log rather than a rule, a description with no word
  saying when to use it, and, for a scoped skill, a repository path the
  body names in backticks that the scoped repository's base branch does
  not have. Symbols are not checked.
- **Usage.** gummi counts each skill call as the engine sees it, in
  every session, stage and freeform alike, into
  `.gummi/state/skillusage.json`. It cannot fold the card-event log
  afterwards: a freeform card's calls never reach it (§19.3), and a
  stage's call details are pruned when the stage ends. As Hermes does,
  it keeps three counts apart: invocations (`agent/tooldetail.go`; a
  namespaced one only in the pack's own `gummi-skills:` namespace),
  views (a read of a delivered `SKILL.md` by a backend that reads skills
  as files) and edits (the pack's own commits to the skill since it
  came in). Either of the first two resets the timers.
- **Timers.** A curator skill unused for 14 days is marked stale; for
  30, it is archived — moved under `archive/` in a commit, never
  deleted. A skill younger than its first 14 days is not stale for
  never having been used. Pinned and `person` skills are exempt.
- **Publishing and revert** (§23.6).

### 23.4 The curator: a background job, not a card

Distilling needs a model. The curator is that model's session, run by
gummi on a clock, and it is **not a card**: it has no stage, no branch,
no thread on the board and nothing to land. It is configured in
`.gummi/config.yaml`:

```yaml
curator:
  backend: claude
  model: claude-sonnet-5-5
  schedule: "@daily"     # cron, compiled by internal/schedule (§19.9)
  budget: 100            # credits per run
  monthly: 1500          # credits per calendar month, all runs (0: no cap)
  review_gap: 6h         # least time between staged reviews (off: none)
```

No `curator:` block, no curator: the pack is then a person's, and
§23.3 still lints, counts and archives nothing but what a person
marked `curator`. The block is the switch that starts spending, and
`doctor` reads it back. A person may also write it from the web page's
skills view (`PUT`/`DELETE /api/curator/config`): the same block, saved
into `config.yaml` with the rest of the file and its comments left
alone, and live at the board's next tick without a restart. A run
already going finishes on the block it started with. The page is
refused, before anything is written, what `Load` would refuse and a
curator that could not run: a schedule that does not compile, a
backend gummi does not know or that cannot reach its tools
(`engine.CheckCurator`), or a model the backend's own catalog does not
offer (`CheckCuratorModel`; a backend that cannot say is not held
against it). The block is written as text: only its own lines change,
and the rest of the file keeps its comments and indentation.

**Its budget is its own.** Each run starts with `budget`, capped by
what is left of `monthly`, and gummi stops the turn when it is spent,
exactly as it stops a card's. An exhausted month pauses the curator
until the next one; raising either number is the person's. Every run
is a row of its own — start, end, revision before and after, credits,
outcome — checkpointed as the run goes, so a board stopped mid-run
leaves what it spent and wrote (read back as failed), read by the stats tab, which draws the curator as a lane of
its own in the timeline and charges its spend to the window it started
in (`fleetrun`), and by the digest (§23.6). A curator's spend is never
a card's.

**Where it runs.** Its working directory is `.gummi/curator`, a
directory gummi made, under the same `GIT_CEILING_DIRECTORIES` guard as
a home (§19.3d), sandboxed by the configured posture (§4.4). It is
given no repository checkout. It fires like a schedule (§19.9): only
while a board runs, missed fires coalesce and are caught up by the next
board (its clock starts when it is first armed on the workspace, not at
each board's start), a fire while a run is still going is skipped. A person can run it once from either face.

A staged change (§23.5) starts a run of its own rather than waiting a
schedule out: once the newest pending change is 30 minutes old (so the
few changes one session stages go to one run) and the last run began at
least `review_gap` ago (6h by default, so a busy board does not curate
all day), the next
tick starts one, unless the month's budget cannot pay for a turn. That
run is review-only: its kickoff lists the staged changes and the pack,
and it is not offered the memory or event reads, so it costs a review
and not a curation. The scheduled run still does the rest. The gap is
a Go duration of at least 30 minutes, since a change the curator left
undecided would otherwise start a run at nearly every tick; `off` leaves
staged changes to the schedule. A person sets it from the skills view's
settings like the rest of the block. The 30-minute quiet is fixed. The
ledger records the run's reason as `staged`.

**What it reads and writes is tools, not files.** Its session carries
its own tool set on its own MCP endpoint:

- reads: every card's session memory and dead-ends, the card-event log
  since its last run (verdicts, failed verifies, critique findings,
  `ask_user` answers), the pack at `published` with each skill's usage
  and age;
- the changes card sessions staged (§23.5): `staged_read` shows one
  beside the published skill it changes, and says when that skill moved
  after it was staged;
- each repository at its base branch: `repo_read` a file (its
  `AGENTS.md` first) and `repo_grep` a pattern, so a claim a skill makes
  about a codebase is checked against it, and a skill that only repeats
  or contradicts `AGENTS.md` is caught. Never a card's worktree;
- writes: `skill_write` (create or replace one curator skill),
  `skill_archive`, and `staged_decide`: accept a staged change (gummi
  lints and commits it, as one of the run's own commits), mark it
  merged (the curator wrote what it taught into another skill), or
  reject it with a reason the card reads. Merging several skills into
  one is one `skill_write` naming the skills it absorbs in `replaces`,
  which gummi commits, the write and the archives together, as one
  commit.

The curator is the review between one card's lesson and every session:
it is the only writer of `published`, and reviewing what sessions staged
is the first thing its kickoff asks of it.

gummi does each write: lint, commit to the pack under gummi's identity
with the run's id in the message, advance `published`. Every write to
the pack (the curator's, the sweep's, a person's pin or revert from
either face or the CLI) holds `.gummi/skills.lock`, so a CLI beside a
running board waits its turn rather than interleaving. The curator
never holds a path to the pack, so how its backend cages file writes
does not matter, and no other session is offered these tools.

**Why not a card, and why this is not a board-level agent.** §16
removed an agent bound to the whole workspace that acted on cards: it
ran them, minted them and read their state for a person who had not
asked. The curator acts on no card. It reads what sessions left behind
and writes into one repository gummi owns, through its own tools. A card
would have given it a budget, metering and a place on the board, at the
cost of a branch it never lands, a worktree it never uses and a thread
nobody is meant to talk in. The goal lead (§17.4) already showed the
other way: a model session inside gummi's own loop, charged to a ledger
that is not a card's. The curator is the second.

### 23.5 What a session records and stages

**Staging** (decided 2026-10-16). Every card session on a backend the
pack reaches (§23.2), freeform and every pass but a rebase, is given two
gummi tools: `skill_read` (a skill's `SKILL.md` or a file of it, as the
card sees it) and `skill_manage` (`write` a new skill or the whole new
text of a curator one, `archive` one with a reason, `withdraw` its own
change, `list` its changes and the curator's verdicts). Neither touches
the pack. A change is staged under `.gummi/state/skillstage/<card>/`,
files rather than commits, with the published revision it was staged
against, and is linted as the curator's own write would be; a person's
or pinned skill is refused, as it is to the curator.

Only the staging card reads its changes. Its sessions get the revision
they pinned with the card's pending changes laid over it (written skills
in, archived ones out), composed once per content under the card's
stage; every other card gets the revision itself. `skill_read` reads
the overlay live, so a skill staged mid-session is readable at once;
the backend's own skill list waits for the card's next session, because
every backend reads it at start. A use of a skill the card has staged
is counted apart from the published skill's (`Use.Staged`), so the
curator can tell whether a change was used where it was made.

The curator decides each change (§23.4). An accepted one becomes a pack
commit of the run's and leaves the stage; a merged one leaves it; a
rejected one stays with its reason, reaches no session, and may be
staged again. A card that finishes keeps its pending changes for review:
they are the lessons. The hourly sweep removes its composed views.

**A skill that misled.** gummi counts a skill's uses, not whether
following it worked, so the signal that a skill is wrong comes from
the sessions, in three places. The staging hint asks a session to
fix, before it finishes, a skill it followed into a failure. The next
turn a person sends, to a freeform session or a stage session it
steers, asks once whether each pack skill the card used held up. Each
skill is asked about once per card, held in memory, so a restart may
ask once more. The note reaches the agent but stays out of the
transcript, and is only added on a backend with `skill_manage`. And a
scheduled run's `curator_inputs` lists each curator skill named in a
dead-end it has not read, with the cards that name it, and the
kickoff sends it to those cards first. A bare one-word skill name is
not counted, since "make" is prose as often as it is a skill. A
review-only run is not given the list. It is a list of names, not
verdicts: the curator reads the dead-end before it judges.

**Capture without staging** is what sessions already do. A freeform session writes session
memory and dead-ends (§19.3c), and its hints now say what the curator
reads them for: a rule a later session should follow, said once, not
the story of how it was learned. Stage sessions carry no memory tools
and are not given any. What a workflow card teaches is in its events
(a critique's findings, a failed verify, a rerun edge taken), and the
curator reads those instead.

**What is new to a run.** The cutoff belongs to each card, not to each
run. A run records the cards it opened, and when: their memory through
`curator_read` and their events through `curator_events`. A card is
offered again only once it has changed since the curator last read it. A run that failed, timed out or ran out of budget still read what it
read, so a large backlog moves on rather than being reread each run.
One cutoff for the whole run would have hidden every card a run listed
and never opened. A first run on a workspace with history reads three
cards out of fifty, and the next run would have been offered none of
them. A run that reads nothing therefore moves nothing. The kickoff
gives each card's memory size and asks the curator to work through the
list until its budget runs low. It also says that only the pack counts:
a skill the curator's own backend loads from its home reaches no other
backend, so it does not make a lesson covered.

### 23.6 No person before, a person after

A skill reaches sessions as soon as its write passes lint. That is
Hermes's default, and the safety it relies on is Hermes's too:

- **Snapshots.** Every curator write is a commit; every run's
  before-and-after is a pair of revisions on its row.
- **Revert.** A person reverts a run (one commit undoing all of the
  run's own commits, and none of a sweep's or a person's pins that
  landed while it ran) or one skill, from either face or `gummi skills revert`; `published`
  moves with the commit, so the next session starts without it. A pin
  holds a skill the curator would otherwise rewrite or archive.
  Reverting a run that accepted a staged change does not stage it again:
  the person's revert is the rejection, and the card's own copy left
  its stage when it was accepted.
- **A digest, not a gate.** After each run that changed something, the
  board raises a notice listing what was written and archived and what
  it cost, with revert beside each line. Nothing waits on it.
- **Only its own.** The curator never touches a `person` or pinned
  skill, never global memory, and nothing outside the pack.

This is the one scheduled output that takes effect without a person,
and it is stated here and in Decisions 17 and 26 and §19.9 for that
reason. What it does not change: every card still walks the whole
graph, every critique and verify still runs, and a skill cannot cross a
gate, answer a decision or land anything. A skill can make a pass worse
at its work. It cannot remove the pass.

**Where it shows.** The digest is a board notice and an alert. A
person reverts a run and pins a skill from the TUI's skills view (`E`),
the web page's Skills view, or `gummi curator revert|pin|unpin`; all
three call the same engine acts over one read model
(`engine.CuratorView`). Both skills views also list what sessions
staged (§23.5): each change's card, pending ones with their reason and
rejected ones with the curator's, read-only. The web page's view also
reads a skill's SKILL.md (`engine.SkillDoc`): a published one rendered or
as source, a staged write as a diff against the published one beside the
SKILL.md it would publish, and a staged archive as the SKILL.md it would
take out. A person's read counts no use. The stats tab draws the curator as its own
lane and money line, outside the card figures and outside the clock
and concurrency figures, which measure cards. `doctor` reports the
pack, the curator and every routed backend that runs without the pack.
The curator reads a card's failed verifies, critiques that asked for
changes, rerun edges and answered questions from the event log, but
not a spec's reviewer threads.

### 23.7 Deferred

- **Global memory.** The curator does not write `global.md`: it has no
  history to revert from, and it is the tier a person curates (§19.3c).
  The distillation pass §19.7 lists stays open. When it comes, the file
  moves into a repository first.
- **Delivery beyond claude, copilot and opencode** (§23.2). No skill
  index in the hints and no MCP fallback for the others.
- **Validation beyond Hermes's.** Usage and lint are the signals.
  Checking that a skill's lesson stops recurring, holding it back from
  a random share of sessions, or replaying a session with and without
  it are all possible, because gummi picks what each session gets, and
  none is built.
- **Importing existing skills.** Skills an operator's own tools wrote
  into `~/.claude/skills` are the operator's discovery (§4.1a), not the
  pack. Bringing them in is a person's commit.

## 24. Sessions and crews — work without a workflow

> **Amended by §25 (decision 28).** Crews are replaced by workflows:
> recipe files of steps that run in the session's own tree and leave
> their changes uncommitted, a list of specs per session, and no crew
> branch, node landing or owns check.

*Decided 2026-10-21 (decision 27); built.* This section describes the
code as it is. It supersedes the workflow (§3), the kinds' stage
contracts (§12, §13), spec ingestion and card dependencies (§11), goals
(§17), the "write a spec" hand-off (§19.8), delegation (§19.10),
autopilot (decision 17) and branch adoption (decision 22); each of those
sections carries a note saying so and is kept as history. Workflow
cards are retired: nothing mints or runs one, and a legacy row in an old
database is read-only (§24.11).

Every unit of work starts as a **session** (§19.8). A session may grow a
**spec**, and it may call on a **crew**: a planner that turns the spec
into a plan of nodes, and specialist agents that carry them out, each
checked and reviewed, landing on a branch of the crew's own. The session
agent calls a crew like a tool, or a person asks for one. gummi offers
the process; it no longer enforces it.

```
session ──talk──▶ spec (optional) ──crew_start──▶ plan ──you approve──▶ run ──▶ report
   ▲                                                                              │
   └──────────── the session carries on from the report, and may start another ───┘
   lands on a person's read of its diff, as a session always has (§19.1)
```

### 24.1 Why

The graph made every change pay for a design, a critique, an
implementation, a second critique and a verify, whether the change needed
them or not. Freeform cards (§19) were the admission that most work does
not, and once sessions became the default (§19.8) the graph was the less
common path with most of the code. The design survives in it because
its ideas are right: a spec as the context carrier, a fresh-context
reviewer, machine-checked "done", budgets a person sets.

The market (surveyed 2026-10) lands in the same place. The products that
work with coding agents at scale have a planner write a spec or task
graph and check each step with an independent verifier (Augment Intent,
Factory Missions, Cognition's managed Devins, and the spec-driven IDEs' task lists). The
two widely read arguments, Anthropic's for multi-agent systems and
Cognition's against them, now agree on the same rules:
- one writer per area;
- a clean-context reviewer;
- artifacts between agents, not transcripts;
- parallel work only where the work divides.

None of them shows the fleet as a tree with cost and attention on it,
makes each planned step a gate with checks of its own, or catches
conflicting decisions between siblings before they merge. A crew does
all three.

So the floor moves from the card to the crew. A crew always plans,
checks, reviews and verifies, and a person approves its plan and every
widening of it. Whether to use a crew is the session's choice and the
person's, not gummi's.

### 24.2 The session's spec

A session has at most one spec, and most never have one. A session about
to call a crew needs one, because a crew is judged against its done-when
items.

| tool | does | origin |
|---|---|---|
| `spec_start(title)` | creates the spec from the template | new; replaces §19.8's "write a spec" hand-off |
| `spec_view(section?)` | reads it | the stage tool, offered to sessions |
| `spec_replace_section(section, body)` | writes one section | the stage tool, offered to sessions |
| `spec_annotate(anchor, note)` | anchors a `%%` question to the line a snippet names | the stage tool, offered to sessions |
| `spec_critique()` | a fresh-context reviewer tries to refute the spec; findings land as anchored threads | the plan critique (decision 4), **offered, never required**; *not built* (§24.13) |

The first four are offered to every session (`engine/sessionspec.go`);
the spec stays on the session rather than handing itself off into a
feature card.

**The template** merges what the three kinds' contracts asked for:
Problem, Approach, **Done when** (the `gummi-done-when` block,
`domain.DoneWhen` and `spec.ParseDoneWhen` unchanged), Limits (what is
deliberately not done) and Notes, which carry the `%%` checklist. A bug
is offered a Reproduction section and research a Question in place of
Approach. They are prompts, not sections a gate demands: no gate reads
a session's spec.

**It lives with the workspace** (`Feature.SpecPath`,
`.gummi/specs/<ID>-<slug>.md`), not on the branch, so it is never in the
card's diff and never lands in the repository. Each write through the
spec tools or the Spec tab is a numbered **revision**, snapshotted in the
state store, which nothing keeps today. The person reads, edits and
annotates it on the card's Spec tab, the way a diff is annotated (§6.1).
Extending `resolve_annotation`, still a diff comment's only, to the
spec's comments is deferred (§24.13).

### 24.3 A crew, from the session's side

| tool | does | returns |
|---|---|---|
| `crew_start(brief, budget)` | asks for a crew; the planner wakes and writes the plan | at once: the crew is proposed |
| `crew_status()` | nodes, states, spend, open questions | text |
| `crew_steer(note)` | a note to the planner, which wakes it | ack |
| `crew_stop(reason)` | winds the crew down; what has landed stays | ack |
| `crew_land()` | lands a finished crew's branch on the session's | the merge, or why not |

The rules for using them:

- **Always offered.** A crew is an ordinary option, so there is no
  opt-in like delegation's. What holds the cost is the approval, the
  budget, and the planner's own answer that a change is too small to
  split.
- **A spec first.** `crew_start` refuses a session whose spec has no
  done-when item, and says so. The session agent writes the spec with
  the person; it holds the context, so the planner does not write it.
- **Asynchronous.** `crew_start` returns at once. A crew runs for tens
  of minutes, and a tool call held open that long fights every
  backend's idea of a turn and locks the person out of the session. The
  report comes back as a turn gummi sends under an actor of its own
  (`WithActor`), the way an objective's next turn does (§19.11). The
  thread draws it as the crew's.
- **No `crew_approve`.** The plan goes to the person as a decision, the
  way every checkpoint does (decision 18). A person may pre-authorise
  a session's crews up to an amount each, in the manner of
  delegation's "this one and all that follow": a plan under it runs
  without asking, and the session's own budget still caps how many.
  Pre-authorisation covers the first plan only; a widening always goes
  to the person. The agent never approves its own crew.
- **Starting a crew freezes the spec.** The crew is judged against
  that revision. An edit to the spec during a run is a steer to the
  planner.
- **One crew at a time per session**, so one branch has one writer.
- **The session waits for its crew.** A session may not land while a
  crew is open, as §19.10's parent waits for its cards. Closing a
  session with a crew running asks the person first, then stops the
  crew (`crew_stop`) and keeps what landed on the crew branch.
- **Nothing resumes itself.** A quit parks a running crew: its nodes'
  backends stop, and its pending planner wakes are kept in the store.
  After a restart the crew shows as paused, and runs again only when a
  person resumes it (decision 17).

**Where the crew works.** A crew has a branch of its own, forked from the
session's tip when the crew starts: `crew/<slug>/<n>`, and its nodes'
`crew/<slug>/<n>.<node>`, where a node id is always `t<k>`. They are
not nested under the session's `ff/…` ref, because git cannot hold both
a ref and a directory of that name; and the slash after the slug keeps
session `foo`'s crew 1 apart from session `foo-1`'s. `Feature.BranchName`
stays the one constructor (crews and nodes are card rows, §24.6), and
`Store.BranchTaken` and cleanup cover `crew/` refs. Nodes fork from the
crew branch and land back on it, routed on the parent the way goal
cards are (`Pool.ManagerFor`). The session keeps its own checkout and
may keep working while the crew runs.

**Landing a crew.** A finished or partial crew lands on the session's
branch (`crew_land`, `Engine.CrewLand`) as one squash commit, refused while the session's tree holds uncommitted work, with a
conflict going back to the session agent. A partial crew lands what it
landed, and says so. After the squash gummi runs the done-when checks
on the session's new tip, and that commit is what the verified mark
(§24.8) is pinned to. Commits the session made during the run sit
under it: the checks saw them, the crew's reviewers did not, and the
report says so. Node branches go when their node lands; the crew branch
goes when the crew lands, or with the session.

**Budget.** A session and its crews spend from one pool: the session's
envelope (§24.7a). A crew's budget is the person's approved estimate of
what it will cost, and a node's envelope the planner's estimate of one
node; neither is a fence. The planner and the reviews are the crew's own
spend, and the session's turns are the session's, but all of it comes
out of the same envelope.

### 24.4 The crew document

`.gummi/crews/<ID>-<slug>/<n>.md` (`Engine.CrewDocPath`). Its sections:

- **Brief**: what the session asked for, and the spec revision frozen
  for the crew.
- **Done when**: copied from the spec when the crew starts.
- **Plan**: a `gummi-crew` block.
- **Limits**.
- **Run log**: gummi appends each node's handoff.
- **Report**: written when the crew ends.

```yaml
# gummi-crew
lanes: 3
budget: 1500            # credits, out of the session's envelope
nodes:
  - id: t2
    role: contractor    # scout | contractor | implementer | tester | scribe
    title: TurnLog type and store interface
    brief: |
      Define TurnLog and the store interface, with stubs.
      Do not wire the engine.
    needs: [t1]
    owns: [internal/domain/turn.go, internal/state/turns.go]
    serves: [DW-1]
    checks: [go build ./..., go vet ./internal/state/]
    tier: standard      # cheap | standard | strong — a profile slot
    envelope: 220
```

A node row (`domain.CrewNode`) is a title, the items it serves, the
nodes it `needs`, an envelope, and its `role`, `brief`, `owns`, `checks`
and `tier`.

**The roles are a fixed catalogue.** The planner sharpens a role with a
brief and with skills from the pack (§23); it does not invent one. An
invented role has no profile slot and no record to price it by.

| role | does |
|---|---|
| scout | reads, reproduces and maps; writes nothing but its handoff |
| contractor | writes, first and alone, the interfaces and stubs that parallel nodes build against |
| implementer | builds inside what it owns |
| tester | writes and extends tests |
| scribe | writes docs and the try-it guide |

**The reviewer is gummi's, never the planner's to place.** It runs after
every writing node, and a plan cannot leave it out because a plan cannot
name it. Integration was designed as a second such role; as built, a
node whose landing conflicts is sent back to integrate the crew branch
into its own and resolve the conflict, then lands again.

**Profiles map roles to models.** `planner` falls back to `architect`;
a node's tier is the slot `crew-<tier>`, falling back to `implementer`;
reviews use `reviewer`.

### 24.5 The plan checks

A pure `crewplan.Problems(plan, items) []string`; the budget is the
plan's own. A plan that fails is sent back to the planner
before any person sees it.

1. Node ids are unique, every `needs` names a node, and there is no
   cycle.
2. Every done-when item is served by at least one node.
3. Every writing node (contractor, implementer, tester, scribe) owns at
   least one path. It also has at least one check, or serves an item
   that has one. A scout owns nothing.
4. **Two nodes whose `owns` overlap are ordered:** one must reach the
   other through `needs`. This is the one-writer rule. Overlap is a
   conservative test on path prefixes, so a false positive costs only a
   serialised pair.
5. The nodes' envelopes and the planner's reserve fit the budget, and
   no envelope is below what a node can finish in.
6. Every role is in the catalogue, and every tier is in the profile.

A plan of one implementer node is valid, and the planner is told to
choose it whenever the work does not divide.

### 24.6 A node

```
waiting ─▶ running ─▶ checking ─▶ reviewing ─▶ landed
              ▲           │            │
              └── rework ─┴────────────┘     (capped at 2, then stuck)
           also: stuck · exhausted · dropped
```

- **A node is a short session that no person works.** It is stored as a
  card row of its own kind, child of the crew and hidden from the board,
  because everything it reuses takes a card: `Pool.ManagerFor`,
  `SquashMerge`, `DelegateHeld`, `cardrun` and `BranchTaken`. It runs in
  its own tree on its own branch, with its role's prompt, its brief, the
  frozen spec and the handoffs of the nodes it needs. It never sees
  another agent's transcript.
- **Its sandbox is the session's.** A scout runs read-only wherever the
  backend enforces it (`Capabilities().ReadOnlyEnforce`, as research
  does), and the owns check at landing (below) holds for every node. A
  tool call asking to act outside the sandbox parks for the person, as
  under decision 17. Neither the planner nor a node can answer it.
- **The handoff** ends every node in a fixed grammar, parsed the way
  `FINDING:` lines are:
  - `DONE:` what it did;
  - `DECISION:` a choice it made that the brief did not make for it;
  - `OPEN:` what it left.
  `DECISION:` lines are how conflicting choices between siblings
  surface before they merge.
- **Checking** runs the node's `checks` in its tree.
- **Reviewing** is a fresh-context reviewer on the profile's `reviewer`,
  ending in `FINDING:` lines and one `VERDICT: pass|changes` line
  (`engine/crewreview.go`). Findings rework the node in place.
- **What it owns is enforced when it lands.** A diff that touches a path
  outside `owns` is sent back as a finding. The one-writer rule is
  something gummi checks, not something it asks for.
- **Landing** squashes the node onto the crew branch in plan order
  (`Pool.SquashMerge`). A conflict sends the node back to merge the crew
  branch into its own and resolve it.
- **Questions** (`ask_user`) go to the planner first, as a goal's lead
  once took its cards' questions. It answers what the frozen spec
  settles. What the spec does not settle goes to the person as a crew
  decision. Unlike the goal's lead, which fell back to the recommended
  option, a crew never answers for the person.
  The planner's answers are drawn in the node's thread as its own.

### 24.7 The conductor and the planner

`crewpolicy.Decide(Input) []Action` is pure: the subset of the retired
goal conductor a crew needs.
`Engine.CrewTick` builds the snapshot and executes the actions. Each
tick, in this order:

1. Wind down when stopped: land what is ready, drop the rest.
2. Pause when the session's pool is spent (§24.7a), and do nothing else.
3. Land one node that has passed its checks and review.
4. Put an exhausted node — a runaway — to the person.
5. Send a stuck node to the planner up to twice, then to the person.
6. Wake the planner on a pending reason: the start, a steer, a handoff
   with `DECISION:` or `OPEN:` lines, a question it must answer, or the
   person's answer to a widening.
7. Start ready nodes up to the lanes.
8. Warn the person, once, when the forecast is past what the pool has
   left (§24.7a).
9. When nothing is left, run the **final review** and write the report.
   The final review is the done-when checks on the crew's tip, plus one
   reviewer over the whole crew diff against the frozen spec.
   With no crew diff to read (nothing landed) or a stop, no reviewer
   runs: the report marks the judge items unjudged (`–`), never met, and
   the crew is partial.

Tranches, substrates, experiments and programmes were goal machinery,
and were retired with goals.

### 24.7a Money: one pool, estimates, and no work thrown away

*For workflow runs (§25), which replaced crews, only **One pool**,
**Pause, never drop** and **Only a person raises the money** below
stand. A run estimates nothing: there is no forecast to warn ahead and no
runaway, and a run goes on until the session's pool is spent, then
pauses for a top-up (§25.7, §25.14).*

Stopping an agent that is doing its job wastes what it has spent, so a
crew is never stopped over money unless the person says so.

- **One pool.** A crew spends from its session's envelope: the session's
  own turns, the crew's planner and reviews, and every node's card are
  taken off the same figure (`Engine.sessionPoolLeft`), and the session's
  own meter counts its crews' spend (`sessionBudget`). The crew's budget
  and a node's envelope are estimates. A session without an envelope has
  no ceiling.
- **Pause, never drop.** When the pool is spent the crew pauses
  (`OutOfBudget`). Its nodes are not stopped: each finishes the turn it
  is in, and nothing new is sent. A node whose turn ends then waits,
  unreviewed, rather than failing its review for want of money. The
  person is asked to top the session up by a suggested amount. Raising
  the session's envelope — from that decision, the envelope dialog, or
  `gummi resume --envelope` — carries the crew on from where it was: a
  node waiting to be reviewed goes straight to review, and any other node
  carries on in its own tree and conversation. Nothing starts over.
- **Warn ahead.** Every tick reads a forecast
  (`crewpolicy.ComputeForecast`): each unsettled node's estimate, scaled
  by how far the landed nodes ran over theirs (never below 1), less what
  it has spent, plus a reserve for the planner and the final review. When
  that is past what the pool has left the person is asked, once, while
  there is still money to finish what runs: top up, finish what is
  running and start nothing new (the nodes not yet started are dropped —
  they hold no work), or stop. Nothing pauses while the question stands.
  A raise of the envelope lets it warn again.
- **Runaways are behaviour, not budget.** A node's card is capped at
  three times its estimate (`runawayFactor`). Past it the node is a
  runaway, and the person decides: let it carry on for another three
  estimates, or drop it. A node stuck past the planner goes to the person
  the same way: one more round of rework, or drop it. The conductor drops
  nothing on its own.
- **A dropped node keeps its branch.** Its tree goes; its commits stay
  on `crew/<slug>/<n>.<node>` until the crew lands or is declined.
- **Only a person raises the money.** No agent tops a session up; the
  planner may move estimates between nodes inside the crew's approved
  estimate, and raising that estimate is a widening.

**The planner** is a short turn on `planner`, in the crew tree, waking
only on those events and acting only through its own tools:
- `crew_doc(section?)` (reads the crew document),
- `plan_write` (the first plan, or a revision),
- `node_rework(node, note)`,
- `node_drop(node, why)`,
- `node_raise(node, to)`,
- `answer(node, text)`,
- `ask_person(node)` (the spec does not settle the question).
Each turn is booked to the session.

**Widening is mechanical.** gummi compares every revision with the
approved plan. The revision is a decision for the person if it:
- adds, removes or rewords a done-when item, or changes its check;
- raises the crew's budget;
- gives a node a path outside the union of the paths the person
  approved.

Everything else is applied and written to the run log: new nodes inside
the approved paths and budget, splits, reorders, reworks and drops.
This is decision 17's "may redo, may never widen", stated for crews. A
person who answers "keep the scope" writes the gap into the crew's
Limits instead.

### 24.8 The report, and after it

A crew ends **finished** when the final review passes, and **partial**
when it is stopped or a node was dropped on the way. Either way
the report reaches the session as one turn, and the same text goes into
the crew document. It holds:
- each done-when item, met or not, with its check's result;
- the nodes' `DECISION:` lines, gathered;
- what is left open;
- the cost against the plan, and how much of it was rework.

The spec's Notes take the decisions and the open items, so the next crew
starts from the spec rather than the transcript.

The session agent carries on from the report, the diff since the crew
started and the spec. It answers questions, makes small fixes, lands the
crew (`crew_land`) or starts another crew. A follow-up planner reads the
previous crew's run log.

**There is no automatic review after a crew.** The session may change
its branch after a crew has verified it. The card's head says so:
**"crew verified at `<sha>` · N commits since"**, where the sha is the
landing commit whose checks passed (§24.3). A partial crew's mark says
partial. One action re-runs the done-when checks on the tip and moves
the mark there. Landing is still the person's
read of the diff (§19.1). The crew's review and checks are evidence for
that read, never a gate on it.

### 24.9 The faces

**The Crew tab.** It sits beside Thread, Spec and Diff on a session's
page. It holds:
- a budget strip: spent, then planned, then the session's envelope,
  with the lanes, rework and the time elapsed;
- the plan drawn as waves of nodes: role, title, check bars, spend,
  time and state, with a rework badge, and an amber ring on the node
  that waits on the person;
- the crew branch with what has landed on it;
- a strip saying when the planner last woke and why.

Selecting a node opens its page beside the canvas: its question
(recommended answer first), its brief, what it owns, its checks, its
`DECISION:` lines, and tabs for its thread, its diff and its handoff.

**In the thread.** The plan is a decision. It shows the nodes in waves
with their owns and costs, the plan checks as ticks, the models each
role gets, and three answers: run the crew, talk it over, keep it a
session. A widening is a decision too, drawn as a diff of the plan. The
run itself folds to one line per event.

**On the board.** A session with a crew shows one square per node in its
state's colour, and its attention lane counts the crew's open questions.

**Read models.** As built, `internal/ui/webcrew.go` reads the crew
record into what the page draws (`internal/web/assets/crew.js`), with
its JSON shape set in `webapi` (`webapi/crew.go`) first. A pure
`crewrun` fold shared with the TUI and reusing `cardrun` per node is
deferred (§24.13).

The TUI ticks crews (`ui/crewloop.go`) and answers their decisions in
its picker; it has no Crew tab of its own yet. The canvas is the web
face's own.

### 24.10 What a person gives up

- **The guarantee.** A card in the workflow could not land unverified,
  and `MayLand` enforced it. Now a branch is as verified as the crews
  that ran on it, and the verified mark says how current that is.
  `MayLand` refuses only a card with no landing of its own; an open
  crew, a stack below and unresolved diff comments hold a landing
  (`cardops`).
- **A fixed contract for outside drivers.** `gummi run`/`resume`, their
  NDJSON and the `gummi skill` bundle described stages. They were
  rebuilt on sessions and crews (`docs/HEADLESS.md`); the stage-shaped
  flags and events are gone rather than deprecated.
- **Adoption.** An adopted card had to walk the whole graph (D22). No
  new card adopts a branch now. A legacy adopted card keeps its custody:
  gummi never deletes, rebases or force-pushes its branch.

### 24.11 What becomes of what is there

| was | now |
|---|---|
| the graph and its rerun edges (§3, `internal/workflow`) | retired; a node's rework and the planner's revisions replace the reruns |
| plan stage: explore, converge, plan | the session's conversation and its spec |
| plan critique | `spec_critique`, offered; the planner's plan is checked by `crewplan.Problems` |
| implement and its critique | a crew's nodes, each reviewed |
| verify | each node's checks and the crew's final review |
| feature, bug and research kinds | retired; spec template prompts and planner guidance; research is a session's, or a crew of scouts and a scribe |
| research's decompose gate | retired; a report's open items go into the spec's Notes, and a person starts new sessions |
| spec ingestion (§11) and card dependencies (§11.4a) | retired; a crew node's `needs` orders work inside a crew, and a stack (§18) orders landings |
| goals (§17) | retired, with their ledger, substrates, experiments, notebook and programmes; `crewpolicy` keeps the held-budget rule |
| delegation (§19.10) | replaced by crews |
| autopilot (decision 17) | retired; inside a crew the planner may redo and never widen |
| branch adoption (decision 22) | no new adoption; a legacy adopted card keeps its custody rules |
| "write a spec" (§19.8) | `spec_start` |
| objectives (§19.11), schedules and heartbeats (§19.9) | unchanged: they are already a session's |
| stacks (§18) | unchanged |
| `gatepolicy` | deleted; the two decisions left, a plan and a widening, are crew decisions (`Engine.CrewDecision`, `CrewAnswer`) |
| `MayLand`'s two floors | the verified-branch floor is gone; a person's read of the diff (§19.1) stays |
| `run` / `resume` | `run` mints a session from a prompt, pre-authorises its crew to `--envelope` and drives it until it is `done` or `needs_you`; `resume` answers or sends a turn and drives on (`docs/HEADLESS.md`) |
| workflow, bug, research and goal rows in an old database | read-only: shown, never run, minted or copied |

### 24.12 The order of retirement

*Done.* Step 1 shipped, and the web face's Crew tab draws the plan as
waves of nodes (step 2); the TUI has no Crew view yet (§24.13). Step 3 went further: rather
than moving goals onto crews, goals were retired, and `run`/`resume`
were rebuilt on sessions without keeping the old output. Step 4's
evidence was not waited for: workflow cards stopped being minted, and
`internal/workflow`, the stage contracts, autopilot, goals, delegation,
adoption, research, ingestion, card dependencies and the verified-branch
floor were deleted together, with legacy rows left read-only. The plan
as written:

1. **Crews in sessions.** Session specs, the crew tools, the document
   and its checks, nodes, the conductor, the report, and a Crew tab
   that is a list. Workflow cards are untouched.
2. **The Crew tab's canvas.**
3. **Goals and the headless driver move onto crews.** The separate goal
   engine is removed. `run`/`resume` take their new shape, with the old
   output kept for a deprecation period.
4. **Evidence.** `cardrun` compares crews with workflow cards on cost and
   rework, over real work.
5. **Workflow cards stop being minted.** Those in flight finish on the
   old engine. Then `internal/workflow`, the stage contracts, delegation
   and the verified-branch floor are deleted, and §3, §12, §13, §17 and §19.10
   are rewritten as history.

### 24.13 Deferred

- **A node that is a whole session**, with its own spec, escalated when
  the planner judges a piece too large for one node. Goals need it in
  step 3.
- **A crew across repositories.** Goal trees already do this, and a crew
  inherits it with goals.
- **Two crews at once on one session**, which would need two integration
  branches and a merge between them.
- **Planner-invented roles.**
- **`spec_critique`** (§24.2), and `resolve_annotation` on a spec's
  comments.
- **A shared `crewrun` read model** and a Crew view in the TUI (§24.9).
- **An integrator role** separate from the node whose landing conflicts
  (§24.4).

## 25. Workflows — recipes a session runs in its own tree

*Decided and built 2026-10-22 (decision 28).* This section replaces crews
(§24.3–§24.9), the session's single spec (§24.2) and the Crew tab. Where
it and §24 disagree, this section wins. §24 is kept as the history of
how crews worked. Where the build settled something this section left
open, or departed from it, §25.14 says so.

A **workflow** is a recipe of steps, kept as a file. A session **runs**
one workflow at a time, against one of the **specs** it holds.

- Each step is a fresh-context agent.
- The steps work **in the session's own worktree**: one after another,
  or side by side inside a `parallel` block.
- Nothing is branched and nothing is landed. A run leaves its changes in
  the tree, uncommitted, and the session commits what it keeps.
- gummi snapshots the tree at every step boundary. Each step therefore
  has a diff of its own, a reviewer can read the run's diff, and a
  person can put the tree back.
- Review, checks and design are steps a recipe chooses. gummi does not
  impose them.
- A run is drawn as one block in the session's thread, and a person can
  open any step there and talk to its agent.

```
session ──spec A──▶ allow ──▶ run 1 (deliver) ──▶ report · changes left in the tree
   │                         the session (or a person) reads them and commits
   ├──spec B──▶ allow ──▶ run 2 ──▶ …                         one run at a time
   lands on a person's read of its diff, as a session always has (§19.1)
```

### 25.1 Why

Crews were used, and three things about them were wrong:

- **`crew_land` made a crew a thing beside the session**, with a branch
  of its own to land. A workflow is how a session gets a piece of work
  done, so its work belongs in the session's tree.
- **One spec per session tied unrelated pieces of work together.**
- **The crew was drawn off to the side**, on the Crew tab and in the
  terminal's strip, rather than in the thread.

Landing each reviewed step on the session's branch was considered and
rejected. It needs git writes into a live agent's tree, which no backend
can be confined from. It needs a pin on the reviewed commit, checks on
the integrated result, and an integration tree per run. All of that
exists only to support parallel writers, each reviewed before its work
counts.

This design gives that up: steps share one tree, review is a step, and
a run ends with its changes in the tree for a person to read. Parallel
writers and per-step isolation go, and with them all their machinery.

### 25.2 Names

| was (§24) | now |
|---|---|
| crew | a **workflow** (the recipe) and its **run** (one execution of it) |
| node | **step** |
| `crew_start(brief, budget)` | `workflow_run(recipe, spec, …)` |
| `crew_status`, `crew_steer`, `crew_stop` | `workflow_status`, `workflow_steer`, `workflow_stop` |
| `crew_land` | none |
| crew branch, node branches | none |
| the session's spec | the session's **specs** (§25.3) |
| Crew tab, the TUI's `crewLines` | the run's block in the thread (§25.9) |
| profile slot `crew-<tier>` | the step's role's slot (§25.4); a recipe has no tiers |
| `Engine.CrewHolds` | `Engine.RunHolds` |

**Crews already in an old database** are stopped on upgrade, if any was
open, and left unread. Their hidden node cards stay hidden.

### 25.3 A session's specs

A session holds a **list of specs, one per piece of work**. Most
sessions have none.

**Store.**
- A `session_specs` table holds `(feature_id, n, title, path,
  created_at)`, where `n` counts from 1 per session.
- `spec_revisions` gains `spec_n`, so revisions are numbered per spec.
- A new spec's file is `.gummi/specs/<ID>/<n>-<slug>.md`.
- A session that already has a spec gets that file registered as spec 1,
  at its old path, with its revisions.

**Tools.**
- `spec_start(title)` adds a spec and returns its number.
- `spec_view(spec?, section?)` and `spec_replace_section(spec?, section,
  body)` take the spec to act on.
- `spec_annotate(spec?, anchor, note)` does the same.
- `spec_list()` names every spec with its state.
- The `spec` argument may be left out while a session has a single spec.
  With more than one, leaving it out is refused, so no write lands in the
  wrong spec.

**Item ids.** Wherever two specs meet in one view, a done-when item is
named by its spec as well, as in `S2/DW-1`.

**A run freezes the revision it starts on.** An edit during the run is
the next revision, and is offered to the run as a steer.

**The report goes back into its spec.** It is written into a Report
section, replacing the last one, and the thread carries the same text
as a turn.

**A spec's state comes from its runs:**

| state | meaning |
|---|---|
| `draft` | never run |
| `running` | a run on it is open |
| `met` | its last run passed every item with a check, and left none unjudged |
| `partial` | anything else |

A spec may be run again.

**The faces.** The Spec tab becomes **Specs**: a list of specs with each
one's state, revision, runs and done-when count, and the selected spec
beneath it. Editing and annotating work as they did. The TUI's spec view
(alt+s) gains a list to switch between specs. `gummi spec` takes
`--spec <n>`.

### 25.4 Recipes

A recipe is a YAML file. gummi looks under `.gummi/workflows/<name>.yaml`
first, then in its bundled recipes (`internal/workflowrecipe/bundled/`),
so a workspace may override a bundled recipe by name. The session agent
may also hand a recipe over inline (`recipe_yaml`), drafted for the work
in hand. The recipe is copied into the run when the run is proposed, and
what the person allows is that copy.

```yaml
name: deliver
description: Design, build and validate a spec's done-when items
inputs: {}                # name: hint, filled at workflow_run
steps:
  - id: design
    role: contractor
    prompt: |
      Write the design for the spec below into {{artifacts.design}}.
      Do not edit code.

      {{spec}}
    artifacts: { design: design.md }
  - id: build
    repeat:
      max: 3
      until: { verdict: APPROVED, in: review }
      onMax: pause
      steps:
        - id: implement
          role: implementer
          prompt: |
            Implement {{artifacts.design}} for the spec below. If
            {{artifacts.review}} exists, address its findings first.

            {{spec}}
        - id: reviews
          parallel:
            join: allSettled
            steps:
              # two reviews on two models: review-b reads the profile's
              # reviewer-alt slot, its reviewer when the profile has none
              - { id: review-a, role: reviewer,                     prompt: "Review {{run.diff}} against the spec. List findings.\n\n{{spec}}" }
              - { id: review-b, role: reviewer, slot: reviewer-alt, prompt: "Review {{run.diff}} against the spec. List findings.\n\n{{spec}}" }
        - id: verdict
          role: reviewer
          prompt: |
            Merge these reviews into {{artifacts.review}} as JSON:
            {"verdict": "APPROVED" | "CHANGES", "findings": [...]}.
            {{review-a.output}}
            {{review-b.output}}
          artifacts: { review: review.json }
  - id: validate
    role: reviewer
    prompt: |
      Read {{run.diff}} against the spec's done-when items. End with one
      line per item: MET: DW-n or UNMET: DW-n <why>.

      {{spec}}
```

**Fields.** A recipe has `name`, `description`, `inputs` and
`steps`, which run in order. Each entry of `steps` is exactly one of:

- **a step**:
  - `id` and `role`, required;
  - `prompt`, required;
  - `slot` and `artifacts` (name to file name), optional;
  - `captureOutput`, optional and true by default.

  A step has no `envelope`: a run estimates nothing and spends from the
  session's budget (§25.7). It has no `tier` either: its model is the
  person's choice, made per role in their profile (below). A recipe from
  before either change still parses, and `Check` warns that the field is
  ignored.
- **`parallel`**: an `id`, `join` (`all`, `allSettled` or `any`) and
  `steps`, which are plain steps.
- **`repeat`**: an `id`, `max`, an `until` stop condition, `onMax`
  (`abort`, `continue` or `pause`) and `steps`.

**Roles.** `scout`, `planner`, `contractor`, `implementer`, `tester`,
`scribe` and `reviewer`.
- Each role has a prompt preamble and a **profile slot** of its own
  name: a step's model is looked up in the slot its recipe names
  (`slot:`), then its role's, then — for a `planner` or `contractor` —
  `architect`, then `implementer` (`Engine.stepRole`). A recipe names a
  slot only to run two steps of one role on different models, as
  `deliver`'s second reviewer does; by default it never picks a model's
  size, which is the profile's to decide. A stored step from before
  carries a tier, read as no slot.
- **A step may pin its agent and model** (`agent:`, `model:`; amends the
  rule above). Either wins over the slot and the role: an `agent` alone
  runs that backend's default model (`opencode` has none: it needs a
  `provider/model`), a `model` alone runs on the backend the profile
  gives the step (`Engine.stepRoleFor`). A step card takes only the
  backend and model from its slot, pinned or not: a slot's
  `output_token_max` does not reach a workflow step. An agent gummi has
  no adapter for, or a model that agent refuses, is refused when the run
  is proposed, a schedule fires or a revised plan is checked, at any
  depth, a dispatched recipe included (`agentProblems`,
  `CheckSessionModel`, the refusal a session's own picker gives); a model
  alone is checked then against the backend the session's profile gives
  the step (`modelProblems`), and again when the step starts — the only
  check a dispatched recipe's model-alone step gets, its profile being
  its own session's. A pinned step records no slot, and the faces show
  no slot for it. It is for a recipe a person
  writes for their own setup — a recipe that pins a model only runs where
  that backend has it — so the bundled recipes stay on roles.
- `scout` and `reviewer` are **reading roles**: their preamble tells them
  to change no files. They have the full shell any step has — a reviewer
  runs the tests — so nothing enforces it; a reading step whose end
  snapshot differs from its start is flagged in its row and the run's
  report, as evidence, never a refusal.
- Every other role writes.

**Templates**, filled when a step starts:

| template | is |
|---|---|
| `{{spec}}` | the frozen spec revision's text |
| `{{inputs.<name>}}` | an input given at `workflow_run` |
| `{{artifacts.<name>}}` | the absolute path of an artifact in the run's directory |
| `{{previous.output}}` | the output of the step before |
| `{{<id>.output}}` | that step's output; inside a `repeat`, its latest iteration |
| `{{run.diff}}` | a file gummi writes before the step starts: the diff from the run's starting tree to now |

Output is the step's final message. It is wrapped in delimiters naming
its step, so text from one step cannot pose as instructions to the
next.

**The run's directory.** `.gummi/runs/<ID>/<n>/` holds the recipe copy,
the artifacts, the diff files and each step's output. It is in the
workspace, not the tree, so it is never in the session's diff. Every
step may write it: a backend that cages its writes to the working
directory has the run's directory opened beside it. A step that ends
without writing an artifact it declared has it captured from its
output — the last JSON object for a `.json` artifact, else the last
fenced block — and the run's notes say gummi wrote it.

**Stop conditions for `repeat`.** After each iteration, a `repeat` checks
its `until`:

| `until` | the loop ends when |
|---|---|
| `checks` | every done-when check of the frozen spec passes in the tree |
| `{verdict: <word>, in: <artifact>}` | the artifact's JSON field `verdict` equals the word |
| `{contains: <text>}` | the last step's output contains the text |

At `max` without the condition, `onMax` decides:
- `abort` ends the run as partial;
- `continue` goes on to the next step;
- `pause` asks the person whether to run one more iteration, continue,
  or stop.

A condition gummi **cannot judge** is never counted as not met: the
artifact a verdict is read from was never written (nor captured), or a
check ran no tests (§25.6). The run waits on the person, whatever
`onMax` says — "the loop can't judge: `review` (`review.json`) was
never written" — with the same answers: one more iteration (which does
not count against `max`), continue, or stop.

**Joins for `parallel`.** Each branch is a step of its own, in the same
tree.

| `join` | the block |
|---|---|
| `all` | fails as soon as one branch fails, and stops the others |
| `allSettled` | waits for every branch, and fails only if all of them do |
| `any` | settles when one branch succeeds, and stops the rest |

**A step fails** in two cases:
- its backend errors;
- its output's last line is `FAILED: <why>`.

A failed step outside a `parallel` ends the run as partial.

**A planner step** may end its output with a fenced `gummi-workflow`
block of steps. Those steps replace the run's remaining steps, once they
pass `Check`, and go to the person as a revised plan to allow. A step
that is already running is never rewritten.

**`workflowrecipe`** is a pure package: `Parse`, `Check` and the bundled
recipes. `Check` refuses a recipe that cannot run:
- an id that is missing or duplicated;
- an unknown role, or a slot that is not a slot name;
- a `{{…}}` that names no input, artifact or earlier step;
- a `repeat` without a `max` or an `until`;
- a `parallel` without a valid `join`;
- an `until` naming an artifact no step in its body declares.

`Check` **warns** when one `parallel` block holds more than one writing
role, because those steps can overwrite each other's work, and when a
step still names an `envelope` or a `tier`, which are ignored. It sets no floor:
whether a recipe reviews, tests or designs is its own choice.

**The bundled recipes:**
- **`deliver`**, as above;
- **`investigate`**: a scout, then a scribe writing a `report.md`
  artifact, with the report as the run's output;
- **`fix-until-green`**: one implementer in a `repeat` with
  `until: checks`, `max: 5` and `onMax: pause`.

### 25.5 Proposing and allowing a run

`workflow_run(recipe | recipe_yaml, spec?, inputs?)` proposes a run. The
call returns at once, saying the run is proposed.

- It is refused when the recipe fails `Check`, when the spec does not
  exist, or when a run is already queued behind an open one. The queue
  is one deep.
- The proposal goes to the person as a **decision in the thread**. It
  shows:
  - the recipe's step tree, with each step's role, the model it runs
    on under the session's profile, and its prompt's first paragraph;
  - `Check`'s warnings, and gummi's own on running this recipe on this
    spec: a loop until the checks pass on a spec with none, and checks
    on a recipe with no step that writes code;
  - the spec and the revision it is judged against, frozen as the
    proposal is made: what the person reads is what the run is judged
    against, whenever it starts;
  - every done-when check command the run will execute;
  - the run's **baseline**, each checked item as it stands before the
    run (below), or that the checks are still running;
  - what is left in the session's pool, which the run spends from —
    nothing is estimated;
  - whether the tree holds uncommitted work.
- The person has four answers:
  - **Allow**;
  - **Always allow**, which allows this run and every later run of the
    session without asking; the session's budget is the only cap;
  - **Deny**;
  - typed **feedback**, delivered to the session agent as a turn, which
    may propose again.
- A run on a session whose runs are always allowed is allowed without
  asking. No agent allows its own run.
- A run allowed while another is open waits as **queued**. It starts
  when the open run ends and the session's turn reading its report is
  over. A person may **withdraw** it until then. An edit of its spec
  after the proposal does not move what it is judged against.
- **Always allow is visible and revocable.** The newest run's block of a
  session whose runs are always allowed says so, with **Ask again**,
  which makes the next proposal wait on the person.
- **A recipe the session wrote inline is the person's to keep.** From a
  proposal on, the run's block offers **Save as recipe…**: a person names
  it, and gummi writes the recipe as the session gave it to
  `.gummi/workflows/<name>.yaml`, where any session may then run it by
  name. The agent cannot: `.gummi/workflows/` is outside its write cage,
  and no session tool, MCP call or headless path saves one (§25.9).
- **The baseline.** A check that already passes before a run, or a `go
  test -run` that matches no test, proves nothing about what the run did,
  and its end result alone cannot tell. So gummi runs the spec's done-when
  checks as the run's baseline: in the background when the run is
  proposed (`workflow_run` still returns at once) — unless another run's
  steps are writing the tree, whose half-done work the checks would read
  — and again before the first step starts — allowed, always allowed or
  dequeued — when the tree or the frozen revision has moved since. It records each checked item's
  result (passes, fails, or ran no tests by `verify.RanNoTests`; an item
  judged by a step has none) and the tree snapshot it ran at.
  - An item that **already passes** or **ran no tests** is named on the
    proposal ("DW-2 already passes before the run", "DW-3 ran no tests"),
    and the session agent is told which and why in a turn under the
    workflow's actor, so it can rewrite the check, withdraw the proposal
    and propose again, or carry on.
  - The report and the ended block read each checked item before and
    after: "DW-1 failed before the run, now passes", "DW-2 passed before
    the run, still passes".
  - It is evidence, never a gate (decision 28): no proposal is refused
    and no run is paused over what it finds. The first step waits only
    for the checks to have run; checks that cannot run at all leave the
    run without a baseline.
- **Checks a step adds** (`engine/workflowadded.go`). Whoever writes the
  spec fixes its scope before anyone has looked: a step that maps the
  work may find the same mistake in a sibling, or a behaviour a fix must
  keep, that no item names, and the frozen spec could not say so. A step
  may end its output with a `gummi-done-when` block; each check in it
  whose command no item already runs joins the run as its next item
  (`DW-n` after the spec's own, its statement marked "added by" the
  step). The steps after it are given them under the spec, a repeat's
  `until: checks` holds until they pass too, and the end checks, the
  report and the spec's state read them like the frozen ones. When the
  tree is still the one the run started on, gummi runs them as the step
  ends and keeps the result as their baseline; once a step has written,
  there is no before to take. They only add: a step cannot drop, change
  or weaken a frozen item, the spec is never written, and nothing about
  the run's plan changes — decision 28's rules stand, a stricter test of
  the same plan asking nothing new of the person.

- **How a run gets asked for.** An agent left to judge for itself,
  mid-request, whether work is worth a run mostly just starts on it. So
  the decision has three ways in besides the agent's own, and none of
  them allows anything: the agent still proposes, and the person still
  answers.
  - **The person asks.** `/workflow [recipe] [on spec n] [what for]` is
    a session command on every face: the transcript keeps the line, and
    the agent is told to write or update the spec and propose the run,
    not to do the work. A session spec's **Propose a run** (the web
    page's Specs tab, `w` in the terminal's spec view) sends
    `/workflow on spec n`.
  - **Triage.** A session's first request of at least eight words,
    from a person, is read before its agent hears it by a tool-less
    one-shot on the profile's `triage` slot (else `scout`, else
    `scribe`, else the session's own model), given the request and the
    recipes' descriptions, which answers `TRIAGE: here` or `TRIAGE:
    <recipe>`. A recipe reaches the agent as a note ahead of the request
    and the person as a line on the thread; `here`, an unknown name, a
    failure or a timeout send the request as it was. Its cost goes on
    the card. The same one-shot names the session on a `NAME:` line:
    a session minted from a sentence is titled with its first one and
    its branch spelled from that title's words, filler dropped
    (`domain.BranchSlug`); while the title is still that one, the name
    retitles it and moves its branch to the name's words before the
    agent hears the request (`engine/sessionname.go`), keeping the
    opening words as its one-liner. A branch that was pushed, is in a
    stack or has repositories attached keeps its name; the title
    changes regardless. A person renames a session the same way with
    `/rename <title>` in the composer, or the **rename…** row of its
    menu (the TUI puts `/rename <title>` in the composer, the web page
    asks for the title); `/rename` alone says its name and branch, and
    a rename waits while a turn or a workflow run writes the tree.
    `triage: off` in `config.yaml` turns it off
    (`engine/triage.go`). `make triage-eval` scores the prompt on a real
    model against the labelled requests in
    `engine/testdata/triage-eval.yaml`; add a case there when triage
    misreads a request.
  - **The hint.** The session's workflow hint asks for the decision
    before the first edit of each new piece of work, and names the
    shapes of work each bundled recipe is for.

### 25.6 The tree and its snapshots

> *Amended by §27.6 (decision 30): a run's snapshots, step diffs, reader-change flag and Revert run span the session's own tree and every attached repository's.*

- **Steps run in the session's worktree.** A step is a hidden card row,
  child of the session, carrying the run's number and the step's path:
  `build/2/implement` for iteration 2 of `build`. Its agent's working
  directory is the session's checkout. It has its own backend
  conversation, model and spend.
- **Nothing is committed by gummi.** A step's role preamble tells it not
  to commit. One that commits anyway is not refused, and the report
  names the commits.
- **Snapshots.** At a run's start, and at each step's start and end,
  gummi records the tree with `worktree.Snapshot`. It copies the index to
  a temporary file and runs `git add -A` and `git write-tree` against
  it, through `GIT_INDEX_FILE`. This touches neither the branch, the
  real index nor the files. A snapshot is a tree hash stored on the
  step or the run. Two diffs come from snapshots:
  - a step's own diff runs from its start snapshot to its end snapshot;
  - the run's diff runs from the run's start snapshot to a fresh one.
- **Stop** interrupts the running steps at their next tool boundary,
  marks the run stopped, and reverts nothing. Both faces confirm it
  first.
- **Revert run** is a person's act, from the run's block (the web
  page's Revert run…, the terminal's `v`). It lists every path that
  differs between the run's start snapshot and now. Once the person
  confirms, each path is put back to its start state (`git restore
  --source=<tree> --worktree`), and a path the run created is deleted.
  Each path's index entry goes back too, to the index recorded as a tree
  when the run started (`worktree.IndexTree`, `RestoreIndex`), so a
  reverted file the run staged is not brought back by the next commit.
  This also undoes anything the session changed during the run, and the
  list says so, naming the runs after it and the commits made since.
  Revert is refused while a step is running, while the run itself is
  running (pause it first), and while gummi runs its checks.
- **The run's own diff and artifacts** are read from its block: the
  diff from its start to the tree now, and each artifact its recipe
  declares (a report, a design, a review) once written.
- **Revert to a step.** An opened step offers to put the tree back to
  where it started — the snapshot at its boundary — keeping what the
  steps before it did, behind the same preview and confirm (the page's
  Revert to before this step…, the terminal step view's `v`). Its index
  entries go back to the run's start, as a step stages nothing.
- **Retry.** A person retries the session's newest run that ended
  partial or stopped, while no other run is open or queued
  (`domain.RetryRefusal`): each step that failed or stopped runs again at
  its own path, its earlier attempt kept beside it as `<path>@<k>` with
  its card and its spend; a loop a person stopped asks again at its max;
  every step that finished stays. The run is open again, against the same
  frozen spec, its diff still from where it first started. It is a
  person's act from the faces (Retry failed steps, the terminal's `t`);
  no session tool retries.
- **A follow-up waits while the run writes the tree.** A person's message
  to an ended step may change the tree; while a step of the open run is
  at work it is refused, or its edits would land in that step's diff.
- **Run end.** When the last step settles, gummi runs the frozen spec's
  done-when check commands in the tree (`verify`) and records each
  result. Each check is bounded (fifteen minutes, or the check's own
  timeout) and killed with its process group past it; one that timed out
  or never ran leaves its item unjudged. A tree gummi cannot snapshot at
  the end still ends the run, partial, its report saying why.
  - An item without a check is judged by the last step's `MET: DW-n`
    or `UNMET: DW-n` line, if it has one. Otherwise it is unjudged
    (`–`).
  - An item whose check passed but ran no tests — a `go test` with
    `-run` whose output says "no tests to run" — is unjudged too, "the
    check ran no tests", never ✓: it proves nothing.
  - The results are the run's evidence: they are in its report and its
    block, and they set its spec's state (§25.3).
- **No validated mark.** The crew-verified mark (§24.8) has no
  successor: gummi pins no tree as validated and the head carries no
  mark, and there is no Re-check (§25.14).

### 25.7 Conducting a run

`workflowpolicy.Decide(Snapshot) []Action` is pure. `Engine.WorkflowTick`
builds the snapshot, executes the actions, and is called from the faces'
tick loops (`ui/workflowloop.go`) and the headless driver. In order:

1. Wind down when stopped: interrupt running steps, then end the run.
2. Pause when the session's pool is spent (§24.7a), and do nothing else
   — unless nothing is left to start, settle or judge: the end checks
   and the report spend nothing, so the run ends.
3. Settle a `parallel` whose join is decided, and a `repeat` whose
   iteration has ended: check `until`, then iterate, end the loop, or
   apply `onMax`.
4. Start the next ready step or steps. These are the next sibling in
   order, or every branch of a `parallel`.
5. When nothing is left, run the end checks (§25.6), write the report
   into the spec and the run's directory, and send it to the session as
   a turn under the workflow's actor.

**Money** is one pool, and nothing estimated against it:
- the run binds to the session's envelope, and a step's backend may
  spend what the pool has left;
- nothing forecasts what a run will cost, and no step is a runaway; the
  planner plans without regard to money;
- a run goes on until the pool is spent, then pauses for a top-up and
  never drops work;
- only a person raises the money.

**Questions.** A step's `ask_user` is a decision to the person, drawn
under the step in the run's block. The step waits for the answer. An
answer names the question it was read off, and is refused once the step
asks another. A loop whose `until: checks` finds no checks, or a check
that timed out, cannot judge itself: it asks the person, as one whose
check ran no tests does. A person's own words to a run's question that
are none of its answers — a loop at its max, the budget, a parked run —
go to the session agent as a turn, and the question stays open for the
person alone to answer.

**Steering.** `workflow_steer(note)` from the session agent, and an edit
to the frozen spec, reach every running step as a turn. A step that has
not started yet sees the note in its prompt. A spec edit's note names the
file in the run's directory holding the edited spec, rather than copying
the whole spec into every step again.

**Nothing resumes itself.** *Amended by decision 32 (§28.6): a run
waiting only in a `watch:` resumes its polling by itself; the first
handler after the restart waits for a person's Resume.*
A quit parks a running run, keeping its state
and stopping its steps. A run is parked by the first tick of a gummi
that did not start or resume it — at once, when a board starts — and
asks its person: **Resume the run** or **Stop the run**, the session's
pinned decision, so the board counts it as needing them. Its steps read
as stopped where they were. It runs again only when a person resumes it;
`gummi resume` is a person resuming it. A run's clock leaves its pauses
out: its elapsed time is the time it worked.

### 25.8 The session during a run

- **One run open at a time.**
- **The session agent keeps talking.** Every turn it takes during a run
  begins with a note saying that a run is changing the tree and that it
  must not touch files until the report arrives. Nothing enforces this.
  A file it changes shows in the next step's snapshot as if the run had
  changed it.
- **The session cannot land while a run is open** (`Engine.RunHolds`, in
  `cardops`).
- **After the report**, the session agent reads it and the diff, then
  commits on purpose, or the person does. gummi never commits for it.

### 25.9 In the thread

Each run is **one block in the session's thread**, updated in place.
`threadfold` takes the session's runs as an input
(`Options.Runs`, read from the store). It does not take an event per
step action. The block is an item keyed `run:<n>`, carrying a revision
counter so a face redraws only what changed. The web page and the TUI
both draw it from that fold.

The block holds:

- **A head**: the recipe, the run's number, its spec, its spend, elapsed
  time, and Pause, Resume and Stop.
- **A strip** of the recipe's top-level entries, each in its state:
  - a `repeat` shows its iteration, as in `build ↺ 2/3`;
  - a `parallel` shows its branches joined by `∥`.
- **A row per step that has run or is running**:
  - its path, role, title or id, and spend;
  - its live last action, or its end summary;
  - a flag when a reading step changed files ("changed 2 files although
    it only reads");
  - **Open**.

  A finished iteration of a `repeat` folds to one row with its verdict.
  A step waiting on the person is ringed, and its question is drawn
  under it with the recommended answer first. **Every step at work reads
  as running and stands out**: in the TUI its glyph is the spinner and
  its bar, name and line are lit; on the web page its row is tinted and
  barred and its dot breathes. Side-by-side steps of a `parallel` are
  each marked so.
- **A footer**: the files the run has changed so far, uncommitted, and
  **Revert run…**; for a recipe the session wrote inline, **Save as
  recipe…** (the TUI's `s`), and once saved "saved as <name>" in its
  place.

While the run is proposed or queued, or open before its first step, the
block carries its **baseline** (§25.5) under its head: "running the
done-when checks before the run…", then a line per checked item, one
that proves nothing marked amber. The proposal's card says the same.

An ended run folds to:
- its result per done-when item, a checked one with its baseline before
  it ("DW-1 failed before the run, now passes");
- the files it left changed;
- the commits steps made, if any;
- Revert run;
- a link to its report.

**Where a block stands.** Each run's block stands where the run was
proposed, among the history, in both faces, and `threadfold` alone says
where: `RunPlace` puts it before the first turn of the session's
conversation written after its proposal that is no part of an activity
row (a tool call, a thought), after every turn when nothing
came since, and among the thread's settled items (`placeRuns`, by time)
when it predates every turn shown. The terminal draws the whole
conversation and places each block by `RunPlace`. The page's live block
carries only the newest turns, so the server places the runs among them
(`Conversation.Runs`, on `GET …/live` and the thread's own live block) and
the page draws a run it does not place there among the settled items. No
face places a run by a rule of its own, and none stacks runs at the
bottom.

**An ended run folds to one line** once it is no longer the latest news:
its state's glyph (✓ done, ◐ partial, ■ stopped), and `deliver · run 2 ·
spec 2 “Close the TUI's gaps” · met 5/5 · $2.92 · 14 files ▸` — the
state's word after the run for partial and stopped, the checked items it
met of those checked, its spend and the files it left changed. The run
that ended most recently stays whole until the person says something to
the session after it ended (a line of theirs, never a run's report or an
objective's turn: `PersonSaid`) or another run is proposed; every older
ended run is one line. `threadfold.FoldRuns` decides it (`Run.Folded`,
`Mark`, `Line`; folding moves the block's `Rev`), so both faces fold the
same runs. An open run (proposed, queued, running, paused) is always
whole. Activating the line unfolds the whole block as above; activating
the block's fold (▾ on the page, enter on its head in the terminal) folds
it again. That unfolding is the face's own, kept per card and run until
the person leaves the card. In the terminal enter on a whole ended run's
head folds it, and its steps' line is a cursor stop of its own that enter
unfolds.

**Each spec lists its runs**, newest first: the run's glyph, number and
recipe, its state, the checked items met, its spend and when it ended —
on the web page's Specs tab under the spec's bar (`Spec.Runs`), and in the
terminal's spec view under the list of specs. A row jumps to the run's
block in the thread and unfolds it: the page's Jump (scroll and flash),
the terminal's alt+w (a digit, 1–9, names the row).

**A live strip stands above the composer while a run is open** (running
or paused; never while it is proposed or queued, and gone once it ends),
so a person talking to the session keeps the run in sight after its
block has scrolled up the thread. The block stays where the run was
proposed. The strip is `threadfold`'s too: `RunStrip`, folded with the
block from the same run, steps and live sessions and carried on it
(`Run.Live`), so its words move the block's revision. It says one of:

- **running**: a live mark, `<recipe> · run <n>`, the recipe's top-level
  entries as the block's strip has them (done `✓`, the current one lit, a
  `repeat` as `↺ i/max`), the active step's id and its live last action
  (side-by-side steps' ids joined by `∥`), the spend and the elapsed time;
  **↑ Jump to run**, and **Open <step>** — **Open… ▾**, a pick of the live
  steps, when several work side by side;
- **a step asks** (amber): `<step> asks: <question>`, **Answer**, which
  opens the step on its question, and Jump;
- **the run's own decision** (amber): what it waits on, answered in the
  pinned decision;
- **paused on a spent pool** (grey): "paused · the session's $X is spent ·
  top up to carry on", **Top up…** (the session's budget action, whose
  raise carries the run on), and Jump;
- **paused by a person** (grey), with **Resume**.

Jump scrolls the thread to the run's block and flashes it. A narrow strip
gives up the recipe's entries first, then the money and the time; the
run's name, the step's action and Jump stay.

- **On the web page** the strip is a row of the dock, directly above the
  composer (a phone's too, compact), updated in place part by part on
  every refetch. A pill at the top of the thread, "↑ <recipe> · run <n>
  is above", shows while the open run's block is scrolled out of view
  above (an `IntersectionObserver`), and jumps to it.
- **In the TUI** it is one line on the composer, with an `alt+w jump`
  hint. alt+w jumps: the thread scrolls the open run's block into view
  with the cursor on its active step's row, enter opens it and esc gives
  the keys back. The line takes its row from the body: on a short
  terminal it is the first pinned row to go, before the head, an open
  decision or the composer give up one of theirs.

The proposal and a revised plan are decisions in the thread (§25.5).
The **Crew tab**, `ui/webcrew.go`, `web/assets/crew.js` and the TUI's
`crewLines` are removed.

### 25.10 Opening a step

**Open** shows the step beside the session's thread. On the web page it
is a right-hand panel; in the TUI it is a pushed view with a crumb back,
such as `Session › deliver run 2 › build ↺ 2 › implement`.

- **Head**: the role, the model and the slot it named, its spend, and
  its state.
- **Transcript**: the step card's own thread, live.
- **Diff**: the step's own diff, from its snapshots.
- **Brief**: the prompt as filled in, and the artifacts it reads and
  writes.

A step has these three tabs in both faces, and no **Terminal**: it works
in the session's tree, whose terminal (§20.5) is the session's own.

**A person may talk to the step's agent.**
- While the step runs, a message is a turn to it, and the step's output
  is whatever it says last.
- After the step has ended, a message resumes its conversation for a
  follow-up. That can change the tree, which the run's diff then shows.
  It does not re-run the steps that came after it.
- Every message is a turn on the step's card, paid from the session's
  pool.
- The run's other steps see only what the step's output and artifacts
  carry.

### 25.11 The headless driver

> **Rebuilt.** The `run`/`resume` pair that stopped on every question
> with `needs_you` was removed with the rest of the command-line verbs;
> `gummi run` came back alone, as an unattended drive for evals and CI.
> There is no `resume`.

`gummi run --envelope <dollars> "<prompt>"` (`internal/driver`) mints one
session, sends the prompt as its first turn, and drives it to its end
with no person at the keyboard.
- It always allows the session's workflow runs: the person typing the
  command allows them once, up front, and the envelope is their only
  cap.
- A question is answered on the person's behalf, and the answer is an
  `answered` line on the stream: an `ask_user` (the session's or a
  step's) takes its recommended option (`engine.RecommendedOption`), or
  is told to decide for itself when it offered none; a parked run is
  resumed; a `repeat` at its max, or one that cannot judge its `until`,
  carries on; a revised plan is declined, so the run keeps the plan it
  was allowed with.
- Money is never decided: a spent envelope, or a run paused on a spent
  pool, ends the drive as `exhausted` (exit 3) and leaves the card for a
  person to top up on the board. A run a person paused from the board
  ends it as `paused` (exit 2); a quiet past `--timeout` as `timeout`
  (exit 4); a failure as `error` (exit 1).
- It is done (exit 0) when the session is idle with no run open or
  queued and has answered every run's report. The session stays on the
  board, its branch uncommitted to by gummi, for a person to read and
  land.

`gummi run --workflow <recipe>` (or `--workflow-file`) runs a workflow
on inputs instead of sending a prompt — a bug report to triage, a spec
to deliver.
- Its inputs are `--input name=value` (`@file`, `-` for stdin), each
  repeatable, over an `--inputs` YAML or JSON file. A recipe declares an
  input as `name: hint` (required) or `{hint, required: false}` /
  `{hint, default}` (optional); one it does not declare, or a required
  one left out, is refused before any card is minted
  (`workflowrecipe.ResolveInputs`, `Engine.WorkflowCheck`). A step is
  given each input between delimiters naming it (`WrapInput`), as it is
  a step's output: an input is text from outside the run.
- The session takes no turn first. gummi writes its spec: the one
  `--spec` gives, or one written from the prompt and the inputs, with a
  done-when item per `--check`. A run is still judged against a spec,
  so its report, its spec state and both faces work as for any run.
- Without `--spec` (or with `--no-session`) the report is the product:
  gummi keeps it from the session (`Engine.KeepReports`) and the drive
  ends with the run. With one, the session reads the report as for a run
  it proposed.
- The run's end is a `workflow_ended` line: its report, each artifact a
  step declared with its text, and the steps' `DECISION:` and `OPEN:`
  lines — what a caller posts back where the work came from. A run that
  ends `partial` makes the drive `partial` (exit 5).

### 25.12 What goes, and what stays

**Goes:**
- crews' branches;
- node landing (`CrewLand`, `SquashMerge` for nodes);
- the owns check and the one-writer rule;
- the reviewer gummi placed after every writing node;
- the conflict-integration path;
- the planner's tool set;
- `crewplan` and `crewpolicy`, replaced by `workflowrecipe` and
  `workflowpolicy`;
- the handoff grammar as a requirement. `DECISION:` and `OPEN:` lines
  are still gathered into the report when a step writes them.

**Stays:**
- money as one pool (§24.7a), with nothing estimated against it;
- a person's allowance of every run and every revised plan, or a
  person's "always allow" for the session's runs, with no agent allowing
  its own run;
- questions reaching the person;
- a session landing on a person's read of its diff.

**Decision 27 is amended.** Its "what does not bend, inside a crew"
becomes three rules for a run:
- a person allows the run and every revision of its plan;
- money is a ceiling no agent raises;
- a question the run cannot settle reaches the person.

Review, checks and design are what a recipe chooses, and the bundled
`deliver` chooses all three.

### 25.13 Deferred

- **Waiting on a command** without spending model turns, as a step kind
  (Kiro's `watch` node). *A single command step, `run:`, is decided in
  §26.3; a step that polls a command inside an open run stays deferred,
  since long waits belong to a schedule.* *A wait on the session's own
  PR is decided in §28 (decision 32); a wait on any other command stays
  deferred.*
- **Narrowing a step's tools**, per step: a step has every tool its
  role's session has, and a reading role is told, not caged, to change
  no files.
- **Recipes kept outside the workspace**, per person.
- **Isolated parallel writers**, as an option a recipe asks for: §24's
  branches and owns check, kept in history.

### 25.14 As built

The build follows §25, with these differences.

**Specs and proposals.**
- **A proposal needs a spec.** `workflow_run` is refused while the
  session has no spec, even for a recipe whose prompts never name
  `{{spec}}`: the run is judged against a frozen spec revision, and
  there is none to freeze.
- **A spec judged only by agents is never met.** A run meets its spec
  only when at least one done-when item has a check, every checked item
  passes and no item is unjudged, so a spec whose items are all judged
  by `MET:` lines stays `partial` after every run (§25.3, §25.6).
- **The validated mark was dropped.** It was built — a run whose checked
  items all passed pinned its end snapshot as the session's validated
  tree; the head read "validated at tree `<hash>`" or "changed since
  validation"; Re-check (`alt+v`, the web head's button) ran the checks
  again and moved it — and then removed: it restated what the run's
  results already say, while a person's read of the diff is the floor
  (decision 27). A run's end checks and their per-item results stay, in
  its report and its block, and a spec's `met` reads them
  (`domain.ResultsMet`). The store's `validated_tree` column is left in
  place, unread and unwritten.
- **Every run decision has a words row**, as an `ask_user` question
  does, named for what the words do: **Feedback** on a proposal, to the
  session agent; **Talk it over** on a revised plan, to its planner;
  **Another amount** on a paused budget, the credits to top up by. A
  `repeat` at its max offers one more iteration, continue or stop; a
  spent pool offers a top-up or a stop.
- **An answer names the decision it answers.** A run raises one decision
  after another, so `WorkflowAnswer` takes the decision's id, and an
  answer to a decision the run has since replaced is refused, saying
  what the run asks now. The board's picker, the web page (through it)
  and the headless driver pass the id of the decision they read.
- **In the headless driver a number is always an option**, on every
  decision; another amount to top up by is spelt with its unit, `300cr`
  or `$3`, and a bare number past the options is refused.

**Recipes.**
- **Ids are unique across the whole recipe**, not only among siblings,
  so `{{<id>.output}}` always names one step.
- **A `repeat` without `onMax` pauses** at its max.
- **A step's `envelope` is ignored, with a warning.** A run estimates
  nothing, so a recipe from before that names one still parses, and
  `Check` warns on the proposal (and on a revised plan) that it is
  ignored; the copy the run keeps drops it.
- **A step's path names its `parallel` too**: a branch of `reviews` in
  iteration 1 of `build` is `build/1/reviews/review-a`.

**Money.**
- **A run estimates nothing.** The proposal, the run's block, the step
  view, `workflow_status`, the report and the driver's NDJSON say what a
  run has spent, never what it might: there is no estimate, no forecast
  warning and no runaway. A run binds to its session's envelope and goes
  on until it is spent.
- **"Always allow" carries no amount.** It is kept in the old
  pre-authorisation's table (`crew_preauth`) as yes or no, and an amount
  a person set before reads as yes. `gummi run` sets it for the session
  it mints, so its driver stops on a proposal only for a session minted
  elsewhere whose runs are not always allowed.
- **A paused run's top-up is as much again as the envelope**, rounded up
  to 50 credits and never below 100; the person may top up by another
  amount, or stop the run. The decision no longer offers Continue, which
  answered the forecast warning.
- **The store keeps the old columns unread.** `workflow_runs.estimate`
  and `warned`, and `workflow_steps.estimate` and `allowance`, stay in a
  database from before, unwritten.
- **The NDJSON follows.** `created` carries `always_allow` instead of
  `preauth`; a `workflow` line carries `spent` instead of `estimate`; the
  `crew` line beside it drops `budget`; `workflow_runaway` is gone.

**Artifacts and checks.**
- **Steps write the run's directory on every backend that cages
  writes.** It lies outside the session's checkout, so on claude a
  verdict step's `review.json` was refused and `until: {verdict: …}`
  never held: the loop ran to its max. A step's backend is handed the
  directory (`SessionOpts.WriteDirs`, made before it spawns): claude
  takes it as `--add-dir`, opencode opens it to edit, write, read and
  `external_directory` beside the scratch directory. copilot and the
  headless backend cage nothing but their working directory, so they need
  nothing.
- **A missing artifact is captured from the step's output** when the
  step ends done (`workflowrecipe.CaptureArtifact`), before the step
  reads as done, so the loop is judged on it; one the output does not
  hold stays unwritten, and the run's notes say which.
- **A loop that cannot judge asks** (`RunDecideUnjudged`, the
  driver's `workflow_unjudged`). The run starts nothing while it waits,
  as at a loop's max; its block's folded iteration says "can't judge".
- **A check that ran no tests is unjudged**, at a run's end and in
  `until: checks` (where it makes the loop unable to
  judge). Detection is deliberately narrow (`verify.RanNoTests`): a
  passing `go test` naming `-run`, whose output says "no tests to run"
  and shows no package or test that ran. Such an item keeps the spec
  from `met`, and a run that would have been done ends partial: "a
  done-when check ran no tests".
- **Saving an inline recipe** (§25.5, §25.9), deferred at first, is a
  person's act from the run's block: `Engine.WorkflowSaveRecipe`, reached
  through `WebDocs.SaveRecipe` by the TUI's `s` (a one-line prompt) and
  the page's `POST /api/cards/{id}/runs/{n}/save`. It writes the recipe as
  the session proposed it (kept as `proposed.yaml` in the run's directory,
  since a revised plan rewrites `recipe.yaml`), refuses a name that is no
  recipe name (`workflowrecipe.CheckName`) or one a workspace or bundled
  recipe has (`workflowrecipe.Taken`), and offers nothing on a run of a
  named recipe or one that was denied. The name is kept on the run
  (`workflow_runs.saved_as`), so the block says it was saved after a
  restart and offers it no more. The suggested name is the recipe's own.
- **A run's baseline is evidence, never a gate** (§25.5). It is kept on
  the run (`workflow_runs.baseline`: the tree, the revision, each checked
  item's result, and the items the session was told of, so a baseline run
  again tells it only what is new). One taken at the proposal stands for
  the first step while the tree and the revision have not moved; else the
  checks run again in the tree the first step starts on, once — a tree
  that moves meanwhile does not send them round again. A checked item's
  end result carries its baseline (`ItemResult.Before`), which the report
  and both faces read as before → after. A proposal whose baseline was
  running when gummi stopped runs it again on the next tick. The driver's
  NDJSON is unchanged.

**Teaching the session agent.**
- **gummi ships two skills of its own** (§23.2): `workflows` — do the
  work yourself by default, and propose a run only when the person asks,
  when fresh-context design, review and validation pay for themselves,
  for a loop against failing checks or for a written investigation;
  write the spec first and run every check, each of which must fail
  before the run (a `go test` check greps its test's own `--- PASS:`
  line); which recipe for what; what to do while a run is open; how to
  read the report and commit — and `workflow-recipes`: the grammar, the
  roles, the templates, the lines a step's output carries, loops and
  joins, worked examples, with the bundled recipes as its references.
  Every recipe and done-when block they show is parsed and checked in a
  test (`TestEveryRecipeTheSkillsShowCanRun`), so they cannot drift from
  the grammar.
- **The session's hint carries the rules that matter most**, for a
  backend that takes no skills: do it yourself by default, run the checks
  red first, read the report and the diff and commit. Where the backend
  takes skills it names the two (`sessionWorkflowHintFor`).
- **`workflow_recipes` shows a recipe whole**: given a name, it answers
  with that recipe's YAML as written, for the session to adapt into a
  `recipe_yaml` of its own.

**Running.**
- **The live strip has two moods its design did not draw**: the run's
  own decision (a revised plan, a loop at its max or one that cannot
  judge) reads amber and points at the pinned decision, and a person's
  pause reads grey with **Resume**. A stopping run's strip says it is
  stopping. A step that asks is answered in the step's panel as well as
  under its row: the panel carries its question (`StepFace.Question`),
  which is where the strip's **Answer** goes.
- **A reading step is never spawned read-only.** The backend's read-only
  mode is a research session's allowlist (claude's is Read, Grep, Glob
  and a few git commands), which kept a reviewer from running `go test`.
  A scout or a reviewer gets the shell every step gets; its preamble
  tells it to change no files. When its end snapshot differs from its
  start, the paths are kept on the step (`RunStep.Changed`): its row says
  "changed N files although it only reads" (a folded iteration says a
  reading step in it did), and the report lists them under **Reading
  steps that changed files**.
- **A process conducts only the runs it holds.** A tick conducts a
  session's run only while its process holds the session's card lock,
  which it keeps while a run is open or queued, so two gummis never
  conduct one run and a quit's parking never reaches a run another gummi
  is conducting. The headless driver conducts its own session's run
  alone.
- **The person's Pause stops the steps' backends**, as a quit does.
  Resume starts them again, each with a turn saying the run carries on.
- **The envelope is a ceiling across a run's steps.** A step's own cap is
  what the pool had left when its backend spawned, so every step's spend
  re-checks the session's pool: once it is spent the run pauses at once
  and every live step's backend stops, as a Pause does. On a spent pool a
  step's cap is nothing, and no step backend spawns.
- **A step is briefed only into a running run.** Its brief goes under the
  run lock, once its backend is up, and only while the step is live and
  the run running and not stopping; a stop, pause, drop or join that came
  while the backend spawned leaves the step unbriefed, and a paused run
  briefs it when it carries on. A message to a live step of a paused run
  is refused until the run is resumed.
- **A session is not closed under its run.** A hand-off is refused while
  a run is open or queued, as it is while the session's agent is
  mid-turn, so a run's report always reaches the session it was for; a
  run still only proposed is withdrawn by the hand-off. Landing was
  already held (§25.8).
- **The run's tree is a person's to write only between its steps.** The
  Tree tab refuses a write, and says why, while the open run has a step
  under way or any step's agent is answering a follow-up; Revert run
  waits on the same, and on the session's own agent mid-turn.
- **A step's card stays open after its run ends**, so a person can go on
  talking to the step's agent (§25.10). It stays hidden from the board.
- **Revert run is offered from the faces only.** There is no
  `gummi workflow revert`.
- **Revert run stays offered after a revert.** The block's files are
  what the run left changed, which a revert does not rewrite. A second
  revert lists whatever differs from the run's start at that moment.
- **A revert is of what its preview listed.** The preview names the
  snapshot it was read from, and the confirm — on the page and in the
  terminal — sends it back; a tree that has changed since is refused,
  and the person reads the preview again.
- **Ignored files on disk at a run's start are not the run's.** A run
  lists them when it starts (`worktree.Ignored`, kept in its directory)
  and every snapshot it takes keeps them out whatever `.gitignore` says
  later: an edit of `.gitignore` under the run neither puts their
  contents in its diff nor lets a revert delete them.
- **A session's spec revisions with no spec file move to spec 0** on the
  upgrade to workflows, which no spec is, so a spec started later begins
  at revision 1. The upgrade reads the version inside its own
  transaction, so two gummis opening an old database run it once.

**In the thread.**
- **A run item's `Rev` is a hash of what the block draws**, not a
  counter, so two folds that draw the same block carry the same `Rev`.
- **A step's row names the step by its id.** A recipe gives a step no
  title.
- **The terminal's spec view starts a spec too.** `+` prompts for a
  title and starts the spec through `Engine.StartSessionSpec`, refused
  where the web page's **+ New spec** is (`newSpecRefusal`: only an open
  session takes one); `[` and `]` still switch between a session's specs.
- **A step has no terminal, in either face, by design.** A person's
  shell is the session's own — the step runs in the session's tree, not
  one of its own, so there is nothing a step-level terminal would open
  that the session's doesn't already.
- **A step's transcript reads as the session's thread, in both faces.**
  The terminal's step view is laid out by the thread's own regions
  (`composeThread`, `cardPageChrome`): its tabs and crumb stand where the
  card page's tabs and crumb do, its masthead where the card's does, its
  question is pinned above the composer as the card's decision is, and
  its composer is the thread's. Its conversation is drawn by the same
  function as the session's (`conversationLines`; on the page, `talk`).
- **A step's row reads its own session, not the store's last word.**
  The stored state moves between running and asking only when the run is
  ticked, so a step that had just asked, or had just been answered, read
  as it was until the next tick (up to the ten-second poll). The fold now
  takes a live step's state from its session (`threadfold.StepLive`): an
  open question is asking, anything else running; a planner's revised
  plan still waits on the person. An ended step a person is talking to
  reads running ("a follow-up · …"), and the strip reads live steps as
  their rows do.
- **A tick that starts steps tells the faces at once.** The step rows are
  written running by the tick, but the faces were told only once each
  step's backend had come up, which on a real backend takes seconds; the
  block showed the step before done and nothing at work meanwhile.
- **Ended runs no longer pile up at the bottom of the page's thread.**
  The page dropped every run from the settled items and drew runs only
  inside the live conversation, before the first line a person typed
  after each; runs from earlier stretches stacked at the start of the
  live part, and a run with no typed line after it (answered through
  decisions, its report gummi's turn) fell to the very bottom. Placement
  is now `threadfold`'s alone (`RunPlace`, the settled items' `placeRuns`
  and `Conversation.Runs`), read by both faces, and an ended run folds to
  one line once it is no longer the latest news.
- **A step's transcript follows its newest line**, as the thread does:
  it opens at the end, follows while the reader is there, and stays put
  while the reader has scrolled back (the terminal's BG-042 rule).
- **A run's end, its loop and its baseline cannot wedge it.** Done-when
  checks ran unbounded, so a check that never exited held the run's end
  (and Stop, which ends through it) for good; a tree gummi could not
  snapshot left the run open, retried on every tick; a spent pool paused
  a run that had nothing left but its free end checks; and one session's
  failing tick stopped every other session's. Each check is now bounded,
  a run with an unreadable tree ends partial saying why, a settled run
  ends on a spent pool, and the tick carries on past a session in
  trouble, naming it.
- **What a loop or a step cannot settle reaches the person.** `until:
  checks` on a spec with no checks read as met, and a check that timed
  out as failed; both now ask. A step's question answered on one face
  could be answered again, read off a stale page, into the step's next
  question; an answer now names its question's call. The session agent's
  own `ask_user` hid behind a run's decision in the picker; it now comes
  first. A person's words to a loop at its max were refused with "answer
  with one of"; they now reach the session and the question stays open.
- **A parked run asks to be resumed.** A run left running by a gummi
  that is gone was parked by the board's first tick, ten seconds in, and
  said so only in its block; the board showed nothing needing the
  person. It is now parked at the board's start and pins Resume or Stop;
  `gummi resume` answers it by resuming.
- **Revert run puts the index back.** It restored the files only, so a
  file the run had staged came back with the next commit.

## 26. Watches — an agent on a schedule that dispatches sessions

*Decided 2026-10-23 (decision 29); built — §26.8 says where the build departs.* This section amends §19.3d
(an agent may run a workflow and dispatch sessions), §19.9 (an agent may
propose a schedule, and a schedule may start a run) and §25.13 (a command
step is no longer deferred). Where it and those sections disagree, this
section wins.

A person wants an agent to watch something — an upstream repository's
releases, say — and, when it changes, to have work done in one of their
repositories. Before this section none of it connected: an agent could
not run a workflow, could not file a session and could not schedule
itself, and every fire of a schedule or a heartbeat was a model turn. An
hourly heartbeat that runs `curl` spent a turn every hour to learn that
nothing had changed, and still could not start the work.

A **watch** is not a new kind of thing. It is a schedule on an agent
that starts a run of a recipe whose first step is a command. The command
decides, with no model, whether there is anything to do. Only when there
is does a model run, and the run's `dispatch` step mints a session in a
repository the person allowed, with work of its own.

```
schedule ──fire──▶ run on the agent ──▶ run: gate ──quiet exit──▶ ends; row reads no-change
                                           │ exit 0
                                           ▼
                                     dispatch ──▶ new session in repo X, its own run
                                                        │
                       the agent reads its status, sends it turns ◀─┘
                       lands on a person's read of its diff, as a session always has
```

### 26.1 Why

- **The check is free.** A command answers "has anything changed" as
  well as a model does, for nothing. A model is spent only on a change.
- **The agent writes the automation, not each step of it.** The person
  describes the job once; the agent turns it into a schedule and a
  recipe and proposes them. Afterwards the agent may read how its fires
  ran (`cardrun`) and propose a cheaper recipe — a model step that
  always says "no change" made a command, a step's tier lowered. Each
  change is a new proposal.
- **No new concept.** The cron engine, the claim before a fire, the
  coalescing of missed fires and the envelope as the brake are §19.9's.
  The steps, their snapshots and the run's report are §25's.

### 26.2 A schedule that starts a run

A third schedule kind, `run`, beside `mint` and `heartbeat`: on its
cadence, start a run of the schedule's frozen recipe against one of the
agent's specs, on the agent's own card. It sends no turn.

- The run is on the agent. `WorkflowPropose`'s refusal of a home is
  lifted: the home is already a git repository (§19.3d), so the run's
  snapshots and diffs work there unchanged.
- A fire while the agent's previous run is still open, or the agent is
  mid-turn, is `skipped-busy`, as a heartbeat's is.
- An agent whose envelope is spent pauses the schedule
  (`paused-exhausted`), as a heartbeat's target does.

### 26.3 The `run:` step: a command gate

A step may be a command instead of an agent:

```yaml
- id: check
  run: ./check-release.sh        # runs in the run's tree, no model
  quietExit: 1                   # "nothing to do"
```

- It has no role, no tier and no prompt. It spends nothing.
- **Exit 0** continues. The command's standard output is the step's
  output, so a later step reads it as `{{check.output}}`.
- **The exit the recipe declares** as `quietExit` ends the run quietly.
  A plain `test`, `grep -q` or `diff -q` is a gate as it stands.
- **Any other exit** fails the step, as a `FAILED:` line does (§25.4).
  A broken command is never mistaken for "nothing to do".
- gummi keeps no state for the gate. A command that needs to remember
  the last tag it saw keeps it in a file in the agent's home.

**A quiet run leaves almost nothing behind.** The schedule's row records
`no-change` as its last status and the time as its last run. There is no
run row, no thread item and no notification. The schedules view (TUI
`L`, the web page, `gummi schedule list`) shows when it last checked.

A `run:` step is one check, not a wait. Waiting for days belongs to the
schedule; a step that polls a command inside an open run stays deferred
(§25.13).

### 26.4 The `dispatch` step: a session in a named repository

> *Amended by §27.8 (decision 30): a dispatch may also name `repos: [...]`, literals checked like `repo`, which the minted session attaches; every dispatch repository is checked against the pool when a run or schedule is proposed.*

```yaml
- id: bump
  dispatch:
    repo: myrepo                 # literal; never a template
    title: "Bump foo to {{check.output}}"
    envelope: 5                  # dollars, as every budget a person types
    spec: |                      # becomes the new session's spec 1
      Upstream foo released {{check.output}}. Bump the dependency.
    recipe: fix-until-green      # frozen into the proposal
```

- It mints a freeform session in `repo`, through `cardmint`, with an
  envelope of its own, written in dollars (`5`, `2.50`) and stored in
  credits like every envelope.
- With a `recipe`, the new session holds the filled `spec` as its spec 1,
  and the run starts on it at once. Without a recipe, `dispatch` takes a
  `prompt` instead of a `spec`, and the filled prompt is the session's
  opening turn.
- **Only the repositories the recipe names.** `repo` is a literal, and
  `Check` refuses a template there. The proposal shows every target,
  and allowing it allows exactly those. A new repository is a new
  proposal.
- **The child's envelope comes out of the agent's.** An open child holds
  the larger of its envelope *now*, the one recorded at dispatch and its
  spend; a closed one, its spend: the rule delegation used (§19.10). A
  dispatch the agent's pool cannot cover pauses the run for the person's
  top-up (§24.7a), never mints below its envelope. The agent's tree of
  work never spends past the agent's own envelope, so a person raising a
  child's envelope is refused when the agent's pool left cannot cover the
  increase — the refusal names the agent, and the person tops the agent
  up first. Lowering is always allowed; removing the cap is not.
- **The agent drives its children, and is woken by them** (§26.8, §26.9).
  The agent has `session_status` and `session_send`, which answer only for
  sessions it dispatched, and `session_send` is refused while the child's
  envelope or the agent's own is spent. A child's `ask_user` and its
  run's decisions reach the person, never the agent.
- A dispatched session is a session like any other. It lands on a
  person's read of its diff; nothing about its parent changes that.

### 26.5 The agent proposes; a person allows once

An agent may propose a schedule: `schedule_propose(name, cron or preset,
timezone?, recipe or recipe_yaml, spec)`. The proposal is a **disabled
run-schedule row** and a line in the agent's thread. The person reads it
in the schedules view — the cadence, the recipe's steps, every `run:`
command verbatim and every repository a `dispatch` targets, with the
recipe it starts there — and enables it from there.

- **One allowance covers the chain.** The recipe, and every recipe its
  `dispatch` steps name, are copied into the row (`Schedule.Prompt` holds
  the YAML, each dispatch's `recipeYAML` the named recipe as it was at
  proposal). Enabling it allows every run the schedule starts and every
  run a dispatch starts. A change to any of them is a new proposal.
- **Allowing is enabling.** The schedule is stored as §19.9 stores any:
  disabled, and enabled only by the person's enable — that is the
  allowance, not a decision in the thread. An agent never enables, edits
  or deletes one; `CreateSchedule` from the tool is hard-wired off. A
  person's edit to a row forces it off again, and a run row's recipe is
  not editable: a different recipe is a new proposal.
- A person may still create the same schedule by hand, from the
  schedules view, with an agent as its target.

### 26.6 What does not bend

Decision 28's three rules hold, and nothing here relaxes them:

- **A person allows every run** — here, by allowing a standing schedule
  and the recipes it carries, once, the way "always allow" allows a
  session's runs.
- **Money is a ceiling no agent raises.** One pool, the agent's
  envelope; a child's envelope is carved from it; a spent pool pauses.
- **A question the run cannot settle reaches the person**, from the
  agent's run or from any child's.

An agent (a home) still never lands, publishes, enables a schedule or
raises an envelope; a *session* may publish its own branch under §22.14.

### 26.7 Deferred

- A per-fire history for a schedule, beyond its row's last outcome.
- A dispatch target chosen at fire time from a list the person allowed.
- A step that polls a command inside an open run (§25.13).

### 26.8 As built

What was built follows §26. Where it chose, or departs:

- **A quiet exit is the recipe's first step only.** `Check` refuses
  `quietExit` anywhere else: a run that ended quietly leaves no trace, so
  no model may have worked before it. A quiet run deletes its row, its
  steps and its run directory, records `no-change` on the schedule that
  started it, and sends no turn and leaves no note. The tree is not
  reverted.
- **A value filled into a `run:` command is one quoted shell word**
  (`FillShell`), not wrapped in the delimiters a prompt's are, so an
  earlier step's output cannot add to the command it is filled into; a
  template stands as a word of its own, never inside quotes. A
  dispatch's title, spec and prompt are data and fill plain
  (`FillPlain`). `{{run.diff}}` is refused in a command.
- **A command step runs unconfined**, as the person's user in the run's
  tree (an agent's home), through `sh -c`, killed with its process group
  after ten minutes. It is not under `internal/sandbox`'s confinement,
  and it inherits gummi's environment. Its end tree is a snapshot like any step's; it has no card and spends
  nothing, and a step stored for it carries `run` or `dispatch` where a
  role would be.
- **A dispatch's `spec` is the child's whole spec 1**, replacing the
  template. A recipe a dispatch carries may not itself dispatch, and
  freezing refuses one that does.
- **The agent's spec is not frozen into its schedule.** A run schedule
  names the agent's spec by number, and each run freezes that spec's
  revision as it starts, so an agent that edits the spec after the person
  enabled the schedule changes what a model step of the watch reads. A
  dispatch's spec is frozen in the recipe and cannot drift. Freezing the
  agent's spec into the row is deferred.
- **A dispatch needs a bounded agent** — the child's envelope comes out of
  the agent's own, so an agent with no envelope fails the step — and is
  **idempotent per step**: `dispatches` keeps the step's path, and a step
  restarted after it minted resumes from the record.
- **Pool accounting is one number.** `SessionPoolSpent` of an agent adds
  what its dispatched sessions hold (`dispatchedHeld`), so the pool left,
  the tick's spent-pool check and the driver's lines agree. A dispatch the
  pool cannot cover pauses the run (`pauseForBudgetLocked`) and leaves the
  step live; a top-up starts it again.
- **A child wakes its agent.** This reverses the first build, where a
  child's run end was a line and nothing woke the agent. Each of these is
  the `dispatch: ` activity line *and* a turn into the agent (actor
  `dispatch:<child>`, `wakeDispatcher`): a dispatched session's run
  ending (the child, the run, how it ended and the head of its report);
  a dispatched session with no open run finishing a turn and waiting for
  input (once per turn, with the head of its last reply; the turn that
  reads a run's report is not a second wake); and a dispatched session
  closing. A step's hidden card never wakes anyone. A closed agent, or
  one whose pool is spent (`sessionPoolLeft`, which counts what its children hold), is not woken: only the line is written. An
  agent mid-turn has the message queued as any message to a busy session
  is — never dropped, never an interruption — and one waiting on its own
  question gets the line alone. Waking sends nothing to the child, so a
  wake, a `session_send` and a wake again are bounded by the agent's
  envelope. Its status and report are `session_status`; `session_send`
  is a turn into a child it dispatched, and both refuse any other card.
- **A planner's revised plan adds no command or dispatch**: what the
  person allowed is the recipe's own.
- **A dispatch is refused at proposal on a session that is not an agent**
  (and fails at run time).
- **The faces:** the schedules view (TUI `L`, the web page,
  `gummi schedule list`) shows a run row as `run · <agent> · S<n>`, its
  steps, commands and repositories, and the `no-change` status. A run
  row has no edit form and no `--kind run`; proposals come from the
  agent.
- A schedule's `ok` outcome is written when the run starts; a quiet gate
  overwrites its status with `no-change` after.
- Nothing limits how many disabled run schedules an agent may propose.

### 26.9 Following a child

A dispatched session is shown as tracked by its agent, and stays a full
session: its own branch, landed on a person's read of its diff, never a
hidden child (`IsChild` is false).

- **The block.** In the agent's thread each dispatched session is one
  item keyed `dispatch:<card>` (`threadfold/dispatches.go`), placed where
  it was dispatched and delivered on every page. It carries the child's
  title and repository, its state — *working*, *waiting for input*,
  *run N <state>*, *waiting for the person*, *envelope spent*, *closed* or
  *gone* — its open run's steps ended of steps and live step, its spend
  against its envelope now (whole credits), and the last line of its last
  reply. Its `Rev` moves only when something it draws does. The inputs
  are read once (`ui/webdispatch.go`) for both faces; the terminal draws
  the block in the thread (`ui/dispatchblock.go`; alt+w steps onto it,
  enter opens the child's card) and the page draws it
  (`assets/dispatch.js`; a click selects the child).
- **The board.** An open dispatched session stands directly under its
  agent, marked `└` and without its "from <agent>" badge, in the agent's
  group (`ui/boardnest.go`, `assets/rail.js`). A session whose agent is
  not shown — folded away, closed, or filtered out — keeps its own place
  and its badge.
- **The budget** is §26.4's: the child holds its envelope now, and no
  raise past the agent's pool is accepted.

## 27. Sessions across repositories

*Decided 2026-10-23 (decision 30); built — §27.11 says where the build
departs.* This section amends §19 (a session's repository), §22 (a PR
per repository), §25.6 (snapshots span the trees), §26.4 (a dispatch's
`repos`) and §18 (stacks refuse attachments). Where it and those
sections disagree, this section wins.

### 27.1 Why

Work often spans repositories: a library change and its consumer. A
session had exactly one repository, one branch and one worktree, so the
only way across was a dispatch, which mints a second session with a
context of its own. The only cross-repository machinery gummi ever had
was the retired goal (§17.2a): a branch in each repository its cards
touched, landed once per repository, with no atomicity across them —
git has no merge that spans repositories.

Rejected: **linked sessions**, one per repository, which splits the
context the work needs in one head; and a **fully multi-repository
session** with no primary, which breaks every path that reads
`Feature.Repo`. Chosen: one session, a primary repository, and
attachments.

### 27.2 The model

- A session keeps its **primary repository** (`Feature.Repo`): every
  single-repository path is unchanged, and a card with no attachment
  behaves exactly as before.
- It may have **attached repositories**, rows of the `attachments` table
  (`state/attachments.go`): card, repository name, base, created-at and
  the landed state per repository. A repository is a name in the pool
  (`repos:` plus discovered checkouts), never a path.
- Each attachment is the card's **same branch name** in that repository,
  in a worktree `.gummi/worktrees/<ID>@<repo>` beside the primary one.
  The seam is `Feature.TreeRepo` (never stored): `domain.Feature.
  AttachedView(a)` is the card as the attached repository's manager sees
  it, and `worktree.Manager.featurePaths` names the tree `<ID>@<repo>` for
  it. Everything else in `internal/worktree` — `Create`, `Remove`,
  `Dirty`, `Land`, `Diff`, `BranchAhead` — works on the view unchanged.
  Fork points and landed commits, which the store keeps per card id, are
  kept per tree under `<ID>@<repo>` (`Feature.TreeKey`, routed to the
  `attachments` row).
- The branch forks from the repository's default base, or the base given
  at attach (a local branch of that repository).

### 27.3 Attaching and detaching

`Engine.AttachRepo(card, repo, base)` and `DetachRepo(card, repo)`
(`engine/attach.go`), under the card's lock.

Attach is refused for an agent (its home has no repository), a card that
is not a session, a main-checkout card, a closed card, a stacked card, an
empty name, a path, an unknown or ambiguous name, the primary repository
(compared by root, so an alias cannot slip through), and one already
attached. The branch is cut first and the row stored second; a failed
insert removes what was cut.

Detach is refused while the attachment's branch has commits past its base
that have not landed, or its tree has changes, and while a workflow run is
open; it then removes the worktree and the branch. A landed attachment
may be detached. The primary is never detached.

### 27.4 What the agent sees

The session's start adds every attached worktree to
`SessionOpts.WriteDirs` (claude `--add-dir`, opencode config) and a hint
naming each repository, path, branch and base. A workflow step's card
gets the same, through its session. An attach or detach made while the
backend runs takes effect at its next start: the next turn restarts it
(`FreeformSession.dirsStale`), replaying the conversation. A backend with
no directory cage option still gets the hint and works the path with its
shell.

Every non-child session of a repository has three tools
(`engine/repotools.go`): `repo_list`, `repo_attach(repo, base?)`,
`repo_detach(repo)`. The agent attaches **freely, with no decision**:
cutting a branch is cheap, and nothing lands without the person. An
agent (a home) has none.

### 27.5 Landing repository by repository

`cardops.Ops.MergeRepo(id, repo, …)` (`Merge` is `MergeRepo` with the
primary) lands one repository under the checks every landing crosses:
open diff comments (of the card, whichever repository), an open workflow
run (`Engine.RunHolds`), `MayLand`. `gummi merge --repo <name>` takes it.

There is **no atomicity** across repositories. The card closes
(`StageDone`) only when the primary and every attachment that has commits
have landed: an attachment with no commits is skipped, one with
uncommitted changes holds the card open. The `merged` event names the
`repo` and lists the `remaining` ones. `Clean` takes a closed card's
attached worktrees away, and the branch of each that landed or is empty.

### 27.6 Runs span the trees

A run's `StartTree`/`EndTree` columns keep their shape: a bare hash for a
session that spans nothing, and for one that does, the primary's hash and
one `<repo>=<hash>` line per attached tree (`domain.EncodeTrees`). The
snapshots, the step and run diffs (an attached repository's patch under a
`# repo: <name>` line, its paths as `<repo>:<path>`), the reader-change
flag and Revert run walk every tree (`engine/workflowtree.go`). The ignored
files a run records are per tree (`ignored@<repo>`). A tree attached
under a run has no start snapshot and is left alone by Revert run: a run
never deletes what it cannot prove it made. Done-when checks still run in
the primary tree; a recipe reaches an attachment by the path the step's
hint names.

### 27.7 Publishing

A person publishes each repository: `publish.PlanAll` resolves and plans
every tree that has commits, `publish.DoAll` pushes and opens one PR per
repository, each body carrying `Related changes in other repositories:`
with the others' branches and, once opened, their PR URLs; once the last is
open the earlier ones are edited to name it. Publishing stays a person's
act: no agent, run, schedule or MCP tool reaches it. A stacked card
stays refused. Both faces offer it as the card menu's "open pull
requests in every repository…" (`ui/publishspan.go`, `GET`/`POST
/api/cards/{id}/publish/span`): the confirm shows each repository's plan,
or why it is left out, takes one title and description for every PR, and
sends back a fingerprint per repository it publishes. A fork's target is
chosen in the one-repository confirm; a span refuses a fork nobody has
chosen for.

### 27.8 Stacks and dispatch

A card with attachments cannot join a stack, and a stacked card cannot
attach (`state.ErrStackAttachments`). A dispatch step may name `repos:
[…]` beside `repo`; they are literals, checked like `repo` (no template,
no path, no duplicate), and the minted session attaches them. Every
dispatch repository is checked against the pool when a run or schedule is
proposed (`freezeDispatches`), and again when the step runs.

### 27.9 What does not bend

Decision 28's three rules hold. A person still allows every run; money is
still one pool, the session's envelope; and a question the run cannot
settle reaches the person. An open run holds every repository's landing
and every detach. The agent attaches freely because attaching grants it
nothing it could not do with a branch: nothing is pushed, published or
landed without a person.

### 27.10 Deferred

- A stacked card with attachments.
- Atomic landing across repositories.
- Handing off, squashing and committing per repository from the board
  (they act on the primary; the agent commits an attachment itself with
  `git -C`).
- Choosing attached repositories in the terminal's new-session form
  (`/attach` and the card menu do it afterwards; the web page's draft
  has an "also" toggle for each).

### 27.11 As built

What was built follows §27. Where it chose, or departs:

- **The faces.** Both read one inventory: the card's menu gains `attach
  repository…`, `detach repository…` and `land <repo>` (`ui/repos.go`),
  the terminal's thread head lists the repositories with the landed ones
  marked, and its diff view switches repository with `[`/`]` and lands the
  one shown with `L`. The web page adds a repository tab strip to the Diff
  tab (`GET …/diff?repo=<name>`), a "Repositories" mark in the card's head,
  and an "also" toggle on a new session (`attach` in `POST /api/cards`).
  `/attach <repo> [base]` and `/detach <repo>` are the composer's commands
  in both. The shapes are in `internal/webapi` with a golden
  (`TestSpanningSessionShapes`).
- **Diff comments belong to a repository.** `diff_annotations.repo` is
  empty for the session's own; a comment is listed and located on the diff
  it was written on, and every repository's open comments hold every
  repository's landing.

- The tree-set encoding rides in the existing snapshot columns: no
  migration, and rows from before read as the primary alone.
- Fork points and landed commits for an attached tree live in the
  `attachments` row, reached through the same store methods by a
  `<card>@<repo>` key.
- `repo_list` names ambiguous folder names it will not attach.
- A dispatch whose attach fails fails its step with the attach sentence;
  the minted session stays.
- An attachment whose repository the pool no longer names holds the card
  open until it lands, its repository comes back, or a person detaches it:
  gummi cannot read it, so nothing says it holds no unlanded work.
- A repository name with `=` or whitespace is refused (`RepoNameOK`): the
  run's tree set is stored as `repo=tree` lines.
- Attaching or detaching restarts an idle backend with the new set of
  directories, resuming its own conversation where the backend can.
- A run's done-when checks run in the primary tree only; a check that
  needs an attached repository names its path.

## 28. A run that publishes its PR and waits on it

Decision 32. A session allowed to publish (§22.14) can open its PR and
watch its checks, but every minute it waits is a turn it pays for, and
what it does about a red check or a review is whatever the model decides
that turn. A recipe can now say it once: open the PR, wait without a
model, and on each thing that happens run the steps the recipe names for
it, until a person merges.

### 28.1 The steps

```yaml
name: ship
inputs:
  trust:
    hint: "who may steer the run from the PR: @login, @org/team, role:maintain"
    default: "role:maintain"
steps:
  - id: build
    role: implementer
    prompt: "Implement the spec and commit your work.\n\n{{spec}}"
  - id: open
    pr: { act: create, draft: true }
  - id: shepherd
    watch:
      until: merged                 # or green-approved
      trust: "{{inputs.trust}}"
      max: 6                        # handler passes, all kinds together
      quiet: 7d                     # nothing at all this long: ask the person
      onClosed: abort               # closed unmerged: the run ends partial
      on:
        checks-failed: { recipe: fix-until-green }
        changes-requested: { goto: build }
        comment:
          - id: answer
            role: implementer
            prompt: "Address this and commit. Do not reply on GitHub.\n\n{{event}}"
        base-moved: { recipe: merge-base }
```

- **`pr:`** is gummi's own act, like `run:` and `dispatch` (§26.3,
  §26.4): no role, no model, no prompt. `act` is `push`, `create`,
  `update` or `ready`; `create` takes `draft`. Each goes through
  `internal/agentpublish` with every refusal a session's `pr_*` tool
  meets (a dirty tree, a stacked or landed card, a planted hook or
  steering configuration, a moved target). A refusal fails the step.
  The step's output is the act's answer.
- **`watch:`** is one step, at the top level of a recipe, at most one
  per recipe, after a step that pushes. It waits on the session's PR.
  `until` is `merged` (a person merged it) or `green-approved` (every
  check passed and the review decision is APPROVED). It succeeds when
  its `until` holds, and the run goes on to the steps after it.

`Check` refuses: a second `watch`, a `watch` inside a `parallel` or a
`repeat`, a `pr:` step inside a `parallel`, a handler event outside the
closed set, a handler with more than one form, a `goto` that names no
earlier top-level step, a `recipe` handler whose recipe holds a `watch`
or a `pr:` step of its own, a `trust` that does not parse (§28.3), and
`{{event}}` outside a handler.

### 28.2 Waiting without a model

- **The step's state is `waiting`.** Its row keeps the PR's reference and
  what it has seen: the head commit, the checks and their buckets per
  head, the review and thread ids, the base commit. Nothing else is
  remembered.
- **gummi polls.** `Engine.WorkflowTick` asks for a watch's PR at most
  once a poll interval, backing off while nothing changes: a cheap read
  of the PR's state and head first (`pr.LiveStatus`), the checks and
  threads only when that changed. The reads go through the person's
  credential, as `pr_checks`' do. A watch spends no money.
- **`workflowpolicy.Decide` stays pure.** The engine writes what it read
  into the snapshot; `Decide` compares it with what the row has seen and
  returns the actions: still waiting, an event's handler to start, the
  watch met, the run to end partial, a question for the person.

### 28.3 Events and the trust filter

gummi sorts what changed into five events, in this order, and starts at
most one handler at a time:

| event | when |
|---|---|
| `base-moved` | the PR cannot merge cleanly against its base, or its base moved under a required up-to-date check |
| `checks-failed` | every check on the head has settled and one failed |
| `changes-requested` | a trusted review asks for changes, or a trusted unresolved thread is new |
| `comment` | a trusted top-level comment is new |
| merged / closed | ends the watch: met, or `onClosed` |

- **A failure is read once its head has settled.** While a check on the
  head is pending, no `checks-failed` starts, so one push brings at most
  one fix pass.
- **The trust filter** is `trust:`, a comma-separated list, usually an
  input so the run's caller sets it: `@login`, `@org/team` (read through
  the organisation's team members, which needs `read:org`) and
  `role:write`, `role:maintain` or `role:admin` (the collaborator's
  permission on the repository). Empty means the login gummi publishes
  as. The proposal shows it resolved: "acts on feedback from @alice,
  members of @acme/core, repository maintainers".
- **It fails closed.** A team or a permission gummi cannot read is never
  guessed: the watch asks the person and acts on nothing meanwhile.
  Membership is read again at each event.
- **What it covers.** Reviews, threads and comments. A review, thread or
  comment whose author is outside it, or that someone outside it last
  edited, is not an event and never reaches an agent. Check logs are not
  people: a recipe may name the checks it reads (`checks: [ci, lint]`),
  and every other check's log is left out.
- **Nothing is dropped silently.** The watch's strip counts what the
  filter kept out ("2 comments from outside the filter, not read"), and
  a person may forward one to the run by hand, as they forward failing
  checks (§22.11).
- **What gets through is data.** `{{event}}` is the event's facts —
  checks and their log tails, or each comment's author, URL and body —
  wrapped as a recipe's inputs are, so its text cannot pose as
  instructions.

### 28.4 Handlers

An event's handler is one of three forms:

- **inline steps**, a list of plain steps under the event;
- **`recipe: <name>`**, a recipe frozen into the proposal as a dispatch's
  is (`engine/freeze.go`), run as a nested block of the same run: the same
  tree, the same pool, the same frozen spec, `{{event}}` given as its
  input `event` when it declares one;
- **`goto: <id>`**, an earlier top-level step: the steps from it up to
  the watch run again as a new pass, reading `{{watch.event}}`, which is
  empty on the first pass.

After each pass gummi pushes (never forced; a remote that moved fails
the push and the watch asks the person) and the watch waits again.

- **Bounded.** `max` counts every pass of every kind; an event may name
  a `max` of its own. At either, the watch asks the person: one more
  pass, go on waiting without acting, or stop.
- **No loops on the same thing.** The same event on the same head after
  a pass, or a pass that changed nothing and leaves its event standing,
  asks the person.
- **A moved base is merged, never rebased.** A `base-moved` handler
  merges the base into the branch and resolves what conflicts; the push
  stays a plain one. gummi never force-pushes for a run.
- **Money** is the session's pool, as for every step: a spent pool
  pauses the run.

### 28.5 Who may

- A run with a `pr:` or `watch:` step is proposed only for a session
  whose "agent may publish" is on, and is refused at the proposal
  otherwise, naming the switch. The allowance's repository is the only
  target; every act is checked against it.
- Taking the allowance back while the run waits fails its next act or
  read, and the watch asks the person.
- The proposal names every `pr:` act, the watch's `until`, its events
  and handlers, and the resolved trust filter. Allowing the run allows
  exactly those; a planner's revision that adds or changes one is a
  revised plan the person allows (§25.4).
- `agentpublish` lets the run's own act through where it refuses
  `agent-busy` for a session's tool while a run works the tree.
- Under `gummi run`, the trust filter is the `--input` its caller gives.

### 28.6 Restarts

A run waiting only in a watch — no handler running, no question open —
is not parked by a board that did not start it: its polling resumes by
itself, since polling spends nothing. The first handler it would start
after that restart waits for a person's **Resume**, the run's pinned
decision as for any parked run. A run doing anything else is parked as
§25.7 says.

### 28.7 Faces

The run's block draws the watch as a row with a live strip: "waiting on
the PR: 3 checks pending · 2 threads seen · 1 comment outside the
filter", and each pass under it, named by its event. The web page draws
the same from `threadfold`'s fold.

### 28.8 What does not bend

gummi never merges, retargets, force-pushes, resolves or replies to a
thread, or lands. A watch ends on a merge a person made. A question the
watch cannot settle reaches the person.

### 28.9 As built

Where the code differs from, or adds to, the text above:

- **The watch's stored state is `watching`**, not `waiting`: `waiting`
  already means "a row not started", and the conductor starts any step
  in it. The faces still say "waiting on the PR". The row is live, so a
  stop interrupts it and the run cannot end around it.
- **The row keeps its memory as JSON** (`workflow_steps.watch`,
  `domain.WatchState`): the PR, the parsed trust filter, the last read
  (`WatchFacts`), the feedback ids handed to passes, the passes, and the
  person's answers. `workflowpolicy.Decide` reads it from the snapshot,
  with `Snapshot.Now` as its only clock.
- **The cheap read is `pr.WatchStatus`** (state, head, base, mergeability,
  review decision, `updatedAt`), not `LiveStatus`, which does not move on
  a new review or thread. The checks are read again when the head moved,
  one was pending, none had reported, the PR was touched or a while has
  passed; the feedback when the PR was touched. Three failed reads in a
  row are put down to the network before the person is asked.
- **A pass's steps sit at `<watch>/<n>/…`.** A `goto` pass leaves `pr:`
  steps out, so it cannot open the PR again.
- **After a push the watch forgets what it read** and ignores reads that
  still show the head from before the push (for two minutes), so GitHub
  lagging behind does not read as "the same event on the same head".
- **"The same event after a pass"** compares the event's key (which
  carries the head for checks and the ids for feedback), so a new comment
  on an unchanged head is a new event, not a loop. A pass that changed
  nothing asks as written.
- **An unreadable trust filter asks at once**, not only when an event
  stands, and while a check is still pending: feedback is withheld while
  the filter cannot be read, so an event could never stand.
- **`green-approved` on a head with no checks** reads it as green once
  nothing has changed for five minutes, so a repository without CI ends
  the watch, and a push CI has not reached yet does not.
- **`base-moved`** fetches the base with the person's credential before
  the pass (a failed fetch asks the person to read again); the handler
  merges the local ref the event names. The bundled `merge-base` recipe
  does it, and `fix-until-green` takes an optional `event` input.
- **A spent pool does not pause a watch that is only waiting.**
- **Questions are `RunDecideWatch`**; `gummi run` answers each with its
  first option, and the quiet question with stop, so a drive cannot wait
  forever.
- **A planner's revised plan** adds no `pr:` or `watch:` step, as it adds
  no command or dispatch: the planner is told so and the plan the person
  allowed runs on. What a run does to the PR is what its proposal named.
- **Feedback is read to a cap.** Threads are read in full, each to its
  first 100 comments; a thread with more is kept out, since the filter
  cannot see everyone in it. Top-level comments and reviews are read to
  the first 100 each, and the strip names how many past that were not
  read.
- **Forwarding is all or nothing.** A person forwards everything the
  filter kept out at once (TUI `F` on the run's head, the run block's
  **Forward kept-out feedback** on the page), after reading it on the PR.
  Each item is forwarded by its print — its id and a hash of its body and
  everyone in it — so an edit after the forward, by anyone, keeps it out
  again. A forward makes the watch read the feedback again at once.
- **A pass's steps commit.** A step under a watch (not a reading role) is
  briefed to commit its changes on the branch, since gummi pushes only
  what the branch holds and never commits for it; the bundled `ship` and
  `merge-base` prompts say so too.
- **`base-moved` stands only when GitHub says the branch conflicts or is
  behind** (`mergeable` CONFLICTING, merge state BEHIND). A base that moved
  without either, on a repository that does not require up-to-date
  branches, raises nothing.
- **`checks:` narrows which logs are read, not which failures count**: any
  failing check raises `checks-failed`; one outside the list reaches the
  pass without its log.
- **The role filter reads `role_name`** (maintain and triage show there;
  `permission` alone reads write for them), falling back to `permission`;
  a feedback item with no author is outside every filter.
- **The checks and feedback are read in full again** after `watchPollMax`
  since the last full read (`WatchFacts.FetchedAt`), even when the PR
  shows no change.
- Feedback past the first 100 reviews, comments or inner comments of a
  thread is not read.
