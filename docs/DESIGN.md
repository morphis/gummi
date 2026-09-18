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
the scarce resource. Autonomous runs draw from two independent attention
pools, each with its own cap and its own FIFO queue (internal/engine): one
**attended** lane for a card whose gate-approval mode is `off` — you've
said you'll stay with it, so it must never queue behind unattended
work — and, by default, two **autopilot** lanes for every other card
(`gates` or `full`, including the everyday default). A slot freed in one
pool is never handed to a session waiting in the other; an attended run
always starts immediately. `GUMMI_MAX_ACTIVE` overrides the attended
pool's size (default 1); `autopilot_lanes` in `config.yaml` overrides the
autopilot pool's (default 2). gummi's job is to make the "waiting on you"
queue visible and make context-switching between features cheap.

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
  read — never swallowed into "this card has no checks". Results recorded
  in the spec. Deterministic floor, adaptive ceiling.
- **Done** — you decide the feature is done. A verified card has **three
  endings**, and the answer set at the verify gate offers all three
  rather than assuming the first:
  - **Land on main** (`g`, or `m` at any stage; `gummi merge`) —
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
  - **Hand off** (`h`, `/handoff`; `gummi handoff`) — the card closes and
    the branch stays exactly where it is: yours to push, PR by hand,
    cherry-pick, or keep. gummi commits a final checkpoint so nothing
    loose is left on the branch, stamps `handed_off_at`, and crosses the
    same gate with the landing waived and **every other floor intact** —
    open `%%` threads, open diff annotations, the omission gate and the
    document floor all still hold it. Landing it after all stays
    available for as long as the branch exists, and retracts the stamp.
    Cleanup refuses a handed-off card: `c` would delete the branch that
    was kept on purpose.

  Because of the last two, **Done means the card is closed, not that
  anything merged.** The board badges which ending a card took (`landed`
  / `handed off` / `dropped`), and `status --json` names it in one
  `ending` field beside `branch_state` — "how did it close" and "is the
  work on the trunk" being two different questions. gummi then offers
  worktree cleanup on a landed card — per card with `c`, or for the whole
  board in the close-out sweep. The card's artifact stays where it is at
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
│  workflow state machine · scheduler (attention slots) ·  │
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

**opencode adapter (v2):** opencode ships a headless server with an HTTP API
(`opencode serve`), so it fits the same interface. Also planned: a **generic
headless adapter** (spawn `<cmd> -p "<prompt>"`, capture output) as the
lowest common denominator for one-shot autonomous stages with any CLI agent.

Shipped alongside the above: **claude** (Claude Code CLI, streaming
stream-json), **codex** (Codex CLI, `codex exec --json`), and **zz** — a
small Rust coding agent that fronts any OpenAI-compatible endpoint (local
llama.cpp, OpenRouter, a self-hosted gateway). zz's CLI is one process per
turn with no server mode and no stdin form, so its adapter follows codex's
process-per-turn shape, resuming via a `--session` transcript file instead
of an in-process thread id. A role's `provider:` field in `profiles.yaml`
selects which `[providers.<name>]` stanza of the operator's own zz config
that role's session hits, forwarded as `--provider`; omitted, the session
falls back to zz's own default. A sibling `think:` field forwards an
opaque thinking level as `--think`, letting an architect role reason at a
high level while a scribe role runs at none. Every zz session also
carries an unconditional `--max-turns` (default 200, overridable via
`GUMMI_ZZ_MAX_TURNS`) as a runaway-loop backstop distinct from the credit
envelope that is gummi's real spend limiter.

### 4.2 Orchestrator

- **State machine** per card — the workflow is compiled in, not
  configured; the only non-forward movement is the rerun edges.
  Transitions fire actions (start session, run checks, request human gate)
  and emit events.
- **Scheduler with attention slots, split into two pools** (§1): an
  attended lane (default cap 1, `GUMMI_MAX_ACTIVE`) for a card whose
  gate-approval mode is `off`, and autopilot lanes (default cap 2,
  `autopilot_lanes`) for every other card. Each pool has its own FIFO
  queue; a slot freed in one pool is never handed to a session waiting in
  the other, so an attended run always starts immediately regardless of
  how full the autopilot pool is. Either cap can be raised (or, internally,
  set to 0 for uncapped) and excess autopilot sessions queue behind it; a
  paused/blocked session frees its slot. Parallel token burn is the
  operator's call: cards drive in disjoint worktrees under per-card locks
  (§8.2 Decision 12), so nothing in the engine needs the serialization.
  Interactive stages only run when you attach.
- **Needs-attention queue**: gates, agent questions, budget exhaustion, and
  failures — plus permission requests when running in `guarded` mode — land
  in one inbox, newest-first, with desktop-bell/notification hooks.
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
  claude, opencode and zz pin their file-writing tools to the session's
  working directory, so a write naming somewhere else is refused by the
  backend. copilot, codex and headless are merely *started* there — their
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
allow_free_form, spec_anchor}`). When the model calls it, the adapter
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
its budget, so gummi does, twice:

- *At session start*, in the stage system hints:
  > You have a budget of ~N credits (≈$X) for this stage. Work
  > budget-consciously: prefer targeted reads over broad exploration, batch
  > related edits, avoid speculative refactors. If you estimate the task
  > cannot be finished within budget, stop early and write a checkpoint
  > (what's done, what's left, where to resume) into the spec's progress
  > section instead of running dry mid-edit.
- *Mid-session*, the orchestrator meters actual spend from SDK usage events
  and injects budget updates at thresholds (50%, 80%, 95%):
  `[budget] 80% consumed, ~12 credits left — wrap up or checkpoint now.`
  The 95% message explicitly demands a checkpoint. This converts the hard
  stop from a cliff into a landing.

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
- **Plan-time estimation**: the envelope is proposed from the historical
  median spend of completed features blended with a scribe-role
  estimate, padded and floored (`MinEnvelope`) so estimates skewing low
  don't gate instantly.

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

The board sits behind a one-row tab bar shared with the status bar:
`gummi │ board │ inbox │ agent │`. `tab` cycles all three;
`alt+1`/`alt+2`/`alt+3` jump straight to one — alt-prefixed deliberately
(the same reasoning as the thread's `alt+o` outputs toggle: a plain
`ctrl`/bare key a terminal multiplexer or the hosted agent tab's own pty
might already claim). Both are answered at the top of `handleKey`, above
whatever surface holds the keyboard, so a tab is always one keystroke
away from inside a card's thread, its spec view or its diff view.
**The keyboard lock.** The agent tab hosts a program with its own
keymap, which raises the only genuinely hard question in the scheme: a
hosted CLI wants `tab` for completion, and gummi wants it for the cycle.
Both cannot have it, and picking either side loses something real —
giving it to the CLI makes cycling onto the tab a one-way door (press
`tab` a third time, nothing happens, nothing says why); keeping it means
the CLI's completion is unreachable.

gummi resolves it the way zellij does, with an explicit mode the user
controls and can see. `ctrl+g` toggles a keyboard **lock** over any
`tabDef.foreign` tab:

| | board / inbox | agent, unlocked | agent, **locked** |
|---|---|---|---|
| `ctrl+g` | says what it is for | lock | **unlock** |
| `tab`, `alt+1/2/3`, `alt+/` | gummi | gummi | hosted CLI |
| `?` | gummi (unless typing) | hosted CLI | hosted CLI |
| `ctrl+c`, `esc`, text | gummi | hosted CLI | hosted CLI |
| mouse | terminal's own selection | terminal's own selection | hosted CLI |

The lock is over the *input*, not just the keyboard. Mouse capture
follows it rather than the tab because taking the mouse is not free:
while gummi captures it the terminal's own click-drag selection stops
working, and selecting a block of agent output to copy is something
people do far more often than clicking inside a CLI. `MouseMode` is a
per-frame `tea.View` field, so this costs nothing anywhere else — gummi's
own surfaces are keyboard-only and never ask for the mouse at all.
Forwarded events are translated into pane coordinates (the child has no
idea the tab bar exists) and dropped over gummi's own chrome; x/vt
encodes them for whichever tracking mode the child actually set, and
drops them entirely if it set none.

**`?` and `alt+/`.** `?` is the convenient help key, but it is ordinary
punctuation, so it must yield wherever the user types prose: the thread's
composer, the bug-import filter, and the hosted CLI. Those are exactly
the surfaces whose key rules are least guessable, so leaving them without
a route to their own key table was the worst place to leave one. `alt+/`
is the help key that is always gummi's — alt-prefixed for the same reason
`alt+N` is. It is tier-1, not a second `ctrl+g`: a locked keyboard yields
it too, because "locked keeps exactly one key" stops being true the
moment there are two.

You *arrive* unlocked, so the cycle always continues and typing at the
agent works with no extra keystroke — gummi claims only the tab switches
there. A user who wants the CLI's own `tab` asks for it. `ctrl+g` is the
one key gummi never yields, in either state and above the overlay stack:
a lock you can enter but not leave is the trap the mechanism exists to
remove.

**Saying so before it matters.** A lock nobody knows about is the same as
no lock, and the hint has to name the trade rather than the mechanism —
"lock" tells someone who already understands, which is not who needs it.
So `ctrl+g tab→agent` in the bar, plus a notice at the two moments it is
worth anything: arriving at the tab (just before you reach for a key
gummi is holding) and having `tab` move you when you meant completion
(the strongest reason anyone ever wants the lock). Working the lock once
retires both — it is an offer, not a nag, and having taken it is proof it
landed; a user who never tries it keeps being told, because they never
learned. Teaching never costs the keypress: `tab` still cycles, and the
notice explains what just happened rather than swallowing it.

Because the lock changes what every other key does, it is never silent:
the tab wears a `⬤ locked` badge (visible from the other tabs too, since
the lock outlives a tab switch), the bar's hint becomes `ctrl+g unlock`,
and the status bar's leading pill turns alert-weighted. Every one of
those states what is true *now* rather than a general rule — a bar still
advertising the tab cycle while the keyboard is locked would be telling
the user to press the one key that cannot work, which is precisely how
the original one-way door went unnoticed.

The board's own overlaying surfaces (spec, diff, ingest review, bug
import, dependency picker) are scoped to the board tab: each belongs to a
card, and a card belongs to the board. Leaving the tab hides them and
returning restores them — never discards, since a card's thread holds an
unsent composer draft the same way. The inbox tab promotes the
needs-attention queue out of its modal overlay; the agent tab hosts a
pty running the user's own coding CLI.

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
  self-healing — a gate crossed by `g`, by `gummi run`, or by the
  workspace MCP all move the card, and moving the card abandons what the
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
  the durable record — free-form only, because the recorded options died
  with the process and are never stored (§ above), so the answer is
  prose, which the control always allows. The answer rides a fresh turn
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
it: `internal/gatepolicy`'s `RaiseGate` outcome is the single place a
decision is opened, which is what keeps the TUI and `gummi run` from
drifting the way `attnKind` and the driver's NDJSON vocabulary already
have.

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
  landing at a time, bottom first. PRs, pushing,
  and releasing stay in your hands. A card may name and read the PR it
  lands through — linking it and pulling its review threads in as diff
  annotations — but gummi still never writes to GitHub: no PR creation,
  no push, no merge, no base retarget, no thread resolution, no CI
  gating. Pushing a replayed branch is a `git push --force-with-lease`
  gummi prints and never runs.
- Not a process editor — one workflow, compiled in. If the workflow needs
  changing, that's a gummi release, not a config file.
- Not a second driver — a hosted agent acts on the running board through
  the board-level tool contract (§16); it never reaches gummi by invoking
  another `gummi` process.

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
Scheduler with attention slots, pause/resume, needs-attention queue,
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
   retry-on-conflict.
3. **One strict workflow, never configurable.** No workflow YAML, ever —
   and, since the merge (§3), literally one graph for every kind:
   `todo → plan → implement → verify → done`. There are no skip flags and
   no quick route; the flexibility they offered was three graphs' worth of
   duplication for two slots each graph half-ignored. The only movement
   that is not forward is the *rerun edges* (implement → plan, verify →
   implement). The quality floor is non-negotiable.
4. **Critique before every gate**: each stage ends with a fresh-context
   pass that tries to refute what it produced, and a *changes* verdict
   re-runs that stage in place — capped (default 2–3 rounds, and bounded
   by the card's budget envelope); past the cap it escalates to the human
   instead of looping. Review used to be a stage of its own; it is this
   pass on the work stage. At design altitude the same pattern is the
   **plan critique**: critique→replan, capped at 2 rounds, then the human
   gate — catching design-level security/correctness flaws before
   implementation tokens are spent.
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
6. **The endgame is a squash commit on main.** *Amended for stacks
   (§18):* the branch a card forks **from** is now the card's own — a
   chosen local branch, or the branch of the card below it in a stack —
   while what it lands **onto** is unchanged, and a stacked card refuses
   to land until every card below it has. When you accept a
   verified feature, gummi lands its branch on local main as one squash
   commit with a message you approve — no PR or push automation; sharing
   the result is yours. gummi detects when a branch landed outside this
   flow and offers worktree cleanup either way.
7. **Verify = discovered checks + spec plan**: the repo's build/test/lint
   commands always run, from the spec's `gummi-checks` block —
   auto-discovered into the Verification plan at approval and
   human-gated with the rest of the spec; the verification plan adds
   feature-specific live checks the agent executes.
8. **First-class providers**: GitHub Copilot Pro/Pro+ (premium-request
   pool) and OpenAI-compatible BYOK endpoints (llama.cpp, vLLM, hosted).
   Profiles are designed around exactly these two paths.
9. **Attention slots are uncapped by default** — as many concurrent
   autonomous sessions as you start. A cap is configurable for anyone who
   wants one.
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

    `worktree.Pool` caches one `Manager` per repo root and resolves a card
    through `ManagerFor`; worktrees still live under the *workspace* root,
    so a multi-repo board keeps one `.gummi`. Dependency edges cross repos
    freely — `feature_deps` references `features(id)` with no repo
    awareness.

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
14. **Third kind, `RS-NNN`** — own compiled-in graph and artifact under
    `.gummi/research/`, no branch or worktree, reusing Review/Verify
    verbatim as the quality floor.
15. **Decomposition is wired into the RS `verify → done` gate** —
    re-runnable from `done`, back-annotates minted FD ids into `## Slices`,
    never wedges the crossing, reserves envelope for its own architect
    pass.
16. **`investigate` as a borrowed pass on an existing FD/BG** is deferred
    to a follow-up.
17. **Autopilot may redo its own work. It may never widen its own
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
21. **A plan's promises are part of the quality floor**, decided after
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
- **One-shot agent passes already exist.** Plan-time `Estimate` (§5.1) is the
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
  modeled on `Estimate`: copy the source into `.gummi/ingest/`, open a transient
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
new-card dialog: a pasted issue reference is imported into it with `alt+g`
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
- **Coverage reconciles** — every `## Questions` thread and every
  requirement referenced from `## Slices` maps to a slice row or an explicit
  `## Out of scope` line; anything left unmapped is surfaced loudly rather
  than silently dropped.

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
  (`--envelope N` or `GUMMI_ENVELOPE`); exhaustion fails loud (no auto-topup).
  The board's creation dialogs prefill `ui.DefaultEnvelopeCredits` (2000) when
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
opencode share one convention; Codex needs its own. **Content converges on
one primitive** — there is no per-agent instruction format, only where it
lands differs: a **project-scope** install writes `.claude/skills/gummi/` for
Claude, Copilot, and opencode (Copilot and opencode both read the Claude-style
skill layout), plus `.agents/skills/gummi/` for Codex — two files, not four.
`--scope user` diverges per agent (Claude + opencode share
`$CLAUDE_CONFIG_DIR`/`~/.claude`; Copilot uses `~/.copilot`; Codex uses
`~/.agents`); `--agent` overrides detection; scope is detect-and-ask with
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

## 16. Hosted vs. outside: the two ways an agent drives gummi

**The taxonomy is three-way, not two.** A human drives the board directly
at the keyboard. An agent can drive the same running gummi process from
*inside* — hosted in the TUI's agent tab, acting on the workspace through
a board-level tool contract. An agent, script, or CI can drive a *fresh*
gummi from *outside*, via the headless CLI driver (§14). The axis that
matters for "how does an agent talk to gummi" is inside-vs-outside, not
human-vs-agent — the TUI hosts both a human and (in its agent tab) an
agent; only the outside path is a second process.

**Why the inside path exists.** A card's stage session and the running
board share one process, and that process holds the card's per-card lock
for as long as it's driving it. A hosted agent that shells out to
`gummi run`/`resume` spawns a *second* gummi contending for a lock its
own parent already holds, and loses — the very process it's trying to
help fails outright. The inside path exists so a hosted agent can act on
the workspace without becoming a second driver.

**The board-level tool contract**, stated at the level §14.1 states the
driver's flags (a stable summary, not a schema dump that would drift out
of sync with the code):

| tool | effect | lock |
|---|---|---|
| `board_list` | list every card: id, kind, title, stage, spend/envelope, verified/done | none |
| `card_status` | one card's stage, branch state, spend, verified/done/running, open gate blockers | none |
| `card_spec` | one card's current design artifact as markdown | none |
| `card_diff` | one card's worktree diff against main | none |
| `card_run` | start an autonomous stage session for a card, in this process | acquires (in-process) |
| `card_resume` | resume a parked stage, optionally with a note, in this process | acquires (in-process) |
| `card_new` | mint a new card onto the backlog; design gates default to checkpointing for the human, not auto-crossing | none to mint |

`card_run`/`card_resume` don't *shell out* to acquire a lock — they ask
the engine already running in this process to drive the card, the same
way the TUI's own key bindings do. That's the whole trick: the lock gets
acquired in-process either way, so routing through these tools instead of
a second `gummi` process is what avoids the contention, not some
different locking rule.

**The shell-out line is lock-acquisition, not read-vs-write.** A hosted
agent may shell out to a CLI verb iff that verb never touches the
per-card lock, regardless of whether it writes:

- Lock-free, safe to shell out to: `status`, `spec`, `diff`, `watch`,
  `doctor` (all read-only) and `deps add`/`deps rm` (a write, but one that
  opens the state store directly rather than starting a driven session —
  no lock, no engine). `deps add`/`deps rm` are the exception worth
  naming explicitly: they're writes, but the dividing line here is
  lock-acquisition, not write-vs-read, and they don't acquire one.
- Lock-holding, never safe to shell out to from inside: `run`, `resume`,
  `merge`, `squash`, `commit`, `clean`. For `run`/`resume` the board-level
  tools above are the in-process substitute; for the rest, see below.

**Deliberately withheld vs. merely unbuilt** — the same "absent from the
tool set" surface splits two ways, and a hosted agent (or a future card)
must not conflate them:

- *Withheld, permanently*: `merge`, `squash`, `clean`, and crossing any
  workflow gate. These are human decisions at the board on purpose — the
  inside path does not grow a tool for them regardless of future work.
- *Unbuilt, not yet*: answering a delegated `ask_user`, a board-level
  needs-attention view, `doctor`'s readiness checklist, the PR verbs, and
  a run stream equivalent to the CLI's NDJSON. Nothing about the inside
  path rules these out; they're absent because no card has built them.

**The knowledge-delivery contract.** Knowledge about *gummi* — the
workflow, the gates, the lock, this taxonomy — travels with the agent's
session (the skill/system context a hosted or outside agent is given),
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
  `gummi-done-when` block (each item `says` a statement and has either a
  `check:` command or `judge: true`), limits, a budget section with a rough
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
least 100) belongs to the goal's own review and verify. `available < 0`
wraps the goal up. Only a person raises the ceiling, and a raise lifts a
wrap-up the budget forced.

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


## 18. Stacks — slicing one piece of work into several landings

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
execute — and the board (`queueStackTick`, drained in `Update` beside
`drainGoalTicks`, plus a backstop poll) and the headless driver both tick
without either deciding. `stack.Decide` returns **at most one** restack per
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
card opens the ordinary new-card form with its `stack` row pre-answered,
and creating that second card is what brings the stack into being, named
after the card at the bottom. The common case costs one key, and a stack of
one card — which nobody wants — never exists. The row itself lives in the
folded run options, because the overwhelming majority of cards are
standalone and a row every reader tabs past to say "no" costs more than it
gives; what keeps the gesture legible is the always-visible `becomes`
readout, which names the branch the card will fork from.

`T` and not `S`: `S` is the severity sort, and one key wearing two meanings
on one surface is the defect this keymap's own comments keep recording.

### 18.5 Deferred

- **Pushing and retargeting.** gummi prints the `git push
  --force-with-lease` a replayed branch needs and never runs it, and never
  changes a PR's base. Reversing that needs a write-scoped token story
  where gummi reads no token at all today (`pr.Available` is a `LookPath`).
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
