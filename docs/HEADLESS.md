# Driving gummi headlessly

The board is one way in. The other is the non-interactive driver: the same
engine and the same quality floor, with nobody at the keyboard. This page is
the full reference for that path. The README carries the short version, and
`gummi skill show` prints the loop as a calling agent reads it.

Headless verbs change *who approves a gate*, never *whether* review and
verify run. `run`, `research` and `resume` never merge. They stop at a
verified branch, and landing is the separate `merge` verb.

## Starting a card

```sh
gummi run --envelope 500 "Add a --format=json flag to the export command"
gummi run --envelope 500 --gate-approval autopilot "..."
gummi research --envelope 300 "Where does the exporter buffer, and why?"
```

Two things must be true before any work begins, and both fail loud:

- an envelope is named (`--envelope N`, or `GUMMI_ENVELOPE`). The board's
  creation form prefills 2000 credits you can edit; an unattended run has
  no one to read a default, so it must name one.
- an agent backend is configured. `gummi doctor` tells you which.

`--gate-approval` takes one of two modes. `attended` (the default) stops at
the design gate and hands the decision back through `resume`. `autopilot`
crosses its own gates and runs to a verified branch. The mode is saved on
the card; `resume` keeps it unless you pass the flag again. The retired
spellings `off`, `gates`, `caller`, `auto` and `full` are still accepted
and mapped.

Other `run` flags:

| flag | purpose |
|---|---|
| `--until plan` | stop cleanly before implementation, for a human design review. `plan` is the only stop |
| `--autonomous` | take the agent's recommended answer instead of stopping on a question. The card's history marks the answer as unattended |
| `--acceptance <file\|->` | seed the spec's verification plan |
| `--ref <id>` | your own tracker id, echoed in the stream. `status` and `resume` accept it in place of the card id |
| `--repo <name>` | which managed repository to create the card in. Required when `repos:` is configured |
| `--profile <name>` | the profile mapping roles to models |
| `--stage-timeout <dur>` | per-stage inactivity timeout, 0 disables |
| `--verbose` | add per-tool-call lines to the stream |

## Verbs

| command | purpose |
|---|---|
| `gummi run [flags] "<description>"` | create and drive one feature to a verified branch |
| `gummi research [flags] "<question>"` | create and drive one research card |
| `gummi goal [flags] "<objective>"` | agree a goal, then let it run its cards on one branch until it is ready for you |
| `gummi resume <id\|ref> [decision]` | apply a decision and drive on |
| `gummi resume <id\|ref> --say "<line>"` | read a line the way the card page would and report what it would do, as a `say` event, without acting |
| `gummi status <id\|ref> [--json]` | stage, blockers, spend, branch state |
| `gummi status <id\|ref> --stats [--json]` | how the card ran: each pass and what it cost, the share that was rework, the share of its life spent waiting on a person |
| `gummi watch <id\|ref> [--json] [--wait] [--once]` | follow the live agent stream of a card another gummi is driving |
| `gummi spec <id\|ref>` | the current spec or report markdown |
| `gummi diff <id\|ref>` | the worktree diff against main |
| `gummi verify <id\|ref>` | re-run the checks on a verified branch and finalize its card |
| `gummi merge <id\|ref> -m <message\|->` | land a verified branch as one squash commit (a goal: one merge commit, `-m` optional) |
| `gummi squash <id\|ref> -m <message\|->` | collapse a card's branch to one commit in place |
| `gummi commit <id\|ref> -m <message\|->` | commit a card's own uncommitted worktree changes onto its branch |
| `gummi handoff <id\|ref>` | close a verified card and keep its branch — nothing lands (a goal not yet ready is abandoned) |
| `gummi clean <id\|ref>` | remove a landed card's worktree and branch (a goal: its cards' come out with it; an adopted card keeps its branch) |
| `gummi pr link\|unlink\|status\|comments <id> [flags]` | link a card to a PR you opened, or read its status and review comments |
| `gummi deps add\|rm <dependent> <depends-on>`, `gummi deps list <id>` | dependency edges between cards |
| `gummi ingest [flags] <spec-file>` | decompose a spec into feature proposals and materialize them |
| `gummi bugs ingest [flags]`, `gummi bugs new [flags]` | import bugs from GitHub issues, or add one by hand |
| `gummi init` | create and seed the `.gummi` workspace without opening the board |
| `gummi doctor [--json] [--deep]` | readiness: repo, backend, auth, profile, envelope, lock, per-role reach |
| `gummi skill show\|install\|list` | the calling-agent skill |

`status`, `watch`, `spec` and `diff` take no lock, so you can inspect a card
while a run is live. Everything that mutates the workspace (`run`,
`resume`, `merge`, `squash`, `commit`, `clean`) holds an exclusive `.gummi`
lock, so a headless run and the board never touch the same workspace at
once.

## Watching a run

`run`, `research`, `diagnose` and `resume` stream NDJSON milestones on
stdout. `--verbose` adds one `activity` line per tool call. The line worth
knowing is `stage`, whose `result` names what the card is doing:

```
{"event":"stage","stage":"plan"}                        the stage's own session
{"event":"stage","stage":"plan","result":"critiquing"}  the adversarial read
{"event":"stage","stage":"plan","result":"changes"}     it found something blocking
{"event":"stage","stage":"plan","round":1,"result":"replanning"}
{"event":"stage","stage":"plan","result":"pass"}
{"event":"stage","stage":"plan","result":"discovering checks"}
{"event":"stage","stage":"plan","result":"baselining checks"}
{"event":"gate","from":"plan","to":"implement",...}
{"event":"stage","stage":"verify","result":"drafting the landing message"}
```

The last two are the one-shot passes that run at the approval gate — the
scribe survey that writes the repo's commands into the spec's
`gummi-checks` block, and the run of those commands that records which
were already failing. They are model-and-shell work that can take minutes
on a large repository, which is why they are on the stream: between the
plan's verdict and the gate crossing, they are the only thing happening.
They carry the stage the card is crossing **from**, because that is the
gate they belong to — the card's own stage field has already advanced by
the time they start.

The last line is the same kind of pass at the other end of the drive.
When a run stops on a verified branch it composes the card's squash-merge
landing message and stores it, so the person who comes back to land the
card opens a merge dialog that already holds one instead of waiting
~60s for it — the draft is stamped with the branch tip it describes, and
a branch that moves afterwards is drafted live at the landing as before.
It is skipped, silently, for a card whose message is written by something
else: a goal's own, a goal's card (its goal lands it), a card linked to a
PR, a research card, and one already handed off or dropped. The run stops
at its verified branch either way — a failed draft costs the landing
nothing but the wait it was meant to save.

The survey is remembered per repository, not per card: a second card in
the same repo reuses the first card's answer and skips the session
entirely, until one of the files that decides the answer changes (the
Makefile or task file, the CI workflows, the dependency manifest, the
lint config, or the repo's AGENTS.md/CLAUDE.md). Delete
`.gummi/checks-cache.json` to force a fresh survey.

A finished card's receipt carries three numbers worth reading together:

```
{"event":"verified","spent_credits":661.4,
 "review_rounds":3,            the critiques THIS invocation ran
 "corrective_rounds":1,        the CARD's cumulative rework, across processes
 "unproven_files":["lxd/images.go"]}
```

`unproven_files` is verify's own declaration of the files this branch
changed that no check that ran exercised — a tree the local toolchain
cannot build, a suite only CI runs. It does not block the card; it is how
a caller tells a branch every check covered from one where a whole
subtree was never compiled. `gummi status --json` carries the same list
(with each file's reason) beside `excused_checks`, and both qualify
`verified` in the same way.

To measure where a run's spend went rather than watch it, `gummi status
<id> --json` carries `stage_spend`: one row per (stage, role) with the
model, the credits, and input/cached/output tokens. It is the supported
way to answer "what did this card cost, and where" — the `spend` field
above it is only the total.

## Exit statuses

Every `run`, `research` and `resume` ends on a typed exit the caller
branches on:

| exit | status | caller action |
|---|---|---|
| `0` | `verified` | verified branch ready. Report it and stop |
| `0` | `stopped` | `--until` reached its stop. `resume --approve` to continue |
| `0` | `said` | `--say` reported a reading and acted on nothing |
| `0` | `noted` | a goal decision (`--goal-note`, `--wrap-up`, `--reverse`) was handed to a goal another process is driving; it acts on it, this call drove nothing |
| `2` | `question` | a delegated question or a design gate. `resume --answer`, `--approve` or `--request-changes` |
| `3` | `blocked` | open `%%` or diff threads block a gate (resolve them, or `resume --request-changes`), an unmet dependency blocks the coding stage (`blocking_deps` on the event: wait for it to land, or `gummi deps rm`), or the plan's own promises are unmet at the verify→done gate (`reason` names them: an invariant verify never answered or answered fail, or a golden whose quoted input appears nowhere on the branch) |
| `4` | `escalation` | a rerun or critique cap, or an unclear verdict. Report to a human; resumable |
| `5` | `exhausted` | envelope dry. `resume --envelope N` with a higher number |
| `6` | `timeout` | a stage went quiet. Report; resumable |
| `1` | `error` | setup or agent failure. Nothing partial landed |
| `1` | `stalled` | a goal stopped on something it can only wait for: its agent backend could not serve it (a quota, a rate limit, an overload), or a verify — a card's, or the goal's own — said this machine cannot run its verification plan. Nothing was dropped and nothing was judged — the event's `reason` says which, and the same `resume` continues it |

`gummi doctor` has its own exit status, outside this table: **7** while any
check is failing, 0 once the workspace is ready. It applies to `--json` too,
so a setup step can branch on the exit code instead of parsing `.ready`. The
code sits above the drive statuses on purpose — a workspace that is not ready
is not a run that asked a question.

## Resuming

`gummi resume` carries one decision flag at a time:

- `--answer "<text>"` resolves a delegated `ask_user` question. The answer
  rides the same round trip the board's picker rides, and the card's
  history records who answered and which option was chosen. When more
  than one decision is open, the newest one is resolved.
- `--approve` / `--request-changes "<note>"` decide a design gate. Passing
  `--answer` at a gate, or `--approve` at a question, is refused with a
  usage error naming the verb this stop actually takes.
- `--bounce [--note "<why>"]` rewinds one rerun edge: a verify failure back
  to implement, an implement-stage card back to plan. It is the board's
  `b` key.
- `--envelope N` raises the envelope before resuming. It never lowers it.
- `--say "<line>"` reads a line the way the card page reads typed text and
  reports the routing as a `say` event without acting. Use it to see what
  a sentence would do before you commit to a verb.

A goal takes three more, one at a time:

- `--goal-note "<text>"` hands a running goal's lead a note. It lands in the
  goal doc's Notes and the lead reads it on its next turn; the goal stays
  silent.
- `--reverse D-N` reverses a decision for review and sends the goal back to
  its cards. Add `--request-changes "<why>"` to say why.
- `--wrap-up` tells a running goal to finish now: nothing new starts,
  verified cards land, the rest is dropped, and it comes back partial.

All three are for a goal that is running, which is a goal another process
is driving: each writes one row the conductor reads on its next tick and
drives nothing itself, so it is delivered while that process keeps the
card lock and the call exits `noted` (0). Run against a goal nobody is
driving, the same command delivers the decision and then drives the goal
on, as any resume does.

On a goal that is ready for you, `--request-changes "<notes>"` sends it back
to its cards with the notes, and `--envelope N` raises its budget — the one
move of its ceiling, and only a person makes it.

## Landing

A run stops at a verified branch on purpose. The landing commit is a
review decision, so the headless way to make it is explicit:

```sh
gummi merge FD-042 -m "feat(export): add a --format=json flag"
gummi merge FD-042 -m - <<'MSG'     # or read the message from stdin
feat(export): add a --format=json flag

The flag writes NDJSON to stdout instead of the table layout.
MSG
```

`merge` requires the card to be at a verified branch, takes no other
input, and is stricter than the board's dialog: the message must be a
Conventional Commits `type(scope): summary` with no diff dump and no agent
attribution, or the command refuses before touching git. On success it
emits a `merged` event with the landed sha and moves the card to `done`.

### Ending a card without landing it

Landing is one of three endings, not the only one. When the branch is
yours to take — you will push it, open the PR by hand, cherry-pick two
commits out of it, or simply keep it — `handoff` closes the card and
leaves the branch exactly where it is:

```sh
gummi handoff FD-042
{"event":"handed off","id":"FD-042","branch":"feat/json-export"}
```

It commits a final checkpoint first (the branch is the deliverable, so
loose work must not be left behind), stamps `handed_off`, and crosses the
same verify→done gate with the landing waived and **every other floor
intact**: open `%%` threads, open diff annotations, the omission gate and
the document floor all still refuse it. Landing it after all stays
available for as long as the branch exists (`gummi merge` accepts a
handed-off card and retracts the stamp).

Before this verb the only way to say "I'll take it from here" was to
delete the card, which destroys the branch.

`clean <id>` is the board's `c` key: it removes a landed card's worktree
and branch and keeps the card as a done entry. It refuses anything that
has not actually landed — including a handed-off card, where cleaning up
would delete the branch that was kept on purpose — or that carries
tracked-dirty rework.

`status --json` carries these terminal signals separately, and a poller
must not conflate them:

| field | means |
|---|---|
| `verified: true` | the verify gate passed and the branch is ready to land — where a headless run stops, and what a CI caller polls for |
| `stage: "done"` | the card is **closed**. It does not mean anything merged |
| `ending` | how it closed: `landed`, `handed_off` or `dropped`; absent while the card is open |
| `branch_state: "landed"` | the branch is on the trunk — the field to read for "did this merge" |

After a headless run expect `verified:true` with no `ending` until you
merge or hand off.

The TUI names the same three words on the board badge and in a finished
card's closing block, and `adopt` (TUI only) takes a `dropped` card back
onto the open board at the stage the drop closed it from.

`ending` replaced the `done` and `handed_off` booleans, which took two
fields to name one fact and could not name a drop at all: a card its goal
dropped was closed by borrowing the hand-off stamp, so it reported
`handed_off: true` with no branch and nothing spent.

`rounds` on the same payload is a **live counter, not a history**. Each
loop resets its own count when it completes, so a finished card that took
three plan rounds still reads `{"plan": 0, "review": 0, "corrective": 0}`.
The tally of critique passes is `review_rounds` on the terminal `verified`
event. A poller reading `rounds` as "how much rework did this take" will
read zero every time.

## Goals

A goal is a card whose work is other cards. You describe an outcome and a
budget, agree what "done" means, and gummi runs the cards that get there on
one shared branch and comes back when the result is ready for you.

```sh
gummi goal --envelope 4000 "Export works offline"
gummi goal --envelope 4000 --plan-file goal.md "Export works offline"
```

**Plan.** The goal's plan is a conversation with the architect, and it
stops at its gate like any card's (`question`, exit 2; `--gate-approval
autopilot --autonomous` lets the architect settle it unattended). The goal
doc it writes carries the objective, a **done-when** list — checkable
statements, each with a `check:` command or `judge: true` — the limits, a
rough cost per item, the lanes, and the cards, each serving at least one
done-when item. A row with an existing card's `id:` hands that card to the
goal. The gate refuses a done-when item nothing can check, an item no card
serves, and a card list the budget cannot fund.

**Repositories.** `gummi goal` takes no `--repo`: a goal is not in a
repository. Each card row names its own `repo:` (omit it where the
workspace has a default), an attached row brings its card's, and a
done-when item whose `check:` must run somewhere else names that
repository too. The gate refuses a name the workspace does not manage.
The goal's own home — where its card, its lead and its review are — is
settled from its cards when the plan is approved, and it grows a branch of
the same name in every other repository its cards are in.

**Run.** Approving the plan starts the goal, and from there it runs itself.
It mints its cards and runs each on autopilot on the goal branch
`goal/<slug>` **of the card's own repository**, up to its lanes. When
a card verifies it lands on that goal branch as one commit — after the goal branch catches up with main, and
after the card is rebased and re-checked if the goal branch moved under
it. The goal's **lead** (the `lead` role, or the architect's model) answers
the cards' questions, reads their plans before they implement, re-plans
stuck and exhausted cards, records **decisions for review** for every call
a user of the result would notice, declines reviewer findings with a
reason, and files what it finds outside the goal as open-board cards. The
stream carries a `goal` event for each step and a `card_verified` event for
each card; a card's `card_verified` is not the goal's own `verified`.

**Budget.** The envelope is the goal's whole budget and a hard ceiling. Each
card starts on the lesser of its planned estimate and an even share, and what
is left over is not handed out until a card proves it needs it: a card that
runs out is raised from what is available. A landed or dropped card returns
what it did not spend; the lead's turns count; nothing is ever raised past
the ceiling. A reserve is held back for the goal's own review and verify.

When there is nothing left to give, the goal stops rather than choosing work
to abandon: it exits `exhausted` (5) naming the card that is waiting and
roughly what it needs, with the hand-over on the event's `goal` object and
the waiting card's done-when items marked `waiting on budget` — unfinished
and resumable, not given up on. Nothing is dropped and the card keeps its
branch and its spend, so raising the envelope continues it:

```sh
gummi resume GL-004 --envelope 6000     # more budget, and it carries on
```

A goal proved by an experiment has a second ceiling — substrate runs and
minutes — and hitting it ends the same way: `exhausted`, nothing dropped, the
hand-over's `needs_substrate` saying which run it cannot afford.

```sh
gummi resume GL-004 --runs 40 --minutes 1800   # more substrate, and it makes the run
```

One stop is a question rather than a ceiling. When the goal's lead finds that
an agreed done-when item cannot hold as written — the thing it assumes is not
so — it may not change the item and must not quietly fail it: it asks. The
cards that serve only that item freeze, everything else runs on, and when
nothing else can move the run exits `question` with an `owner_question` event
carrying the item, what was found and the amendment the lead would propose.

```sh
gummi resume GL-004 --goal-note "accepted — DW-3 amended in the doc"
```

A wrap-up you ask for (`--wrap-up`) still drops what is unfinished: that is
what finishing now means.

A backend that cannot serve the goal at all — a provider quota, a rate
limit, an overload — is neither a budget question nor the work's fault, so
it is not a wrap-up either. The run exits `stalled` with the backend's own
sentence (which usually says when it comes back) on a `stalled` event, every
card keeps its branch, its spend and its place, and `gummi resume GL-NNN`
carries on. A goal left running in the board picks itself back up on its own,
asking the backend again a couple of minutes after each refusal.

A verify that says `VERDICT: blocked` — *this machine cannot run the
verification plan* — is the same shape, and it is kept apart from a failed
verify all the way up. A goal card that stops on one is **blocked**, not
stuck: its lead is shown it once, it is never dropped for it, and its
done-when items read `waiting on an environment` rather than `not met`. When
nothing else can move the run exits `stalled` naming the card and what it
lacked. A goal's own verify that blocks stops at verify the same way — it is
not sent back to its cards and spends no rework round, because nothing was
judged. Retrying is a verify session and costs what one costs, so a goal never
retries on a timer: `gummi resume GL-NNN` (or a note to the goal in the board)
is what says someone has been there, and the blocked verify runs again.

This is only the verifier's own word. A pass that gummi floored to `blocked`
— a check failed, a promise is not on the branch — is a statement about the
work and is handled as before.

**Hand-over.** When its cards have settled, the goal reviews and verifies
the combined branch. The run exits `verified` with the hand-over on the event's
`goal` object — done-when items met, partial or whole, cards landed and
dropped, decisions for review, and the full report — and `status --json`
carries the same report under `goal`. From there:

A goal across repositories lands once in each — git has no merge that
spans them — so `merge` walks them home repository first and the
hand-over's `repos` says which have the goal and which do not. A merge
that stops half-way leaves the rest to a second `gummi merge GL-NNN`,
which skips what already landed.

```sh
gummi merge GL-004                                   # one merge commit on main over its cards' commits, per repository
gummi resume GL-004 --request-changes "<notes>"     # back to its cards, with the notes
gummi resume GL-004 --reverse D-2                    # take the other way on a decision
gummi handoff GL-004                                 # close it, keep the branch
```

## Landing through a PR

Some repos land through a PR on GitHub instead of `gummi merge`. gummi
still never writes to GitHub on that route. It only names and reads the
PR you already opened. The loop is four commands:

```sh
gummi pr link FD-042 --auto                          # or a URL / number instead of --auto
gummi pr comments FD-042 --ingest                    # unresolved review threads become diff annotations
gummi resume FD-042 --bounce --note "address review" # rewinds to fix the annotated lines
git push                                             # push the fix onto the open PR
```

What you do before the first push depends on the repo's merge setting:

| merge method | before you push |
|---|---|
| squash merge | nothing; GitHub collapses the branch to one commit |
| merge commit / rebase merge | `gummi squash <id> -m <message\|->` first, so the branch lands as one commit either way. Later fix rounds may keep their own commits or `squash` again, as long as no review thread is open |

`squash` refuses while the worktree has uncommitted changes, because
folding them silently into the collapsed commit would hide what changed.
Commit them first, then squash:

```sh
gummi commit FD-042 -m "fix(export): tighten the empty-array case"
gummi squash FD-042 -m "feat(export): add a --format=json flag"
```

`commit` commits exactly the card's own uncommitted worktree changes onto
its own branch with your message. It touches no PR, remote or main
checkout, moves no stage, and has no precondition. A clean worktree is a
no-op, reported as such.

## Adopting a branch or a PR

A card can be minted **onto** a branch gummi did not cut, and rework it in
place (DESIGN §10 D22):

```sh
gummi run --adopt feat/their-parser --envelope 500 "finish the empty-case handling"
gummi run --pr 412 --envelope 500 "address the review comments"
gummi bugs new --title "Parser drops empty input" --adopt fix/parser --envelope 300
```

`--adopt <branch>` takes a local branch as it stands. `--pr <url|number>`
resolves the pull request, fetches its head branch locally (a fork's
included — the local copy is named `pr-<N>-<branch>` and the result is a
branch you own, since gummi cannot write to somebody else's fork), links
the card to the PR and ingests its unresolved review threads as diff
annotations before the first stage runs. Both refuse before a card is
minted if the branch is missing, is checked out elsewhere, carries no
commits of its own, shares no history with the base, or already belongs to
another card — one branch, one card.

The card then runs the ordinary graph. There is no fast lane: `plan` reads
the inherited diff and designs the rework against it, which is what keeps
the quality floor true for code gummi did not write.

Four rules apply to an adopted branch for as long as the card exists:

| rule | what it means |
|---|---|
| never deleted | `clean` removes the worktree and keeps the branch; the `cleaned` event carries `"branch_kept": true` |
| never rewritten | no rebase, reset or force-push; the card reports how far behind its base it is and works where it stands |
| not blamed for what it inherited | the approval baseline records what was already failing, and verify holds the card only to what the rework broke |
| handed back by default | `gummi handoff <id>` is the expected ending; `gummi merge` still works and is a deliberate act |

## Dependencies

`gummi deps add <dependent> <depends-on>` records that one card needs
another (`rm` and `list` remove and read the edges). A dependency counts
as met only when the target reaches `done`, meaning verified *and*
landed. Anything short of that blocks the dependent card's entry into
implement with a `blocked` exit naming the outstanding cards. The board
shows the same edges through the `p` key's dependency picker, and
`gummi ingest` seeds edges when a decomposed spec says one proposal
depends on another.

## The calling-agent skill

gummi generates its own skill, a `SKILL.md` documenting this loop, and
installs it where Claude Code, GitHub Copilot CLI, Codex and opencode
read it:

```sh
gummi skill install          # project scope: .claude/skills + .agents/skills
gummi doctor                 # then check backend, auth and envelope are ready
```

A project-scope install writes `.claude/skills/gummi/SKILL.md` for Claude,
Copilot and opencode, plus `.agents/skills/gummi/SKILL.md` for Codex.
`--scope user` writes to each detected agent's home instead, `--agent`
targets one, `--dry-run` prints what would be written, and `--check`
fails if any target is absent or drifted. The command grammar and exit
table are generated from the binary's real flags, so they cannot drift.
The frontmatter is version-stamped, so `install` and `list` detect a
stale or edited file and refuse to overwrite it without `--force`.

`gummi doctor` is the readiness check the skill's first-run setup runs
(`--json` for a machine-readable checklist, `--deep` to probe each role's
backend). It reports and never repairs: a backend needing login is
surfaced as the exact command for a human to run. Provider config lives in
each backend's native store, never in `profiles.yaml`.
