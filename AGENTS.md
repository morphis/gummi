# AGENTS.md

Guidance for AI agents working on **gummi**. Read this first, then
`docs/DESIGN.md` before making non-trivial changes.

## What gummi is

gummi is a **meta-harness for coding agents**: a single binary that
drives a fleet of coding agents through a fixed, spec-driven workflow,
each work item on its own git worktree and branch. It orchestrates other
agents — it is not itself a coding agent. There are two entry points onto
the same engine and quality floor: an interactive TUI (a human at the
keyboard) and a headless CLI driver (`gummi run`/`resume`, for scripts,
CI, or a calling agent) — see `internal/driver` and README's "Running
headlessly".

- **Language:** Go 1.26, single module `github.com/morphis/gummi`.
- **Binary:** `cmd/gummi` → `bin/gummi`. Run with no args inside a git repo.
- **TUI stack:** Bubbletea v2 / Lipgloss v2 / Bubbles v2 (Charm).
- **Storage:** SQLite (`modernc.org/sqlite`, pure-Go, no cgo).
- **Runtime workspace:** a `.gummi/` dir created lazily in the target
  repo (state DB, `config.yaml`, `profiles.yaml`, `worktrees/`). Gitignored.

The mental model: every unit of work is a **card** moving through a
compiled-in workflow. Each stage is performed by a **role**
(`architect`, `implementer`, `reviewer`, `scribe`); a **profile** maps
roles to concrete models. The durable context carrier between stages is
a **markdown spec on the feature's branch**, not a transcript.

```
feature  FD-NNN   todo → brainstorm → spec → plan → implement → review → verify → done
bug      BG-NNN   todo → triage → diagnose → fix ──────────────↗ (same review→verify floor)
```

Read `README.md` for the user-facing feature tour and key bindings.

## Package map (`internal/`)

Each package has a real doc comment at the top of its lead file — read it
before touching the package. Dependency flow is roughly
domain → state/spec/workflow → engine → ui, with agent/worktree/verify as
leaf services.

| package | responsibility |
|---|---|
| `domain` | Core types: features, bugs, stages, work items. No I/O. |
| `workflow` | The single fixed state machine: stages, legal transitions, skip flags, rerun caps. **Compiled in, never configurable.** |
| `spec` | The markdown spec artifact + its `gummi-checks` verification block. |
| `state` | SQLite store: features, sessions, diff annotations, dependency edges, sequences, workspace. |
| `engine` | The orchestrator. Binds stages to agent sessions, schedules autonomous runs across attention slots, routes turns, streams activity. Start here to trace behavior. |
| `agent` | Adapter layer over concrete agents. Interfaces hide the backend: `copilot` (default), `opencode`, `headless`, plus `fake.go` for tests. |
| `worktree` | Per-feature git worktrees under `.gummi/worktrees/`: create, rebase-on-main, dirty/landed detection, cleanup. |
| `verify` | Runs a spec's `gummi-checks` in the worktree, reports pass/fail. |
| `diffannot` | Anchors line comments to diff content (survives minor rebases). |
| `config` | Loads `.gummi/config.yaml` (permission mode only, since M5). |
| `notify` | Terminal bell / desktop notification on needs-attention. |
| `atomicfile` | Crash-safe file writes for pre-approval drafts (no git backstop). |
| `ui` | The Bubbletea TUI: board, chat, diff/spec views, inbox, dialogs. |
| `driver` | The headless counterpart of the TUI's autonomous loop: drives `run`/`resume` over the engine, emits NDJSON + typed exit statuses, holds the `.gummi` lock. |
| `planround` / `reviewround` | Single seam persisting the plan-critique / review→fix round counters across process boundaries, so the TUI and headless driver can't drift apart on rerun caps. |
| `sandbox` | Resolves effective confinement (`enforce`/`warn`/`off`) from config, profile, and backend capabilities; shared by engine refusal and `doctor`. |
| `verdict` | Shared stage-verdict grammar the TUI and headless driver both parse, per DESIGN §13. |
| `mcp` | Backs the hidden `gummi __mcp` shim: bridges an agent backend's MCP stdio calls to the engine's live stage tools over the session socket. |

`cmd/gummi` holds `main.go` plus the board's supporting subcommands: `ingest`
(spec decomposition), `bugs` (GitHub issue import / manual add), and the
headless driver surface — `run`, `resume`, `status`, `spec`, `diff`, `verify`,
`merge`, `clean`, `deps`, `doctor`, `skill`. See README's "Running headlessly"
for the driver's command grammar and exit-status table.

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

Running the real TUI (`bin/gummi`) needs a git repo and, for the default
backend, an authenticated GitHub Copilot CLI. To drive agents without
Copilot auth, set `GUMMI_AGENT=headless` with `GUMMI_AGENT_CMD` pointed at
a BYOK/local endpoint's adapter (`GUMMI_HEADLESS_CREDITS_PER_1K` prices its
spend into credits). With no usable agent, creation/specs/worktrees/gates
still work — the board just stays static. Key env vars are tabled in
`README.md#configuration`.

## Conventions & guardrails

- **The workflow is invariant.** No implementation without an approved
  spec; no merge without review **and** verify. Review and Verify can
  never be skipped. Do not add configuration that softens this — it's a
  core design decision, not an oversight.
- **The spec is the context carrier**, not chat transcripts. Keep token
  windows small: pass specs between stages, not conversation history.
- **gummi's job ends at a verified branch.** It does not open PRs or
  release. Don't add that scope without checking `docs/DESIGN.md §7`
  (scope guards) and §10 (Decisions — binding).
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
- A TUI bug → `internal/ui` (`board.go`, `chat.go`, `diffview.go`, `inbox.go`).
- Agent/model wiring → `internal/agent` + `internal/engine/profiles.go`.
- Anything architectural or a "why is it this way" question →
  `docs/DESIGN.md` (its **Decisions** list in §10 is binding).
