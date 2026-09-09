# gummi

> A meta-harness for coding agents. Drive a fleet of agents through a
> spec-driven workflow across git worktrees, from one board or headlessly
> from your own agents and CI.

![the gummi board: cards at four stages, a new feature created from one form, and the architect interviewing you about it](docs/assets/demo.gif)

**The bottleneck in agentic coding isn't the agents anymore. It's you.**

One coding agent is a pair programmer. Five are a management problem: a
pile of terminals, worktrees you keep straight in your head, and an agent
that has been silently waiting on a question for twenty minutes. Every
step burns frontier-model tokens whether it needs them or not, and nothing
stops an agent from skipping the spec or shipping unreviewed work.

gummi replaces the pile of terminals with one board. Every piece of work
is a card. Every card gets its own git worktree and branch and walks the
same fixed workflow. Every stage is done by an agent whose model you
choose. gummi's job ends at a **verified branch**. Landing it on main is
your keypress, always.

## Three ideas

**Your attention is the scarce resource.** The point is not to run ten
agents at once. It is to make switching between features cheap, and to
never leave an agent waiting on you without you knowing. Everything that
needs a human, a gate, a question, a failure, an empty budget, lands in
one inbox.

**The process is fixed. Only the spend is yours.** The workflow is
compiled in. No implementation without an approved plan, no merge without
review and verification. You cannot configure the quality floor away,
because there is no configuration for it.

**Frontier models only where they earn it.** Stages are done by roles:
architect, implementer, reviewer, scribe. A profile maps each role to a
model, a strong one for design and review, a cheap or local one for
mechanical steps. Each card carries a credit envelope, and you top it up
when it runs dry.

## The workflow

```
todo → plan → implement → verify → done

feature   FD-NNN   design the change, build it, prove it
bug       BG-NNN   reproduce and diagnose, fix, prove
research  RS-NNN   shape the question, gather evidence, check the citations
```

All three kinds walk the same stages. The kind changes what each stage
writes and what its gate demands, not the route.

- **Plan is a conversation.** The architect works the problem through
  with you in the card's thread and writes the plan as a markdown spec.
  The spec, not the transcript, carries context between stages.
- **Every stage ends with an adversarial read.** A fresh-context reviewer
  critiques the plan, or the diff against the plan, before the stage
  reaches its gate. Findings are `%%` threads anchored to the lines they
  indict. Serious ones re-run the stage; the rest wait for you.
- **A stage that wrote nothing does not cross its gate.** A section still
  holding its template prompt keeps the gate shut.
- **Implement runs alone** in the card's worktree. The agent can ask you a
  bounded question mid-turn through the built-in `ask_user` tool, and the
  turn spends nothing while it waits.
- **Verify proves it.** The repo's checks and the spec's own verification
  plan run. A failure sends the work back to implement — unless the check
  was already red on the fresh branch, which is excused and gates nothing.
  `gummi status` names the excused ones.
- **Research writes a document, not a branch.** Its verify is a citation
  check that spends no tokens. Crossing `done` turns the approved document
  into pre-seeded feature cards with dependency edges.

## Install

gummi is a single binary. There are no releases yet, so install with the
Go toolchain (Go 1.26+):

```sh
go install github.com/morphis/gummi/cmd/gummi@latest
```

or build from a clone:

```sh
git clone https://github.com/morphis/gummi
cd gummi
make build        # → bin/gummi
```

The default agent backend is the GitHub Copilot CLI, authenticated:

```sh
curl -fsSL https://gh.io/copilot-install | bash
```

## Quick start

```sh
cd your-repo
gummi
```

The first run creates `.gummi/`: state, a starter `config.yaml` and
`profiles.yaml`, a worktrees directory, and the ignore rules that keep it
out of your repo's history. `gummi init` does the same without opening
the board. Then:

1. Press `n` and describe the feature. The first line is the title.
   Anything after it seeds the spec, so the architect starts from your
   words (`alt+enter` for a newline). The envelope defaults to 2000
   credits.
2. Press `enter` to open the card and design it with the architect in
   its thread. `s` shows the spec; `c` comments on a line, `x` resolves a
   thread.
3. Press `g` to approve the plan. The implementer starts in the card's
   worktree.
4. Watch it work. `d` shows the diff. `b` bounces the work back with
   your notes.
5. Done means a verified branch. Press `m` to squash-merge it into main.
   gummi drafts the landing message from the spec; you edit and approve
   it. Or merge outside gummi: it notices either way and offers cleanup
   with `c`.

The keys you need first:

| key | does |
|---|---|
| `n` | new card — feature, bug or research; paste a GitHub issue link and `alt+g` imports it (`B` / `R` / `G` preset bug / research / browse issues) |
| `enter` | open the selected card; in the card, send what you typed |
| `↑` | the card's actions, when nothing is typed |
| `s` / `d` | spec / diff, with comments in place |
| `g` / `b` | cross the gate / bounce back one stage |
| `A` | run this card on autopilot |
| `m` / `c` | squash-merge into main / clean up a landed branch |
| `i` | the needs-attention inbox |
| `tab`, `alt+1/2/3` | the board, inbox and agent tabs |
| `?` or `alt+/` | the full key table |

### The card page

A card is a thread. When it stops for you, a short paragraph says why,
what it did unattended, and, at an approval, what the branch actually
does against what the plan asked for. Every sentence there cites
something real: a check, a hunk, a spec section, a moment in the log.
The mark beside it (`[alt+a]`) is the key that opens it. A claim citing
nothing is dropped, not shown.

**Typing at a stop is always safe.** gummi reads your line for what it
asks. A missed requirement goes into the spec and the card walks back to
plan. A missing check goes into the verification plan and the checks run
again. Work that is not this card's opens a new card. "go on" approves
whatever the stop is offering, and a question gets answered. Anything
that moves the card or spends credits shows you first: `enter` confirms
a move, `y` a spend, `esc` sends the line as a plain message instead.
Nothing you type can produce a move the workflow does not already have.

Reading the line takes a few seconds, and the thread says so while it
does — your options stay where they are, and `esc` stops the read with
your line still in the composer. Backing out never costs you the
sentence: `esc` at the reading keeps it, `esc` at the chip sends it as a
message, and `esc` at the new card it may open puts it back where you
typed it.

The **agent tab** hosts your own coding CLI, picked once on the first
visit. `ctrl+g` locks the keyboard to it when you want its own `tab`
completion, and `ctrl+g` unlocks.

## Attended or autopilot

Every card runs in one of two modes. `A` sets it and starts the card
from wherever it sits.

| mode | what happens |
|---|---|
| attended | every gate and every question waits for you |
| autopilot | the card crosses its own gates, answers its own questions, reworks a failed verify, and stops at a verified branch |

On autopilot the agent is told its recommendation will be taken unread,
so it has to be defensible. All retries share one corrective budget.
What autopilot never does is widen its own reach. A tool asking to act
outside the sandbox parks the card. A research card parks before its
document becomes new cards. **It never lands on main.** When it cannot
finish, it parks to the inbox and notifies you.

An attended card always gets a lane at once. Autopilot cards share two
lanes by default and queue behind each other.

Autopilot runs inside the board process, so quitting stops it. The quit
dialog names the running cards, and reopening asks once whether to pick
them up.

## Bringing in existing work

- **Spec ingestion** (`I`, or `gummi ingest <file>`): an architect agent
  decomposes a PRD or design doc into PR-sized proposals with a coverage
  map. You edit, merge or drop them, then approve. Each becomes a card in
  todo.
- **Bug import** (`G`, or `gummi bugs ingest --issue N`): agent-free
  import of one GitHub issue at a time through `gh`. Re-importing skips
  bugs already on the board. `gummi bugs new` adds one by hand.

## Headless

The same engine runs with nobody at the keyboard. `gummi run` drives one
feature to a verified branch, streams NDJSON milestones and decisions on
stdout, and exits with a typed status your script or agent branches on.
It changes who approves a gate, never whether review and verify run.

```sh
gummi run --envelope 500 "Add a --format=json flag to the export command"
gummi run --envelope 500 --gate-approval autopilot "..."    # cross its own gates
gummi research --envelope 300 "Where does the exporter buffer, and why?"
```

An envelope is required headlessly. `--until plan` stops before
implementation for a human design review. `--autonomous` takes the
agent's recommended answer instead of stopping on a question.

| verb | |
|---|---|
| `run`, `research` | create and drive a feature or research card |
| `resume <id> --approve` / `--request-changes …` / `--answer …` / `--bounce` | apply a decision and drive on |
| `resume <id> --say "<line>"` | report how the card page would read a line, without acting |
| `status`, `watch`, `spec`, `diff` | read-only; they take no lock |
| `verify <id>` | re-run the checks on a verified branch |
| `merge <id> -m <msg\|->` | land the branch as one squash commit |
| `squash`, `commit`, `clean` | collapse the branch, commit stray changes, remove a landed worktree |
| `pr link\|unlink\|status\|comments` | land through a PR you opened; gummi never writes to GitHub |
| `deps add\|rm\|list` | dependency edges between cards |
| `ingest`, `bugs ingest\|new` | bring in existing work |
| `init`, `doctor`, `skill` | set up, check readiness, install the calling-agent skill |

| exit | status | meaning |
|---|---|---|
| `0` | `done` / `stopped` / `said` | verified branch, `--until` stop, or `--say` reading |
| `2` | `question` | a question or gate waits: `resume` with the matching flag |
| `3` | `blocked` | open threads or an unmet dependency block a gate |
| `4` | `escalation` | a retry cap or unclear verdict; a human should look |
| `5` | `exhausted` | envelope dry: `resume --envelope N` |
| `6` | `timeout` | a stage went quiet; resumable |
| `1` | `error` | setup or agent failure; nothing partial landed |

`status --json` says `verified:true` when the branch is ready to land and
`done:true` once it is merged. A headless run stops at the first.

**Let your agent drive gummi.** `gummi skill install` writes a `SKILL.md`
for Claude Code, Copilot CLI, Codex and opencode, generated from the
binary's real flags so it cannot drift. `gummi doctor` checks backend,
auth, profile and envelope. The full reference, PR landing loop included,
is in [docs/HEADLESS.md](docs/HEADLESS.md).

## Backends and configuration

Stages run on one of six backends. `GUMMI_AGENT` picks the default, and a
role's `backend:` in `profiles.yaml` overrides it, so one profile can mix
them.

- **copilot** (default): the Copilot CLI through its Go SDK.
- **claude**: the Claude Code CLI. Needs `permissions: allow-all`.
- **codex**: the Codex CLI. Needs `permissions: allow-all`.
- **opencode**: the opencode CLI.
- **headless**: any binary speaking a small stdio JSON protocol.
- **zz**: a small Rust agent for any OpenAI-compatible endpoint, local
  llama.cpp included. Cannot run read-only research roles.

Each backend owns its own login and provider config. gummi never copies
credentials or writes them to a file it manages.

Two files in `.gummi/`, both scaffolded on first run:

- **`config.yaml`**: `permissions` (`allow-all` or `guarded`), `sandbox`,
  `autopilot_lanes`, `repo` and `repos` when `.gummi` sits above the
  repository, `checks.default` to fix the verify commands instead of
  discovering them, `env` prerequisites the verification plan can cite,
  `instructions` files, and `agent` for the agent tab.
- **`profiles.yaml`**: named profiles mapping each role to
  `{backend, model}`, and which one is the default.

The environment variables you meet first:

| variable | effect |
|---|---|
| `GUMMI_AGENT` | default backend |
| `GUMMI_ENVELOPE` | default credit envelope for new cards |
| `GUMMI_MAX_ACTIVE` | attended lanes (default 1) |
| `GUMMI_THEME` | `dark`, `light`, `neon` |
| `GUMMI_NOTIFY` | `bell`, `desktop`, `off` |

Every backend's specifics, every config key and the full environment
table are in [docs/CONFIGURATION.md](docs/CONFIGURATION.md).

## Try it without your repo

```sh
make demo   # a throwaway repo with gummi initialized
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

`docs/DESIGN.md` is the design document; its Decisions list is binding.
`scripts/record-demo.sh` regenerates the demo GIF (needs tmux,
[vhs](https://github.com/charmbracelet/vhs), ttyd and ffmpeg).

## License

[MIT](LICENSE)
