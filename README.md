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
is a card. Every card gets its own git worktree and branch (`feat/dark-mode`,
`bug/flaky-login`) and walks the
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

feature    FD-NNN   design the change, build it, prove it
bug        BG-NNN   reproduce and diagnose, fix, prove
research   RS-NNN   shape the question, gather evidence, check the citations
diagnosis  RS-NNN   scope the fault, find every cause, propose the fixes
goal       GL-NNN   agree what done means, run the cards that get there, prove the whole
```

All of them walk the same stages. The kind changes what each stage
writes and what its gate demands, not the route.

**Diagnosis is research in a second mode**, not a fifth kind: the same RS
card, branchless and read-only, with a document that starts from a
symptom instead of a question. Use it when something already behaves
wrong and you do not yet know what — or how many things — it is. A bug
card presumes one defect and commits to fixing it in the same card; a
diagnosis may find two, records what it **ruled out**, writes no code,
and decomposes into fix cards rather than features.

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
  turn spends nothing while it waits. Every question it asks also offers
  to talk it over — pick one of its options, or press `o` and answer in
  your own words.
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
5. Done means a verified branch, and the card asks how it leaves gummi.
   Press `g` (or `m`) to squash-merge it into main — gummi drafted the
   landing message when verify passed, so the dialog opens on it; you
   edit and approve it (`ctrl+r` composes another). `ctrl+t` in that
   dialog switches the landing to a merge commit that keeps the branch's
   own commits; squash stays the default. Press `h` to
   hand it off instead: the card closes and the branch stays yours, to
   push, PR by hand or cherry-pick. Or merge outside gummi: it notices
   either way and offers cleanup with `c`.
6. A finished card keeps its receipt. Open it and the page says how it
   ended, which commit it became, where its branch is now and what it
   cost — and offers what is left: clean up, land it after all, or open
   a bug from it, which carries the spec, the branch and the thread into
   a fresh card.
7. Cards that settled more than a day ago fold into the board's archive,
   whose header carries how many of them still hold a worktree. `f` opens
   it. A card its goal dropped keeps a group of its own for a day, with
   the reason it was dropped and the one answer that matters: `adopt`
   takes it back with its work kept.
8. At the end of a session, `C` closes out: it walks the cards whose
   branches are ready, one confirm each, then sweeps the worktrees that
   landing left behind — naming what it is holding back and why. `W` says
   what the last seven days produced, grouped by how each card ended.

The keys you need first:

| key | does |
|---|---|
| `n` | new card — feature, bug, research, diagnosis or goal; paste a GitHub issue link and `alt+g` imports it (`B` / `R` / `G` preset bug / research / browse issues) |
| `f` | fold or unfold a goal's cards on the board |
| `enter` | open the selected card; in the card, send what you typed |
| `↑` | the card's actions, when nothing is typed |
| `s` / `d` | spec / diff, with comments in place |
| `alt+r` | the card's stats — where its credits and hours went, and how much was work done twice |
| `g` / `b` | cross the gate / bounce back one stage |
| `A` | run this card on autopilot |
| `m` / `h` / `c` | squash-merge into main (`ctrl+t` in the dialog: merge commit instead) / hand the branch off and close the card / clean up a landed branch |
| `T` | new card stacked on this one — its branch forks from this card's, and gummi replays it whenever this card changes |
| `C` / `W` | close out the session — land what is ready, then sweep the worktrees / what the last seven days produced |
| `f` | fold a goal's cards, or the board's archive of everything settled earlier |
| `P` | a goal's page — done-when, its cards (`enter` watches one run), budget, the lead's log |
| `i` | the needs-attention inbox |
| `tab`, `alt+1/2/3` | the board, stats and inbox tabs |
| `?` or `alt+/` | the full key table |

### The stats tab

`alt+2` leaves the board for its ledger: where every card's credits and
hours went — over a window (`h`/`l` zooms 6h/24h/7d/30d/all) and over
all time — with a timeline of the lanes themselves. One row per card
that moved in the window: a filled block for a stage session (bright
and growing to the right edge while it runs), `▒` for time it sat
waiting on you, `◆` a gate, `✔` a landing. `j`/`k` walk the lanes,
`enter` opens the card, `f` pins the right edge to now. Below the
timeline: the window's money beside the all-time ledger, the clock
(agent working, waiting on you, nothing running — and how many lanes
ran at once at the busiest stretch), and the window's costliest cards
with the one-line reason each earned its place. The same derivation the
card's own run tab reads, one scale up: the fold (internal/fleetrun)
charges a pass to the window it started in, so no window can hold the
same credits twice.

### The card page

A card is a thread. When it stops for you, a short paragraph says why,
what it did unattended, and, at an approval, what the branch actually
does against what the plan asked for. Every sentence there cites
something real: a check, a hunk, a spec section, a moment in the log.
The mark beside it (`[alt+a]`) is the key that opens it. A claim citing
nothing is dropped, not shown.

Above the thread, the page's tabs are one chord each: `alt+s` the spec,
`alt+d` the diff, `alt+l` the log, `alt+r` the stats, `alt+t` back to the
thread. The **log** is the card's own commits, oldest first — `enter`
shows what one changed, `e` rewords it, `s` squashes it into the one
before, and `a` applies the draft after asking. A rewrite never reorders
or drops a commit, so the branch's content, and the verify that ran on it,
are unchanged. It is refused while an agent is working on the card, and
on a branch gummi did not cut. Commits already pushed are marked; gummi
prints the force push a rewrite of them needs and never runs it.

**Typing at a stop is always safe.** gummi reads your line for what it
asks. A missed requirement goes into the spec and the card walks back to
plan. A missing check goes into the verification plan and the checks run
again. Work that is not this card's opens a new card. "go on" approves
whatever the stop is offering, and a question gets answered. Anything
that moves the card or spends credits shows you first: `enter` confirms
a move, `y` a spend, `esc` sends the line as a plain message instead.
Nothing you type can produce a move the workflow does not already have.

Reading the line takes a few seconds, and the thread says so while it
does. The options go while it runs — you answered them by sending the
line — and `esc` stops the read, which brings them back with your line
still in the composer. Backing out never costs you the
sentence: `esc` at the reading keeps it, `esc` at the chip sends it as a
message, and `esc` at the new card it may open puts it back where you
typed it.

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

Every autonomous run starts the moment you ask for it. Nothing caps how many
run at once, or queues one behind another: how many you run in parallel is
your call.

Autopilot runs inside the board process, so quitting stops it. The quit
dialog names the running cards, and reopening asks once whether to pick
them up. A stage running on an attended card is stopped the same way and
offered back too — restarting `gummi web` is a quit — and a card left
paused says the quit cut its run.

## Goals

A goal is a card whose work is other cards. You give it an outcome and a
budget; it comes back with one working branch.

```
todo → plan ─────────────▶ implement ──────────────▶ verify ─────────▶ done
       agree the goal       its cards run on the      the combined     you land it,
       with the architect   goal branch (silent)      branch (silent)  send it back,
       ▲ you                                                            or hand it off ▲ you
```

- **Plan is the one conversation.** The architect agrees the objective with
  you, a **done-when** list of checkable statements, the limits, a rough
  cost per item against the budget, how many cards may run at once, and
  the cards — each serving a done-when item. Name an existing card's id to
  hand it to the goal.
- **Then it runs itself.** Approving the plan mints the cards and runs each
  on autopilot on the goal branch. A verified card lands there as one
  commit. The goal branch keeps up with main between landings. The goal's
  **lead** — an agent on the `lead` role, or the architect's model —
  answers the cards' questions, reads their plans before they implement,
  re-plans stuck and exhausted cards, and records a **decision for review**
  for every call a user of the result would notice. None of it reaches
  your inbox.
- **The budget is a hard ceiling, and only you raise it.** Each card starts
  on what its plan estimated, capped at an even share, and the rest stays
  unspoken for: the lead raises a card from what is left when it proves it
  needs more, never past the ceiling. When there is nothing left to give,
  the goal does not choose work to abandon — it stops and tells you which
  card is waiting and roughly what it needs. Nothing is dropped, the card
  keeps its branch and its spend, and **top up and continue** carries it on
  from where it stopped.
- **A goal is not in a repository.** You are never asked which one it is
  in: the cards its plan agrees name their own `repo:`, and the goal keeps
  a branch of its name in every repository they are in. It lands once in
  each — git has no merge across repositories — and the hand-over says
  which of them have it.
- **You can watch every card it runs.** `P` opens the goal's page from the
  goal or any of its cards; its card list has a cursor and `enter` opens
  the selected card's thread, live, with `esc` coming back to the page.
  (They are on the board too, folded under the goal behind `f`.) What you
  get is a watch, not a chat: while the goal's lead is driving a card it
  starts, answers and lands it, so the board withholds the verbs and the
  composer that would put a second driver on it — the same rule as a card
  another gummi process is running.
- **You can still reach in — through the goal.** Type into a running goal
  and the lead reads it as a note. Stop it, and verified work lands and
  the rest is dropped.
- **Two stops reach you, and only two**: it is ready, or it needs more
  budget. Everything else its lead settles. The goal page shows
  each done-when item met or not met with its evidence, a try-it guide, the
  decisions for review, the declined reviewer findings, what was found
  along the way, the diff by card, and the spend. Land it (`g`: one merge
  commit over its cards' commits, per repository), send it back to its cards with your
  notes (`b`), reverse a decision, or hand it off (`h`).

## Bringing in existing work

- **Spec ingestion** (`I`, or `gummi ingest <file>`): an architect agent
  decomposes a PRD or design doc into PR-sized proposals with a coverage
  map. You edit, merge or drop them, then approve. Each becomes a card in
  todo.
- **Bug import** (`G`, or `gummi bugs ingest --issue N`): agent-free
  import of one GitHub issue at a time through `gh`. Re-importing skips
  bugs already on the board. `gummi bugs new` adds one by hand.

## Schedules — sessions that come back on a clock

Freeform sessions only move when a person types. Two primitives, sharing
one cron engine, let work come back on its own; press `L` on the board to
see them.

- A **schedule** mints a **new** freeform card on a cron cadence and
  starts it with a stored prompt, its own agent/model and an envelope —
  the nightly "triage new issues". Every minted card gets its own branch
  and worktree, like any freeform card.
- A **heartbeat** sends a recurring turn into **one** freeform session —
  "check CI, keep going" every hour — so the same conversation
  reassesses and continues.

How they behave:

- **Cron is canonical.** Presets (`5m`, `15m`, `1h`, `6h`, `@daily`,
  `@weekly`) compile to a 5-field cron expression, and that is what is
  stored — what you see and what fires agree. Timezones are per schedule
  (`--tz`, IANA name). A cadence no month can match is refused at the
  store, not discovered the first night it should have fired. The clock
  skips a wall time that does not exist (spring forward); in an hour
  that happens twice (fall back) a sub-hourly cadence keeps firing —
  the hour's matched minutes fire at both passes — while a schedule set
  for the hour itself fires it once, at its first pass.
- **Off by default, and edits turn it off.** A definition is stored
  disabled; enabling is explicit and asks, because it is the switch that
  starts spending. Any change to a definition turns it off again until
  you re-enable it — a cadence you have not re-approved is not a cadence
  that fires.
- **The board is the clock.** Nothing fires while no board is running
  (the TUI, or `gummi web`); missed fires coalesce into one catch-up
  fire when a board comes up. A fire against a session that is working
  right now is skipped, not queued.
- **The envelope is the brake.** A minted card always carries its
  schedule's envelope; a heartbeat spends its target's. An exhausted
  target pauses the schedule and tells you; nothing is ever raised
  automatically.
- **A failed kickoff never piles up cards.** If a minted card's session
  fails to start, the schedule retries that same card next time instead
  of minting another — until you close or delete the card.

`gummi schedule add --name nightly --every 1h --prompt "triage new
issues" --envelope 50` defines a mint; `--heartbeat FF-001` (with no
envelope — the target's own is the brake) defines a heartbeat. `enable`,
`disable`, `run-now` (the running board fires it off-cadence), `rm` and
`list --json` round it out. The board's Schedules view (`L`) defines and
edits through a dialog of its own — the agent and model picked from the
session picker's catalog, the cadence previewed as you type (the cron
the store would keep, and when it fires next), the timezone picked from
a shortlist — and the web page's Schedules view has the same form.

## Headless

The same engine runs with nobody at the keyboard. `gummi run` drives one
feature to a verified branch, streams NDJSON milestones and decisions on
stdout, and exits with a typed status your script or agent branches on.
It changes who approves a gate, never whether review and verify run.

```sh
gummi run --envelope 500 "Add a --format=json flag to the export command"
gummi run --envelope 500 --gate-approval autopilot "..."    # cross its own gates
gummi research --envelope 300 "Where does the exporter buffer, and why?"
gummi diagnose --envelope 300 "Exports truncate at 64KB, but only over HTTP/2"
```

An envelope is required headlessly. `--until plan` stops before
implementation for a human design review. `--autonomous` takes the
agent's recommended answer instead of stopping on a question.

An envelope bounds what a card may **start**, not what it may finish: the
check fires between sessions, so the session already running when the cap
is reached runs to its end. Expect a card to stop a little over its
envelope — one session's worth, which on a small envelope can be most of
it. `gummi status` says by how much when it happens.

| verb | |
|---|---|
| `run`, `research`, `diagnose` | create and drive a feature, research or diagnosis card |
| `goal` | agree a goal, then drive it until it is ready for you |
| `resume <id> --approve` / `--request-changes …` / `--answer …` / `--bounce` | apply a decision and drive on |
| `resume <id> --say "<line>"` | report how the card page would read a line, without acting |
| `status`, `watch`, `spec`, `diff` | read-only; they take no lock |
| `status <id> --stats` | where the card's credits and hours went, per pass — the rework split included |
| `verify <id>` | re-run the checks on a verified branch |
| `merge <id> -m <msg\|->` | land the branch as one squash commit (`--no-squash`: a merge commit keeping its commits) |
| `handoff <id>` | close a verified card and keep its branch — nothing lands |
| `squash`, `commit`, `clean` | collapse the branch, commit stray changes, remove a landed worktree |
| `log <id>` / `rewrite <id> --plan <file\|->` | list a card's own commits / reword or squash them in place — never reorder or drop, so the content stays verified |
| `stack new\|add\|rm\|mv\|list` | build and read a stack of cards whose branches fork from one another |
| `stack restack <stack>` | replay every card in a stack onto its current base now (the board does this on its own) |
| `schedule list\|add\|enable\|disable\|run-now\|rm` | schedules and heartbeats — freeform sessions that come back on a cron cadence; store verbs, with or without a board |
| `pr link\|unlink\|status\|comments` | land through a PR you opened; gummi never writes to GitHub |
| `deps add\|rm\|list` | dependency edges between cards |
| `ingest`, `bugs ingest\|new` | bring in existing work |
| `run\|bugs new --adopt <branch>` / `--pr <url\|number>` | mint the card onto a branch gummi did not cut, and rework it |
| `init`, `doctor`, `skill` | set up, check readiness, install the calling-agent skill |

| exit | status | meaning |
|---|---|---|
| `0` | `verified` / `stopped` / `said` | verified branch, `--until` stop, or `--say` reading |
| `2` | `question` | a question or gate waits: `resume` with the matching flag |
| `3` | `blocked` | open threads or an unmet dependency block a gate |
| `4` | `escalation` | a retry cap or unclear verdict; a human should look |
| `5` | `exhausted` | envelope dry: `resume --envelope N` |
| `6` | `timeout` | a stage went quiet; resumable |
| `1` | `error` | setup or agent failure; nothing partial landed |

`status --json` says `verified:true` when the branch is ready to land, and
names how the card closed in one `ending` field — `landed`, `handed_off`
or `dropped`. A headless run stops at the first.

**Let your agent drive gummi.** `gummi skill install` writes a skill bundle
— `SKILL.md` plus the `references/` it points at — for Claude Code, Copilot
CLI, Codex and opencode, generated from the same cobra tree that parses a
real command line, so it cannot document a flag the binary lacks. `gummi
doctor` checks backend, auth, profile and envelope. The full reference, PR
landing loop included, is in [docs/HEADLESS.md](docs/HEADLESS.md).

## The board in a browser

`gummi web` serves the same board to a browser: the rail of cards, the
open card's conversation, and its spec, diff, log, pull request and stats
beside it. It is a board host like the TUI — it builds the board the same
way and runs the TUI's own model without a screen — so one board has one
host at a time, and whichever starts second names the first and exits.

```sh
gummi web                  # 127.0.0.1:7878 (or GUMMI_WEB_ADDR); prints a pairing code
gummi web pair --name Ana  # a code for another browser, from another terminal
gummi web devices          # who is paired or waiting;  gummi web unpair <id> | --all
gummi web --tailscale --ts-tls   # also https://gummi.<tailnet>.ts.net, for a phone
```

A browser pairs once with the six-digit code and a name; devices paired
under one name are one person, and receipts carry it. It listens on
loopback unless told otherwise: `--tls-cert`/`--tls-key` serve HTTPS, and
`--tailscale` also puts the board on your tailnet as its own node
(embedded; no `tailscaled`, no port forwarding). The first run prints a
login URL to open on any device (or set `TS_AUTHKEY`, which unlike
`--ts-authkey` stays out of `ps`);
`--ts-tls` serves HTTPS with a tailnet certificate, which notifications
need. Pairing still applies on the tailnet, and `--no-pairing` is refused
on anything but loopback. See
[docs/CONFIGURATION.md](docs/CONFIGURATION.md#the-web-host-on-a-tailnet).

**Sessions.** The rail's **New session** opens an empty conversation
rather than a form. You pick the repository, base and budget in the
composer, and the model beside Send. The first message you send starts it.
A session is a freeform card: one agent in its own worktree, with no
stages and no gates, and it lands on your read of its diff. Flip the
draft's **runs in** toggle to work in the main checkout instead — no
branch, no worktree, its changes left uncommitted for you to commit; such
a session never lands through gummi, it just hands off when you are done
([DESIGN §19.3b](docs/DESIGN.md#193b-where-a-session-works-the-main-checkout)).
It runs on any model an installed agent offers (claude, codex, copilot,
opencode, pi, or a headless command). The picker offers the models the
agent itself provides — asked live, where it can say so — plus the ids
your profiles already use, and takes any id you type. Switching models
mid-session keeps
the conversation. When the work turns out to need a design, **Write a
spec** in the session's head continues it as a feature: its branch is cut
from the session's, the profile's architect plans it from the
conversation, and from there it walks the whole workflow. Profiles only
ever choose the models for a spec's stages
([DESIGN §19.8](docs/DESIGN.md#198-sessions-the-model-is-the-sessions-own)).

The spec starts from a **handoff brief**, not a list of your lines. When
the dialog opens, gummi asks the session itself to write what the next
card's architect will read — what was asked, what was decided (including
every question it asked you and the answer you gave), what was done on
the branch, and what remains — and you edit that draft before anything
mints. On the web the dialog opens at once and the brief lands in it a
moment later; in the terminal the same dialog opens under `w` on a
freeform card, from its menu, or by typing `/writespec`. While the brief
is being written the thread says so ("drafting the handoff brief…"),
because the turn is gummi's, and it stays in the conversation afterwards
as the record of the hand. When no live session can answer — after a
restart, say — the dialog labels the draft it assembles from the
conversation instead, so a degraded brief is never mistaken for the
session's own words. A plain hand off (the `h` ending) still closes the
card with no brief turn; only write a spec asks the session to write.

Once one browser is paired, a new one paired with a `gummi web pair` code
(or one a browser asked for) only waits: every page at the board shows the
request — name, browser, address, how it paired — with **Approve** and
**Reject**, and it lapses after ten minutes. The first browser, and one
paired with the code `gummi web` prints when it starts, are let in at
once. There is no command that approves, on purpose: an agent running as
you could run it too. This stops an agent on the same machine pairing
itself silently; it does not stop one that can drive your own browser
profile or read its cookies, pair before you do, or edit gummi's files
and restart the server — for those, run agents in a container
([DESIGN §20.5](docs/DESIGN.md#205-scope-guards)).

The page can attach images (PNG, JPEG, GIF, WebP, ≤5 MB, up to 8 at a
time) in three places: the new-card form, a spec comment, and the card's
composer. A description or a spec note stores the image and links it
into the document, so every stage that reads the spec sees it — even a
backend with no vision support gets the file's path. The composer's
paperclip only shows when the card's current agent can actually take an
image with a turn (claude, copilot, codex, opencode, pi; not the
headless backend), since that one sends the bytes with the turn itself
rather than just naming a file.

## Backends and configuration

Stages run on one of seven backends. `GUMMI_AGENT` picks the default, and a
role's `backend:` in `profiles.yaml` overrides it, so one profile can mix
them.

- **copilot** (default): the Copilot CLI through its Go SDK.
- **claude**: the Claude Code CLI. Needs `permissions: allow-all`.
- **codex**: the Codex CLI. Needs `permissions: allow-all`.
- **opencode**: one `opencode serve` process per session, every action
  over HTTP — turns, aborts, the model catalog, compaction — with guarded
  mode surfacing each held tool call for approval. The CLI is still what
  `gummi doctor` probes and what auth runs through.
- **pi**: the pi coding agent, in its RPC mode.
- **antigravity**: Google's Antigravity CLI (`agy`), in its stream-json
  mode. Needs `permissions: allow-all`; every card runs agy under a
  redirected per-card home, so its config — login copy, MCP wiring,
  conversations — never touches your own. Turns cannot be interrupted
  mid-flight; a stop waits for the turn to end.
- **headless**: any binary speaking a small stdio JSON protocol.

Each backend owns its own login and provider config. gummi never copies
credentials or writes them to a file it manages.

Two files in `.gummi/`, both scaffolded on first run:

- **`config.yaml`**: `permissions` (`allow-all` or `guarded`), `sandbox`,
  `repo` and `repos` when `.gummi` sits above the
  repository, `checks.default` to fix the verify commands instead of
  discovering them, `env` prerequisites the verification plan can cite,
  `instructions` files, and `hooks` scripts run on board events. In
  `guarded` mode a tool call a backend holds — opencode's server does —
  becomes the card's open decision with approve/deny options, answered
  from the thread like any question; loops with no decision surface
  (title scribes, estimates, ingest) run allow-all regardless.
- **`profiles.yaml`**: named profiles mapping each role to
  `{backend, model}`, and which one is the default. A running board
  picks up an edit for its next session (a session already running keeps
  its model); an edit that does not parse, or that names a backend the
  board did not start, is refused and the board says so. The claude
  backend spells model versions with dashes (`claude-haiku-4-5`), not
  the dots other CLIs use; `gummi doctor` flags the dotted form.

The environment variables you meet first:

| variable | effect |
|---|---|
| `GUMMI_AGENT` | default backend |
| `GUMMI_ENVELOPE` | default credit envelope for new cards |
| `GUMMI_THEME` | `dark`, `light`, `neon` |
| `GUMMI_NOTIFY` | `bell`, `desktop`, `off` |
| `GUMMI_ANTIGRAVITY_BIN` | the antigravity backend's binary, when `agy` is not on PATH |
| `GUMMI_ANTIGRAVITY_CREDITS_PER_1K` | token→credit rate for antigravity sessions; 0 uses the engine default |

Every backend's specifics, every config key and the full environment
table are in [docs/CONFIGURATION.md](docs/CONFIGURATION.md).

## Hooks — scripts on board events

`GUMMI_NOTIFY` rings the bell; `hooks:` in `config.yaml` runs your
script instead of (or beside) it. Each entry is a shell command line run
when the board changes — a JSON payload on stdin, the event in
`GUMMI_EVENT` (and as the shell's `"$1"`, which you forward if the script
wants it), in the workspace root:

```yaml
hooks:
  - run: ~/bin/gummi-notify "$1"       # every event
  - run: page-oncall.sh "$1"
    events: [gate.waiting, budget.exhausted]   # or filter
```

The event vocabulary: `card.created`, `stage.enter`, `card.verified`,
`card.parked`, `card.merged`, `gate.waiting`, `question.waiting`,
`budget.exhausted`, `card.failed`. `card.verified` is the one to watch
for "the branch is ready": it fires at the verify gate, not at a merge.
Hooks are advisory: a slow script is killed after 15 seconds, a failing
one changes nothing, and none of it can stall a run — the drain at exit
is bounded too. What failed or never ran is counted and printed in one
line when the process exits, and `gummi doctor` checks the script paths.
The full contract is in [docs/CONFIGURATION.md](docs/CONFIGURATION.md).

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

## Stacks — slice it, land it in pieces

A **stack** chains cards so each one's branch forks from the one below it.
You slice a feature into several reviewable branches, work them **all at
once**, land them one at a time, and take review feedback on the ones
below — while gummi keeps everything above them rebased.

Press `T` on a card and the next card is created *on top of it*; that is
the whole setup, and it creates the stack. If you are already in the new
card dialog, its `stack` row (under `alt+o`) offers the same thing —
`←/→` cycles the cards you could fork from, most recently touched first.
The board then shows the chain:

```
▾ rule-engine · 3 cards · on main
  1 ⬤ FD-101  token parser     ⛁1/3 ← main      PR#412
  2 ◐ FD-104  evaluate rules   ⛁2/3 ← FD-101
  3 ◐ FD-103  cli surface      ⛁3/3 ← FD-104
```

Their branches are `feat/token-parser`, `feat/evaluate-rules`,
`feat/cli-surface` — the kind of work and the label. Two cards of one kind
in one repo cannot share a label, and gummi says so when you create the
second rather than when it tries to cut the branch:

```
BG-001 already uses the branch bug/flaky-login — retitle this card so it gets a different one
```

All three run at the same time: **a stack orders landing, never work.**
A dependency (`gummi deps add`) is the separate, opt-in fact that holds a
card back until another is done.

When you apply review feedback to FD-101 and its branch moves, the cards
above it are replaying before you could have typed the rebase — there is no
key for it and no order to remember. Only a conflict interrupts, with the
same offer a manual rebase gets: let an agent resolve it in that worktree.
When FD-101 lands, FD-104's base moves to `main` on its own and the stack
shortens by one.

Cards land bottom-first, and gummi refuses out of order: a card's branch
carries the commits of every card below it, so landing it early would land
their work under its message. Pushing a replayed branch is still yours —
gummi prints the `git push --force-with-lease` and never runs it.

A card can also fork from a branch that is not checked out — pick it on the
creation dialog's `forks from` row, or pass `--base release-2.1`.

## Picking up work that already exists

A card does not have to start from an empty branch. Point one at a branch
somebody already started — a colleague's half-finished work, a pull request
sitting under review — and gummi runs the ordinary workflow on top of it:

```sh
gummi run --adopt feat/their-parser --envelope 500 "finish the empty-case handling"
gummi run --pr 412 --envelope 500 "address the review comments"
```

`--pr` does three things in one go: it finds the branch behind the pull
request (fetching it for you, forks included), links the card to the PR, and
pulls the unresolved review threads in as annotations on the card's diff —
so the plan stage reads the reviewers' own words before it designs anything.
In the board, the creation dialog's `works on` row offers the same choice.

The card's first stage opens onto the inherited diff rather than a blank
branch, and its artifact starts with an `Inherited work` section naming the
branch, its commits and how far behind it has fallen. From there it is an
ordinary card: it plans, implements and verifies, and you comment on its
diff with `c` exactly as you would on any other.

What gummi will **not** do to a branch it did not cut:

- **delete it.** `clean` removes the worktree and leaves the branch.
- **rewrite it.** No rebase, no reset, no force-push — it may already be
  pushed and being read. gummi says how far behind the branch is and works
  where it stands; catching it up is your call.
- **blame the card for it.** Whatever was already failing when you adopted
  it is recorded at the plan gate, and verify holds the card only to what
  the rework changed.

Because the branch is yours, the default ending is `h` — hand it back —
rather than a squash onto main. Landing it is still there if you want it.
