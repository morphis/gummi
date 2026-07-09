# gummi

> A meta-harness for coding agents. Drive a fleet of agents through a
> spec-driven workflow across git worktrees, from one beautiful TUI.

Working on several features at once with coding agents usually means
juggling terminals, worktrees, and your own memory of which agent is
doing what and which one is stuck waiting for you. gummi replaces that
with one board. Every unit of work is a card that moves through a fixed,
compiled-in workflow; every card gets its own git worktree and branch;
every stage is performed by an agent whose model you choose per feature.
gummi's job ends at a **verified branch** — land it with the built-in
one-key squash-merge or however you like; PRs and releasing stay in
your hands.

## The core concept

gummi solves three problems at once:

1. **Orchestration** — many features in flight, each isolated in its own
   worktree under `.gummi/worktrees/`, each at a known stage. The
   parallelism model is *attention-based, not throughput-based*: you are
   the scarce resource, so by default one autonomous agent runs at a
   time while the rest queue, and everything that needs a human — gates,
   agent questions, failures, exhausted budgets — lands in a single
   needs-attention inbox.

2. **Quality** — a spec-driven state machine with gates. No
   implementation without an approved spec, no merge without review and
   verification. The workflow is never configurable; the only degrees of
   freedom are skip flags for the early design stages and automatic
   rerun transitions (fix → re-review). Review and Verify can never be
   skipped.

3. **Cost** — per-stage model routing. Stages are performed by *roles*
   (`architect`, `implementer`, `reviewer`, `scribe`), and a *profile*
   maps roles to concrete models — frontier models for design and
   review, cheap or local models for mechanical steps. Each feature
   carries a credit envelope split into per-stage allocations with
   rollover, a protected review floor, and human gates on exhaustion.

Two kinds of work item share the machinery:

```
feature  FD-NNN   todo → brainstorm → spec → plan → implement → review → verify → done
bug      BG-NNN   todo → triage → diagnose → fix ──────────────↗ (same quality floor)
```

Brainstorm and Spec (features) and Triage and Diagnose (bugs) are
interactive — you talk to the architect in gummi's chat pane, and the
durable artifact is a markdown spec (or bug report) that lives on the
feature's branch. Plan is critiqued by a fresh-context reviewer
(security, correctness, completeness) before it reaches your approval
gate — findings land as `%%` threads in the spec, serious ones trigger
an automatic replan, capped. Then Implement/Fix runs autonomously in
the worktree, streaming activity to the card. Review is
a fresh session with no shared context (ideally a different model), and
findings bounce the work back automatically, capped before it escalates
to you. Verify runs the repo's configured checks plus the spec's own
verification plan. The spec — not the transcript — is the context
carrier between stages, which keeps token windows small.

Agents can ask you bounded questions mid-turn via a built-in `ask_user`
tool: the question renders as an inline picker in the chat pane, the
blocked turn spends no tokens while it waits, and answers anchored to a
spec line are written back into the spec as resolved `%%` markers.

## Install

gummi is a single binary. There are no releases yet — install with the
Go toolchain (Go 1.26+):

```sh
go install github.com/morphis/gummi/cmd/gummi@latest
```

or build from a clone:

```sh
git clone https://github.com/morphis/gummi
cd gummi
make build        # → bin/gummi   (or: go build ./cmd/gummi)
```

For the default agent backend you also need the GitHub Copilot CLI,
authenticated:

```sh
curl -fsSL https://gh.io/copilot-install | bash
```

## Quick start

Run `gummi` with no arguments from the root of a git repository:

```sh
cd your-repo
gummi
```

First run creates the `.gummi/` workspace lazily — state directory
(0700), starter `config.yaml` and `profiles.yaml`, worktrees directory,
and the ignore rules that keep it all out of your repo's history. Then:

1. Press `n` and type a one-line description — that's the whole creation
   form; brainstorm develops the rest. Profile and skip flags sit on a
   quiet options row.
2. Press `enter` to attach and brainstorm/spec with the architect in the
   chat pane. Open questions are tracked as a `%%` checklist in the
   spec (`s` to view it; `tab` toggles a PR-style annotate mode).
3. Press `g` to advance through gates: approving the spec creates the
   worktree and branch and commits the spec to it; approving the plan
   launches the autonomous implementer.
4. Watch the running agent (`enter`), review the diff (`d`), and let the
   review/verify loop run. `b` bounces work back with your annotations.
5. Done means a verified branch. Press `m` to squash-merge it into main:
   a scribe-role agent drafts the commit message from the branch diff,
   and you edit and confirm it in a dialog before anything is committed
   (no agent configured just means a plain template to edit). Or merge
   outside gummi — either way it detects the landing (merge or
   squash-merge) and offers cleanup (`c`).

Key surfaces on the board (press `?` anywhere for the full table):

| key | action |
|---|---|
| `j/k`, `1..9` | select / jump to a card |
| `enter` | chat (interactive stages) · run / watch (autonomous stages) |
| `s` / `d` | spec / diff view — `tab` switches read ⇄ annotate |
| `g` / `b` | advance a gate / bounce back to implement or fix |
| `v` | run the verify checks |
| `tab` / `i` | cycle / open the needs-attention inbox |
| `n` / `B` | new feature / new bug |
| `I` / `G` | ingest a spec doc / import bugs from GitHub issues |
| `a` | raw-attach the agent CLI in the worktree (escape hatch) |
| `r` / `m` | rebase onto main / squash-merge into main (drafted commit message) |
| `c` / `x` | clean up a landed branch / delete |

## Bringing in existing work

Work rarely starts from a blank line. Two ingestion paths pre-seed the
board, both gated on your review before anything is created:

- **Spec ingestion** (`I` on the board, or `gummi ingest <spec-file>`) —
  an architect-role agent decomposes a PRD or design doc into PR-sized
  feature proposals with a coverage map showing where every requirement
  went. You rename, edit, merge, or drop proposals, then approve;
  each one materializes as a pre-seeded draft in todo.
- **Bug import** (`G`, or `gummi bugs ingest`) — deterministic,
  agent-free import of GitHub issues via `gh`: one issue → one proposed
  bug, with a live `/` filter to narrow a big backlog before approving.
  External refs are remembered, so re-importing skips bugs already on
  the board. `gummi bugs new` adds a single bug by hand.

## Agent backends

The agent layer is pluggable, selected with `GUMMI_AGENT`:

- **copilot** *(default)* — the official Copilot SDK for Go, driving the
  `copilot` CLI in server mode. Full duplex: streaming, tool-call
  visibility, client tools, session resume, and per-session BYOK env —
  an OpenAI-compatible endpoint (llama.cpp, vLLM, hosted) works without
  GitHub authentication.
- **opencode** — the opencode CLI (`GUMMI_OPENCODE_BIN` overrides the
  binary). Provider/model config is owned by opencode itself.
- **headless** — a generic subprocess adapter for any agent binary
  speaking a small stdio JSON protocol (`GUMMI_AGENT_CMD` is its
  command line).

No usable agent just leaves the board static — creation, specs,
worktrees, and gates all still work.

## Configuration

Two files in `.gummi/`, both scaffolded on first run:

- **`config.yaml`** — the permission mode: `allow-all` (default — gummi
  assumes it runs in a sandbox, and warns once on a bare host) or
  `guarded` (agent tool calls need approval through the inbox). The
  Verify stage's check commands are not configured here: gummi
  auto-discovers the repo's build/test/lint commands at approval into
  each spec's Verification plan (a `gummi-checks` block), where you
  review and edit them — and the TUI still surfaces the exact commands
  before running them.
- **`profiles.yaml`** — role → model maps per profile (`premium`,
  `thrifty`, …) with a declared default. BYOK per role via a `byok`
  block; API keys are referenced by environment-variable name, never
  written as literals. `credits_per_1k_tokens` sets the token→credit
  rate so local/BYOK spend meters against the same budgets.

Environment variables:

| variable | effect |
|---|---|
| `GUMMI_AGENT` | backend: `copilot` (default) · `opencode` · `headless` |
| `GUMMI_AGENT_CMD` | headless adapter's command line |
| `GUMMI_MODEL` | fallback model when a role isn't covered by a profile |
| `GUMMI_PROVIDER_BASE_URL` / `_TYPE` / `_KEY_ENV` | ad-hoc BYOK endpoint without editing profiles |
| `GUMMI_MAX_ACTIVE` | concurrent autonomous sessions (default 1) |
| `GUMMI_ENVELOPE` | default credit envelope for new features |
| `GUMMI_STAGE_BUDGET` | flat per-stage credit cap |
| `GUMMI_THEME` | `dark` (default) · `light` · `neon` |
| `GUMMI_NOTIFY` | needs-attention hook: `bell` (default) · `desktop` · `off` |
| `GUMMI_ATTACH_CMD` | command for raw-attach (default `copilot`) |

## Try it without your repo

```sh
make demo   # creates a throwaway repo with gummi initialized
make e2e    # scripted TUI drive asserting the full lifecycle (needs tmux)
```

## Development

```sh
make build          # build bin/gummi
make test           # run all tests
make lint           # go vet + golangci-lint
make golden-update  # regenerate UI golden files
make ci             # build + test + lint
```

`docs/DESIGN.md` is the design document — its Decisions list is binding.

## License

[MIT](LICENSE)
