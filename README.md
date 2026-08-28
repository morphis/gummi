# gummi

> A meta-harness for coding agents. Drive a fleet of agents through a
> spec-driven workflow across git worktrees — from one TUI, or headlessly
> from your own agents and CI.

![the gummi board: features at different stages, a spec, a diff, and a new feature advancing](docs/assets/demo.gif)

**The bottleneck in agentic coding isn't the agents anymore. It's you.**

One coding agent is a pair programmer. Five are a management problem:
a pile of terminals, worktrees you have to keep straight in your head,
and an agent that has been silently blocked on a question for the last
twenty minutes. Meanwhile every step burns frontier-model tokens whether
it needs frontier intelligence or not, and nothing stops an agent from
jumping straight to code without a spec or shipping unreviewed work.

gummi replaces the pile of terminals with one board. Every unit of work
is a card moving through a fixed workflow; every card gets its own git
worktree and branch; every stage is performed by an agent whose model
*you* choose. gummi's job ends at a **verified branch** — land it with
the built-in one-key squash-merge or however you like; PRs and releasing
stay in your hands.

## Three ideas

**You are the scarce resource.** gummi's parallelism is attention-based,
not throughput-based. By default one autonomous agent runs at a time
while the rest queue, and everything that needs a human — gates, agent
questions, failures, exhausted budgets — lands in a single
needs-attention inbox. The point isn't to run ten agents at once; it's
to make context-switching between features cheap and to never leave an
agent waiting on you without you knowing.

**The process is fixed; only the spend is yours to choose.** The
workflow is compiled in, never configurable: no implementation without
an approved spec, no merge without review and verification. The only
degrees of freedom are skip flags for the early design stages and
automatic rerun transitions (fix → re-review). Review and Verify can
never be skipped. You can't accidentally configure the quality floor
away, because there is no configuration.

**Frontier models only where they earn it.** Stages are performed by
*roles* (`architect`, `implementer`, `reviewer`, `scribe`), and a
*profile* maps roles to concrete models — a frontier model for design
and review, a cheap or local model for mechanical steps. Each feature
carries a credit envelope every stage draws from, with a human top-up
gate when it runs dry. Same process every time; spend chosen per
feature.

## The workflow

Three kinds of work item share the machinery:

```
feature  FD-NNN   todo → brainstorm → spec → plan → implement → review → verify → done
bug      BG-NNN   todo → triage → diagnose → fix ──────────────↗ (same quality floor)
research RS-NNN   todo → investigate → shape ──────────────────↗ (done = approved doc)
```

- **Design is a conversation.** Brainstorm and Spec (Triage and Diagnose
  for bugs) are interactive — you talk to the architect in gummi's chat
  pane, and the durable artifact is a markdown spec that lives in the
  repo's `.gummi` workspace. The spec — not the transcript — is the
  context carrier between stages, which keeps token windows small.
- **The quick route trades gates, never artifacts.** For well-understood
  work, `q` on the creation form picks the quick route: brainstorm and
  plan are skipped, and the spec stage drafts the complete spec —
  implementation steps included — in one pass for you to steer and
  approve. Same spec, same review/verify tail; one conversation and one
  gate instead of three. If it outgrows quick, `P` restores the plan
  stage — loosening a skip is always allowed, tightening one never is.
- **Plans get an adversarial read.** Before a plan reaches your approval
  gate, a fresh-context reviewer critiques it for security, correctness,
  and completeness. Findings land as `%%` threads in the spec; serious
  ones trigger an automatic replan, capped.
- **Implementation runs alone.** Implement/Fix runs autonomously in the
  feature's worktree, streaming activity to the card. Agents can ask you
  bounded questions mid-turn via a built-in `ask_user` tool — the
  question renders as an inline picker, the blocked turn spends no
  tokens while it waits, and answers anchored to a spec line are written
  back into the spec.
- **Review has no shared context.** Review is a fresh session (ideally a
  different model) with nothing but the spec and the diff. Findings
  bounce the work back automatically, capped before it escalates to you.
  Verify runs the repo's checks plus the spec's own verification plan.
- **Research is a document, not a branch.** Investigate runs worktree-less
  in the main checkout, grounding a brief against the repo; Shape is the
  convergence stage where findings become a recommended direction and a
  slice breakdown. Verify is a deterministic citation + coverage check
  that spends no tokens. Crossing `done` decomposes the approved document
  into pre-seeded features with first-class dependency edges.

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

1. Press `n` and describe the feature — that's the whole creation form;
   brainstorm develops the rest. The first line becomes the card title;
   write (or paste) as much as you know past it and it seeds the spec's
   Problem section, so brainstorm starts from your words instead of a
   blank page (`alt+enter` for a newline). Profile and skip flags sit
   on a quiet options row.
2. Press `enter` to attach and brainstorm/spec with the architect in the
   chat pane. Open questions are tracked as a `%%` checklist in the
   spec (`s` to view it; `tab` toggles a PR-style annotate mode).
3. Press `g` to advance through gates: approving the spec creates the
   worktree and branch and settles the spec into `.gummi/specs/`;
   approving the plan launches the autonomous implementer.
4. Watch the running agent (`enter`), review the diff (`d`), and let the
   review/verify loop run. `b` bounces work back with your annotations.
5. Done means a verified branch. Press `m` to squash-merge it into main:
   the dialog drafts a suggested landing message from the spec and the
   branch's commits; you review, edit, and approve it — nothing is
   committed until you confirm. Or merge outside gummi — either way it
   detects the landing (merge or squash-merge) and offers cleanup (`c`).

Key surfaces on the board (press `?` anywhere for the full table):

| key | action |
|---|---|
| `j/k`, `pgup/pgdn`, `1..9` | select / jump to a card, or to the first / last card |
| `→` / `←` | focus the selected card's action list / go back to the cards — `↑↓` move, `enter` runs |
| `space` | open the command menu (everything that belongs to no card); type to filter |
| `enter` | chat (interactive stages) · run / watch (autonomous stages) |
| `p` / `t` | pause the running agent, or open the dependency picker on a card with none running / read the session transcript |
| `s` / `d` | spec / diff view — `tab` switches read ⇄ annotate |
| `g` / `b` | advance a gate / bounce back to implement or fix |
| `P` | restore the plan stage on a quick / skip-plan feature (design phase only) |
| `v` | run the verify checks |
| `u` | set the budget envelope (credits; 0 = uncapped) |
| `o` | change the card's managed repository (before worktree) |
| `S` | toggle severity sort (todo only) |
| `tab` / `i` | cycle / open the needs-attention inbox |
| `n` / `B` / `R` | new feature / new bug / new research card |
| `I` / `G` | ingest a spec doc / import bugs from GitHub issues |
| `a` | raw-attach the agent CLI in the worktree (escape hatch) |
| `r` / `m` / `z` | rebase onto main (conflicts hand off to an agent session; verify re-runs after) / squash-merge into main (drafts the message; review & approve, or edit) / squash in place (collapse the branch to one commit on its fork point) |
| duplicate | a fresh copy starts over in todo, the original stays — no key: reach it from the card's action list (`→`) or the command menu (`space`), since `y` is "yes" in the confirm it raises |
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
  agent-free import of GitHub issues via `gh`: a searchable picker opens
  with the filter focused, typing narrows the list live, and `enter`
  imports exactly the highlighted issue — one issue per pass, never a
  whole repo at once. External refs are remembered, so re-importing
  skips bugs already on the board. `gummi bugs ingest --issue N` gives
  the same single-issue import headlessly (the CLI's batch flags still
  work unchanged for scripted imports). `gummi bugs new` adds a single
  bug by hand.

## Running headlessly (driving gummi from an agent)

The board is one way in; the other is a **non-interactive driver** that runs
the same engine and the same quality floor with no human at the keyboard.
Each `gummi run` ships **one PR-sized feature to a verified branch**, streams
milestone + decision NDJSON on stdout, and exits with a **typed status** — so
a calling agent (or a script) can drive it and branch on the result. It
changes *who approves a gate*, not *whether* review and verify run.
`run`/`resume` never merge — they stop at a verified branch; landing is the
separate `gummi merge` verb below.

```sh
gummi run --envelope 500 "Add a --format=json flag to the export command"
```

An envelope is required (`--envelope N`, or `GUMMI_ENVELOPE`) and an agent
backend must be configured — both fail loud before any work begins. By
default a run takes the quick route (spec → implement → review → verify) and
auto-crosses design gates; `--full` adds brainstorm + plan, and
`--gate-approval=caller` hands the design gates back to you via `resume`.

| command | purpose |
|---|---|
| `gummi run [flags] "<description>"` | create and drive one feature to a verified branch |
| `gummi resume <id\|ref> [--answer … \| --approve \| --request-changes …]` | apply a decision and drive on |
| `gummi status <id\|ref> [--json]` | read-only: stage, blockers, spend, branch state |
| `gummi spec <id\|ref>` | read-only: the current spec/report markdown |
| `gummi diff <id\|ref>` | read-only: the worktree diff |
| `gummi merge <id\|ref> -m <message\|->` | land a verified branch as one squash commit (message required) |
| `gummi clean <id\|ref>` | remove a landed card's worktree and branch |
| `gummi pr link\|unlink\|status\|comments <id> [flags]` | link/unlink a card to a PR, or read its linked-PR status and review comments |
| `gummi squash <id\|ref> -m <message\|->` | collapse a card's branch to one commit in place (message required) |
| `gummi commit <id\|ref> -m <message\|->` | commit a card's own uncommitted worktree changes onto its branch (message required) |
| `gummi deps add\|rm <dependent> <depends-on>` / `gummi deps list <id>` | manage a card's direct dependency edges |
| `gummi doctor [--json] [--deep]` | readiness: repo, backend, auth, profile, envelope, lock, per-role reach (`--deep`) |
| `gummi skill show\|install\|list` | generate and install the calling-agent skill |

`status`/`spec`/`diff` take no lock, so you can inspect a feature while a run
is live. A run holds an exclusive `.gummi` lock, so a headless run and the TUI
never touch the same workspace at once. So do `gummi merge`, `gummi squash`,
`gummi commit`, and `gummi clean`, which mutate the workspace (and, for
`merge`, main).

### Landing and cleanup without the TUI

A run stops at a **verified branch** — it deliberately never merges, because
the squash-merge landing commit is a review decision. The headless way to make
that decision is `gummi merge`, which takes the landing message explicitly and
lands it:

```sh
gummi merge FD-042 -m "feat(export): add a --format=json flag"
gummi merge FD-042 -m - <<'EOF'     # or read the message from stdin
feat(export): add a --format=json flag

The flag writes NDJSON to stdout instead of the table layout.
EOF
```

`gummi merge` requires the card to be at a **verified branch** (the same
`verified:true` state a run stops at), takes no other input, and is stricter
than the TUI's dialog: the message must be a Conventional Commits
`type(scope): summary` with no diff dump or agent attribution, or the command
refuses with a non-zero exit before touching git. On success it emits a
`merged` NDJSON event carrying the landed commit's sha and moves the card to
`done`.

`gummi clean <id>` is the headless counterpart of the board's `c` key: it
removes a landed card's worktree and branch, keeping the card as a done entry.
It refuses anything that has not actually landed, or that carries tracked-dirty
rework. Both verbs stream their NDJSON and exit with the same typed statuses as
a run (`done` = 0, `error` = 1).

`status --json` carries two distinct terminal signals. `verified:true` means
the verify gate passed and the branch is **ready to land** — the state a
headless run stops at, and the flag a CI caller polls for. `done:true` means
the branch was actually **squash-merged** into main (the TUI's `m`, `gummi
merge`, or a manual land sets it) — so after a headless run, expect
`verified:true` with `done:false` until you merge.

Every `run`/`resume` ends on a typed exit the caller branches on:

| exit | status | caller action |
|---|---|---|
| `0` | `done` | verified branch ready — report it, stop |
| `0` | `stopped` | `--until` reached its clean stop — `resume --approve` to continue |
| `2` | `question` | a delegated question or caller gate — `resume --answer`/`--approve`/`--request-changes` |
| `3` | `blocked` | open `%%`/diff threads block a gate (resolve, or `resume --request-changes`), or an unmet dependency blocks coding-stage entry (`blocking_deps` on the event — wait for it to land, or edit the edge with `gummi deps rm`) |
| `4` | `escalation` | a rerun/critique cap or unclear verdict — report to a human; resumable |
| `5` | `exhausted` | envelope dry — raise it, then `resume` |
| `6` | `timeout` | a stage went quiet (likely hang) — report; resumable |
| `1` | `error` | setup/agent failure — nothing partial landed |

Useful `run` flags: `--ref <id>` correlates a feature with your own tracker
(and lets `status`/`resume` look it up by that id), `--acceptance <file|->`
seeds the spec's verification plan, `--until spec` stops cleanly for a human
design review before implementation burns tokens, and `--autonomous`
auto-takes the recommended answer instead of checkpointing questions.

### Landing through a PR

Some repos land a card by opening a PR on GitHub instead of running `gummi
merge`. On that route gummi still never writes to GitHub — it only names
and reads the PR you already opened. The loop is four commands:

```sh
gummi pr link FD-042 --auto                        # or a URL/number instead of --auto
gummi pr comments FD-042 --ingest                   # unresolved review threads land as diff annotations
gummi resume FD-042 --bounce --note "address review" # rewinds to fix the annotated lines
git push                                            # push the fix onto the open PR
```

What you do before that first push depends on the repo's merge setting:

| merge method | before you push |
|---|---|
| squash merge | nothing — GitHub already collapses the branch to one commit |
| merge commit / rebase merge | `gummi squash <id> -m <message\|->` first, so the branch lands as one commit either way; later fix rounds can keep their own commits or `squash` again, as long as no review thread is open |

`squash` refuses outright while the worktree carries uncommitted changes,
by design — folding them silently into the collapsed commit would hide what
changed. If a PR-linked card has stray worktree changes (say, a fix made by
hand rather than through `resume`), commit them first with `gummi commit`,
then squash:

```sh
gummi commit FD-042 -m "fix(export): tighten the empty-array case"
gummi squash FD-042 -m "feat(export): add a --format=json flag"
```

`gummi commit <id\|ref> -m <message\|->` commits exactly the target card's own
uncommitted worktree changes onto its own branch, using the caller-supplied
message — no PR, remote, or main-checkout interaction, and no stage
transition. It has no PR-linked or stage precondition, so it works
regardless of what `merge`/`squash` would otherwise require; a clean
worktree is a no-op, reported as such, not an error.

### Dependencies between cards

A card can declare a direct dependency on another with `gummi deps add
<dependent> <depends-on>` (`gummi deps rm`/`list` remove or read them back).
A dependency counts as met only once the target reaches `done` — its branch
is verified and landed — so anything short of that blocks the dependent
card's entry into its coding stage (`Implement`/`Fix`) with a `blocked`
status naming the outstanding card(s). The TUI exposes the same edges via the
`p` key's dependency picker (on a card with nothing running) and shows live
per-dependency status on the board and spec view. `gummi ingest` also seeds
edges automatically when a decomposed spec calls one proposal out as
depending on another.

### The calling-agent skill

gummi generates its own **skill** — a `SKILL.md` documenting this loop — and
installs it where Claude Code, GitHub Copilot CLI, Codex, and opencode read it:

```sh
gummi skill install          # project scope: shared .claude/skills + Codex .agents/skills
gummi doctor                 # then check the backend/auth/envelope are ready
```

A **project-scope** install writes `.claude/skills/gummi/SKILL.md` for Claude,
Copilot, and opencode, plus `.agents/skills/gummi/SKILL.md` for Codex.
`--scope user` writes to each detected agent's home instead, and `--agent`
targets one (`$HOME/.agents/skills` for Codex). The doc's command grammar and
exit table are generated from the binary's real flags, so they can't drift;
the frontmatter is version-stamped,
so `install`/`list` detect a stale or edited file and refuse to overwrite it
without `--force`. `gummi skill show` prints the rendered doc.

`gummi doctor` is the readiness check the skill's first-run setup runs (`--json`
for the machine-readable checklist). It **reports**; it never repairs auth or
writes secrets — a backend needing login is surfaced as the exact command for
a human to run. Provider config (endpoints, API keys) lives in each backend's
native store (Claude Code login, `codex login`, `opencode auth`, environment for headless),
never in `profiles.yaml`.

## Agent backends

The agent layer is pluggable. `GUMMI_AGENT` selects the default backend;
a `profiles.yaml` role can pick a specific backend via its `backend:`
field (see Configuration below) so one session can mix providers — e.g.
copilot for implement, claude for review.

- **copilot** *(default)* — the official Copilot SDK for Go, driving the
  `copilot` CLI in server mode. Full duplex: streaming, tool-call
  visibility, client tools, session resume.
- **claude** — the Claude Code CLI in streaming print mode
  (`GUMMI_CLAUDE_BIN` overrides the binary). Requires
  `permissions: allow-all` — guarded mode is rejected because the CLI's
  default permission mode silently auto-denies tools. Claude Code
  manages its own endpoint routing via its native config
  (`ANTHROPIC_BASE_URL`, Claude Code login).
- **opencode** — the opencode CLI (`GUMMI_OPENCODE_BIN` overrides the
  binary). Provider/model config is owned by opencode itself
  (`opencode auth`, `opencode.json`).
- **codex** — the Codex CLI (`GUMMI_CODEX_BIN` overrides the binary), using
  stable `codex exec --json` JSONL turns and `codex exec resume` while the
  gummi session remains live. Codex owns authentication (`codex login`) and
  provider configuration; gummi passes the profile's model through Codex's
  native `-m` flag and never copies credentials or writes Codex config. This backend requires
  `permissions: allow-all`: the stable exec stream cannot service guarded
  approval callbacks. Messages appear when Codex completes each message item,
  while command, file-change, and MCP activity remains visible as tool events.
- **headless** — a generic subprocess adapter for any agent binary
  speaking a small stdio JSON protocol (`GUMMI_AGENT_CMD` is its
  command line). The child inherits gummi's environment and reads its
  own provider config from there. Set `GUMMI_HEADLESS_CREDITS_PER_1K`
  to price a local endpoint's token spend into credits so it meters
  against the same budget envelope.
- **zz** — the zz CLI (`GUMMI_ZZ_BIN` overrides the binary), a small Rust
  coding agent that fronts any OpenAI-compatible endpoint (local
  llama.cpp, OpenRouter, a self-hosted gateway). zz's `-p ask` mode is
  process-per-turn with no stdin form, so gummi resumes a session via a
  `--session` transcript file rather than an in-process handle. zz owns
  provider selection through its own `~/.config/zz/config.toml`; a
  role's `provider:` field in `profiles.yaml` names one of that file's
  `[providers.<name>]` stanzas and gummi forwards it as `--provider`, so
  different roles under one zz binary can hit different endpoints. A
  role's `think:` field is forwarded as `--think <level>`, an opaque
  value declared by the provider stanza (an architect wants a high
  level, a scribe wants none). This backend requires `permissions:
  allow-all` (zz has no approval callback) and cannot run a read-only
  research session (zz has no flag to disable its write/edit/bash
  tools) — point those roles at `claude` or `opencode` instead. Its
  prompt travels as a positional argv string, so a single turn is
  bounded well under Linux's 128 KiB argv limit. Every invocation also
  carries `--max-turns` (default 200, override with
  `GUMMI_ZZ_MAX_TURNS`) as a runaway-loop backstop — gummi's real spend
  limiter is the credit envelope, not a turn count, so this only exists
  to catch a session that never converges; hitting it ends the turn
  with an actionable error naming the cap and the env knob that raises
  it. Set `GUMMI_ZZ_CREDITS_PER_1K` to price its token spend into
  credits, the same escape hatch headless uses.

  zz also offers `--no-skills` and `--no-agents-md`; gummi passes
  neither. gummi suppresses OPERATOR-level config that could hijack a
  stage (codex gets `--ignore-user-config`, claude gets
  `--strict-mcp-config` and a scrubbed session env), but it does not
  suppress REPO-level agent instructions — no adapter disables
  AGENTS.md, CLAUDE.md, or project skills, because those are the
  repository's own guidance for agents working in it. zz follows the
  same rule and sees the same repo context every other backend sees.

No usable agent just leaves the board static — creation, specs,
worktrees, and gates all still work.

## Configuration

Two files in `.gummi/`, both scaffolded on first run:

- **`config.yaml`** — the permission mode: `allow-all` (default — gummi
  assumes it runs in a sandbox) or `guarded` (agent tool calls need
  approval through the inbox). The per-profile `sandbox: enforce|warn|off`
  confinement and the main-checkout tripwire back the allow-all default;
  see DESIGN §4.4. The
  Verify stage's check commands are not configured here: gummi
  auto-discovers the repo's build/test/lint commands at approval into
  each spec's Verification plan (a `gummi-checks` block), where you
  review and edit them — and the TUI still surfaces the exact commands
  before running them.
- **`profiles.yaml`** — role → `{backend, model}` maps per profile
  (`premium`, `thrifty`, …) with a declared default. `backend:` is
  optional (`copilot` | `claude` | `codex` | `opencode` | `headless` | `zz`); omitted, the
  role uses whatever `GUMMI_AGENT` selects. This lets a single profile
  mix providers — e.g. `implementer: copilot`, `reviewer: claude` — and
  keeps all provider config (endpoints, keys, credit rates) out of the
  repo-committed file. A role also takes two optional zz-only fields:
  `provider:` names a `[providers.<name>]` stanza in the operator's own
  `~/.config/zz/config.toml`, so one zz binary can serve different
  endpoints per role, and `think:` forwards a thinking level (opaque to
  gummi — whatever the provider stanza declares) —
  `architect: { backend: zz, model: gpt-5, provider: hosted-gateway,
  think: high }` alongside `implementer: { backend: zz, model:
  qwen2.5-coder-32b, provider: local-llama-cpp }`.

Environment variables:

| variable | effect |
|---|---|
| `GUMMI_AGENT` | default backend: `copilot` (default) · `claude` · `codex` · `opencode` · `headless` · `zz` |
| `GUMMI_AGENT_CMD` | headless adapter's command line |
| `GUMMI_CLAUDE_BIN` | claude backend's binary (default `claude` on PATH) |
| `GUMMI_CODEX_BIN` | codex backend's binary (default `codex` on PATH) |
| `GUMMI_OPENCODE_BIN` | opencode backend's binary (default `opencode` on PATH) |
| `GUMMI_ZZ_BIN` | zz backend's binary (default `zz` on PATH) |
| `GUMMI_HEADLESS_CREDITS_PER_1K` | headless adapter's token→credit rate for a local endpoint (llama.cpp, vLLM); 0 uses the engine default |
| `GUMMI_ZZ_CREDITS_PER_1K` | zz adapter's token→credit rate; 0 uses the engine default |
| `GUMMI_ZZ_MAX_TURNS` | zz adapter's runaway-turn backstop (default 200); a session that hits the cap ends with an actionable error |
| `GUMMI_MODEL` | fallback model when a role isn't covered by a profile |
| `GUMMI_MAX_ACTIVE` | cap on concurrent autonomous sessions (default: no cap — every run you start begins immediately) |
| `GUMMI_ENVELOPE` | default credit envelope for new features; also a floor under the estimated envelope — the scribe/history blend may raise it, never undercut it |
| `GUMMI_STAGE_BUDGET` | flat per-stage credit cap |
| `GUMMI_TURN_RESERVE` | one turn's credits — the floor under envelope-derived stage budgets (default `domain.TurnReserveCredits`; override for unusual models) |
| `GUMMI_COPILOT_HINT` | `off` hides the status-bar Copilot quota pill (on by default; needs an authenticated `gh` CLI to show anything) |
| `GUMMI_THEME` | `dark` (default) · `light` · `neon` |
| `GUMMI_NOTIFY` | needs-attention hook: `bell` (default) · `desktop` · `off` |
| `GUMMI_ATTACH_CMD` | command for raw-attach (default: selected backend's CLI) |

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

`scripts/record-demo.sh` regenerates the README demo GIF (needs tmux,
[vhs](https://github.com/charmbracelet/vhs), ttyd, and ffmpeg): it seeds
a throwaway repo through the real TUI and records a scripted drive.

## License

[MIT](LICENSE)
