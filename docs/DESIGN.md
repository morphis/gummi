# gummi — Design Document

> A meta-harness for coding agents. Drive a fleet of agents through a
> spec-driven workflow across git worktrees — from one beautiful TUI, or
> headlessly from your own agents and CI.

**Status:** brainstorm / v0 design — 2026-07-03

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
the scarce resource. One or two agents active, the rest paused or waiting on
you. gummi's job is to make the "waiting on you" queue visible and make
context-switching between features cheap.

## 2. Core concepts (domain model)

| Concept | Description |
|---|---|
| **Feature** | One unit of work. Has an ID (`FD-042`), a spec file, a worktree + branch, a workflow state, and a profile. The kanban card. |
| **Spec (FD)** | Markdown feature design doc, lives *in the repo* (`.gummi/specs/FD-042-dark-mode.md`). Problem, out of scope, considered solutions, chosen approach, implementation notes, verification plan. The durable artifact agents read and write. |
| **Workflow** | The single, fixed state machine of stages a feature moves through. Never configurable — only skip flags (early phases, set at creation) and rerun transitions (e.g. re-review after fixes). |
| **Stage** | One node in the workflow. Declares: agent action, interaction mode (`interactive` / `autonomous` / `manual`), completion gate, and which *role* performs it. |
| **Role** | A named agent capability slot: `architect`, `implementer`, `reviewer`, `scribe`. Workflows reference roles, never concrete models. |
| **Profile** | Maps roles → concrete agent configs (adapter, model, provider/BYOK env, permission level). Selected per feature. `premium`, `thrifty`, `local-heavy`, ... |
| **Session** | One live agent conversation bound to a feature + stage. Can be attached (focused in TUI), running in background, or paused. |
| **Worktree** | Git worktree per feature: `.gummi/worktrees/FD-042/`, branch `gummi/FD-042-dark-mode`. Created at spec approval, removed after merge. |

### Why roles indirect between workflow and profile

The workflow says *"the review stage is performed by `reviewer`, autonomously"*.
The profile says *"`reviewer` = copilot with Claude Opus"* or *"`reviewer` =
copilot BYOK → local llama.cpp with Qwen-Coder"*. Same process, different
spend. This is the single most important design decision for the cost goal:
**the process is fixed; spend is chosen per feature.**

## 3. The workflow

There is exactly one workflow, compiled into gummi — never configurable.
The only degrees of freedom: **skip flags** (Brainstorm and/or Plan can be
marked skip at feature creation for small, obvious work) and **rerun
transitions** (fix → re-review). Review and Verify can never be skipped.

The **quick route** is a named preset over those flags, not a third
workflow: created with both skips plus a marker that flips the Spec
stage into a one-pass flavor — the architect asks its few clarifying
questions up front, then drafts the whole spec (implementation steps
folded into Implementation notes, since no Plan stage follows) for the
user to steer and approve. It skips gates, never artifacts: the spec is
a normal spec, so a quick item that turns out bigger than it looked
escalates for free — restoring the Plan stage (`P`) re-routes approval
through plan. Skip flags loosen in one direction only: clearing a flag
(adding a stage back) is always legal, setting one mid-flight never is.

```
            ┌──────────┐    ┌──────────┐    ┌──────────┐
  todo ───▶ │ Brainstorm│──▶│   Spec    │──▶│   Plan   │──▶ gate: you approve plan
            │(interactive)  │(interactive)  │(autonomous│
            └──────────┘    └──────────┘    │ or inter.)│
                                            └──────────┘
            ┌──────────┐    ┌──────────┐    ┌──────────┐
        ──▶ │Implement │──▶ │  Review  │──▶ │  Verify  │──▶ gate: checks green
            │(autonomous,   │(autonomous,   │(autonomous:
            │ pausable)     │ fresh context)│ build/test/lint
            └──────────┘    └──────────┘    │ + live check)
                                            └──────────┘
        ──▶ gate: you accept ──▶ Done: landed on main as one squash
                                 commit (you approve the message;
                                 gummi offers worktree cleanup)
```

Stage semantics:

- **Brainstorm** *(interactive, role: architect)* — you talk to the agent
  inside gummi's chat pane. Output: problem statement + candidate approaches
  appended to the spec draft. Unresolved questions flagged with `%%` markers
  (schipper convention) that gummi surfaces as a checklist.
- **Spec** *(interactive, role: architect)* — converge on one approach.
  Gate: you mark the spec **Approved**. gummi promotes the spec to its
  workspace home (`.gummi/specs/`).
- **Plan** *(autonomous or interactive, role: architect)* — numbered,
  tracer-bullet-ordered implementation plan derived from the spec, one
  line per step so critique markers can anchor. Gate: your approval (unless
  the feature was created with the Plan skip flag). Before the gate is
  raised, a **plan critique** runs: a fresh-context reviewer session
  (same cross-model property as Review) that tries to refute the plan —
  security, correctness, completeness — before any implementation
  tokens are spent. Findings land as `%% @reviewer:` threads anchored
  to the plan lines they indict, and missing checks are appended to the
  spec's Verification plan so Verify proves them later. The critique
  ends with the Review verdict grammar: *pass* raises your approval
  gate; *changes* bounces to an automatic replan round (capped at 2,
  then it escalates to you with the findings in the checklist). The
  loop is invisible to the state machine — the feature never leaves
  Plan — and it spends from the Plan stage's budget envelope. A feature
  created with the Plan skip flag skips the critique with it.
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
- **Review** *(autonomous, role: reviewer)* — **fresh session, no shared
  context with the implementer**, ideally a *different model* (cross-model
  review catches more). Findings written into the spec's review section;
  serious findings bounce the feature back to Implement. After fixes, a
  fresh review pass triggers **automatically**, capped (default 2–3 rounds,
  bounded by the protected budget floor); past the cap it escalates to you
  instead of looping.
- **Verify** *(autonomous, role: implementer or scribe)* — two parts:
  the repo's check commands (build/test/lint) always run, and the spec's
  verification plan adds feature-specific live checks the agent
  executes. The commands live in the spec itself — a `gummi-checks`
  block in the Verification plan, auto-discovered by a one-shot scribe
  pass when approval creates the worktree, then human-gated and edited
  like any other spec content (the implementer updates it when a change
  alters how the repo builds/tests). Results recorded in the spec.
  Deterministic floor, adaptive ceiling.
- **Done** — you decide the feature is done: advancing out of Verify
  squash-merges the branch into main as a single commit whose message gummi
  drafts from the spec and the branch — you review, edit, and approve it
  before anything lands. A PR merge is a first-class landing route
  alongside gummi's own squash merge, and works under any of GitHub's
  three merge methods — squash merge, merge commit, or rebase merge. The
  trigger is your `git pull` on main: no new verb, no `pr merge` shim —
  the existing verify→done gate carries the flow, and the fork-point
  invariant continues to hold because a fast-forward pull keeps the
  recorded fork point an ancestor of main. A branch that lands this way
  (or any other manual merge) skips straight to Done. gummi then offers
  worktree cleanup + spec archival.

Every stage transition is recorded (who/what/when) in the feature's history —
the audit trail is part of the quality story.

## 4. Architecture

```
┌─────────────────────────────────────────────────────────┐
│  TUI (Bubble Tea)                                        │
│  kanban │ feature detail │ chat/attach │ activity/queue  │
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
│ (go-git +│  │  specs/)   │  └──┬────────┬───┘
│  git CLI)│  └────────────┘     │        │
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

**Interactive mode is gummi-native chat, not embedded copilot TUI.** Because
the SDK exposes full duplex sessions, the Brainstorm/Spec stages render as a
charm-styled chat pane inside gummi (glamour for markdown, streaming
responses, tool-call collapsibles). One integration surface for both
interactive and autonomous stages; the UI stays coherent and beautiful.

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

- **State machine** per feature — the workflow is compiled in, not
  configured; the engine only honors skip flags and rerun transitions.
  Transitions fire actions (start session, run checks, request human gate)
  and emit events.
- **Scheduler with attention slots**: `max_active` is uncapped by default —
  every autonomous run you start begins immediately, however many cards
  that is. Set it to a positive number and excess sessions queue behind
  the cap; a paused/blocked session frees its slot. Parallel token burn is
  the operator's call: cards drive in disjoint worktrees under per-card
  locks (§8.2 Decision 12), so nothing in the engine needs the
  serialization.
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
  `git worktree add .gummi/worktrees/FD-042 -b gummi/FD-042-slug`.
  Worktrees are nested by design — `gummi init` writes the ignore rules
  (`.gummi/worktrees/`, `.gummi/state/`) so the repo stays clean.
- Handles: creation at spec-approval (drafts live in `.gummi/state/drafts/`
  until then), rebase-on-main helper, dirty-state detection, landed-branch
  detection with worktree cleanup + spec archival.
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
`sandbox: enforce | warn | off` confinement is the second — `enforce`
blocks operations that reach outside the sandbox, `warn` (the default for
allow-all) flags them, `off` trusts the boundary. A main-checkout
tripwire (approving a spec that would run the implementer in the main
checkout rather than a worktree) is the third. Running gummi on a bare
host with allow-all is possible and surfaced, not silently degraded —
the escape hatches below stay the honest path.

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
human is free. gummi renders the question as an inline option picker in
the chat pane (or, when detached, a needs-attention item that jumps to
the picker). The chosen answer is fed back as the tool's result, so the
model's turn resumes in-context — cheaper than a fresh chat round-trip.
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
│  IN PROGRESS          │   Branch: gummi/FD-042-dark-mode   +412 −38 · 9 files    │
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
  - *Chat/attach* — full-screen-ish interactive session for
    brainstorm/spec stages; `esc` detaches, session keeps state.
  - *Spec view* — glamour-rendered FD with `%%` open questions extracted to
    a checklist; gate approval lives here. `tab` switches into **annotate
    mode** (see 6.1).
  - *Diff view* — worktree diff pager before gates, with the same
    annotation mechanics as the spec view.
- **Global**: `n` new feature (a single description line — brainstorm
  develops the rest; profile and skip flags on a demoted options row),
  `tab` cycle gummi's own tabs (§6 below), `1..9` jump to feature, `?` help.

**One board, tabbed.** The split layout the diagram above shows (a kanban
column beside the dashboard, with `→`/`←` moving the arrow keys between
them) is retired. The board is the **backlog**: no column, the full width
is the same super-state-grouped list, and `enter` opens the selected card
on a page of its own (`esc` back, `J`/`K` to the previous/next card
without leaving it). Card titles, badges and the card's own detail get
the whole terminal, and there is only ever one list on screen at a time
— so the arrow keys never have to be aimed and the focus band never has
to disambiguate which pane owns them. Every card verb (`g`, `v`, `m`,
`d`, …) answers at either level (the list or the page) because both
route through the one guarded `boardVerb`; only movement, `enter` and
`esc` differ, and each level's binding table says which (`keymap.go`).

The board sits behind a one-row tab bar shared with the status bar:
`gummi │ board │ inbox │ agent │`. `tab` cycles all three;
`alt+1`/`alt+2`/`alt+3` jump straight to one — alt-prefixed deliberately
(§6.1's `alt+o` reasoning: a plain `ctrl`/bare key a terminal multiplexer
or the hosted agent tab's own pty might already claim). Both are answered
at the top of `handleKey`, above whatever surface holds the keyboard, so
a tab is always one keystroke away from inside a chat, a spec or a diff.
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
punctuation, so it must yield wherever the user types prose: the chat's
message box, the bug-import filter, and the hosted CLI. Those are exactly
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

The board's own overlaying surfaces (chat, spec, diff, ingest review, bug
import, dependency picker) are scoped to the board tab: each belongs to a
card, and a card belongs to the board. Leaving the tab hides them and
returning restores them — never discards, since a chat holds an unsent
input buffer. The inbox tab promotes the needs-attention queue out of its modal
overlay; the agent tab hosts a pty running the user's own coding CLI. Both
are later work — this pass lands the tab shell and the backlog as the
board's only shape.

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
every save. Guidance lives in one place — a `next` card at the bottom,
the same ranked actions the board offers — so a gate that is open *is*
that card and nothing on screen is offered twice. A finished autopilot
run adds a decision receipt above it, reporting what the card chose while
nobody was watching and carrying no actions of its own.

Typing into it is first-class: a line whose first word is one of a closed
vocabulary is a command, and every other line is a message to the agent.
Nothing is fuzzy-matched, so the classification is deterministic — and
because `verify` and `changes` are also ordinary English first words, a
verb that spends money or changes state confirms in place rather than
firing. The mitigation belongs at the point of action, not in the parser.

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
Sub-components (kanban list, chat, spec view) render to strings and are
painted into their rects; dialogs live on an **overlay stack** (gate
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

## 7. What gummi is not (scope guards)

- Not a CI system — Verify runs local checks; real CI stays in your PR flow.
- Not a general agent framework — it orchestrates *existing* coding agents.
- Not a cloud service — single-binary local tool, state in your repo.
- Not tmux — gummi owns its sessions; raw attach is the escape hatch.
- Not a merge pipeline — the one integration gummi does is the squash
  merge onto local main when you accept a feature; PRs, pushing, and
  releasing stay in your hands. A card may name and read the PR it
  lands through — linking it and pulling its review threads in as diff
  annotations — but gummi still never writes to GitHub: no PR creation,
  no push, no merge, no thread resolution, no CI gating.
- Not a process editor — one workflow, compiled in. If the workflow needs
  changing, that's a gummi release, not a config file.

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
Copilot adapter via Go SDK: interactive chat pane (brainstorm/spec) +
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
3. **One strict workflow, never configurable.** No workflow YAML, ever. The
   only flexibility: *skip flags* — Brainstorm and Plan can be marked skip
   at feature creation for small/obvious work, with the quick route as a
   named preset over them (both skips + the one-pass spec flavor) — and
   *rerun transitions* (e.g. re-review after fixes). Skip flags may be
   cleared after creation (restoring a stage is safe), never set. Review
   and Verify are never skippable; the quality floor is non-negotiable.
4. **Review loop**: after the implementer addresses findings, a fresh
   review pass triggers automatically, capped (default 2–3 rounds, and
   bounded by the feature's budget envelope); past the cap it escalates
   to the human instead of looping. The **plan critique** is the same
   pattern at design altitude: critique→replan, capped at 2 rounds,
   then the human gate — catching design-level security/correctness
   flaws before implementation tokens are spent.
5. **Interactive stages are gummi-native chat** over SDK sessions; raw
   copilot attach is an escape hatch only.
6. **The endgame is a squash commit on main.** When you accept a
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
10. **One repo per gummi instance, permanently** — multi-repo is out of
    scope by design, not deferred.
11. **Spec drafts** live in `.gummi/state/drafts/` during Brainstorm/Spec
    (no worktree exists yet); at spec approval the worktree + branch are
    created and the spec is promoted to `.gummi/specs/FD-NNN-slug.md` in
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

## 11. Spec ingestion — decomposing an existing spec into features

gummi's unit is a Feature (FD): one PR-sized branch that flows through the
fixed workflow. Today features are *born blank* — the creation form takes a
single title line, mints an ID, and stamps the empty spec template; brainstorm
and spec then author the FD from scratch. But work often *starts* from a
document that already exists — a PRD, a design doc, a meeting write-up that
describes many features at once. Ingestion inverts the birth path: gummi reads
a source spec and **decomposes it into N pre-seeded FDs**, so brainstorm/spec
*refine* an already-populated draft instead of starting cold.

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
  the **how**; converging on an approach is still brainstorm's job.
- **open questions** — anything the decomposition is unsure about is emitted as
  a `%%` marker on its anchor, so **decomposition uncertainty becomes the FD's
  open-questions checklist for free**, with no new machinery.
- **depends_on** — other proposals this one needs, recorded as a first-class
  edge in the dependency store (§11.4a), not prose.
- **skip hint** — a well-specified slice may propose the Brainstorm skip flag.

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
a mini-project that brainstorm has to re-split. A typical PRD lands as a handful
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
  gate operation — a too-coarse slice is better re-split by brainstorm once it
  is a real feature, so the gate only coarsens (drop/merge) and edits. On the
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
`--gate-approval=caller` path uses — reports the same blockage before
offering to approve a coding gate. Both TUI and headless drivers route
through `Advance`, so the gate cannot be bypassed by a future driver; no
transitive closure is walked (only direct dependencies block), and design
stages (brainstorm/spec/plan, triage/diagnose) are never gated — only entry
to the coding stage is. The board and spec view resolve and render each
dependency's live status rather than static prose.

Each piece is independently testable: domain types + the seeded template (golden
tests, no agent), `Engine.Ingest` + the tool (unit-tested against the `Fake`
agent, both the client-tool and fenced-convention paths), materialization
(headless engine/state), the `gummi ingest` CLI, and the TUI review pane (driven
through simulated key presses against a `Fake` architect).

### 11.5 Deferred

- **Persisted coverage report** — keeping the source→FD map queryable after
  ingestion, for traceability audits against the original document.

## 12. Bugs — the second workflow

Features are design-driven: brainstorm an approach, spec it, plan it, build it.
Bugs are diagnosis-driven: something already works wrong, and the work is to
reproduce it, find why, and fix it without regressing. gummi models a bug as a
second **kind** of work item that shares everything structural with a feature —
the store, the engine, the worktree, the board, the never-skippable Review →
Verify quality floor — but runs its own compiled-in workflow and carries a bug
report instead of a spec.

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

### 12.2 The bug workflow

One more fixed graph, still never configurable:

```
  todo ──▶ Triage ──▶ Diagnose ──▶ Fix ──▶ Review ──▶ Verify ──▶ Done
           (interactive) (interactive) (autonomous) (shared quality floor)
```

- **Triage** *(interactive, architect; skippable)* — confirm and reproduce the
  bug; pin down repro steps, expected vs actual, environment, severity. The
  analog of Brainstorm.
- **Diagnose** *(interactive, architect; skippable)* — converge on the root
  cause and record it in the report; gated on human approval. The analog of
  Spec. (Both design-side stages are skippable for an obvious bug; when both are
  skipped a `todo → fix` edge applies, since they are adjacent.)
- **Fix** *(autonomous, implementer)* — implement the smallest change that
  resolves the bug **and add a regression test**. The analog of Implement; the
  Review/Verify rerun edges bounce here.
- **Review / Verify** — the same stages features use, so the scheduler, slots,
  budgets, and board columns are untouched. Verify gains a sharp bug meaning:
  the deterministic repo checks still run, and on top the reproduction must no
  longer reproduce and a regression test must cover the fix.

### 12.3 The bug report

A bug's durable artifact is `.gummi/bugs/BG-NNN-slug.md` (the analog of the
spec): Summary · Reproduction · Expected vs actual · Environment · Root cause ·
Fix · Review · Verification. Symptoms are seeded from the source; Root cause and
Fix stay open `%%` prompts — converging on *why* and *how* is diagnose/fix work,
exactly as the spec's chosen-approach is brainstorm's.

### 12.4 Ingestion — sources, not decomposition

Bug ingestion reuses spec ingestion's pipeline shape — source → proposals →
human gate → materialize — but the source yields *discrete* bugs rather than one
document to decompose, so there is no architect pass and no coverage map. A
`BugSource` is the seam:

- **GitHub** — `gh issue list` against a target repo (default: the repo's origin
  remote; overridable to any `owner/repo`), filtered by label/state. The import
  is **deterministic and agent-free**: one issue → one proposal, body verbatim
  into Summary, labels mapped to severity. Per-bug reproduction and root-cause
  enrichment is the triage/diagnose stages' job, not the source's — "tool owns
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
should never land in todo from one keystroke. The TUI import-review pane is a
searchable picker: it opens with a live substring filter over the fetched
issues' title/label/body already focused, `Tab` swaps focus between typing
and commanding the list (rename/edit), and `enter` imports exactly the
highlighted issue — the filter narrows what you see, never what gets
created. The CLI (`gummi bugs ingest`, gated y/N or `--yes`) keeps its batch
import for scripted use, plus an additive `--issue N` that resolves N against
the same fetched batch and materializes just that one bug.

### 12.5 Deferred

- **Severity-ordered backlog** — using the seeded severity to rank the todo
  column, once there are enough bugs for ordering to matter.
- **Agent triage-at-ingest** — an optional pass that dedupes/clusters near-
  duplicate issues before the gate, if verbatim import proves too noisy.

## 13. Research — the third workflow

Features are design-driven and bugs are diagnosis-driven; research is
investigation-driven: the ask is open enough that neither a spec nor a bug
report fits, and the work is to ground an answer before anything gets built.
gummi models research as a third **kind** of work item that shares everything
structural with a feature or a bug — the store, the engine, the worktree
machinery, the board, the never-skippable Review → Verify quality floor —
but runs its own compiled-in workflow and carries a research document instead
of a spec or a bug report.

### 13.1 The third kind

A work item's `Kind` gains a third value: `research`. `RS-NNN` IDs draw from
the same monotonic counter features and bugs share, so numbers never
collide. Unlike a feature or a bug, an `RS` card has **no branch and no
worktree** — its artifact resolves to `.gummi/research/RS-NNN-slug.md` in the
main checkout, and investigate runs there directly. Only three things branch
on kind, same as bugs: which workflow governs transitions, which template
seeds the artifact, and a board badge. The empty kind still reads as a
feature, so nothing predating research needs a backfill.

### 13.2 The research workflow

One more fixed graph, still never configurable, and with **no skip edges at
all**:

```
  todo ──▶ Investigate ──▶ Shape ──▶ Review ──▶ Verify ──▶ Done
           (autonomous)    (interactive)  (shared quality floor)
```

- **Investigate** *(autonomous, architect)* — the work stage: ground the
  brief against the repo (and any cited external sources) and write up
  findings with citations. Worktree-less — it runs in the main checkout, not
  a branch. It is also the stage rerun/bounce edges land on, the research
  analog of Implement/Fix.
- **Shape** *(interactive, architect)* — converge the findings into a
  recommended direction and a proposed slice breakdown; the convergence gate
  analogous to Spec (features) or Diagnose (bugs).
- **Review / Verify are reused verbatim** — same stages, same reviewround
  cap, same escalation, same board columns as features and bugs, sharpened
  only in *what content they enforce* (§13.4).

Unlike the feature and bug graphs, `researchGraph`
(`internal/workflow/workflow.go` L80–98) carries no skip edges at all —
Investigate and Shape are both first-class stages that always run before the
shared Review/Verify floor. Roles reuse `architect` and `reviewer`; there is
no `profiles.yaml` migration for research.

### 13.3 The research document

The template's sections and the decompose gate's `propose_features`-shaped
input (§13.5) are designed together, so turning an approved document into
FDs is nearly free — the template *is* the ingest contract:

| section | seeded by | consumed by |
|---|---|---|
| `## Brief` | the ask, in the requester's own words | Investigate (the question to ground) |
| `## Questions` | the open questions the research must answer | Investigate (grounding target), §13.4's coverage check |
| `## Findings` | Investigate — prose with inline `path:line`/`path:start-end` citations | §13.4's citation check, Shape |
| `## Constraints` | the constraints the investigation is bound by | Shape (direction must fit them) |
| `## Options` | Shape — candidate directions with tradeoffs | Shape's own convergence |
| `## Direction` | Shape — the recommended direction and why it wins | the reviewer, and the reader of `done` |
| `## Slices` | Shape — one row per proposed follow-on (title/one-liner/depends-on/requirements/id) | §13.5's decompose gate (`propose_features`-shaped rows), back-annotated with minted FD ids |
| `## Out of scope` | Shape — what the research deliberately won't cover | §13.4's coverage check (an explicit out-of-scope line settles a question without a slice) |
| `## Open risks` | Shape — risks and what would de-risk each | the reviewer |
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

A safety note: research's autonomous Investigate runs in the main checkout
rather than a worktree, under the sandbox `warn` tripwire and the reviewer's
per-role read-only deny policy — both defined in §4.4.

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

### 13.6 Deferred

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

## 14. Non-interactive driver & skill distribution

The TUI assumes a human at the keyboard. A second driver runs the **same
engine and the same quality floor** unattended, so a calling agent can ship a
feature end to end — and gummi ships the skill that teaches that agent how.
Nothing here softens the invariant workflow: the driver changes *who approves
a gate*, not *whether* the floor runs. Anything a human would resolve becomes a
durable, resumable escalation with a non-zero exit — deterministic failure over
silent degradation.

### 14.1 The driver

One `gummi run` process drives exactly one feature (a free-form description),
via the quick route by default, holds the feature's per-card lock, streams
milestone + decision NDJSON, and **stops at a verified branch — it never
merges**. The engine's full restartability (SQLite state, spec on the branch,
session resume) makes `resume` free: each invocation runs forward until the
caller must decide, then exits.

- **Gate control** — `--gate-approval=auto` (default) auto-crosses design
  gates; `=caller` checkpoints them for `resume --approve`/`--request-changes`.
  Blockers (open `%%`/diff threads) are honored either way; Review and Verify
  are never a caller gate — Verify is the floor's stop-at-verified.
- **Verify-fail bounce** — a `verify FAILED` (or a review cap-hit) escalation
  is un-parked with `resume --bounce [--note <why>]`, which rewinds the
  feature to its work stage (implement/fix) and drives the review→verify tail
  again — the CLI counterpart of the TUI's `b` key (§10 review floor's rerun
  edge). The `--note` becomes an addendum to the reborn implement kickoff,
  alongside any open `%%` diff/spec annotations the engine already folds in.
- **Dependency gate** — a card cannot enter its coding stage while a direct
  dependency (§11.4a) is unmet; `Advance` returns `StatusBlockedDependency`,
  which the driver reports as the same `blocked` event as an open `%%`/diff
  thread, naming the outstanding card(s) in `blocking_deps`.
- **Landing and cleanup are separate verbs, not part of `run`/`resume`.**
  `run`/`resume` never merge; `gummi merge <id> -m <message>` is the headless
  counterpart of the TUI's `m`, requiring the card at a verified branch and a
  Conventional Commits message with no diff dump or agent attribution before
  it will touch git. `gummi clean <id>` is the counterpart of `c`, removing a
  landed card's worktree and branch. `gummi commit <id> -m <message>` commits
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
- **Envelope required** — a run refuses to start without one (`--envelope N` or
  `GUMMI_ENVELOPE`); exhaustion fails loud (no auto-topup).
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
  false-positives) — and `done` — the branch was **squash-merged** into main.
  A headless run ends at `verified:true`/`done:false`; only a land flips
  `done`.

### 14.2 The exit contract

Each `run`/`resume` ends on a typed `Status` with a stable exit code, so a
caller branches on the result without parsing stdout:

| exit | status | meaning |
|---|---|---|
| `0` | `done` | verified branch ready — report it, stop |
| `0` | `stopped` | `--until` reached its clean stop — `resume --approve` to continue |
| `1` | `error` | setup/agent failure — nothing partial landed |
| `2` | `question` | delegated `ask_user`, or a caller design gate awaiting a decision |
| `3` | `blocked` | open `%%`/diff threads block a gate |
| `4` | `escalation` | a rerun/critique cap was hit, or a stage returned no clear verdict |
| `5` | `exhausted` | the credit envelope ran dry |
| `6` | `timeout` | a stage went quiet past the inactivity budget |

The exit/event `done` above names the run outcome — *a verified branch is
ready* — not a merge; it is deliberately distinct from `status --json`'s `done`
field, which is true only once the branch is **merged**. A caller keying off a
completed run should poll `status`'s `verified`, not its `done` (§14.1).

Long autonomous stretches (implement → review → verify) carry no caller
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
