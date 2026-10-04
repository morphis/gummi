# AGENTS.md

Guidance for AI agents working on **gummi**. Read this first, then
`docs/DESIGN.md` before making non-trivial changes.

## What gummi is

gummi is a **meta-harness for coding agents**: a single binary that
drives a fleet of coding agents through a fixed, spec-driven workflow,
each work item on its own git worktree and branch. It orchestrates other
agents — it is not itself a coding agent. Two ways drive the same
engine and quality floor: a human at the keyboard in the TUI; and an
agent, script, or CI driving a *fresh* gummi from *outside*, via the
headless CLI driver (`gummi run`/`resume`). An agent working *inside* a
card reaches gummi's tools only through that card's own session MCP
endpoint (§16) — there is no board-level agent. See `internal/driver`
and README's "Running headlessly" for the outside path.

- **Language:** Go 1.26, single module `github.com/morphis/gummi`.
- **Binary:** `cmd/gummi` → `bin/gummi`. Run with no args inside a git repo.
- **TUI stack:** Bubbletea v2 / Lipgloss v2 / Bubbles v2 (Charm).
- **Storage:** SQLite (`modernc.org/sqlite`, pure-Go, no cgo).
- **Runtime workspace:** a `.gummi/` dir created lazily in the target
  repo (state DB, `config.yaml`, `profiles.yaml`, `worktrees/`, `scratch/`).
  Gitignored.

The mental model: every unit of work is a **card** moving through a
compiled-in workflow. Each stage is performed by a **role**
(`architect`, `implementer`, `reviewer`, `scribe`); a **profile** maps
roles to concrete models. The durable context carrier between stages is
a **markdown spec on the feature's branch**, not a transcript.

```
todo → plan → implement → verify → done      (every kind, one graph)
     ↖──────────┘        ↖────────┘          rerun edges
```

`plan` is the design stage: explore, converge, write the plan. The kind
(FD/BG/RS) selects each stage's **contract** — its hints, its artifact,
the sections its gate demands — never a graph of its own. Every stage
ends with a critique pass before its gate; review is a pass, not a
stage.

Read `README.md` for the user-facing feature tour and key bindings.

## Package map (`internal/`)

Each package has a real doc comment at the top of its lead file — read it
before touching the package. Dependency flow is roughly
domain → state/spec/workflow → engine → ui, with agent/worktree/verify as
leaf services.

| package | responsibility |
|---|---|
| `domain` | Core types: features, bugs, stages, work items. No I/O. |
| `workflow` | The single fixed state machine: one graph, its legal transitions and rerun edges. **Compiled in, never configurable.** |
| `gatepolicy` | Shared checkpoint policy used by both the TUI and headless driver to raise and cross workflow gates. |
| `spec` | The markdown spec artifact + its `gummi-checks` verification block. |
| `state` | SQLite store: features, sessions, diff annotations, dependency edges, sequences, workspace. |
| `engine` | The orchestrator. Binds stages to agent sessions, starts autonomous runs (nothing caps or queues them), routes turns, streams activity. Start here to trace behavior. |
| `agent` | Adapter layer over concrete agents. Interfaces hide the backend: `copilot` (default), `opencode`, `headless`, plus `fake.go` for tests. |
| `worktree` | Per-feature git worktrees under `.gummi/worktrees/`: create, rebase-on-main, dirty/landed detection, cleanup. Every feature and bug stage runs in the card's own branch worktree, from its first stage. Research keeps the per-card **scratch tree** (`scratch.go`, `.gummi/scratch/<ID>`) — a detached throwaway checkout, since a research card never gets a branch. `adopt.go` is the other half: attaching to a branch gummi did **not** cut, and the custody rules that follow from that. |
| `verify` | Runs a spec's `gummi-checks` in the worktree, reports pass/fail. |
| `stack` | Pure policy for a **stack** — a chain of cards whose branches fork from one another. Answers what each card forks from, which are sitting on commits that have moved, and whether one may land yet. No git, no store, no clock. Read by `Engine.StackTick`, the worktree base seam and the board alike. |
| `schedule` | Pure policy for **schedules and heartbeats** — freeform sessions that come back on a clock. Cron parse, compile, `Due`, `Next` and `Advance` (missed fires coalesce). No clock, no IO, no store. Read by the store's write boundary (`Check`), `Engine.ScheduleTick` and the faces. |
| `cardrun` | Pure read model: one card's record → how it ran (its passes, what each cost, how much was rework, how long it waited). Shared by the card's run tab, `status --stats` and the week view. |
| `branchlog` | Pure read model of a card's own commits (`Manager.Log` → rows: checkpoint, pushed, attribution) and the one rule for whether they may be rewritten (`Refusal`). Its `Env` runs the reads and `Manager.Rewrite`. Read by the TUI's log tab, the web page's, and `gummi log`/`rewrite` (DESIGN §21). |
| `fleetrun` | Pure fold at the workspace scale: every card's run → the stats tab's report (window and all-time money, the clock, peak concurrency, and the timeline lanes). Charges a pass to the window it started in; reuses `cardrun` per card, so the tab cannot disagree with the cards it is made of. |
| `diffannot` | Anchors line comments to diff content (survives minor rebases). |
| `config` | Loads `.gummi/config.yaml` (permission mode only, since M5). |
| `notify` | Terminal bell / desktop notification on needs-attention. |
| `atomicfile` | Crash-safe file writes for pre-approval drafts (no git backstop). |
| `ui` | The Bubbletea TUI: board, chat, diff/spec views, inbox, dialogs. |
| `driver` | The headless counterpart of the TUI's autonomous loop: drives `run`/`resume` over the engine, emits NDJSON + typed exit statuses, holds the `.gummi` lock. |
| `planround` / `reviewround` | Single seam persisting the plan-critique / review→fix round counters across process boundaries, so the TUI and headless driver can't drift apart on rerun caps. |
| `sandbox` | Resolves effective confinement (`enforce`/`warn`/`off`) from config, profile, and backend capabilities; shared by engine refusal and `doctor`. |
| `verdict` | Shared stage-verdict grammar the TUI and headless driver both parse, per DESIGN §13. |
| `engine/freeformsession.go` | The one session that is not a stage run and still writes: a freeform card's whole working life — its worktree, its per-card lock and its envelope. It never commits for the agent: every commit on its branch is one the agent made on purpose. Built from `consultsession.go`'s lifecycle and `boardsession.go`'s absences; read all three together. The web face calls it a **session**: `engine/sessionmodel.go` holds the agent and model a session picks for itself, starting a backend the board did not launch, and switching mid-conversation, and `ui/websession.go` holds the picker's catalog and "write a spec" (DESIGN §19.8). |
| `web` | `gummi web`: serves the board to a browser as a page of its own (DESIGN §20). Holds no board: it asks the TUI's model, run headless behind `ui.Bridge`, for every read and write. Pairing and device tokens (`pair.go`), same-origin and auth (`auth.go`), the SSE hub (`hub.go`), one `routes_*.go` per area, and the page itself under `assets/` (plain ES modules, no build step, strict CSP — no inline script or style). |
| `web/push` | Web Push from the host with the standard library alone: VAPID identity, RFC 8291 encryption, the per-device subscription store, and the fan-out notifier the board's attention hook posts to. |
| `webapi` | The JSON contract between `internal/web` and its page: types only, golden-tested shapes. Change a shape here first. |
| `threadfold` | Pure fold of a card's event log into what a thread draws — stage sessions, answered decisions, autopilot stretches, and the structured `Items` the web page renders — in the words the TUI thread uses. Both faces read it. |
| `decisions` | Pure rules for a card's open decisions: which one a card shows (`Rank`), its attention lane, how an `ask_user` question is offered as options, and what an answer says. Both faces read it. |
| `mcp` | Backs the hidden `gummi __mcp` shim: bridges an agent backend's MCP stdio calls to a live stage session's tools (`--feature <id>`). |

`cmd/gummi` holds `main.go` plus the board's supporting subcommands: `ingest`
(spec decomposition), `bugs` (GitHub issue import / manual add), and the
headless driver surface — `run`, `resume`, `status` (`--stats` reports where a
card's credits and hours went), `spec`, `diff`, `verify`,
`merge`, `clean`, `deps`, `stack`, `doctor`, `skill`. See README's "Running headlessly"
for the driver's command grammar and exit-status table.

**Flags are declared once.** Cobra owns routing, help, completion *and*
parsing: every flag is declared in `cobra.go`'s `bind*Flags` functions (the
surface the driving verbs share lives in `flags.go` as `driveFlags`), and a
command body reads what cobra parsed through `cliFlags`. Do not add a
`flag.NewFlagSet` to a command — `TestNoCommandParsesItsOwnFlags` fails if you
do. A second parser is what let `stack new --name`, `stack add --pos` and
`merge --m` each be declared, advertised and then rejected at parse.

`gummi skill` generates its bundle (`SKILL.md` + `references/`) from that same
cobra tree, so the doc cannot name a flag the binary lacks or miss one it has.
Tests drive verbs through the real tree via `runCLI` rather than calling
`runXxx` directly; that is what makes an unreachable flag fail a test.

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

To try the headless driver instead of the TUI, `make demo` still gives you
a throwaway repo — run `bin/gummi run --envelope 500 "<description>"` in it.

Driving a **goal** costs real money and its budget arithmetic is the part
hardest to eyeball, so run `scripts/goal-ledger.py ./bin/gummi <workspace>
GL-NNN` beside it: it polls `status --json` and asserts DESIGN §17.3's
invariants every tick, exiting non-zero if one ever broke. Most questions
about that arithmetic need no goal at all — `goalpolicy.Decide` is a pure
function, so a throwaway test that holds cards with a known cost, calls
it, applies the actions and books the credits answers wide/deep/thin/fat
budgets in milliseconds and for nothing.

Running the real TUI (`bin/gummi`) needs a git repo and, for the default
backend, an authenticated GitHub Copilot CLI. To drive agents without
Copilot auth, set `GUMMI_AGENT=headless` with `GUMMI_AGENT_CMD` pointed at
a BYOK/local endpoint's adapter (`GUMMI_HEADLESS_CREDITS_PER_1K` prices its
spend into credits). With no usable agent, creation/specs/worktrees/gates
still work — the board just stays static. Key env vars are tabled in
`README.md#configuration`.

## Conventions & guardrails

- **The workflow is invariant — for the cards that are in it.** No
  implementation without an approved design; no merge without a critique
  **and** verify. There are no routes and no skips, and no configuration
  may soften this — it's a core design decision, not an oversight.
  The one card outside it is a **freeform card** (`FF`, `KindFreeform`,
  DESIGN §19): no stages, no gates, no critique, no verify. It is not a
  route through the graph — it holds `StageOpen`, which has no edge in the
  transition table either way — and it lands on a human's read of its diff
  instead of on a verified branch. Both floors live in one predicate,
  `domain.Feature.MayLand`; read it before touching any landing path. A
  freeform card may not adopt a branch, belong to a goal, or be driven by
  `run`/`resume`.
- **The spec is the context carrier**, not chat transcripts. Keep token
  windows small: pass specs between stages, not conversation history.
- **gummi's job ends at a verified branch.** It does not open PRs or
  release. Don't add that scope without checking `docs/DESIGN.md §7`
  (scope guards) and §10 (Decisions — binding). Stacks (§18) replay
  branches locally and print the `git push --force-with-lease` they need;
  they still never push, create a PR, or retarget one.
- **An adopted branch is held, never owned.** A card can be minted onto a
  branch gummi did not cut (`--adopt`, `--pr`; DESIGN §10 D22). gummi may
  add commits to it and nothing else: it never deletes one (`clean` keeps
  it), never rebases or force-pushes one, and never holds the card
  responsible for what was already failing on it. An adopted card still
  walks the whole graph — the plan stage reads the inherited diff and
  designs the rework — because the alternative is the first hole in the
  quality floor. `TestAnAdoptedCardWalksTheWholeGraph` and
  `TestAnAdoptedBranchIsNeverDeleted` assert the two halves.
- **A stack is topology; a dependency is scheduling.** A stack position
  says "my branch forks from that card's branch" and must never gate a
  card from running — a dependency is met only at `StageDone`, so a
  position that implied one would serialize exactly the parallel work a
  stack exists for. See DESIGN §18.1; `internal/stack` enforces it and
  `TestAStackNeverBlocksWork` asserts it.
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

- Behavior of a stage/transition → `internal/workflow` then `internal/engine`.
- "why did this card's branch move" / stacks → `internal/stack` for the
  rules, `internal/engine/stack.go` for the tick, `worktree.Manager.baseRev`
  for what a card forks from (and read its comment before touching any
  `"HEAD"` in that package — the token means two different things there).
- A TUI bug → `internal/ui` (`board.go`, `chat.go`, `diffview.go`, `inbox.go`).
- A web face bug → `internal/web` for the server and page, `internal/ui/web*.go`
  and `internal/ui/bridge.go` for what the page is told and how its writes
  reach the board. The web face never decides anything itself: if a web
  answer differs from the TUI's, the fix belongs in the shared code
  (`threadfold`, `decisions`, the Shell), and `TestWebDecisionsMatchThePicker`
  is the test that should have caught it. Browser tests live in `e2e/web`
  (`scripts/web-e2e.sh`, Playwright).
- "what did this card cost / how did it run" → `internal/cardrun`, then its
  three readers (`internal/ui/statsview.go`, `cmd/gummi/statusstats.go`,
  `internal/ui/week.go`). The derivation lives in one place on purpose: two
  surfaces that disagree about what a card cost are worse than one.
- "how is the whole board running / the timeline" → `internal/fleetrun`,
  then its one reader (`internal/ui/wsstats.go`). Same seam, one scale up:
  the fold reuses `cardrun` per card and states its own attribution rules
  (a pass is charged to the window it started in; the window clock counts
  an open session to the right edge).
- "can gummi work on a branch it did not cut" → `internal/worktree/adopt.go`
  (attach + inspect), `internal/cardmint` for the mint-time half, and
  `adoptedHint` in `internal/engine/hints.go` for what the stages are told.
- Agent/model wiring → `internal/agent` + `internal/engine/profiles.go`.
- "why can this card land without verifying" / freeform cards →
  `domain.Feature.MayLand` for the rule, `internal/engine/freeformsession.go`
  for the session, `docs/DESIGN.md` §19 for why the second floor exists and
  what it costs.
- Anything architectural or a "why is it this way" question →
  `docs/DESIGN.md` (its **Decisions** list in §10 is binding).
