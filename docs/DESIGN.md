# gummi — Design Document

> A meta-harness for coding agents. Drive a fleet of agents through a
> spec-driven workflow across git worktrees, from one beautiful TUI.

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
| **Spec (FD)** | Markdown feature design doc, lives *in the repo* (`.gummi/specs/FD-042-dark-mode.md`). Problem, considered solutions, chosen approach, implementation notes, verification plan. The durable artifact agents read and write. |
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
        ──▶ Done: verified branch, handed to you ──▶ (you merge/PR it;
                                                      gummi offers cleanup)
```

Stage semantics:

- **Brainstorm** *(interactive, role: architect)* — you talk to the agent
  inside gummi's chat pane. Output: problem statement + candidate approaches
  appended to the spec draft. Unresolved questions flagged with `%%` markers
  (schipper convention) that gummi surfaces as a checklist.
- **Spec** *(interactive, role: architect)* — converge on one approach.
  Gate: you mark the spec **Approved**. gummi commits the spec to the branch.
- **Plan** *(autonomous or interactive, role: architect)* — line-level
  implementation plan derived from the spec. Gate: your approval (unless
  the feature was created with the Plan skip flag).
- **Implement** *(autonomous, role: implementer)* — agent works in the
  worktree with the spec + plan as context. Streams progress into the feature
  card. Pauses when it needs input (or on permission requests in `guarded`
  mode) → feature jumps into your "needs attention" queue.
- **Review** *(autonomous, role: reviewer)* — **fresh session, no shared
  context with the implementer**, ideally a *different model* (cross-model
  review catches more). Findings written into the spec's review section;
  serious findings bounce the feature back to Implement. After fixes, a
  fresh review pass triggers **automatically**, capped (default 2–3 rounds,
  bounded by the protected budget floor); past the cap it escalates to you
  instead of looping.
- **Verify** *(autonomous, role: implementer or scribe)* — two parts:
  the repo's fixed check commands from `.gummi/config.yaml`
  (build/test/lint) always run, and the spec's verification plan adds
  feature-specific live checks the agent executes. Results recorded in
  the spec. Deterministic floor, adaptive ceiling.
- **Done** — gummi's job ends at a verified branch. Integration (PR,
  merge, release) is yours; gummi detects when the branch lands on main
  and offers worktree cleanup + spec archival.

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

### 4.2 Orchestrator

- **State machine** per feature — the workflow is compiled in, not
  configured; the engine only honors skip flags and rerun transitions.
  Transitions fire actions (start session, run checks, request human gate)
  and emit events.
- **Scheduler with attention slots**: `max_active` (default `1`) autonomous
  session; excess sessions queue; interactive stages only run when you
  attach. A paused/blocked session frees its slot. This matches "one active,
  one paused" reality and caps parallel token burn.
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

On first run outside an obvious container (no `/.dockerenv`, no
`$CONTAINER`-ish markers), gummi shows a one-time "you're on a bare host
with allow-all permissions" warning rather than silently degrading either
safety or flow.

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

**Layer 3 — the spend plan.** Each feature carries a spend plan: an envelope
plus per-stage allocations, recorded in the spec and shown on the kanban
card.

```yaml
# defaults per profile; overridable per feature at creation or plan-approval
budget:
  envelope: 300            # credits for the whole feature
  allocation:              # of envelope
    brainstorm+spec: 15%
    plan:            10%
    implement:       45%
    review:          15%   # protected — implement cannot borrow from it
    verify:          10%
    reserve:         5%    # orchestrator-held; released only at a human gate
```

Rules that make the plan real rather than decorative:

- **Rollover forward**: unspent budget from a completed stage flows into the
  next stage — finishing the spec cheaply buys implementation headroom.
- **Protected quality floor**: review/verify allocations can't be borrowed
  against. A feature that exhausts implementation budget pauses; it doesn't
  eat its own review.
- **Human gates on exhaustion**: when a stage runs dry, the gate offers:
  *top up* (release reserve or raise envelope), *downshift* (re-route the
  role to a cheaper/BYOK model from the profile's fallback chain and
  resume from checkpoint), *split* (agent proposes cutting scope into a
  follow-up FD), or *park*.
- **Plan-time estimation (later)**: at plan approval, a scribe-role pass can
  size the work (files touched, test surface, historical spend on similar
  features from `.gummi/state`) and propose adjusted allocations. v1 ships
  static profile percentages; estimation is an M4+ refinement.

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
- **Global**: `n` new feature (huh form: title, one-liner, profile),
  `tab` cycle needs-attention queue, `1..9` jump to feature, `?` help.

### 6.1 Annotation editor (line-level review, like a PR)

Gates shouldn't force feedback through chat ("the third paragraph is wrong,
the one about caching…"). Specs and diffs get a **review-style annotator**:
move a line cursor, mark a line or range, attach a comment — exactly the
GitHub PR review interaction, in the TUI.

**Interaction.** In spec/diff view, `tab` toggles read mode ↔ annotate mode.
Annotate mode renders the *source* (raw markdown / unified diff) with line
numbers and a cursor — glamour's pretty rendering re-wraps text, so stable
line addressing requires the source view; read mode stays glamour.

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
  diff-annotate mode instead of building one.
- **Restraint**: generous padding, subtle separators over heavy borders,
  one accent per surface, spinners only where something is actually
  happening.

**Quality enforcement.** Crush golden-tests its UI (`x/exp/golden`); gummi
does the same from M0 — every component gets golden-file snapshot tests at
several widths, so visual regressions fail CI, not eyes. Demo GIFs via
`vhs`, scripted and reproducible.

## 7. What gummi is not (scope guards)

- Not a CI system — Verify runs local checks; real CI stays in your PR flow.
- Not a general agent framework — it orchestrates *existing* coding agents.
- Not a cloud service — single-binary local tool, state in your repo.
- Not tmux — gummi owns its sessions; raw attach is the escape hatch.
- Not a merge pipeline — the deliverable is a verified branch; PRs,
  merging, and releasing stay in your hands.
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
Spec annotation editor (`%%`-based, annotate mode + gate feedback loop).

**M3 — profiles & cost**
`profiles.yaml`, per-role model/BYOK env injection, llama.cpp smoke test,
spend metering + kanban cost column, cross-model review default. Budget
layers 1+2: `--max-ai-credits` caps per session, budget-aware system hints,
threshold nudges, budget-exhausted checkpointing. Spend plans with rollover,
protected review floor, and exhaustion gates (layer 3).

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
   at feature creation for small/obvious work — and *rerun transitions*
   (e.g. re-review after fixes). Review and Verify are never skippable;
   the quality floor is non-negotiable.
4. **Review loop**: after the implementer addresses findings, a fresh
   review pass triggers automatically, capped (default 2–3 rounds, and
   bounded by the protected budget floor); past the cap it escalates to
   the human instead of looping.
5. **Interactive stages are gummi-native chat** over SDK sessions; raw
   copilot attach is an escape hatch only.
6. **The endgame is a verified branch.** No PR or merge automation —
   gummi's job ends when Verify passes; integration is yours. gummi
   detects when a feature branch has landed on main and offers worktree
   cleanup.
7. **Verify = repo config + spec plan**: fixed check commands from
   `.gummi/config.yaml` (build/test/lint) always run; the spec's
   verification plan adds feature-specific live checks the agent executes.
8. **First-class providers**: GitHub Copilot Pro/Pro+ (premium-request
   pool) and OpenAI-compatible BYOK endpoints (llama.cpp, vLLM, hosted).
   Profiles are designed around exactly these two paths.
9. **Attention slots default to 1** active autonomous session
   (configurable).
10. **One repo per gummi instance, permanently** — multi-repo is out of
    scope by design, not deferred.
11. **Spec drafts** live in `.gummi/state/drafts/` during Brainstorm/Spec
    (no worktree exists yet); at spec approval the worktree + branch are
    created and the spec is committed there as
    `.gummi/specs/FD-NNN-slug.md`, so the spec travels with its feature
    branch.

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

## 11. References

- Spec-driven parallel agents workflow: <https://schipper.ai/posts/parallel-coding-agents/>
- Copilot SDK (Go): <https://github.com/github/copilot-sdk>
- Copilot SDK GA announcement: <https://github.blog/changelog/2026-06-02-copilot-sdk-is-now-generally-available/>
- Copilot CLI BYOK/local models: <https://github.blog/changelog/2026-04-07-copilot-cli-now-supports-byok-and-local-models/>
- BYOK how-to (env vars): <https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/use-byok-models>
- Charm libraries: <https://charm.land/>
- Crush (the visual bar; UI architecture studied from
  `internal/ui/AGENTS.md`): <https://github.com/charmbracelet/crush>
