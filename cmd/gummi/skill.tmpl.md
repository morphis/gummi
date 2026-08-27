# Using gummi to ship a feature or bug

gummi is a meta-harness that drives coding agents through a **fixed, spec-driven
workflow** — spec → review → verify — with each work item on its own git
worktree and branch. You (the calling agent) invoke `gummi run`, answer the
questions it delegates to you, and report the **verified branch** it produces.
gummi orchestrates the coding work; it never merges and never skips review or
verify.

## When to use gummi (vs. editing directly)

Use gummi for **PR-sized work that deserves a real quality bar** — a feature or
bug fix that warrants a written spec, an independent code review, a verify pass
(build/test/lint + acceptance checks), and an isolated branch a human can review
before it lands.

Edit files directly for **trivia**: a typo, a one-line config tweak, a rename —
anything where a spec-and-review cycle is pure overhead. If the change is big
enough that you'd open a PR for it, it's big enough for gummi.

**Oversized asks:** if the request spans several independently shippable pieces,
don't push it through one run. Decompose it into PR-sized features yourself and
loop `gummi run` once per feature, sequentially — one verified branch each.

## First run: check readiness with `gummi doctor`

Before the first `gummi run` in a repo, run:

```
gummi doctor --json
```

It returns a structured checklist — repo, workspace, backend, profile, auth,
envelope, lock. Repair each item that isn't `ok`:

- **backend** — set gummi's backend to the **same agent that is driving it**:
  give every role a `backend:` in `.gummi/profiles.yaml` (e.g. `backend:
  claude` if you are Claude Code, `backend: codex` if you are Codex); export
  `GUMMI_AGENT` only to set the fallback default for roles that omit it.
  Match the backend to yourself by default — it keeps the whole pipeline on
  one vendor's auth, and it sidesteps the cross-model trap where the claude
  backend forwards each role's model to the Claude CLI as `--model` and the
  CLI rejects any non-`claude-*` id (a mismatched run passes spec, then dies
  at implement).
- **profile** — **do not rely on the auto-seeded default** (its implementer/scribe
  roles are OpenAI ids the claude backend cannot drive). Instead **write the
  `default` profile in `.gummi/profiles.yaml` yourself, and ask the human their
  model preferences first.** Offer a cost-tiered shape — a frontier model for the
  architect/reviewer roles, a cheaper one for implementer/scribe — with every
  role on a model your backend can drive (for the claude backend, every role a
  `claude-*` id, e.g. architect/reviewer `claude-sonnet-5`, implementer
  `claude-sonnet-5`, scribe `claude-haiku-4-5`). If the human has no preference,
  seed those tiered defaults. Tier down rather than pointing every role at the
  top frontier model — you don't need the biggest model on the scribe.
- **auth** — if a check reports auth is needed, gummi hands you the **exact
  command**. Give it to the **human** to run (e.g. surface it with the `!`
  prefix). You never handle secrets: API keys are referenced by environment
  variable **name**, never written as literal values.
- **envelope** — every run needs a credit envelope. Pass `--envelope N` per run,
  or set `GUMMI_ENVELOPE`. A run refuses to start without one.

Re-run `gummi doctor` until it reports `ready: true`, then proceed.

## Command grammar

```
{{.Grammar}}
```

A run drives one feature via the **quick route** by default (spec → implement →
review → verify). `--full` opts into the brainstorm + plan stages for larger
work. The design gates auto-cross under the default `--gate-approval=auto`; pass
`--gate-approval=caller` to approve them yourself.

`status`, `spec`, and `diff` are **read-only** — they take no lock, so you can
inspect a feature while a run is live.

## The decision loop

Each `gummi run` / `gummi resume` streams milestone + decision NDJSON on stdout
and exits with a **typed status**. Branch on the exit code:

{{.ExitTable}}

**Capture that exit code directly — never through a pipe.** `gummi run … | tee
run.log` (or `| head`, `| grep`) reports the *filter's* exit status, not
gummi's, so a `question`/`exhausted`/`error` looks like success. Redirect to a
file and read `$?` on the next line:

```
gummi run --envelope N "<description>" > run.log; ec=$?
```

(or `set -o pipefail` if you must pipe). Equivalently, the exit code is fully
redundant with the stream: the **last NDJSON line's `event`** (`done`,
`question`, `blocked`, `escalation`, `exhausted`, `timeout`, `stopped`,
`error`) is the terminal status — parse that if you'd rather not depend on the
shell.

Every resumable terminal event also carries a **`next`** field: the literal
`gummi resume …` command for that stop, with the right verb already chosen
(free-form values you must supply appear as a `<placeholder>`, e.g. `--answer
"<answer>"`, and `exhausted` pre-fills a doubled `--envelope`). Prefer running
`next` verbatim over assembling the command yourself — it can't pick the wrong
verb. `done` carries no `next` (nothing to resume); `blocked` carries none
because its next step is to resolve the threads, not a fixed command.

The loop you run:

1. `gummi run --envelope N "<description>"` (add `--ref <id>` to correlate with
   your own tracker, `--acceptance <file|->` to seed the verification plan).
2. Read the exit code:
   - **done (0)** — parse the final `done` line for the branch; report it to your
     caller/human and stop. The branch is verified, not merged.
   - **question (2)** — a design decision was delegated to you. Answer from your
     **task context first**; if unsure, read the spec (`gummi spec <id>`) or diff
     (`gummi diff <id>`); use the `recommended` option only as a tiebreaker. Then
     `gummi resume <id> --answer "<text-or-option>"`. A caller gate (under
     `--gate-approval=caller`) is the same shape: `resume <id> --approve` or
     `--request-changes "<note>"`.
   - **blocked (3)** — either open `%%` spec threads or diff comments block a
     gate (resolve them, or `gummi resume <id> --request-changes "<note>"` to
     send it back; if you can't, escalate to the human), or an unmet
     dependency blocks entry to the coding stage — the event's `blocking_deps`
     names the outstanding card(s) and their stage. There's no gate to
     request changes on in that case: wait for the dependency to reach
     `done`, or drop the edge with `gummi deps rm <id> <dep-id>` if it no
     longer applies.
   - **escalation (4) / exhausted (5) / timeout (6)** — stop and report to the
     human. These are durable and resumable: the card stays on the board.
     `exhausted` resumes with `gummi resume <id> --envelope N`, where N is
     larger than the envelope it ran dry on (the `envelope` field on the
     `exhausted` event) — the raise is a floor, so it can never shrink the
     budget. `escalation` and `timeout` resume once the human weighs in.
     `timeout` also carries `stage_timeout_used` (what limit fired) — if the
     backend agent is fine and just needed longer, resume with a bigger
     `--stage-timeout` (e.g. double the value it reports) instead of
     retrying at the same budget.
   - **error (1)** — a setup/agent failure. Check `gummi status <id>`: a
     pre-id setup failure landed nothing (report it), but a mid-run failure
     can leave a durable, non-terminal card that a `gummi resume <id>` may
     finish — the error event's `resumable`/`stage` fields say which.
3. After a `resume`, read the new exit code and repeat until `done`.

### Before you retry: check `preconditions.check_running`

`exhausted` and `timeout` events carry a `preconditions.check_running` shell
one-liner — run it before following `next`. gummi ignores SIGHUP so that a
detached wrapper's death doesn't kill the model turn; the flipside is that
your wrapper dying (e.g. the harness killed it) can leave gummi still
churning as an orphan. A bare retry there fights the exclusive lock and
looks like a fresh failure. `check_running` reads that card's own pid file
under `.gummi/state/locks/` and probes it with `kill -0`: if it prints "gummi still running as pid …",
wait (or `tail -f .gummi/state/events.jsonl` to watch progress) until the
pid is gone, then follow `next`. `gummi status --json` also exposes this
under `"running"` when you'd rather branch on JSON than a shell probe.

### Which `resume` verb — match the flag to why the run stopped

`gummi resume <id>` does different things depending on its flag. Pick the one
that matches the terminal event; the wrong verb wastes a stage or stalls:

- `--answer "<text>"` — answers a delegated **`question`** event (an `ask_user`).
- `--approve` / `--request-changes "<note>"` — decides a caller **`gate`** event
  (a design gate under `--gate-approval=caller`), and the same pair continues a
  `--until` **`stopped`** run.
- `--bounce [--note "<why>"]` — un-parks a verify-fail (or a review cap-hit)
  **`escalation`**: rewinds the feature to its work stage (implement/fix) and
  drives the review → verify tail again. The optional `--note` is an addendum
  to the reborn implement kickoff, alongside any open `%%` diff/spec threads
  the engine already folds in. This is the CLI counterpart of the TUI's `b`
  key; use it when the human has looked at the verify evidence and decided a
  rework is the right call.
- `--envelope N` — clears an **`exhausted`** stop; N must exceed the dry envelope
  (the `envelope` field on the event). It only raises, never lowers.
- **no decision flag** — re-runs the parked stage exactly as it is. This is *only*
  for retrying a stage that stalled (**`timeout`**), escalated (**`escalation`**),
  or ran dry (after you top up with `--envelope`) — once the human has weighed in.
  A bare `resume` is **not** how you cross a gate or answer a question: at a gate
  it only re-presents the same gate, and at a delegated ask it has nothing to
  send. Reach for `--approve` / `--answer` there.

Exactly one decision flag per resume (`--answer`, `--approve`,
`--request-changes`, and `--bounce` are mutually exclusive). `--envelope`
composes with any of them, or stands alone to top up a plain re-run.

For a **small or well-specified** feature whose delegated questions you'd answer
with the `recommended` option anyway, start with `--autonomous`: it auto-takes
the recommended answer instead of stopping at `question`, shipping the feature to
a verified branch hands-free. Reserve the checkpointing loop above for genuinely
ambiguous forks where the recommended answer might be wrong.

## Stop early for a human design review

For work where a human should sign off on the design before implementation burns
tokens, add `--until spec` (or `--until plan` on `--full`). The run stops cleanly
at that stage — event `stopped`, exit 0 — with the feature parked and resumable.
Hand the spec to the human (`gummi spec <id>`); once approved, `gummi resume <id>
--approve` continues to the verified branch.

## Research cards: `gummi research`

`gummi research "<brief>"` mints one RS card from a free-form brief and drives
it headlessly through investigate and shape to the decompose gate — no
brainstorm, no plan, no worktree. `--until shape` is the only pre-decompose
stop on its route. At the gate the stream emits a `question` event whose
payload carries the proposed follow-on FD slices plus a coverage map, and the
run exits `question` (code 2). From there, `gummi resume RS-NNN --approve`
mints each proposed FD and moves the RS card to `done`; `gummi resume RS-NNN
--request-changes "<note>"` re-runs the decompose pass with the note attached
and emits a fresh `question`. `gummi status RS-NNN` and `gummi spec RS-NNN`
work the same as for any other card.

## Worktrees, and re-attaching a proven branch

Each feature is checked out in its **own linked git worktree** under
`.gummi/worktrees/FD-NNN`, on branch `gummi/FD-NNN-slug`. That branch is
*already checked out there*, so a plain `git checkout gummi/FD-NNN-…` in the
main repo fails (`exit 128`, "already checked out"). To build, test, or inspect
a branch outside gummi, `cd` into its worktree — don't check the branch out in
the main checkout.

If a `run`/`resume` was cut off **right at the end of verify** — the branch has
all its commits and passes the acceptance checks, but the card is still
`stage: verify` with `verified: false` (e.g. `status <id>` shows it) — you don't
have to re-run the whole verify stage. `gummi verify <id>` re-runs the spec's
`gummi-checks` on the existing branch and, if they pass, finalizes the card
(stamps `verified`, emits `done`, exit 0) with no fresh agent pass. If the
checks still fail it escalates (exit 4); if a cheap re-attach can't be trusted
(not parked at verify, no checks) it exits 1 and points you back to `resume`.

## Landing, cleanup, and dependencies

`run`/`resume` stop at a verified branch and never merge. Landing that
decision is a separate, explicit verb: `gummi merge <id> -m "<message>"`
(or `-m -` to read the message from stdin) squash-merges the branch into
main — but only once the card is at a verified branch, and only with a
Conventional Commits `type(scope): summary` message with no diff dump or
agent attribution, or it refuses before touching git. Draft that message
from the spec and the branch's own commits, and have the human confirm it
before you run `gummi merge` — don't invent a message and merge
unattended. `gummi clean <id>` removes a landed card's worktree and branch
afterward. Both hold the same per-card lock as `run`/`resume`.

If a card depends on another (`gummi deps add <dependent> <depends-on>`,
`rm`/`list` to remove or read them back), an unmet dependency blocks it
from entering its coding stage — see the `blocked (3)` case above.

## What gummi guarantees

- **Review and verify always run.** There is no flag, profile, or config that
  skips them — the quality floor is invariant.
- **gummi stops at a verified branch. It never merges.** Landing (a PR, a push, a
  squash-merge) stays with the human.
- **It fails loud.** Anything a human would resolve — a design question, a
  rerun/critique cap, an exhausted envelope, a stalled stage — becomes a typed
  non-zero exit and a durable, resumable card, never a silent auto-proceed.

Your job: drive the loop, answer what's delegated to you, and report the verified
branch upward.
